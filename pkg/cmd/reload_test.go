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
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdevicewatcher "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/server"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

// TestReloadEventForGPUState checks GPU state changes map to the right reload event.
func TestReloadEventForGPUState(t *testing.T) {
	tests := []struct {
		state dcgm.BindUnbindEventState
		want  reloadEvent
		ok    bool
	}{
		{state: dcgm.DcgmBUEventStateSystemReinitializing, want: evNone, ok: false},
		{state: dcgm.DcgmBUEventStateSystemReinitializationCompleted, want: evGPUReinitialized, ok: true},
		{state: dcgm.BindUnbindEventState(99), want: evNone, ok: false},
	}

	for _, tt := range tests {
		got, ok := reloadEventForGPUState(tt.state)
		assert.Equal(t, tt.want, got)
		assert.Equal(t, tt.ok, ok)
	}
}

// TestBuildReloadConfigRequiresStartupSnapshot checks reload needs the saved startup configuration.
func TestBuildReloadConfigRequiresStartupSnapshot(t *testing.T) {
	coord := newReloadCoordinator()

	cfg, err := coord.buildReloadConfig()

	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "no startup config snapshot")
}

// withMockDCGMClient installs a fresh MockDCGM for the duration of the test
// and restores the previous client on cleanup.
func withMockDCGMClient(t *testing.T) *mockdcgmprovider.MockDCGM {
	t.Helper()
	ctrl := gomock.NewController(t)
	mock := mockdcgmprovider.NewMockDCGM(ctrl)
	mock.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	prev := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(prev) })
	dcgmprovider.SetClient(mock)
	return mock
}

func TestQueryDCPMetricsSkipsDetachedGPU(t *testing.T) {
	for _, tt := range []struct {
		name           string
		gpuIDs         []uint
		discoveryError error
	}{
		{name: "all GPUs detached"},
		{name: "attached GPU retains its ID", gpuIDs: []uint{1}},
		{name: "discovery failure disables profiling", discoveryError: errors.New("GPU discovery unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := mockdcgmprovider.NewMockDCGM(gomock.NewController(t))
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(client)
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			client.EXPECT().GetGPUStatus(uint(0)).Return(dcgm.EntityStatusDetached)
			client.EXPECT().GetSupportedDevices().Return(tt.gpuIDs, tt.discoveryError)
			groups := sampleMetricGroups()
			if len(tt.gpuIDs) > 0 {
				client.EXPECT().GetSupportedMetricGroups(uint(1)).Return(groups, nil)
				client.EXPECT().GetAllDeviceCount().Return(uint(2), nil)
				client.EXPECT().GetDeviceInfo(uint(1)).Return(dcgm.Device{GPU: 1}, nil)
			}
			coord := newTestCoordinator(t)
			coord.dcp = counters.NewDCPCapabilities(groups)
			coord.queryDCPMetrics(0)
			require.Equal(t, len(tt.gpuIDs) > 0, coord.dcp.Supports(groups[0].FieldIds[0]))
		})
	}
}

// TestQueryDCPMetrics_PublishesSuccess proves the success path writes an
// enabled snapshot containing the queried metric groups.
func TestQueryDCPMetrics_PublishesSuccess(t *testing.T) {
	mock := withMockDCGMClient(t)
	groups := sampleMetricGroups()
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groups, nil)
	mock.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no gpus")).AnyTimes()

	coord := newTestCoordinator(t)
	coord.queryDCPMetrics(0)

	assert.True(t, coord.dcp.Supports(groups[0].FieldIds[0]), "query must publish the supported field")
}

// TestQueryDCPMetrics_PublishesDisabledOnError proves the error path
// overwrites any prior enabled snapshot with a disabled one. This is the
// property that prevents stale enabled capabilities from surviving a failed
// topology-change query into a later hot reload.
func TestQueryDCPMetrics_PublishesDisabledOnError(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling unsupported"))

	coord := newTestCoordinator(t)
	// Seed a prior enabled snapshot to ensure the error path overwrites it.
	coord.dcp = counters.NewDCPCapabilities(sampleMetricGroups())

	coord.queryDCPMetrics(0)

	assert.False(t, coord.dcp.Supports(sampleMetricGroups()[0].FieldIds[0]), "error path replaces the prior snapshot")
}

// TestQueryDCPMetrics_PublishesDisabledOnPanic checks that a profiling query
// panic clears previously supported fields and returns without panicking.
// Later reloads must not reuse capabilities from the old GPU topology.
func TestQueryDCPMetrics_PublishesDisabledOnPanic(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).DoAndReturn(
		func(uint) ([]dcgm.MetricGroup, error) { panic("profiling API segfault") },
	)

	coord := newTestCoordinator(t)
	coord.dcp = counters.NewDCPCapabilities(sampleMetricGroups())

	require.NotPanics(t, func() { coord.queryDCPMetrics(0) },
		"queryDCPMetrics must recover from profiling API panics")

	assert.False(t, coord.dcp.Supports(sampleMetricGroups()[0].FieldIds[0]), "panic path replaces the prior snapshot")
}

// ---- Reload coordinator tests ----

// runCoordinator starts a coordinator for a test and guarantees it stops when
// the test ends. Reload tests use it to exercise the asynchronous event loop.
func runCoordinator(t *testing.T, coord *reloadCoordinator) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coord.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("coordinator did not exit after context cancellation")
		}
	})
}

// cleanupTrackingCollector records registry cleanup calls made during a test.
// registryWithCleanup wraps it in a registry to expose that lifecycle event.
type cleanupTrackingCollector struct {
	cleanup func()
}

// trackingGPUWatcherLifecycle records lifecycle calls made by the coordinator.
type trackingGPUWatcherLifecycle struct {
	start func()
	stop  func()
}

// Start records a watcher start when the test configured one.
func (w trackingGPUWatcherLifecycle) Start(context.Context) error {
	if w.start != nil {
		w.start()
	}
	return nil
}

// Stop records a watcher stop when the test configured one.
func (w trackingGPUWatcherLifecycle) Stop() error {
	if w.stop != nil {
		w.stop()
	}
	return nil
}

// scriptedGPUWatcherLifecycle returns configured start failures in order and
// stopErr from every Stop. Recovery tests use it to verify that retries resume
// only unfinished steps.
type scriptedGPUWatcherLifecycle struct {
	startErrors []error
	stopErr     error
	starts      int
	stops       int
}

// Start records an attempt and returns the next configured failure, if any.
// The reload coordinator invokes it while rebuilding the GPU watcher.
func (w *scriptedGPUWatcherLifecycle) Start(context.Context) error {
	w.starts++
	if w.starts <= len(w.startErrors) {
		return w.startErrors[w.starts-1]
	}
	return nil
}

// Stop records a lifecycle stop request from the coordinator and returns the
// configured stop error. Recovery tests assert the resulting stop count.
func (w *scriptedGPUWatcherLifecycle) Stop() error {
	w.stops++
	return w.stopErr
}

// GetMetrics returns an empty sample because cleanupTrackingCollector exists
// only to observe registry cleanup in lifecycle tests.
func (*cleanupTrackingCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return collector.MetricsByCounter{}, nil
}

// Cleanup runs the callback supplied by registryWithCleanup.
// Tests use the callback to observe when a registry has retired.
func (c *cleanupTrackingCollector) Cleanup() {
	c.cleanup()
}

// registryWithCleanup creates a registry whose only collector reports cleanup.
// Reload tests use it to prove a replaced registry retires at the right time.
func registryWithCleanup(t *testing.T, cleanup func()) *registry.Registry {
	t.Helper()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(&cleanupTrackingCollector{cleanup: cleanup})
	r := registry.NewRegistry()
	require.NoError(t, r.Register(tuple))
	return r
}

// TestReloadCoordinator_PendingReloadPreservesLatestGPUState checks queued reloads use the newest GPU state.
func TestReloadCoordinator_PendingReloadPreservesLatestGPUState(t *testing.T) {
	coord := newTestCoordinator(t)

	coord.Trigger(evConfigChanged)
	coord.Trigger(evGPUReinitialized)

	pending := coord.takePending()
	assert.True(t, pending.configChanged)
	assert.Equal(t, evGPUReinitialized, pending.latestGPUEvent)
	assert.True(t, coord.takePending().empty())
}

