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

package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/session"
)

// MemoryAPIController is the controller for the Memory API.
type MemoryAPIController struct {
	sessionService session.Service
	memoryService  memory.Service
}

// NewMemoryAPIController creates a new MemoryAPIController. A nil memoryService
// is allowed; the handler then reports that memory is not configured.
func NewMemoryAPIController(sessionService session.Service, memoryService memory.Service) *MemoryAPIController {
	return &MemoryAPIController{sessionService: sessionService, memoryService: memoryService}
}

// PatchMemoryHandler adds all events of a session to the memory service.
func (c *MemoryAPIController) PatchMemoryHandler(rw http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	sessionID, err := models.SessionIDFromHTTPParameters(params)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	if c.memoryService == nil {
		http.Error(rw, "Memory service is not configured.", http.StatusBadRequest)
		return
	}
	var updateRequest models.UpdateMemoryRequest
	if err := json.NewDecoder(req.Body).Decode(&updateRequest); err != nil {
		http.Error(rw, "Update memory request is invalid.", http.StatusBadRequest)
		return
	}
	if updateRequest.SessionID == "" {
		http.Error(rw, "Update memory request is invalid.", http.StatusBadRequest)
		return
	}

	ctx := req.Context()
	storedSession, err := c.sessionService.Get(ctx, &session.GetRequest{
		AppName:   sessionID.AppName,
		UserID:    sessionID.UserID,
		SessionID: updateRequest.SessionID,
	})
	if err != nil {
		http.Error(rw, fmt.Errorf("session not found: %w", err).Error(), http.StatusNotFound)
		return
	}
	if err := c.memoryService.AddSessionToMemory(ctx, storedSession.Session); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	EncodeJSONResponse(nil, http.StatusOK, rw)
}
