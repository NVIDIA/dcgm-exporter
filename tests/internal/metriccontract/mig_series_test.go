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

package metriccontract

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Scrape lines in the shape the exporter serves on a MIG node: gpu 0 is a
// whole GPU, gpu 1 has GPU instances 1 and 2.
const (
	wholeGPUPower        = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="0",UUID="GPU-0"} 150`
	wholeGPUFB           = `DCGM_FI_DEV_FB_USED_BYTES{gpu="0",UUID="GPU-0"} 1024`
	wholeGPUTemp         = `DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0"} 40`
	migGPUPower          = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="1",UUID="GPU-1"} 201`
	migGPUPowerWithPod   = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="1",UUID="GPU-1",pod="a"} 201`
	migGPUPCIeTX         = `DCGM_FI_PROF_PCIE_TX_BYTES{gpu="1",UUID="GPU-1"} 704000`
	migGPUFB             = `DCGM_FI_DEV_FB_USED_BYTES{gpu="1",UUID="GPU-1"} 512`
	instance1Power       = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="1",UUID="GPU-1",GPU_I_ID="1",GPU_I_PROFILE="3g.90gb"} 201`
	instance2Power       = `DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="1",UUID="GPU-1",GPU_I_ID="2",GPU_I_PROFILE="3g.90gb"} 201`
	instance1PCIeTX      = `DCGM_FI_PROF_PCIE_TX_BYTES{gpu="1",UUID="GPU-1",GPU_I_ID="1",GPU_I_PROFILE="3g.90gb"} 704000`
	instance1FB          = `DCGM_FI_DEV_FB_USED_BYTES{gpu="1",UUID="GPU-1",GPU_I_ID="1",GPU_I_PROFILE="3g.90gb",GPU_CI_ID="0"} 512`
	instance2FB          = `DCGM_FI_DEV_FB_USED_BYTES{gpu="1",UUID="GPU-1",GPU_I_ID="2",GPU_I_PROFILE="3g.90gb",GPU_CI_ID="0"} 256`
	instance1DRAM        = `DCGM_FI_PROF_DRAM_UTIL_RATIO{gpu="1",UUID="GPU-1",GPU_I_ID="1",GPU_I_PROFILE="3g.90gb"} 0.1`
	instance1NoProfileFB = `DCGM_FI_DEV_FB_USED_BYTES{gpu="1",UUID="GPU-1",GPU_I_ID="1"} 512`
)

// TestValidateMIGSeriesShape checks the MIG series shape rule on fixed and
// broken scrapes.
func TestValidateMIGSeriesShape(t *testing.T) {
	for _, tt := range []struct {
		name    string
		lines   []string
		wantErr string
	}{
		{
			name:  "GPU-level families once per MIG GPU, instance families per instance",
			lines: []string{migGPUPower, migGPUPCIeTX, instance1FB, instance2FB},
		},
		{
			name:  "whole GPU on a mixed node",
			lines: []string{wholeGPUPower, wholeGPUFB, migGPUPower, instance1FB, instance2FB},
		},
		{
			name:    "GPU-level family copied onto instance series",
			lines:   []string{instance1Power, instance2Power, instance1FB, instance2FB},
			wantErr: `GPU-level family "DCGM_FI_DEV_BOARD_POWER_WATTS" has a sample with MIG labels on gpu="1"`,
		},
		{
			name:    "GPU-level profiling field read through an instance",
			lines:   []string{migGPUPower, instance1PCIeTX, instance1FB},
			wantErr: `GPU-level family "DCGM_FI_PROF_PCIE_TX_BYTES" has a sample with MIG labels on gpu="1"`,
		},
		{
			name:    "GPU-level family missing on a MIG GPU",
			lines:   []string{wholeGPUTemp, migGPUPower, instance1FB},
			wantErr: `GPU-level family "DCGM_FI_DEV_GPU_TEMP_CELSIUS" has 0 samples for MIG gpu="1", want 1`,
		},
		{
			name:    "two GPU-level series on one MIG GPU",
			lines:   []string{migGPUPower, migGPUPowerWithPod, instance1FB},
			wantErr: `GPU-level family "DCGM_FI_DEV_BOARD_POWER_WATTS" has 2 samples for MIG gpu="1", want 1`,
		},
		{
			name:    "instance-level family also reported on the MIG GPU",
			lines:   []string{migGPUPower, instance1FB, migGPUFB},
			wantErr: `instance-level family "DCGM_FI_DEV_FB_USED_BYTES" has a sample without MIG labels on gpu="1"`,
		},
		{
			name:    "instance-level family lost its MIG labels",
			lines:   []string{migGPUPower, migGPUFB, instance1DRAM},
			wantErr: `instance-level family "DCGM_FI_DEV_FB_USED_BYTES" has no MIG-labeled sample on gpu="1"`,
		},
		{
			name:    "incomplete MIG labels",
			lines:   []string{migGPUPower, instance1NoProfileFB},
			wantErr: `instance-level family "DCGM_FI_DEV_FB_USED_BYTES" has a sample without MIG labels on gpu="1"`,
		},
		{
			name:    "required GPU-level family missing",
			lines:   []string{migGPUPCIeTX, instance1FB},
			wantErr: `family "DCGM_FI_DEV_BOARD_POWER_WATTS" was not emitted`,
		},
		{
			name:    "no MIG samples",
			lines:   []string{wholeGPUPower, wholeGPUFB},
			wantErr: "no MIG instance DCGM sample was emitted",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			families, err := ParseText([]byte(strings.Join(tt.lines, "\n") + "\n"))
			require.NoError(t, err)

			err = ValidateMIGSeriesShape(families)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestMIGInstances checks that instances are grouped by GPU, once each.
func TestMIGInstances(t *testing.T) {
	families, err := ParseText([]byte(strings.Join([]string{wholeGPUPower, migGPUPower, instance1FB, instance2FB, instance1DRAM}, "\n") + "\n"))
	require.NoError(t, err)

	require.Equal(t, map[string][]string{"1": {"1", "2"}}, sortedInstances(MIGInstances(families)))
}

// sortedInstances sorts each GPU's instance IDs, because family order is random.
func sortedInstances(instances map[string][]string) map[string][]string {
	for gpu := range instances {
		slices.Sort(instances[gpu])
	}
	return instances
}
