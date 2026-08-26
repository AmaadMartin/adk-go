# App inspection and out-of-band updates over REST

`server/adkrest` serves four endpoints that let a client read what an app is and
change a session without running the agent. Reach for them when a UI or a script
must inspect an agent tree, seed session state, or promote a conversation into
memory.

## Introduction

The ADK REST API is shared across ADK implementations, so the ADK Web UI and the
adk-python client both expect the same paths. Four of those paths answer
questions that have no other REST route.

`GET /version` reports the runtime. A client reads it to learn which ADK it talks
to before it uses a newer route.

`GET /apps/{app_name}/app-info` returns the agent tree. It answers "what is this
app made of" before any run happens, so a UI can draw the tree from a cold start.
`GET /list-apps` only returns names.

The two `PATCH` routes change server state without an agent run. Patching a
session merges a state delta; patching memory hands a finished session to the
memory service. `POST /run` also changes state, but only as a side effect of
invoking the model, which costs a model call and appends model output.

## Get started

This server exposes all four endpoints. It needs a memory service only for the
memory route.

```go
package main

import (
	"log"
	"net/http"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/server/adkrest"
	"google.golang.org/adk/v2/session"
)

func main() {
	child, err := llmagent.New(llmagent.Config{Name: "child", Description: "A child."})
	if err != nil {
		log.Fatal(err)
	}
	root, err := llmagent.New(llmagent.Config{
		Name:        "demo",
		Description: "Demo app.",
		Instruction: "Be brief.",
		SubAgents:   []agent.Agent{child},
	})
	if err != nil {
		log.Fatal(err)
	}
	server, err := adkrest.NewServer(adkrest.ServerConfig{
		AgentLoader:    agent.NewSingleLoader(root),
		SessionService: session.InMemoryService(),
		MemoryService:  memory.InMemoryService(),
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe(":8099", server))
}
```

Read the runtime:

```bash
curl -s localhost:8099/version
```

The reply carries three keys: `version` is the ADK Go version, `language` is
always `go`, and `language_version` is the Go toolchain version with its `go`
prefix removed.

Read the agent tree:

```bash
curl -s localhost:8099/apps/demo/app-info
```

```json
{
  "name": "demo",
  "rootAgentName": "demo",
  "description": "Demo app.",
  "language": "go",
  "isComputerUse": false,
  "agents": {
    "child": {
      "name": "child",
      "description": "A child.",
      "instruction": "",
      "tools": [],
      "sub_agents": []
    },
    "demo": {
      "name": "demo",
      "description": "Demo app.",
      "instruction": "Be brief.",
      "tools": [],
      "sub_agents": ["child"]
    }
  }
}
```

Seed session state, then hand the session to memory:

```bash
curl -s -X POST localhost:8099/apps/demo/users/u1/sessions/s1
curl -s -X PATCH localhost:8099/apps/demo/users/u1/sessions/s1 \
  -H 'Content-Type: application/json' -d '{"stateDelta":{"theme":"dark"}}'
curl -s -X PATCH localhost:8099/apps/demo/users/u1/memory \
  -H 'Content-Type: application/json' -d '{"sessionId":"s1"}'
```

## What the responses guarantee

The JSON keys match adk-python exactly, and the casing is not uniform. `AppInfo`
uses `rootAgentName` and `isComputerUse`, while the nested agent entries use
`sub_agents`. `/version` uses `language_version`. A client written against the
Python server needs no changes.

`app-info` reports the tree keyed by agent name. Only LLM agents appear: a
sub-agent built with `agent.New` is left out, because it carries no instruction
or tool list to report. The walk visits each name once, so a name that repeats in
the tree is reported once.

`tools` lists the function declaration of each directly attached tool. A tool
with no declaration is omitted, so a built-in such as `geminitool.GoogleSearch`
does not appear. Toolsets are not expanded: `tool.Toolset.Tools` needs an
`agent.ReadonlyContext` that exists only inside an invocation.

Patching a session appends one event authored by `user`, whose `invocationId`
starts with `p-`. The session service applies the delta when it appends that
event, so the response is the session with the delta already merged. Keys the
delta does not mention keep their value.

The session route reads `stateDelta` or `state_delta`. It accepts the second
spelling because adk-python's conformance client hand-writes it when it updates
a session, while it camel-cases every other call. The memory route reads
`sessionId` only; no client sends another spelling for it.

## Failure modes

| Request | Response |
| ------- | -------- |
| `app-info` for an app name starting with `__` | 403; those names are reserved for internal agents |
| `app-info` for an unknown app | 404 with the loader's error |
| `app-info` whose root agent is not an LLM agent | 400 |
| `PATCH` session with a body carrying neither spelling | 400 |
| `PATCH` memory with a body carrying no `sessionId` | 400 |
| `PATCH` session or memory for an unknown session | 404 |
| `PATCH` memory with no memory service configured | 400 |

## Serving through adkgo web

`adkgo web` allowlists the HTTP methods it forwards, and it forwards `PATCH`. Its
CORS preflight advertises `PATCH` too, so a browser client can call both routes.
