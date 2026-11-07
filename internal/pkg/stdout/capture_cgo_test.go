//go:build cgo

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

package stdout

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/stdout/internal/testhelper"
)

// TestCaptureWithCGO checks that Capture forwards stdout written by C code.
// It lives in a cgo-only file so non-CGO test runs retain the pure-Go capture
// tests without importing a package that has no non-CGO implementation.
func TestCaptureWithCGO(t *testing.T) {
	if os.Getenv(captureTestScenarioEnv) == "cgo" {
		require.NoError(t, Capture(context.Background(), func() error {
			testhelper.WriteCaptureTestMessage()
			return nil
		}))
		return
	}

	stdout, _ := runCaptureSubprocess(t, "TestCaptureWithCGO", "cgo")
	assert.Equal(t, 1, strings.Count(stdout, "C output\n"))
}
