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

// Package marker formats NVIDIA test result markers.
//
// Marker lines use the NVIDIA harness convention:
//
//	&&&& STATUS parser_facing_name
//
// The e2e CLI emits harness lifecycle markers for setup, suites, and groups.
// Ginkgo suites use tests/internal/resultmarker to emit per-spec
// RUNNING/PASSED/FAILED/WAIVED markers.
package marker

import (
	"fmt"
	"io"
)

// Status is an NVIDIA test marker outcome.
type Status string

const (
	StatusRunning Status = "RUNNING"
	StatusPassed  Status = "PASSED"
	StatusFailed  Status = "FAILED"
	StatusSkipped Status = "SKIPPED"
	StatusWaived  Status = "WAIVED"
)

// Line returns one &&&& marker line.
func Line(status Status, name string) string {
	return fmt.Sprintf("&&&& %s %s", status, name)
}

// Reporter emits &&&& markers.
type Reporter struct {
	w io.Writer
}

// NewReporter creates a marker reporter.
func NewReporter(w io.Writer) Reporter {
	return Reporter{w: w}
}

// Emit writes one marker line with a trailing newline.
func (r Reporter) Emit(status Status, name string) error {
	if r.w == nil {
		return fmt.Errorf("marker reporter writer is nil")
	}
	_, err := fmt.Fprintln(r.w, Line(status, name))
	return err
}
