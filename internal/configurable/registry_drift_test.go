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

package configurable

import (
	"maps"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The Agent Builder Assistant (agent/builderassistant) tells the model which
// agent classes and tool names a config may use, and rejects a config that
// names anything else. It cannot read these registries: importing this package
// would pull the whole config loader, and its process-spawning MCP toolset,
// into a public agent package. So it holds its own copy of both lists.
//
// These are that copy. Registering a new agent class or tool factory without
// updating agent/builderassistant would leave the assistant unable to offer it,
// so this test fails rather than letting the two drift apart quietly.
var (
	builderAssistantAgentClasses = []string{
		"LlmAgent",
		"LoopAgent",
		"ParallelAgent",
		"SequentialAgent",
		"Workflow",
	}
	builderAssistantToolNames = []string{
		"AgentTool",
		"ExampleTool",
		"LongRunningFunctionTool",
		"McpToolset",
		"exit_loop",
		"google_maps_grounding",
		"google_search",
		"url_context",
	}
)

// toolNamesRegisteredByTests are fixtures that other tests in this package
// register from their own init, so they are in the registry but are not part of
// the loader's built-in surface. Add a new fixture here when you add one.
var toolNamesRegisteredByTests = []string{"test_tool"}

func TestRegistriesMatchTheAgentBuilderAssistantLists(t *testing.T) {
	registryMu.RLock()
	registeredClasses := slices.Sorted(maps.Keys(registry))
	registeredTools := slices.Sorted(maps.Keys(toolRegistry))
	registryMu.RUnlock()

	registeredTools = slices.DeleteFunc(registeredTools, func(name string) bool {
		return slices.Contains(toolNamesRegisteredByTests, name)
	})

	tests := []struct {
		name       string
		registered []string
		copied     []string
	}{
		{name: "agent classes", registered: registeredClasses, copied: builderAssistantAgentClasses},
		{name: "tool names", registered: registeredTools, copied: builderAssistantToolNames},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := slices.Sorted(slices.Values(test.copied))
			if diff := cmp.Diff(want, test.registered); diff != "" {
				t.Errorf("the registered %s no longer match agent/builderassistant; "+
					"update agentClasses and configToolNames there too "+
					"(-builderassistant +registered):\n%s", test.name, diff)
			}
		})
	}
}
