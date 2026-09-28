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
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

func TestSelectSkipsUnavailableCapabilities(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvlink", Status: capability.StatusUnsupported, Reason: "active NVLink evidence was not detected"},
	})

	plan, err := Select(Catalog, caps, config.Config{})
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range plan.Scenarios {
		if item.Scenario.Name != "nvlink" {
			continue
		}
		if item.Outcome != OutcomeSkipped {
			t.Fatalf("nvlink outcome = %s, want skipped", item.Outcome)
		}
		if item.Reason != "active NVLink evidence was not detected" {
			t.Fatalf("nvlink reason = %q", item.Reason)
		}
		return
	}
	t.Fatal("nvlink scenario not planned")
}

func TestSelectDisablesFeatureModesByDefault(t *testing.T) {
	plan, err := Select(Catalog, capability.NewSnapshot(nil), config.Config{})
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range plan.Scenarios {
		if strings.HasPrefix(item.Scenario.Name, "gpuOperator") || strings.HasPrefix(item.Scenario.Name, "failure") {
			t.Fatalf("feature-mode scenario %s planned by default", item.Scenario.Name)
		}
	}
}

func TestHostDCGMURIIsSelectedByDefault(t *testing.T) {
	defaultHost := SelectedForSuite(Catalog, SuiteHost, config.Config{Tests: config.Tests{Suites: []string{"host"}}})
	found := false
	for _, entry := range defaultHost {
		if entry.Selector() == "host/dcgmUri" {
			found = true
		}
	}
	if !found {
		t.Fatal("host/dcgmUri was not selected by default")
	}
}

func TestNVMLInjectionMetricsRequiresYAML(t *testing.T) {
	tests := []struct {
		name string
		opts config.Tests
		want bool
	}{
		{name: "ordinary host", opts: config.Tests{Suites: []string{"host"}}},
		{name: "yaml", opts: config.Tests{Suites: []string{"host"}, DCGMNVMLInjectionYAML: "fixture.yaml"}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected := SelectedForSuite(Catalog, SuiteHost, config.Config{Tests: tt.opts})
			found := false
			for _, entry := range selected {
				found = found || entry.Selector() == "host/nvmlInjectionMetrics"
			}
			if found != tt.want {
				t.Fatalf("host/nvmlInjectionMetrics selected = %t, want %t", found, tt.want)
			}
		})
	}
}

func TestPlanSummaryUsesSkippedNotWaived(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvlink", Status: capability.StatusUnsupported, Reason: "active NVLink evidence was not detected"},
	})
	plan, err := Select(Catalog, caps, config.Config{})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := plan.WriteSummary(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "active NVLink evidence was not detected:\n    k8s/nvlink") {
		t.Fatalf("summary does not show skipped outcome:\n%s", out.String())
	}
	if strings.Contains(out.String(), "waived") {
		t.Fatalf("summary contains waived for pre-execution skip:\n%s", out.String())
	}
}

