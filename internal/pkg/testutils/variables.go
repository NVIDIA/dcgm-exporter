/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package testutils

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	osinterface "github.com/NVIDIA/dcgm-exporter/internal/pkg/os"
)

var os osinterface.OS = osinterface.RealOS{}

var (
	SampleGPUTempCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
		FieldName: "DCGM_FI_DEV_GPU_TEMP",
		PromType:  "gauge",
		Help:      "Temperature Help info",
	}

	SampleGPUTotalEnergyCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION,
		FieldName: "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION",
		PromType:  "gauge",
		Help:      "Energy help info",
	}

	SampleGPUPowerUsageCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_POWER_USAGE,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
		Help:      "Power help info",
	}

	SampleVGPULicenseStatusCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS,
		FieldName: "DCGM_FI_DEV_VGPU_LICENSE_STATUS",
		PromType:  "gauge",
		Help:      "vgpu license status",
	}

	SampleDriverVersionCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DRIVER_VERSION,
		FieldName: "DCGM_FI_DRIVER_VERSION",
		PromType:  "label",
		Help:      "Driver version",
	}

	SampleSwitchCurrentTempCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT,
		FieldName: "DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT",
		PromType:  "gauge",
		Help:      "switch temperature",
	}

	SampleSwitchLinkFlitErrorsCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS,
		FieldName: "DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS",
		PromType:  "gauge",
		Help:      "per-link flit errors",
	}

	SampleCPUUtilTotalCounter = counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_CPU_UTIL_TOTAL,
		FieldName: "DCGM_FI_DEV_CPU_UTIL_TOTAL",
		PromType:  "gauge",
		Help:      "Total CPU utilization",
	}

	SampleCounters = []counters.Counter{
		SampleGPUTempCounter,
		SampleGPUTotalEnergyCounter,
		SampleGPUPowerUsageCounter,
		SampleDriverVersionCounter,
		/* test that switch and link metrics are filtered out automatically when devices are not detected */
		SampleSwitchCurrentTempCounter,
		SampleSwitchLinkFlitErrorsCounter,
		/* test that vgpu metrics are not filtered out */
		SampleVGPULicenseStatusCounter,
		/* test that cpu and cpu core metrics are filtered out automatically when devices are not detected */
		SampleCPUUtilTotalCounter,
	}

	SampleAllFieldIDs = []dcgm.Short{
		SampleGPUTempCounter.FieldID, SampleGPUTotalEnergyCounter.FieldID,
		SampleGPUPowerUsageCounter.FieldID, SampleVGPULicenseStatusCounter.FieldID,
		SampleDriverVersionCounter.FieldID, SampleSwitchCurrentTempCounter.FieldID,
		SampleSwitchLinkFlitErrorsCounter.FieldID, SampleCPUUtilTotalCounter.FieldID,
	}

	SampleGPUFieldIDs = []dcgm.Short{
		SampleGPUTempCounter.FieldID, SampleGPUTotalEnergyCounter.FieldID,
		SampleGPUPowerUsageCounter.FieldID, SampleVGPULicenseStatusCounter.FieldID,
	}

	SampleFieldIDToFieldMeta = map[dcgm.Short]dcgm.FieldMeta{
		SampleGPUTempCounter.FieldID: {
			FieldID:     SampleGPUTempCounter.FieldID,
			EntityLevel: dcgm.FE_GPU,
		},
		SampleGPUTotalEnergyCounter.FieldID: {
			FieldID:     SampleGPUTotalEnergyCounter.FieldID,
			EntityLevel: dcgm.FE_GPU,
		},
		SampleGPUPowerUsageCounter.FieldID: {
			FieldID:     SampleGPUPowerUsageCounter.FieldID,
			EntityLevel: dcgm.FE_GPU_I,
		},
		SampleVGPULicenseStatusCounter.FieldID: {
			FieldID:     SampleVGPULicenseStatusCounter.FieldID,
			EntityLevel: dcgm.FE_VGPU,
		},
		SampleDriverVersionCounter.FieldID: {
			FieldID:     SampleDriverVersionCounter.FieldID,
			EntityLevel: dcgm.FE_NONE,
		},
		SampleSwitchCurrentTempCounter.FieldID: {
			FieldID:     SampleSwitchCurrentTempCounter.FieldID,
			EntityLevel: dcgm.FE_SWITCH,
		},
		SampleSwitchLinkFlitErrorsCounter.FieldID: {
			FieldID:     SampleSwitchLinkFlitErrorsCounter.FieldID,
			EntityLevel: dcgm.FE_LINK,
		},
		SampleCPUUtilTotalCounter.FieldID: {
			FieldID:     SampleCPUUtilTotalCounter.FieldID,
			EntityLevel: dcgm.FE_CPU_CORE,
		},
	}
)
