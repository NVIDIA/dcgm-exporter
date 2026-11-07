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

// Package testdcgm initializes DCGM for hardware-backed tests without adding
// test-only APIs or testing dependencies to production packages.
package testdcgm

import (
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// Initialize prepares the process-wide DCGM client for a hardware-backed
// test. It tries embedded DCGM first, then the local standalone host engine,
// and skips the test when neither option is available.
func Initialize(t *testing.T, config *appconfig.Config) {
	t.Helper()
	if dcgmprovider.Client() != nil {
		return
	}

	config.UseRemoteHE = false
	if err := dcgmprovider.Initialize(config); err == nil {
		return
	}

	config.UseRemoteHE = true
	config.RemoteHEInfo = "localhost:5555"
	if err := dcgmprovider.Initialize(config); err != nil {
		t.Skipf("Skipping test: embedded and local standalone DCGM initialization failed: %v", err)
	}
}
