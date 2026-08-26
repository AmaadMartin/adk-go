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

package routers

import (
	"net/http"

	"google.golang.org/adk/v2/server/adkrest/controllers"
)

// MemoryAPIRouter defines the routes for the Memory API.
type MemoryAPIRouter struct {
	memoryController *controllers.MemoryAPIController
}

// NewMemoryAPIRouter creates a new MemoryAPIRouter.
func NewMemoryAPIRouter(controller *controllers.MemoryAPIController) *MemoryAPIRouter {
	return &MemoryAPIRouter{memoryController: controller}
}

// Routes returns the routes for the Memory API.
func (r *MemoryAPIRouter) Routes() Routes {
	return Routes{
		Route{
			Name:        "PatchMemory",
			Methods:     []string{http.MethodPatch},
			Pattern:     "/apps/{app_name}/users/{user_id}/memory",
			HandlerFunc: r.memoryController.PatchMemoryHandler,
		},
	}
}
