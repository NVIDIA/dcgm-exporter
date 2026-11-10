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
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

// TestDCGMCleanupTimeoutPrecedence checks the default and that a version 2
// file, the environment variable and the flag each replace the layer below,
// and that a version 1 file, which has no key, still takes the flag and the
// environment variable. The origin is the text timeout messages show.
func TestDCGMCleanupTimeoutPrecedence(t *testing.T) {
	const withKey = "version: 2\nsources: {dcgm: {cleanup_timeout: 2m}}\n"
	tests := []struct {
		name, yaml, env string
		args            []string
		want            time.Duration
		wantOrigin      string
	}{
		{name: "default", yaml: "version: 2\n", want: 5 * time.Minute, wantOrigin: "default"},
		{name: "file", yaml: withKey, want: 2 * time.Minute, wantOrigin: "YAML: sources.dcgm.cleanup_timeout"},
		{name: "env over file", yaml: withKey, env: "3m", want: 3 * time.Minute, wantOrigin: "environment: " + envDCGMCleanupTimeout},
		{
			name: "flag over env", yaml: withKey, env: "3m", args: []string{"--dcgm-cleanup-timeout", "4m"},
			want: 4 * time.Minute, wantOrigin: "argv: --dcgm-cleanup-timeout",
		},
		{name: "version 1 file with env", yaml: "version: 1\n", env: "90s", want: 90 * time.Second, wantOrigin: "environment: " + envDCGMCleanupTimeout},
		{
			name: "version 1 file with flag", yaml: "version: 1\n", args: []string{"--dcgm-cleanup-timeout", "1500ms"},
			want: 1500 * time.Millisecond, wantOrigin: "argv: --dcgm-cleanup-timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			if tt.env != "" {
				t.Setenv(envDCGMCleanupTimeout, tt.env)
			}

			resolved, err := resolveFileAt(t, writeConfigFile(t, tt.yaml), tt.args...)

			require.NoError(t, err)
			assert.Equal(t, tt.want, resolved.Config.DCGMCleanupTimeout)
			assert.Equal(t, tt.wantOrigin, resolved.Origin(appconfig.FieldDCGMCleanupTimeout))
		})
	}
}

