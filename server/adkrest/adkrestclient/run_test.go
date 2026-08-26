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

package adkrestclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/session"
)

// sseServer serves body verbatim on /run_sse and records the request body.
func sseServer(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var received string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		received = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &received
}

func testRunRequest() RunRequest {
	return RunRequest{
		AppName:    "test_app",
		UserID:     "test_user",
		SessionID:  "test_session",
		NewMessage: genai.NewContentFromText("hello", genai.RoleUser),
	}
}

// collect drains an event stream into its events and its terminating error.
func collect(seq func(func(*session.Event, error) bool)) ([]*session.Event, error) {
	var events []*session.Event
	for event, err := range seq {
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
	return events, nil
}

func TestRunAgent_StreamsEvents(t *testing.T) {
	srv, _ := sseServer(t, "data: {\"id\":\"e1\",\"author\":\"agent\"}\n\ndata:\n\ndata: {\"id\":\"e2\",\"author\":\"agent\"}\n\n")

	events, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), testRunRequest()))
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	if events[0].ID != "e1" || events[1].ID != "e2" {
		t.Errorf("event IDs = %q, %q, want e1, e2", events[0].ID, events[1].ID)
	}
}

func TestRunAgent_StreamsFinalLineWithoutNewline(t *testing.T) {
	srv, _ := sseServer(t, "data: {\"id\":\"e1\",\"author\":\"agent\"}")

	events, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), testRunRequest()))
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if len(events) != 1 || events[0].ID != "e1" {
		t.Fatalf("events = %+v, want one event e1", events)
	}
}

func TestRunAgent_RaisesOnStreamedError(t *testing.T) {
	srv, _ := sseServer(t, "data: {\"error\": \"boom\"}\n\n")

	events, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), testRunRequest()))
	if err == nil {
		t.Fatal("RunAgent() error = nil, want the streamed error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to contain %q", err, "boom")
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want none: %+v", len(events), events)
	}
}

func TestRunAgent_IgnoresEventErrorLine(t *testing.T) {
	srv, _ := sseServer(t, "event: error\ndata: {\"error\":\"boom\"}\n\n")

	events, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), testRunRequest()))
	if err == nil {
		t.Fatal("RunAgent() error = nil, want the streamed error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want it to contain %q", err, "boom")
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want none: %+v", len(events), events)
	}
}

func TestRunAgent_MalformedJSON(t *testing.T) {
	srv, _ := sseServer(t, "data: {not json}\n\n")

	_, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), testRunRequest()))
	if err == nil {
		t.Fatal("RunAgent() error = nil, want a decode error")
	}
	if !strings.Contains(err.Error(), "decode event") {
		t.Errorf("error = %q, want it to mention decoding an event", err)
	}
}

func TestRunAgent_ConformanceStateDelta(t *testing.T) {
	tests := []struct {
		name      string
		mode      ConformanceMode
		streaming bool
		wantKey   string
		wantMode  string
	}{
		{name: "record buffered", mode: ModeRecord, streaming: false, wantKey: recordingsConfigKey, wantMode: "none"},
		{name: "record streaming", mode: ModeRecord, streaming: true, wantKey: recordingsConfigKey, wantMode: "sse"},
		{name: "replay buffered", mode: ModeReplay, streaming: false, wantKey: replayConfigKey, wantMode: "none"},
		{name: "replay streaming", mode: ModeReplay, streaming: true, wantKey: replayConfigKey, wantMode: "sse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, body := sseServer(t, "")

			req := testRunRequest()
			req.Streaming = tt.streaming
			if _, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), req,
				WithConformance(tt.mode, "/cases/c1", 2))); err != nil {
				t.Fatalf("RunAgent() error = %v", err)
			}

			want := map[string]any{tt.wantKey: map[string]any{
				"dir":                "/cases/c1",
				"user_message_index": float64(2),
				"streaming_mode":     tt.wantMode,
			}}
			if diff := cmp.Diff(want, decodeStateDelta(t, *body)); diff != "" {
				t.Errorf("stateDelta mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunAgent_MergesExistingStateDelta(t *testing.T) {
	srv, body := sseServer(t, "")

	req := testRunRequest()
	req.StateDelta = map[string]any{"caller": "value"}
	if _, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), req,
		WithConformance(ModeReplay, "/cases/c1", 0))); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}

	want := map[string]any{
		"caller": "value",
		replayConfigKey: map[string]any{
			"dir":                "/cases/c1",
			"user_message_index": float64(0),
			"streaming_mode":     "none",
		},
	}
	if diff := cmp.Diff(want, decodeStateDelta(t, *body)); diff != "" {
		t.Errorf("stateDelta mismatch (-want +got):\n%s", diff)
	}
}