// TestReloadCoordinator_CoalescesBurstOfConfigChanged checks repeated config events become one pending reload.
func TestReloadCoordinator_CoalescesBurstOfConfigChanged(t *testing.T) {
	coord := newTestCoordinator(t)
	var calls atomic.Int32
	applied := make(chan struct{})
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		if calls.Add(1) == 1 {
			close(applied)
		}
	}

	for range 50 {
		coord.Trigger(evConfigChanged)
	}
	runCoordinator(t, coord)

	select {
	case <-applied:
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced config event was not delivered")
	}
	assert.Equal(t, int32(1), calls.Load())
}

// TestReloadCoordinator_DRAResourceSliceChangeUsesRegistryReload checks DRA changes rebuild the registry without restarting DCGM.
func TestReloadCoordinator_DRAResourceSliceChangeUsesRegistryReload(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)

	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		builds.Add(1)
		return registry.NewRegistry(), topologyManager(), nil
	}
	coord.cleanupDCGM = func() { t.Fatal("DRA topology reload must not reset DCGM") }
	coord.cleanupNVML = func() { t.Fatal("DRA topology reload must not reset NVML") }

	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		t.Fatal("a coalesced DRA event must use the outcome-aware registry reload path")
	}
	coord.Trigger(evConfigChanged)
	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(1), builds.Load())
	assert.NotSame(t, initialRegistry, coord.server.GetRegistry())
}

// TestReloadCoordinator_DRAResourceSliceChangeRetriesFailedRegistryBuild checks failed DRA registry builds are retried.
func TestReloadCoordinator_DRAResourceSliceChangeRetriesFailedRegistryBuild(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	refreshedRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			return nil, nil, errors.New("registry build failed")
		}
		return refreshedRegistry, topologyManager(), nil
	}
	coord.cleanupDCGM = func() { t.Fatal("DRA topology reload must not reset DCGM") }
	coord.cleanupNVML = func() { t.Fatal("DRA topology reload must not reset NVML") }

	coord.Trigger(evConfigChanged)
	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Same(t, initialRegistry, coord.server.GetRegistry())
	pending := coord.takePending()
	assert.Equal(t, evDRAResourceSliceRetry, pending.draResourceSliceEvent)

	coord.handlePending(context.Background(), pending)

	assert.Equal(t, int32(2), builds.Load(), "the failed DRA reload must be replayed once")
	assert.Same(t, refreshedRegistry, coord.server.GetRegistry())
	assert.True(t, coord.takePending().empty(), "a successful retry must not leave more DRA work pending")
}

// TestReloadCoordinator_DRAResourceSliceChangeRetryIsBounded checks DRA registry retries stop at their limit.
func TestReloadCoordinator_DRAResourceSliceChangeRetryIsBounded(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		builds.Add(1)
		return nil, nil, errors.New("registry build failed")
	}

	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())
	pending := coord.takePending()
	require.Equal(t, evDRAResourceSliceRetry, pending.draResourceSliceEvent)

	coord.handlePending(context.Background(), pending)

	assert.Equal(t, int32(2), builds.Load(), "one notification permits one retry")
	assert.Same(t, initialRegistry, coord.server.GetRegistry(), "both failed builds must preserve the last-good registry")
	assert.True(t, coord.takePending().empty(), "a failed retry must not schedule another retry")
}

// TestReloadCoordinator_RollbackCleanupPanicBlocksDRARetry proves an unsafe
// candidate rollback preserves the serving registry and rejects the retry.
func TestReloadCoordinator_RollbackCleanupPanicBlocksDRARetry(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)

	registrationErr := errors.New("collector registration failed")
	cleanupPanic := errors.New("candidate cleanup panicked")
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		builds.Add(1)
		return nil, nil, collectorRegistrationError{
			err:        registrationErr,
			cleanupErr: cleanupPanic,
		}
	}

	var providerCleanups atomic.Int32
	coord.cleanupDCGM = func() { providerCleanups.Add(1) }
	coord.cleanupNVML = func() { providerCleanups.Add(1) }
	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(1), builds.Load())
	assert.True(t, coord.nativeCleanupBlocked.Load())
	assert.ErrorIs(t, coord.server.TerminalError(), cleanupPanic)
	assert.Same(t, initialRegistry, coord.server.GetRegistry())
	coord.handle(context.Background(), evGPUReinitialized)
	assert.Equal(t, int32(1), builds.Load(), "terminal safe mode must reject a stale lifecycle event")
	assert.Zero(t, providerCleanups.Load())
	coord.Trigger(evDRAResourceSliceRetry)
	assert.True(t, coord.takePending().empty(), "terminal safe mode must reject a DRA retry")
}

// TestReloadCoordinator_DRAResourceSliceChangeRetriesAfterPanic checks a DRA build panic enters the retry path.
func TestReloadCoordinator_DRAResourceSliceChangeRetriesAfterPanic(t *testing.T) {
	coord := newTestCoordinator(t)
	initialRegistry := registry.NewRegistry()
	refreshedRegistry := registry.NewRegistry()
	coord.server.SetRegistry(initialRegistry)
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			panic("synthetic DRA registry build panic")
		}
		return refreshedRegistry, topologyManager(), nil
	}

	coord.Trigger(evDRAResourceSliceChanged)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Same(t, initialRegistry, coord.server.GetRegistry())
	pending := coord.takePending()
	require.Equal(t, evDRAResourceSliceRetry, pending.draResourceSliceEvent)

	coord.handlePending(context.Background(), pending)

	assert.Equal(t, int32(2), builds.Load())
	assert.Same(t, refreshedRegistry, coord.server.GetRegistry())
	assert.False(t, coord.server.IsReloadInProgress())
	assert.True(t, coord.takePending().empty(), "a successful panic retry must not leave more DRA work pending")
}

// TestReloadCoordinator_CoalescedLifecycleResetsProvidersOnce checks the full reset order.
func TestReloadCoordinator_CoalescedLifecycleResetsProvidersOnce(t *testing.T) {
	mock := withMockDCGMClient(t)
	var order []string
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		DoAndReturn(func(uint) ([]dcgm.MetricGroup, error) {
			order = append(order, "dcp")
			return nil, errors.New("profiling unavailable")
		})

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registryWithCleanup(t, func() {
		order = append(order, "registry-cleanup")
	}))
	coord.gpuWatcher = trackingGPUWatcherLifecycle{
		stop:  func() { order = append(order, "watcher-stop") },
		start: func() { order = append(order, "watcher-start") },
	}
	coord.cleanupNVML = func() { order = append(order, "nvml-cleanup") }
	coord.cleanupDCGM = func() { order = append(order, "dcgm-cleanup") }
	coord.initializeDCGM = func(*appconfig.Config) error {
		order = append(order, "dcgm-init")
		return nil
	}
	coord.initializeNVML = func() error {
		order = append(order, "nvml-init")
		return nil
	}
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		order = append(order, "build")
		return newRegistry, topologyManager(), nil
	}
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		t.Fatal("lifecycle reset must consume a coalesced config reload")
	}

	coord.Trigger(evConfigChanged)
	coord.Trigger(evDRAResourceSliceChanged)
	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, []string{
		"watcher-stop",
		"registry-cleanup",
		"nvml-cleanup",
		"dcgm-cleanup",
		"dcgm-init",
		"watcher-start",
		"nvml-init",
		"dcp",
		"build",
	}, order)
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

// TestReloadCoordinator_RetriesFailedRegistryBuildWithoutAnotherEvent checks self-recovery.
func TestReloadCoordinator_RetriesFailedRegistryBuildWithoutAnotherEvent(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	var retry func()
	var retryDelays []time.Duration
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		retryDelays = append(retryDelays, delay)
		retry = callback
		return func() {}
	}
	coord.server.SetRegistry(registry.NewRegistry())
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	var cleanups atomic.Int32
	coord.cleanupNVML = func() { cleanups.Add(1) }
	coord.cleanupDCGM = func() { cleanups.Add(1) }
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) <= 2 {
			return nil, nil, errors.New("registry build failed")
		}
		return newRegistry, topologyManager(), nil
	}

	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(1), builds.Load())
	assert.Equal(t, gpuRecoveryBuildRegistry, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())
	require.NotNil(t, retry)
	retry()
	coord.handlePending(context.Background(), coord.takePending())
	assert.Equal(t, int32(2), builds.Load())
	require.NotNil(t, retry)
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(3), builds.Load())
	assert.Equal(t, int32(2), cleanups.Load(), "a registry retry must not repeat provider teardown")
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, retryDelays)
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

