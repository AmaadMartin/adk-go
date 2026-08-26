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

// The template carries these four placeholders and nothing else. They are
// spelled with doubled braces so they cannot collide with the single-brace
// session-state syntax of llmagent.Config.Instruction.
const (
	modelPlaceholder        = "{{default_model}}"
	folderPlaceholder       = "{{project_folder_name}}"
	toolNamesPlaceholder    = "{{tool_names}}"
	agentClassesPlaceholder = "{{agent_classes}}"
)

// fallbackProjectFolderName names the project when the root directory has no
// name of its own, which happens only when it is the filesystem root.
const fallbackProjectFolderName = "project"

// newInstructionProvider returns the assistant's instruction provider.
//
// Only the project folder name changes between invocations, so everything else
// is substituted once here. The tool and agent class lists come from the same
// values write_config_files checks against, so the prompt cannot promise the
// model something the check rejects.
func newInstructionProvider(modelName string) llmagent.InstructionProvider {
	prompt := strings.NewReplacer(
		modelPlaceholder, modelName,
		toolNamesPlaceholder, strings.Join(configToolNames, ", "),
		agentClassesPlaceholder, strings.Join(agentClasses, ", "),
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