// TestDCGMCleanupTimeoutRejectsInvalidValues checks that a value from the
// environment variable or the flag that is not positive or not a duration
// fails startup, and that the error names the source. appconfig tests cover
// the file.
func TestDCGMCleanupTimeoutRejectsInvalidValues(t *testing.T) {
	const notPositive = `invalid dcgm-cleanup-timeout parameter value: %q, must be greater than 0; set by %s`
	tests := []struct {
		name, env string
		args      []string
		want      []string
	}{
		{name: "env zero", env: "0s", want: []string{fmt.Sprintf(notPositive, "0s", "environment: "+envDCGMCleanupTimeout)}},
		{name: "env negative", env: "-1s", want: []string{fmt.Sprintf(notPositive, "-1s", "environment: "+envDCGMCleanupTimeout)}},
		{name: "env garbage", env: "soon", want: []string{envDCGMCleanupTimeout, "invalid duration"}},
		{name: "flag zero", args: []string{"--dcgm-cleanup-timeout", "0s"}, want: []string{fmt.Sprintf(notPositive, "0s", "argv: --dcgm-cleanup-timeout")}},
		{name: "flag negative", args: []string{"--dcgm-cleanup-timeout=-1s"}, want: []string{fmt.Sprintf(notPositive, "-1s", "argv: --dcgm-cleanup-timeout")}},
		{name: "flag garbage", args: []string{"--dcgm-cleanup-timeout", "soon"}, want: []string{"-dcgm-cleanup-timeout", `invalid value "soon"`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			if tt.env != "" {
				t.Setenv(envDCGMCleanupTimeout, tt.env)
			}

			_, err := resolveFileAt(t, writeConfigFile(t, "version: 2\n"), tt.args...)

			require.Error(t, err)
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestNewCleanupWaitUsesTheCleanupTimeout checks every wait reads the DCGM
// cleanup timeout and the given source, never the web write timeout, and that
// a config without a timeout falls back to the default with no source.
func TestNewCleanupWaitUsesTheCleanupTimeout(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.WebWriteTimeout = time.Second
	config.DCGMCleanupTimeout = 7 * time.Minute
	const source = "argv: --dcgm-cleanup-timeout"

	steps := []cleanupStep{
		cleanupReload, cleanupShutdown, cleanupFailedBuild, cleanupCanceledBuild, cleanupGPUWatchStart, cleanupGPUWatchStop,
	}

	for _, step := range steps {
		assert.Equal(t, cleanupWait{step: step, timeout: 7 * time.Minute, source: source}, newCleanupWait(config, source, step))
	}

	assert.Equal(t, cleanupWait{step: cleanupReload, timeout: appconfig.DefaultDCGMCleanupTimeout},
		newCleanupWait(nil, source, cleanupReload))
	assert.Equal(t, cleanupWait{step: cleanupReload, timeout: appconfig.DefaultDCGMCleanupTimeout},
		newCleanupWait(&appconfig.Config{WebWriteTimeout: time.Second}, source, cleanupReload))
}

// TestCleanupTimeoutErrorWording checks the message for each wait names the
// timeout and its source, says "restarted" only where the exporter stops so it
// can be restarted,
// and keeps the cause for errors.Is.
func TestCleanupTimeoutErrorWording(t *testing.T) {
	const hint = " DCGM can answer slowly, for example while it samples many NVLink fields. Raise sources.dcgm.cleanup_timeout " +
		"(--dcgm-cleanup-timeout, DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT) or watch fewer NVLink fields."
	const within = "within 5m0s (set by environment: DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT). "
	const restart = "The exporter is stopping so it can be restarted with a clean DCGM session."
	const stop = "The exporter is stopping without closing its DCGM session."
	tests := []struct {
		step cleanupStep
		want string
	}{
		{cleanupReload, "DCGM did not release the replaced watches " + within + restart + hint},
		{cleanupShutdown, "DCGM did not release the watches at shutdown " + within + stop + hint},
		{cleanupFailedBuild, "DCGM did not release the watches of collectors from a failed collector build " + within + restart + hint},
		{cleanupCanceledBuild, "DCGM did not release the watches of a registry build that was canceled " + within + restart + hint},
		{cleanupGPUWatchStart, "DCGM did not finish starting the GPU bind/unbind watch " + within + restart + hint},
		{cleanupGPUWatchStop, "DCGM did not finish stopping the GPU bind/unbind watch " + within + stop + hint},
	}

	for _, tt := range tests {
		wait := cleanupWait{step: tt.step, timeout: 5 * time.Minute, source: "environment: " + envDCGMCleanupTimeout}
		err := wait.expired(context.DeadlineExceeded)

		assert.Equal(t, tt.want, err.Error())
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	}

	unknown := cleanupWait{step: cleanupReload, timeout: time.Minute}.expired(context.DeadlineExceeded)
	assert.Contains(t, unknown.Error(), "within 1m0s. The exporter", "no source when startup did not record one")
}

// newCleanupTimeoutCoordinator returns a test coordinator whose cleanup
// timeout came from the default, as startup records it.
func newCleanupTimeoutCoordinator(t *testing.T) *reloadCoordinator {
	t.Helper()
	coord := newTestCoordinator(t)
	coord.cleanupSource = "default"
	coord.server.SetRegistry(registry.NewRegistry())

	return coord
}

// buildsFreshRegistry makes the coordinator's registry builds succeed.
func buildsFreshRegistry(coord *reloadCoordinator) {
	coord.buildRegistry = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
		*registry.Registry, devicewatchlistmanager.Manager, error,
	) {
		return registry.NewRegistry(), topologyManager(), nil
	}
}

// yamlCleanupSource is the source startup records for a cleanup timeout set
// in the config file.
const yamlCleanupSource = "YAML: " + appconfig.DCGMCleanupTimeoutKey

// TestReloadWaitsUseTheCleanupTimeout checks that a reload waits the cleanup
// timeout, not the web write timeout, both for the replaced registry and for
// the collectors of a failed build, and name the source of the timeout. A
// failed build that outlasts the timeout logs that the reload stopped, not
// that the last registry is kept.
func TestReloadWaitsUseTheCleanupTimeout(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.WebWriteTimeout = time.Second
	config.DCGMCleanupTimeout = 7 * time.Minute

	t.Run("replaced registry", func(t *testing.T) {
		coord := newCleanupTimeoutCoordinator(t)
		coord.cleanupSource = yamlCleanupSource
		buildsFreshRegistry(coord)
		var waits []cleanupWait
		coord.awaitRegistryRetirement = func(_ context.Context, _ *registry.Registry, wait cleanupWait) error {
			waits = append(waits, wait)
			return nil
		}

		require.True(t, coord.doConfigReload(context.Background(), config, 1))

		assert.Equal(t, []cleanupWait{{step: cleanupReload, timeout: 7 * time.Minute, source: yamlCleanupSource}}, waits)
	})

	t.Run("failed build", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			logs := captureTextLogs(t)
			coord := newCleanupTimeoutCoordinator(t)
			coord.cleanupSource = yamlCleanupSource
			done := make(chan struct{})
			coord.buildRegistry = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
				*registry.Registry, devicewatchlistmanager.Manager, error,
			) {
				return nil, nil, deferredBuildError{err: errors.New("build failed"), done: done}
			}

			began := time.Now()
			require.False(t, coord.doConfigReload(context.Background(), config, 1))

			assert.Equal(t, 7*time.Minute, time.Since(began))
			assert.True(t, coord.nativeCleanupBlocked.Load())
			assert.Contains(t, logs(), `level=ERROR msg="Hot reload stopped because collector cleanup did not finish" `+
				`reload_id=1 build_error="build failed" error="DCGM did not release the watches of collectors from a failed `+
				`collector build within 7m0s (set by YAML: sources.dcgm.cleanup_timeout). The exporter is stopping so it can be restarted`)
			assert.Contains(t, logs(), `cleanup_timeout=7m0s`)
			assert.NotContains(t, logs(), "preserving last-good registry")
			close(done)
		})
	})
}