// TestReloadCoordinator_RetriesRegistryBuildAfterPanic checks a registry build panic is retried during recovery.
func TestReloadCoordinator_RetriesRegistryBuildAfterPanic(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	var retry func()
	var retryDelays []time.Duration
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		retryDelays = append(retryDelays, delay)
		retry = callback
		return func() {}
	}
	var providerCleanups atomic.Int32
	coord.cleanupNVML = func() { providerCleanups.Add(1) }
	coord.cleanupDCGM = func() { providerCleanups.Add(1) }
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			panic("synthetic registry build panic")
		}
		return newRegistry, topologyManager(), nil
	}

	coord.handle(context.Background(), evGPUReinitialized)

	assert.Equal(t, gpuRecoveryBuildRegistry, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())
	require.NotNil(t, retry, "a recovered panic must leave one retry pending")
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(2), builds.Load())
	assert.Equal(t, int32(2), providerCleanups.Load(), "a panic retry must not repeat provider teardown")
	assert.Equal(t, []time.Duration{time.Second}, retryDelays)
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

// TestReloadCoordinator_RetriesWatcherStartAfterPanic checks a watcher startup panic is retried during recovery.
func TestReloadCoordinator_RetriesWatcherStartAfterPanic(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	var retry func()
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		assert.Equal(t, time.Second, delay)
		retry = callback
		return func() {}
	}
	var watcherStarts atomic.Int32
	coord.gpuWatcher = trackingGPUWatcherLifecycle{
		start: func() {
			if watcherStarts.Add(1) == 1 {
				panic("synthetic watcher start panic")
			}
		},
	}
	var providerCleanups atomic.Int32
	coord.cleanupNVML = func() { providerCleanups.Add(1) }
	coord.cleanupDCGM = func() { providerCleanups.Add(1) }
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return newRegistry, topologyManager(), nil
	}

	coord.handle(context.Background(), evGPUReinitialized)

	assert.Equal(t, gpuRecoveryRestartWatcher, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())
	require.NotNil(t, retry, "a watcher panic must leave one retry pending")
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(2), watcherStarts.Load())
	assert.Equal(t, int32(2), providerCleanups.Load(), "a watcher panic retry must not repeat provider teardown")
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

// TestReloadCoordinator_RetriesGPUWatcherRegistrationAfterProviderReset checks GPU watching resumes after DCGM is reset.
func TestReloadCoordinator_RetriesGPUWatcherRegistrationAfterProviderReset(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	gpuWatcher := &scriptedGPUWatcherLifecycle{startErrors: []error{context.DeadlineExceeded}}
	coord.gpuWatcher = gpuWatcher
	var providerCleanups atomic.Int32
	coord.cleanupNVML = func() { providerCleanups.Add(1) }
	coord.cleanupDCGM = func() { providerCleanups.Add(1) }
	var retry func()
	var retryDelays []time.Duration
	coord.scheduleRetry = func(_ context.Context, delay time.Duration, callback func()) context.CancelFunc {
		retryDelays = append(retryDelays, delay)
		retry = callback
		return func() {}
	}
	newRegistry := registry.NewRegistry()
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		return newRegistry, topologyManager(), nil
	}

	coord.doGPULifecycleReset(context.Background(), 1)

	assert.Equal(t, 1, gpuWatcher.stops)
	assert.Equal(t, 1, gpuWatcher.starts)
	assert.Equal(t, gpuRecoveryRestartWatcher, coord.gpuRecoveryStage)
	require.NotNil(t, retry)
	retry()
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, 2, gpuWatcher.starts)
	assert.Equal(t, int32(2), providerCleanups.Load(), "a watcher retry must not repeat provider teardown")
	assert.Equal(t, []time.Duration{time.Second}, retryDelays)
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

// TestReloadCoordinator_NewLifecycleSupersedesQueuedRecoveryRetry checks a newer GPU event replaces a queued recovery retry.
func TestReloadCoordinator_NewLifecycleSupersedesQueuedRecoveryRetry(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable")).
		Times(2)

	coord := newTestCoordinator(t)
	var retry func()
	var retryCanceled atomic.Int32
	coord.scheduleRetry = func(_ context.Context, _ time.Duration, callback func()) context.CancelFunc {
		retry = callback
		return func() { retryCanceled.Add(1) }
	}
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			return nil, nil, errors.New("registry build failed")
		}
		return newRegistry, topologyManager(), nil
	}

	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())
	require.NotNil(t, retry)
	retry()
	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())

	assert.Equal(t, int32(2), builds.Load())
	assert.Positive(t, retryCanceled.Load())
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.Same(t, newRegistry, coord.server.GetRegistry())
}

// TestNextLifecycleRetryDelayCapsAtThirtySeconds checks recovery backoff stops growing after thirty seconds.
func TestNextLifecycleRetryDelayCapsAtThirtySeconds(t *testing.T) {
	assert.Equal(t, 2*time.Second, nextLifecycleRetryDelay(time.Second))
	assert.Equal(t, 30*time.Second, nextLifecycleRetryDelay(16*time.Second))
	assert.Equal(t, 30*time.Second, nextLifecycleRetryDelay(30*time.Second))
}

// TestReloadCoordinatorStopsAfterGPULifecycleRetryLimit ensures a failed
// reset cannot leave the published registry empty through unlimited retries.
func TestReloadCoordinatorStopsAfterGPULifecycleRetryLimit(t *testing.T) {
	coord := newTestCoordinator(t)
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	var scheduled int
	coord.scheduleRetry = func(context.Context, time.Duration, func()) context.CancelFunc {
		scheduled++
		return func() {}
	}

	for range maxGPURecoveryRetries {
		coord.scheduleRecoveryRetry(context.Background(), 1, "recovery failed", errors.New("DCGM unavailable"))
	}

	assert.Equal(t, maxGPURecoveryRetries, scheduled)
	assert.False(t, coord.nativeCleanupBlocked.Load())

	coord.scheduleRecoveryRetry(context.Background(), 1, "recovery failed", errors.New("DCGM unavailable"))

	assert.Equal(t, maxGPURecoveryRetries, scheduled)
	assert.True(t, coord.nativeCleanupBlocked.Load())
	assert.ErrorContains(t, coord.server.TerminalError(), "after 6 retries")
	assert.NotContains(t, coord.server.TerminalError().Error(), "registry retirement")
	assert.Contains(t, logs.String(), `"metrics_state":"recovery pending"`)
	assert.Contains(t, logs.String(), `"metrics_state":"terminal error"`)
	assert.Contains(t, logs.String(), `"retry_limit":6`)
}

// TestReloadCoordinator_CleanupPanicBlocksNativeTeardown verifies that a
// collector cleanup panic ends lifecycle work instead of resetting DCGM below
// an unknown native-resource state.
func TestReloadCoordinator_CleanupPanicBlocksNativeTeardown(t *testing.T) {
	coord := newTestCoordinator(t)
	coord.reloadConfig.Kubernetes = true
	coord.server.SetRegistry(registryWithCleanup(t, func() {
		panic("synthetic collector cleanup failure")
	}))

	var nvmlCleanups atomic.Int32
	var watcherStarts atomic.Int32
	var watcherStops atomic.Int32
	coord.gpuWatcher = trackingGPUWatcherLifecycle{
		start: func() { watcherStarts.Add(1) },
		stop:  func() { watcherStops.Add(1) },
	}
	coord.cleanupNVML = func() { nvmlCleanups.Add(1) }
	var dcgmCleanups atomic.Int32
	coord.cleanupDCGM = func() { dcgmCleanups.Add(1) }

	coord.Trigger(evGPUReinitialized)
	coord.handlePending(context.Background(), coord.takePending())
	coord.Trigger(evGPUReinitialized)

	assert.True(t, coord.nativeCleanupBlocked.Load())
	assert.True(t, coord.takePending().empty(), "terminal safe mode must reject later lifecycle resets")
	assert.Equal(t, int32(0), nvmlCleanups.Load())
	assert.Equal(t, int32(0), dcgmCleanups.Load())
	assert.Equal(t, int32(0), watcherStarts.Load(), "terminal safe mode must not restart the GPU watcher")
	assert.Equal(t, int32(1), watcherStops.Load())
}

