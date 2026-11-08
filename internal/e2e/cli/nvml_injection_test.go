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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

func TestPrepareYAMLFixtures(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(fixture, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		prepare func(*config.TestOptions, bool) error
		value   func(config.TestOptions) string
	}{
		{
			name:    "NVML injection",
			prepare: prepareNVMLInjection,
			value:   func(opts config.TestOptions) string { return opts.DCGMNVMLInjectionYAML },
		},
		{
			name:    "NVSDM mock",
			prepare: prepareNVSDMMock,
			value:   func(opts config.TestOptions) string { return opts.DCGMNVSDMMockYAML },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(dir)
			opts := config.TestOptions{DCGMNVMLInjectionYAML: "fixture.yaml", DCGMNVSDMMockYAML: "fixture.yaml"}
			if err := tt.prepare(&opts, true); err != nil {
				t.Fatal(err)
			}
			if !filepath.IsAbs(tt.value(opts)) {
				t.Fatalf("prepared fixture path = %q, want absolute", tt.value(opts))
			}

			opts = config.TestOptions{DCGMNVMLInjectionYAML: ".", DCGMNVSDMMockYAML: "."}
			err := tt.prepare(&opts, true)
			if err == nil || !strings.Contains(err.Error(), "must name a regular file") {
				t.Fatalf("prepare directory error = %v, want regular-file error", err)
			}
		})
	}
}

// TestInjectionScenariosCanRunTogether allows several scenarios that use the
// same injection file in one run.
func TestInjectionScenariosCanRunTogether(t *testing.T) {
	opts := config.TestOptions{
		Suites:                []string{"host"},
		Scenarios:             []string{"host/nvmlInjectionMetrics", "host/migXidCounters", "host/configServesMetrics"},
		DCGMNVMLInjectionYAML: "fixture.yaml",
	}
	if err := validateConfiguredDCGMScenarios(opts); err != nil {
		t.Fatalf("validateConfiguredDCGMScenarios() error = %v", err)
	}
}

// TestOptionalInjectionScenariosShareTheFixtureRun lets the config scenarios run under the fixture next to
// the required ones, rejects a scenario that cannot use it, and lets them run without a fixture.
func TestOptionalInjectionScenariosShareTheFixtureRun(t *testing.T) {
	optional := hostScenarioSelectorsInGroup(scenario.GroupHostConfigInjection)
	if len(optional) == 0 {
		t.Fatal("no optional injection scenarios in the catalog")
	}
	if slices.Contains(optional, "host/configServesMetrics") {
		t.Fatalf("%s requires the fixture and must not be optional", "host/configServesMetrics")
	}

	withFixture := config.TestOptions{
		Suites:                []string{"host"},
		Scenarios:             append([]string{"host/configServesMetrics"}, optional...),
		DCGMNVMLInjectionYAML: "fixture.yaml",
	}
	if err := validateConfiguredDCGMScenarios(withFixture); err != nil {
		t.Fatalf("fixture with optional scenarios: %v", err)
	}
	onlyOptional := config.TestOptions{Suites: []string{"host"}, Scenarios: optional, DCGMNVMLInjectionYAML: "fixture.yaml"}
	if err := validateConfiguredDCGMScenarios(onlyOptional); err != nil {
		t.Fatalf("fixture with only optional scenarios: %v", err)
	}

	foreign := withFixture
	foreign.Scenarios = append(slices.Clone(withFixture.Scenarios), "host/startupMetrics")
	if err := validateConfiguredDCGMScenarios(foreign); err == nil || !strings.Contains(err.Error(), "host/startupMetrics") {
		t.Fatalf("fixture with a scenario that cannot use it: error = %v", err)
	}

	withoutFixture := config.TestOptions{Suites: []string{"host"}, Scenarios: optional}
	if err := validateConfiguredDCGMScenarios(withoutFixture); err != nil {
		t.Fatalf("optional scenarios without a fixture: %v", err)
	}
	required := config.TestOptions{Suites: []string{"host"}, Scenarios: []string{"host/configServesMetrics"}}
	if err := validateConfiguredDCGMScenarios(required); err == nil {
		t.Fatalf("%s without a fixture must fail", "host/configServesMetrics")
	}
}
