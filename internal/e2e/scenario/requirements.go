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
	"fmt"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// RequirementKind names one thing needed before a scenario can run.
type RequirementKind string

const (
	RequirementDocker           RequirementKind = "docker"
	RequirementDockerCLI        RequirementKind = "docker-cli"
	RequirementBuildx           RequirementKind = "docker-buildx"
	RequirementContainerToolkit RequirementKind = "container-toolkit"
	RequirementGPURuntime       RequirementKind = "gpu-runtime"
	RequirementHostDCGM         RequirementKind = "host-dcgm"
	RequirementDCGMDevelopment  RequirementKind = "dcgm-development"
	RequirementVSOCK            RequirementKind = "vsock"
	RequirementKubernetesTools  RequirementKind = "kubernetes-tools"
	RequirementKubernetesAPI    RequirementKind = "kubernetes-api"
	RequirementContainerImage   RequirementKind = "container-image"
	RequirementKubernetesImage  RequirementKind = "kubernetes-image"
)

// ContainerImage names a configured image without including its registry reference.
type ContainerImage string

const (
	ContainerImageDCGM            ContainerImage = "dcgm"
	ContainerImageExporter        ContainerImage = "exporter"
	ContainerImageBusybox         ContainerImage = "busybox"
	ContainerImageCUDAWorkload    ContainerImage = "cuda-workload"
	ContainerImageGPUOperatorDCGM ContainerImage = "gpu-operator-dcgm"
)

// Requirement describes one thing a scenario needs. Image is set only for controller-checked or Kubernetes-target image requirements.
type Requirement struct {
	Kind  RequirementKind
	Image ContainerImage
}

// CapabilityProbeInput names data used only to measure a scenario capability.
// It does not describe a setup action; RequirementsFor adds any supporting requirement it needs.
type CapabilityProbeInput string

const (
	CapabilityProbeInputDCGMImage CapabilityProbeInput = "dcgm-image"
	// CapabilityProbeInputMIGState reads the current MIG state of a local host while planning, which needs the DCGM image.
	// An external target plans the scenario from its Kubernetes allocation instead.
	CapabilityProbeInputMIGState CapabilityProbeInput = "mig-state"
)

// RunConditions records facts that add or remove requirements for one test run.
// It contains run facts, not CLI flags or package-manager details.
type RunConditions struct {
	LocalKubernetes         bool
	RegistryLoginConfigured bool
	NVMLInjectionConfigured bool
	HostDCGMIsMocked        bool
}

// SelectedRequirement records one requirement and the selected scenarios or run facts that need it.
type SelectedRequirement struct {
	Requirement Requirement
	RequiredBy  []string
}

var suiteRequirements = map[Suite][]Requirement{
	SuiteHost: {
		{Kind: RequirementHostDCGM},
	},
	SuiteContainer: {
		{Kind: RequirementContainerImage, Image: ContainerImageExporter},
	},
	SuiteK8s: {
		{Kind: RequirementKubernetesTools},
		{Kind: RequirementKubernetesAPI},
		{Kind: RequirementContainerImage, Image: ContainerImageExporter},
	},
}

// AllRequirements returns the suite requirements followed by requirements declared by this scenario.
func (s Scenario) AllRequirements() []Requirement {
	requirements := append([]Requirement{}, suiteRequirements[s.Suite]...)
	return append(requirements, s.Requirements...)
}

// Selected returns every non-capability-gated scenario selected by the test configuration.
func Selected(cat []Scenario, cfg config.Config) []Scenario {
	var selected []Scenario
	for _, suite := range AllSuites() {
		selected = append(selected, SelectedForSuite(cat, suite, cfg)...)
	}
	return selected
}

// RequirementsFor returns all requirements for one run in first-use order.
// RequiredBy uses suite/scenario selectors and plain run reasons for reports and errors; external Kubernetes runs omit controller-only probes.
func RequirementsFor(selected []Scenario, conditions RunConditions) []SelectedRequirement {
	var requirements []SelectedRequirement
	addScenarioRequirements(&requirements, selected, conditions)
	addRunConditionRequirements(&requirements, selected, conditions)
	addProbeImageRequirements(&requirements, selected, conditions)
	return requirements
}

