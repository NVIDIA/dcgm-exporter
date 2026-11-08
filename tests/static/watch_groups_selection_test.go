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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// ginkgoSelectedSpecs intentionally ignores the suite total, which grows as independent specs are added.
var ginkgoSelectedSpecs = regexp.MustCompile(`Will run ([0-9]+) of [0-9]+ specs`)

const watchGroupsLabel = "watchGroups"

// TestWatchGroupsLabelSelectsHostSpecs proves the catalog selector reaches at least one host spec.
func TestWatchGroupsLabelSelectsHostSpecs(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoPath(t), "go.mod")); os.IsNotExist(err) {
		t.Skip("host label analysis requires a source checkout and Go toolchain")
	} else if err != nil {
		t.Fatal(err)
	}
	if goExecutable() == "go" {
		if _, err := exec.LookPath("go"); err != nil {
			t.Skip("host label analysis requires a source checkout and Go toolchain")
		}
	}
	entry := scenario.MustFind(scenario.Catalog, scenario.SuiteHost, "watchGroups")
	if entry.ResultName != watchGroupsLabel {
		t.Fatalf("%s result name = %q, want %q", entry.Selector(), entry.ResultName, watchGroupsLabel)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	//nolint:gosec // Bazel supplies the executable from the declared Go SDK target.
	cmd := exec.CommandContext(ctx, goExecutable(),
		"test", "--tags=dcgm_uri_integration", "./tests/host",
		"-run", "^TestHostIntegration$", "-count=1", "-v",
		"-args", "--ginkgo.dry-run", "--ginkgo.no-color",
		"--ginkgo.label-filter="+watchGroupsLabel,
	)
	cmd.Dir = repoPath(t)
	// The dry-run only needs suite registration; disabling cgo keeps it independent of host DCGM libraries.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")

	// Ginkgo dry-run loads and filters the suite without running GPU/DCGM-dependent spec bodies.
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run host %s dry-run: %v\n%s", entry.Selector(), err, output)
	}

	match := ginkgoSelectedSpecs.FindSubmatch(output)
	if match == nil {
		t.Fatalf("host %s dry-run did not report selected specs:\n%s", entry.Selector(), output)
	}
	selected, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatalf("parse selected spec count %q: %v", match[1], err)
	}
	if selected == 0 {
		t.Fatalf("host %s label selected no specs:\n%s", entry.Selector(), output)
	}
}
