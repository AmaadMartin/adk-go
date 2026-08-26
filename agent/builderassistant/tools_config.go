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
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// defaultAgentClass is what ADK's config loader assumes when a config omits
// agent_class.
const defaultAgentClass = "LlmAgent"

// agentClasses are the agent_class values adk-go's config loader accepts. They
// mirror the classes registered in internal/configurable; a config naming
// anything else fails to load.
var agentClasses = []string{
	"LlmAgent",
	"LoopAgent",
	"ParallelAgent",
	"SequentialAgent",
	"Workflow",
}

// configToolNames are the tool names adk-go's config loader resolves. A tools
// entry naming anything else fails to load.
var configToolNames = []string{
	"exit_loop",
	"google_search",
	"url_context",
	"google_maps_grounding",
	"AgentTool",
	"LongRunningFunctionTool",
	"ExampleTool",
	"McpToolset",
}

type readConfigFilesArgs struct {
	FilePaths []string `json:"file_paths" jsonschema:"paths of the YAML agent configs to read, relative to the project directory"`
}

// agentSummary is the part of a config a designer needs at a glance.
type agentSummary struct {
	Name              string `json:"name"`
	AgentClass        string `json:"agent_class"`
	Description       string `json:"description,omitempty"`
	Model             string `json:"model,omitempty"`
	HasInstruction    bool   `json:"has_instruction"`
	InstructionLength int    `json:"instruction_length"`
}

// subAgentRef is one entry of a config's sub_agents list.
type subAgentRef struct {
	ConfigPath string `json:"config_path,omitempty"`
	Code       string `json:"code,omitempty"`
	// Exists reports whether ConfigPath names a file that is present. It is
	// resolved against the directory of the referring config, which is how
	// ADK's loader resolves it.
	Exists bool `json:"exists"`
}

// toolRef is one entry of a config's tools list.
type toolRef struct {
	Name    string `json:"name"`
	HasArgs bool   `json:"has_args"`
	// Registered reports whether ADK's config loader knows this tool name.
	Registered bool `json:"registered"`
}