func TestSelectFailsRequiredUnavailableScenarios(t *testing.T) {
	tests := []struct {
		name string
		opts config.Tests
		cap  capability.Capability
		want string
	}{
		{
			name: "explicit MIG scenario",
			opts: config.Tests{Scenarios: []string{"k8s/mig"}, MIGConfigure: "auto"},
			cap:  capability.Capability{Name: "host:mig", Status: capability.StatusUnsupported, Reason: "MIG unavailable"},
			want: "k8s/mig required but unsupported: MIG unavailable",
		},
		{
			name: "explicit hardware scenario",
			opts: config.Tests{Scenarios: []string{"k8s/nvlink"}},
			cap:  capability.Capability{Name: "host:nvlink", Status: capability.StatusUnsupported, Reason: "NVLink unavailable"},
			want: "k8s/nvlink required but unsupported: NVLink unavailable",
		},
		{
			name: "explicit DRA configure",
			opts: config.Tests{Scenarios: []string{"k8s/dra"}, DRAConfigure: "true"},
			cap:  capability.Capability{Name: "cluster:dra", Status: capability.StatusUnsupported, Reason: "DRA unavailable"},
			want: "k8s/dra required but unsupported: DRA unavailable",
		},
		{
			name: "explicit shared GPU configure",
			opts: config.Tests{Scenarios: []string{"k8s/sharedGpu"}, SharedGPUConfigure: "true"},
			cap:  capability.Capability{Name: "cluster:shared_gpu", Status: capability.StatusUnsupported, Reason: "shared GPU unavailable"},
			want: "k8s/sharedGpu required but unsupported: shared GPU unavailable",
		},
		{
			name: "explicit GPU Operator",
			opts: config.Tests{Scenarios: []string{"k8s/gpuOperatorChart"}, GPUOperator: "existing"},
			cap:  capability.Capability{Name: "cluster:gpu_operator", Status: capability.StatusUnsupported, Reason: "GPU Operator unavailable"},
			want: "k8s/gpuOperatorChart required but unsupported: GPU Operator unavailable",
		},
		{
			name: "explicit failure injection",
			opts: config.Tests{Scenarios: []string{"k8s/failureXid"}, DCGMFailureInjection: "true"},
			cap:  capability.Capability{Name: "dcgm:failure_injection", Status: capability.StatusUnsupported, Reason: "failure injection unavailable"},
			want: "k8s/failureXid required but unsupported: failure injection unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Select(Catalog, capability.NewSnapshot([]capability.Capability{tt.cap}), config.Config{Tests: tt.opts})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Select() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSelectAutoFailureInjectionSkipsUnavailableScenarios(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "dcgm:failure_injection", Status: capability.StatusUnsupported, Reason: "failure injection unavailable"},
		{Name: "dcgm:failure_injection_nvlink_crc", Status: capability.StatusUnsupported, Reason: "failure injection unavailable"},
		{Name: "cluster:standalone_dcgm_resources", Status: capability.StatusSupported, Reason: "cluster has GPU resources"},
	})
	plan, err := Select(Catalog, caps, config.Config{Tests: config.Tests{DCGMFailureInjection: "auto"}})
	if err != nil {
		t.Fatalf("Select() error = %v, want nil", err)
	}
	want := map[string]bool{}
	for _, entry := range Catalog {
		if strings.HasPrefix(entry.Selector(), "k8s/failure") {
			want[entry.Selector()] = true
		}
	}
	for _, item := range plan.Scenarios {
		if want[item.Scenario.Selector()] {
			if item.Outcome != OutcomeSkipped {
				t.Fatalf("%s outcome = %s, want skipped", item.Scenario.Selector(), item.Outcome)
			}
			delete(want, item.Scenario.Selector())
		}
	}
	if len(want) != 0 {
		t.Fatalf("failure injection scenarios not planned in auto mode: %#v", want)
	}
}

func TestSelectSkipsExplicitScenarioWhenDCGMReportsNotSupported(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "dcgm:failure_injection_nvlink_crc", Status: capability.StatusUnsupported, Reason: "DCGM field 409 returned DCGM_FT_INT64_NOT_SUPPORTED", DCGMNotSupported: true},
		{Name: "cluster:standalone_dcgm_resources", Status: capability.StatusSupported, Reason: "cluster has GPU resources"},
	})

	plan, err := Select(Catalog, caps, config.Config{Tests: config.Tests{
		Scenarios:            []string{"k8s/failureNvlinkHealth"},
		DCGMFailureInjection: "auto",
	}})
	if err != nil {
		t.Fatalf("Select() error = %v, want nil", err)
	}
	if len(plan.Scenarios) != 1 {
		t.Fatalf("planned scenarios = %d, want 1", len(plan.Scenarios))
	}
	got := plan.Scenarios[0]
	if got.Outcome != OutcomeSkipped {
		t.Fatalf("outcome = %s, want skipped", got.Outcome)
	}
	if !strings.Contains(got.Reason, "DCGM_FT_INT64_NOT_SUPPORTED") {
		t.Fatalf("reason = %q, want decoded sentinel evidence", got.Reason)
	}
}

func TestSelectFailsExplicitScenarioWhenPrerequisiteFails(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "dcgm:failure_injection_nvlink_crc", Status: capability.StatusUnsupported, Reason: "DCGM image pull failed"},
		{Name: "cluster:standalone_dcgm_resources", Status: capability.StatusSupported, Reason: "cluster has GPU resources"},
	})

	_, err := Select(Catalog, caps, config.Config{Tests: config.Tests{
		Scenarios:            []string{"k8s/failureNvlinkHealth"},
		DCGMFailureInjection: "auto",
	}})
	if err == nil || !strings.Contains(err.Error(), "DCGM image pull failed") {
		t.Fatalf("Select() error = %v, want image prerequisite failure", err)
	}
}

func TestSelectAutoMIGSkipsUnavailableScenarios(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:mig", Status: capability.StatusUnsupported, Reason: "MIG instances are not present"},
	})
	plan, err := Select(Catalog, caps, config.Config{Tests: config.Tests{MIGConfigure: "auto"}})
	if err != nil {
		t.Fatalf("Select() error = %v, want nil", err)
	}
	for _, item := range plan.Scenarios {
		if item.Scenario.Selector() != "k8s/mig" {
			continue
		}
		if item.Outcome != OutcomeSkipped {
			t.Fatalf("%s outcome = %s, want skipped", item.Scenario.Selector(), item.Outcome)
		}
		return
	}
	t.Fatal("k8s/mig was not planned")
}