// TestDoGPULifecycleReset_ProviderOrder checks provider order and NVML failure policy.
func TestDoGPULifecycleReset_ProviderOrder(t *testing.T) {
	wantOrder := []string{
		"stale-cleanup",
		"nvml-cleanup",
		"dcgm-cleanup",
		"dcgm-init",
		"nvml",
		"dcp",
		"build",
	}
	tests := []struct {
		name                   string
		kubernetes             bool
		virtualGPUs            bool
		disableStartupValidate bool
		useFakeGPUs            bool
		initializeError        error
		wantFailureLogLevel    string
	}{
		{
			name: "non-Kubernetes real GPUs",
		},
		{
			name:                "Kubernetes validated failure logs error and still rebuilds",
			kubernetes:          true,
			initializeError:     errors.New("NVML unavailable"),
			wantFailureLogLevel: "ERROR",
		},
		{
			name:                "non-Kubernetes failure warns and still rebuilds",
			initializeError:     errors.New("NVML unavailable"),
			wantFailureLogLevel: "WARN",
		},
		{
			name:                   "Kubernetes validation-disabled failure warns and still rebuilds",
			kubernetes:             true,
			disableStartupValidate: true,
			initializeError:        errors.New("NVML unavailable"),
			wantFailureLogLevel:    "WARN",
		},
		{
			name:        "Kubernetes virtual GPUs use the same provider lifecycle",
			kubernetes:  true,
			virtualGPUs: true,
		},
		{
			name:        "fake-GPU mode uses the same provider lifecycle",
			useFakeGPUs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			var order []string

			mock := withMockDCGMClient(t)
			mock.EXPECT().
				GetSupportedMetricGroups(uint(0)).
				DoAndReturn(func(uint) ([]dcgm.MetricGroup, error) {
					order = append(order, "dcp")
					return nil, errors.New("profiling unavailable")
				})

			coord := newTestCoordinator(t)
			coord.reloadConfig.Kubernetes = tt.kubernetes
			coord.reloadConfig.KubernetesVirtualGPUs = tt.virtualGPUs
			coord.reloadConfig.DisableStartupValidate = tt.disableStartupValidate
			coord.reloadConfig.UseFakeGPUs = tt.useFakeGPUs
			coord.cleanupNVML = func() { order = append(order, "nvml-cleanup") }
			coord.cleanupDCGM = func() { order = append(order, "dcgm-cleanup") }
			coord.initializeDCGM = func(*appconfig.Config) error {
				order = append(order, "dcgm-init")
				return nil
			}
			coord.initializeNVML = func() error {
				order = append(order, "nvml")
				return tt.initializeError
			}
			coord.server.SetRegistry(registryWithCleanup(t, func() {
				order = append(order, "stale-cleanup")
			}))
			newRegistry := registry.NewRegistry()
			coord.buildRegistry = func(
				context.Context,
				*appconfig.Config,
				counters.DCPCapabilities,
			) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				order = append(order, "build")
				return newRegistry, topologyManager(), nil
			}

			coord.handle(context.Background(), evGPUReinitialized)

			assert.Equal(t, wantOrder, order)
			if tt.wantFailureLogLevel != "" {
				assert.Contains(t, logs.String(),
					"level="+tt.wantFailureLogLevel+" msg=\"Failed to reinitialize NVML\"")
			}
			assert.Same(t, newRegistry, coord.server.GetRegistry())
		})
	}
}

// TestDoGPULifecycleReset_BuildFailureUsesLatestConfigOnRetry checks that a
// queued recovery retry publishes the latest file config only once.
func TestDoGPULifecycleReset_BuildFailureUsesLatestConfigOnRetry(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable"))

	coord := newTestCoordinator(t)
	var retry func()
	coord.scheduleRetry = func(_ context.Context, _ time.Duration, callback func()) context.CancelFunc {
		retry = callback
		return func() {}
	}
	newRegistry := registry.NewRegistry()
	var builds atomic.Int32
	coord.buildRegistry = func(
		_ context.Context,
		cfg *appconfig.Config,
		_ counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			return nil, nil, errors.New("registry build failed")
		}
		assert.Equal(t, "updated-counters.csv", cfg.MetricSource.File)
		return newRegistry, topologyManager(), nil
	}

	coord.handle(context.Background(), evGPUReinitialized)

	assert.Nil(t, coord.server.ClearRegistry())
	assert.Equal(t, gpuRecoveryBuildRegistry, coord.gpuRecoveryStage)
	assert.True(t, coord.server.IsReloadInProgress())

	coord.reloadConfig.MetricSource.File = "updated-counters.csv"
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		t.Fatal("config reload must wait for the pending recovery retry")
	}
	require.NotNil(t, retry)
	retry()
	coord.Trigger(evConfigChanged)
	coord.handlePending(context.Background(), coord.takePending())
	assert.Equal(t, int32(2), builds.Load())
	assert.Same(t, newRegistry, coord.server.GetRegistry())
	assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
	assert.False(t, coord.server.IsReloadInProgress())
}

// TestReloadCoordinator_TriggerIsConcurrentSafe checks concurrent event calls do not lose reloads.
func TestReloadCoordinator_TriggerIsConcurrentSafe(t *testing.T) {
	coord := newTestCoordinator(t)
	events := []reloadEvent{evConfigChanged, evGPUReinitialized}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func(offset int) {
				defer wg.Done()
				for j := 0; j < 1000; j++ {
					coord.Trigger(events[(offset+j)%len(events)])
				}
			}(i)
		}
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Trigger did not complete under concurrent producers")
	}

	pending := coord.takePending()
	assert.True(t, pending.configChanged)
	assert.Equal(t, evGPUReinitialized, pending.latestGPUEvent)
}

// TestReloadCoordinator_ShutsDownOnContextCancel checks canceling the context stops reload work.
func TestReloadCoordinator_ShutsDownOnContextCancel(t *testing.T) {
	coord := newTestCoordinator(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		coord.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("coordinator did not exit after context cancellation")
	}
}

// TestInitReloadCoordinatorStoresStartupConfig checks coordinator setup saves the startup config for reloads.
func TestInitReloadCoordinatorStoresStartupConfig(t *testing.T) {
	cfg := &appconfig.Config{MetricSource: appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: "counters.csv"}}
	coord := initReloadCoordinator(cfg)

	require.NotNil(t, coord.reloadConfig)
	assert.NotSame(t, cfg, coord.reloadConfig)
	assert.Equal(t, cfg.MetricSource, coord.reloadConfig.MetricSource)
	assert.False(t, coord.dcp.Supports(1001), "DCP discovery must wait until the lifecycle watch is registered")
}

// TestReloadCoordinator_RepublishedDCPReplacesPreviousSnapshot checks a new DCP snapshot replaces the old one.
func TestReloadCoordinator_RepublishedDCPReplacesPreviousSnapshot(t *testing.T) {
	mock := withMockDCGMClient(t)
	groupsA := []dcgm.MetricGroup{{Major: 0, Minor: 0, FieldIds: []uint{1001, 1002}}}
	groupsB := []dcgm.MetricGroup{{Major: 7, Minor: 1, FieldIds: []uint{1077, 1078}}}
	gomock.InOrder(
		mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groupsA, nil),
		mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(groupsB, nil),
	)
	mock.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no gpus")).AnyTimes()

	coord := newTestCoordinator(t)
	coord.queryDCPMetrics(0)
	assert.True(t, coord.dcp.Supports(groupsA[0].FieldIds[0]))

	coord.queryDCPMetrics(1)
	assert.False(t, coord.dcp.Supports(groupsA[0].FieldIds[0]))
	assert.True(t, coord.dcp.Supports(groupsB[0].FieldIds[0]))
}

