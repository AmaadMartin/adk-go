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
	"errors"
	"io"
	"iter"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	icontext "google.golang.org/adk/v2/internal/context"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// waitFor is how long a test waits for a background tool goroutine to react.
const waitFor = 5 * time.Second

// liveInputProbe reports what an input-streaming tool observed. Every channel
// is buffered, so the tool never blocks on a test that stopped reading.
type liveInputProbe struct {
	// started carries one value per handler invocation that began.
	started chan struct{}
	// received carries one value per live request the handler read.
	received chan agent.LiveRequest
	// finished carries one value per handler invocation that returned.
	finished chan struct{}
}

func newLiveInputProbe(capacity int) *liveInputProbe {
	return &liveInputProbe{
		started:  make(chan struct{}, capacity),
		received: make(chan agent.LiveRequest, capacity),
		finished: make(chan struct{}, capacity),
	}
}

// tool builds an input-streaming tool that reports every request it reads and
// yields one chunk per request.
func (p *liveInputProbe) tool(t *testing.T, name string) tool.Tool {
	t.Helper()
	return newLiveStreamingTool(t, name, func(input <-chan agent.LiveRequest, yield func(string, error) bool) {
		p.started <- struct{}{}
		defer func() { p.finished <- struct{}{} }()
		for req := range input {
			p.received <- req
			if !yield("saw frame", nil) {
				return
			}
		}
	})
}

// drainingTool builds an input-streaming tool that only drains its input, so
// its handler returns exactly when the channel closes.
func (p *liveInputProbe) drainingTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	return newLiveStreamingTool(t, name, func(input <-chan agent.LiveRequest, _ func(string, error) bool) {
		p.started <- struct{}{}
		defer func() { p.finished <- struct{}{} }()
		for req := range input {
			p.received <- req
		}
	})
}

// awaitStarted fails unless a handler invocation began within waitFor. The
// flow registers a tool before it launches the handler, so a started handler
// also means the registration is done.
func (p *liveInputProbe) awaitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(waitFor):
		t.Fatal("the tool handler never started")
	}
}

// awaitFinished fails unless a handler invocation returned within waitFor.
func (p *liveInputProbe) awaitFinished(t *testing.T, why string) {
	t.Helper()
	select {
	case <-p.finished:
	case <-time.After(waitFor):
		t.Fatalf("the tool handler never returned: %s", why)
	}
}

// awaitReceived fails unless the handler read a request within waitFor.
func (p *liveInputProbe) awaitReceived(t *testing.T) agent.LiveRequest {
	t.Helper()
	select {
	case req := <-p.received:
		return req
	case <-time.After(waitFor):
		t.Fatal("the tool handler never read a live request")
		return agent.LiveRequest{}
	}
}

func newLiveStreamingTool(t *testing.T, name string, body func(input <-chan agent.LiveRequest, yield func(string, error) bool)) tool.Tool {
	t.Helper()
	created, err := functiontool.NewLiveStreaming(functiontool.Config{
		Name:        name,
		Description: "reads live client input",
	}, func(_ agent.Context, _ struct{}, input <-chan agent.LiveRequest) iter.Seq2[string, error] {
		return func(yield func(string, error) bool) {
			body(input, yield)
		}
	})
	if err != nil {
		t.Fatalf("NewLiveStreaming() failed: %v", err)
	}
	return created
}

// jpegFrame is the request shape the canonical video-monitoring tool filters
// for.
func jpegFrame(data string) agent.LiveRequest {
	return agent.LiveRequest{RealtimeInput: &genai.Blob{MIMEType: "image/jpeg", Data: []byte(data)}}
}

func callTool(callID, name string) *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{
			Role: "model",
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{ID: callID, Name: name, Args: map[string]any{}},
			}},
		},
	}
}

func stopStreaming(callID, name string) *model.LLMResponse {
	return &model.LLMResponse{
		Content: &genai.Content{
			Role: "model",
			Parts: []*genai.Part{{
				FunctionCall: &genai.FunctionCall{
					ID:   callID,
					Name: "stop_streaming",
					Args: map[string]any{"function_name": name},
				},
			}},
		},
	}
}

