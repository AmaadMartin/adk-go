// Copyright 2025 Google LLC
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

package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/server/adkrest/internal/fakes"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/session"
)

func TestNewRuntimeAPIController_PluginsAssignment(t *testing.T) {
	p1, err := plugin.New(plugin.Config{Name: "plugin1"})
	if err != nil {
		t.Fatalf("plugin.New() failed for plugin1: %v", err)
	}

	p2, err := plugin.New(plugin.Config{Name: "plugin2"})
	if err != nil {
		t.Fatalf("plugin.New() failed for plugin2: %v", err)
	}

	tc := []struct {
		name        string
		plugins     []*plugin.Plugin
		wantPlugins int
	}{
		{
			name:        "with no plugins",
			plugins:     nil,
			wantPlugins: 0,
		},
		{
			name:        "with empty plugin list",
			plugins:     []*plugin.Plugin{},
			wantPlugins: 0,
		},
		{
			name:        "with single plugin",
			plugins:     []*plugin.Plugin{p1},
			wantPlugins: 1,
		},
		{
			name:        "with multiple plugins",
			plugins:     []*plugin.Plugin{p1, p2},
			wantPlugins: 2,
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			controller := NewRuntimeAPIController(nil, nil, nil, nil, 10*time.Second, runner.PluginConfig{
				Plugins: tt.plugins,
			}, false)

			if controller == nil {
				t.Fatal("NewRuntimeAPIController returned nil")
			}

			if got := len(controller.pluginConfig.Plugins); got != tt.wantPlugins {
				t.Errorf("NewRuntimeAPIController() plugins count = %v, want %v", got, tt.wantPlugins)
			}
		})
	}
}

// recorderWithDeadline records every SetWriteDeadline call, so a test can
// assert whether the handler armed a deadline, and can make the call fail.
type recorderWithDeadline struct {
	*httptest.ResponseRecorder

	deadlines   []time.Time
	deadlineErr error
}

func (r *recorderWithDeadline) SetWriteDeadline(t time.Time) error {
	r.deadlines = append(r.deadlines, t)
	return r.deadlineErr
}

type testAgentResult struct {
	event *session.Event
	err   error
}

func testAgent(results []testAgentResult) func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			for _, res := range results {
				if !yield(res.event, res.err) {
					return
				}
			}
		}
	}
}

func makeEvent(id, author, text string) *session.Event {
	e := session.NewEvent(context.Background(), id)
	e.Author = author
	e.LLMResponse.Content = &genai.Content{
		Parts: []*genai.Part{{Text: text}},
	}
	return e
}

func TestRunSSEHandler(t *testing.T) {
	tc := []struct {
		name       string
		results    []testAgentResult
		wantStatus int
		wantBody   []string
	}{
		{
			name: "success case",
			results: []testAgentResult{
				{event: makeEvent("invocation-1", "testApp", "Hello from agent"), err: nil},
			},
			wantStatus: http.StatusOK,
			wantBody:   []string{"data: {", "Hello from agent"},
		},
		{
			name: "error case",
			results: []testAgentResult{
				{err: fmt.Errorf("agent failed")},
			},
			wantStatus: http.StatusOK,
			wantBody:   []string{"event: error\ndata: {\"error\":\"agent failed\"}\n\n"},
		},
		{
			name: "interleaved success and error",
			results: []testAgentResult{
				{event: makeEvent("invocation-1", "testApp", "Hello from agent"), err: nil},
				{err: fmt.Errorf("agent failed")},
				{event: makeEvent("invocation-1", "testApp", "More data"), err: nil},
				{err: fmt.Errorf("agent failed again")},
			},
			wantStatus: http.StatusOK,
			wantBody: []string{
				"data: {", "Hello from agent",
				"event: error\ndata: {\"error\":\"agent failed\"}\n\n",
				"data: {", "More data",
				"event: error\ndata: {\"error\":\"agent failed again\"}\n\n",
			},
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			controller := newRunSSEController(t, tt.results, 10*time.Second)

			// Record response
			rr := httptest.NewRecorder()
			w := &recorderWithDeadline{ResponseRecorder: rr}

			// Call handler
			controller.RunSSEHandler(w, newRunSSERequest(t))

			// Verify response
			if rr.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, rr.Code)
			}

			body := rr.Body.String()
			for _, s := range tt.wantBody {
				if !strings.Contains(body, s) {
					t.Errorf("expected body to contain %q, got %s", s, body)
				}
			}
		})
	}
}

