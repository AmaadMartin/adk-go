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

// gapicInstanceAdmin adapts the generated instance admin client to
// instanceAdmin. It only builds requests and delegates.
type gapicInstanceAdmin struct {
	client *instance.InstanceAdminClient
}

func (a *gapicInstanceAdmin) ListInstances(ctx context.Context, projectID string) ([]string, error) {
	it := a.client.ListInstances(ctx, &instancepb.ListInstancesRequest{Parent: projectPath(projectID)})
	return collectNames(it.All(), (*instancepb.Instance).GetName)
}

func (a *gapicInstanceAdmin) GetInstance(ctx context.Context, projectID, instanceID string) (*instancepb.Instance, error) {
	return a.client.GetInstance(ctx, &instancepb.GetInstanceRequest{Name: instancePath(projectID, instanceID)})
}

func (a *gapicInstanceAdmin) ListInstanceConfigs(ctx context.Context, projectID string) ([]string, error) {
	it := a.client.ListInstanceConfigs(ctx, &instancepb.ListInstanceConfigsRequest{Parent: projectPath(projectID)})
	return collectNames(it.All(), (*instancepb.InstanceConfig).GetName)
}

func (a *gapicInstanceAdmin) GetInstanceConfig(ctx context.Context, projectID, configID string) (*instancepb.InstanceConfig, error) {
	return a.client.GetInstanceConfig(ctx, &instancepb.GetInstanceConfigRequest{Name: instanceConfigPath(projectID, configID)})
}

func (a *gapicInstanceAdmin) CreateInstance(ctx context.Context, projectID, instanceID, configID, displayName string, nodes int32) error {
	op, err := a.client.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
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

// gapicDatabaseAdmin adapts the generated database admin client to
// databaseAdmin. It only builds requests and delegates.
type gapicDatabaseAdmin struct {
	client *database.DatabaseAdminClient
}

func (a *gapicDatabaseAdmin) ListDatabases(ctx context.Context, projectID, instanceID string) ([]string, error) {
	it := a.client.ListDatabases(ctx, &databasepb.ListDatabasesRequest{Parent: instancePath(projectID, instanceID)})
	return collectNames(it.All(), (*databasepb.Database).GetName)
}

func (a *gapicDatabaseAdmin) CreateDatabase(ctx context.Context, projectID, instanceID, databaseID string) error {
	op, err := a.client.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
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
