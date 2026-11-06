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

package k8s

import (
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// requireContractError fails unless err is nil when want is empty, or err
// contains every string in want.
func requireContractError(t *testing.T, err error, want []string) {
	t.Helper()
	if len(want) == 0 {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}

	if err == nil {
		t.Fatalf("error = nil, want one containing %q", want)
	}
	for _, text := range want {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error = %v, want it to contain %q", err, text)
		}
	}
}

// TestClusterPolicyReadyError requires status.state "ready" on every policy.
func TestClusterPolicyReadyError(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr []string
	}{
		{name: "ready", data: `{"items":[{"metadata":{"name":"cluster-policy"},"status":{"state":"ready"}}]}`},
		{
			name:    "not ready",
			data:    `{"items":[{"metadata":{"name":"cluster-policy"},"status":{"state":"notReady"}}]}`,
			wantErr: []string{`ClusterPolicy "cluster-policy" status.state = "notReady", want "ready"`},
		},
		{
			name:    "no status yet",
			data:    `{"items":[{"metadata":{"name":"cluster-policy"}}]}`,
			wantErr: []string{`status.state = ""`},
		},
		{name: "no policy", data: `{"items":[]}`, wantErr: []string{"no ClusterPolicy found"}},
		{name: "not JSON", data: "error: the server doesn't have a resource type", wantErr: []string{"decode ClusterPolicy list"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireContractError(t, clusterPolicyReadyError([]byte(test.data)), test.wantErr)
		})
	}
}

// TestGPUOperatorExporterRolloutError checks DaemonSet readiness counts and
// the image on the pod template and on each owned pod.
func TestGPUOperatorExporterRolloutError(t *testing.T) {
	const image = "registry.example/dcgm-exporter:4.9.0"
	daemonSet := func(desired, ready, updated int, templateImage string) string {
		return `{"items":[{"metadata":{"name":"nvidia-dcgm-exporter"},` +
			`"spec":{"template":{"spec":{"containers":[{"name":"nvidia-dcgm-exporter","image":"` + templateImage + `"}]}}},` +
			`"status":{"desiredNumberScheduled":` + strconv.Itoa(desired) + `,"numberReady":` + strconv.Itoa(ready) + `,"updatedNumberScheduled":` + strconv.Itoa(updated) + `}},` +
			`{"metadata":{"name":"gpu-feature-discovery"},"status":{"desiredNumberScheduled":1}}]}`
	}
	podWithReady := func(name, phase, ready, podImage string) string {
		return `{"metadata":{"name":"` + name + `","ownerReferences":[{"kind":"DaemonSet","name":"nvidia-dcgm-exporter"}]},` +
			`"spec":{"containers":[{"name":"nvidia-dcgm-exporter","image":"` + podImage + `"}]},` +
			`"status":{"phase":"` + phase + `","conditions":[{"type":"Ready","status":"` + ready + `"}]}}`
	}
	pod := func(name, phase, podImage string) string {
		return podWithReady(name, phase, "True", podImage)
	}
	otherPod := `{"metadata":{"name":"gpu-operator-abc","ownerReferences":[{"kind":"ReplicaSet","name":"gpu-operator"}]},` +
		`"spec":{"containers":[{"name":"gpu-operator","image":"other"}]},"status":{"phase":"Running"}}`

	tests := []struct {
		name       string
		daemonSets string
		pods       string
		wantErr    []string
	}{
		{
			name:       "ready with expected image",
			daemonSets: daemonSet(2, 2, 2, image),
			pods:       `{"items":[` + pod("exporter-a", "Running", image) + `,` + pod("exporter-b", "Running", image) + `,` + otherPod + `]}`,
		},
		{
			name:       "not all pods ready",
			daemonSets: daemonSet(2, 1, 2, image),
			pods:       `{"items":[` + pod("exporter-a", "Running", image) + `,` + pod("exporter-b", "Pending", image) + `]}`,
			wantErr:    []string{"status.numberReady = 1, want desiredNumberScheduled 2", `pod "exporter-b" phase = "Pending"`},
		},
		{
			name:       "running pod is not ready",
			daemonSets: daemonSet(1, 1, 1, image),
			pods:       `{"items":[` + podWithReady("exporter-a", "Running", "False", image) + `]}`,
			wantErr:    []string{`pod "exporter-a" is running but its Ready condition is not True`},
		},
		{
			name:       "replaced failed pod is ignored",
			daemonSets: daemonSet(1, 1, 1, image),
			pods:       `{"items":[` + pod("exporter-old", "Failed", image) + `,` + pod("exporter-a", "Running", image) + `]}`,
		},
		{
			name:       "nothing scheduled",
			daemonSets: daemonSet(0, 0, 0, image),
			pods:       `{"items":[]}`,
			wantErr:    []string{"desiredNumberScheduled = 0"},
		},
		{
			name:       "rollout not finished",
			daemonSets: daemonSet(1, 1, 0, image),
			pods:       `{"items":[` + pod("exporter-a", "Running", image) + `]}`,
			wantErr:    []string{"status.updatedNumberScheduled = 0"},
		},
		{
			name:       "wrong image",
			daemonSets: daemonSet(1, 1, 1, "nvcr.io/nvidia/k8s/dcgm-exporter:old"),
			pods:       `{"items":[` + pod("exporter-a", "Running", "nvcr.io/nvidia/k8s/dcgm-exporter:old") + `]}`,
			wantErr:    []string{"pod template images", `pod "exporter-a" images`, image},
		},
		{
			name:       "owned pod missing",
			daemonSets: daemonSet(2, 2, 2, image),
			pods:       `{"items":[` + pod("exporter-a", "Running", image) + `]}`,
			wantErr:    []string{"owns 1 pods, want desiredNumberScheduled 2"},
		},
		{
			name:       "no exporter DaemonSet",
			daemonSets: `{"items":[{"metadata":{"name":"gpu-feature-discovery"}}]}`,
			pods:       `{"items":[]}`,
			wantErr:    []string{"no GPU Operator dcgm-exporter DaemonSet found"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := gpuOperatorExporterRolloutError([]byte(test.daemonSets), []byte(test.pods), image)
			requireContractError(t, err, test.wantErr)
		})
	}
}

