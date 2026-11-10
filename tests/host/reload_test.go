/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

const (
	dcpCollectorsFixture   = "./testdata/dcp-counters.csv"
	reloadMarkerMetric     = "DCGM_FI_DEV_GPU_UTIL_RATIO"
	fileWatcherProbeMetric = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"
	profilingMetricPrefix  = "DCGM_FI_PROF_"
	startupWaitTimeout     = 60 * time.Second
	reloadWaitTimeout      = 30 * time.Second
	reloadPollInterval     = 500 * time.Millisecond
	startupPollInterval    = 500 * time.Millisecond
)

// verifyNoReloadGoroutineLeaks checks reload scenarios for goroutine leaks while ignoring Ginkgo and idle HTTP workers.
func verifyNoReloadGoroutineLeaks(t testing.TB) {
	t.Helper()
	goleak.VerifyNone(
		t,
		// Ginkgo keeps suite coordination goroutines alive while a spec is
		// running; reload tests use GinkgoTB, so these are part of the runner.
		goleak.IgnoreTopFunction("github.com/onsi/ginkgo/v2/internal.(*Suite).runNode"),
		goleak.IgnoreTopFunction("github.com/onsi/ginkgo/v2/internal.RegisterForProgressSignal.func1"),
		goleak.IgnoreTopFunction("github.com/onsi/ginkgo/v2/internal/interrupt_handler.(*InterruptHandler).registerForInterrupts.func2"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	)
}

// copyCollectorsFixtureToTempFile copies a collectors CSV fixture into a mutable temp file.
func copyCollectorsFixtureToTempFile(t testing.TB, fixturePath string) string {
	t.Helper()

	data, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	tempFile := filepath.Join(t.TempDir(), "collectors.csv")
	require.NoError(t, os.WriteFile(tempFile, data, 0o600))

	return tempFile
}

// removeMetricFromCollectorsFile removes one metric row to prove a reload applied new collectors.
func removeMetricFromCollectorsFile(t testing.TB, collectorsPath string, metricName string) {
	t.Helper()

	data, err := os.ReadFile(collectorsPath)
	require.NoError(t, err)

	var (
		removed bool
		lines   []string
	)

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, metricName+",") {
			removed = true
			continue
		}
		lines = append(lines, line)
	}

	require.Truef(t, removed, "metric %s must exist in fixture before removal", metricName)
	lines = append(lines, fmt.Sprintf("# Removed %s to prove reload applied updated collectors", metricName))

	require.NoError(t, os.WriteFile(collectorsPath, []byte(strings.Join(lines, "\n")), 0o600))
}

// writeInvalidCollectorsFile replaces a collectors file with invalid CSV for failed-reload tests.
func writeInvalidCollectorsFile(t testing.TB, collectorsPath string) {
	t.Helper()

	invalidCollectors := "# Invalid CSV used to prove failed reloads keep last-good metrics.\n" +
		"DCGM_FI_DEV_GPU_TEMP_CELSIUS,not-a-prometheus-type,temp\n"
	require.NoError(t, os.WriteFile(collectorsPath, []byte(invalidCollectors), 0o600))
}

// parseMetricFamilies parses Prometheus text into metric families for reload assertions.
func parseMetricFamilies(metrics string) (map[string]*io_prometheus_client.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)

	return parser.TextToMetricFamilies(strings.NewReader(metrics))
}

