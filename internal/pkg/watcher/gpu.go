package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// GPUBindUnbindWatcher monitors GPU bind/unbind events using DCGM_FI_BIND_UNBIND_EVENT field
// This is a GLOBAL field (DCGM_FS_GLOBAL) that tracks system-wide driver attach/detach events
// Requires DCGM 4.5.0 or later
type GPUBindUnbindWatcher struct {
	pollInterval time.Duration
}

// GPUBindUnbindWatcherOption configures a GPUBindUnbindWatcher
type GPUBindUnbindWatcherOption func(*GPUBindUnbindWatcher)

// WithPollInterval sets how often to check for bind/unbind events
// DCGM recommends 1 second for this field (see dcgm_fields.h)
// Default is 1 second
func WithPollInterval(interval time.Duration) GPUBindUnbindWatcherOption {
	return func(w *GPUBindUnbindWatcher) {
		w.pollInterval = interval
	}
}

// NewGPUBindUnbindWatcher creates a new GPU bind/unbind event watcher
func NewGPUBindUnbindWatcher(opts ...GPUBindUnbindWatcherOption) *GPUBindUnbindWatcher {
	w := &GPUBindUnbindWatcher{
		pollInterval: 1 * time.Second, // DCGM recommended frequency
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Watch starts monitoring GPU bind/unbind events and calls onChange when detected
// It blocks until the context is cancelled
// onChange is called for any GPU topology change (bind or unbind)
func (w *GPUBindUnbindWatcher) Watch(ctx context.Context, onChange func()) error {
	slog.Info("Watching for GPU bind/unbind events",
		slog.Duration("poll_interval", w.pollInterval))

	// Create field group for bind/unbind event
	fieldGroupName := "dcgm_exporter_bind_unbind_watch"
	fieldGroup, err := dcgmprovider.Client().FieldGroupCreate(fieldGroupName, []dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT})
	if err != nil {
		// Check if this is because NVML isn't available
		if strings.Contains(err.Error(), "NVML doesn't exist") {
			slog.Warn("GPU bind/unbind watcher disabled - NVML not available on this system")
			return nil
		}
		return fmt.Errorf("failed to create bind/unbind field group: %w", err)
	}
	defer func() {
		if destroyErr := dcgmprovider.Client().FieldGroupDestroy(fieldGroup); destroyErr != nil {
			slog.Warn("Failed to destroy bind/unbind field group", slog.String("error", destroyErr.Error()))
		}
	}()

	// DCGM_FI_BIND_UNBIND_EVENT is a GLOBAL field (DCGM_FE_NONE)
	// Use GPU ID 0 - ID doesn't matter for global fields
	groupID := dcgmprovider.Client().GroupAllGPUs()
	err = dcgmprovider.Client().WatchFieldsWithGroupEx(
		fieldGroup,
		groupID,
		int64(w.pollInterval.Microseconds()),
		0.0, // maxKeepAge - no limit
		0,   // maxKeepSamples - no limit
	)
	if err != nil {
		return fmt.Errorf("failed to watch bind/unbind events: %w", err)
	}
	defer func() {
		// Explicitly unwatch the bind/unbind event field from GroupAllGPUs().
		//
		// This is independent from metric collection watchers because:
		// 1. Different field group: we watch "dcgm_exporter_bind_unbind_watch" (only DCGM_FI_BIND_UNBIND_EVENT)
		// 2. Different device group: we use built-in GroupAllGPUs(), metrics use custom "gpu-collector-group-XXX"
		// 3. Each watch is identified by (fieldGroup, deviceGroup) pair - unwatching ours doesn't affect others
		//
		// Note: Metric collectors use the legacy pattern (only destroy groups/field groups without explicit unwatch).
		// We use UnwatchFields explicitly for proper cleanup of the global bind/unbind event field.
		if unwatchErr := dcgmprovider.Client().UnwatchFields(fieldGroup, groupID); unwatchErr != nil {
			// Ignore benign errors when DCGM shuts down before cleanup (during reload)
			errMsg := unwatchErr.Error()
			if !strings.Contains(errMsg, "Setting not configured") &&
				!strings.Contains(errMsg, "Field is not being watched") {
				slog.Warn("Failed to unwatch bind/unbind events", slog.String("error", errMsg))
			}
		}
	}()

	slog.Info("Successfully started watching GPU bind/unbind events (global field)")

	// Initialize with current timestamp to avoid triggering on startup state
	// We want to detect CHANGES in GPU topology, not the initial state
	var lastEventTS int64
	err = dcgmprovider.Client().UpdateAllFields()
	if err == nil {
		values, err := dcgmprovider.Client().EntityGetLatestValues(
			dcgm.FE_GPU,
			0, // GPU ID doesn't matter for global fields
			[]dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT},
		)
		if err == nil && len(values) > 0 {
			lastEventTS = values[0].TS
			slog.Debug("Initialized bind/unbind watcher with current timestamp",
				slog.Int64("initial_timestamp", lastEventTS),
				slog.Int64("initial_state", values[0].Int64()))
		}
	}

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Debug("GPU bind/unbind watcher stopping")
			return ctx.Err()

		case <-ticker.C:
			// Update field values
			err := dcgmprovider.Client().UpdateAllFields()
			if err != nil {
				slog.Warn("Failed to update fields for bind/unbind check",
					slog.String("error", err.Error()))
				continue
			}

			// Get latest value for the global bind/unbind event field
			// Use GPU ID 0 since it's a global field
			values, err := dcgmprovider.Client().EntityGetLatestValues(
				dcgm.FE_GPU,
				0, // GPU ID doesn't matter for global fields
				[]dcgm.Short{dcgm.DCGM_FI_BIND_UNBIND_EVENT},
			)
			if err != nil {
				slog.Debug("No bind/unbind events available yet",
					slog.String("error", err.Error()))
				continue
			}

			if len(values) == 0 {
				continue
			}

			// Check event value and timestamp
			eventValue := values[0].Int64()
			eventTS := values[0].TS

			// Only process if this is a new event (timestamp changed)
			if eventTS > lastEventTS && eventValue != 0 {
				lastEventTS = eventTS

				if eventValue == int64(dcgm.DcgmBUEventStateSystemReinitializing) {
					slog.Info("GPU unbind event detected (system reinitializing)",
						slog.Int64("event_state", eventValue),
						slog.Int64("timestamp", eventTS))
					onChange()
					// Continue watching for more events
				} else if eventValue == int64(dcgm.DcgmBUEventStateSystemReinitializationCompleted) {
					slog.Info("GPU bind event detected (reinitialization completed)",
						slog.Int64("event_state", eventValue),
						slog.Int64("timestamp", eventTS))
					onChange()
					// Continue watching for more events
				}
			}
		}
	}
}
