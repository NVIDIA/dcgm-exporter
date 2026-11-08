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
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// GPU inventory comparisons retry because nvidia-smi and DCGM sample at
// different times.
const (
	gpuInventoryTimeout      = 60 * time.Second
	gpuInventoryPollInterval = 3 * time.Second
)

// hostInventory reads GPU data from nvidia-smi on this host. Its identity read
// is shared by every comparison in the test process.
var hostInventory = nvidiasmi.NewInventory(nvidiasmi.LocalRunner)

// validateHostGPUInventory compares the identity labels and values the running
// exporter serves with nvidia-smi on this host, for the families in the CSV at
// collectorsPath. Each attempt scrapes and reads the nvidia-smi values for those
// families again until they agree or the timeout passes. The exporter runs here without NODE_NAME, so
// its hostname label is this machine's host name.
func validateHostGPUInventory(t testing.TB, metricsURL string, process *hostExporterProcess, collectorsPath string) {
	t.Helper()

	// NVML injection replaces the GPUs DCGM sees, so nvidia-smi cannot be the expectation.
	if os.Getenv("NVML_INJECTION_MODE") != "" {
		t.Log("GPU inventory comparison skipped: NVML injection replaces the host GPUs the exporter reports")
		return
	}

	contents, err := os.ReadFile(collectorsPath)
	require.NoError(t, err)
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(contents))
	require.NoError(t, err)
	hostname, err := os.Hostname()
	require.NoError(t, err, "read the host name")
	opts := metriccontract.GPUInventoryOptions{CSVRows: rows, ExpectedHostname: hostname, HostnameSource: "host name"}

	var lastErr error
	deadline := time.Now().Add(gpuInventoryTimeout)
	for {
		report, err := compareHostGPUInventory(t, metricsURL, process, opts)
		if err == nil {
			for _, reason := range report.Skipped {
				t.Logf("GPU inventory comparison skipped: %s", reason)
			}
			return
		}

		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(gpuInventoryPollInterval)
	}

	require.NoError(t, lastErr, "exporter GPU identity and values should match nvidia-smi within %s", gpuInventoryTimeout)
}

// compareHostGPUInventory makes one comparison attempt: read nvidia-smi, scrape
// the exporter, and compare. A missing nvidia-smi binary or a stopped exporter
// fails the test at once; any other nvidia-smi failure, a scrape error or a
// mismatch is returned for retry.
func compareHostGPUInventory(
	t testing.TB, metricsURL string, process *hostExporterProcess, opts metriccontract.GPUInventoryOptions,
) (metriccontract.GPUInventoryReport, error) {
	t.Helper()

	gpus, err := hostInventory.Identity(context.Background())
	if err != nil {
		return metriccontract.GPUInventoryReport{}, nvidiaSmiAttemptError(t, err)
	}
	readings, err := hostInventory.Readings(context.Background(), metriccontract.ValueQueryFields(opts.CSVRows))
	if err != nil {
		return metriccontract.GPUInventoryReport{}, nvidiaSmiAttemptError(t, err)
	}
	require.NoError(t, process.running(), "the exporter stopped during the GPU inventory comparison")

	body, statusCode, err := fetchRaw(metricsURL)
	if err != nil {
		return metriccontract.GPUInventoryReport{}, fmt.Errorf("scrape exporter: %w", err)
	}
	if statusCode != http.StatusOK {
		return metriccontract.GPUInventoryReport{}, fmt.Errorf("scrape exporter: status %d", statusCode)
	}

	families, err := metriccontract.ParseText([]byte(body))
	require.NoError(t, err, "metrics should parse as Prometheus text")
	report := metriccontract.CompareGPUInventory(families, gpus, readings, opts)
	return report, report.Err()
}

// nvidiaSmiAttemptError fails the test when nvidia-smi is not installed and
// otherwise returns err so the caller retries within the comparison window.
func nvidiaSmiAttemptError(t testing.TB, err error) error {
	t.Helper()

	require.False(t, nvidiasmi.IsMissing(err),
		"setup: comparing exporter output with the host GPUs needs nvidia-smi on PATH: %v", err)
	return err
}
