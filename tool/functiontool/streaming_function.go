// Copyright 2025 Google LLC
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

package functiontool

import (
	"fmt"
	"iter"
	"reflect"
	"runtime/debug"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/internal/toolinternal"
	"google.golang.org/adk/v2/internal/typeutil"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolutils"
)

// StreamingFunc represents a Go function that streams results.
type StreamingFunc[TArgs any] func(agent.Context, TArgs) iter.Seq2[string, error]

// LiveStreamingFunc is a streaming tool handler that also consumes live client
// input.
//
// In a live (bidirectional streaming) session the flow gives the handler a
// dedicated channel carrying every [agent.LiveRequest] the client sends while
// the tool runs. The channel is closed when the tool is cancelled through
// stop_streaming, and when the live session ends, so a "for req := range input"
// loop terminates on its own. Outside a live session the channel is already
// closed.
//
// Delivery is lossy under backpressure: a handler that stops draining the
// channel loses requests rather than stalling the session.
type LiveStreamingFunc[TArgs any] func(ctx agent.Context, args TArgs, input <-chan agent.LiveRequest) iter.Seq2[string, error]

// NewStreaming creates a new streaming tool.
func NewStreaming[TArgs any](cfg Config, handler StreamingFunc[TArgs]) (tool.Tool, error) {
	return newStreamingTool(cfg, func(ctx agent.Context, args TArgs, _ <-chan agent.LiveRequest) iter.Seq2[string, error] {
		return handler(ctx, args)
	})
}

// NewLiveStreaming creates a streaming tool that receives live client input
// while it runs. The tool behaves like a [NewStreaming] tool in every other
// respect.
func NewLiveStreaming[TArgs any](cfg Config, handler LiveStreamingFunc[TArgs]) (tool.Tool, error) {
	base, err := newStreamingTool(cfg, handler)
	if err != nil {
		return nil, err
	}
	return &liveStreamingFunctionTool[TArgs]{streamingFunctionTool: base}, nil
}

// newStreamingTool validates cfg and builds the tool both streaming
// constructors are built on.
func newStreamingTool[TArgs any](cfg Config, handler LiveStreamingFunc[TArgs]) (*streamingFunctionTool[TArgs], error) {
	var zeroArgs TArgs
	argsType := reflect.TypeOf(zeroArgs)
	for argsType != nil && argsType.Kind() == reflect.Pointer {
		argsType = argsType.Elem()
	}
	if argsType == nil || (argsType.Kind() != reflect.Struct && argsType.Kind() != reflect.Map) {
		return nil, fmt.Errorf("input must be a struct or a map or a pointer to those types, but received: %v: %w", argsType, ErrInvalidArgument)
	}

	ischema, err := resolvedSchema[TArgs](cfg.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to infer input schema: %w", err)
	}

	var confirmWrapper func(TArgs) bool

	if cfg.RequireConfirmationProvider != nil {
		fn, ok := cfg.RequireConfirmationProvider.(func(TArgs) bool)
		if !ok {
			return nil, fmt.Errorf("error RequireConfirmationProvider must be a function with signature func(%T) bool", *new(TArgs))
		}
		confirmWrapper = fn
	}

	return &streamingFunctionTool[TArgs]{
		cfg:                         cfg,
		inputSchema:                 ischema,
		handler:                     handler,
		requireConfirmation:         cfg.RequireConfirmation,
		requireConfirmationProvider: confirmWrapper,
	}, nil
}

// streamingFunctionTool wraps a Go function that streams results.
type streamingFunctionTool[TArgs any] struct {
	cfg Config

	// A JSON Schema object defining the expected parameters for the tool.
	inputSchema *jsonschema.Resolved

	// handler is the Go function.
	handler LiveStreamingFunc[TArgs]

	requireConfirmation bool

	requireConfirmationProvider func(TArgs) bool
}