// TestClusterPolicyDevicePluginConfig reads the device plugin ConfigMap name
// and default key, and rejects a policy without them.
func TestClusterPolicyDevicePluginConfig(t *testing.T) {
	name, key, err := clusterPolicyDevicePluginConfig([]byte(
		`{"items":[{"metadata":{"name":"cluster-policy"},"spec":{"devicePlugin":{"config":{"name":"dcgm-exporter-e2e-time-slicing","default":"any"}}}}]}`))
	if err != nil || name != "dcgm-exporter-e2e-time-slicing" || key != "any" {
		t.Fatalf("clusterPolicyDevicePluginConfig() = %q, %q, %v; want the configured name and key", name, key, err)
	}

	_, _, err = clusterPolicyDevicePluginConfig([]byte(`{"items":[{"metadata":{"name":"cluster-policy"},"spec":{"devicePlugin":{}}}]}`))
	requireContractError(t, err, []string{`ClusterPolicy "cluster-policy" spec.devicePlugin.config name = ""`})
}

// TestTimeSlicingGPUReplicas reads nvidia.com/gpu replicas from the device
// plugin config in the format the harness writes.
func TestTimeSlicingGPUReplicas(t *testing.T) {
	config := func(resources string) string {
		return `{"metadata":{"name":"dcgm-exporter-e2e-time-slicing"},"data":{"any":` +
			strconv.Quote("version: v1\nflags:\n  migStrategy: none\nsharing:\n  timeSlicing:\n    renameByDefault: false\n    failRequestsGreaterThanOne: true\n    resources:\n"+resources) + `}}`
	}
	tests := []struct {
		name      string
		configMap string
		key       string
		want      int
		wantErr   []string
	}{
		{name: "configured replicas", configMap: config("    - name: nvidia.com/gpu\n      replicas: 4\n"), key: "any", want: 4},
		{name: "missing key", configMap: config("    - name: nvidia.com/gpu\n      replicas: 4\n"), key: "other", wantErr: []string{`has no key "other"`}},
		{name: "other resource only", configMap: config("    - name: nvidia.com/mig-1g.10gb\n      replicas: 2\n"), key: "any", wantErr: []string{`no sharing.timeSlicing resource "nvidia.com/gpu"`}},
		{name: "zero replicas", configMap: config("    - name: nvidia.com/gpu\n      replicas: 0\n"), key: "any", wantErr: []string{"replicas = 0"}},
		{name: "config is not YAML", configMap: config("    - name: [unclosed\n"), key: "any", wantErr: []string{"decode device plugin config"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := timeSlicingGPUReplicas([]byte(test.configMap), test.key)
			requireContractError(t, err, test.wantErr)
			if got != test.want {
				t.Fatalf("timeSlicingGPUReplicas() = %d, want %d", got, test.want)
			}
		})
	}
}