// fetchMetricFamilies scrapes a metrics URL and parses the Prometheus response.
func fetchMetricFamilies(metricsURL string) (map[string]*io_prometheus_client.MetricFamily, int, error) {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(metricsURL)
	if err != nil {
		return nil, -1, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	if len(body) == 0 {
		return nil, resp.StatusCode, fmt.Errorf("empty metrics response")
	}

	metricFamilies, err := parseMetricFamilies(string(body))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return metricFamilies, resp.StatusCode, nil
}

// validateMetricFamilyAvailable verifies one metric family is present after a scrape.
func validateMetricFamilyAvailable(metricsURL string, metricName string) error {
	return validateMetricFamilyState(metricsURL, []string{metricName}, nil)
}

// validateMetricFamilyState verifies required metric families exist and forbidden ones do not.
func validateMetricFamilyState(metricsURL string, required []string, forbidden []string) error {
	metricFamilies, statusCode, err := fetchMetricFamilies(metricsURL)
	if err != nil {
		return fmt.Errorf("scrape failed with status %d: %w", statusCode, err)
	}
	for _, metricName := range required {
		if !hasMetricFamily(metricFamilies, metricName) {
			return fmt.Errorf("metric family %s missing after scrape", metricName)
		}
	}
	for _, metricName := range forbidden {
		if hasMetricFamily(metricFamilies, metricName) {
			return fmt.Errorf("metric family %s unexpectedly present after scrape", metricName)
		}
	}
	return nil
}

// assertMetricFamilyContinuouslyAvailable verifies one metric family remains scrapeable for a duration.
func assertMetricFamilyContinuouslyAvailable(
	t testing.TB,
	metricsURL string,
	metricName string,
	duration time.Duration,
	beforeScrape func(iteration int),
) {
	t.Helper()

	assertMetricFamilyStateContinuously(t, metricsURL, []string{metricName}, nil, duration, beforeScrape)
}

// assertMetricFamilyStateContinuously checks metric-family presence across repeated scrapes.
func assertMetricFamilyStateContinuously(
	t testing.TB,
	metricsURL string,
	required []string,
	forbidden []string,
	duration time.Duration,
	beforeScrape func(iteration int),
) {
	t.Helper()

	deadline := time.Now().Add(duration)
	iteration := 0
	for time.Now().Before(deadline) {
		if beforeScrape != nil {
			beforeScrape(iteration)
		}

		require.NoError(t, validateMetricFamilyState(metricsURL, required, forbidden))
		iteration++
		time.Sleep(100 * time.Millisecond)
	}
	require.Greater(t, iteration, 0, "continuous scrape assertion must perform at least one scrape")
}

// profilingMetricNames returns sorted profiling metric-family names from a scrape.
func profilingMetricNames(metricFamilies map[string]*io_prometheus_client.MetricFamily) []string {
	names := make([]string, 0)
	for name := range metricFamilies {
		if strings.HasPrefix(name, profilingMetricPrefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// hasMetricFamily reports whether a parsed scrape contains the named metric family.
func hasMetricFamily(metricFamilies map[string]*io_prometheus_client.MetricFamily, metricName string) bool {
	_, exists := metricFamilies[metricName]
	return exists
}

// includesAllMetricFamilies reports whether all required metric families are present.
func includesAllMetricFamilies(metricFamilies map[string]*io_prometheus_client.MetricFamily, required []string) bool {
	for _, name := range required {
		if !hasMetricFamily(metricFamilies, name) {
			return false
		}
	}
	return true
}

// hotReloadsCompleted counts the reloads the exporter has logged as finished.
func hotReloadsCompleted(process *hostExporterProcess) int {
	return strings.Count(process.output.String(), "Hot reload complete")
}

// waitForHotReload waits until the exporter logs one more finished reload than
// completedBefore, so a test continues when the reload is done and not after a
// fixed delay.
func waitForHotReload(t testing.TB, process *hostExporterProcess, completedBefore int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return hotReloadsCompleted(process) > completedBefore
	}, reloadWaitTimeout, 100*time.Millisecond, "reload did not complete; output:\n%s", process.output)
}

// startTestExporter starts dcgm-exporter with a mutable collectors file and returns cleanup controls.
func startTestExporter(t testing.TB, collectors string) (*hostExporterProcess, string, func()) {
	t.Helper()

	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startFastExporterAndWait(
		t,
		metricsURL,
		"--collectors", collectors,
		"--address", fmt.Sprintf(":%d", port),
	)

	cleanup := func() {
		t.Helper()
		process.terminate(t)
	}

	return process, metricsURL, cleanup
}

// requireProfilingSupport skips until profiling support has been established
// by the CLI probe or by the caller preparing a direct native run.
func requireProfilingSupport(t testing.TB) {
	t.Helper()
	if os.Getenv("E2E_REQUIRE_PROFILING") != "1" {
		t.Skip("profiling requires a support check; run the e2e CLI or set E2E_REQUIRE_PROFILING=1 on a prepared host")
	}
}

// waitForProfilingMetrics requires output after profiling support was established.
func waitForProfilingMetrics(
	t testing.TB, metricsURL string,
) ([]string, map[string]*io_prometheus_client.MetricFamily) {
	t.Helper()

	deadline := time.Now().Add(startupWaitTimeout)
	for time.Now().Before(deadline) {
		resp, statusCode, err := httpGet(t, metricsURL)
		if err == nil && statusCode == 200 && len(resp) > 0 {
			metricFamilies, parseErr := parseMetricFamilies(resp)
			if parseErr == nil {
				names := profilingMetricNames(metricFamilies)
				if len(names) > 0 {
					return names, metricFamilies
				}
			}
		}
		time.Sleep(reloadPollInterval)
	}

	require.Failf(t, "profiling metrics did not become available",
		"No %s metric families became available within %s after profiling support was established",
		profilingMetricPrefix, startupWaitTimeout)
	return nil, nil
}

// waitForMetricFamilies waits until parsed metrics satisfy the supplied predicate.
func waitForMetricFamilies(
	t testing.TB,
	metricsURL string,
	timeout time.Duration,
	predicate func(map[string]*io_prometheus_client.MetricFamily) bool,
	description string,
) map[string]*io_prometheus_client.MetricFamily {
	t.Helper()

	var (
		lastErr         error
		lastResponseLen int
		metricFamilies  map[string]*io_prometheus_client.MetricFamily
	)

	require.Eventually(t, func() bool {
		resp, statusCode, err := httpGet(t, metricsURL)
		if err != nil {
			lastErr = err
			return false
		}
		if statusCode != 200 || len(resp) == 0 {
			lastErr = fmt.Errorf("unexpected response status=%d len=%d", statusCode, len(resp))
			lastResponseLen = len(resp)
			return false
		}

		metricFamilies, err = parseMetricFamilies(resp)
		if err != nil {
			lastErr = err
			lastResponseLen = len(resp)
			return false
		}

		lastErr = nil
		lastResponseLen = len(resp)
		return predicate(metricFamilies)
	}, timeout, reloadPollInterval, "%s; last_error=%v last_response_len=%d",
		description, lastErr, lastResponseLen)

	return metricFamilies
}

// runSIGHUPReloadPreservesProfilingMetrics proves the profiling reload path:
// after a config-only reload, the updated registry must still export the same
// profiling metric families that were present before reload.
func runSIGHUPReloadPreservesProfilingMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	requireProfilingSupport(t)

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, dcpCollectorsFixture)
	exporter, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	beforeProfiling, beforeFamilies := waitForProfilingMetrics(t, metricsURL)
	require.True(t, hasMetricFamily(beforeFamilies, reloadMarkerMetric),
		"reload marker metric must be present before SIGHUP reload")

	removeMetricFromCollectorsFile(t, collectorsFile, reloadMarkerMetric)

	t.Log("Triggering SIGHUP reload...")
	exporter.signal(t, syscall.SIGHUP)

	afterFamilies := waitForMetricFamilies(
		t, metricsURL, reloadWaitTimeout,
		func(metricFamilies map[string]*io_prometheus_client.MetricFamily) bool {
			return !hasMetricFamily(metricFamilies, reloadMarkerMetric) &&
				includesAllMetricFamilies(metricFamilies, beforeProfiling)
		},
		"SIGHUP reload should apply the updated collectors file and preserve profiling metrics",
	)

	assert.False(t, hasMetricFamily(afterFamilies, reloadMarkerMetric))
	for _, name := range beforeProfiling {
		assert.Containsf(t, afterFamilies, name, "profiling metric %s should survive SIGHUP reload", name)
	}
}

