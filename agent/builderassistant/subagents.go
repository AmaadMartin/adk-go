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
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/geminitool"
)

// Gemini's built-in search and URL tools cannot share one agent's tool list, so
// each gets its own agent that the assistant calls as a tool.

const googleSearchAgentInstruction = `You are the search agent for the Agent Builder Assistant.

Search for ADK examples, patterns, documentation and solutions. Look for:
- agent configuration examples
- multi-agent architectures and workflows
- best practices and reference documentation
- similar use cases and implementations
- fixes for an error message the user reports

Narrow the search with a site filter when you can:
- site:github.com/google/adk-go for Go examples
- site:github.com/google/adk-python for the reference implementation
- site:github.com/google/adk-docs for documentation

Report the URLs you found, what each one contains, how it relates to the
question, and which ones are worth fetching in full.`

const urlContextAgentInstruction = `You are the URL analysis agent for the Agent Builder Assistant.

Fetch a URL and report what it contains. You are usually pointed at a GitHub
file, an ADK documentation page, or a code example.

Report:
- what the page provides
- the agent configuration, tool implementation or code pattern it shows
- how it answers the question you were given
- any warning or constraint the page states

Quote the configuration and code you found rather than describing it, so the
assistant can reuse it directly.`

// newGoogleSearchAgent returns the agent that runs Google Search. Its name is
// also the name of the tool the assistant calls, so it must not change.
func newGoogleSearchAgent(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        "google_search_agent",
		Description: "Searches Google for ADK examples and documentation",
		Instruction: googleSearchAgentInstruction,
		Model:       llm,
		Tools:       []tool.Tool{geminitool.GoogleSearch{}},
	})
}

// newURLContextAgent returns the agent that reads a URL. Its name is also the
// name of the tool the assistant calls, so it must not change.
func newURLContextAgent(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        "url_context_agent",
		Description: "Fetches and analyses the content of a URL, such as a GitHub file or a documentation page",
		Instruction: urlContextAgentInstruction,
		Model:       llm,
		Tools:       []tool.Tool{geminitool.New("url_context", "url context", &genai.Tool{URLContext: &genai.URLContext{}})},
	})
}