// captureTextLogs sends the default logger to a text handler for the test and
// returns its output with the time attribute removed.
func captureTextLogs(t *testing.T) func() string {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	timeAttr := regexp.MustCompile(`(?m)^time=\S+ `)
	return func() string { return timeAttr.ReplaceAllString(logs.String(), "") }
}

// TestReloadTimeoutLogsTheCleanupTimeoutMessage checks that a reload whose
// replaced registry outlasts the cleanup timeout logs that the reload stopped,
// with the reload ID, the full timeout message and the timeout, and ends the
// exporter.
func TestReloadTimeoutLogsTheCleanupTimeoutMessage(t *testing.T) {
	logs := captureTextLogs(t)
	coord := newCleanupTimeoutCoordinator(t)
	buildsFreshRegistry(coord)
	coord.awaitRegistryRetirement = func(_ context.Context, _ *registry.Registry, wait cleanupWait) error {
		return wait.expired(context.DeadlineExceeded)
	}

	require.False(t, coord.doConfigReload(context.Background(), appconfig.DefaultConfig(), 1))

	assert.Contains(t, logs(), `level=ERROR msg="Hot reload stopped because the replaced registry did not retire" reload_id=1 `+
		`error="DCGM did not release the replaced watches within 5m0s (set by default). `+
		`The exporter is stopping so it can be restarted with a clean DCGM session. `+
		`DCGM can answer slowly, for example while it samples many NVLink fields. Raise sources.dcgm.cleanup_timeout `+
		`(--dcgm-cleanup-timeout, DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT) or watch fewer NVLink fields." `+
		`cleanup_timeout=5m0s`+"\n")
	assert.True(t, coord.nativeCleanupBlocked.Load())
	require.ErrorIs(t, coord.server.TerminalError(), context.DeadlineExceeded)
}

// canceledBuildMessage is the start of the timeout message for a registry
// build that was canceled, with the source startup recorded.
const canceledBuildMessage = "DCGM did not release the watches of a registry build that was canceled within 5m0s " +
	"(set by argv: --dcgm-cleanup-timeout). The exporter is stopping"

