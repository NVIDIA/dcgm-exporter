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

package cluster

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// ProbeCapabilities probes cluster feature gates through kubectl.
func ProbeCapabilities(ctx context.Context, runner e2eexec.Runner, cfg Config, featureCfgs ...FeatureConfig) ([]capability.Capability, error) {
	featureCfg := FeatureConfig{}
	if len(featureCfgs) != 0 {
		featureCfg = featureCfgs[0]
	}
	gpuOutput := text(ctx, runner, kubectl(cfg, "get", "nodes", "-o", "jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}{\"\\n\"}{end}"))
	gpuCount := sumInts(gpuOutput)
	nodesJSON := text(ctx, runner, kubectl(cfg, "get", "nodes", "-o", "json"))
	rawNodes := runner.Run(ctx, kubectl(cfg, "get", "--raw", "/api/v1/nodes"))
	serviceFamilies := text(ctx, runner, kubectl(cfg, "get", "svc", "kubernetes", "-n", "default", "-o", "jsonpath={.spec.ipFamilies[*]}"))
	clusterPolicy := runner.Run(ctx, kubectl(cfg, "get", "clusterpolicy", "-A"))
	apiResources := text(ctx, runner, kubectl(cfg, "api-resources"))
	resourceSlices := text(ctx, runner, kubectl(cfg, "get", "resourceslices", "-A", "-o", "name"))

	gpuStatus := statusIf(gpuCount > 0)
	draStatus := capability.StatusUnsupported
	draReason := "cluster does not expose DRA ResourceSlice API"
	draEvidence := ""
	if strings.Contains(strings.ToLower(apiResources), "resourceslices") {
		if strings.Contains(strings.ToLower(resourceSlices), "resourceslice") {
			draStatus = capability.StatusSupported
			draReason = "cluster exposes DRA ResourceSlice objects"
			draEvidence = firstLine(resourceSlices)
		} else {
			draStatus = capability.StatusUnknown
			draReason = "DRA API exists but no ResourceSlices were found"
		}
	}
	if draStatus != capability.StatusSupported && draConfigurationEnabled(featureCfg, cfg) {
		version := strings.TrimSpace(featureCfg.Dependencies.DRADriverVersion)
		if version == "" {
			return nil, fmt.Errorf("NVIDIA_DRA_DRIVER_VERSION is required")
		}
		draStatus = capability.StatusSupported
		draReason = "validation can install NVIDIA DRA driver " + version
	}
	gpuOperatorStatus := statusIf(clusterPolicy.ExitCode == 0)
	gpuOperatorReason := reasonIf(clusterPolicy.ExitCode == 0, "GPU Operator ClusterPolicy is present", "GPU Operator ClusterPolicy is not present")
	if clusterPolicy.ExitCode != 0 && gpuOperatorInstallEnabled(featureCfg, cfg) {
		version := strings.TrimSpace(featureCfg.Dependencies.GPUOperatorVersion)
		if version == "" {
			return nil, fmt.Errorf("GPU_OPERATOR_VERSION is required")
		}
		gpuOperatorStatus = capability.StatusSupported
		gpuOperatorReason = "validation can install GPU Operator " + version
	}
	return []capability.Capability{
		clusterCapability("gpu_resources", gpuStatus, "kubectl nodes allocatable", reasonIf(gpuCount > 0, "cluster reports allocatable nvidia.com/gpu resources", "cluster does not report allocatable nvidia.com/gpu resources"), strings.TrimSpace(gpuOutput)),
		clusterCapability("standalone_dcgm_resources", gpuStatus, "kubectl nodes allocatable", reasonIf(gpuCount > 0, "cluster can schedule standalone DCGM GPU pods", "standalone DCGM requires allocatable GPU resources"), strings.TrimSpace(gpuOutput)),
		clusterCapability("mig_resources", statusIf(hasMIGResources(nodesJSON)), "kubectl nodes json", reasonIf(hasMIGResources(nodesJSON), "cluster reports allocatable MIG resources", "cluster does not report allocatable MIG resources"), ""),
		clusterCapability("dra", draStatus, "kubectl api-resources/get resourceslices", draReason, draEvidence),
		sharedGPUCapability(gpuCount, nodesJSON, featureCfg, cfg),
		clusterCapability("pod_resources", statusIf(rawNodes.ExitCode == 0), "kubectl get --raw /api/v1/nodes", reasonIf(rawNodes.ExitCode == 0, "Kubernetes node API is reachable", "Kubernetes node API is not reachable"), firstNonEmpty(rawNodes.Stdout, rawNodes.Stderr)),
		clusterCapability("ipv6", statusIf(strings.Contains(serviceFamilies, "IPv6")), "kubectl service ipFamilies", reasonIf(strings.Contains(serviceFamilies, "IPv6"), "cluster allocates IPv6 service addresses", "cluster does not advertise IPv6 service addresses"), strings.TrimSpace(serviceFamilies)),
		clusterCapability("gpu_operator", gpuOperatorStatus, "kubectl get clusterpolicy", gpuOperatorReason, firstNonEmpty(clusterPolicy.Stdout, clusterPolicy.Stderr)),
	}, nil
}

