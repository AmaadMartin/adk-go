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

// Package tool defines internal-only interfaces and logic for tools.
package toolinternal

import (
	"iter"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

type FunctionTool interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(ctx agent.Context, args any) (result map[string]any, err error)
}

type StreamingFunctionTool interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	RunStream(ctx agent.Context, args any) iter.Seq2[string, error]
}

// LiveInputStreamingTool is a streaming tool that consumes live client input
// while it runs. The live flow gives such a tool a dedicated channel of
// incoming requests; a tool that does not implement this interface keeps the
// plain [StreamingFunctionTool] path and is unaffected.
//
// Implementing the interface is the opt-in: there is no extra flag to check.
type LiveInputStreamingTool interface {
	StreamingFunctionTool
	RunStreamLive(ctx agent.Context, args any, input <-chan agent.LiveRequest) iter.Seq2[string, error]
}

// closedLiveRequests is shared by every caller: an already-closed channel
// carries no state, so a single instance is enough.
var closedLiveRequests = func() chan agent.LiveRequest {
	ch := make(chan agent.LiveRequest)
	close(ch)
	return ch
}()

// ClosedLiveRequests returns an already-closed live-request channel, for an
// input-streaming tool that runs with no live input source. A handler ranging
// over it finishes at once instead of blocking forever.
func ClosedLiveRequests() <-chan agent.LiveRequest {
	return closedLiveRequests
}

type RequestProcessor interface {
	ProcessRequest(ctx agent.Context, req *model.LLMRequest) error
}

// ResponseDeferrer allows to skip generation of the FR by the tool.
// Used in the cases when FR is generated externally (e.g. TaskAgentTool)
type ResponseDeferrer interface {
	DefersResponse() bool
}
