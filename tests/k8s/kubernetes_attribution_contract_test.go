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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const regularDRAClaimRequestName = "gpu"

// regularDRAAllocation is the identity Kubernetes assigned to one regular GPU claim.
// The live E2E test copies these values into the expected exporter labels.
type regularDRAAllocation struct {
	ClaimName      string
	ClaimNamespace string
	Driver         string
	Pool           string
	Device         string
}

// regularDRAMetricExpectation combines workload identity with its live DRA allocation.
// A single exported metric sample must contain every value in this structure.
type regularDRAMetricExpectation struct {
	Pod       string
	Namespace string
	Container string
	regularDRAAllocation
}

// renderRegularDRAWorkloadManifestForIdentity renders a claim and Pod with the supplied identity and node selector.
func renderRegularDRAWorkloadManifestForIdentity(namespace, image string, nodeSelector map[string]string, runID, podName, containerName string) string {
	var scheduling strings.Builder
	if len(nodeSelector) > 0 {
		scheduling.WriteString("  nodeSelector:\n")
		keys := make([]string, 0, len(nodeSelector))
		for key := range nodeSelector {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&scheduling, "    %s: %s\n", strconv.Quote(key), strconv.Quote(nodeSelector[key]))
		}
	}

	return fmt.Sprintf(`
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: gpu-claim-template
  namespace: %s
spec:
  spec:
    devices:
      requests:
      - name: %s
        exactly:
          deviceClassName: gpu.nvidia.com
---
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    e2eRunID: %s
spec:
  restartPolicy: Never
%s  resourceClaims:
  - name: %s
    resourceClaimTemplateName: gpu-claim-template
  containers:
  - name: %s
    image: %s
    command: ["sh", "-c", "nvidia-smi && sleep 120"]
    resources:
      claims:
      - name: %s
`,
		strconv.Quote(namespace),
		regularDRAClaimRequestName,
		podName,
		strconv.Quote(namespace),
		podName,
		runID,
		scheduling.String(),
		regularDRAClaimRequestName,
		containerName,
		strconv.Quote(image),
		regularDRAClaimRequestName,
	)
}

// TestRenderRegularDRAWorkloadManifest checks the rendered claim and optional Pod node selector.
func TestRenderRegularDRAWorkloadManifest(t *testing.T) {
	tests := []struct {
		name         string
		nodeSelector map[string]string
		wantSelector map[string]string
	}{
		{name: "without node selector"},
		{
			name: "with node selector",
			nodeSelector: map[string]string{
				"node-role.kubernetes.io/gpu": "true",
			},
			wantSelector: map[string]string{
				"node-role.kubernetes.io/gpu": "true",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := renderRegularDRAWorkloadManifestForIdentity("e2e", "cuda:latest", test.nodeSelector, "run-id", "cuda-workload", "cuda-workload")
			pod := regularDRAWorkloadPod(t, manifest)
			if !reflect.DeepEqual(pod.Spec.NodeSelector, test.wantSelector) {
				t.Fatalf("node selector = %#v, want %#v", pod.Spec.NodeSelector, test.wantSelector)
			}
			requireRegularDRAWorkload(t, pod)
		})
	}
}

// regularDRAWorkloadPod decodes the workload Pod, the second document of the rendered manifest.
func regularDRAWorkloadPod(t *testing.T, manifest string) corev1.Pod {
	t.Helper()
	documents := strings.Split(manifest, "---")
	if len(documents) != 2 {
		t.Fatalf("manifest has %d documents, want 2", len(documents))
	}
	var pod corev1.Pod
	if err := yaml.Unmarshal([]byte(documents[1]), &pod); err != nil {
		t.Fatalf("unmarshal workload pod: %v", err)
	}
	return pod
}

// requireRegularDRAWorkload checks the workload identity, its one CUDA container, and its template-backed GPU claim.
func requireRegularDRAWorkload(t *testing.T, pod corev1.Pod) {
	t.Helper()
	if pod.Name != "cuda-workload" || pod.Labels["e2eRunID"] != "run-id" {
		t.Fatalf("workload identity = name %q labels %#v", pod.Name, pod.Labels)
	}
	if len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != "cuda-workload" || pod.Spec.Containers[0].Image != "cuda:latest" {
		t.Fatalf("workload container = %#v", pod.Spec.Containers)
	}
	if len(pod.Spec.ResourceClaims) != 1 {
		t.Fatalf("resource claims = %#v, want one claim", pod.Spec.ResourceClaims)
	}
	claim := pod.Spec.ResourceClaims[0]
	if claim.Name != regularDRAClaimRequestName || claim.ResourceClaimTemplateName == nil ||
		*claim.ResourceClaimTemplateName != "gpu-claim-template" {
		t.Fatalf("resource claim = %#v, want template-backed gpu claim", claim)
	}
}