// TestReloadCoordinator_DefersReloadUntilGPURecovery checks that file and DRA
// changes wait for one recovery build with fresh profiling support.
func TestReloadCoordinator_DefersReloadUntilGPURecovery(t *testing.T) {
	for _, stage := range []struct {
		name  string
		value gpuRecoveryStage
	}{
		{name: "DCGM startup", value: gpuRecoveryInitializeDCGM},
		{name: "watcher restart", value: gpuRecoveryRestartWatcher},
		{name: "registry retry", value: gpuRecoveryBuildRegistry},
	} {
		for _, event := range []struct {
			name  string
			value reloadEvent
		}{
			{name: "config change", value: evConfigChanged},
			{name: "DRA change", value: evDRAResourceSliceChanged},
			{name: "DRA retry", value: evDRAResourceSliceRetry},
		} {
			t.Run(stage.name+"/"+event.name, func(t *testing.T) {
				coord := newTestCoordinator(t)
				coord.gpuRecoveryStage = stage.value
				coord.gpuRecoveryConfig = coord.reloadConfig.Clone()
				coord.gpuRecoveryConfig.MetricSource.File = "previous.csv"
				coord.reloadConfig.MetricSource.File = "updated.csv"
				initialRegistry := registry.NewRegistry()
				coord.server.SetRegistry(initialRegistry)
				coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
					t.Fatal("config reload must wait for profiling support")
				}
				coord.buildRegistry = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (
					*registry.Registry, devicewatchlistmanager.Manager, error,
				) {
					t.Fatal("registry build must wait for profiling support")
					return nil, nil, nil
				}

				coord.handleConfigEvent(context.Background(), event.value, 1)
				assert.Same(t, initialRegistry, coord.server.GetRegistry())
				wantFile := "previous.csv"
				if event.value == evConfigChanged {
					wantFile = "updated.csv"
				}
				assert.Equal(t, wantFile, coord.gpuRecoveryConfig.MetricSource.File)

				// Recovery refreshes profiling support before publishing the saved config.
				coord.gpuRecoveryStage = gpuRecoveryBuildRegistry
				groups := sampleMetricGroups()
				coord.dcp = counters.NewDCPCapabilities(groups)
				newRegistry := registry.NewRegistry()
				coord.buildRegistry = func(
					_ context.Context, cfg *appconfig.Config, dcp counters.DCPCapabilities,
				) (*registry.Registry, devicewatchlistmanager.Manager, error) {
					assert.Equal(t, wantFile, cfg.MetricSource.File)
					assert.True(t, dcp.Supports(groups[0].FieldIds[0]))
					return newRegistry, topologyManager(), nil
				}
				coord.buildAndPublishRecoveredRegistry(context.Background(), 2)
				assert.Same(t, newRegistry, coord.server.GetRegistry())
				assert.Equal(t, gpuRecoveryIdle, coord.gpuRecoveryStage)
			})
		}
	}
}

// TestReloadCoordinator_ConfigBuildFailureDoesNotPoisonNextReload checks a failed config build leaves later reloads usable.
func TestReloadCoordinator_ConfigBuildFailureDoesNotPoisonNextReload(t *testing.T) {
	coord := newReloadCoordinator()
	coord.setServer(&server.MetricsServer{})

	var calls atomic.Int32
	coord.applyConfigReload = func(context.Context, *appconfig.Config, uint64) {
		calls.Add(1)
	}

	coord.handle(context.Background(), evConfigChanged)
	assert.False(t, coord.server.IsReloadInProgress())
	assert.Equal(t, int32(0), calls.Load())

	cfg := appconfig.DefaultConfig()
	coord.reloadConfig = cfg.Clone()
	coord.handle(context.Background(), evConfigChanged)

	assert.False(t, coord.server.IsReloadInProgress())
	assert.Equal(t, int32(1), calls.Load())
}

// TestReloadCoordinator_ProviderCleanupPanicIsTerminal checks a cleanup panic stops provider recovery.
func TestReloadCoordinator_ProviderCleanupPanicIsTerminal(t *testing.T) {
	for _, provider := range []string{"NVML", "DCGM"} {
		t.Run(provider, func(t *testing.T) {
			coord := newTestCoordinator(t)
			coord.server.SetRegistry(registry.NewRegistry())
			coord.cleanupNVML = func() {}
			coord.cleanupDCGM = func() {}
			fail := func() { panic("provider cleanup failed") }
			if provider == "NVML" {
				coord.cleanupNVML = fail
			} else {
				coord.cleanupDCGM = fail
			}
			coord.initializeDCGM = func(*appconfig.Config) error {
				t.Fatal("must not initialize providers after unsafe teardown")
				return nil
			}
			coord.scheduleRetry = func(context.Context, time.Duration, func()) context.CancelFunc {
				t.Fatal("must not retry unsafe teardown")
				return func() {}
			}
			coord.handle(context.Background(), evGPUReinitialized)
			assert.True(t, coord.nativeCleanupBlocked.Load())
			assert.ErrorContains(t, coord.server.TerminalError(), "panic during GPU lifecycle reset")
			coord.Trigger(evGPUReinitialized)
			assert.True(t, coord.takePending().empty())
		})
	}
}

