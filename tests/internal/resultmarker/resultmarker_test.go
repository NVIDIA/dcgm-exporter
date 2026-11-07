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
	"slices"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	"github.com/onsi/gomega"
)

// Select each marker contract by native Ginkgo labels to emit one stream per spec.
var _ = Register(
	func() bool { return !slices.Contains(ginkgo.CurrentSpecReport().Labels(), "imageStartup") },
	map[string]string{"selected": "dcgm_exporter_e2e_selected"},
)

var _ = RegisterSuite(
	func() bool { return slices.Contains(ginkgo.CurrentSpecReport().Labels(), "imageStartup") },
	"container",
)

// These callbacks exercise a no-op pass and a runtime skip.
var _ = ginkgo.Describe("result marker lifecycle", ginkgo.Label("selected"), func() {
	ginkgo.It("passes", func() {})
	ginkgo.It("waives runtime skips", func() { ginkgo.Skip("runtime prerequisite unavailable") })
})

// This callback fails if label filtering unexpectedly starts the spec.
var _ = ginkgo.Describe("filtered result marker lifecycle", ginkgo.Label("filtered"), func() {
	ginkgo.It("does not start", func() { ginkgo.Fail("filtered spec unexpectedly ran") })
})

// These no-op specs exercise native marker naming, not container validation.
var _ = ginkgo.Describe("image result marker lifecycle", ginkgo.Serial, ginkgo.Label("imageStartup"), func() {
	ginkgo.It("should validate [distroless] image", func() {})
})

// TestLifecycleMarkers verifies hooks mark executed specs and ignore filtered specs.
func TestLifecycleMarkers(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	suiteConfig.LabelFilter = "selected || imageStartup"

	var buf bytes.Buffer
	previousOutput := output
	output = &buf
	// Restore the shared writer after this test's capture.
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
		"&&&& RUNNING dcgm_exporter_e2e_container_image_startup_should_validate_distroless_image\n",
		"&&&& PASSED dcgm_exporter_e2e_container_image_startup_should_validate_distroless_image\n",
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
	// Restore the shared writer after this test's capture.
	defer func() { output = previousOutput }()

	// Map each executed outcome to its reserved terminal marker.
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
	want := "dcgm_exporter_e2e_default_should_install_dcgm_exporter_helm_chart_default"
	if got != want {
		t.Fatalf("specName() = %q, want %q", got, want)
	}
}

// TestSuiteSpecNamesMatchCatalog keeps derived host and container names catalog-compatible.
func TestSuiteSpecNamesMatchCatalog(t *testing.T) {
	// Hard-coded from internal/e2e/scenario.Catalog MarkerName values so this
	// package does not import internal/e2e. Update when host/container scenarios change.
	want := map[string]map[string]string{
		"host": {
			"startupMetrics":         "dcgm_exporter_e2e_host_startup_metrics",
			"configFile":             "dcgm_exporter_e2e_host_config_file",
			"configInspection":       "dcgm_exporter_e2e_host_config_inspection",
			"configEnrichment":       "dcgm_exporter_e2e_host_config_enrichment",
			"configPrecedence":       "dcgm_exporter_e2e_host_config_precedence",
			"configServesMetrics":    "dcgm_exporter_e2e_host_config_serves_metrics",
			"configStartupErrors":    "dcgm_exporter_e2e_host_config_startup_errors",
			"nvmlInjectionMetrics":   "dcgm_exporter_e2e_host_nvml_injection_metrics",
			"migXidCounters":         "dcgm_exporter_e2e_host_mig_xid_counters",
			"nvsdmMockMetrics":       "dcgm_exporter_e2e_host_nvsdm_mock_metrics",
			"watchGroups":            "dcgm_exporter_e2e_host_watch_groups",
			"latestValueBatching":    "dcgm_exporter_e2e_host_latest_value_batching",
			"oversizedFieldGroup":    "dcgm_exporter_e2e_host_oversized_field_group",
			"runtimeContainerLabels": "dcgm_exporter_e2e_host_runtime_container_labels",
			"hpcJobMapping":          "dcgm_exporter_e2e_host_hpc_job_mapping",
			"startupTLS":             "dcgm_exporter_e2e_host_startup_tls",
			"reload":                 "dcgm_exporter_e2e_host_reload",
			"profilingReload":        "dcgm_exporter_e2e_host_profiling_reload",
			"gpuLinkFields":          "dcgm_exporter_e2e_host_gpu_link_fields",
			"dcgmUri":                "dcgm_exporter_e2e_host_dcgm_uri",
			"dcgmUriUnix":            "dcgm_exporter_e2e_host_dcgm_uri_unix",
			"dcgmUriVSOCK":           "dcgm_exporter_e2e_host_dcgm_uri_vsock",
			"ipv6Listen":             "dcgm_exporter_e2e_host_ipv6_listen",
			"gpuBindUnbindWatch":     "dcgm_exporter_e2e_host_gpu_bind_unbind_watch",
			"systemdSocket":          "dcgm_exporter_e2e_host_systemd_socket",
			"gpuSelection":           "dcgm_exporter_e2e_host_gpu_selection",
			"modelNameLabels":        "dcgm_exporter_e2e_host_model_name_labels",
			"scrapeLimit":            "dcgm_exporter_e2e_host_scrape_limit",
			"httpTimeouts":           "dcgm_exporter_e2e_host_http_timeouts",
			"logLevels":              "dcgm_exporter_e2e_host_log_levels",
			"injectedEventCounts":    "dcgm_exporter_e2e_host_injected_event_counts",
			"gpuLoadUtilization":     "dcgm_exporter_e2e_host_gpu_load_utilization",
			"gpuBindUnbindEvents":    "dcgm_exporter_e2e_host_gpu_bind_unbind_events",
			"migGpuLevelFields":      "dcgm_exporter_e2e_host_mig_gpu_level_fields",
		},
		"container": {
			"imageStartup":           "dcgm_exporter_e2e_container_image_startup",
			"configuration":          "dcgm_exporter_e2e_container_configuration",
			"pprofRequiresWebConfig": "dcgm_exporter_e2e_container_pprof_requires_web_config",
			"invalidDeviceSelectors": "dcgm_exporter_e2e_container_invalid_device_selectors",
			"remoteDcgmUri":          "dcgm_exporter_e2e_container_remote_dcgm_uri",
		},
	}

	for suite, labels := range want {
		for label, marker := range labels {
			report := ginkgo.SpecReport{ContainerHierarchyLabels: [][]string{{label}}}
			got := suiteSpecName(report, suite)
			if got != marker {
				t.Fatalf("suiteSpecName(%s/%s) = %q, want %q", suite, label, got, marker)
			}
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

// TestTerminalStatus verifies every Ginkgo outcome receives a terminal marker state.
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
