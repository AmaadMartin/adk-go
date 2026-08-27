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
	"errors"
	"fmt"
	"maps"
	"strings"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// defaultNodes is the node count used when the model omits it, matching
// adk-python's `nodes: int = 1`.
const defaultNodes int32 = 1

// instanceInfo describes a single Spanner instance.
type instanceInfo struct {
	InstanceID      string            `json:"instance_id"`
	DisplayName     string            `json:"display_name"`
	Config          string            `json:"config"`
	NodeCount       int32             `json:"node_count"`
	ProcessingUnits int32             `json:"processing_units"`
	Labels          map[string]string `json:"labels"`
}

// replicaInfo describes one replica of a Spanner instance config.
type replicaInfo struct {
	Location string `json:"location"`
	// Type is the replica type name, for example "READ_WRITE".
	Type                  string `json:"type"`
	DefaultLeaderLocation bool   `json:"default_leader_location"`
}

// instanceConfigInfo describes a single Spanner instance config.
type instanceConfigInfo struct {
	// Name is the full resource name of the config.
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Replicas    []replicaInfo     `json:"replicas"`
	Labels      map[string]string `json:"labels"`
}

type listInstancesArgs struct {
	ProjectID string `json:"project_id"` // the Google Cloud project id
}

type listInstancesResult struct {
	Instances []string `json:"instances"` // the Spanner instance ids
}

type getInstanceArgs struct {
	ProjectID  string `json:"project_id"`  // the Google Cloud project id
	InstanceID string `json:"instance_id"` // the Spanner instance id
}

type listInstanceConfigsArgs struct {
	ProjectID string `json:"project_id"` // the Google Cloud project id
}

type listInstanceConfigsResult struct {
	Configs []string `json:"configs"` // the Spanner instance config ids
}

type getInstanceConfigArgs struct {
	ProjectID string `json:"project_id"` // the Google Cloud project id
	ConfigID  string `json:"config_id"`  // the Spanner instance config id, for example regional-us-central1
}

type createInstanceArgs struct {
	ProjectID   string `json:"project_id"`   // the Google Cloud project id
	InstanceID  string `json:"instance_id"`  // the Spanner instance id to create
	ConfigID    string `json:"config_id"`    // the instance config id, for example regional-us-central1
	DisplayName string `json:"display_name"` // the display name for the instance
	// Nodes is the number of nodes for the instance. It is optional and
	// defaults to 1.
	Nodes int32 `json:"nodes,omitempty"`
}

type listDatabasesArgs struct {
	ProjectID  string `json:"project_id"`  // the Google Cloud project id
	InstanceID string `json:"instance_id"` // the Spanner instance id
}

type listDatabasesResult struct {
	Databases []string `json:"databases"` // the Spanner database ids
}

type createDatabaseArgs struct {
	ProjectID  string `json:"project_id"`  // the Google Cloud project id
	InstanceID string `json:"instance_id"` // the Spanner instance id
	DatabaseID string `json:"database_id"` // the Spanner database id to create
}

// messageResult reports the outcome of a provisioning tool.
type messageResult struct {
	Message string `json:"message"`
}