// regularDRAWorkloadClaimAndNode extracts the assigned node and sole resolved DRA claim.
func regularDRAWorkloadClaimAndNode(output []byte) (string, string, error) {
	var pod struct {
		Spec struct {
			NodeName string `json:"nodeName"`
		} `json:"spec"`
		Status struct {
			ResourceClaimStatuses []struct {
				Name              string `json:"name"`
				ResourceClaimName string `json:"resourceClaimName"`
			} `json:"resourceClaimStatuses"`
		} `json:"status"`
	}
	if err := json.Unmarshal(output, &pod); err != nil {
		return "", "", fmt.Errorf("parse regular DRA workload pod: %w", err)
	}
	if strings.TrimSpace(pod.Spec.NodeName) == "" {
		return "", "", errors.New("regular DRA workload pod is not assigned to a node")
	}

	var claimNames []string
	for _, status := range pod.Status.ResourceClaimStatuses {
		if status.Name == regularDRAClaimRequestName {
			claimNames = append(claimNames, status.ResourceClaimName)
		}
	}
	if len(claimNames) != 1 || strings.TrimSpace(claimNames[0]) == "" {
		return "", "", fmt.Errorf("regular DRA workload must have one resolved %q claim", regularDRAClaimRequestName)
	}
	return claimNames[0], pod.Spec.NodeName, nil
}

// regularDRAAllocationFromClaim reads the one device allocation that the test expects Kubernetes to assign.
func regularDRAAllocationFromClaim(output []byte, namespace, claimName string) (regularDRAAllocation, error) {
	var claim resourcev1.ResourceClaim
	if err := json.Unmarshal(output, &claim); err != nil {
		return regularDRAAllocation{}, fmt.Errorf("parse regular DRA ResourceClaim: %w", err)
	}
	if claim.Status.Allocation == nil {
		return regularDRAAllocation{}, errors.New("regular DRA ResourceClaim has no allocation")
	}
	results := claim.Status.Allocation.Devices.Results
	if len(results) != 1 {
		return regularDRAAllocation{}, fmt.Errorf("regular DRA ResourceClaim has %d allocation results, want 1", len(results))
	}
	result := results[0]
	if result.Request != regularDRAClaimRequestName {
		return regularDRAAllocation{}, fmt.Errorf("regular DRA allocation request = %q, want %q", result.Request, regularDRAClaimRequestName)
	}
	for name, value := range map[string]string{
		"driver": result.Driver,
		"pool":   result.Pool,
		"device": result.Device,
	} {
		if strings.TrimSpace(value) == "" {
			return regularDRAAllocation{}, fmt.Errorf("regular DRA allocation is missing %s", name)
		}
	}
	return regularDRAAllocation{
		ClaimName:      claimName,
		ClaimNamespace: namespace,
		Driver:         result.Driver,
		Pool:           result.Pool,
		Device:         result.Device,
	}, nil
}

// regularDRAAttributionContract requires complete DRA identity without mixed virtual-GPU labels.
func regularDRAAttributionContract(metrics []byte, want regularDRAMetricExpectation) error {
	if err := validateRegularDRAExpectation(want); err != nil {
		return err
	}
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return fmt.Errorf("parse exporter metrics: %w", err)
	}
	expected := map[string]string{
		"pod":                 want.Pod,
		"namespace":           want.Namespace,
		"container":           want.Container,
		"dra_claim_name":      want.ClaimName,
		"dra_claim_namespace": want.ClaimNamespace,
		"dra_driver_name":     want.Driver,
		"dra_pool_name":       want.Pool,
		"dra_device_name":     want.Device,
	}
	matched := false
	for familyName, family := range families {
		if !strings.HasPrefix(familyName, "DCGM_FI_") {
			continue
		}
		familyMatched, err := regularDRAFamilyMatchesExpectation(family, expected, want)
		if err != nil {
			return err
		}
		if familyMatched {
			matched = true
		}
	}
	if matched {
		return nil
	}
	return fmt.Errorf("no DCGM sample matches regular DRA allocation %s/%s on %s", want.ClaimNamespace, want.ClaimName, want.Pod)
}

// regularDRAFamilyMatchesExpectation rejects virtual-GPU identity on matching regular-GPU samples.
func regularDRAFamilyMatchesExpectation(
	family *dto.MetricFamily,
	expected map[string]string,
	want regularDRAMetricExpectation,
) (bool, error) {
	matched := false
	for _, metric := range family.GetMetric() {
		labels := regularDRAMetricLabels(metric.GetLabel())
		if labels["gpu"] == "" || !labelsContainExpectedValues(labels, expected) {
			continue
		}
		if _, exists := labels["vgpu"]; exists {
			return false, fmt.Errorf("DRA sample for %s/%s also carries virtual-GPU identity", want.ClaimNamespace, want.ClaimName)
		}
		matched = true
	}
	return matched, nil
}

// labelsContainExpectedValues reports whether actual contains every expected key and value.
func labelsContainExpectedValues(labels, expected map[string]string) bool {
	for name, value := range expected {
		if labels[name] != value {
			return false
		}
	}
	return true
}

// validateRegularDRAExpectation rejects expectations missing fields needed to check DRA attribution.
func validateRegularDRAExpectation(want regularDRAMetricExpectation) error {
	for name, value := range map[string]string{
		"pod":             want.Pod,
		"namespace":       want.Namespace,
		"container":       want.Container,
		"claim name":      want.ClaimName,
		"claim namespace": want.ClaimNamespace,
		"driver":          want.Driver,
		"pool":            want.Pool,
		"device":          want.Device,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("regular DRA metric expectation is missing %s", name)
		}
	}
	return nil
}

// regularDRAMetricLabels maps a Prometheus sample's labels for DRA contract checks.
func regularDRAMetricLabels(labels []*dto.LabelPair) map[string]string {
	values := make(map[string]string, len(labels))
	for _, label := range labels {
		values[label.GetName()] = label.GetValue()
	}
	return values
}