// liveStreamingFunctionTool is the tool [NewLiveStreaming] returns. The extra
// method is what makes it, and only it, a toolinternal.LiveInputStreamingTool.
type liveStreamingFunctionTool[TArgs any] struct {
	*streamingFunctionTool[TArgs]
}

// RunStreamLive implements toolinternal.LiveInputStreamingTool.
func (f *liveStreamingFunctionTool[TArgs]) RunStreamLive(ctx agent.Context, args any, input <-chan agent.LiveRequest) iter.Seq2[string, error] {
	return f.run(ctx, args, input)
}

// Description implements tool.Tool.
func (f *streamingFunctionTool[TArgs]) Description() string {
	return f.cfg.Description
}

// Name implements tool.Tool.
func (f *streamingFunctionTool[TArgs]) Name() string {
	return f.cfg.Name
}

// IsLongRunning implements tool.Tool.
func (f *streamingFunctionTool[TArgs]) IsLongRunning() bool {
	return f.cfg.IsLongRunning
}

// ProcessRequest packs the function tool's declaration into the LLM request.
func (f *streamingFunctionTool[TArgs]) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, f)
}

// FunctionDeclaration implements toolinternal.StreamingFunctionTool.
func (f *streamingFunctionTool[TArgs]) Declaration() *genai.FunctionDeclaration {
	decl := &genai.FunctionDeclaration{
		Name:        f.Name(),
		Description: f.Description(),
	}
	if f.inputSchema != nil {
		decl.ParametersJsonSchema = f.inputSchema.Schema()
	}

	if f.cfg.IsLongRunning {
		instruction := "NOTE: This is a long-running operation. Do not call this tool again if it has already returned some intermediate or pending status."
		if decl.Description != "" {
			decl.Description += "\n\n" + instruction
		} else {
			decl.Description = instruction
		}
	}

	return decl
}

// RunStream executes the tool with the provided context and yields events.
func (f *streamingFunctionTool[TArgs]) RunStream(ctx agent.Context, args any) iter.Seq2[string, error] {
	return f.run(ctx, args, toolinternal.ClosedLiveRequests())
}

// run executes the tool and yields its chunks. input carries the live client
// requests; it is already closed for a tool that did not ask for them.
func (f *streamingFunctionTool[TArgs]) run(ctx agent.Context, args any, input <-chan agent.LiveRequest) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		defer func() {
			if r := recover(); r != nil {
				yield("", fmt.Errorf("panic in tool %q: %v\nstack: %s", f.Name(), r, debug.Stack()))
			}
		}()

		m, ok := args.(map[string]any)
		if !ok {
			yield("", fmt.Errorf("unexpected args type, got: %T", args))
			return
		}
		toolArgs, err := typeutil.ConvertToWithJSONSchema[map[string]any, TArgs](m, f.inputSchema)
		if err != nil {
			yield("", err)
			return
		}

		if confirmation := ctx.ToolConfirmation(); confirmation != nil {
			if !confirmation.Confirmed {
				yield("", fmt.Errorf("error tool %q %w", f.Name(), tool.ErrConfirmationRejected))
				return
			}
		} else {
			requireConfirmation := f.requireConfirmation

			if f.requireConfirmationProvider != nil {
				requireConfirmation = f.requireConfirmationProvider(toolArgs)
			}

			if requireConfirmation {
				err := ctx.RequestConfirmation(
					fmt.Sprintf("Please approve or reject the tool call %s() by responding with a FunctionResponse with an expected ToolConfirmation payload.",
						f.Name()), nil)
				if err != nil {
					yield("", err)
					return
				}
				ctx.Actions().SkipSummarization = true
				yield("", fmt.Errorf("error tool %q %w", f.Name(), tool.ErrConfirmationRequired))
				return
			}
		}

		for res, err := range f.handler(ctx, toolArgs, input) {
			if !yield(res, err) {
				return
			}
		}
	}
}
