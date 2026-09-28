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
	"fmt"
	"log/slog"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// GPUBindUnbindWatcher monitors GPU bind/unbind events using DCGM_FI_SYSTEM_GPU_BIND_EVENT.
// This is a global field that tracks system-wide driver attach and detach events. Detached-GPU
// support requires DCGM detached-GPU support and an NVIDIA driver from the 590 series or later.
type GPUBindUnbindWatcher struct {
	pollInterval time.Duration
}

// GPUBindUnbindEventHandler receives an observed DCGM lifecycle state.
type GPUBindUnbindEventHandler func(dcgm.BindUnbindEventState)

// A physical lifecycle emits reinitializing and then completed. Two retained samples preserve
// that pair while the watcher polls at DCGM's recommended one-second cadence.
const bindUnbindHistorySamples = 2

// GPUBindUnbindWatcherOption changes a GPU lifecycle watcher's behavior.
type GPUBindUnbindWatcherOption func(*GPUBindUnbindWatcher)

// WithPollInterval sets how often the watcher reads retained lifecycle events.
// The default is DCGM's recommended one-second cadence.
func WithPollInterval(interval time.Duration) GPUBindUnbindWatcherOption {
	return func(w *GPUBindUnbindWatcher) {
		w.pollInterval = interval
	}
}

// NewGPUBindUnbindWatcher creates a lifecycle watcher that polls once each
// second by default. The application starts it only when bind/unbind detection
// is enabled.
func NewGPUBindUnbindWatcher(opts ...GPUBindUnbindWatcherOption) *GPUBindUnbindWatcher {
	w := &GPUBindUnbindWatcher{
		pollInterval: time.Second,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start installs the DCGM watch synchronously and returns the blocking event
// loop. Callers can therefore establish the watch before taking a topology
// snapshot without losing an event in between.
func (w *GPUBindUnbindWatcher) Start(
	ctx context.Context,
	onEvent GPUBindUnbindEventHandler,
) (func() error, error) {
	slog.Info("Watching for GPU bind/unbind events",
		slog.Duration("poll_interval", w.pollInterval))
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// This is a global field (DCGM_FE_NONE), so it must be watched directly.
	// Watching DCGM_GROUP_ALL_GPUS creates no watch while the exporter starts
	// without GPUs, which is precisely the recovery case this watcher handles.
	cursor := time.Now()
	if err := dcgmprovider.Client().WatchFieldValue(
		0,
		dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
		w.pollInterval,
		0,
		bindUnbindHistorySamples,
	); err != nil {
		return nil, fmt.Errorf(
			"GPU bind/unbind detection requires DCGM detached-GPU support and NVIDIA driver 590+; DCGM watch setup failed: %w",
			err,
		)
	}

	// The application stops this watcher before it terminates DCGM for a
	// lifecycle reset or process shutdown. That DCGM cleanup releases this
	// direct field watch; a field group is neither created nor needed here.
	return func() error {
		return w.watchFieldEvents(ctx, cursor, onEvent)
	}, nil
}

// Watch installs the watch and monitors events until the context is cancelled.
func (w *GPUBindUnbindWatcher) Watch(ctx context.Context, onEvent GPUBindUnbindEventHandler) error {
	run, err := w.Start(ctx, onEvent)
	if err != nil {
		return err
	}
	return run()
}

// watchFieldEvents reads retained system-field history newer than cursor. DCGM
// records bind/unbind events directly, so UpdateAllFields is not required.
func (w *GPUBindUnbindWatcher) watchFieldEvents(
	ctx context.Context,
	cursor time.Time,
	onEvent GPUBindUnbindEventHandler,
) error {
	slog.Info("Successfully started watching GPU bind/unbind events (global field)")

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Debug("GPU bind/unbind watcher stopping")
			return ctx.Err()
		case <-ticker.C:
			values, err := dcgmprovider.Client().GetMultipleValuesForField(
				0,
				dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT,
				bindUnbindHistorySamples,
				cursor,
				time.Time{},
			)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				slog.Warn("Failed to read bind/unbind events",
					slog.Time("cursor", cursor),
					slog.String("error", err.Error()))
				continue
			}

			for _, value := range values {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				if value.TS >= cursor.UnixMicro() {
					cursor = time.UnixMicro(value.TS + 1)
				}

				if value.FieldID != dcgm.DCGM_FI_SYSTEM_GPU_BIND_EVENT || value.Status != 0 {
					continue
				}

				switch state := dcgm.BindUnbindEventState(value.Int64()); state {
				case dcgm.DcgmBUEventStateSystemReinitializing:
					// Keep watching. Resetting while DCGM is reinitializing can race
					// the topology transition and prevent us from seeing completion.
					slog.Info("GPU system reinitializing event detected",
						slog.Int64("event_state", int64(state)),
						slog.Int64("timestamp", value.TS))
				case dcgm.DcgmBUEventStateSystemReinitializationCompleted:
					slog.Info("GPU system reinitialization-completed event detected",
						slog.Int64("event_state", int64(state)),
						slog.Int64("timestamp", value.TS))
					onEvent(state)
				}
			}

			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
}