// newLiveToolHarness wires a live session to the flow the way RunLive does,
// minus the model connection: toModel receives the text of everything the flow
// sends to the model.
func newLiveToolHarness(t *testing.T, tools ...tool.Tool) (agent.InvocationContext, *Flow, map[string]tool.Tool, *liveSessionImpl, <-chan string) {
	t.Helper()
	invCtx := icontext.NewInvocationContext(t.Context(), icontext.InvocationContextParams{
		InvocationID: "inv_1",
		Agent:        &mockAgent{name: "agent_1"},
	})
	toolsDict := make(map[string]tool.Tool, len(tools))
	for _, tl := range tools {
		toolsDict[tl.Name()] = tl
	}
	sess := newLiveSessionImpl()
	t.Cleanup(func() { _ = sess.Close() })

	toModel := make(chan string, 256)
	go func() {
		for {
			select {
			case req := <-sess.inputCh:
				if req.Content != nil && len(req.Content.Parts) > 0 {
					select {
					case toModel <- req.Content.Parts[0].Text:
					default:
					}
				}
			case <-sess.done:
				return
			}
		}
	}()
	return invCtx, &Flow{Tools: tools}, toolsDict, sess, toModel
}

// activeTasks returns the tasks registered for a tool name.
func activeTasks(t *testing.T, sess *liveSessionImpl, name string) []*activeTask {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.activeTools[name]
}

func TestLiveInputStreamingTool_ReceivesClientInput(t *testing.T) {
	probe := newLiveInputProbe(8)
	monitor := probe.tool(t, "monitor_video_stream")
	invCtx, flow, toolsDict, sess, toModel := newLiveToolHarness(t, monitor)

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_1", "monitor_video_stream"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}

	frames := []string{"frame-0", "frame-1", "frame-2"}
	for _, frame := range frames {
		if err := sess.Send(jpegFrame(frame)); err != nil {
			t.Fatalf("Send(%s) failed: %v", frame, err)
		}
	}

	for _, want := range frames {
		got := probe.awaitReceived(t)
		blob, ok := got.RealtimeInput.(*genai.Blob)
		if !ok {
			t.Fatalf("RealtimeInput = %T, want *genai.Blob", got.RealtimeInput)
		}
		if string(blob.Data) != want {
			t.Errorf("frame = %q, want %q", blob.Data, want)
		}
	}

	// Each frame the tool read produced one chunk back to the model.
	for range frames {
		select {
		case got := <-toModel:
			if want := "Function monitor_video_stream returned: saw frame"; got != want {
				t.Errorf("chunk sent to the model = %q, want %q", got, want)
			}
		case <-time.After(waitFor):
			t.Fatal("the tool's chunk never reached the model")
		}
	}
}

func TestLiveInputStreamingTool_OptInOnly(t *testing.T) {
	probe := newLiveInputProbe(4)
	optedIn := probe.tool(t, "opted_in")

	plain, err := functiontool.NewStreaming(functiontool.Config{
		Name:        "plain_stream",
		Description: "does not read live input",
	}, func(_ agent.Context, _ struct{}) iter.Seq2[string, error] {
		return func(yield func(string, error) bool) {
			// Stay registered so the assertions below see the task.
			<-time.After(waitFor)
			yield("done", nil)
		}
	})
	if err != nil {
		t.Fatalf("NewStreaming() failed: %v", err)
	}

	invCtx, flow, toolsDict, sess, _ := newLiveToolHarness(t, optedIn, plain)

	// Nothing is registered before the model calls a tool: the channel is
	// allocated lazily, per call.
	if got := len(sess.activeTools); got != 0 {
		t.Fatalf("registered tools before any call = %d, want 0", got)
	}

	for _, name := range []string{"opted_in", "plain_stream"} {
		if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_"+name, name), nil, sess); err != nil {
			t.Fatalf("handleFunctionCalls(%s) failed: %v", name, err)
		}
	}

	tasks := activeTasks(t, sess, "opted_in")
	if len(tasks) != 1 || tasks[0].input == nil {
		t.Fatalf("opted-in tool tasks = %v, want one task with an input channel", tasks)
	}
	tasks = activeTasks(t, sess, "plain_stream")
	if len(tasks) != 1 || tasks[0].input != nil {
		t.Fatalf("plain tool tasks = %v, want one task with no input channel", tasks)
	}

	if err := sess.Send(jpegFrame("frame-0")); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	probe.awaitReceived(t)
}

