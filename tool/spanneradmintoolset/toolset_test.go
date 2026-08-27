// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package spanneradmintoolset

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"google.golang.org/adk/v2/agent"
	icontext "google.golang.org/adk/v2/internal/context"
	"google.golang.org/adk/v2/internal/toolinternal"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

// allToolNames is the fixed, model-visible membership of the toolset.
var allToolNames = []string{
	"spanner_list_instances",
	"spanner_get_instance",
	"spanner_list_instance_configs",
	"spanner_get_instance_config",
	"spanner_create_instance",
	"spanner_list_databases",
	"spanner_create_database",
}

// errServer is what the fake Spanner Admin services report when a test asks
// them to fail.
var errServer = status.Error(codes.FailedPrecondition, "server refused")

// requestRecorder stores the last request a fake service received. The gRPC
// server handles the call on its own goroutine, so access is guarded.
type requestRecorder struct {
	mu   sync.Mutex
	last proto.Message
}

func (r *requestRecorder) record(req proto.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = req
}

func (r *requestRecorder) lastRequest() proto.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// fakeInstanceAdminServer serves the instance half of the Spanner Admin API.
type fakeInstanceAdminServer struct {
	instancepb.UnimplementedInstanceAdminServer
	requestRecorder

	fail      bool
	instances []*instancepb.Instance
	instance  *instancepb.Instance
	configs   []*instancepb.InstanceConfig
	config    *instancepb.InstanceConfig
	createOp  *longrunningpb.Operation
}

func (s *fakeInstanceAdminServer) ListInstances(_ context.Context, req *instancepb.ListInstancesRequest) (*instancepb.ListInstancesResponse, error) {
	s.record(req)
	if s.fail {
		return nil, errServer
	}
	return &instancepb.ListInstancesResponse{Instances: s.instances}, nil
}

func (s *fakeInstanceAdminServer) GetInstance(_ context.Context, req *instancepb.GetInstanceRequest) (*instancepb.Instance, error) {
	s.record(req)
	if s.fail {
		return nil, errServer
	}
	return s.instance, nil
}

func (s *fakeInstanceAdminServer) ListInstanceConfigs(_ context.Context, req *instancepb.ListInstanceConfigsRequest) (*instancepb.ListInstanceConfigsResponse, error) {
	s.record(req)
	if s.fail {
		return nil, errServer
	}
	return &instancepb.ListInstanceConfigsResponse{InstanceConfigs: s.configs}, nil
}

func (s *fakeInstanceAdminServer) GetInstanceConfig(_ context.Context, req *instancepb.GetInstanceConfigRequest) (*instancepb.InstanceConfig, error) {
	s.record(req)
	if s.fail {
		return nil, errServer
	}
	return s.config, nil
}

func (s *fakeInstanceAdminServer) CreateInstance(_ context.Context, req *instancepb.CreateInstanceRequest) (*longrunningpb.Operation, error) {
	s.record(req)
	if s.fail {
		return nil, errServer
	}
	return s.createOp, nil
}

// fakeDatabaseAdminServer serves the database half of the Spanner Admin API.
type fakeDatabaseAdminServer struct {
	databasepb.UnimplementedDatabaseAdminServer
	requestRecorder

	fail      bool
	databases []*databasepb.Database
	createOp  *longrunningpb.Operation
}

func (s *fakeDatabaseAdminServer) ListDatabases(_ context.Context, req *databasepb.ListDatabasesRequest) (*databasepb.ListDatabasesResponse, error) {
	s.record(req)
	if s.fail {
		return nil, errServer
	}
	return &databasepb.ListDatabasesResponse{Databases: s.databases}, nil
}

func (s *fakeDatabaseAdminServer) CreateDatabase(_ context.Context, req *databasepb.CreateDatabaseRequest) (*longrunningpb.Operation, error) {
	s.record(req)
	if s.fail {
		return nil, errServer
	}
	return s.createOp, nil
}

// doneOperation builds a long-running operation that has already completed.
func doneOperation(t *testing.T, name string, response proto.Message) *longrunningpb.Operation {
	t.Helper()
	packed, err := anypb.New(response)
	if err != nil {
		t.Fatalf("anypb.New(%v) failed: %v", response, err)
	}
	return &longrunningpb.Operation{
		Name:   name,
		Done:   true,
		Result: &longrunningpb.Operation_Response{Response: packed},
	}
}

// serveAdminAPI starts both fake admin services on an in-process listener and
// returns the client options that reach them.
func serveAdminAPI(t *testing.T, instances *fakeInstanceAdminServer, databases *fakeDatabaseAdminServer) []option.ClientOption {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	instancepb.RegisterInstanceAdminServer(server, instances)
	databasepb.RegisterDatabaseAdminServer(server, databases)
	served := make(chan error, 1)
	go func() {
		served <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		// Stop also closes the listener.
		server.Stop()
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("server.Serve() failed: %v", err)
		}
	})

	// Each admin client dials its own connection over the same in-process
	// listener, so that closing one client does not disturb the other.
	return []option.ClientOption{
		option.WithEndpoint("passthrough:///bufnet"),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		})),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
	}
}

