# Agent Builder Assistant

The Agent Builder Assistant is an agent that builds agents. It interviews you
about the multi-agent system you want, proposes a topology, and writes the YAML
agent configs that ADK's config loader reads. Reach for it when you are
starting a project from an empty directory and do not want to learn the YAML
surface from the Go structs first.

## Introduction

ADK can build an agent tree from YAML. You write `root_agent.yaml`, point the
loader at it, and ADK constructs the agents, their sub-agents and their tools.
Writing that file by hand means knowing which fields each agent class takes,
which tool names the loader resolves, and which adk-python fields adk-go does
not read yet.

The assistant closes that gap. It is an ordinary `llmagent` with a prompt that
carries a reference to adk-go's config surface, and with tools that let it read
your project directory and write files into it. You talk to it through a
`Runner` like any other agent.

It is a design partner, not a code generator you fire and forget. It asks what
the system has to do, asks which model to use, shows you every file before it
writes anything, and waits for you to approve.

Two limits are worth knowing before you start.

The assistant writes YAML, and a custom tool needs Go. It can write the Go
source for a tool, but a `tools:` entry only resolves a name that the program
registered with `configurable.RegisterToolFactory` before it loaded the config.
The assistant says so rather than emitting a config that fails to load.

Everything the assistant writes lands under one directory. That directory is
the sandbox, and the section below explains how you set it.

## Get started

Build the assistant with a model, hand it to a `Runner`, and put the project
directory in the session state.

```go
package main

import (
	"context"
	"log"
	"os"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/builderassistant"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
)

func main() {
	ctx := context.Background()

	llm, err := gemini.NewModel(ctx, "gemini-2.5-pro", &genai.ClientConfig{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
	})
	if err != nil {
		log.Fatalf("create the model: %v", err)
	}

	assistant, err := builderassistant.New(builderassistant.Config{Model: llm})
	if err != nil {
		log.Fatalf("create the assistant: %v", err)
	}

	sessions := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "agent-builder",
		Agent:             assistant,
		SessionService:    sessions,
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("create the runner: %v", err)
	}

	// Every file tool resolves its paths against this directory.
	created, err := sessions.Create(ctx, &session.CreateRequest{
		AppName: "agent-builder",
		UserID:  "dev",
		State: map[string]any{
			builderassistant.RootDirectoryStateKey: "./my_project",
		},
	})
	if err != nil {
		log.Fatalf("create the session: %v", err)
	}

	message := genai.NewContentFromText("Build me a two-stage research pipeline.", genai.RoleUser)
	for event, err := range r.Run(ctx, "dev", created.Session.ID(), message, agent.RunConfig{}) {
		if err != nil {
			log.Fatalf("run: %v", err)
		}
		log.Println(event.LLMResponse.Content)
	}
}
```

## The project directory is a sandbox

`builderassistant.RootDirectoryStateKey` is the session state key `root_directory`.
Its value is the only directory the assistant can touch. When the key is absent,
the sandbox is the process working directory.

Every path the model supplies is resolved against that directory, and a path
that leaves it is refused with `builderassistant.ErrOutsideRoot`. That covers
`..`, an absolute path elsewhere, and a symlink inside the directory that points
out of it. The write tools create the directory when it does not exist yet, so
you can name a project folder before you make it.

An absolute path inside the sandbox is accepted. Spell it with the directory's
own symlink-free prefix; the assistant is told to use relative paths.

## Configuration

```go
type Config struct {
	Model model.LLM
}
```

`Model` is required, and it backs the assistant and both research sub-agents.
Its `Name` is also the model id the assistant writes into the configs it
generates, so use a model ADK's config loader accepts. `New` returns
`ErrNoModel` when it is nil.

## Tools

The assistant has nine tools. Two are research agents it calls as tools, and
seven work on your project directory.

| Tool | What it does |
| --- | --- |
| `google_search_agent` | Searches the web for ADK examples and documentation. |
| `url_context_agent` | Fetches and analyses one URL. |
| `explore_project` | Reports the project layout and the configs already in it. |
| `read_config_files` | Reads YAML configs and reports the agent, its sub-agent references and its tools. |
| `write_config_files` | Checks a config, then writes it. |
| `read_files` | Reads several files. |
| `write_files` | Writes several files, creating any missing parent directory. |
| `delete_files` | Deletes several files. |
| `cleanup_unused_files` | Lists files nothing references. It never deletes. |

`write_config_files` checks each config before it writes it: the YAML has to
parse, the config has to name the agent, and `agent_class` has to be one the
loader registers. A config that fails the check is reported and not written, and
the rest of the batch still goes through. The check is a load-time smoke test,
not a full schema validation.

A tool reports a per-file problem, such as a missing file, in that file's entry
and keeps going. It returns a Go error only when the whole call is unusable: a
path that escapes the sandbox, or a project directory it cannot open.

## What the assistant does not do

`cleanup_unused_files` reports; it does not delete. Deleting is `delete_files`,
after you confirm the list.

The assistant is not registered as a built-in that `adk web` discovers. Build it
in your own program, as the example above does.
