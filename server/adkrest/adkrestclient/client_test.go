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
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestNew_Defaults(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.baseURL != "http://127.0.0.1:8000" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://127.0.0.1:8000")
	}
	if c.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want %v", c.timeout, 30*time.Second)
	}
	if c.httpClient == nil {
		t.Error("httpClient = nil, want a default client")
	}
}

func TestNew_CustomValues(t *testing.T) {
	custom := &http.Client{}
	c, err := New(Config{BaseURL: "http://test.com:9000", Timeout: 5 * time.Second, HTTPClient: custom})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.baseURL != "http://test.com:9000" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://test.com:9000")
	}
	if c.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want %v", c.timeout, 5*time.Second)
	}
	if c.httpClient != custom {
		t.Error("httpClient is not the configured client")
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c, err := New(Config{BaseURL: "http://test.com/"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.baseURL != "http://test.com" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://test.com")
	}
}

func TestNew_NonPositiveTimeoutFallsBackToDefault(t *testing.T) {
	c, err := New(Config{Timeout: -1 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want %v", c.timeout, 30*time.Second)
	}
}

func TestNew_RejectsBadBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{name: "unparseable", baseURL: "http://[::1", wantErr: "invalid BaseURL"},
		{name: "no scheme", baseURL: "test.com", wantErr: "must use the http or https scheme"},
		{name: "wrong scheme", baseURL: "ftp://test.com", wantErr: "must use the http or https scheme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(Config{BaseURL: tt.baseURL})
			if err == nil {
				t.Fatalf("New(%q) error = nil, want an error", tt.baseURL)
			}
			if c != nil {
				t.Errorf("New(%q) client = %+v, want nil", tt.baseURL, c)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("New(%q) error = %q, want it to contain %q", tt.baseURL, err, tt.wantErr)
			}
		})
	}
}

func TestGetSession(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		writeJSON(t, w, map[string]any{
			"id":             "test_session",
			"appName":        "test_app",
			"userId":         "test_user",
			"lastUpdateTime": 1234,
			"state":          map[string]any{"key": "value"},
			"events":         []any{map[string]any{"id": "e1", "author": "user"}},
		})
	}))
	defer srv.Close()

	got, err := newTestClient(t, srv.URL).GetSession(context.Background(), "test_app", "test_user", "test_session")
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodGet)
	}
	if want := "/apps/test_app/users/test_user/sessions/test_session"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if got.ID != "test_session" || got.AppName != "test_app" || got.UserID != "test_user" {
		t.Errorf("session = %+v, want id/app/user test_session/test_app/test_user", got)
	}
	if diff := cmp.Diff(map[string]any{"key": "value"}, got.State); diff != "" {
		t.Errorf("state mismatch (-want +got):\n%s", diff)
	}
	if got.LastUpdateTime != 1234 {
		t.Errorf("LastUpdateTime = %d, want 1234", got.LastUpdateTime)
	}
	if len(got.Events) != 1 || got.Events[0].ID != "e1" || got.Events[0].Author != "user" {
		t.Errorf("events = %+v, want one event e1 authored by user", got.Events)
	}
}

func TestCreateSession(t *testing.T) {
	tests := []struct {
		name     string
		state    map[string]any
		wantBody string
	}{
		{name: "with state", state: map[string]any{"key": "value"}, wantBody: `{"state":{"key":"value"}}`},
		{name: "nil state", state: nil, wantBody: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath, gotBody, gotContentType string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotContentType = r.Header.Get("Content-Type")
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				gotBody = string(raw)
				writeJSON(t, w, map[string]any{"id": "new_session", "appName": "test_app", "userId": "test_user"})
			}))
			defer srv.Close()

			got, err := newTestClient(t, srv.URL).CreateSession(context.Background(), "test_app", "test_user", tt.state)
			if err != nil {
				t.Fatalf("CreateSession() error = %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
			}
			if want := "/apps/test_app/users/test_user/sessions"; gotPath != want {
				t.Errorf("path = %q, want %q", gotPath, want)
			}
			if gotBody != tt.wantBody {
				t.Errorf("body = %q, want %q", gotBody, tt.wantBody)
			}
			if gotContentType != "application/json" {
				t.Errorf("Content-Type = %q, want %q", gotContentType, "application/json")
			}
			if got.ID != "new_session" {
				t.Errorf("session ID = %q, want %q", got.ID, "new_session")
			}
		})
	}
}