// runSIGHUPReloadPreservesEventWatches exercises multiple collection cadences
// and every clock/XID collector on the shared DCGM session. COUNT families are
// emitted without injected events; TOTAL families depend on observed events.
func runSIGHUPReloadPreservesEventWatches(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	configFile, err := filepath.Abs("./testdata/multi-group-events.yaml")
	require.NoError(t, err)
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startExporterAndWaitForFamilies(t, metricsURL, nil, nil,
		"--config-file", configFile,
		"--address", fmt.Sprintf(":%d", port),
		"--devices", "f",
		"--switch-devices", "g",
		"--collect-interval", "1000",
		"--clock-events-count-window-size", "5000",
		"--xid-count-window-size", "5000",
	)
	defer process.terminate(t)

	required := []string{"DCGM_EXP_CLOCK_EVENTS_COUNT", "DCGM_EXP_XID_ERRORS_COUNT"}
	waitForMetricFamilies(t, metricsURL, startupWaitTimeout,
		func(families map[string]*io_prometheus_client.MetricFamily) bool {
			return includesAllMetricFamilies(families, required)
		}, "event COUNT families should be present before reload")

	for reload := 0; reload < 3; reload++ {
		completed := hotReloadsCompleted(process)
		process.signal(t, syscall.SIGHUP)
		deadline := time.Now().Add(reloadWaitTimeout)
		for hotReloadsCompleted(process) == completed {
			require.NoError(t, validateMetricFamilyState(metricsURL, required, nil))
			require.True(t, time.Now().Before(deadline), "reload did not complete; output:\n%s", process.output.String())
			time.Sleep(100 * time.Millisecond)
		}
		assertMetricFamilyStateContinuously(t, metricsURL, required, nil, 5*time.Second, nil)
	}
	require.NotContains(t, process.output.String(), "dcgmGetValuesSince_v2 failed",
		"background event pollers must also retain their native watches")
}

