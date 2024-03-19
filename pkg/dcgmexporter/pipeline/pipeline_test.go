/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package pipeline

import (
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/collector"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo"
)

var sampleCounters = []common.Counter{
	{dcgm.DCGM_FI_DEV_GPU_TEMP, "DCGM_FI_DEV_GPU_TEMP", "gauge", "Temperature Help info"},
	{dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", "gauge", "Energy help info"},
	{dcgm.DCGM_FI_DEV_POWER_USAGE, "DCGM_FI_DEV_POWER_USAGE", "gauge", "Power help info"},
	{dcgm.DCGM_FI_DRIVER_VERSION, "DCGM_FI_DRIVER_VERSION", "label", "Driver version"},
	/* test that switch and link metrics are filtered out automatically when devices are not detected */
	{
		dcgm.DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT,
		"DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT",
		"gauge",
		"switch temperature",
	},
	{
		dcgm.DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS,
		"DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS",
		"gauge",
		"per-link flit errors",
	},
	/* test that vgpu metrics are not filtered out */
	{dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS, "DCGM_FI_DEV_VGPU_LICENSE_STATUS", "gauge", "vgpu license status"},
	/* test that cpu and cpu core metrics are filtered out automatically when devices are not detected */
	{dcgm.DCGM_FI_DEV_CPU_UTIL_TOTAL, "DCGM_FI_DEV_CPU_UTIL_TOTAL", "gauge", "Total CPU utilization"},
}

func TestNewMetricsPipelineWhenFieldEntityGroupTypeSystemInfoItemIsEmpty(t *testing.T) {
	cleanup, err := dcgm.Init(dcgm.Embedded)
	require.NoError(t, err)
	defer cleanup()

	config := &common.Config{}

	fieldEntityGroupTypeSystemInfo := &sysinfo.FieldEntityGroupTypeSystemInfo{
		Items: map[dcgm.Field_Entity_Group]sysinfo.FieldEntityGroupTypeSystemInfoItem{
			dcgm.FE_GPU:      sysinfo.FieldEntityGroupTypeSystemInfoItem{},
			dcgm.FE_SWITCH:   sysinfo.FieldEntityGroupTypeSystemInfoItem{},
			dcgm.FE_LINK:     sysinfo.FieldEntityGroupTypeSystemInfoItem{},
			dcgm.FE_CPU:      sysinfo.FieldEntityGroupTypeSystemInfoItem{},
			dcgm.FE_CPU_CORE: sysinfo.FieldEntityGroupTypeSystemInfoItem{},
		},
	}

	p, cleanup, err := NewMetricsPipeline(config,
		sampleCounters,
		"",
		func(
			_ []common.Counter, _ string, _ *common.Config, item sysinfo.FieldEntityGroupTypeSystemInfoItem,
		) (*collector.DCGMCollector, func(), error) {
			assert.True(t, item.IsEmpty())
			return nil, func() {}, fmt.Errorf("empty")
		},
		fieldEntityGroupTypeSystemInfo,
	)
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, err)

	out, err := p.run()
	require.NoError(t, err)
	require.Empty(t, out)
}
