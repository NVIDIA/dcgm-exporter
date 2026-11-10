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
	"bytes"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// TestSelectSkipsUnsupportedCoarseFeature supplies the target allocation so the host feature remains the decision under test.
// Target allocation is evaluated first to prevent a remote controller skip from masking a missing target resource.
func TestSelectSkipsUnsupportedCoarseFeature(t *testing.T) {
	plan, err := Select(Catalog, capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvlink_fabric", Status: capability.StatusUnsupported, Reason: "host has no NVLink hardware"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	}), config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/nvlink"}}})
	if err != nil {
		t.Fatal(err)
	}

	got := onlyDecision(t, plan)
	if got.Outcome != OutcomeSkipped || got.Reason != "feature unsupported: host has no NVLink hardware" {
		t.Fatalf("decision = %#v", got)
	}
}

// TestSelectSkipsIPv6HostnameWithoutIPv6SingleStackServices keeps the target GPU evidence separate from its IPv6 feature gate.
// This lets the test prove that a supported allocation still skips when the scenario-specific cluster feature is unsupported.
func TestSelectSkipsIPv6HostnameWithoutIPv6SingleStackServices(t *testing.T) {
	plan, err := Select(Catalog, capability.NewSnapshot([]capability.Capability{
		{Name: "cluster:ipv6_single_stack", Status: capability.StatusUnsupported, Reason: "cluster rejects IPv6 single-stack Services"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	}), config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/remoteDcgmIPv6Hostname"}}})
	if err != nil {
		t.Fatal(err)
	}

	got := onlyDecision(t, plan)
	if got.Outcome != OutcomeSkipped || got.Reason != "feature unsupported: cluster rejects IPv6 single-stack Services" {
		t.Fatalf("decision = %#v", got)
	}
}

// TestSelectSkipsOnlyInapplicableScenario skips the NVSwitch link case while retaining an applicable throughput case.
func TestSelectSkipsOnlyInapplicableScenario(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvswitch", Status: capability.StatusSupported, Reason: "switch hardware present"},
		{Name: "host:nvswitch_nscq", Status: capability.StatusUnsupported, Reason: "platform uses NVSDM"},
		{Name: "host:nvswitch_nvsdm", Status: capability.StatusSupported, Reason: "platform uses NVSDM"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	})
	plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{Scenarios: []string{
		"k8s/nvswitchLink",
		"k8s/nvswitchThroughput",
	}}})
	if err != nil {
		t.Fatal(err)
	}

	decisions := decisionsByName(plan)
	if got := decisions["nvswitchLink"]; got.Outcome != OutcomeSkipped ||
		got.Reason != "scenario not applicable: platform uses NVSDM" {
		t.Fatalf("nvswitchLink = %#v", got)
	}
	if got := decisions["nvswitchThroughput"]; got.Outcome != OutcomeSelected {
		t.Fatalf("nvswitchThroughput = %#v", got)
	}
}