// runFileWatcherReloadPreservesProfilingMetrics exercises the fsnotify path:
// changing the watched CSV must reload the registry and keep previously
// exported profiling metric families intact.
func runFileWatcherReloadPreservesProfilingMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}
	requireProfilingSupport(t)

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, dcpCollectorsFixture)
	_, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	beforeProfiling, beforeFamilies := waitForProfilingMetrics(t, metricsURL)
	require.True(t, hasMetricFamily(beforeFamilies, reloadMarkerMetric),
		"reload marker metric must be present before file-watcher reload")

	removeMetricFromCollectorsFile(t, collectorsFile, reloadMarkerMetric)

	afterFamilies := waitForMetricFamilies(
		t, metricsURL, reloadWaitTimeout,
		func(metricFamilies map[string]*io_prometheus_client.MetricFamily) bool {
			return !hasMetricFamily(metricFamilies, reloadMarkerMetric) &&
				includesAllMetricFamilies(metricFamilies, beforeProfiling)
		},
		"file-watcher reload should apply the updated collectors file and preserve profiling metrics",
	)

	assert.False(t, hasMetricFamily(afterFamilies, reloadMarkerMetric))
	for _, name := range beforeProfiling {
		assert.Containsf(t, afterFamilies, name, "profiling metric %s should survive file-watcher reload", name)
	}
}

