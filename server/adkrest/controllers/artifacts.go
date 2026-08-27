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
	"context"
	"errors"
	"io/fs"
	"net/http"
	"slices"
	"strconv"

	"github.com/gorilla/mux"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
)

// ArtifactsAPIController is the controller for the Artifacts API.
type ArtifactsAPIController struct {
	artifactService artifact.Service
}

// NewArtifactsAPIController creates an ArtifactsAPIController backed by the given artifact service.
func NewArtifactsAPIController(artifactService artifact.Service) *ArtifactsAPIController {
	return &ArtifactsAPIController{artifactService: artifactService}
}

// ListArtifactsHandler lists all the artifact filenames within a session.
func (c *ArtifactsAPIController) ListArtifactsHandler(rw http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	sessionID, err := models.SessionIDFromHTTPParameters(vars)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	if sessionID.ID == "" {
		http.Error(rw, "session_id parameter is required", http.StatusBadRequest)
		return
	}
	resp, err := c.artifactService.List(req.Context(), &artifact.ListRequest{
		AppName:   sessionID.AppName,
		UserID:    sessionID.UserID,
		SessionID: sessionID.ID,
	})
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	files := resp.FileNames
	if files == nil {
		files = []string{}
	}
	EncodeJSONResponse(files, http.StatusOK, rw)
}

// LoadArtifactHandler gets an artifact from the artifact service storage.
func (c *ArtifactsAPIController) LoadArtifactHandler(rw http.ResponseWriter, req *http.Request) {
	sessionID, artifactName, err := artifactTargetFromVars(mux.Vars(req))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	loadReq := &artifact.LoadRequest{
		AppName:   sessionID.AppName,
		UserID:    sessionID.UserID,
		SessionID: sessionID.ID,
		FileName:  artifactName,
	}

	queryParams := req.URL.Query()
	version := queryParams.Get("version")
	if version != "" {
		versionInt, err := strconv.Atoi(version)
		if err != nil {
			http.Error(rw, "version parameter must be an integer", http.StatusBadRequest)
			return
		}
		loadReq.Version = int64(versionInt)
	}

	resp, err := c.artifactService.Load(req.Context(), loadReq)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	EncodeJSONResponse(resp.Part, http.StatusOK, rw)
}

// LoadArtifactVersionHandler gets an artifact from the artifact service storage with specified version.
func (c *ArtifactsAPIController) LoadArtifactVersionHandler(rw http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	sessionID, artifactName, err := artifactTargetFromVars(vars)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	version := vars["version"]

	if version == "" {
		http.Error(rw, "version parameter is required", http.StatusBadRequest)
		return
	}

	versionInt, err := strconv.Atoi(version)
	if err != nil {
		http.Error(rw, "version parameter must be an integer", http.StatusBadRequest)
		return
	}

	loadReq := &artifact.LoadRequest{
		AppName:   sessionID.AppName,
		UserID:    sessionID.UserID,
		SessionID: sessionID.ID,
		FileName:  artifactName,
		Version:   int64(versionInt),
	}

	resp, err := c.artifactService.Load(req.Context(), loadReq)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	EncodeJSONResponse(resp.Part, http.StatusOK, rw)
}

// DeleteArtifactHandler handles deleting an artifact.
func (c *ArtifactsAPIController) DeleteArtifactHandler(rw http.ResponseWriter, req *http.Request) {
	sessionID, artifactName, err := artifactTargetFromVars(mux.Vars(req))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	err = c.artifactService.Delete(req.Context(), &artifact.DeleteRequest{
		AppName:   sessionID.AppName,
		UserID:    sessionID.UserID,
		SessionID: sessionID.ID,
		FileName:  artifactName,
	})
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	EncodeJSONResponse(nil, http.StatusOK, rw)
}

// GetArtifactVersionMetadataHandler returns the metadata of one artifact version.
func (c *ArtifactsAPIController) GetArtifactVersionMetadataHandler(rw http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	sessionID, artifactName, err := artifactTargetFromVars(vars)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	// "latest" asks for the newest version, which GetArtifactVersion expresses
	// as version 0.
	var version int64
	if versionID := vars["version_id"]; versionID != "latest" {
		parsed, err := strconv.ParseInt(versionID, 10, 64)
		if err != nil || parsed < 0 {
			http.Error(rw, "version parameter must be an integer", http.StatusBadRequest)
			return
		}
		version = parsed
	}
	metadata, err := c.versionMetadata(req.Context(), sessionID, artifactName, version)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(rw, "artifact version not found", http.StatusNotFound)
			return
		}
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	EncodeJSONResponse(metadata, http.StatusOK, rw)
}

// ListArtifactVersionsMetadataHandler returns the metadata of every version of
// an artifact, oldest version first.
func (c *ArtifactsAPIController) ListArtifactVersionsMetadataHandler(rw http.ResponseWriter, req *http.Request) {
	sessionID, artifactName, err := artifactTargetFromVars(mux.Vars(req))
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := c.artifactService.Versions(req.Context(), &artifact.VersionsRequest{
		AppName:   sessionID.AppName,
		UserID:    sessionID.UserID,
		SessionID: sessionID.ID,
		FileName:  artifactName,
	})
	// An artifact with no versions is an empty list, not an error, as in adk-python.
	if errors.Is(err, fs.ErrNotExist) {
		EncodeJSONResponse([]models.ArtifactVersion{}, http.StatusOK, rw)
		return
	}
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	// The in-memory service lists versions newest first.
	versions := slices.Sorted(slices.Values(resp.Versions))
	metadata := []models.ArtifactVersion{}
	for _, version := range versions {
		one, err := c.versionMetadata(req.Context(), sessionID, artifactName, version)
		if err != nil {
			// The version disappeared between the two calls.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		metadata = append(metadata, one)
	}
	EncodeJSONResponse(metadata, http.StatusOK, rw)
}

// versionMetadata loads the wire metadata of a single artifact version. It
// reports a missing version as [fs.ErrNotExist].
func (c *ArtifactsAPIController) versionMetadata(ctx context.Context, sessionID models.SessionID, artifactName string, version int64) (models.ArtifactVersion, error) {
	resp, err := c.artifactService.GetArtifactVersion(ctx, &artifact.GetArtifactVersionRequest{
		AppName:   sessionID.AppName,
		UserID:    sessionID.UserID,
		SessionID: sessionID.ID,
		FileName:  artifactName,
		Version:   version,
	})
	if err != nil {
		return models.ArtifactVersion{}, err
	}
	if resp.ArtifactVersion == nil {
		return models.ArtifactVersion{}, fs.ErrNotExist
	}
	return models.FromArtifactVersion(resp.ArtifactVersion), nil
}

// artifactTargetFromVars reads the session and artifact name a request addresses.
func artifactTargetFromVars(vars map[string]string) (models.SessionID, string, error) {
	sessionID, err := models.SessionIDFromHTTPParameters(vars)
	if err != nil {
		return sessionID, "", err
	}
	if sessionID.ID == "" {
		return sessionID, "", errors.New("session_id parameter is required")
	}
	artifactName := vars["artifact_name"]
	if artifactName == "" {
		return sessionID, "", errors.New("artifact_name parameter is required")
	}
	return sessionID, artifactName, nil
}