func TestLiveInputStreamingTool_StopStreamingClosesInput(t *testing.T) {
	probe := newLiveInputProbe(8)
	monitor := probe.drainingTool(t, "monitor_video_stream")
	invCtx, flow, toolsDict, sess, _ := newLiveToolHarness(t, monitor)

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_1", "monitor_video_stream"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}
	if err := sess.Send(jpegFrame("frame-0")); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	probe.awaitReceived(t)

	stopEvent, err := flow.handleFunctionCalls(invCtx, toolsDict, stopStreaming("call_stop", "monitor_video_stream"), nil, sess)
	if err != nil {
		t.Fatalf("handleFunctionCalls(stop_streaming) failed: %v", err)
	}
	gotStatus := stopEvent.LLMResponse.Content.Parts[0].FunctionResponse.Response["status"]
	if want := "Successfully stopped all running instances of monitor_video_stream"; gotStatus != want {
		t.Errorf("stop status = %v, want %q", gotStatus, want)
	}

	probe.awaitFinished(t, "stop_streaming must close its input channel")

	if got := activeTasks(t, sess, "monitor_video_stream"); len(got) != 0 {
		t.Errorf("tasks after stop_streaming = %v, want none", got)
	}
	// A later request reaches neither a closed channel nor a stale entry.
	if err := sess.Send(jpegFrame("frame-1")); err != nil {
		t.Fatalf("Send() after stop_streaming failed: %v", err)
	}
	select {
	case req := <-probe.received:
		t.Errorf("the stopped tool still received %v", req)
	default:
	}

	// Stopping it a second time finds nothing left to stop.
	stopEvent, err = flow.handleFunctionCalls(invCtx, toolsDict, stopStreaming("call_stop_2", "monitor_video_stream"), nil, sess)
	if err != nil {
		t.Fatalf("handleFunctionCalls(stop_streaming) failed: %v", err)
	}
	gotStatus = stopEvent.LLMResponse.Content.Parts[0].FunctionResponse.Response["status"]
	if want := "No active streaming function named monitor_video_stream found"; gotStatus != want {
		t.Errorf("second stop status = %v, want %q", gotStatus, want)
	}
}

func TestLiveInputStreamingTool_SendAfterCloseIsRejected(t *testing.T) {
	probe := newLiveInputProbe(4)
	monitor := probe.drainingTool(t, "monitor_video_stream")
	invCtx, flow, toolsDict, sess, _ := newLiveToolHarness(t, monitor)

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_1", "monitor_video_stream"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}
	probe.awaitStarted(t)
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	probe.awaitFinished(t, "closing the session must close its input channel")

	// A request the session rejects reaches no tool.
	if err := sess.Send(jpegFrame("frame-0")); !errors.Is(err, io.EOF) {
		t.Errorf("Send() after Close error = %v, want %v", err, io.EOF)
	}
	select {
	case req := <-probe.received:
		t.Errorf("the tool received %v from a closed session", req)
	default:
	}
}

func TestLiveInputStreamingTool_SessionCloseClosesInput(t *testing.T) {
	probe := newLiveInputProbe(4)
	monitor := probe.drainingTool(t, "monitor_video_stream")
	invCtx, flow, toolsDict, sess, _ := newLiveToolHarness(t, monitor)

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_1", "monitor_video_stream"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}
	if err := sess.Send(jpegFrame("frame-0")); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	probe.awaitReceived(t)

	if err := sess.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	probe.awaitFinished(t, "closing the session must close its input channel")
}

