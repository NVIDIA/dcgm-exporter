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
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

const (
	reloadTemperatureFamily = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"
	reloadPowerFamily       = "DCGM_FI_DEV_BOARD_POWER_WATTS"
)

// startWithVersion2MetricsFile writes a version 2 file whose metrics come from a
// separate counters file with both families, starts the exporter on it and
// returns the process, the metrics URL and the paths of both files.
func startWithVersion2MetricsFile(t testing.TB) (process *hostExporterProcess, metricsURL, configPath, countersPath string) {
	t.Helper()
	dir := t.TempDir()
	countersPath = filepath.Join(dir, "counters.csv")
	writeCounters(t, countersPath, gaugeRows(reloadTemperatureFamily, reloadPowerFamily)...)

	template, err := os.ReadFile(configFixture("version2-metrics-file.yaml"))
	require.NoError(t, err)
	configPath = filepath.Join(dir, "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configPath, //nolint:gosec // G703: the path is inside the test temp directory
		[]byte(strings.ReplaceAll(string(template), "${METRICS_FILE}", countersPath)), 0o600))

	port := getRandomAvailablePort(t)
	metricsURL = fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ = startExporterAndWaitForFamilies(t, metricsURL, nil,
		[]string{reloadTemperatureFamily, reloadPowerFamily},
		"--config-file", configPath, "--address", fmt.Sprintf(":%d", port))
	return process, metricsURL, configPath, countersPath
}

// runYAMLEditAndSIGHUPKeepsStartupSettings shows what a reload does with a
// changed or broken version 2 file: SIGHUP rebuilds the registry from the
// settings resolved at startup and does not read the file again, so an edit does
// not apply and a broken file does no harm. Both families keep being served.
func runYAMLEditAndSIGHUPKeepsStartupSettings(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	process, metricsURL, configPath, countersPath := startWithVersion2MetricsFile(t)
	defer process.terminate(t)
	both := []string{reloadTemperatureFamily, reloadPowerFamily}

	// A valid edit: the file now asks for a different counters file with one family.
	otherCounters := filepath.Join(filepath.Dir(countersPath), "other-counters.csv")
	writeCounters(t, otherCounters, gaugeRows(reloadTemperatureFamily)...)
	edited, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, //nolint:gosec // G703: the path is inside the test temp directory
		[]byte(strings.ReplaceAll(string(edited), countersPath, otherCounters)), 0o600))
	reloadAndExpectFamilies(t, process, metricsURL, both, nil)

	// A broken file.
	require.NoError(t, os.WriteFile(configPath, []byte("version: 2\nmetrics: [\n"), 0o600))
	reloadAndExpectFamilies(t, process, metricsURL, both, nil)
}

// reloadAndExpectFamilies sends SIGHUP, waits for the exporter to log the finished
// reload, and checks which families the next scrape holds.
func reloadAndExpectFamilies(t testing.TB, process *hostExporterProcess, metricsURL string, present, absent []string) {
	t.Helper()
	completed := hotReloadsCompleted(process)
	process.signal(t, syscall.SIGHUP)
	waitForHotReload(t, process, completed)
	require.NoError(t, validateMetricFamilyState(metricsURL, present, absent))
}

// runVersion2MetricsFileChangeReloads shows that the exporter reloads when the
// counters file named by a version 2 file changes, and keeps the last good
// metrics when the counters file turns invalid.
func runVersion2MetricsFileChangeReloads(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	process, metricsURL, _, countersPath := startWithVersion2MetricsFile(t)
	defer process.terminate(t)

	writeCounters(t, countersPath, gaugeRows(reloadTemperatureFamily)...)
	waitForMetricFamilies(t, metricsURL, reloadWaitTimeout,
		func(families map[string]*io_prometheus_client.MetricFamily) bool {
			return hasMetricFamily(families, reloadTemperatureFamily) && !hasMetricFamily(families, reloadPowerFamily)
		}, "the counters file change should reload the registry")

	// The invalid file must trigger a reload that fails: wait for that log line
	// before checking that the last good families are still served.
	const failedReload = "Failed to build new registry during hot reload"
	failuresBefore := strings.Count(process.output.String(), failedReload)
	writeInvalidCollectorsFile(t, countersPath)
	require.Eventually(t, func() bool {
		return strings.Count(process.output.String(), failedReload) > failuresBefore
	}, reloadWaitTimeout, 100*time.Millisecond, "the invalid counters file did not trigger a failed reload; output:\n%s", process.output.String())
	assertMetricFamilyStateContinuously(t, metricsURL,
		[]string{reloadTemperatureFamily}, []string{reloadPowerFamily}, 5*time.Second, nil)
}
