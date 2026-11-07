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
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// ProbeCapabilities probes non-mutating cluster feature gates through kubectl.
func ProbeCapabilities(ctx context.Context, runner e2eexec.Runner, cfg Config) ([]capability.Capability, error) {
	allocatable := runner.Run(ctx, kubectl(cfg, "get", "nodes", "-o", "json"))
	rawNodes := runner.Run(ctx, kubectl(cfg, "get", "--raw", "/api/v1/nodes"))
	ipv6 := probeDualStackServices(ctx, runner, cfg)
	ipv6SingleStack := probeIPv6SingleStackServices(ctx, runner, cfg)
	apiResources := runner.Run(ctx, kubectl(cfg, "api-resources"))

	dra := draAPICapability(apiResources)
	allocations := nvidiaAllocationCapabilities(allocatable)
	return append(allocations, []capability.Capability{
		dra,
		draResourceCapability(ctx, runner, cfg, dra),
		clusterCapability("pod_resources", statusIf(rawNodes.ExitCode == 0), "kubectl get --raw /api/v1/nodes", reasonIf(rawNodes.ExitCode == 0, "Kubernetes node API is reachable", "Kubernetes node API is not reachable"), firstNonEmpty(rawNodes.Stdout, rawNodes.Stderr)),
		ipv6,
		ipv6SingleStack,
	}...), nil
}

// nvidiaAllocatableResources records NVIDIA allocation capacity reported by Kubernetes nodes.
// The target probe derives ordinary, MIG, and mixed-MIG scenario gates from these totals.
type nvidiaAllocatableResources struct {
	GPU      int
	MIG      int
	MixedMIG bool
}

// nvidiaNodeList is the small portion of a Kubernetes NodeList needed for allocation planning.
// Keeping this private wire type here makes the JSON boundary easy to test without importing Kubernetes clients.
type nvidiaNodeList struct {
	Items []struct {
		Status struct {
			Allocatable map[string]string `json:"allocatable"`
		} `json:"status"`
	} `json:"items"`
}

// nvidiaAllocationCapabilities reports the allocation mechanisms available on the selected Kubernetes target.
// A failed or malformed node query is unknown rather than a controller-side hardware fallback.
func nvidiaAllocationCapabilities(result e2eexec.Result) []capability.Capability {
	const source = "kubectl get nodes -o json"
	if result.ExitCode != 0 {
		return unknownNVIDIAAllocationCapabilities(source, "Kubernetes node allocatable resource query failed", firstNonEmpty(result.Stdout, result.Stderr))
	}
	resources, ok := parseNVIDIAAllocatableResources(result.Stdout)
	if !ok {
		return unknownNVIDIAAllocationCapabilities(source, "Kubernetes returned invalid node allocatable resource data", firstNonEmpty(result.Stdout, result.Stderr))
	}
	evidence := fmt.Sprintf("allocatable GPU=%d MIG=%d mixed-MIG=%t", resources.GPU, resources.MIG, resources.MixedMIG)
	return []capability.Capability{
		clusterCapability("gpu_resources", statusIf(resources.GPU > 0), source, reasonIf(resources.GPU > 0, "cluster reports allocatable nvidia.com/gpu resources", "cluster does not report allocatable nvidia.com/gpu resources"), evidence),
		clusterCapability("mig_resources", statusIf(resources.MIG > 0), source, reasonIf(resources.MIG > 0, "cluster reports allocatable NVIDIA MIG resources", "cluster does not report allocatable NVIDIA MIG resources"), evidence),
		clusterCapability("mixed_mig_resources", statusIf(resources.MixedMIG), source, reasonIf(resources.MixedMIG, "cluster reports ordinary GPU and MIG resources on one node", "cluster does not report ordinary GPU and MIG resources on one node"), evidence),
	}
}

// unknownNVIDIAAllocationCapabilities preserves one failed target query as explicit unknown evidence for every affected allocation type.
// Scenario planning can then report the target fact that needs preparation instead of using controller hardware.
func unknownNVIDIAAllocationCapabilities(source, reason, evidence string) []capability.Capability {
	return []capability.Capability{
		clusterCapability("gpu_resources", capability.StatusUnknown, source, reason, evidence),
		clusterCapability("mig_resources", capability.StatusUnknown, source, reason, evidence),
		clusterCapability("mixed_mig_resources", capability.StatusUnknown, source, reason, evidence),
	}
}

