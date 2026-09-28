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

package resultmarker

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	"github.com/onsi/gomega"
)

var _ = Register(
	func() bool { return true },
	map[string]string{"selected": "dcgm_exporter_e2e_selected"},
)

var _ = ginkgo.Describe("result marker lifecycle", ginkgo.Label("selected"), func() {
	ginkgo.It("passes", func() {})
	ginkgo.It("waives runtime skips", func() { ginkgo.Skip("runtime prerequisite unavailable") })
})

var _ = ginkgo.Describe("filtered result marker lifecycle", ginkgo.Label("filtered"), func() {
	ginkgo.It("does not start", func() { ginkgo.Fail("filtered spec unexpectedly ran") })
})

// TestLifecycleMarkers verifies hooks mark executed specs and ignore filtered specs.
func TestLifecycleMarkers(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	suiteConfig.LabelFilter = "selected"

	var buf bytes.Buffer
	previousOutput := output
	output = &buf
	defer func() { output = previousOutput }()

	if !ginkgo.RunSpecs(t, "Result marker lifecycle", suiteConfig, reporterConfig) {
		t.Fatal("Ginkgo suite failed")
	}

	got := buf.String()
	for _, want := range []string{
		"&&&& RUNNING dcgm_exporter_e2e_selected_passes\n",
		"&&&& PASSED dcgm_exporter_e2e_selected_passes\n",
		"&&&& RUNNING dcgm_exporter_e2e_selected_waives_runtime_skips\n",
		"&&&& WAIVED dcgm_exporter_e2e_selected_waives_runtime_skips\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("marker output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "does_not_start") || strings.Contains(got, "SKIPPED") {
		t.Fatalf("marker output included filtered or pre-execution status:\n%s", got)
	}
	assertPairedLifecycles(t, got)
}

// TestMarkerLineContract verifies exact standalone marker text for Ginkgo outcomes.
func TestMarkerLineContract(t *testing.T) {
	var buf bytes.Buffer
	previousOutput := output
	output = &buf
	defer func() { output = previousOutput }()

	states := []struct {
		name     string
		state    types.SpecState
		terminal string
	}{
		{name: "passed", state: types.SpecStatePassed, terminal: "PASSED"},
		{name: "failed", state: types.SpecStateFailed, terminal: "FAILED"},
		{name: "waived", state: types.SpecStateSkipped, terminal: "WAIVED"},
	}
	for _, test := range states {
		name := "dcgm_exporter_e2e_" + test.name
		line("RUNNING", name)
		line(terminalStatus(ginkgo.SpecReport{State: test.state}), name)
	}

	want := strings.Join([]string{
		"&&&& RUNNING dcgm_exporter_e2e_passed",
		"&&&& PASSED dcgm_exporter_e2e_passed",
		"&&&& RUNNING dcgm_exporter_e2e_failed",
		"&&&& FAILED dcgm_exporter_e2e_failed",
		"&&&& RUNNING dcgm_exporter_e2e_waived",
		"&&&& WAIVED dcgm_exporter_e2e_waived",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Fatalf("marker output = %q, want %q", got, want)
	}
	assertPairedLifecycles(t, buf.String())
}

// TestSpecName verifies catalog labels and Ginkgo text form stable marker names.
func TestSpecName(t *testing.T) {
	report := ginkgo.SpecReport{
		LeafNodeText:             "should install dcgm-exporter helm chart [default]",
		ContainerHierarchyLabels: [][]string{{"default"}},
	}

	got := specName(report, map[string]string{"default": "dcgm_exporter_e2e_default"})
	want := "dcgm_exporter_e2e_default_should_install_dcgm_exporter_helm_chart"
	if got != want {
		t.Fatalf("specName() = %q, want %q", got, want)
	}
}

// TestSuiteSpecNamesMatchCatalog keeps derived host and container names catalog-compatible.
func TestSuiteSpecNamesMatchCatalog(t *testing.T) {
	for _, entry := range scenario.Catalog {
		if entry.Suite != scenario.SuiteHost && entry.Suite != scenario.SuiteContainer {
			continue
		}

		report := ginkgo.SpecReport{ContainerHierarchyLabels: [][]string{{entry.ResultName}}}
		got := suiteSpecName(report, string(entry.Suite))
		want, ok := entry.MarkerBaseName()
		if !ok {
			t.Fatalf("%s has no marker base", entry.Selector())
		}
		if got != want {
			t.Fatalf("suiteSpecName(%s) = %q, want %q", entry.Selector(), got, want)
		}
	}
}

// TestEnabledFromEnvUsesFirstConfiguredName verifies suite overrides take precedence.
func TestEnabledFromEnvUsesFirstConfiguredName(t *testing.T) {
	t.Setenv("E2E_RESULT_MARKERS", "true")
	t.Setenv("E2E_SUITE_RESULT_MARKERS", "false")
	if EnabledFromEnv("E2E_SUITE_RESULT_MARKERS", "E2E_RESULT_MARKERS") {
		t.Fatal("EnabledFromEnv() = true, want suite-level false override")
	}
}

// TestTerminalStatus verifies every Ginkgo outcome receives a DVS terminal state.
func TestTerminalStatus(t *testing.T) {
	tests := map[types.SpecState]string{
		types.SpecStatePassed:   "PASSED",
		types.SpecStateSkipped:  "WAIVED",
		types.SpecStateFailed:   "FAILED",
		types.SpecStatePanicked: "FAILED",
	}
	for state, want := range tests {
		if got := terminalStatus(ginkgo.SpecReport{State: state}); got != want {
			t.Fatalf("terminalStatus(%v) = %q, want %q", state, got, want)
		}
	}
}

// TestSafeText verifies marker diagnostics cannot spill onto another line.
func TestSafeText(t *testing.T) {
	got := safeText("dcgm\texporter\ne2e\rmarker")
	want := "dcgm exporter e2e marker"
	if got != want {
		t.Fatalf("safeText() = %q, want %q", got, want)
	}
}

// assertPairedLifecycles requires one terminal marker for every RUNNING marker.
func assertPairedLifecycles(t *testing.T, text string) {
	t.Helper()
	started := map[string]int{}
	terminal := map[string]int{}
	for _, value := range strings.Split(strings.TrimSpace(text), "\n") {
		fields := strings.Fields(value)
		if len(fields) != 3 || fields[0] != "&&&&" {
			t.Fatalf("invalid marker line %q", value)
		}
		switch fields[1] {
		case "RUNNING":
			started[fields[2]]++
		case "PASSED", "FAILED", "SKIPPED", "WAIVED":
			terminal[fields[2]]++
		default:
			t.Fatalf("unexpected marker status in %q", value)
		}
	}
	for name, count := range started {
		if count != 1 || terminal[name] != 1 {
			t.Fatalf("marker lifecycle %q has %d RUNNING and %d terminal lines", name, count, terminal[name])
		}
	}
	for name := range terminal {
		if started[name] != 1 {
			t.Fatalf("terminal marker %q has no matching RUNNING line", name)
		}
	}
}
