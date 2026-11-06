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
	"slices"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// TestRequirementsForGroupsSelectedScenarioReasons verifies duplicate requirements keep each user in order.
func TestRequirementsForGroupsSelectedScenarioReasons(t *testing.T) {
	selected := []Scenario{
		{Suite: SuiteStatic, Name: "first", Requirements: []Requirement{{Kind: RequirementContainerImage, Image: ContainerImageDCGM}}},
		{Suite: SuiteStatic, Name: "second", Requirements: []Requirement{{Kind: RequirementContainerImage, Image: ContainerImageDCGM}}},
	}

	got := RequirementsFor(selected, RunConditions{})
	want := []SelectedRequirement{
		{Requirement: Requirement{Kind: RequirementDocker}, RequiredBy: []string{"container image check"}},
		{Requirement: Requirement{Kind: RequirementContainerImage, Image: ContainerImageDCGM}, RequiredBy: []string{"static/first", "static/second"}},
	}
	if !sameSelectedRequirements(got, want) {
		t.Fatalf("RequirementsFor() = %#v, want %#v", got, want)
	}
}

// TestRequirementsForAddsRunRequirements verifies local clusters, registry login, and injection use explicit run reasons.
func TestRequirementsForAddsRunRequirements(t *testing.T) {
	selected := []Scenario{{Suite: SuiteK8s, Name: "default"}, {Suite: SuiteHost, Name: "host"}}

	got := RequirementsFor(selected, RunConditions{
		LocalKubernetes:         true,
		RegistryLoginConfigured: true,
		NVMLInjectionConfigured: true,
	})
	want := []SelectedRequirement{
		{Requirement: Requirement{Kind: RequirementKubernetesTools}, RequiredBy: []string{"k8s/default"}},
		{Requirement: Requirement{Kind: RequirementKubernetesAPI}, RequiredBy: []string{"k8s/default"}},
		{Requirement: Requirement{Kind: RequirementDocker}, RequiredBy: []string{"container image check", "Docker Buildx", "GPU runtime", "container toolkit"}},
		{Requirement: Requirement{Kind: RequirementContainerImage, Image: ContainerImageExporter}, RequiredBy: []string{"k8s/default"}},
		{Requirement: Requirement{Kind: RequirementHostDCGM}, RequiredBy: []string{"host/host"}},
		{Requirement: Requirement{Kind: RequirementBuildx}, RequiredBy: []string{"local Kubernetes"}},
		{Requirement: Requirement{Kind: RequirementContainerToolkit}, RequiredBy: []string{"GPU runtime"}},
		{Requirement: Requirement{Kind: RequirementGPURuntime}, RequiredBy: []string{"local Kubernetes"}},
		{Requirement: Requirement{Kind: RequirementDockerCLI}, RequiredBy: []string{"registry login"}},
		{Requirement: Requirement{Kind: RequirementDCGMDevelopment}, RequiredBy: []string{"NVML injection"}},
	}
	if !sameSelectedRequirements(got, want) {
		t.Fatalf("RequirementsFor() = %#v, want %#v", got, want)
	}
}

// TestRequirementsForSkipsHostVSOCKForMockedDCGM verifies mocked host DCGM keeps the existing VSOCK exception.
func TestRequirementsForSkipsHostVSOCKForMockedDCGM(t *testing.T) {
	selected := []Scenario{{Suite: SuiteHost, Name: "dcgm", Requirements: []Requirement{{Kind: RequirementVSOCK}}}}

	got := RequirementsFor(selected, RunConditions{HostDCGMIsMocked: true})
	if HasRequirement(got, Requirement{Kind: RequirementVSOCK}) {
		t.Fatalf("RequirementsFor() = %#v, want no VSOCK requirement", got)
	}
	if !HasRequirement(got, Requirement{Kind: RequirementHostDCGM}) {
		t.Fatalf("RequirementsFor() = %#v, want host DCGM requirement", got)
	}
}

// TestRequirementsForAddsDCGMImageForCapabilityProbe verifies a probe input adds the named DCGM image once.
func TestRequirementsForAddsDCGMImageForCapabilityProbe(t *testing.T) {
	selected := []Scenario{
		{Suite: SuiteK8s, Name: "first", CapabilityProbeInputs: []CapabilityProbeInput{CapabilityProbeInputDCGMImage}},
		{Suite: SuiteK8s, Name: "second", CapabilityProbeInputs: []CapabilityProbeInput{CapabilityProbeInputDCGMImage}},
	}

	got := RequirementsFor(selected, RunConditions{LocalKubernetes: true})
	for _, requirement := range got {
		if requirement.Requirement != (Requirement{Kind: RequirementContainerImage, Image: ContainerImageDCGM}) {
			continue
		}
		if !sameStrings(requirement.RequiredBy, []string{"k8s/first", "k8s/second"}) {
			t.Fatalf("DCGM image requirement = %#v", requirement)
		}
		return
	}
	t.Fatalf("RequirementsFor() = %#v, want DCGM capability image", got)
}