// configFile is the analysis of one YAML agent config.
type configFile struct {
	Success   bool           `json:"success"`
	FilePath  string         `json:"file_path"`
	FileSize  int64          `json:"file_size"`
	LineCount int            `json:"line_count"`
	Content   map[string]any `json:"content,omitempty"`
	Agent     *agentSummary  `json:"agent_info,omitempty"`
	SubAgents []subAgentRef  `json:"sub_agents,omitempty"`
	Tools     []toolRef      `json:"tools,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type readConfigFilesResult struct {
	Success         bool                  `json:"success"`
	Files           map[string]configFile `json:"files"`
	SuccessfulReads int                   `json:"successful_reads"`
	TotalFiles      int                   `json:"total_files"`
}

// newReadConfigFilesTool reads and parses several YAML agent configs.
func newReadConfigFilesTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "read_config_files",
		Description: "Read several YAML agent configs and report the agent, its " +
			"sub-agent references and its tools. A config that cannot be " +
			"parsed is reported in its own entry.",
	}, readConfigFiles)
}

func readConfigFiles(ctx agent.Context, args readConfigFilesArgs) (readConfigFilesResult, error) {
	w, err := openWorkspace(ctx)
	if err != nil {
		return readConfigFilesResult{}, err
	}
	defer func() { _ = w.Close() }()

	result := readConfigFilesResult{
		Success:    true,
		Files:      make(map[string]configFile, len(args.FilePaths)),
		TotalFiles: len(args.FilePaths),
	}
	for _, requested := range args.FilePaths {
		rel, err := w.resolve(requested)
		if err != nil {
			return readConfigFilesResult{}, err
		}
		analysis := w.analyzeConfig(rel)
		if analysis.Success {
			result.SuccessfulReads++
		} else {
			result.Success = false
		}
		result.Files[rel] = analysis
	}
	return result, nil
}

// analyzeConfig reads one config and reports either its contents or the reason
// it could not be used.
func (w *workspace) analyzeConfig(rel string) configFile {
	analysis := configFile{FilePath: w.abs(rel)}
	if !isYAMLPath(rel) {
		analysis.Error = fmt.Sprintf("not a YAML file: %s", analysis.FilePath)
		return analysis
	}
	raw, err := w.root.ReadFile(rel)
	if err != nil {
		analysis.Error = describeFileError("read", analysis.FilePath, err)
		return analysis
	}
	analysis.FileSize = int64(len(raw))
	analysis.LineCount = strings.Count(string(raw), "\n") + 1

	content, err := parseAgentConfig(raw)
	if err != nil {
		analysis.Error = err.Error()
		return analysis
	}
	analysis.Success = true
	analysis.Content = content
	analysis.Agent = summarizeAgent(content)
	analysis.SubAgents = w.summarizeSubAgents(rel, content)
	analysis.Tools = summarizeTools(content)
	return analysis
}

type writeConfigFilesArgs struct {
	Files map[string]string `json:"files" jsonschema:"maps each YAML agent config path, relative to the project directory, to the config text"`
}

type writeConfigFilesResult struct {
	Success          bool                 `json:"success"`
	Files            map[string]fileWrite `json:"files"`
	SuccessfulWrites int                  `json:"successful_writes"`
	TotalFiles       int                  `json:"total_files"`
}

// newWriteConfigFilesTool validates and writes several YAML agent configs.
func newWriteConfigFilesTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "write_config_files",
		Description: "Write several YAML agent configs into the project " +
			"directory. Each config is checked before it is written, and a " +
			"config that fails the check is reported instead of written.",
	}, writeConfigFiles)
}

func writeConfigFiles(ctx agent.Context, args writeConfigFilesArgs) (writeConfigFilesResult, error) {
	w, err := createWorkspace(ctx)
	if err != nil {
		return writeConfigFilesResult{}, err
	}
	defer func() { _ = w.Close() }()

	result := writeConfigFilesResult{
		Success:    true,
		Files:      make(map[string]fileWrite, len(args.Files)),
		TotalFiles: len(args.Files),
	}
	for _, requested := range sortedKeys(args.Files) {
		rel, err := w.resolve(requested)
		if err != nil {
			return writeConfigFilesResult{}, err
		}
		written, writeErr := w.writeConfig(rel, args.Files[requested])
		if writeErr != nil {
			written.Error = writeErr.Error()
			result.Success = false
		} else {
			result.SuccessfulWrites++
		}
		result.Files[rel] = written
	}
	return result, nil
}

// writeConfig validates content and writes it only if the check passes.
func (w *workspace) writeConfig(rel, content string) (fileWrite, error) {
	if !isYAMLPath(rel) {
		return fileWrite{}, fmt.Errorf("not a YAML file: %s", w.abs(rel))
	}
	if err := validateAgentConfig([]byte(content)); err != nil {
		return fileWrite{}, err
	}
	written, err := w.write(rel, content)
	if err != nil {
		return written, fmt.Errorf("failed to write %s: %w", w.abs(rel), err)
	}
	return written, nil
}

// parseAgentConfig decodes an agent config into its YAML mapping.
func parseAgentConfig(raw []byte) (map[string]any, error) {
	var content map[string]any
	if err := yaml.Unmarshal(raw, &content); err != nil {
		return nil, fmt.Errorf("invalid YAML syntax: %w", err)
	}
	if content == nil {
		return nil, errors.New("the config is empty; an agent config must be a YAML mapping")
	}
	return content, nil
}

// validateAgentConfig checks the parts of an agent config that decide whether
// ADK's loader can build an agent from it at all: the config parses, it names
// the agent, and its agent_class is one the loader registers.
//
// simplicity: this is a load-time smoke test, not a schema check. Validating
// every field would need a schema that adk-go does not have; the loader itself
// reports the rest when the config is used.
func validateAgentConfig(raw []byte) error {
	content, err := parseAgentConfig(raw)
	if err != nil {
		return err
	}
	name, _ := content["name"].(string)
	if strings.TrimSpace(name) == "" {
		return errors.New("the config has no name; every agent config needs a name")
	}
	class := agentClassOf(content)
	if !slices.Contains(agentClasses, class) {
		return fmt.Errorf("unknown agent_class %q; ADK loads %s",
			class, strings.Join(agentClasses, ", "))
	}
	return nil
}

// agentClassOf reports the config's agent_class, defaulting as the loader does.
func agentClassOf(content map[string]any) string {
	if class, ok := content["agent_class"].(string); ok && class != "" {
		return class
	}
	return defaultAgentClass
}

// summarizeAgent extracts the fields a designer reviews first.
func summarizeAgent(content map[string]any) *agentSummary {
	name, _ := content["name"].(string)
	description, _ := content["description"].(string)
	model, _ := content["model"].(string)
	instruction, _ := content["instruction"].(string)
	return &agentSummary{
		Name:              name,
		AgentClass:        agentClassOf(content),
		Description:       description,
		Model:             model,
		HasInstruction:    strings.TrimSpace(instruction) != "",
		InstructionLength: len(instruction),
	}
}

// summarizeSubAgents lists the sub_agents entries and reports whether each
// referenced config is actually there.
func (w *workspace) summarizeSubAgents(rel string, content map[string]any) []subAgentRef {
	entries, ok := content["sub_agents"].([]any)
	if !ok {
		return nil
	}
	refs := make([]subAgentRef, 0, len(entries))
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		ref := subAgentRef{}
		ref.ConfigPath, _ = fields["config_path"].(string)
		ref.Code, _ = fields["code"].(string)
		ref.Exists = ref.ConfigPath != "" && w.exists(filepath.Join(filepath.Dir(rel), ref.ConfigPath))
		refs = append(refs, ref)
	}
	return refs
}

// summarizeTools lists the tools entries and flags any name the loader does
// not know, which is the failure the assistant is most likely to cause.
func summarizeTools(content map[string]any) []toolRef {
	entries, ok := content["tools"].([]any)
	if !ok {
		return nil
	}
	refs := make([]toolRef, 0, len(entries))
	for _, entry := range entries {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := fields["name"].(string)
		_, hasArgs := fields["args"]
		refs = append(refs, toolRef{
			Name:       name,
			HasArgs:    hasArgs,
			Registered: slices.Contains(configToolNames, name),
		})
	}
	return refs
}

// exists reports whether a path inside the sandbox is present. A path that
// escapes the sandbox does not exist as far as the assistant is concerned.
func (w *workspace) exists(rel string) bool {
	_, err := w.root.Stat(rel)
	return err == nil
}

// isYAMLPath reports whether the path names a YAML file.
func isYAMLPath(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}
