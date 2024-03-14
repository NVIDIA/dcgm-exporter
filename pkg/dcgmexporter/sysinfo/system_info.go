/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package sysinfo

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/bits-and-blooms/bitset"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	dcgmClient "github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/dcgm_client"
)

type SystemInfo struct {
	GPUCount uint
	GPUs     [dcgm.MAX_NUM_DEVICES]GPUInfo
	GOpt     common.DeviceOptions
	SOpt     common.DeviceOptions
	COpt     common.DeviceOptions
	InfoType dcgm.Field_Entity_Group
	Switches []SwitchInfo
	CPUs     []CPUInfo
}

func InitializeSystemInfo(
	gOpt common.DeviceOptions, sOpt common.DeviceOptions, cOpt common.DeviceOptions, useFakeGPUs bool,
	entityType dcgm.Field_Entity_Group,
) (*SystemInfo, error) {
	sysInfo := &SystemInfo{}
	var err error

	logrus.Info("Initializing system entities of type: ", entityType)
	switch entityType {
	case dcgm.FE_LINK:
		sysInfo.InfoType = dcgm.FE_LINK
		err = sysInfo.InitializeNvSwitchInfo(sOpt)
	case dcgm.FE_SWITCH:
		sysInfo.InfoType = dcgm.FE_SWITCH
		err = sysInfo.InitializeNvSwitchInfo(sOpt)
	case dcgm.FE_GPU:
		sysInfo.InfoType = dcgm.FE_GPU
		err = sysInfo.InitializeGPUInfo(gOpt, useFakeGPUs)
	case dcgm.FE_CPU:
		sysInfo.InfoType = dcgm.FE_CPU
		err = sysInfo.InitializeCPUInfo(cOpt)
	case dcgm.FE_CPU_CORE:
		sysInfo.InfoType = dcgm.FE_CPU_CORE
		err = sysInfo.InitializeCPUInfo(cOpt)
	default:
		err = fmt.Errorf("invalid entity type")
	}

	return sysInfo, err
}

func (s *SystemInfo) InitializeNvSwitchInfo(sOpt common.DeviceOptions) error {
	switches, err := dcgmClient.Client().GetEntityGroupEntities(dcgm.FE_SWITCH)
	if err != nil {
		return err
	}

	if len(switches) <= 0 {
		return fmt.Errorf("no switches to monitor")
	}

	links, err := dcgmClient.Client().GetNvLinkLinkStatus()
	if err != nil {
		return err
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

		s.Switches = append(s.Switches, sw)
	}

	s.SOpt = sOpt
	err = s.VerifySwitchDevicePresence(sOpt)
	if err == nil {
		logrus.Debugf("System entities of type %s initialized", s.InfoType)
	}

	return err
}

func (s *SystemInfo) InitializeGPUInfo(gOpt common.DeviceOptions, useFakeGPUs bool) error {
	gpuCount, err := dcgmClient.Client().GetAllDeviceCount()
	if err != nil {
		return err
	}
	s.GPUCount = gpuCount

	for i := uint(0); i < s.GPUCount; i++ {
		// Default mig enabled to false
		s.GPUs[i].MigEnabled = false
		s.GPUs[i].DeviceInfo, err = dcgmClient.Client().GetDeviceInfo(i)
		if err != nil {
			if useFakeGPUs {
				s.GPUs[i].DeviceInfo.GPU = i
				s.GPUs[i].DeviceInfo.UUID = fmt.Sprintf("fake%d", i)
			} else {
				return err
			}
		}
	}

	hierarchy, err := dcgmClient.Client().GetGpuInstanceHierarchy()
	if err != nil {
		return err
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
				s.GPUs[gpuID].MigEnabled = true
				s.GPUs[gpuID].GPUInstances = append(s.GPUs[gpuID].GPUInstances, instanceInfo)
				entities = append(entities, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: entityID})
				instanceIndex = len(s.GPUs[gpuID].GPUInstances) - 1
			} else if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU_I {
				// Add the compute instance, gpuId is recorded previously
				entityID := hierarchy.EntityList[i].Entity.EntityId
				ciInfo := ComputeInstanceInfo{hierarchy.EntityList[i].Info, "", entityID}
				s.GPUs[gpuID].GPUInstances[instanceIndex].ComputeInstances = append(s.GPUs[gpuID].
					GPUInstances[instanceIndex].ComputeInstances, ciInfo)
			}
		}

		err = s.PopulateMigProfileNames(entities)
		if err != nil {
			return err
		}
	}

	s.GOpt = gOpt
	err = s.VerifyDevicePresence(gOpt)
	if err == nil {
		logrus.Debugf("System entities of type %s initialized", s.InfoType)
	}
	return err
}