// cancelsAfterBuild returns a registry build that succeeds after it cancels
// the run, as when a stop or a newer reload arrives during the build.
func cancelsAfterBuild(cancel context.CancelFunc) func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
	*registry.Registry, devicewatchlistmanager.Manager, error,
) {
	return func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
		*registry.Registry, devicewatchlistmanager.Manager, error,
	) {
		cancel()
		return registry.NewRegistry(), topologyManager(), nil
	}
}

// TestCanceledBuildTimeoutNamesTheStepAndSource checks that a registry whose
// build finished after a cancel, and whose watches DCGM does not release in
// time, reports the canceled-build step and the input that set the timeout:
// at startup once in the returned error, and on reload in the log.
func TestCanceledBuildTimeoutNamesTheStepAndSource(t *testing.T) {
	expire := func(_ context.Context, _ *registry.Registry, wait cleanupWait) error {
		return wait.expired(context.DeadlineExceeded)
	}

	t.Run("startup", func(t *testing.T) {
		previous := buildRegistryFunc
		t.Cleanup(func() { buildRegistryFunc = previous })
		coord := newTestCoordinator(t)
		coord.cleanupSource = "argv: --dcgm-cleanup-timeout"
		coord.awaitRegistryRetirement = expire
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		buildRegistryFunc = cancelsAfterBuild(cancel)

		_, _, err := buildInitialRegistry(ctx, appconfig.DefaultConfig(), coord)

		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, 1, strings.Count(err.Error(), canceledBuildMessage), err.Error())
		assert.Equal(t, 1, strings.Count(err.Error(), "DCGM did not"), "the timeout is reported once")
		assert.True(t, coord.nativeCleanupBlocked.Load())
	})

	t.Run("reload", func(t *testing.T) {
		logs := captureTextLogs(t)
		coord := newCleanupTimeoutCoordinator(t)
		coord.cleanupSource = "argv: --dcgm-cleanup-timeout"
		coord.awaitRegistryRetirement = expire
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		coord.buildRegistry = cancelsAfterBuild(cancel)

		require.False(t, coord.doConfigReload(ctx, appconfig.DefaultConfig(), 1))

		assert.Contains(t, logs(), `level=ERROR msg="Canceled reload candidate did not retire safely" reload_id=1 error="`+
			canceledBuildMessage)
		assert.Contains(t, logs(), `cleanup_timeout=5m0s`)
		assert.True(t, coord.nativeCleanupBlocked.Load())
	})
}

// TestShutdownTimeoutReturnsTheCleanupTimeoutMessage checks, through the
// shutdown cleanup the exporter installs, that a last registry that outlasts
// the cleanup timeout returns the shutdown message, which names the shutdown
// step, the time limit and its source and does not promise a restart, and
// keeps DCGM open.
func TestShutdownTimeoutReturnsTheCleanupTimeoutMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := registry.NewRegistry()
		blocking := &retirementBlockingCollector{done: make(chan struct{})}
		tuple := collector.EntityCollectorTuple{}
		tuple.SetEntity(dcgm.FE_GPU)
		tuple.SetCollector(blocking)
		require.NoError(t, reg.Register(tuple))

		config := appconfig.DefaultConfig()
		config.DCGMCleanupTimeout = 2 * time.Minute
		coord := newTestCoordinator(t)
		coord.cleanupSource = yamlCleanupSource
		cleanup := shutdownRegistryCleanup(context.Background(), coord, config, func() *registry.Registry { return reg })

		var returnErr error
		began := time.Now()
		cleanupRegistryAfterShutdown(coord, cleanup, &returnErr)

		assert.Equal(t, 2*time.Minute, time.Since(began))
		require.ErrorIs(t, returnErr, context.DeadlineExceeded)
		assert.Contains(t, returnErr.Error(), "DCGM did not release the watches at shutdown within 2m0s "+
			"(set by YAML: sources.dcgm.cleanup_timeout). The exporter is stopping without closing its DCGM session.")
		assert.NotContains(t, returnErr.Error(), "restart")
		assert.True(t, coord.nativeCleanupBlocked.Load())
		close(blocking.done)
	})
}

// errTimedOutGPUWatch is the error a GPU watcher returns after its native call
// outlasted the watcher's time limit.
var errTimedOutGPUWatch = fmt.Errorf("%w: stop took longer than 1m0s", watcher.ErrGPUWatchTimeout)

