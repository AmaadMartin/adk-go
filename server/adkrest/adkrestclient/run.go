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

package adkrestclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/session"
)

// runSSEPath is the streaming run endpoint of the ADK web server.
const runSSEPath = "/run_sse"

// State keys the conformance plugins read. They stay snake_case inside the
// state map even though the request envelope is camelCase.
const (
	replayConfigKey     = "_adk_replay_config"
	recordingsConfigKey = "_adk_recordings_config"
)

// RunRequest describes one agent run.
type RunRequest struct {
	// AppName is the app to run.
	AppName string
	// UserID owns the session.
	UserID string
	// SessionID is the session to run in.
	SessionID string
	// NewMessage is the user message that starts the run.
	NewMessage *genai.Content
	// Streaming asks the server to stream partial model output.
	Streaming bool
	// StateDelta is applied to the session state before the run.
	StateDelta map[string]any
}

// ConformanceMode selects what the server does with the model traffic of a run.
type ConformanceMode string

const (
	// ModeRecord writes the model traffic of the run to the test case directory.
	ModeRecord ConformanceMode = "record"
	// ModeReplay serves the run from the recordings in the test case directory.
	ModeReplay ConformanceMode = "replay"
)

// RunOption modifies a run.
type RunOption func(*runOptions)

type runOptions struct {
	conformance      bool
	mode             ConformanceMode
	testCaseDir      string
	userMessageIndex int
}

// WithConformance makes the server record or replay this run. testCaseDir is
// the test case directory the server reads or writes, and userMessageIndex is
// the index of this run's message within the test case.
func WithConformance(mode ConformanceMode, testCaseDir string, userMessageIndex int) RunOption {
	return func(o *runOptions) {
		o.conformance = true
		o.mode = mode
		o.testCaseDir = testCaseDir
		o.userMessageIndex = userMessageIndex
	}
}

// stateDelta returns the state delta to send, leaving base untouched. Without
// [WithConformance] that is base itself.
func (o runOptions) stateDelta(base map[string]any, streaming bool) (map[string]any, error) {
	if !o.conformance {
		return base, nil
	}
	var key string
	switch o.mode {
	case ModeRecord:
		key = recordingsConfigKey
	case ModeReplay:
		key = replayConfigKey
	default:
		return nil, fmt.Errorf("adkrestclient: unsupported conformance mode %q", o.mode)
	}
	if o.testCaseDir == "" {
		return nil, errors.New("adkrestclient: conformance test case directory must not be empty")
	}
	if o.userMessageIndex < 0 {
		return nil, fmt.Errorf("adkrestclient: conformance user message index must not be negative, got %d", o.userMessageIndex)
	}

	streamingMode := "none"
	if streaming {
		streamingMode = "sse"
	}
	delta := maps.Clone(base)
	if delta == nil {
		delta = map[string]any{}
	}
	delta[key] = map[string]any{
		"dir":                o.testCaseDir,
		"user_message_index": o.userMessageIndex,
		"streaming_mode":     streamingMode,
	}
	return delta, nil
}

// RunAgent runs an agent and streams the events the server sends back. It
// leaves req and its StateDelta unmodified.
//
// Iteration stops after the first error, which the server may have streamed as
// an error payload. Breaking out of the loop closes the response body.
func (c *Client) RunAgent(ctx context.Context, req RunRequest, opts ...RunOption) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		var options runOptions
		for _, opt := range opts {
			opt(&options)
		}
		delta, err := options.stateDelta(req.StateDelta, req.Streaming)
		if err != nil {
			yield(nil, err)
			return
		}

		body := models.RunAgentRequest{
			AppName:   req.AppName,
			UserId:    req.UserID,
			SessionId: req.SessionID,
			Streaming: req.Streaming,
		}
		if req.NewMessage != nil {
			body.NewMessage = *req.NewMessage
		}
		if delta != nil {
			body.StateDelta = &delta
		}

		// The stream is bounded by ctx, not by the client timeout: an agent run
		// can legitimately outlive a single request.
		resp, err := c.roundTrip(ctx, http.MethodPost, runSSEPath, body)
		if err != nil {
			yield(nil, err)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		reader := bufio.NewReader(resp.Body)
		for {
			line, readErr := reader.ReadString('\n')
			if data, ok := sseData(line); ok {
				event, err := decodeSSEEvent(data)
				if err != nil {
					yield(nil, err)
					return
				}
				if !yield(event, nil) {
					return
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					yield(nil, fmt.Errorf("adkrestclient: POST %s: read stream: %w", runSSEPath, readErr))
				}
				return
			}
		}
	}
}

// sseData returns the payload of an SSE "data:" line. Any other line is not a
// payload: the blank frame separator, and the "event: error" line the server
// writes ahead of a failure payload.
func sseData(line string) (string, bool) {
	payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data:")
	if !ok {
		return "", false
	}
	payload = strings.TrimSpace(payload)
	return payload, payload != ""
}

// decodeSSEEvent decodes one SSE payload. A payload carrying an "error" key is
// the server's failure sentinel; models.Event has no such field, so an event
// and a failure cannot be confused.
func decodeSSEEvent(data string) (*session.Event, error) {
	var failure struct {
		Error *string `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &failure); err == nil && failure.Error != nil {
		return nil, fmt.Errorf("adkrestclient: server streamed an error: %s", *failure.Error)
	}
	var event models.Event
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, fmt.Errorf("adkrestclient: POST %s: decode event: %w", runSSEPath, err)
	}
	return models.ToSessionEvent(event), nil
}