// TestRequirementsForKeepsExternalKubernetesImagesOffTheController verifies target-pulled images do not imply Docker.
// The image settings remain requirements so the CLI can reject missing image references before it mutates the target.
func TestRequirementsForKeepsExternalKubernetesImagesOffTheController(t *testing.T) {
	selected := []Scenario{{Suite: SuiteK8s, Name: "default"}}
	requirements := RequirementsFor(selected, RunConditions{RegistryLoginConfigured: true})
	if HasRequirement(requirements, Requirement{Kind: RequirementDocker}) {
		t.Fatalf("RequirementsFor() = %#v, want no controller Docker requirement", requirements)
	}
	if !HasRequirement(requirements, Requirement{Kind: RequirementDockerCLI}) {
		t.Fatalf("RequirementsFor() = %#v, want Docker client for registry login", requirements)
	}
	if !HasRequirement(requirements, Requirement{Kind: RequirementKubernetesImage, Image: ContainerImageExporter}) {
		t.Fatalf("RequirementsFor() = %#v, want target exporter image", requirements)
	}
	probeScenario := []Scenario{{
		Suite:                 SuiteK8s,
		Name:                  "MIG selection",
		CapabilityProbeInputs: []CapabilityProbeInput{CapabilityProbeInputDCGMImage},
	}}
	if HasImageRequirement(RequirementsFor(probeScenario, RunConditions{}), ContainerImageDCGM) {
		t.Fatal("external Kubernetes capability probe unexpectedly requested a controller DCGM image")
	}
}

// TestContainerGPUScenariosRequireGPURuntime verifies only scenarios that start GPU containers request the GPU runtime check.
func TestContainerGPUScenariosRequireGPURuntime(t *testing.T) {
	want := map[string]bool{
		"imageStartup":           true,
		"dcgmLibraryFailure":     false,
		"configuration":          true,
		"logFormat":              true,
		"pprofRequiresWebConfig": true,
		"invalidDeviceSelectors": true,
		"remoteDcgmUri":          true,
	}
	for _, entry := range Catalog {
		if entry.Suite != SuiteContainer {
			continue
		}
		got := HasRequirement(
			RequirementsFor([]Scenario{entry}, RunConditions{}),
			Requirement{Kind: RequirementGPURuntime},
		)
		if got != want[entry.Name] {
			t.Fatalf("%s GPU runtime requirement = %t, want %t", entry.Selector(), got, want[entry.Name])
		}
	}
}

// TestCatalogScenarioImageRequirements verifies every image requirement belongs to the scenarios that use it.
func TestCatalogScenarioImageRequirements(t *testing.T) {
	want := map[ContainerImage][]string{
		ContainerImageDCGM: {
			"host/gpuLoadUtilization",
			"container/remoteDcgmUri",
			"k8s/watchRetention",
			"k8s/remoteDcgm",
			"k8s/remoteDcgmRestart",
			"k8s/remoteDcgmIPv6Hostname",
		},
		ContainerImageBusybox: {
			"k8s/metricsNetworkPolicy",
			"k8s/debugDump",
			"k8s/gpuOperatorIPv6",
			"k8s/gpuOperatorMainEmbedded",
		},
		ContainerImageGPUOperatorDCGM: {
			"k8s/gpuOperatorMainRemote",
			"k8s/gpuOperatorMainDRA",
		},
		ContainerImageCUDAWorkload: {
			"k8s/default",
			"k8s/configMatrix",
			"k8s/configMapData",
			"k8s/oldNamespace",
			"k8s/clockEventsCounters",
			"k8s/serviceAccess",
			"k8s/podLabels",
			"k8s/podUID",
			"k8s/podLabelAllowlist",
			"k8s/kubernetesGpuId",
			"k8s/noHostname",
			"k8s/modelName",
			"k8s/debugDump",
			"k8s/hpcJobMapping",
			"k8s/customResourceNames",
			"k8s/profiling",
			"k8s/p2pStatus",
			"k8s/fieldUnsupported",
			"k8s/nvlinkFabric",
			"k8s/nvlink",
			"k8s/nvlinkIntegrity",
			"k8s/mig",
			"k8s/migFullGpuSelection",
			"k8s/migInstanceSelection",
			"k8s/migSpecificInstanceSelection",
			"k8s/migCombinedDeviceSelection",
			"k8s/migPodAttribution",
			"k8s/dra",
			"k8s/sharedGpu",
			"k8s/nvswitch",
			"k8s/nvswitchLink",
			"k8s/nvswitchThroughput",
			"k8s/graceCpu",
			"k8s/c2c",
			"k8s/gpuOperatorMainEmbedded",
			"k8s/gpuOperatorMainDRA",
		},
	}
	got := map[ContainerImage][]string{}
	for _, entry := range Catalog {
		for _, requirement := range entry.Requirements {
			if requirement.Kind == RequirementContainerImage {
				got[requirement.Image] = append(got[requirement.Image], entry.Selector())
			}
		}
		if entry.Suite != SuiteContainer && entry.Suite != SuiteK8s {
			continue
		}
		if !hasContainerImage(entry.AllRequirements(), ContainerImageExporter) {
			t.Fatalf("%s does not use the exporter image", entry.Selector())
		}
	}
	for image, selectors := range want {
		if !sameStrings(got[image], selectors) {
			t.Fatalf("%s selectors = %#v, want %#v", image, got[image], selectors)
		}
	}
	for image, selectors := range got {
		if _, ok := want[image]; !ok {
			t.Fatalf("unexpected scenario image %q used by %#v", image, selectors)
		}
	}
}

