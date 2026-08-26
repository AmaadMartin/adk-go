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

package googlellm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"google.golang.org/genai"
)

// newFakeLiveConnection dials a LiveConnection at a Gemini Live wire-protocol
// fake: it upgrades the websocket, consumes the client's setup frame, replies
// {"setupComplete":{}}, then drains frames until the read fails. The drain is
// required. Without a reader the socket buffer fills, the client's writes
// block, and a test hangs instead of failing.
func newFakeLiveConnection(t *testing.T) *LiveConnection {
	t.Helper()
	var upgrader websocket.Upgrader
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("websocket upgrade failed: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Errorf("reading setup frame failed: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`)); err != nil {
			t.Errorf("writing setupComplete failed: %v", err)
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(ts.Close)

	client, err := genai.NewClient(t.Context(), &genai.ClientConfig{
		Backend:     genai.BackendGeminiAPI,
		APIKey:      "test-api-key",
		HTTPOptions: genai.HTTPOptions{BaseURL: strings.Replace(ts.URL, "http", "ws", 1)},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	session, err := client.Live.Connect(t.Context(), "test-live-model", &genai.LiveConnectConfig{})
	if err != nil {
		t.Fatalf("Live.Connect failed: %v", err)
	}
	conn := NewLiveConnection(session, "test-live-model", genai.BackendGeminiAPI)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestLiveConnectionConcurrentSends drives every write path of one
// LiveConnection from many goroutines at once, the way RunLive does when its
// input pump and its function-call loop share a connection.
//
// The assertion is the -race verdict and the absence of gorilla's "panic:
// concurrent write to websocket connection", not the error values: the
// underlying websocket permits one writer, so an unserialised Send corrupts
// frames on the wire. The returned errors are still checked so a silently
// broken connection cannot pass the test by never writing at all.
func TestLiveConnectionConcurrentSends(t *testing.T) {
	const (
		senders    = 8
		iterations = 50
	)
	conn := newFakeLiveConnection(t)
	ctx := t.Context()

	// One send per guarded method, chosen by the sender's index so all three
	// run concurrently with each other.
	sendFuncs := []func() error{
		func() error {
			return conn.SendContent(ctx, genai.NewContentFromText("ping", genai.RoleUser))
		},
		func() error {
			return conn.SendContent(ctx, &genai.Content{
				Role: genai.RoleUser,
				Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
					ID:       "call_1",
					Name:     "probe",
					Response: map[string]any{"ok": true},
				}}},
			})
		},
		func() error {
			// A fresh Blob per send: SendRealtime fills in an empty MIMEType,
			// so a shared value would be mutated in place and the test would
			// report its own race instead of the one under test.
			return conn.SendRealtime(ctx, &genai.Blob{
				Data:     []byte{0x00, 0x01, 0x02, 0x03},
				MIMEType: "audio/pcm",
			})
		},
		func() error {
			return conn.SendHistory(ctx, []*genai.Content{
				genai.NewContentFromText("history", genai.RoleUser),
			})
		},
	}

	errCh := make(chan error, senders*iterations)
	var wg sync.WaitGroup
	for i := range senders {
		send := sendFuncs[i%len(sendFuncs)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if err := send(); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent send failed: %v", err)
	}
}
