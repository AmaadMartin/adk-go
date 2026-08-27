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

package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"google.golang.org/adk/v2/server/agentengine/controllers/method"
)

// streamingStub emits a few JSON lines and flushes between them, imitating what
// the real streaming handlers do via helper.EmitJSON.
type streamingStub struct {
	name  string
	lines []string
}

func (s *streamingStub) Name() string { return s.name }

func (s *streamingStub) Handle(_ context.Context, rw http.ResponseWriter, _ []byte) error {
	rc := http.NewResponseController(rw)
	for _, line := range s.lines {
		if _, err := fmt.Fprintf(rw, "%s\n", line); err != nil {
			return fmt.Errorf("write failed: %w", err)
		}
		if err := rc.Flush(); err != nil {
			return fmt.Errorf("flush failed: %w", err)
		}
	}
	return nil
}

func (s *streamingStub) Metadata() (*structpb.Struct, error) {
	return structpb.NewStruct(map[string]any{"name": s.name})
}

// TestQueryWriteDeadline serves Query over a real listener, which is the only
// way to observe the write deadline: an httptest.ResponseRecorder has no
// connection, so SetWriteDeadline returns http.ErrNotSupported and the bug
// disappears.
func TestQueryWriteDeadline(t *testing.T) {
	lines := []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}

	tests := []struct {
		name       string
		sseTimeout time.Duration
	}{
		{name: "zero", sseTimeout: 0},
		{name: "positive", sseTimeout: 30 * time.Second},
		{name: "negative", sseTimeout: -1 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &streamingStub{name: "async_stream_query", lines: lines}
			c, err := NewAgentEngineAPIController(nil, tt.sseTimeout, 1<<20, []method.MethodHandler{stub})
			if err != nil {
				t.Fatalf("NewAgentEngineAPIController() failed: %v", err)
			}

			srv := httptest.NewServer(http.HandlerFunc(c.Query))
			defer srv.Close()

			body := strings.NewReader(`{"class_method":"async_stream_query","input":{}}`)
			// Pre-fix the connection dies before a response exists, so this
			// call itself returns an EOF error rather than a bad response.
			resp, err := http.Post(srv.URL, "application/json", body)
			if err != nil {
				t.Fatalf("http.Post() failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("io.ReadAll() failed: %v", err)
			}
			for _, line := range lines {
				if !strings.Contains(string(got), line) {
					t.Errorf("body = %q, want it to contain %q", got, line)
				}
			}
		})
	}
}

// TestQueryDeadlineUnsupported covers the failure branch of the guard. An
// httptest.ResponseRecorder has no connection, so SetWriteDeadline returns
// http.ErrNotSupported; Query must log it and serve the response anyway.
func TestQueryDeadlineUnsupported(t *testing.T) {
	lines := []string{`{"n":1}`, `{"n":2}`}
	stub := &streamingStub{name: "async_stream_query", lines: lines}
	c, err := NewAgentEngineAPIController(nil, 30*time.Second, 1<<20, []method.MethodHandler{stub})
	if err != nil {
		t.Fatalf("NewAgentEngineAPIController() failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/stream_reasoning_engine",
		strings.NewReader(`{"class_method":"async_stream_query","input":{}}`))
	rec := httptest.NewRecorder()
	c.Query(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	for _, line := range lines {
		if !strings.Contains(rec.Body.String(), line) {
			t.Errorf("body = %q, want it to contain %q", rec.Body.String(), line)
		}
	}
}

// TestNewAgentEngineAPIControllerDuplicateMethod covers the constructor's
// rejection of two handlers sharing a name.
func TestNewAgentEngineAPIControllerDuplicateMethod(t *testing.T) {
	handlers := []method.MethodHandler{
		&streamingStub{name: "async_stream_query"},
		&streamingStub{name: "async_stream_query"},
	}
	if _, err := NewAgentEngineAPIController(nil, 0, 1<<20, handlers); err == nil {
		t.Error("NewAgentEngineAPIController() succeeded, want a duplicate method name error")
	}
}

// TestQueryUnknownClassMethod covers the error path: an unrecognised method
// makes handleQuery fail and Query answer 500.
func TestQueryUnknownClassMethod(t *testing.T) {
	stub := &streamingStub{name: "async_stream_query", lines: []string{`{"n":1}`}}
	c, err := NewAgentEngineAPIController(nil, 0, 1<<20, []method.MethodHandler{stub})
	if err != nil {
		t.Fatalf("NewAgentEngineAPIController() failed: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(c.Query))
	defer srv.Close()

	body := strings.NewReader(`{"class_method":"no_such_method","input":{}}`)
	resp, err := http.Post(srv.URL, "application/json", body)
	if err != nil {
		t.Fatalf("http.Post() failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// failingReader fails on the first read, standing in for a request body that
// breaks mid-transfer.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken body") }

// TestQueryUnreadableBody covers the error path where reading the request body
// fails: Query must answer 400.
func TestQueryUnreadableBody(t *testing.T) {
	stub := &streamingStub{name: "async_stream_query", lines: []string{`{"n":1}`}}
	c, err := NewAgentEngineAPIController(nil, 0, 1<<20, []method.MethodHandler{stub})
	if err != nil {
		t.Fatalf("NewAgentEngineAPIController() failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/stream_reasoning_engine", failingReader{})
	rec := httptest.NewRecorder()
	c.Query(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestQueryMalformedPayload covers the other error path: a body that is not
// JSON makes Query answer 400.
func TestQueryMalformedPayload(t *testing.T) {
	stub := &streamingStub{name: "async_stream_query", lines: []string{`{"n":1}`}}
	c, err := NewAgentEngineAPIController(nil, 0, 1<<20, []method.MethodHandler{stub})
	if err != nil {
		t.Fatalf("NewAgentEngineAPIController() failed: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(c.Query))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`not json`))
	if err != nil {
		t.Fatalf("http.Post() failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}
