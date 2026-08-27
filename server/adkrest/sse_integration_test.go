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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/server/adkrest"
	"google.golang.org/adk/v2/session"
)

const (
	sseApp   = "sse_default_config"
	sseUser  = "u"
	sseReply = "Hello from the agent"
)

// TestRunSSEStreamsWithDefaultServerConfig streams /run_sse from a server
// built with the default [adkrest.ServerConfig].
//
// SSEWriteTimeout stays unset on purpose. Do not set it here: an unset
// timeout used to arm a write deadline of now+0, which expired before the
// first byte and dropped the connection. Only a real listener shows this,
// so the test runs over [httptest.NewServer] rather than a recorder.
func TestRunSSEStreamsWithDefaultServerConfig(t *testing.T) {
	testServer := httptest.NewServer(newSSEServer(t))
	t.Cleanup(testServer.Close)

	sid := createSSESession(t, testServer.URL)
	body, err := json.Marshal(map[string]any{
		"appName":    sseApp,
		"userId":     sseUser,
		"sessionId":  sid,
		"streaming":  true,
		"newMessage": genai.NewContentFromText("hi", genai.RoleUser),
	})
	if err != nil {
		t.Fatalf("marshal /run_sse request: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, testServer.URL+"/run_sse", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := testServer.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /run_sse failed: %v; an unset SSEWriteTimeout must not arm an expired write deadline", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got, want := resp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("POST /run_sse status = %d, want %d", got, want)
	}
	if got, want := resp.Header.Get("Content-Type"), "text/event-stream"; got != want {
		t.Errorf("POST /run_sse Content-Type = %q, want %q", got, want)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /run_sse body: %v", err)
	}
	stream := string(raw)
	if !strings.Contains(stream, "data: ") {
		t.Errorf("/run_sse body has no data frame; got %q", stream)
	}
	if !strings.Contains(stream, sseReply) {
		t.Errorf("/run_sse body does not carry the agent text %q; got %q", sseReply, stream)
	}
}

// newSSEServer builds the REST handler around an agent that yields one
// text event, with every [adkrest.ServerConfig] timeout left at its
// default.
func newSSEServer(t *testing.T) *adkrest.Server {
	t.Helper()
	a, err := agent.New(agent.Config{
		Name: sseApp,
		Run: func(ic agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				e := session.NewEvent(ic, ic.InvocationID())
				e.Author = sseApp
				e.LLMResponse.Content = genai.NewContentFromText(sseReply, genai.RoleModel)
				yield(e, nil)
			}
		},
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	srv, err := adkrest.NewServer(adkrest.ServerConfig{
		SessionService: session.InMemoryService(),
		AgentLoader:    agent.NewSingleLoader(a),
	})
	if err != nil {
		t.Fatalf("adkrest.NewServer() error = %v", err)
	}
	return srv
}

func createSSESession(t *testing.T, baseURL string) string {
	t.Helper()
	var resp struct {
		ID string `json:"id"`
	}
	postJSON(t, fmt.Sprintf("%s/apps/%s/users/%s/sessions", baseURL, sseApp, sseUser), map[string]any{}, &resp)
	if resp.ID == "" {
		t.Fatal("create session returned an empty ID")
	}
	return resp.ID
}
