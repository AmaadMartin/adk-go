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
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
)

func TestResearchSubAgents(t *testing.T) {
	llm := stubModel{name: "gemini-2.5-flash"}

	tests := []struct {
		name  string
		build func(model.LLM) (agent.Agent, error)
		want  string
	}{
		{name: "search agent", build: newGoogleSearchAgent, want: "google_search_agent"},
		{name: "url context agent", build: newURLContextAgent, want: "url_context_agent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			built, err := test.build(llm)
			if err != nil {
				t.Fatalf("building the sub-agent returned error: %v", err)
			}
			// The agent's name is the name of the tool the assistant calls,
			// so the roster depends on it.
			if built.Name() != test.want {
				t.Errorf("agent name = %q, want %q", built.Name(), test.want)
			}
			if built.Description() == "" {
				t.Error("the agent has no description, so a caller cannot route to it")
			}
		})
	}
}
