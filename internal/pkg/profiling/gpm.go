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

// Package profiling validates runtime support for DCGM profiling (DCP/GPM) metrics.
package profiling

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

const gpmProbeSampleInterval = 500 * time.Millisecond

// VirtualizationModeBlocksGPM reports whether the NVML virtualization mode prevents
// collection of GPM-backed DCGM_FI_PROF_* metrics. Fractional vGPU guests (for example
// GKE G4 g4-standard-6/12/24 shapes) run in vGPU mode and cannot provide GPM samples.
func VirtualizationModeBlocksGPM(mode nvml.GpuVirtualizationMode) bool {
	switch mode {
	case nvml.GPU_VIRTUALIZATION_MODE_VGPU, nvml.GPU_VIRTUALIZATION_MODE_HOST_VGPU:
		return true
	default:
		return false
	}
}

// ValidateGPMSupport checks whether GPM profiling metrics can be collected on all
// visible GPUs. When validation fails, callers should disable DCP metric collection
// while continuing to export standard NVML-backed DCGM metrics.
func ValidateGPMSupport() (bool, string) {
	gpuCount, err := dcgmprovider.Client().GetAllDeviceCount()
	if err != nil {
		return false, fmt.Sprintf("failed to get GPU count: %v", err)
	}
	if gpuCount == 0 {
		return false, "no GPUs found"
	}

	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		slog.Warn("Skipping GPM validation because NVML could not be initialized",
			slog.String("error", nvml.ErrorString(ret)))
		return true, ""
	}
	defer nvml.Shutdown()

	for gpuID := uint(0); gpuID < gpuCount; gpuID++ {
		gpuInfo, err := dcgmprovider.Client().GetDeviceInfo(gpuID)
		if err != nil {
			return false, fmt.Sprintf("failed to get device info for GPU %d: %v", gpuID, err)
		}

		device, ret := nvml.DeviceGetHandleByUUID(gpuInfo.UUID)
		if ret != nvml.SUCCESS {
			return false, fmt.Sprintf("failed to get NVML handle for GPU %s: %s", gpuInfo.UUID, nvml.ErrorString(ret))
		}

		if mode, ret := device.GetVirtualizationMode(); ret == nvml.SUCCESS {
			if VirtualizationModeBlocksGPM(mode) {
				return false, fmt.Sprintf(
					"GPU %s is in vGPU virtualization mode; GPM profiling metrics are not available",
					gpuInfo.UUID,
				)
			}
		} else {
			slog.Debug("Could not query GPU virtualization mode",
				slog.String("gpu_uuid", gpuInfo.UUID),
				slog.String("error", nvml.ErrorString(ret)))
		}

		gpmSupport, ret := device.GpmQueryDeviceSupport()
		if ret != nvml.SUCCESS {
			return false, fmt.Sprintf("GPU %s GPM support query failed: %s", gpuInfo.UUID, nvml.ErrorString(ret))
		}
		if gpmSupport.IsSupportedDevice == 0 {
			return false, fmt.Sprintf("GPU %s does not support GPM", gpuInfo.UUID)
		}

		if !probeGPMSample(device) {
			return false, fmt.Sprintf("GPU %s failed GPM sample probe", gpuInfo.UUID)
		}
	}

	return true, ""
}

func probeGPMSample(device nvml.Device) bool {
	sample1, ret := nvml.GpmSampleAlloc()
	if ret != nvml.SUCCESS {
		slog.Debug("GPM sample allocation failed", slog.String("error", nvml.ErrorString(ret)))
		return false
	}
	defer func() {
		if freeRet := nvml.GpmSampleFree(sample1); freeRet != nvml.SUCCESS {
			slog.Debug("GPM sample free failed", slog.String("error", nvml.ErrorString(freeRet)))
		}
	}()

	sample2, ret := nvml.GpmSampleAlloc()
	if ret != nvml.SUCCESS {
		slog.Debug("GPM sample allocation failed", slog.String("error", nvml.ErrorString(ret)))
		return false
	}
	defer func() {
		if freeRet := nvml.GpmSampleFree(sample2); freeRet != nvml.SUCCESS {
			slog.Debug("GPM sample free failed", slog.String("error", nvml.ErrorString(freeRet)))
		}
	}()

	if ret := device.GpmSampleGet(sample1); ret != nvml.SUCCESS {
		slog.Debug("First GPM sample failed", slog.String("error", nvml.ErrorString(ret)))
		return false
	}

	time.Sleep(gpmProbeSampleInterval)

	if ret := device.GpmSampleGet(sample2); ret != nvml.SUCCESS {
		slog.Debug("Second GPM sample failed", slog.String("error", nvml.ErrorString(ret)))
		return false
	}

	metricsGet := nvml.GpmMetricsGetType{
		Sample1:    sample1,
		Sample2:    sample2,
		NumMetrics: 1,
	}
	metricsGet.Metrics[0].MetricId = uint32(nvml.GPM_METRIC_GRAPHICS_UTIL)

	if ret := nvml.GpmMetricsGet(&metricsGet); ret != nvml.SUCCESS {
		slog.Debug("GPM metrics query failed", slog.String("error", nvml.ErrorString(ret)))
		return false
	}
	if metricsGet.Metrics[0].NvmlReturn != uint32(nvml.SUCCESS) {
		slog.Debug("GPM metric returned NVML error", slog.Uint64("nvml_return", uint64(metricsGet.Metrics[0].NvmlReturn)))
		return false
	}

	return true
}