func (s *SystemInfo) InitializeCPUInfo(sOpt common.DeviceOptions) error {
	hierarchy, err := dcgmClient.Client().GetCpuHierarchy()
	if err != nil {
		return err
	}

	if hierarchy.NumCpus <= 0 {
		return fmt.Errorf("no CPUs to monitor")
	}

	for i := 0; i < int(hierarchy.NumCpus); i++ {
		cores := getCoreArray([]uint64(hierarchy.Cpus[i].OwnedCores))

		cpu := CPUInfo{
			hierarchy.Cpus[i].CpuId,
			cores,
		}

		s.CPUs = append(s.CPUs, cpu)
	}

	s.COpt = sOpt

	err = s.VerifyCPUDevicePresence(sOpt)
	if err != nil {
		return err
	}
	logrus.Debugf("System entities of type %s initialized", s.InfoType)
	return nil
}

func (s *SystemInfo) SetGPUInstanceProfileName(entityId uint, profileName string) bool {
	for i := uint(0); i < s.GPUCount; i++ {
		for j := range s.GPUs[i].GPUInstances {
			if s.GPUs[i].GPUInstances[j].EntityId == entityId {
				s.GPUs[i].GPUInstances[j].ProfileName = profileName
				return true
			}
		}
	}

	return false
}

func (s *SystemInfo) VerifyCPUDevicePresence(sOpt common.DeviceOptions) error {
	if sOpt.Flex {
		return nil
	}

	if len(sOpt.MajorRange) > 0 && sOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified Switches
		for _, cpuID := range sOpt.MajorRange {
			if !s.SwitchIdExists(cpuID) {
				return fmt.Errorf("couldn't find requested CPU ID '%d'", cpuID)
			}
		}
	}

	if len(sOpt.MinorRange) > 0 && sOpt.MinorRange[0] != -1 {
		for _, coreID := range sOpt.MinorRange {
			if !s.CPUCoreIdExists(coreID) {
				return fmt.Errorf("couldn't find requested CPU core '%d'", coreID)
			}
		}
	}

	return nil
}

func (s *SystemInfo) VerifySwitchDevicePresence(sOpt common.DeviceOptions) error {
	if sOpt.Flex {
		return nil
	}

	if len(sOpt.MajorRange) > 0 && sOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified Switches
		for _, swID := range sOpt.MajorRange {
			if !s.SwitchIdExists(swID) {
				return fmt.Errorf("couldn't find requested NvSwitch ID '%d'", swID)
			}
		}
	}

	if len(sOpt.MinorRange) > 0 && sOpt.MinorRange[0] != -1 {
		for _, linkID := range sOpt.MinorRange {
			if !s.LinkIdExists(linkID) {
				return fmt.Errorf("couldn't find requested NvLink '%d'", linkID)
			}
		}
	}

	return nil
}

