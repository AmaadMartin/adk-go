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
	"context"
	"errors"
	"iter"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

// stubModel is a model.LLM that only carries a name. Building the assistant
// never calls a model, so a stub keeps the tests offline.
type stubModel struct{ name string }

func (m stubModel) Name() string { return m.name }

func (m stubModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, errors.New("stubModel does not generate content"))
	}
}

func toolNames(tools []tool.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}

func TestNewExposesTheFullAgentBuildingToolSet(t *testing.T) {
	tools, err := newTools(Config{Model: stubModel{name: "gemini-2.5-pro"}})
	if err != nil {
		t.Fatalf("newTools returned error: %v", err)
	}

	// Every capability the assistant needs to build an agent from a prompt:
	// the two research agents wrapped as tools, plus the config, file and
	// project tools. A missing entry silently disables a capability.
	want := []string{
		"cleanup_unused_files",
		"delete_files",
		"explore_project",
		"google_search_agent",
		"read_config_files",
		"read_files",
		"url_context_agent",
		"write_config_files",
		"write_files",
	}
	got := toolNames(tools)
	sort.Strings(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("tool names mismatch (-want +got):\n%s", diff)
	}
}

func TestNewOrdersTheResearchToolsFirst(t *testing.T) {
	tools, err := newTools(Config{Model: stubModel{name: "gemini-2.5-pro"}})
	if err != nil {
		t.Fatalf("newTools returned error: %v", err)
	}

	want := []string{
		"google_search_agent",
		"url_context_agent",
		"read_config_files",
		"write_config_files",
		"explore_project",
		"read_files",
		"write_files",
		"delete_files",
		"cleanup_unused_files",
	}
	if diff := cmp.Diff(want, toolNames(tools)); diff != "" {
		t.Errorf("tool order mismatch (-want +got):\n%s", diff)
	}
}

func TestNewNamesAndDescribesTheAgent(t *testing.T) {
	built, err := New(Config{Model: stubModel{name: "gemini-2.5-pro"}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if built.Name() != AgentName {
		t.Errorf("agent name = %q, want %q", built.Name(), AgentName)
	}
	if built.Description() != agentDescription {
		t.Errorf("agent description = %q, want %q", built.Description(), agentDescription)
	}
}

func TestNewCapsTheReplyLength(t *testing.T) {
	cfg, err := newAgentConfig(Config{Model: stubModel{name: "gemini-2.5-pro"}})
	if err != nil {
		t.Fatalf("newAgentConfig returned error: %v", err)
	}

	if cfg.GenerateContentConfig == nil {
		t.Fatal("newAgentConfig set no GenerateContentConfig")
	}
	if got := cfg.GenerateContentConfig.MaxOutputTokens; got != maxOutputTokens {
		t.Errorf("MaxOutputTokens = %d, want %d", got, maxOutputTokens)
	}
}

func TestNewUsesAnInstructionProvider(t *testing.T) {
	cfg, err := newAgentConfig(Config{Model: stubModel{name: "gemini-2.5-pro"}})
	if err != nil {
		t.Fatalf("newAgentConfig returned error: %v", err)
	}

	// A plain Instruction would read the prompt's literal braces as
	// session-state placeholders and fail at run time.
	if cfg.InstructionProvider == nil {
		t.Error("newAgentConfig set no InstructionProvider")
	}
	if cfg.Instruction != "" {
		t.Errorf("newAgentConfig set Instruction to %q, want it empty", cfg.Instruction)
	}
}

func TestNewRejectsAConfigWithoutAModel(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrNoModel) {
		t.Errorf("New returned %v, want an error matching ErrNoModel", err)
	}
	if _, err := newAgentConfig(Config{SearchModel: stubModel{name: "gemini-2.5-flash"}}); !errors.Is(err, ErrNoModel) {
		t.Errorf("newAgentConfig returned %v, want an error matching ErrNoModel", err)
	}
}

func TestResearchModelFallsBackToTheAssistantModel(t *testing.T) {
	assistant := stubModel{name: "gemini-2.5-pro"}
	search := stubModel{name: "gemini-2.5-flash"}

	if got := researchModel(Config{Model: assistant}); got != model.LLM(assistant) {
		t.Errorf("researchModel = %v, want the assistant model", got)
	}
	if got := researchModel(Config{Model: assistant, SearchModel: search}); got != model.LLM(search) {
		t.Errorf("researchModel = %v, want the search model", got)
	}
}

// TestNewBuildsAgentARunnerCanDrive is the end-to-end construction check: the
// assistant, its sub-agents and its tools assemble into an agent a Runner
// accepts. It does not call the model.
func TestNewBuildsAgentARunnerCanDrive(t *testing.T) {
	built, err := New(Config{Model: stubModel{name: "gemini-2.5-pro"}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if _, err := runner.New(runner.Config{
		AppName:           "agent-builder",
		Agent:             built,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	}); err != nil {
		t.Fatalf("runner.New returned error: %v", err)
	}
	if found := built.FindAgent(AgentName); found == nil {
		t.Errorf("FindAgent(%q) found nothing", AgentName)
	}
}