// addScenarioRequirements adds each selected scenario's own requirements. A mocked host
// DCGM needs no VSOCK, and image requirements resolve to their target image.
func addScenarioRequirements(requirements *[]SelectedRequirement, selected []Scenario, conditions RunConditions) {
	for _, entry := range selected {
		for _, requirement := range entry.AllRequirements() {
			if requirement.Kind == RequirementVSOCK && entry.Suite == SuiteHost && conditions.HostDCGMIsMocked {
				continue
			}
			requirement = targetImageRequirement(entry, requirement, conditions)
			addRequirement(requirements, requirement, entry.Selector())
		}
	}
}

// addRunConditionRequirements adds requirements implied by the run configuration: a
// local cluster, registry login, and NVML injection.
func addRunConditionRequirements(requirements *[]SelectedRequirement, selected []Scenario, conditions RunConditions) {
	if selectedKubernetes(selected) && conditions.LocalKubernetes {
		addRequirement(requirements, Requirement{Kind: RequirementBuildx}, "local Kubernetes")
		addRequirement(requirements, Requirement{Kind: RequirementGPURuntime}, "local Kubernetes")
	}
	if conditions.RegistryLoginConfigured {
		requirement := Requirement{Kind: RequirementDockerCLI}
		addRequirement(requirements, requirement, requirementReason(requirement))
	}
	if conditions.NVMLInjectionConfigured && containsRequirement(*requirements, Requirement{Kind: RequirementHostDCGM}) {
		addRequirement(requirements, Requirement{Kind: RequirementDCGMDevelopment}, "NVML injection")
	}
}

// addProbeImageRequirements adds the DCGM image for scenarios whose capability probe
// runs it on this controller; external Kubernetes targets never probe with it.
func addProbeImageRequirements(requirements *[]SelectedRequirement, selected []Scenario, conditions RunConditions) {
	for _, entry := range selected {
		if entry.Suite == SuiteK8s && !conditions.LocalKubernetes {
			continue
		}
		for _, input := range entry.CapabilityProbeInputs {
			if input == CapabilityProbeInputDCGMImage || input == CapabilityProbeInputMIGState {
				addRequirement(requirements, Requirement{Kind: RequirementContainerImage, Image: ContainerImageDCGM}, entry.Selector())
			}
		}
	}
}

// CapabilityProbeInputsFor returns the capability-probe inputs needed by selected scenarios.
// The Kubernetes CLI uses these inputs only to preserve capability-probe behavior.
func CapabilityProbeInputsFor(selected []Scenario) []CapabilityProbeInput {
	seen := make(map[CapabilityProbeInput]struct{})
	var inputs []CapabilityProbeInput
	for _, entry := range selected {
		for _, input := range entry.CapabilityProbeInputs {
			if _, ok := seen[input]; ok {
				continue
			}
			seen[input] = struct{}{}
			inputs = append(inputs, input)
		}
	}
	return inputs
}

// HasRequirement reports whether requirements contains one exact requirement.
func HasRequirement(requirements []SelectedRequirement, wanted Requirement) bool {
	return containsRequirement(requirements, wanted)
}

// HasImageRequirement reports whether requirements includes name for either a local or Kubernetes image consumer.
// Kubernetes setup uses it when an external target needs an image reference but the controller does not need Docker.
func HasImageRequirement(requirements []SelectedRequirement, name ContainerImage) bool {
	for _, requirement := range requirements {
		if requirement.Requirement.Image != name {
			continue
		}
		if IsImageRequirement(requirement.Requirement) {
			return true
		}
	}
	return false
}

// IsImageRequirement reports whether requirement names an image that must be configured for a test run.
// Validation uses it for both controller-checked images and images pulled only by a selected Kubernetes target.
func IsImageRequirement(requirement Requirement) bool {
	return requirement.Kind == RequirementContainerImage || requirement.Kind == RequirementKubernetesImage
}

// RequiresLocalImageCheck reports whether the controller must inspect an image through Docker.
// External Kubernetes images deliberately return false because image availability belongs to the target cluster.
func RequiresLocalImageCheck(requirement Requirement) bool {
	return requirement.Kind == RequirementContainerImage
}

// targetImageRequirement makes external Kubernetes image use explicit in a run's requirements.
// The same configured image is still validated, but it no longer adds a Docker dependency to the controller.
func targetImageRequirement(entry Scenario, requirement Requirement, conditions RunConditions) Requirement {
	if entry.Suite == SuiteK8s && !conditions.LocalKubernetes && requirement.Kind == RequirementContainerImage {
		requirement.Kind = RequirementKubernetesImage
	}
	return requirement
}

