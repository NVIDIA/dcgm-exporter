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
	"fmt"
	"strings"
	"testing"
	"time"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

const (
	oversizedFieldGroupLogMessage = "split oversized watch group into physical DCGM field groups"
	oversizedGPUWatchFields       = 131
)

func TestHasOversizedFieldGroupLog(t *testing.T) {
	output := strings.Join([]string{
		`time=2026-07-27T12:00:00Z level=INFO msg="split oversized watch group into physical DCGM field groups" logical_group=batching-1 total_fields=131 chunk_index=1 chunk_count=2 chunk_fields=127 capacity=127`,
		`time=2026-07-27T12:00:00Z level=INFO msg="split oversized watch group into physical DCGM field groups" logical_group=batching-1 total_fields=131 chunk_index=2 chunk_count=2 chunk_fields=4 capacity=127`,
	}, "\n")

	require.True(t, hasOversizedFieldGroupLog(output, 1, 127))
	require.True(t, hasOversizedFieldGroupLog(output, 2, 4))
	require.False(t, hasOversizedFieldGroupLog(output, 2, 127))
}

// runOversizedFieldGroup verifies one logical group remains above the physical
// DCGM limit after scope partitioning and is split into valid chunks.
func runOversizedFieldGroup(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	configPath := writeLatestValueBatchingConfig(t, latestValueBatchingFields)
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startExporterAndWait(
		t,
		metricsURL,
		"--config-file", configPath,
		"--address", fmt.Sprintf(":%d", port),
	)

	require.NoError(t, process.running())
	output := process.output.String()
	require.Truef(t, hasOversizedFieldGroupLog(output, 1, 127),
		"exporter logs should describe the 127-field first physical group; output:\n%s", output)
	require.Truef(t, hasOversizedFieldGroupLog(output, 2, 4),
		"exporter logs should describe the 4-field second physical group; output:\n%s", output)

	families := waitForMetricFamilies(
		t,
		metricsURL,
		60*time.Second,
		func(families map[string]*io_prometheus_client.MetricFamily) bool {
			return includesAllMetricFamilies(families, latestValueBoundaryFields[:])
		},
		"split physical groups should emit fixture rows 127, 128, and 129",
	)
	for _, name := range latestValueBoundaryFields {
		require.Contains(t, families, name)
	}
	require.NoError(t, process.running())
}

func hasOversizedFieldGroupLog(output string, chunkIndex int, chunkFields int) bool {
	expected := []string{
		oversizedFieldGroupLogMessage,
		"logical_group=batching-1",
		fmt.Sprintf("total_fields=%d", oversizedGPUWatchFields),
		fmt.Sprintf("chunk_index=%d", chunkIndex),
		"chunk_count=2",
		fmt.Sprintf("chunk_fields=%d", chunkFields),
		"capacity=127",
	}

	for _, line := range strings.Split(output, "\n") {
		matches := true
		for _, token := range expected {
			if !strings.Contains(line, token) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
