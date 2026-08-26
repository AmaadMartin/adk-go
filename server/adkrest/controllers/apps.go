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

package controllers

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/server/adkrest/internal/services"
)

// AppsAPIController is the controller for the Apps API.
type AppsAPIController struct {
	agentLoader agent.Loader
}

// NewAppsAPIController creates a controller for Apps API.
func NewAppsAPIController(agentLoader agent.Loader) *AppsAPIController {
	return &AppsAPIController{agentLoader: agentLoader}
}

// ListAppsHandler handles listing all loaded agents.
func (c *AppsAPIController) ListAppsHandler(rw http.ResponseWriter, req *http.Request) {
	apps := c.agentLoader.ListAgents()
	EncodeJSONResponse(apps, http.StatusOK, rw)
}

// GetAppInfoHandler returns an app's root agent and its agent tree.
func (c *AppsAPIController) GetAppInfoHandler(rw http.ResponseWriter, req *http.Request) {
	appName := mux.Vars(req)["app_name"]
	if strings.HasPrefix(appName, "__") {
		http.Error(rw, "Access to internal special agents is disabled in API server mode.", http.StatusForbidden)
		return
	}
	rootAgent, err := c.agentLoader.LoadAgent(appName)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusNotFound)
		return
	}
	agents, ok := services.BuildAgentsTree(rootAgent)
	if !ok {
		http.Error(rw, "Root agent is not an LlmAgent", http.StatusBadRequest)
		return
	}
	EncodeJSONResponse(models.AppInfo{
		Name:          appName,
		RootAgentName: rootAgent.Name(),
		Description:   rootAgent.Description(),
		Language:      "go",
		Agents:        agents,
	}, http.StatusOK, rw)
}