// newTestToolset builds a real toolset whose GAPIC clients talk to the fake
// admin services.
func newTestToolset(t *testing.T, instances *fakeInstanceAdminServer, databases *fakeDatabaseAdminServer) *Toolset {
	t.Helper()
	ts, err := New(t.Context(), Config{ClientOptions: serveAdminAPI(t, instances, databases)})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	t.Cleanup(func() {
		if err := ts.Close(); err != nil {
			t.Errorf("Close() failed: %v", err)
		}
	})
	return ts
}

// runTool invokes the named tool of the toolset with the given raw arguments.
func runTool(t *testing.T, ts *Toolset, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	tools, err := ts.Tools(nil)
	if err != nil {
		t.Fatalf("Tools() failed: %v", err)
	}
	for _, candidate := range tools {
		if candidate.Name() != name {
			continue
		}
		runnable, ok := candidate.(toolinternal.FunctionTool)
		if !ok {
			t.Fatalf("tool %q is %T, want toolinternal.FunctionTool", name, candidate)
		}
		return runnable.Run(newToolContext(t), args)
	}
	t.Fatalf("tool %q not found in toolset", name)
	return nil, nil
}

func newToolContext(t *testing.T) agent.Context {
	t.Helper()
	invocation := icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{})
	return agent.NewToolContext(invocation, "", &session.EventActions{}, nil)
}

// asJSON renders a tool result so a test can pin the exact field names and
// the difference between [] and null.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%v) failed: %v", v, err)
	}
	return string(encoded)
}

func toolNames(t *testing.T, ts *Toolset) []string {
	t.Helper()
	tools, err := ts.Tools(nil)
	if err != nil {
		t.Fatalf("Tools() failed: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name())
	}
	return names
}

// fakeCloser records that Close ran and reports a canned error.
type fakeCloser struct {
	err    error
	closed bool
}

func (c *fakeCloser) Close() error {
	c.closed = true
	return c.err
}

func TestToolsetName(t *testing.T) {
	tests := []struct {
		name     string
		override string
		want     string
	}{
		{name: "an empty name falls back to the default", override: "", want: "SpannerAdminToolset"},
		{name: "a configured name wins", override: "my-spanner-tools", want: "my-spanner-tools"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newToolset(Config{Name: tc.override}, nil)
			if got := ts.Name(); got != tc.want {
				t.Errorf("Name() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToolsetTools(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdminServer{}, &fakeDatabaseAdminServer{})

	if got := ts.Name(); got != defaultName {
		t.Errorf("Name() = %q, want %q", got, defaultName)
	}
	if diff := cmp.Diff(allToolNames, toolNames(t, ts)); diff != "" {
		t.Errorf("tool names mismatch (-want +got):\n%s", diff)
	}
}

func TestToolsetToolsHaveDescriptions(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdminServer{}, &fakeDatabaseAdminServer{})
	tools, err := ts.Tools(nil)
	if err != nil {
		t.Fatalf("Tools() failed: %v", err)
	}

	for _, tl := range tools {
		if tl.Description() == "" {
			t.Errorf("tool %q has an empty description", tl.Name())
		}
	}
}

func TestToolsetClose(t *testing.T) {
	firstErr := errors.New("first closer failed")
	secondErr := errors.New("second closer failed")
	tests := []struct {
		name     string
		closers  []*fakeCloser
		wantErrs []error
	}{
		{
			name:    "no closers report no error",
			closers: nil,
		},
		{
			name:    "healthy closers report no error",
			closers: []*fakeCloser{{}, {}},
		},
		{
			name:     "both failures are joined",
			closers:  []*fakeCloser{{err: firstErr}, {err: secondErr}},
			wantErrs: []error{firstErr, secondErr},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			closers := make([]io.Closer, 0, len(tc.closers))
			for _, c := range tc.closers {
				closers = append(closers, c)
			}
			ts := newToolset(Config{}, []tool.Tool{}, closers...)

			gotErr := ts.Close()

			for _, want := range tc.wantErrs {
				if !errors.Is(gotErr, want) {
					t.Errorf("errors.Is(Close(), %v) = false for %v, want true", want, gotErr)
				}
			}
			if len(tc.wantErrs) == 0 && gotErr != nil {
				t.Errorf("Close() = %v, want nil", gotErr)
			}
			for i, c := range tc.closers {
				if !c.closed {
					t.Errorf("closer %d was not closed", i)
				}
			}
		})
	}
}

func TestNewRejectsUnusableCredentials(t *testing.T) {
	_, err := New(t.Context(), Config{
		ClientOptions: []option.ClientOption{option.WithCredentialsFile("testdata/does-not-exist.json")},
	})

	if err == nil {
		t.Fatal("New() succeeded, want an error for a missing credentials file")
	}
}
