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

// TestChartTolerationsRenderContract keeps default control-plane placement and permits opting into the GPU taint.
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

// TestGPUTaintedNodesPatch verifies the example strategic patch adds the GPU NoSchedule toleration.
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

// TestRawManifestSelectorsRemainStableAcrossReleaseVersions keeps selectors version-independent while retaining version labels.
func TestRawManifestSelectorsRemainStableAcrossReleaseVersions(t *testing.T) {
	manifest, err := os.ReadFile(repoPath(t, "deployment", "examples", "dcgm-exporter.yaml"))
	require.NoError(t, err)

	resources := parseManifest(t, string(manifest))
	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	require.Equal(t, map[string]string{
		"app.kubernetes.io/name": "dcgm-exporter",
	}, daemonSet.Spec.Selector.MatchLabels)
	require.NotEmpty(t, daemonSet.Metadata.Labels["app.kubernetes.io/version"])

	var service struct {
		Spec struct {
			Selector map[string]string `yaml:"selector"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(requireManifestKind(t, string(manifest), "Service"), &service))
	require.Equal(t, map[string]string{
		"app.kubernetes.io/name": "dcgm-exporter",
	}, service.Spec.Selector)

	serviceMonitor, err := os.ReadFile(repoPath(t, "deployment", "examples", "service-monitor.yaml"))
	require.NoError(t, err)

	resource := requireResourceKind(t, parseManifest(t, string(serviceMonitor)), "ServiceMonitor")
	require.Equal(t, map[string]string{
		"app.kubernetes.io/name": "dcgm-exporter",
	}, resource.Spec.Selector.MatchLabels)
	require.NotEmpty(t, resource.Metadata.Labels["app.kubernetes.io/version"])
}

// TestRawManifestVersionedSelectorMigration documents deletion as the upgrade path for immutable selectors.
func TestRawManifestVersionedSelectorMigration(t *testing.T) {
	readme, err := os.ReadFile(repoPath(t, "deployment", "examples", "README.md"))
	require.NoError(t, err)

	require.Contains(t, strings.ReplaceAll(string(readme), "\n", " "), "Kubernetes selectors are immutable")
	require.Contains(t, string(readme), "kubectl delete daemonset dcgm-exporter")
}

// requireManifestKind returns the raw YAML document for a resource kind or fails the test.
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

// assertToleratesNoSchedule requires an Exists toleration for the named NoSchedule taint.
func assertToleratesNoSchedule(t *testing.T, tolerations []toleration, key string) {
	t.Helper()

	for _, toleration := range tolerations {
		if toleration.Key == key && toleration.Operator == "Exists" && toleration.Effect == "NoSchedule" {
			return
		}
	}
	t.Errorf("no Exists/NoSchedule toleration for taint %q in %+v", key, tolerations)
}
