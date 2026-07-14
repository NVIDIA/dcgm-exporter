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

package cli

import (
	"fmt"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// validateTestsConfig checks selectors and feature-mode values after env and flag parsing.
func validateTestsConfig(opts config.Tests) error {
	if opts.DCGMNVMLInjectionYAML == "" && containsString(opts.Scenarios, "host/nvmlInjectionMetrics") {
		return fmt.Errorf("host/nvmlInjectionMetrics requires --dcgm-nvml-injection-yaml")
	}
	if opts.DCGMNVMLInjectionYAML != "" && containsString(opts.Scenarios, "host/dcgmUri") {
		return fmt.Errorf("host/dcgmUri is incompatible with NVML injection")
	}
	for name, value := range map[string]string{
		"--install-deps-docker":                   opts.InstallDepsDocker,
		"--install-deps-nvidia-container-toolkit": opts.InstallDepsNvidiaToolkit,
		"--install-deps-dcgm":                     opts.InstallDepsDCGM,
		"--install-deps-vsock":                    opts.InstallDepsVSOCK,
	} {
		if value != "" && !validBoolLiteral(value) {
			return fmt.Errorf("%s must be true or false", name)
		}
	}
	for name, value := range map[string]string{
		"--dra-configure":        opts.DRAConfigure,
		"--shared-gpu-configure": opts.SharedGPUConfigure,
	} {
		if !oneOfFold(value, "auto", "true", "false") {
			return fmt.Errorf("%s must be auto, true, or false", name)
		}
	}
	if !oneOfFold(opts.GPUOperator, "auto", "existing", "install", "false") {
		return fmt.Errorf("--gpu-operator must be auto, existing, install, or false")
	}
	if !oneOfFold(opts.DCGMFailureInjection, "auto", "true", "false") {
		return fmt.Errorf("--dcgm-failure-injection must be auto, true, or false")
	}
	if !oneOfFold(opts.K3dIPFamily, "ipv4", "ipv6", "dualstack") {
		return fmt.Errorf("--k3d-ip-family must be ipv4, ipv6, or dualstack")
	}
	for _, suiteName := range append(append([]string{}, opts.Suites...), opts.SkipSuites...) {
		if !validSuiteSelector(suiteName) {
			return fmt.Errorf("unknown suite selector %q", suiteName)
		}
	}
	for _, selector := range append(append([]string{}, opts.Scenarios...), opts.SkipScenarios...) {
		if !validScenarioSelector(selector) {
			return fmt.Errorf("unknown scenario selector %q", selector)
		}
	}
	for _, suiteName := range opts.Suites {
		if containsString(opts.SkipSuites, suiteName) || containsString(opts.SkipSuites, "all") {
			return fmt.Errorf("suite %q cannot be both selected and skipped", suiteName)
		}
	}
	for _, selector := range opts.Scenarios {
		if containsString(opts.SkipScenarios, selector) {
			return fmt.Errorf("scenario %q cannot be both selected and skipped", selector)
		}
	}
	if opts.DCGMNVMLInjectionYAML != "" && len(scenario.SelectedForSuite(
		scenario.Catalog,
		scenario.SuiteHost,
		config.Config{Tests: opts},
	)) == 0 {
		return fmt.Errorf("NVML injection requires at least one selected host scenario")
	}
	return nil
}

// validSuiteSelector delegates suite-name validation to the scenario catalog package.
func validSuiteSelector(value string) bool {
	return scenario.ValidSuiteSelector(value)
}

// validScenarioSelector reports whether value names a catalog scenario exactly.
func validScenarioSelector(value string) bool {
	for _, entry := range scenario.Catalog {
		if entry.Selector() == value {
			return true
		}
	}
	return false
}
