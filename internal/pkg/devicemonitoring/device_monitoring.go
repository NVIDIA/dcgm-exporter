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
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// GetMonitoredEntities returns the configured entities, without detached GPUs
// and without adding child compute instances.
func GetMonitoredEntities(deviceInfo deviceinfo.Provider) []Info {
	return getMonitoredEntities(deviceInfo, false)
}

// GetMonitoredEntitiesIncludingComputeInstances returns the configured GPU
// entities and the compute instances belonging to selected GPU instances.
func GetMonitoredEntitiesIncludingComputeInstances(deviceInfo deviceinfo.Provider) []Info {
	return getMonitoredEntities(deviceInfo, true)
}

// GetMonitoredEntitiesAtAllGPUScopes includes each required parent GPU once so
// historical collectors do not read overlapping entity groups.
func GetMonitoredEntitiesAtAllGPUScopes(deviceInfo deviceinfo.Provider) []Info {
	monitoring := GetMonitoredEntitiesIncludingComputeInstances(deviceInfo)
	return append(monitoring, appendMissingParentGPUs(monitoring)...)
}

// GetMonitoredEntitiesForComputeInstanceFields returns selected whole GPUs and
// the compute instances belonging to selected GPU instances. DCGM exposes
// compute-instance fields at both of those scopes, but not on GPU instances.
func GetMonitoredEntitiesForComputeInstanceFields(deviceInfo deviceinfo.Provider) []Info {
	selected := getMonitoredEntities(deviceInfo, false)
	monitoring := make([]Info, 0, len(selected))
	for _, entity := range selected {
		if entity.Entity.EntityGroupId == dcgm.FE_GPU {
			monitoring = append(monitoring, entity)
		}
	}

	return append(monitoring, computeInstancesFor(selected)...)
}

// GetParentGPUsForMonitoredGPUInstances returns parent GPUs that are not
// already included in the configured GPU entities. Callers use this to collect
// a field that DCGM stores on both GPU instances and their parent GPUs.
func GetParentGPUsForMonitoredGPUInstances(deviceInfo deviceinfo.Provider) []Info {
	return appendMissingParentGPUs(getMonitoredEntities(deviceInfo, false))
}

// GetPhysicalGPUs returns the selected whole GPUs and the parent GPUs of
// selected GPU instances, each once. DCGM keeps GPU-level fields on these
// entities, so collectors watch and read those fields here and never on the
// instances.
func GetPhysicalGPUs(deviceInfo deviceinfo.Provider) []Info {
	selected := getMonitoredEntities(deviceInfo, false)
	gpus := make([]Info, 0, len(selected))
	for _, entity := range selected {
		if entity.Entity.EntityGroupId == dcgm.FE_GPU {
			gpus = append(gpus, entity)
		}
	}

	return append(gpus, appendMissingParentGPUs(selected)...)
}

// SelectsGPUInstances reports whether the device selection includes at least
// one MIG GPU instance.
func SelectsGPUInstances(deviceInfo deviceinfo.Provider) bool {
	for _, entity := range getMonitoredEntities(deviceInfo, false) {
		if entity.Entity.EntityGroupId == dcgm.FE_GPU_I {
			return true
		}
	}

	return false
}

// getMonitoredEntities applies the configured device selection and optionally
// adds compute instances below the selected GPU instances. The exported
// helpers choose the option appropriate for their collection scope.
func getMonitoredEntities(deviceInfo deviceinfo.Provider, includeComputeInstances bool) []Info {
	var monitoring []Info

	switch deviceInfo.InfoType() {
	case dcgm.FE_SWITCH:
		monitoring = monitorAllSwitches(deviceInfo)
	case dcgm.FE_LINK:
		var links []Info
		monitoring = monitorAllGPUNvLinks(deviceInfo)
		links = monitorAllNvSwitchNvLinks(deviceInfo)
		monitoring = append(monitoring, links...)
		monitoring = withoutDetachedGPUs(deviceInfo, monitoring)
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
		monitoring = withoutDetachedGPUs(deviceInfo, monitoring)
	}

	if includeComputeInstances {
		monitoring = append(monitoring, computeInstancesFor(monitoring)...)
	}

	return monitoring
}

// withoutDetachedGPUs applies GPU availability after selector validation. The
// same selection supplies watches and samples, including MIG and GPU NVLinks.
func withoutDetachedGPUs(deviceInfo deviceinfo.Provider, monitoring []Info) []Info {
	detached := make(map[uint]bool)
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		gpu := deviceInfo.GPU(i)
		if gpu.Status == dcgm.EntityStatusDetached {
			detached[gpu.DeviceInfo.GPU] = true
		}
	}
	if len(detached) == 0 {
		return monitoring
	}
	return slices.DeleteFunc(monitoring, func(entity Info) bool {
		if entity.Entity.EntityGroupId == dcgm.FE_LINK && entity.ParentType != dcgm.FE_GPU {
			return false
		}
		return detached[entity.DeviceInfo.GPU]
	})
}

// computeInstancesFor returns one monitored entity for each compute instance
// below an already selected GPU instance. It preserves the parent identity so
// metric rendering can keep the GPU-instance labels.
func computeInstancesFor(monitoring []Info) []Info {
	computeInstances := make([]Info, 0)
	for _, monitored := range monitoring {
		if monitored.Entity.EntityGroupId != dcgm.FE_GPU_I || monitored.InstanceInfo == nil {
			continue
		}

		for index := range monitored.InstanceInfo.ComputeInstances {
			computeInstance := &monitored.InstanceInfo.ComputeInstances[index]
			computeInstances = append(computeInstances, Info{
				Entity: dcgm.GroupEntityPair{
					EntityGroupId: dcgm.FE_GPU_CI,
					EntityId:      computeInstance.EntityId,
				},
				DeviceInfo:          monitored.DeviceInfo,
				InstanceInfo:        monitored.InstanceInfo,
				ComputeInstanceInfo: computeInstance,
				ParentId:            monitored.Entity.EntityId,
				ParentType:          dcgm.FE_GPU_I,
			})
		}
	}

	return computeInstances
}

