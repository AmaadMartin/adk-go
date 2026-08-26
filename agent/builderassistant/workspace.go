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
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// ErrOutsideRoot reports a path that resolves outside the sandbox root. The
// file tools return it instead of clamping the path, so a traversal attempt
// fails loudly rather than reading or writing somewhere unexpected.
var ErrOutsideRoot = errors.New("path resolves outside the root directory")

// RootDirectoryStateKey is the session state key holding the directory the
// assistant may read and write. When the key is absent the sandbox root is the
// process working directory.
const RootDirectoryStateKey = "root_directory"

// pathBoundaryChars are stripped from both ends of every path segment. The
// model occasionally emits a quoted path such as 'tools/web.yaml', which would
// otherwise create a directory literally named 'tools.
const pathBoundaryChars = " \t\r\n'\"`"

// dirPerm is the mode for directories the write tools create.
const dirPerm fs.FileMode = 0o755

// filePerm is the mode for files the write tools create.
const filePerm fs.FileMode = 0o644

// workspace is a sandboxed view of one directory. Every read and write goes
// through the [os.Root] handle, which refuses any name that leaves the
// directory, including through a symlink.
type workspace struct {
	root *os.Root
	// path is the absolute, symlink-free location of root. It is reported to
	// the model and used to interpret absolute paths the model supplies.
	path string
}

// openWorkspace opens the sandbox recorded in the invocation's session state.
// The directory must already exist.
func openWorkspace(ctx agent.ReadonlyContext) (*workspace, error) {
	dir, err := rootDirectory(ctx.ReadonlyState())
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve the root directory %q: %w", dir, err)
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, fmt.Errorf("open the root directory %q: %w", real, err)
	}
	return &workspace{root: root, path: real}, nil
}

// createWorkspace opens the sandbox, creating the root directory when it does
// not exist yet. The write tools use it so that a project can be built in a
// directory the user has only named.
func createWorkspace(ctx agent.ReadonlyContext) (*workspace, error) {
	dir, err := rootDirectory(ctx.ReadonlyState())
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create the root directory %q: %w", dir, err)
	}
	return openWorkspace(ctx)
}

// Close releases the directory handle. It reports nothing: every write is
// already flushed when it returns, so a caller has no decision to make.
func (w *workspace) Close() {
	_ = w.root.Close()
}

// resolve turns a path supplied by the model into a name relative to the
// sandbox root. It reports [ErrOutsideRoot] for a path that escapes the root
// through "..", through an absolute location elsewhere, or through a symlink.
//
// An absolute path must be spelled with the root's own symlink-free prefix;
// reaching the root by another name is rejected rather than resolved.
func (w *workspace) resolve(name string) (string, error) {
	clean := filepath.Clean(sanitizePath(name))
	// An absolute path inside the sandbox is allowed, but os.Root takes only
	// relative names, so strip the root prefix. The separator belongs to the
	// prefix: "/srv/rootless" starts with "/srv/root" yet is another
	// directory.
	if inside, ok := strings.CutPrefix(clean, w.path+string(filepath.Separator)); ok {
		clean = inside
	} else if clean == w.path {
		clean = "."
	}
	// Stat walks the name through the root handle, so a name that leaves the
	// sandbox is refused here rather than at the first read or write.
	if _, err := w.root.Stat(clean); isEscape(err) {
		return "", fmt.Errorf("%w: %q: %w", ErrOutsideRoot, name, err)
	}
	return clean, nil
}

// isEscape reports whether err is [os.Root] refusing to leave the sandbox.
//
// The standard library does not export that error, so it is identified by
// elimination: every other failure the sandbox reports comes from the operating
// system and carries a [syscall.Errno], while the refusal is a plain error
// value. Treating an unrecognised error as an escape fails closed.
func isEscape(err error) bool {
	if err == nil {
		return false
	}
	var errno syscall.Errno
	return !errors.As(err, &errno)
}

// resolveAll resolves every path, stopping at the first one that escapes.
func (w *workspace) resolveAll(names []string) ([]string, error) {
	resolved := make([]string, 0, len(names))
	for _, name := range names {
		rel, err := w.resolve(name)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, rel)
	}
	return resolved, nil
}

// abs returns the absolute location of a name already resolved by resolve.
func (w *workspace) abs(rel string) string {
	return filepath.Join(w.path, rel)
}

// rootDirectory returns the absolute sandbox root recorded in state. A missing
// key, a non-string value and an empty string all select the process working
// directory, which is what an assistant started in a project folder expects.
func rootDirectory(state session.ReadonlyState) (string, error) {
	dir := "."
	if state != nil {
		if value, err := state.Get(RootDirectoryStateKey); err == nil {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				dir = strings.TrimSpace(text)
			}
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve the root directory %q: %w", dir, err)
	}
	return abs, nil
}

// sanitizePath strips whitespace and quote characters from both ends of the
// path and of each of its segments.
func sanitizePath(path string) string {
	segments := strings.Split(strings.Trim(path, pathBoundaryChars), "/")
	for i, segment := range segments {
		segments[i] = strings.Trim(segment, pathBoundaryChars)
	}
	return strings.Join(segments, "/")
}
