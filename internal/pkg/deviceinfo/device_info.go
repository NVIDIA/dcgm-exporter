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

package deviceinfo

import (
	"fmt"
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/bits-and-blooms/bitset"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// TODO (roarora): These functional substitutions aren't compatible and causes a panic if the dcgmprovider client is
//
//	 used, the problem goes away when test itself uses mocks instead of these functions substitutions.
//		In the subsequent patch that refactors gpu_collector tests below issue would be resolved.
var (
	DcgmGetAllDeviceCount       = dcgm.GetAllDeviceCount
	DcgmGetDeviceInfo           = dcgm.GetDeviceInfo
	DcgmGetGpuInstanceHierarchy = dcgm.GetGpuInstanceHierarchy
	DcgmAddEntityToGroup        = dcgm.AddEntityToGroup
	DcgmCreateGroup             = dcgm.CreateGroup
	DcgmGetCpuHierarchy         = dcgm.GetCpuHierarchy
)

type Info struct {
	gpuCount uint
	gpus     [dcgm.MAX_NUM_DEVICES]GPUInfo
	switches []SwitchInfo
	cpus     []CPUInfo
	gOpt     appconfig.DeviceOptions
	sOpt     appconfig.DeviceOptions
	cOpt     appconfig.DeviceOptions
	infoType dcgm.Field_Entity_Group
}

func (s *Info) GPUCount() uint {
	return s.gpuCount
}

func (s *Info) GPUs() []GPUInfo {
	return s.gpus[:]
}

func (s *Info) GPU(i uint) GPUInfo {
	return s.gpus[i]
}

func (s *Info) Switches() []SwitchInfo {
	return s.switches
}

func (s *Info) Switch(i uint) SwitchInfo {
	return s.switches[i]
}

func (s *Info) CPUs() []CPUInfo {
	return s.cpus
}

func (s *Info) CPU(i uint) CPUInfo {
	return s.cpus[i]
}

func (s *Info) GOpts() appconfig.DeviceOptions {
	return s.gOpt
}

func (s *Info) SOpts() appconfig.DeviceOptions {
	return s.sOpt
}

func (s *Info) COpts() appconfig.DeviceOptions {
	return s.cOpt
}

func (s *Info) InfoType() dcgm.Field_Entity_Group {
	return s.infoType
}

func Initialize(
	gOpt appconfig.DeviceOptions, sOpt appconfig.DeviceOptions, cOpt appconfig.DeviceOptions, useFakeGPUs bool,
	entityType dcgm.Field_Entity_Group,
) (*Info, error) {
	deviceInfo := &Info{}
	var err error

	logrus.Infof("Initializing system entities of type '%s'", entityType.String())
	switch entityType {
	case dcgm.FE_LINK:
		deviceInfo.infoType = dcgm.FE_LINK
		err = deviceInfo.InitializeNvSwitchInfo(sOpt)
	case dcgm.FE_SWITCH:
		deviceInfo.infoType = dcgm.FE_SWITCH
		err = deviceInfo.InitializeNvSwitchInfo(sOpt)
	case dcgm.FE_GPU:
		deviceInfo.infoType = dcgm.FE_GPU
		err = deviceInfo.InitializeGPUInfo(gOpt, useFakeGPUs)
	case dcgm.FE_CPU:
		deviceInfo.infoType = dcgm.FE_CPU
		err = deviceInfo.InitializeCPUInfo(cOpt)
	case dcgm.FE_CPU_CORE:
		deviceInfo.infoType = dcgm.FE_CPU_CORE
		err = deviceInfo.InitializeCPUInfo(cOpt)
	default:
		err = fmt.Errorf("invalid entity type '%d'", entityType)
	}

	return deviceInfo, err
}

func (s *Info) InitializeGPUInfo(gOpt appconfig.DeviceOptions, useFakeGPUs bool) error {
	gpuCount, err := DcgmGetAllDeviceCount()
	if err != nil {
		return err
	}
	s.gpuCount = gpuCount

	for i := uint(0); i < s.gpuCount; i++ {
		// Default mig enabled to false
		s.gpus[i].MigEnabled = false
		s.gpus[i].DeviceInfo, err = DcgmGetDeviceInfo(i)
		if err != nil {
			if useFakeGPUs {
				s.gpus[i].DeviceInfo.GPU = i
				s.gpus[i].DeviceInfo.UUID = fmt.Sprintf("fake%d", i)
			} else {
				return err
			}
		}
	}

	hierarchy, err := DcgmGetGpuInstanceHierarchy()
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
				s.gpus[gpuID].MigEnabled = true
				s.gpus[gpuID].GPUInstances = append(s.gpus[gpuID].GPUInstances, instanceInfo)
				entities = append(entities, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: entityID})
				instanceIndex = len(s.gpus[gpuID].GPUInstances) - 1
			} else if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU_I {
				// Add the compute instance, gpuId is recorded previously
				entityID := hierarchy.EntityList[i].Entity.EntityId
				ciInfo := ComputeInstanceInfo{hierarchy.EntityList[i].Info, "", entityID}
				s.gpus[gpuID].GPUInstances[instanceIndex].ComputeInstances = append(s.gpus[gpuID].GPUInstances[instanceIndex].ComputeInstances,
					ciInfo)
			}
		}

		err = s.PopulateMigProfileNames(entities)
		if err != nil {
			return err
		}
	}

	s.gOpt = gOpt
	err = s.VerifyDevicePresence(gOpt)
	if err == nil {
		logrus.Debugf("System entities of type %s initialized", s.infoType)
	}
	return err
}

