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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/websocket"
	"google.golang.org/genai"
)

// framesBuffer caps how many client frames the fake keeps for inspection.
// Sends into the channel are non-blocking, so a test that writes more frames
// than this drops the surplus rather than wedging the fake's read loop.
const framesBuffer = 64

// newFakeLiveConnection dials a LiveConnection at a Gemini Live wire-protocol
// fake: it upgrades the websocket, consumes the client's setup frame, replies
// {"setupComplete":{}}, then drains frames until the read fails. The drain is
// required. Without a reader the socket buffer fills, the client's writes
// block, and a test hangs instead of failing.
//
// The returned channel carries the frames the client sent after setup.
//
// backend is the value the LiveConnection routes on. The transport always
// speaks the Gemini API wire format, because that is what the fake serves, so
// a non-Gemini-API backend here selects the routing branch without changing
// the frame layout the tests decode.
func newFakeLiveConnection(t *testing.T, modelName string, backend genai.Backend) (*LiveConnection, <-chan []byte) {
	t.Helper()
	frames := make(chan []byte, framesBuffer)
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
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case frames <- frame:
			default:
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
	session, err := client.Live.Connect(t.Context(), modelName, &genai.LiveConnectConfig{})
	if err != nil {
		t.Fatalf("Live.Connect failed: %v", err)
	}
	conn := NewLiveConnection(session, modelName, backend)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, frames
}

// wireBlob is a genai.Blob as the Gemini Live wire format carries it. Data is
// base64 in the JSON and encoding/json decodes it back into the byte slice.
type wireBlob struct {
	Data     []byte `json:"data"`
	MIMEType string `json:"mimeType"`
}

// realtimeInput holds the three realtime channels SendRealtime routes a blob
// to. Exactly one is set on any frame the blob case sends.
type realtimeInput struct {
	MediaChunks []*wireBlob `json:"mediaChunks,omitempty"`
	Audio       *wireBlob   `json:"audio,omitempty"`
	Video       *wireBlob   `json:"video,omitempty"`
}

