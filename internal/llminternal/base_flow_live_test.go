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

package llminternal

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/internal/agent/runconfig"
	icontext "google.golang.org/adk/v2/internal/context"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
)

// fakeLiveModel implements model.LLM plus the Client() accessor RunLive
// type-asserts on, backed by a genai client pointed at a fake websocket server.
type fakeLiveModel struct {
	client *genai.Client
	// name overrides the reported model name, and backend the reported
	// variant. The connection branches on both, so a test that needs a
	// model-specific send path sets them. The zero backend is
	// BackendUnspecified, which is what a model that reports no variant at all
	// resolves to.
	name    string
	backend genai.Backend
}

func (m *fakeLiveModel) Name() string {
	if m.name != "" {
		return m.name
	}
	return "test-live-model"
}

func (m *fakeLiveModel) GetGoogleLLMVariant() genai.Backend { return m.backend }

func (m *fakeLiveModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {}
}

func (m *fakeLiveModel) Client() *genai.Client { return m.client }

const serverContentPong = `{"serverContent":{"modelTurn":{"parts":[{"text":"pong"}],"role":"model"},"turnComplete":true}}`

// startFakeLiveServer runs a Gemini Live wire-protocol fake: it upgrades the
// websocket, consumes the client's setup frame, replies {"setupComplete":{}},
// then hands the connection to serveConn with a 1-based connection number.
// When serveConn returns, the connection is hard-closed (no close handshake).
func startFakeLiveServer(t *testing.T, serveConn func(connNum int, conn *websocket.Conn)) (*genai.Client, *atomic.Int32) {
	t.Helper()
	var upgrader websocket.Upgrader
	var connCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("websocket upgrade failed: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		// Count before the setup exchange: a client that saw setupComplete is
		// then guaranteed to observe this connection in the count.
		connNum := int(connCount.Add(1))
		if _, _, err := conn.ReadMessage(); err != nil {
			t.Errorf("reading setup frame failed: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`)); err != nil {
			t.Errorf("writing setupComplete failed: %v", err)
			return
		}
		serveConn(connNum, conn)
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
	return client, &connCount
}

// blockUntilClientCloses parks the fake connection until the client tears it
// down, so an idle server never triggers a spurious reconnect.
func blockUntilClientCloses(conn *websocket.Conn) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// runLiveStacks returns the goroutine stacks still parked inside RunLive's
// producer/consumer closures, and their count.
func runLiveStacks() (int, string) {
	// runtime.Stack silently truncates when the dump outgrows buf, which would
	// hide leaked goroutines and turn this helper into a false pass. Grow until
	// the whole dump fits.
	buf := make([]byte, 1<<20)
	var n int
	for {
		n = runtime.Stack(buf, true)
		if n < len(buf) {
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var leaked []string
	for g := range strings.SplitSeq(string(buf[:n]), "\n\n") {
		if strings.Contains(g, "llminternal.(*Flow).RunLive") {
			leaked = append(leaked, g)
		}
	}
	return len(leaked), strings.Join(leaked, "\n\n")
}

// assertNoRunLiveLeak fails if this test left more RunLive goroutines behind
// than existed at its start. The baseline keeps a leak (a permanently blocked
// goroutine) in one subtest from failing every subsequent subtest too.
func assertNoRunLiveLeak(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var count int
	var stacks string
	for time.Now().Before(deadline) {
		count, stacks = runLiveStacks()
		if count <= baseline {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("leaked %d RunLive goroutines (baseline %d):\n%s", count-baseline, baseline, stacks)
}

func newLiveInvocationContext(t *testing.T) (agent.InvocationContext, context.CancelFunc) {
	t.Helper()
	testAgent, err := agent.New(agent.Config{Name: "test-live-agent"})
	if err != nil {
		t.Fatalf("agent.New failed: %v", err)
	}
	baseCtx, cancel := context.WithCancel(t.Context())
	ctx := icontext.NewInvocationContext(
		runconfig.ToContext(baseCtx, &runconfig.RunConfig{
			StreamingMode: runconfig.StreamingModeBidi,
			Live:          &agent.LiveRunConfig{},
		}),
		icontext.InvocationContextParams{Agent: testAgent},
	)
	return ctx, cancel
}

// liveConfigProcessor is the minimal request processor RunLive needs: it
// populates req.Config, which RunLive dereferences to build the connect config.
func liveConfigProcessor(ctx agent.InvocationContext, req *model.LLMRequest, f *Flow) iter.Seq2[*session.Event, error] {
	req.Config = &genai.GenerateContentConfig{}
	return func(yield func(*session.Event, error) bool) {}
}

func TestRunLiveNoGoroutineLeak(t *testing.T) {
	tests := []struct {
		name string
		// serveConn scripts the fake server per connection (post setup-complete).
		serveConn func(connNum int, conn *websocket.Conn)
		// drive consumes the event iterator and steers the session; it must
		// return only when the iterator is exhausted.
		drive     func(t *testing.T, sess agent.LiveSession, seq iter.Seq2[*session.Event, error], cancel context.CancelFunc)
		wantConns int32
	}{
		{
			// Connection 1 dies with an abrupt close (a resumable error).
			// RunLive must reconnect, serve a turn on connection 2, and tear
			// down without stranding either connection's producer goroutines.
			name: "reader resumable error reconnects without leak",
			serveConn: func(connNum int, conn *websocket.Conn) {
				if connNum == 1 {
					return // hard close right after setup => resumable 1006 on the client
				}
				if err := conn.WriteMessage(websocket.TextMessage, []byte(serverContentPong)); err != nil {
					return
				}
				blockUntilClientCloses(conn)
			},
			drive: func(t *testing.T, sess agent.LiveSession, seq iter.Seq2[*session.Event, error], cancel context.CancelFunc) {
				sawTurn := false
				for ev, err := range seq {
					if err != nil {
						continue // the post-cancel context.Canceled error is expected
					}
					if ev != nil && ev.LLMResponse.Content != nil {
						sawTurn = true
						cancel() // got the post-reconnect turn; tear the session down
					}
				}
				if !sawTurn {
					t.Error("never received the post-reconnect model turn")
				}
			},
			wantConns: 2,
		},
		{
			// Connection 1 dies before the client sends anything. The sender
			// goroutine that picks up the queued Send may hit a dead
			// connection; its error must be discarded, not strand the sender,
			// and a retried Send must succeed on connection 2.
			name: "sender error after connection loss does not leak",
			serveConn: func(connNum int, conn *websocket.Conn) {
				if connNum == 1 {
					return
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					if err := conn.WriteMessage(websocket.TextMessage, []byte(serverContentPong)); err != nil {
						return
					}
				}
			},
			drive: func(t *testing.T, sess agent.LiveSession, seq iter.Seq2[*session.Event, error], cancel context.CancelFunc) {
				stop := make(chan struct{})
				go func() {
					for range 200 {
						select {
						case <-stop:
							return
						default:
						}
						if err := sess.Send(agent.LiveRequest{Content: genai.NewContentFromText("ping", "user")}); err != nil {
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
				}()
				sawTurn := false
				for ev, err := range seq {
					if err != nil {
						continue
					}
					if ev != nil && ev.LLMResponse.Content != nil && !sawTurn {
						sawTurn = true
						close(stop)
						cancel()
					}
				}
				if !sawTurn {
					t.Error("never received a model turn for the retried send")
				}
			},
			wantConns: 2,
		},
		{
			// Same connection-loss shape as the case above, but the queued
			// request carries RealtimeInput rather than Content, so the sender
			// fails inside SendRealtime — the third errChan send site. Without
			// its guard the sender strands just as it does for SendContent.
			name: "realtime sender error after connection loss does not leak",
			serveConn: func(connNum int, conn *websocket.Conn) {
				if connNum == 1 {
					return
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					if err := conn.WriteMessage(websocket.TextMessage, []byte(serverContentPong)); err != nil {
						return
					}
				}
			},
			drive: func(t *testing.T, sess agent.LiveSession, seq iter.Seq2[*session.Event, error], cancel context.CancelFunc) {
				stop := make(chan struct{})
				go func() {
					for range 200 {
						select {
						case <-stop:
							return
						default:
						}
						// A fresh Blob per send: SendRealtime fills in an empty
						// MIMEType, so a shared value would be mutated in place.
						req := agent.LiveRequest{RealtimeInput: &genai.Blob{
							Data:     []byte{0x00, 0x01, 0x02, 0x03},
							MIMEType: "audio/pcm",
						}}
						if err := sess.Send(req); err != nil {
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
				}()
				sawTurn := false
				for ev, err := range seq {
					if err != nil {
						continue
					}
					if ev != nil && ev.LLMResponse.Content != nil && !sawTurn {
						sawTurn = true
						close(stop)
						cancel()
					}
				}
				if !sawTurn {
					t.Error("never received a model turn for the retried realtime send")
				}
			},
			wantConns: 2,
		},
		{
			// Same connection-loss shape again, but the queued request only
			// sets AudioStreamEnd, so the sender fails inside the
			// audio-stream-end branch. That branch needs the same guarded
			// errChan send as its siblings or it strands the sender.
			name: "audio stream end sender error after connection loss does not leak",
			serveConn: func(connNum int, conn *websocket.Conn) {
				if connNum == 1 {
					return
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					if err := conn.WriteMessage(websocket.TextMessage, []byte(serverContentPong)); err != nil {
						return
					}
				}
			},
			drive: func(t *testing.T, sess agent.LiveSession, seq iter.Seq2[*session.Event, error], cancel context.CancelFunc) {
				stop := make(chan struct{})
				go func() {
					for range 200 {
						select {
						case <-stop:
							return
						default:
						}
						if err := sess.Send(agent.LiveRequest{AudioStreamEnd: true}); err != nil {
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
				}()
				sawTurn := false
				for ev, err := range seq {
					if err != nil {
						continue
					}
					if ev != nil && ev.LLMResponse.Content != nil && !sawTurn {
						sawTurn = true
						close(stop)
						cancel()
					}
				}
				if !sawTurn {
					t.Error("never received a model turn for the retried audio-stream-end send")
				}
			},
			wantConns: 2,
		},
		{
			// A close frame with a non-resumable code must surface as an error
			// to the caller and terminate the flow after a single connection.
			//
			// This case passes with or without the errChan guards: the sender
			// exits through the existing connCtx.Done() arm, so nothing is
			// stranded. It pins the terminal-path behaviour; it is not a
			// regression guard for the leak.
			name: "non-resumable error terminates after one connection",
			serveConn: func(connNum int, conn *websocket.Conn) {
				deadline := time.Now().Add(time.Second)
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseProtocolError, "fatal"), deadline)
			},
			drive: func(t *testing.T, sess agent.LiveSession, seq iter.Seq2[*session.Event, error], cancel context.CancelFunc) {
				sawErr := false
				for _, err := range seq {
					if err != nil && strings.Contains(err.Error(), "1002") {
						sawErr = true
					}
				}
				if !sawErr {
					t.Error("expected the close-1002 error to surface through the iterator")
				}
			},
			wantConns: 1,
		},
		{
			// Cancelling the invocation context while the reader is blocked in
			// Recv is the everyday teardown path: cleanup closes the
			// connection, the reader's Recv fails, and its error send must not
			// block forever on the abandoned channel.
			name: "parent ctx cancel exits cleanly",
			serveConn: func(connNum int, conn *websocket.Conn) {
				blockUntilClientCloses(conn)
			},
			drive: func(t *testing.T, sess agent.LiveSession, seq iter.Seq2[*session.Event, error], cancel context.CancelFunc) {
				// Cancel only after the first event (the setup-complete
				// acknowledgement) so the reader is parked in Recv when
				// cleanup tears the connection down.
				first := true
				for range seq {
					if first {
						first = false
						cancel()
					}
				}
			},
			wantConns: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseline, _ := runLiveStacks()
			client, connCount := startFakeLiveServer(t, tc.serveConn)
			f := &Flow{
				Model:             &fakeLiveModel{client: client},
				RequestProcessors: []func(ctx agent.InvocationContext, req *model.LLMRequest, f *Flow) iter.Seq2[*session.Event, error]{liveConfigProcessor},
			}
			ctx, cancel := newLiveInvocationContext(t)
			defer cancel()

			sess, seq, err := f.RunLive(ctx)
			if err != nil {
				t.Fatalf("RunLive failed: %v", err)
			}
			tc.drive(t, sess, seq, cancel)

			if got := connCount.Load(); got != tc.wantConns {
				t.Errorf("connection count = %d, want %d", got, tc.wantConns)
			}
			assertNoRunLiveLeak(t, baseline)
		})
	}
}

// clientFrame is the subset of the Gemini Live client protocol these tests
// assert on. Decoding the frame keeps the assertions on the wire contract
// rather than on the SDK's byte layout.
//
// The field names are the wire names, and two of them differ from the SDK's
// parameter struct: a Blob passed as Media arrives as mediaChunks, and
// turnComplete is a plain bool tagged omitempty, so an incomplete turn is
// expressed by leaving the field out.
type clientFrame struct {
	RealtimeInput *struct {
		AudioStreamEnd bool          `json:"audioStreamEnd"`
		MediaChunks    []*genai.Blob `json:"mediaChunks"`
		Text           string        `json:"text"`
	} `json:"realtimeInput"`
	ClientContent *struct {
		Turns        []*genai.Content `json:"turns"`
		TurnComplete bool             `json:"turnComplete"`
	} `json:"clientContent"`
}

// collectClientFrames runs one live session against a fake server that records
// every frame the client sends, then returns the frames sent after setup.
//
// send drives the session; the fake server answers each frame with a model
// turn, so the caller can wait for wantFrames to arrive rather than sleeping.
// m selects the model-specific send path; the zero value is the default, and
// the helper fills in its client.
func collectClientFrames(t *testing.T, m fakeLiveModel, wantFrames int, send func(sess agent.LiveSession) error) []clientFrame {
	t.Helper()
	baseline, _ := runLiveStacks()

	frames := make(chan []byte, 16)
	client, _ := startFakeLiveServer(t, func(connNum int, conn *websocket.Conn) {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			frames <- data
			if err := conn.WriteMessage(websocket.TextMessage, []byte(serverContentPong)); err != nil {
				return
			}
		}
	})

	m.client = client
	f := &Flow{
		Model:             &m,
		RequestProcessors: []func(ctx agent.InvocationContext, req *model.LLMRequest, f *Flow) iter.Seq2[*session.Event, error]{liveConfigProcessor},
	}
	ctx, cancel := newLiveInvocationContext(t)
	defer cancel()

	sess, seq, err := f.RunLive(ctx)
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range seq {
		}
	}()

	if err := send(sess); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	var got []clientFrame
	deadline := time.After(10 * time.Second)
	for len(got) < wantFrames {
		select {
		case data := <-frames:
			var frame clientFrame
			if err := json.Unmarshal(data, &frame); err != nil {
				t.Fatalf("decoding client frame %q failed: %v", data, err)
			}
			got = append(got, frame)
		case <-deadline:
			t.Fatalf("received %d client frames, want %d", len(got), wantFrames)
		}
	}

	cancel()
	<-done
	assertNoRunLiveLeak(t, baseline)
	return got
}

// TestRunLiveSendsAudioStreamEnd pins the wire frame the flow emits for an
// audio-stream-end request, and the precedence rule when realtime input is set
// on the same request.
func TestRunLiveSendsAudioStreamEnd(t *testing.T) {
	t.Run("audio stream end sends a realtime input frame", func(t *testing.T) {
		frames := collectClientFrames(t, fakeLiveModel{}, 1, func(sess agent.LiveSession) error {
			return sess.Send(agent.LiveRequest{AudioStreamEnd: true})
		})
		if frames[0].RealtimeInput == nil {
			t.Fatalf("frame 0 = %+v, want a realtimeInput frame", frames[0])
		}
		if !frames[0].RealtimeInput.AudioStreamEnd {
			t.Error("realtimeInput.audioStreamEnd = false, want true")
		}
	})

	t.Run("realtime input wins over audio stream end", func(t *testing.T) {
		// A plain content request follows the combined one. If the combined
		// request wrongly emitted a second frame, that frame lands here as
		// frame 1 and the content frame never arrives, so a missing suppression
		// fails rather than passing unnoticed.
		frames := collectClientFrames(t, fakeLiveModel{}, 2, func(sess agent.LiveSession) error {
			err := sess.Send(agent.LiveRequest{
				RealtimeInput:  &genai.Blob{Data: []byte{0, 1, 2, 3}, MIMEType: "audio/pcm"},
				AudioStreamEnd: true,
			})
			if err != nil {
				return err
			}
			return sess.Send(agent.LiveRequest{
				Content: genai.NewContentFromText("done", genai.RoleUser),
			})
		})
		if frames[0].RealtimeInput == nil || len(frames[0].RealtimeInput.MediaChunks) == 0 {
			t.Fatalf("frame 0 = %+v, want the realtime media frame", frames[0])
		}
		if frames[0].RealtimeInput.AudioStreamEnd {
			t.Error("frame 0 carried audioStreamEnd, want the realtime input to suppress it")
		}
		if frames[1].ClientContent == nil {
			t.Errorf("frame 1 = %+v, want the following clientContent frame", frames[1])
		}
	})
}

// TestRunLiveSendsTurnComplete pins how Partial reaches the model: a partial
// request must not complete the turn.
func TestRunLiveSendsTurnComplete(t *testing.T) {
	tests := []struct {
		name             string
		partial          bool
		wantTurnComplete bool
	}{
		{name: "a complete turn sets turnComplete", wantTurnComplete: true},
		{name: "a partial turn clears turnComplete", partial: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frames := collectClientFrames(t, fakeLiveModel{}, 1, func(sess agent.LiveSession) error {
				return sess.Send(agent.LiveRequest{
					Content: genai.NewContentFromText("still typing", genai.RoleUser),
					Partial: tc.partial,
				})
			})
			if frames[0].ClientContent == nil {
				t.Fatalf("frame 0 = %+v, want a clientContent frame", frames[0])
			}
			if frames[0].RealtimeInput != nil {
				t.Errorf("frame 0 also carried realtimeInput %+v, want only clientContent", frames[0].RealtimeInput)
			}
			if got := frames[0].ClientContent.TurnComplete; got != tc.wantTurnComplete {
				t.Errorf("clientContent.turnComplete = %v, want %v", got, tc.wantTurnComplete)
			}
		})
	}
}

// TestRunLivePartialSkipsGemini31TextPath covers the one send path that only
// some models take. Gemini 3.1 on the Gemini API sends a lone text part as
// realtime input, which always completes the turn, so a partial request has to
// fall back to a client-content frame.
func TestRunLivePartialSkipsGemini31TextPath(t *testing.T) {
	gemini31Model := fakeLiveModel{name: "gemini-3.1-flash-live", backend: genai.BackendGeminiAPI}

	t.Run("a complete turn uses the realtime text path", func(t *testing.T) {
		frames := collectClientFrames(t, gemini31Model, 1, func(sess agent.LiveSession) error {
			return sess.Send(agent.LiveRequest{
				Content: genai.NewContentFromText("hello", genai.RoleUser),
			})
		})
		if frames[0].RealtimeInput == nil {
			t.Fatalf("frame 0 = %+v, want a realtimeInput frame", frames[0])
		}
		if got := frames[0].RealtimeInput.Text; got != "hello" {
			t.Errorf("realtimeInput.text = %q, want %q", got, "hello")
		}
	})

	t.Run("a partial turn falls back to client content", func(t *testing.T) {
		frames := collectClientFrames(t, gemini31Model, 1, func(sess agent.LiveSession) error {
			return sess.Send(agent.LiveRequest{
				Content: genai.NewContentFromText("still typing", genai.RoleUser),
				Partial: true,
			})
		})
		if frames[0].RealtimeInput != nil {
			t.Fatalf("frame 0 = %+v, want a clientContent frame: the realtime text path completes the turn", frames[0].RealtimeInput)
		}
		if frames[0].ClientContent == nil {
			t.Fatalf("frame 0 = %+v, want a clientContent frame", frames[0])
		}
		if frames[0].ClientContent.TurnComplete {
			t.Error("clientContent.turnComplete = true, want false for a partial turn")
		}
	})
}

// liveHistoryProcessor populates req.Contents with a turn the SDK cannot
// marshal, so RunLive's SendHistory call fails on the client side.
//
// The failure has to be client-side: a broken socket would also fail the send,
// but then there would be no live connection left to observe. json.Marshal
// rejects NaN, so the send fails while the websocket stays healthy.
func liveHistoryProcessor(ctx agent.InvocationContext, req *model.LLMRequest, f *Flow) iter.Seq2[*session.Event, error] {
	req.Config = &genai.GenerateContentConfig{}
	req.Contents = []*genai.Content{{
		Role: "user",
		Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				Name: "unmarshalable",
				Args: map[string]any{"nan": math.NaN()},
			},
		}},
	}}
	return func(yield func(*session.Event, error) bool) {}
}

// TestRunLiveClosesConnectionOnSendHistoryFailure pins the cleanup discipline
// on RunLive's one early-return path.
//
// Every other return in RunLive's own body releases the attempt through
// cleanup (or cancelConn, before the connection exists). The SendHistory
// failure path returns without either, so the websocket is never closed.
// Cancelling the context would not help: genai dials with
// websocket.DefaultDialer.Dial, which takes no context, and nothing in the SDK
// watches one — only an explicit Close releases that socket. So the connection
// survives for the lifetime of the process, not just the invocation.
func TestRunLiveClosesConnectionOnSendHistoryFailure(t *testing.T) {
	// How long the fake server waits for the client's close before concluding
	// the socket was abandoned. Only paid on failure.
	const closeWait = 3 * time.Second

	baseline, _ := runLiveStacks()

	// clientClosed reports whether the client tore the connection down, as
	// opposed to leaving the server parked until its read deadline expired.
	clientClosed := make(chan bool, 1)
	client, connCount := startFakeLiveServer(t, func(connNum int, conn *websocket.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(closeWait))
		_, _, err := conn.ReadMessage()
		var netErr net.Error
		clientClosed <- !(errors.As(err, &netErr) && netErr.Timeout())
	})

	f := &Flow{
		Model:             &fakeLiveModel{client: client},
		RequestProcessors: []func(ctx agent.InvocationContext, req *model.LLMRequest, f *Flow) iter.Seq2[*session.Event, error]{liveHistoryProcessor},
	}
	ctx, cancel := newLiveInvocationContext(t)
	defer cancel()

	_, seq, err := f.RunLive(ctx)
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}

	// RunLive pushes the SendHistory error and returns. Stop iterating at that
	// error: nothing closes the session, so ranging further would block.
	var gotErr error
	for _, e := range seq {
		if e != nil {
			gotErr = e
			break
		}
	}
	if gotErr == nil {
		t.Fatal("expected the SendHistory failure to surface through the iterator")
	}
	if !strings.Contains(gotErr.Error(), "history") {
		t.Errorf("surfaced error = %v, want it to mention the history send", gotErr)
	}
	if got := connCount.Load(); got != 1 {
		t.Errorf("connection count = %d, want 1", got)
	}

	select {
	case closed := <-clientClosed:
		if !closed {
			t.Error("RunLive returned without closing the live connection: the socket " +
				"is leaked for the life of the process (missing cleanup on the SendHistory path)")
		}
	case <-time.After(closeWait + 2*time.Second):
		t.Fatal("fake server never reported a connection outcome")
	}

	assertNoRunLiveLeak(t, baseline)
}

func TestRunLiveCloseStopsIdleSession(t *testing.T) {
	baseline, _ := runLiveStacks()

	// connGone fires when the fake server's read returns, i.e. once the client
	// has torn the socket down. Buffered so the handler never blocks, whether
	// the test consumed the signal or gave up and failed.
	connGone := make(chan struct{}, 1)
	client, connCount := startFakeLiveServer(t, func(connNum int, conn *websocket.Conn) {
		blockUntilClientCloses(conn) // an idle server: no unsolicited traffic
		connGone <- struct{}{}
	})

	f := &Flow{
		Model:             &fakeLiveModel{client: client},
		RequestProcessors: []func(ctx agent.InvocationContext, req *model.LLMRequest, f *Flow) iter.Seq2[*session.Event, error]{liveConfigProcessor},
	}
	// The context is deliberately left alive across the assertions below:
	// this test is about Close standing on its own, with no help from ctx.
	ctx, cancel := newLiveInvocationContext(t)
	defer cancel()

	sess, seq, err := f.RunLive(ctx)
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}

	next, stop := iter.Pull2(seq)
	defer stop()

	// The setup-complete acknowledgement. Once it lands the connection is up
	// and the flow is settling into the idle consumer select.
	if _, _, ok := next(); !ok {
		t.Fatal("iterator ended before the setup-complete event; the connection never came up")
	}
	if got := connCount.Load(); got != 1 {
		t.Fatalf("connection count = %d, want 1", got)
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if _, _, ok := next(); ok {
		t.Error("iterator produced an event after Close")
	}

	select {
	case <-connGone:
	case <-time.After(10 * time.Second):
		_, stacks := runLiveStacks()
		t.Fatalf("Close did not release the live connection: the websocket is still "+
			"open and the flow is still running:\n%s", stacks)
	}

	if got := connCount.Load(); got != 1 {
		t.Errorf("connection count = %d, want 1 (Close must not trigger a reconnect)", got)
	}
	assertNoRunLiveLeak(t, baseline)
}
