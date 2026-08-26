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
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"sort"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// defaultCleanupPatterns matches the files the assistant itself writes as Go
// tool sources, which are the ones that go stale when a design changes.
var defaultCleanupPatterns = []string{"*.go"}

// defaultCleanupExcludes keeps tests out of the unused-file report.
var defaultCleanupExcludes = []string{"*_test.go"}

type readFilesArgs struct {
	FilePaths []string `json:"file_paths" jsonschema:"paths of the files to read, relative to the project directory"`
}

// fileRead is the outcome of reading one file.
type fileRead struct {
	Content  string `json:"content"`
	FileSize int64  `json:"file_size"`
	Exists   bool   `json:"exists"`
	Error    string `json:"error,omitempty"`
}

type readFilesResult struct {
	Success         bool                `json:"success"`
	Files           map[string]fileRead `json:"files"`
	SuccessfulReads int                 `json:"successful_reads"`
	TotalFiles      int                 `json:"total_files"`
}

// newReadFilesTool reads several files in one call.
func newReadFilesTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "read_files",
		Description: "Read several files from the project directory. A file that " +
			"cannot be read is reported in its own entry, so the other files " +
			"are still returned.",
	}, readFiles)
}

func readFiles(ctx agent.Context, args readFilesArgs) (readFilesResult, error) {
	w, err := openWorkspace(ctx)
	if err != nil {
		return readFilesResult{}, err
	}
	defer func() { _ = w.Close() }()

	result := readFilesResult{
		Success:    true,
		Files:      make(map[string]fileRead, len(args.FilePaths)),
		TotalFiles: len(args.FilePaths),
	}
	for _, requested := range args.FilePaths {
		rel, err := w.resolve(requested)
		if err != nil {
			return readFilesResult{}, err
		}
		content, readErr := w.root.ReadFile(rel)
		if readErr != nil {
			result.Files[rel] = fileRead{Error: describeFileError("read", w.abs(rel), readErr)}
			result.Success = false
			continue
		}
		result.Files[rel] = fileRead{
			Content:  string(content),
			FileSize: int64(len(content)),
			Exists:   true,
		}
		result.SuccessfulReads++
	}
	return result, nil
}

type writeFilesArgs struct {
	Files map[string]string `json:"files" jsonschema:"maps each file path, relative to the project directory, to the content to write"`
}

// fileWrite is the outcome of writing one file.
type fileWrite struct {
	FileSize      int64  `json:"file_size"`
	ExistedBefore bool   `json:"existed_before"`
	Error         string `json:"error,omitempty"`
}

type writeFilesResult struct {
	Success          bool                 `json:"success"`
	Files            map[string]fileWrite `json:"files"`
	SuccessfulWrites int                  `json:"successful_writes"`
	TotalFiles       int                  `json:"total_files"`
}

// newWriteFilesTool writes several files in one call, creating any missing
// parent directory.
func newWriteFilesTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "write_files",
		Description: "Write several files into the project directory, creating " +
			"any missing parent directory. Use write_config_files for agent " +
			"YAML configs instead, because that tool validates them.",
	}, writeFiles)
}

func writeFiles(ctx agent.Context, args writeFilesArgs) (writeFilesResult, error) {
	w, err := createWorkspace(ctx)
	if err != nil {
		return writeFilesResult{}, err
	}
	defer func() { _ = w.Close() }()

	result := writeFilesResult{
		Success:    true,
		Files:      make(map[string]fileWrite, len(args.Files)),
		TotalFiles: len(args.Files),
	}
	for _, requested := range sortedKeys(args.Files) {
		rel, err := w.resolve(requested)
		if err != nil {
			return writeFilesResult{}, err
		}
		written, writeErr := w.write(rel, args.Files[requested])
		if writeErr != nil {
			written.Error = describeFileError("write", w.abs(rel), writeErr)
			result.Success = false
		} else {
			result.SuccessfulWrites++
		}
		result.Files[rel] = written
	}
	return result, nil
}

// write creates rel inside the sandbox and reports what it found there first.
func (w *workspace) write(rel, content string) (fileWrite, error) {
	written := fileWrite{}
	if _, err := w.root.Stat(rel); err == nil {
		written.ExistedBefore = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return written, err
	}
	if parent := filepath.Dir(rel); parent != "." {
		if err := w.root.MkdirAll(parent, dirPerm); err != nil {
			return written, err
		}
	}
	if err := w.root.WriteFile(rel, []byte(content), filePerm); err != nil {
		return written, err
	}
	written.FileSize = int64(len(content))
	return written, nil
}

type deleteFilesArgs struct {
	FilePaths []string `json:"file_paths" jsonschema:"paths of the files to delete, relative to the project directory"`
}

// fileDelete is the outcome of deleting one file.
type fileDelete struct {
	Existed  bool   `json:"existed"`
	FileSize int64  `json:"file_size"`
	Error    string `json:"error,omitempty"`
}