func TestDeleteSession(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
	}))
	defer srv.Close()

	if err := newTestClient(t, srv.URL).DeleteSession(context.Background(), "test_app", "test_user", "test_session"); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodDelete)
	}
	if want := "/apps/test_app/users/test_user/sessions/test_session"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotContentType != "" {
		t.Errorf("Content-Type = %q, want it unset on a request with no body", gotContentType)
	}
}

func TestSessionPathsEscapeSegments(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
	}))
	defer srv.Close()

	if err := newTestClient(t, srv.URL).DeleteSession(context.Background(), "a/b", "u 1", "s?x"); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if want := "/apps/a%2Fb/users/u%201/sessions/s%3Fx"; gotPath != want {
		t.Errorf("escaped path = %q, want %q", gotPath, want)
	}
}

func TestHTTPErrorStatuses(t *testing.T) {
	calls := map[string]func(*Client) error{
		"CreateSession": func(c *Client) error {
			_, err := c.CreateSession(context.Background(), "a", "u", nil)
			return err
		},
		"GetSession": func(c *Client) error {
			_, err := c.GetSession(context.Background(), "a", "u", "s")
			return err
		},
		"DeleteSession": func(c *Client) error {
			return c.DeleteSession(context.Background(), "a", "u", "s")
		},
	}
	for name, call := range calls {
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError} {
			t.Run(name+"/"+http.StatusText(status), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "server said no", status)
				}))
				defer srv.Close()

				err := call(newTestClient(t, srv.URL))
				if err == nil {
					t.Fatalf("%s with status %d returned no error", name, status)
				}
				if !strings.Contains(err.Error(), "status "+strconv.Itoa(status)) {
					t.Errorf("error = %q, want it to mention status %d", err, status)
				}
				if !strings.Contains(err.Error(), "server said no") {
					t.Errorf("error = %q, want it to quote the response body", err)
				}
			})
		}
	}
}

func TestGetSession_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "not json"); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).GetSession(context.Background(), "a", "u", "s")
	if err == nil {
		t.Fatal("GetSession() error = nil, want a decode error")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("error = %q, want it to mention decoding", err)
	}
}

func TestSend_TransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // Nothing listens on the address any more.

	err := newTestClient(t, srv.URL).DeleteSession(context.Background(), "a", "u", "s")
	if err == nil {
		t.Fatal("DeleteSession() error = nil, want a transport error")
	}
	if !strings.Contains(err.Error(), "DELETE /apps/a/users/u/sessions/s") {
		t.Errorf("error = %q, want it to name the method and path", err)
	}
}

func TestSend_BuildRequestError(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	err := c.send(context.Background(), "bad method", "/x", nil, nil)
	if err == nil {
		t.Fatal("send() error = nil, want a request build error")
	}
	if !strings.Contains(err.Error(), "build request") {
		t.Errorf("error = %q, want it to mention building the request", err)
	}
}

func TestSend_EncodeRequestError(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	err := c.send(context.Background(), http.MethodPost, "/x", map[string]any{"bad": make(chan int)}, nil)
	if err == nil {
		t.Fatal("send() error = nil, want an encode error")
	}
	if !strings.Contains(err.Error(), "encode request") {
		t.Errorf("error = %q, want it to mention encoding the request", err)
	}
}

func TestStatusError_UnreadableBody(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(errReader{})}
	err := statusError(http.MethodGet, "/x", resp)
	if err == nil {
		t.Fatal("statusError() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "unreadable body") {
		t.Errorf("error = %q, want it to report an unreadable body", err)
	}
	if !errors.Is(err, errRead) {
		t.Errorf("error = %v, want it to wrap %v", err, errRead)
	}
}

var errRead = errors.New("read failed")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errRead }

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