func TestSelectMaximumCoverageFailsUnknownCapability(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "host:profiling", Status: capability.StatusUnknown, Reason: "DCGM profiling probe was unavailable"},
	})
	_, err := Select(Catalog, caps, config.Config{Tests: config.Tests{
		MIGConfigure:         "true",
		DRAConfigure:         "true",
		SharedGPUConfigure:   "true",
		DCGMFailureInjection: "true",
		GPUOperator:          "install",
	}})
	if err == nil || !strings.Contains(err.Error(), "k8s/profiling required but unsupported: DCGM profiling probe was unavailable") {
		t.Fatalf("Select() error = %v, want profiling probe failure", err)
	}
}

func TestSelectDryRunDoesNotFailRequiredUnavailableScenarios(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{{Name: "host:mig", Status: capability.StatusUnsupported, Reason: "MIG unavailable"}})
	_, err := Select(Catalog, caps, config.Config{Tests: config.Tests{DryRun: true, Scenarios: []string{"k8s/mig"}, MIGConfigure: "auto"}})
	if err != nil {
		t.Fatalf("Select() error = %v, want nil", err)
	}
}

func TestDryRunDecisionFixture(t *testing.T) {
	want := fixtureDryRunDecisions(t)
	plan, err := Select(Catalog, fixtureCapabilitySnapshot(), config.Config{Tests: config.Tests{
		DCGMFailureInjection: "auto",
		GPUOperator:          "auto",
	}})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]Outcome{}
	for _, item := range plan.Scenarios {
		got[item.Scenario.Selector()] = item.Outcome
	}

	if len(got) != len(want) {
		t.Fatalf("planned scenario count = %d, want %d\ngot: %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for selector, outcome := range want {
		if got[selector] != outcome {
			t.Fatalf("%s outcome = %s, want %s", selector, got[selector], outcome)
		}
	}
}

func fixtureDryRunDecisions(t *testing.T) map[string]Outcome {
	t.Helper()
	data, err := os.ReadFile("testdata/dry-run.normalized.txt")
	if err != nil {
		t.Fatal(err)
	}

	decisions := map[string]Outcome{}
	inExecutionGroups := false
	inSkippedScenarios := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "Execution groups:":
			inExecutionGroups = true
			inSkippedScenarios = false
			continue
		case line == "Skipped scenarios:":
			inExecutionGroups = false
			inSkippedScenarios = true
			continue
		case !inExecutionGroups && !inSkippedScenarios:
			continue
		}

		trimmed := strings.TrimSpace(line)
		if inExecutionGroups && strings.HasPrefix(line, "    ") {
			for _, label := range strings.Split(trimmed, ",") {
				label = strings.TrimSpace(label)
				if label != "" {
					decisions["k8s/"+label] = OutcomeSelected
				}
			}
			continue
		}
		if inSkippedScenarios && strings.HasPrefix(trimmed, "k8s/") {
			decisions[trimmed] = OutcomeSkipped
		}
	}
	return decisions
}

func fixtureCapabilitySnapshot() capability.Snapshot {
	return capability.NewSnapshot([]capability.Capability{
		{Name: "host:profiling", Status: capability.StatusUnknown},
		{Name: "host:p2p", Status: capability.StatusUnsupported},
		{Name: "host:unsupported_field", Status: capability.StatusUnknown},
		{Name: "host:nvlink", Status: capability.StatusUnsupported},
		{Name: "host:mig", Status: capability.StatusUnsupported},
		{Name: "host:mixed_mig", Status: capability.StatusUnsupported},
		{Name: "host:mig_instance_entity", Status: capability.StatusUnsupported},
		{Name: "cluster:dra", Status: capability.StatusUnknown},
		{Name: "cluster:shared_gpu", Status: capability.StatusUnknown},
		{Name: "host:nvswitch", Status: capability.StatusUnknown},
		{Name: "host:grace_cpu", Status: capability.StatusUnsupported},
		{Name: "host:c2c", Status: capability.StatusUnsupported},
		{Name: "dcgm:remote_dcgm", Status: capability.StatusUnknown},
		{Name: "dcgm:failure_injection", Status: capability.StatusUnsupported},
		{Name: "dcgm:failure_injection_nvlink_crc", Status: capability.StatusUnsupported},
	})
}