type deleteFilesResult struct {
	Success             bool                  `json:"success"`
	Files               map[string]fileDelete `json:"files"`
	SuccessfulDeletions int                   `json:"successful_deletions"`
	TotalFiles          int                   `json:"total_files"`
}

// newDeleteFilesTool deletes several files in one call.
func newDeleteFilesTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "delete_files",
		Description: "Delete several files from the project directory. Deleting " +
			"a file that is already gone succeeds.",
	}, deleteFiles)
}

func deleteFiles(ctx agent.Context, args deleteFilesArgs) (deleteFilesResult, error) {
	w, err := openWorkspace(ctx)
	if err != nil {
		return deleteFilesResult{}, err
	}
	defer func() { _ = w.Close() }()

	result := deleteFilesResult{
		Success:    true,
		Files:      make(map[string]fileDelete, len(args.FilePaths)),
		TotalFiles: len(args.FilePaths),
	}
	for _, requested := range args.FilePaths {
		rel, err := w.resolve(requested)
		if err != nil {
			return deleteFilesResult{}, err
		}
		deleted, deleteErr := w.delete(rel)
		if deleteErr != nil {
			deleted.Error = describeFileError("delete", w.abs(rel), deleteErr)
			result.Success = false
		} else {
			result.SuccessfulDeletions++
		}
		result.Files[rel] = deleted
	}
	return result, nil
}

// delete removes rel from the sandbox. A file that is already gone is not an
// error: the assistant deletes files it believes are obsolete, and the goal is
// their absence.
func (w *workspace) delete(rel string) (fileDelete, error) {
	deleted := fileDelete{}
	info, err := w.root.Stat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return deleted, nil
	}
	if err != nil {
		return deleted, err
	}
	deleted.Existed = true
	deleted.FileSize = info.Size()
	if err := w.root.Remove(rel); err != nil {
		return fileDelete{}, err
	}
	return deleted, nil
}

type cleanupUnusedFilesArgs struct {
	UsedFiles       []string `json:"used_files" jsonschema:"paths that are still referenced and must not be reported"`
	FilePatterns    []string `json:"file_patterns,omitempty" jsonschema:"file name patterns to scan; defaults to *.go"`
	ExcludePatterns []string `json:"exclude_patterns,omitempty" jsonschema:"file name patterns to skip; defaults to *_test.go"`
}

type cleanupUnusedFilesResult struct {
	UnusedFiles  []string `json:"unused_files"`
	ScannedFiles int      `json:"scanned_files"`
}

// newCleanupUnusedFilesTool reports files no longer referenced by the design.
// It never deletes anything; delete_files does that, after the user agrees.
func newCleanupUnusedFilesTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "cleanup_unused_files",
		Description: "List the files in the project directory that are no longer " +
			"referenced. This tool only reports; call delete_files once the " +
			"user has confirmed the list.",
	}, cleanupUnusedFiles)
}

func cleanupUnusedFiles(ctx agent.Context, args cleanupUnusedFilesArgs) (cleanupUnusedFilesResult, error) {
	w, err := openWorkspace(ctx)
	if err != nil {
		return cleanupUnusedFilesResult{}, err
	}
	defer func() { _ = w.Close() }()

	used, err := w.resolveAll(args.UsedFiles)
	if err != nil {
		return cleanupUnusedFilesResult{}, err
	}
	include := args.FilePatterns
	if len(include) == 0 {
		include = defaultCleanupPatterns
	}
	exclude := args.ExcludePatterns
	if len(exclude) == 0 {
		exclude = defaultCleanupExcludes
	}

	result := cleanupUnusedFilesResult{UnusedFiles: []string{}}
	walkErr := fs.WalkDir(w.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		result.ScannedFiles++
		if !matchesAny(include, entry.Name()) || matchesAny(exclude, entry.Name()) {
			return nil
		}
		if !slices.Contains(used, filepath.FromSlash(name)) {
			result.UnusedFiles = append(result.UnusedFiles, name)
		}
		return nil
	})
	if walkErr != nil {
		return cleanupUnusedFilesResult{}, fmt.Errorf("scan the project directory %q: %w", w.path, walkErr)
	}
	sort.Strings(result.UnusedFiles)
	return result, nil
}

// matchesAny reports whether name matches one of the shell patterns. A pattern
// the standard library rejects simply matches nothing, which keeps a typo in a
// model-supplied pattern from failing the whole scan.
func matchesAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if ok, err := path.Match(pattern, name); ok && err == nil {
			return true
		}
	}
	return false
}

// describeFileError renders a per-file failure for the model. It names the
// absolute path so the model can tell two same-named files apart.
func describeFileError(action, absPath string, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("file does not exist: %s", absPath)
	}
	return fmt.Sprintf("failed to %s %s: %v", action, absPath, err)
}

// sortedKeys returns the keys of m in a fixed order, so that a batch write
// behaves the same way on every run.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