// appendMissingParentGPUs adds each parent GPU required to read a multi-scope
// field when that GPU was not selected directly. It avoids duplicate parent
// reads when a GPU and one of its instances are both selected.
func appendMissingParentGPUs(monitoring []Info) []Info {
	knownGPUs := make(map[uint]struct{})
	for _, monitored := range monitoring {
		if monitored.Entity.EntityGroupId == dcgm.FE_GPU {
			knownGPUs[monitored.DeviceInfo.GPU] = struct{}{}
		}
	}

	parents := make([]Info, 0)
	for _, monitored := range monitoring {
		if monitored.Entity.EntityGroupId != dcgm.FE_GPU_I {
			continue
		}

		gpuID := monitored.DeviceInfo.GPU
		if _, exists := knownGPUs[gpuID]; exists {
			continue
		}

		parents = append(parents, Info{
			Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: gpuID},
			DeviceInfo: monitored.DeviceInfo,
			ParentId:   PARENT_ID_IGNORED,
			ParentType: dcgm.FE_NONE,
		})
		knownGPUs[gpuID] = struct{}{}
	}
	return parents
}

// handleGPUOptions resolves the configured whole-GPU and GPU-instance selections.
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

// monitorAllGPUs returns one monitored entity for each GPU in the provider.
func monitorAllGPUs(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		mi := Info{
			dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: deviceInfo.GPU(i).DeviceInfo.GPU},
			deviceInfo.GPU(i).DeviceInfo,
			nil,
			nil,
			PARENT_ID_IGNORED,
			dcgm.FE_NONE,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

// monitorAllGPUInstances returns every GPU instance, also including whole GPUs
// without instances when addFlexibly is set.
func monitorAllGPUInstances(deviceInfo deviceinfo.Provider, addFlexibly bool) []Info {
	var monitoring []Info

	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		// If the GPU Instance count is 0, addFlexibly allows adding GPU to the monitoring list.
		if addFlexibly && len(deviceInfo.GPU(i).GPUInstances) == 0 {
			mi := Info{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: deviceInfo.GPU(i).DeviceInfo.GPU},
				deviceInfo.GPU(i).DeviceInfo,
				nil,
				nil,
				PARENT_ID_IGNORED,
				dcgm.FE_NONE,
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
					nil,
					PARENT_ID_IGNORED,
					dcgm.FE_GPU,
				}
				monitoring = append(monitoring, mi)
			}
		}
	}

	return monitoring
}

// monitorAllCPUs returns one monitored entity for every watched CPU.
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
			nil,
			PARENT_ID_IGNORED,
			dcgm.FE_NONE,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

// monitorAllCPUCores returns one monitored entity for each watched core of a watched CPU.
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
				nil,
				cpu.EntityId,
				dcgm.FE_CPU,
			}
			monitoring = append(monitoring, mi)
		}
	}

	return monitoring
}

// monitorAllSwitches returns one monitored entity for every watched switch.
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
			nil,
			PARENT_ID_IGNORED,
			dcgm.FE_NONE,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

// monitorAllNvSwitchNvLinks returns each up, watched link belonging to a watched switch.
func monitorAllNvSwitchNvLinks(deviceInfo deviceinfo.Provider) []Info {
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
				nil,
				link.ParentId,
				dcgm.FE_SWITCH,
			}
			monitoring = append(monitoring, mi)
		}
	}

	return monitoring
}

// monitorAllGPUNvLinks returns each up link belonging to a GPU.
func monitorAllGPUNvLinks(deviceInfo deviceinfo.Provider) []Info {
	var monitoring []Info

	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		for _, link := range deviceInfo.GPU(i).NvLinks {
			if link.State != dcgm.LS_UP {
				continue
			}

			mi := Info{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: link.Index},
				deviceInfo.GPU(i).DeviceInfo,
				nil,
				nil,
				link.ParentId,
				dcgm.FE_GPU,
			}
			monitoring = append(monitoring, mi)
		}
	}

	return monitoring
}

// monitorGPU returns the selected GPU and its device metadata, or nil when no
// GPU has that ID.
func monitorGPU(deviceInfo deviceinfo.Provider, gpuID int) *Info {
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		if deviceInfo.GPU(i).DeviceInfo.GPU == uint(gpuID) {
			return &Info{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: deviceInfo.GPU(i).DeviceInfo.GPU},
				deviceInfo.GPU(i).DeviceInfo,
				nil,
				nil,
				PARENT_ID_IGNORED,
				dcgm.FE_NONE,
			}
		}
	}

	return nil
}

// monitorGPUInstance returns the selected GPU instance and its parent metadata,
// or nil when no GPU instance has that ID.
func monitorGPUInstance(deviceInfo deviceinfo.Provider, gpuInstanceID int) *Info {
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		for _, instance := range deviceInfo.GPU(i).GPUInstances {
			if instance.EntityId == uint(gpuInstanceID) {
				return &Info{
					dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: uint(gpuInstanceID)},
					deviceInfo.GPU(i).DeviceInfo,
					&instance,
					nil,
					PARENT_ID_IGNORED,
					dcgm.FE_GPU,
				}
			}
		}
	}

	return nil
}