// TestSelectDoesNotEvaluateApplicabilityAfterUnsupportedFeature proves an unsupported feature short-circuits later applicability checks.
// The target allocation fixture is deliberately valid because allocation validation now runs before those controller feature gates.
func TestSelectDoesNotEvaluateApplicabilityAfterUnsupportedFeature(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvswitch", Status: capability.StatusUnsupported, Reason: "host has no NVSwitch hardware"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	})
	plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{
		Scenarios: []string{"k8s/nvswitchThroughput"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := onlyDecision(t, plan); got.Outcome != OutcomeSkipped ||
		got.Reason != "feature unsupported: host has no NVSwitch hardware" {
		t.Fatalf("decision = %#v", got)
	}
}

// TestSelectFailsUnknownCoarseFeature keeps an unknown host feature fatal when the caller did not opt into remote skip policy.
// Valid target allocation evidence isolates the strict local-planning behavior from the allocation-first ordering.
func TestSelectFailsUnknownCoarseFeature(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvlink_fabric", Status: capability.StatusUnknown, Source: "nvidia-smi topology", Reason: "inventory command failed"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	})
	_, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/nvlink"}}})
	if err == nil || !strings.Contains(err.Error(), "capability probe failed") ||
		!strings.Contains(err.Error(), "inventory command failed") {
		t.Fatalf("Select() error = %v", err)
	}
}

// TestSelectWithOptionsSkipsOnlyIntentionallyUnprobedCapabilities keeps remote controller limits distinct from target failures.
// An external target cannot supply controller host evidence, while a missing target allocation must still stop planning.
func TestSelectWithOptionsSkipsOnlyIntentionallyUnprobedCapabilities(t *testing.T) {
	options := SelectionOptions{SkipUnknownCapabilityPrefixes: []string{"host:"}}
	plan, err := SelectWithOptions(Catalog, capability.NewSnapshot([]capability.Capability{
		{Name: "host:profiling", Status: capability.StatusUnknown, Reason: "controller hardware was not probed"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	}), config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/profiling"}}}, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := onlyDecision(t, plan); got.Outcome != OutcomeSkipped ||
		got.Reason != "not evaluated for the selected Kubernetes target: controller hardware was not probed" {
		t.Fatalf("profiling decision = %#v", got)
	}

	_, err = SelectWithOptions(Catalog, capability.NewSnapshot([]capability.Capability{
		{Name: "host:profiling", Status: capability.StatusUnknown, Reason: "controller hardware was not probed"},
	}), config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/profiling"}}}, options)
	if err == nil || !strings.Contains(err.Error(), "cluster:gpu_resources") {
		t.Fatalf("SelectWithOptions() error = %v, want missing target allocation error before controller skip", err)
	}

	_, err = SelectWithOptions(Catalog, capability.NewSnapshot(nil), config.Config{
		TestOptions: config.TestOptions{Scenarios: []string{"k8s/default"}},
	}, SelectionOptions{SkipUnknownCapabilityPrefixes: []string{""}})
	if err == nil || !strings.Contains(err.Error(), "cluster:gpu_resources") {
		t.Fatalf("SelectWithOptions() with empty prefix error = %v, want missing target allocation error", err)
	}
}

// TestSelectFailsUnknownApplicability keeps planning fatal when a required generation check is inconclusive.
func TestSelectFailsUnknownApplicability(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvlink_fabric", Status: capability.StatusSupported, Reason: "fabric hardware present"},
		{Name: "host:nvlink5", Status: capability.StatusUnknown, Source: "nvidia-smi", Reason: "generation unavailable"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	})
	_, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/nvlinkIntegrity"}}})
	if err == nil || !strings.Contains(err.Error(), "host:nvlink5") {
		t.Fatalf("Select() error = %v", err)
	}
}

// TestSelectDryRunReportsUnknownAsSkipped marks an unprobed host capability as unevaluated in dry-run output.
func TestSelectDryRunReportsUnknownAsSkipped(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{{
		Name:   "host:nvswitch",
		Status: capability.StatusUnknown,
		Reason: "dry-run did not execute switch inventory",
	}})
	plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{
		DryRun:    true,
		Scenarios: []string{"k8s/nvswitch"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := onlyDecision(t, plan)
	if got.Outcome != OutcomeSkipped || !strings.HasPrefix(got.Reason, "not evaluated during dry-run:") {
		t.Fatalf("decision = %#v", got)
	}
}

// TestMIGConfigurationModeDoesNotChangeSelection keeps scenario selection independent of the requested MIG configuration mode.
func TestMIGConfigurationModeDoesNotChangeSelection(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{{
		Name:   "cluster:mig_resources",
		Status: capability.StatusSupported,
		Reason: "target MIG resources are available",
	}})
	for _, mode := range []string{"true", "false", "auto"} {
		t.Run(mode, func(t *testing.T) {
			plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{
				MIGConfigure: mode,
				Scenarios:    []string{"k8s/mig"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if got := onlyDecision(t, plan); got.Outcome != OutcomeSelected {
				t.Fatalf("mode %q decision = %#v", mode, got)
			}
		})
	}
}

// TestDRAConfigurationModeDoesNotChangeSelection keeps scenario selection independent of the requested DRA configuration mode.
func TestDRAConfigurationModeDoesNotChangeSelection(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{{
		Name:   "cluster:dra_resources",
		Status: capability.StatusSupported,
		Reason: "target DRA resources are available",
	}})
	for _, mode := range []config.FeatureMode{config.FeatureOn, config.FeatureOff, config.FeatureAuto} {
		plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{
			DRAConfigure: mode,
			Scenarios:    []string{"k8s/dra"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if got := onlyDecision(t, plan); got.Outcome != OutcomeSelected {
			t.Fatalf("mode %q decision = %#v", mode, got)
		}
	}
}

// TestGPUOperatorMainDRAPlansBeforeItsOwnResourcesExist requires the DRA API but allows setup to create the DRA resources.
func TestGPUOperatorMainDRAPlansBeforeItsOwnResourcesExist(t *testing.T) {
	tests := []struct {
		name       string
		capability capability.Capability
		want       Outcome
	}{
		{
			name: "DRA API unavailable",
			capability: capability.Capability{
				Name:   "cluster:dra",
				Status: capability.StatusUnsupported,
				Reason: "target does not serve the DRA API",
			},
			want: OutcomeSkipped,
		},
		{
			name: "GPU Operator setup will create DRA resources",
			capability: capability.Capability{
				Name:   "cluster:dra",
				Status: capability.StatusSupported,
				Reason: "target serves the DRA API",
			},
			want: OutcomeSelected,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caps := capability.NewSnapshot([]capability.Capability{
				test.capability,
				{
					Name:   "cluster:dra_resources",
					Status: capability.StatusUnsupported,
					Reason: "GPU Operator DRA resources are not installed yet",
				},
			})
			plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{
				GPUOperator:      "install",
				GPUOperatorChart: "/workspace/gpu-operator/deployments/gpu-operator",
				Scenarios:        []string{"k8s/gpuOperatorMainDRA"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			got := onlyDecision(t, plan)
			if got.Outcome != test.want {
				t.Fatalf("decision = %#v, want outcome %q", got, test.want)
			}
		})
	}
}

func TestFeatureModesAreDisabledByDefault(t *testing.T) {
	selected := SelectedForSuite(Catalog, SuiteK8s, config.Config{})
	for _, entry := range selected {
		if strings.HasPrefix(entry.Name, "gpuOperator") || entry.Name == "sharedGpu" || entry.Name == "customResourceNames" {
			t.Fatalf("feature-mode scenario %s selected by default", entry.Name)
		}
	}
}

func TestGPUOperatorMainScenariosRequireALocalChart(t *testing.T) {
	entry := MustFind(Catalog, SuiteK8s, "gpuOperatorMainEmbedded")

	withoutChart := config.Config{TestOptions: config.TestOptions{GPUOperator: "install"}}
	if scenarioEnabled(entry, withoutChart) {
		t.Fatal("GPU Operator main scenario is enabled without a local chart")
	}

	withChart := config.Config{TestOptions: config.TestOptions{
		GPUOperator:      "install",
		GPUOperatorChart: "/workspace/gpu-operator/deployments/gpu-operator",
	}}
	if !scenarioEnabled(entry, withChart) {
		t.Fatal("GPU Operator main scenario is not enabled with a local chart")
	}
}

func TestSharedGPUConfigurationModeControlsDefaultSelection(t *testing.T) {
	for _, test := range []struct {
		mode config.FeatureMode
		want bool
	}{
		{mode: "false"},
		{mode: "auto", want: true},
		{mode: "true", want: true},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			selected := SelectedForSuite(Catalog, SuiteK8s, config.Config{TestOptions: config.TestOptions{
				SharedGPUConfigure: test.mode,
			}})
			// Both time-sliced groups follow the same configuration mode.
			for _, name := range []string{"sharedGpu", "customResourceNames"} {
				found := false
				for _, entry := range selected {
					found = found || entry.Name == name
				}
				if found != test.want {
					t.Fatalf("%s selected = %t, want %t for mode %q", name, found, test.want, test.mode)
				}
			}
		})
	}
}

// TestHostRemoteDCGMScenariosAreSelectedByDefault includes the TCP, Unix socket, and VSOCK cases in the host suite.
func TestHostRemoteDCGMScenariosAreSelectedByDefault(t *testing.T) {
	selected := SelectedForSuite(Catalog, SuiteHost, config.Config{TestOptions: config.TestOptions{Suites: []string{"host"}}})
	want := map[string]bool{
		"host/dcgmUri":      false,
		"host/dcgmUriUnix":  false,
		"host/dcgmUriVSOCK": false,
	}
	for _, entry := range selected {
		if _, ok := want[entry.Selector()]; ok {
			want[entry.Selector()] = true
		}
	}
	for selector, found := range want {
		if !found {
			t.Fatalf("%s was not selected by default", selector)
		}
	}
}

func TestNVMLInjectionRequiresExplicitScenario(t *testing.T) {
	type testCase struct {
		name     string
		selector string
		opts     config.TestOptions
		want     bool
	}
	var tests []testCase
	for _, selector := range []string{"host/nvmlInjectionMetrics", "host/migXidCounters"} {
		tests = append(tests,
			testCase{name: "ordinary host", selector: selector, opts: config.TestOptions{Suites: []string{"host"}}},
			testCase{name: "yaml without explicit scenario", selector: selector, opts: config.TestOptions{Suites: []string{"host"}, DCGMNVMLInjectionYAML: "fixture.yaml"}},
			testCase{name: "explicit configured scenario", selector: selector, opts: config.TestOptions{Scenarios: []string{selector}, DCGMNVMLInjectionYAML: "fixture.yaml"}, want: true},
		)
	}
	for _, test := range tests {
		t.Run(test.selector+"/"+test.name, func(t *testing.T) {
			selected := SelectedForSuite(Catalog, SuiteHost, config.Config{TestOptions: test.opts})
			found := false
			for _, entry := range selected {
				found = found || entry.Selector() == test.selector
			}
			if found != test.want {
				t.Fatalf("%s selected = %t, want %t", test.selector, found, test.want)
			}
		})
	}
}

func TestNVSDMMockMetricsRequiresExplicitScenario(t *testing.T) {
	tests := []struct {
		name string
		opts config.TestOptions
		want bool
	}{
		{name: "ordinary host", opts: config.TestOptions{Suites: []string{"host"}}},
		{name: "yaml without explicit scenario", opts: config.TestOptions{Suites: []string{"host"}, DCGMNVSDMMockYAML: "fixture.yaml"}},
		{
			name: "explicit configured scenario",
			opts: config.TestOptions{
				Scenarios:         []string{"host/nvsdmMockMetrics"},
				DCGMNVSDMMockYAML: "fixture.yaml",
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected := SelectedForSuite(Catalog, SuiteHost, config.Config{TestOptions: tt.opts})
			found := false
			for _, entry := range selected {
				found = found || entry.Selector() == "host/nvsdmMockMetrics"
			}
			if found != tt.want {
				t.Fatalf("host/nvsdmMockMetrics selected = %t, want %t", found, tt.want)
			}
		})
	}
}

// TestPlanSummaryExplainsTieredSkip preserves the applicability reason and rejects the obsolete "waived" wording.
func TestPlanSummaryExplainsTieredSkip(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvswitch", Status: capability.StatusSupported, Reason: "switch present"},
		{Name: "host:nvswitch_nscq", Status: capability.StatusUnsupported, Reason: "platform uses NVSDM"},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported, Reason: "target has an ordinary GPU"},
	})
	plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/nvswitchLink"}}})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := plan.WriteSummary(&output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "scenario not applicable: platform uses NVSDM") {
		t.Fatalf("summary =\n%s", output.String())
	}
	if strings.Contains(output.String(), "waived") {
		t.Fatalf("summary uses obsolete waived terminology:\n%s", output.String())
	}
}

// TestKubernetesAllocationUsesTargetCapability verifies ordinary GPU, MIG, and DRA scenarios select from target evidence alone.
// It is the CPU-level contract behind external-cluster planning, where the controller may not have a GPU.
func TestKubernetesAllocationUsesTargetCapability(t *testing.T) {
	tests := []struct {
		name       string
		scenario   string
		capability string
	}{
		{name: "ordinary GPU", scenario: "k8s/default", capability: "cluster:gpu_resources"},
		{name: "MIG", scenario: "k8s/mig", capability: "cluster:mig_resources"},
		{name: "DRA", scenario: "k8s/dra", capability: "cluster:dra_resources"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caps := capability.NewSnapshot([]capability.Capability{{
				Name:   test.capability,
				Status: capability.StatusSupported,
				Reason: "selected target reports the required allocation",
			}})
			plan, err := Select(Catalog, caps, config.Config{TestOptions: config.TestOptions{Scenarios: []string{test.scenario}}})
			if err != nil {
				t.Fatal(err)
			}
			if got := onlyDecision(t, plan); got.Outcome != OutcomeSelected {
				t.Fatalf("decision = %#v, want selected", got)
			}
		})
	}
}

func TestSkipSelectedLabelRemovesExecutionGroupEntry(t *testing.T) {
	plan := Plan{
		Scenarios: []PlannedScenario{
			{Scenario: Scenario{Name: "fieldUnsupported"}, Outcome: OutcomeSelected},
		},
		EmbeddedLabels: []string{"fieldUnsupported"},
		Groups:         []PlanGroup{{Name: "embedded-hardware", Labels: []string{"fieldUnsupported", "nvlink"}}},
	}

	got := SkipSelectedLabel(plan, "fieldUnsupported", "scenario not applicable")
	if got.Scenarios[0].Outcome != OutcomeSkipped {
		t.Fatalf("outcome = %q, want skipped", got.Scenarios[0].Outcome)
	}
	if len(got.EmbeddedLabels) != 0 {
		t.Fatalf("embedded labels = %#v, want none", got.EmbeddedLabels)
	}
	if len(got.Groups) != 1 || len(got.Groups[0].Labels) != 1 || got.Groups[0].Labels[0] != "nvlink" {
		t.Fatalf("groups = %#v, want nvlink only", got.Groups)
	}
}

func TestGroupSelectedPreservesFirstOccurrenceAndExactLabels(t *testing.T) {
	selected := []Scenario{
		{Suite: SuiteContainer, Name: "first", Group: GroupContainerBaseline},
		{Suite: SuiteContainer, Name: "remote", Group: GroupContainerRemoteDCGM},
		{Suite: SuiteContainer, Name: "second", Group: GroupContainerBaseline},
	}
	groups := GroupSelected(selected)
	if len(groups) != 2 {
		t.Fatalf("groups = %#v, want two groups", groups)
	}
	if groups[0].Name != GroupContainerBaseline || !sameStrings(groups[0].Labels, []string{"first", "second"}) {
		t.Fatalf("first group = %#v", groups[0])
	}
	if groups[1].Name != GroupContainerRemoteDCGM || !sameStrings(groups[1].Labels, []string{"remote"}) {
		t.Fatalf("second group = %#v", groups[1])
	}
}

// TestNVSwitchScenariosUseDedicatedRuntimeGroup keeps CDI setup away from other hardware tests.
func TestNVSwitchScenariosUseDedicatedRuntimeGroup(t *testing.T) {
	for _, name := range []string{"nvswitch", "nvswitchLink", "nvswitchThroughput"} {
		entry := MustFind(Catalog, SuiteK8s, name)
		if entry.Group != "embedded-nvswitch" {
			t.Fatalf("%s group = %q, want embedded-nvswitch", entry.Selector(), entry.Group)
		}
	}
	if entry := MustFind(Catalog, SuiteK8s, "profiling"); entry.Group != "embedded-hardware" {
		t.Fatalf("%s group = %q, want embedded-hardware", entry.Selector(), entry.Group)
	}
}

func onlyDecision(t *testing.T, plan Plan) PlannedScenario {
	t.Helper()
	if len(plan.Scenarios) != 1 {
		t.Fatalf("planned scenarios = %d, want 1", len(plan.Scenarios))
	}
	return plan.Scenarios[0]
}

func decisionsByName(plan Plan) map[string]PlannedScenario {
	decisions := make(map[string]PlannedScenario, len(plan.Scenarios))
	for _, decision := range plan.Scenarios {
		decisions[decision.Scenario.Name] = decision
	}
	return decisions
}
