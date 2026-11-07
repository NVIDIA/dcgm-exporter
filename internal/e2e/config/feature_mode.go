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

package config

import (
	"fmt"
	"strings"
)

// FeatureMode is the parsed value of an auto, true or false setup flag:
// --shared-gpu-configure, --dra-configure and --k3d-cgroup-v1. The zero value
// is "", not FeatureOff: Enabled, Required and SetUpOn treat it like
// FeatureOff, but the local k3d cgroup-v1 setup treats it as FeatureAuto.
// --mig-configure is not a FeatureMode: its value can also be a profile ID,
// and its auto must never change the host (see mig.MutationRequested).
type FeatureMode string

const (
	// FeatureOff leaves the feature out unless a scenario names it.
	FeatureOff FeatureMode = "false"
	// FeatureAuto sets the feature up only where the harness owns the target.
	FeatureAuto FeatureMode = "auto"
	// FeatureOn sets the feature up on any target and fails when it cannot.
	FeatureOn FeatureMode = "true"
)

// ParseFeatureMode reads an auto, true or false flag value, ignoring case and
// surrounding spaces. Any other value is an error.
func ParseFeatureMode(value string) (FeatureMode, error) {
	switch mode := FeatureMode(strings.ToLower(strings.TrimSpace(value))); mode {
	case FeatureOff, FeatureAuto, FeatureOn:
		return mode, nil
	default:
		return "", fmt.Errorf("must be auto, true, or false")
	}
}

// Enabled reports whether the feature's scenarios run by default. Auto counts:
// its scenarios run and skip when setup leaves the feature out.
func (m FeatureMode) Enabled() bool {
	return m == FeatureAuto || m == FeatureOn
}

// Required reports whether setup must fail when the feature cannot be set up.
func (m FeatureMode) Required() bool {
	return m == FeatureOn
}

// SetUpOn reports whether setup should configure the feature on a target.
// ownedTarget is true for a local cluster the harness created and may change.
func (m FeatureMode) SetUpOn(ownedTarget bool) bool {
	return m == FeatureOn || (m == FeatureAuto && ownedTarget)
}

// GPUOperatorMode is the parsed value of --gpu-operator. The zero value is "",
// which Enabled and InstallOn treat like GPUOperatorOff.
type GPUOperatorMode string

const (
	// GPUOperatorOff leaves GPU Operator scenarios out unless one is named.
	GPUOperatorOff GPUOperatorMode = "false"
	// GPUOperatorAuto installs GPU Operator only where the harness owns the target.
	GPUOperatorAuto GPUOperatorMode = "auto"
	// GPUOperatorExisting uses a GPU Operator that is already installed.
	GPUOperatorExisting GPUOperatorMode = "existing"
	// GPUOperatorInstall installs GPU Operator on any target.
	GPUOperatorInstall GPUOperatorMode = "install"
)

// ParseGPUOperatorMode reads a --gpu-operator value, ignoring case and
// surrounding spaces. Any other value is an error.
func ParseGPUOperatorMode(value string) (GPUOperatorMode, error) {
	switch mode := GPUOperatorMode(strings.ToLower(strings.TrimSpace(value))); mode {
	case GPUOperatorOff, GPUOperatorAuto, GPUOperatorExisting, GPUOperatorInstall:
		return mode, nil
	default:
		return "", fmt.Errorf("must be auto, existing, install, or false")
	}
}

// Enabled reports whether GPU Operator scenarios run by default.
func (m GPUOperatorMode) Enabled() bool {
	return m != "" && m != GPUOperatorOff
}

// InstallOn reports whether setup should install GPU Operator on a target.
// ownedTarget is true for a local cluster the harness created and may change.
func (m GPUOperatorMode) InstallOn(ownedTarget bool) bool {
	return m == GPUOperatorInstall || (m == GPUOperatorAuto && ownedTarget)
}
