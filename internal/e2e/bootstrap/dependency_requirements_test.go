/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package bootstrap

import (
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// TestBuildDependencyRequirementsMapsHostRequirementKinds verifies each host requirement reaches its existing bootstrap check.
func TestBuildDependencyRequirementsMapsHostRequirementKinds(t *testing.T) {
	requirements := selectedRequirements(
		scenario.RequirementDocker,
		scenario.RequirementDockerCLI,
		scenario.RequirementBuildx,
		scenario.RequirementContainerToolkit,
		scenario.RequirementGPURuntime,
		scenario.RequirementHostDCGM,
		scenario.RequirementDCGMDevelopment,
		scenario.RequirementVSOCK,
		scenario.RequirementKubernetesTools,
	)

	got, err := BuildDependencyRequirements(requirements)
	if err != nil {
		t.Fatal(err)
	}
	want := DependencyRequirements{
		Docker:           true,
		DockerCLI:        true,
		Buildx:           true,
		ContainerToolkit: true,
		GPURuntime:       true,
		HostDCGM:         true,
		DCGMDevelopment:  true,
		VSOCK:            true,
		KubernetesTools:  true,
	}
	if got != want {
		t.Fatalf("BuildDependencyRequirements() = %#v, want %#v", got, want)
	}
}

// TestBuildDependencyRequirementsLeavesCLIRequirementKinds verifies bootstrap does not own API checks or either image location.
// Controller images and Kubernetes-target images are checked later because bootstrap only prepares the controller host.
func TestBuildDependencyRequirementsLeavesCLIRequirementKinds(t *testing.T) {
	requirements := []scenario.SelectedRequirement{
		{Requirement: scenario.Requirement{Kind: scenario.RequirementContainerImage, Image: scenario.ContainerImageDCGM}},
		{Requirement: scenario.Requirement{Kind: scenario.RequirementKubernetesImage, Image: scenario.ContainerImageDCGM}},
		{Requirement: scenario.Requirement{Kind: scenario.RequirementKubernetesAPI}},
	}

	got, err := BuildDependencyRequirements(requirements)
	if err != nil {
		t.Fatal(err)
	}
	if got != (DependencyRequirements{}) {
		t.Fatalf("BuildDependencyRequirements() = %#v, want no host requirements", got)
	}
}

// TestBuildDependencyRequirementsKeepsGPURuntimeSeparate verifies toolkit-only work cannot start GPU runtime checks.
func TestBuildDependencyRequirementsKeepsGPURuntimeSeparate(t *testing.T) {
	got, err := BuildDependencyRequirements(selectedRequirements(scenario.RequirementContainerToolkit))
	if err != nil {
		t.Fatal(err)
	}
	if !got.ContainerToolkit || got.GPURuntime {
		t.Fatalf("BuildDependencyRequirements() = %#v, want toolkit without GPU runtime", got)
	}
}

// TestBuildDependencyRequirementsRejectsUnknownRequirement verifies catalog drift fails before host work starts.
func TestBuildDependencyRequirementsRejectsUnknownRequirement(t *testing.T) {
	_, err := BuildDependencyRequirements(selectedRequirements(scenario.RequirementKind("unknown")))
	if err == nil {
		t.Fatal("BuildDependencyRequirements() succeeded with an unknown requirement")
	}
}

// selectedRequirements creates the minimal selected-requirement list used by mapping tests.
func selectedRequirements(kinds ...scenario.RequirementKind) []scenario.SelectedRequirement {
	requirements := make([]scenario.SelectedRequirement, 0, len(kinds))
	for _, kind := range kinds {
		requirements = append(requirements, scenario.SelectedRequirement{Requirement: scenario.Requirement{Kind: kind}})
	}
	return requirements
}
