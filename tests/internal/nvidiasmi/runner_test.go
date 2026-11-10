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

package nvidiasmi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// TestIsMissingSeparatesAMissingBinaryFromOtherFailures fails at once only for
// a binary that is not on PATH.
func TestIsMissingSeparatesAMissingBinaryFromOtherFailures(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, missingErr := nvidiasmi.LocalRunner(context.Background(), "--version")

	if !nvidiasmi.IsMissing(missingErr) {
		t.Errorf("IsMissing(%v) = false, want true", missingErr)
	}
	if nvidiasmi.IsMissing(errors.New("NVML: Unable to determine the device handle")) {
		t.Error("IsMissing(a transient NVML error) = true, want false")
	}
	if nvidiasmi.IsMissing(nil) {
		t.Error("IsMissing(nil) = true, want false")
	}
}
