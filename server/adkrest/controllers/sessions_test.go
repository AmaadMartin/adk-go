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

package controllers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/gorilla/mux"

	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/server/adkrest/controllers"
	"google.golang.org/adk/v2/server/adkrest/internal/fakes"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
)

func TestGetSession(t *testing.T) {
	id := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "testSession",
	}

	tc := []struct {
		name           string
		storedSessions map[fakes.SessionKey]fakes.TestSession
		sessionID      fakes.SessionKey
		wantSession    models.Session
		wantErr        error
		wantStatus     int
	}{
		{
			name: "session exists",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {
					Id:            id,
					SessionState:  fakes.TestState{"foo": "bar"},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
			},
			sessionID: id,
			wantSession: models.Session{
				ID:        "testSession",
				AppName:   "testApp",
				UserID:    "testUser",
				UpdatedAt: time.Now().Unix(),
				Events:    []models.Event{},
				State: map[string]any{
					"foo": "bar",
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:           "session does not exist",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{},
			sessionID:      id,
			wantErr:        fmt.Errorf("not found"),
			wantStatus:     http.StatusInternalServerError,
		},
		{
			name: "user ID is missing in input",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {
					Id:            id,
					SessionState:  fakes.TestState{"foo": "bar"},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
			},
			sessionID: fakes.SessionKey{
				AppName:   "testApp",
				SessionID: "testSession",
			},
			wantErr:    fmt.Errorf("user_id parameter is required"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "session ID is missing",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {
					Id: fakes.SessionKey{
						AppName: "testApp",
						UserID:  "testUser",
					},
					SessionState:  fakes.TestState{"foo": "bar"},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
			},
			sessionID:  id,
			wantErr:    fmt.Errorf("session_id is empty in received session"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			sessionService := fakes.FakeSessionService{Sessions: tt.storedSessions}
			apiController := controllers.NewSessionsAPIController(&sessionService)
			req, err := http.NewRequest(http.MethodGet, "/apps/testApp/users/testUser/sessions/testSession", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			// Manually set the URL variables on the request using mux.SetURLVars.
			req = mux.SetURLVars(req, sessionVars(tt.sessionID))
			rr := httptest.NewRecorder()

			apiController.GetSessionHandler(rr, req)

			if status := rr.Code; status != tt.wantStatus {
				t.Fatalf("handler returned wrong status code: got %v want %v", status, tt.wantStatus)
			}
			if tt.wantErr != nil {
				respErr := strings.Trim(rr.Body.String(), "\n")
				if tt.wantErr.Error() != respErr {
					t.Errorf("CreateSession() mismatch (-want +got):\n%v, %v", tt.wantErr.Error(), respErr)
				}
				return
			}
			var gotSession models.Session
			err = json.NewDecoder(rr.Body).Decode(&gotSession)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if diff := cmp.Diff(tt.wantSession, gotSession, EquateApproxInt(int64(time.Second))); diff != "" {
				t.Errorf("GetSession() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCreateSession(t *testing.T) {
	id := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "testSession",
	}

	tc := []struct {
		name             string
		storedSessions   map[fakes.SessionKey]fakes.TestSession
		sessionID        fakes.SessionKey
		createRequestObj models.CreateSessionRequest
		wantSession      models.Session
		wantErr          error
		wantStatus       int
	}{
		{
			name: "session exists",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {
					Id:            id,
					SessionState:  fakes.TestState{"foo": "bar"},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
			},
			sessionID:  id,
			wantErr:    fmt.Errorf("session already exists"),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:           "successful create operation",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{},
			sessionID:      id,
			createRequestObj: models.CreateSessionRequest{
				State: map[string]any{
					"foo": "bar",
				},
				Events: []models.Event{
					{
						ID:     "eventID",
						Author: "testUser",
					},
				},
			},
			wantSession: models.Session{
				ID:      "testSession",
				AppName: "testApp",
				UserID:  "testUser",
				State: map[string]any{
					"foo": "bar",
				},
				Events: []models.Event{
					{
						ID:     "eventID",
						Author: "testUser",
					},
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:           "user id is missing",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{},
			sessionID: fakes.SessionKey{
				AppName:   "testApp",
				SessionID: "testSession",
			},
			createRequestObj: models.CreateSessionRequest{},
			wantStatus:       http.StatusBadRequest,
			wantErr:          fmt.Errorf("user_id parameter is required"),
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			sessionService := fakes.FakeSessionService{Sessions: tt.storedSessions}
			apiController := controllers.NewSessionsAPIController(&sessionService)
			reqBytes, err := json.Marshal(tt.createRequestObj)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			req, err := http.NewRequest(http.MethodPost, "/apps/testApp/users/testUser/sessions/testSession", bytes.NewBuffer(reqBytes))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			// Manually set the URL variables on the request using mux.SetURLVars.
			req = mux.SetURLVars(req, sessionVars(tt.sessionID))
			rr := httptest.NewRecorder()

			apiController.CreateSessionHandler(rr, req)

			if status := rr.Code; status != tt.wantStatus {
				t.Errorf("handler returned wrong status code: got %v want %v", status, tt.wantStatus)
			}
			if tt.wantErr != nil {
				respErr := strings.Trim(rr.Body.String(), "\n")
				if tt.wantErr.Error() != respErr {
					t.Errorf("CreateSession() mismatch (-want +got):\n%v, %v", tt.wantErr.Error(), respErr)
				}
				return
			}
			var gotSession models.Session
			err = json.NewDecoder(rr.Body).Decode(&gotSession)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if diff := cmp.Diff(tt.wantSession, gotSession, EquateApproxInt(int64(time.Second)),
				cmpopts.IgnoreFields(models.Session{}, "UpdatedAt")); diff != "" {
				t.Errorf("CreateSession() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDeleteSession(t *testing.T) {
	id := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "testSession",
	}

	tc := []struct {
		name           string
		storedSessions map[fakes.SessionKey]fakes.TestSession
		sessionID      fakes.SessionKey
		wantStatus     int
	}{
		{
			name: "session exists",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {
					Id:            id,
					SessionState:  fakes.TestState{"foo": "bar"},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
			},
			sessionID:  id,
			wantStatus: http.StatusOK,
		},
		{
			name:           "session does not exist",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{},
			sessionID:      id,
			wantStatus:     http.StatusInternalServerError,
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			sessionService := fakes.FakeSessionService{Sessions: tt.storedSessions}
			apiController := controllers.NewSessionsAPIController(&sessionService)
			req, err := http.NewRequest(http.MethodDelete, "/apps/testApp/users/testUser/sessions/testSession", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			// Manually set the URL variables on the request using mux.SetURLVars.
			req = mux.SetURLVars(req, sessionVars(tt.sessionID))
			rr := httptest.NewRecorder()

			apiController.DeleteSessionHandler(rr, req)
			if status := rr.Code; status != tt.wantStatus {
				t.Fatalf("handler returned wrong status code: got %v want %v", status, tt.wantStatus)
			}
			if _, ok := sessionService.Sessions[tt.sessionID]; ok {
				t.Errorf("session was not deleted")
			}
		})
	}
}

func TestListSessions(t *testing.T) {
	id := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "testSession",
	}
	newSessionID := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "newSession",
	}
	oldSessionID := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "oldSession",
	}

	tc := []struct {
		name           string
		storedSessions map[fakes.SessionKey]fakes.TestSession
		wantSessions   []models.Session
		wantStatus     int
	}{
		{
			name: "session exists",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {
					Id:            id,
					SessionState:  fakes.TestState{"foo": "bar"},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
				newSessionID: {
					Id:            newSessionID,
					SessionState:  fakes.TestState{"xyz": "abc"},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
				oldSessionID: {
					Id:            oldSessionID,
					SessionState:  fakes.TestState{},
					SessionEvents: fakes.TestEvents{},
					UpdatedAt:     time.Now(),
				},
			},
			wantSessions: []models.Session{
				{
					ID:        "testSession",
					AppName:   "testApp",
					UserID:    "testUser",
					UpdatedAt: time.Now().Unix(),
					Events:    []models.Event{},
					State: map[string]any{
						"foo": "bar",
					},
				},
				{
					ID:        "newSession",
					AppName:   "testApp",
					UserID:    "testUser",
					UpdatedAt: time.Now().Unix(),
					Events:    []models.Event{},
					State: map[string]any{
						"xyz": "abc",
					},
				},
				{
					ID:        "oldSession",
					AppName:   "testApp",
					UserID:    "testUser",
					State:     map[string]any{},
					UpdatedAt: time.Now().Unix(),
					Events:    []models.Event{},
				},
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			sessionService := fakes.FakeSessionService{Sessions: tt.storedSessions}
			apiController := controllers.NewSessionsAPIController(&sessionService)
			req, err := http.NewRequest(http.MethodDelete, "/apps/testApp/users/testUser/sessions/testSession", nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			// Manually set the URL variables on the request using mux.SetURLVars.
			req = mux.SetURLVars(req, map[string]string{
				"app_name": "testApp",
				"user_id":  "testUser",
			})
			rr := httptest.NewRecorder()

			apiController.ListSessionsHandler(rr, req)
			if status := rr.Code; status != tt.wantStatus {
				t.Fatalf("handler returned wrong status code: got %v want %v", status, tt.wantStatus)
			}
			got := []models.Session{}
			err = json.NewDecoder(rr.Body).Decode(&got)
			if err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if diff := cmp.Diff(tt.wantSessions, got, EquateApproxInt(int64(time.Second)), cmpopts.SortSlices(func(a, b models.Session) bool {
				return a.ID < b.ID
			})); diff != "" {
				t.Errorf("ListSessions() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func sessionVars(sessionID fakes.SessionKey) map[string]string {
	return map[string]string{
		"app_name":   sessionID.AppName,
		"user_id":    sessionID.UserID,
		"session_id": sessionID.SessionID,
	}
}

// EquateApproxInt returns a cmp.Comparer option that determines integer values
// to be equal if they are within a certain absolute margin.
func EquateApproxInt(margin int64) cmp.Option {
	return cmp.Comparer(func(x, y int64) bool {
		diff := x - y
		if diff < 0 {
			diff = -diff
		}

		return diff <= margin
	})
}

func TestUpdateSession(t *testing.T) {
	id := fakes.SessionKey{
		AppName:   "testApp",
		UserID:    "testUser",
		SessionID: "testSession",
	}
	const fixedUUID = "fixed-uuid"
	fixedTime := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	tc := []struct {
		name           string
		storedSessions map[fakes.SessionKey]fakes.TestSession
		appendErr      error
		// epochTime timestamps the synthetic event at the Unix epoch, which
		// Session.Validate treats as an empty updatedAt.
		epochTime  bool
		sessionID  fakes.SessionKey
		body       string
		wantState  map[string]any
		wantDelta  map[string]any
		wantErr    error
		wantStatus int
	}{
		{
			name: "camelCase state delta is merged",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{"keep": "me"}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			sessionID:  id,
			body:       `{"stateDelta":{"added":"value"}}`,
			wantState:  map[string]any{"keep": "me", "added": "value"},
			wantDelta:  map[string]any{"added": "value"},
			wantStatus: http.StatusOK,
		},
		{
			name: "snake_case state delta is merged",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{"keep": "me"}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			sessionID:  id,
			body:       `{"state_delta":{"added":"value"}}`,
			wantState:  map[string]any{"keep": "me", "added": "value"},
			wantDelta:  map[string]any{"added": "value"},
			wantStatus: http.StatusOK,
		},
		{
			name: "an empty delta object is accepted",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{"keep": "me"}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			sessionID:  id,
			body:       `{"stateDelta":{}}`,
			wantState:  map[string]any{"keep": "me"},
			wantDelta:  map[string]any{},
			wantStatus: http.StatusOK,
		},
		{
			name: "user ID is missing in input",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			sessionID:  fakes.SessionKey{AppName: "testApp", SessionID: "testSession"},
			body:       `{"stateDelta":{"added":"value"}}`,
			wantErr:    fmt.Errorf("user_id parameter is required"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:           "session ID is missing in input",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{},
			sessionID:      fakes.SessionKey{AppName: "testApp", UserID: "testUser"},
			body:           `{"stateDelta":{"added":"value"}}`,
			wantErr:        fmt.Errorf("session_id parameter is required"),
			wantStatus:     http.StatusBadRequest,
		},
		{
			name: "body is not valid JSON",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			sessionID:  id,
			body:       `{"stateDelta":`,
			wantErr:    fmt.Errorf("unexpected EOF"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "body carries neither spelling",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			sessionID:  id,
			body:       `{}`,
			wantErr:    fmt.Errorf("stateDelta is required"),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:           "session does not exist",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{},
			sessionID:      id,
			body:           `{"stateDelta":{"added":"value"}}`,
			wantErr:        fmt.Errorf("session not found: not found"),
			wantStatus:     http.StatusNotFound,
		},
		{
			name: "appending the event fails",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			appendErr:  fmt.Errorf("storage is down"),
			sessionID:  id,
			body:       `{"stateDelta":{"added":"value"}}`,
			wantErr:    fmt.Errorf("storage is down"),
			wantStatus: http.StatusInternalServerError,
		},
		{
			// The event timestamp becomes the session updatedAt. This pins
			// that a session models.FromSession rejects produces a 500
			// rather than an invalid response body.
			name: "a session that fails validation is rejected",
			storedSessions: map[fakes.SessionKey]fakes.TestSession{
				id: {Id: id, SessionState: fakes.TestState{}, SessionEvents: fakes.TestEvents{}, UpdatedAt: fixedTime},
			},
			epochTime:  true,
			sessionID:  id,
			body:       `{"stateDelta":{"added":"value"}}`,
			wantErr:    fmt.Errorf("updated_at is empty"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			sessionService := fakes.FakeSessionService{Sessions: tt.storedSessions, AppendErr: tt.appendErr}
			apiController := controllers.NewSessionsAPIController(&sessionService)
			req, err := http.NewRequest(http.MethodPatch, "/apps/testApp/users/testUser/sessions/testSession", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req = mux.SetURLVars(req, sessionVars(tt.sessionID))
			// Pin the event ID and timestamp so the assertions are exact.
			eventTime := fixedTime
			if tt.epochTime {
				eventTime = time.Unix(0, 0)
			}
			ctx := platform.WithUUIDProvider(req.Context(), func() string { return fixedUUID })
			ctx = platform.WithTimeProvider(ctx, func() time.Time { return eventTime })
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			apiController.UpdateSessionHandler(rr, req)

			if status := rr.Code; status != tt.wantStatus {
				t.Fatalf("UpdateSession() status = %v, want %v; body %q", status, tt.wantStatus, rr.Body.String())
			}
			if tt.wantErr != nil {
				if respErr := strings.Trim(rr.Body.String(), "\n"); tt.wantErr.Error() != respErr {
					t.Errorf("UpdateSession() error = %q, want %q", respErr, tt.wantErr.Error())
				}
				return
			}
			var gotSession models.Session
			if err := json.NewDecoder(rr.Body).Decode(&gotSession); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if diff := cmp.Diff(tt.wantState, gotSession.State); diff != "" {
				t.Errorf("UpdateSession() state mismatch (-want +got):\n%s", diff)
			}
			wantEvents := []models.Event{{
				ID:           fixedUUID,
				InvocationID: "p-" + fixedUUID,
				Author:       "user",
				Actions: models.EventActions{
					StateDelta:    tt.wantDelta,
					ArtifactDelta: map[string]int64{},
				},
			}}
			if diff := cmp.Diff(wantEvents, gotSession.Events); diff != "" {
				t.Errorf("UpdateSession() events mismatch (-want +got):\n%s", diff)
			}
			if got, want := gotSession.UpdatedAt, fixedTime.Unix(); got != want {
				t.Errorf("UpdateSession() lastUpdateTime = %d, want %d", got, want)
			}
		})
	}
}
