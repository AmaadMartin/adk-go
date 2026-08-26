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

// Package adkrest provides an HTTP server for the ADK REST API.
package adkrest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/mux"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/trace"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/internal/version"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/server/adkrest/controllers"
	"google.golang.org/adk/v2/server/adkrest/internal/models"
	"google.golang.org/adk/v2/server/adkrest/internal/routers"
	"google.golang.org/adk/v2/server/adkrest/internal/services"
	"google.golang.org/adk/v2/session"
)

// NewServer creates a new ADK REST API server which implements [http.Handler] interface.
func NewServer(cfg ServerConfig) (*Server, error) {
	debugTelemetry, err := services.NewDebugTelemetryWithConfig(&services.DebugTelemetryConfig{
		TraceCapacity: cfg.DebugConfig.TraceCapacity,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create debug telemetry service: %w", err)
	}

	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/health", healthHandler).Methods(http.MethodGet)
	router.HandleFunc("/version", versionHandler).Methods(http.MethodGet)
	var appsOpts []controllers.AppsAPIOption
	if cfg.AllowSpecialAgents {
		appsOpts = append(appsOpts, controllers.WithSpecialAgents())
	}
	// TODO: Allow taking a prefix to allow customizing the path
	// where the ADK REST API will be served.
	setupRouter(router,
		routers.NewSessionsAPIRouter(controllers.NewSessionsAPIController(cfg.SessionService)),
		routers.NewRuntimeAPIRouter(controllers.NewRuntimeAPIController(cfg.SessionService, cfg.MemoryService, cfg.AgentLoader, cfg.ArtifactService, cfg.SSEWriteTimeout, cfg.PluginConfig, false)),
		routers.NewMemoryAPIRouter(controllers.NewMemoryAPIController(cfg.SessionService, cfg.MemoryService)),
		routers.NewAppsAPIRouter(controllers.NewAppsAPIController(cfg.AgentLoader, appsOpts...)),
		routers.NewDebugAPIRouter(controllers.NewDebugAPIController(cfg.SessionService, cfg.AgentLoader, debugTelemetry)),
		routers.NewArtifactsAPIRouter(controllers.NewArtifactsAPIController(cfg.ArtifactService)),
		&routers.EvalAPIRouter{},
	)
	return &Server{
		router:         router,
		telemetryStore: debugTelemetry,
	}, nil
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(models.VersionInfo{
		Version:  version.Version,
		Language: "go",
		// Report a bare version like adk-python does, not the "go" prefixed
		// toolchain string. A devel toolchain has no prefix and passes through.
		LanguageVersion: strings.TrimPrefix(runtime.Version(), "go"),
	})
}

// ServerConfig contains parameters for the ADK REST API server.
type ServerConfig struct {
	SessionService  session.Service
	MemoryService   memory.Service
	AgentLoader     agent.Loader
	ArtifactService artifact.Service
	SSEWriteTimeout time.Duration
	PluginConfig    runner.PluginConfig
	DebugConfig     DebugTelemetryConfig
	// AllowSpecialAgents lets the Apps API serve apps whose name starts with
	// "__". Those apps are internal, so the API rejects them by default.
	AllowSpecialAgents bool
}

// DebugTelemetryConfig contains parameters for the debug telemetry.
type DebugTelemetryConfig struct {
	// Maximum number of traces to keep in memory.
	// If <= 0, the default capacity 10_000 is used.
	TraceCapacity int
}

// Server is an HTTP server that serves the ADK REST API.
type Server struct {
	router         *mux.Router
	telemetryStore *services.DebugTelemetry
}

// ServeHTTP makes [Server] implement [http.Handler] interface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// SpanProcessor returns a processor that captures spans used for /debug/trace endpoint of the ADK REST API server.
// You can register it in your application TracerProvider to populate it with these spans.
func (s *Server) SpanProcessor() trace.SpanProcessor {
	return s.telemetryStore.SpanProcessor()
}

// LogProcessor returns a processor that captures log records used for /debug/trace endpoint of the ADK REST API server.
// You can register it in your application LoggerProvider to populate it with these logs.
func (s *Server) LogProcessor() sdklog.Processor {
	return s.telemetryStore.LogProcessor()
}

func setupRouter(router *mux.Router, subrouters ...routers.Router) *mux.Router {
	routers.SetupSubRouters(router, subrouters...)
	return router
}
