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
	"strings"
	"testing"

	"google.golang.org/adk/v2/session"
)

// unsupportedMarker opens the paragraph that lists the adk-python fields
// adk-go does not read.
const unsupportedMarker = "Not supported by adk-go."

func TestInstructionProviderFillsTheModelAndTheProjectFolder(t *testing.T) {
	project := filepath.Join(newProject(t), "my_agent_project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	provider := newInstructionProvider("gemini-2.0-flash")

	// The instruction is resolved per invocation so it can name the session's
	// project folder; the schema and the model are fixed when New is called.
	instruction, err := provider(newContext(t, project))
	if err != nil {
		t.Fatalf("the instruction provider returned error: %v", err)
	}

	for _, want := range []string{
		"gemini-2.0-flash",
		"my_agent_project",
		"ADK AgentConfig quick reference",
	} {
		if !strings.Contains(instruction, want) {
			t.Errorf("the instruction does not contain %q", want)
		}
	}
	for _, placeholder := range []string{modelPlaceholder, folderPlaceholder, toolNamesPlaceholder, agentClassesPlaceholder} {
		if strings.Contains(instruction, placeholder) {
			t.Errorf("the instruction still contains the placeholder %q", placeholder)
		}
	}
}

func TestInstructionProviderNamesTheProjectFolderFromTheSession(t *testing.T) {
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
		{name: "root directory in state", state: mapState{RootDirectoryStateKey: project}, want: filepath.Base(project)},
		{name: "root directory absent", state: mapState{}, want: filepath.Base(working)},
		{name: "root directory empty", state: mapState{RootDirectoryStateKey: ""}, want: filepath.Base(working)},
		{
			name:  "root directory is the filesystem root",
			state: mapState{RootDirectoryStateKey: string(filepath.Separator)},
			want:  fallbackProjectFolderName,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := projectFolderName(test.state)
			if err != nil {
				t.Fatalf("projectFolderName returned error: %v", err)
			}
			if got != test.want {
				t.Errorf("projectFolderName = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInstructionProviderFailsWithoutAWorkingDirectory(t *testing.T) {
	gone := filepath.Join(newProject(t), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	ctx := newContextWithState(t, mapState{})
	if _, err := rootDirectory(ctx.ReadonlyState()); err == nil {
		t.Skip("this platform still reports a working directory after it is removed")
	}

	if _, err := newInstructionProvider("gemini-2.5-pro")(ctx); err == nil {
		t.Error("the instruction provider returned no error without a working directory")
	}
}

// TestInstructionDescribesEveryTool guards the prompt against a tool that the
// assistant has but never learns to use.
func TestInstructionDescribesEveryTool(t *testing.T) {
	tools, err := newTools(Config{Model: stubModel{name: "gemini-2.5-pro"}})
	if err != nil {
		t.Fatalf("newTools returned error: %v", err)
	}

	for _, name := range toolNames(tools) {
		if !strings.Contains(instructionTemplate, "`"+name+"`") {
			t.Errorf("the instruction does not mention the tool %q", name)
		}
	}
}

// TestInstructionKeepsOnlyTheKnownPlaceholders catches a placeholder that a
// later edit adds to the template but nothing substitutes.
func TestInstructionKeepsOnlyTheKnownPlaceholders(t *testing.T) {
	stripped := strings.NewReplacer(
		modelPlaceholder, "",
		folderPlaceholder, "",
		toolNamesPlaceholder, "",
		agentClassesPlaceholder, "",
	).Replace(instructionTemplate)

	if strings.Contains(stripped, "{{") {
		t.Errorf("the instruction holds a placeholder nothing substitutes:\n%s", placeholderContext(stripped))
	}
}

// placeholderContext returns the text around the first unknown placeholder.
func placeholderContext(text string) string {
	start := strings.Index(text, "{{")
	end := min(start+60, len(text))
	return text[start:end]
}

// renderInstruction resolves the prompt for a session rooted at dir.
func renderInstruction(t *testing.T, dir string) string {
	t.Helper()
	instruction, err := newInstructionProvider("gemini-2.5-pro")(newContext(t, dir))
	if err != nil {
		t.Fatalf("the instruction provider returned error: %v", err)
	}
	return instruction
}

// referenceBlock returns the fenced AgentConfig reference inside instruction.
func referenceBlock(t *testing.T, instruction string) string {
	t.Helper()
	const fence = "```text\n"
	start := strings.Index(instruction, fence)
	if start < 0 {
		t.Fatal("the instruction has no fenced reference block")
	}
	block := instruction[start+len(fence):]
	end := strings.Index(block, "\n```")
	if end < 0 {
		t.Fatal("the fenced reference block is never closed")
	}
	return block[:end]
}

func TestInstructionOpensTheReferenceWithItsHeading(t *testing.T) {
	block := referenceBlock(t, renderInstruction(t, newProject(t)))

	const want = "ADK AgentConfig quick reference\n----"
	if !strings.HasPrefix(block, want) {
		t.Errorf("the reference starts with %q, want it to start with %q", first(block, len(want)), want)
	}
}

func TestInstructionNamesEverythingTheLoaderAccepts(t *testing.T) {
	block := referenceBlock(t, renderInstruction(t, newProject(t)))

	for _, class := range agentClasses {
		if !strings.Contains(block, class) {
			t.Errorf("the reference does not name the agent class %q", class)
		}
	}
	for _, name := range configToolNames {
		if !strings.Contains(block, name) {
			t.Errorf("the reference does not name the tool %q", name)
		}
	}
}

// TestInstructionKeepsPythonOnlyFieldsOutOfTheGuidance is the guard against
// someone pasting adk-python's reference text back in. adk-go's loader ignores
// or rejects these fields, so the assistant must only meet them in the
// paragraph that says they do not work.
func TestInstructionKeepsPythonOnlyFieldsOutOfTheGuidance(t *testing.T) {
	instruction := renderInstruction(t, newProject(t))
	cut := strings.Index(instruction, unsupportedMarker)
	if cut < 0 {
		t.Fatalf("the instruction has no %q paragraph", unsupportedMarker)
	}
	guidance := instruction[:cut]

	unsupported := []string{
		"input_schema",
		"output_schema",
		"output_key",
		"include_contents",
		"before_model_callbacks",
		"after_model_callbacks",
		"before_tool_callbacks",
		"after_tool_callbacks",
	}
	for _, field := range unsupported {
		if strings.Contains(guidance, field) {
			t.Errorf("the instruction offers %q, which adk-go's loader does not read", field)
		}
		if !strings.Contains(instruction[cut:], field) {
			t.Errorf("the instruction does not warn that %q is unsupported", field)
		}
	}
}

func TestInstructionStatesTheLoaderLimits(t *testing.T) {
	instruction := renderInstruction(t, newProject(t))

	for _, want := range []string{
		"inline code agent references are not yet supported",
		"RegisterToolFactory",
		"max_iterations",
		"generate_content_config",
	} {
		if !strings.Contains(instruction, want) {
			t.Errorf("the instruction does not mention %q", want)
		}
	}
}

// first returns at most n characters of text.
func first(text string, n int) string {
	return text[:min(n, len(text))]
}
