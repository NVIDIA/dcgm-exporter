/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

// TestMain uses goleak to catch goroutines that outlive their test. The
// reload coordinator, the metrics server goroutine, and the file/GPU
// watchers are all long-lived; any test that forgets to cancel a context
// fails loudly here rather than silently leaking into subsequent tests.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestRunDCGMExporter_RegistersGPULifecycleWatchBeforeInitialRegistry checks startup registers the GPU watcher before building its first registry.
func TestRunDCGMExporter_RegistersGPULifecycleWatchBeforeInitialRegistry(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	watchRegistered := false
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).DoAndReturn(func(uint) ([]dcgm.MetricGroup, error) {
		assert.True(t, watchRegistered, "lifecycle watch must be registered before DCP discovery")
		return nil, errors.New("profiling unavailable")
	})
	mock.EXPECT().
		WatchFieldValue(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			time.Second,
			time.Duration(0),
			2,
		).
		DoAndReturn(func(uint, dcgm.Short, time.Duration, time.Duration, int) error {
			watchRegistered = true
			return nil
		})
	mock.EXPECT().Cleanup()

	stopAfterBuild := errors.New("stop after initial registry build")
	initializeDCGMProviderFunc = successfulDCGMInitialize
	initializeNVMLProviderFunc = func() error {
		assert.True(t, watchRegistered, "lifecycle watch must be registered before NVML discovery")
		return nil
	}
	cleanupNVMLProviderFunc = func() {}
	buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
		*registry.Registry,
		devicewatchlistmanager.Manager,
		error,
	) {
		assert.True(t, watchRegistered, "lifecycle watch must be registered before topology discovery")
		return nil, nil, stopAfterBuild
	}

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	setGPUBindUnbindTestConfig(t, ctx)

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.ErrorIs(t, err, stopAfterBuild)
}

func TestStartupRetriesOnlyConfirmedGPUDetach(t *testing.T) {
	nativeErr := &dcgm.Error{Code: dcgm.DCGM_ST_BADPARAM}
	detachErr := errors.Join(errGPUDetachedDuringBuild, nativeErr)
	stopErr := errors.New("invalid collector configuration")
	for _, tt := range []struct {
		name           string
		firstError     error
		repeated       bool
		cancel         bool
		wantCalls      int
		wantError      error
		cleanupPending bool
		wantCleanups   int
	}{
		{name: "fresh snapshot after detach", firstError: detachErr, wantCalls: 2, wantError: stopErr, wantCleanups: 1},
		{name: "unclassified native error remains fatal", firstError: nativeErr, wantCalls: 1, wantError: nativeErr, wantCleanups: 1},
		{name: "repeated detach is bounded", firstError: detachErr, repeated: true, wantCalls: maxGPURecoveryRetries + 1, wantError: nativeErr, wantCleanups: 1},
		{name: "cancellation stops retry", firstError: detachErr, cancel: true, wantCalls: 1, wantError: context.Canceled, wantCleanups: 1},
		{name: "unfinished rollback blocks retry and native cleanup", firstError: detachErr, cleanupPending: true, wantCalls: 1, wantError: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			client := withMockDCGMClient(t)
			profilingQueries := 0
			client.EXPECT().GetSupportedMetricGroups(uint(0)).DoAndReturn(func(uint) ([]dcgm.MetricGroup, error) {
				profilingQueries++
				return nil, errors.New("profiling unavailable")
			}).AnyTimes()
			client.EXPECT().Cleanup().Times(tt.wantCleanups)
			initializeDCGMProviderFunc = successfulDCGMInitialize
			initializeNVMLProviderFunc = func() error { return nil }
			nvmlCleanups := 0
			cleanupNVMLProviderFunc = func() { nvmlCleanups++ }
			done := make(chan struct{})
			defer close(done)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				calls++
				if tt.cancel {
					cancel()
				}
				if calls > 1 && !tt.repeated {
					return nil, nil, stopErr
				}
				if tt.cleanupPending {
					return nil, nil, collectorRegistrationError{err: tt.firstError, completionSignals: []<-chan struct{}{done}}
				}
				return nil, nil, tt.firstError
			}
			cliCtx := newTestCLIContext(t)
			require.NoError(t, cliCtx.Set(CLIDCGMCleanupTimeout, "10ms"))
			err := runDCGMExporter(ctx, cliCtx, nil)
			require.ErrorIs(t, err, tt.wantError)
			require.Equal(t, tt.wantCalls, calls)
			require.Equal(t, tt.wantCalls, profilingQueries, "each refreshed topology needs a fresh capability snapshot")
			require.Equal(t, tt.wantCleanups, nvmlCleanups)
		})
	}
}

// TestRunDCGMExporter_FailsBeforeRegistryBuildWhenGPULifecycleWatchCannotStart checks a watcher startup error stops registry creation.
func TestRunDCGMExporter_FailsBeforeRegistryBuildWhenGPULifecycleWatchCannotStart(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	setupErr := errors.New("bind/unbind field unsupported")
	mock.EXPECT().
		WatchFieldValue(
			uint(0),
			dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
			time.Second,
			time.Duration(0),
			2,
		).
		Return(setupErr)
	mock.EXPECT().Cleanup()

	initializeDCGMProviderFunc = successfulDCGMInitialize
	initializeNVMLProviderFunc = func() error {
		t.Fatal("NVML must not initialize after the explicitly enabled lifecycle watch fails")
		return nil
	}
	cleanupNVMLProviderFunc = func() {}
	buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
		*registry.Registry,
		devicewatchlistmanager.Manager,
		error,
	) {
		t.Fatal("registry must not build without the explicitly enabled lifecycle watch")
		return nil, nil, nil
	}

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	setGPUBindUnbindTestConfig(t, ctx)

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.ErrorIs(t, err, setupErr)
	assert.Contains(t, err.Error(), "start GPU bind/unbind watcher")
}

func TestRunDCGMExporterOutputAvailability(t *testing.T) {
	const prometheusMetrics = `metrics:
  - name: scrape
    file: /etc/dcgm-exporter/default-counters.csv
    every: 10s
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`
	tests := []struct {
		name          string
		yaml          string
		wantConfigErr string
	}{
		{name: "default Prometheus"},
		{name: "version 1 Prometheus", yaml: "version: 1\n"},
		{name: "version 2 Prometheus", yaml: "version: 2\n"},
		{name: "version 2 Prometheus output", yaml: "version: 2\n" + prometheusMetrics},
		{
			name: "OTLP output is not accepted",
			yaml: `version: 2
metrics:
  - name: scrape
    file: /etc/dcgm-exporter/default-counters.csv
outputs:
  telemetry: {type: otlp, schema: dcgm/v1}
`,
			wantConfigErr: "outputs.telemetry.type: must be prometheus",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			ctx := newTestCLIContext(t)
			if tt.yaml != "" {
				configFile := filepath.Join(t.TempDir(), "config.yaml")
				require.NoError(t, os.WriteFile(configFile, []byte(tt.yaml), 0o600))
				require.NoError(t, ctx.Set(CLIConfigFile, configFile))
			}

			providerCalled := false
			providerErr := errors.New("DCGM unavailable")
			initializeDCGMProviderFunc = func(config *appconfig.Config) error {
				providerCalled = true
				assert.False(t, config.DisablePrometheus)
				assert.False(t, config.EnableOTLP)
				return providerErr
			}
			buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				t.Fatal("registry must not build after DCGM initialization fails")
				return nil, nil, nil
			}

			err := runDCGMExporter(context.Background(), ctx, nil)

			if tt.wantConfigErr != "" {
				require.ErrorContains(t, err, tt.wantConfigErr)
				assert.False(t, providerCalled)
			} else {
				require.ErrorIs(t, err, providerErr)
				assert.True(t, providerCalled)
			}
		})
	}
}