func (s *Info) InitializeCPUInfo(sOpt appconfig.DeviceOptions) error {
	hierarchy, err := DcgmGetCpuHierarchy()
	if err != nil {
		return err
	}

	if hierarchy.NumCpus <= 0 {
		return fmt.Errorf("no cpus to monitor")
	}

	for i := 0; i < int(hierarchy.NumCpus); i++ {
		cores := getCoreArray([]uint64(hierarchy.Cpus[i].OwnedCores))

		cpu := CPUInfo{
			hierarchy.Cpus[i].CpuId,
			cores,
		}

		s.cpus = append(s.cpus, cpu)
	}

	s.cOpt = sOpt

	err = s.VerifyCPUDevicePresence(sOpt)
	if err != nil {
		return err
	}
	logrus.Debugf("System entities of type %s initialized", s.infoType)
	return nil
}

func (s *Info) InitializeNvSwitchInfo(sOpt appconfig.DeviceOptions) error {
	switches, err := dcgmprovider.Client().GetEntityGroupEntities(dcgm.FE_SWITCH)
	if err != nil {
		return err
	}

	if len(switches) <= 0 {
		return fmt.Errorf("no switches to monitor")
	}

	links, err := dcgmprovider.Client().GetNvLinkLinkStatus()
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

		s.switches = append(s.switches, sw)
	}

	s.sOpt = sOpt
	err = s.VerifySwitchDevicePresence(sOpt)
	if err == nil {
		logrus.Debugf("System entities of type %s initialized", s.infoType)
	}

	return err
}

func (s *Info) SetGPUInstanceProfileName(entityId uint, profileName string) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		for j := range s.gpus[i].GPUInstances {
			if s.gpus[i].GPUInstances[j].EntityId == entityId {
				s.gpus[i].GPUInstances[j].ProfileName = profileName
				return true
			}
		}
	}

	return false
}

func (s *Info) SetMigProfileNames(values []dcgm.FieldValue_v2) error {
	var err error
	var errFound bool
	errStr := "cannot find match for entities:"

	for _, v := range values {
		if !s.SetGPUInstanceProfileName(v.EntityId, dcgmprovider.Client().Fv2_String(v)) {
			errStr = fmt.Sprintf("%s group %d, id %d", errStr, v.EntityGroupId, v.EntityId)
			errFound = true
		}
	}

	if errFound {
		err = fmt.Errorf("%s", errStr)
	}

	return err
}

func (s *Info) PopulateMigProfileNames(entities []dcgm.GroupEntityPair) error {
	if len(entities) == 0 {
		// There are no entities to populate
		return nil
	}

	var fields []dcgm.Short
	fields = append(fields, dcgm.DCGM_FI_DEV_NAME)
	flags := dcgm.DCGM_FV_FLAG_LIVE_DATA
	values, err := dcgmprovider.Client().EntitiesGetLatestValues(entities, fields, flags)

	if err != nil {
		return err
	}

	return s.SetMigProfileNames(values)
}

func (s *Info) GPUIDExists(gpuId int) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		if s.gpus[i].DeviceInfo.GPU == uint(gpuId) {
			return true
		}
	}
	return false
}

func (s *Info) GPUInstanceIDExists(gpuInstanceId int) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		for _, instance := range s.gpus[i].GPUInstances {
			if instance.EntityId == uint(gpuInstanceId) {
				return true
			}
		}
	}
	return false
}

func (s *Info) CPUIDExists(cpuId int) bool {
	for _, cpu := range s.cpus {
		if cpu.EntityId == uint(cpuId) {
			return true
		}
	}
	return false
}

func (s *Info) CPUCoreIDExists(coreId int) bool {
	for _, cpu := range s.cpus {
		for _, core := range cpu.Cores {
			if core == uint(coreId) {
				return true
			}
		}
	}
	return false
}

func (s *Info) SwitchIDExists(switchId int) bool {
	for _, sw := range s.switches {
		if sw.EntityId == uint(switchId) {
			return true
		}
	}
	return false
}

func (s *Info) LinkIDExists(linkId int) bool {
	for _, sw := range s.switches {
		for _, link := range sw.NvLinks {
			if link.Index == uint(linkId) {
				return true
			}
		}
	}
	return false
}

