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

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/cmd/launcher"
	"google.golang.org/adk/v2/session"
)

// newRouter builds the launcher's router the way SetupSubrouters does, with
// the given path prefix.
func newRouter(t *testing.T, pathPrefix string) *mux.Router {
	t.Helper()
	rootAgent, err := llmagent.New(llmagent.Config{Name: "test-app"})
	if err != nil {
		t.Fatalf("llmagent.New() failed: %v", err)
	}
	launcherAPI := &apiLauncher{config: &apiConfig{
		frontendAddress: "http://localhost:4200",
		pathPrefix:      pathPrefix,
	}}
	router := mux.NewRouter()
	config := &launcher.Config{
		SessionService: session.InMemoryService(),
		AgentLoader:    agent.NewSingleLoader(rootAgent),
	}
	if err := launcherAPI.SetupSubrouters(router, config); err != nil {
		t.Fatalf("SetupSubrouters() failed: %v", err)
	}
	return router
}

// TestPatchReachesTheAPI pins that the launcher's method allowlist passes
// PATCH through. Without it the two PATCH endpoints answer 405.
func TestPatchReachesTheAPI(t *testing.T) {
	for _, pathPrefix := range []string{"", "/api"} {
		t.Run("prefix "+pathPrefix, func(t *testing.T) {
			router := newRouter(t, pathPrefix)
			// A session that does not exist. Reaching the handler at all is
			// the point: a blocked method answers 405 instead.
			target := pathPrefix + "/apps/test-app/users/u1/sessions/missing"
			req := httptest.NewRequest(http.MethodPatch, target, strings.NewReader(`{"stateDelta":{"k":"v"}}`))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			if got, want := recorder.Code, http.StatusNotFound; got != want {
				t.Errorf("PATCH %s status = %d, want %d; body %q", target, got, want, recorder.Body.String())
			}
		})
	}
}

// TestCorsAllowsPatch pins the preflight header, which a browser checks before
// it sends a PATCH.
func TestCorsAllowsPatch(t *testing.T) {
	recorder := httptest.NewRecorder()
	handler := corsWithArgs("http://localhost:4200")(http.NotFoundHandler())

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodOptions, "/apps/test-app/users/u1/memory", nil))

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Errorf("preflight status = %d, want %d", got, want)
	}
	allowed := recorder.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowed, "PATCH") {
		t.Errorf("Access-Control-Allow-Methods = %q, want it to include PATCH", allowed)
	}
}
