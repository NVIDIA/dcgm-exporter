package main

import (
	"fmt"
	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"math/rand"
)

type ComputeInstanceInfo struct {
	InstanceInfo dcgm.MigEntityInfo
	ProfileName  string
	EntityId     uint
}

type GpuInstanceInfo struct {
	Info             dcgm.MigEntityInfo
	ProfileName      string
	EntityId         uint
	ComputeInstances []ComputeInstanceInfo
}

type GpuInfo struct {
	DeviceInfo   dcgm.Device
	GpuInstances []GpuInstanceInfo
}

type SystemInfo struct {
	GpuCount   uint
	Gpus       [dcgm.MAX_NUM_DEVICES]GpuInfo
	MigEnabled bool
}

func SetGpuInstanceProfileName(sysInfo SystemInfo, entityId uint, profileName string) bool {
	for i := uint(0); i < sysInfo.GpuCount; i++ {
		for _, instance := range sysInfo.Gpus[i].GpuInstances {
			if instance.EntityId == entityId {
				instance.ProfileName = profileName
				return true
			}
		}
	}

	return false
}

func PopulateMigProfileNames(sysInfo SystemInfo, entities []dcgm.GroupEntityPair) error {
	var fields []dcgm.Short
	fields = append(fields, dcgm.DCGM_FI_DEV_NAME)
	flags := dcgm.DCGM_FV_FLAG_LIVE_DATA
	values, err := dcgm.EntitiesGetLatestValues(entities, fields, flags)

	if err != nil {
		return err
	}

	notFound := false
	err = fmt.Errorf("Cannot find match for entities:")
	for _, v := range values {
		found := SetGpuInstanceProfileName(sysInfo, v.EntityId, dcgm.Fv2_String(v))
		if found == false {
			err = fmt.Errorf("%s group %d, id %d", err, v.EntityGroupId, v.EntityId)
			notFound = true
		}
	}

	if notFound {
		return err
	}

	return nil
}

func InitializeSystemInfo() (SystemInfo, error) {
	sysInfo := SystemInfo{}
	gpuCount, err := dcgm.GetAllDeviceCount()
	if err != nil {
		return sysInfo, err
	}
	sysInfo.GpuCount = gpuCount

	for i := uint(0); i < sysInfo.GpuCount; i++ {
		sysInfo.Gpus[i].DeviceInfo, err = dcgm.GetDeviceInfo(i)
		if err != nil {
			return sysInfo, err
		}
	}

	hierarchy, err := dcgm.GetGpuInstanceHierarchy()
	if err != nil {
		return sysInfo, err
	}

	if hierarchy.Count == 0 {
		sysInfo.MigEnabled = false
	} else {
		sysInfo.MigEnabled = true
	}

	var entities []dcgm.GroupEntityPair

	gpuId := uint(0)
	for i := uint(0); i < hierarchy.Count; i++ {
		if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU {
			// We are adding a GPU instance
			gpuId := hierarchy.EntityList[i].Parent.EntityId
			entityId := hierarchy.EntityList[i].Entity.EntityId
			sysInfo.Gpus[gpuId].GpuInstances[entityId].Info = hierarchy.EntityList[i].Info
			sysInfo.Gpus[gpuId].GpuInstances[entityId].EntityId = entityId
			entities = append(entities, dcgm.GroupEntityPair{dcgm.FE_GPU_I, entityId})
		} else if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU_I {
			// Add the compute instance, gpuId is recorded previously
			instanceId := hierarchy.EntityList[i].Parent.EntityId
			entityId := hierarchy.EntityList[i].Entity.EntityId
			sysInfo.Gpus[gpuId].GpuInstances[instanceId].ComputeInstances[entityId] = ComputeInstanceInfo{hierarchy.EntityList[i].Info, "", entityId}
		}
	}

	err = PopulateMigProfileNames(sysInfo, entities)
	if err != nil {
		return sysInfo, err
	}

	return sysInfo, nil
}

func CreateGroupFromSystemInfo(sysInfo SystemInfo) (dcgm.GroupHandle, func(), error) {
	groupId, err := dcgm.CreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	for i := uint(0); i < sysInfo.GpuCount; i++ {
		for _, instance := range sysInfo.Gpus[i].GpuInstances {
			err := dcgm.AddEntityToGroup(groupId, dcgm.FE_GPU_I, instance.EntityId)
			if err != nil {
				return groupId, func() { dcgm.DestroyGroup(groupId) }, err
			}
		}
	}

	return groupId, func() { dcgm.DestroyGroup(groupId) }, nil
}
