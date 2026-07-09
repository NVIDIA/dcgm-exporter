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

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

const gpmProbeSampleInterval = 500 * time.Millisecond

// gpuGPMStatus captures the GPM capability of a single GPU as observed at runtime.
type gpuGPMStatus int

const (
	// gpmUnknown means GPM capability could not be determined (for example an NVML
	// handle or query error). Such GPUs are ignored by the DCP decision so a transient
	// lookup failure never disables profiling on its own.
	gpmUnknown gpuGPMStatus = iota
	// gpmNotApplicable means the GPU does not use GPM. Its DCGM_FI_PROF_* fields are
	// served by the DCGM profiling module (DCP), not GPM (for example pre-Hopper GPUs
	// such as the A100), so GPM validation must not disable profiling for it.
	gpmNotApplicable
	// gpmHealthy means the GPU supports GPM and a live sample probe succeeded.
	gpmHealthy
	// gpmBroken means the GPU advertises GPM support but a live sample probe failed.
	// Fractional vGPU guests (for example GKE G4 g4-standard-6/12/24 shapes) exhibit
	// this and trigger the repeated "-14 from m_gpmManager.GetLatestSample" error
	// loop described in issue #661.
	gpmBroken
)

// ValidateGPMSupport inspects every visible GPU and decides whether DCP/profiling
// metric collection should be disabled for this cycle. It returns (true, reason) only
// when disabling profiling is required to stop the GPM error loop and no GPU is able
// to produce profiling metrics.
//
// The decision is intentionally conservative to avoid removing profiling from GPUs
// that still support it:
//   - Remote hostengine deployments are trusted, because local NVML cannot describe
//     GPUs owned by a remote DCGM host.
//   - GPUs that do not use GPM are left untouched; their profiling flows through the
//     DCGM profiling module and is unaffected by GPM sample availability.
//   - Profiling is disabled globally only when at least one GPM-capable GPU fails a
//     live probe AND no other GPU (healthy GPM or non-GPM) can serve profiling.
func ValidateGPMSupport(config *appconfig.Config) (bool, string) {
	// Remote hostengine: GPU UUIDs come from the remote host and cannot be resolved
	// through the local NVML library, so trust DCGM's capability result instead.
	if config != nil && config.UseRemoteHE {
		return false, ""
	}

	gpuCount, err := dcgmprovider.Client().GetAllDeviceCount()
	if err != nil {
		// Unable to enumerate GPUs; keep existing behavior rather than disabling.
		slog.Debug("Skipping GPM validation: could not get GPU count",
			slog.String("error", err.Error()))
		return false, ""
	}
	if gpuCount == 0 {
		return false, ""
	}

	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		slog.Warn("Skipping GPM validation because NVML could not be initialized",
			slog.String("error", nvml.ErrorString(ret)))
		return false, ""
	}
	defer nvml.Shutdown()

	statuses := make([]gpuGPMStatus, 0, gpuCount)
	for gpuID := uint(0); gpuID < gpuCount; gpuID++ {
		statuses = append(statuses, gpmStatusForGPU(gpuID))
	}

	disableDCP, reason := decideDCPFromGPMStatuses(statuses)

	// When profiling is retained but some GPUs cannot serve GPM metrics, surface the
	// partial-coverage situation so operators of heterogeneous nodes understand why a
	// subset of GPUs is missing DCGM_FI_PROF_* values.
	if !disableDCP {
		if broken := countStatus(statuses, gpmBroken); broken > 0 {
			slog.Warn("Some GPUs failed the GPM probe; profiling metrics will be incomplete for those GPUs",
				slog.Int("gpm_broken_gpus", broken),
				slog.Int("total_gpus", int(gpuCount)))
		}
	}

	return disableDCP, reason
}

// gpmStatusForGPU determines the GPM status of a single GPU using NVML.
func gpmStatusForGPU(gpuID uint) gpuGPMStatus {
	gpuInfo, err := dcgmprovider.Client().GetDeviceInfo(gpuID)
	if err != nil {
		slog.Debug("GPM validation: could not get device info",
			slog.Uint64("gpu_id", uint64(gpuID)),
			slog.String("error", err.Error()))
		return gpmUnknown
	}

	device, ret := nvml.DeviceGetHandleByUUID(gpuInfo.UUID)
	if ret != nvml.SUCCESS {
		slog.Debug("GPM validation: could not get NVML handle",
			slog.String("gpu_uuid", gpuInfo.UUID),
			slog.String("error", nvml.ErrorString(ret)))
		return gpmUnknown
	}

	gpmSupport, ret := device.GpmQueryDeviceSupport()
	if ret != nvml.SUCCESS {
		slog.Debug("GPM validation: GPM support query failed",
			slog.String("gpu_uuid", gpuInfo.UUID),
			slog.String("error", nvml.ErrorString(ret)))
		return gpmUnknown
	}
	if gpmSupport.IsSupportedDevice == 0 {
		// GPU does not use GPM for profiling; the DCGM profiling module serves its
		// DCGM_FI_PROF_* fields, so GPM sample availability is irrelevant here.
		return gpmNotApplicable
	}

	if !probeGPMSample(device) {
		return gpmBroken
	}
	return gpmHealthy
}

// decideDCPFromGPMStatuses implements the per-GPU DCP policy. Profiling is disabled
// globally only when at least one GPM-capable GPU is broken and no GPU (healthy GPM,
// non-GPM/DCP-module, or not-yet-determined) can serve profiling metrics.
func decideDCPFromGPMStatuses(statuses []gpuGPMStatus) (bool, string) {
	healthy := countStatus(statuses, gpmHealthy)
	broken := countStatus(statuses, gpmBroken)
	notApplicable := countStatus(statuses, gpmNotApplicable)
	unknown := countStatus(statuses, gpmUnknown)

	if broken > 0 && healthy == 0 && notApplicable == 0 && unknown == 0 {
		return true, fmt.Sprintf(
			"all %d GPM-capable GPU(s) failed the live GPM probe; disabling profiling metrics "+
				"(fractional vGPU guests cannot provide GPM samples, see issue #661)", broken)
	}
	return false, ""
}

func countStatus(statuses []gpuGPMStatus, want gpuGPMStatus) int {
	n := 0
	for _, s := range statuses {
		if s == want {
			n++
		}
	}
	return n
}

// probeGPMSample takes two GPM samples and computes a metric from them to confirm that
// GPM data can actually be retrieved at runtime. Fractional vGPU guests advertise GPM
// support but fail here, which is the runtime signal used to gate profiling.
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
