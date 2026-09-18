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
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/releaseutil"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"sigs.k8s.io/yaml"
)

const (
	controlPlaneTaintKey = "node-role.kubernetes.io/control-plane"
	gpuTaintKey          = "nvidia.com/gpu"
)

func TestChartTolerationsRenderContract(t *testing.T) {
	t.Run("default tolerations remain unchanged", func(t *testing.T) {
		resources := renderChart(t, nil)
		tolerations := requireResourceKind(t, resources, "DaemonSet").Spec.Template.Spec.Tolerations

		require.Len(t, tolerations, 1)
		assertToleratesNoSchedule(t, tolerations, controlPlaneTaintKey)
	})

	t.Run("GPU taint toleration can be opted into", func(t *testing.T) {
		resources := renderChart(t, map[string]interface{}{
			"tolerations": []interface{}{
				map[string]interface{}{
					"key":      controlPlaneTaintKey,
					"operator": "Exists",
					"effect":   "NoSchedule",
				},
				map[string]interface{}{
					"key":      gpuTaintKey,
					"operator": "Exists",
					"effect":   "NoSchedule",
				},
			},
		})
		tolerations := requireResourceKind(t, resources, "DaemonSet").Spec.Template.Spec.Tolerations

		require.Len(t, tolerations, 2)
		assertToleratesNoSchedule(t, tolerations, controlPlaneTaintKey)
		assertToleratesNoSchedule(t, tolerations, gpuTaintKey)
	})
}

func TestGPUTaintedNodesPatch(t *testing.T) {
	base, err := os.ReadFile(repoPath(t, "deployment/examples/dcgm-exporter.yaml"))
	require.NoError(t, err)

	patch, err := os.ReadFile(repoPath(t, "deployment/examples/gpu-tainted-nodes-patch.yaml"))
	require.NoError(t, err)

	baseJSON, err := yaml.YAMLToJSON(requireManifestKind(t, string(base), "DaemonSet"))
	require.NoError(t, err)

	patchJSON, err := yaml.YAMLToJSON(patch)
	require.NoError(t, err)

	manifest, err := strategicpatch.StrategicMergePatch(
		baseJSON,
		patchJSON,
		appsv1.DaemonSet{},
	)
	require.NoError(t, err)

	daemonSet := requireResourceKind(t, parseManifest(t, string(manifest)), "DaemonSet")
	require.Len(t, daemonSet.Spec.Template.Spec.Tolerations, 1)
	assertToleratesNoSchedule(t, daemonSet.Spec.Template.Spec.Tolerations, gpuTaintKey)
}

func requireManifestKind(t *testing.T, manifest string, kind string) []byte {
	t.Helper()

	documents := releaseutil.SplitManifests(manifest)
	keys := make([]string, 0, len(documents))
	for key := range documents {
		keys = append(keys, key)
	}
	sort.Sort(releaseutil.BySplitManifestsOrder(keys))

	for _, key := range keys {
		document := strings.TrimSpace(documents[key])
		if document == "" {
			continue
		}

		var resource chartResource
		require.NoError(t, yaml.Unmarshal([]byte(document), &resource))
		if resource.Kind == kind {
			return []byte(document)
		}
	}

	t.Fatalf("manifest has no %s resource", kind)
	return nil
}

func assertToleratesNoSchedule(t *testing.T, tolerations []toleration, key string) {
	t.Helper()

	for _, toleration := range tolerations {
		if toleration.Key == key && toleration.Operator == "Exists" && toleration.Effect == "NoSchedule" {
			return
		}
	}
	t.Errorf("no Exists/NoSchedule toleration for taint %q in %+v", key, tolerations)
}
