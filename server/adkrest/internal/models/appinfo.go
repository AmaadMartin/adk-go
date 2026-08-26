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

package models

import "google.golang.org/genai"

// AgentInfo describes a single agent in an app's agent tree.
//
// The keys stay snake_case because adk-python's AgentInfo is a plain pydantic
// model, unlike AppInfo below.
type AgentInfo struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Instruction string       `json:"instruction"`
	Tools       []genai.Tool `json:"tools"`
	SubAgents   []string     `json:"sub_agents"`
}

// AppInfo describes an ADK app and its agent tree.
//
// The keys are camelCase because adk-python's AppInfo derives from a base
// model that camel-cases every field.
type AppInfo struct {
	Name          string               `json:"name"`
	RootAgentName string               `json:"rootAgentName"`
	Description   string               `json:"description"`
	Language      string               `json:"language"`
	IsComputerUse bool                 `json:"isComputerUse"`
	Agents        map[string]AgentInfo `json:"agents,omitempty"`
}
