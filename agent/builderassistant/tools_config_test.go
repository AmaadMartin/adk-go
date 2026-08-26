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
)

const rootAgentYAML = `agent_class: LlmAgent
name: root_agent
description: coordinates the pipeline
model: gemini-2.5-pro
instruction: Answer the user.
sub_agents:
  - config_path: research_agent.yaml
  - config_path: absent_agent.yaml
  - code: mypkg.Agent
tools:
  - name: google_search
  - name: AgentTool
    args:
      agent:
        config_path: research_agent.yaml
  - name: not_registered
`

func TestReadConfigFiles(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", rootAgentYAML)
	writeProjectFile(t, root, "research_agent.yaml", "name: research_agent\n")
	ctx := newContext(t, root)

	got, err := readConfigFiles(ctx, readConfigFilesArgs{FilePaths: []string{"root_agent.yaml"}})
	if err != nil {
		t.Fatalf("readConfigFiles returned error: %v", err)
	}
	if !got.Success || got.SuccessfulReads != 1 || got.TotalFiles != 1 {
		t.Fatalf("readConfigFiles = %+v, want one successful read", got)
	}
	analysis := got.Files["root_agent.yaml"]
	wantAgent := &agentSummary{
		Name:              "root_agent",
		AgentClass:        "LlmAgent",
		Description:       "coordinates the pipeline",
		Model:             "gemini-2.5-pro",
		HasInstruction:    true,
		InstructionLength: len("Answer the user."),
	}
	if diff := cmp.Diff(wantAgent, analysis.Agent); diff != "" {
		t.Errorf("agent summary mismatch (-want +got):\n%s", diff)
	}
	wantSubAgents := []subAgentRef{
		{ConfigPath: "research_agent.yaml", Exists: true},
		{ConfigPath: "absent_agent.yaml", Exists: false},
		{Code: "mypkg.Agent", Exists: false},
	}
	if diff := cmp.Diff(wantSubAgents, analysis.SubAgents); diff != "" {
		t.Errorf("sub-agent references mismatch (-want +got):\n%s", diff)
	}
	wantTools := []toolRef{
		{Name: "google_search", Registered: true},
		{Name: "AgentTool", HasArgs: true, Registered: true},
		{Name: "not_registered"},
	}
	if diff := cmp.Diff(wantTools, analysis.Tools); diff != "" {
		t.Errorf("tool references mismatch (-want +got):\n%s", diff)
	}
	if analysis.Content["name"] != "root_agent" {
		t.Errorf("parsed content name = %v, want root_agent", analysis.Content["name"])
	}
	if analysis.LineCount != strings.Count(rootAgentYAML, "\n")+1 {
		t.Errorf("line count = %d, want %d", analysis.LineCount, strings.Count(rootAgentYAML, "\n")+1)
	}
	if analysis.FilePath != filepath.Join(root, "root_agent.yaml") {
		t.Errorf("file path = %q, want the absolute config path", analysis.FilePath)
	}
}

func TestReadConfigFilesReportsUnusableConfigs(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "broken.yaml", "name: [unclosed\n")
	writeProjectFile(t, root, "empty.yaml", "")
	writeProjectFile(t, root, "scalar.yaml", "just a string\n")
	writeProjectFile(t, root, "notes.md", "# notes\n")
	ctx := newContext(t, root)

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "invalid syntax", path: "broken.yaml", wantErr: "invalid YAML syntax"},
		{name: "empty file", path: "empty.yaml", wantErr: "the config is empty"},
		{name: "not a mapping", path: "scalar.yaml", wantErr: "cannot unmarshal"},
		{name: "not a YAML file", path: "notes.md", wantErr: "not a YAML file"},
		{name: "missing file", path: "absent.yaml", wantErr: "file does not exist"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readConfigFiles(ctx, readConfigFilesArgs{FilePaths: []string{test.path}})
			if err != nil {
				t.Fatalf("readConfigFiles returned error: %v", err)
			}
			if got.Success || got.SuccessfulReads != 0 {
				t.Fatalf("readConfigFiles = %+v, want a failed read", got)
			}
			if message := got.Files[test.path].Error; !strings.Contains(message, test.wantErr) {
				t.Errorf("error = %q, want it to contain %q", message, test.wantErr)
			}
		})
	}
}