func (s *SystemInfo) VerifyDevicePresence(gOpt common.DeviceOptions) error {
	if gOpt.Flex {
		return nil
	}

	if len(gOpt.MajorRange) > 0 && gOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified GPUs
		for _, gpuID := range gOpt.MajorRange {
			if !s.GPUIdExists(gpuID) {
				return fmt.Errorf("couldn't find requested GPU ID '%d'", gpuID)
			}
		}
	}

	if len(gOpt.MinorRange) > 0 && gOpt.MinorRange[0] != -1 {
		for _, gpuInstanceID := range gOpt.MinorRange {
			if !s.GPUInstanceIdExists(gpuInstanceID) {
				return fmt.Errorf("couldn't find requested GPU instance ID '%d'", gpuInstanceID)
			}
		}
	}

	return nil
}

func (s *SystemInfo) PopulateMigProfileNames(entities []dcgm.GroupEntityPair) error {
	if len(entities) == 0 {
		// There are no entities to populate
		return nil
	}

	var fields []dcgm.Short
	fields = append(fields, dcgm.DCGM_FI_DEV_NAME)
	flags := dcgm.DCGM_FV_FLAG_LIVE_DATA
	values, err := dcgmClient.Client().EntitiesGetLatestValues(entities, fields, flags)

	if err != nil {
		return err
	}

	return s.SetMigProfileNames(values)
}

func (s *SystemInfo) SetMigProfileNames(values []dcgm.FieldValue_v2) error {
	var err error
	var errFound bool
	errStr := "cannot find match for entities:"

	for _, v := range values {
		if !s.SetGPUInstanceProfileName(v.EntityId, dcgm.Fv2_String(v)) {
			errStr = fmt.Sprintf("%s group %d, id %d", errStr, v.EntityGroupId, v.EntityId)
			errFound = true
		}
	}

	if errFound {
		err = fmt.Errorf("%s", errStr)
	}

	return err
}

func (s *SystemInfo) GPUIdExists(gpuId int) bool {
	for i := uint(0); i < s.GPUCount; i++ {
		if s.GPUs[i].DeviceInfo.GPU == uint(gpuId) {
			return true
		}
	}
	return false
}

func (s *SystemInfo) SwitchIdExists(switchId int) bool {
	for _, sw := range s.Switches {
		if sw.EntityId == uint(switchId) {
			return true
		}
	}
	return false
}

func (s *SystemInfo) CPUIdExists(cpuId int) bool {
	for _, cpu := range s.CPUs {
		if cpu.EntityId == uint(cpuId) {
			return true
		}
	}
	return false
}

func (s *SystemInfo) GPUInstanceIdExists(gpuInstanceId int) bool {
	for i := uint(0); i < s.GPUCount; i++ {
		for _, instance := range s.GPUs[i].GPUInstances {
			if instance.EntityId == uint(gpuInstanceId) {
				return true
			}
		}
	}
	return false
}

func (s *SystemInfo) LinkIdExists(linkId int) bool {
	for _, sw := range s.Switches {
		for _, link := range sw.NvLinks {
			if link.Index == uint(linkId) {
				return true
			}
		}
	}
	return false
}

func (s *SystemInfo) CPUCoreIdExists(coreId int) bool {
	for _, cpu := range s.CPUs {
		for _, core := range cpu.Cores {
			if core == uint(coreId) {
				return true
			}
		}
	}
	return false
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

func CreateCoreGroupsFromSystemInfo(sysInfo SystemInfo) ([]dcgm.GroupHandle, []func(), error) {
	var groups []dcgm.GroupHandle
	var cleanups []func()
	var groupID dcgm.GroupHandle
	var err error

	/* Create per-cpu core groups */
	for _, cpu := range sysInfo.CPUs {
		if !IsCPUWatched(cpu.EntityId, sysInfo) {
			continue
		}

		for i, core := range cpu.Cores {

			if i == 0 || i%dcgm.DCGM_GROUP_MAX_ENTITIES == 0 {
				groupID, err = dcgmClient.Client().CreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
				if err != nil {
					return nil, cleanups, err
				}

				groups = append(groups, groupID)
			}

			if !IsCoreWatched(core, cpu.EntityId, sysInfo) {
				continue
			}

			err = dcgmClient.Client().AddEntityToGroup(groupID, dcgm.FE_CPU_CORE, core)

			if err != nil {
				return groups, cleanups, err
			}

			cleanups = append(cleanups, func() {
				err := dcgmClient.Client().DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						common.LoggerGroupIDKey: groupID,
						logrus.ErrorKey:         err,
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

		groupID, err := dcgmClient.Client().CreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
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

			err = dcgmClient.Client().AddLinkEntityToGroup(groupID, link.Index, link.ParentId)

			if err != nil {
				return groups, cleanups, err
			}

			cleanups = append(cleanups, func() {
				err := dcgmClient.Client().DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						common.LoggerGroupIDKey: groupID,
						logrus.ErrorKey:         err,
					}).Warn("can not destroy group")
				}
			})
		}
	}

	return groups, cleanups, nil
}

