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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
)

// retirementBlockingCollector models a collector with a native call that
// cannot return during the application's retirement deadline.
type retirementBlockingCollector struct {
	done chan struct{}
	once sync.Once
}

// deferredBuildError models a collector factory failure whose rollback still
// waits for a native worker to release its watches.
type deferredBuildError struct {
	err  error
	done <-chan struct{}
}

// Error returns the factory failure that caused the deferred cleanup.
func (e deferredBuildError) Error() string {
	return e.err.Error()
}

// Unwrap preserves errors.Is behavior for the underlying factory failure.
func (e deferredBuildError) Unwrap() error {
	return e.err
}

// CleanupComplete reports when the simulated factory rollback has finished.
func (e deferredBuildError) CleanupComplete() <-chan struct{} {
	return e.done
}

// GetMetrics provides the collector interface; this test exercises retirement
// completion rather than metric generation.
func (c *retirementBlockingCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return collector.MetricsByCounter{}, nil
}

// Cleanup records a stop request but deliberately leaves completion controlled
// by the test, like an uninterruptible native call.
func (c *retirementBlockingCollector) Cleanup() {
	c.once.Do(func() {})
}

// CleanupComplete exposes the test-controlled native completion signal.
func (c *retirementBlockingCollector) CleanupComplete() <-chan struct{} {
	return c.done
}

// TestWaitForRegistryRetirementTimeoutLeavesCleanupRunning verifies a timeout
// does not pretend the registry is clean. The caller can enter terminal safe
// mode while the one registry worker continues to wait without provider close.
func TestWaitForRegistryRetirementTimeoutLeavesCleanupRunning(t *testing.T) {
	reg := registry.NewRegistry()
	blocking := &retirementBlockingCollector{done: make(chan struct{})}
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(blocking)
	require.NoError(t, reg.Register(tuple))

	err := waitForRegistryRetirement(context.Background(), reg, cleanupWait{timeout: 10 * time.Millisecond})
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	retired := reg.Retire()

	select {
	case <-retired:
		t.Fatal("registry reported retirement despite blocked collector cleanup")
	default:
	}

	close(blocking.done)
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("registry did not finish after the blocked collector completed")
	}
	require.NoError(t, reg.RetirementError())
}

// TestWaitForCollectorRollbackTimeoutPreservesNativeSafety verifies candidate
// rollback gets the same bounded, terminal-safe handling as a published
// registry retirement.
func TestWaitForCollectorRollbackTimeoutPreservesNativeSafety(t *testing.T) {
	done := make(chan struct{})
	err := waitForCollectorRollback(
		context.Background(),
		deferredBuildError{err: errors.New("build failed"), done: done},
		cleanupWait{timeout: 10 * time.Millisecond},
	)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	close(done)
	require.NoError(t, waitForCollectorRollback(
		context.Background(),
		deferredBuildError{err: errors.New("build failed"), done: done},
		cleanupWait{timeout: time.Second},
	))
}

// TestReloadCoordinatorRetirementFailureSkipsProviderCleanup verifies a GPU
// reset stops before NVML or DCGM cleanup when its removed registry cannot
// prove that native users have finished.
func TestReloadCoordinatorRetirementFailureSkipsProviderCleanup(t *testing.T) {
	coord := newTestCoordinator(t)
	coord.server.SetRegistry(registry.NewRegistry())
	coord.awaitRegistryRetirement = func(context.Context, *registry.Registry, cleanupWait) error {
		return errors.New("native call still running")
	}

	var nvmlCleanups atomic.Int32
	var dcgmCleanups atomic.Int32
	coord.cleanupNVML = func() { nvmlCleanups.Add(1) }
	coord.cleanupDCGM = func() { dcgmCleanups.Add(1) }

	coord.doGPULifecycleReset(context.Background(), 1)

	require.True(t, coord.nativeCleanupBlocked.Load())
	require.Error(t, coord.server.TerminalError())
	require.Equal(t, int32(0), nvmlCleanups.Load())
	require.Equal(t, int32(0), dcgmCleanups.Load())
}

// rejectingRegistry rejects one registration and forwards the others.
type rejectingRegistry struct {
	registry *registry.Registry
	rejectAt int
	calls    int
}

// Register rejects the configured call after forwarding earlier candidates.
func (r *rejectingRegistry) Register(tuple collector.EntityCollectorTuple) error {
	index := r.calls
	r.calls++
	if index == r.rejectAt {
		return registry.ErrRegistryShuttingDown
	}
	return r.registry.Register(tuple)
}

// synchronousTrackingCollector records cleanup requests without a wait signal.
type synchronousTrackingCollector struct {
	cleanupCalls atomic.Int32
}

// GetMetrics returns an empty sample for registry ownership tests.
func (*synchronousTrackingCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return collector.MetricsByCounter{}, nil
}

// Cleanup records one synchronous cleanup request.
func (c *synchronousTrackingCollector) Cleanup() {
	c.cleanupCalls.Add(1)
}

