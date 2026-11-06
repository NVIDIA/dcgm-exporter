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

package scenario

import "strings"

// KubernetesAllocation names the Kubernetes resource a scenario needs to schedule its workload.
// Catalog entries use it to make target prerequisites explicit without coupling the CLI to scenario groups.
type KubernetesAllocation string

const (
	KubernetesAllocationNone     KubernetesAllocation = ""
	KubernetesAllocationGPU      KubernetesAllocation = "gpu"
	KubernetesAllocationMIG      KubernetesAllocation = "mig"
	KubernetesAllocationMixedMIG KubernetesAllocation = "mixed-mig"
	KubernetesAllocationDRA      KubernetesAllocation = "dra"
)

// Capability returns the target capability that proves this allocation is ready.
// Callers include the returned capability in ordinary scenario selection alongside declared feature gates.
func (a KubernetesAllocation) Capability() string {
	switch a {
	case KubernetesAllocationGPU:
		return "cluster:gpu_resources"
	case KubernetesAllocationMIG:
		return "cluster:mig_resources"
	case KubernetesAllocationMixedMIG:
		return "cluster:mixed_mig_resources"
	case KubernetesAllocationDRA:
		return "cluster:dra_resources"
	default:
		return ""
	}
}

// Valid reports whether a is a known Kubernetes allocation choice.
// Catalog validation uses it to reject misspelled or non-Kubernetes allocation metadata before a run starts.
func (a KubernetesAllocation) Valid() bool {
	switch a {
	case KubernetesAllocationNone, KubernetesAllocationGPU, KubernetesAllocationMIG,
		KubernetesAllocationMixedMIG, KubernetesAllocationDRA:
		return true
	default:
		return false
	}
}

// RequiredCapabilities returns the scenario's feature gates plus its Kubernetes allocation capability.
// Catalog display calls it instead of reading Features directly so the catalog owns both kinds of prerequisite.
func (s Scenario) RequiredCapabilities() []string {
	capabilities := append([]string{}, s.Features...)
	if allocation := s.Allocation.Capability(); allocation != "" {
		capabilities = append(capabilities, allocation)
	}
	return capabilities
}

// RequiredCapabilitiesFor returns the gates that can be evaluated for one planning mode.
// Normal runs check target allocation and feature capabilities first so an unknown controller capability cannot hide missing target evidence.
// Dry runs retain their current behavior of showing host-based candidates without claiming an unprobed Kubernetes target has resources.
func (s Scenario) RequiredCapabilitiesFor(dryRun bool) []string {
	features := append([]string{}, s.Features...)
	if dryRun {
		return features
	}
	target, controller := splitTargetCapabilities(features)
	if allocation := s.Allocation.Capability(); allocation != "" {
		target = append([]string{allocation}, target...)
	}
	return append(target, controller...)
}

// splitTargetCapabilities orders cluster facts before controller facts for normal scenario planning.
// The capability namespace identifies the evidence source; it does not use controller hardware as target evidence.
func splitTargetCapabilities(capabilities []string) (target, controller []string) {
	for _, capability := range capabilities {
		if strings.HasPrefix(capability, "cluster:") {
			target = append(target, capability)
			continue
		}
		controller = append(controller, capability)
	}
	return target, controller
}

// UsesKubernetesAllocation reports whether the scenario needs any supplied target allocation.
// Local setup uses this to split MIG mutation from ordinary GPU work without relying on feature-gate spelling.
func (s Scenario) UsesKubernetesAllocation(allocations ...KubernetesAllocation) bool {
	for _, allocation := range allocations {
		if s.Allocation == allocation {
			return true
		}
	}
	return false
}