// TestCapabilityProbeInputsForUsesCatalogEntries verifies only the profiling, NVLink fabric, Grace, C2C,
// unsupported-field and MIG-allocation catalog scenarios declare a capability probe input. Of the MIG-allocation
// scenarios, migSpecificInstanceSelection and migPodAttribution declare the DCGM image input; every other one
// declares the MIG state input instead.
func TestCapabilityProbeInputsForUsesCatalogEntries(t *testing.T) {
	want := map[string]bool{
		"profiling":                    true,
		"nvlinkFabric":                 true,
		"graceCpu":                     true,
		"c2c":                          true,
		"fieldUnsupported":             true,
		"migSpecificInstanceSelection": true,
		"migPodAttribution":            true,
	}
	for _, entry := range Catalog {
		if entry.Suite != SuiteK8s {
			continue
		}
		wantDCGMImage := want[entry.Name]
		wantMIGState := !wantDCGMImage && entry.UsesKubernetesAllocation(KubernetesAllocationMIG, KubernetesAllocationMixedMIG)
		inputs := CapabilityProbeInputsFor([]Scenario{entry})
		if got := len(inputs) != 0; got != (wantDCGMImage || wantMIGState) {
			t.Fatalf("%s capability probe input = %t, want %t", entry.Selector(), got, wantDCGMImage || wantMIGState)
		}
		if got := slices.Contains(inputs, CapabilityProbeInputDCGMImage); got != wantDCGMImage {
			t.Fatalf("%s DCGM image input = %t, want %t", entry.Selector(), got, wantDCGMImage)
		}
		if got := slices.Contains(inputs, CapabilityProbeInputMIGState); got != wantMIGState {
			t.Fatalf("%s MIG state input = %t, want %t", entry.Selector(), got, wantMIGState)
		}
	}
}

