/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/bits-and-blooms/bitset"
	"github.com/sirupsen/logrus"
)

var (
	dcgmGetAllDeviceCount       = dcgm.GetAllDeviceCount
	dcgmGetDeviceInfo           = dcgm.GetDeviceInfo
	dcgmGetGpuInstanceHierarchy = dcgm.GetGpuInstanceHierarchy
	dcgmAddEntityToGroup        = dcgm.AddEntityToGroup
	dcgmCreateGroup             = dcgm.CreateGroup
	dcgmGetCpuHierarchy         = dcgm.GetCpuHierarchy
)

type ComputeInstanceInfo struct {
	InstanceInfo dcgm.MigEntityInfo
	ProfileName  string
	EntityId     uint
}

type GPUInstanceInfo struct {
	Info             dcgm.MigEntityInfo
	ProfileName      string
	EntityId         uint
	ComputeInstances []ComputeInstanceInfo
}

type GPUInfo struct {
	DeviceInfo   dcgm.Device
	GPUInstances []GPUInstanceInfo
	MigEnabled   bool
}

type SwitchInfo struct {
	EntityId uint
	NvLinks  []dcgm.NvLinkStatus
}

type CPUInfo struct {
	EntityId uint
	Cores    []uint
}

type SystemInfo struct {
	GPUCount uint
	GPUs     [dcgm.MAX_NUM_DEVICES]GPUInfo
	gOpt     DeviceOptions
	sOpt     DeviceOptions
	cOpt     DeviceOptions
	InfoType dcgm.Field_Entity_Group
	Switches []SwitchInfo
	CPUs     []CPUInfo
}

type MonitoringInfo struct {
	Entity       dcgm.GroupEntityPair
	DeviceInfo   dcgm.Device
	InstanceInfo *GPUInstanceInfo
	ParentId     uint
}

func SetGPUInstanceProfileName(sysInfo *SystemInfo, entityId uint, profileName string) bool {
	for i := uint(0); i < sysInfo.GPUCount; i++ {
		for j := range sysInfo.GPUs[i].GPUInstances {
			if sysInfo.GPUs[i].GPUInstances[j].EntityId == entityId {
				sysInfo.GPUs[i].GPUInstances[j].ProfileName = profileName
				return true
			}
		}
	}

	return false
}

func SetMigProfileNames(sysInfo *SystemInfo, values []dcgm.FieldValue_v2) error {
	var err error
	var errFound bool
	errStr := "cannot find match for entities:"

	for _, v := range values {
		if !SetGPUInstanceProfileName(sysInfo, v.EntityId, dcgm.Fv2_String(v)) {
			errStr = fmt.Sprintf("%s group %d, id %d", errStr, v.EntityGroupId, v.EntityId)
			errFound = true
		}
	}

	if errFound {
		err = fmt.Errorf("%s", errStr)
	}

	return err
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

func GPUIdExists(sysInfo *SystemInfo, gpuId int) bool {
	for i := uint(0); i < sysInfo.GPUCount; i++ {
		if sysInfo.GPUs[i].DeviceInfo.GPU == uint(gpuId) {
			return true
		}
	}
	return false
}

func SwitchIdExists(sysInfo *SystemInfo, switchId int) bool {
	for _, sw := range sysInfo.Switches {
		if sw.EntityId == uint(switchId) {
			return true
		}
	}
	return false
}

func CPUIdExists(sysInfo *SystemInfo, cpuId int) bool {
	for _, cpu := range sysInfo.CPUs {
		if cpu.EntityId == uint(cpuId) {
			return true
		}
	}
	return false
}

func GPUInstanceIdExists(sysInfo *SystemInfo, gpuInstanceId int) bool {
	for i := uint(0); i < sysInfo.GPUCount; i++ {
		for _, instance := range sysInfo.GPUs[i].GPUInstances {
			if instance.EntityId == uint(gpuInstanceId) {
				return true
			}
		}
	}
	return false
}

func LinkIdExists(sysInfo *SystemInfo, linkId int) bool {
	for _, sw := range sysInfo.Switches {
		for _, link := range sw.NvLinks {
			if link.Index == uint(linkId) {
				return true
			}
		}
	}
	return false
}

func CPUCoreIdExists(sysInfo *SystemInfo, coreId int) bool {
	for _, cpu := range sysInfo.CPUs {
		for _, core := range cpu.Cores {
			if core == uint(coreId) {
				return true
			}
		}
	}
	return false
}

func VerifyCPUDevicePresence(sysInfo *SystemInfo, sOpt DeviceOptions) error {
	if sOpt.Flex {
		return nil
	}

	if len(sOpt.MajorRange) > 0 && sOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified Switches
		for _, cpuID := range sOpt.MajorRange {
			if !SwitchIdExists(sysInfo, cpuID) {
				return fmt.Errorf("couldn't find requested CPU ID '%d'", cpuID)
			}
		}
	}

	if len(sOpt.MinorRange) > 0 && sOpt.MinorRange[0] != -1 {
		for _, coreID := range sOpt.MinorRange {
			if !CPUCoreIdExists(sysInfo, coreID) {
				return fmt.Errorf("couldn't find requested CPU core '%d'", coreID)
			}
		}
	}

	return nil
}