func TestLiveInputStreamingTool_RegisterAfterCloseGetsClosedChannel(t *testing.T) {
	sess := newLiveSessionImpl()
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	input := sess.RegisterStreamingTool("late_tool", "call_1", func() {}, true)
	if input == nil {
		t.Fatal("RegisterStreamingTool() returned no channel for an opted-in tool")
	}
	select {
	case _, open := <-input:
		if open {
			t.Error("the channel delivered a request, want it closed")
		}
	case <-time.After(waitFor):
		t.Error("registering after Close left the input channel open, so the tool would block forever")
	}
}

func TestLiveInputStreamingTool_ConcurrentCallsGetSeparateChannels(t *testing.T) {
	probe := newLiveInputProbe(8)
	monitor := probe.drainingTool(t, "monitor_video_stream")
	invCtx, flow, toolsDict, sess, _ := newLiveToolHarness(t, monitor)

	twoCalls := &model.LLMResponse{
		Content: &genai.Content{
			Role: "model",
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "call_1", Name: "monitor_video_stream", Args: map[string]any{}}},
				{FunctionCall: &genai.FunctionCall{ID: "call_2", Name: "monitor_video_stream", Args: map[string]any{}}},
			},
		},
	}
	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, twoCalls, nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}

	tasks := activeTasks(t, sess, "monitor_video_stream")
	if len(tasks) != 2 {
		t.Fatalf("registered tasks = %d, want 2", len(tasks))
	}
	if tasks[0].input == nil || tasks[1].input == nil {
		t.Fatal("both concurrent calls must own an input channel")
	}
	if tasks[0].input == tasks[1].input {
		t.Error("the two calls share one input channel, want one each")
	}

	if err := sess.Send(jpegFrame("frame-0")); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	// Both invocations observe the same request.
	for range 2 {
		probe.awaitReceived(t)
	}
}

func TestLiveInputStreamingTool_SlowToolDoesNotStallSend(t *testing.T) {
	started := make(chan struct{}, 1)
	stalled := newLiveStreamingTool(t, "never_drains", func(_ <-chan agent.LiveRequest, _ func(string, error) bool) {
		started <- struct{}{}
		// Park without reading the input channel until the test's context ends.
		<-time.After(waitFor)
	})
	invCtx, flow, toolsDict, sess, _ := newLiveToolHarness(t, stalled)

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_1", "never_drains"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}
	select {
	case <-started:
	case <-time.After(waitFor):
		t.Fatal("the tool never started")
	}

	sent := make(chan error, 1)
	go func() {
		for range liveToolInputBuffer + 10 {
			if err := sess.Send(jpegFrame("frame")); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()

	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("Send() failed: %v", err)
		}
	case <-time.After(waitFor):
		t.Fatal("Send stalled on a tool that stopped draining its input channel")
	}
}

func TestLiveInputStreamingTool_ToolOutputIsNotEchoed(t *testing.T) {
	probe := newLiveInputProbe(8)
	chatty := newLiveStreamingTool(t, "chatty", func(input <-chan agent.LiveRequest, yield func(string, error) bool) {
		defer func() { probe.finished <- struct{}{} }()
		if !yield("hello", nil) {
			return
		}
		for req := range input {
			probe.received <- req
		}
	})
	invCtx, flow, toolsDict, sess, toModel := newLiveToolHarness(t, chatty)

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_1", "chatty"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}

	select {
	case got := <-toModel:
		if want := "Function chatty returned: hello"; got != want {
			t.Fatalf("chunk sent to the model = %q, want %q", got, want)
		}
	case <-time.After(waitFor):
		t.Fatal("the tool's chunk never reached the model")
	}

	// A client frame sent after the chunk must be the first thing the tool
	// reads: its own output must not be in the channel ahead of it.
	if err := sess.Send(jpegFrame("frame-0")); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	got := probe.awaitReceived(t)
	blob, ok := got.RealtimeInput.(*genai.Blob)
	if !ok {
		t.Fatalf("the tool read %+v, want the client frame; its own output was echoed back", got)
	}
	if string(blob.Data) != "frame-0" {
		t.Errorf("frame = %q, want %q", blob.Data, "frame-0")
	}
}