// TestRequirementsForMIGOnlySelectionNeedsDCGMImage verifies a local MIG-only run gets the DCGM image its
// plan-time MIG state probe uses, and an external target does not.
func TestRequirementsForMIGOnlySelectionNeedsDCGMImage(t *testing.T) {
	selected := Selected(Catalog, config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/mig"}}})
	want := Requirement{Kind: RequirementContainerImage, Image: ContainerImageDCGM}
	if !HasRequirement(RequirementsFor(selected, RunConditions{LocalKubernetes: true}), want) {
		t.Fatal("local MIG-only selection does not require the DCGM image")
	}
	if HasRequirement(RequirementsFor(selected, RunConditions{}), want) {
		t.Fatal("external MIG-only selection requires the DCGM image")
	}
}

// TestDefaultHostSelectionNeedsNoContainerRuntime keeps the GPU load scenario,
// which needs Docker, the GPU runtime and the DCGM image, out of a default
// host-suite run, and adds those requirements when it is named.
func TestDefaultHostSelectionNeedsNoContainerRuntime(t *testing.T) {
	unwanted := []Requirement{
		{Kind: RequirementDocker},
		{Kind: RequirementGPURuntime},
		{Kind: RequirementContainerImage, Image: ContainerImageDCGM},
	}
	host := RequirementsFor(Selected(Catalog, config.Config{TestOptions: config.TestOptions{Suites: []string{"host"}}}), RunConditions{})
	for _, requirement := range unwanted {
		if HasRequirement(host, requirement) {
			t.Fatalf("a default host-suite run requires %#v", requirement)
		}
	}

	named := RequirementsFor(Selected(Catalog, config.Config{TestOptions: config.TestOptions{Scenarios: []string{"host/gpuLoadUtilization"}}}), RunConditions{})
	for _, requirement := range unwanted {
		if !HasRequirement(named, requirement) {
			t.Fatalf("host/gpuLoadUtilization does not require %#v", requirement)
		}
	}
}

// TestSelectedAppliesSuiteAndScenarioFilters verifies the complete selection uses existing filter rules.
func TestSelectedAppliesSuiteAndScenarioFilters(t *testing.T) {
	cfg := config.Config{TestOptions: config.TestOptions{
		Suites:        []string{"k8s"},
		Scenarios:     []string{"k8s/default", "k8s/profiling"},
		SkipScenarios: []string{"k8s/profiling"},
	}}
	got := Selected(Catalog, cfg)
	if len(got) != 1 || got[0].Selector() != "k8s/default" {
		t.Fatalf("Selected() = %#v, want only k8s/default", got)
	}
}

// TestKubernetesAPIRequirementFollowsSelectedScenarios verifies only selected Kubernetes scenarios request API readiness.
func TestKubernetesAPIRequirementFollowsSelectedScenarios(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{
			name: "static scenario",
			cfg: config.Config{TestOptions: config.TestOptions{
				Suites:    []string{"static"},
				Scenarios: []string{"static/paths"},
			}},
			want: false,
		},
		{
			name: "Kubernetes scenario",
			cfg: config.Config{TestOptions: config.TestOptions{
				Suites:    []string{"k8s"},
				Scenarios: []string{"k8s/default"},
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requirements := RequirementsFor(Selected(Catalog, tt.cfg), RunConditions{})
			got := HasRequirement(requirements, Requirement{Kind: RequirementKubernetesAPI})
			if got != tt.want {
				t.Fatalf("Kubernetes API requirement = %t, want %t for %#v", got, tt.want, tt.cfg.TestOptions)
			}
		})
	}
}

// TestValidateCatalogRejectsInvalidRequirements verifies malformed catalog requirement data fails before execution.
func TestValidateCatalogRejectsInvalidRequirements(t *testing.T) {
	valid := Scenario{Suite: SuiteStatic, Name: "valid", Group: GroupStaticBaseline, ResultName: "valid"}
	valid.Requirements = []Requirement{{Kind: RequirementDockerCLI}}
	if err := ValidateCatalog([]Scenario{valid}); err != nil {
		t.Fatalf("ValidateCatalog() rejected Docker CLI requirement: %v", err)
	}
	for _, requirement := range []Requirement{
		{Kind: RequirementKind("unknown")},
		{Kind: RequirementDocker, Image: ContainerImageDCGM},
		{Kind: RequirementContainerImage},
		{Kind: RequirementContainerImage, Image: ContainerImage("unknown")},
	} {
		entry := valid
		entry.Requirements = []Requirement{requirement}
		if err := ValidateCatalog([]Scenario{entry}); err == nil {
			t.Fatalf("ValidateCatalog() accepted requirement %#v", requirement)
		}
	}
}

// TestValidateCatalogRejectsDuplicateRequirementAndCapabilityInput verifies one scenario cannot repeat metadata.
func TestValidateCatalogRejectsDuplicateRequirementAndCapabilityInput(t *testing.T) {
	entry := Scenario{
		Suite:        SuiteStatic,
		Name:         "duplicate",
		Group:        GroupStaticBaseline,
		ResultName:   "duplicate",
		Requirements: []Requirement{{Kind: RequirementDocker}, {Kind: RequirementDocker}},
	}
	if err := ValidateCatalog([]Scenario{entry}); err == nil {
		t.Fatal("ValidateCatalog() accepted duplicate requirements")
	}
	entry.Requirements = nil
	entry.CapabilityProbeInputs = []CapabilityProbeInput{CapabilityProbeInputDCGMImage, CapabilityProbeInputDCGMImage}
	if err := ValidateCatalog([]Scenario{entry}); err == nil {
		t.Fatal("ValidateCatalog() accepted duplicate capability probe inputs")
	}
}

// sameSelectedRequirements compares requirement lists without hiding order or reason drift.
func sameSelectedRequirements(got, want []SelectedRequirement) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].Requirement != want[index].Requirement || !sameStrings(got[index].RequiredBy, want[index].RequiredBy) {
			return false
		}
	}
	return true
}

// hasContainerImage reports whether requirements contains one named image.
func hasContainerImage(requirements []Requirement, wanted ContainerImage) bool {
	for _, requirement := range requirements {
		if requirement.Kind == RequirementContainerImage && requirement.Image == wanted {
			return true
		}
	}
	return false
}
