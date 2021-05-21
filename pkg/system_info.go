/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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
	dOpt       DeviceOptions
}

type MonitoringInfo struct {
	Entity       dcgm.GroupEntityPair
	DeviceInfo   dcgm.Device
	InstanceInfo *GpuInstanceInfo
}

func SetGpuInstanceProfileName(sysInfo *SystemInfo, entityId uint, profileName string) bool {
	for i := uint(0); i < sysInfo.GpuCount; i++ {
		for j := range sysInfo.Gpus[i].GpuInstances {
			if sysInfo.Gpus[i].GpuInstances[j].EntityId == entityId {
				sysInfo.Gpus[i].GpuInstances[j].ProfileName = profileName
				return true
			}
		}
	}

	return false
}

func SetMigProfileNames(sysInfo *SystemInfo, values []dcgm.FieldValue_v2) error {
	notFound := false
	err := fmt.Errorf("Cannot find match for entities:")
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

func PopulateMigProfileNames(sysInfo *SystemInfo, entities []dcgm.GroupEntityPair) error {
	if len(entities) == 0 {
		// There are no entities to populate
		return nil
	}

	var fields []dcgm.Short
	fields = append(fields, dcgm.DCGM_FI_DEV_NAME)
	flags := dcgm.DCGM_FV_FLAG_LIVE_DATA
	values, err := dcgm.EntitiesGetLatestValues(entities, fields, flags)

	if err != nil {
		return err
	}

	return SetMigProfileNames(sysInfo, values)
}

func GpuIdExists(sysInfo *SystemInfo, gpuId int) bool {
	for i := uint(0); i < sysInfo.GpuCount; i++ {
		if sysInfo.Gpus[i].DeviceInfo.GPU == uint(gpuId) {
			return true
		}
	}
	return false
}

func GpuInstanceIdExists(sysInfo *SystemInfo, gpuInstanceId int) bool {
	for i := uint(0); i < sysInfo.GpuCount; i++ {
		for _, instance := range sysInfo.Gpus[i].GpuInstances {
			if instance.EntityId == uint(gpuInstanceId) {
				return true
			}
		}
	}
	return false
}

func VerifyDevicePresence(sysInfo *SystemInfo, dOpt DeviceOptions) error {
	if dOpt.Flex {
		return nil
	}

	if len(dOpt.GpuRange) > 0 && dOpt.GpuRange[0] != -1 {
		// Verify we can find all the specified GPUs
		for _, gpuId := range dOpt.GpuRange {
			if GpuIdExists(sysInfo, gpuId) == false {
				return fmt.Errorf("Couldn't find requested GPU id %d", gpuId)
			}
		}
	}

	if len(dOpt.GpuInstanceRange) > 0 && dOpt.GpuInstanceRange[0] != -1 {
		for _, gpuInstanceId := range dOpt.GpuInstanceRange {
			if GpuInstanceIdExists(sysInfo, gpuInstanceId) == false {
				return fmt.Errorf("Couldn't find requested GPU instance id %d", gpuInstanceId)
			}
		}
	}

	return nil
}

func InitializeSystemInfo(dOpt DeviceOptions, useFakeGpus bool) (SystemInfo, error) {
	sysInfo := SystemInfo{}
	gpuCount, err := dcgm.GetAllDeviceCount()
	if err != nil {
		return sysInfo, err
	}
	sysInfo.GpuCount = gpuCount

	for i := uint(0); i < sysInfo.GpuCount; i++ {
		sysInfo.Gpus[i].DeviceInfo, err = dcgm.GetDeviceInfo(i)
		if err != nil {
			if useFakeGpus {
				sysInfo.Gpus[i].DeviceInfo.GPU = i
				sysInfo.Gpus[i].DeviceInfo.UUID = fmt.Sprintf("fake%d", i)
			} else {
				return sysInfo, err
			}
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

		var entities []dcgm.GroupEntityPair

		gpuId := uint(0)
		instanceIndex := 0
		for i := uint(0); i < hierarchy.Count; i++ {
			if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU {
				// We are adding a GPU instance
				gpuId = hierarchy.EntityList[i].Parent.EntityId
				entityId := hierarchy.EntityList[i].Entity.EntityId
				instanceInfo := GpuInstanceInfo{
					Info:        hierarchy.EntityList[i].Info,
					ProfileName: "",
					EntityId:    entityId,
				}
				sysInfo.Gpus[gpuId].GpuInstances = append(sysInfo.Gpus[gpuId].GpuInstances, instanceInfo)
				entities = append(entities, dcgm.GroupEntityPair{dcgm.FE_GPU_I, entityId})
				instanceIndex = len(sysInfo.Gpus[gpuId].GpuInstances) - 1
			} else if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU_I {
				// Add the compute instance, gpuId is recorded previously
				entityId := hierarchy.EntityList[i].Entity.EntityId
				ciInfo := ComputeInstanceInfo{hierarchy.EntityList[i].Info, "", entityId}
				sysInfo.Gpus[gpuId].GpuInstances[instanceIndex].ComputeInstances = append(sysInfo.Gpus[gpuId].GpuInstances[instanceIndex].ComputeInstances, ciInfo)
			}
		}

		err = PopulateMigProfileNames(&sysInfo, entities)
		if err != nil {
			return sysInfo, err
		}
	}

	sysInfo.dOpt = dOpt
	err = VerifyDevicePresence(&sysInfo, dOpt)

	return sysInfo, nil
}

func CreateGroupFromSystemInfo(sysInfo SystemInfo) (dcgm.GroupHandle, func(), error) {
	monitoringInfo := GetMonitoredEntities(sysInfo)
	groupId, err := dcgm.CreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	for _, mi := range monitoringInfo {
		err := dcgm.AddEntityToGroup(groupId, mi.Entity.EntityGroupId, mi.Entity.EntityId)
		if err != nil {
			return groupId, func() { dcgm.DestroyGroup(groupId) }, err
		}
	}

	return groupId, func() { dcgm.DestroyGroup(groupId) }, nil
}

func AddAllGpus(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for i := uint(0); i < sysInfo.GpuCount; i++ {
		mi := MonitoringInfo{
			dcgm.GroupEntityPair{dcgm.FE_GPU, sysInfo.Gpus[i].DeviceInfo.GPU},
			sysInfo.Gpus[i].DeviceInfo,
			nil,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

func AddAllGpuInstances(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for i := uint(0); i < sysInfo.GpuCount; i++ {
		for j := 0; j < len(sysInfo.Gpus[i].GpuInstances); j++ {
			mi := MonitoringInfo{
				dcgm.GroupEntityPair{dcgm.FE_GPU_I, sysInfo.Gpus[i].GpuInstances[j].EntityId},
				sysInfo.Gpus[i].DeviceInfo,
				&sysInfo.Gpus[i].GpuInstances[j],
			}
			monitoring = append(monitoring, mi)
		}
	}

	return monitoring
}

func GetMonitoringInfoForGpu(sysInfo SystemInfo, gpuId int) *MonitoringInfo {
	for i := uint(0); i < sysInfo.GpuCount; i++ {
		if sysInfo.Gpus[i].DeviceInfo.GPU == uint(gpuId) {
			return &MonitoringInfo{
				dcgm.GroupEntityPair{dcgm.FE_GPU, sysInfo.Gpus[i].DeviceInfo.GPU},
				sysInfo.Gpus[i].DeviceInfo,
				nil,
			}
		}
	}

	return nil
}

func GetMonitoringInfoForGpuInstance(sysInfo SystemInfo, gpuInstanceId int) *MonitoringInfo {
	for i := uint(0); i < sysInfo.GpuCount; i++ {
		for _, instance := range sysInfo.Gpus[i].GpuInstances {
			if instance.EntityId == uint(gpuInstanceId) {
				return &MonitoringInfo{
					dcgm.GroupEntityPair{dcgm.FE_GPU_I, uint(gpuInstanceId)},
					sysInfo.Gpus[i].DeviceInfo,
					&instance,
				}
			}
		}
	}

	return nil
}

func GetMonitoredEntities(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	if sysInfo.dOpt.Flex == true {
		if sysInfo.MigEnabled == true {
			return AddAllGpuInstances(sysInfo)
		} else {
			return AddAllGpus(sysInfo)
		}
	} else {
		if len(sysInfo.dOpt.GpuRange) > 0 && sysInfo.dOpt.GpuRange[0] == -1 {
			return AddAllGpus(sysInfo)
		} else {
			for _, gpuId := range sysInfo.dOpt.GpuRange {
				// We've already verified that everying in the options list exists
				monitoring = append(monitoring, *GetMonitoringInfoForGpu(sysInfo, gpuId))
			}
		}

		if len(sysInfo.dOpt.GpuInstanceRange) > 0 && sysInfo.dOpt.GpuInstanceRange[0] == -1 {
			return AddAllGpuInstances(sysInfo)
		} else {
			for _, gpuInstanceId := range sysInfo.dOpt.GpuInstanceRange {
				// We've already verified that everything in the options list exists
				monitoring = append(monitoring, *GetMonitoringInfoForGpuInstance(sysInfo, gpuInstanceId))
			}
		}
	}

	return monitoring
}

func GetGpuInstanceIdentifier(sysInfo SystemInfo, gpuuuid string, gpuInstanceId string) string {
	for i := uint(0); i < sysInfo.GpuCount; i++ {
		if sysInfo.Gpus[i].DeviceInfo.UUID == gpuuuid {
			identifier := fmt.Sprintf("%d-%s", sysInfo.Gpus[i].DeviceInfo.GPU, gpuInstanceId)
			return identifier
		}
	}

	return ""
}