// TestReloadCoordinatorCleanupDCGMAllowsMissingClient verifies that shutdown
// remains safe after a GPU recovery attempt clears DCGM before reinitializing it.
func TestReloadCoordinatorCleanupDCGMAllowsMissingClient(t *testing.T) {
	originalClient := dcgmprovider.Client()
	dcgmprovider.SetClient(nil)
	t.Cleanup(func() { dcgmprovider.SetClient(originalClient) })

	coord := newReloadCoordinator()

	require.NotPanics(t, coord.cleanupDCGM)
}

// TestReloadCoordinatorRetriesDCGMInitialization verifies that a failed GPU
// recovery provider setup keeps the recovery stage and schedules a retry.
func TestReloadCoordinatorRetriesDCGMInitialization(t *testing.T) {
	coord := newTestCoordinator(t)
	coord.gpuRecoveryConfig = &appconfig.Config{}
	coord.gpuRecoveryStage = gpuRecoveryInitializeDCGM
	wantErr := errors.New("DCGM reconnect failed")
	coord.initializeDCGM = func(*appconfig.Config) error { return wantErr }
	var scheduleCalls atomic.Int32
	coord.scheduleRetry = func(_ context.Context, _ time.Duration, _ func()) context.CancelFunc {
		scheduleCalls.Add(1)
		return func() {}
	}

	coord.continueGPULifecycleRecovery(context.Background(), 1)

	assert.Equal(t, int32(1), scheduleCalls.Load())
	assert.Equal(t, gpuRecoveryInitializeDCGM, coord.gpuRecoveryStage)
}

// TestRunDCGMExporter_RejectsPprofWithoutWebConfigFileBeforeStartup checks invalid profiling settings stop startup before DCGM is opened.
func TestRunDCGMExporter_RejectsPprofWithoutWebConfigFileBeforeStartup(t *testing.T) {
	restoreStartupSeams(t)
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIEnablePprof, "true"))

	initializeDCGMProviderFunc = func(*appconfig.Config) error {
		t.Fatal("DCGM must not initialize after pprof config validation fails")
		return nil
	}

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), CLIEnablePprof)
	assert.Contains(t, err.Error(), CLIWebConfigFile)
}

// TestRunDCGMExporter_RejectsPprofWithUnprotectedWebConfigBeforeStartup checks
// that profiling needs a web config file that turns on TLS or basic auth.
func TestRunDCGMExporter_RejectsPprofWithUnprotectedWebConfigBeforeStartup(t *testing.T) {
	restoreStartupSeams(t)
	ctx := newTestCLIContext(t)
	webConfig := filepath.Join(t.TempDir(), "web-config.yml")
	require.NoError(t, os.WriteFile(webConfig, []byte("http_server_config:\n  http2: true\n"), 0o600))
	require.NoError(t, ctx.Set(CLIEnablePprof, "true"))
	require.NoError(t, ctx.Set(CLIWebConfigFile, webConfig))

	initializeDCGMProviderFunc = func(*appconfig.Config) error {
		t.Fatal("DCGM must not initialize after pprof config validation fails")
		return nil
	}

	err := runDCGMExporter(context.Background(), ctx, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "tls_server_config")
	assert.Contains(t, err.Error(), "basic_auth_users")
}

// TestRunDCGMExporter_NVMLStartupPolicy checks when startup opens NVML and how its errors are handled.
func TestRunDCGMExporter_NVMLStartupPolicy(t *testing.T) {
	buildStop := errors.New("stop after initial registry build")
	tests := []struct {
		name                   string
		kubernetes             bool
		disableStartupValidate bool
		useFakeGPUs            bool
		initializeError        error
		wantContinue           bool
		wantInitializeCalls    int32
		wantCleanupCalls       int32
	}{
		{
			name:                "non-Kubernetes success",
			wantContinue:        true,
			wantInitializeCalls: 1,
			wantCleanupCalls:    1,
		},
		{
			name:                "non-Kubernetes failure falls back",
			initializeError:     errors.New("NVML unavailable"),
			wantContinue:        true,
			wantInitializeCalls: 1,
			wantCleanupCalls:    1,
		},
		{
			name:                "Kubernetes validated failure remains fatal",
			kubernetes:          true,
			initializeError:     errors.New("NVML unavailable"),
			wantInitializeCalls: 1,
		},
		{
			name:                   "Kubernetes failure with validation disabled falls back",
			kubernetes:             true,
			disableStartupValidate: true,
			initializeError:        errors.New("NVML unavailable"),
			wantContinue:           true,
			wantInitializeCalls:    1,
			wantCleanupCalls:       1,
		},
		{
			name:                "fake-GPU mode uses the optional NVML lifecycle",
			useFakeGPUs:         true,
			wantContinue:        true,
			wantInitializeCalls: 1,
			wantCleanupCalls:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			mock := withMockDCGMClient(t)
			mock.EXPECT().
				GetSupportedMetricGroups(uint(0)).
				Return(nil, errors.New("profiling unsupported")).
				AnyTimes()
			mock.EXPECT().Cleanup()

			initializeDCGMProviderFunc = successfulDCGMInitialize

			var initializeCalls atomic.Int32
			initializeNVMLProviderFunc = func() error {
				initializeCalls.Add(1)
				return tt.initializeError
			}
			var cleanupCalls atomic.Int32
			cleanupNVMLProviderFunc = func() {
				cleanupCalls.Add(1)
			}

			var buildCalls atomic.Int32
			buildRegistryFunc = func(
				context.Context,
				*appconfig.Config,
				counters.DCPCapabilities,
			) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				buildCalls.Add(1)
				return nil, nil, buildStop
			}

			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIKubernetes, fmt.Sprintf("%t", tt.kubernetes)))
			require.NoError(t, ctx.Set(CLIDisableStartupValidate, fmt.Sprintf("%t", tt.disableStartupValidate)))
			require.NoError(t, ctx.Set(CLIUseFakeGPUs, fmt.Sprintf("%t", tt.useFakeGPUs)))

			err := runDCGMExporter(context.Background(), ctx, nil)

			if tt.wantContinue {
				require.ErrorIs(t, err, buildStop)
				assert.Equal(t, int32(1), buildCalls.Load())
			} else {
				require.ErrorIs(t, err, tt.initializeError)
				assert.Zero(t, buildCalls.Load())
			}
			assert.Equal(t, tt.wantInitializeCalls, initializeCalls.Load())
			assert.Equal(t, tt.wantCleanupCalls, cleanupCalls.Load())
		})
	}
}

// TestRunDCGMExporter_InitialRegistryBuildFailure checks a failed first registry build stops startup and cleans resources.
func TestRunDCGMExporter_InitialRegistryBuildFailure(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	mock.EXPECT().Cleanup()

	initializeDCGMProviderFunc = successfulDCGMInitialize
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}
	buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return nil, nil, errors.New("registry build failed")
	}
	newMetricsServerFunc = func(*appconfig.Config, devicewatchlistmanager.Manager, *registry.Registry, func()) (*server.MetricsServer, func(), error) {
		t.Fatal("metrics server must not start after registry build fails")
		return nil, nil, nil
	}

	err := runDCGMExporter(context.Background(), newTestCLIContext(t), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "registry build failed")
}

