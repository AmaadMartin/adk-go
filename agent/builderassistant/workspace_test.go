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
	"errors"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// mapState is a session.ReadonlyState backed by a map.
type mapState map[string]any

func (s mapState) Get(key string) (any, error) {
	value, ok := s[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s mapState) All() iter.Seq2[string, any] { return maps.All(s) }

// fakeContext is an agent.Context that carries session state and nothing else.
// Every other method panics, so a tool that reaches for more fails loudly.
type fakeContext struct {
	agent.StrictContextMock
	state session.ReadonlyState
}

func (c *fakeContext) ReadonlyState() session.ReadonlyState { return c.state }

// newContext returns a context whose sandbox root is dir.
func newContext(t *testing.T, dir string) *fakeContext {
	t.Helper()
	return newContextWithState(t, mapState{RootDirectoryStateKey: dir})
}

func newContextWithState(t *testing.T, state session.ReadonlyState) *fakeContext {
	t.Helper()
	return &fakeContext{StrictContextMock: agent.NewStrictContextMock(t.Context()), state: state}
}

// newProject returns a temporary project directory with its symlinks resolved,
// so that tests can compare against the paths the workspace reports.
func newProject(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return real
}

// writeProjectFile creates a file inside the project directory.
func writeProjectFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return full
}

