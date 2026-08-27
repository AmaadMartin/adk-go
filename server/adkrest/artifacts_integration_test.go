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

package adkrest_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/server/adkrest"
)

const artifactPath = "/apps/testApp/users/testUser/sessions/testSession/artifacts/report"

// newArtifactServer returns a server holding one artifact with two versions.
func newArtifactServer(t *testing.T) *adkrest.Server {
	t.Helper()
	artifactService := artifact.InMemoryService()
	for _, text := range []string{"first", "second"} {
		if _, err := artifactService.Save(t.Context(), &artifact.SaveRequest{
			AppName:   "testApp",
			UserID:    "testUser",
			SessionID: "testSession",
			FileName:  "report",
			Part:      genai.NewPartFromText(text),
		}); err != nil {
			t.Fatalf("artifact service Save() failed: %v", err)
		}
	}
	server, err := adkrest.NewServer(adkrest.ServerConfig{ArtifactService: artifactService})
	if err != nil {
		t.Fatalf("NewServer() failed: %v", err)
	}
	return server
}

func getArtifactPath(t *testing.T, server *adkrest.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

// TestListArtifactVersionsMetadataRoute guards the route order. The route that
// loads one version also matches the literal "metadata" segment, so a wrong
// order turns this request into 400 "version parameter must be an integer".
func TestListArtifactVersionsMetadataRoute(t *testing.T) {
	server := newArtifactServer(t)

	recorder := getArtifactPath(t, server, artifactPath+"/versions/metadata")

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("GET versions/metadata status = %d, want %d; body: %s", got, want, recorder.Body)
	}
	var got []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal(response body) failed: %v; body: %s", err, recorder.Body)
	}
	want := []map[string]any{
		{"version": float64(1), "canonicalUri": "", "customMetadata": map[string]any{}, "createTime": float64(0), "mimeType": "text/plain"},
		{"version": float64(2), "canonicalUri": "", "customMetadata": map[string]any{}, "createTime": float64(0), "mimeType": "text/plain"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("GET versions/metadata body mismatch (-want +got):\n%s", diff)
	}
}

func TestGetArtifactVersionMetadataRoute(t *testing.T) {
	server := newArtifactServer(t)

	tests := []struct {
		name        string
		path        string
		wantVersion float64
	}{
		{name: "explicit version", path: artifactPath + "/versions/1/metadata", wantVersion: 1},
		{name: "latest version", path: artifactPath + "/versions/latest/metadata", wantVersion: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := getArtifactPath(t, server, tt.path)

			if got, want := recorder.Code, http.StatusOK; got != want {
				t.Fatalf("GET %s status = %d, want %d; body: %s", tt.path, got, want, recorder.Body)
			}
			var got map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatalf("json.Unmarshal(response body) failed: %v; body: %s", err, recorder.Body)
			}
			if got["version"] != tt.wantVersion {
				t.Errorf("GET %s version = %v, want %v", tt.path, got["version"], tt.wantVersion)
			}
		})
	}
}

// TestLoadArtifactVersionRouteStillMatches proves the two new routes do not
// shadow the route that loads a version's payload.
func TestLoadArtifactVersionRouteStillMatches(t *testing.T) {
	server := newArtifactServer(t)

	recorder := getArtifactPath(t, server, artifactPath+"/versions/1")

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("GET versions/1 status = %d, want %d; body: %s", got, want, recorder.Body)
	}
	var got genai.Part
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal(response body) failed: %v; body: %s", err, recorder.Body)
	}
	if want := "first"; got.Text != want {
		t.Errorf("GET versions/1 text = %q, want %q", got.Text, want)
	}
}