// TestRunDCGMExporter_MetricsServerFailureCleansRegistry checks server startup failure cleans the built registry.
func TestRunDCGMExporter_MetricsServerFailureCleansRegistry(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	mock.EXPECT().Cleanup()

	initializeDCGMProviderFunc = successfulDCGMInitialize
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}
	reg := registry.NewRegistry()
	buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return reg, topologyManager(), nil
	}
	newMetricsServerFunc = func(*appconfig.Config, devicewatchlistmanager.Manager, *registry.Registry, func()) (*server.MetricsServer, func(), error) {
		return nil, nil, errors.New("listen setup failed")
	}

	err := runDCGMExporter(context.Background(), newTestCLIContext(t), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen setup failed")
}

// TestRunDCGMExporter_RejectsInvalidSettingsBeforeDCGMInit checks that startup
// refuses a bad Kubernetes ID type, a bad or missing pod label pattern and a
// bad listen address, with Kubernetes off, before DCGM is initialized, and that config
// validate and config show report the same flag.
func TestRunDCGMExporter_RejectsInvalidSettingsBeforeDCGMInit(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		flag string
	}{
		{"GPU ID type", []string{"--kubernetes-gpu-id-type", "bad"}, CLIKubernetesGPUIDType},
		{"pod label pattern", []string{"--kubernetes-pod-label-allowlist-regex", "("}, CLIKubernetesPodLabelAllowlistRegex},
		{"pod labels without allowlist", []string{"--kubernetes-enable-pod-labels"}, CLIKubernetesPodLabelAllowlistRegex},
		{"empty pod label pattern", []string{"--kubernetes-enable-pod-labels", "--kubernetes-pod-label-allowlist-regex="}, CLIKubernetesPodLabelAllowlistRegex},
		{"listen address", []string{"--address", "bad"}, CLIAddress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreStartupSeams(t)
			unsetFlagEnvVars(t, NewApp("test").Flags)
			initializeDCGMProviderFunc = func(*appconfig.Config) error {
				t.Fatal("DCGM must not be initialized for an invalid setting")
				return nil
			}

			app := NewApp("test")
			app.Action = func(c *cli.Context) error { return runDCGMExporter(context.Background(), c, nil) }
			err := app.Run(append([]string{"dcgm-exporter"}, tc.args...))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.flag)
			assert.Equal(t, exitFailure, ExitStatus(err))

			for _, command := range []string{"validate", "show"} {
				_, stderr, err := inspectionRun(t, append([]string{"config", command, "--"}, tc.args...)...)
				require.Error(t, err, command)
				assert.Contains(t, stderr, tc.flag, command)
			}
		})
	}
}

// TestConfigureLogger checks CLI logging format and level selection.
func TestConfigureLogger(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		debug   bool
		wantErr string
	}{
		{name: "text", format: "text"},
		{name: "json with debug", format: "json", debug: true},
		{name: "invalid", format: "yaml", wantErr: "invalid log-format parameter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := appconfig.DefaultConfig()
			config.LogFormat = tt.format
			config.Debug = tt.debug

			err := configureLogger(config)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
		})
	}
}

// fakeWatcher stands in for a file watcher in startup tests.
type fakeWatcher struct {
	err     error
	started chan struct{}
	release chan struct{}
}

// Watch sends a change event, then waits so startup tests can stop the watcher.
func (f *fakeWatcher) Watch(ctx context.Context, onChange func()) error {
	close(f.started)
	onChange()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return f.err
	}
}

// TestRunWatcher checks watcher events start and stop the reload path.
func TestRunWatcher(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "context canceled", err: context.Canceled},
		{name: "watcher failure is contained", err: errors.New("watch failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fw := &fakeWatcher{err: tt.err, started: make(chan struct{}), release: make(chan struct{})}
			var wg sync.WaitGroup
			var called atomic.Bool

			runWatcher(ctx, fw, func() { called.Store(true) }, &wg)

			select {
			case <-fw.started:
			case <-time.After(time.Second):
				t.Fatal("watcher did not start")
			}
			require.Eventually(t, called.Load, time.Second, 10*time.Millisecond)
			if errors.Is(tt.err, context.Canceled) {
				cancel()
			} else {
				close(fw.release)
			}

			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("runWatcher did not stop")
			}
		})
	}
}

// TestNewOSWatcher checks creation of the file watcher used at startup.
func TestNewOSWatcher(t *testing.T) {
	ch, cleanup := newOSWatcher(os.Interrupt)
	require.NotNil(t, ch)
	cleanup()
	_, ok := <-ch
	assert.False(t, ok)
}

// restoreStartupSeams resets replaceable startup functions after each test.
func restoreStartupSeams(t *testing.T) {
	t.Helper()
	prevInitializeDCGMProvider := initializeDCGMProviderFunc
	prevBeginDCGMShutdown := beginDCGMShutdownFunc
	prevInitializeNVMLProvider := initializeNVMLProviderFunc
	prevCleanupNVMLProvider := cleanupNVMLProviderFunc
	prevBuildRegistry := buildRegistryFunc
	prevGetCounters := getCountersFunc
	prevStartWatchListManager := startWatchListManagerFunc
	prevGetHostname := getHostnameFunc
	prevInitCollectorFactory := initCollectorFactoryFunc
	prevNewMetricsServer := newMetricsServerFunc
	prevNewFileWatcher := newFileWatcherFunc
	prevNewGPUBindUnbindWatcher := newGPUBindUnbindWatcherFunc
	t.Cleanup(func() {
		initializeDCGMProviderFunc = prevInitializeDCGMProvider
		beginDCGMShutdownFunc = prevBeginDCGMShutdown
		initializeNVMLProviderFunc = prevInitializeNVMLProvider
		cleanupNVMLProviderFunc = prevCleanupNVMLProvider
		buildRegistryFunc = prevBuildRegistry
		getCountersFunc = prevGetCounters
		startWatchListManagerFunc = prevStartWatchListManager
		getHostnameFunc = prevGetHostname
		initCollectorFactoryFunc = prevInitCollectorFactory
		newMetricsServerFunc = prevNewMetricsServer
		newFileWatcherFunc = prevNewFileWatcher
		newGPUBindUnbindWatcherFunc = prevNewGPUBindUnbindWatcher
	})
}

// successfulDCGMInitialize is the no-op provider seam for tests that exercise
// later startup behavior without depending on a real DCGM installation.
func successfulDCGMInitialize(*appconfig.Config) error {
	return nil
}

// staticWatchListManager supplies a fixed topology to cmd tests that build a
// registry without running device discovery.
type staticWatchListManager map[dcgm.Field_Entity_Group]devicewatchlistmanager.WatchList

// CreateEntityWatchList skips watch setup in startup tests.
func (s staticWatchListManager) CreateEntityWatchList(
	dcgm.Field_Entity_Group, devicewatcher.WatchInstaller, int64,
) error {
	return nil
}

// EntityWatchList returns the test watch list used by the registry builder.
func (s staticWatchListManager) EntityWatchList(group dcgm.Field_Entity_Group) (devicewatchlistmanager.WatchList, bool) {
	watchList, ok := s[group]
	return watchList, ok
}

// topologyDeviceInfo provides fixed topology data to startup and registry tests.
type topologyDeviceInfo struct {
	gpuCount uint
	switches []deviceinfo.SwitchInfo
	cpus     []deviceinfo.CPUInfo
}

// GPUCount returns the stub GPU count to registry tests.
func (t topologyDeviceInfo) GPUCount() uint { return t.gpuCount }

// GPUs returns the stub GPU list to registry tests.
func (t topologyDeviceInfo) GPUs() []deviceinfo.GPUInfo { return nil }

