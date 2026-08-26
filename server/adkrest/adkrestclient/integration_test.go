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

package adkrestclient_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/server/adkrest"
	"google.golang.org/adk/v2/server/adkrest/adkrestclient"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
)

const (
	integrationApp  = "greeter"
	integrationUser = "u1"
)

// TestClient_RoundTripAgainstRealServer drives a real adkrest.Server through
// the client: create a session, run the agent, read the session back, then
// delete it. It is the proof that the client speaks the server's wire format.
func TestClient_RoundTripAgainstRealServer(t *testing.T) {
	srv := httptest.NewServer(newGreeterServer(t))
	defer srv.Close()

	ctx := context.Background()
	c, err := adkrestclient.New(adkrestclient.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer c.Close()

	created, err := c.CreateSession(ctx, integrationApp, integrationUser, map[string]any{"greeting": "Hi"})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateSession() returned an empty session ID")
	}
	if got := created.State["greeting"]; got != "Hi" {
		t.Errorf("CreateSession() state[greeting] = %v, want %q", got, "Hi")
	}
	if created.AppName != integrationApp || created.UserID != integrationUser {
		t.Errorf("CreateSession() = app %q user %q, want %q / %q",
			created.AppName, created.UserID, integrationApp, integrationUser)
	}

	req := adkrestclient.RunRequest{
		AppName:    integrationApp,
		UserID:     integrationUser,
		SessionID:  created.ID,
		NewMessage: genai.NewContentFromText("Wojtek", genai.RoleUser),
	}
	var outputs []string
	for event, err := range c.RunAgent(ctx, req) {
		if err != nil {
			t.Fatalf("RunAgent() error = %v", err)
		}
		if s, ok := event.Output.(string); ok {
			outputs = append(outputs, s)
		}
	}
	if want := "Hello, Wojtek!"; len(outputs) == 0 || outputs[len(outputs)-1] != want {
		t.Fatalf("RunAgent() outputs = %v, want the last one to be %q", outputs, want)
	}

	fetched, err := c.GetSession(ctx, integrationApp, integrationUser, created.ID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if fetched.ID != created.ID {
		t.Errorf("GetSession() ID = %q, want %q", fetched.ID, created.ID)
	}
	if len(fetched.Events) == 0 {
		t.Error("GetSession() returned no events; the run should have been persisted")
	}
	if fetched.LastUpdateTime == 0 {
		t.Error("GetSession() LastUpdateTime = 0, want the server's update time")
	}

	if err := c.DeleteSession(ctx, integrationApp, integrationUser, created.ID); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, err := c.GetSession(ctx, integrationApp, integrationUser, created.ID); err == nil {
		t.Error("GetSession() after delete returned no error, want a not-found error")
	}
}

// TestClient_RunAgentAgainstRealServerRejectsUnknownApp proves the client
// surfaces a server-side failure that arrives as a streamed error payload.
func TestClient_RunAgentAgainstRealServerRejectsUnknownApp(t *testing.T) {
	srv := httptest.NewServer(newGreeterServer(t))
	defer srv.Close()

	ctx := context.Background()
	c, err := adkrestclient.New(adkrestclient.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer c.Close()

	created, err := c.CreateSession(ctx, integrationApp, integrationUser, nil)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	req := adkrestclient.RunRequest{
		AppName:    "no_such_app",
		UserID:     integrationUser,
		SessionID:  created.ID,
		NewMessage: genai.NewContentFromText("Wojtek", genai.RoleUser),
	}
	var gotErr error
	for event, err := range c.RunAgent(ctx, req) {
		if err != nil {
			gotErr = err
			break
		}
		t.Errorf("RunAgent() yielded event %+v, want only an error", event)
	}
	if gotErr == nil {
		t.Fatal("RunAgent() with an unknown app returned no error")
	}
}

// newGreeterServer builds the REST handler around a workflow agent that needs
// no LLM: it greets whatever text the user sent.
func newGreeterServer(t *testing.T) *adkrest.Server {
	t.Helper()
	greet := workflow.NewFunctionNode("greet",
		func(ic agent.Context, _ any) (string, error) {
			name := "stranger"
			for _, part := range ic.UserContent().Parts {
				if text := strings.TrimSpace(part.Text); text != "" {
					name = text
				}
			}
			return "Hello, " + name + "!", nil
		},
		workflow.NodeConfig{},
	)
	a, err := workflowagent.New(workflowagent.Config{
		Name:  integrationApp,
		Edges: workflow.Chain(workflow.Start, greet),
	})
	if err != nil {
		t.Fatalf("workflowagent.New() error = %v", err)
	}

	srv, err := adkrest.NewServer(adkrest.ServerConfig{
		SessionService: session.InMemoryService(),
		AgentLoader:    agent.NewSingleLoader(a),
		// The zero value makes the server set an already-expired SSE write
		// deadline, which closes /run_sse before it streams anything.
		SSEWriteTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("adkrest.NewServer() error = %v", err)
	}
	return srv
}
