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

package watcher

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	// restartableTestTimeout bounds each Start and Stop in these tests.
	restartableTestTimeout = time.Second
	// testSignalTimeout bounds how long a test waits for one watcher signal.
	testSignalTimeout = time.Second
)

// preparedWatch adapts a blocking watch function to a GPUWatchStarter whose
// setup always succeeds.
func preparedWatch(
	watch func(context.Context, GPUBindUnbindEventHandler) error,
) GPUWatchStarter {
	return func(ctx context.Context, onEvent GPUBindUnbindEventHandler) (func() error, error) {
		return func() error { return watch(ctx, onEvent) }, nil
	}
}

// blockedSetup returns a starter whose setup waits for release and then
// succeeds with a loop that ends on cancellation. calls counts setups.
func blockedSetup(release <-chan struct{}, calls *atomic.Int32) GPUWatchStarter {
	return func(ctx context.Context, _ GPUBindUnbindEventHandler) (func() error, error) {
		calls.Add(1)
		<-release

		return func() error {
			<-ctx.Done()
			return ctx.Err()
		}, nil
	}
}

// newTestWatcher wraps start with the test timeout and no event handler.
func newTestWatcher(start GPUWatchStarter) *RestartableGPUWatcher {
	return NewRestartableGPUWatcher(start, nil, restartableTestTimeout)
}

// TestRestartableGPUWatcherCanceledStart checks that a canceled context starts no work.
func TestRestartableGPUWatcherCanceledStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := newTestWatcher(blockedSetup(nil, &calls)).Start(ctx)

		require.ErrorIs(t, err, context.Canceled)
		synctest.Wait()
		require.Zero(t, calls.Load(), "canceled context caused a watch setup")
	})
}

// TestRestartableGPUWatcherLifecycle checks restart and repeated stop behavior.
func TestRestartableGPUWatcherLifecycle(t *testing.T) {
	starts := make(chan int32, 2)
	stops := make(chan int32, 2)
	var watchCount atomic.Int32
	watch := func(ctx context.Context, _ GPUBindUnbindEventHandler) error {
		call := watchCount.Add(1)
		starts <- call
		<-ctx.Done()
		stops <- call
		return ctx.Err()
	}

	managed := newTestWatcher(preparedWatch(watch))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		require.NoError(t, managed.Stop())
	})

	require.NoError(t, managed.Start(ctx))
	require.Equal(t, int32(1), receiveInt32(t, starts))
	require.NoError(t, managed.Stop())
	require.Equal(t, int32(1), receiveInt32(t, stops))
	require.NoError(t, managed.Stop())

	require.NoError(t, managed.Start(ctx))
	require.Equal(t, int32(2), receiveInt32(t, starts))
	require.NoError(t, managed.Stop())
	require.Equal(t, int32(2), receiveInt32(t, stops))
}

// TestRestartableGPUWatcherConcurrentStopsWait checks that every caller waits for cleanup.
func TestRestartableGPUWatcherConcurrentStopsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		cleanupStarted := make(chan struct{})
		allowCleanup := make(chan struct{})
		watch := func(ctx context.Context, _ GPUBindUnbindEventHandler) error {
			close(started)
			<-ctx.Done()
			close(cleanupStarted)
			<-allowCleanup
			return ctx.Err()
		}

		managed := newTestWatcher(preparedWatch(watch))
		require.NoError(t, managed.Start(t.Context()))
		waitForSignal(t, started, "watcher did not start")

		stopErrs := make(chan error, 2)
		go func() { stopErrs <- managed.Stop() }()
		waitForSignal(t, cleanupStarted, "watcher cleanup did not start")
		go func() { stopErrs <- managed.Stop() }()
		synctest.Wait()
		require.Empty(t, stopErrs, "Stop returned before watcher cleanup finished")

		close(allowCleanup)
		synctest.Wait()
		require.Len(t, stopErrs, 2, "Stop did not return after watcher cleanup finished")
		require.NoError(t, <-stopErrs)
		require.NoError(t, <-stopErrs)
	})
}

// TestRestartableGPUWatcherStopWaitsForSetup checks that a Stop during setup
// waits for setup to return before reporting the watch stopped.
func TestRestartableGPUWatcherStopWaitsForSetup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		managed := newTestWatcher(blockedSetup(release, &calls))

		startErr := make(chan error, 1)
		go func() { startErr <- managed.Start(t.Context()) }()
		synctest.Wait()

		stopErr := make(chan error, 1)
		go func() { stopErr <- managed.Stop() }()
		synctest.Wait()
		require.Empty(t, stopErr, "Stop returned while setup was running")

		close(release)
		synctest.Wait()
		require.NoError(t, <-startErr)
		require.NoError(t, <-stopErr)
	})
}

