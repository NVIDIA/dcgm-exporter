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

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
)

// collectorRegistrationError preserves the registration failure, asynchronous
// cleanup evidence, and any cleanup failure from rejected collectors.
type collectorRegistrationError struct {
	err               error
	cleanupErr        error
	completionSignals []<-chan struct{}
}

// Error reports the registration failure and any unsafe cleanup result.
func (e collectorRegistrationError) Error() string {
	if e.cleanupErr == nil {
		return e.err.Error()
	}
	return errors.Join(e.err, e.cleanupErr).Error()
}

// Unwrap preserves errors.Is and errors.As for registration and cleanup failures.
func (e collectorRegistrationError) Unwrap() []error {
	if e.cleanupErr == nil {
		return []error{e.err}
	}
	return []error{e.err, e.cleanupErr}
}

// waitForRegistryRetirement starts one registry retirement and waits for its
// completion or the DCGM cleanup timeout. A timeout returns a
// *dcgmCleanupTimeoutError and deliberately leaves the registry and its native
// client intact; the application must end instead of reinitializing DCGM while
// old work may still hold native handles.
func waitForRegistryRetirement(
	ctx context.Context,
	r *registry.Registry,
	wait cleanupWait,
) error {
	if r == nil {
		return nil
	}

	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait.timeout)
	defer cancel()

	select {
	case <-r.Retire():
		if err := r.RetirementError(); err != nil {
			return fmt.Errorf("retire registry: %w", err)
		}
		return nil
	case <-waitCtx.Done():
		return wait.expired(waitCtx.Err())
	}
}

// waitForCollectorRollback reports unsafe cleanup failures or waits for a
// failed candidate's asynchronous cleanup evidence. The DCGM cleanup timeout
// keeps a failed candidate from closing the client beneath a poller that
// started while the factory was constructing later collector families.
// Cancellation cannot abort that poller, so the timeout bounds the wait, and
// running out of time returns a *dcgmCleanupTimeoutError.
func waitForCollectorRollback(ctx context.Context, buildErr error, wait cleanupWait) error {
	var completionSignals []<-chan struct{}
	var registrationErr collectorRegistrationError
	if errors.As(buildErr, &registrationErr) {
		if registrationErr.cleanupErr != nil {
			return fmt.Errorf("registration rollback cleanup failed: %w", registrationErr.cleanupErr)
		}

		completionSignals = registrationErr.completionSignals
	} else {
		var cleanupErr collector.CleanupCompletionError
		if errors.As(buildErr, &cleanupErr) {
			completionSignals = []<-chan struct{}{cleanupErr.CleanupComplete()}
		}
	}

	if len(completionSignals) == 0 {
		return nil
	}

	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait.timeout)
	defer cancel()

	for _, completion := range completionSignals {
		select {
		case <-completion:
		case <-waitCtx.Done():
			return wait.expired(waitCtx.Err())
		}
	}

	return nil
}
