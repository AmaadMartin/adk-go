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

package runner

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/session"
)

type mockLiveAgent struct {
	agent.Agent
	runLiveFn func(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error)
}

func (m *mockLiveAgent) RunLive(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
	return m.runLiveFn(ctx)
}

type dummyLiveSession struct{}

func (d *dummyLiveSession) Send(req agent.LiveRequest) error { return nil }
func (d *dummyLiveSession) Close() error                     { return nil }

// recordingLiveSession captures what the runner forwards to the agent's live
// session, so a test can assert the request reaches the flow unchanged.
type recordingLiveSession struct {
	requests []agent.LiveRequest
	sendErr  error
}

func (d *recordingLiveSession) Send(req agent.LiveRequest) error {
	d.requests = append(d.requests, req)
	return d.sendErr
}

func (d *recordingLiveSession) Close() error { return nil }

// appendFailingService fails every AppendEvent, so a test can reach the
// runner's two wrapped-error paths.
type appendFailingService struct {
	session.Service
	err error
}

func (s *appendFailingService) AppendEvent(context.Context, session.Session, *session.Event) error {
	return s.err
}

// liveSendFixture drives runnerLiveSession.Send against a real session service
// and reads back what the send persisted.
type liveSendFixture struct {
	t       *testing.T
	ctx     context.Context
	sess    agent.LiveSession
	inner   *recordingLiveSession
	service session.Service
}

const (
	liveSendApp     = "liveSendApp"
	liveSendUser    = "liveSendUser"
	liveSendSession = "liveSendSession"
)

// newLiveSendFixture builds a runner whose agent hands back inner, over
// service. Pass session.InMemoryService() for the persistence assertions.
func newLiveSendFixture(t *testing.T, service session.Service, inner *recordingLiveSession) *liveSendFixture {
	t.Helper()
	ctx := t.Context()

	// The fixture always creates the session through the in-memory service, so
	// a wrapper that only overrides AppendEvent still has a session to append
	// to.
	if _, err := service.Create(ctx, &session.CreateRequest{
		AppName:   liveSendApp,
		UserID:    liveSendUser,
		SessionID: liveSendSession,
	}); err != nil {
		t.Fatalf("session Create failed: %v", err)
	}

	mockLive := &mockLiveAgent{
		Agent: must(agent.New(agent.Config{Name: "test_agent"})),
		runLiveFn: func(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
			return inner, func(yield func(*session.Event, error) bool) {}, nil
		},
	}
	r, err := New(Config{AppName: liveSendApp, Agent: mockLive, SessionService: service})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	sess, _, err := r.RunLive(ctx, liveSendUser, liveSendSession, agent.LiveRunConfig{})
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}
	return &liveSendFixture{t: t, ctx: ctx, sess: sess, inner: inner, service: service}
}

// stored returns the session as the service holds it after the sends.
func (f *liveSendFixture) stored() session.Session {
	f.t.Helper()
	getResp, err := f.service.Get(f.ctx, &session.GetRequest{
		AppName:   liveSendApp,
		UserID:    liveSendUser,
		SessionID: liveSendSession,
	})
	if err != nil {
		f.t.Fatalf("session Get failed: %v", err)
	}
	return getResp.Session
}

// stateValue returns the session state value at key, or nil when it is absent.
func (f *liveSendFixture) stateValue(key string) any {
	f.t.Helper()
	v, err := f.stored().State().Get(key)
	if err != nil {
		return nil
	}
	return v
}