// TestGPUWatchStopTimeoutAtShutdownLogsTheStopMessage checks that a GPU watch
// that does not stop within the cleanup timeout when the exporter stops logs
// the stop message without promising a restart.
func TestGPUWatchStopTimeoutAtShutdownLogsTheStopMessage(t *testing.T) {
	logs := captureTextLogs(t)
	coord := newCleanupTimeoutCoordinator(t)
	coord.setGPUWatcher(&scriptedGPUWatcherLifecycle{stopErr: errTimedOutGPUWatch},
		cleanupWait{timeout: time.Minute, source: "environment: " + envDCGMCleanupTimeout})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	coord.stopGPUWatcherOnDone(ctx, &wg)
	cancel()
	wg.Wait()

	assert.Contains(t, logs(), `level=ERROR msg="GPU watcher did not stop" error="DCGM did not finish stopping the GPU `+
		`bind/unbind watch within 1m0s (set by environment: DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT). `+
		`The exporter is stopping without closing its DCGM session.`)
	assert.Contains(t, logs(), `cleanup_timeout=1m0s`)
	assert.NotContains(t, logs(), "restart")
	assert.True(t, coord.nativeCleanupBlocked.Load())
}

// TestGPUWatchRestartTimeoutAfterResetLogsTheStartMessage checks that a GPU
// watch that does not start again after a GPU reset logs the start message
// with the reload ID, ends the exporter, and schedules no retry.
func TestGPUWatchRestartTimeoutAfterResetLogsTheStartMessage(t *testing.T) {
	logs := captureTextLogs(t)
	coord := newCleanupTimeoutCoordinator(t)
	coord.setGPUWatcher(&scriptedGPUWatcherLifecycle{startErrors: []error{errTimedOutGPUWatch}},
		cleanupWait{timeout: time.Minute, source: "default"})
	retries := 0
	coord.scheduleRetry = func(context.Context, time.Duration, func()) context.CancelFunc {
		retries++
		return func() {}
	}
	coord.gpuRecoveryStage = gpuRecoveryRestartWatcher
	coord.gpuRecoveryConfig = appconfig.DefaultConfig()

	require.False(t, coord.restartRecoveryGPUWatcher(context.Background(), 1))

	assert.Contains(t, logs(), `level=ERROR msg="GPU watcher did not restart after lifecycle reset" reload_id=1 `+
		`error="DCGM did not finish starting the GPU bind/unbind watch within 1m0s (set by default). `+
		`The exporter is stopping so it can be restarted with a clean DCGM session.`)
	assert.Contains(t, logs(), `cleanup_timeout=1m0s`)
	assert.Zero(t, retries)
	assert.True(t, coord.nativeCleanupBlocked.Load())
	require.ErrorIs(t, coord.server.TerminalError(), watcher.ErrGPUWatchTimeout)
}

// TestGPUWatchTimeoutIsReportedOnce checks that the watcher's one timeout comes
// back as the same error from start and stop, so the exit error holds it once.
func TestGPUWatchTimeoutIsReportedOnce(t *testing.T) {
	coord := newCleanupTimeoutCoordinator(t)
	coord.setGPUWatcher(&scriptedGPUWatcherLifecycle{startErrors: []error{errTimedOutGPUWatch}, stopErr: errTimedOutGPUWatch},
		cleanupWait{timeout: time.Minute})

	startErr := coord.startGPUWatcher(context.Background())
	stopErr := coord.stopGPUWatcher()

	require.Error(t, startErr)
	assert.Same(t, startErr, stopErr)
	assert.ErrorIs(t, fmt.Errorf("start GPU bind/unbind watcher: %w", startErr), stopErr)
	assert.Contains(t, startErr.Error(), "finish starting the GPU bind/unbind watch")
}

