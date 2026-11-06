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
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

// reloadEvent identifies one kind of reload work.
type reloadEvent int

const (
	evNone reloadEvent = iota
	evConfigChanged
	// evDRAResourceSliceChanged rebuilds the registry after a complete DRA pool topology changes.
	evDRAResourceSliceChanged
	// evDRAResourceSliceRetry is the one bounded replay for a failed
	// ResourceSlice-triggered registry build. A retry never schedules another retry.
	evDRAResourceSliceRetry
	evGPUReinitialized
	evGPURecoveryRetry
)

// gpuRecoveryStage records the next unfinished step after a GPU lifecycle
// reset. The reload coordinator retries this step without repeating completed
// provider cleanup or registry work.
type gpuRecoveryStage int

const (
	gpuRecoveryIdle gpuRecoveryStage = iota
	// gpuRecoveryInitializeDCGM retries provider creation before work touches it.
	gpuRecoveryInitializeDCGM
	// gpuRecoveryRestartWatcher restarts the watcher after DCGM is usable.
	gpuRecoveryRestartWatcher
	// gpuRecoveryBuildRegistry retries only registry construction after providers work.
	gpuRecoveryBuildRegistry
)

// pendingReload stores one pending change of each kind for the reload loop.
// Trigger combines repeated notifications under a lock; Run processes the
// saved events in the order required for safe GPU recovery.
type pendingReload struct {
	configChanged bool
	// draResourceSliceEvent retains one DRA-triggered registry rebuild while other reload work is coalesced.
	draResourceSliceEvent reloadEvent
	latestGPUEvent        reloadEvent
}

// empty reports whether the snapshot contains no work for Run to process.
func (p pendingReload) empty() bool {
	return !p.configChanged && p.draResourceSliceEvent == evNone && p.latestGPUEvent == evNone
}

// gpuWatcherLifecycle lets the reload coordinator stop and restart the GPU
// watcher. Start and Stop return watcher.ErrGPUWatchTimeout when native work
// may still be running.
type gpuWatcherLifecycle interface {
	Start(context.Context) error
	Stop() error
}

// reloadCoordinator handles metric-file, Kubernetes resource, and GPU lifecycle
// changes one at a time. The application queues events through Trigger; Run
// rebuilds collectors and waits for old native work to finish before GPU resets.
type reloadCoordinator struct {
	server       *server.MetricsServer
	reloadConfig *appconfig.Config
	readConfig   func() (*appconfig.Config, error)

	pendingMu sync.Mutex
	pending   pendingReload
	wake      chan struct{}

	// Owned by the Run goroutine.
	dcp counters.DCPCapabilities

	// Operations used during reload and GPU recovery. Tests replace these
	// callbacks to check ordering and failures without GPU hardware.
	applyConfigReload       func(ctx context.Context, cfg *appconfig.Config, reloadID uint64)
	buildRegistry           func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error)
	cleanupDCGM             func()
	initializeDCGM          func(cfg *appconfig.Config) error
	cleanupNVML             func()
	initializeNVML          func() error
	gpuWatcher              gpuWatcherLifecycle
	scheduleRetry           func(context.Context, time.Duration, func()) context.CancelFunc
	awaitRegistryRetirement func(context.Context, *registry.Registry, cleanupWait) error
	// cleanupSource is the input that set the DCGM cleanup timeout, as startup
	// resolved it. A reload cannot change the timeout, so this stays accurate
	// for the whole run.
	cleanupSource string
	// gpuWatchWait is the time limit the GPU watcher was built with, so a
	// watcher timeout can be reported with it.
	gpuWatchWait cleanupWait
	// gpuWatchErr is the reported GPU watcher timeout. The watcher keeps
	// returning its first timeout, so later calls report the same error value.
	gpuWatchErr atomic.Pointer[dcgmCleanupTimeoutError]
	// gpuWatchErrLogged is set once gpuWatchErr has been logged.
	gpuWatchErrLogged atomic.Bool

	// nativeCleanupBlocked is set after a registry cannot prove every native
	// user has finished. The run-level defers then leave DCGM and NVML intact
	// and return a terminal error for the process supervisor to restart safely.
	nativeCleanupBlocked atomic.Bool

	// Owned by the Run goroutine. Recovery retries resume at the failed stage
	// so DCGM and NVML are not repeatedly torn down while the coordinator
	// remains free to process config reloads and shutdown.
	gpuRecoveryStage           gpuRecoveryStage
	gpuRecoveryConfig          *appconfig.Config
	gpuRecoveryStartedAt       time.Time
	gpuRecoveryRetryDelay      time.Duration
	gpuRecoveryRetries         int
	cancelGPURecoveryRetry     context.CancelFunc
	reloadYAMLAfterGPURecovery bool
}

const (
	lifecycleRetryInitial = time.Second
	lifecycleRetryMax     = 30 * time.Second
	maxGPURecoveryRetries = 6
)

