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

package devicemonitoring

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

func GetMonitoredEntities(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	switch deviceInfo.InfoType() {
	case dcgm.FE_SWITCH:
		monitoring = monitorAllSwitches(deviceInfo)
	case dcgm.FE_LINK:
		monitoring = monitorAllLinks(deviceInfo)
	case dcgm.FE_CPU:
		monitoring = monitorAllCPUs(deviceInfo)
	case dcgm.FE_CPU_CORE:
		monitoring = monitorAllCPUCores(deviceInfo)
	default:
		if deviceInfo.GOpts().Flex {
			monitoring = monitorAllGPUInstances(deviceInfo, true)
		} else {
			monitoring = handleGPUOptions(deviceInfo)
		}
	}

	return monitoring
}

func handleGPUOptions(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	// Current logic:
	// if MajorRange -1, MinorRange -1: Monitor all GPUs and GPU Instances
	// if MajorRange -1, MinorRange <Some Range>: Monitor all GPU and specific GPU Instances
	// if MajorRange  <Some Range>, MinorRange -1: Monitor specific GPU and all GPU Instances
	// if MajorRange  <Some Range>, MinorRange <Some Range>: Monitor specific GPUs and specific GPU Instances
	if len(deviceInfo.GOpts().MajorRange) > 0 && deviceInfo.GOpts().MajorRange[0] == -1 {
		monitoring = monitorAllGPUs(deviceInfo)
	} else {
		for _, gpuID := range deviceInfo.GOpts().MajorRange {
			// We've already verified that everything in the options list exists
			monitoring = append(monitoring, *monitorGPU(deviceInfo, gpuID))
		}
	}

	if len(deviceInfo.GOpts().MinorRange) > 0 && deviceInfo.GOpts().MinorRange[0] == -1 {
		monitoring = append(monitoring, monitorAllGPUInstances(deviceInfo, false)...)
	} else {
		for _, gpuInstanceID := range deviceInfo.GOpts().MinorRange {
			// We've already verified that everything in the options list exists
			monitoring = append(monitoring, *monitorGPUInstance(deviceInfo, gpuInstanceID))
		}
	}

	return monitoring
}

func monitorAllGPUs(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		mi := Info{
			dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: deviceInfo.GPU(i).DeviceInfo.GPU},
			deviceInfo.GPU(i).DeviceInfo,
			nil,
			PARENT_ID_IGNORED,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

func monitorAllGPUInstances(deviceInfo deviceinfo.Provider, addFlexibly bool) []Info {
	var monitoring []Info

	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		// If the GPU Instance count is 0, addFlexibly allows adding GPU to the monitoring list.
		if addFlexibly && len(deviceInfo.GPU(i).GPUInstances) == 0 {
			mi := Info{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: deviceInfo.GPU(i).DeviceInfo.GPU},
				deviceInfo.GPU(i).DeviceInfo,
				nil,
				PARENT_ID_IGNORED,
			}
			monitoring = append(monitoring, mi)
		} else {
			for j := 0; j < len(deviceInfo.GPU(i).GPUInstances); j++ {
				mi := Info{
					dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      deviceInfo.GPU(i).GPUInstances[j].EntityId,
					},
					deviceInfo.GPU(i).DeviceInfo,
					&deviceInfo.GPU(i).GPUInstances[j],
					PARENT_ID_IGNORED,
				}
				monitoring = append(monitoring, mi)
			}
		}
	}

	return monitoring
}

func monitorAllCPUs(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	for _, cpu := range deviceInfo.CPUs() {
		if !deviceInfo.IsCPUWatched(cpu.EntityId) {
			continue
		}

		mi := Info{
			dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: cpu.EntityId},
			dcgm.Device{},
			nil,
			PARENT_ID_IGNORED,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

func monitorAllCPUCores(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	for _, cpu := range deviceInfo.CPUs() {
		if !deviceInfo.IsCPUWatched(cpu.EntityId) {
			continue
		}

		for _, core := range cpu.Cores {
			if !deviceInfo.IsCoreWatched(core, cpu.EntityId) {
				continue
			}

			mi := Info{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: core},
				dcgm.Device{},
				nil,
				cpu.EntityId,
			}
			monitoring = append(monitoring, mi)
		}
	}

	return monitoring
}

func monitorAllSwitches(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	for _, sw := range deviceInfo.Switches() {
		if !deviceInfo.IsSwitchWatched(sw.EntityId) {
			continue
		}

		mi := Info{
			dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: sw.EntityId},
			dcgm.Device{},
			nil,
			PARENT_ID_IGNORED,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

func monitorAllLinks(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	for _, sw := range deviceInfo.Switches() {
		if !deviceInfo.IsSwitchWatched(sw.EntityId) {
			continue
		}

		for _, link := range sw.NvLinks {
			if link.State != dcgm.LS_UP {
				continue
			}

			if !deviceInfo.IsLinkWatched(link.Index, sw.EntityId) {
				continue
			}

			mi := Info{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: link.Index},
				dcgm.Device{},
				nil,
				link.ParentId,
			}
			monitoring = append(monitoring, mi)
		}
	}

	return monitoring
}

func monitorGPU(deviceInfo deviceinfo.Provider, gpuID int) *Info {
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		if deviceInfo.GPU(i).DeviceInfo.GPU == uint(gpuID) {
			return &Info{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: deviceInfo.GPU(i).DeviceInfo.GPU},
				deviceInfo.GPU(i).DeviceInfo,
				nil,
				PARENT_ID_IGNORED,
			}
		}
	}

	return nil
}

func monitorGPUInstance(deviceInfo deviceinfo.Provider, gpuInstanceID int) *Info {
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		for _, instance := range deviceInfo.GPU(i).GPUInstances {
			if instance.EntityId == uint(gpuInstanceID) {
				return &Info{
					dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: uint(gpuInstanceID)},
					deviceInfo.GPU(i).DeviceInfo,
					&instance,
					PARENT_ID_IGNORED,
				}
			}
		}
	}

	return nil
}
