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

package controllers_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/mux"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/server/adkrest/controllers"
	"google.golang.org/adk/v2/server/adkrest/internal/fakes"
)

// createdAt is 123.4 unix seconds, the create time used by the adk-python
// reference test for this endpoint.
var createdAt = time.Unix(123, 400000000).UTC()

func artifactVars(vars map[string]string) map[string]string {
	base := map[string]string{
		"app_name":      "testApp",
		"user_id":       "testUser",
		"session_id":    "testSession",
		"artifact_name": "report",
	}
	for k, v := range vars {
		base[k] = v
	}
	return base
}

// TestArtifactHandlerParameterValidation pins the 400 responses the artifact
// handlers share. Extracting artifactTargetFromVars must not change which
// parameter a handler rejects, nor the message it rejects it with.
//
// ListArtifactsHandler addresses a session rather than one artifact, so it does
// not take part and keeps its own two checks.
func TestArtifactHandlerParameterValidation(t *testing.T) {
	handlers := map[string]func(*controllers.ArtifactsAPIController) http.HandlerFunc{
		"LoadArtifact": func(c *controllers.ArtifactsAPIController) http.HandlerFunc {
			return c.LoadArtifactHandler
		},
		"LoadArtifactVersion": func(c *controllers.ArtifactsAPIController) http.HandlerFunc {
			return c.LoadArtifactVersionHandler
		},
		"DeleteArtifact": func(c *controllers.ArtifactsAPIController) http.HandlerFunc {
			return c.DeleteArtifactHandler
		},
		"GetArtifactVersionMetadata": func(c *controllers.ArtifactsAPIController) http.HandlerFunc {
			return c.GetArtifactVersionMetadataHandler
		},
		"ListArtifactVersionsMetadata": func(c *controllers.ArtifactsAPIController) http.HandlerFunc {
			return c.ListArtifactVersionsMetadataHandler
		},
	}

	cases := []struct {
		name    string
		absent  string
		wantErr string
	}{
		{name: "app name is missing", absent: "app_name", wantErr: "app_name parameter is required"},
		{name: "user ID is missing", absent: "user_id", wantErr: "user_id parameter is required"},
		{name: "session ID is missing", absent: "session_id", wantErr: "session_id parameter is required"},
		{name: "artifact name is missing", absent: "artifact_name", wantErr: "artifact_name parameter is required"},
	}

	for name, handlerOf := range handlers {
		for _, tt := range cases {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				vars := artifactVars(map[string]string{"version": "1", "version_id": "1"})
				vars[tt.absent] = ""
				apiController := controllers.NewArtifactsAPIController(&fakes.FakeArtifactService{})
				req := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/artifacts", nil), vars)
				rr := httptest.NewRecorder()

				handlerOf(apiController)(rr, req)

				if got, want := rr.Code, http.StatusBadRequest; got != want {
					t.Fatalf("%s status = %d, want %d; body: %s", name, got, want, rr.Body)
				}
				if got := strings.Trim(rr.Body.String(), "\n"); got != tt.wantErr {
					t.Errorf("%s body = %q, want %q", name, got, tt.wantErr)
				}
			})
		}
	}
}