// runSIGHUPReloadFailureKeepsLastGoodMetrics proves a malformed collector
// update cannot make /metrics empty. Failed config reloads must leave the
// previously serving registry in place.
func runSIGHUPReloadFailureKeepsLastGoodMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, defaultCollectorsFile(t))
	exporter, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	waitForMetricFamilies(
		t, metricsURL, startupWaitTimeout,
		func(metricFamilies map[string]*io_prometheus_client.MetricFamily) bool {
			return hasMetricFamily(metricFamilies, reloadMarkerMetric)
		},
		"startup metrics should include the reload marker metric before failed SIGHUP reloads",
	)

	writeInvalidCollectorsFile(t, collectorsFile)
	assertMetricFamilyContinuouslyAvailable(
		t, metricsURL, reloadMarkerMetric, 5*time.Second,
		func(iteration int) {
			if iteration%2 == 0 {
				exporter.signal(t, syscall.SIGHUP)
			}
		},
	)
}

// runFileWatcherReloadFailureKeepsLastGoodMetrics covers the fsnotify path
// for the same last-good contract as SIGHUP reloads.
func runFileWatcherReloadFailureKeepsLastGoodMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, defaultCollectorsFile(t))
	_, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	waitForMetricFamilies(
		t, metricsURL, startupWaitTimeout,
		func(metricFamilies map[string]*io_prometheus_client.MetricFamily) bool {
			return hasMetricFamily(metricFamilies, reloadMarkerMetric) &&
				hasMetricFamily(metricFamilies, fileWatcherProbeMetric)
		},
		"startup metrics should include probe metrics before failed file-watcher reload",
	)

	removeMetricFromCollectorsFile(t, collectorsFile, fileWatcherProbeMetric)
	waitForMetricFamilies(
		t, metricsURL, reloadWaitTimeout,
		func(metricFamilies map[string]*io_prometheus_client.MetricFamily) bool {
			return hasMetricFamily(metricFamilies, reloadMarkerMetric) &&
				!hasMetricFamily(metricFamilies, fileWatcherProbeMetric)
		},
		"file watcher should apply a valid collectors update before the failed reload check",
	)

	writeInvalidCollectorsFile(t, collectorsFile)
	assertMetricFamilyStateContinuously(
		t, metricsURL,
		[]string{reloadMarkerMetric},
		[]string{fileWatcherProbeMetric},
		5*time.Second,
		nil,
	)
}

// runConcurrentScrapeDuringSIGHUPReloads keeps the public /metrics contract
// under pressure while reloads are being requested. A scrape may see either
// the previous or replacement registry, but it must always be parseable and
// include the baseline metric family.
func runConcurrentScrapeDuringSIGHUPReloads(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, defaultCollectorsFile(t))
	exporter, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	waitForMetricFamilies(
		t, metricsURL, startupWaitTimeout,
		func(metricFamilies map[string]*io_prometheus_client.MetricFamily) bool {
			return hasMetricFamily(metricFamilies, reloadMarkerMetric)
		},
		"startup metrics should include the reload marker metric before concurrent reloads",
	)

	stopScrapes := make(chan struct{})
	scrapeErrors := make(chan error, 64)
	var scrapeWG sync.WaitGroup
	scrapeWG.Add(1)
	go func() {
		defer scrapeWG.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stopScrapes:
				return
			case <-ticker.C:
				if err := validateMetricFamilyAvailable(metricsURL, reloadMarkerMetric); err != nil {
					select {
					case scrapeErrors <- err:
					default:
					}
				}
			}
		}
	}()

	const reloads = 10
	for i := 0; i < reloads; i++ {
		exporter.signal(t, syscall.SIGHUP)
		time.Sleep(100 * time.Millisecond)
	}
	// Signals sent while a reload runs may merge, so one more reload after the
	// burst gives a point where every earlier reload has finished.
	completed := hotReloadsCompleted(exporter)
	exporter.signal(t, syscall.SIGHUP)
	waitForHotReload(t, exporter, completed)

	close(stopScrapes)
	scrapeWG.Wait()
	close(scrapeErrors)

	var errs []error
	for err := range scrapeErrors {
		errs = append(errs, err)
	}
	require.Empty(t, errs, "concurrent scrapes should stay parseable through reloads")
}

