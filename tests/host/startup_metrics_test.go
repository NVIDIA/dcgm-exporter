/*
 * Copyright (c) 2023, NVIDIA CORPORATION.  All rights reserved.
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

package host

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvmlinjection"
)

const dcgmRequiresRootStatusCode = -29

const (
	nvmlInjectionCollectInterval = time.Second
	nvmlInjectionWarmup          = 2 * nvmlInjectionCollectInterval
)

// runStartAndReadMetrics starts the exporter on the host and verifies it serves parseable metrics.
func runStartAndReadMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	collectorsPath := defaultCollectorsFile(t)
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, metricsResp := startFastCSVExporterAndWait(
		t,
		metricsURL,
		collectorsPath,
		"--address", fmt.Sprintf(":%d", port),
	)

	validateHostDefaultMetrics(t, metricsResp)
	validateHostGPUInventory(t, metricsURL, process, collectorsPath)
}

// runStartWithDefaultPreset starts and validates the exporter using the shipped CSV.
func runStartWithDefaultPreset(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	collectorsPath := canonicalDefaultCollectorsFile(t)
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, metricsResp := startFastCSVExporterAndWait(
		t,
		metricsURL,
		collectorsPath,
		"--address", fmt.Sprintf(":%d", port),
	)

	validateHostMetricsFromCollectors(t, metricsResp, collectorsPath)
	validateHostGPUInventory(t, metricsURL, process, collectorsPath)
}

// runNVMLInjectionMetrics compares exporter output with direct configured-DCGM telemetry.
func runNVMLInjectionMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	injectionPath := strings.TrimSpace(os.Getenv("NVML_YAML_FILE"))
	if injectionPath == "" {
		t.Skip("skipping NVML injection contract test without an injection YAML")
	}
	require.NotEmpty(t, strings.TrimSpace(*dcgmProbeBinaryFlag), "-dcgm-probe-binary is required")
	require.NotEmpty(t, strings.TrimSpace(*dcgmFieldsFileFlag), "-dcgm-fields-file is required")
	contractPath := filepath.Join(t.TempDir(), "dcgm-contract.json")
	// #nosec G204 -- both paths are explicit E2E harness inputs validated before the host suite starts.
	cmd := exec.Command(*dcgmProbeBinaryFlag, "nvml-injection", "--field-names", *dcgmFieldsFileFlag, "--output", contractPath)
	cmd.Env = hostExporterEnv()
	cmd.Stdout = io.Discard
	var probeStderr bytes.Buffer
	cmd.Stderr = &probeStderr
	if err := cmd.Run(); err != nil {
		require.NoError(t, err, "query direct injected DCGM contract (%s)", safeProbeFailureSummary(probeStderr.String()))
	}
	contractFile, err := os.Open(contractPath)
	require.NoError(t, err)
	defer contractFile.Close()
	contract, err := nvmlinjection.Read(contractFile)
	require.NoError(t, err)
	require.NoError(t, contract.ValidateComputeInstanceSamples())
	if contract.DeviceCount == 0 {
		port := getRandomAvailablePort(t)
		process, healthResp := startFastExporterAndWaitForHealth(
			t,
			fmt.Sprintf("http://localhost:%d/health", port),
			"--collectors", defaultCollectorsFile(t),
			"--address", fmt.Sprintf(":%d", port),
		)
		defer process.terminate(t)
		require.Equal(t, "OK", strings.TrimSpace(healthResp))
		t.Log("dcgm-exporter health check passed without direct DCGM GPU entities")
		return
	}

	for index, batch := range contract.Batches() {
		var collectors bytes.Buffer
		require.NoError(t, nvmlinjection.WriteCollectors(&collectors, batch))
		collectorsPath := filepath.Join(t.TempDir(), fmt.Sprintf("nvml-injection-collectors-%d.csv", index))
		require.NoError(t, os.WriteFile(collectorsPath, collectors.Bytes(), 0o600))

		port := getRandomAvailablePort(t)
		metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
		healthURL := fmt.Sprintf("http://localhost:%d/health", port)
		func() {
			process := startExporterProcess(
				t,
				"--collectors", collectorsPath,
				"--address", fmt.Sprintf(":%d", port),
				"--collect-interval", fmt.Sprint(nvmlInjectionCollectInterval.Milliseconds()),
			)
			defer process.terminate(t)

			healthResp, err := retryNonEmptyResponse(healthURL, process)
			require.NoError(t, err)
			require.Equal(t, "OK", strings.TrimSpace(healthResp))
			// Profiling fields require a baseline and current sample. The direct-DCGM
			// oracle forces an update after two watch intervals; this black-box process
			// has no update hook, so allow its injected cache manager to update first.
			time.Sleep(nvmlInjectionWarmup)
			require.NoError(t, waitForNVMLInjectionBatch(metricsURL, process, batch))
		}()
	}
	profiling := 0
	for _, metric := range contract.Metrics {
		if metric.Profiling {
			profiling++
		}
	}
	t.Logf("NVML injection summary: devices=%d available=%d profiling=%d unavailable=%d unavailable-by-reason=%s",
		contract.DeviceCount, len(contract.Metrics), profiling, len(contract.Unavailable), unavailableReasonSummary(contract.Unavailable))
}

// unavailableReasonSummary returns sorted counts for unavailable injected fields.
func unavailableReasonSummary(unavailable []nvmlinjection.Unavailable) string {
	counts := map[string]int{}
	for _, field := range unavailable {
		counts[field.Reason]++
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s:%d", reason, counts[reason]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

// noActiveGPULinkMessage is the probe's own text when DCGM reports no
// GPU-owned link that is up.
const noActiveGPULinkMessage = "DCGM has no active GPU-owned link"

// probeMessages are fixed texts the probe writes itself, with no device or
// path values, so they are safe to show.
var probeMessages = []string{noActiveGPULinkMessage, "DCGM cache query found no supported GPUs"}

// probeMessage returns the first fixed probe text that stderr contains.
func probeMessage(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		for _, message := range probeMessages {
			if strings.HasSuffix(line, message) {
				return message
			}
		}
	}
	return ""
}

// safeProbeFailureSummary reports only recognized probe stages and the actionable root requirement.
func safeProbeFailureSummary(stderr string) string {
	const prefix = "DCGM probe failed during "
	allowed := map[string]struct{}{
		"DCGM initialization":          {},
		"entity discovery":             {},
		"entity group creation":        {},
		"entity group population":      {},
		"entity group destruction":     {},
		"field availability discovery": {},
		"field group creation":         {},
		"field watch":                  {},
		"field update":                 {},
		"field read":                   {},
		"field unwatch":                {},
		"field group destruction":      {},
	}
	stage := "unknown"
	for _, line := range strings.Split(stderr, "\n") {
		candidate := strings.TrimPrefix(strings.TrimSpace(line), prefix)
		if _, found := allowed[candidate]; found {
			stage = candidate
			break
		}
	}
	summary := "stage: " + stage
	if message := probeMessage(stderr); message != "" {
		summary += "; " + message
	}
	if hasProbeStatus(stderr, dcgmRequiresRootStatusCode) {
		summary += "; status: DCGM_ST_REQUIRES_ROOT; run the host test as root"
	}
	return summary
}

// hasProbeStatus reports whether one stderr line pairs Error: with the requested status code.
func hasProbeStatus(stderr string, code int) bool {
	want := fmt.Sprint(code)
	for _, line := range strings.Split(stderr, "\n") {
		fields := strings.Fields(line)
		for i := 1; i < len(fields); i++ {
			if fields[i-1] == "Error:" && fields[i] == want {
				return true
			}
		}
	}
	return false
}

// TestSafeProbeFailureSummary allows known probe stages and root guidance but hides arbitrary stderr details.
func TestSafeProbeFailureSummary(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "entity cleanup", stderr: "private output\nDCGM probe failed during entity group destruction\n", want: "stage: entity group destruction"},
		{name: "allowed stage", stderr: "private output\nDCGM probe failed during field watch\n", want: "stage: field watch"},
		{
			name:   "requires root",
			stderr: "CacheManager Init Failed. Error: -29\nDCGM probe failed during DCGM initialization\n",
			want:   "stage: DCGM initialization; status: DCGM_ST_REQUIRES_ROOT; run the host test as root",
		},
		{
			name:   "requires root without stage",
			stderr: "CacheManager Init Failed. Error: -29\n",
			want:   "stage: unknown; status: DCGM_ST_REQUIRES_ROOT; run the host test as root",
		},
		{name: "status split across lines", stderr: "CacheManager Init Failed. Error:\n-29\n", want: "stage: unknown"},
		{name: "different status", stderr: "CacheManager Init Failed. Error: -290\n", want: "stage: unknown"},
		{name: "fixed probe message", stderr: "private output\n" + noActiveGPULinkMessage + "\n", want: "stage: unknown; " + noActiveGPULinkMessage},
		{name: "not allowlisted", stderr: "DCGM probe failed during private-device-value\n", want: "stage: unknown"},
		{name: "empty", want: "stage: unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, safeProbeFailureSummary(test.stderr))
		})
	}
}

// TestUnavailableReasonSummaryIsStable sorts reason counts and labels an empty set as none.
func TestUnavailableReasonSummaryIsStable(t *testing.T) {
	unavailable := []nvmlinjection.Unavailable{
		{Reason: "not-supported"},
		{Reason: "blank"},
		{Reason: "not-supported"},
	}
	require.Equal(t, "blank:1,not-supported:2", unavailableReasonSummary(unavailable))
	require.Equal(t, "none", unavailableReasonSummary(nil))
}

// waitForNVMLInjectionBatch polls until exporter families match one direct-DCGM injection batch or times out.
func waitForNVMLInjectionBatch(metricsURL string, process *hostExporterProcess, batch []nvmlinjection.Metric) error {
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := process.running(); err != nil {
			return err
		}
		body, status, err := fetchRaw(metricsURL)
		lastErr = validateNVMLInjectionBatchResponse(body, status, err, batch)
		if lastErr == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for complete NVML injection batch: %w", lastErr)
}

// validateNVMLInjectionBatchResponse reports transport, HTTP, parse, or metric-contract failures.
func validateNVMLInjectionBatchResponse(
	body string,
	status int,
	requestErr error,
	batch []nvmlinjection.Metric,
) error {
	if requestErr != nil {
		return requestErr
	}
	if status != http.StatusOK {
		return fmt.Errorf("metrics endpoint returned HTTP %d", status)
	}
	families, err := metriccontract.ParseText([]byte(body))
	if err != nil {
		return err
	}
	return nvmlinjection.ValidateFamilies(families, batch)
}

// TestValidateNVMLInjectionBatchResponseReportsHTTPFailure keeps non-OK responses in timeout diagnostics.
func TestValidateNVMLInjectionBatchResponseReportsHTTPFailure(t *testing.T) {
	err := validateNVMLInjectionBatchResponse("warming up", http.StatusServiceUnavailable, nil, nil)
	require.ErrorContains(t, err, "HTTP 503")
}

// runStartWithGPUBindUnbindWatch verifies enabling topology-change watching still serves metrics.
func runStartWithGPUBindUnbindWatch(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    detect_bind_unbind:
      enabled: true
`), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startFastCSVExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		defaultCollectorsFile(t),
		"--config-file", configFile,
		"--address", fmt.Sprintf(":%d", port),
	)

	validateHostDefaultMetrics(t, metricsResp)
}

// runStartWithYAMLConfigFile verifies the exporter starts from a version 2 YAML
// file with inline metric fields, serves only the metric that file lists, and
// gives it the file's # TYPE and # HELP text.
func runStartWithYAMLConfigFile(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    enrichment: {kubernetes: false, container: false, hpc_jobs: false}
    every: 1s
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP_CELSIUS
        prometheus_type: `+inlineFieldType+`
        help: `+inlineFieldHelp+`
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
`), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWaitForFamilies(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		nil, []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"},
		"--config-file", configFile,
		"--address", fmt.Sprintf(":%d", port),
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)

	require.Contains(t, families, "DCGM_FI_DEV_GPU_TEMP_CELSIUS")
	require.NotContains(t, families, "DCGM_FI_DEV_BOARD_POWER_WATTS")
	require.Empty(t, inlineFieldTextProblems(families, "DCGM_FI_DEV_GPU_TEMP_CELSIUS"),
		"the inline field's # TYPE and # HELP lines")
}

// runStartWithYAMLOverrides verifies that a config file with metrics[].overrides is accepted by the runtime startup path.
func runStartWithYAMLOverrides(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  - name: gpu
    enrichment: {kubernetes: false, container: false, hpc_jobs: false}
    every: 1s
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP_CELSIUS
        prometheus_type: gauge
        help: GPU temperature.
      - name: DCGM_FI_DEV_BOARD_POWER_WATTS
        prometheus_type: gauge
        help: Power draw.
    overrides:
      - name: temperature
        match: [DCGM_FI_DEV_GPU_TEMP_CELSIUS]
        every: 1s
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
`), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startFastExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--config-file", configFile,
		"--address", fmt.Sprintf(":%d", port),
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	require.Contains(t, families, "DCGM_FI_DEV_GPU_TEMP_CELSIUS")
	require.Contains(t, families, "DCGM_FI_DEV_BOARD_POWER_WATTS")
}

// runStartWithHPCJobMapping attaches the job name from a host mapping file to GPU samples.
func runStartWithHPCJobMapping(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	jobDir := t.TempDir()
	const jobName = "host-job-0"
	require.NoError(t, os.WriteFile(filepath.Join(jobDir, "0"), []byte(jobName+"\n"), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startFastExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--collectors", defaultCollectorsFile(t),
		"--address", fmt.Sprintf(":%d", port),
		"--hpc-job-mapping-dir", jobDir,
	)

	require.True(t, metricsContainLabelValue(t, metricsResp, "hpc_job", jobName), "expected hpc_job label %q", jobName)
}

// runStartWithRepeatedHPCJobMapping verifies the process serves one sample for
// each distinct mapping-file job ID rather than duplicate Prometheus series.
func runStartWithRepeatedHPCJobMapping(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	jobDir := t.TempDir()
	const (
		firstJob  = "host-job-0"
		secondJob = "host-job-1"
	)
	require.NoError(t, os.WriteFile(filepath.Join(jobDir, "0"), []byte(firstJob+"\n"+secondJob+"\n"+firstJob+"\n"), 0o600))
	collectorsPath := filepath.Join(t.TempDir(), "collectors.csv")
	require.NoError(t, os.WriteFile(collectorsPath, []byte("DCGM_FI_DEV_GPU_TEMP_CELSIUS,gauge,GPU temperature.\n"), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startFastExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--collectors", collectorsPath,
		"--address", fmt.Sprintf(":%d", port),
		"--hpc-job-mapping-dir", jobDir,
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	family, found := families["DCGM_FI_DEV_GPU_TEMP_CELSIUS"]
	require.True(t, found, "expected DCGM_FI_DEV_GPU_TEMP_CELSIUS metric family")
	jobSamples := map[string]int{}
	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["gpu"] == "0" && labels["hpc_job"] != "" {
			jobSamples[labels["hpc_job"]]++
		}
	}
	require.Equal(t, map[string]int{firstJob: 1, secondJob: 1}, jobSamples)
}

// runStartWithHPCJobMappingLineRules verifies how mapping-file lines turn into
// hpc_job labels: a zero line is an idle GPU, blank lines are skipped, padding
// is trimmed and a repeated job appears once. Each case writes one mapping file
// for GPU 0, so the check needs one GPU.
func runStartWithHPCJobMappingLineRules(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	collectorsPath := filepath.Join(t.TempDir(), "collectors.csv")
	require.NoError(t, os.WriteFile(collectorsPath, []byte("DCGM_FI_DEV_GPU_TEMP_CELSIUS,gauge,GPU temperature.\n"), 0o600))

	cases := []struct {
		name string
		file string
		jobs []string
	}{
		{name: "a zero line marks an idle GPU", file: "0", jobs: nil},
		{name: "padding and blank lines are dropped and repeats merged", file: "  4242 \n\n4242\n", jobs: []string{"4242"}},
		{name: "a zero line next to a job keeps only the job", file: "0\n77", jobs: []string{"77"}},
	}
	for _, tc := range cases {
		t.Log(tc.name)
		jobDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(jobDir, "0"), []byte(tc.file), 0o600))
		checkGPUZeroJobLabels(t, collectorsPath, jobDir, tc.name, tc.jobs)
	}
}

// checkGPUZeroJobLabels starts an exporter on the mapping directory and checks
// the hpc_job labels on the gpu=0 series. It ends the exporter before it returns.
func checkGPUZeroJobLabels(t testing.TB, collectorsPath, jobDir, name string, want []string) {
	t.Helper()
	port := getRandomAvailablePort(t)
	process, metricsResp := startFastExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--collectors", collectorsPath,
		"--address", fmt.Sprintf(":%d", port),
		"--hpc-job-mapping-dir", jobDir,
	)
	defer process.terminate(t)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	require.Contains(t, families, "DCGM_FI_DEV_GPU_TEMP_CELSIUS")

	var jobs []string
	seen := 0
	for _, metric := range families["DCGM_FI_DEV_GPU_TEMP_CELSIUS"].GetMetric() {
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}

		if labels["gpu"] != "0" {
			continue
		}

		seen++
		job, labeled := labels["hpc_job"]
		if labeled {
			jobs = append(jobs, job)
		}
		require.NotEqual(t, "0", job, "an idle GPU must not get hpc_job=\"0\"")
	}

	require.Positive(t, seen, "%s: the scrape has no gpu=0 series", name)
	require.Equal(t, want, jobs)
}

// metricsContainLabelValue reports whether any parsed sample contains the requested label pair.
func metricsContainLabelValue(t testing.TB, metricsResp string, labelName string, labelValue string) bool {
	t.Helper()
	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == labelName && label.GetValue() == labelValue {
					return true
				}
			}
		}
	}
	return false
}

// exporterFamilyNames are the exporter's own families that
// --enable-exporter-metrics and observability.metrics.enabled add.
var exporterFamilyNames = []string{
	"go_goroutines",
	"process_cpu_seconds_total",
	"promhttp_metric_handler_requests_in_flight",
	"promhttp_metric_handler_requests_total",
}

// runExporterMetricsEffect runs the exporter with exporter metrics on, as the
// flag and as the config key, and checks each run adds the exporter's own
// families to the baseline and leaves every DCGM family as it was.
func runExporterMetricsEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{
			name: "--enable-exporter-metrics",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--enable-exporter-metrics")
			},
			check: checkExporterFamiliesAdded,
		},
		{
			name: "observability.metrics.enabled",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--config-file", writeSettingConfig(t, map[string]any{
					"version":       2,
					"observability": map[string]any{"metrics": map[string]any{"enabled": true}},
				}))
			},
			check: checkExporterFamiliesAdded,
		},
	})
}

// checkExporterFamiliesAdded checks the run adds the exporter's own families,
// which the baseline does not serve, and leaves every DCGM family as it was.
func checkExporterFamiliesAdded(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	for _, name := range exporterFamilyNames {
		require.Containsf(t, run.families, name, "the exporter's own family %s is missing", name)
	}

	checkSameDCGMShapeAsBaseline(t, base, run)
}