// newReloadCoordinator creates the serialized lifecycle owner with production
// dependencies. Startup installs its configuration and server before Run
// begins processing events.
func newReloadCoordinator() *reloadCoordinator {
	r := &reloadCoordinator{
		wake: make(chan struct{}, 1),
	}
	r.applyConfigReload = func(ctx context.Context, cfg *appconfig.Config, reloadID uint64) {
		r.doConfigReload(ctx, cfg, reloadID)
	}
	r.buildRegistry = buildRegistryFunc

	r.cleanupDCGM = func() {
		if dcgmClient := dcgmprovider.Client(); dcgmClient != nil {
			dcgmClient.Cleanup()
		}
	}
	r.initializeDCGM = initializeDCGMProviderFunc
	r.cleanupNVML = cleanupNVMLProviderFunc
	r.initializeNVML = initializeNVMLProviderFunc

	r.scheduleRetry = scheduleAfter
	r.awaitRegistryRetirement = waitForRegistryRetirement
	r.gpuRecoveryRetryDelay = lifecycleRetryInitial

	return r
}

// scheduleAfter invokes callback after delay unless ctx is canceled first.
// GPU lifecycle recovery uses the returned cancel function to replace a stale
// retry whenever new work completes or the exporter shuts down.
func scheduleAfter(ctx context.Context, delay time.Duration, callback func()) context.CancelFunc {
	retryCtx, cancel := context.WithCancel(ctx)
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-retryCtx.Done():
		case <-timer.C:
			callback()
		}
	}()
	return cancel
}

// nextLifecycleRetryDelay doubles a retry delay without exceeding the
// lifecycle retry ceiling. Recovery calls it after scheduling each retry.
func nextLifecycleRetryDelay(delay time.Duration) time.Duration {
	delay *= 2
	if delay > lifecycleRetryMax {
		return lifecycleRetryMax
	}
	return delay
}

// setServer installs the metrics server before Run starts.
func (r *reloadCoordinator) setServer(s *server.MetricsServer) {
	r.server = s
}

// setGPUWatcher gives lifecycle resets control of the GPU watcher. wait is the
// time limit the watcher was built with, which a watcher timeout reports.
func (r *reloadCoordinator) setGPUWatcher(w gpuWatcherLifecycle, wait cleanupWait) {
	r.gpuWatcher = w
	r.gpuWatchWait = wait
}

// cleanupWait is the wait for one step, with the cleanup timeout from config
// and the input that startup recorded for it.
func (r *reloadCoordinator) cleanupWait(config *appconfig.Config, step cleanupStep) cleanupWait {
	return newCleanupWait(config, r.cleanupSource, step)
}

// blockNativeCleanup records that native work may still be running, after an
// unsafe registry retirement or a GPU watcher timeout, and asks the HTTP server
// to end the process with an error. It is idempotent because shutdown defers
// may observe the same failure later.
func (r *reloadCoordinator) blockNativeCleanup(err error) {
	if err == nil {
		return
	}
	r.nativeCleanupBlocked.Store(true)
	if r.server != nil {
		r.server.ReportTerminalError(fmt.Errorf("native work did not finish safely: %w", err))
	}
}

// startGPUWatcher starts the GPU watcher. A setup timeout blocks native
// cleanup, because the setup call may still be using DCGM, and is returned as a
// *dcgmCleanupTimeoutError.
func (r *reloadCoordinator) startGPUWatcher(ctx context.Context) error {
	err := r.gpuWatchTimeoutError(r.gpuWatcher.Start(ctx), cleanupGPUWatchStart)
	if errors.Is(err, watcher.ErrGPUWatchTimeout) {
		r.blockNativeCleanup(err)
	}
	return err
}

// stopGPUWatcher stops the GPU watcher, if there is one. A stop timeout blocks
// native cleanup, because the watch may still be using DCGM.
func (r *reloadCoordinator) stopGPUWatcher() error {
	if r.gpuWatcher == nil {
		return nil
	}

	err := r.gpuWatchTimeoutError(r.gpuWatcher.Stop(), cleanupGPUWatchStop)
	if errors.Is(err, watcher.ErrGPUWatchTimeout) {
		r.blockNativeCleanup(err)
	}
	return err
}

// gpuWatchTimeoutError turns a GPU watcher timeout during step into a
// *dcgmCleanupTimeoutError that names the time limit the watcher was built
// with. The first timeout is kept and returned again, so start and stop
// report one error. Other errors are returned as they are.
func (r *reloadCoordinator) gpuWatchTimeoutError(err error, step cleanupStep) error {
	if !errors.Is(err, watcher.ErrGPUWatchTimeout) {
		return err
	}

	wait := r.gpuWatchWait
	wait.step = step
	r.gpuWatchErr.CompareAndSwap(nil, wait.expired(err))

	return r.gpuWatchErr.Load()
}