func VerifySwitchDevicePresence(sysInfo *SystemInfo, sOpt DeviceOptions) error {
	if sOpt.Flex {
		return nil
	}

	if len(sOpt.MajorRange) > 0 && sOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified Switches
		for _, swID := range sOpt.MajorRange {
			if !SwitchIdExists(sysInfo, swID) {
				return fmt.Errorf("couldn't find requested NvSwitch ID '%d'", swID)
			}
		}
	}

	if len(sOpt.MinorRange) > 0 && sOpt.MinorRange[0] != -1 {
		for _, linkID := range sOpt.MinorRange {
			if !LinkIdExists(sysInfo, linkID) {
				return fmt.Errorf("couldn't find requested NvLink '%d'", linkID)
			}
		}
	}

	return nil
}

func VerifyDevicePresence(sysInfo *SystemInfo, gOpt DeviceOptions) error {
	if gOpt.Flex {
		return nil
	}

	if len(gOpt.MajorRange) > 0 && gOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified GPUs
		for _, gpuID := range gOpt.MajorRange {
			if !GPUIdExists(sysInfo, gpuID) {
				return fmt.Errorf("couldn't find requested GPU ID '%d'", gpuID)
			}
		}
	}

	if len(gOpt.MinorRange) > 0 && gOpt.MinorRange[0] != -1 {
		for _, gpuInstanceID := range gOpt.MinorRange {
			if !GPUInstanceIdExists(sysInfo, gpuInstanceID) {
				return fmt.Errorf("couldn't find requested GPU instance ID '%d'", gpuInstanceID)
			}
		}
	}

	return nil
}

func getCoreArray(bitmask []uint64) []uint {

	var cores []uint
	bits := make([]uint64, dcgm.MAX_CPU_CORE_BITMASK_COUNT)

	for i := 0; i < len(bitmask); i++ {
		bits[i] = uint64(bitmask[i])
	}

	b := bitset.From(bits)

	for i := uint(0); i < dcgm.MAX_NUM_CPU_CORES; i++ {
		if b.Test(i) {
			cores = append(cores, uint(i))
		}
	}

	return cores
}

func InitializeCPUInfo(sysInfo SystemInfo, sOpt DeviceOptions) (SystemInfo, error) {
	hierarchy, err := dcgmGetCpuHierarchy()
	if err != nil {
		return sysInfo, err
	}

	if hierarchy.NumCpus <= 0 {
		return sysInfo, fmt.Errorf("no CPUs to monitor")
	}

	for i := 0; i < int(hierarchy.NumCpus); i++ {
		cores := getCoreArray([]uint64(hierarchy.Cpus[i].OwnedCores))

		cpu := CPUInfo{
			hierarchy.Cpus[i].CpuId,
			cores,
		}

		sysInfo.CPUs = append(sysInfo.CPUs, cpu)
	}

	sysInfo.cOpt = sOpt

	err = VerifyCPUDevicePresence(&sysInfo, sOpt)
	if err != nil {
		return sysInfo, err
	}
	logrus.Debugf("System entities of type %s initialized", sysInfo.InfoType)
	return sysInfo, nil
}

