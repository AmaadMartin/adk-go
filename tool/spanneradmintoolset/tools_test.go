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
	"strings"
	"testing"

	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"

	"google.golang.org/adk/v2/agent"
	icontext "google.golang.org/adk/v2/internal/context"
	"google.golang.org/adk/v2/internal/toolinternal"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

// errBoom is the failure the fakes report, so a test can assert that the
// handler wrapped it with %w rather than replacing it.
var errBoom = errors.New("boom")

// fakeInstanceAdmin records the arguments of the single call a test makes and
// returns canned results.
type fakeInstanceAdmin struct {
	instanceNames []string
	instance      *instancepb.Instance
	configNames   []string
	config        *instancepb.InstanceConfig
	err           error

	gotProjectID   string
	gotInstanceID  string
	gotConfigID    string
	gotDisplayName string
	gotNodes       int32
}

func (f *fakeInstanceAdmin) ListInstances(_ context.Context, projectID string) ([]string, error) {
	f.gotProjectID = projectID
	return f.instanceNames, f.err
}

func (f *fakeInstanceAdmin) GetInstance(_ context.Context, projectID, instanceID string) (*instancepb.Instance, error) {
	f.gotProjectID, f.gotInstanceID = projectID, instanceID
	return f.instance, f.err
}

func (f *fakeInstanceAdmin) ListInstanceConfigs(_ context.Context, projectID string) ([]string, error) {
	f.gotProjectID = projectID
	return f.configNames, f.err
}

func (f *fakeInstanceAdmin) GetInstanceConfig(_ context.Context, projectID, configID string) (*instancepb.InstanceConfig, error) {
	f.gotProjectID, f.gotConfigID = projectID, configID
	return f.config, f.err
}

func (f *fakeInstanceAdmin) CreateInstance(_ context.Context, projectID, instanceID, configID, displayName string, nodes int32) error {
	f.gotProjectID, f.gotInstanceID, f.gotConfigID = projectID, instanceID, configID
	f.gotDisplayName, f.gotNodes = displayName, nodes
	return f.err
}

// fakeDatabaseAdmin records the arguments of the single call a test makes and
// returns canned results.
type fakeDatabaseAdmin struct {
	databaseNames []string
	err           error

	gotProjectID  string
	gotInstanceID string
	gotDatabaseID string
}

func (f *fakeDatabaseAdmin) ListDatabases(_ context.Context, projectID, instanceID string) ([]string, error) {
	f.gotProjectID, f.gotInstanceID = projectID, instanceID
	return f.databaseNames, f.err
}

func (f *fakeDatabaseAdmin) CreateDatabase(_ context.Context, projectID, instanceID, databaseID string) error {
	f.gotProjectID, f.gotInstanceID, f.gotDatabaseID = projectID, instanceID, databaseID
	return f.err
}

// testTools builds the seven tools over the given fakes.
func testTools(t *testing.T, instances instanceAdmin, databases databaseAdmin) []tool.Tool {
	t.Helper()
	tools, err := buildTools(instances, databases)
	if err != nil {
		t.Fatalf("buildTools() failed: %v", err)
	}
	return tools
}

// newTestToolset builds a toolset over the given fakes.
func newTestToolset(t *testing.T, instances instanceAdmin, databases databaseAdmin) *Toolset {
	t.Helper()
	return newToolset(Config{}, testTools(t, instances, databases))
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

// assertWrapped checks that err wraps errBoom and names the resource.
func assertWrapped(t *testing.T, err error, wantContains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want an error wrapping %v", errBoom)
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("errors.Is(err, errBoom) = false for %v, want true", err)
	}
	if !strings.Contains(err.Error(), wantContains) {
		t.Errorf("error %q does not contain %q", err, wantContains)
	}
}

