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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	gpuTaintKey          = "nvidia.com/gpu"
	controlPlaneTaintKey = "node-role.kubernetes.io/control-plane"
)

// TestChartDefaultTolerationsRenderContract verifies the default DaemonSet can schedule on tainted GPU nodes.
func TestChartDefaultTolerationsRenderContract(t *testing.T) {
	resources := renderChart(t, nil)

	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	tolerations := daemonSet.Spec.Template.Spec.Tolerations
	assertToleratesNoSchedule(t, tolerations, controlPlaneTaintKey)
	assertToleratesNoSchedule(t, tolerations, gpuTaintKey)
}

// TestChartTolerationsOverrideReplacesDefaults verifies user-supplied tolerations are rendered verbatim.
func TestChartTolerationsOverrideReplacesDefaults(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"tolerations": []interface{}{
			map[string]interface{}{
				"key":      "example.com/accelerator",
				"operator": "Exists",
				"effect":   "NoSchedule",
			},
		},
	})

	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	tolerations := daemonSet.Spec.Template.Spec.Tolerations
	require.Len(t, tolerations, 1)
	assert.Equal(t, "example.com/accelerator", tolerations[0].Key)
	assert.Equal(t, "Exists", tolerations[0].Operator)
	assert.Equal(t, "NoSchedule", tolerations[0].Effect)
}

// TestStandaloneManifestToleratesGPUTaint keeps the raw DaemonSet manifest aligned with the chart default.
func TestStandaloneManifestToleratesGPUTaint(t *testing.T) {
	manifest, err := os.ReadFile(repoPath(t, "dcgm-exporter.yaml"))
	require.NoError(t, err)

	resources := parseManifest(t, string(manifest))
	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	assertToleratesNoSchedule(t, daemonSet.Spec.Template.Spec.Tolerations, gpuTaintKey)
}

// assertToleratesNoSchedule requires an Exists/NoSchedule toleration for the given taint key.
func assertToleratesNoSchedule(t *testing.T, tolerations []toleration, key string) {
	t.Helper()

	for _, tol := range tolerations {
		if tol.Key == key && tol.Operator == "Exists" && tol.Effect == "NoSchedule" {
			return
		}
	}
	t.Errorf("no Exists/NoSchedule toleration for taint %q in %+v", key, tolerations)
}