func TestRunAgent_SendsCallerStateDeltaUnchanged(t *testing.T) {
	srv, body := sseServer(t, "")

	req := testRunRequest()
	req.StateDelta = map[string]any{"caller": "value"}
	if _, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), req)); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if diff := cmp.Diff(map[string]any{"caller": "value"}, decodeStateDelta(t, *body)); diff != "" {
		t.Errorf("stateDelta mismatch (-want +got):\n%s", diff)
	}
}

func TestRunAgent_OmitsAbsentStateDelta(t *testing.T) {
	srv, body := sseServer(t, "")

	if _, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), testRunRequest())); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if strings.Contains(*body, "stateDelta") {
		t.Errorf("request body = %s, want no stateDelta key", *body)
	}
}

func TestRunAgent_DoesNotMutateCaller(t *testing.T) {
	srv, _ := sseServer(t, "data: {\"id\":\"e1\",\"author\":\"agent\"}\n\n")

	req := testRunRequest()
	req.StateDelta = map[string]any{"caller": "value"}
	if _, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), req,
		WithConformance(ModeRecord, "/cases/c1", 1))); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if diff := cmp.Diff(map[string]any{"caller": "value"}, req.StateDelta); diff != "" {
		t.Errorf("caller StateDelta was modified (-want +got):\n%s", diff)
	}
}

func TestRunAgent_InvalidConformanceOptions(t *testing.T) {
	tests := []struct {
		name    string
		option  RunOption
		wantErr string
	}{
		{name: "empty dir", option: WithConformance(ModeRecord, "", 0), wantErr: "test case directory must not be empty"},
		{name: "negative index", option: WithConformance(ModeReplay, "/cases/c1", -1), wantErr: "must not be negative"},
		{name: "unknown mode", option: WithConformance(ConformanceMode("audit"), "/cases/c1", 0), wantErr: "unsupported conformance mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests.Add(1)
			}))
			defer srv.Close()

			events, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), testRunRequest(), tt.option))
			if err == nil {
				t.Fatal("RunAgent() error = nil, want a validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			if len(events) != 0 {
				t.Errorf("got %d events, want none", len(events))
			}
			if got := requests.Load(); got != 0 {
				t.Errorf("server received %d requests, want 0", got)
			}
		})
	}
}

// TestRunAgent_RequestBodyFields proves the client cannot send a field the real
// server would reject: the server decodes with DisallowUnknownFields.
func TestRunAgent_RequestBodyFields(t *testing.T) {
	srv, body := sseServer(t, "")

	req := testRunRequest()
	req.Streaming = true
	req.StateDelta = map[string]any{"caller": "value"}
	if _, err := collect(newTestClient(t, srv.URL).RunAgent(context.Background(), req,
		WithConformance(ModeReplay, "/cases/c1", 3))); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}

	decoder := json.NewDecoder(strings.NewReader(*body))
	decoder.DisallowUnknownFields()
	var decoded models.RunAgentRequest
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("server-side strict decode of %s failed: %v", *body, err)
	}
	if err := decoded.AssertRunAgentRequestRequired(); err != nil {
		t.Errorf("required field missing from request body %s: %v", *body, err)
	}
	if decoded.AppName != "test_app" || decoded.UserId != "test_user" || decoded.SessionId != "test_session" {
		t.Errorf("decoded = %+v, want test_app/test_user/test_session", decoded)
	}
	if !decoded.Streaming {
		t.Error("decoded Streaming = false, want true")
	}
}

