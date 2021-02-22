package main

import (
	"fmt"
	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"math/rand"
)

type ComputeInstanceInfo struct {
	instanceInfo dcgm.MigEntityInfo
	entityId     uint
}

type GpuInstanceInfo struct {
	info             dcgm.MigEntityInfo
	entityId         uint
	computeInstances []ComputeInstanceInfo
}

type GpuInfo struct {
	deviceInfo   dcgm.Device
	gpuInstances []GpuInstanceInfo
}

type SystemInfo struct {
	gpuCount   uint
	gpus       [dcgm.MAX_NUM_DEVICES]GpuInfo
	migEnabled bool
}

func InitializeSystemInfo() (SystemInfo, error) {
	sysInfo := SystemInfo{}
	gpuCount, err := dcgm.GetAllDeviceCount()
	if err != nil {
		return sysInfo, err
	}
	sysInfo.gpuCount = gpuCount

	for i := uint(0); i < sysInfo.gpuCount; i++ {
		sysInfo.gpus[i].deviceInfo, err = dcgm.GetDeviceInfo(i)
		if err != nil {
			return sysInfo, err
		}
	}

	hierarchy, err := dcgm.GetGpuInstanceHierarchy()
	if err != nil {
		return sysInfo, err
	}

	if hierarchy.Count == 0 {
		sysInfo.migEnabled = false
	} else {
		sysInfo.migEnabled = true
	}

	gpuId := uint(0)
	for i := uint(0); i < hierarchy.Count; i++ {
		if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU {
			// We are adding a GPU instance
			gpuId = hierarchy.EntityList[i].Parent.EntityId
			sysInfo.gpus[gpuId].gpuInstances[hierarchy.EntityList[i].Entity.EntityId].info = hierarchy.EntityList[i].Info
		} else if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU_I {
			// Add the compute instance, gpuId is recorded previously
			instanceId := hierarchy.EntityList[i].Parent.EntityId
			entityId := hierarchy.EntityList[i].Entity.EntityId
			sysInfo.gpus[gpuId].gpuInstances[instanceId].computeInstances[entityId] = ComputeInstanceInfo{hierarchy.EntityList[i].Info, entityId}
		}
	}

	return sysInfo, nil
}

func CreateGroupFromSystemInfo(sysInfo SystemInfo) (dcgm.GroupHandle, func(), error) {
	groupId, err := dcgm.CreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	for i := uint(0); i < sysInfo.gpuCount; i++ {
		for _, instance := range sysInfo.gpus[i].gpuInstances {
			err := dcgm.AddEntityToGroup(groupId, dcgm.FE_GPU_I, instance.entityId)
			if err != nil {
				return groupId, func() { dcgm.DestroyGroup(groupId) }, err
			}
			for _, computeInstance := range instance.computeInstances {
				err := dcgm.AddEntityToGroup(groupId, dcgm.FE_GPU_CI, computeInstance.entityId)
				if err != nil {
					return groupId, func() { dcgm.DestroyGroup(groupId) }, err
				}
			}
		}
	}

	return groupId, func() { dcgm.DestroyGroup(groupId) }, nil
}
