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

package builderassistant

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestExploreProject(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", "name: root_agent\nsub_agents:\n  - config_path: research.yml\ntools:\n  - name: google_search\n")
	writeProjectFile(t, root, "research.yml", "agent_class: SequentialAgent\nname: research\n")
	writeProjectFile(t, root, "broken.yaml", "name: [unclosed\n")
	writeProjectFile(t, root, "tools/search.go", "package tools\n")
	writeProjectFile(t, root, "notes.md", "# notes\n")
	writeProjectFile(t, root, ".hidden/secret.txt", "hidden\n")
	writeProjectFile(t, root, "node_modules/dep/index.js", "// dep\n")
	ctx := newContext(t, root)

	got, err := exploreProject(ctx, exploreProjectArgs{})
	if err != nil {
		t.Fatalf("exploreProject returned error: %v", err)
	}

	wantProject := projectInfo{
		Name:             filepath.Base(root),
		AbsolutePath:     root,
		TotalFiles:       5,
		TotalDirectories: 1,
		HasGoFiles:       true,
		HasYAMLFiles:     true,
	}
	if diff := cmp.Diff(wantProject, got.Project); diff != "" {
		t.Errorf("project info mismatch (-want +got):\n%s", diff)
	}

	wantConfigs := []configSummary{
		{Filename: "broken.yaml", RelativePath: "broken.yaml", Size: 16},
		{
			Filename: "research.yml", RelativePath: "research.yml", Size: 44,
			IsValidYAML: true, AgentName: "research", AgentClass: "SequentialAgent",
		},
		{
			Filename: "root_agent.yaml", RelativePath: "root_agent.yaml", Size: 90,
			IsValidYAML: true, AgentName: "root_agent", AgentClass: "LlmAgent",
			HasSubAgents: true, HasTools: true,
		},
	}
	if diff := cmp.Diff(wantConfigs, got.ExistingConfigs); diff != "" {
		t.Errorf("existing configs mismatch (-want +got):\n%s", diff)
	}

	wantEntries := []string{"broken.yaml", "notes.md", "research.yml", "root_agent.yaml", "tools/", "tools/search.go"}
	if diff := cmp.Diff(wantEntries, got.Entries); diff != "" {
		t.Errorf("entries mismatch (-want +got):\n%s", diff)
	}
}

func TestExploreProjectReportsAnEmptyDirectory(t *testing.T) {
	root := newProject(t)
	ctx := newContext(t, root)

	got, err := exploreProject(ctx, exploreProjectArgs{})
	if err != nil {
		t.Fatalf("exploreProject returned error: %v", err)
	}
	want := exploreProjectResult{
		Project:         projectInfo{Name: filepath.Base(root), AbsolutePath: root},
		ExistingConfigs: []configSummary{},
		Entries:         []string{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("exploreProject mismatch (-want +got):\n%s", diff)
	}
}

func TestExploreProjectStopsAtTheDepthLimit(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "a/b/c.go", "package c\n")
	writeProjectFile(t, root, "a/b/deep/d.go", "package d\n")
	ctx := newContext(t, root)

	got, err := exploreProject(ctx, exploreProjectArgs{})
	if err != nil {
		t.Fatalf("exploreProject returned error: %v", err)
	}
	wantEntries := []string{"a/", "a/b/", "a/b/c.go"}
	if diff := cmp.Diff(wantEntries, got.Entries); diff != "" {
		t.Errorf("entries mismatch (-want +got):\n%s", diff)
	}
	// The directory below the limit is still counted, only not listed.
	if got.Project.TotalDirectories != 3 {
		t.Errorf("total directories = %d, want 3", got.Project.TotalDirectories)
	}
	if got.Project.TotalFiles != 1 {
		t.Errorf("total files = %d, want 1; the tree below the limit is not walked", got.Project.TotalFiles)
	}
}

func TestExploreProjectReportsAConfigItCannotRead(t *testing.T) {
	root := newProject(t)
	w := openTestWorkspace(t, root)

	// A name that is not there stands in for a config that disappears between
	// the directory listing and the read.
	got := w.summarizeConfig("vanished.yaml")
	want := configSummary{Filename: "vanished.yaml", RelativePath: "vanished.yaml"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("summarizeConfig mismatch (-want +got):\n%s", diff)
	}
}

func TestExploreProjectReportsAMissingRoot(t *testing.T) {
	ctx := newContext(t, filepath.Join(newProject(t), "absent"))

	if _, err := exploreProject(ctx, exploreProjectArgs{}); err == nil {
		t.Error("exploreProject on a missing project directory returned no error")
	}
}

func TestDepthOf(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{name: "a.go", want: 1},
		{name: "tools/a.go", want: 2},
		{name: "a/b/c.go", want: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := depthOf(test.name); got != test.want {
				t.Errorf("depthOf(%q) = %d, want %d", test.name, got, test.want)
			}
		})
	}
}

func TestExploreProjectReportsAnUnreadableDirectory(t *testing.T) {
	skipWhenRoot(t)
	root := newProject(t)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ctx := newContext(t, root)

	if _, err := exploreProject(ctx, exploreProjectArgs{}); err == nil {
		t.Error("exploreProject over an unreadable directory returned no error")
	}
}
