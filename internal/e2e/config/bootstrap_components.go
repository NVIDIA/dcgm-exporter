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

// BootstrapComponent identifies a dependency that can have its own setup mode.
// TestOptions uses it as the key for check, install, or skip overrides.
type BootstrapComponent string

const (
	BootstrapDocker           BootstrapComponent = "docker"
	BootstrapBuildx           BootstrapComponent = "buildx"
	BootstrapContainerToolkit BootstrapComponent = "container-toolkit"
	BootstrapGPURuntime       BootstrapComponent = "gpu-runtime"
	BootstrapHostDCGM         BootstrapComponent = "host-dcgm"
	BootstrapKubernetesTools  BootstrapComponent = "kubernetes-tools"
	BootstrapVSOCK            BootstrapComponent = "vsock"
)

// ParseBootstrapComponents reads name=mode settings from flags or the environment.
// CLI parsing uses it before storing the resulting overrides in TestOptions.
func ParseBootstrapComponents(values []string) (map[BootstrapComponent]BootstrapMode, error) {
	components := make(map[BootstrapComponent]BootstrapMode, len(values))
	for _, value := range values {
		name, mode, found := strings.Cut(strings.TrimSpace(value), "=")
		if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(mode) == "" {
			return nil, fmt.Errorf("bootstrap component %q must use name=check, name=install, or name=skip", value)
		}
		component := BootstrapComponent(strings.ToLower(strings.TrimSpace(name)))
		if !ValidBootstrapComponent(component) {
			return nil, fmt.Errorf("unknown bootstrap component %q", name)
		}
		parsedMode, err := ParseBootstrapMode(mode)
		if err != nil {
			return nil, fmt.Errorf("bootstrap component %q: %w", component, err)
		}
		if _, exists := components[component]; exists {
			return nil, fmt.Errorf("bootstrap component %q is set more than once", component)
		}
		components[component] = parsedMode
	}
	return components, nil
}

// ParseBootstrapMode parses the three supported setup modes.
// ParseBootstrapComponents and bootstrap validation use it to reject invalid values.
func ParseBootstrapMode(value string) (BootstrapMode, error) {
	switch BootstrapMode(strings.ToLower(strings.TrimSpace(value))) {
	case BootstrapCheck:
		return BootstrapCheck, nil
	case BootstrapInstall:
		return BootstrapInstall, nil
	case BootstrapSkip:
		return BootstrapSkip, nil
	default:
		return "", fmt.Errorf("must be check, install, or skip")
	}
}

// ValidBootstrapComponent reports whether name is a supported setup component.
// ParseBootstrapComponents and bootstrap validation use it to reject unknown names.
func ValidBootstrapComponent(component BootstrapComponent) bool {
	switch component {
	case BootstrapDocker, BootstrapBuildx, BootstrapContainerToolkit, BootstrapGPURuntime,
		BootstrapHostDCGM, BootstrapKubernetesTools, BootstrapVSOCK:
		return true
	default:
		return false
	}
}

// BootstrapModeFor returns a component override or the run-wide default.
// Bootstrap checks call it before deciding whether a dependency may be installed.
func (o TestOptions) BootstrapModeFor(component BootstrapComponent) BootstrapMode {
	if mode, found := o.BootstrapComponents[component]; found {
		return mode
	}
	if o.BootstrapMode == "" {
		return BootstrapCheck
	}
	return o.BootstrapMode
}

// SkipsAllBootstrap reports whether no component can run setup work.
// CLI validation uses it to reject a bootstrap-only run that would do nothing.
func (o TestOptions) SkipsAllBootstrap() bool {
	if o.BootstrapMode != BootstrapSkip {
		return false
	}
	for _, mode := range o.BootstrapComponents {
		if mode != BootstrapSkip {
			return false
		}
	}
	return true
}
