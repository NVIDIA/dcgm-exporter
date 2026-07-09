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

package profiling

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestDecideDCPFromGPMStatuses(t *testing.T) {
	tests := []struct {
		name        string
		statuses    []gpuGPMStatus
		wantDisable bool
	}{
		{
			name:        "no GPUs keeps profiling",
			statuses:    nil,
			wantDisable: false,
		},
		{
			name:        "single non-GPM GPU (pre-Hopper A100) keeps profiling via DCP module",
			statuses:    []gpuGPMStatus{gpmNotApplicable},
			wantDisable: false,
		},
		{
			name:        "single healthy GPM GPU (full GPU shape) keeps profiling",
			statuses:    []gpuGPMStatus{gpmHealthy},
			wantDisable: false,
		},
		{
			name:        "single broken GPM GPU (fractional vGPU) disables profiling",
			statuses:    []gpuGPMStatus{gpmBroken},
			wantDisable: true,
		},
		{
			name:        "homogeneous fractional vGPU node disables profiling",
			statuses:    []gpuGPMStatus{gpmBroken, gpmBroken, gpmBroken, gpmBroken},
			wantDisable: true,
		},
		{
			name:        "mixed healthy and broken GPM keeps profiling for healthy GPUs",
			statuses:    []gpuGPMStatus{gpmHealthy, gpmBroken},
			wantDisable: false,
		},
		{
			name:        "non-GPM plus broken GPM keeps profiling for the DCP-module GPU",
			statuses:    []gpuGPMStatus{gpmNotApplicable, gpmBroken},
			wantDisable: false,
		},
		{
			name:        "unknown plus broken stays conservative and keeps profiling",
			statuses:    []gpuGPMStatus{gpmUnknown, gpmBroken},
			wantDisable: false,
		},
		{
			name:        "all unknown keeps profiling",
			statuses:    []gpuGPMStatus{gpmUnknown},
			wantDisable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disable, reason := decideDCPFromGPMStatuses(tt.statuses)
			assert.Equal(t, tt.wantDisable, disable)
			if tt.wantDisable {
				assert.NotEmpty(t, reason, "a disable decision must carry a reason")
			} else {
				assert.Empty(t, reason, "a keep decision must not carry a reason")
			}
		})
	}
}

func TestCountStatus(t *testing.T) {
	statuses := []gpuGPMStatus{gpmHealthy, gpmBroken, gpmBroken, gpmNotApplicable, gpmUnknown}

	assert.Equal(t, 1, countStatus(statuses, gpmHealthy))
	assert.Equal(t, 2, countStatus(statuses, gpmBroken))
	assert.Equal(t, 1, countStatus(statuses, gpmNotApplicable))
	assert.Equal(t, 1, countStatus(statuses, gpmUnknown))
	assert.Equal(t, 0, countStatus(nil, gpmBroken))
}

// TestValidateGPMSupportRemoteHostengine verifies that remote hostengine deployments
// skip local NVML validation entirely: local NVML cannot resolve GPUs owned by a
// remote DCGM host, so DCGM's own capability result must be trusted.
func TestValidateGPMSupportRemoteHostengine(t *testing.T) {
	disable, reason := ValidateGPMSupport(&appconfig.Config{UseRemoteHE: true})
	assert.False(t, disable)
	assert.Empty(t, reason)
}
