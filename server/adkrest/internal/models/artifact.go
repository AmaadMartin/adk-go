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

import (
	"google.golang.org/adk/v2/artifact"
)

// ArtifactVersion is the wire representation of [artifact.ArtifactVersion].
//
// The service type carries no JSON tags, so encoding it directly would emit
// PascalCase keys and an RFC 3339 create time. Both differ from adk-python.
type ArtifactVersion struct {
	Version        int64          `json:"version"`
	CanonicalURI   string         `json:"canonicalUri"`
	CustomMetadata map[string]any `json:"customMetadata"`
	CreateTime     float64        `json:"createTime"`
	MimeType       string         `json:"mimeType,omitempty"`
}

// FromArtifactVersion maps an artifact version to its wire representation.
//
// CreateTime becomes unix seconds, and an absent custom metadata map becomes
// an empty object, because adk-python defaults that field to {}.
func FromArtifactVersion(v *artifact.ArtifactVersion) ArtifactVersion {
	metadata := v.CustomMetadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	var createTime float64
	if !v.CreateTime.IsZero() {
		createTime = float64(v.CreateTime.UnixNano()) / 1e9
	}
	return ArtifactVersion{
		Version:        v.Version,
		CanonicalURI:   v.CanonicalURI,
		CustomMetadata: metadata,
		CreateTime:     createTime,
		MimeType:       v.MimeType,
	}
}
