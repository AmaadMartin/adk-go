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
	"io"
	"testing"

	"github.com/google/go-cmp/cmp"

	"google.golang.org/adk/v2/agent"
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

// fakeCloser records that Close ran and reports a canned error.
type fakeCloser struct {
	err    error
	closed bool
}

func (c *fakeCloser) Close() error {
	c.closed = true
	return c.err
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
			ts := newToolset(Config{Name: tc.override}, testTools(t, &fakeInstanceAdmin{}, &fakeDatabaseAdmin{}))
			if got := ts.Name(); got != tc.want {
				t.Errorf("Name() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToolsetTools(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{}, &fakeDatabaseAdmin{})

	if diff := cmp.Diff(allToolNames, toolNames(t, ts)); diff != "" {
		t.Errorf("tool names mismatch (-want +got):\n%s", diff)
	}
}

func TestToolsetToolsHaveDescriptions(t *testing.T) {
	ts := newTestToolset(t, &fakeInstanceAdmin{}, &fakeDatabaseAdmin{})
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

func TestToolsetToolFilter(t *testing.T) {
	tests := []struct {
		name   string
		filter tool.Predicate
		want   []string
	}{
		{
			name:   "a nil filter exposes every tool",
			filter: nil,
			want:   allToolNames,
		},
		{
			name:   "an allow list narrows the set",
			filter: tool.AllowedToolsPredicate([]string{"spanner_list_instances", "spanner_list_databases"}),
			want:   []string{"spanner_list_instances", "spanner_list_databases"},
		},
		{
			name:   "a filter that rejects everything yields no tools",
			filter: func(agent.ReadonlyContext, tool.Tool) bool { return false },
			want:   []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newToolset(Config{ToolFilter: tc.filter}, testTools(t, &fakeInstanceAdmin{}, &fakeDatabaseAdmin{}))
			if diff := cmp.Diff(tc.want, toolNames(t, ts)); diff != "" {
				t.Errorf("tool names mismatch (-want +got):\n%s", diff)
			}
		})
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
			ts := newToolset(Config{}, testTools(t, &fakeInstanceAdmin{}, &fakeDatabaseAdmin{}), closers...)

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
