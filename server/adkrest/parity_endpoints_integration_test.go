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

package adkrest_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/server/adkrest"
	"google.golang.org/adk/v2/session"
)

// recordingMemoryService records the sessions the server hands it.
type recordingMemoryService struct {
	added []session.Session
}

func (m *recordingMemoryService) AddSessionToMemory(ctx context.Context, s session.Session) error {
	m.added = append(m.added, s)
	return nil
}

func (m *recordingMemoryService) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	return &memory.SearchResponse{}, nil
}

// newParityServer starts a real adkrest server backed by the in-memory
// session service and a single LLM agent named "test-app".
func newParityServer(t *testing.T) (*httptest.Server, *recordingMemoryService) {
	t.Helper()
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "test-app",
		Description: "An app used by the parity tests.",
		Instruction: "Be brief.",
		SubAgents: []agent.Agent{mustAgent(t, llmagent.Config{
			Name:        "helper",
			Description: "A helper.",
		})},
	})
	if err != nil {
		t.Fatalf("llmagent.New() failed: %v", err)
	}
	memoryService := &recordingMemoryService{}
	server, err := adkrest.NewServer(adkrest.ServerConfig{
		SessionService: session.InMemoryService(),
		MemoryService:  memoryService,
		AgentLoader:    agent.NewSingleLoader(rootAgent),
	})
	if err != nil {
		t.Fatalf("adkrest.NewServer() failed: %v", err)
	}
	testServer := httptest.NewServer(server)
	t.Cleanup(testServer.Close)
	return testServer, memoryService
}

func mustAgent(t *testing.T, cfg llmagent.Config) agent.Agent {
	t.Helper()
	a, err := llmagent.New(cfg)
	if err != nil {
		t.Fatalf("llmagent.New(%q) failed: %v", cfg.Name, err)
	}
	return a
}

// do issues a request against the test server and returns the status and body.
func do(t *testing.T, testServer *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, testServer.URL+path, reader)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext(%s %s) failed: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := testServer.Client().Do(req)
	if err != nil {
		t.Fatalf("client.Do(%s %s) failed: %v", method, path, err)
	}
	respBody, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		t.Fatalf("io.ReadAll(response body) failed: %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("response body Close() failed: %v", closeErr)
	}
	return resp.StatusCode, respBody
}