func TestListInstances(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{
			name: "two instances reduce to short ids",
			names: []string{
				"projects/test-project/instances/test-instance-1",
				"projects/test-project/instances/test-instance-2",
			},
			want: `{"instances":["test-instance-1","test-instance-2"]}`,
		},
		{
			name:  "no instances encode as an empty array",
			names: nil,
			want:  `{"instances":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			instances := &fakeInstanceAdmin{instanceNames: tc.names}
			ts := newTestToolset(t, instances, &fakeDatabaseAdmin{})

			got, err := runTool(t, ts, "spanner_list_instances", map[string]any{"project_id": "test-project"})
			if err != nil {
				t.Fatalf("spanner_list_instances failed: %v", err)
			}
			if diff := asJSON(t, got); diff != tc.want {
				t.Errorf("result = %s, want %s", diff, tc.want)
			}
			if instances.gotProjectID != "test-project" {
				t.Errorf("project id = %q, want %q", instances.gotProjectID, "test-project")
			}
		})
	}
}

func TestListInstancesError(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{err: errBoom}, &fakeDatabaseAdmin{})

	_, err := runTool(t, ts, "spanner_list_instances", map[string]any{"project_id": "test-project"})

	assertWrapped(t, err, `list instances for project "test-project"`)
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
			instances := &fakeInstanceAdmin{instance: &instancepb.Instance{
				Name:            "projects/test-project/instances/ignored-name",
				DisplayName:     "Test Instance",
				Config:          "projects/test-project/instanceConfigs/regional-us-central1",
				NodeCount:       1,
				ProcessingUnits: 1000,
				Labels:          tc.labels,
			}}
			ts := newTestToolset(t, instances, &fakeDatabaseAdmin{})

			got, err := runTool(t, ts, "spanner_get_instance", map[string]any{
				"project_id":  "test-project",
				"instance_id": "test-instance",
			})
			if err != nil {
				t.Fatalf("spanner_get_instance failed: %v", err)
			}
			if encoded := asJSON(t, got); encoded != tc.want {
				t.Errorf("result = %s, want %s", encoded, tc.want)
			}
			if instances.gotInstanceID != "test-instance" {
				t.Errorf("instance id = %q, want %q", instances.gotInstanceID, "test-instance")
			}
		})
	}
}

func TestGetInstanceError(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{err: errBoom}, &fakeDatabaseAdmin{})

	_, err := runTool(t, ts, "spanner_get_instance", map[string]any{
		"project_id":  "test-project",
		"instance_id": "test-instance",
	})

	assertWrapped(t, err, `get instance "test-instance" in project "test-project"`)
}

func TestListInstanceConfigs(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{
			name: "configs reduce to short ids",
			names: []string{
				"projects/test-project/instanceConfigs/regional-us-central1",
				"projects/test-project/instanceConfigs/nam3",
			},
			want: `{"configs":["regional-us-central1","nam3"]}`,
		},
		{
			name:  "no configs encode as an empty array",
			names: nil,
			want:  `{"configs":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestToolset(t, &fakeInstanceAdmin{configNames: tc.names}, &fakeDatabaseAdmin{})

			got, err := runTool(t, ts, "spanner_list_instance_configs", map[string]any{"project_id": "test-project"})
			if err != nil {
				t.Fatalf("spanner_list_instance_configs failed: %v", err)
			}
			if diff := asJSON(t, got); diff != tc.want {
				t.Errorf("result = %s, want %s", diff, tc.want)
			}
		})
	}
}

func TestListInstanceConfigsError(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{err: errBoom}, &fakeDatabaseAdmin{})

	_, err := runTool(t, ts, "spanner_list_instance_configs", map[string]any{"project_id": "test-project"})

	assertWrapped(t, err, `list instance configs for project "test-project"`)
}

