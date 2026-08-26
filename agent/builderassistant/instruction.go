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

package builderassistant

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/session"
)

//go:embed instruction.md
var instructionTemplate string

// The template carries these three placeholders and nothing else. They are
// spelled with doubled braces so they cannot collide with the single-brace
// session-state syntax of llmagent.Config.Instruction.
const (
	schemaPlaceholder = "{{schema_content}}"
	modelPlaceholder  = "{{default_model}}"
	folderPlaceholder = "{{project_folder_name}}"
)

// fallbackProjectFolderName names the project when the root directory has no
// name of its own, which happens only when it is the filesystem root.
const fallbackProjectFolderName = "project"

// newInstructionProvider returns the assistant's instruction provider.
//
// The schema reference and the model id are the same on every invocation, so
// they are substituted once here. Only the project folder name is resolved per
// invocation, because the session decides it.
func newInstructionProvider(modelName string) llmagent.InstructionProvider {
	prompt := strings.NewReplacer(
		schemaPlaceholder, agentConfigReference(),
		modelPlaceholder, modelName,
	).Replace(instructionTemplate)

	return func(ctx agent.ReadonlyContext) (string, error) {
		folder, err := projectFolderName(ctx.ReadonlyState())
		if err != nil {
			return "", err
		}
		return strings.ReplaceAll(prompt, folderPlaceholder, folder), nil
	}
}

// projectFolderName is the last element of the session's root directory. The
// assistant names it in its replies, and tells the model not to repeat it in a
// path.
func projectFolderName(state session.ReadonlyState) (string, error) {
	dir, err := rootDirectory(state)
	if err != nil {
		return "", fmt.Errorf("builderassistant: name the project folder: %w", err)
	}
	name := filepath.Base(dir)
	if name == "." || name == string(filepath.Separator) {
		return fallbackProjectFolderName, nil
	}
	return name, nil
}
