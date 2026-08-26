# Live input for streaming tools

Gives a streaming tool a channel of the live requests the client sends while the
tool runs. Reach for it when a background tool must watch the live stream, for
example a tool that reports what it sees in the user's camera feed.

## Introduction

A streaming tool built with `functiontool.NewStreaming` is output-only. It
yields chunks, ADK sends each chunk to the model as
`Function <name> returned: <chunk>`, and the tool never sees what the client is
sending. That is enough for a tool that counts or polls, and not enough for a
tool whose job is to consume the live stream.

`functiontool.NewLiveStreaming` adds the missing half. Its handler takes one
extra parameter, a receive-only `<-chan agent.LiveRequest`, and ADK copies every
client request into it while the tool runs. Taking that parameter is the whole
opt-in: a `NewStreaming` tool is unaffected and gets no channel. This is the Go
form of the reserved `input_stream` parameter in ADK Python.

The channel is per call, not per tool. Two concurrent calls of the same tool own
separate channels and each sees every request.

## Get started

The tool below reports each JPEG frame it receives. `examples/bidi/streamingtool`
runs it in a full live session.

```go
import (
	"fmt"
	"iter"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool/functiontool"
)

monitorTool, err := functiontool.NewLiveStreaming(functiontool.Config{
	Name:        "monitor_video_stream",
	Description: "Watches the live video stream and reports the frames it receives.",
}, func(ctx agent.Context, args struct{}, input <-chan agent.LiveRequest) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for req := range input {
			blob, ok := req.RealtimeInput.(*genai.Blob)
			if !ok || blob.MIMEType != "image/jpeg" {
				continue
			}
			if !yield(fmt.Sprintf("Saw a video frame of %d bytes.", len(blob.Data)), nil) {
				return
			}
		}
	}
})
```

Register the tool on an agent as usual. The model starts it with a normal
function call, and ADK answers that call at once with a pending status while the
tool keeps running in the background.

## Lifecycle

ADK allocates the channel when the model calls the tool, and closes it when the
tool stops. Both endpoints are handled for you:

- The model calls `stop_streaming` with the tool's name. ADK cancels the tool's
  context and closes its channel.
- The live session ends. ADK closes the channel of every running tool.

A `for req := range input` loop therefore ends on its own, and needs no `select`
on `ctx.Done()`. Outside a live session the channel is already closed, so the
same handler run through the non-live path finishes instead of blocking.

Calling the tool again after `stop_streaming` gives it a fresh channel.

## Delivery guarantees

Delivery is lossy under backpressure. Each channel holds a bounded backlog, and
ADK drops a request rather than block the loop that feeds the model, so a tool
that stops draining loses requests and never stalls the session. Keep the body
of the loop cheap, and hand slow work to a goroutine.

A tool's own output does not come back as its own input. Chunks the tool yields
go to the model only. ADK Python echoes them into the tool's queue instead; ADK
Go does not, because a tool that reads its own output cannot tell client input
from its own chatter.
