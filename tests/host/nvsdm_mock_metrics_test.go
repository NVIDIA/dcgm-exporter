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
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvsdmmock"
)

const (
	nvsdmMockMetricsPollTimeout  = 60 * time.Second
	nvsdmMockMetricsPollInterval = 500 * time.Millisecond
)

func runNVSDMMockMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	mockPath := strings.TrimSpace(os.Getenv("DCGM_NVSDM_MOCK_YAML"))
	require.NotEmpty(t, mockPath, "DCGM_NVSDM_MOCK_YAML is required")
	require.NotEmpty(t, strings.TrimSpace(*dcgmProbeBinaryFlag), "-dcgm-probe-binary is required")
	require.NotEmpty(t, strings.TrimSpace(*dcgmFieldsFileFlag), "-dcgm-fields-file is required")

	contractPath := filepath.Join(t.TempDir(), "nvsdm-mock-contract.json")
	// #nosec G204 -- both paths are explicit E2E harness inputs validated before the host suite starts.
	cmd := exec.Command(*dcgmProbeBinaryFlag, "nvsdm-mock", "--field-names", *dcgmFieldsFileFlag, "--output", contractPath)
	cmd.Env = hostExporterEnv()
	cmd.Stdout = io.Discard
	var probeStderr bytes.Buffer
	cmd.Stderr = &probeStderr
	if err := cmd.Run(); err != nil {
		require.NoError(t, err, "query direct NVSDM mock DCGM contract (%s)", safeProbeFailureSummary(probeStderr.String()))
	}
	contractFile, err := os.Open(contractPath)
	require.NoError(t, err)
	defer contractFile.Close()
	contract, err := nvsdmmock.Read(contractFile)
	require.NoError(t, err)
	if len(contract.Metrics) == 0 {
		port := getRandomAvailablePort(t)
		process, healthResp := startExporterAndWait(
			t,
			fmt.Sprintf("http://localhost:%d/health", port),
			"--collectors", "./testdata/default-counters.csv",
			"--address", fmt.Sprintf(":%d", port),
			"--switch-devices", "f",
		)
		defer process.terminate(t)
		require.Equal(t, "OK", strings.TrimSpace(healthResp))
		t.Log("dcgm-exporter health check passed without direct DCGM NVSwitch/link metrics")
		return
	}

	for index, batch := range contract.Batches() {
		var collectors bytes.Buffer
		require.NoError(t, nvsdmmock.WriteCollectors(&collectors, batch))
		collectorsPath := filepath.Join(t.TempDir(), fmt.Sprintf("nvsdm-mock-collectors-%d.csv", index))
		require.NoError(t, os.WriteFile(collectorsPath, collectors.Bytes(), 0o600))

		port := getRandomAvailablePort(t)
		metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
		process, _ := startExporterAndWait(
			t,
			metricsURL,
			"--collectors", collectorsPath,
			"--address", fmt.Sprintf(":%d", port),
			"--collect-interval", "100",
			"--switch-devices", "f",
		)
		require.NoError(t, waitForNVSDMMockMetrics(metricsURL, process, batch))
		process.terminate(t)
	}
	t.Logf("NVSDM mock summary: switches=%d active-switch-links=%d available=%d unavailable=%d unavailable-by-reason=%s",
		contract.SwitchCount, contract.LinkCount, len(contract.Metrics), len(contract.Unavailable), nvsdmUnavailableReasonSummary(contract.Unavailable))
}

func waitForNVSDMMockMetrics(metricsURL string, process *hostExporterProcess, batch []nvsdmmock.Metric) error {
	deadline := time.Now().Add(nvsdmMockMetricsPollTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := process.running(); err != nil {
			return err
		}
		body, status, err := fetchRaw(metricsURL)
		switch {
		case err != nil:
			lastErr = err
		case status != http.StatusOK:
			lastErr = fmt.Errorf("metrics endpoint returned HTTP %d", status)
		default:
			families, parseErr := metriccontract.ParseText([]byte(body))
			if parseErr == nil {
				lastErr = nvsdmmock.ValidateFamilies(families, batch)
				if lastErr == nil {
					return nil
				}
			} else {
				lastErr = parseErr
			}
		}
		time.Sleep(nvsdmMockMetricsPollInterval)
	}
	return fmt.Errorf("timed out waiting for NVSDM mock metrics: %w", lastErr)
}

func nvsdmUnavailableReasonSummary(unavailable []nvsdmmock.Unavailable) string {
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