// logGPUWatchFailure logs a failed start or stop of the GPU watcher under msg.
// A watcher timeout is logged only the first time, because the watcher keeps
// returning it and every later start or stop would report it again.
func (r *reloadCoordinator) logGPUWatchFailure(ctx context.Context, msg string, err error, attrs ...any) {
	var timeout *dcgmCleanupTimeoutError
	if errors.As(err, &timeout) && r.gpuWatchErrLogged.Swap(true) {
		return
	}

	logCleanupFailure(ctx, msg, err, attrs...)
}

// retireRegistry waits for a registry that was removed from serving or never
// published; step says which, for a timeout message. The DCGM cleanup timeout
// bounds the complete gather, collector stop, and watch-release path; an error
// switches the application to terminal safe mode.
func (r *reloadCoordinator) retireRegistry(
	ctx context.Context,
	reg *registry.Registry,
	config *appconfig.Config,
	step cleanupStep,
) error {
	err := r.awaitRegistryRetirement(ctx, reg, r.cleanupWait(config, step))
	if err != nil {
		r.blockNativeCleanup(err)
	}
	return err
}

// initReloadCoordinator saves the startup configuration used by file-only
// reloads. DRA and DCP runtime state stay on the coordinator rather than in
// that configuration snapshot.
func initReloadCoordinator(config *appconfig.Config) *reloadCoordinator {
	coord := newReloadCoordinator()
	coord.reloadConfig = config.Clone()
	return coord
}

// Trigger coalesces work into the bounded pending snapshot unless a failed
// retirement already put the application into terminal safe mode.
func (r *reloadCoordinator) Trigger(ev reloadEvent) {
	if r.nativeCleanupBlocked.Load() {
		return
	}
	r.pendingMu.Lock()
	switch ev {
	case evConfigChanged:
		r.pending.configChanged = true
	case evDRAResourceSliceChanged:
		r.pending.draResourceSliceEvent = evDRAResourceSliceChanged
	case evDRAResourceSliceRetry:
		if r.pending.draResourceSliceEvent == evNone {
			r.pending.draResourceSliceEvent = evDRAResourceSliceRetry
		}
	case evGPUReinitialized:
		r.pending.latestGPUEvent = evGPUReinitialized
	case evGPURecoveryRetry:
		if r.pending.latestGPUEvent == evNone {
			r.pending.latestGPUEvent = evGPURecoveryRetry
		}
	}
	r.pendingMu.Unlock()

	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// takePending returns and clears the coalesced event snapshot under its lock.
// Run owns processing after this handoff, so it can work without the lock held.
func (r *reloadCoordinator) takePending() pendingReload {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()

	pending := r.pending
	r.pending = pendingReload{}
	return pending
}

// handlePending orders coalesced config, DRA, and GPU events safely. Run calls
// it after takePending so a completed GPU reset supersedes ordinary reload work.
func (r *reloadCoordinator) handlePending(ctx context.Context, pending pendingReload) {
	reloadYAML := pending.configChanged && r.reloadConfig != nil && r.reloadConfig.EnableOTLP
	// A completed physical lifecycle is safe to reset. The watcher deliberately
	// suppresses the earlier reinitializing state because its topology is not
	// ready for exporter reinitialization yet.
	if pending.latestGPUEvent == evGPUReinitialized {
		if reloadYAML {
			r.reloadYAMLAfterGPURecovery = true
		}
		r.handle(ctx, pending.latestGPUEvent)
	} else if pending.draResourceSliceEvent != evNone {
		r.handle(ctx, pending.draResourceSliceEvent)
	}
	if pending.configChanged && pending.latestGPUEvent != evGPUReinitialized &&
		(pending.draResourceSliceEvent == evNone || reloadYAML) {
		r.handle(ctx, evConfigChanged)
	}
	if pending.latestGPUEvent == evGPURecoveryRetry {
		r.handle(ctx, pending.latestGPUEvent)
	}
}

// Run drains pending work while preserving the latest GPU lifecycle state.
func (r *reloadCoordinator) Run(ctx context.Context) {
	defer r.cancelRecoveryRetry()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			pending := r.takePending()
			if pending.empty() {
				continue
			}
			r.handlePending(ctx, pending)
		}
	}
}

// handle runs one event with panic recovery so later pending work can still run.
func (r *reloadCoordinator) handle(ctx context.Context, ev reloadEvent) {
	if r.nativeCleanupBlocked.Load() {
		return
	}
	reloadID := hotReloadCounter.Add(1)

	defer func() {
		if panicValue := recover(); panicValue != nil {
			r.recoverReloadPanic(ctx, ev, reloadID, panicValue)
		}
	}()

	r.server.SetReloadInProgress(true)
	defer func() {
		if r.gpuRecoveryStage == gpuRecoveryIdle {
			r.server.SetReloadInProgress(false)
		}
	}()

	switch ev {
	case evConfigChanged, evDRAResourceSliceChanged, evDRAResourceSliceRetry:
		r.handleConfigEvent(ctx, ev, reloadID)
	case evGPUReinitialized:
		r.doGPULifecycleReset(ctx, reloadID)
	case evGPURecoveryRetry:
		r.retryGPULifecycleRecovery(ctx, reloadID)
	}
}