// TestCanceledReloadPreservesRuntime checks cancellation retires only the candidate registry.
func TestCanceledReloadPreservesRuntime(t *testing.T) {
	coord := newTestCoordinator(t)
	coord.reloadConfig = &appconfig.Config{EnableOTLP: true, CollectInterval: 30000}
	activeConfig := coord.reloadConfig
	snapshot := activeConfig.Clone()
	var oldCleanups, candidateCleanups atomic.Int32
	active := registryWithCleanup(t, func() { oldCleanups.Add(1) })
	t.Cleanup(active.Cleanup)
	coord.server.SetRegistry(active)
	candidate := registryWithCleanup(t, func() { candidateCleanups.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coord.buildRegistry = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		cancel()
		return candidate, topologyManager(), nil
	}
	require.False(t, coord.doConfigReload(ctx, &appconfig.Config{EnableOTLP: true, CollectInterval: 5000}, 1))
	require.Same(t, active, coord.server.GetRegistry())
	require.Same(t, activeConfig, coord.reloadConfig)
	require.Equal(t, snapshot, coord.reloadConfig)
	require.Equal(t, int32(0), oldCleanups.Load())
	require.Equal(t, int32(1), candidateCleanups.Load())
}

// batchingDCGM is a mock DCGM client that also unwatches in batches, as the
// client dcgmprovider.Client returns does.
type batchingDCGM struct {
	*mockdcgmprovider.MockDCGM
	*mockdcgmprovider.MockFieldGroupUnwatcher
}

// TestConfigReloadUnwatchesInOneBatchForAnyGPUCount builds a registry on 1, 8
// and 16 GPUs and reloads it with a changed interval. At every size the
// exporter adds no UpdateAllFields call of its own, the replacement installs
// the same watches, and the retired registry's watches are unwatched in one
// batch.
func TestConfigReloadUnwatchesInOneBatchForAnyGPUCount(t *testing.T) {
	for _, gpus := range []int{1, 8, 16} {
		t.Run(fmt.Sprintf("%d GPUs", gpus), func(t *testing.T) {
			restoreStartupSeams(t)
			ctrl := gomock.NewController(t)
			mock := mockdcgmprovider.NewMockDCGM(ctrl)
			unwatcher := mockdcgmprovider.NewMockFieldGroupUnwatcher(ctrl)
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(&batchingDCGM{MockDCGM: mock, MockFieldGroupUnwatcher: unwatcher})
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			config := &appconfig.Config{NoHostname: true, GPUDeviceOptions: appconfig.DeviceOptions{Flex: true}, CollectInterval: 1000}
			cs := &counters.CounterSet{DCGMCounters: counters.CounterList{testutils.SampleGPUTempCounter}}
			getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
				return cs, nil
			}

			// Discovery finds the same GPUs for every registry build.
			mock.EXPECT().GetAllDeviceCount().Return(uint(gpus), nil).AnyTimes()
			mock.EXPECT().GetDeviceInfo(gomock.Any()).DoAndReturn(func(id uint) (dcgm.Device, error) {
				return dcgm.Device{GPU: id, UUID: fmt.Sprintf("GPU-%d", id), DCGMSupported: "Yes"}, nil
			}).AnyTimes()
			mock.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
			mock.EXPECT().GetNvLinkLinkStatus().Return(nil, nil).AnyTimes()
			mock.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil).AnyTimes()
			mock.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_GPU_TEMP).Return(testutils.SampleFieldIDToFieldMeta[dcgm.DCGM_FI_DEV_GPU_TEMP], nil).AnyTimes()

			// Watch calls record their pairs; the test counts refreshes and unwatch batches.
			next := uintptr(0)
			var watches []dcgmprovider.FieldGroupWatch
			var unwatchBatches [][]dcgmprovider.FieldGroupWatch
			refreshes := 0
			addedGPUs := 0
			groups := 0
			mock.EXPECT().CreateGroup(gomock.Any()).DoAndReturn(func(string) (dcgm.GroupHandle, error) {
				next++
				groups++
				group := dcgm.GroupHandle{}
				group.SetHandle(next)
				return group, nil
			}).AnyTimes()
			mock.EXPECT().AddEntityToGroup(gomock.Any(), dcgm.FE_GPU, gomock.Any()).DoAndReturn(func(dcgm.GroupHandle, dcgm.Field_Entity_Group, uint) error {
				addedGPUs++
				return nil
			}).AnyTimes()
			mock.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(func(string, []dcgm.Short) (dcgm.FieldHandle, error) {
				next++
				fieldGroup := dcgm.FieldHandle{}
				fieldGroup.SetHandle(next)
				return fieldGroup, nil
			}).AnyTimes()
			mock.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(fieldGroup dcgm.FieldHandle, group dcgm.GroupHandle, _ int64, _ float64, _ int32) error {
					watches = append(watches, dcgmprovider.FieldGroupWatch{FieldGroup: fieldGroup, Group: group})
					return nil
				}).AnyTimes()
			unwatcher.EXPECT().UnwatchFieldGroups(gomock.Any()).DoAndReturn(func(closed []dcgmprovider.FieldGroupWatch) error {
				unwatchBatches = append(unwatchBatches, closed)
				return nil
			}).AnyTimes()
			mock.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
			mock.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
			mock.EXPECT().UpdateAllFields().DoAndReturn(func() error {
				refreshes++
				return nil
			}).AnyTimes()

			old, oldTopology, err := buildRegistry(context.Background(), config, counters.DCPCapabilities{})
			require.NoError(t, err)
			oldWatches := slices.Clone(watches)
			require.NotEmpty(t, oldWatches)
			require.Equal(t, 1, groups, "one group holds every GPU")
			require.Equal(t, gpus, addedGPUs, "every GPU is added to the shared group once")

			coord := newTestCoordinator(t)
			coord.buildRegistry = buildRegistry
			coord.reloadConfig = config.Clone()
			coord.server.SwapMetricsRuntime(old, oldTopology)

			candidate := config.Clone()
			candidate.CollectInterval = 5000
			require.True(t, coord.doConfigReload(context.Background(), candidate, 1))
			require.NotSame(t, old, coord.server.GetRegistry())
			t.Cleanup(coord.server.GetRegistry().Cleanup)

			assert.Zero(t, refreshes, "the exporter adds no UpdateAllFields call of its own")
			assert.Len(t, watches, 2*len(oldWatches), "the replacement installs the same watches")
			assert.Equal(t, [][]dcgmprovider.FieldGroupWatch{oldWatches}, unwatchBatches,
				"the retired registry unwatches its watches in one batch")
		})
	}
}

// TestConfigReloadFailureKeepsWatch exercises discovery and watch-installation failures through the real registry path.
func TestConfigReloadFailureKeepsWatch(t *testing.T) {
	for _, tt := range []struct {
		name         string
		watchFailure bool
	}{
		{name: "discovery"},
		{name: "later watch scope", watchFailure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			restoreStartupSeams(t)
			mock := mockdcgmprovider.NewMockDCGM(gomock.NewController(t))
			mock.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(mock)
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			config := &appconfig.Config{EnableOTLP: true, NoHostname: true, GPUDeviceOptions: appconfig.DeviceOptions{Flex: true}, CollectInterval: 1000}
			cs := &counters.CounterSet{DCGMCounters: counters.CounterList{testutils.SampleGPUTempCounter}}
			getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
				return cs, nil
			}
			mock.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_GPU_TEMP).Return(testutils.SampleFieldIDToFieldMeta[dcgm.DCGM_FI_DEV_GPU_TEMP], nil).AnyTimes()
			mock.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
			mock.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{GPU: 0, UUID: "GPU-test", DCGMSupported: "Yes"}, nil)
			mock.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
			mock.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
			var group dcgm.GroupHandle
			group.SetHandle(42)
			var fields dcgm.FieldHandle
			fields.SetHandle(43)
			mock.EXPECT().CreateGroup(gomock.Any()).Return(group, nil).Times(1)
			mock.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(0)).Return(nil).Times(1)
			mock.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}).Return(fields, nil).Times(1)
			mock.EXPECT().WatchFieldsWithGroupExNoRefresh(fields, group, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)
			old, oldTopology, err := buildRegistry(context.Background(), config, counters.DCPCapabilities{})
			require.NoError(t, err)
			t.Cleanup(old.Cleanup)
			coord := newTestCoordinator(t)
			coord.buildRegistry = buildRegistry
			coord.reloadConfig = config.Clone()
			activeConfig := coord.reloadConfig
			coord.server.SwapMetricsRuntime(old, oldTopology)

			candidate := config.Clone()
			candidate.CollectInterval = 5000
			if tt.watchFailure {
				expectWatchFailure(t, mock, candidate)
			} else {
				mock.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
				mock.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{}, assert.AnError)
			}
			require.False(t, coord.doConfigReload(context.Background(), candidate, 1))
			require.Same(t, old, coord.server.GetRegistry())
			require.Same(t, activeConfig, coord.reloadConfig)
			require.Equal(t, config, coord.reloadConfig)
			mock.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}).Return(sharedWatchValues([]dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}), nil)
			metrics, err := old.Gather()
			require.NoError(t, err)
			require.Len(t, metrics[dcgm.FE_GPU][testutils.SampleGPUTempCounter], 1)

			// Register active-handle cleanup only after rejection so premature cleanup fails.
			mock.EXPECT().UnwatchFields(fields, group).Return(nil).Times(1)
			mock.EXPECT().FieldGroupDestroy(fields).Return(nil).Times(1)
			mock.EXPECT().DestroyGroup(group).Return(nil).Times(1)
			mock.EXPECT().GetAllDeviceCount().Return(uint(0), nil)
			mock.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
			mock.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
			require.True(t, coord.doConfigReload(context.Background(), config, 2))
			require.NotSame(t, old, coord.server.GetRegistry())
			require.ErrorIs(t, old.BeginUse(), registry.ErrRegistryShuttingDown)
			coord.server.GetRegistry().Cleanup()
		})
	}
}

