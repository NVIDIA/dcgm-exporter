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

package metriclabels

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
)

// TestIsBuiltInRecognizesRendererLabels keeps the collision check aligned with
// every label the renderer owns for each supported DCGM entity group.
func TestIsBuiltInRecognizesRendererLabels(t *testing.T) {
	// Each case lists the complete built-in identity for one renderer entity group.
	tests := []struct {
		name      string
		group     dcgm.Field_Entity_Group
		uuidLabel string
		builtIn   []string
	}{
		{
			name:      "GPU labels",
			group:     dcgm.FE_GPU,
			uuidLabel: "custom_uuid",
			builtIn: []string{
				"GPU_CI_ID", "GPU_I_ID", "GPU_I_PROFILE", "custom_uuid",
				"device", "gpu", "hostname", "modelName", "pci_bus_id",
			},
		},
		{
			name:    "switch labels",
			group:   dcgm.FE_SWITCH,
			builtIn: []string{"hostname", "nvswitch"},
		},
		{
			name:  "link labels",
			group: dcgm.FE_LINK,
			builtIn: []string{
				"GPU_I_ID", "GPU_I_PROFILE", "device", "gpu", "gpu_uuid",
				"hostname", "model_name", "nvlink", "nvswitch", "pci_bus_id",
			},
		},
		{
			name:    "CPU labels",
			group:   dcgm.FE_CPU,
			builtIn: []string{"cpu", "cpu_serial", "hostname"},
		},
		{
			name:    "CPU core labels",
			group:   dcgm.FE_CPU_CORE,
			builtIn: []string{"cpu", "cpucore", "hostname"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range tt.builtIn {
				assert.True(t, IsBuiltIn(tt.group, name, tt.uuidLabel), name)
			}

			assert.False(t, IsBuiltIn(tt.group, "app", tt.uuidLabel))
			assert.False(t, IsBuiltIn(tt.group, "", tt.uuidLabel))
		})
	}
}
