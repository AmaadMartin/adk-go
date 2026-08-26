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

package controllers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/mux"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/server/adkrest/controllers"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

type echoArgs struct {
	Text string `json:"text"`
}

type echoResults struct {
	Text string `json:"text"`
}

// newEchoTool returns a function tool named name, so app-info has a real
// function declaration to report.
func newEchoTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	echoTool, err := functiontool.New[echoArgs, echoResults](
		functiontool.Config{Name: name, Description: "Echoes the input."},
		func(ctx agent.Context, args echoArgs) (echoResults, error) {
			return echoResults(args), nil
		},
	)
	if err != nil {
		t.Fatalf("functiontool.New(%q) failed: %v", name, err)
	}
	return echoTool
}

func newLLMAgent(t *testing.T, cfg llmagent.Config) agent.Agent {
	t.Helper()
	a, err := llmagent.New(cfg)
	if err != nil {
		t.Fatalf("llmagent.New(%q) failed: %v", cfg.Name, err)
	}
	return a
}

func newLoader(t *testing.T, root agent.Agent) agent.Loader {
	t.Helper()
	return agent.NewSingleLoader(root)
}

// appInfoRequest issues GET /apps/{appName}/app-info against the controller.
func appInfoRequest(t *testing.T, c *controllers.AppsAPIController, appName string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "/apps/"+appName+"/app-info", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req = mux.SetURLVars(req, map[string]string{"app_name": appName})
	rr := httptest.NewRecorder()
	c.GetAppInfoHandler(rr, req)
	return rr
}

func TestGetAppInfoSingleAgent(t *testing.T) {
	root := newLLMAgent(t, llmagent.Config{
		Name:        "assistant",
		Description: "A helpful assistant.",
		Instruction: "Be helpful.",
	})

	rr := appInfoRequest(t, controllers.NewAppsAPIController(newLoader(t, root)), "assistant")

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("GetAppInfoHandler() status = %d, want %d; body %q", got, want, rr.Body.String())
	}
	var got models.AppInfo
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode app info: %v", err)
	}
	want := models.AppInfo{
		Name:          "assistant",
		RootAgentName: "assistant",
		Description:   "A helpful assistant.",
		Language:      "go",
		IsComputerUse: false,
		Agents: map[string]models.AgentInfo{
			"assistant": {
				Name:        "assistant",
				Description: "A helpful assistant.",
				Instruction: "Be helpful.",
				Tools:       []genai.Tool{},
				SubAgents:   []string{},
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GetAppInfoHandler() mismatch (-want +got):\n%s", diff)
	}
}

// TestGetAppInfoWireKeys pins the exact JSON keys, which are the parity
// contract with adk-python and are not all camelCase.
func TestGetAppInfoWireKeys(t *testing.T) {
	root := newLLMAgent(t, llmagent.Config{
		Name:      "root",
		SubAgents: []agent.Agent{newLLMAgent(t, llmagent.Config{Name: "child"})},
	})

	rr := appInfoRequest(t, controllers.NewAppsAPIController(newLoader(t, root)), "root")

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("GetAppInfoHandler() status = %d, want %d", got, want)
	}
	body := rr.Body.String()
	for _, key := range []string{`"rootAgentName"`, `"isComputerUse"`, `"sub_agents"`, `"agents"`} {
		if !strings.Contains(body, key) {
			t.Errorf("GetAppInfoHandler() body is missing key %s; body %q", key, body)
		}
	}
	if strings.Contains(body, `"root_agent_name"`) {
		t.Errorf("GetAppInfoHandler() body has snake_case root_agent_name; body %q", body)
	}
	if strings.Contains(body, `"subAgents"`) {
		t.Errorf("GetAppInfoHandler() body has camelCase subAgents; body %q", body)
	}
}