// expectWatchFailure rejects an instance watch and checks that candidate handles are released.
func expectWatchFailure(t *testing.T, native *mockdcgmprovider.MockDCGM, config *appconfig.Config) {
	t.Helper()
	sources, err := signalcatalog.WatchSourcesFor("gpu.xid.records", signalcatalog.ProfileNVIDIA)
	require.NoError(t, err)
	config.EnableOTLP = true
	config.OTLPWatchCollections = []appconfig.OTLPWatchCollection{{Name: "telemetry", Sources: sources, Interval: int(config.CollectInterval)}}
	native.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_XID_ERRORS).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU_CI}, nil).AnyTimes()
	expectGPUDiscovery(native, true)

	groups := make(map[uintptr]bool)
	fields := make(map[uintptr]bool)
	bindings := make(map[[2]uintptr]bool)
	next := uintptr(44)
	rejected := false
	native.EXPECT().CreateGroup(gomock.Any()).DoAndReturn(func(string) (dcgm.GroupHandle, error) {
		handle := dcgm.GroupHandle{}
		handle.SetHandle(next)
		groups[next] = true
		next++
		return handle, nil
	}).AnyTimes()
	native.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ dcgm.GroupHandle, kind dcgm.Field_Entity_Group, _ uint) error {
			if kind == dcgm.FE_GPU_I {
				rejected = true
				return assert.AnError
			}
			return nil
		},
	).AnyTimes()
	native.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(func(string, []dcgm.Short) (dcgm.FieldHandle, error) {
		handle := dcgm.FieldHandle{}
		handle.SetHandle(next)
		fields[next] = true
		next++
		return handle, nil
	}).AnyTimes()
	native.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(field dcgm.FieldHandle, group dcgm.GroupHandle, _ int64, _ float64, _ int32) error {
			bindings[[2]uintptr{field.GetHandle(), group.GetHandle()}] = true
			return nil
		},
	).AnyTimes()
	native.EXPECT().UnwatchFields(gomock.Cond(func(h dcgm.FieldHandle) bool { return fields[h.GetHandle()] }), gomock.Any()).DoAndReturn(
		func(field dcgm.FieldHandle, group dcgm.GroupHandle) error {
			key := [2]uintptr{field.GetHandle(), group.GetHandle()}
			require.True(t, bindings[key])
			delete(bindings, key)
			return nil
		},
	).AnyTimes()
	native.EXPECT().FieldGroupDestroy(gomock.Cond(func(h dcgm.FieldHandle) bool { return fields[h.GetHandle()] })).DoAndReturn(
		func(field dcgm.FieldHandle) error { delete(fields, field.GetHandle()); return nil },
	).AnyTimes()
	native.EXPECT().DestroyGroup(gomock.Cond(func(h dcgm.GroupHandle) bool { return groups[h.GetHandle()] })).DoAndReturn(
		func(group dcgm.GroupHandle) error { delete(groups, group.GetHandle()); return nil },
	).AnyTimes()
	t.Cleanup(func() {
		require.True(t, rejected)
		require.Empty(t, groups)
		require.Empty(t, fields)
		require.Empty(t, bindings)
	})
}

// A reload that rebuilds the registry starts new cumulative collectors, so XID and
// clock-event totals do not carry over from the previous registry.
func TestRegistryReloadResetsCumulativeCounters(t *testing.T) {
	tests := []struct {
		name    string
		fieldID dcgm.Short
		value   byte
		build   func(counters.CounterList, string, *appconfig.Config, devicewatchlistmanager.WatchList) (collector.Collector, error)
	}{
		{counters.DCGMExpXIDErrorsTotal, dcgm.DCGM_FI_DEV_XID_ERRORS, 42, collector.NewXIDTotalCollector},
		{counters.DCGMExpClockEventsTotal, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, 1, collector.NewClockEventsTotalCollector},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockDCGM := withMockDCGMClient(t)
			mockDCGM.EXPECT().UpdateAllFields().Return(nil).AnyTimes()
			var eventsConsumed atomic.Bool
			mockDCGM.EXPECT().GetValuesSince(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(dcgm.GroupHandle, dcgm.FieldHandle, time.Time) ([]dcgm.FieldValue_v2, time.Time, error) {
					if !eventsConsumed.CompareAndSwap(false, true) {
						return nil, time.Now(), nil
					}

					value := dcgm.FieldValue_v2{EntityGroupId: dcgm.FE_GPU, FieldID: tt.fieldID, FieldType: dcgm.DCGM_FT_INT64}
					event := value
					event.Value[0] = tt.value
					return []dcgm.FieldValue_v2{value, event}, time.Now(), nil
				}).AnyTimes()

			deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
			deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			watcher := mockdevicewatcher.NewMockWatcher(ctrl)
			watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return(
				[]dcgm.GroupHandle{{}}, []dcgm.FieldHandle{{}}, nil, nil).Times(2)

			watchList := devicewatchlistmanager.NewWatchList(deviceInfo, nil, nil, watcher, 10)
			counter := counters.Counter{FieldName: tt.name, PromType: "counter"}

			startup := appconfig.DefaultConfig()
			coord := initReloadCoordinator(startup)
			coord.setServer(&server.MetricsServer{})
			coord.buildRegistry = func(_ context.Context, cfg *appconfig.Config, _ counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
				pollConfig := cfg.Clone()
				pollConfig.CollectInterval = 10
				c, err := tt.build(counters.CounterList{counter}, "localhost", pollConfig, *watchList)
				if err != nil {
					return nil, nil, err
				}

				reg := registry.NewRegistry()
				tuple := collector.EntityCollectorTuple{}
				tuple.SetEntity(dcgm.FE_GPU)
				tuple.SetCollector(c)
				return reg, topologyManager(), reg.Register(tuple)
			}

			initial, _, err := coord.buildRegistry(context.Background(), startup, coord.dcp)
			require.NoError(t, err)
			coord.server.SetRegistry(initial)
			t.Cleanup(func() { coord.server.GetRegistry().Cleanup() })
			require.Eventually(t, func() bool {
				metrics, err := initial.Gather()
				return err == nil && len(metrics[dcgm.FE_GPU][counter]) == 1 && metrics[dcgm.FE_GPU][counter][0].Value == "1"
			}, 2*time.Second, 10*time.Millisecond)

			coord.handle(context.Background(), evConfigChanged)
			require.NotSame(t, initial, coord.server.GetRegistry())
			metrics, err := coord.server.GetRegistry().Gather()
			require.NoError(t, err)
			assert.Empty(t, metrics[dcgm.FE_GPU][counter], "new collector must not carry totals from the previous runtime")
		})
	}
}

// TestBuildConfigReloadRejectsHPCJobMappingChanges checks a reread config that
// changes the HPC mapping directory or a file limit asks for a restart, and
// leaves the saved config alone.
func TestBuildConfigReloadRejectsHPCJobMappingChanges(t *testing.T) {
	startup := appconfig.DefaultConfig()
	startup.EnableOTLP = true
	startup.ConfigFile = "/etc/dcgm-exporter/config.yaml"
	startup.HPCJobMappingDir = "/jobs"
	snapshot := startup.Clone()

	for _, tt := range []struct {
		name   string
		change func(*appconfig.Config)
	}{
		{"directory", func(c *appconfig.Config) { c.HPCJobMappingDir = "/other" }},
		{"file bytes", func(c *appconfig.Config) { c.HPCJobMappingMaxFileBytes = 1 }},
		{"job ID bytes", func(c *appconfig.Config) { c.HPCJobMappingMaxJobIDBytes = 1 }},
		{"jobs per file", func(c *appconfig.Config) { c.HPCJobMappingMaxJobsPerFile = 1 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			coord := newReloadCoordinator()
			coord.reloadConfig = startup.Clone()
			coord.readConfig = func() (*appconfig.Config, error) {
				next := startup.Clone()
				tt.change(next)
				return next, nil
			}

			cfg, err := coord.buildConfigReload()
			require.ErrorContains(t, err, "changing sources.hpc_jobs requires exporter restart")
			assert.Nil(t, cfg)
			assert.Equal(t, snapshot, coord.reloadConfig)
		})
	}

	coord := newReloadCoordinator()
	coord.reloadConfig = startup.Clone()
	coord.readConfig = func() (*appconfig.Config, error) { return startup.Clone(), nil }

	cfg, err := coord.buildConfigReload()
	require.NoError(t, err)
	assert.Equal(t, "/jobs", cfg.HPCJobMappingDir)
}

