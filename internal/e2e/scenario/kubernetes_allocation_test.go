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

import (
	"strings"
	"testing"
)

// TestKubernetesAllocationContract verifies the catalog's allocation vocabulary and its planning gates.
// It keeps a new allocation from silently losing target qualification or changing dry-run behavior.
func TestKubernetesAllocationContract(t *testing.T) {
	tests := []struct {
		name       string
		allocation KubernetesAllocation
		capability string
		valid      bool
	}{
		{name: "none", allocation: KubernetesAllocationNone, valid: true},
		{name: "ordinary GPU", allocation: KubernetesAllocationGPU, capability: "cluster:gpu_resources", valid: true},
		{name: "MIG", allocation: KubernetesAllocationMIG, capability: "cluster:mig_resources", valid: true},
		{name: "mixed MIG", allocation: KubernetesAllocationMixedMIG, capability: "cluster:mixed_mig_resources", valid: true},
		{name: "DRA", allocation: KubernetesAllocationDRA, capability: "cluster:dra_resources", valid: true},
		{name: "unknown", allocation: KubernetesAllocation("unknown"), valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.allocation.Capability(); got != test.capability {
				t.Fatalf("Capability() = %q, want %q", got, test.capability)
			}
			if got := test.allocation.Valid(); got != test.valid {
				t.Fatalf("Valid() = %t, want %t", got, test.valid)
			}
		})
	}

	entry := Scenario{Features: []string{"host:controller", "cluster:existing"}, Allocation: KubernetesAllocationGPU}
	if got := entry.RequiredCapabilities(); !sameStrings(got, []string{"host:controller", "cluster:existing", "cluster:gpu_resources"}) {
		t.Fatalf("RequiredCapabilities() = %#v, want feature and target allocation", got)
	}
	if got := entry.RequiredCapabilitiesFor(false); !sameStrings(got, []string{"cluster:gpu_resources", "cluster:existing", "host:controller"}) {
		t.Fatalf("RequiredCapabilitiesFor(false) = %#v, want target capabilities before controller gates", got)
	}
	if got := entry.RequiredCapabilitiesFor(true); !sameStrings(got, []string{"host:controller", "cluster:existing"}) {
		t.Fatalf("RequiredCapabilitiesFor(true) = %#v, want feature gates only", got)
	}
}

// TestGPUOperatorMigUsesMIGAllocation keeps the scenario in host MIG preconfiguration
// and in the MIG phase of a split run, which both select by MIG allocation.
func TestGPUOperatorMigUsesMIGAllocation(t *testing.T) {
	entry, ok := Find(Catalog, SuiteK8s, "gpuOperatorMig")
	if !ok {
		t.Fatal("gpuOperatorMig scenario missing")
	}
	if !entry.UsesKubernetesAllocation(KubernetesAllocationMIG, KubernetesAllocationMixedMIG) {
		t.Fatalf("gpuOperatorMig allocation = %q, want MIG", entry.Allocation)
	}
}

// TestGPUOperatorSharedGPUUsesGPUAllocation skips shared-GPU operator checks on
// targets without an ordinary GPU allocation instead of failing after setup.
func TestGPUOperatorSharedGPUUsesGPUAllocation(t *testing.T) {
	entry, ok := Find(Catalog, SuiteK8s, "gpuOperatorSharedGpu")
	if !ok {
		t.Fatal("gpuOperatorSharedGpu scenario missing")
	}
	if entry.Allocation != KubernetesAllocationGPU {
		t.Fatalf("gpuOperatorSharedGpu allocation = %q, want GPU", entry.Allocation)
	}
}

// TestValidateCatalogRejectsInvalidKubernetesAllocation verifies allocation metadata cannot escape its suite boundary.
// Catalog startup uses this check to reject misspelled allocations and allocations on non-Kubernetes scenarios.
func TestValidateCatalogRejectsInvalidKubernetesAllocation(t *testing.T) {
	tests := []struct {
		name  string
		entry Scenario
		want  string
	}{
		{
			name:  "unknown allocation",
			entry: Scenario{Suite: SuiteK8s, Name: "unknown", ResultName: "unknown", Group: "embedded-baseline", Allocation: KubernetesAllocation("unknown")},
			want:  "uses unknown Kubernetes allocation",
		},
		{
			name:  "allocation outside Kubernetes suite",
			entry: Scenario{Suite: SuiteHost, Name: "host", ResultName: "host", Group: GroupHostBaseline, Allocation: KubernetesAllocationGPU},
			want:  "declares a Kubernetes allocation outside the Kubernetes suite",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCatalog([]Scenario{test.entry})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateCatalog() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestGPUOperatorMainDRARequiresDRAAPI keeps the served DRA API as the preflight requirement because this scenario installs its DRA resources.
func TestGPUOperatorMainDRARequiresDRAAPI(t *testing.T) {
	for _, entry := range Catalog {
		if entry.Name != "gpuOperatorMainDRA" {
			continue
		}
		if entry.Allocation != KubernetesAllocationNone {
			t.Fatalf("allocation = %q, want no pre-existing allocation gate", entry.Allocation)
		}
		if !sameStrings(entry.RequiredCapabilities(), []string{"cluster:dra"}) {
			t.Fatalf("RequiredCapabilities() = %#v, want DRA API only", entry.RequiredCapabilities())
		}
		return
	}
	t.Fatal("gpuOperatorMainDRA scenario missing")
}
