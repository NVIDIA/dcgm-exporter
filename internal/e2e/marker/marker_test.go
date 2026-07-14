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

package marker

import (
	"bytes"
	"testing"
)

func TestLine(t *testing.T) {
	got := Line(StatusSkipped, "test_kubernetes_nvlink")
	want := "&&&& SKIPPED test_kubernetes_nvlink"
	if got != want {
		t.Fatalf("Line() = %q, want %q", got, want)
	}
}

func TestLineStatuses(t *testing.T) {
	for _, status := range []Status{StatusRunning, StatusPassed, StatusFailed, StatusSkipped, StatusWaived} {
		want := "&&&& " + string(status) + " dcgm_exporter_e2e_marker"
		if got := Line(status, "dcgm_exporter_e2e_marker"); got != want {
			t.Fatalf("Line(%s) = %q, want %q", status, got, want)
		}
	}
}

func TestReporterEmit(t *testing.T) {
	var out bytes.Buffer
	reporter := NewReporter(&out)
	if err := reporter.Emit(StatusPassed, "dcgm_exporter_e2e_default"); err != nil {
		t.Fatal(err)
	}

	want := "&&&& PASSED dcgm_exporter_e2e_default\n"
	if out.String() != want {
		t.Fatalf("Emit() wrote %q, want %q", out.String(), want)
	}
}

func TestReporterEmitRejectsNilWriter(t *testing.T) {
	if err := (Reporter{}).Emit(StatusPassed, "dcgm_exporter_e2e_default"); err == nil {
		t.Fatal("Emit() error = nil, want nil writer error")
	}
}