func TestGetInstanceConfig(t *testing.T) {
	tests := []struct {
		name     string
		replicas []*instancepb.ReplicaInfo
		want     string
	}{
		{
			name: "one read-write replica",
			replicas: []*instancepb.ReplicaInfo{{
				Location:              "us-central1",
				Type:                  instancepb.ReplicaInfo_READ_WRITE,
				DefaultLeaderLocation: true,
			}},
			want: `{"display_name":"us-central1","labels":{"env":"test"},` +
				`"name":"projects/test-project/instanceConfigs/regional-us-central1",` +
				`"replicas":[{"default_leader_location":true,"location":"us-central1","type":"READ_WRITE"}]}`,
		},
		{
			name:     "no replicas encode as an empty array",
			replicas: nil,
			want: `{"display_name":"us-central1","labels":{"env":"test"},` +
				`"name":"projects/test-project/instanceConfigs/regional-us-central1","replicas":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			instances := &fakeInstanceAdmin{config: &instancepb.InstanceConfig{
				Name:        "projects/test-project/instanceConfigs/regional-us-central1",
				DisplayName: "us-central1",
				Replicas:    tc.replicas,
				Labels:      map[string]string{"env": "test"},
			}}
			ts := newTestToolset(t, instances, &fakeDatabaseAdmin{})

			got, err := runTool(t, ts, "spanner_get_instance_config", map[string]any{
				"project_id": "test-project",
				"config_id":  "regional-us-central1",
			})
			if err != nil {
				t.Fatalf("spanner_get_instance_config failed: %v", err)
			}
			if diff := asJSON(t, got); diff != tc.want {
				t.Errorf("result = %s, want %s", diff, tc.want)
			}
			if instances.gotConfigID != "regional-us-central1" {
				t.Errorf("config id = %q, want %q", instances.gotConfigID, "regional-us-central1")
			}
		})
	}
}

func TestGetInstanceConfigError(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{err: errBoom}, &fakeDatabaseAdmin{})

	_, err := runTool(t, ts, "spanner_get_instance_config", map[string]any{
		"project_id": "test-project",
		"config_id":  "regional-us-central1",
	})

	assertWrapped(t, err, `get instance config "regional-us-central1" in project "test-project"`)
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
			instances := &fakeInstanceAdmin{}
			ts := newTestToolset(t, instances, &fakeDatabaseAdmin{})

			got, err := runTool(t, ts, "spanner_create_instance", tc.args)
			if err != nil {
				t.Fatalf("spanner_create_instance failed: %v", err)
			}

			want := `{"message":"Instance test-instance created successfully."}`
			if diff := asJSON(t, got); diff != want {
				t.Errorf("result = %s, want %s", diff, want)
			}
			if instances.gotNodes != tc.wantNodes {
				t.Errorf("nodes = %d, want %d", instances.gotNodes, tc.wantNodes)
			}
			if instances.gotConfigID != "regional-us-central1" {
				t.Errorf("config id = %q, want %q", instances.gotConfigID, "regional-us-central1")
			}
			if instances.gotDisplayName != "Test Instance" {
				t.Errorf("display name = %q, want %q", instances.gotDisplayName, "Test Instance")
			}
		})
	}
}

func TestCreateInstanceError(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{err: errBoom}, &fakeDatabaseAdmin{})

	_, err := runTool(t, ts, "spanner_create_instance", map[string]any{
		"project_id":   "test-project",
		"instance_id":  "test-instance",
		"config_id":    "regional-us-central1",
		"display_name": "Test Instance",
	})

	assertWrapped(t, err, `create instance "test-instance" in project "test-project"`)
}

func TestListDatabases(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{
			name: "databases reduce to short ids",
			names: []string{
				"projects/test-project/instances/test-instance/databases/test-database-1",
				"projects/test-project/instances/test-instance/databases/test-database-2",
			},
			want: `{"databases":["test-database-1","test-database-2"]}`,
		},
		{
			name:  "no databases encode as an empty array",
			names: nil,
			want:  `{"databases":[]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			databases := &fakeDatabaseAdmin{databaseNames: tc.names}
			ts := newTestToolset(t, &fakeInstanceAdmin{}, databases)

			got, err := runTool(t, ts, "spanner_list_databases", map[string]any{
				"project_id":  "test-project",
				"instance_id": "test-instance",
			})
			if err != nil {
				t.Fatalf("spanner_list_databases failed: %v", err)
			}
			if diff := asJSON(t, got); diff != tc.want {
				t.Errorf("result = %s, want %s", diff, tc.want)
			}
			if databases.gotInstanceID != "test-instance" {
				t.Errorf("instance id = %q, want %q", databases.gotInstanceID, "test-instance")
			}
		})
	}
}

func TestListDatabasesError(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{}, &fakeDatabaseAdmin{err: errBoom})

	_, err := runTool(t, ts, "spanner_list_databases", map[string]any{
		"project_id":  "test-project",
		"instance_id": "test-instance",
	})

	assertWrapped(t, err, `list databases in instance "test-instance" of project "test-project"`)
}

func TestCreateDatabase(t *testing.T) {
	databases := &fakeDatabaseAdmin{}
	ts := newTestToolset(t, &fakeInstanceAdmin{}, databases)

	got, err := runTool(t, ts, "spanner_create_database", map[string]any{
		"project_id":  "test-project",
		"instance_id": "test-instance",
		"database_id": "db-1",
	})
	if err != nil {
		t.Fatalf("spanner_create_database failed: %v", err)
	}

	want := `{"message":"Database db-1 created successfully."}`
	if diff := asJSON(t, got); diff != want {
		t.Errorf("result = %s, want %s", diff, want)
	}
	if databases.gotDatabaseID != "db-1" {
		t.Errorf("database id = %q, want %q", databases.gotDatabaseID, "db-1")
	}
}

func TestCreateDatabaseError(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{}, &fakeDatabaseAdmin{err: errBoom})

	_, err := runTool(t, ts, "spanner_create_database", map[string]any{
		"project_id":  "test-project",
		"instance_id": "test-instance",
		"database_id": "db-1",
	})

	assertWrapped(t, err, `create database "db-1" in instance "test-instance" of project "test-project"`)
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
			got := shortIDs(tc.names)
			if asJSON(t, got) != asJSON(t, tc.want) {
				t.Errorf("shortIDs(%q) = %q, want %q", tc.names, got, tc.want)
			}
		})
	}
}
