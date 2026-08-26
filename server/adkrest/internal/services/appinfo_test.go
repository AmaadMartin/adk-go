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

package services_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/server/adkrest/internal/services"
	"google.golang.org/adk/v2/tool"
)

// bareTool implements tool.Tool and nothing else, so it has no declaration to
// report.
type bareTool struct{}

func (bareTool) Name() string        { return "bare" }
func (bareTool) Description() string { return "A tool with no declaration." }
func (bareTool) IsLongRunning() bool { return false }

// nilDeclarationTool exposes a Declaration method that returns nil.
type nilDeclarationTool struct{ bareTool }

func (nilDeclarationTool) Declaration() *genai.FunctionDeclaration { return nil }

func TestBuildAgentsTreeSkipsToolsWithoutDeclaration(t *testing.T) {
	root, err := llmagent.New(llmagent.Config{
		Name:  "root",
		Tools: []tool.Tool{bareTool{}, nilDeclarationTool{}},
	})
	if err != nil {
		t.Fatalf("llmagent.New() failed: %v", err)
	}

	got, ok := services.BuildAgentsTree(root)

	if !ok {
		t.Fatal("BuildAgentsTree() ok = false, want true for an LLM root")
	}
	want := map[string]models.AgentInfo{
		"root": {Name: "root", Tools: []genai.Tool{}, SubAgents: []string{}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("BuildAgentsTree() mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildAgentsTreeNonLLMRoot(t *testing.T) {
	root, err := agent.New(agent.Config{Name: "custom"})
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}

	got, ok := services.BuildAgentsTree(root)

	if ok {
		t.Error("BuildAgentsTree() ok = true, want false for a non-LLM root")
	}
	if got != nil {
		t.Errorf("BuildAgentsTree() = %v, want nil for a non-LLM root", got)
	}
}
