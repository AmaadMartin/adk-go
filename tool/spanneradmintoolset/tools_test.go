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
	"strings"
	"testing"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
)

// checkRequest compares the request the fake service received with the one
// the tool should have sent.
func checkRequest(t *testing.T, recorder *requestRecorder, want proto.Message) {
	t.Helper()
	if diff := cmp.Diff(want, recorder.lastRequest(), protocmp.Transform()); diff != "" {
		t.Errorf("request mismatch (-want +got):\n%s", diff)
	}
}

func checkResult(t *testing.T, got map[string]any, want string) {
	t.Helper()
	if encoded := asJSON(t, got); encoded != want {
		t.Errorf("result = %s, want %s", encoded, want)
	}
}

func TestListInstances(t *testing.T) {
	tests := []struct {
		name      string
		instances []*instancepb.Instance
		want      string
	}{
		{
			name: "two instances reduce to short ids",
			instances: []*instancepb.Instance{
				{Name: "projects/test-project/instances/test-instance-1"},
				{Name: "projects/test-project/instances/test-instance-2"},
			},
			want: `{"instances":["test-instance-1","test-instance-2"]}`,
		},
		{
			name:      "no instances encode as an empty array",
			instances: nil,
			want:      `{"instances":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeInstanceAdminServer{instances: tc.instances}
			ts := newTestToolset(t, server, &fakeDatabaseAdminServer{})

			got, err := runTool(t, ts, "spanner_list_instances", map[string]any{"project_id": "test-project"})
			if err != nil {
				t.Fatalf("spanner_list_instances failed: %v", err)
			}

			checkResult(t, got, tc.want)
			checkRequest(t, &server.requestRecorder, &instancepb.ListInstancesRequest{
				Parent: "projects/test-project",
			})
		})
	}
}

func TestGetInstance(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name:   "every field is reported and the requested id is echoed",
			labels: map[string]string{"env": "test"},
			want: `{"config":"projects/test-project/instanceConfigs/regional-us-central1",` +
				`"display_name":"Test Instance","instance_id":"test-instance",` +
				`"labels":{"env":"test"},"node_count":1,"processing_units":1000}`,
		},
		{
			name:   "absent labels encode as an empty object",
			labels: nil,
			want: `{"config":"projects/test-project/instanceConfigs/regional-us-central1",` +
				`"display_name":"Test Instance","instance_id":"test-instance",` +
				`"labels":{},"node_count":1,"processing_units":1000}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeInstanceAdminServer{instance: &instancepb.Instance{
				// The tool echoes the requested id, so this name is not used.
				Name:            "projects/test-project/instances/ignored-name",
				DisplayName:     "Test Instance",
				Config:          "projects/test-project/instanceConfigs/regional-us-central1",
				NodeCount:       1,
				ProcessingUnits: 1000,
				Labels:          tc.labels,
			}}
			ts := newTestToolset(t, server, &fakeDatabaseAdminServer{})

			got, err := runTool(t, ts, "spanner_get_instance", map[string]any{
				"project_id":  "test-project",
				"instance_id": "test-instance",
			})
			if err != nil {
				t.Fatalf("spanner_get_instance failed: %v", err)
			}

			checkResult(t, got, tc.want)
			checkRequest(t, &server.requestRecorder, &instancepb.GetInstanceRequest{
				Name: "projects/test-project/instances/test-instance",
			})
		})
	}
}

