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

// Package builderassistant provides the ADK Agent Builder Assistant: an agent
// that interviews a developer about the multi-agent system they want, and then
// writes the YAML agent configs that ADK's config loader reads.
//
// Build one with [New] and drive it with a [runner.Runner]:
//
//	llm, err := gemini.NewModel(ctx, "gemini-2.5-pro", &genai.ClientConfig{
//		APIKey: os.Getenv("GOOGLE_API_KEY"),
//	})
//	// handle err
//	a, err := builderassistant.New(builderassistant.Config{Model: llm})
//	// handle err
//	r, err := runner.New(runner.Config{
//		AppName:           "agent-builder",
//		Agent:             a,
//		SessionService:    session.InMemoryService(),
//		AutoCreateSession: true,
//	})
//	// handle err
//
// The assistant reads and writes files through a sandbox. Every path a tool
// receives is resolved against the root directory recorded in the session
// state under the key "root_directory", and a path that escapes that directory
// is rejected with [ErrOutsideRoot]. When the key is absent the sandbox root is
// the process working directory.
package builderassistant
