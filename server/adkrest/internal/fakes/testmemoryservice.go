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

package fakes

import (
	"context"
	"fmt"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

// FakeMemoryService records the sessions it was asked to remember.
type FakeMemoryService struct {
	// AddedSessions holds every session passed to AddSessionToMemory.
	AddedSessions []session.Session
	// AddErr, when set, is returned by AddSessionToMemory.
	AddErr error
}

func (s *FakeMemoryService) AddSessionToMemory(ctx context.Context, sess session.Session) error {
	if s.AddErr != nil {
		return s.AddErr
	}
	s.AddedSessions = append(s.AddedSessions, sess)
	return nil
}

func (s *FakeMemoryService) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

var _ memory.Service = (*FakeMemoryService)(nil)
