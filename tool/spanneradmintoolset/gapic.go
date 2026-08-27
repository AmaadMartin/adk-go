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
	"fmt"
	"iter"
	"time"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
)

// createTimeout bounds the wait for a provisioning operation, matching
// adk-python's timeout=300.
const createTimeout = 5 * time.Minute

// listInstances returns the full resource name of every instance in a project.
func listInstances(ctx context.Context, client *instance.InstanceAdminClient, projectID string) ([]string, error) {
	it := client.ListInstances(ctx, &instancepb.ListInstancesRequest{Parent: projectPath(projectID)})
	return collectNames(it.All(), (*instancepb.Instance).GetName)
}

func getInstance(ctx context.Context, client *instance.InstanceAdminClient, projectID, instanceID string) (*instancepb.Instance, error) {
	return client.GetInstance(ctx, &instancepb.GetInstanceRequest{Name: instancePath(projectID, instanceID)})
}

// listInstanceConfigs returns the full resource name of every instance config
// available to a project.
func listInstanceConfigs(ctx context.Context, client *instance.InstanceAdminClient, projectID string) ([]string, error) {
	it := client.ListInstanceConfigs(ctx, &instancepb.ListInstanceConfigsRequest{Parent: projectPath(projectID)})
	return collectNames(it.All(), (*instancepb.InstanceConfig).GetName)
}

func getInstanceConfig(ctx context.Context, client *instance.InstanceAdminClient, projectID, configID string) (*instancepb.InstanceConfig, error) {
	return client.GetInstanceConfig(ctx, &instancepb.GetInstanceConfigRequest{Name: instanceConfigPath(projectID, configID)})
}

// createInstance provisions an instance and waits for the operation to finish.
func createInstance(ctx context.Context, client *instance.InstanceAdminClient, projectID, instanceID, configID, displayName string, nodes int32) error {
	op, err := client.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
		Parent:     projectPath(projectID),
		InstanceId: instanceID,
		Instance: &instancepb.Instance{
			Config:      instanceConfigPath(projectID, configID),
			DisplayName: displayName,
			NodeCount:   nodes,
		},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	_, err = op.Wait(ctx)
	return err
}

// listDatabases returns the full resource name of every database in an instance.
func listDatabases(ctx context.Context, client *database.DatabaseAdminClient, projectID, instanceID string) ([]string, error) {
	it := client.ListDatabases(ctx, &databasepb.ListDatabasesRequest{Parent: instancePath(projectID, instanceID)})
	return collectNames(it.All(), (*databasepb.Database).GetName)
}

// createDatabase provisions a database and waits for the operation to finish.
func createDatabase(ctx context.Context, client *database.DatabaseAdminClient, projectID, instanceID, databaseID string) error {
	op, err := client.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          instancePath(projectID, instanceID),
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", databaseID),
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	_, err = op.Wait(ctx)
	return err
}

// collectNames drains a resource iterator into the resources' full names.
func collectNames[T any](seq iter.Seq2[T, error], name func(T) string) ([]string, error) {
	var names []string
	for resource, err := range seq {
		if err != nil {
			return nil, err
		}
		names = append(names, name(resource))
	}
	return names, nil
}

func projectPath(projectID string) string {
	return "projects/" + projectID
}

func instancePath(projectID, instanceID string) string {
	return fmt.Sprintf("projects/%s/instances/%s", projectID, instanceID)
}

func instanceConfigPath(projectID, configID string) string {
	return fmt.Sprintf("projects/%s/instanceConfigs/%s", projectID, configID)
}