func CreateGroupFromSystemInfo(sysInfo SystemInfo) (dcgm.GroupHandle, func(), error) {
	monitoringInfo := GetMonitoredEntities(sysInfo)
	groupID, err := dcgmClient.Client().CreateGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	for _, mi := range monitoringInfo {
		err := dcgmClient.Client().AddEntityToGroup(groupID, mi.Entity.EntityGroupId, mi.Entity.EntityId)
		if err != nil {
			return groupID, func() {
				err := dcgmClient.Client().DestroyGroup(groupID)
				if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
					logrus.WithFields(logrus.Fields{
						common.LoggerGroupIDKey: groupID,
						logrus.ErrorKey:         err,
					}).Warn("can not destroy group")
				}
			}, err
		}
	}

	return groupID, func() {
		err := dcgmClient.Client().DestroyGroup(groupID)
		if err != nil && !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
			logrus.WithFields(logrus.Fields{
				common.LoggerGroupIDKey: groupID,
				logrus.ErrorKey:         err,
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
	if sysInfo.SOpt.Flex {
		return true
	}

	// When MajorRange contains -1 value, we do monitorig of all switches
	if len(sysInfo.SOpt.MajorRange) > 0 && sysInfo.SOpt.MajorRange[0] == -1 {
		return true
	}

	return slices.Contains(sysInfo.SOpt.MajorRange, int(switchID))
}

func IsLinkWatched(linkIndex uint, switchID uint, sysInfo SystemInfo) bool {
	if sysInfo.SOpt.Flex {
		return true
	}

	// Find a switch
	switchIdx := slices.IndexFunc(sysInfo.Switches, func(si SwitchInfo) bool {
		return si.EntityId == switchID && IsSwitchWatched(si.EntityId, sysInfo)
	})

	if switchIdx > -1 {
		// Switch exists and is watched
		sw := sysInfo.Switches[switchIdx]

		if len(sysInfo.SOpt.MinorRange) > 0 && sysInfo.SOpt.MinorRange[0] == -1 {
			return true
		}

		// The Link exists
		if slices.ContainsFunc(sw.NvLinks, func(nls dcgm.NvLinkStatus) bool {
			return nls.Index == linkIndex
		}) {
			// and the link index in the Minor range
			return slices.Contains(sysInfo.SOpt.MinorRange, int(linkIndex))
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

	if sysInfo.COpt.Flex {
		return true
	}

	if len(sysInfo.COpt.MajorRange) > 0 && sysInfo.COpt.MajorRange[0] == -1 {
		return true
	}

	return slices.ContainsFunc(sysInfo.COpt.MajorRange, func(cpu int) bool {
		return uint(cpu) == cpuID
	})
}

func IsCoreWatched(coreID uint, cpuID uint, sysInfo SystemInfo) bool {
	if sysInfo.COpt.Flex {
		return true
	}

	// Find a CPU
	cpuIdx := slices.IndexFunc(sysInfo.CPUs, func(cpu CPUInfo) bool {
		return IsCPUWatched(cpu.EntityId, sysInfo) && cpu.EntityId == cpuID
	})

	if cpuIdx > -1 {
		if len(sysInfo.COpt.MinorRange) > 0 && sysInfo.COpt.MinorRange[0] == -1 {
			return true
		}

		return slices.Contains(sysInfo.COpt.MinorRange, int(coreID))
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
		for _, core := range cpu.Cores {
			if !IsCPUWatched(cpu.EntityId, sysInfo) {
				continue
			}

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
	} else if sysInfo.GOpt.Flex {
		monitoring = AddAllGPUInstances(sysInfo, true)
	} else {
		if len(sysInfo.GOpt.MajorRange) > 0 && sysInfo.GOpt.MajorRange[0] == -1 {
			monitoring = AddAllGPUs(sysInfo)
		} else {
			for _, gpuID := range sysInfo.GOpt.MajorRange {
				// We've already verified that everything in the options list exists
				monitoring = append(monitoring, *GetMonitoringInfoForGPU(sysInfo, gpuID))
			}
		}

		if len(sysInfo.GOpt.MinorRange) > 0 && sysInfo.GOpt.MinorRange[0] == -1 {
			monitoring = AddAllGPUInstances(sysInfo, false)
		} else {
			for _, gpuInstanceID := range sysInfo.GOpt.MinorRange {
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

func SetupDcgmFieldsWatch(
	deviceFields []dcgm.Short, sysInfo SystemInfo,
	collectIntervalUsec int64,
) ([]func(), error) {
	var err error
	var cleanups []func()
	var cleanup func()
	var groups []dcgm.GroupHandle
	var fieldGroup dcgm.FieldHandle

	if sysInfo.InfoType == dcgm.FE_LINK {
		/* one group per-nvswitch is created for nvlinks */
		groups, cleanups, err = CreateLinkGroupsFromSystemInfo(sysInfo)
	} else if sysInfo.InfoType == dcgm.FE_CPU_CORE {
		/* one group per-CPU is created for cpu cores */
		groups, cleanups, err = CreateCoreGroupsFromSystemInfo(sysInfo)
	} else {
		group, cleanup, err := CreateGroupFromSystemInfo(sysInfo)
		if err == nil {
			groups = append(groups, group)
			cleanups = append(cleanups, cleanup)
		}
	}

	if err != nil {
		goto fail
	}

	for _, gr := range groups {
		fieldGroup, cleanup, err = NewFieldGroup(deviceFields)
		if err != nil {
			goto fail
		}

		cleanups = append(cleanups, cleanup)

		err = WatchFieldGroup(gr, fieldGroup, collectIntervalUsec, 0.0, 1)
		if err != nil {
			goto fail
		}
	}

	return cleanups, nil

fail:
	for _, f := range cleanups {
		f()
	}

	return nil, err
}

func NewFieldGroup(deviceFields []dcgm.Short) (dcgm.FieldHandle, func(), error) {
	name := fmt.Sprintf("gpu-collector-fieldgroup-%d", rand.Uint64())
	fieldGroup, err := dcgmClient.Client().FieldGroupCreate(name, deviceFields)
	if err != nil {
		return dcgm.FieldHandle{}, func() {}, err
	}

	return fieldGroup, func() {
		err := dcgmClient.Client().FieldGroupDestroy(fieldGroup)
		if err != nil {
			logrus.WithError(err).Warn("Cannot destroy field group.")
		}
	}, nil
}

func WatchFieldGroup(
	group dcgm.GroupHandle, field dcgm.FieldHandle, updateFreq int64, maxKeepAge float64, maxKeepSamples int32,
) error {
	err := dcgmClient.Client().WatchFieldsWithGroupEx(field, group, updateFreq, maxKeepAge, maxKeepSamples)
	if err != nil {
		return err
	}

	return nil
}
