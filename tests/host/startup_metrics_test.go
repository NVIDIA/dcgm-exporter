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

// runNVMLInjectionMetrics compares exporter output with direct injected-DCGM availability.
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
	cmd := exec.Command(*dcgmProbeBinaryFlag, "--field-names", *dcgmFieldsFileFlag, "--output", contractPath)
	cmd.Env = hostExporterEnv()
	cmd.Stdout = io.Discard
	var probeStderr bytes.Buffer
	cmd.Stderr = &probeStderr
	if err := cmd.Run(); err != nil {
		require.NoError(t, err, "query direct injected DCGM contract (stage: %s)", safeProbeFailureStage(probeStderr.String()))
	}
	contractFile, err := os.Open(contractPath)
	require.NoError(t, err)
	defer contractFile.Close()
	contract, err := nvmlinjection.Read(contractFile)
	require.NoError(t, err)
	switch strings.TrimSpace(os.Getenv("E2E_DCGM_EXPECT_DEVICES")) {
	case "true":
		require.Positive(t, contract.DeviceCount, "fixture declares devices but DCGM discovered none")
	case "false":
		require.Zero(t, contract.DeviceCount, "loader-only fixture unexpectedly discovered devices")
	}
	if contract.DeviceCount == 0 {
		t.Log("NVML injection initialized successfully with zero declared devices")
		return
	}

	for index, batch := range contract.Batches() {
		var collectors bytes.Buffer
		require.NoError(t, nvmlinjection.WriteCollectors(&collectors, batch))
		collectorsPath := filepath.Join(t.TempDir(), fmt.Sprintf("nvml-injection-collectors-%d.csv", index))
		require.NoError(t, os.WriteFile(collectorsPath, collectors.Bytes(), 0o600))

		port := getRandomAvailablePort(t)
		metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
		process, _ := startExporterAndWait(
			t,
			metricsURL,
			"--collectors", collectorsPath,
			"--address", fmt.Sprintf(":%d", port),
			"--collect-interval", "100",
		)
		require.NoError(t, waitForNVMLInjectionBatch(metricsURL, process, batch))
		process.terminate(t)
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

func safeProbeFailureStage(stderr string) string {
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
	for _, line := range strings.Split(stderr, "\n") {
		stage := strings.TrimPrefix(strings.TrimSpace(line), prefix)
		if _, found := allowed[stage]; found {
			return stage
		}
	}
	return "unknown"
}

func TestSafeProbeFailureStage(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "allowed", stderr: "private output\nDCGM probe failed during field watch\n", want: "field watch"},
		{name: "not allowlisted", stderr: "DCGM probe failed during private-device-value\n", want: "unknown"},
		{name: "empty", want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, safeProbeFailureStage(test.stderr))
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

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf(":%d", port),
		"--enable-gpu-bind-unbind-watch",
		"--gpu-bind-unbind-poll-interval", "1s",
	)

	validateHostDefaultMetrics(t, metricsResp)
}

func runStartWithYAMLConfigFile(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collection:
  interval: 1s
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

// runStartWithYAMLWatchGroups verifies YAML watch groups are accepted by the runtime startup path.
func runStartWithYAMLWatchGroups(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
    - name: DCGM_FI_DEV_POWER_USAGE
      prometheusType: gauge
      help: Power draw.
collection:
  interval: 1s
  watchGroups:
    - name: temperature
      interval: 1s
      fields:
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
