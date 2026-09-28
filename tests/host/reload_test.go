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
	reloadMarkerMetric     = "DCGM_FI_DEV_GPU_UTIL"
	fileWatcherProbeMetric = "DCGM_FI_DEV_GPU_TEMP"
	profilingMetricPrefix  = "DCGM_FI_PROF_"
	startupWaitTimeout     = 60 * time.Second
	reloadWaitTimeout      = 30 * time.Second
	reloadPollInterval     = 500 * time.Millisecond
)

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
		"DCGM_FI_DEV_GPU_TEMP,not-a-prometheus-type,temp\n"
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

// startTestExporter starts dcgm-exporter with a mutable collectors file and returns cleanup controls.
func startTestExporter(t testing.TB, collectors string) (*hostExporterProcess, string, func()) {
	t.Helper()

	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startExporterAndWait(
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

// waitForProfilingMetricsOrSkip waits for DCP metrics and skips when the host cannot emit them.
func waitForProfilingMetricsOrSkip(
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

	t.Skipf("No %s metric families became available within %s; DCP likely unsupported in this environment",
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

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, dcpCollectorsFixture)
	exporter, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	beforeProfiling, beforeFamilies := waitForProfilingMetricsOrSkip(t, metricsURL)
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

// runFileWatcherReloadPreservesProfilingMetrics exercises the fsnotify path:
// changing the watched CSV must reload the registry and keep previously
// exported profiling metric families intact.
func runFileWatcherReloadPreservesProfilingMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)
	collectorsFile := copyCollectorsFixtureToTempFile(t, dcpCollectorsFixture)
	_, metricsURL, cleanup := startTestExporter(t, collectorsFile)
	defer cleanup()

	beforeProfiling, beforeFamilies := waitForProfilingMetricsOrSkip(t, metricsURL)
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
	collectorsFile := copyCollectorsFixtureToTempFile(t, "./testdata/default-counters.csv")
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
	collectorsFile := copyCollectorsFixtureToTempFile(t, "./testdata/default-counters.csv")
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
	collectorsFile := copyCollectorsFixtureToTempFile(t, "./testdata/default-counters.csv")
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
	time.Sleep(time.Second)

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
	collectorsFile := copyCollectorsFixtureToTempFile(t, "./testdata/default-counters.csv")
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

		time.Sleep(2 * time.Second)
	}

	t.Logf("Successfully completed %d reload cycles", numReloads)

	runtime.GC()
	time.Sleep(500 * time.Millisecond)
}

// TestGoroutineLeakOnReload verifies that goroutines don't leak during SIGHUP reloads
func TestGoroutineLeakOnReload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer verifyNoReloadGoroutineLeaks(t)

	goroutinesBefore := runtime.NumGoroutine()
	t.Logf("Goroutines before starting app: %d", goroutinesBefore)

	collectorsFile := copyCollectorsFixtureToTempFile(t, "./testdata/default-counters.csv")
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
	// Force GC and wait for goroutines to cleanup
	runtime.GC()
	time.Sleep(500 * time.Millisecond)

	goroutinesAfterShutdown := runtime.NumGoroutine()
	t.Logf("Goroutines after shutdown: %d", goroutinesAfterShutdown)

	// Allow some tolerance (goleak will catch actual leaks)
	const maxGoroutineGrowth = 15
	growth := goroutinesAfterShutdown - goroutinesBefore
	assert.LessOrEqual(t, growth, maxGoroutineGrowth,
		"Goroutine count should not grow significantly. Growth: %d", growth)
}
