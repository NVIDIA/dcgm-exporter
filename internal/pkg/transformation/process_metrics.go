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

package transformation

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

func isPerProcessMetric(fieldName string) bool {
	return fieldName == metricGPUUtil || fieldName == metricFBUsed || fieldName == metricGREngineActive
}

// getGPUUUIDToDeviceID builds a mapping from GPU UUID to device ID based on the specified ID type.
func getGPUUUIDToDeviceID(devInfo deviceinfo.Provider, idType appconfig.KubernetesGPUIDType) map[string]string {
	result := make(map[string]string, devInfo.GPUCount())
	for i := uint(0); i < devInfo.GPUCount(); i++ {
		gpu := devInfo.GPU(i)
		uuid := gpu.DeviceInfo.UUID

		var deviceID string
		switch idType {
		case appconfig.GPUUID:
			deviceID = uuid
		default:
			deviceID = fmt.Sprintf("nvidia%d", gpu.DeviceInfo.GPU)
		}

		result[uuid] = deviceID
	}
	return result
}

type perProcessMetrics struct {
	pidToSMUtil map[uint32]uint32
	pidToMemory map[uint32]uint64
}

func (c *perProcessCollector) processRegularGPU(gpuUUID string, podInfos []PodInfo) (*perProcessMetrics, map[uint32]*PodInfo) {
	var err error
	data := &perProcessMetrics{}

	data.pidToMemory, err = c.client.GetDeviceProcessMemory(gpuUUID)
	if err != nil {
		slog.Debug("Failed to get process memory", "gpuUUID", gpuUUID, "error", err)
	}

	data.pidToSMUtil, err = c.client.GetDeviceProcessUtilization(gpuUUID)
	if err != nil {
		slog.Debug("Failed to get process utilization", "gpuUUID", gpuUUID, "error", err)
	}

	pidToPod := c.pidMapper.buildPIDToPodMap(data.getAllPIDs(), podInfos)
	return data, pidToPod
}

func (m *perProcessMetrics) getAllPIDs() []uint32 {
	pidSet := make(map[uint32]struct{})
	for pid := range m.pidToSMUtil {
		pidSet[pid] = struct{}{}
	}
	for pid := range m.pidToMemory {
		pidSet[pid] = struct{}{}
	}
	return slices.Collect(maps.Keys(pidSet))
}

func (m *perProcessMetrics) getValueForMetric(fieldName string, pid uint32) (string, bool) {
	switch fieldName {
	case metricGPUUtil:
		if util, ok := m.pidToSMUtil[pid]; ok {
			return fmt.Sprintf("%d", util), true
		}
	case metricFBUsed:
		if mem, ok := m.pidToMemory[pid]; ok {
			memMiB := mem / (1024 * 1024)
			return fmt.Sprintf("%d", memMiB), true
		}
	}
	return "", false
}

type perProcessDataMap struct {
	metrics      map[string]*perProcessMetrics // keyed by GPU UUID or "<parentUUID>/<gpuInstanceID>" for MIG
	pidToPod     map[uint32]*PodInfo
	deviceToPods map[string][]PodInfo // keyed by GPU UUID or "<parentUUID>/<gpuInstanceID>" for MIG
}

type PIDMapper interface {
	buildPIDToPodMap(pids []uint32, pods []PodInfo) map[uint32]*PodInfo
}

type perProcessCollector struct {
	client    nvmlprovider.NVML
	pidMapper PIDMapper
}

func getMIGMetricsKey(parentUUID string, gpuInstanceID string) string {
	return parentUUID + "/" + gpuInstanceID
}

func (c *perProcessCollector) processMIGEnabledGPU(
	gpu deviceinfo.GPUInfo,
	deviceToPods map[string][]PodInfo,
) (map[string]*perProcessMetrics, map[uint32]*PodInfo, map[string][]PodInfo) {
	gpuUUID := gpu.DeviceInfo.UUID
	gpuIndex := gpu.DeviceInfo.GPU

	allMIGProcessMemory, err := c.client.GetAllMIGDevicesProcessMemory(gpuUUID)
	if err != nil {
		slog.Debug("Failed to get MIG device process memory", "gpuUUID", gpuUUID, "error", err)
		return nil, nil, nil
	}

	metrics := make(map[string]*perProcessMetrics)
	pidToPod := make(map[uint32]*PodInfo)
	migKeyToPods := make(map[string][]PodInfo)

	for _, instance := range gpu.GPUInstances {
		gpuInstanceID := instance.Info.NvmlInstanceId
		migDeviceID := fmt.Sprintf("%d-%d", gpuIndex, gpuInstanceID)
		podInfos := deviceToPods[migDeviceID]

		if len(podInfos) == 0 {
			continue
		}

		data := &perProcessMetrics{pidToMemory: allMIGProcessMemory[gpuInstanceID]}
		migKey := getMIGMetricsKey(gpuUUID, fmt.Sprintf("%d", gpuInstanceID))
		metrics[migKey] = data
		migKeyToPods[migKey] = podInfos
		maps.Copy(pidToPod, c.pidMapper.buildPIDToPodMap(data.getAllPIDs(), podInfos))
	}

	return metrics, pidToPod, migKeyToPods
}

func (c *perProcessCollector) Collect(gpuDeviceMap map[string]string, deviceToPods map[string][]PodInfo, devInfo deviceinfo.Provider) *perProcessDataMap {
	result := &perProcessDataMap{
		metrics:      make(map[string]*perProcessMetrics),
		pidToPod:     make(map[uint32]*PodInfo),
		deviceToPods: make(map[string][]PodInfo),
	}

	if devInfo == nil || c.client == nil {
		return result
	}

	for i := uint(0); i < devInfo.GPUCount(); i++ {
		gpu := devInfo.GPU(i)
		gpuUUID := gpu.DeviceInfo.UUID

		if len(gpu.GPUInstances) > 0 {
			metrics, pidToPod, keyToPods := c.processMIGEnabledGPU(gpu, deviceToPods)
			maps.Copy(result.metrics, metrics)
			maps.Copy(result.pidToPod, pidToPod)
			maps.Copy(result.deviceToPods, keyToPods)
		} else {
			deviceID := gpuDeviceMap[gpuUUID]
			podInfos := deviceToPods[deviceID]
			if len(podInfos) == 0 {
				continue
			}
			data, pidToPod := c.processRegularGPU(gpuUUID, podInfos)
			result.metrics[gpuUUID] = data
			result.deviceToPods[gpuUUID] = podInfos
			maps.Copy(result.pidToPod, pidToPod)
		}
	}

	return result
}
