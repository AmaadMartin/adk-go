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

import "strings"

// agentConfigReferenceBody describes the YAML surface adk-go's config loader
// actually reads. It is written by hand against internal/configurable, because
// adk-go has no generated schema artefact to build it from. The agent class and
// tool name lists are filled in from the same values the write_config_files
// check uses, so the prompt cannot drift from the check.
const agentConfigReferenceBody = `ADK AgentConfig quick reference
--------------------------------

LlmAgent (agent_class: LlmAgent, the default when agent_class is omitted)
  Required fields: name, instruction. Always set model explicitly.
  Optional fields:
    agent_class: keep it for clarity even though LlmAgent is the default.
    description: a one-line summary; a parent agent uses it to decide
      whether to delegate.
    model: the model id, for example gemini-2.5-flash. It must be a Gemini
      model; anything else fails to load.
    sub_agents: a list of AgentRef entries (see below).
    tools: a list of ToolConfig entries (see below).
    before_agent_callbacks / after_agent_callbacks: lists of code
      references that run around the agent.
    disallow_transfer_to_parent / disallow_transfer_to_peers: booleans
      that stop the model from transferring control.
    generate_content_config: passed straight to the genai
      GenerateContentConfig, so maxOutputTokens, temperature, topP, topK
      and the other generation settings apply.

Workflow agents (LoopAgent, ParallelAgent, SequentialAgent)
  They share the base fields: agent_class, name, description, sub_agents,
  before_agent_callbacks and after_agent_callbacks. Never give a workflow
  agent a model, an instruction or tools; it orchestrates the sub-agents
  that hold those.
  LoopAgent adds max_iterations, which caps the number of passes.

AgentRef
  An entry of a sub_agents list. Give it config_path, the path of another
  YAML config. adk-go does not support the code reference form yet; an
  entry that uses code fails to load with this message:
    inline code agent references are not yet supported

ToolConfig
  An entry of a tools list. Required field name. Optional field args, a
  mapping passed to the tool when it is built.
  adk-go resolves these tool names: {{tool_names}}.
  A name outside that list fails to load. A tool written in Go becomes
  loadable only after the program registers it with
  configurable.RegisterToolFactory, so writing a Go file alone is not
  enough.

Agent classes adk-go loads: {{agent_classes}}.

Not supported by adk-go. adk-python's AgentConfig also has input_schema,
output_schema, output_key, include_contents, before_model_callbacks,
after_model_callbacks, before_tool_callbacks and after_tool_callbacks.
adk-go's loader ignores or rejects all of them, so leave them out of every
config you write.`

// agentConfigReference returns the quick reference for embedding in the prompt.
func agentConfigReference() string {
	body := strings.NewReplacer(
		"{{tool_names}}", strings.Join(configToolNames, ", "),
		"{{agent_classes}}", strings.Join(agentClasses, ", "),
	).Replace(agentConfigReferenceBody)
	return "```text\n" + body + "\n```"
}
