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
	"errors"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/configinspect"
)

// Exit statuses of the process: 1 for any failure, 2 for wrong command usage.
const (
	exitFailure = 1
	exitUsage   = 2
)

// statusError carries the exit status of a failed run. reported says the
// message has already been written to the user, so main must not repeat it.
type statusError struct {
	status   int
	err      error
	reported bool
}

// Error returns the wrapped message with control characters escaped, because
// the message can quote what the user typed, such as a flag, a path or a YAML value.
func (e *statusError) Error() string {
	if e.err == nil {
		return ""
	}

	return configinspect.EscapeText(e.err.Error())
}

// Unwrap returns the wrapped error.
func (e *statusError) Unwrap() error { return e.err }

// usageError marks err as wrong command usage, which exits with status 2.
// If err already carries an exit status, that status is kept: an error that was
// already reported with status 1 stays 1.
func usageError(err error) error {
	if err == nil {
		return nil
	}

	var existing *statusError
	if errors.As(err, &existing) {
		return err
	}

	return &statusError{status: exitUsage, err: err}
}

// reportedFailure ends the run with status after the diagnostics were written.
func reportedFailure(status int, err error) error {
	return &statusError{status: status, err: err, reported: true}
}

// ExitStatus is the process exit status for an error returned by the app:
// 2 for wrong command usage and 1 for everything else.
func ExitStatus(err error) int {
	var status *statusError
	if errors.As(err, &status) {
		return status.status
	}

	return exitFailure
}

// IsUsageError says the run ended because of wrong command usage.
func IsUsageError(err error) bool {
	var status *statusError
	return errors.As(err, &status) && status.status == exitUsage
}

// AlreadyReported says the error's message was already written to the user.
func AlreadyReported(err error) bool {
	var status *statusError
	return errors.As(err, &status) && status.reported
}