// TestGPUWatchTimeoutIsLoggedOnce checks that a GPU watch that times out when
// it restarts after a GPU reset is logged there and not again when the
// exporter stops, although the watcher returns the same timeout from the stop.
func TestGPUWatchTimeoutIsLoggedOnce(t *testing.T) {
	logs := captureTextLogs(t)
	coord := newCleanupTimeoutCoordinator(t)
	coord.setGPUWatcher(&scriptedGPUWatcherLifecycle{startErrors: []error{errTimedOutGPUWatch}, stopErr: errTimedOutGPUWatch},
		cleanupWait{timeout: time.Minute, source: "default"})
	coord.gpuRecoveryStage = gpuRecoveryRestartWatcher
	coord.gpuRecoveryConfig = appconfig.DefaultConfig()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	require.False(t, coord.restartRecoveryGPUWatcher(ctx, 1))
	coord.stopGPUWatcherOnDone(ctx, &wg)
	cancel()
	wg.Wait()

	assert.Equal(t, 1, strings.Count(logs(), "DCGM did not finish"), logs())
	assert.NotContains(t, logs(), `msg="GPU watcher did not stop"`)
}

// TestGPURecoveryFailedBuildTimeoutStopsRecovery checks that a registry build
// after a GPU reset that fails, and whose collectors DCGM does not release
// within the cleanup timeout, waits exactly that timeout, logs that recovery
// stopped with the reload ID and the timeout, blocks native cleanup, and
// schedules no retry.
func TestGPURecoveryFailedBuildTimeoutStopsRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logs := captureTextLogs(t)
		coord := newCleanupTimeoutCoordinator(t)
		coord.gpuRecoveryConfig = appconfig.DefaultConfig()
		coord.gpuRecoveryConfig.DCGMCleanupTimeout = 7 * time.Minute
		done := make(chan struct{})
		coord.buildRegistry = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
			*registry.Registry, devicewatchlistmanager.Manager, error,
		) {
			return nil, nil, deferredBuildError{err: errors.New("build failed"), done: done}
		}
		coord.scheduleRetry = func(context.Context, time.Duration, func()) context.CancelFunc {
			t.Error("a cleanup timeout must not schedule a retry")
			return func() {}
		}

		began := time.Now()
		coord.buildAndPublishRecoveredRegistry(context.Background(), 3)

		assert.Equal(t, 7*time.Minute, time.Since(began))
		assert.True(t, coord.nativeCleanupBlocked.Load())
		assert.Contains(t, logs(), `level=ERROR msg="GPU lifecycle recovery stopped because candidate cleanup did not finish" `+
			`reload_id=3 error="DCGM did not release the watches of collectors from a failed collector build within 7m0s (set by default).`)
		assert.Contains(t, logs(), `cleanup_timeout=7m0s`)
		close(done)
	})
}

// TestRetireTimeoutsLogTheirPath checks that a cleanup timeout on each path
// that retires a registry logs that path's message with the reload ID, the
// timeout message and the cleanup_timeout attribute.
func TestRetireTimeoutsLogTheirPath(t *testing.T) {
	tests := []struct {
		name    string
		run     func(ctx context.Context, cancel context.CancelFunc, coord *reloadCoordinator)
		wantMsg string
	}{
		{
			name: "canceled reload candidate",
			run: func(ctx context.Context, cancel context.CancelFunc, coord *reloadCoordinator) {
				coord.buildRegistry = cancelsAfterBuild(cancel)
				coord.doConfigReload(ctx, appconfig.DefaultConfig(), 1)
			},
			wantMsg: "Canceled reload candidate did not retire safely",
		},
		{
			name: "replaced registry",
			run: func(ctx context.Context, _ context.CancelFunc, coord *reloadCoordinator) {
				buildsFreshRegistry(coord)
				coord.doConfigReload(ctx, appconfig.DefaultConfig(), 1)
			},
			wantMsg: "Hot reload stopped because the replaced registry did not retire",
		},
		{
			name: "canceled recovery candidate",
			run: func(ctx context.Context, cancel context.CancelFunc, coord *reloadCoordinator) {
				coord.buildRegistry = cancelsAfterBuild(cancel)
				coord.buildAndPublishRecoveredRegistry(ctx, 1)
			},
			wantMsg: "Discarded registry did not retire safely",
		},
		{
			name: "recovered registry replacement",
			run: func(ctx context.Context, _ context.CancelFunc, coord *reloadCoordinator) {
				buildsFreshRegistry(coord)
				coord.buildAndPublishRecoveredRegistry(ctx, 1)
			},
			wantMsg: "Recovered registry replacement did not retire safely",
		},
		{
			name: "registry cleared by a GPU reset",
			run: func(ctx context.Context, _ context.CancelFunc, coord *reloadCoordinator) {
				coord.doGPULifecycleReset(ctx, 1)
			},
			wantMsg: "GPU lifecycle reset stopped because registry retirement did not finish",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureTextLogs(t)
			coord := newCleanupTimeoutCoordinator(t)
			coord.gpuRecoveryConfig = appconfig.DefaultConfig()
			coord.awaitRegistryRetirement = func(_ context.Context, _ *registry.Registry, wait cleanupWait) error {
				return wait.expired(context.DeadlineExceeded)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			tt.run(ctx, cancel, coord)

			assert.Contains(t, logs(), `level=ERROR msg="`+tt.wantMsg+`" reload_id=1 error="DCGM did not release`)
			assert.Contains(t, logs(), `(set by default)`)
			assert.Contains(t, logs(), `cleanup_timeout=5m0s`)
			assert.True(t, coord.nativeCleanupBlocked.Load())
		})
	}
}

