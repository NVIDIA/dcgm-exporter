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
	"log/slog"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// envDCGMCleanupTimeout is the environment variable of --dcgm-cleanup-timeout.
const envDCGMCleanupTimeout = "DCGM_EXPORTER_DCGM_CLEANUP_TIMEOUT"

// cleanupStep names a wait for DCGM, so a timeout message can say what DCGM
// did not finish: releasing watches, or starting or stopping the GPU
// bind/unbind watch.
type cleanupStep int

const (
	// cleanupReload is the wait for a registry that a reload or GPU reset replaced.
	cleanupReload cleanupStep = iota
	// cleanupShutdown is the wait for the last registry when the exporter stops.
	cleanupShutdown
	// cleanupFailedBuild is the wait for collectors from a failed collector build.
	cleanupFailedBuild
	// cleanupCanceledBuild is the wait for a registry whose build finished
	// after startup, a reload, or GPU recovery was canceled.
	cleanupCanceledBuild
	// cleanupGPUWatchStart is the wait for the GPU bind/unbind watch to start.
	cleanupGPUWatchStart
	// cleanupGPUWatchStop is the wait for the GPU bind/unbind watch to stop.
	cleanupGPUWatchStop
)

// cleanupTimeoutHint is the part of every timeout message that says why DCGM
// may be slow and how to fix it.
const cleanupTimeoutHint = "DCGM can answer slowly, for example while it samples many NVLink fields. Raise " +
	appconfig.DCGMCleanupTimeoutKey + " (--" + appconfig.FlagDCGMCleanupTimeout + ", " + envDCGMCleanupTimeout +
	") or watch fewer NVLink fields."

// cleanupWait is one wait for DCGM: what is waited for, how long, and the
// input that set the time limit, in the wording of validation errors.
type cleanupWait struct {
	step    cleanupStep
	timeout time.Duration
	// source is empty when the input is not known.
	source string
}

// newCleanupWait builds the wait for one step from the effective DCGM cleanup
// timeout of config. source is the input startup recorded for the timeout; it
// is dropped when config holds no positive timeout and the default applies.
func newCleanupWait(config *appconfig.Config, source string, step cleanupStep) cleanupWait {
	timeout, configured := config.EffectiveDCGMCleanupTimeout()
	if !configured {
		source = ""
	}

	return cleanupWait{step: step, timeout: timeout, source: source}
}

// expired returns the error for a wait that ran out of time; cause is what the
// wait itself reported.
func (w cleanupWait) expired(cause error) *dcgmCleanupTimeoutError {
	return &dcgmCleanupTimeoutError{wait: w, cause: cause}
}

// dcgmCleanupTimeoutError reports that DCGM did not finish a wait within the
// cleanup timeout: releasing watches, or starting or stopping the GPU
// bind/unbind watch. The exporter then leaves the old registry and native
// client alone and stops.
type dcgmCleanupTimeoutError struct {
	wait  cleanupWait
	cause error
}

// Error says what DCGM did not finish, how long the exporter waited and which
// input set that time, what the exporter does about it, and how to fix it.
func (e *dcgmCleanupTimeoutError) Error() string {
	const restart = "so it can be restarted with a clean DCGM session"
	const stop = "without closing its DCGM session"

	var what, outcome string
	switch e.wait.step {
	case cleanupShutdown:
		what, outcome = "release the watches at shutdown", stop
	case cleanupFailedBuild:
		what, outcome = "release the watches of collectors from a failed collector build", restart
	case cleanupCanceledBuild:
		what, outcome = "release the watches of a registry build that was canceled", restart
	case cleanupGPUWatchStart:
		what, outcome = "finish starting the GPU bind/unbind watch", restart
	case cleanupGPUWatchStop:
		what, outcome = "finish stopping the GPU bind/unbind watch", stop
	default:
		what, outcome = "release the replaced watches", restart
	}

	within := e.wait.timeout.String()
	if e.wait.source != "" {
		within += " (set by " + e.wait.source + ")"
	}

	return fmt.Sprintf("DCGM did not %s within %s. The exporter is stopping %s. %s",
		what, within, outcome, cleanupTimeoutHint)
}

// Unwrap returns what the wait reported, such as context.DeadlineExceeded.
func (e *dcgmCleanupTimeoutError) Unwrap() error { return e.cause }

// logCleanupFailure logs a failed wait for DCGM under msg, with attrs such as
// the reload ID first and then the error text. A cleanup timeout also gets
// its time limit as cleanup_timeout.
func logCleanupFailure(ctx context.Context, msg string, err error, attrs ...any) {
	attrs = append(attrs, slog.String("error", err.Error()))

	var timeout *dcgmCleanupTimeoutError
	if errors.As(err, &timeout) {
		attrs = append(attrs, slog.Duration("cleanup_timeout", timeout.wait.timeout))
	}

	slog.ErrorContext(ctx, msg, attrs...)
}