// GPU returns a stub GPU to registry tests.
func (t topologyDeviceInfo) GPU(uint) deviceinfo.GPUInfo { return deviceinfo.GPUInfo{} }

// Switches returns the stub switch list to registry tests.
func (t topologyDeviceInfo) Switches() []deviceinfo.SwitchInfo { return t.switches }

// Switch returns a stub switch to registry tests.
func (t topologyDeviceInfo) Switch(uint) deviceinfo.SwitchInfo { return deviceinfo.SwitchInfo{} }

// CPUs returns the stub CPU list to registry tests.
func (t topologyDeviceInfo) CPUs() []deviceinfo.CPUInfo { return t.cpus }

// CPU returns a stub CPU to registry tests.
func (t topologyDeviceInfo) CPU(uint) deviceinfo.CPUInfo { return deviceinfo.CPUInfo{} }

// GOpts returns stub GPU selection settings to registry tests.
func (t topologyDeviceInfo) GOpts() appconfig.DeviceOptions { return appconfig.DeviceOptions{} }

// SOpts returns stub switch selection settings to registry tests.
func (t topologyDeviceInfo) SOpts() appconfig.DeviceOptions { return appconfig.DeviceOptions{} }

// COpts returns stub CPU selection settings to registry tests.
func (t topologyDeviceInfo) COpts() appconfig.DeviceOptions { return appconfig.DeviceOptions{} }

// InfoType returns the stub entity type to registry tests.
func (t topologyDeviceInfo) InfoType() dcgm.Field_Entity_Group { return dcgm.FE_NONE }

// IsCPUWatched reports stub CPU watch membership to registry tests.
func (t topologyDeviceInfo) IsCPUWatched(uint) bool { return false }

// IsCoreWatched reports stub core watch membership to registry tests.
func (t topologyDeviceInfo) IsCoreWatched(uint, uint) bool { return false }

// IsSwitchWatched reports stub switch watch membership to registry tests.
func (t topologyDeviceInfo) IsSwitchWatched(uint) bool { return false }

// IsLinkWatched reports stub link watch membership to registry tests.
func (t topologyDeviceInfo) IsLinkWatched(uint, uint) bool { return false }

// topologyManager serves fixed topology data through the manager interface in startup tests.
func topologyManager() devicewatchlistmanager.Manager {
	return staticWatchListManager{
		dcgm.FE_GPU: *devicewatchlistmanager.NewWatchList(
			topologyDeviceInfo{gpuCount: 2}, nil, nil, nil, 0,
		),
		dcgm.FE_SWITCH: *devicewatchlistmanager.NewWatchList(
			topologyDeviceInfo{switches: []deviceinfo.SwitchInfo{{EntityId: 7}, {EntityId: 8}}}, nil, nil, nil, 0,
		),
		dcgm.FE_CPU: *devicewatchlistmanager.NewWatchList(
			topologyDeviceInfo{cpus: []deviceinfo.CPUInfo{{EntityId: 0}, {EntityId: 1}, {EntityId: 2}}}, nil, nil, nil, 0,
		),
	}
}

// TestActionRecoversStartupPanic checks the CLI action turns a startup panic into an error.
func TestActionRecoversStartupPanic(t *testing.T) {
	restoreStartupSeams(t)
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	initializeDCGMProviderFunc = func(*appconfig.Config) error {
		panic("synthetic startup panic")
	}

	err := action(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "synthetic startup panic")
}

// TestNewAppRunReturnsErrorWhenRemoteHostengineStartupPanics checks a remote hostengine panic reaches the CLI error path.
func TestNewAppRunReturnsErrorWhenRemoteHostengineStartupPanics(t *testing.T) {
	restoreStartupSeams(t)
	initializeDCGMProviderFunc = func(config *appconfig.Config) error {
		assert.True(t, config.UseRemoteHE)
		assert.Equal(t, "127.0.0.1:1", config.RemoteHEInfo)
		panic("remote hostengine connection failed")
	}

	err := NewApp("test-version").Run([]string{
		"dcgm-exporter",
		"--disable-startup-validate",
		"--remote-hostengine-info",
		"127.0.0.1:1",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote hostengine connection failed")
}

// TestRunDCGMExporter_HotReloadAndShutdown checks reload and shutdown release each startup resource.
func TestRunDCGMExporter_HotReloadAndShutdown(t *testing.T) {
	restoreStartupSeams(t)
	mock := withMockDCGMClient(t)
	groups := sampleMetricGroups()
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groups, nil)
	mock.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no GPUs"))
	mock.EXPECT().Cleanup()

	countersFile := filepath.Join(t.TempDir(), "counters.csv")
	require.NoError(t, os.WriteFile(countersFile, []byte("DCGM_FI_DEV_GPU_TEMP,gauge,temp\n"), 0o600))

	var buildCalls atomic.Int32
	var dcpBuildCalls atomic.Int32
	var shutdownCalls atomic.Int32
	beginDCGMShutdownFunc = func() { shutdownCalls.Add(1) }
	buildRegistryFunc = func(
		_ context.Context,
		cfg *appconfig.Config,
		dcp counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		assert.Equal(t, countersFile, cfg.MetricSource.File)
		assert.Zero(t, shutdownCalls.Load(), "building a replacement must keep watch restoration enabled")
		if dcp.Supports(groups[0].FieldIds[0]) {
			dcpBuildCalls.Add(1)
		}
		buildCalls.Add(1)
		return registry.NewRegistry(), topologyManager(), nil
	}
	initializeDCGMProviderFunc = successfulDCGMInitialize
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}
	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIFieldsFile, countersFile))
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	require.NoError(t, ctx.Set(CLIAddress, "127.0.0.1:0"))
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	reloadRequests := make(chan struct{}, 1)

	done := make(chan error, 1)
	go func() {
		done <- runDCGMExporter(lifecycleCtx, ctx, reloadRequests)
	}()

	require.Eventually(t, func() bool { return buildCalls.Load() >= 1 }, 2*time.Second, 10*time.Millisecond)
	reloadRequests <- struct{}{}
	require.Eventually(t, func() bool { return buildCalls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("exporter did not shut down after SIGTERM")
	}
	assert.Equal(t, int32(2), dcpBuildCalls.Load(), "startup capabilities must be reused for the CSV reload")
	assert.Equal(t, int32(1), shutdownCalls.Load(), "only final process cleanup must mark shutdown")
}

func TestCleanupRegistryAfterShutdown(t *testing.T) {
	originalErr := errors.New("existing lifecycle failure")
	cleanupErr := errors.New("registry retirement failed")
	for _, tt := range []struct {
		name       string
		blocked    bool
		cleanupErr error
		wantCalls  []string
	}{
		{name: "success", wantCalls: []string{"shutdown", "cleanup"}},
		{name: "cleanup failure", cleanupErr: cleanupErr, wantCalls: []string{"shutdown", "cleanup"}},
		{name: "already blocked", blocked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			coord := &reloadCoordinator{}
			coord.nativeCleanupBlocked.Store(tt.blocked)
			var calls []string
			beginDCGMShutdownFunc = func() { calls = append(calls, "shutdown") }
			cleanup := func() error {
				calls = append(calls, "cleanup")
				return tt.cleanupErr
			}
			returnErr := originalErr

			cleanupRegistryAfterShutdown(coord, cleanup, &returnErr)

			assert.Equal(t, tt.wantCalls, calls)
			assert.ErrorIs(t, returnErr, originalErr)
			if tt.cleanupErr != nil {
				assert.ErrorIs(t, returnErr, tt.cleanupErr)
			}
			assert.Equal(t, tt.blocked || tt.cleanupErr != nil, coord.nativeCleanupBlocked.Load())
		})
	}
}