// TestConfigReloadRequiresRestartForTheCleanupTimeout checks that a reread
// config file that changes cleanup_timeout is rejected until a restart.
func TestConfigReloadRequiresRestartForTheCleanupTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		wantErr string
	}{
		{name: "unchanged", timeout: appconfig.DefaultDCGMCleanupTimeout},
		{name: "changed", timeout: time.Minute, wantErr: "changing sources.dcgm.cleanup_timeout requires exporter restart"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startup := appconfig.DefaultConfig()
			startup.EnableOTLP = true
			startup.ConfigFile = "/etc/dcgm-exporter/config.yaml"
			coord := initReloadCoordinator(startup)
			coord.readConfig = func() (*appconfig.Config, error) {
				reread := startup.Clone()
				reread.DCGMCleanupTimeout = tt.timeout
				return reread, nil
			}

			cfg, err := coord.buildConfigReload()

			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.timeout, cfg.DCGMCleanupTimeout)
				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, cfg)
		})
	}
}

// TestExplainShowsTheCleanupTimeoutSource checks config explain reports the
// cleanup timeout with the input that set it.
func TestExplainShowsTheCleanupTimeoutSource(t *testing.T) {
	tests := []struct {
		name, env  string
		root       []string
		want       string
		wantSource settingSource
	}{
		{name: "default", want: "5m", wantSource: settingSource{Kind: appconfig.OriginDefault}},
		{name: "env", env: "3m", want: "3m", wantSource: settingSource{Kind: appconfig.OriginEnv, Name: envDCGMCleanupTimeout}},
		{
			name: "flag", root: []string{"--dcgm-cleanup-timeout", "2m"}, want: "2m",
			wantSource: settingSource{Kind: appconfig.OriginFlag, Name: "--dcgm-cleanup-timeout"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			if tt.env != "" {
				t.Setenv(envDCGMCleanupTimeout, tt.env)
			}

			row := settingByPath(t, explainJSON(t, tt.root, explainFile(t, ""), "--verbose"), "sources.dcgm.cleanup_timeout")

			assert.Equal(t, tt.want, row.Value)
			assert.Equal(t, tt.wantSource, row.Source)
		})
	}
}

// TestExplainShowsTheCleanupTimeoutFromTheFile checks a file value is shown
// with its file position, and that config show writes it back unchanged.
func TestExplainShowsTheCleanupTimeoutFromTheFile(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, fmt.Sprintf("version: 2\nsources:\n  dcgm:\n    cleanup_timeout: 4m\n"+
		"metrics:\n  - name: gpu\n    file: %s\noutputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n",
		inspectionCSV(t, explainCSVRows)))

	row := settingByPath(t, explainJSON(t, nil, path), "sources.dcgm.cleanup_timeout")
	shown, _, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err)

	assert.Equal(t, "4m", row.Value)
	assert.Equal(t, appconfig.OriginFile, row.Source.Kind)
	assert.Equal(t, "sources.dcgm.cleanup_timeout", row.Source.Path)
	assert.Equal(t, 4, row.Source.Line)
	assert.Contains(t, shown, "\n    cleanup_timeout: 4m\n")
}
