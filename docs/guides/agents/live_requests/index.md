# Live requests

`agent.LiveRequest` is the one message type a caller sends into a live
(bidirectional streaming) session. Beyond audio and text it carries three
control signals: end the audio stream, mark a turn as partial, and change
session state.

## Introduction

A live session has two halves. `Runner.RunLive` returns an
`agent.LiveSession` that you send requests to, and an
`iter.Seq2[*session.Event, error]` that you range over for model output. Every
request is an `agent.LiveRequest`, and the runner does two things with it: it
forwards the request to the model, and it decides what to record in session
history.

The two obvious fields cover the common cases. `RealtimeInput` streams audio or
video frames, and `Content` sends a turn of text or a reply to a tool call.
Three more fields cover the cases those two cannot express:

- `AudioStreamEnd` tells the model that the audio stream stopped. With
  automatic activity detection (Voice Activity Detection) the model normally
  waits for a pause before it answers. When your client already knows the
  microphone is off, this flushes the buffered audio at once instead of waiting
  for the timeout.
- `Partial` marks `Content` as an intermediate update. The model receives the
  content, but the turn stays open, and the runner keeps the content out of
  session history.
- `StateDelta` changes session state. The runner applies it whether or not the
  request carries content, so a state change still lands for a content-less,
  partial, or function-response request.

All three are optional, and their zero values reproduce the behaviour of a
request that does not set them.

`StateDelta` is a `*map[string]any`, so write it as
`&map[string]any{"key": value}`. The pointer keeps `LiveRequest` comparable: a
map field would take `==` away from every caller.

## Get started

This program opens a live session, streams audio, flushes it, and records a
state change. It needs a `GOOGLE_API_KEY` and a live-capable model.

```go
package main

import (
	"context"
	"log"
	"os"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
)

func main() {
	ctx := context.Background()

	m, err := gemini.NewModel(ctx, "gemini-3.1-flash-live-preview",
		&genai.ClientConfig{APIKey: os.Getenv("GOOGLE_API_KEY")})
	if err != nil {
		log.Fatal(err)
	}
	a, err := llmagent.New(llmagent.Config{
		Name:        "assistant",
		Model:       m,
		Instruction: "You are a helpful assistant.",
	})
	if err != nil {
		log.Fatal(err)
	}
	r, err := runner.New(runner.Config{
		AppName:           "live-demo",
		Agent:             a,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	sess, events, err := r.RunLive(ctx, "user", "session", agent.LiveRunConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
	})
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		defer sess.Close()

		// Stream microphone audio. Replace micChunks with your capture loop.
		for _, chunk := range micChunks() {
			err := sess.Send(agent.LiveRequest{RealtimeInput: &genai.Blob{
				Data:     chunk,
				MIMEType: "audio/pcm",
			}})
			if err != nil {
				log.Print(err)
				return
			}
		}

		// The microphone is off, so flush rather than wait for the pause.
		if err := sess.Send(agent.LiveRequest{AudioStreamEnd: true}); err != nil {
			log.Print(err)
			return
		}

		// Record a change the user made in the interface.
		err = sess.Send(agent.LiveRequest{
			StateDelta: &map[string]any{"ui_locale": "fr-FR"},
		})
		if err != nil {
			log.Print(err)
		}
	}()

	for event, err := range events {
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("%s: %v", event.Author, event.LLMResponse.Content)
	}
}

func micChunks() [][]byte { return nil }
```

## What reaches the model

`AudioStreamEnd` becomes a realtime-input frame with `audioStreamEnd` set. Send
it as a request of its own: a request that also sets `RealtimeInput` sends the
input and ignores the flag, because the two together have no meaning.

`Partial` sets `turnComplete` to false on the client-content frame, so the
model adds the content to its context and waits for more. A complete turn sets
`turnComplete` to true.

`StateDelta` never reaches the model. It is a session concern only.

## What lands in session history

A `Send` appends at most one event, never two. Which event depends on the
request:

| Request | Appended event |
| --- | --- |
| Content, not partial, not a function response | The user content event, carrying `StateDelta` |
| Content with `Partial: true` | A content-less event, only if `StateDelta` is set |
| Content that is a function response | A content-less event, only if `StateDelta` is set |
| No content | A content-less event, only if `StateDelta` is set |

Every event this path appends is authored `user`. A state change therefore
survives on its own, and a partial turn leaves no trace in history even though
the model saw it.

`AudioStreamEnd` appends nothing by itself. It is a signal to the model, not a
turn.
