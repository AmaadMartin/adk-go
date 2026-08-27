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

package agent

import "testing"

// TestLiveRequestStaysComparable guards the equality operator on LiveRequest.
//
// A map or a slice field takes == away from every caller, and the build stays
// green because nothing in this repository compares a LiveRequest. Only the
// apidiff job catches it, and only on a pull request. This case fails to
// compile instead, which is what makes the constraint visible to whoever adds
// the field. StateDelta is a *map[string]any for this reason.
func TestLiveRequestStaysComparable(t *testing.T) {
	delta := map[string]any{"ui_locale": "fr-FR"}
	a := LiveRequest{AudioStreamEnd: true, StateDelta: &delta}
	b := a

	if a != b {
		t.Error("a LiveRequest does not equal its own copy")
	}
	if a == (LiveRequest{}) {
		t.Error("a populated LiveRequest equals the zero value")
	}
}
