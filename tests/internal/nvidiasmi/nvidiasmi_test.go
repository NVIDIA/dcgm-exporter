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
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// TestFabricHealth distinguishes healthy, unhealthy, and absent Fabric reports.
func TestFabricHealth(t *testing.T) {
	healthyOutput := `
GPU 00000000:01:00.0
    Fabric
        State                       : Completed
        Status                      : Success
`
	reported, healthy, evidence := nvidiasmi.FabricHealth(healthyOutput)
	if !reported || !healthy {
		t.Fatalf("FabricHealth() = %v, %v, %q, want reported healthy", reported, healthy, evidence)
	}

	unhealthyOutput := `
GPU 00000000:01:00.0
    Fabric
        State                       : Completed
        Status                      : Failure
`
	reported, healthy, evidence = nvidiasmi.FabricHealth(unhealthyOutput)
	if !reported || healthy {
		t.Fatalf("FabricHealth() = %v, %v, %q, want reported unhealthy", reported, healthy, evidence)
	}

	reported, healthy, evidence = nvidiasmi.FabricHealth("no fabric here")
	if reported || healthy || evidence != "" {
		t.Fatalf("FabricHealth() = %v, %v, %q, want unreported", reported, healthy, evidence)
	}
}
