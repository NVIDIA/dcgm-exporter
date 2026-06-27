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

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/stretchr/testify/assert"
)

func TestVirtualizationModeBlocksGPM(t *testing.T) {
	tests := []struct {
		name  string
		mode  nvml.GpuVirtualizationMode
		block bool
	}{
		{
			name:  "passthrough allows GPM",
			mode:  nvml.GPU_VIRTUALIZATION_MODE_PASSTHROUGH,
			block: false,
		},
		{
			name:  "none allows GPM",
			mode:  nvml.GPU_VIRTUALIZATION_MODE_NONE,
			block: false,
		},
		{
			name:  "vGPU guest blocks GPM",
			mode:  nvml.GPU_VIRTUALIZATION_MODE_VGPU,
			block: true,
		},
		{
			name:  "host vGPU blocks GPM",
			mode:  nvml.GPU_VIRTUALIZATION_MODE_HOST_VGPU,
			block: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.block, VirtualizationModeBlocksGPM(tt.mode))
		})
	}
}
