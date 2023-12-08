/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
)

var sampleCounters = []Counter{
	{dcgm.DCGM_FI_DEV_GPU_TEMP, "DCGM_FI_DEV_GPU_TEMP", "gauge", "Temperature Help info"},
	{dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", "gauge", "Energy help info"},
	{dcgm.DCGM_FI_DEV_POWER_USAGE, "DCGM_FI_DEV_POWER_USAGE", "gauge", "Power help info"},
	{dcgm.DCGM_FI_DRIVER_VERSION, "DCGM_FI_DRIVER_VERSION", "label", "Driver version"},
	/* test that switch and link metrics are filtered out automatically when devices are not detected */
	{dcgm.DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT, "DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT", "gauge", "switch temperature"},
	{dcgm.DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS, "DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS", "gauge", "per-link flit errors"},
	/* test that vgpu metrics are not filtered out */
	{dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS, "DCGM_FI_DEV_VGPU_LICENSE_STATUS", "gauge", "vgpu license status"},
}

var expectedMetrics = map[string]bool{
	"DCGM_FI_DEV_GPU_TEMP":                 true,
	"DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION": true,
	"DCGM_FI_DEV_POWER_USAGE":              true,
	"DCGM_FI_DEV_VGPU_LICENSE_STATUS":      true,
}

func TestDCGMCollector(t *testing.T) {
	cleanup, err := dcgm.Init(dcgm.Embedded)
	require.NoError(t, err)
	defer cleanup()

	_, cleanup = testDCGMCollector(t, sampleCounters)
	cleanup()
}

func testDCGMCollector(t *testing.T, counters []Counter) (*DCGMCollector, func()) {
	dOpt := DeviceOptions{true, []int{-1}, []int{-1}}
	cfg := Config{
		GPUDevices:      dOpt,
		NoHostname:      false,
		UseOldNamespace: false,
		UseFakeGpus:     false,
	}

	dcgmGetAllDeviceCount = func() (uint, error) {
		return 1, nil
	}

	dcgmGetDeviceInfo = func(gpuId uint) (dcgm.Device, error) {
		dev := dcgm.Device{
			GPU:  0,
			UUID: fmt.Sprintf("fake%d", gpuId),
		}

		return dev, nil
	}

	dcgmGetGpuInstanceHierarchy = func() (dcgm.MigHierarchy_v2, error) {
		hierarchy := dcgm.MigHierarchy_v2{
			Count: 0,
		}
		return hierarchy, nil
	}

	dcgmAddEntityToGroup = func(groupId dcgm.GroupHandle, entityGroupId dcgm.Field_Entity_Group, entityId uint) (err error) {
		return nil
	}

	defer func() {
		dcgmGetAllDeviceCount = dcgm.GetAllDeviceCount
		dcgmGetDeviceInfo = dcgm.GetDeviceInfo
		dcgmGetGpuInstanceHierarchy = dcgm.GetGpuInstanceHierarchy
		dcgmAddEntityToGroup = dcgm.AddEntityToGroup
	}()

	c, cleanup, err := NewDCGMCollector(counters, &cfg, dcgm.FE_GPU)
	require.NoError(t, err)

	/* Test for error when no switches are available to monitor.
	   NOTE: This test will fail on a system with switches present. */
	_, _, err = NewDCGMCollector(counters, &cfg, dcgm.FE_SWITCH)
	require.Error(t, err)

	out, err := c.GetMetrics()
	require.NoError(t, err)
	require.Greater(t, len(out), 0, "Check that you have a GPU on this node")
	require.Len(t, out[0], len(expectedMetrics))

	for i, dev := range out {
		seenMetrics := map[string]bool{}
		for _, metric := range dev {
			seenMetrics[metric.Counter.FieldName] = true
			require.Equal(t, metric.GPU, fmt.Sprintf("%d", i))

			require.NotEmpty(t, metric.Value)
			require.NotEqual(t, metric.Value, FailedToConvert)
		}
		require.Equal(t, seenMetrics, expectedMetrics)
	}

	return c, cleanup
}
