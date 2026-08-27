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

const (
	liveTestApp  = "testApp"
	liveTestUser = "testUser"
)

type liveRunLiveFn = func(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error)

// appendFailingService is an in-memory session service whose AppendEvent always
// fails, so the state-delta append error path is reachable.
type appendFailingService struct {
	session.Service
	err error
}

func (s *appendFailingService) AppendEvent(context.Context, session.Session, *session.Event) error {
	return s.err
}

// newLiveTestRunner creates sessionID in service and returns a runner over a
// mockLiveAgent driven by runLiveFn.
func newLiveTestRunner(t *testing.T, service session.Service, sessionID string, runLiveFn liveRunLiveFn, plugins ...*plugin.Plugin) *Runner {
	t.Helper()
	if _, err := service.Create(t.Context(), &session.CreateRequest{
		AppName:   liveTestApp,
		UserID:    liveTestUser,
		SessionID: sessionID,
	}); err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{
		AppName: liveTestApp,
		Agent: &mockLiveAgent{
			Agent:     must(agent.New(agent.Config{Name: "test_agent"})),
			runLiveFn: runLiveFn,
		},
		SessionService: service,
		PluginConfig:   PluginConfig{Plugins: plugins},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// emitOneEvent is a runLiveFn that yields a single bare event.
func emitOneEvent(ctx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
	return &dummyLiveSession{}, func(yield func(*session.Event, error) bool) {
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ev.LLMResponse.Content = genai.NewContentFromText("hi", genai.RoleModel)
		yield(ev, nil)
	}, nil
}

// storedEvents drains events and returns the session as persisted.
func storedEvents(t *testing.T, service session.Service, sessionID string) session.Events {
	t.Helper()
	getResp, err := service.Get(t.Context(), &session.GetRequest{
		AppName:   liveTestApp,
		UserID:    liveTestUser,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return getResp.Session.Events()
}

func drain(t *testing.T, events iter.Seq2[*session.Event, error]) {
	t.Helper()
	for _, err := range events {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunner_RunLive_StateDeltaSeedsSessionBeforeAgentRuns(t *testing.T) {
	ctx := t.Context()
	sessionID := "testSessionStateDelta"
	sessionService := session.InMemoryService()

	var seenByAgent any
	r := newLiveTestRunner(t, sessionService, sessionID, func(ictx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
		// Read inside the agent: this proves the delta lands before it starts.
		seenByAgent, _ = ictx.Session().State().Get("tenant")
		return emitOneEvent(ictx)
	})

	_, events, err := r.RunLive(ctx, liveTestUser, sessionID, agent.LiveRunConfig{},
		WithStateDelta(map[string]any{"tenant": "acme"}))
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}
	if seenByAgent != "acme" {
		t.Errorf("agent saw state tenant=%v, want %q", seenByAgent, "acme")
	}
	drain(t, events)

	getResp, err := sessionService.Get(ctx, &session.GetRequest{
		AppName:   liveTestApp,
		UserID:    liveTestUser,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := getResp.Session.State().Get("tenant")
	if err != nil {
		t.Fatalf("persisted state Get(tenant) failed: %v", err)
	}
	if got != "acme" {
		t.Errorf("persisted state tenant=%v, want %q", got, "acme")
	}

	persisted := getResp.Session.Events()
	// One state event plus the single event the agent emitted.
	if persisted.Len() != 2 {
		t.Fatalf("persisted %d events, want 2", persisted.Len())
	}
	stateEvent := persisted.At(0)
	if stateEvent.Author != "user" {
		t.Errorf("state event Author=%q, want %q", stateEvent.Author, "user")
	}
	if stateEvent.LLMResponse.Content != nil {
		t.Errorf("state event carries content %v, want nil", stateEvent.LLMResponse.Content)
	}
	if stateEvent.Actions.StateDelta["tenant"] != "acme" {
		t.Errorf("state event delta tenant=%v, want %q", stateEvent.Actions.StateDelta["tenant"], "acme")
	}
	if stateEvent.IsolationScope != "" {
		t.Errorf("state event IsolationScope=%q, want empty", stateEvent.IsolationScope)
	}
}

func TestRunner_RunLive_StateDeltaVisibleToBeforeRunCallback(t *testing.T) {
	ctx := t.Context()
	sessionID := "testSessionStateDeltaPlugin"

	var seenByCallback any
	p, err := plugin.New(plugin.Config{
		Name: "state_reader",
		BeforeRunCallback: func(ictx agent.InvocationContext) (*genai.Content, error) {
			seenByCallback, _ = ictx.Session().State().Get("tenant")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	r := newLiveTestRunner(t, session.InMemoryService(), sessionID, emitOneEvent, p)

	_, events, err := r.RunLive(ctx, liveTestUser, sessionID, agent.LiveRunConfig{},
		WithStateDelta(map[string]any{"tenant": "acme"}))
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}
	drain(t, events)

	if seenByCallback != "acme" {
		t.Errorf("BeforeRunCallback saw state tenant=%v, want %q", seenByCallback, "acme")
	}
}

func TestRunner_RunLive_YieldUserMessageRejected(t *testing.T) {
	ctx := t.Context()
	sessionID := "testSessionYieldUserMessage"
	sessionService := session.InMemoryService()

	var runLiveCalled bool
	r := newLiveTestRunner(t, sessionService, sessionID, func(ictx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
		runLiveCalled = true
		return emitOneEvent(ictx)
	})

	sess, events, err := r.RunLive(ctx, liveTestUser, sessionID, agent.LiveRunConfig{}, WithYieldUserMessage())
	if err == nil {
		t.Fatal("RunLive succeeded, want an error for WithYieldUserMessage")
	}
	if !strings.Contains(err.Error(), "WithYieldUserMessage") {
		t.Errorf("error %q does not name WithYieldUserMessage", err)
	}
	if sess != nil {
		t.Errorf("RunLive returned session %v, want nil", sess)
	}
	if events != nil {
		t.Error("RunLive returned an event iterator, want nil")
	}
	if runLiveCalled {
		t.Error("the agent's RunLive ran, want no side effect from a rejected call")
	}
	if n := storedEvents(t, sessionService, sessionID).Len(); n != 0 {
		t.Errorf("session holds %d events, want 0", n)
	}
}

func TestRunner_RunLive_WithoutStateDeltaAppendsNoExtraEvent(t *testing.T) {
	tests := []struct {
		name string
		opts []RunOption
	}{
		{name: "no options"},
		{name: "nil delta", opts: []RunOption{WithStateDelta(nil)}},
		{name: "empty delta", opts: []RunOption{WithStateDelta(map[string]any{})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			sessionID := "testSessionNoDelta"
			sessionService := session.InMemoryService()
			r := newLiveTestRunner(t, sessionService, sessionID, emitOneEvent)

			_, events, err := r.RunLive(ctx, liveTestUser, sessionID, agent.LiveRunConfig{}, tt.opts...)
			if err != nil {
				t.Fatalf("RunLive failed: %v", err)
			}
			drain(t, events)

			persisted := storedEvents(t, sessionService, sessionID)
			// Only the event the agent emitted: no state event was appended.
			if persisted.Len() != 1 {
				t.Fatalf("persisted %d events, want 1", persisted.Len())
			}
			if got := persisted.At(0).Author; got == "user" {
				t.Errorf("persisted a %q-authored event, want none", got)
			}
		})
	}
}

func TestRunner_RunLive_StateDeltaAppendFailureIsReturned(t *testing.T) {
	ctx := t.Context()
	sessionID := "testSessionAppendFails"
	appendErr := errors.New("boom")
	sessionService := &appendFailingService{Service: session.InMemoryService(), err: appendErr}

	var runLiveCalled bool
	r := newLiveTestRunner(t, sessionService, sessionID, func(ictx agent.InvocationContext) (agent.LiveSession, iter.Seq2[*session.Event, error], error) {
		runLiveCalled = true
		return emitOneEvent(ictx)
	})

	sess, events, err := r.RunLive(ctx, liveTestUser, sessionID, agent.LiveRunConfig{},
		WithStateDelta(map[string]any{"tenant": "acme"}))
	if err == nil {
		t.Fatal("RunLive succeeded, want the AppendEvent error")
	}
	if !errors.Is(err, appendErr) {
		t.Errorf("error %q does not wrap the AppendEvent error", err)
	}
	if sess != nil || events != nil {
		t.Errorf("RunLive returned (%v, non-nil iterator=%t), want (nil, nil)", sess, events != nil)
	}
	if runLiveCalled {
		t.Error("the agent's RunLive ran after the state delta failed to apply")
	}
}