// handleConfigEvent applies file and DRA reloads or defers them to GPU recovery.
func (r *reloadCoordinator) handleConfigEvent(ctx context.Context, ev reloadEvent, reloadID uint64) {
	if ev == evConfigChanged && r.gpuRecoveryStage != gpuRecoveryIdle &&
		r.reloadConfig != nil && r.reloadConfig.EnableOTLP {
		r.reloadYAMLAfterGPURecovery = true
		return
	}
	var cfg *appconfig.Config
	var err error
	if ev == evConfigChanged {
		cfg, err = r.buildConfigReload()
	} else {
		cfg, err = r.buildReloadConfig()
	}
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build reload config",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
		return
	}

	if r.gpuRecoveryStage != gpuRecoveryIdle {
		if ev == evConfigChanged {
			// A later recovery retry must use the latest config.
			r.gpuRecoveryConfig = cfg.Clone()
		}
		// Recovery publishes one registry after DCGM and profiling support are ready.
		return
	}

	if ev == evConfigChanged {
		r.applyConfigReload(ctx, cfg, reloadID)
		return
	}

	if !r.doConfigReload(ctx, cfg, reloadID) && ev == evDRAResourceSliceChanged {
		// One immediate, coalesced retry covers a transient registry build
		// failure without creating an unbounded or tight retry loop.
		r.Trigger(evDRAResourceSliceRetry)
	}
}

// recoverReloadPanic records a reload panic and applies the event-specific
// recovery policy. GPU reset panics either retry the unfinished stage or stop
// native cleanup; DRA rebuild panics receive one bounded replay.
func (r *reloadCoordinator) recoverReloadPanic(ctx context.Context, ev reloadEvent, reloadID uint64, panicValue any) {
	stackBuf := make([]byte, 8192)
	n := runtime.Stack(stackBuf, false)
	slog.ErrorContext(ctx, "PANIC RECOVERED in reload",
		slog.String("panic_value", fmt.Sprintf("%v", panicValue)),
		slog.String("panic_type", fmt.Sprintf("%T", panicValue)),
		slog.Uint64("reload_id", reloadID),
		slog.String("stack_trace", string(stackBuf[:n])))

	switch {
	case (ev == evGPUReinitialized || ev == evGPURecoveryRetry) &&
		r.gpuRecoveryStage != gpuRecoveryIdle && r.gpuRecoveryConfig != nil && ctx.Err() == nil:
		r.scheduleRecoveryRetry(
			ctx,
			reloadID,
			"GPU lifecycle recovery panicked",
			fmt.Errorf("panic: %v", panicValue),
		)
	case (ev == evGPUReinitialized || ev == evGPURecoveryRetry) && ctx.Err() == nil:
		// Provider teardown may be incomplete; do not retry native initialization.
		r.nativeCleanupBlocked.Store(true)
		r.server.ReportTerminalError(fmt.Errorf("panic during GPU lifecycle reset: %v", panicValue))
	case ev == evDRAResourceSliceChanged && ctx.Err() == nil:
		// Match the bounded error path: the first DRA-triggered attempt gets
		// one replay even when registry construction panics.
		r.Trigger(evDRAResourceSliceRetry)
	}
}

// buildReloadConfig clones the startup-resolved settings for one rebuild.
// Reload paths call it instead of rereading YAML or command-line inputs.
func (r *reloadCoordinator) buildReloadConfig() (*appconfig.Config, error) {
	if r.reloadConfig != nil {
		return r.reloadConfig.Clone(), nil
	}

	return nil, fmt.Errorf("buildReloadConfig: no startup config snapshot; coordinator was not initialized via initReloadCoordinator")
}