// TestPatchSessionRoundTrip drives create, patch and get through the real mux,
// which a controller unit test with mux.SetURLVars cannot do.
func TestPatchSessionRoundTrip(t *testing.T) {
	testServer, _ := newParityServer(t)
	const sessionPath = "/apps/test-app/users/u1/sessions/s1"

	if status, body := do(t, testServer, http.MethodPost, sessionPath, `{"state":{"seed":"one"}}`); status != http.StatusOK {
		t.Fatalf("create session status = %d, want %d; body: %s", status, http.StatusOK, body)
	}

	status, body := do(t, testServer, http.MethodPatch, sessionPath, `{"stateDelta":{"patched":"two"}}`)
	if status != http.StatusOK {
		t.Fatalf("patch session status = %d, want %d; body: %s", status, http.StatusOK, body)
	}

	status, body = do(t, testServer, http.MethodGet, sessionPath, "")
	if status != http.StatusOK {
		t.Fatalf("get session status = %d, want %d; body: %s", status, http.StatusOK, body)
	}
	var got struct {
		State  map[string]any `json:"state"`
		Events []struct {
			InvocationID string `json:"invocationId"`
			Author       string `json:"author"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json.Unmarshal(get session body) failed: %v; body: %s", err, body)
	}
	if want := "one"; got.State["seed"] != want {
		t.Errorf("session state seed = %v, want %q", got.State["seed"], want)
	}
	if want := "two"; got.State["patched"] != want {
		t.Errorf("session state patched = %v, want %q", got.State["patched"], want)
	}
	if len(got.Events) != 1 {
		t.Fatalf("session event count = %d, want 1; body: %s", len(got.Events), body)
	}
	if !strings.HasPrefix(got.Events[0].InvocationID, "p-") {
		t.Errorf("patch event invocationId = %q, want a %q prefix", got.Events[0].InvocationID, "p-")
	}
	if want := "user"; got.Events[0].Author != want {
		t.Errorf("patch event author = %q, want %q", got.Events[0].Author, want)
	}
}

func TestPatchMemoryRoundTrip(t *testing.T) {
	testServer, memoryService := newParityServer(t)

	if status, body := do(t, testServer, http.MethodPost, "/apps/test-app/users/u1/sessions/s1", ""); status != http.StatusOK {
		t.Fatalf("create session status = %d, want %d; body: %s", status, http.StatusOK, body)
	}

	status, body := do(t, testServer, http.MethodPatch, "/apps/test-app/users/u1/memory", `{"sessionId":"s1"}`)
	if status != http.StatusOK {
		t.Fatalf("patch memory status = %d, want %d; body: %s", status, http.StatusOK, body)
	}
	if len(memoryService.added) != 1 {
		t.Fatalf("memory service received %d sessions, want 1", len(memoryService.added))
	}
	if got, want := memoryService.added[0].ID(), "s1"; got != want {
		t.Errorf("remembered session ID = %q, want %q", got, want)
	}

	status, body = do(t, testServer, http.MethodPatch, "/apps/test-app/users/u1/memory", `{"sessionId":"missing"}`)
	if status != http.StatusNotFound {
		t.Fatalf("patch memory for unknown session status = %d, want %d; body: %s", status, http.StatusNotFound, body)
	}
	if len(memoryService.added) != 1 {
		t.Errorf("memory service received %d sessions after the 404, want 1", len(memoryService.added))
	}
}

func TestAppInfoRoundTrip(t *testing.T) {
	testServer, _ := newParityServer(t)

	status, body := do(t, testServer, http.MethodGet, "/apps/test-app/app-info", "")
	if status != http.StatusOK {
		t.Fatalf("app-info status = %d, want %d; body: %s", status, http.StatusOK, body)
	}
	var got struct {
		Name          string `json:"name"`
		RootAgentName string `json:"rootAgentName"`
		Language      string `json:"language"`
		Agents        map[string]struct {
			SubAgents []string `json:"sub_agents"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("json.Unmarshal(app-info body) failed: %v; body: %s", err, body)
	}
	if want := "test-app"; got.Name != want {
		t.Errorf("app-info name = %q, want %q", got.Name, want)
	}
	if want := "test-app"; got.RootAgentName != want {
		t.Errorf("app-info rootAgentName = %q, want %q", got.RootAgentName, want)
	}
	if want := "go"; got.Language != want {
		t.Errorf("app-info language = %q, want %q", got.Language, want)
	}
	if _, ok := got.Agents["helper"]; !ok {
		t.Errorf("app-info agents is missing the sub-agent; body: %s", body)
	}
	if diff := strings.Join(got.Agents["test-app"].SubAgents, ","); diff != "helper" {
		t.Errorf("app-info root sub_agents = %v, want [helper]", got.Agents["test-app"].SubAgents)
	}
}

// TestPatchSessionErrorsEndToEnd drives the PATCH error paths through the real
// mux, so a mis-registered route cannot hide them.
func TestPatchSessionErrorsEndToEnd(t *testing.T) {
	testServer, _ := newParityServer(t)

	tc := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "unknown session",
			path:       "/apps/test-app/users/u1/sessions/missing",
			body:       `{"stateDelta":{"k":"v"}}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "body carries neither spelling",
			path:       "/apps/test-app/users/u1/sessions/s1",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   "stateDelta is required",
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			status, body := do(t, testServer, http.MethodPatch, tt.path, tt.body)

			if status != tt.wantStatus {
				t.Fatalf("patch session status = %d, want %d; body: %s", status, tt.wantStatus, body)
			}
			if tt.wantBody != "" {
				if got := strings.TrimSpace(string(body)); got != tt.wantBody {
					t.Errorf("patch session body = %q, want %q", got, tt.wantBody)
				}
			}
		})
	}
}