// TestRunSSEHandler_WriteDeadline pins how RunSSEHandler reads the configured
// SSE write timeout. Only a positive timeout may arm a write deadline.
func TestRunSSEHandler_WriteDeadline(t *testing.T) {
	tc := []struct {
		name          string
		sseTimeout    time.Duration
		deadlineErr   error
		wantDeadlines int
		wantStatus    int
		wantBody      string
	}{
		{
			name:          "zero timeout arms no deadline",
			sseTimeout:    0,
			wantDeadlines: 0,
			wantStatus:    http.StatusOK,
			wantBody:      "data: {",
		},
		{
			name:          "negative timeout arms no deadline",
			sseTimeout:    -time.Second,
			wantDeadlines: 0,
			wantStatus:    http.StatusOK,
			wantBody:      "data: {",
		},
		{
			name:          "positive timeout arms one deadline",
			sseTimeout:    10 * time.Second,
			wantDeadlines: 1,
			wantStatus:    http.StatusOK,
			wantBody:      "data: {",
		},
		{
			name:          "deadline error returns 500",
			sseTimeout:    10 * time.Second,
			deadlineErr:   errors.New("deadlines are not supported"),
			wantDeadlines: 1,
			wantStatus:    http.StatusInternalServerError,
			wantBody:      "failed to set write deadline",
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			results := []testAgentResult{{event: makeEvent("invocation-1", "testApp", "Hello from agent")}}
			controller := newRunSSEController(t, results, tt.sseTimeout)
			rr := httptest.NewRecorder()
			w := &recorderWithDeadline{ResponseRecorder: rr, deadlineErr: tt.deadlineErr}

			start := time.Now()
			controller.RunSSEHandler(w, newRunSSERequest(t))

			if got := len(w.deadlines); got != tt.wantDeadlines {
				t.Errorf("SetWriteDeadline call count = %d, want %d", got, tt.wantDeadlines)
			}
			for _, deadline := range w.deadlines {
				if !deadline.After(start) {
					t.Errorf("write deadline = %v, want a time after %v", deadline, start)
				}
			}
			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
			if body := rr.Body.String(); !strings.Contains(body, tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", body, tt.wantBody)
			}
		})
	}
}

// newRunSSEController builds a controller whose agent yields results, backed
// by a fake session service holding the testApp/testUser/testSession session.
func newRunSSEController(t *testing.T, results []testAgentResult, sseTimeout time.Duration) *RuntimeAPIController {
	t.Helper()
	fakeAgent, err := agent.New(agent.Config{
		Name: "testApp",
		Run:  testAgent(results),
	})
	if err != nil {
		t.Fatalf("agent.New failed: %v", err)
	}

	id := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "testSession",
	}
	sessionService := fakes.FakeSessionService{
		Sessions: map[fakes.SessionKey]fakes.TestSession{
			id: {
				Id:            id,
				SessionState:  fakes.TestState{},
				SessionEvents: fakes.TestEvents{},
				UpdatedAt:     time.Now(),
			},
		},
	}

	return NewRuntimeAPIController(
		&sessionService,
		nil,
		agent.NewSingleLoader(fakeAgent),
		nil,
		sseTimeout,
		runner.PluginConfig{},
		false,
	)
}

// newRunSSERequest builds a /run_sse request for the session that
// newRunSSEController creates.
func newRunSSERequest(t *testing.T) *http.Request {
	t.Helper()
	reqBytes, err := json.Marshal(models.RunAgentRequest{
		AppName:   "testApp",
		UserId:    "testUser",
		SessionId: "testSession",
		Streaming: true,
		NewMessage: genai.Content{
			Parts: []*genai.Part{{Text: "Hello"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal RunAgentRequest failed: %v", err)
	}
	return httptest.NewRequest(http.MethodPost, "/run-sse", bytes.NewBuffer(reqBytes))
}

func TestDecodeRequestBody_AcceptsFunctionCallEventID(t *testing.T) {
	body := `{
		"appName": "a",
		"userId": "u",
		"sessionId": "s",
		"newMessage": {"role": "user", "parts": [{"text": "hi"}]},
		"functionCallEventId": "fce-1"
	}`
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(body))

	got, err := decodeRequestBody(req)
	if err != nil {
		t.Fatalf("decodeRequestBody: unexpected error: %v", err)
	}
	if got.FunctionCallEventID == nil || *got.FunctionCallEventID != "fce-1" {
		t.Errorf("FunctionCallEventID = %v, want %q", got.FunctionCallEventID, "fce-1")
	}
}

func TestDecodeRequestBody_RejectsUnknownFields(t *testing.T) {
	body := `{
		"appName": "a",
		"userId": "u",
		"sessionId": "s",
		"newMessage": {},
		"totallyMadeUpField": 123
	}`
	req := httptest.NewRequest(http.MethodPost, "/run", bytes.NewBufferString(body))

	if _, err := decodeRequestBody(req); err == nil {
		t.Errorf("decodeRequestBody: expected error for unknown field, got nil")
	}
}