// doConfigReload preserves the last-good registry until its replacement exists.
// It reports whether a replacement was installed so DRA topology reloads can
// schedule their single bounded retry after a transient build failure.
func (r *reloadCoordinator) doConfigReload(ctx context.Context, cfg *appconfig.Config, reloadID uint64) bool {
	slog.InfoContext(ctx, "Hot reload triggered - building new registry",
		slog.Uint64("reload_id", reloadID))
	startTime := time.Now()

	newRegistry, deviceWatchListMgr, err := r.buildRegistry(ctx, cfg, r.dcp)
	if err != nil {
		if rollbackErr := waitForCollectorRollback(ctx, err, r.cleanupWait(cfg, cleanupFailedBuild)); rollbackErr != nil {
			r.blockNativeCleanup(rollbackErr)
			logCleanupFailure(ctx, "Hot reload stopped because collector cleanup did not finish", rollbackErr,
				slog.Uint64("reload_id", reloadID),
				slog.String("build_error", err.Error()))
			return false
		}

		slog.ErrorContext(ctx, "Failed to build new registry during hot reload",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()),
			slog.String("metrics_state", "preserving last-good registry"))
		return false
	}

	if ctx.Err() != nil {
		if err := r.retireRegistry(ctx, newRegistry, cfg, cleanupCanceledBuild); err != nil {
			logCleanupFailure(ctx, "Canceled reload candidate did not retire safely", err,
				slog.Uint64("reload_id", reloadID))
		}
		return false
	}
	oldRegistry := r.server.SwapMetricsRuntime(newRegistry, deviceWatchListMgr)
	if err := r.retireRegistry(ctx, oldRegistry, cfg, cleanupReload); err != nil {
		logCleanupFailure(ctx, "Hot reload stopped because the replaced registry did not retire", err,
			slog.Uint64("reload_id", reloadID))
		return false
	}

	if r.reloadConfig != nil && r.reloadConfig.EnableOTLP {
		r.reloadConfig = cfg.Clone()
	}
	duration := time.Since(startTime)
	slog.InfoContext(ctx, "Hot reload complete",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("reload_duration", duration))
	logTopologyInfo(reloadID, deviceWatchListMgr, duration)
	return true
}

// clearMetricsRegistry removes the published registry and waits until it no
// longer has DCGM users. GPU reset calls it before tearing down providers;
// registry-only reloads retire the old registry after a successful swap.
func (r *reloadCoordinator) clearMetricsRegistry(ctx context.Context, cfg *appconfig.Config) error {
	oldRegistry := r.server.ClearRegistry()
	return r.retireRegistry(ctx, oldRegistry, cfg, cleanupReload)
}