func InitializeNvSwitchInfo(sysInfo SystemInfo, sOpt DeviceOptions) (SystemInfo, error) {
	switches, err := dcgm.GetEntityGroupEntities(dcgm.FE_SWITCH)
	if err != nil {
		return sysInfo, err
	}

	if len(switches) <= 0 {
		return sysInfo, fmt.Errorf("no switches to monitor")
	}

	links, err := dcgm.GetNvLinkLinkStatus()
	if err != nil {
		return sysInfo, err
	}

	for i := 0; i < len(switches); i++ {
		var matchingLinks []dcgm.NvLinkStatus
		for _, link := range links {
			if link.ParentType == dcgm.FE_SWITCH && link.ParentId == uint(switches[i]) {
				matchingLinks = append(matchingLinks, link)
			}
		}

		sw := SwitchInfo{
			switches[i],
			matchingLinks,
		}

		sysInfo.Switches = append(sysInfo.Switches, sw)
	}

	sysInfo.sOpt = sOpt
	err = VerifySwitchDevicePresence(&sysInfo, sOpt)
	if err == nil {
		logrus.Debugf("System entities of type %s initialized", sysInfo.InfoType)
	}

	return sysInfo, err
}

func InitializeGPUInfo(sysInfo SystemInfo, gOpt DeviceOptions, useFakeGPUs bool) (SystemInfo, error) {
	gpuCount, err := dcgmGetAllDeviceCount()
	if err != nil {
		return sysInfo, err
	}
	sysInfo.GPUCount = gpuCount

	for i := uint(0); i < sysInfo.GPUCount; i++ {
		// Default mig enabled to false
		sysInfo.GPUs[i].MigEnabled = false
		sysInfo.GPUs[i].DeviceInfo, err = dcgmGetDeviceInfo(i)
		if err != nil {
			if useFakeGPUs {
				sysInfo.GPUs[i].DeviceInfo.GPU = i
				sysInfo.GPUs[i].DeviceInfo.UUID = fmt.Sprintf("fake%d", i)
			} else {
				return sysInfo, err
			}
		}
	}

	hierarchy, err := dcgmGetGpuInstanceHierarchy()
	if err != nil {
		return sysInfo, err
	}

	if hierarchy.Count > 0 {
		var entities []dcgm.GroupEntityPair

		gpuID := uint(0)
		instanceIndex := 0
		for i := uint(0); i < hierarchy.Count; i++ {
			if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU {
				// We are adding a GPU instance
				gpuID = hierarchy.EntityList[i].Parent.EntityId
				entityID := hierarchy.EntityList[i].Entity.EntityId
				instanceInfo := GPUInstanceInfo{
					Info:        hierarchy.EntityList[i].Info,
					ProfileName: "",
					EntityId:    entityID,
				}
				sysInfo.GPUs[gpuID].MigEnabled = true
				sysInfo.GPUs[gpuID].GPUInstances = append(sysInfo.GPUs[gpuID].GPUInstances, instanceInfo)
				entities = append(entities, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: entityID})
				instanceIndex = len(sysInfo.GPUs[gpuID].GPUInstances) - 1
			} else if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU_I {
				// Add the compute instance, gpuId is recorded previously
				entityID := hierarchy.EntityList[i].Entity.EntityId
				ciInfo := ComputeInstanceInfo{hierarchy.EntityList[i].Info, "", entityID}
				sysInfo.GPUs[gpuID].GPUInstances[instanceIndex].ComputeInstances = append(sysInfo.GPUs[gpuID].GPUInstances[instanceIndex].ComputeInstances,
					ciInfo)
			}
		}

		err = PopulateMigProfileNames(&sysInfo, entities)
		if err != nil {
			return sysInfo, err
		}
	}

	sysInfo.gOpt = gOpt
	err = VerifyDevicePresence(&sysInfo, gOpt)
	if err == nil {
		logrus.Debugf("System entities of type %s initialized", sysInfo.InfoType)
	}
	return sysInfo, err
}

