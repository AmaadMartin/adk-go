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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/mux"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/server/adkrest/controllers"
	"google.golang.org/adk/v2/server/adkrest/internal/fakes"
)

func TestPatchMemory(t *testing.T) {
	id := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "testSession",
	}
	storedSession := fakes.TestSession{
		Id:            id,
		SessionState:  fakes.TestState{"foo": "bar"},
		SessionEvents: fakes.TestEvents{},
		UpdatedAt:     time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}

	tc := []struct {
		name string
		// noMemoryService leaves the controller without a memory service.
		noMemoryService bool
		addErr          error
		storedSessions  map[fakes.SessionKey]fakes.TestSession
		sessionID       fakes.SessionKey
		body            string
		wantAddedIDs    []string
		wantErr         error
		wantStatus      int
	}{
		{
			name:           "session ID is remembered",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{id: storedSession},
			sessionID:      id,
			body:           `{"sessionId":"testSession"}`,
			wantAddedIDs:   []string{"testSession"},
			wantStatus:     http.StatusOK,
		},
		{
			name:            "memory service is not configured",
			noMemoryService: true,
			storedSessions:  map[fakes.SessionKey]fakes.TestSession{id: storedSession},
			sessionID:       id,
			body:            `{"sessionId":"testSession"}`,
			wantErr:         fmt.Errorf("Memory service is not configured."),
			wantStatus:      http.StatusBadRequest,
		},
		{
			name:           "user ID is missing in input",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{id: storedSession},
			sessionID:      fakes.SessionKey{AppName: "testApp"},
			body:           `{"sessionId":"testSession"}`,
			wantErr:        fmt.Errorf("user_id parameter is required"),
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "body is not valid JSON",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{id: storedSession},
			sessionID:      id,
			body:           `{"sessionId":`,
			wantErr:        fmt.Errorf("Update memory request is invalid."),
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "session ID is absent",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{id: storedSession},
			sessionID:      id,
			body:           `{}`,
			wantErr:        fmt.Errorf("Update memory request is invalid."),
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "session does not exist",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{},
			sessionID:      id,
			body:           `{"sessionId":"testSession"}`,
			wantErr:        fmt.Errorf("session not found: not found"),
			wantStatus:     http.StatusNotFound,
		},
		{
			name:           "memory service fails",
			addErr:         fmt.Errorf("memory is full"),
			storedSessions: map[fakes.SessionKey]fakes.TestSession{id: storedSession},
			sessionID:      id,
			body:           `{"sessionId":"testSession"}`,
			wantErr:        fmt.Errorf("memory is full"),
			wantStatus:     http.StatusInternalServerError,
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			sessionService := fakes.FakeSessionService{Sessions: tt.storedSessions}
			memoryService := &fakes.FakeMemoryService{AddErr: tt.addErr}
			// A nil interface value, not a typed nil, is what an unconfigured
			// ServerConfig hands the controller.
			var configured memory.Service
			if !tt.noMemoryService {
				configured = memoryService
			}
			apiController := controllers.NewMemoryAPIController(&sessionService, configured)
			req, err := http.NewRequest(http.MethodPatch, "/apps/testApp/users/testUser/memory", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req = mux.SetURLVars(req, map[string]string{
				"app_name": tt.sessionID.AppName,
				"user_id":  tt.sessionID.UserID,
			})
			rr := httptest.NewRecorder()

			apiController.PatchMemoryHandler(rr, req)

			if status := rr.Code; status != tt.wantStatus {
				t.Fatalf("PatchMemory() status = %v, want %v; body %q", status, tt.wantStatus, rr.Body.String())
			}
			if diff := cmp.Diff(tt.wantAddedIDs, addedSessionIDs(memoryService)); diff != "" {
				t.Errorf("PatchMemory() remembered sessions mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErr != nil {
				if respErr := strings.Trim(rr.Body.String(), "\n"); tt.wantErr.Error() != respErr {
					t.Errorf("PatchMemory() error = %q, want %q", respErr, tt.wantErr.Error())
				}
				return
			}
			if got := rr.Body.String(); got != "" {
				t.Errorf("PatchMemory() body = %q, want empty", got)
			}
		})
	}
}

// addedSessionIDs returns the IDs of the sessions handed to the memory service.
func addedSessionIDs(s *fakes.FakeMemoryService) []string {
	if len(s.AddedSessions) == 0 {
		return nil
	}
	ids := make([]string, 0, len(s.AddedSessions))
	for _, sess := range s.AddedSessions {
		ids = append(ids, sess.ID())
	}
	return ids
}