// doGPULifecycleReset replaces provider and registry state after a completed GPU bind or unbind.
func (r *reloadCoordinator) doGPULifecycleReset(ctx context.Context, reloadID uint64) {
	slog.InfoContext(ctx, "GPU lifecycle event detected - resetting providers",
		slog.Uint64("reload_id", reloadID))
	r.gpuRecoveryStartedAt = time.Now()
	r.cancelRecoveryRetry()
	r.gpuRecoveryStage = gpuRecoveryIdle
	r.gpuRecoveryConfig = nil
	r.gpuRecoveryRetryDelay = lifecycleRetryInitial
	r.gpuRecoveryRetries = 0

	cfg, err := r.buildReloadConfig()
	if err != nil {
		slog.ErrorContext(ctx, "Failed to build GPU lifecycle reload config",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
		return
	}

	r.dcp = counters.DCPCapabilities{}

	// Stop the watcher before releasing the DCGM resources it uses.
	watcherStopped := false

	if r.gpuWatcher != nil {
		if err := r.stopGPUWatcher(); err != nil {
			r.logGPUWatchFailure(ctx, "GPU lifecycle reset stopped because the GPU watcher did not stop", err,
				slog.Uint64("reload_id", reloadID))
			return
		}
		watcherStopped = true

		// Restore the watch after an interrupted reset, unless the exporter is
		// ending because native work may still be running.
		defer func() {
			if watcherStopped && !r.nativeCleanupBlocked.Load() {
				if err := r.startGPUWatcher(ctx); err != nil && !errors.Is(err, context.Canceled) {
					slog.ErrorContext(ctx, "Failed to restore GPU watcher after interrupted lifecycle reset",
						slog.Uint64("reload_id", reloadID),
						slog.String("error", err.Error()))
				}
			}
		}()
	}

	slog.InfoContext(ctx, "Clearing registry - metrics remain empty during GPU lifecycle reset",
		slog.Uint64("reload_id", reloadID))
	if err := r.clearMetricsRegistry(ctx, cfg); err != nil {
		logCleanupFailure(ctx, "GPU lifecycle reset stopped because registry retirement did not finish", err,
			slog.Uint64("reload_id", reloadID))
		return
	}

	// Release providers in reverse startup order, then bring DCGM back first.
	slog.InfoContext(ctx, "Cleaning up exporter NVML resources",
		slog.Uint64("reload_id", reloadID))
	r.cleanupNVML()

	slog.InfoContext(ctx, "Cleaning up DCGM resources",
		slog.Uint64("reload_id", reloadID))
	r.cleanupDCGM()

	r.gpuRecoveryConfig = cfg
	r.gpuRecoveryStage = gpuRecoveryInitializeDCGM
	watcherStopped = false
	r.continueGPULifecycleRecovery(ctx, reloadID)
}

// prepareRegistryRecovery refreshes optional NVML and DCP state after DCGM is
// ready, then advances the coordinator to registry construction.
func (r *reloadCoordinator) prepareRegistryRecovery(ctx context.Context, cfg *appconfig.Config, reloadID uint64) {
	// Match startup scope: refresh optional NVML state on every lifecycle reset.
	if err := r.initializeNVML(); err != nil {
		level := slog.LevelWarn
		if cfg.Kubernetes && !cfg.DisableStartupValidate {
			level = slog.LevelError
		}
		slog.LogAttrs(ctx, level, "Failed to reinitialize NVML",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", err.Error()))
	}

	r.queryDCPMetrics(reloadID)
	r.gpuRecoveryStage = gpuRecoveryBuildRegistry
}

// retryGPULifecycleRecovery resumes a previously scheduled lifecycle step.
// It ignores stale retries after a successful recovery or a newer reset.
func (r *reloadCoordinator) retryGPULifecycleRecovery(ctx context.Context, reloadID uint64) {
	r.cancelRecoveryRetry()
	if r.gpuRecoveryStage == gpuRecoveryIdle || r.gpuRecoveryConfig == nil {
		return
	}
	r.continueGPULifecycleRecovery(ctx, reloadID)
}

// continueGPULifecycleRecovery resumes the first unfinished GPU reset step.
// The coordinator invokes it initially and after retry events, retaining the
// metrics outage created by reset until a replacement registry is ready.
func (r *reloadCoordinator) continueGPULifecycleRecovery(ctx context.Context, reloadID uint64) {
	if ctx.Err() != nil || r.gpuRecoveryConfig == nil {
		return
	}

	if !r.initializeRecoveryDCGM(ctx, reloadID) {
		return
	}
	if !r.restartRecoveryGPUWatcher(ctx, reloadID) {
		return
	}
	if r.gpuRecoveryStage != gpuRecoveryBuildRegistry {
		return
	}

	r.buildAndPublishRecoveredRegistry(ctx, reloadID)
}

// initializeRecoveryDCGM opens a new DCGM client during GPU recovery.
func (r *reloadCoordinator) initializeRecoveryDCGM(ctx context.Context, reloadID uint64) bool {
	if r.gpuRecoveryStage != gpuRecoveryInitializeDCGM {
		return true
	}
	slog.InfoContext(ctx, "Reinitializing DCGM", slog.Uint64("reload_id", reloadID))
	if err := r.initializeDCGM(r.gpuRecoveryConfig); err != nil {
		r.scheduleRecoveryRetry(ctx, reloadID, "Failed to reinitialize DCGM after lifecycle reset", err)
		return false
	}

	if r.gpuWatcher != nil {
		r.gpuRecoveryStage = gpuRecoveryRestartWatcher
	} else {
		r.prepareRegistryRecovery(ctx, r.gpuRecoveryConfig, reloadID)
	}
	return true
}

// restartRecoveryGPUWatcher restarts GPU lifecycle watching after DCGM recovery.
func (r *reloadCoordinator) restartRecoveryGPUWatcher(ctx context.Context, reloadID uint64) bool {
	if r.gpuRecoveryStage != gpuRecoveryRestartWatcher {
		return true
	}
	if err := r.startGPUWatcher(ctx); err != nil {
		if ctx.Err() == nil && !r.nativeCleanupBlocked.Load() {
			r.scheduleRecoveryRetry(ctx, reloadID, "Failed to restart GPU watcher after lifecycle reset", err)
		} else if errors.Is(err, watcher.ErrGPUWatchTimeout) {
			r.logGPUWatchFailure(ctx, "GPU watcher did not restart after lifecycle reset", err,
				slog.Uint64("reload_id", reloadID))
		}
		return false
	}

	r.prepareRegistryRecovery(ctx, r.gpuRecoveryConfig, reloadID)
	return true
}

// buildAndPublishRecoveredRegistry builds and publishes the registry after GPU recovery.
func (r *reloadCoordinator) buildAndPublishRecoveredRegistry(ctx context.Context, reloadID uint64) {
	// Rebuild metrics only after the providers reflect the new topology. A
	// failed attempt schedules another coordinator event without blocking
	// config reloads or repeating provider teardown.
	newRegistry, deviceWatchListMgr, err := r.buildRegistry(ctx, r.gpuRecoveryConfig, r.dcp)
	if err != nil {
		if rollbackErr := waitForCollectorRollback(ctx, err, r.cleanupWait(r.gpuRecoveryConfig, cleanupFailedBuild)); rollbackErr != nil {
			r.blockNativeCleanup(rollbackErr)
			logCleanupFailure(ctx, "GPU lifecycle recovery stopped because candidate cleanup did not finish", rollbackErr,
				slog.Uint64("reload_id", reloadID))
			return
		}

		r.scheduleRecoveryRetry(ctx, reloadID, "Failed to build registry after GPU lifecycle reset", err)
		return
	}

	if ctx.Err() != nil {
		if err := r.retireRegistry(ctx, newRegistry, r.gpuRecoveryConfig, cleanupCanceledBuild); err != nil {
			logCleanupFailure(ctx, "Discarded registry did not retire safely", err,
				slog.Uint64("reload_id", reloadID))
		}
		return
	}

	oldRegistry := r.server.SwapMetricsRuntime(newRegistry, deviceWatchListMgr)
	if err := r.retireRegistry(ctx, oldRegistry, r.gpuRecoveryConfig, cleanupReload); err != nil {
		logCleanupFailure(ctx, "Recovered registry replacement did not retire safely", err,
			slog.Uint64("reload_id", reloadID))
		return
	}

	duration := time.Since(r.gpuRecoveryStartedAt)
	slog.InfoContext(ctx, "GPU metrics resumed after lifecycle reset",
		slog.Uint64("reload_id", reloadID),
		slog.Duration("reload_duration", duration))
	logTopologyInfo(reloadID, deviceWatchListMgr, duration)
	r.finishGPURecovery()
}

// scheduleRecoveryRetry records a delayed retry for the first unfinished GPU
// lifecycle step. It replaces any older retry so work stays bounded.
func (r *reloadCoordinator) scheduleRecoveryRetry(
	ctx context.Context,
	reloadID uint64,
	message string,
	err error,
) {
	if r.gpuRecoveryRetries >= maxGPURecoveryRetries {
		recoveryErr := fmt.Errorf("GPU lifecycle recovery failed after %d retries: %w", r.gpuRecoveryRetries, err)
		r.cancelRecoveryRetry()
		r.nativeCleanupBlocked.Store(true)
		if r.server != nil {
			r.server.ReportTerminalError(recoveryErr)
		}
		slog.ErrorContext(ctx, "GPU lifecycle recovery stopped after retry limit",
			slog.Uint64("reload_id", reloadID),
			slog.String("error", recoveryErr.Error()),
			slog.Int("retry_limit", maxGPURecoveryRetries),
			slog.String("metrics_state", "terminal error"))
		return
	}
	delay := r.gpuRecoveryRetryDelay
	slog.ErrorContext(ctx, message,
		slog.Uint64("reload_id", reloadID),
		slog.String("error", err.Error()),
		slog.Duration("retry_delay", delay),
		slog.String("metrics_state", "recovery pending"))
	r.cancelRecoveryRetry()
	r.cancelGPURecoveryRetry = r.scheduleRetry(ctx, delay, func() {
		r.Trigger(evGPURecoveryRetry)
	})
	r.gpuRecoveryRetries++
	r.gpuRecoveryRetryDelay = nextLifecycleRetryDelay(delay)
}

// cancelRecoveryRetry stops the pending GPU lifecycle retry, if one exists.
// Reset, completion, and shutdown paths call it before changing lifecycle state.
func (r *reloadCoordinator) cancelRecoveryRetry() {
	if r.cancelGPURecoveryRetry != nil {
		r.cancelGPURecoveryRetry()
		r.cancelGPURecoveryRetry = nil
	}
}

// finishGPURecovery clears recovery state after a replacement registry is live.
// It also tells the server that normal metric serving has resumed.
func (r *reloadCoordinator) finishGPURecovery() {
	r.cancelRecoveryRetry()
	r.gpuRecoveryStage = gpuRecoveryIdle
	r.gpuRecoveryConfig = nil
	r.gpuRecoveryRetryDelay = lifecycleRetryInitial
	r.gpuRecoveryRetries = 0
	r.server.SetReloadInProgress(false)
	if r.reloadYAMLAfterGPURecovery {
		r.reloadYAMLAfterGPURecovery = false
		r.Trigger(evConfigChanged)
	}
}

// queryDCPMetrics refreshes profiling support at startup and after GPU recovery.
// CSV reloads reuse the snapshot; errors or panics disable profiling support.
func (r *reloadCoordinator) queryDCPMetrics(reloadID uint64) {
	slog.Debug("Querying DCGM profiling metric groups", slog.Uint64("reload_id", reloadID))

	defer func() {
		if p := recover(); p != nil {
			slog.Warn("Profiling API panic - DCP metrics disabled",
				slog.Uint64("reload_id", reloadID),
				slog.String("panic", fmt.Sprintf("%v", p)))
			r.dcp = counters.DCPCapabilities{}
		}
	}()

	client := dcgmprovider.Client()
	gpuID := uint(0)
	if client.GetGPUStatus(gpuID) == dcgm.EntityStatusDetached {
		gpuIDs, err := client.GetSupportedDevices()
		if err != nil {
			r.dcp = counters.DCPCapabilities{}
			slog.Info("Not collecting DCP metrics: cannot discover supported GPUs", "error", err)
			return
		}
		if len(gpuIDs) == 0 {
			r.dcp = counters.DCPCapabilities{}
			slog.Info("Not collecting DCP metrics: no attached supported GPUs")
			return
		}
		gpuID = gpuIDs[0]
	}

	groups, err := client.GetSupportedMetricGroups(gpuID)
	if err != nil {
		r.dcp = counters.DCPCapabilities{}
		slog.Info("Not collecting DCP metrics: " + err.Error())
		return
	}

	gpuModel := "unknown"
	if gpuCount, err := client.GetAllDeviceCount(); err == nil && gpuCount > 0 {
		if gpuInfo, err := client.GetDeviceInfo(gpuID); err == nil {
			gpuModel = gpuInfo.Identifiers.Model
		}
	}

	slog.Info("Successfully queried DCGM profiling metric groups",
		slog.Uint64("reload_id", reloadID),
		slog.Int("count", len(groups)),
		slog.String("gpu_model", gpuModel))

	r.dcp = counters.NewDCPCapabilities(groups)
}

// runWatcher starts a file watcher in a goroutine and manages its lifecycle.
func runWatcher(ctx context.Context, w watcher.Watcher, onChange func(), wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := w.Watch(ctx, onChange)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("Watcher failed", slog.String("error", err.Error()))
		}
	}()
}

