/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package bootstrap plans and prepares host prerequisites for E2E scenarios.
package bootstrap

import (
	"fmt"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// DependencyRequirements lists the host checks and packages required for one run.
type DependencyRequirements struct {
	Docker bool
	// DockerCLI is needed for registry login even when no local daemon is used.
	DockerCLI        bool
	Buildx           bool
	ContainerToolkit bool
	GPURuntime       bool
	HostDCGM         bool
	DCGMDevelopment  bool
	VSOCK            bool
	KubernetesTools  bool
}

// BuildDependencyRequirements maps selected requirements to the host checks owned by bootstrap.
// Kubernetes API and image requirements belong to later CLI checks and are ignored here.
func BuildDependencyRequirements(requirements []scenario.SelectedRequirement) (DependencyRequirements, error) {
	var required DependencyRequirements
	for _, selected := range requirements {
		switch selected.Requirement.Kind {
		case scenario.RequirementDocker:
			required.Docker = true
		case scenario.RequirementDockerCLI:
			required.DockerCLI = true
		case scenario.RequirementBuildx:
			required.Buildx = true
		case scenario.RequirementContainerToolkit:
			required.ContainerToolkit = true
		case scenario.RequirementGPURuntime:
			required.GPURuntime = true
		case scenario.RequirementHostDCGM:
			required.HostDCGM = true
		case scenario.RequirementDCGMDevelopment:
			required.DCGMDevelopment = true
		case scenario.RequirementVSOCK:
			required.VSOCK = true
		case scenario.RequirementKubernetesTools:
			required.KubernetesTools = true
		case scenario.RequirementKubernetesAPI, scenario.RequirementContainerImage, scenario.RequirementKubernetesImage:
			continue
		default:
			return DependencyRequirements{}, fmt.Errorf("unknown requirement %q", selected.Requirement.Kind)
		}
	}
	return required, nil
}