// openTestWorkspace opens the sandbox rooted at dir.
func openTestWorkspace(t *testing.T, dir string) *workspace {
	t.Helper()
	w, err := openWorkspace(newContext(t, dir))
	if err != nil {
		t.Fatalf("openWorkspace(%q) returned error: %v", dir, err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestWorkspaceResolveAcceptsPathsInsideTheRoot(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "tools/search.go", "package tools\n")
	w := openTestWorkspace(t, root)

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "plain relative path", path: "root_agent.yaml", want: "root_agent.yaml"},
		{name: "nested relative path", path: "tools/search.go", want: "tools/search.go"},
		{name: "dot prefixed path", path: "./root_agent.yaml", want: "root_agent.yaml"},
		{name: "interior parent segment", path: "tools/../root_agent.yaml", want: "root_agent.yaml"},
		{name: "empty path is the root", path: "", want: "."},
		{name: "dot is the root", path: ".", want: "."},
		{name: "quoted path", path: "'tools/search.go'", want: "tools/search.go"},
		{name: "padded segments", path: " tools / search.go ", want: "tools/search.go"},
		{name: "absolute path inside the root", path: filepath.Join(root, "tools/search.go"), want: "tools/search.go"},
		{name: "absolute path of the root", path: root, want: "."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := w.resolve(test.path)
			if err != nil {
				t.Fatalf("resolve(%q) returned error: %v", test.path, err)
			}
			if got != test.want {
				t.Errorf("resolve(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestWorkspaceResolveRejectsPathsOutsideTheRoot(t *testing.T) {
	parent := newProject(t)
	root := filepath.Join(parent, "project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// A sibling whose name extends the root's name: a prefix comparison that
	// forgets the separator would accept it.
	sibling := filepath.Join(parent, "project-evil")
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	outside := writeProjectFile(t, parent, "secret.txt", "secret\n")
	if err := os.Symlink(parent, filepath.Join(root, "escape_link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(parent, "missing.txt"), filepath.Join(root, "dangling_link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	w := openTestWorkspace(t, root)

	tests := []struct {
		name string
		path string
	}{
		{name: "parent directory", path: ".."},
		{name: "traversal through a child", path: "tools/../../secret.txt"},
		{name: "traversal from the root", path: "../secret.txt"},
		{name: "absolute path outside the root", path: outside},
		{name: "absolute sibling sharing the root prefix", path: filepath.Join(sibling, "secret.txt")},
		{name: "symlink leaving the root", path: "escape_link/secret.txt"},
		{name: "symlink target outside the root", path: "escape_link"},
		{name: "dangling symlink outside the root", path: "dangling_link"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := w.resolve(test.path)
			if !errors.Is(err, ErrOutsideRoot) {
				t.Fatalf("resolve(%q) = %q, %v; want an error matching ErrOutsideRoot", test.path, got, err)
			}
		})
	}
}

func TestWorkspaceResolveAllStopsAtTheFirstEscape(t *testing.T) {
	root := newProject(t)
	w := openTestWorkspace(t, root)

	got, err := w.resolveAll([]string{"a.yaml", "b.yaml"})
	if err != nil {
		t.Fatalf("resolveAll returned error: %v", err)
	}
	if want := []string{"a.yaml", "b.yaml"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("resolveAll = %v, want %v", got, want)
	}

	if _, err := w.resolveAll([]string{"a.yaml", "../escape"}); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("resolveAll with an escaping path returned %v, want an error matching ErrOutsideRoot", err)
	}
}

func TestWorkspaceAbsJoinsTheRoot(t *testing.T) {
	root := newProject(t)
	w := openTestWorkspace(t, root)

	if got, want := w.abs("tools/a.go"), filepath.Join(root, "tools/a.go"); got != want {
		t.Errorf("abs = %q, want %q", got, want)
	}
}

func TestOpenWorkspaceRejectsAMissingRoot(t *testing.T) {
	missing := filepath.Join(newProject(t), "absent")

	if _, err := openWorkspace(newContext(t, missing)); err == nil {
		t.Fatal("openWorkspace on a missing directory returned no error")
	}
}

func TestOpenWorkspaceRejectsARootThatIsAFile(t *testing.T) {
	blocked := writeProjectFile(t, newProject(t), "occupied", "not a directory\n")

	if _, err := openWorkspace(newContext(t, blocked)); err == nil {
		t.Fatal("openWorkspace on a regular file returned no error")
	}
}

func TestCreateWorkspaceCreatesTheRoot(t *testing.T) {
	root := filepath.Join(newProject(t), "nested", "project")

	w, err := createWorkspace(newContext(t, root))
	if err != nil {
		t.Fatalf("createWorkspace returned error: %v", err)
	}
	defer func() { _ = w.Close() }()

	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("Stat(%q) = %v, %v; want a directory", root, info, err)
	}
}

func TestCreateWorkspaceReportsAnUnusableRoot(t *testing.T) {
	// A regular file cannot become a directory, so MkdirAll fails.
	blocked := writeProjectFile(t, newProject(t), "occupied", "not a directory\n")

	if _, err := createWorkspace(newContext(t, blocked)); err == nil {
		t.Fatal("createWorkspace on a path occupied by a file returned no error")
	}
}

func TestRootDirectory(t *testing.T) {
	working, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	project := newProject(t)

	tests := []struct {
		name  string
		state session.ReadonlyState
		want  string
	}{
		{name: "absolute root", state: mapState{RootDirectoryStateKey: project}, want: project},
		{name: "padded root", state: mapState{RootDirectoryStateKey: "  " + project + "  "}, want: project},
		{name: "relative root", state: mapState{RootDirectoryStateKey: "sub"}, want: filepath.Join(working, "sub")},
		{name: "key absent", state: mapState{}, want: working},
		{name: "empty root", state: mapState{RootDirectoryStateKey: ""}, want: working},
		{name: "blank root", state: mapState{RootDirectoryStateKey: "   "}, want: working},
		{name: "root is not a string", state: mapState{RootDirectoryStateKey: 42}, want: working},
		{name: "no state at all", state: nil, want: working},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := rootDirectory(test.state)
			if err != nil {
				t.Fatalf("rootDirectory returned error: %v", err)
			}
			if got != test.want {
				t.Errorf("rootDirectory = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRootDirectoryFailsWithoutAWorkingDirectory(t *testing.T) {
	gone := filepath.Join(newProject(t), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := rootDirectory(nil); err == nil {
		t.Skip("this platform still reports a working directory after it is removed")
	}
}

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "unchanged", path: "tools/a.go", want: "tools/a.go"},
		{name: "surrounding quotes", path: "'tools/a.go'", want: "tools/a.go"},
		{name: "quoted segments", path: `"tools"/"a.go"`, want: "tools/a.go"},
		{name: "surrounding whitespace", path: "  tools/a.go\n", want: "tools/a.go"},
		{name: "padded segments", path: "tools / a.go", want: "tools/a.go"},
		{name: "empty", path: "", want: ""},
		{name: "interior spaces kept", path: "my tools/a b.go", want: "my tools/a b.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sanitizePath(test.path); got != test.want {
				t.Errorf("sanitizePath(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestWorkspaceOpenersFailWithoutAWorkingDirectory(t *testing.T) {
	gone := filepath.Join(newProject(t), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// With no root_directory in state the sandbox falls back to the working
	// directory, which no longer exists.
	ctx := newContextWithState(t, mapState{})

	if _, err := rootDirectory(ctx.ReadonlyState()); err == nil {
		t.Skip("this platform still reports a working directory after it is removed")
	}
	if _, err := openWorkspace(ctx); err == nil {
		t.Error("openWorkspace returned no error without a working directory")
	}
	if _, err := createWorkspace(ctx); err == nil {
		t.Error("createWorkspace returned no error without a working directory")
	}
}