// TestSharedGPUNodesError checks the replicas label and the shared GPU
// capacity on each GPU node.
func TestSharedGPUNodesError(t *testing.T) {
	node := func(name, count, replicas, capacity string) string {
		return `{"metadata":{"name":"` + name + `","labels":{"nvidia.com/gpu.count":"` + count + `","nvidia.com/gpu.replicas":"` + replicas + `"}},` +
			`"status":{"capacity":{"nvidia.com/gpu":"` + capacity + `"}}}`
	}
	controlPlane := `{"metadata":{"name":"control-plane","labels":{}},"status":{"capacity":{}}}`

	tests := []struct {
		name    string
		nodes   string
		wantErr []string
	}{
		{name: "shared capacity", nodes: `{"items":[` + node("gpu-a", "8", "2", "16") + `,` + controlPlane + `]}`},
		{
			name:    "replicas label differs",
			nodes:   `{"items":[` + node("gpu-a", "8", "1", "16") + `]}`,
			wantErr: []string{`node "gpu-a" label nvidia.com/gpu.replicas = "1", want 2`},
		},
		{
			name:    "capacity not shared",
			nodes:   `{"items":[` + node("gpu-a", "8", "2", "8") + `]}`,
			wantErr: []string{`node "gpu-a" capacity nvidia.com/gpu = 8, want 16`},
		},
		{
			name:    "count label not an integer",
			nodes:   `{"items":[` + node("gpu-a", "eight", "2", "16") + `]}`,
			wantErr: []string{`node "gpu-a" label nvidia.com/gpu.count = "eight", want an integer`},
		},
		{name: "no GPU node", nodes: `{"items":[` + controlPlane + `]}`, wantErr: []string{"no node has the nvidia.com/gpu.count label"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireContractError(t, sharedGPUNodesError([]byte(test.nodes), 2), test.wantErr)
		})
	}
}

// TestDRAResourcesError requires the gpu.nvidia.com DeviceClass and a GPU
// ResourceSlice published for an existing node.
func TestDRAResourcesError(t *testing.T) {
	const (
		classes = `{"items":[{"metadata":{"name":"gpu.nvidia.com"}},{"metadata":{"name":"mig.nvidia.com"}}]}`
		nodes   = `{"items":[{"metadata":{"name":"gpu-a"}}]}`
	)
	slice := func(driver, nodeName, deviceType string) string {
		return `{"items":[{"metadata":{"name":"gpu-a-gpu.nvidia.com-x"},"spec":{"driver":"` + driver + `","nodeName":"` + nodeName + `",` +
			`"pool":{"name":"gpu-a","resourceSliceCount":1},"devices":[{"name":"gpu-0","attributes":{"type":{"string":"` + deviceType + `"}}}]}}]}`
	}

	tests := []struct {
		name    string
		classes string
		slices  string
		wantErr []string
	}{
		{name: "class and node slice", classes: classes, slices: slice("gpu.nvidia.com", "gpu-a", "gpu")},
		{
			name:    "domain-qualified type attribute",
			classes: classes,
			slices: `{"items":[{"metadata":{"name":"s"},"spec":{"driver":"gpu.nvidia.com","nodeName":"gpu-a",` +
				`"devices":[{"name":"gpu-0","attributes":{"gpu.nvidia.com/type":{"string":"gpu"}}}]}}]}`,
		},
		{
			name:    "missing DeviceClass",
			classes: `{"items":[{"metadata":{"name":"mig.nvidia.com"}}]}`,
			slices:  slice("gpu.nvidia.com", "gpu-a", "gpu"),
			wantErr: []string{`no DeviceClass named "gpu.nvidia.com"`},
		},
		{
			name:    "slice for unknown node",
			classes: classes,
			slices:  slice("gpu.nvidia.com", "gone", "gpu"),
			wantErr: []string{"no gpu.nvidia.com ResourceSlice names a cluster node", `node="gone"`},
		},
		{
			name:    "slice without GPU devices",
			classes: classes,
			slices:  slice("gpu.nvidia.com", "gpu-a", "mig"),
			wantErr: []string{"gpus=0"},
		},
		{
			name:    "other driver",
			classes: classes,
			slices:  slice("compute-domain.nvidia.com", "gpu-a", "gpu"),
			wantErr: []string{"no gpu.nvidia.com ResourceSlice"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := draResourcesError([]byte(test.classes), []byte(test.slices), []byte(nodes))
			requireContractError(t, err, test.wantErr)
		})
	}
}