func TestListInstanceConfigs(t *testing.T) {
	tests := []struct {
		name    string
		configs []*instancepb.InstanceConfig
		want    string
	}{
		{
			name: "configs reduce to short ids",
			configs: []*instancepb.InstanceConfig{
				{Name: "projects/test-project/instanceConfigs/regional-us-central1"},
				{Name: "projects/test-project/instanceConfigs/nam3"},
			},
			want: `{"configs":["regional-us-central1","nam3"]}`,
		},
		{
			name:    "no configs encode as an empty array",
			configs: nil,
			want:    `{"configs":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeInstanceAdminServer{configs: tc.configs}
			ts := newTestToolset(t, server, &fakeDatabaseAdminServer{})

			got, err := runTool(t, ts, "spanner_list_instance_configs", map[string]any{"project_id": "test-project"})
			if err != nil {
				t.Fatalf("spanner_list_instance_configs failed: %v", err)
			}

			checkResult(t, got, tc.want)
			checkRequest(t, &server.requestRecorder, &instancepb.ListInstanceConfigsRequest{
				Parent: "projects/test-project",
			})
		})
	}
}

func TestGetInstanceConfig(t *testing.T) {
	tests := []struct {
		name     string
		replicas []*instancepb.ReplicaInfo
		labels   map[string]string
		want     string
	}{
		{
			name: "one read-write replica",
			replicas: []*instancepb.ReplicaInfo{{
				Location:              "us-central1",
				Type:                  instancepb.ReplicaInfo_READ_WRITE,
				DefaultLeaderLocation: true,
			}},
			labels: map[string]string{"env": "test"},
			want: `{"display_name":"us-central1","labels":{"env":"test"},` +
				`"name":"projects/test-project/instanceConfigs/regional-us-central1",` +
				`"replicas":[{"default_leader_location":true,"location":"us-central1","type":"READ_WRITE"}]}`,
		},
		{
			name:     "no replicas and no labels encode as empty",
			replicas: nil,
			labels:   nil,
			want: `{"display_name":"us-central1","labels":{},` +
				`"name":"projects/test-project/instanceConfigs/regional-us-central1","replicas":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeInstanceAdminServer{config: &instancepb.InstanceConfig{
				// Unlike the list tools, this one reports the full resource name.
				Name:        "projects/test-project/instanceConfigs/regional-us-central1",
				DisplayName: "us-central1",
				Replicas:    tc.replicas,
				Labels:      tc.labels,
			}}
			ts := newTestToolset(t, server, &fakeDatabaseAdminServer{})

			got, err := runTool(t, ts, "spanner_get_instance_config", map[string]any{
				"project_id": "test-project",
				"config_id":  "regional-us-central1",
			})
			if err != nil {
				t.Fatalf("spanner_get_instance_config failed: %v", err)
			}

			checkResult(t, got, tc.want)
			checkRequest(t, &server.requestRecorder, &instancepb.GetInstanceConfigRequest{
				Name: "projects/test-project/instanceConfigs/regional-us-central1",
			})
		})
	}
}

func TestCreateInstance(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		wantNodes int32
	}{
		{
			name: "an explicit node count reaches the API",
			args: map[string]any{
				"project_id":   "test-project",
				"instance_id":  "test-instance",
				"config_id":    "regional-us-central1",
				"display_name": "Test Instance",
				"nodes":        3,
			},
			wantNodes: 3,
		},
		{
			name: "an omitted node count defaults to one",
			args: map[string]any{
				"project_id":   "test-project",
				"instance_id":  "test-instance",
				"config_id":    "regional-us-central1",
				"display_name": "Test Instance",
			},
			wantNodes: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeInstanceAdminServer{createOp: doneOperation(t, "create-instance", &instancepb.Instance{
				Name: "projects/test-project/instances/test-instance",
			})}
			ts := newTestToolset(t, server, &fakeDatabaseAdminServer{})

			got, err := runTool(t, ts, "spanner_create_instance", tc.args)
			if err != nil {
				t.Fatalf("spanner_create_instance failed: %v", err)
			}

			checkResult(t, got, `{"message":"Instance test-instance created successfully."}`)
			checkRequest(t, &server.requestRecorder, &instancepb.CreateInstanceRequest{
				Parent:     "projects/test-project",
				InstanceId: "test-instance",
				Instance: &instancepb.Instance{
					Config:      "projects/test-project/instanceConfigs/regional-us-central1",
					DisplayName: "Test Instance",
					NodeCount:   tc.wantNodes,
				},
			})
		})
	}
}

