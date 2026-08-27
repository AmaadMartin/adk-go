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

package agentengine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/session"
)

// TestNewHandlerZeroSSEWriteTimeout drives the exported handler over a real
// listener with a zero SSE write timeout, the configuration that dropped every
// connection before the fix. The agent answers from a BeforeAgent callback, so
// the test makes no model call.
func TestNewHandlerZeroSSEWriteTimeout(t *testing.T) {
	a, err := llmagent.New(llmagent.Config{
		Name: "Echo",
		BeforeAgentCallbacks: []agent.BeforeAgentCallback{
			func(cc agent.Context) (*genai.Content, error) {
				return cc.UserContent(), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("llmagent.New() failed: %v", err)
	}

	config := &launcher.Config{
		AgentLoader:    agent.NewSingleLoader(a),
		SessionService: session.InMemoryService(),
	}
	h, err := NewHandler(config, 0, 1<<20, "123")
	if err != nil {
		t.Fatalf("NewHandler() failed: %v", err)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()

	body := strings.NewReader(`{"class_method":"async_stream_query","input":{"message":"Say hello","user_id":"u"}}`)
	resp, err := http.Post(srv.URL+"/stream_reasoning_engine", "application/json", body)
	if err != nil {
		t.Fatalf("http.Post() failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() failed: %v", err)
	}
	if !strings.Contains(string(got), "Say hello") {
		t.Errorf("body = %q, want it to contain %q", got, "Say hello")
	}
}
