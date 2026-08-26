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

// BuildAgentsTree returns the agent tree rooted at root, keyed by agent name.
// Only LLM agents appear in the tree, and a name already in the tree is not
// walked again.
func BuildAgentsTree(root agent.Agent) map[string]models.AgentInfo {
	agents := map[string]models.AgentInfo{}
	visitAgent(root, agents)
	return agents
}

func visitAgent(a agent.Agent, agents map[string]models.AgentInfo) {
	llmAgent, ok := a.(llmagentinternal.Agent)
	if !ok {
		return
	}
	if _, seen := agents[a.Name()]; seen {
		return
	}

	subAgentNames := []string{}
	for _, subAgent := range a.SubAgents() {
		if _, ok := subAgent.(llmagentinternal.Agent); !ok {
			continue
		}
		visitAgent(subAgent, agents)
		subAgentNames = append(subAgentNames, subAgent.Name())
	}

	state := llmagentinternal.Reveal(llmAgent)
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
		declaration := declared.Declaration()
		if declaration == nil {
			continue
		}
		declarations = append(declarations, genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{declaration},
		})
	}
	return declarations
}