func InitializeSystemInfo(
	gOpt DeviceOptions, sOpt DeviceOptions, cOpt DeviceOptions, useFakeGPUs bool, entityType dcgm.Field_Entity_Group,
) (SystemInfo, error) {
	sysInfo := SystemInfo{}

	logrus.Info("Initializing system entities of type: ", entityType)
	switch entityType {
	case dcgm.FE_LINK:
		sysInfo.InfoType = dcgm.FE_LINK
		return InitializeNvSwitchInfo(sysInfo, sOpt)
	case dcgm.FE_SWITCH:
		sysInfo.InfoType = dcgm.FE_SWITCH
		return InitializeNvSwitchInfo(sysInfo, sOpt)
	case dcgm.FE_GPU:
		sysInfo.InfoType = dcgm.FE_GPU
		return InitializeGPUInfo(sysInfo, gOpt, useFakeGPUs)
	case dcgm.FE_CPU:
		sysInfo.InfoType = dcgm.FE_CPU
		return InitializeCPUInfo(sysInfo, cOpt)
	case dcgm.FE_CPU_CORE:
		sysInfo.InfoType = dcgm.FE_CPU_CORE
		return InitializeCPUInfo(sysInfo, cOpt)
	}

	return sysInfo, fmt.Errorf("unhandled entity type '%d'", entityType)
}

func CreateCoreGroupsFromSystemInfo(sysInfo SystemInfo) ([]dcgm.GroupHandle, []func(), error) {
	var groups []dcgm.GroupHandle
	var cleanups []func()
	var groupID dcgm.GroupHandle
	var err error

	for _, cpu := range sysInfo.CPUs {
		if !IsCPUWatched(cpu.EntityId, sysInfo) {
			continue
		}

		var groupCoreCount int
		for _, core := range cpu.Cores {
			if !IsCoreWatched(core, cpu.EntityId, sysInfo) {
				continue
			}

			// Create per-cpu core groups or after max number of CPU cores have been added to current group
			if groupCoreCount%dcgm.DCGM_GROUP_MAX_ENTITIES == 0 {
				groupID, err = dcgm.CreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
				if err != nil {
					return nil, cleanups, err
				}
				groups = append(groups, groupID)
			}

			groupCoreCount++

			err = dcgm.AddEntityToGroup(groupID, dcgm.FE_CPU_CORE, core)

			if err != nil {
				return groups, cleanups, err
			}

			cleanups = append(cleanups, func() {
				err := dcgm.DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						LoggerGroupIDKey: groupID,
						logrus.ErrorKey:  err,
					}).Warn("can not destroy group")
				}
			})
		}
	}

	return groups, cleanups, nil
}

func CreateLinkGroupsFromSystemInfo(sysInfo SystemInfo) ([]dcgm.GroupHandle, []func(), error) {
	var groups []dcgm.GroupHandle
	var cleanups []func()

	/* Create per-switch link groups */
	for _, sw := range sysInfo.Switches {
		if !IsSwitchWatched(sw.EntityId, sysInfo) {
			continue
		}

		groupID, err := dcgmCreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
		if err != nil {
			return nil, cleanups, err
		}

		groups = append(groups, groupID)

		for _, link := range sw.NvLinks {
			if link.State != dcgm.LS_UP {
				continue
			}

			if !IsLinkWatched(link.Index, sw.EntityId, sysInfo) {
				continue
			}

			err = dcgm.AddLinkEntityToGroup(groupID, link.Index, link.ParentId)

			if err != nil {
				return groups, cleanups, err
			}

			cleanups = append(cleanups, func() {
				err := dcgm.DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						LoggerGroupIDKey: groupID,
						logrus.ErrorKey:  err,
					}).Warn("can not destroy group")
				}
			})
		}
	}

	return groups, cleanups, nil
}

func CreateGroupFromSystemInfo(sysInfo SystemInfo) (dcgm.GroupHandle, func(), error) {
	monitoringInfo := GetMonitoredEntities(sysInfo)
	groupID, err := dcgmCreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	for _, mi := range monitoringInfo {
		err := dcgmAddEntityToGroup(groupID, mi.Entity.EntityGroupId, mi.Entity.EntityId)
		if err != nil {
			return groupID, func() {
				err := dcgm.DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						LoggerGroupIDKey: groupID,
						logrus.ErrorKey:  err,
					}).Warn("can not destroy group")
				}
			}, err
		}
	}

	return groupID, func() {
		err := dcgm.DestroyGroup(groupID)
		if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
			logrus.WithFields(logrus.Fields{
				LoggerGroupIDKey: groupID,
				logrus.ErrorKey:  err,
			}).Warn("can not destroy group")
		}
	}, nil
}