// TestRunDCGMExporter_ShutdownCleansCurrentNVMLClient checks shutdown closes the active NVML client after a reload.
func TestRunDCGMExporter_ShutdownCleansCurrentNVMLClient(t *testing.T) {
	tests := []struct {
		name       string
		kubernetes bool
	}{
		{name: "non-Kubernetes"},
		{name: "Kubernetes", kubernetes: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			ctrl := gomock.NewController(t)

			originalNVML := nvmlprovider.Client()
			t.Cleanup(func() { nvmlprovider.SetClient(originalNVML) })
			initialNVML := mocknvmlprovider.NewMockNVML(ctrl)
			currentNVML := mocknvmlprovider.NewMockNVML(ctrl)
			initialNVML.EXPECT().Cleanup().Times(0)
			currentNVML.EXPECT().Cleanup().Times(1)
			nvmlprovider.SetClient(initialNVML)

			mock := withMockDCGMClient(t)
			mock.EXPECT().
				GetSupportedMetricGroups(uint(0)).
				Return(nil, errors.New("profiling unsupported"))
			mock.EXPECT().Cleanup()

			var buildCalls atomic.Int32
			buildRegistryFunc = func(
				context.Context,
				*appconfig.Config,
				counters.DCPCapabilities,
			) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				buildCalls.Add(1)
				return registry.NewRegistry(), topologyManager(), nil
			}
			initializeDCGMProviderFunc = successfulDCGMInitialize
			initializeNVMLProviderFunc = func() error { return nil }

			ctx := newTestCLIContext(t)
			require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
			require.NoError(t, ctx.Set(CLIKubernetes, fmt.Sprintf("%t", tt.kubernetes)))
			require.NoError(t, ctx.Set(CLIAddress, "127.0.0.1:0"))

			lifecycleCtx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- runDCGMExporter(lifecycleCtx, ctx, nil)
			}()

			require.Eventually(t, func() bool {
				return buildCalls.Load() == 1
			}, 2*time.Second, 10*time.Millisecond)
			nvmlprovider.SetClient(currentNVML)
			cancel()

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("exporter did not shut down")
			}
		})
	}
}

// TestRunDCGMExporter_ShutdownCleansCurrentDCGMClient checks shutdown closes the active DCGM client after a reload.
func TestRunDCGMExporter_ShutdownCleansCurrentDCGMClient(t *testing.T) {
	restoreStartupSeams(t)
	ctrl := gomock.NewController(t)

	originalDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(originalDCGM) })
	initialDCGM := mockdcgmprovider.NewMockDCGM(ctrl)
	currentDCGM := mockdcgmprovider.NewMockDCGM(ctrl)
	initialDCGM.EXPECT().GetGPUStatus(uint(0)).Return(dcgm.EntityStatusUnknown)
	initialDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))
	initialDCGM.EXPECT().Cleanup().Times(0)
	currentDCGM.EXPECT().Cleanup().Times(1)
	dcgmprovider.SetClient(initialDCGM)

	var buildCalls atomic.Int32
	buildRegistryFunc = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		buildCalls.Add(1)
		return registry.NewRegistry(), topologyManager(), nil
	}
	initializeDCGMProviderFunc = successfulDCGMInitialize
	initializeNVMLProviderFunc = func() error { return nil }
	cleanupNVMLProviderFunc = func() {}

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	require.NoError(t, ctx.Set(CLIAddress, "127.0.0.1:0"))

	lifecycleCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runDCGMExporter(lifecycleCtx, ctx, nil)
	}()

	require.Eventually(t, func() bool {
		return buildCalls.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
	dcgmprovider.SetClient(currentDCGM)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("exporter did not shut down")
	}
}

// TestDoConfigReloadSuccessAndFailure checks reload publishes a valid config and keeps the old one on failure.
func TestDoConfigReloadSuccessAndFailure(t *testing.T) {
	t.Run("success installs rebuilt registry", func(t *testing.T) {
		coord := newTestCoordinator(t)
		initialRegistry := registry.NewRegistry()
		coord.server.SetRegistry(initialRegistry)
		newRegistry := registry.NewRegistry()
		coord.buildRegistry = func(
			context.Context,
			*appconfig.Config,
			counters.DCPCapabilities,
		) (*registry.Registry, devicewatchlistmanager.Manager, error) {
			return newRegistry, topologyManager(), nil
		}

		coord.doConfigReload(context.Background(), &appconfig.Config{}, 1)

		assert.Same(t, newRegistry, coord.server.GetRegistry())
	})

	t.Run("failure keeps serving last-good registry", func(t *testing.T) {
		coord := newTestCoordinator(t)
		initialRegistry := registry.NewRegistry()
		coord.server.SetRegistry(initialRegistry)
		coord.buildRegistry = func(
			context.Context,
			*appconfig.Config,
			counters.DCPCapabilities,
		) (*registry.Registry, devicewatchlistmanager.Manager, error) {
			return nil, nil, errors.New("build failed")
		}

		coord.doConfigReload(context.Background(), &appconfig.Config{}, 1)

		assert.Same(t, initialRegistry, coord.server.GetRegistry())
	})
}

// TestGetCountersCopiesLabelCounters checks counter loading copies labels before callers can mutate them.
func TestGetCountersCopiesLabelCounters(t *testing.T) {
	countersFile := filepath.Join(t.TempDir(), "counters.csv")
	require.NoError(t, os.WriteFile(countersFile, []byte(
		"DCGM_FI_DEV_GPU_TEMP,gauge,temp\nDCGM_FI_DRIVER_VERSION,label,driver\n",
	), 0o600))

	got, err := getCounters(context.Background(), &appconfig.Config{
		MetricSource: appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: countersFile},
	}, counters.DCPCapabilities{})

	require.NoError(t, err)
	require.Len(t, got.DCGMCounters, 2)
	require.Len(t, got.ExporterCounters, 1)
	assert.Equal(t, "DCGM_FI_DRIVER_VERSION", got.ExporterCounters[0].FieldName)
}

// sampleMetricGroups provides fixed metric groups for startup and reload tests.
func sampleMetricGroups() []dcgm.MetricGroup {
	return []dcgm.MetricGroup{
		{Major: 0, Minor: 0, FieldIds: []uint{1001, 1002, 1003}},
		{Major: 1, Minor: 0, FieldIds: []uint{1010, 1011}},
	}
}

// newTestCoordinator builds a coordinator backed by a zero-value
// *server.MetricsServer (sufficient for SetReloadInProgress/IsReloadInProgress)
// and a minimal cli.Context. Tests replace only the dependency seam relevant to the behavior under test.
func newTestCoordinator(t *testing.T) *reloadCoordinator {
	t.Helper()
	coord := newReloadCoordinator()
	coord.cleanupDCGM = func() {}
	coord.initializeDCGM = successfulDCGMInitialize
	coord.cleanupNVML = func() {}
	coord.initializeNVML = func() error { return nil }
	coord.scheduleRetry = func(context.Context, time.Duration, func()) context.CancelFunc {
		return func() {}
	}

	cfg := appconfig.DefaultConfig()
	coord.reloadConfig = cfg.Clone()
	coord.setServer(&server.MetricsServer{})
	return coord
}

