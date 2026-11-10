//go:build dcgm_native_integration

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

package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

const (
	nativeResetChildEnv       = "DCGM_EXPORTER_NATIVE_RESET_TEST_CHILD"
	nativeResetProcessLimit   = 2 * time.Minute
	nativeResetTestLimit      = 90 * time.Second
	nativeResetScrapeAttempts = 12
)

// TestNativeLifecycleReset runs the native lifecycle scenario in a killable child process.
func TestNativeLifecycleReset(t *testing.T) {
	if os.Getenv(nativeResetChildEnv) == "1" {
		runNativeLifecycleResetInChild(t)
		return
	}

	executable, err := os.Executable()
	require.NoError(t, err)

	processCtx, cancel := context.WithTimeout(context.Background(), nativeResetProcessLimit)
	defer cancel()

	command := exec.CommandContext(
		processCtx,
		executable,
		"-test.run=^TestNativeLifecycleReset$",
		"-test.v",
		"-test.timeout="+nativeResetTestLimit.String(),
	)
	command.Env = nativeResetChildEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if processCtx.Err() != nil {
		t.Fatalf("native reset test exceeded its %s process deadline:\n%s", nativeResetProcessLimit, output)
	}
	require.NoError(t, err, "native reset child output:\n%s", output)
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "NATIVE_RESET_EVIDENCE ") {
			t.Log(line)
		}
	}
}

// nativeResetChildEnvironment marks the child as the only process that enters DCGM.
func nativeResetChildEnvironment(environment []string) []string {
	childEnvironment := make([]string, 0, len(environment)+1)
	for _, value := range environment {
		if !strings.HasPrefix(value, nativeResetChildEnv+"=") {
			childEnvironment = append(childEnvironment, value)
		}
	}
	return append(childEnvironment, nativeResetChildEnv+"=1")
}