// TestBuildConfigReloadRequiresRestartForDumpChanges checks that a reread
// config that changes any debug dump setting is refused, and an unchanged one
// is accepted.
func TestBuildConfigReloadRequiresRestartForDumpChanges(t *testing.T) {
	tests := []struct {
		name   string
		change func(*appconfig.DumpConfig)
	}{
		{name: "unchanged", change: func(*appconfig.DumpConfig) {}},
		{name: "enabled", change: func(c *appconfig.DumpConfig) { c.Enabled = !c.Enabled }},
		{name: "directory", change: func(c *appconfig.DumpConfig) { c.Directory = "/other" }},
		{name: "retention", change: func(c *appconfig.DumpConfig) { c.Retention++ }},
		{name: "compression", change: func(c *appconfig.DumpConfig) { c.Compression = !c.Compression }},
		{name: "min interval", change: func(c *appconfig.DumpConfig) { c.MinInterval += time.Second }},
		{name: "max file bytes", change: func(c *appconfig.DumpConfig) { c.MaxFileBytes++ }},
		{name: "max total bytes", change: func(c *appconfig.DumpConfig) { c.MaxTotalBytes++ }},
		{name: "max files", change: func(c *appconfig.DumpConfig) { c.MaxFiles++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startup := appconfig.DefaultConfig()
			startup.EnableOTLP = true
			startup.ConfigFile = "/etc/dcgm-exporter/config.yaml"
			coord := initReloadCoordinator(startup)
			coord.readConfig = func() (*appconfig.Config, error) {
				reread := startup.Clone()
				tt.change(&reread.DumpConfig)
				return reread, nil
			}

			cfg, err := coord.buildConfigReload()

			if tt.name == "unchanged" {
				require.NoError(t, err)
				assert.Equal(t, startup.DumpConfig, cfg.DumpConfig)
				return
			}
			require.ErrorContains(t, err, "changing debug_dump requires exporter restart")
			assert.Nil(t, cfg)
		})
	}
}

const (
	watcherTestTimeout      = time.Second
	watcherTestPollInterval = time.Millisecond
)

func preparedWatch(
	watch func(context.Context, watcher.GPUBindUnbindEventHandler) error,
) watcher.GPUWatchStarter {
	return func(ctx context.Context, onEvent watcher.GPUBindUnbindEventHandler) (func() error, error) {
		return func() error { return watch(ctx, onEvent) }, nil
	}
}

// TestStopGPUWatcherOnDoneStopsOnContextCancel checks that shutdown stops the
// GPU watch before the wait group lets DCGM cleanup run.
func TestStopGPUWatcherOnDoneStopsOnContextCancel(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	watch := func(ctx context.Context, _ watcher.GPUBindUnbindEventHandler) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	coord := newReloadCoordinator()
	coord.setGPUWatcher(watcher.NewRestartableGPUWatcher(preparedWatch(watch), nil, watcherTestTimeout),
		cleanupWait{timeout: watcherTestTimeout})
	var wg sync.WaitGroup
	require.NoError(t, coord.startGPUWatcher(ctx))
	coord.stopGPUWatcherOnDone(ctx, &wg)
	waitForSignal(t, started, "watcher did not start")
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		select {
		case <-stopped:
		default:
			t.Fatal("watcher goroutine exited before cleanup finished")
		}
	case <-time.After(time.Second):
		t.Fatal("GPU watcher did not stop after context cancellation")
	}
	assert.False(t, coord.nativeCleanupBlocked.Load())
}

// TestReloadCoordinator_GPUWatcherTimeoutKeepsProviders checks that a GPU
// watcher timeout during a lifecycle reset, recovery or shutdown leaves DCGM
// and NVML in place and ends the exporter with an error.
func TestReloadCoordinator_GPUWatcherTimeoutKeepsProviders(t *testing.T) {
	timeoutErr := fmt.Errorf("%w: stop took longer than 30s", watcher.ErrGPUWatchTimeout)

	tests := []struct {
		name    string
		watcher *scriptedGPUWatcherLifecycle
		// wantStarts counts Start calls, which must not include a restart.
		wantStarts int
		// run drives the coordinator into the watcher call that times out.
		run func(t *testing.T, coord *reloadCoordinator)
	}{
		{
			name:    "reset stop",
			watcher: &scriptedGPUWatcherLifecycle{stopErr: timeoutErr},
			run: func(_ *testing.T, coord *reloadCoordinator) {
				coord.doGPULifecycleReset(context.Background(), 1)
			},
		},
		{
			name:       "recovery start",
			watcher:    &scriptedGPUWatcherLifecycle{startErrors: []error{timeoutErr}},
			wantStarts: 1,
			run: func(_ *testing.T, coord *reloadCoordinator) {
				coord.gpuRecoveryConfig = coord.reloadConfig.Clone()
				coord.gpuRecoveryStage = gpuRecoveryRestartWatcher
				coord.continueGPULifecycleRecovery(context.Background(), 1)
			},
		},
		{
			name:    "shutdown stop",
			watcher: &scriptedGPUWatcherLifecycle{stopErr: timeoutErr},
			run: func(t *testing.T, coord *reloadCoordinator) {
				ctx, cancel := context.WithCancel(context.Background())
				var wg sync.WaitGroup
				coord.stopGPUWatcherOnDone(ctx, &wg)
				cancel()
				wg.Wait()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			coord := newTestCoordinator(t)
			published := registry.NewRegistry()
			coord.server.SetRegistry(published)
			coord.gpuWatcher = tc.watcher
			var providerCleanups atomic.Int32
			coord.cleanupNVML = func() { providerCleanups.Add(1) }
			coord.cleanupDCGM = func() { providerCleanups.Add(1) }
			coord.scheduleRetry = func(context.Context, time.Duration, func()) context.CancelFunc {
				t.Error("a GPU watcher timeout must not schedule a recovery retry")
				return func() {}
			}

			tc.run(t, coord)

			assert.True(t, coord.nativeCleanupBlocked.Load())
			require.ErrorIs(t, coord.server.TerminalError(), watcher.ErrGPUWatchTimeout)
			assert.Zero(t, providerCleanups.Load(), "providers were cleaned up while native work may run")
			assert.Equal(t, tc.wantStarts, tc.watcher.starts, "the watcher restarted after a timeout")
			assert.Same(t, published, coord.server.GetRegistry())
		})
	}
}

// TestRestartableGPUWatcher_RecoveryCompletionResetsRegistryOnce checks that one
// recovery completion rebuilds the registry once and restarts the watch.
func TestRestartableGPUWatcher_RecoveryCompletionResetsRegistryOnce(t *testing.T) {
	mock := withMockDCGMClient(t)
	mock.EXPECT().
		GetSupportedMetricGroups(uint(0)).
		Return(nil, errors.New("profiling unavailable")).
		Times(1)

	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	newRegistry := registry.NewRegistry()
	buildCompleted := make(chan struct{})
	var builds atomic.Int32
	coord.buildRegistry = func(
		context.Context,
		*appconfig.Config,
		counters.DCPCapabilities,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			close(buildCompleted)
		}
		return newRegistry, topologyManager(), nil
	}

	started := make(chan int32, 2)
	var watchCalls atomic.Int32
	managed := watcher.NewRestartableGPUWatcher(
		preparedWatch(func(ctx context.Context, onEvent watcher.GPUBindUnbindEventHandler) error {
			call := watchCalls.Add(1)
			started <- call
			if call == 1 {
				onEvent(dcgm.DcgmBUEventStateSystemReinitializationCompleted)
			}
			<-ctx.Done()
			return ctx.Err()
		}),
		func(state dcgm.BindUnbindEventState) {
			event, ok := reloadEventForGPUState(state)
			if ok {
				coord.Trigger(event)
			}
		},
		watcherTestTimeout,
	)
	coord.setGPUWatcher(managed, cleanupWait{timeout: watcherTestTimeout})
	runCoordinator(t, coord)
	t.Cleanup(func() { require.NoError(t, managed.Stop()) })

	require.NoError(t, managed.Start(t.Context()))
	require.Equal(t, int32(1), receiveInt32(t, started))
	waitForSignal(t, buildCompleted, "recovery completion did not rebuild the registry")
	require.Equal(t, int32(2), receiveInt32(t, started))
	require.Equal(t, int32(1), builds.Load())
	assert.Eventually(t, func() bool {
		return coord.server.GetRegistry() == newRegistry
	}, watcherTestTimeout, watcherTestPollInterval)
}

// receiveInt32 reads one test value or fails if the watcher does not respond.
func receiveInt32(t *testing.T, values <-chan int32) int32 {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(watcherTestTimeout):
		t.Fatal("watcher did not report its lifecycle call")
		return 0
	}
}

// waitForSignal waits for one test signal or fails with the supplied message.
func waitForSignal(t *testing.T, signal <-chan struct{}, failureMessage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(watcherTestTimeout):
		t.Fatal(failureMessage)
	}
}