// runMultipleSIGHUPReloads verifies that the exporter can handle multiple SIGHUP signals
// and continues functioning correctly after each reload
func runMultipleSIGHUPReloads(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, defaultCollectorsFile(t))
	exporter, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	// Now we can programmatically trigger reloads!
	const numReloads = 5
	parser := expfmt.NewTextParser(model.UTF8Validation)
	for i := 0; i < numReloads; i++ {
		t.Logf("Reload iteration %d/%d", i+1, numReloads)

		// Verify metrics endpoint is accessible before reload
		resp, statusCode, err := httpGet(t, metricsURL)
		require.NoError(t, err, "Metrics endpoint should be accessible before reload")
		require.Equal(t, 200, statusCode)
		require.NotEmpty(t, resp, "Should return metrics before reload")

		// Parse metrics to verify they're valid
		mf, err := parser.TextToMetricFamilies(strings.NewReader(resp))
		require.NoError(t, err, "Should parse metrics before reload")
		require.Greater(t, len(mf), 0, "Should have metrics before reload")

		t.Log("Triggering reload...")
		completed := hotReloadsCompleted(exporter)
		exporter.signal(t, syscall.SIGHUP)

		// Wait for server to restart (race detector slows things down)
		var reloadedResp string
		require.Eventually(t, func() bool {
			r, _, e := httpGet(t, metricsURL)
			if e == nil && len(r) > 0 {
				reloadedResp = r
				return true
			}
			return false
		}, 30*time.Second, 500*time.Millisecond, "Metrics endpoint should be accessible after reload %d", i+1)

		// Parse metrics to verify they're still valid
		mf, err = parser.TextToMetricFamilies(strings.NewReader(reloadedResp))
		require.NoError(t, err, "Should parse metrics after reload")
		require.Greater(t, len(mf), 0, "Should have metrics after reload")

		waitForHotReload(t, exporter, completed)
	}

	t.Logf("Successfully completed %d reload cycles", numReloads)

	runtime.GC()
}

// TestGoroutineLeakOnReload verifies that goroutines don't leak during SIGHUP reloads
func TestGoroutineLeakOnReload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)

	goroutinesBefore := runtime.NumGoroutine()
	t.Logf("Goroutines before starting app: %d", goroutinesBefore)

	collectorsFile := copyCollectorsFixtureToTempFile(t, defaultCollectorsFile(t))
	exporter, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	goroutinesAfterStart := runtime.NumGoroutine()
	t.Logf("Goroutines after starting app: %d", goroutinesAfterStart)

	// Perform several reloads
	const numReloads = 3
	for i := 0; i < numReloads; i++ {
		t.Logf("Reload iteration %d", i+1)
		exporter.signal(t, syscall.SIGHUP)

		// Wait for server to restart
		require.Eventually(t, func() bool {
			r, _, e := httpGet(t, metricsURL)
			return e == nil && len(r) > 0
		}, 30*time.Second, 500*time.Millisecond, "Metrics should be accessible after reload %d", i+1)

		goroutinesAfterReload := runtime.NumGoroutine()
		t.Logf("Goroutines after reload %d: %d", i+1, goroutinesAfterReload)
	}

	// Note: cleanup is handled by deferred function
	// Force GC; deferred cleanup and goleak handle goroutine teardown.
	runtime.GC()

	goroutinesAfterShutdown := runtime.NumGoroutine()
	t.Logf("Goroutines after shutdown: %d", goroutinesAfterShutdown)

	// Allow some tolerance (goleak will catch actual leaks)
	const maxGoroutineGrowth = 15
	growth := goroutinesAfterShutdown - goroutinesBefore
	assert.LessOrEqual(t, growth, maxGoroutineGrowth,
		"Goroutine count should not grow significantly. Growth: %d", growth)
}