// buildTools creates the seven admin tools bound to the given admin clients.
func buildTools(instances *instance.InstanceAdminClient, databases *database.DatabaseAdminClient) ([]tool.Tool, error) {
	var tools []tool.Tool
	var errs []error
	add := func(t tool.Tool, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		tools = append(tools, t)
	}

	add(functiontool.New(functiontool.Config{
		Name:        "spanner_list_instances",
		Description: "List Spanner instances within a project.",
	}, func(ctx agent.Context, args listInstancesArgs) (listInstancesResult, error) {
		names, err := listInstances(ctx, instances, args.ProjectID)
		if err != nil {
			return listInstancesResult{}, fmt.Errorf("list instances for project %q: %w", args.ProjectID, err)
		}
		return listInstancesResult{Instances: shortIDs(names)}, nil
	}))

	add(functiontool.New(functiontool.Config{
		Name:        "spanner_get_instance",
		Description: "Get details of a Spanner instance.",
	}, func(ctx agent.Context, args getInstanceArgs) (*instanceInfo, error) {
		inst, err := getInstance(ctx, instances, args.ProjectID, args.InstanceID)
		if err != nil {
			return nil, fmt.Errorf("get instance %q in project %q: %w", args.InstanceID, args.ProjectID, err)
		}
		return &instanceInfo{
			InstanceID:      args.InstanceID,
			DisplayName:     inst.GetDisplayName(),
			Config:          inst.GetConfig(),
			NodeCount:       inst.GetNodeCount(),
			ProcessingUnits: inst.GetProcessingUnits(),
			Labels:          copyLabels(inst.GetLabels()),
		}, nil
	}))

	add(functiontool.New(functiontool.Config{
		Name:        "spanner_list_instance_configs",
		Description: "List Spanner instance configs available for a project.",
	}, func(ctx agent.Context, args listInstanceConfigsArgs) (listInstanceConfigsResult, error) {
		names, err := listInstanceConfigs(ctx, instances, args.ProjectID)
		if err != nil {
			return listInstanceConfigsResult{}, fmt.Errorf("list instance configs for project %q: %w", args.ProjectID, err)
		}
		return listInstanceConfigsResult{Configs: shortIDs(names)}, nil
	}))

	add(functiontool.New(functiontool.Config{
		Name:        "spanner_get_instance_config",
		Description: "Get details of a Spanner instance config.",
	}, func(ctx agent.Context, args getInstanceConfigArgs) (*instanceConfigInfo, error) {
		cfg, err := getInstanceConfig(ctx, instances, args.ProjectID, args.ConfigID)
		if err != nil {
			return nil, fmt.Errorf("get instance config %q in project %q: %w", args.ConfigID, args.ProjectID, err)
		}
		replicas := make([]replicaInfo, 0, len(cfg.GetReplicas()))
		for _, r := range cfg.GetReplicas() {
			replicas = append(replicas, replicaInfo{
				Location:              r.GetLocation(),
				Type:                  r.GetType().String(),
				DefaultLeaderLocation: r.GetDefaultLeaderLocation(),
			})
		}
		return &instanceConfigInfo{
			Name:        cfg.GetName(),
			DisplayName: cfg.GetDisplayName(),
			Replicas:    replicas,
			Labels:      copyLabels(cfg.GetLabels()),
		}, nil
	}))

	add(functiontool.New(functiontool.Config{
		Name:        "spanner_create_instance",
		Description: "Create a Spanner instance. This provisions a billable Google Cloud resource.",
	}, func(ctx agent.Context, args createInstanceArgs) (messageResult, error) {
		nodes := args.Nodes
		if nodes == 0 {
			nodes = defaultNodes
		}
		if err := createInstance(ctx, instances, args.ProjectID, args.InstanceID, args.ConfigID, args.DisplayName, nodes); err != nil {
			return messageResult{}, fmt.Errorf("create instance %q in project %q: %w", args.InstanceID, args.ProjectID, err)
		}
		return messageResult{Message: fmt.Sprintf("Instance %s created successfully.", args.InstanceID)}, nil
	}))

	add(functiontool.New(functiontool.Config{
		Name:        "spanner_list_databases",
		Description: "List Spanner databases within an instance.",
	}, func(ctx agent.Context, args listDatabasesArgs) (listDatabasesResult, error) {
		names, err := listDatabases(ctx, databases, args.ProjectID, args.InstanceID)
		if err != nil {
			return listDatabasesResult{}, fmt.Errorf("list databases in instance %q of project %q: %w", args.InstanceID, args.ProjectID, err)
		}
		return listDatabasesResult{Databases: shortIDs(names)}, nil
	}))

	add(functiontool.New(functiontool.Config{
		Name:        "spanner_create_database",
		Description: "Create a Spanner database. This provisions a billable Google Cloud resource.",
	}, func(ctx agent.Context, args createDatabaseArgs) (messageResult, error) {
		if err := createDatabase(ctx, databases, args.ProjectID, args.InstanceID, args.DatabaseID); err != nil {
			return messageResult{}, fmt.Errorf("create database %q in instance %q of project %q: %w", args.DatabaseID, args.InstanceID, args.ProjectID, err)
		}
		return messageResult{Message: fmt.Sprintf("Database %s created successfully.", args.DatabaseID)}, nil
	}))

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("create Spanner admin tools: %w", err)
	}
	return tools, nil
}

// copyLabels detaches the labels from the API response. The result is never
// nil, so an absent label set encodes as {}, matching adk-python's dict copy.
func copyLabels(labels map[string]string) map[string]string {
	copied := make(map[string]string, len(labels))
	maps.Copy(copied, labels)
	return copied
}

// shortIDs reduces full resource names to their last path segment, as
// adk-python does with name.split("/")[-1]. The result is never nil, so an
// empty list encodes as [].
func shortIDs(names []string) []string {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		ids = append(ids, name[strings.LastIndex(name, "/")+1:])
	}
	return ids
}