// TestRestartableGPUWatcherConcurrentStartsShareSetup checks that a Start made
// during setup gets that setup's result instead of starting a second watch,
// and that a failed setup lets a later Start try again.
func TestRestartableGPUWatcherConcurrentStartsShareSetup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupErr := errors.New("watch setup failed")
		release := make(chan struct{})
		var calls atomic.Int32
		managed := newTestWatcher(func(context.Context, GPUBindUnbindEventHandler) (func() error, error) {
			if calls.Add(1) == 1 {
				<-release
				return nil, setupErr
			}
			return func() error { return nil }, nil
		})

		startErrs := make(chan error, 2)
		go func() { startErrs <- managed.Start(t.Context()) }()
		synctest.Wait()
		go func() { startErrs <- managed.Start(t.Context()) }()
		synctest.Wait()

		close(release)
		synctest.Wait()
		require.ErrorIs(t, <-startErrs, setupErr)
		require.ErrorIs(t, <-startErrs, setupErr)
		require.Equal(t, int32(1), calls.Load(), "concurrent Start ran a second setup")

		require.NoError(t, managed.Start(t.Context()))
		require.Equal(t, int32(2), calls.Load())
		require.NoError(t, managed.Stop())
	})
}

// TestRestartableGPUWatcherSetupPanicIsAnError checks that a panic in setup,
// which runs on its own goroutine, reaches the caller as an error.
func TestRestartableGPUWatcherSetupPanicIsAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		managed := newTestWatcher(func(context.Context, GPUBindUnbindEventHandler) (func() error, error) {
			panic("native setup failed")
		})

		err := managed.Start(t.Context())

		require.ErrorContains(t, err, "GPU watch setup panicked: native setup failed")
		require.NotErrorIs(t, err, ErrGPUWatchTimeout)
		require.NoError(t, managed.Stop())
	})
}

// TestRestartableGPUWatcherTimeouts checks that a setup or stop that outlasts
// the timeout fails, and that the failure stays after the native call returns.
func TestRestartableGPUWatcherTimeouts(t *testing.T) {
	tests := []struct {
		name string
		// blockSetup holds setup until release; otherwise the event loop
		// ignores cancellation until release.
		blockSetup bool
		// stall makes the call that times out while native work is held.
		stall func(t *testing.T, w *RestartableGPUWatcher) error
	}{
		{
			name:       "setup",
			blockSetup: true,
			stall: func(t *testing.T, w *RestartableGPUWatcher) error {
				return w.Start(t.Context())
			},
		},
		{
			name: "stop",
			stall: func(t *testing.T, w *RestartableGPUWatcher) error {
				require.NoError(t, w.Start(t.Context()))
				return w.Stop()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				var calls atomic.Int32
				managed := newTestWatcher(func(ctx context.Context, _ GPUBindUnbindEventHandler) (func() error, error) {
					calls.Add(1)
					if tc.blockSetup {
						<-release
					}
					return func() error {
						<-release
						return ctx.Err()
					}, nil
				})

				began := time.Now()
				err := tc.stall(t, managed)

				require.ErrorIs(t, err, ErrGPUWatchTimeout)
				require.Equal(t, restartableTestTimeout, time.Since(began))

				// The native call returning late does not undo the failure.
				close(release)
				synctest.Wait()
				require.ErrorIs(t, managed.Start(t.Context()), ErrGPUWatchTimeout)
				require.ErrorIs(t, managed.Stop(), ErrGPUWatchTimeout)
				require.Equal(t, int32(1), calls.Load(), "a timed-out watcher ran setup again")
			})
		})
	}
}

// receiveInt32 reads one test value or fails if the watcher does not respond.
func receiveInt32(t *testing.T, values <-chan int32) int32 {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(testSignalTimeout):
		t.Fatal("watcher did not report its lifecycle call")
		return 0
	}
}

// waitForSignal waits for one test signal or fails with the supplied message.
func waitForSignal(t *testing.T, signal <-chan struct{}, failureMessage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(testSignalTimeout):
		t.Fatal(failureMessage)
	}
}
