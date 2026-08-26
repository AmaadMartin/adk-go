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
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// maxTreeDepth bounds how deep explore_project descends. Three levels covers
// the layout ADK projects use and keeps a large checkout out of the prompt.
const maxTreeDepth = 3

// skippedDirs are never descended into, because nothing the assistant writes
// lives there and they are large.
var skippedDirs = []string{"node_modules", "testdata", "vendor"}

// exploreProjectArgs carries no parameters: the directory to explore is the
// session's root directory, not something the model chooses.
type exploreProjectArgs struct{}

// projectInfo is the shape of the project directory at a glance.
type projectInfo struct {
	Name             string `json:"name"`
	AbsolutePath     string `json:"absolute_path"`
	TotalFiles       int    `json:"total_files"`
	TotalDirectories int    `json:"total_directories"`
	HasGoFiles       bool   `json:"has_go_files"`
	HasYAMLFiles     bool   `json:"has_yaml_files"`
}

// configSummary describes one YAML config found in the project directory.
type configSummary struct {
	Filename     string `json:"filename"`
	RelativePath string `json:"relative_path"`
	Size         int64  `json:"size"`
	IsValidYAML  bool   `json:"is_valid_yaml"`
	AgentName    string `json:"agent_name,omitempty"`
	AgentClass   string `json:"agent_class,omitempty"`
	HasSubAgents bool   `json:"has_sub_agents"`
	HasTools     bool   `json:"has_tools"`
}

type exploreProjectResult struct {
	Project projectInfo `json:"project_info"`
	// ExistingConfigs covers the YAML configs directly in the project
	// directory, which is where ADK looks for root_agent.yaml.
	ExistingConfigs []configSummary `json:"existing_configs"`
	// Entries lists the project tree to maxTreeDepth, as paths relative to
	// the project directory. A directory ends with a slash.
	Entries []string `json:"entries"`
}

// newExploreProjectTool reports the layout of the session's project directory.
func newExploreProjectTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "explore_project",
		Description: "Report the layout of the project directory and the ADK " +
			"agent configs already in it. Takes no arguments: the directory " +
			"comes from the session.",
	}, exploreProject)
}

func exploreProject(ctx agent.Context, _ exploreProjectArgs) (exploreProjectResult, error) {
	w, err := openWorkspace(ctx)
	if err != nil {
		return exploreProjectResult{}, err
	}
	defer w.Close()

	result := exploreProjectResult{
		Project:         projectInfo{Name: filepath.Base(w.path), AbsolutePath: w.path},
		ExistingConfigs: []configSummary{},
		Entries:         []string{},
	}
	tree := w.root.FS()
	walkErr := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") || slices.Contains(skippedDirs, entry.Name()) {
				return fs.SkipDir
			}
			result.Project.TotalDirectories++
			if depthOf(name) < maxTreeDepth {
				result.Entries = append(result.Entries, name+"/")
				return nil
			}
			return fs.SkipDir
		}
		result.Project.TotalFiles++
		switch strings.ToLower(path.Ext(name)) {
		case ".go":
			result.Project.HasGoFiles = true
		case ".yaml", ".yml":
			result.Project.HasYAMLFiles = true
			if !strings.Contains(name, "/") {
				result.ExistingConfigs = append(result.ExistingConfigs, w.summarizeConfig(name))
			}
		}
		if depthOf(name) <= maxTreeDepth {
			result.Entries = append(result.Entries, name)
		}
		return nil
	})
	if walkErr != nil {
		return exploreProjectResult{}, fmt.Errorf("explore the project directory %q: %w", w.path, walkErr)
	}
	slices.Sort(result.Entries)
	slices.SortFunc(result.ExistingConfigs, func(a, b configSummary) int {
		return strings.Compare(a.Filename, b.Filename)
	})
	return result, nil
}

// summarizeConfig describes one config file. A file that does not parse is
// reported with IsValidYAML false rather than failing the exploration, because
// a half-written config is exactly what the assistant may need to repair.
func (w *workspace) summarizeConfig(rel string) configSummary {
	summary := configSummary{Filename: path.Base(rel), RelativePath: rel}
	raw, err := w.root.ReadFile(rel)
	if err != nil {
		return summary
	}
	summary.Size = int64(len(raw))
	content, err := parseAgentConfig(raw)
	if err != nil {
		return summary
	}
	summary.IsValidYAML = true
	summary.AgentName, _ = content["name"].(string)
	summary.AgentClass = agentClassOf(content)
	_, summary.HasSubAgents = content["sub_agents"]
	_, summary.HasTools = content["tools"]
	return summary
}

// depthOf counts the path segments of a slash-separated name.
func depthOf(name string) int {
	return strings.Count(name, "/") + 1
}