// newTestCLIContext returns a cli.Context with the minimum flags needed for
// contextToConfig to succeed — it sets the device-option strings and the DCGM
// log level. Other flags default to their zero value.
func newTestCLIContext(t *testing.T) *cli.Context {
	t.Helper()
	app := cli.NewApp()
	app.Flags = []cli.Flag{
		&cli.StringFlag{Name: CLIConfigFile},
		&cli.StringFlag{Name: CLIFieldsFile},
		&cli.StringFlag{Name: CLIAddress},
		&cli.IntFlag{Name: CLICollectInterval},
		&cli.DurationFlag{Name: CLIWatchMaxKeepAge},
		&cli.Int64Flag{Name: CLIWatchMaxKeepSamples},
		&cli.StringFlag{Name: CLIConfigMapData},
		&cli.StringFlag{Name: CLIGPUDevices},
		&cli.StringFlag{Name: CLISwitchDevices},
		&cli.StringFlag{Name: CLICPUDevices},
		&cli.StringFlag{Name: CLIDCGMLogLevel},
		&cli.StringFlag{Name: CLILogFormat},
		&cli.StringFlag{Name: CLIRemoteHEInfo},
		&cli.BoolFlag{Name: CLIDisableStartupValidate},
		&cli.BoolFlag{Name: CLIKubernetes},
		&cli.BoolFlag{Name: CLIKubernetesVirtualGPUs},
		&cli.BoolFlag{Name: CLIUseFakeGPUs},
		&cli.BoolFlag{Name: CLIContainerLabels},
		&cli.StringFlag{Name: CLIContainerRuntimeSocket},
		&cli.BoolFlag{Name: CLIKubernetesEnableDRA},
		&cli.StringFlag{Name: CLIHPCJobMappingDir},
		&cli.StringFlag{Name: CLIPodResourcesKubeletSocket},
		&cli.BoolFlag{Name: CLIDumpEnabled},
		&cli.StringFlag{Name: CLIDumpDirectory},
		&cli.IntFlag{Name: CLIDumpRetention},
		&cli.BoolFlag{Name: CLIDumpCompression},
		&cli.StringFlag{Name: CLIDumpMinInterval},
		&cli.Int64Flag{Name: CLIDumpMaxFileBytes},
		&cli.Int64Flag{Name: CLIDumpMaxTotalBytes},
		&cli.Int64Flag{Name: CLIDumpMaxFiles},
		&cli.StringFlag{Name: CLIWebReadTimeout},
		&cli.StringFlag{Name: CLIWebWriteTimeout},
		&cli.BoolFlag{Name: CLIEnableExporterMetrics},
		&cli.StringFlag{Name: CLIWebConfigFile},
		&cli.BoolFlag{Name: CLIEnableGPUBindUnbindWatch},
		&cli.StringFlag{Name: CLIGPUBindUnbindPollInterval},
		&cli.DurationFlag{Name: CLIDCGMCleanupTimeout},
		&cli.BoolFlag{Name: CLIEnablePprof},
	}

	set := flag.NewFlagSet("test", 0)
	set.String(CLIFieldsFile, filepath.Join(t.TempDir(), "counters.csv"), "")
	set.String(CLIConfigFile, "", "")
	set.String(CLIAddress, "127.0.0.1:0", "")
	set.Int(CLICollectInterval, 1, "")
	set.Duration(CLIWatchMaxKeepAge, appconfig.DefaultWatchMaxKeepAge, "")
	set.Int64(CLIWatchMaxKeepSamples, appconfig.DefaultWatchMaxSamples, "")
	set.String(CLIConfigMapData, undefinedConfigMapData, "")
	set.String(CLIGPUDevices, "f", "")
	set.String(CLISwitchDevices, "f", "")
	set.String(CLICPUDevices, "f", "")
	set.String(CLIDCGMLogLevel, "NONE", "")
	set.String(CLILogFormat, "text", "")
	set.String(CLIRemoteHEInfo, "localhost:5555", "")
	set.Bool(CLIDisableStartupValidate, false, "")
	set.Bool(CLIKubernetes, false, "")
	set.Bool(CLIKubernetesVirtualGPUs, false, "")
	set.Bool(CLIUseFakeGPUs, false, "")
	set.Bool(CLIContainerLabels, false, "")
	set.String(CLIContainerRuntimeSocket, "", "")
	set.Bool(CLIKubernetesEnableDRA, false, "")
	set.String(CLIHPCJobMappingDir, "", "")
	// Mirror the production flag default: a flagset default does not count as
	// explicitly set (c.IsSet stays false), so this behaves like an unset flag.
	set.String(CLIPodResourcesKubeletSocket, appconfig.DefaultConfig().PodResourcesKubeletSocket, "")
	set.Bool(CLIDumpEnabled, false, "")
	set.String(CLIDumpDirectory, "/tmp/dcgm-exporter-debug", "")
	set.Int(CLIDumpRetention, 24, "")
	set.Bool(CLIDumpCompression, true, "")
	set.String(CLIDumpMinInterval, "1m0s", "")
	set.Int64(CLIDumpMaxFileBytes, 16<<20, "")
	set.Int64(CLIDumpMaxTotalBytes, 256<<20, "")
	set.Int64(CLIDumpMaxFiles, 128, "")
	set.String(CLIWebReadTimeout, appconfig.DefaultWebReadTimeout.String(), "")
	set.String(CLIWebWriteTimeout, appconfig.DefaultWebWriteTimeout.String(), "")
	set.Int(CLIMaxConcurrentScrapes, appconfig.DefaultMaxConcurrentScrapes, "")
	set.Bool(CLIEnableExporterMetrics, false, "")
	set.String(CLIWebConfigFile, "", "")
	set.Bool(CLIEnableGPUBindUnbindWatch, false, "")
	set.String(CLIGPUBindUnbindPollInterval, time.Second.String(), "")
	set.Duration(CLIDCGMCleanupTimeout, appconfig.DefaultDCGMCleanupTimeout, "")
	set.Bool(CLIEnablePprof, false, "")
	return cli.NewContext(app, set, nil)
}

// setGPUBindUnbindTestConfig sets the GPU watch options used by lifecycle tests.
func setGPUBindUnbindTestConfig(t *testing.T, ctx *cli.Context) {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(`
version: 2
sources:
  dcgm:
    detect_bind_unbind:
      enabled: true
`), 0o600))
	require.NoError(t, ctx.Set(CLIConfigFile, configFile))
}

// unsetFlagEnvVars clears flag environment variables between CLI test cases.
func unsetFlagEnvVars(t *testing.T, flags []cli.Flag) {
	t.Helper()

	seen := make(map[string]struct{})
	for _, flag := range flags {
		docFlag, ok := flag.(cli.DocGenerationFlag)
		if !ok {
			continue
		}
		for _, envVar := range docFlag.GetEnvVars() {
			if _, ok := seen[envVar]; ok {
				continue
			}
			seen[envVar] = struct{}{}

			name := envVar
			value, wasSet := os.LookupEnv(name)
			require.NoError(t, os.Unsetenv(name))
			t.Cleanup(func() {
				if wasSet {
					require.NoError(t, os.Setenv(name, value))
					return
				}
				require.NoError(t, os.Unsetenv(name))
			})
		}
	}
}

// TestNewGenerationGPUBindUnbindWatcherUsesCurrentClientAtStart verifies a
// lifecycle reset restarts the application-owned watcher with the new client.
func TestNewGenerationGPUBindUnbindWatcherUsesCurrentClientAtStart(t *testing.T) {
	ctrl := gomock.NewController(t)
	staleClient := mockdcgmprovider.NewMockDCGM(ctrl)
	currentClient := mockdcgmprovider.NewMockDCGM(ctrl)
	previousClient := dcgmprovider.Client()
	dcgmprovider.SetClient(staleClient)
	t.Cleanup(func() { dcgmprovider.SetClient(previousClient) })

	w := newGenerationGPUBindUnbindWatcher()
	dcgmprovider.SetClient(currentClient)
	currentClient.EXPECT().
		WatchFieldValue(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, time.Second, time.Duration(0), gomock.Any()).
		Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	run, err := w.Start(ctx, func(dcgm.BindUnbindEventState) {})
	require.NoError(t, err)
	cancel()
	require.ErrorIs(t, run(), context.Canceled)
}