func (s *Info) VerifyDevicePresence(gOpt appconfig.DeviceOptions) error {
	if gOpt.Flex {
		return nil
	}

	if len(gOpt.MajorRange) > 0 && gOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified gpus
		for _, gpuID := range gOpt.MajorRange {
			if !s.GPUIDExists(gpuID) {
				return fmt.Errorf("couldn't find requested GPU ID '%d'", gpuID)
			}
		}
	}

	if len(gOpt.MinorRange) > 0 && gOpt.MinorRange[0] != -1 {
		for _, gpuInstanceID := range gOpt.MinorRange {
			if !s.GPUInstanceIDExists(gpuInstanceID) {
				return fmt.Errorf("couldn't find requested GPU instance ID '%d'", gpuInstanceID)
			}
		}
	}

	return nil
}

func (s *Info) VerifyCPUDevicePresence(sOpt appconfig.DeviceOptions) error {
	if sOpt.Flex {
		return nil
	}

	if len(sOpt.MajorRange) > 0 && sOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified switches
		for _, cpuID := range sOpt.MajorRange {
			if !s.SwitchIDExists(cpuID) {
				return fmt.Errorf("couldn't find requested CPU ID '%d'", cpuID)
			}
		}
	}

	if len(sOpt.MinorRange) > 0 && sOpt.MinorRange[0] != -1 {
		for _, coreID := range sOpt.MinorRange {
			if !s.CPUCoreIDExists(coreID) {
				return fmt.Errorf("couldn't find requested CPU core '%d'", coreID)
			}
		}
	}

	return nil
}

func (s *Info) VerifySwitchDevicePresence(sOpt appconfig.DeviceOptions) error {
	if sOpt.Flex {
		return nil
	}

	if len(sOpt.MajorRange) > 0 && sOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified switches
		for _, swID := range sOpt.MajorRange {
			if !s.SwitchIDExists(swID) {
				return fmt.Errorf("couldn't find requested NvSwitch ID '%d'", swID)
			}
		}
	}

	if len(sOpt.MinorRange) > 0 && sOpt.MinorRange[0] != -1 {
		for _, linkID := range sOpt.MinorRange {
			if !s.LinkIDExists(linkID) {
				return fmt.Errorf("couldn't find requested NvLink '%d'", linkID)
			}
		}
	}

	return nil
}

func (s *Info) IsCPUWatched(cpuID uint) bool {

	if !slices.ContainsFunc(s.cpus, func(cpu CPUInfo) bool {
		return cpu.EntityId == cpuID
	}) {
		return false
	}

	if s.cOpt.Flex {
		return true
	}

	if len(s.cOpt.MajorRange) > 0 && s.cOpt.MajorRange[0] == -1 {
		return true
	}

	return slices.ContainsFunc(s.cOpt.MajorRange, func(cpu int) bool {
		return uint(cpu) == cpuID
	})
}

func (s *Info) IsCoreWatched(coreID uint, cpuID uint) bool {
	if s.cOpt.Flex {
		return true
	}

	// Find a CPU
	cpuIdx := slices.IndexFunc(s.cpus, func(cpu CPUInfo) bool {
		return s.IsCPUWatched(cpu.EntityId) && cpu.EntityId == cpuID
	})

	if cpuIdx > -1 {
		if len(s.cOpt.MinorRange) > 0 && s.cOpt.MinorRange[0] == -1 {
			return true
		}

		return slices.Contains(s.cOpt.MinorRange, int(coreID))
	}

	return false
}

func (s *Info) IsSwitchWatched(switchID uint) bool {
	if s.sOpt.Flex {
		return true
	}

	// When MajorRange contains -1 value, we do monitorig of all switches
	if len(s.sOpt.MajorRange) > 0 && s.sOpt.MajorRange[0] == -1 {
		return true
	}

	return slices.Contains(s.sOpt.MajorRange, int(switchID))
}

func (s *Info) IsLinkWatched(linkIndex uint, switchID uint) bool {
	if s.sOpt.Flex {
		return true
	}

	// Find a switch
	switchIdx := slices.IndexFunc(s.switches, func(si SwitchInfo) bool {
		return si.EntityId == switchID && s.IsSwitchWatched(si.EntityId)
	})

	if switchIdx > -1 {
		// Switch exists and is watched
		sw := s.switches[switchIdx]

		if len(s.sOpt.MinorRange) > 0 && s.sOpt.MinorRange[0] == -1 {
			return true
		}

		// The Link exists
		if slices.ContainsFunc(sw.NvLinks, func(nls dcgm.NvLinkStatus) bool {
			return nls.Index == linkIndex
		}) {
			// and the link index in the Minor range
			return slices.Contains(s.sOpt.MinorRange, int(linkIndex))
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

// Helper Functions

func GetGPUInstanceIdentifier(deviceInfo Provider, gpuuuid string, gpuInstanceID uint) string {
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		if deviceInfo.GPU(i).DeviceInfo.UUID == gpuuuid {
			identifier := fmt.Sprintf("%d-%d", deviceInfo.GPU(i).DeviceInfo.GPU, gpuInstanceID)
			return identifier
		}
	}

	return ""
}
