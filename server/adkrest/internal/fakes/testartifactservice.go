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

	"google.golang.org/adk/v2/artifact"
)

// FakeArtifactService serves programmable artifact version metadata.
//
// [artifact.InMemoryService] populates only the version and the MIME type, so
// it cannot exercise the canonical URI, the custom metadata or the create time.
type FakeArtifactService struct {
	// VersionList is what Versions returns, in the order given.
	VersionList []int64
	// VersionsErr, when set, is what Versions returns instead of VersionList.
	VersionsErr error
	// Metadata holds the metadata GetArtifactVersion returns per version. A
	// version that is absent from the map yields a nil ArtifactVersion.
	Metadata map[int64]*artifact.ArtifactVersion
	// MetadataErr holds the error GetArtifactVersion returns per version.
	MetadataErr map[int64]error
	// RequestedVersions records every version GetArtifactVersion was asked for.
	RequestedVersions []int64
}

func (s *FakeArtifactService) Versions(ctx context.Context, req *artifact.VersionsRequest) (*artifact.VersionsResponse, error) {
	if s.VersionsErr != nil {
		return nil, s.VersionsErr
	}
	return &artifact.VersionsResponse{Versions: s.VersionList}, nil
}

func (s *FakeArtifactService) GetArtifactVersion(ctx context.Context, req *artifact.GetArtifactVersionRequest) (*artifact.GetArtifactVersionResponse, error) {
	s.RequestedVersions = append(s.RequestedVersions, req.Version)
	if err, ok := s.MetadataErr[req.Version]; ok {
		return nil, err
	}
	return &artifact.GetArtifactVersionResponse{ArtifactVersion: s.Metadata[req.Version]}, nil
}

func (s *FakeArtifactService) Save(ctx context.Context, req *artifact.SaveRequest) (*artifact.SaveResponse, error) {
	return nil, fmt.Errorf("Save is not supported by FakeArtifactService")
}

func (s *FakeArtifactService) Load(ctx context.Context, req *artifact.LoadRequest) (*artifact.LoadResponse, error) {
	return nil, fmt.Errorf("Load is not supported by FakeArtifactService")
}

func (s *FakeArtifactService) Delete(ctx context.Context, req *artifact.DeleteRequest) error {
	return fmt.Errorf("Delete is not supported by FakeArtifactService")
}

func (s *FakeArtifactService) List(ctx context.Context, req *artifact.ListRequest) (*artifact.ListResponse, error) {
	return nil, fmt.Errorf("List is not supported by FakeArtifactService")
}

var _ artifact.Service = (*FakeArtifactService)(nil)