// TestClusterPolicyEmbeddedExporterError checks the exporter config name,
// pod metadata switches and label allowlist on the ClusterPolicy.
func TestClusterPolicyEmbeddedExporterError(t *testing.T) {
	policy := func(exporter string) string {
		return `{"items":[{"metadata":{"name":"cluster-policy"},"spec":{"dcgmExporter":` + exporter + `}}]}`
	}
	tests := []struct {
		name    string
		data    string
		wantErr []string
	}{
		{
			name: "configured",
			data: policy(`{"config":{"name":"dcgm-exporter-e2e-metrics"},"enablePodLabels":true,"enablePodUID":true,"podLabelAllowlistRegex":["^e2e_allowed$"]}`),
		},
		{
			name: "settings missing",
			data: policy(`{"enablePodLabels":false}`),
			wantErr: []string{
				`spec.dcgmExporter.config.name = "", want "dcgm-exporter-e2e-metrics"`,
				"spec.dcgmExporter.enablePodLabels = false, want true",
				"spec.dcgmExporter.enablePodUID = unset, want true",
				`podLabelAllowlistRegex = [], want it to include "^e2e_allowed$"`,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := clusterPolicyEmbeddedExporterError([]byte(test.data), "dcgm-exporter-e2e-metrics", "^e2e_allowed$")
			requireContractError(t, err, test.wantErr)
		})
	}
}

// TestClusterPolicyRemoteDCGMError checks that the ClusterPolicy enables the
// standalone hostengine and names the metrics config.
func TestClusterPolicyRemoteDCGMError(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr []string
	}{
		{
			name: "configured",
			data: `{"items":[{"metadata":{"name":"cluster-policy"},"spec":{"dcgm":{"enabled":true},"dcgmExporter":{"config":{"name":"metrics"}}}}]}`,
		},
		{
			name:    "hostengine disabled",
			data:    `{"items":[{"metadata":{"name":"cluster-policy"},"spec":{"dcgm":{"enabled":false},"dcgmExporter":{"config":{"name":"other"}}}}]}`,
			wantErr: []string{"spec.dcgm.enabled = false, want true", `config.name = "other", want "metrics"`},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireContractError(t, clusterPolicyRemoteDCGMError([]byte(test.data), "metrics"), test.wantErr)
		})
	}
}

// TestGPUClusterReadyError requires a ready GPUCluster with the standalone
// hostengine enabled.
func TestGPUClusterReadyError(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantErr []string
	}{
		{name: "ready", data: `{"items":[{"metadata":{"name":"cluster"},"spec":{"dcgm":{"enabled":true}},"status":{"state":"ready"}}]}`},
		{
			name:    "not ready without hostengine",
			data:    `{"items":[{"metadata":{"name":"cluster"},"spec":{},"status":{"state":"notReady"}}]}`,
			wantErr: []string{`GPUCluster "cluster" spec.dcgm.enabled = unset`, `status.state = "notReady", want "ready"`},
		},
		{name: "none", data: `{"items":[]}`, wantErr: []string{"no GPUCluster found"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireContractError(t, gpuClusterReadyError([]byte(test.data)), test.wantErr)
		})
	}
}

// TestExporterContainerEnvError finds an environment variable on any
// container of the exporter pod and compares its value when one is wanted.
func TestExporterContainerEnvError(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nvidia-dcgm-exporter-abc"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "nvidia-dcgm-exporter",
			Env:  []corev1.EnvVar{{Name: "DCGM_REMOTE_HOSTENGINE_INFO", Value: "nvidia-dcgm-dra:5555"}},
		}}},
	}

	requireContractError(t, exporterContainerEnvError(pod, "DCGM_REMOTE_HOSTENGINE_INFO", ""), nil)
	requireContractError(t, exporterContainerEnvError(pod, "DCGM_REMOTE_HOSTENGINE_INFO", "nvidia-dcgm-dra:5555"), nil)
	requireContractError(t, exporterContainerEnvError(pod, "DCGM_REMOTE_HOSTENGINE_INFO", "nvidia-dcgm:5555"),
		[]string{`nvidia-dcgm-exporter="nvidia-dcgm-dra:5555"`, `want "nvidia-dcgm:5555"`})
	requireContractError(t, exporterContainerEnvError(pod, "DCGM_REMOTE_HOSTENGINE_INFO", "dra:5555"), nil)
	requireContractError(t, exporterContainerEnvError(pod, "KUBERNETES_ENABLE_DRA", ""),
		[]string{"env KUBERNETES_ENABLE_DRA", "want any non-empty value"})

	// A value that only ends with the wanted text, without a "-" boundary, is not a match.
	nearMiss := *pod.DeepCopy()
	nearMiss.Spec.Containers[0].Env[0].Value = "badnvidia-dcgm-dra:5555"
	requireContractError(t, exporterContainerEnvError(nearMiss, "DCGM_REMOTE_HOSTENGINE_INFO", "nvidia-dcgm-dra:5555"),
		[]string{`nvidia-dcgm-exporter="badnvidia-dcgm-dra:5555"`, `want "nvidia-dcgm-dra:5555"`})
}