func TestGetArtifactVersionMetadata(t *testing.T) {
	full := &artifact.ArtifactVersion{
		Version:        2,
		CanonicalURI:   "memory://testApp/testUser/testSession/report/2",
		CustomMetadata: map[string]any{"foo": "bar"},
		CreateTime:     createdAt,
		MimeType:       "text/plain",
	}

	tests := []struct {
		name          string
		vars          map[string]string
		service       *fakes.FakeArtifactService
		wantStatus    int
		wantBody      map[string]any
		wantErr       string
		wantRequested []int64
	}{
		{
			name: "explicit version",
			vars: artifactVars(map[string]string{"version_id": "2"}),
			service: &fakes.FakeArtifactService{
				Metadata: map[int64]*artifact.ArtifactVersion{2: full},
			},
			wantStatus: http.StatusOK,
			wantBody: map[string]any{
				"version":        float64(2),
				"canonicalUri":   "memory://testApp/testUser/testSession/report/2",
				"customMetadata": map[string]any{"foo": "bar"},
				"createTime":     123.4,
				"mimeType":       "text/plain",
			},
			wantRequested: []int64{2},
		},
		{
			name: "latest asks the service for version zero",
			vars: artifactVars(map[string]string{"version_id": "latest"}),
			service: &fakes.FakeArtifactService{
				Metadata: map[int64]*artifact.ArtifactVersion{0: {Version: 7, CanonicalURI: "memory://latest"}},
			},
			wantStatus: http.StatusOK,
			wantBody: map[string]any{
				"version":        float64(7),
				"canonicalUri":   "memory://latest",
				"customMetadata": map[string]any{},
				"createTime":     float64(0),
			},
			wantRequested: []int64{0},
		},
		{
			name: "absent custom metadata becomes an empty object",
			vars: artifactVars(map[string]string{"version_id": "1"}),
			service: &fakes.FakeArtifactService{
				Metadata: map[int64]*artifact.ArtifactVersion{1: {
					Version:      1,
					CanonicalURI: "memory://one",
					CreateTime:   createdAt,
					MimeType:     "text/plain",
				}},
			},
			wantStatus: http.StatusOK,
			wantBody: map[string]any{
				"version":        float64(1),
				"canonicalUri":   "memory://one",
				"customMetadata": map[string]any{},
				"createTime":     123.4,
				"mimeType":       "text/plain",
			},
			wantRequested: []int64{1},
		},
		{
			name: "unknown version",
			vars: artifactVars(map[string]string{"version_id": "9"}),
			service: &fakes.FakeArtifactService{
				MetadataErr: map[int64]error{9: fmt.Errorf("artifact not found: %w", fs.ErrNotExist)},
			},
			wantStatus:    http.StatusNotFound,
			wantErr:       "artifact version not found",
			wantRequested: []int64{9},
		},
		{
			name:          "service returns no metadata",
			vars:          artifactVars(map[string]string{"version_id": "1"}),
			service:       &fakes.FakeArtifactService{},
			wantStatus:    http.StatusNotFound,
			wantErr:       "artifact version not found",
			wantRequested: []int64{1},
		},
		{
			name: "service fails",
			vars: artifactVars(map[string]string{"version_id": "1"}),
			service: &fakes.FakeArtifactService{
				MetadataErr: map[int64]error{1: fmt.Errorf("storage unavailable")},
			},
			wantStatus:    http.StatusInternalServerError,
			wantErr:       "storage unavailable",
			wantRequested: []int64{1},
		},
		{
			name:       "version is not a number",
			vars:       artifactVars(map[string]string{"version_id": "abc"}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "version parameter must be an integer",
		},
		{
			name:       "version is negative",
			vars:       artifactVars(map[string]string{"version_id": "-1"}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "version parameter must be an integer",
		},
		{
			name:       "user ID is missing",
			vars:       artifactVars(map[string]string{"version_id": "1", "user_id": ""}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "user_id parameter is required",
		},
		{
			name:       "session ID is missing",
			vars:       artifactVars(map[string]string{"version_id": "1", "session_id": ""}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "session_id parameter is required",
		},
		{
			name:       "artifact name is missing",
			vars:       artifactVars(map[string]string{"version_id": "1", "artifact_name": ""}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "artifact_name parameter is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiController := controllers.NewArtifactsAPIController(tt.service)
			req := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/metadata", nil), tt.vars)
			rr := httptest.NewRecorder()

			apiController.GetArtifactVersionMetadataHandler(rr, req)

			if got := rr.Code; got != tt.wantStatus {
				t.Fatalf("GetArtifactVersionMetadata() status = %d, want %d; body: %s", got, tt.wantStatus, rr.Body)
			}
			if diff := cmp.Diff(tt.wantRequested, tt.service.RequestedVersions); diff != "" {
				t.Errorf("versions requested from the service mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErr != "" {
				if got := strings.Trim(rr.Body.String(), "\n"); got != tt.wantErr {
					t.Errorf("GetArtifactVersionMetadata() body = %q, want %q", got, tt.wantErr)
				}
				return
			}
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("json.Unmarshal(response body) failed: %v; body: %s", err, rr.Body)
			}
			if diff := cmp.Diff(tt.wantBody, got); diff != "" {
				t.Errorf("GetArtifactVersionMetadata() body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestListArtifactVersionsMetadata(t *testing.T) {
	metadata := map[int64]*artifact.ArtifactVersion{
		1: {Version: 1, CanonicalURI: "memory://one", CreateTime: createdAt},
		2: {Version: 2, CanonicalURI: "memory://two", CreateTime: createdAt, MimeType: "text/plain"},
		3: {Version: 3, CanonicalURI: "memory://three", CreateTime: createdAt},
	}
	versionOne := map[string]any{
		"version":        float64(1),
		"canonicalUri":   "memory://one",
		"customMetadata": map[string]any{},
		"createTime":     123.4,
	}
	versionTwo := map[string]any{
		"version":        float64(2),
		"canonicalUri":   "memory://two",
		"customMetadata": map[string]any{},
		"createTime":     123.4,
		"mimeType":       "text/plain",
	}
	versionThree := map[string]any{
		"version":        float64(3),
		"canonicalUri":   "memory://three",
		"customMetadata": map[string]any{},
		"createTime":     123.4,
	}

	tests := []struct {
		name       string
		vars       map[string]string
		service    *fakes.FakeArtifactService
		wantStatus int
		wantBody   []map[string]any
		wantErr    string
	}{
		{
			name: "versions are sorted ascending",
			vars: artifactVars(nil),
			service: &fakes.FakeArtifactService{
				VersionList: []int64{3, 1, 2},
				Metadata:    metadata,
			},
			wantStatus: http.StatusOK,
			wantBody:   []map[string]any{versionOne, versionTwo, versionThree},
		},
		{
			name: "unknown artifact is an empty list",
			vars: artifactVars(nil),
			service: &fakes.FakeArtifactService{
				VersionsErr: fmt.Errorf("artifact not found: %w", fs.ErrNotExist),
			},
			wantStatus: http.StatusOK,
			wantBody:   []map[string]any{},
		},
		{
			name: "listing versions fails",
			vars: artifactVars(nil),
			service: &fakes.FakeArtifactService{
				VersionsErr: fmt.Errorf("storage unavailable"),
			},
			wantStatus: http.StatusInternalServerError,
			wantErr:    "storage unavailable",
		},
		{
			name: "a version that disappeared is skipped",
			vars: artifactVars(nil),
			service: &fakes.FakeArtifactService{
				VersionList: []int64{1, 2},
				Metadata:    metadata,
				MetadataErr: map[int64]error{1: fmt.Errorf("artifact not found: %w", fs.ErrNotExist)},
			},
			wantStatus: http.StatusOK,
			wantBody:   []map[string]any{versionTwo},
		},
		{
			name: "every version vanished",
			vars: artifactVars(nil),
			service: &fakes.FakeArtifactService{
				VersionList: []int64{1},
				Metadata:    metadata,
				MetadataErr: map[int64]error{1: fmt.Errorf("artifact not found: %w", fs.ErrNotExist)},
			},
			wantStatus: http.StatusOK,
			wantBody:   []map[string]any{},
		},
		{
			name: "a version with no metadata is skipped",
			vars: artifactVars(nil),
			service: &fakes.FakeArtifactService{
				VersionList: []int64{1, 4},
				Metadata:    metadata,
			},
			wantStatus: http.StatusOK,
			wantBody:   []map[string]any{versionOne},
		},
		{
			name: "reading one version fails",
			vars: artifactVars(nil),
			service: &fakes.FakeArtifactService{
				VersionList: []int64{1, 2},
				Metadata:    metadata,
				MetadataErr: map[int64]error{2: fmt.Errorf("storage unavailable")},
			},
			wantStatus: http.StatusInternalServerError,
			wantErr:    "storage unavailable",
		},
		{
			name:       "user ID is missing",
			vars:       artifactVars(map[string]string{"user_id": ""}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "user_id parameter is required",
		},
		{
			name:       "session ID is missing",
			vars:       artifactVars(map[string]string{"session_id": ""}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "session_id parameter is required",
		},
		{
			name:       "artifact name is missing",
			vars:       artifactVars(map[string]string{"artifact_name": ""}),
			service:    &fakes.FakeArtifactService{},
			wantStatus: http.StatusBadRequest,
			wantErr:    "artifact_name parameter is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiController := controllers.NewArtifactsAPIController(tt.service)
			req := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/versions/metadata", nil), tt.vars)
			rr := httptest.NewRecorder()

			apiController.ListArtifactVersionsMetadataHandler(rr, req)

			if got := rr.Code; got != tt.wantStatus {
				t.Fatalf("ListArtifactVersionsMetadata() status = %d, want %d; body: %s", got, tt.wantStatus, rr.Body)
			}
			if tt.wantErr != "" {
				if got := strings.Trim(rr.Body.String(), "\n"); got != tt.wantErr {
					t.Errorf("ListArtifactVersionsMetadata() body = %q, want %q", got, tt.wantErr)
				}
				return
			}
			var got []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("json.Unmarshal(response body) failed: %v; body: %s", err, rr.Body)
			}
			if diff := cmp.Diff(tt.wantBody, got); diff != "" {
				t.Errorf("ListArtifactVersionsMetadata() body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