func TestLiveInputStreamingTool_ReInvocationAfterStop(t *testing.T) {
	probe := newLiveInputProbe(8)
	monitor := probe.drainingTool(t, "monitor_video_stream")
	invCtx, flow, toolsDict, sess, _ := newLiveToolHarness(t, monitor)

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_1", "monitor_video_stream"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() failed: %v", err)
	}
	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, stopStreaming("call_stop", "monitor_video_stream"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls(stop_streaming) failed: %v", err)
	}
	probe.awaitFinished(t, "stop_streaming must close its input channel")

	if _, err := flow.handleFunctionCalls(invCtx, toolsDict, callTool("call_2", "monitor_video_stream"), nil, sess); err != nil {
		t.Fatalf("handleFunctionCalls() after stop_streaming failed: %v", err)
	}
	tasks := activeTasks(t, sess, "monitor_video_stream")
	if len(tasks) != 1 || tasks[0].input == nil || tasks[0].closed {
		t.Fatalf("tasks after re-invocation = %v, want one task with an open input channel", tasks)
	}

	if err := sess.Send(jpegFrame("frame-1")); err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	got := probe.awaitReceived(t)
	blob, ok := got.RealtimeInput.(*genai.Blob)
	if !ok {
		t.Fatalf("RealtimeInput = %T, want *genai.Blob", got.RealtimeInput)
	}
	if string(blob.Data) != "frame-1" {
		t.Errorf("frame = %q, want %q", blob.Data, "frame-1")
	}
}

// serverFunctionCall is the wire frame that makes the fake live model call the
// input-streaming tool.
const serverFunctionCall = `{"serverContent":{"modelTurn":{"parts":[{"functionCall":{"id":"call_1","name":"monitor_video_stream","args":{}}}],"role":"model"}}}`

// TestRunLiveInputStreamingToolNoGoroutineLeak drives a whole RunLive session
// against the fake live server. Teardown must release the input-streaming
// tool: its handler is parked on the input channel, and only the session can
// close that channel.
//
// The test does not call sess.Send. RunLive writes the tool's pending
// FunctionResponse from its consumer goroutine (base_flow.go:637) while its
// sender goroutine writes client input (base_flow.go:558), and the two share
// one websocket with no synchronisation. That race predates this change, and
// no ordering the test can impose creates a happens-before edge between the
// two goroutines, so a Send here would only report the pre-existing race
// against this test. Frame delivery is covered by the tests above, which drive
// the same liveSessionImpl directly.
func TestRunLiveInputStreamingToolNoGoroutineLeak(t *testing.T) {
	baseline, _ := runLiveStacks()
	probe := newLiveInputProbe(4)
	monitor := probe.drainingTool(t, "monitor_video_stream")

	client, connCount := startFakeLiveServer(t, func(connNum int, conn *websocket.Conn) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(serverFunctionCall)); err != nil {
			return
		}
		blockUntilClientCloses(conn)
	})

	f := &Flow{
		Model:             &fakeLiveModel{client: client},
		Tools:             []tool.Tool{monitor},
		RequestProcessors: []func(ctx agent.InvocationContext, req *model.LLMRequest, f *Flow) iter.Seq2[*session.Event, error]{liveConfigProcessor},
	}
	ctx, cancel := newLiveInvocationContext(t)
	defer cancel()

	_, seq, err := f.RunLive(ctx)
	if err != nil {
		t.Fatalf("RunLive failed: %v", err)
	}

	// The consumer must run throughout: RunLive pushes every event before it
	// handles the function call that event carries.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range seq {
		}
	}()

	probe.awaitStarted(t)
	cancel()
	probe.awaitFinished(t, "tearing the session down must close the tool's input channel")

	select {
	case <-drained:
	case <-time.After(waitFor):
		t.Fatal("the event iterator never finished after the invocation was cancelled")
	}
	if got := connCount.Load(); got != 1 {
		t.Errorf("connection count = %d, want 1", got)
	}
	assertNoRunLiveLeak(t, baseline)
}
