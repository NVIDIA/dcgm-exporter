/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package static_test

import (
	"os"
	"strings"
	"testing"
)

// TestDockerfileLoadsDefaultConfigFile checks the Dockerfile text: the file exists under
// etc/, the runtime image installs etc/ and then points DCGM_EXPORTER_CONFIG_FILE at the
// file before the final USER and ENTRYPOINT. That the image really starts with it is
// covered by the container suite, which needs Docker and is not part of this static check.
func TestDockerfileLoadsDefaultConfigFile(t *testing.T) {
	if _, err := os.Stat(repoPath(t, "etc", "config.yaml")); err != nil {
		t.Fatalf("the default config file is missing: %v", err)
	}

	data, err := os.ReadFile(repoPath(t, "docker", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	// Split into build stages; each stage text starts at its FROM line.
	stages := map[string]string{}

	var name string
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if strings.HasPrefix(line, "FROM ") {
			fields := strings.Fields(line)
			name = fields[len(fields)-1]
		}
		stages[name] += line
	}

	const envLine = "\nENV DCGM_EXPORTER_CONFIG_FILE=/etc/dcgm-exporter/config.yaml\n"
	const stage = "runtime-distroless"

	text, ok := stages[stage]
	if !ok {
		t.Fatalf("Dockerfile has no %s stage", stage)
	}

	install := strings.Index(text, "etc/* /etc/dcgm-exporter/")
	env := strings.Index(text, envLine)
	user := strings.Index(text, "\nUSER ")
	entrypoint := strings.Index(text, "\nENTRYPOINT ")

	switch {
	case install < 0:
		t.Errorf("%s does not install etc/* into /etc/dcgm-exporter/", stage)
	case env < 0:
		t.Errorf("%s does not set DCGM_EXPORTER_CONFIG_FILE to the default config file", stage)
	case env < install:
		t.Errorf("%s sets DCGM_EXPORTER_CONFIG_FILE before it installs the file", stage)
	case user < 0 || entrypoint < 0 || env > user || env > entrypoint:
		t.Errorf("%s must set DCGM_EXPORTER_CONFIG_FILE before its final USER and ENTRYPOINT", stage)
	}
}
