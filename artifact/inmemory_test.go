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

package artifact_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/internal/artifact/tests"
	"google.golang.org/adk/v2/platform"
)

func TestInMemoryArtifactService(t *testing.T) {
	factory := func(t *testing.T) (artifact.Service, error) {
		return artifact.InMemoryService(), nil
	}
	tests.TestArtifactService(t, "InMemory", factory)
}

// saveVersions saves n versions of fileName and fails the test on any error.
func saveVersions(ctx context.Context, t *testing.T, srv artifact.Service, sessionID, fileName string, n int) {
	t.Helper()
	for i := range n {
		if _, err := srv.Save(ctx, &artifact.SaveRequest{
			AppName: "testapp", UserID: "testuser", SessionID: sessionID, FileName: fileName,
			Part: genai.NewPartFromBytes([]byte{byte('0' + i)}, "text/plain"),
		}); err != nil {
			t.Fatalf("Save() version %d failed: %v", i+1, err)
		}
	}
}

func TestInMemoryArtifactServiceVersionMetadata(t *testing.T) {
	fixed := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	ctx := platform.WithTimeProvider(t.Context(), func() time.Time { return fixed })
	srv := artifact.InMemoryService()
	saveVersions(ctx, t, srv, "testsession", "verfile", 3)

	const uriPrefix = "memory://apps/testapp/users/testuser/sessions/testsession/artifacts/verfile/versions/"
	for _, tc := range []struct {
		name    string
		version int64
		want    *artifact.ArtifactVersion
	}{
		{
			name: "latest",
			want: &artifact.ArtifactVersion{
				Version: 3, CanonicalURI: uriPrefix + "3",
				CustomMetadata: map[string]any{}, CreateTime: fixed, MimeType: "text/plain",
			},
		},
		{
			name: "first", version: 1,
			want: &artifact.ArtifactVersion{
				Version: 1, CanonicalURI: uriPrefix + "1",
				CustomMetadata: map[string]any{}, CreateTime: fixed, MimeType: "text/plain",
			},
		},
		{
			name: "middle", version: 2,
			want: &artifact.ArtifactVersion{
				Version: 2, CanonicalURI: uriPrefix + "2",
				CustomMetadata: map[string]any{}, CreateTime: fixed, MimeType: "text/plain",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.GetArtifactVersion(ctx, &artifact.GetArtifactVersionRequest{
				AppName: "testapp", UserID: "testuser", SessionID: "testsession", FileName: "verfile",
				Version: tc.version,
			})
			if err != nil {
				t.Fatalf("GetArtifactVersion(%d) failed: %v", tc.version, err)
			}
			if diff := cmp.Diff(tc.want, resp.ArtifactVersion); diff != "" {
				t.Errorf("GetArtifactVersion(%d) mismatch (-want +got):\n%s", tc.version, diff)
			}
		})
	}
}

// TestInMemoryArtifactServiceUserScopedVersionMetadata reads with a session
// other than the one used to save, because a user scoped artifact is not bound
// to a session and its URI must not name one.
func TestInMemoryArtifactServiceUserScopedVersionMetadata(t *testing.T) {
	fixed := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	ctx := platform.WithTimeProvider(t.Context(), func() time.Time { return fixed })
	srv := artifact.InMemoryService()
	saveVersions(ctx, t, srv, "session-at-save", "user:document.pdf", 1)

	resp, err := srv.GetArtifactVersion(ctx, &artifact.GetArtifactVersionRequest{
		AppName: "testapp", UserID: "testuser", SessionID: "a-different-session",
		FileName: "user:document.pdf",
	})
	if err != nil {
		t.Fatalf("GetArtifactVersion() failed: %v", err)
	}
	want := &artifact.ArtifactVersion{
		Version:        1,
		CanonicalURI:   "memory://apps/testapp/users/testuser/artifacts/user:document.pdf/versions/1",
		CustomMetadata: map[string]any{},
		CreateTime:     fixed,
		MimeType:       "text/plain",
	}
	if diff := cmp.Diff(want, resp.ArtifactVersion); diff != "" {
		t.Errorf("GetArtifactVersion() mismatch (-want +got):\n%s", diff)
	}
}

// TestInMemoryArtifactServiceCreateTimePerSave proves the service records the
// time of each save, rather than one time for the whole service.
func TestInMemoryArtifactServiceCreateTimePerSave(t *testing.T) {
	base := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	var calls int
	ctx := platform.WithTimeProvider(t.Context(), func() time.Time {
		calls++
		return base.Add(time.Duration(calls) * time.Hour)
	})
	srv := artifact.InMemoryService()
	saveVersions(ctx, t, srv, "testsession", "verfile", 2)

	var got []time.Time
	for _, version := range []int64{1, 2} {
		resp, err := srv.GetArtifactVersion(ctx, &artifact.GetArtifactVersionRequest{
			AppName: "testapp", UserID: "testuser", SessionID: "testsession", FileName: "verfile",
			Version: version,
		})
		if err != nil {
			t.Fatalf("GetArtifactVersion(%d) failed: %v", version, err)
		}
		got = append(got, resp.ArtifactVersion.CreateTime)
	}
	want := []time.Time{base.Add(time.Hour), base.Add(2 * time.Hour)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("CreateTime per version mismatch (-want +got):\n%s", diff)
	}
}