// parseNVIDIAAllocatableResources reads NVIDIA GPU resource capacity from a Kubernetes NodeList response.
// It recognizes a mixed-MIG target only when one node reports both ordinary and MIG resource names.
func parseNVIDIAAllocatableResources(data []byte) (nvidiaAllocatableResources, bool) {
	var nodes nvidiaNodeList
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nvidiaAllocatableResources{}, false
	}

	var resources nvidiaAllocatableResources
	for _, node := range nodes.Items {
		gpu, mig := nvidiaNodeAllocatableResources(node.Status.Allocatable)
		resources.GPU += gpu
		resources.MIG += mig
		resources.MixedMIG = resources.MixedMIG || (gpu > 0 && mig > 0)
	}
	return resources, true
}

// nvidiaNodeAllocatableResources totals ordinary GPU and MIG resource quantities for one Kubernetes node.
// Invalid quantities are ignored because Kubernetes extended-resource capacity must be an integer.
func nvidiaNodeAllocatableResources(allocatable map[string]string) (int, int) {
	var gpu, mig int
	for name, quantity := range allocatable {
		count, err := strconv.Atoi(quantity)
		if err != nil || count <= 0 {
			continue
		}
		switch {
		case name == "nvidia.com/gpu":
			gpu += count
		case strings.HasPrefix(name, "nvidia.com/mig-"):
			mig += count
		}
	}
	return gpu, mig
}

// draAPICapability reports whether the cluster serves the ResourceSlice API.
func draAPICapability(result e2eexec.Result) capability.Capability {
	evidence := firstNonEmpty(result.Stdout, result.Stderr)
	if result.ExitCode != 0 {
		return clusterCapability(
			"dra",
			capability.StatusUnknown,
			"kubectl api-resources",
			"Kubernetes API discovery failed while checking DRA",
			evidence,
		)
	}
	if !strings.Contains(strings.ToLower(string(result.Stdout)), "resourceslices") {
		return clusterCapability(
			"dra",
			capability.StatusUnsupported,
			"kubectl api-resources",
			"cluster does not serve the DRA ResourceSlice API",
			evidence,
		)
	}
	return clusterCapability(
		"dra",
		capability.StatusSupported,
		"kubectl api-resources",
		"cluster serves the DRA ResourceSlice API",
		evidence,
	)
}

// draResourceCapability reports whether the selected target exposes an NVIDIA full-GPU DRA resource.
// It keeps API discovery separate from real allocatable DRA evidence so external runs do not assume a DRA driver is ready.
func draResourceCapability(ctx context.Context, runner e2eexec.Runner, cfg Config, api capability.Capability) capability.Capability {
	const source = "kubectl get resourceslices -o json"
	if api.Status != capability.StatusSupported {
		return clusterCapability("dra_resources", api.Status, api.Source, api.Reason, api.Evidence)
	}
	result := runner.Run(ctx, kubectl(cfg, "get", "resourceslices", "-o", "json"))
	evidence := firstNonEmpty(result.Stdout, result.Stderr)
	if result.ExitCode != 0 {
		return clusterCapability("dra_resources", capability.StatusUnknown, source, "Kubernetes ResourceSlice query failed while checking NVIDIA DRA resources", evidence)
	}
	if _, ok := parseNVIDIAResourceSlices(result.Stdout); !ok {
		if evidence == "" {
			evidence = "empty ResourceSlice response"
		}
		return clusterCapability("dra_resources", capability.StatusUnknown, source, "Kubernetes ResourceSlice query returned invalid JSON", evidence)
	}
	if !hasNVIDIAFullGPUResourceSlice(result.Stdout) {
		return clusterCapability("dra_resources", capability.StatusUnsupported, source, "cluster does not report NVIDIA full-GPU DRA resources", evidence)
	}
	return clusterCapability("dra_resources", capability.StatusSupported, source, "cluster reports NVIDIA full-GPU DRA resources", evidence)
}

const dualStackServiceProbeManifest = `apiVersion: v1
kind: Service
metadata:
  generateName: dcgm-exporter-ipv6-probe-
  namespace: default
spec:
  ipFamilyPolicy: RequireDualStack
  ports:
    - port: 1
      protocol: TCP
`

const dualStackServiceProbeSource = "kubectl server-side dry-run Service"

const ipv6SingleStackServiceProbeManifest = `apiVersion: v1
kind: Service
metadata:
  generateName: dcgm-exporter-ipv6-single-stack-probe-
  namespace: default
spec:
  ipFamilyPolicy: SingleStack
  ipFamilies:
    - IPv6
  ports:
    - port: 1
      protocol: TCP
`

const ipv6SingleStackServiceProbeSource = "kubectl server-side IPv6 single-stack Service dry-run"

