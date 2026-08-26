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

package services

import (
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	llmagentinternal "google.golang.org/adk/v2/internal/llminternal"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/tool"
)

// declaredTool is a tool that exposes a function declaration. tool.Tool itself
// does not, so app-info reaches the declaration through this local interface.
type declaredTool interface {
	Declaration() *genai.FunctionDeclaration
}

// llmState returns the LLM state of a and reports whether a is an LLM agent.
// llmAgent is unexported, so this assertion is the "is it an LLM agent?" test.
func llmState(a agent.Agent) (*llmagentinternal.State, bool) {
	llmAgent, ok := a.(llmagentinternal.Agent)
	if !ok {
		return nil, false
	}
	return llmagentinternal.Reveal(llmAgent), true
}

// BuildAgentsTree returns the agent tree rooted at root, keyed by agent name.
// Only LLM agents appear in the tree, and a name already in the tree is not
// walked again. The tree is empty when root is not an LLM agent.
func BuildAgentsTree(root agent.Agent) map[string]models.AgentInfo {
	agents := map[string]models.AgentInfo{}
	if state, ok := llmState(root); ok {
		visitAgent(root, state, agents)
	}
	return agents
}

func visitAgent(a agent.Agent, state *llmagentinternal.State, agents map[string]models.AgentInfo) {
	if _, seen := agents[a.Name()]; seen {
		return
	}
	// Claim the name before walking the sub-agents, so a sub-agent that leads
	// back to this name stops here instead of recursing forever. The final
	// value replaces this placeholder below.
	agents[a.Name()] = models.AgentInfo{}

	subAgentNames := []string{}
	for _, subAgent := range a.SubAgents() {
		subState, ok := llmState(subAgent)
		if !ok {
			continue
		}
		visitAgent(subAgent, subState, agents)
		subAgentNames = append(subAgentNames, subAgent.Name())
	}

	agents[a.Name()] = models.AgentInfo{
		Name:        a.Name(),
		Description: a.Description(),
		Instruction: state.Instruction,
		Tools:       toolDeclarations(state.Tools),
		SubAgents:   subAgentNames,
	}
}

// toolDeclarations returns the declaration of every directly attached tool.
//
// simplicity: toolsets are omitted because tool.Toolset.Tools needs an
// agent.ReadonlyContext that exists only inside an invocation. The agent graph
// generator has the same limit. Upgrade path: expand toolsets once a toolset
// can be listed outside a run.
func toolDeclarations(tools []tool.Tool) []genai.Tool {
	declarations := []genai.Tool{}
	for _, t := range tools {
		declared, ok := t.(declaredTool)
		if !ok {
			continue
		}
		if declaration := declared.Declaration(); declaration != nil {
			declarations = append(declarations, genai.Tool{
				FunctionDeclarations: []*genai.FunctionDeclaration{declaration},
			})
		}
	}
	return declarations
}