// runNativeLifecycleResetInChild verifies provider replacement, watcher restart, and a parsed live scrape.
func runNativeLifecycleResetInChild(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.UseRemoteHE = false
	config.UseFakeGPUs = false
	config.NoHostname = true
	config.CollectInterval = 1000
	config.DCGMCleanupTimeout = 15 * time.Second
	config.MetricSource = appconfig.MetricSource{
		Kind: appconfig.MetricSourceInline,
		Fields: []appconfig.MetricField{{
			Name:           "DCGM_FI_DEV_GPU_UTIL",
			PrometheusType: "gauge",
			Help:           "GPU utilization in percent.",
		}},
	}

	if err := initializeDCGMProviderFunc(config); err != nil {
		t.Fatalf("initialize embedded DCGM: %v", err)
	}
	if err := initializeNVMLProviderFunc(); err != nil {
		// NVML is optional for this non-Kubernetes GPU metric and the reset path retries it.
		t.Logf("NVML initialization unavailable; DCGM fallback remains enabled: %v", err)
	}
	require.NotNil(t, dcgmprovider.Client())

	coord := initReloadCoordinator(config)
	cleanupDCGM := coord.cleanupDCGM
	initializeDCGM := coord.initializeDCGM
	var cleanupCalls atomic.Int32
	var reinitializeCalls atomic.Int32
	coord.cleanupDCGM = func() {
		cleanupCalls.Add(1)
		cleanupDCGM()
	}
	coord.initializeDCGM = func(config *appconfig.Config) error {
		reinitializeCalls.Add(1)
		return initializeDCGM(config)
	}

	nativeWatcher := newGenerationGPUBindUnbindWatcher(watcher.WithPollInterval(200 * time.Millisecond))
	var watcherStarts atomic.Int32
	watchWait := coord.cleanupWait(config, cleanupGPUWatchStart)
	coord.setGPUWatcher(watcher.NewRestartableGPUWatcher(
		func(ctx context.Context, onEvent watcher.GPUBindUnbindEventHandler) (func() error, error) {
			watcherStarts.Add(1)
			return nativeWatcher.Start(ctx, onEvent)
		},
		func(dcgm.BindUnbindEventState) {},
		watchWait.timeout,
	), watchWait)
	lifecycleCtx := context.Background()
	require.NoError(t, coord.startGPUWatcher(lifecycleCtx))

	coord.queryDCPMetrics(0)
	initialRegistry, deviceWatchList, err := buildRegistry(
		lifecycleCtx,
		config,
		coord.dcp,
	)
	require.NoError(t, err)

	metricsServer, cleanupServer, err := server.NewMetricsServer(config, deviceWatchList, initialRegistry)
	require.NoError(t, err)
	coord.setServer(metricsServer)

	coord.handle(lifecycleCtx, evGPUReinitialized)

	require.Equal(t, int32(1), cleanupCalls.Load(), "the reset must clean up the old DCGM provider once")
	require.Equal(t, int32(1), reinitializeCalls.Load(), "the reset must initialize one replacement DCGM provider")
	require.NotNil(t, dcgmprovider.Client(), "the replacement DCGM provider must remain published")
	require.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage, "the reset must publish its replacement before returning")
	require.False(t, metricsServer.IsReloadInProgress(), "the completed reset must restore normal serving")
	require.NotSame(t, initialRegistry, metricsServer.GetRegistry(), "the reset must publish a new registry")
	require.Equal(t, int32(2), watcherStarts.Load(), "the real GPU watcher must start before and after reset")
	require.False(t, coord.nativeCleanupBlocked.Load(), "a successful reset must not enter terminal cleanup state")
	t.Logf("NATIVE_RESET_EVIDENCE dcgm_cleanup_calls=%d dcgm_reinitialize_calls=%d watcher_starts=%d registry_replaced=true",
		cleanupCalls.Load(), reinitializeCalls.Load(), watcherStarts.Load())

	var statusCode int
	var responseBody string
	var metricFamilies map[string]*dto.MetricFamily
	var parseErr error
	for attempt := 0; attempt < nativeResetScrapeAttempts; attempt++ {
		response := httptest.NewRecorder()
		metricsServer.Metrics(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		statusCode = response.Code
		responseBody = response.Body.String()
		if statusCode == http.StatusOK {
			parser := expfmt.NewTextParser(model.LegacyValidation)
			metricFamilies, parseErr = parser.TextToMetricFamilies(strings.NewReader(responseBody))
			if parseErr == nil && len(metricFamilies["DCGM_FI_DEV_GPU_UTIL"].GetMetric()) > 0 {
				break
			}
		}
		time.Sleep(time.Second)
	}
	require.Equalf(t, http.StatusOK, statusCode, "post-reset scrape failed: %s", responseBody)
	require.NoError(t, parseErr, "post-reset metrics did not parse: %s", responseBody)
	require.NotEmpty(t, metricFamilies, "post-reset scrape must contain parsed metrics")
	require.Contains(t, metricFamilies, "DCGM_FI_DEV_GPU_UTIL")
	require.NotEmpty(t, metricFamilies["DCGM_FI_DEV_GPU_UTIL"].GetMetric())
	t.Logf("NATIVE_RESET_EVIDENCE parsed_metric_families=%d gpu_util_samples=%d",
		len(metricFamilies), len(metricFamilies["DCGM_FI_DEV_GPU_UTIL"].GetMetric()))

	// Failed native stops or retirement must end this process without provider teardown.
	require.NoError(t, coord.stopGPUWatcher(), "the restarted GPU watcher must stop safely")
	t.Log("NATIVE_RESET_EVIDENCE restarted_watcher_stopped=true")
	retiredRegistry := metricsServer.ClearRegistry()
	require.NoError(t, waitForRegistryRetirement(
		lifecycleCtx,
		retiredRegistry,
		coord.cleanupWait(config, cleanupReload),
	), "the replacement registry must retire before provider cleanup")
	cleanupServer()
	cleanupNVMLProviderFunc()
	if client := dcgmprovider.Client(); client != nil {
		client.Cleanup()
	}
	t.Log("NATIVE_RESET_EVIDENCE registry_retired=true nvml_cleanup=true dcgm_shutdown=true")
}