func AddAllGPUs(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for i := uint(0); i < sysInfo.GPUCount; i++ {
		mi := MonitoringInfo{
			dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: sysInfo.GPUs[i].DeviceInfo.GPU},
			sysInfo.GPUs[i].DeviceInfo,
			nil,
			PARENT_ID_IGNORED,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

func AddAllSwitches(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for _, sw := range sysInfo.Switches {
		if !IsSwitchWatched(sw.EntityId, sysInfo) {
			continue
		}

		mi := MonitoringInfo{
			dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: sw.EntityId},
			dcgm.Device{},
			nil,
			PARENT_ID_IGNORED,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

func AddAllLinks(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for _, sw := range sysInfo.Switches {
		for _, link := range sw.NvLinks {
			if link.State != dcgm.LS_UP {
				continue
			}

			if !IsSwitchWatched(sw.EntityId, sysInfo) {
				continue
			}

			if !IsLinkWatched(link.Index, sw.EntityId, sysInfo) {
				continue
			}

			mi := MonitoringInfo{
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

func IsSwitchWatched(switchID uint, sysInfo SystemInfo) bool {
	if sysInfo.sOpt.Flex {
		return true
	}

	// When MajorRange contains -1 value, we do monitorig of all switches
	if len(sysInfo.sOpt.MajorRange) > 0 && sysInfo.sOpt.MajorRange[0] == -1 {
		return true
	}

	return slices.Contains(sysInfo.sOpt.MajorRange, int(switchID))
}

func IsLinkWatched(linkIndex uint, switchID uint, sysInfo SystemInfo) bool {
	if sysInfo.sOpt.Flex {
		return true
	}

	// Find a switch
	switchIdx := slices.IndexFunc(sysInfo.Switches, func(si SwitchInfo) bool {
		return si.EntityId == switchID && IsSwitchWatched(si.EntityId, sysInfo)
	})

	if switchIdx > -1 {
		// Switch exists and is watched
		sw := sysInfo.Switches[switchIdx]

		if len(sysInfo.sOpt.MinorRange) > 0 && sysInfo.sOpt.MinorRange[0] == -1 {
			return true
		}

		// The Link exists
		if slices.ContainsFunc(sw.NvLinks, func(nls dcgm.NvLinkStatus) bool {
			return nls.Index == linkIndex
		}) {
			// and the link index in the Minor range
			return slices.Contains(sysInfo.sOpt.MinorRange, int(linkIndex))
		}
	}

	return false
}

func IsCPUWatched(cpuID uint, sysInfo SystemInfo) bool {

	if !slices.ContainsFunc(sysInfo.CPUs, func(cpu CPUInfo) bool {
		return cpu.EntityId == cpuID
	}) {
		return false
	}

	if sysInfo.cOpt.Flex {
		return true
	}

	if len(sysInfo.cOpt.MajorRange) > 0 && sysInfo.cOpt.MajorRange[0] == -1 {
		return true
	}

	return slices.ContainsFunc(sysInfo.cOpt.MajorRange, func(cpu int) bool {
		return uint(cpu) == cpuID
	})
}

func IsCoreWatched(coreID uint, cpuID uint, sysInfo SystemInfo) bool {
	if sysInfo.cOpt.Flex {
		return true
	}

	// Find a CPU
	cpuIdx := slices.IndexFunc(sysInfo.CPUs, func(cpu CPUInfo) bool {
		return IsCPUWatched(cpu.EntityId, sysInfo) && cpu.EntityId == cpuID
	})

	if cpuIdx > -1 {
		if len(sysInfo.cOpt.MinorRange) > 0 && sysInfo.cOpt.MinorRange[0] == -1 {
			return true
		}

		return slices.Contains(sysInfo.cOpt.MinorRange, int(coreID))
	}

	return false
}

func AddAllCPUs(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for _, cpu := range sysInfo.CPUs {
		if !IsCPUWatched(cpu.EntityId, sysInfo) {
			continue
		}

		mi := MonitoringInfo{
			dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: cpu.EntityId},
			dcgm.Device{},
			nil,
			PARENT_ID_IGNORED,
		}
		monitoring = append(monitoring, mi)
	}

	return monitoring
}

func AddAllCPUCores(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for _, cpu := range sysInfo.CPUs {
		if !IsCPUWatched(cpu.EntityId, sysInfo) {
			continue
		}

		for _, core := range cpu.Cores {
			if !IsCoreWatched(core, cpu.EntityId, sysInfo) {
				continue
			}

			mi := MonitoringInfo{
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

func AddAllGPUInstances(sysInfo SystemInfo, addFlexibly bool) []MonitoringInfo {
	var monitoring []MonitoringInfo

	for i := uint(0); i < sysInfo.GPUCount; i++ {
		if addFlexibly && len(sysInfo.GPUs[i].GPUInstances) == 0 {
			mi := MonitoringInfo{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: sysInfo.GPUs[i].DeviceInfo.GPU},
				sysInfo.GPUs[i].DeviceInfo,
				nil,
				PARENT_ID_IGNORED,
			}
			monitoring = append(monitoring, mi)
		} else {
			for j := 0; j < len(sysInfo.GPUs[i].GPUInstances); j++ {
				mi := MonitoringInfo{
					dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      sysInfo.GPUs[i].GPUInstances[j].EntityId,
					},
					sysInfo.GPUs[i].DeviceInfo,
					&sysInfo.GPUs[i].GPUInstances[j],
					PARENT_ID_IGNORED,
				}
				monitoring = append(monitoring, mi)
			}
		}
	}

	return monitoring
}

func GetMonitoringInfoForGPU(sysInfo SystemInfo, gpuID int) *MonitoringInfo {
	for i := uint(0); i < sysInfo.GPUCount; i++ {
		if sysInfo.GPUs[i].DeviceInfo.GPU == uint(gpuID) {
			return &MonitoringInfo{
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: sysInfo.GPUs[i].DeviceInfo.GPU},
				sysInfo.GPUs[i].DeviceInfo,
				nil,
				PARENT_ID_IGNORED,
			}
		}
	}

	return nil
}

func GetMonitoringInfoForGPUInstance(sysInfo SystemInfo, gpuInstanceID int) *MonitoringInfo {
	for i := uint(0); i < sysInfo.GPUCount; i++ {
		for _, instance := range sysInfo.GPUs[i].GPUInstances {
			if instance.EntityId == uint(gpuInstanceID) {
				return &MonitoringInfo{
					dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: uint(gpuInstanceID)},
					sysInfo.GPUs[i].DeviceInfo,
					&instance,
					PARENT_ID_IGNORED,
				}
			}
		}
	}

	return nil
}

func GetMonitoredEntities(sysInfo SystemInfo) []MonitoringInfo {
	var monitoring []MonitoringInfo

	if sysInfo.InfoType == dcgm.FE_SWITCH {
		monitoring = AddAllSwitches(sysInfo)
	} else if sysInfo.InfoType == dcgm.FE_LINK {
		monitoring = AddAllLinks(sysInfo)
	} else if sysInfo.InfoType == dcgm.FE_CPU {
		monitoring = AddAllCPUs(sysInfo)
	} else if sysInfo.InfoType == dcgm.FE_CPU_CORE {
		monitoring = AddAllCPUCores(sysInfo)
	} else if sysInfo.gOpt.Flex {
		monitoring = AddAllGPUInstances(sysInfo, true)
	} else {
		if len(sysInfo.gOpt.MajorRange) > 0 && sysInfo.gOpt.MajorRange[0] == -1 {
			monitoring = AddAllGPUs(sysInfo)
		} else {
			for _, gpuID := range sysInfo.gOpt.MajorRange {
				// We've already verified that everything in the options list exists
				monitoring = append(monitoring, *GetMonitoringInfoForGPU(sysInfo, gpuID))
			}
		}

		if len(sysInfo.gOpt.MinorRange) > 0 && sysInfo.gOpt.MinorRange[0] == -1 {
			monitoring = AddAllGPUInstances(sysInfo, false)
		} else {
			for _, gpuInstanceID := range sysInfo.gOpt.MinorRange {
				// We've already verified that everything in the options list exists
				monitoring = append(monitoring, *GetMonitoringInfoForGPUInstance(sysInfo, gpuInstanceID))
			}
		}
	}

	return monitoring
}

func GetGPUInstanceIdentifier(sysInfo SystemInfo, gpuuuid string, gpuInstanceID uint) string {
	for i := uint(0); i < sysInfo.GPUCount; i++ {
		if sysInfo.GPUs[i].DeviceInfo.UUID == gpuuuid {
			identifier := fmt.Sprintf("%d-%d", sysInfo.GPUs[i].DeviceInfo.GPU, gpuInstanceID)
			return identifier
		}
	}

	return ""
}
