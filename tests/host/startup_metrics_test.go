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

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf(":%d", port),
	)

	validateHostDefaultMetrics(t, metricsResp)
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
		process, healthResp := startExporterAndWait(
			t,
			fmt.Sprintf("http://localhost:%d/health", port),
			"--collectors", "./testdata/default-counters.csv",
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

			healthResp, err := retryMetrics(healthURL, process)
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

func safeProbeFailureSummary(stderr string) string {
	const prefix = "DCGM probe failed during "
	allowed := map[string]struct{}{
		"DCGM initialization":          {},
		"entity discovery":             {},
		"entity group creation":        {},
		"entity group population":      {},
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
	if hasProbeStatus(stderr, dcgmRequiresRootStatusCode) {
		summary += "; status: DCGM_ST_REQUIRES_ROOT; run the host test as root"
	}
	return summary
}

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

func TestSafeProbeFailureSummary(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
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
		{name: "not allowlisted", stderr: "DCGM probe failed during private-device-value\n", want: "stage: unknown"},
		{name: "empty", want: "stage: unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, safeProbeFailureSummary(test.stderr))
		})
	}
}

func TestUnavailableReasonSummaryIsStable(t *testing.T) {
	unavailable := []nvmlinjection.Unavailable{
		{Reason: "not-supported"},
		{Reason: "blank"},
		{Reason: "not-supported"},
	}
	require.Equal(t, "blank:1,not-supported:2", unavailableReasonSummary(unavailable))
	require.Equal(t, "none", unavailableReasonSummary(nil))
}

func waitForNVMLInjectionBatch(metricsURL string, process *hostExporterProcess, batch []nvmlinjection.Metric) error {
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := process.running(); err != nil {
			return err
		}
		body, status, err := fetchRaw(metricsURL)
		if err == nil && status == http.StatusOK {
			families, parseErr := metriccontract.ParseText([]byte(body))
			if parseErr == nil {
				lastErr = nvmlinjection.ValidateFamilies(families, batch)
				if lastErr == nil {
					return nil
				}
			} else {
				lastErr = parseErr
			}
		} else if err != nil {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for complete NVML injection batch: %w", lastErr)
}

// runStartWithGPUBindUnbindWatch verifies enabling topology-change watching still serves metrics.
func runStartWithGPUBindUnbindWatch(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
sources:
  dcgm:
    detectBindUnbind:
      enabled: true
`), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--config-file", configFile,
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf(":%d", port),
	)

	validateHostDefaultMetrics(t, metricsResp)
}

func runStartWithYAMLConfigFile(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collections:
  - name: scrape
    every: 1s
    metrics:
      include: ["*"]
`), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--config-file", configFile,
		"--address", fmt.Sprintf(":%d", port),
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	require.Contains(t, families, "DCGM_FI_DEV_GPU_TEMP")
	require.NotContains(t, families, "DCGM_FI_DEV_POWER_USAGE")
}

// runStartWithYAMLCollections verifies YAML collections are accepted by the runtime startup path.
func runStartWithYAMLCollections(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
    - name: DCGM_FI_DEV_POWER_USAGE
      prometheusType: gauge
      help: Power draw.
collections:
  - name: scrape
    every: 1s
    metrics:
      include: ["*"]
  - name: temperature
    every: 1s
    metrics:
      include:
        - DCGM_FI_DEV_GPU_TEMP
`), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--config-file", configFile,
		"--address", fmt.Sprintf(":%d", port),
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	require.Contains(t, families, "DCGM_FI_DEV_GPU_TEMP")
	require.Contains(t, families, "DCGM_FI_DEV_POWER_USAGE")
}

func runStartWithHPCJobMapping(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	jobDir := t.TempDir()
	const jobName = "host-job-0"
	require.NoError(t, os.WriteFile(filepath.Join(jobDir, "0"), []byte(jobName+"\n"), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--collectors", "./testdata/default-counters.csv",
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
	require.NoError(t, os.WriteFile(collectorsPath, []byte("DCGM_FI_DEV_GPU_TEMP,gauge,GPU temperature.\n"), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--collectors", collectorsPath,
		"--address", fmt.Sprintf(":%d", port),
		"--hpc-job-mapping-dir", jobDir,
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	family, found := families["DCGM_FI_DEV_GPU_TEMP"]
	require.True(t, found, "expected DCGM_FI_DEV_GPU_TEMP metric family")
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