func TestReadConfigFilesRejectsATraversal(t *testing.T) {
	ctx := newContext(t, newProject(t))

	_, err := readConfigFiles(ctx, readConfigFilesArgs{FilePaths: []string{"../root_agent.yaml"}})
	if !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("readConfigFiles returned %v, want an error matching ErrOutsideRoot", err)
	}
}

func TestReadConfigFilesReportsAMissingRoot(t *testing.T) {
	ctx := newContext(t, filepath.Join(newProject(t), "absent"))

	if _, err := readConfigFiles(ctx, readConfigFilesArgs{FilePaths: []string{"a.yaml"}}); err == nil {
		t.Error("readConfigFiles on a missing project directory returned no error")
	}
}

func TestWriteConfigFiles(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", "name: old\n")
	ctx := newContext(t, root)

	pipeline := "agent_class: SequentialAgent\nname: pipeline\n"
	got, err := writeConfigFiles(ctx, writeConfigFilesArgs{Files: map[string]string{
		"root_agent.yaml":      "name: root_agent\nmodel: gemini-2.5-pro\n",
		"agents/pipeline.yaml": pipeline,
	}})
	if err != nil {
		t.Fatalf("writeConfigFiles returned error: %v", err)
	}
	want := writeConfigFilesResult{
		Success: true,
		Files: map[string]fileWrite{
			"root_agent.yaml":      {FileSize: 39, ExistedBefore: true},
			"agents/pipeline.yaml": {FileSize: int64(len(pipeline))},
		},
		SuccessfulWrites: 2,
		TotalFiles:       2,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("writeConfigFiles mismatch (-want +got):\n%s", diff)
	}
	content, err := os.ReadFile(filepath.Join(root, "agents/pipeline.yaml"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != pipeline {
		t.Errorf("pipeline.yaml holds %q, want %q", content, pipeline)
	}
}

func TestWriteConfigFilesRejectsConfigsThatCannotLoad(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content string
		wantErr string
	}{
		{
			name:    "unknown agent class",
			path:    "bad_class.yaml",
			content: "agent_class: MagicAgent\nname: bad\n",
			wantErr: `unknown agent_class "MagicAgent"`,
		},
		{
			name:    "no name",
			path:    "no_name.yaml",
			content: "agent_class: LlmAgent\n",
			wantErr: "has no name",
		},
		{
			name:    "blank name",
			path:    "blank_name.yaml",
			content: "name: '   '\n",
			wantErr: "has no name",
		},
		{
			name:    "invalid syntax",
			path:    "broken.yaml",
			content: "name: [unclosed\n",
			wantErr: "invalid YAML syntax",
		},
		{
			name:    "empty config",
			path:    "empty.yaml",
			content: "",
			wantErr: "the config is empty",
		},
		{
			name:    "not a YAML file",
			path:    "agent.txt",
			content: "name: root\n",
			wantErr: "not a YAML file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newProject(t)
			ctx := newContext(t, root)

			got, err := writeConfigFiles(ctx, writeConfigFilesArgs{Files: map[string]string{test.path: test.content}})
			if err != nil {
				t.Fatalf("writeConfigFiles returned error: %v", err)
			}
			if got.Success || got.SuccessfulWrites != 0 {
				t.Fatalf("writeConfigFiles = %+v, want a rejected config", got)
			}
			if message := got.Files[test.path].Error; !strings.Contains(message, test.wantErr) {
				t.Errorf("error = %q, want it to contain %q", message, test.wantErr)
			}
			if _, err := os.Stat(filepath.Join(root, test.path)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("writeConfigFiles wrote %s although the config was rejected", test.path)
			}
		})
	}
}

