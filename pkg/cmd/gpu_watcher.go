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
	"log/slog"
	"sync"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/watcher"
)

// gpuWatcherLifecycle lets the reload coordinator stop and restart the GPU watcher.
type gpuWatcherLifecycle interface {
	Start(context.Context) error
	Stop()
}

type gpuWatchStarter func(
	context.Context,
	watcher.GPUBindUnbindEventHandler,
) (func() error, error)

// restartableGPUWatcher lets provider resets stop the DCGM-backed watch safely.
type restartableGPUWatcher struct {
	start   gpuWatchStarter
	onEvent watcher.GPUBindUnbindEventHandler

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// newRestartableGPUWatcher wraps synchronous watch setup and its blocking event loop.
func newRestartableGPUWatcher(
	start gpuWatchStarter,
	onEvent watcher.GPUBindUnbindEventHandler,
) *restartableGPUWatcher {
	return &restartableGPUWatcher{
		start:   start,
		onEvent: onEvent,
	}
}

// runGPUWatcher stops an already-started managed watcher when ctx is canceled.
func runGPUWatcher(ctx context.Context, w gpuWatcherLifecycle, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		w.Stop()
	}()
}

// Start begins watching unless a watch is already running or ctx is canceled.
func (g *restartableGPUWatcher) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cancel != nil || ctx.Err() != nil {
		return ctx.Err()
	}

	watchCtx, cancel := context.WithCancel(ctx)
	run, err := g.start(watchCtx, g.onEvent)
	if err != nil {
		cancel()
		return err
	}
	done := make(chan struct{})
	g.cancel = cancel
	g.done = done

	go func() {
		defer close(done)
		err := run()
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(watchCtx, "GPU watcher failed", slog.String("error", err.Error()))
		}
	}()
	return nil
}

// Stop cancels the active watch; concurrent callers all wait for its cleanup.
func (g *restartableGPUWatcher) Stop() {
	g.mu.Lock()
	if g.cancel == nil {
		g.mu.Unlock()
		return
	}

	cancel := g.cancel
	done := g.done
	cancel()
	g.mu.Unlock()

	<-done

	g.mu.Lock()
	if g.done == done {
		g.cancel = nil
		g.done = nil
	}
	g.mu.Unlock()
}
