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

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// TestWriteDryRunRequirementsDescribesExternalKubernetesImages lists external cluster image requirements.
func TestWriteDryRunRequirementsDescribesExternalKubernetesImages(t *testing.T) {
	selected := []scenario.Scenario{scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "default")}
	opts := config.TestOptions{
		Kubeconfig:    "/tmp/external-kubeconfig",
		ExporterImage: "registry.example/dcgm-exporter:dev",
	}
	var output bytes.Buffer
	writeDryRunRequirements(&output, opts, selected)

	for _, want := range []string{
		"kubernetes-image registry.example/dcgm-exporter:dev:",
		"kubernetes-image cuda-workload (configure its image reference before execution):",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("dry-run requirements missing %q:\n%s", want, output.String())
		}
	}
}