func TestWriteConfigFilesAcceptsEveryAgentClass(t *testing.T) {
	root := newProject(t)
	ctx := newContext(t, root)

	files := map[string]string{"default_class.yaml": "name: implicit\n"}
	for _, class := range agentClasses {
		files[strings.ToLower(class)+".yaml"] = "agent_class: " + class + "\nname: " + strings.ToLower(class) + "\n"
	}

	got, err := writeConfigFiles(ctx, writeConfigFilesArgs{Files: files})
	if err != nil {
		t.Fatalf("writeConfigFiles returned error: %v", err)
	}
	if !got.Success || got.SuccessfulWrites != len(files) {
		t.Fatalf("writeConfigFiles = %+v, want every config written", got)
	}
}

func TestWriteConfigFilesReportsAFileItCannotWrite(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "blocked", "occupied\n")
	ctx := newContext(t, root)

	got, err := writeConfigFiles(ctx, writeConfigFilesArgs{Files: map[string]string{
		"blocked/child.yaml": "name: child\n",
	}})
	if err != nil {
		t.Fatalf("writeConfigFiles returned error: %v", err)
	}
	if got.Success {
		t.Error("writeConfigFiles reported success although the write failed")
	}
	if message := got.Files["blocked/child.yaml"].Error; !strings.Contains(message, "failed to write") {
		t.Errorf("error = %q, want it to contain \"failed to write\"", message)
	}
}

func TestWriteConfigFilesRejectsATraversal(t *testing.T) {
	root := newProject(t)
	ctx := newContext(t, root)

	_, err := writeConfigFiles(ctx, writeConfigFilesArgs{Files: map[string]string{"../escape.yaml": "name: escape\n"}})
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("writeConfigFiles returned %v, want an error matching ErrOutsideRoot", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Error("writeConfigFiles created a config outside the project directory")
	}
}

func TestWriteConfigFilesReportsAnUnusableRoot(t *testing.T) {
	blocked := writeProjectFile(t, newProject(t), "occupied", "not a directory\n")
	ctx := newContext(t, blocked)

	_, err := writeConfigFiles(ctx, writeConfigFilesArgs{Files: map[string]string{"a.yaml": "name: a\n"}})
	if err == nil {
		t.Error("writeConfigFiles with a file as the project directory returned no error")
	}
}

func TestSummarizeSubAgentsSkipsEntriesThatAreNotMappings(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", "name: root\nsub_agents:\n  - plain_string\n  - config_path: a.yaml\ntools: not_a_list\n")
	ctx := newContext(t, root)

	got, err := readConfigFiles(ctx, readConfigFilesArgs{FilePaths: []string{"root_agent.yaml"}})
	if err != nil {
		t.Fatalf("readConfigFiles returned error: %v", err)
	}
	analysis := got.Files["root_agent.yaml"]
	if diff := cmp.Diff([]subAgentRef{{ConfigPath: "a.yaml"}}, analysis.SubAgents); diff != "" {
		t.Errorf("sub-agent references mismatch (-want +got):\n%s", diff)
	}
	if analysis.Tools != nil {
		t.Errorf("tools = %v, want nil for a tools field that is not a list", analysis.Tools)
	}
}

func TestSummarizeToolsSkipsEntriesThatAreNotMappings(t *testing.T) {
	root := newProject(t)
	writeProjectFile(t, root, "root_agent.yaml", "name: root\ntools:\n  - google_search\n  - name: exit_loop\n")
	ctx := newContext(t, root)

	got, err := readConfigFiles(ctx, readConfigFilesArgs{FilePaths: []string{"root_agent.yaml"}})
	if err != nil {
		t.Fatalf("readConfigFiles returned error: %v", err)
	}
	want := []toolRef{{Name: "exit_loop", Registered: true}}
	if diff := cmp.Diff(want, got.Files["root_agent.yaml"].Tools); diff != "" {
		t.Errorf("tool references mismatch (-want +got):\n%s", diff)
	}
}

func TestIsYAMLPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "root_agent.yaml", want: true},
		{path: "root_agent.yml", want: true},
		{path: "ROOT_AGENT.YAML", want: true},
		{path: "agents/nested.yaml", want: true},
		{path: "notes.md", want: false},
		{path: "yaml", want: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := isYAMLPath(test.path); got != test.want {
				t.Errorf("isYAMLPath(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}
