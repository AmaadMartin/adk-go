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

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
)

// AgentName is the name of the agent [New] returns.
const AgentName = "agent_builder_assistant"

// agentDescription tells a parent agent what this one is for.
const agentDescription = "Intelligent assistant for building ADK multi-agent systems using YAML configurations"

// maxOutputTokens caps a reply. An agent config is long, and the assistant
// prints whole files before it writes them.
const maxOutputTokens int32 = 8192

// ErrNoModel reports a [Config] with no model.
var ErrNoModel = errors.New("builderassistant: Config.Model is required")

// Config configures the Agent Builder Assistant.
type Config struct {
	// Model backs the assistant. Its Name is also the model id the assistant
	// writes into the agent configs it generates, so it must be a model ADK's
	// config loader accepts. Required.
	Model model.LLM

	// SearchModel backs the two research sub-agents, which only search the web
	// and read pages. Optional: they use Model when this is nil.
	SearchModel model.LLM
}

// New returns the Agent Builder Assistant: an agent that designs an ADK
// multi-agent system with a developer and writes the YAML configs for it.
//
// Every file the assistant touches lives under the directory named by the
// session state key [RootDirectoryStateKey]; see the package documentation.
func New(cfg Config) (agent.Agent, error) {
	agentCfg, err := newAgentConfig(cfg)
	if err != nil {
		return nil, err
	}
	return llmagent.New(agentCfg)
}

// newAgentConfig assembles the assistant's llmagent configuration. New splits
// it out so a test can read the configuration back; llmagent exposes no getter
// for it.
func newAgentConfig(cfg Config) (llmagent.Config, error) {
	if cfg.Model == nil {
		return llmagent.Config{}, ErrNoModel
	}
	tools, err := newTools(cfg)
	if err != nil {
		return llmagent.Config{}, fmt.Errorf("builderassistant: build tools: %w", err)
	}
	return llmagent.Config{
		Name:        AgentName,
		Description: agentDescription,
		Model:       cfg.Model,
		// InstructionProvider, not Instruction: the prompt is full of literal
		// braces, which Instruction would read as session-state placeholders.
		InstructionProvider:   newInstructionProvider(cfg.Model.Name()),
		Tools:                 tools,
		GenerateContentConfig: &genai.GenerateContentConfig{MaxOutputTokens: maxOutputTokens},
	}, nil
}

// newTools returns the assistant's tools, research agents first. Every entry is
// a capability the prompt relies on, so a missing one silently disables part of
// the assistant.
func newTools(cfg Config) ([]tool.Tool, error) {
	research := researchModel(cfg)
	searchAgent, err := newGoogleSearchAgent(research)
	if err != nil {
		return nil, fmt.Errorf("build the search agent: %w", err)
	}
	urlAgent, err := newURLContextAgent(research)
	if err != nil {
		return nil, fmt.Errorf("build the url context agent: %w", err)
	}

	tools := []tool.Tool{
		agenttool.New(searchAgent, nil),
		agenttool.New(urlAgent, nil),
	}
	for _, build := range []func() (tool.Tool, error){
		newReadConfigFilesTool,
		newWriteConfigFilesTool,
		newExploreProjectTool,
		newReadFilesTool,
		newWriteFilesTool,
		newDeleteFilesTool,
		newCleanupUnusedFilesTool,
	} {
		built, err := build()
		if err != nil {
			return nil, err
		}
		tools = append(tools, built)
	}
	return tools, nil
}

// researchModel is the model the two research sub-agents run on. They only
// search the web and read pages, so a smaller model than the assistant's is
// usually enough.
func researchModel(cfg Config) model.LLM {
	if cfg.SearchModel != nil {
		return cfg.SearchModel
	}
	return cfg.Model
}