// TestPopulateRegistryRollbackOwnership covers rejection before, among, and
// after other candidates, including a later host-scoped collector.
func TestPopulateRegistryRollbackOwnership(t *testing.T) {
	tests := []struct {
		rejectAt int
		mode     string
	}{
		{rejectAt: 0, mode: "synchronous"},
		{rejectAt: 0, mode: "delayed"},
		{rejectAt: 0, mode: "mixed"},
		{rejectAt: 2, mode: "synchronous"},
		{rejectAt: 2, mode: "delayed"},
		{rejectAt: 2, mode: "mixed"},
		{rejectAt: 4, mode: "synchronous"},
		{rejectAt: 4, mode: "delayed"},
		{rejectAt: 4, mode: "mixed"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("reject_%d/%s", tt.rejectAt, tt.mode), func(t *testing.T) {
			assertRegistryRollbackOwnership(t, tt.rejectAt, tt.mode)
		})
	}
}

// assertRegistryRollbackOwnership checks one table case without hiding its ownership assertions.
func assertRegistryRollbackOwnership(t *testing.T, rejectAt int, mode string) {
	t.Helper()
	reg := registry.NewRegistry()
	adapter := &rejectingRegistry{registry: reg, rejectAt: rejectAt}
	tuples, counts, signals := rollbackCollectors(mode)
	// Release held work even if an assertion stops this test.
	t.Cleanup(func() {
		releaseRollbackSignals(signals)
		reg.Cleanup()
	})
	factory := &countingFactory{collectors: tuples}
	count, err := populateRegistry(factory, adapter)
	require.Zero(t, count)
	require.ErrorIs(t, err, registry.ErrRegistryShuttingDown)
	require.Equal(t, int32(1), factory.calls.Load())
	require.Equal(t, rejectAt+1, adapter.calls)
	for index, calls := range counts {
		want := int32(1)
		if index < rejectAt {
			want = 0
		}
		require.Equal(t, want, calls.Load(), "candidate %d ownership", index)
	}

	// Application teardown follows the actual registry retirement
	// and candidate rollback gates before closing either provider.
	providerClosed := make(chan struct{})
	go func() {
		reg.Cleanup()
		if waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: time.Second}) == nil {
			close(providerClosed)
		}
	}()
	if mode != "synchronous" {
		completePendingRollback(t, err, signals, rejectAt, providerClosed)
	}
	require.NoError(t, waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: time.Second}))

	if mode != "synchronous" && rejectAt > 0 {
		completeRegisteredRollback(t, reg, signals, rejectAt, providerClosed)
	}
	select {
	case <-providerClosed:
	case <-time.After(time.Second):
		t.Fatal("provider teardown did not finish after all cleanup completed")
	}
	reg.Cleanup()
	for index, calls := range counts {
		require.Equal(t, int32(1), calls.Load(), "candidate %d cleanup exactly once", index)
	}
}

// rollbackCollectors creates the same five independently owned candidates for each table case.
func rollbackCollectors(mode string) ([]collector.EntityCollectorTuple, []*atomic.Int32, []chan struct{}) {
	tuples := make([]collector.EntityCollectorTuple, 5)
	counts := make([]*atomic.Int32, len(tuples))
	signals := make([]chan struct{}, len(tuples))
	for index := range tuples {
		tuples[index].SetEntity(dcgm.FE_GPU)
		if index == len(tuples)-1 {
			tuples[index].SetEntity(dcgm.FE_NONE)
		}
		if mode == "delayed" || (mode == "mixed" && index%2 == 0) {
			c := &completionTrackingCollector{done: make(chan struct{})}
			tuples[index].SetCollector(c)
			counts[index] = &c.cleanupCalls
			signals[index] = c.done
		} else {
			c := &synchronousTrackingCollector{}
			tuples[index].SetCollector(c)
			counts[index] = &c.cleanupCalls
		}
	}
	return tuples, counts, signals
}

// releaseRollbackSignals releases unfinished work even after a fatal assertion.
func releaseRollbackSignals(signals []chan struct{}) {
	for _, signal := range signals {
		if signal == nil {
			continue
		}
		select {
		case <-signal:
		default:
			close(signal)
		}
	}
}

// completePendingRollback verifies each rejected candidate gates provider teardown.
func completePendingRollback(t *testing.T, err error, signals []chan struct{}, rejectAt int, providerClosed <-chan struct{}) {
	t.Helper()
	// Complete in reverse order: each independently held pending
	// candidate must still prevent native provider teardown.
	for index := len(signals) - 1; index >= rejectAt; index-- {
		if signals[index] == nil {
			continue
		}
		require.ErrorIs(t, waitForCollectorRollback(context.Background(), err,
			cleanupWait{timeout: 5 * time.Millisecond}), context.DeadlineExceeded)
		select {
		case <-providerClosed:
			t.Fatal("provider closed before pending candidates completed")
		default:
		}
		close(signals[index])
	}
}

// completeRegisteredRollback verifies admitted collectors also gate provider teardown.
func completeRegisteredRollback(t *testing.T, reg *registry.Registry, signals []chan struct{}, rejectAt int, providerClosed <-chan struct{}) {
	t.Helper()
	require.ErrorIs(t, waitForRegistryRetirement(context.Background(), reg,
		cleanupWait{timeout: 5 * time.Millisecond}), context.DeadlineExceeded)
	select {
	case <-providerClosed:
		t.Fatal("provider closed before registered collectors completed")
	default:
	}
	for index := 0; index < rejectAt; index++ {
		if signals[index] != nil {
			close(signals[index])
		}
	}
}