// stopGPUWatcherOnDone stops the GPU watcher, if there is one, when ctx is
// canceled. Shutdown waits on wg so the watch ends before DCGM cleanup.
func (r *reloadCoordinator) stopGPUWatcherOnDone(ctx context.Context, wg *sync.WaitGroup) {
	if r.gpuWatcher == nil {
		return
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		if err := r.stopGPUWatcher(); err != nil {
			r.logGPUWatchFailure(context.WithoutCancel(ctx), "GPU watcher did not stop", err)
		}
	}()
}

// reloadEventForGPUState maps completed DCGM lifecycle states to coordinator
// work. GPU watcher callbacks use the boolean to ignore intermediate states.
func reloadEventForGPUState(state dcgm.BindUnbindEventState) (reloadEvent, bool) {
	switch state {
	case dcgm.DcgmBUEventStateSystemReinitializationCompleted:
		return evGPUReinitialized, true
	default:
		return evNone, false
	}
}

// buildConfigReload rereads a configured YAML file when OTLP was enabled at
// startup; otherwise it copies the saved config. Changes to output selection,
// metric file paths, exporter metrics, scrape limits, HPC job mapping, debug
// dumps, the DCGM cleanup timeout, or GPU watching require a restart.
func (r *reloadCoordinator) buildConfigReload() (*appconfig.Config, error) {
	if r.reloadConfig == nil || !r.reloadConfig.EnableOTLP || r.reloadConfig.ConfigFile == "" {
		return r.buildReloadConfig()
	}
	if r.readConfig == nil {
		return nil, errors.New("buildConfigReload: no YAML reader configured")
	}
	cfg, err := r.readConfig()
	if err != nil {
		return nil, err
	}
	if cfg.DisablePrometheus != r.reloadConfig.DisablePrometheus || cfg.EnableOTLP != r.reloadConfig.EnableOTLP {
		return nil, fmt.Errorf("changing output selection requires exporter restart")
	}
	oldMetricFile, oldWatched := r.reloadConfig.MetricFileWatcherPath()
	newMetricFile, newWatched := cfg.MetricFileWatcherPath()
	if oldWatched != newWatched || oldMetricFile != newMetricFile {
		return nil, fmt.Errorf("changing metrics.file source requires exporter restart")
	}
	if cfg.EnableExporterMetrics != r.reloadConfig.EnableExporterMetrics {
		return nil, fmt.Errorf("changing metrics.enableExporterMetrics requires exporter restart")
	}
	if cfg.MaxConcurrentScrapes != r.reloadConfig.MaxConcurrentScrapes {
		return nil, fmt.Errorf("changing server.maxConcurrentScrapes requires exporter restart")
	}
	if hpcJobMappingChanged(cfg, r.reloadConfig) {
		return nil, fmt.Errorf("changing sources.hpc_jobs requires exporter restart")
	}
	if cfg.DumpConfig != r.reloadConfig.DumpConfig {
		return nil, fmt.Errorf("changing debug_dump requires exporter restart")
	}
	if cfg.DCGMCleanupTimeout != r.reloadConfig.DCGMCleanupTimeout {
		return nil, fmt.Errorf("changing sources.dcgm.cleanup_timeout requires exporter restart")
	}
	if cfg.EnableGPUBindUnbindWatch != r.reloadConfig.EnableGPUBindUnbindWatch ||
		cfg.GPUBindUnbindPollInterval != r.reloadConfig.GPUBindUnbindPollInterval {
		return nil, fmt.Errorf("changing sources.dcgm.detectBindUnbind requires exporter restart")
	}
	cfg.ServiceInstanceID = r.reloadConfig.ServiceInstanceID
	return cfg, nil
}

// hpcJobMappingChanged says the HPC mapping directory or one of its file
// limits differs. The mapper keeps the values it started with.
func hpcJobMappingChanged(next, current *appconfig.Config) bool {
	return next.HPCJobMappingDir != current.HPCJobMappingDir ||
		next.HPCJobMappingMaxFileBytes != current.HPCJobMappingMaxFileBytes ||
		next.HPCJobMappingMaxJobIDBytes != current.HPCJobMappingMaxJobIDBytes ||
		next.HPCJobMappingMaxJobsPerFile != current.HPCJobMappingMaxJobsPerFile
}