// TestRunnerLiveSessionSendPersistence covers one row each of the send
// matrix: which events a single Send appends, and what it writes to state.
//
// adk-python's test_send_to_model_state_delta_with_close has no counterpart
// here. adk-go closes a live session through LiveSession.Close rather than a
// close field on the request, so there is no such request shape to test.
func TestRunnerLiveSessionSendPersistence(t *testing.T) {
	userText := genai.NewContentFromText("hello", genai.RoleUser)
	functionResponse := &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "tool"}}},
	}

	tests := []struct {
		name string
		req  agent.LiveRequest
		// wantContent is the content the single appended event must carry.
		// nil with wantEvents == 1 means the event must be content-less.
		wantContent *genai.Content
		wantEvents  int
		wantDelta   map[string]any
		// wantState is the session state expected after the send.
		wantState map[string]any
	}{
		{
			name:        "plain content appends one user event and leaves state alone",
			req:         agent.LiveRequest{Content: userText},
			wantContent: userText,
			wantEvents:  1,
			wantDelta:   map[string]any{},
			wantState:   map[string]any{},
		},
		{
			name:       "partial content appends nothing",
			req:        agent.LiveRequest{Content: userText, Partial: true},
			wantEvents: 0,
			wantState:  map[string]any{},
		},
		{
			name:       "state delta alone appends one content-less event",
			req:        agent.LiveRequest{StateDelta: &map[string]any{"ui_locale": "fr-FR"}},
			wantEvents: 1,
			wantDelta:  map[string]any{"ui_locale": "fr-FR"},
			wantState:  map[string]any{"ui_locale": "fr-FR"},
		},
		{
			name: "content and state delta share one event",
			req: agent.LiveRequest{
				Content:    userText,
				StateDelta: &map[string]any{"ui_locale": "fr-FR"},
			},
			wantContent: userText,
			wantEvents:  1,
			wantDelta:   map[string]any{"ui_locale": "fr-FR"},
			wantState:   map[string]any{"ui_locale": "fr-FR"},
		},
		{
			name: "partial content with a state delta keeps the content out of history",
			req: agent.LiveRequest{
				Content:    userText,
				Partial:    true,
				StateDelta: &map[string]any{"ui_locale": "fr-FR"},
			},
			wantEvents: 1,
			wantDelta:  map[string]any{"ui_locale": "fr-FR"},
			wantState:  map[string]any{"ui_locale": "fr-FR"},
		},
		{
			name: "function response with a state delta keeps the content out of history",
			req: agent.LiveRequest{
				Content:    functionResponse,
				StateDelta: &map[string]any{"ui_locale": "fr-FR"},
			},
			wantEvents: 1,
			wantDelta:  map[string]any{"ui_locale": "fr-FR"},
			wantState:  map[string]any{"ui_locale": "fr-FR"},
		},
		{
			name:       "audio stream end appends nothing",
			req:        agent.LiveRequest{AudioStreamEnd: true},
			wantEvents: 0,
			wantState:  map[string]any{},
		},
		{
			name:       "content with empty parts appends nothing",
			req:        agent.LiveRequest{Content: &genai.Content{Role: genai.RoleUser}},
			wantEvents: 0,
			wantState:  map[string]any{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recordingLiveSession{}
			f := newLiveSendFixture(t, session.InMemoryService(), inner)

			if err := f.sess.Send(tc.req); err != nil {
				t.Fatalf("Send failed: %v", err)
			}

			// Every request reaches the agent's live session untouched, even
			// the ones the runner does not record.
			if len(inner.requests) != 1 {
				t.Fatalf("agent live session received %d requests, want 1", len(inner.requests))
			}
			if diff := cmp.Diff(tc.req, inner.requests[0]); diff != "" {
				t.Errorf("forwarded request mismatch (-want +got):\n%s", diff)
			}

			stored := f.stored()
			if got := stored.Events().Len(); got != tc.wantEvents {
				t.Fatalf("appended %d events, want %d", got, tc.wantEvents)
			}
			for key, want := range tc.wantState {
				if got := f.stateValue(key); got != want {
					t.Errorf("state[%q] = %v, want %v", key, got, want)
				}
			}
			if len(tc.wantState) == 0 {
				for key := range stored.State().All() {
					t.Errorf("state gained key %q, want no state change", key)
				}
			}
			if tc.wantEvents == 0 {
				return
			}

			event := stored.Events().At(0)
			if event.Author != "user" {
				t.Errorf("event author = %q, want %q", event.Author, "user")
			}
			if diff := cmp.Diff(tc.wantContent, event.LLMResponse.Content); diff != "" {
				t.Errorf("event content mismatch (-want +got):\n%s", diff)
			}
			// An allocated-but-empty StateDelta must not become nil: the two
			// encode differently, and adk-python rejects a null one.
			if event.Actions.StateDelta == nil {
				t.Fatal("event Actions.StateDelta is nil, want an allocated map")
			}
			if diff := cmp.Diff(tc.wantDelta, event.Actions.StateDelta); diff != "" {
				t.Errorf("event state delta mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunnerLiveSessionSendReturnsAgentError(t *testing.T) {
	wantErr := errors.New("live session is down")
	inner := &recordingLiveSession{sendErr: wantErr}
	f := newLiveSendFixture(t, session.InMemoryService(), inner)

	err := f.sess.Send(agent.LiveRequest{
		Content:    genai.NewContentFromText("hello", genai.RoleUser),
		StateDelta: &map[string]any{"ui_locale": "fr-FR"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Send error = %v, want %v", err, wantErr)
	}
	// A request the agent rejected never reaches the session.
	if got := f.stored().Events().Len(); got != 0 {
		t.Errorf("appended %d events after a failed send, want 0", got)
	}
}

func TestRunnerLiveSessionSendWrapsAppendFailure(t *testing.T) {
	wantErr := errors.New("append refused")
	tests := []struct {
		name       string
		req        agent.LiveRequest
		wantPrefix string
	}{
		{
			name:       "user content event",
			req:        agent.LiveRequest{Content: genai.NewContentFromText("hello", genai.RoleUser)},
			wantPrefix: "failed to add user event to session",
		},
		{
			name:       "standalone state delta event",
			req:        agent.LiveRequest{StateDelta: &map[string]any{"ui_locale": "fr-FR"}},
			wantPrefix: "failed to add state delta event to session",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &appendFailingService{Service: session.InMemoryService(), err: wantErr}
			f := newLiveSendFixture(t, service, &recordingLiveSession{})

			err := f.sess.Send(tc.req)
			if !errors.Is(err, wantErr) {
				t.Fatalf("Send error = %v, want it to wrap %v", err, wantErr)
			}
			if !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Errorf("Send error = %q, want it to start with %q", err.Error(), tc.wantPrefix)
			}
		})
	}
}

func TestRunner_RunLive_Callbacks(t *testing.T) {
	ctx := t.Context()
	appName, userID, sessionID := "testApp", "testUser", "testSession"

	sessionService := session.InMemoryService()
	_, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}

	var beforeRunCalled, afterRunCalled bool

	p, err := plugin.New(plugin.Config{
		Name: "test_plugin",
		BeforeRunCallback: func(ctx agent.InvocationContext) (*genai.Content, error) {
			beforeRunCalled = true
			return nil, nil
		},
		AfterRunCallback: func(ctx agent.InvocationContext) {
			afterRunCalled = true
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	testAgent := must(agent.New(agent.Config{Name: "test_agent"}))
	mockLive := &mockLiveAgent{
		Agent: testAgent,
		runLiveFn: func(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
			return &dummyLiveSession{}, func(yield func(*session.Event, error) bool) {
				yield(session.NewEvent(ctx, ctx.InvocationID()), nil)
			}, nil
		},
	}

	r, err := New(Config{
		AppName:        appName,
		Agent:          mockLive,
		SessionService: sessionService,
		PluginConfig: PluginConfig{
			Plugins: []*plugin.Plugin{p},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sess, iter, err := r.RunLive(ctx, userID, sessionID, agent.LiveRunConfig{})
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}
	if sess == nil {
		t.Fatal("expected LiveSession to be returned")
	}

	if !beforeRunCalled {
		t.Error("BeforeRunCallback was not called before starting RunLive")
	}

	if afterRunCalled {
		t.Error("AfterRunCallback should not be called until iterator is consumed")
	}

	for _, err := range iter {
		if err != nil {
			t.Fatal(err)
		}
	}

	if !afterRunCalled {
		t.Error("AfterRunCallback was not called after iterator was consumed")
	}
}

func TestRunner_RunLive_EarlyExit(t *testing.T) {
	ctx := t.Context()
	appName, userID, sessionID := "testApp", "testUser", "testSession2"

	sessionService := session.InMemoryService()
	_, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}

	expectedContent := genai.NewContentFromText("early exit content", "")

	p, err := plugin.New(plugin.Config{
		Name: "test_plugin",
		BeforeRunCallback: func(ctx agent.InvocationContext) (*genai.Content, error) {
			return expectedContent, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	testAgent := must(agent.New(agent.Config{Name: "test_agent"}))
	var runLiveCalled bool
	mockLive := &mockLiveAgent{
		Agent: testAgent,
		runLiveFn: func(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
			runLiveCalled = true
			return &dummyLiveSession{}, func(yield func(*session.Event, error) bool) {}, nil
		},
	}

	r, err := New(Config{
		AppName:        appName,
		Agent:          mockLive,
		SessionService: sessionService,
		PluginConfig: PluginConfig{
			Plugins: []*plugin.Plugin{p},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sess, iter, err := r.RunLive(ctx, userID, sessionID, agent.LiveRunConfig{})
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}
	if runLiveCalled {
		t.Error("RunLive should not have been called on the agent due to early exit")
	}

	var events []*session.Event
	for ev, err := range iter {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].LLMResponse.Content != expectedContent {
		t.Errorf("expected content %v, got %v", expectedContent, events[0].LLMResponse.Content)
	}

	err = sess.Send(agent.LiveRequest{})
	if err == nil || !strings.Contains(err.Error(), "session is closed") {
		t.Errorf("expected error 'session is closed' when sending to early exited session, got %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}

func TestRunner_RunLive_ChronologicalBuffering(t *testing.T) {
	ctx := t.Context()
	appName, userID, sessionID := "testApp", "testUser", "testSession3"

	sessionService := session.InMemoryService()
	_, err := sessionService.Create(ctx, &session.CreateRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}

	testAgent := must(agent.New(agent.Config{Name: "test_agent"}))
	mockLive := &mockLiveAgent{
		Agent: testAgent,
		runLiveFn: func(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
			return &dummyLiveSession{}, func(yield func(*session.Event, error) bool) {
				// 1. Partial Transcription
				ev1 := session.NewEvent(ctx, ctx.InvocationID())
				ev1.LLMResponse.Partial = true
				ev1.LLMResponse.OutputTranscription = &genai.Transcription{Text: "Hello"}
				if !yield(ev1, nil) {
					return
				}

				// 2. Function Call (happening during transcription)
				ev2 := session.NewEvent(ctx, ctx.InvocationID())
				ev2.LLMResponse.Content = &genai.Content{
					Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "test_func"}}},
				}
				if !yield(ev2, nil) {
					return
				}

				// 3. Final Transcription
				ev3 := session.NewEvent(ctx, ctx.InvocationID())
				ev3.LLMResponse.OutputTranscription = &genai.Transcription{Text: "Hello there."}
				if !yield(ev3, nil) {
					return
				}
			}, nil
		},
	}

	r, err := New(Config{
		AppName:        appName,
		Agent:          mockLive,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, iter, err := r.RunLive(ctx, userID, sessionID, agent.LiveRunConfig{})
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}

	// Consume iterator to execute everything
	for _, err := range iter {
		if err != nil {
			t.Fatal(err)
		}
	}

	// Verify Session History
	getResp, err := sessionService.Get(ctx, &session.GetRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := getResp.Session.Events()
	// We expect 2 saved events: Final Transcription first, Function Call second.
	// (Partial Transcription is not saved).
	if events.Len() != 2 {
		t.Fatalf("expected 2 saved events in session, got %d", events.Len())
	}

	// First saved event should be the final transcription
	if events.At(0).LLMResponse.OutputTranscription == nil {
		t.Errorf("expected first saved event to be transcription, but got %v", events.At(0))
	}

	if events.At(0).LLMResponse.OutputTranscription.Text != "Hello there." {
		t.Errorf("expected first saved event to be transcription with text: %q, got: %q", "Hello there.", events.At(0).LLMResponse.OutputTranscription.Text)
	}

	// Second saved event should be the function call
	if events.At(1).LLMResponse.Content == nil || events.At(1).LLMResponse.Content.Parts[0].FunctionCall == nil {
		t.Errorf("expected second saved event to be function call, but got %v", events.At(1))
	}
}