// probeDualStackServices asks the API server to default a RequireDualStack
// Service. Server-side dry-run exercises allocation policy without persisting
// the probe object and works on clusters that predate the ServiceCIDR API.
func probeDualStackServices(ctx context.Context, runner e2eexec.Runner, cfg Config) capability.Capability {
	result := runner.Run(ctx, kubectlWithStdin(
		cfg,
		[]byte(dualStackServiceProbeManifest),
		"create", "--dry-run=server", "-f", "-", "-o", "json",
	))
	if result.ExitCode != 0 {
		return failedDualStackServiceProbe(result)
	}

	families, err := serviceIPFamilies(result.Stdout)
	if err != nil {
		return dualStackServiceCapability(
			capability.StatusUnknown,
			"Kubernetes dual-stack Service probe returned invalid JSON",
			err.Error(),
		)
	}
	evidence := strings.Join(families, " ")
	if !slices.Contains(families, "IPv4") || !slices.Contains(families, "IPv6") {
		return dualStackServiceCapability(
			capability.StatusUnknown,
			"Kubernetes accepted RequireDualStack without returning both IP families",
			evidence,
		)
	}
	return dualStackServiceCapability(
		capability.StatusSupported,
		"cluster allocates dual-stack Services",
		evidence,
	)
}

func failedDualStackServiceProbe(result e2eexec.Result) capability.Capability {
	evidence := firstNonEmpty(result.Stderr, result.Stdout)
	if isSingleStackServiceRejection(evidence) {
		return dualStackServiceCapability(
			capability.StatusUnsupported,
			"cluster rejects dual-stack Services",
			evidence,
		)
	}
	return dualStackServiceCapability(
		capability.StatusUnknown,
		"Kubernetes dual-stack Service probe failed",
		evidence,
	)
}

// probeIPv6SingleStackServices checks whether Kubernetes can allocate the exact
// IPv6-only Service family requested by the remote DCGM hostname scenario.
func probeIPv6SingleStackServices(ctx context.Context, runner e2eexec.Runner, cfg Config) capability.Capability {
	result := runner.Run(ctx, kubectlWithStdin(
		cfg,
		[]byte(ipv6SingleStackServiceProbeManifest),
		"create", "--dry-run=server", "-f", "-", "-o", "json",
	))
	if result.ExitCode != 0 {
		evidence := firstNonEmpty(result.Stderr, result.Stdout)
		if isIPv6SingleStackServiceRejection(evidence) {
			return ipv6SingleStackServiceCapability(
				capability.StatusUnsupported,
				"cluster rejects IPv6 single-stack Services",
				evidence,
			)
		}
		return ipv6SingleStackServiceCapability(
			capability.StatusUnknown,
			"Kubernetes IPv6 single-stack Service probe failed",
			evidence,
		)
	}

	families, err := serviceIPFamilies(result.Stdout)
	if err != nil {
		return ipv6SingleStackServiceCapability(
			capability.StatusUnknown,
			"Kubernetes IPv6 single-stack Service probe returned invalid JSON",
			err.Error(),
		)
	}
	evidence := strings.Join(families, " ")
	if len(families) != 1 || families[0] != "IPv6" {
		return ipv6SingleStackServiceCapability(
			capability.StatusUnknown,
			"Kubernetes accepted an IPv6 single-stack Service without returning only IPv6",
			evidence,
		)
	}
	return ipv6SingleStackServiceCapability(
		capability.StatusSupported,
		"cluster allocates IPv6 single-stack Services",
		evidence,
	)
}

func isIPv6SingleStackServiceRejection(evidence string) bool {
	message := strings.ToLower(evidence)
	return strings.Contains(message, "ipv6") &&
		(strings.Contains(message, "not configured") || strings.Contains(message, "not supported"))
}

func serviceIPFamilies(output []byte) ([]string, error) {
	var service struct {
		Spec struct {
			IPFamilies []string `json:"ipFamilies"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(output, &service); err != nil {
		return nil, err
	}
	return service.Spec.IPFamilies, nil
}

func isSingleStackServiceRejection(evidence string) bool {
	message := strings.ToLower(evidence)
	return strings.Contains(message, "not configured for dual-stack") ||
		(strings.Contains(message, "requiredualstack") &&
			(strings.Contains(message, "single-stack") || strings.Contains(message, "single stack")))
}

func dualStackServiceCapability(status capability.Status, reason, evidence string) capability.Capability {
	return clusterCapability("ipv6", status, dualStackServiceProbeSource, reason, evidence)
}

func ipv6SingleStackServiceCapability(status capability.Status, reason, evidence string) capability.Capability {
	return clusterCapability("ipv6_single_stack", status, ipv6SingleStackServiceProbeSource, reason, evidence)
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