// TestRunDCGMExporterReturnsAfterUndrainedScrape verifies that a blocked scrape
// cannot prevent exporter shutdown from returning after its drain deadline.
func TestRunDCGMExporterReturnsAfterUndrainedScrape(t *testing.T) {
	restoreStartupSeams(t)

	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported")).AnyTimes()
	mock.EXPECT().Cleanup().Times(0)

	producerStarted := make(chan struct{})
	releaseProducer := make(chan struct{})
	defer func() {
		select {
		case <-releaseProducer:
		default:
			close(releaseProducer)
		}
	}()
	var cleanupCalls atomic.Int32
	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(blockingShutdownCollector{
		started:      producerStarted,
		release:      releaseProducer,
		cleanupCalls: &cleanupCalls,
	})
	require.NoError(t, reg.Register(tuple))

	serverReady := make(chan *server.MetricsServer, 1)
	initializeDCGMProviderFunc = successfulDCGMInitialize
	initializeNVMLProviderFunc = func() error { return nil }
	var nvmlCleanupCalls atomic.Int32
	cleanupNVMLProviderFunc = func() { nvmlCleanupCalls.Add(1) }
	buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return reg, topologyManager(), nil
	}
	newMetricsServerFunc = func(config *appconfig.Config, manager devicewatchlistmanager.Manager, builtRegistry *registry.Registry, _ func()) (*server.MetricsServer, func(), error) {
		metricsServer, cleanup, err := server.NewMetricsServer(config, manager, builtRegistry)
		if err == nil {
			serverReady <- metricsServer
		}
		return metricsServer, cleanup, err
	}

	ctx := newTestCLIContext(t)
	require.NoError(t, ctx.Set(CLIDisableStartupValidate, "true"))
	require.NoError(t, ctx.Set(CLIAddress, "127.0.0.1:0"))
	require.NoError(t, ctx.Set(CLIWebWriteTimeout, "50ms"))
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan error, 1)
	go func() { result <- runDCGMExporter(lifecycleCtx, ctx, nil) }()

	var metricsServer *server.MetricsServer
	select {
	case metricsServer = <-serverReady:
	case <-time.After(time.Second):
		t.Fatal("metrics server did not become ready")
	}

	scrapeDone := make(chan struct{})
	go func() {
		defer close(scrapeDone)
		metricsServer.Metrics(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}()

	select {
	case <-producerStarted:
	case <-time.After(time.Second):
		t.Fatal("collector scrape did not start")
	}

	cancel()

	select {
	case err := <-result:
		require.ErrorIs(t, err, server.ErrActiveScrapeDrainTimeout)
	case <-time.After(time.Second):
		t.Fatal("exporter did not return after the active-scrape drain deadline")
	}

	require.Zero(t, cleanupCalls.Load(), "registry cleanup waits for the active scrape")
	require.Zero(t, nvmlCleanupCalls.Load(), "NVML cleanup waits for the active scrape")

	close(releaseProducer)

	select {
	case <-scrapeDone:
	case <-time.After(time.Second):
		t.Fatal("scrape did not complete after release")
	}

	reg.Cleanup()
	require.EqualValues(t, 1, cleanupCalls.Load())
}

// TestStartupRegistrationRollbackGatesProviders exercises the application's
// actual deferred provider cleanup with multiple pending completion signals.
func TestStartupRegistrationRollbackGatesProviders(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("complete_%t", complete), func(t *testing.T) {
			restoreStartupSeams(t)
			mock := withMockDCGMClient(t)
			mock.EXPECT().GetSupportedMetricGroups(uint(0)).
				Return(nil, errors.New("profiling unsupported")).AnyTimes()
			var dcgmCleanups atomic.Int32
			mock.EXPECT().Cleanup().Do(func() { dcgmCleanups.Add(1) }).AnyTimes()
			initializeDCGMProviderFunc = successfulDCGMInitialize
			initializeNVMLProviderFunc = func() error { return nil }
			var nvmlCleanups atomic.Int32
			cleanupNVMLProviderFunc = func() { nvmlCleanups.Add(1) }

			first := &completionTrackingCollector{done: make(chan struct{})}
			last := &completionTrackingCollector{done: make(chan struct{})}
			close(first.done)
			if complete {
				close(last.done)
			} else {
				t.Cleanup(func() { close(last.done) })
			}
			tuples := make([]collector.EntityCollectorTuple, 2)
			tuples[0].SetEntity(dcgm.FE_GPU)
			tuples[0].SetCollector(first)
			tuples[1].SetEntity(dcgm.FE_NONE)
			tuples[1].SetCollector(last)
			factory := &countingFactory{collectors: tuples}
			buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
				*registry.Registry, devicewatchlistmanager.Manager, error,
			) {
				reg := registry.NewRegistry()
				reg.Cleanup()
				_, err := populateRegistry(factory, reg)
				reg.Cleanup()
				return nil, nil, fmt.Errorf("populate collector registry: %w", err)
			}
			ctx := newTestCLIContext(t)
			configFile := writeConfigFile(t, "version: 2\nsources: {dcgm: {cleanup_timeout: 10ms}}\n")
			require.NoError(t, ctx.Set(CLIConfigFile, configFile))
			err := runDCGMExporter(context.Background(), ctx, nil)
			require.ErrorIs(t, err, registry.ErrRegistryShuttingDown)
			require.Equal(t, int32(1), factory.calls.Load())
			require.Equal(t, int32(1), first.cleanupCalls.Load())
			require.Equal(t, int32(1), last.cleanupCalls.Load())
			if complete {
				require.Equal(t, int32(1), dcgmCleanups.Load())
				require.Equal(t, int32(1), nvmlCleanups.Load())
			} else {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Contains(t, err.Error(), "within 10ms (set by YAML: sources.dcgm.cleanup_timeout).",
					"the startup wiring names the input that set the timeout")
				require.Zero(t, dcgmCleanups.Load())
				require.Zero(t, nvmlCleanups.Load())
			}
		})
	}
}

// releasedFlag is one line of testdata/released-flags.txt: a flag, its aliases
// and its environment variables, each as users write them.
type releasedFlag struct {
	name       string
	aliases    []string
	envVars    []string
	defaultVal string
	hasDefault bool
}

// defaultPrefix starts the word that gives a released flag's default.
const defaultPrefix = "default="

// readReleasedFlags reads testdata/released-flags.txt. A line starts with the
// flag name; a later word that starts with a dash is an alias, a word that starts
// with default= is the default, and any other word is an environment variable.
func readReleasedFlags(t *testing.T) []releasedFlag {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "released-flags.txt"))
	require.NoError(t, err)

	var flags []releasedFlag
	for _, line := range strings.Split(string(data), "\n") {
		words := strings.Fields(line)
		if len(words) == 0 || strings.HasPrefix(words[0], "#") {
			continue
		}

		flag := releasedFlag{name: words[0]}
		for _, word := range words[1:] {
			switch {
			case strings.HasPrefix(word, defaultPrefix):
				flag.defaultVal = strings.TrimPrefix(word, defaultPrefix)
				flag.hasDefault = true
			case strings.HasPrefix(word, "-"):
				flag.aliases = append(flag.aliases, word)
			default:
				flag.envVars = append(flag.envVars, word)
			}
		}

		flags = append(flags, flag)
	}

	require.NotEmpty(t, flags)

	return flags
}

