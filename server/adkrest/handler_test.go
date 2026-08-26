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

package adkrest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/adk/v2/internal/version"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
)

func TestServerHealth(t *testing.T) {
	server, err := NewServer(ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	server.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Errorf("GET /health status = %d, want %d", got, want)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("GET /health Content-Type = %q, want %q", got, want)
	}
	if got, want := recorder.Body.String(), "{\"status\":\"ok\"}\n"; got != want {
		t.Errorf("GET /health body = %q, want %q", got, want)
	}
}

func TestServerVersion(t *testing.T) {
	server, err := NewServer(ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/version", nil)
	server.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Errorf("GET /version status = %d, want %d", got, want)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Errorf("GET /version Content-Type = %q, want %q", got, want)
	}
	// Decoding into the wire type pins the exact JSON keys, language_version included.
	var got models.VersionInfo
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode /version body: %v", err)
	}
	if want := version.Version; got.Version != want {
		t.Errorf("GET /version version = %q, want %q", got.Version, want)
	}
	if want := "go"; got.Language != want {
		t.Errorf("GET /version language = %q, want %q", got.Language, want)
	}
	if got.LanguageVersion == "" {
		t.Error("GET /version language_version is empty, want a version string")
	}
	if strings.HasPrefix(got.LanguageVersion, "go") {
		t.Errorf("GET /version language_version = %q, want no %q prefix", got.LanguageVersion, "go")
	}
}
