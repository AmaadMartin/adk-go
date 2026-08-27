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

// Package spanneradmintoolset provides tools that administer Cloud Spanner.
// An agent can list and inspect instances, instance configs and databases, and
// it can provision new instances and databases.
//
// The toolset owns the authenticated Spanner admin clients, so credentials
// never reach a tool's arguments and the model never sees them. The clients
// hold gRPC connections, so the caller must release them:
//
//	ts, err := spanneradmintoolset.New(ctx, spanneradmintoolset.Config{})
//	if err != nil {
//		return err
//	}
//	defer ts.Close()
//
// The tools spanner_create_instance and spanner_create_database provision
// billable Google Cloud resources. Neither asks for confirmation, which
// matches adk-python. Wrap the toolset to gate them:
//
//	gated := tool.WithConfirmation(ts, false, func(name string, _ any) bool {
//		return name == "spanner_create_instance" || name == "spanner_create_database"
//	})
//
// adk-python reports a failure as a "status": "ERROR" payload and never
// raises. These tools return a Go error instead. The flow reports that error
// to the model and runs the OnToolError callbacks, which a status field would
// bypass.
//
// EXPERIMENTAL: This package is experimental and its behavior may change or be
// removed without notice.
package spanneradmintoolset

import (
	"context"
	"errors"
	"fmt"
	"io"

	database "cloud.google.com/go/spanner/admin/database/apiv1"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"google.golang.org/api/option"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// defaultName is the toolset name used when Config.Name is empty.
const defaultName = "SpannerAdminToolset"

// Config configures the toolset. The zero value is valid and authenticates
// with Application Default Credentials.
type Config struct {
	// ClientOptions are passed to both Spanner admin clients. Leave nil to use
	// Application Default Credentials.
	ClientOptions []option.ClientOption
	// Name overrides the toolset name. Defaults to "SpannerAdminToolset".
	Name string
	// ToolFilter selects which tools the toolset exposes. Nil exposes all of them.
	ToolFilter tool.Predicate
}

// Toolset groups the Spanner admin tools. Create it with New and release it
// with Close.
type Toolset struct {
	name    string
	tools   []tool.Tool
	filter  tool.Predicate
	closers []io.Closer
}

var _ tool.Toolset = (*Toolset)(nil)

// instanceAdmin is the instance half of the Spanner Admin API that this
// package uses. The list method returns full resource names; the caller
// shortens them.
type instanceAdmin interface {
	ListInstances(ctx context.Context, projectID string) ([]string, error)
	GetInstance(ctx context.Context, projectID, instanceID string) (*instancepb.Instance, error)
	ListInstanceConfigs(ctx context.Context, projectID string) ([]string, error)
	GetInstanceConfig(ctx context.Context, projectID, configID string) (*instancepb.InstanceConfig, error)
	CreateInstance(ctx context.Context, projectID, instanceID, configID, displayName string, nodes int32) error
}

// databaseAdmin is the database half of the Spanner Admin API that this
// package uses.
type databaseAdmin interface {
	ListDatabases(ctx context.Context, projectID, instanceID string) ([]string, error)
	CreateDatabase(ctx context.Context, projectID, instanceID, databaseID string) error
}

// New creates a Spanner admin toolset backed by the Spanner Admin API. The
// caller must call Close to release the underlying gRPC connections.
func New(ctx context.Context, cfg Config) (*Toolset, error) {
	instances, err := instance.NewInstanceAdminClient(ctx, cfg.ClientOptions...)
	if err != nil {
		return nil, fmt.Errorf("create Spanner instance admin client: %w", err)
	}
	databases, err := database.NewDatabaseAdminClient(ctx, cfg.ClientOptions...)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create Spanner database admin client: %w", err), instances.Close())
	}
	tools, err := buildTools(&gapicInstanceAdmin{client: instances}, &gapicDatabaseAdmin{client: databases})
	if err != nil {
		return nil, errors.Join(err, instances.Close(), databases.Close())
	}
	return newToolset(cfg, tools, instances, databases), nil
}

// newToolset assembles a toolset over already-built tools. Closers are
// released by Close.
func newToolset(cfg Config, tools []tool.Tool, closers ...io.Closer) *Toolset {
	name := cfg.Name
	if name == "" {
		name = defaultName
	}
	return &Toolset{name: name, tools: tools, filter: cfg.ToolFilter, closers: closers}
}

// Name implements tool.Toolset. It returns the name of the toolset.
func (ts *Toolset) Name() string { return ts.name }

// Tools implements tool.Toolset. It returns the tools that Config.ToolFilter
// selects, or all of them when the filter is nil.
func (ts *Toolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	if ts.filter == nil {
		return ts.tools, nil
	}
	selected := make([]tool.Tool, 0, len(ts.tools))
	for _, t := range ts.tools {
		if ts.filter(ctx, t) {
			selected = append(selected, t)
		}
	}
	return selected, nil
}

// Close releases the Spanner admin clients. Call it once, when the toolset is
// no longer in use.
func (ts *Toolset) Close() error {
	errs := make([]error, 0, len(ts.closers))
	for _, c := range ts.closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}