func TestGetAppInfoSubAgentsAndTools(t *testing.T) {
	root := newLLMAgent(t, llmagent.Config{
		Name:        "root",
		Description: "The root.",
		Instruction: "Delegate.",
		Tools:       []tool.Tool{newEchoTool(t, "root_echo")},
		SubAgents: []agent.Agent{
			newLLMAgent(t, llmagent.Config{
				Name:        "first",
				Description: "First child.",
				Instruction: "Do the first thing.",
				Tools:       []tool.Tool{newEchoTool(t, "first_echo")},
			}),
			newLLMAgent(t, llmagent.Config{
				Name:        "second",
				Description: "Second child.",
				Instruction: "Do the second thing.",
				Tools:       []tool.Tool{newEchoTool(t, "second_echo")},
			}),
		},
	})

	rr := appInfoRequest(t, controllers.NewAppsAPIController(newLoader(t, root)), "root")

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("GetAppInfoHandler() status = %d, want %d; body %q", got, want, rr.Body.String())
	}
	var got models.AppInfo
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode app info: %v", err)
	}
	if diff := cmp.Diff([]string{"first", "second"}, got.Agents["root"].SubAgents); diff != "" {
		t.Errorf("root sub_agents mismatch (-want +got):\n%s", diff)
	}
	for agentName, toolName := range map[string]string{
		"root":   "root_echo",
		"first":  "first_echo",
		"second": "second_echo",
	} {
		info, ok := got.Agents[agentName]
		if !ok {
			t.Errorf("agents map is missing %q; got keys %v", agentName, agentNames(got.Agents))
			continue
		}
		if len(info.Tools) != 1 || len(info.Tools[0].FunctionDeclarations) != 1 {
			t.Errorf("agent %q tools = %+v, want one declaration", agentName, info.Tools)
			continue
		}
		if gotName := info.Tools[0].FunctionDeclarations[0].Name; gotName != toolName {
			t.Errorf("agent %q tool name = %q, want %q", agentName, gotName, toolName)
		}
	}
	if diff := cmp.Diff([]string{}, got.Agents["first"].SubAgents); diff != "" {
		t.Errorf("leaf sub_agents mismatch (-want +got):\n%s", diff)
	}
}

func TestGetAppInfoTripleNested(t *testing.T) {
	root := newLLMAgent(t, llmagent.Config{
		Name: "level1",
		SubAgents: []agent.Agent{newLLMAgent(t, llmagent.Config{
			Name:      "level2",
			SubAgents: []agent.Agent{newLLMAgent(t, llmagent.Config{Name: "level3"})},
		})},
	})

	rr := appInfoRequest(t, controllers.NewAppsAPIController(newLoader(t, root)), "level1")

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("GetAppInfoHandler() status = %d, want %d", got, want)
	}
	var got models.AppInfo
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode app info: %v", err)
	}
	if diff := cmp.Diff([]string{"level1", "level2", "level3"}, agentNames(got.Agents)); diff != "" {
		t.Errorf("agent names mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"level3"}, got.Agents["level2"].SubAgents); diff != "" {
		t.Errorf("level2 sub_agents mismatch (-want +got):\n%s", diff)
	}
}

// TestGetAppInfoRepeatedAgentName pins the visited guard: an agent name that
// appears twice in the tree is walked once, and the walk terminates.
func TestGetAppInfoRepeatedAgentName(t *testing.T) {
	root := newLLMAgent(t, llmagent.Config{
		Name:        "root",
		Instruction: "The real root.",
		SubAgents: []agent.Agent{
			newLLMAgent(t, llmagent.Config{
				Name: "branch",
				// A second agent that reuses the root's name. Without the
				// visited guard the walk would descend into it again.
				SubAgents: []agent.Agent{newLLMAgent(t, llmagent.Config{
					Name:        "root",
					Instruction: "The impostor.",
				})},
			}),
		},
	})

	rr := appInfoRequest(t, controllers.NewAppsAPIController(newLoader(t, root)), "root")

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("GetAppInfoHandler() status = %d, want %d", got, want)
	}
	var got models.AppInfo
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode app info: %v", err)
	}
	if diff := cmp.Diff([]string{"branch", "root"}, agentNames(got.Agents)); diff != "" {
		t.Errorf("agent names mismatch (-want +got):\n%s", diff)
	}
	if got, want := got.Agents["root"].Instruction, "The real root."; got != want {
		t.Errorf("root instruction = %q, want %q", got, want)
	}
}

