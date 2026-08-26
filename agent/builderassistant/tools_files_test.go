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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"google.golang.org/adk/v2/tool"
)

func TestReadFiles(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", "name: root\n")
	writeProjectFile(t, root, "tools/search.go", "package tools\n")
	ctx := newContext(t, root)

	got, err := readFiles(ctx, readFilesArgs{FilePaths: []string{"root_agent.yaml", "tools/search.go"}})
	if err != nil {
		t.Fatalf("readFiles returned error: %v", err)
	}
	want := readFilesResult{
		Success: true,
		Files: map[string]fileRead{
			"root_agent.yaml": {Content: "name: root\n", FileSize: 11},
			"tools/search.go": {Content: "package tools\n", FileSize: 14},
		},
		SuccessfulReads: 2,
		TotalFiles:      2,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("readFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestReadFilesReportsAMissingFileWithoutFailingTheCall(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "present.yaml", "name: root\n")
	ctx := newContext(t, root)

	got, err := readFiles(ctx, readFilesArgs{FilePaths: []string{"present.yaml", "absent.yaml"}})
	if err != nil {
		t.Fatalf("readFiles returned error: %v", err)
	}
	if got.Success {
		t.Error("readFiles reported success although one file is missing")
	}
	if got.SuccessfulReads != 1 || got.TotalFiles != 2 {
		t.Errorf("readFiles counted %d of %d reads, want 1 of 2", got.SuccessfulReads, got.TotalFiles)
	}
	absent := got.Files["absent.yaml"]
	if absent.Content != "" || absent.FileSize != 0 {
		t.Errorf("readFiles returned content for the file it could not read: %+v", absent)
	}
	if want := "file does not exist: " + filepath.Join(root, "absent.yaml"); absent.Error != want {
		t.Errorf("readFiles error = %q, want %q", absent.Error, want)
	}
}

func TestReadFilesReportsAnUnreadableFile(t *testing.T) {
	root := newProject(t)
	if err := os.Mkdir(filepath.Join(root, "notafile"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	ctx := newContext(t, root)

	got, err := readFiles(ctx, readFilesArgs{FilePaths: []string{"notafile"}})
	if err != nil {
		t.Fatalf("readFiles returned error: %v", err)
	}
	if got.Success {
		t.Error("readFiles reported success although the path is a directory")
	}
	if entry := got.Files["notafile"]; !strings.HasPrefix(entry.Error, "failed to read ") {
		t.Errorf("readFiles error = %q, want a read failure", entry.Error)
	}
}

func TestReadFilesRejectsATraversal(t *testing.T) {
	ctx := newContext(t, newProject(t))

	if _, err := readFiles(ctx, readFilesArgs{FilePaths: []string{"../secret.txt"}}); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("readFiles returned %v, want an error matching ErrOutsideRoot", err)
	}
}

func TestReadFilesReportsAMissingRoot(t *testing.T) {
	ctx := newContext(t, filepath.Join(newProject(t), "absent"))

	if _, err := readFiles(ctx, readFilesArgs{FilePaths: []string{"a.yaml"}}); err == nil {
		t.Error("readFiles on a missing project directory returned no error")
	}
}

func TestWriteFiles(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", "old\n")
	ctx := newContext(t, root)

	got, err := writeFiles(ctx, writeFilesArgs{Files: map[string]string{
		"root_agent.yaml": "name: root\n",
		"tools/search.go": "package tools\n",
	}})
	if err != nil {
		t.Fatalf("writeFiles returned error: %v", err)
	}
	want := writeFilesResult{
		Success: true,
		Files: map[string]fileWrite{
			"root_agent.yaml": {FileSize: 11, ExistedBefore: true},
			"tools/search.go": {FileSize: 14},
		},
		SuccessfulWrites: 2,
		TotalFiles:       2,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("writeFiles mismatch (-want +got):\n%s", diff)
	}
	// The nested file proves the missing parent directory was created.
	content, err := os.ReadFile(filepath.Join(root, "tools/search.go"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "package tools\n" {
		t.Errorf("tools/search.go holds %q, want %q", content, "package tools\n")
	}
}

func TestWriteFilesCreatesTheProjectDirectory(t *testing.T) {
	root := filepath.Join(newProject(t), "new_project")
	ctx := newContext(t, root)

	if _, err := writeFiles(ctx, writeFilesArgs{Files: map[string]string{"root_agent.yaml": "name: root\n"}}); err != nil {
		t.Fatalf("writeFiles returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "root_agent.yaml")); err != nil {
		t.Errorf("Stat after writeFiles: %v", err)
	}
}

func TestWriteFilesReportsAFileItCannotWrite(t *testing.T) {
	root := newProject(t)
	// "blocked" is a file, so "blocked/child.go" has no valid parent.
	writeProjectFile(t, root, "blocked", "occupied\n")
	ctx := newContext(t, root)

	got, err := writeFiles(ctx, writeFilesArgs{Files: map[string]string{
		"blocked/child.go": "package child\n",
		"fine.go":          "package fine\n",
	}})
	if err != nil {
		t.Fatalf("writeFiles returned error: %v", err)
	}
	if got.Success {
		t.Error("writeFiles reported success although one write failed")
	}
	if got.SuccessfulWrites != 1 {
		t.Errorf("writeFiles counted %d writes, want 1", got.SuccessfulWrites)
	}
	if entry := got.Files["blocked/child.go"]; entry.Error == "" {
		t.Error("writeFiles reported no error for the blocked path")
	}
}

func TestWriteFilesRejectsATraversal(t *testing.T) {
	root := newProject(t)
	ctx := newContext(t, root)

	_, err := writeFiles(ctx, writeFilesArgs{Files: map[string]string{"../escape.go": "package escape\n"}})
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("writeFiles returned %v, want an error matching ErrOutsideRoot", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.go")); !errors.Is(err, os.ErrNotExist) {
		t.Error("writeFiles created a file outside the project directory")
	}
}

func TestWriteFilesReportsAnUnusableRoot(t *testing.T) {
	blocked := writeProjectFile(t, newProject(t), "occupied", "not a directory\n")
	ctx := newContext(t, blocked)

	if _, err := writeFiles(ctx, writeFilesArgs{Files: map[string]string{"a.go": "package a\n"}}); err == nil {
		t.Error("writeFiles with a file as the project directory returned no error")
	}
}

func TestDeleteFiles(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "stale.go", "package stale\n")
	ctx := newContext(t, root)

	got, err := deleteFiles(ctx, deleteFilesArgs{FilePaths: []string{"stale.go", "already_gone.go"}})
	if err != nil {
		t.Fatalf("deleteFiles returned error: %v", err)
	}
	want := deleteFilesResult{
		Success: true,
		Files: map[string]fileDelete{
			"stale.go":        {Existed: true, FileSize: 14},
			"already_gone.go": {},
		},
		SuccessfulDeletions: 2,
		TotalFiles:          2,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("deleteFiles mismatch (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(root, "stale.go")); !errors.Is(err, os.ErrNotExist) {
		t.Error("deleteFiles left the file on disk")
	}
}

func TestDeleteFilesReportsAFileItCannotDelete(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "keep/child.go", "package child\n")
	ctx := newContext(t, root)

	got, err := deleteFiles(ctx, deleteFilesArgs{FilePaths: []string{"keep"}})
	if err != nil {
		t.Fatalf("deleteFiles returned error: %v", err)
	}
	if got.Success {
		t.Error("deleteFiles reported success although the directory is not empty")
	}
	if entry := got.Files["keep"]; entry.Error == "" {
		t.Error("deleteFiles reported no error for the non-empty directory")
	}
	if _, err := os.Stat(filepath.Join(root, "keep/child.go")); err != nil {
		t.Errorf("deleteFiles removed the directory contents: %v", err)
	}
}

func TestDeleteFilesRejectsATraversal(t *testing.T) {
	root := newProject(t)
	outside := writeProjectFile(t, filepath.Dir(root), "secret.txt", "secret\n")
	ctx := newContext(t, root)

	if _, err := deleteFiles(ctx, deleteFilesArgs{FilePaths: []string{"../secret.txt"}}); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("deleteFiles returned %v, want an error matching ErrOutsideRoot", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("deleteFiles removed a file outside the project directory: %v", err)
	}
}

func TestDeleteFilesReportsAMissingRoot(t *testing.T) {
	ctx := newContext(t, filepath.Join(newProject(t), "absent"))

	if _, err := deleteFiles(ctx, deleteFilesArgs{FilePaths: []string{"a.go"}}); err == nil {
		t.Error("deleteFiles on a missing project directory returned no error")
	}
}

func TestCleanupUnusedFilesReportsWithoutDeleting(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "used.go", "package used\n")
	writeProjectFile(t, root, "tools/stale.go", "package tools\n")
	writeProjectFile(t, root, "tools/stale_test.go", "package tools\n")
	writeProjectFile(t, root, "root_agent.yaml", "name: root\n")
	ctx := newContext(t, root)

	got, err := cleanupUnusedFiles(ctx, cleanupUnusedFilesArgs{UsedFiles: []string{"used.go"}})
	if err != nil {
		t.Fatalf("cleanupUnusedFiles returned error: %v", err)
	}
	want := cleanupUnusedFilesResult{UnusedFiles: []string{"tools/stale.go"}, ScannedFiles: 4}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("cleanupUnusedFiles mismatch (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(root, "tools/stale.go")); err != nil {
		t.Errorf("cleanupUnusedFiles deleted the file it only had to report: %v", err)
	}
}

func TestCleanupUnusedFilesHonoursSuppliedPatterns(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", "name: root\n")
	writeProjectFile(t, root, "draft.yaml", "name: draft\n")
	writeProjectFile(t, root, "notes.md", "notes\n")
	ctx := newContext(t, root)

	got, err := cleanupUnusedFiles(ctx, cleanupUnusedFilesArgs{
		UsedFiles:       []string{"root_agent.yaml"},
		FilePatterns:    []string{"*.yaml", "*.md"},
		ExcludePatterns: []string{"notes.*"},
	})
	if err != nil {
		t.Fatalf("cleanupUnusedFiles returned error: %v", err)
	}
	want := cleanupUnusedFilesResult{UnusedFiles: []string{"draft.yaml"}, ScannedFiles: 3}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("cleanupUnusedFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestCleanupUnusedFilesIgnoresAnInvalidPattern(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "tool.go", "package tool\n")
	ctx := newContext(t, root)

	got, err := cleanupUnusedFiles(ctx, cleanupUnusedFilesArgs{FilePatterns: []string{"[", "*.go"}})
	if err != nil {
		t.Fatalf("cleanupUnusedFiles returned error: %v", err)
	}
	want := cleanupUnusedFilesResult{UnusedFiles: []string{"tool.go"}, ScannedFiles: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("cleanupUnusedFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestCleanupUnusedFilesRejectsATraversal(t *testing.T) {
	ctx := newContext(t, newProject(t))

	_, err := cleanupUnusedFiles(ctx, cleanupUnusedFilesArgs{UsedFiles: []string{"../outside.go"}})
	if !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("cleanupUnusedFiles returned %v, want an error matching ErrOutsideRoot", err)
	}
}

func TestCleanupUnusedFilesReportsAMissingRoot(t *testing.T) {
	ctx := newContext(t, filepath.Join(newProject(t), "absent"))

	if _, err := cleanupUnusedFiles(ctx, cleanupUnusedFilesArgs{}); err == nil {
		t.Error("cleanupUnusedFiles on a missing project directory returned no error")
	}
}

// TestToolsBuild pins that every tool's JSON schema can be inferred from its
// argument and result types. A tool whose schema cannot be built is rejected at
// construction, which would otherwise only surface when New is called.
func TestToolsBuild(t *testing.T) {
	builders := map[string]func() (tool.Tool, error){
		"read_files":           newReadFilesTool,
		"write_files":          newWriteFilesTool,
		"delete_files":         newDeleteFilesTool,
		"cleanup_unused_files": newCleanupUnusedFilesTool,
		"read_config_files":    newReadConfigFilesTool,
		"write_config_files":   newWriteConfigFilesTool,
		"explore_project":      newExploreProjectTool,
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			built, err := build()
			if err != nil {
				t.Fatalf("%s returned error: %v", name, err)
			}
			if built.Name() != name {
				t.Errorf("tool name = %q, want %q", built.Name(), name)
			}
			if built.Description() == "" {
				t.Error("tool description is empty")
			}
		})
	}
}

func TestWriteFilesReportsAPathItCannotCreate(t *testing.T) {
	root := newProject(t)
	// "link" points at a directory that is not there, so the write sees no
	// file to replace but cannot create the parent either.
	if err := os.Symlink("missing_dir", filepath.Join(root, "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	ctx := newContext(t, root)

	got, err := writeFiles(ctx, writeFilesArgs{Files: map[string]string{
		"link/child.go": "package child\n",
		"adir":          "package adir\n",
	}})
	if err != nil {
		t.Fatalf("writeFiles returned error: %v", err)
	}
	if got.Success || got.SuccessfulWrites != 0 {
		t.Fatalf("writeFiles = %+v, want both writes to fail", got)
	}
	for _, path := range []string{"link/child.go", "adir"} {
		if got.Files[path].Error == "" {
			t.Errorf("writeFiles reported no error for %q", path)
		}
	}
}

func TestDeleteFilesReportsAPathBelowAFile(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "blocked", "occupied\n")
	ctx := newContext(t, root)

	got, err := deleteFiles(ctx, deleteFilesArgs{FilePaths: []string{"blocked/child.go"}})
	if err != nil {
		t.Fatalf("deleteFiles returned error: %v", err)
	}
	if got.Success {
		t.Error("deleteFiles reported success although the path has no directory")
	}
	if got.Files["blocked/child.go"].Error == "" {
		t.Error("deleteFiles reported no error for the path below a file")
	}
}

func TestCleanupUnusedFilesReportsAnUnreadableDirectory(t *testing.T) {
	skipWhenRoot(t)
	root := newProject(t)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ctx := newContext(t, root)

	if _, err := cleanupUnusedFiles(ctx, cleanupUnusedFilesArgs{}); err == nil {
		t.Error("cleanupUnusedFiles over an unreadable directory returned no error")
	}
}

// skipWhenRoot skips a test that relies on directory permissions, which the
// superuser ignores.
func skipWhenRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("the superuser is not stopped by directory permissions")
	}
}
