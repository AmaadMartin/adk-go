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

// Package adkrestclient is an HTTP client for an ADK web server, the server
// implemented by [google.golang.org/adk/v2/server/adkrest].
//
// It creates, reads and deletes sessions, and streams an agent run over
// Server-Sent Events. [WithConformance] additionally switches the server into
// conformance record or replay mode.
package adkrestclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/session"
)

const (
	// defaultBaseURL is the address `adk web` listens on.
	defaultBaseURL = "http://127.0.0.1:8000"
	// defaultTimeout bounds a single session request.
	defaultTimeout = 30 * time.Second
	// maxErrorBodyBytes caps how much of a failed response is quoted back.
	maxErrorBodyBytes = 512
)

// Config configures a [Client].
type Config struct {
	// BaseURL of the ADK web server. Defaults to http://127.0.0.1:8000.
	// A trailing slash is trimmed.
	BaseURL string
	// Timeout bounds each session request. Defaults to 30s. It does not bound
	// [Client.RunAgent], whose stream lives as long as its context.
	Timeout time.Duration
	// HTTPClient overrides the underlying client. Optional.
	HTTPClient *http.Client
}

// Client talks to an ADK web server over HTTP. It is safe for concurrent use.
type Client struct {
	baseURL    string
	timeout    time.Duration
	httpClient *http.Client
}

// New returns a Client for the server named by cfg.BaseURL.
func New(cfg Config) (*Client, error) {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("adkrestclient: invalid BaseURL %q: %w", cfg.BaseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("adkrestclient: BaseURL %q must use the http or https scheme", cfg.BaseURL)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: baseURL, timeout: timeout, httpClient: httpClient}, nil
}

// Session is the client-side view of a session returned by the server.
type Session struct {
	// ID identifies the session within its app and user.
	ID string
	// AppName is the app the session belongs to.
	AppName string
	// UserID is the user the session belongs to.
	UserID string
	// State is the session state the server holds.
	State map[string]any
	// Events is the session history, oldest first.
	Events []*session.Event
	// LastUpdateTime is the Unix time in seconds of the last update.
	LastUpdateTime int64
}

// CreateSession creates a session for userID in appName, with state as its
// initial state. A nil state creates a session with no initial state.
func (c *Client) CreateSession(ctx context.Context, appName, userID string, state map[string]any) (*Session, error) {
	body := map[string]any{}
	if state != nil {
		body["state"] = state
	}
	var decoded models.Session
	if err := c.send(ctx, http.MethodPost, sessionsPath(appName, userID), body, &decoded); err != nil {
		return nil, err
	}
	return toSession(decoded), nil
}

// GetSession returns the session sessionID of userID in appName.
func (c *Client) GetSession(ctx context.Context, appName, userID, sessionID string) (*Session, error) {
	var decoded models.Session
	if err := c.send(ctx, http.MethodGet, sessionPath(appName, userID, sessionID), nil, &decoded); err != nil {
		return nil, err
	}
	return toSession(decoded), nil
}

// DeleteSession deletes the session sessionID of userID in appName.
func (c *Client) DeleteSession(ctx context.Context, appName, userID, sessionID string) error {
	return c.send(ctx, http.MethodDelete, sessionPath(appName, userID, sessionID), nil, nil)
}

func sessionsPath(appName, userID string) string {
	return fmt.Sprintf("/apps/%s/users/%s/sessions", url.PathEscape(appName), url.PathEscape(userID))
}

func sessionPath(appName, userID, sessionID string) string {
	return sessionsPath(appName, userID) + "/" + url.PathEscape(sessionID)
}

func toSession(s models.Session) *Session {
	events := make([]*session.Event, 0, len(s.Events))
	for _, event := range s.Events {
		events = append(events, models.ToSessionEvent(event))
	}
	return &Session{
		ID:             s.ID,
		AppName:        s.AppName,
		UserID:         s.UserID,
		State:          s.State,
		Events:         events,
		LastUpdateTime: s.UpdatedAt,
	}
}

// send performs a request bounded by the client timeout and decodes the JSON
// response into out. A nil out discards the response body.
func (c *Client) send(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.roundTrip(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("adkrestclient: %s %s: decode response: %w", method, path, err)
	}
	return nil
}

// roundTrip performs a request and returns a response whose body the caller
// must close. A non-2xx status is reported as an error and the body is closed.
func (c *Client) roundTrip(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("adkrestclient: %s %s: encode request: %w", method, path, err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return nil, fmt.Errorf("adkrestclient: %s %s: build request: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("adkrestclient: %s %s: %w", method, path, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = resp.Body.Close() }()
		return nil, statusError(method, path, resp)
	}
	return resp, nil
}

// statusError describes a non-2xx response, quoting a truncated body.
func statusError(method, path string, resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	if err != nil {
		return fmt.Errorf("adkrestclient: %s %s: status %d, unreadable body: %w", method, path, resp.StatusCode, err)
	}
	return fmt.Errorf("adkrestclient: %s %s: status %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(body))
}