// addRequirement adds a requirement and its direct host dependencies once.
func addRequirement(requirements *[]SelectedRequirement, requirement Requirement, reason string) {
	switch requirement.Kind {
	case RequirementBuildx, RequirementContainerToolkit, RequirementContainerImage:
		addRequirement(requirements, Requirement{Kind: RequirementDocker}, requirementReason(requirement))
	case RequirementGPURuntime:
		addRequirement(requirements, Requirement{Kind: RequirementDocker}, requirementReason(requirement))
		addRequirement(requirements, Requirement{Kind: RequirementContainerToolkit}, requirementReason(requirement))
	}

	for index := range *requirements {
		if (*requirements)[index].Requirement != requirement {
			continue
		}
		if !contains((*requirements)[index].RequiredBy, reason) {
			(*requirements)[index].RequiredBy = append((*requirements)[index].RequiredBy, reason)
		}
		return
	}
	*requirements = append(*requirements, SelectedRequirement{Requirement: requirement, RequiredBy: []string{reason}})
}

// requirementReason gives a plain reason for a dependency added by another requirement.
func requirementReason(requirement Requirement) string {
	switch requirement.Kind {
	case RequirementBuildx:
		return "Docker Buildx"
	case RequirementContainerToolkit:
		return "container toolkit"
	case RequirementContainerImage:
		return "container image check"
	case RequirementDockerCLI:
		return "registry login"
	case RequirementGPURuntime:
		return "GPU runtime"
	default:
		return string(requirement.Kind)
	}
}

// containsRequirement reports whether requirements contains one exact requirement.
func containsRequirement(requirements []SelectedRequirement, wanted Requirement) bool {
	for _, requirement := range requirements {
		if requirement.Requirement == wanted {
			return true
		}
	}
	return false
}

// selectedKubernetes reports whether selected contains a Kubernetes scenario.
func selectedKubernetes(selected []Scenario) bool {
	for _, entry := range selected {
		if entry.Suite == SuiteK8s {
			return true
		}
	}
	return false
}

// validRequirement reports whether requirement has a known kind and valid image data.
func validRequirement(requirement Requirement) bool {
	switch requirement.Kind {
	case RequirementDocker, RequirementDockerCLI, RequirementBuildx, RequirementContainerToolkit, RequirementGPURuntime,
		RequirementHostDCGM, RequirementDCGMDevelopment, RequirementVSOCK,
		RequirementKubernetesTools, RequirementKubernetesAPI:
		return requirement.Image == ""
	case RequirementContainerImage, RequirementKubernetesImage:
		switch requirement.Image {
		case ContainerImageDCGM, ContainerImageExporter, ContainerImageBusybox,
			ContainerImageCUDAWorkload, ContainerImageGPUOperatorDCGM:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// validCapabilityProbeInput reports whether input is a known capability-probe input.
func validCapabilityProbeInput(input CapabilityProbeInput) bool {
	return input == CapabilityProbeInputDCGMImage || input == CapabilityProbeInputMIGState
}

// validateRequirements rejects unknown, duplicate, and malformed requirement declarations.
func validateRequirements(requirements []Requirement) error {
	seen := make(map[Requirement]struct{}, len(requirements))
	for _, requirement := range requirements {
		if !validRequirement(requirement) {
			return fmt.Errorf("uses invalid requirement %#v", requirement)
		}
		if _, ok := seen[requirement]; ok {
			return fmt.Errorf("declares duplicate requirement %#v", requirement)
		}
		seen[requirement] = struct{}{}
	}
	return nil
}

// validateCapabilityProbeInputs rejects unknown and duplicate capability-probe input declarations.
func validateCapabilityProbeInputs(inputs []CapabilityProbeInput) error {
	seen := make(map[CapabilityProbeInput]struct{}, len(inputs))
	for _, input := range inputs {
		if !validCapabilityProbeInput(input) {
			return fmt.Errorf("uses unknown capability probe input %q", input)
		}
		if _, ok := seen[input]; ok {
			return fmt.Errorf("declares duplicate capability probe input %q", input)
		}
		seen[input] = struct{}{}
	}
	return nil
}
