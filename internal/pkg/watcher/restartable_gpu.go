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
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ErrGPUWatchTimeout reports that native GPU watch setup or stop did not
// finish within the watcher's timeout. The native call may still be running,
// so the caller must keep DCGM alive. The watcher then refuses every later
// Start and Stop with the same error, even if the native call returns.
var ErrGPUWatchTimeout = errors.New("GPU watch did not finish in time")

// GPUWatchStarter installs one native GPU watch and returns its blocking
// event loop. GPUBindUnbindWatcher.Start has this shape.
type GPUWatchStarter func(context.Context, GPUBindUnbindEventHandler) (func() error, error)

// RestartableGPUWatcher lets provider resets stop the DCGM-backed watch safely
// and start it again on the replacement DCGM client. Start and Stop each wait
// at most the watcher's timeout for native work.
type RestartableGPUWatcher struct {
	start   GPUWatchStarter
	onEvent GPUBindUnbindEventHandler
	timeout time.Duration

	mu sync.Mutex
	// current is the watch being set up, running, or stopping; nil when stopped.
	current *gpuWatch
	// err holds the first timeout and makes it permanent.
	err error
}

// gpuWatch is one setup and event-loop run of the native watch.
type gpuWatch struct {
	cancel context.CancelFunc
	// started closes when Start has a result; startErr is set before that.
	started  chan struct{}
	startErr error
	// done closes when setup and the event loop have both returned.
	done chan struct{}
}

// NewRestartableGPUWatcher wraps synchronous watch setup and its blocking
// event loop. timeout bounds each Start and Stop and must be positive.
func NewRestartableGPUWatcher(
	start GPUWatchStarter,
	onEvent GPUBindUnbindEventHandler,
	timeout time.Duration,
) *RestartableGPUWatcher {
	return &RestartableGPUWatcher{
		start:   start,
		onEvent: onEvent,
		timeout: timeout,
	}
}

// Start installs the native watch and returns once it is in place, so the
// caller can read topology afterwards without missing an event. A call made
// while a watch is set up or running returns that watch's setup result.
// Setup that outlasts the timeout returns ErrGPUWatchTimeout.
func (g *RestartableGPUWatcher) Start(ctx context.Context) error {
	g.mu.Lock()
	if err := g.err; err != nil {
		g.mu.Unlock()
		return err
	}

	if w := g.current; w != nil {
		g.mu.Unlock()
		<-w.started
		return w.startErr
	}

	if err := ctx.Err(); err != nil {
		g.mu.Unlock()
		return err
	}

	watchCtx, cancel := context.WithCancel(ctx)
	w := &gpuWatch{
		cancel:  cancel,
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
	g.current = w
	g.mu.Unlock()

	setupErr := make(chan error, 1)
	go g.run(watchCtx, w, setupErr)

	timer := time.NewTimer(g.timeout)
	defer timer.Stop()

	var err error
	select {
	case err = <-setupErr:
	case <-timer.C:
		err = g.fail(fmt.Errorf("%w: setup took longer than %s", ErrGPUWatchTimeout, g.timeout))
	}
	return g.publishStart(w, err)
}

// publishStart records the setup result for callers waiting on the same
// watch. A failed setup cancels the watch; one that failed without a timeout
// is cleared so the next Start can try again.
func (g *RestartableGPUWatcher) publishStart(w *gpuWatch, err error) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if err != nil {
		w.cancel()
		if g.err == nil && g.current == w {
			g.current = nil
		}
	}

	w.startErr = err
	close(w.started)
	return err
}

// run sets up the native watch, sends the setup result, and then runs the
// event loop until the watch is canceled. It closes w.done when no native
// call for this watch remains.
func (g *RestartableGPUWatcher) run(ctx context.Context, w *gpuWatch, setupErr chan<- error) {
	defer close(w.done)

	loop, err := g.setUp(ctx)
	setupErr <- err
	if err != nil {
		return
	}

	// A watch canceled during setup still runs its loop, which returns at once.
	if err := loop(); err != nil && !errors.Is(err, context.Canceled) {
		slog.ErrorContext(ctx, "GPU watcher failed", slog.String("error", err.Error()))
	}
}

// setUp calls the starter and turns a panic into an error, because setup runs
// on its own goroutine where the caller cannot recover it.
func (g *RestartableGPUWatcher) setUp(ctx context.Context) (loop func() error, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("GPU watch setup panicked: %v", recovered)
		}
	}()

	return g.start(ctx, g.onEvent)
}

// Stop cancels the current watch and waits for its native calls to return.
// Concurrent callers wait for the same watch. A wait that outlasts the
// timeout returns ErrGPUWatchTimeout.
func (g *RestartableGPUWatcher) Stop() error {
	g.mu.Lock()
	if err := g.err; err != nil {
		g.mu.Unlock()
		return err
	}

	w := g.current
	if w == nil {
		g.mu.Unlock()
		return nil
	}
	w.cancel()
	g.mu.Unlock()

	timer := time.NewTimer(g.timeout)
	defer timer.Stop()

	select {
	case <-w.done:
	case <-timer.C:
		return g.fail(fmt.Errorf("%w: stop took longer than %s", ErrGPUWatchTimeout, g.timeout))
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.current == w {
		g.current = nil
	}
	return g.err
}

// fail makes the first timeout permanent and returns it.
func (g *RestartableGPUWatcher) fail(err error) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.err == nil {
		g.err = err
	}
	return g.err
}