// definedFlag is the spellings, environment variables and default of one flag of the app.
type definedFlag struct {
	spellings  map[string]bool
	envVars    map[string]bool
	defaultVal string
	hasDefault bool
}

// flagDefault returns a flag's default as text: lists are joined with commas and
// durations are written as Go prints them. It reports false only for a flag
// type this switch does not handle; help and version are bool flags and give "false".
func flagDefault(flag cli.Flag) (string, bool) {
	// The app wraps its flags to record how they were set; read the flag inside.
	if wrapped, ok := flag.(*inspectionFlag); ok {
		flag = wrapped.DocGenerationFlag
	}

	switch typed := flag.(type) {
	case *cli.BoolFlag:
		return strconv.FormatBool(typed.Value), true
	case *cli.IntFlag:
		return strconv.Itoa(typed.Value), true
	case *cli.Int64Flag:
		return strconv.FormatInt(typed.Value, 10), true
	case *cli.StringFlag:
		return typed.Value, true
	case *cli.DurationFlag:
		return typed.Value.String(), true
	case *cli.StringSliceFlag:
		if typed.Value == nil {
			return "", true
		}

		return strings.Join(typed.Value.Value(), ","), true
	}

	return "", false
}

// definedFlags indexes the app's flags by each spelling, written as users type
// it: --name or -a. The help and version flags are included.
func definedFlags(app *cli.App) map[string]definedFlag {
	// Setup adds the help and version flags that the CLI framework provides.
	app.Setup()

	byName := map[string]definedFlag{}
	for _, flag := range app.Flags {
		defined := definedFlag{spellings: map[string]bool{}, envVars: map[string]bool{}}
		defined.defaultVal, defined.hasDefault = flagDefault(flag)
		for _, name := range flag.Names() {
			dashes := "--"
			if len(name) == 1 {
				dashes = "-"
			}

			defined.spellings[dashes+name] = true
		}

		if documented, ok := flag.(cli.DocGenerationFlag); ok {
			for _, env := range documented.GetEnvVars() {
				defined.envVars[env] = true
			}
		}

		for spelling := range defined.spellings {
			byName[spelling] = defined
		}
	}

	return byName
}

// TestNewAppKeepsTheReleasedFlags checks that every flag name, alias,
// environment variable and default listed in testdata/released-flags.txt is
// still defined the same way, and reports all that differ, not only the first.
func TestNewAppKeepsTheReleasedFlags(t *testing.T) {
	defined := definedFlags(NewApp("test"))

	var missing []string
	for _, released := range readReleasedFlags(t) {
		flag, found := defined[released.name]
		if !found {
			missing = append(missing, "flag "+released.name)
		}

		missing = append(missing, releasedFlagDifferences(released, flag, found)...)
	}

	assert.Empty(t, missing, "released flags that NewApp no longer defines the same way")
}

// releasedFlagDifferences lists the aliases, environment variables and default of
// a released flag that the app's flag no longer has.
func releasedFlagDifferences(released releasedFlag, flag definedFlag, found bool) []string {
	var differences []string

	// A missing flag has no spellings or variables, so all its aliases and variables are reported too.
	for _, alias := range released.aliases {
		if !flag.spellings[alias] {
			differences = append(differences, fmt.Sprintf("alias %s of %s", alias, released.name))
		}
	}

	for _, env := range released.envVars {
		if !flag.envVars[env] {
			differences = append(differences, fmt.Sprintf("environment variable %s of %s", env, released.name))
		}
	}

	if released.hasDefault && found && flag.defaultVal != released.defaultVal {
		differences = append(differences, fmt.Sprintf("default of %s is %q, released as %q",
			released.name, flag.defaultVal, released.defaultVal))
	}

	return differences
}

// TestStartGPULifecycleWatcherUsesTheResolvedSettings checks the bind/unbind
// flags reach DCGM: off builds no watcher, and on with a 2s poll interval
// installs the DCGM watch with that interval.
func TestStartGPULifecycleWatcherUsesTheResolvedSettings(t *testing.T) {
	previous := newGPUBindUnbindWatcherFunc
	t.Cleanup(func() { newGPUBindUnbindWatcherFunc = previous })

	ctrl := gomock.NewController(t)
	client := mockdcgmprovider.NewMockDCGM(ctrl)
	built := 0
	newGPUBindUnbindWatcherFunc = func(opts ...watcher.GPUBindUnbindWatcherOption) *watcher.GPUBindUnbindWatcher {
		built++
		return watcher.NewGPUBindUnbindWatcherWithClient(client, opts...)
	}

	off, err := resolveFile(t, "version: 2\n")
	require.NoError(t, err)
	coord := newReloadCoordinator()
	require.NoError(t, startGPULifecycleWatcher(context.Background(), off.Config, coord))
	assert.Nil(t, coord.gpuWatcher)
	assert.Zero(t, built, "a disabled watch must not build a watcher")

	// The 2s interval must reach the DCGM watch, which is where it takes effect.
	client.EXPECT().WatchFieldValue(uint(0), dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT, 2*time.Second, time.Duration(0), 2).Return(nil)
	// The watch may poll once if its ticker fires before Stop.
	client.EXPECT().GetMultipleValuesForField(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil).AnyTimes()
	on, err := resolveFile(t, "version: 2\n",
		"--"+CLIEnableGPUBindUnbindWatch, "--"+CLIGPUBindUnbindPollInterval, "2s")
	require.NoError(t, err)
	coord = newReloadCoordinator()
	require.NoError(t, startGPULifecycleWatcher(context.Background(), on.Config, coord))
	require.NotNil(t, coord.gpuWatcher)
	require.NoError(t, coord.stopGPUWatcher())
	assert.Equal(t, 1, built)
}

// TestStartGPULifecycleWatcherSetupTimeoutKeepsDCGM checks that a DCGM watch
// setup that outlasts --dcgm-cleanup-timeout fails startup and blocks DCGM
// cleanup, because the native call may still be running.
func TestStartGPULifecycleWatcherSetupTimeoutKeepsDCGM(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		previous := newGPUBindUnbindWatcherFunc
		t.Cleanup(func() { newGPUBindUnbindWatcherFunc = previous })

		release := make(chan struct{})
		client := mockdcgmprovider.NewMockDCGM(gomock.NewController(t))
		client.EXPECT().WatchFieldValue(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(uint, dcgm.Short, time.Duration, time.Duration, int) error {
				<-release
				return nil
			})
		newGPUBindUnbindWatcherFunc = func(opts ...watcher.GPUBindUnbindWatcherOption) *watcher.GPUBindUnbindWatcher {
			return watcher.NewGPUBindUnbindWatcherWithClient(client, opts...)
		}

		resolved, err := resolveFile(t, "version: 2\n",
			"--"+CLIEnableGPUBindUnbindWatch, "--"+CLIDCGMCleanupTimeout, "5s")
		require.NoError(t, err)
		coord := newReloadCoordinator()

		began := time.Now()
		err = startGPULifecycleWatcher(t.Context(), resolved.Config, coord)

		require.ErrorIs(t, err, watcher.ErrGPUWatchTimeout)
		assert.Equal(t, 5*time.Second, time.Since(began))
		assert.True(t, coord.nativeCleanupBlocked.Load())

		close(release)
		synctest.Wait()
	})
}
