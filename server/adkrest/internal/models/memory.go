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

package models

// UpdateMemoryRequest is the body of PATCH /apps/{app_name}/users/{user_id}/memory.
//
// adk-python accepts both spellings of the field: its schema advertises
// sessionId, but its own shipped client posts session_id.
type UpdateMemoryRequest struct {
	SessionID      string `json:"sessionId"`
	SessionIDAlias string `json:"session_id"`
}

// SessionIDValue returns the session ID under whichever spelling the caller used.
func (r UpdateMemoryRequest) SessionIDValue() string {
	if r.SessionID != "" {
		return r.SessionID
	}
	return r.SessionIDAlias
}
