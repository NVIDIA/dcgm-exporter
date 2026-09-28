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
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

const (
	watcherTestTimeout      = time.Second
	watcherTestPollInterval = time.Millisecond
)

func preparedWatch(
	watch func(context.Context, watcher.GPUBindUnbindEventHandler) error,
) gpuWatchStarter {
	return func(ctx context.Context, onEvent watcher.GPUBindUnbindEventHandler) (func() error, error) {
		return func() error { return watch(ctx, onEvent) }, nil
	}
}

// TestRestartableGPUWatcherCanceledStart checks that a canceled context starts no work.
func TestRestartableGPUWatcherCanceledStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		starts := make(chan struct{}, 1)
		stops := make(chan struct{}, 1)
		watch := func(ctx context.Context, _ watcher.GPUBindUnbindEventHandler) error {
			starts <- struct{}{}
			<-ctx.Done()
			stops <- struct{}{}
			return ctx.Err()
		}

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := newRestartableGPUWatcher(preparedWatch(watch), nil).Start(ctx)
		require.ErrorIs(t, err, context.Canceled)
		synctest.Wait()

		for name, events := range map[string]<-chan struct{}{"start": starts, "stop": stops} {
			select {
			case <-events:
				t.Fatalf("canceled context caused a watcher %s", name)
			default:
			}
		}
	})
}

// TestRestartableGPUWatcherLifecycle checks restart and repeated stop behavior.
func TestRestartableGPUWatcherLifecycle(t *testing.T) {
	starts := make(chan int32, 2)
	stops := make(chan int32, 2)
	var watchCount atomic.Int32
	watch := func(ctx context.Context, _ watcher.GPUBindUnbindEventHandler) error {
		call := watchCount.Add(1)
		starts <- call
		<-ctx.Done()
		stops <- call
		return ctx.Err()
	}

	managed := newRestartableGPUWatcher(preparedWatch(watch), nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		managed.Stop()
	})

	require.NoError(t, managed.Start(ctx))
	require.Equal(t, int32(1), receiveInt32(t, starts))
	managed.Stop()
	require.Equal(t, int32(1), receiveInt32(t, stops))
	managed.Stop()

	require.NoError(t, managed.Start(ctx))
	require.Equal(t, int32(2), receiveInt32(t, starts))
	managed.Stop()
	require.Equal(t, int32(2), receiveInt32(t, stops))
}

// TestRestartableGPUWatcherConcurrentStopsWait checks that every caller waits for cleanup.
func TestRestartableGPUWatcherConcurrentStopsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		cleanupStarted := make(chan struct{})
		allowCleanup := make(chan struct{})
		watch := func(ctx context.Context, _ watcher.GPUBindUnbindEventHandler) error {
			close(started)
			<-ctx.Done()
			close(cleanupStarted)
			<-allowCleanup
			return ctx.Err()
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		managed := newRestartableGPUWatcher(preparedWatch(watch), nil)
		require.NoError(t, managed.Start(ctx))
		waitForSignal(t, started, "watcher did not start")

		firstDone := make(chan struct{})
		go func() {
			managed.Stop()
			close(firstDone)
		}()
		waitForSignal(t, cleanupStarted, "watcher cleanup did not start")

		secondDone := make(chan struct{})
		go func() {
			managed.Stop()
			close(secondDone)
		}()
		synctest.Wait()

		select {
		case <-secondDone:
			t.Fatal("second Stop returned before watcher cleanup finished")
		default:
		}

		close(allowCleanup)
		synctest.Wait()
		for _, done := range []<-chan struct{}{firstDone, secondDone} {
			select {
			case <-done:
			default:
				t.Fatal("Stop did not return after watcher cleanup finished")
			}
		}
	})
}

// TestRunGPUWatcherStopsOnContextCancel checks the production goroutine wrapper.
func TestRunGPUWatcherStopsOnContextCancel(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	watch := func(ctx context.Context, _ watcher.GPUBindUnbindEventHandler) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	managed := newRestartableGPUWatcher(preparedWatch(watch), nil)
	var wg sync.WaitGroup
	require.NoError(t, managed.Start(ctx))
	runGPUWatcher(ctx, managed, &wg)
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
}

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
		*cli.Context,
		*appconfig.Config,
	) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		if builds.Add(1) == 1 {
			close(buildCompleted)
		}
		return newRegistry, topologyManager(), nil
	}

	started := make(chan int32, 2)
	var watchCalls atomic.Int32
	managed := newRestartableGPUWatcher(
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
	)
	coord.setGPUWatcher(managed)
	runCoordinator(t, coord)
	t.Cleanup(managed.Stop)

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
