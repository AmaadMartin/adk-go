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
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/anypb"
)

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

// serveAdminAPI starts both fake admin services on an in-process connection
// and returns the client options that reach them.
func serveAdminAPI(t *testing.T, instances *fakeInstanceAdminServer, databases *fakeDatabaseAdminServer) []option.ClientOption {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	instancepb.RegisterInstanceAdminServer(server, instances)
	databasepb.RegisterDatabaseAdminServer(server, databases)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
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

// newGRPCToolset builds a real toolset whose GAPIC clients talk to the fake
// admin services.
func newGRPCToolset(t *testing.T, instances *fakeInstanceAdminServer, databases *fakeDatabaseAdminServer) *Toolset {
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

// TestToolsetOverGRPC drives every tool through the real GAPIC clients and a
// real gRPC connection, and checks the request each one sends.
func TestToolsetOverGRPC(t *testing.T) {
	instances := &fakeInstanceAdminServer{
		instances: []*instancepb.Instance{
			{Name: "projects/test-project/instances/test-instance-1"},
			{Name: "projects/test-project/instances/test-instance-2"},
		},
		instance: &instancepb.Instance{
			Name:            "projects/test-project/instances/test-instance",
			DisplayName:     "Test Instance",
			Config:          "projects/test-project/instanceConfigs/regional-us-central1",
			NodeCount:       1,
			ProcessingUnits: 1000,
			Labels:          map[string]string{"env": "test"},
		},
		configs: []*instancepb.InstanceConfig{
			{Name: "projects/test-project/instanceConfigs/regional-us-central1"},
			{Name: "projects/test-project/instanceConfigs/nam3"},
		},
		config: &instancepb.InstanceConfig{
			Name:        "projects/test-project/instanceConfigs/regional-us-central1",
			DisplayName: "us-central1",
			Replicas: []*instancepb.ReplicaInfo{{
				Location:              "us-central1",
				Type:                  instancepb.ReplicaInfo_READ_WRITE,
				DefaultLeaderLocation: true,
			}},
		},
		createOp: doneOperation(t, "create-instance", &instancepb.Instance{
			Name: "projects/test-project/instances/test-instance",
		}),
	}
	databases := &fakeDatabaseAdminServer{
		databases: []*databasepb.Database{
			{Name: "projects/test-project/instances/test-instance/databases/db-1"},
		},
		createOp: doneOperation(t, "create-database", &databasepb.Database{
			Name: "projects/test-project/instances/test-instance/databases/db-1",
		}),
	}
	ts := newGRPCToolset(t, instances, databases)

	tests := []struct {
		name        string
		tool        string
		args        map[string]any
		want        string
		recorder    *requestRecorder
		wantRequest proto.Message
	}{
		{
			tool:     "spanner_list_instances",
			args:     map[string]any{"project_id": "test-project"},
			want:     `{"instances":["test-instance-1","test-instance-2"]}`,
			recorder: &instances.requestRecorder,
			wantRequest: &instancepb.ListInstancesRequest{
				Parent: "projects/test-project",
			},
		},
		{
			tool: "spanner_get_instance",
			args: map[string]any{"project_id": "test-project", "instance_id": "test-instance"},
			want: `{"config":"projects/test-project/instanceConfigs/regional-us-central1",` +
				`"display_name":"Test Instance","instance_id":"test-instance",` +
				`"labels":{"env":"test"},"node_count":1,"processing_units":1000}`,
			recorder: &instances.requestRecorder,
			wantRequest: &instancepb.GetInstanceRequest{
				Name: "projects/test-project/instances/test-instance",
			},
		},
		{
			tool:     "spanner_list_instance_configs",
			args:     map[string]any{"project_id": "test-project"},
			want:     `{"configs":["regional-us-central1","nam3"]}`,
			recorder: &instances.requestRecorder,
			wantRequest: &instancepb.ListInstanceConfigsRequest{
				Parent: "projects/test-project",
			},
		},
		{
			tool: "spanner_get_instance_config",
			args: map[string]any{"project_id": "test-project", "config_id": "regional-us-central1"},
			want: `{"display_name":"us-central1","labels":{},` +
				`"name":"projects/test-project/instanceConfigs/regional-us-central1",` +
				`"replicas":[{"default_leader_location":true,"location":"us-central1","type":"READ_WRITE"}]}`,
			recorder: &instances.requestRecorder,
			wantRequest: &instancepb.GetInstanceConfigRequest{
				Name: "projects/test-project/instanceConfigs/regional-us-central1",
			},
		},
		{
			tool: "spanner_create_instance",
			args: map[string]any{
				"project_id":   "test-project",
				"instance_id":  "test-instance",
				"config_id":    "regional-us-central1",
				"display_name": "Test Instance",
			},
			want:     `{"message":"Instance test-instance created successfully."}`,
			recorder: &instances.requestRecorder,
			wantRequest: &instancepb.CreateInstanceRequest{
				Parent:     "projects/test-project",
				InstanceId: "test-instance",
				Instance: &instancepb.Instance{
					Config:      "projects/test-project/instanceConfigs/regional-us-central1",
					DisplayName: "Test Instance",
					NodeCount:   1,
				},
			},
		},
		{
			tool:     "spanner_list_databases",
			args:     map[string]any{"project_id": "test-project", "instance_id": "test-instance"},
			want:     `{"databases":["db-1"]}`,
			recorder: &databases.requestRecorder,
			wantRequest: &databasepb.ListDatabasesRequest{
				Parent: "projects/test-project/instances/test-instance",
			},
		},
		{
			tool: "spanner_create_database",
			args: map[string]any{
				"project_id":  "test-project",
				"instance_id": "test-instance",
				"database_id": "db-1",
			},
			want:     `{"message":"Database db-1 created successfully."}`,
			recorder: &databases.requestRecorder,
			wantRequest: &databasepb.CreateDatabaseRequest{
				Parent:          "projects/test-project/instances/test-instance",
				CreateStatement: "CREATE DATABASE `db-1`",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			got, err := runTool(t, ts, tc.tool, tc.args)
			if err != nil {
				t.Fatalf("%s failed: %v", tc.tool, err)
			}
			if encoded := asJSON(t, got); encoded != tc.want {
				t.Errorf("result = %s, want %s", encoded, tc.want)
			}
			if diff := cmp.Diff(tc.wantRequest, tc.recorder.lastRequest(), protocmp.Transform()); diff != "" {
				t.Errorf("request mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestToolsetOverGRPCServerErrors checks that a server-side failure reaches
// the caller as an error rather than a success payload.
func TestToolsetOverGRPCServerErrors(t *testing.T) {
	ts := newGRPCToolset(t,
		&fakeInstanceAdminServer{fail: true},
		&fakeDatabaseAdminServer{fail: true},
	)

	tests := []struct {
		tool string
		args map[string]any
	}{
		{tool: "spanner_list_instances", args: map[string]any{"project_id": "test-project"}},
		{tool: "spanner_get_instance", args: map[string]any{"project_id": "test-project", "instance_id": "test-instance"}},
		{tool: "spanner_list_instance_configs", args: map[string]any{"project_id": "test-project"}},
		{tool: "spanner_get_instance_config", args: map[string]any{"project_id": "test-project", "config_id": "regional-us-central1"}},
		{tool: "spanner_create_instance", args: map[string]any{
			"project_id": "test-project", "instance_id": "test-instance",
			"config_id": "regional-us-central1", "display_name": "Test Instance",
		}},
		{tool: "spanner_list_databases", args: map[string]any{"project_id": "test-project", "instance_id": "test-instance"}},
		{tool: "spanner_create_database", args: map[string]any{
			"project_id": "test-project", "instance_id": "test-instance", "database_id": "db-1",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			got, err := runTool(t, ts, tc.tool, tc.args)
			if err == nil {
				t.Fatalf("%s returned %v, want an error", tc.tool, got)
			}
			if status.Code(err) != codes.FailedPrecondition {
				t.Errorf("status code = %v, want %v (error: %v)", status.Code(err), codes.FailedPrecondition, err)
			}
		})
	}
}

// TestCreateOperationFailure checks that a long-running operation that
// completes with an error is reported as an error.
func TestCreateOperationFailure(t *testing.T) {
	failedOp := &longrunningpb.Operation{
		Name:   "create-instance",
		Done:   true,
		Result: &longrunningpb.Operation_Error{Error: status.New(codes.ResourceExhausted, "no quota").Proto()},
	}
	ts := newGRPCToolset(t,
		&fakeInstanceAdminServer{createOp: failedOp},
		&fakeDatabaseAdminServer{createOp: failedOp},
	)

	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{tool: "spanner_create_instance", args: map[string]any{
			"project_id": "test-project", "instance_id": "test-instance",
			"config_id": "regional-us-central1", "display_name": "Test Instance",
		}},
		{tool: "spanner_create_database", args: map[string]any{
			"project_id": "test-project", "instance_id": "test-instance", "database_id": "db-1",
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			got, err := runTool(t, ts, tc.tool, tc.args)
			if err == nil {
				t.Fatalf("%s returned %v, want an error", tc.tool, got)
			}
			if status.Code(err) != codes.ResourceExhausted {
				t.Errorf("status code = %v, want %v (error: %v)", status.Code(err), codes.ResourceExhausted, err)
			}
		})
	}
}

func TestNewToolsetOverGRPC(t *testing.T) {
	ts := newGRPCToolset(t, &fakeInstanceAdminServer{}, &fakeDatabaseAdminServer{})

	if got := ts.Name(); got != defaultName {
		t.Errorf("Name() = %q, want %q", got, defaultName)
	}
	if diff := cmp.Diff(allToolNames, toolNames(t, ts)); diff != "" {
		t.Errorf("tool names mismatch (-want +got):\n%s", diff)
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