// clusterCapability prefixes a cluster-scoped capability name and trims probe evidence.
func clusterCapability(name string, status capability.Status, source, reason, evidence string) capability.Capability {
	return capability.Capability{Name: "cluster:" + name, Status: status, Source: source, Reason: reason, Evidence: strings.TrimSpace(evidence)}
}

// statusIf maps a boolean probe result to supported or unsupported.
func statusIf(ok bool) capability.Status {
	if ok {
		return capability.StatusSupported
	}
	return capability.StatusUnsupported
}

// reasonIf chooses the supported or unsupported reason text.
func reasonIf(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

// hasMIGResources reports whether node allocatable resources include MIG resource names.
func hasMIGResources(nodesJSON string) bool {
	return strings.Contains(nodesJSON, "nvidia.com/mig-")
}

// hasSharedGPUResources reports whether nodes expose time-sliced or virtual GPU evidence.
func hasSharedGPUResources(nodesJSON string) bool {
	lower := strings.ToLower(nodesJSON)
	return strings.Contains(lower, "nvidia.com/gpu.shared") ||
		strings.Contains(lower, "nvidia.com/vgpu") ||
		strings.Contains(lower, "replicas")
}

// sharedGPUCapability reports existing or configurable shared-GPU support.
func sharedGPUCapability(gpuCount int, nodesJSON string, featureCfg FeatureConfig, cfg Config) capability.Capability {
	if hasSharedGPUResources(nodesJSON) {
		return clusterCapability("shared_gpu", capability.StatusSupported, "kubectl nodes json", "cluster reports shared GPU resource evidence", "")
	}
	if gpuCount > 0 && sharedGPUConfigurationEnabled(featureCfg, cfg) {
		replicas := featureCfg.SharedGPUReplicas
		if replicas == "" {
			replicas = "2"
		}
		return clusterCapability("shared_gpu", capability.StatusSupported, "validation shared GPU setup", "validation can configure NVIDIA device-plugin time-slicing", "nvidia.com/gpu replicas="+replicas)
	}
	return clusterCapability("shared_gpu", capability.StatusUnsupported, "kubectl nodes json", "cluster does not report shared GPU resources", "")
}

// sumInts totals integer fields from kubectl jsonpath output.
func sumInts(text string) int {
	total := 0
	for _, field := range strings.Fields(text) {
		value, err := strconv.Atoi(field)
		if err == nil {
			total += value
		}
	}
	return total
}

// firstNonEmpty returns the first non-empty line across command output buffers.
func firstNonEmpty(values ...[]byte) string {
	for _, value := range values {
		if line := firstLine(string(value)); line != "" {
			return line
		}
	}
	return ""
}

// firstLine returns the first non-empty output line for compact evidence.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// sharedGPUConfigurationEnabled reports whether validation may configure shared GPU resources.
func sharedGPUConfigurationEnabled(featureCfg FeatureConfig, cfg Config) bool {
	switch strings.ToLower(strings.TrimSpace(featureCfg.SharedGPUConfigure)) {
	case "true":
		return true
	case "auto", "":
		return cfg.LocalK3D()
	default:
		return false
	}
}
