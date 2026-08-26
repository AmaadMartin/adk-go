# adkrestclient

`adkrestclient` is an HTTP client for an ADK web server. Reach for it when Go
code must drive a running server over the network: a conformance harness, an
end-to-end test, or a script that creates a session and streams a run.

## Introduction

`server/adkrest` serves the ADK REST API. Until now nothing in adk-go could
call it, so callers built `http.Request` values by hand and split the
Server-Sent Events stream themselves. The client removes that work and shares
one definition of the wire format with the server, so the two cannot drift.

The client covers the four operations the conformance harness uses: create,
read and delete a session, and run an agent. It does not cover `/run` or
`/run_live`; use the server package directly for those.

`RunAgent` streams. It returns `iter.Seq2[*session.Event, error]`, the same
shape `runner.Runner.Run` returns, so an in-process run and a remote run read
the same way at the call site.

## Get started

```go
package main

import (
	"context"
	"fmt"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/server/adkrest/adkrestclient"
)

func main() {
	ctx := context.Background()

	c, err := adkrestclient.New(adkrestclient.Config{BaseURL: "http://127.0.0.1:8000"})
	if err != nil {
		panic(err)
	}
	defer c.Close()

	s, err := c.CreateSession(ctx, "my_app", "u1", nil)
	if err != nil {
		panic(err)
	}

	req := adkrestclient.RunRequest{
		AppName:    "my_app",
		UserID:     "u1",
		SessionID:  s.ID,
		NewMessage: genai.NewContentFromText("hello", genai.RoleUser),
	}
	for event, err := range c.RunAgent(ctx, req) {
		if err != nil {
			panic(err)
		}
		fmt.Println(event.Author, event.Content)
	}
}
```

Start the server it talks to with `go run ./cmd/internal/adkcli web` from a
directory that holds a `root_agent.yaml`.

## Configuration

`BaseURL` defaults to `http://127.0.0.1:8000`, the address `adk web` listens
on, and a trailing slash is trimmed. `New` rejects a base URL that is not
`http` or `https`.

`Timeout` defaults to 30 seconds and bounds each session request. It does not
bound `RunAgent`: an agent run can take minutes, so the stream lives as long as
the context you pass. Cancel that context to stop a run.

`HTTPClient` replaces the underlying `*http.Client`. Use it to add a transport,
a proxy, or credentials.

`Close` releases idle connections. The client stays usable afterwards, and
calling it is optional.

## Conformance runs

The ADK web server can record the model traffic of a run, or replay recorded
traffic instead of calling a model. The server reads the mode from the session
state, and `WithConformance` writes it there:

```go
for event, err := range c.RunAgent(ctx, req,
	adkrestclient.WithConformance(adkrestclient.ModeReplay, caseDir, 0)) {
	// ...
}
```

`caseDir` is the test case directory on the *server's* disk, and the third
argument is the index of this message within the test case. The server rejects
a directory outside the base directory its plugins allow, and the client
reports that rejection as an error.

`WithConformance` does not modify the `RunRequest` you passed. A `StateDelta`
you set survives alongside the conformance key.

## Failure modes

`RunAgent` yields at most one error and then stops. Three cases produce one:

- The options are invalid. An empty test case directory, a negative message
  index, or a mode other than `ModeRecord` and `ModeReplay` fails on the first
  iteration, and the client sends no request.
- The server streams an error payload. The server writes `event: error`
  followed by a `data:` line holding the message, and the client yields that
  message as an error.
- The transport or the stream fails. The client wraps the underlying error, so
  `errors.Is(err, context.Canceled)` works after you cancel the context.

Breaking out of the loop closes the response body.

The session methods return an error for any non-2xx response. The error names
the method, the path, the status code, and the start of the response body.
