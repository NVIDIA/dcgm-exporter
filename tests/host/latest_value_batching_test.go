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
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

const (
	latestValueBatchingFixture = "./testdata/latest-value-batching-counters.csv"
	latestValueBatchingFields  = 139
	latestValueBoundaryStart   = 126
)

var latestValueBoundaryFields = [...]string{
	"DCGM_FI_DEV_GPU_TEMP",
	"DCGM_FI_DEV_POWER_USAGE",
	"DCGM_FI_DEV_FB_USED",
}

var oversizedFieldGroupFields = [...]string{
	"DCGM_FI_DEV_ROW_REMAP_UNCORRECTABLE_TOTAL",
	"DCGM_FI_DEV_ROW_REMAP_CORRECTABLE_TOTAL",
	"DCGM_FI_DEV_ROW_REMAP_FAILED",
	"DCGM_FI_DEV_ROW_REMAP_PENDING",
	"DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_TOTAL",
	"DCGM_FI_DEV_NVLINK_CRC_DATA_ERROR_TOTAL",
	"DCGM_FI_DEV_NVLINK_REPLAY_ERROR_TOTAL",
	"DCGM_FI_DEV_NVLINK_RECOVERY_ERROR_TOTAL",
	"DCGM_FI_DEV_NVLINK_THROUGHPUT_TOTAL",
	"DCGM_FI_DEV_NVLINK_CRC_ERROR_TOTAL",
}

var latestValueComputeInstanceOnlyFields = map[string]struct{}{
	"DCGM_FI_DEV_BAR1_TOTAL":    {},
	"DCGM_FI_DEV_BAR1_USED":     {},
	"DCGM_FI_DEV_BAR1_FREE":     {},
	"DCGM_FI_DEV_FB_TOTAL":      {},
	"DCGM_FI_DEV_FB_FREE":       {},
	"DCGM_FI_DEV_FB_RESERVED":   {},
	"DCGM_FI_DEV_FB_USED_RATIO": {},
	"DCGM_FI_DEV_FB_USED":       {},
}

// TestLatestValueBatchingFixtureContract guards the request-boundary shape even
// when the GPU-backed scenario is skipped by the short unit-test target.
func TestLatestValueBatchingFixtureContract(t *testing.T) {
	fields := readLatestValueBatchingFields(t)

	boundaryEnd := latestValueBoundaryStart + len(latestValueBoundaryFields)
	require.Equal(t, latestValueBoundaryFields[:], fields[latestValueBoundaryStart:boundaryEnd])
	require.Equal(t, oversizedFieldGroupFields[:], fields[boundaryEnd:])

	gpuFields := 0
	for _, field := range fields {
		if _, computeInstanceOnlyField := latestValueComputeInstanceOnlyFields[field]; !computeInstanceOnlyField {
			gpuFields++
		}
	}
	require.Equal(t, oversizedGPUWatchFields, gpuFields)
}

// readLatestValueBatchingFields validates and returns the fixture's stable field order.
func readLatestValueBatchingFields(t testing.TB) []string {
	t.Helper()

	file, err := os.Open(latestValueBatchingFixture)
	require.NoError(t, err)
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comment = '#'
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, latestValueBatchingFields)

	fields := make([]string, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for row, record := range records {
		require.Lenf(t, record, 3, "fixture row %d must use the counter CSV contract", row+1)

		name := strings.TrimSpace(record[0])
		metricType := strings.TrimSpace(record[1])
		help := strings.TrimSpace(record[2])
		require.Truef(t, strings.HasPrefix(name, "DCGM_FI_DEV_"),
			"fixture row %d must be a regular GPU field, got %q", row+1, name)
		require.NotContainsf(t, name, "DCGM_FI_PROF_", "fixture row %d must not be a profiling field", row+1)
		require.Equalf(t, "gauge", metricType, "fixture row %d must be a numeric metric", row+1)
		require.NotEmptyf(t, help, "fixture row %d must include help text", row+1)
		require.NotContainsf(t, seen, name, "fixture row %d duplicates %q", row+1, name)

		seen[name] = struct{}{}
		fields = append(fields, name)
	}

	return fields
}

// writeLatestValueBatchingConfig assigns the fixture fields to logical
// collections with the requested sizes while preserving their CSV order.
func writeLatestValueBatchingConfig(t testing.TB, groupSizes ...int) string {
	t.Helper()

	fields := readLatestValueBatchingFields(t)
	total := 0
	for _, size := range groupSizes {
		require.Positive(t, size)
		total += size
	}
	require.Equal(t, len(fields), total, "watch groups must cover the entire batching fixture")

	fixturePath, err := filepath.Abs(latestValueBatchingFixture)
	require.NoError(t, err)

	var config strings.Builder
	fmt.Fprintf(&config, "version: 2\nmetrics:\n  file: %q\ncollections:\n  - name: scrape\n    every: 1s\n    metrics:\n      include: [\"*\"]\n", fixturePath)

	offset := 0
	for index, size := range groupSizes {
		fmt.Fprintf(&config, "  - name: batching-%d\n    every: 1s\n    metrics:\n      include:\n", index+1)
		for _, field := range fields[offset : offset+size] {
			fmt.Fprintf(&config, "        - %s\n", field)
		}
		offset += size
	}

	configPath := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(config.String()), 0o600))
	return configPath
}

// runLatestValueBatching verifies a real DCGM scrape can read more than 128
// fields while the physical watch groups remain within DCGM's 127-field limit.
func runLatestValueBatching(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configPath := writeLatestValueBatchingConfig(t, 127, 12)
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startExporterAndWait(
		t,
		metricsURL,
		"--config-file", configPath,
		"--address", fmt.Sprintf(":%d", port),
	)

	require.NoError(t, process.running())
	families := waitForMetricFamilies(
		t,
		metricsURL,
		60*time.Second,
		func(families map[string]*io_prometheus_client.MetricFamily) bool {
			return includesAllMetricFamilies(families, latestValueBoundaryFields[:])
		},
		"latest-value batches should emit fixture rows 127, 128, and 129",
	)
	for _, name := range latestValueBoundaryFields {
		require.Contains(t, families, name)
	}
	require.NoError(t, process.running())
}