// TestGetAppInfoSkipsNonLLMSubAgent pins that a sub-agent that is not an LLM
// agent is left out of the tree, like adk-python's isinstance check.
func TestGetAppInfoSkipsNonLLMSubAgent(t *testing.T) {
	custom, err := agent.New(agent.Config{Name: "custom"})
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	root := newLLMAgent(t, llmagent.Config{
		Name:      "root",
		SubAgents: []agent.Agent{custom, newLLMAgent(t, llmagent.Config{Name: "llm_child"})},
	})

	rr := appInfoRequest(t, controllers.NewAppsAPIController(newLoader(t, root)), "root")

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("GetAppInfoHandler() status = %d, want %d", got, want)
	}
	var got models.AppInfo
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode app info: %v", err)
	}
	if diff := cmp.Diff([]string{"llm_child", "root"}, agentNames(got.Agents)); diff != "" {
		t.Errorf("agent names mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"llm_child"}, got.Agents["root"].SubAgents); diff != "" {
		t.Errorf("root sub_agents mismatch (-want +got):\n%s", diff)
	}
}

func TestGetAppInfoErrors(t *testing.T) {
	llmRoot := newLLMAgent(t, llmagent.Config{Name: "assistant"})
	customRoot, err := agent.New(agent.Config{Name: "custom"})
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	specialRoot := newLLMAgent(t, llmagent.Config{Name: "__special"})

	tc := []struct {
		name       string
		root       agent.Agent
		opts       []controllers.AppsAPIOption
		appName    string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "root agent is not an LLM agent",
			root:       customRoot,
			appName:    "custom",
			wantStatus: http.StatusBadRequest,
			wantBody:   "Root agent is not an LlmAgent",
		},
		{
			name:       "unknown app",
			root:       llmRoot,
			appName:    "missing",
			wantStatus: http.StatusNotFound,
			wantBody:   "cannot load agent 'missing' - provide an empty string or use 'assistant'",
		},
		{
			name:       "special agent is rejected by default",
			root:       specialRoot,
			appName:    "__special",
			wantStatus: http.StatusForbidden,
			wantBody:   "Access to internal special agents is disabled in API server mode.",
		},
		{
			name:       "special agent is served when allowed",
			root:       specialRoot,
			opts:       []controllers.AppsAPIOption{controllers.WithSpecialAgents()},
			appName:    "__special",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			c := controllers.NewAppsAPIController(newLoader(t, tt.root), tt.opts...)

			rr := appInfoRequest(t, c, tt.appName)

			if got := rr.Code; got != tt.wantStatus {
				t.Fatalf("GetAppInfoHandler() status = %d, want %d; body %q", got, tt.wantStatus, rr.Body.String())
			}
			if tt.wantBody == "" {
				return
			}
			if got := strings.Trim(rr.Body.String(), "\n"); got != tt.wantBody {
				t.Errorf("GetAppInfoHandler() body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestListApps(t *testing.T) {
	root := newLLMAgent(t, llmagent.Config{Name: "assistant"})
	c := controllers.NewAppsAPIController(newLoader(t, root))
	req, err := http.NewRequest(http.MethodGet, "/list-apps", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	rr := httptest.NewRecorder()

	c.ListAppsHandler(rr, req)

	if got, want := rr.Code, http.StatusOK; got != want {
		t.Fatalf("ListAppsHandler() status = %d, want %d", got, want)
	}
	var got []string
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode apps: %v", err)
	}
	if diff := cmp.Diff([]string{"assistant"}, got); diff != "" {
		t.Errorf("ListAppsHandler() mismatch (-want +got):\n%s", diff)
	}
}

// agentNames returns the sorted keys of an agents map, so comparisons do not
// depend on map iteration order.
func agentNames(agents map[string]models.AgentInfo) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
