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
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

const (
	nvsdmMockScenario = "host/nvsdmMockMetrics"
)

// validateTestsConfig checks selectors and feature-mode values after env and flag parsing.
func validateTestsConfig(opts config.TestOptions) error {
	for _, validate := range []func(config.TestOptions) error{
		validateRunName,
		validateDurations,
		validateBootstrapOptions,
		validateGPUOperatorOptions,
		validateK3dModes,
		validateSelectors,
		validateConfiguredDCGMScenarios,
	} {
		if err := validate(opts); err != nil {
			return err
		}
	}
	return nil
}

// validateRunName requires a run name that can derive every owned resource name.
func validateRunName(opts config.TestOptions) error {
	_, err := cluster.ConfigForRunName(opts.RunName)
	return err
}

// validateDurations requires a positive readiness wait and a valid optional run timeout.
func validateDurations(opts config.TestOptions) error {
	waitTimeout, err := time.ParseDuration(opts.WaitTimeout)
	if err != nil || waitTimeout <= 0 {
		return fmt.Errorf("--wait-timeout must be a positive Go duration, got %q", opts.WaitTimeout)
	}
	_, cancelTimeout, err := timeoutContext(context.Background(), opts.Timeout)
	if err != nil {
		return err
	}
	cancelTimeout()
	return nil
}

// validateBootstrapOptions rejects an unknown bootstrap mode and a bootstrap-only run that skips everything.
func validateBootstrapOptions(opts config.TestOptions) error {
	if opts.BootstrapMode != "" {
		if _, err := parseBootstrapMode(string(opts.BootstrapMode)); err != nil {
			return fmt.Errorf("--bootstrap: %w", err)
		}
	}
	if opts.BootstrapOnly && opts.SkipsAllBootstrap() {
		return fmt.Errorf("--bootstrap-only cannot be combined with --bootstrap=skip")
	}
	return nil
}

// validateGPUOperatorOptions checks GPU Operator mode, chart, and image inputs, including
// the chart install that GPU Operator main-branch scenarios require.
func validateGPUOperatorOptions(opts config.TestOptions) error {
	chartSet := strings.TrimSpace(opts.GPUOperatorChart) != ""
	if chartSet && opts.GPUOperator != config.GPUOperatorInstall {
		return fmt.Errorf("--gpu-operator-chart requires --gpu-operator=install")
	}
	if strings.TrimSpace(opts.GPUOperatorImage) != "" && !chartSet {
		return fmt.Errorf("--gpu-operator-image requires --gpu-operator-chart")
	}
	if strings.TrimSpace(opts.GPUOperatorDCGMImage) != "" && !chartSet {
		return fmt.Errorf("--gpu-operator-dcgm-image requires --gpu-operator-chart")
	}
	for _, selector := range opts.Scenarios {
		if !strings.HasPrefix(selector, "k8s/gpuOperatorMain") {
			continue
		}
		if !chartSet {
			return fmt.Errorf("%s requires --gpu-operator-chart", selector)
		}
		if opts.GPUOperator != config.GPUOperatorInstall {
			return fmt.Errorf("%s requires --gpu-operator=install", selector)
		}
	}
	return nil
}

// validateK3dModes checks the owned k3d network mode.
func validateK3dModes(opts config.TestOptions) error {
	if !oneOfFold(opts.K3dIPFamily, "auto", "ipv4", "ipv6", "dualstack") {
		return fmt.Errorf("--k3d-ip-family must be auto, ipv4, ipv6, or dualstack")
	}
	return nil
}

// validateSelectors rejects unknown suite or scenario selectors and ones both selected and skipped.
func validateSelectors(opts config.TestOptions) error {
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
	return nil
}

// validateConfiguredDCGMScenarios keeps injected and mocked DCGM runs isolated
// from the default real-hardware host suite.
func validateConfiguredDCGMScenarios(opts config.TestOptions) error {
	if opts.DCGMNVMLInjectionYAML != "" && opts.DCGMNVSDMMockYAML != "" {
		return fmt.Errorf("--dcgm-nvml-injection-yaml and --dcgm-nvsdm-mock-yaml are mutually exclusive")
	}

	modes := []struct {
		// required scenarios need the fixture; optional ones use it when it is given.
		required, optional []string
		flag               string
		path               string
	}{
		{
			required: hostScenarioSelectorsEnabled(scenario.EnabledNVMLInjection),
			optional: hostScenarioSelectorsInGroup(scenario.GroupHostConfigInjection),
			flag:     "--dcgm-nvml-injection-yaml",
			path:     opts.DCGMNVMLInjectionYAML,
		},
		{required: []string{nvsdmMockScenario}, flag: "--dcgm-nvsdm-mock-yaml", path: opts.DCGMNVSDMMockYAML},
	}
	for _, mode := range modes {
		selector := strings.Join(mode.required, " or ")
		selectedRequired := false
		for _, candidate := range mode.required {
			if containsString(opts.Scenarios, candidate) {
				selectedRequired, selector = true, candidate
				break
			}
		}

		usable := append(slices.Clone(mode.required), mode.optional...)
		foreign := firstScenarioNotIn(opts.Scenarios, usable)

		// The subject of a selection error is the selected required scenario, or
		// the flag when only optional scenarios were selected.
		subject := selector
		if !selectedRequired {
			subject = mode.flag
		}

		// Any mix of required and optional scenarios may use the fixture, as long as
		// one scenario is selected and every selected scenario can use it.
		switch {
		case mode.path == "" && selectedRequired:
			return fmt.Errorf("%s requires %s", selector, mode.flag)
		case mode.path == "":
			continue
		case len(opts.Scenarios) == 0:
			return fmt.Errorf("%s requires --scenario %s", mode.flag, strings.Join(usable, " or "))
		case foreign != "":
			return fmt.Errorf("%s cannot run in the same invocation as %s, which does not use %s",
				subject, foreign, mode.flag)
		case !hostSuiteSelected(opts):
			return fmt.Errorf("%s requires the host suite", subject)
		}
	}

	return nil
}

// hostScenarioSelectorsEnabled lists the host scenarios that have the given
// Enabled mode.
func hostScenarioSelectorsEnabled(enabled string) []string {
	var selectors []string
	for _, entry := range scenario.Catalog {
		if entry.Suite == scenario.SuiteHost && entry.Enabled == enabled {
			selectors = append(selectors, entry.Selector())
		}
	}

	return selectors
}

// hostScenarioSelectorsInGroup lists the host scenarios in the given group,
// leaving out those in the Enabled mode that requires a fixture.
func hostScenarioSelectorsInGroup(group string) []string {
	var selectors []string
	for _, entry := range scenario.Catalog {
		if entry.Suite == scenario.SuiteHost && entry.Group == group && entry.Enabled != scenario.EnabledNVMLInjection {
			selectors = append(selectors, entry.Selector())
		}
	}

	return selectors
}

// firstScenarioNotIn returns the first selected scenario that is not in allowed, or "" if there is none.
func firstScenarioNotIn(selected, allowed []string) string {
	for _, selector := range selected {
		if !containsString(allowed, selector) {
			return selector
		}
	}
	return ""
}

func hostSuiteSelected(opts config.TestOptions) bool {
	if containsString(opts.SkipSuites, "all") || containsString(opts.SkipSuites, "host") {
		return false
	}
	return len(opts.Suites) == 0 ||
		containsString(opts.Suites, "all") ||
		containsString(opts.Suites, "host")
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