func TestRunAgent_ContextCancelled(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := io.WriteString(w, "data: {\"id\":\"e1\",\"author\":\"agent\"}\n\n"); err != nil {
			t.Errorf("write response: %v", err)
		}
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var events []*session.Event
	var gotErr error
	for event, err := range newTestClient(t, srv.URL).RunAgent(ctx, testRunRequest()) {
		if err != nil {
			gotErr = err
			break
		}
		events = append(events, event)
		cancel()
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if gotErr == nil {
		t.Fatal("RunAgent() error = nil, want the context error")
	}
	if !strings.Contains(gotErr.Error(), context.Canceled.Error()) {
		t.Errorf("error = %q, want it to report %q", gotErr, context.Canceled)
	}
}

// TestRunAgent_EarlyBreak proves breaking out of the range closes the response
// body, using a transport that tracks whether the body was closed.
func TestRunAgent_EarlyBreak(t *testing.T) {
	srv, _ := sseServer(t, "data: {\"id\":\"e1\",\"author\":\"agent\"}\n\ndata: {\"id\":\"e2\",\"author\":\"agent\"}\n\n")

	tracker := &closeTracker{}
	c, err := New(Config{BaseURL: srv.URL, HTTPClient: &http.Client{Transport: tracker}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer c.Close()

	seen := 0
	for _, err := range c.RunAgent(context.Background(), testRunRequest()) {
		if err != nil {
			t.Fatalf("RunAgent() error = %v", err)
		}
		seen++
		break
	}
	if seen != 1 {
		t.Fatalf("got %d events before the break, want 1", seen)
	}
	if !tracker.closed.Load() {
		t.Error("response body was not closed after breaking out of the range")
	}
}

// TestRunAgent_ReadError surfaces a mid-stream read failure that is not EOF.
func TestRunAgent_ReadError(t *testing.T) {
	srv, _ := sseServer(t, "data: {\"id\":\"e1\",\"author\":\"agent\"}\n\n")

	c, err := New(Config{BaseURL: srv.URL, HTTPClient: &http.Client{Transport: &brokenStreamTransport{}}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer c.Close()

	events, runErr := collect(c.RunAgent(context.Background(), testRunRequest()))
	if runErr == nil {
		t.Fatal("RunAgent() error = nil, want a read error")
	}
	if !strings.Contains(runErr.Error(), "read stream") {
		t.Errorf("error = %q, want it to mention reading the stream", runErr)
	}
	if !errors.Is(runErr, errBrokenStream) {
		t.Errorf("error = %v, want it to wrap %v", runErr, errBrokenStream)
	}
	if len(events) != 1 {
		t.Errorf("got %d events, want the one that arrived before the failure", len(events))
	}
}

func TestSSEData(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "payload", line: "data: {\"id\":\"e1\"}\n", want: `{"id":"e1"}`, ok: true},
		{name: "payload without space", line: "data:{\"id\":\"e1\"}\n", want: `{"id":"e1"}`, ok: true},
		{name: "carriage return", line: "data: {\"id\":\"e1\"}\r\n", want: `{"id":"e1"}`, ok: true},
		{name: "empty payload", line: "data:\n", ok: false},
		{name: "frame separator", line: "\n", ok: false},
		{name: "event line", line: "event: error\n", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := sseData(tt.line)
			if ok != tt.ok {
				t.Fatalf("sseData(%q) ok = %v, want %v", tt.line, ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("sseData(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func decodeStateDelta(t *testing.T, body string) map[string]any {
	t.Helper()
	var decoded struct {
		StateDelta map[string]any `json:"stateDelta"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode request body %s: %v", body, err)
	}
	return decoded.StateDelta
}

// closeTracker records whether the response body was closed.
type closeTracker struct {
	closed atomic.Bool
}

func (t *closeTracker) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &trackedBody{ReadCloser: resp.Body, closed: &t.closed}
	return resp, nil
}

type trackedBody struct {
	io.ReadCloser
	closed *atomic.Bool
}

func (b *trackedBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

// brokenStreamTransport turns the end of the response body into a read
// failure, so the stream ends with something other than io.EOF.
type brokenStreamTransport struct{}

func (*brokenStreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &brokenBody{inner: resp.Body}
	return resp, nil
}

type brokenBody struct {
	inner io.ReadCloser
}

func (b *brokenBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if err != nil {
		return n, errBrokenStream
	}
	return n, nil
}

func (b *brokenBody) Close() error { return b.inner.Close() }

var errBrokenStream = errors.New("the connection broke")
