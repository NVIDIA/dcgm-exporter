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

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// normalizeBootstrapFlag applies the winning CLI or environment bootstrap mode.
func normalizeBootstrapFlag(opts *config.TestOptions, value string, set bool) error {
	if !set {
		mode, source, err := bootstrapFromEnvironment()
		if err != nil {
			return err
		}
		opts.BootstrapMode = mode
		opts.BootstrapSource = source
		return nil
	}
	mode, err := parseBootstrapMode(value)
	if err != nil {
		return fmt.Errorf("--bootstrap: %w", err)
	}
	opts.BootstrapMode = mode
	opts.BootstrapSource = config.ValueCLI
	return nil
}

// normalizeBootstrapComponents applies command-line dependency overrides.
// The tests command calls it after flag parsing so CLI values override environment values.
func normalizeBootstrapComponents(opts *config.TestOptions, values []string, set bool) error {
	var cliComponents map[config.BootstrapComponent]config.BootstrapMode
	if set {
		var err error
		cliComponents, err = config.ParseBootstrapComponents(values)
		if err != nil {
			return fmt.Errorf("--bootstrap-component: %w", err)
		}
	}
	components, err := bootstrapComponentsFromEnvironmentWithOverrides(os.Getenv("E2E_BOOTSTRAP_COMPONENTS"), cliComponents)
	if err != nil {
		return err
	}
	for component, mode := range cliComponents {
		if components == nil {
			components = make(map[config.BootstrapComponent]config.BootstrapMode)
		}
		components[component] = mode
	}
	opts.BootstrapComponents = components
	return nil
}

// bootstrapComponentsFromEnvironmentWithOverrides validates environment entries not replaced by the CLI.
func bootstrapComponentsFromEnvironmentWithOverrides(value string, overrides map[config.BootstrapComponent]config.BootstrapMode) (map[config.BootstrapComponent]config.BootstrapMode, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	values := strings.Split(value, ",")
	if len(overrides) != 0 {
		remaining := values[:0]
		for _, entry := range values {
			component, _, _ := strings.Cut(strings.TrimSpace(entry), "=")
			componentName := config.BootstrapComponent(strings.ToLower(strings.TrimSpace(component)))
			if _, overridden := overrides[componentName]; overridden {
				continue
			}
			remaining = append(remaining, entry)
		}
		values = remaining
	}
	if len(values) == 0 {
		return nil, nil
	}
	components, err := config.ParseBootstrapComponents(values)
	if err != nil {
		return nil, fmt.Errorf("E2E_BOOTSTRAP_COMPONENTS: %w", err)
	}
	return components, nil
}

// normalizeBootstrapForArtifact chooses the source or package default after detecting the artifact type.
func normalizeBootstrapForArtifact(opts *config.TestOptions, artifact artifactMode) error {
	if opts.BootstrapMode == "" {
		switch artifact {
		case artifactModeSource:
			opts.BootstrapMode = config.BootstrapCheck
		case artifactModePackage:
			opts.BootstrapMode = config.BootstrapInstall
		default:
			return fmt.Errorf("cannot choose bootstrap default for artifact mode %q", artifact)
		}
		opts.BootstrapSource = config.ValueDefault
	}
	if opts.BootstrapOnly && opts.SkipsAllBootstrap() {
		return fmt.Errorf("--bootstrap-only cannot be combined with --bootstrap=skip")
	}
	return nil
}