// nextRealtimeInput returns the realtime input of the next frame the client
// sent. It fails the test when no frame arrives before the deadline.
func nextRealtimeInput(t *testing.T, frames <-chan []byte) realtimeInput {
	t.Helper()
	select {
	case frame := <-frames:
		var decoded struct {
			RealtimeInput realtimeInput `json:"realtimeInput"`
		}
		if err := json.Unmarshal(frame, &decoded); err != nil {
			t.Fatalf("decoding frame %q failed: %v", frame, err)
		}
		return decoded.RealtimeInput
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a realtime frame")
		return realtimeInput{}
	}
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
	conn, _ := newFakeLiveConnection(t, "test-live-model", genai.BackendGeminiAPI)
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

// TestRealtimeBlob pins the sniffing rules and the read-only contract on the
// caller's value.
func TestRealtimeBlob(t *testing.T) {
	signature := []byte("\x89PNG\r\n\x1a\n")
	pcm := []byte{0x00, 0x01, 0x02, 0x03}

	tests := []struct {
		name string
		in   *genai.Blob
		// wantSame is true when realtimeBlob may return the caller's pointer,
		// which it does only when no sniffing is needed.
		wantSame bool
		want     genai.Blob
	}{
		{
			name:     "png signature with a payload",
			in:       &genai.Blob{Data: append(slices.Clone(signature), 'p', 'a', 'y')},
			wantSame: false,
			want:     genai.Blob{Data: append(slices.Clone(signature), 'p', 'a', 'y'), MIMEType: "image/png"},
		},
		{
			name:     "exactly the png signature",
			in:       &genai.Blob{Data: slices.Clone(signature)},
			wantSame: false,
			want:     genai.Blob{Data: slices.Clone(signature), MIMEType: "image/png"},
		},
		{
			name:     "bytes that are not png",
			in:       &genai.Blob{Data: pcm},
			wantSame: false,
			want:     genai.Blob{Data: pcm, MIMEType: "audio/pcm"},
		},
		{
			name:     "a truncated png signature",
			in:       &genai.Blob{Data: signature[:7]},
			wantSame: false,
			want:     genai.Blob{Data: signature[:7], MIMEType: "audio/pcm"},
		},
		{
			name:     "the last signature byte differs",
			in:       &genai.Blob{Data: append(slices.Clone(signature[:7]), 0x0B)},
			wantSame: false,
			want:     genai.Blob{Data: append(slices.Clone(signature[:7]), 0x0B), MIMEType: "audio/pcm"},
		},
		{
			name:     "no data at all",
			in:       &genai.Blob{},
			wantSame: false,
			want:     genai.Blob{MIMEType: "audio/pcm"},
		},
		{
			name:     "the copy keeps every other field",
			in:       &genai.Blob{Data: slices.Clone(signature), DisplayName: "frame.png"},
			wantSame: false,
			want:     genai.Blob{Data: slices.Clone(signature), DisplayName: "frame.png", MIMEType: "image/png"},
		},
		{
			name:     "an explicit mime type is passed through",
			in:       &genai.Blob{Data: pcm, MIMEType: "audio/pcm;rate=16000"},
			wantSame: true,
			want:     genai.Blob{Data: pcm, MIMEType: "audio/pcm;rate=16000"},
		},
		{
			name:     "an explicit mime type wins over the png signature",
			in:       &genai.Blob{Data: slices.Clone(signature), MIMEType: "image/jpeg"},
			wantSame: true,
			want:     genai.Blob{Data: slices.Clone(signature), MIMEType: "image/jpeg"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := *tc.in

			got := realtimeBlob(tc.in)

			if same := got == tc.in; same != tc.wantSame {
				t.Errorf("realtimeBlob() returned the caller's pointer = %t, want %t", same, tc.wantSame)
			}
			if diff := cmp.Diff(tc.want, *got); diff != "" {
				t.Errorf("realtimeBlob() returned an unexpected blob (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, *tc.in); diff != "" {
				t.Errorf("realtimeBlob() modified the caller's blob (-before +after):\n%s", diff)
			}
		})
	}
}

// TestSendRealtimeDoesNotMutateCallerBlob reuses one Blob value across two
// sends, the way a caller that owns a single capture buffer does. Writing the
// sniffed type back into that value labels the second send with the first
// send's type.
func TestSendRealtimeDoesNotMutateCallerBlob(t *testing.T) {
	conn, frames := newFakeLiveConnection(t, "gemini-3.1-flash-live", genai.BackendGeminiAPI)
	ctx := t.Context()

	png := []byte("\x89PNG\r\n\x1a\nframe")
	pcm := []byte{0x00, 0x01, 0x02, 0x03}

	blob := &genai.Blob{Data: png}
	if err := conn.SendRealtime(ctx, blob); err != nil {
		t.Fatalf("SendRealtime(png) failed: %v", err)
	}
	if blob.MIMEType != "" {
		t.Errorf("after the first send the caller's MIMEType = %q, want it still empty", blob.MIMEType)
	}
	wantFirst := realtimeInput{Video: &wireBlob{Data: png, MIMEType: "image/png"}}
	if diff := cmp.Diff(wantFirst, nextRealtimeInput(t, frames)); diff != "" {
		t.Errorf("first frame is unexpected (-want +got):\n%s", diff)
	}

	blob.Data = pcm
	if err := conn.SendRealtime(ctx, blob); err != nil {
		t.Fatalf("SendRealtime(pcm) failed: %v", err)
	}
	if blob.MIMEType != "" {
		t.Errorf("after the second send the caller's MIMEType = %q, want it still empty", blob.MIMEType)
	}
	wantSecond := realtimeInput{Audio: &wireBlob{Data: pcm, MIMEType: "audio/pcm"}}
	if diff := cmp.Diff(wantSecond, nextRealtimeInput(t, frames)); diff != "" {
		t.Errorf("second frame is unexpected (-want +got):\n%s", diff)
	}
}

// TestSendRealtimeRoutesBlob pins the realtime channel each combination of
// model, backend and MIME type selects, so moving the sniff off the caller's
// value cannot change what goes on the wire.
func TestSendRealtimeRoutesBlob(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nframe")
	pcm := []byte{0x00, 0x01, 0x02, 0x03}

	tests := []struct {
		name      string
		modelName string
		backend   genai.Backend
		blob      *genai.Blob
		want      realtimeInput
	}{
		{
			name:      "gemini 3.1 sends a sniffed image on the video channel",
			modelName: "gemini-3.1-flash-live",
			backend:   genai.BackendGeminiAPI,
			blob:      &genai.Blob{Data: png},
			want:      realtimeInput{Video: &wireBlob{Data: png, MIMEType: "image/png"}},
		},
		{
			name:      "gemini 3.1 sends sniffed audio on the audio channel",
			modelName: "gemini-3.1-flash-live",
			backend:   genai.BackendGeminiAPI,
			blob:      &genai.Blob{Data: pcm},
			want:      realtimeInput{Audio: &wireBlob{Data: pcm, MIMEType: "audio/pcm"}},
		},
		{
			name:      "gemini 3.1 keeps an explicit mime type",
			modelName: "gemini-3.1-flash-live",
			backend:   genai.BackendGeminiAPI,
			blob:      &genai.Blob{Data: pcm, MIMEType: "audio/pcm;rate=16000"},
			want:      realtimeInput{Audio: &wireBlob{Data: pcm, MIMEType: "audio/pcm;rate=16000"}},
		},
		{
			name:      "an older model sends media chunks",
			modelName: "gemini-2.0-flash-live-001",
			backend:   genai.BackendGeminiAPI,
			blob:      &genai.Blob{Data: pcm},
			want:      realtimeInput{MediaChunks: []*wireBlob{{Data: pcm, MIMEType: "audio/pcm"}}},
		},
		{
			name:      "another backend sends media chunks",
			modelName: "gemini-3.1-flash-live",
			backend:   genai.BackendVertexAI,
			blob:      &genai.Blob{Data: png},
			want:      realtimeInput{MediaChunks: []*wireBlob{{Data: png, MIMEType: "image/png"}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn, frames := newFakeLiveConnection(t, tc.modelName, tc.backend)
			before := *tc.blob

			if err := conn.SendRealtime(t.Context(), tc.blob); err != nil {
				t.Fatalf("SendRealtime failed: %v", err)
			}

			if diff := cmp.Diff(tc.want, nextRealtimeInput(t, frames)); diff != "" {
				t.Errorf("frame is unexpected (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, *tc.blob); diff != "" {
				t.Errorf("SendRealtime modified the caller's blob (-before +after):\n%s", diff)
			}
		})
	}
}

// TestSendRealtimeNonBlobInput covers the activity signals and the error the
// default arm returns for an input type the method does not accept.
func TestSendRealtimeNonBlobInput(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantErr string
	}{
		{name: "activity start", input: &genai.ActivityStart{}},
		{name: "activity end", input: &genai.ActivityEnd{}},
		{name: "an unsupported type", input: "text", wantErr: "unsupported real-time input type: string"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := newFakeLiveConnection(t, "test-live-model", genai.BackendGeminiAPI)

			err := conn.SendRealtime(t.Context(), tc.input)

			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("SendRealtime() = %v, want no error", err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("SendRealtime() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