func TestListDatabases(t *testing.T) {
	tests := []struct {
		name      string
		databases []*databasepb.Database
		want      string
	}{
		{
			name: "databases reduce to short ids",
			databases: []*databasepb.Database{
				{Name: "projects/test-project/instances/test-instance/databases/test-database-1"},
				{Name: "projects/test-project/instances/test-instance/databases/test-database-2"},
			},
			want: `{"databases":["test-database-1","test-database-2"]}`,
		},
		{
			name:      "no databases encode as an empty array",
			databases: nil,
			want:      `{"databases":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeDatabaseAdminServer{databases: tc.databases}
			ts := newTestToolset(t, &fakeInstanceAdminServer{}, server)

			got, err := runTool(t, ts, "spanner_list_databases", map[string]any{
				"project_id":  "test-project",
				"instance_id": "test-instance",
			})
			if err != nil {
				t.Fatalf("spanner_list_databases failed: %v", err)
			}

			checkResult(t, got, tc.want)
			checkRequest(t, &server.requestRecorder, &databasepb.ListDatabasesRequest{
				Parent: "projects/test-project/instances/test-instance",
			})
		})
	}
}

func TestCreateDatabase(t *testing.T) {
	server := &fakeDatabaseAdminServer{createOp: doneOperation(t, "create-database", &databasepb.Database{
		Name: "projects/test-project/instances/test-instance/databases/db-1",
	})}
	ts := newTestToolset(t, &fakeInstanceAdminServer{}, server)

	got, err := runTool(t, ts, "spanner_create_database", map[string]any{
		"project_id":  "test-project",
		"instance_id": "test-instance",
		"database_id": "db-1",
	})
	if err != nil {
		t.Fatalf("spanner_create_database failed: %v", err)
	}

	checkResult(t, got, `{"message":"Database db-1 created successfully."}`)
	checkRequest(t, &server.requestRecorder, &databasepb.CreateDatabaseRequest{
		Parent:          "projects/test-project/instances/test-instance",
		CreateStatement: "CREATE DATABASE `db-1`",
	})
}

// TestToolErrors checks that a server-side failure reaches the caller as an
// error that names the resource and keeps the gRPC status code.
func TestToolErrors(t *testing.T) {
	ts := newTestToolset(t,
		&fakeInstanceAdminServer{fail: true},
		&fakeDatabaseAdminServer{fail: true},
	)

	tests := []struct {
		tool     string
		args     map[string]any
		wantErr  string
		wantCode codes.Code
	}{
		{
			tool:    "spanner_list_instances",
			args:    map[string]any{"project_id": "test-project"},
			wantErr: `list instances for project "test-project"`,
		},
		{
			tool:    "spanner_get_instance",
			args:    map[string]any{"project_id": "test-project", "instance_id": "test-instance"},
			wantErr: `get instance "test-instance" in project "test-project"`,
		},
		{
			tool:    "spanner_list_instance_configs",
			args:    map[string]any{"project_id": "test-project"},
			wantErr: `list instance configs for project "test-project"`,
		},
		{
			tool:    "spanner_get_instance_config",
			args:    map[string]any{"project_id": "test-project", "config_id": "regional-us-central1"},
			wantErr: `get instance config "regional-us-central1" in project "test-project"`,
		},
		{
			tool: "spanner_create_instance",
			args: map[string]any{
				"project_id": "test-project", "instance_id": "test-instance",
				"config_id": "regional-us-central1", "display_name": "Test Instance",
			},
			wantErr: `create instance "test-instance" in project "test-project"`,
		},
		{
			tool:    "spanner_list_databases",
			args:    map[string]any{"project_id": "test-project", "instance_id": "test-instance"},
			wantErr: `list databases in instance "test-instance" of project "test-project"`,
		},
		{
			tool: "spanner_create_database",
			args: map[string]any{
				"project_id": "test-project", "instance_id": "test-instance", "database_id": "db-1",
			},
			wantErr: `create database "db-1" in instance "test-instance" of project "test-project"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			got, err := runTool(t, ts, tc.tool, tc.args)
			if err == nil {
				t.Fatalf("%s returned %v, want an error", tc.tool, got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
			// The status code survives only because the handler wraps with %w.
			if status.Code(err) != codes.FailedPrecondition {
				t.Errorf("status code = %v, want %v (error: %v)", status.Code(err), codes.FailedPrecondition, err)
			}
		})
	}
}

// TestCreateOperationFailure checks that a long-running operation which
// completes with an error is reported as an error.
func TestCreateOperationFailure(t *testing.T) {
	failedOp := &longrunningpb.Operation{
		Name:   "create",
		Done:   true,
		Result: &longrunningpb.Operation_Error{Error: status.New(codes.ResourceExhausted, "no quota").Proto()},
	}
	ts := newTestToolset(t,
		&fakeInstanceAdminServer{createOp: failedOp},
		&fakeDatabaseAdminServer{createOp: failedOp},
	)

	tests := []struct {
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
	}
	for _, tc := range tests {
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

func TestShortIDs(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  []string
	}{
		{name: "empty input yields an empty slice", names: nil, want: []string{}},
		{name: "a resource name keeps its last segment", names: []string{"projects/p/instances/i"}, want: []string{"i"}},
		{name: "a bare id is unchanged", names: []string{"i"}, want: []string{"i"}},
		{name: "a trailing slash yields an empty id", names: []string{"projects/p/instances/"}, want: []string{""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, shortIDs(tc.names)); diff != "" {
				t.Errorf("shortIDs(%q) mismatch (-want +got):\n%s", tc.names, diff)
			}
		})
	}
}
