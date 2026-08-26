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
	"strings"
	"testing"
)

// unsupportedMarker opens the paragraph that lists the adk-python fields
// adk-go does not read.
const unsupportedMarker = "Not supported by adk-go."

func TestAgentConfigReferenceOpensWithItsHeading(t *testing.T) {
	reference := agentConfigReference()

	const want = "```text\nADK AgentConfig quick reference\n----"
	if !strings.HasPrefix(reference, want) {
		t.Errorf("the reference starts with %q, want it to start with %q", first(reference, len(want)), want)
	}
	if !strings.HasSuffix(reference, "\n```") {
		t.Error("the reference does not close its code fence")
	}
}

func TestAgentConfigReferenceNamesEverythingTheLoaderAccepts(t *testing.T) {
	reference := agentConfigReference()

	for _, class := range agentClasses {
		if !strings.Contains(reference, class) {
			t.Errorf("the reference does not name the agent class %q", class)
		}
	}
	for _, name := range configToolNames {
		if !strings.Contains(reference, name) {
			t.Errorf("the reference does not name the tool %q", name)
		}
	}
}

// TestAgentConfigReferenceKeepsPythonOnlyFieldsOutOfTheGuidance is the guard
// against someone pasting adk-python's reference text back in. adk-go's loader
// ignores or rejects these fields, so the assistant must only meet them in the
// paragraph that says they do not work.
func TestAgentConfigReferenceKeepsPythonOnlyFieldsOutOfTheGuidance(t *testing.T) {
	reference := agentConfigReference()
	cut := strings.Index(reference, unsupportedMarker)
	if cut < 0 {
		t.Fatalf("the reference has no %q paragraph", unsupportedMarker)
	}
	guidance := reference[:cut]

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
			t.Errorf("the reference offers %q, which adk-go's loader does not read", field)
		}
		if !strings.Contains(reference[cut:], field) {
			t.Errorf("the reference does not warn that %q is unsupported", field)
		}
	}
}

func TestAgentConfigReferenceStatesTheLoaderLimits(t *testing.T) {
	reference := agentConfigReference()

	for _, want := range []string{
		"inline code agent references are not yet supported",
		"RegisterToolFactory",
		"max_iterations",
		"generate_content_config",
	} {
		if !strings.Contains(reference, want) {
			t.Errorf("the reference does not mention %q", want)
		}
	}
}

// first returns at most n characters of text.
func first(text string, n int) string {
	return text[:min(n, len(text))]
}
