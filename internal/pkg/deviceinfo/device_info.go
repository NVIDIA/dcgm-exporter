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
	"log/slog"
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/bits-and-blooms/bitset"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

const deviceInitMessage = "System entities of type %s initialized"

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

	slog.Info(fmt.Sprintf("Initializing system entities of type '%s'", entityType.String()))
	switch entityType {
	case dcgm.FE_LINK:
		deviceInfo.infoType = dcgm.FE_LINK
		err = deviceInfo.initializeNvSwitchInfo(sOpt)
	case dcgm.FE_SWITCH:
		deviceInfo.infoType = dcgm.FE_SWITCH
		err = deviceInfo.initializeNvSwitchInfo(sOpt)
	case dcgm.FE_GPU:
		deviceInfo.infoType = dcgm.FE_GPU
		err = deviceInfo.initializeGPUInfo(gOpt, useFakeGPUs)
	case dcgm.FE_CPU:
		deviceInfo.infoType = dcgm.FE_CPU
		err = deviceInfo.initializeCPUInfo(cOpt)
	case dcgm.FE_CPU_CORE:
		deviceInfo.infoType = dcgm.FE_CPU_CORE
		err = deviceInfo.initializeCPUInfo(cOpt)
	default:
		err = fmt.Errorf("invalid entity type '%d'", entityType)
	}

	return deviceInfo, err
}

func (s *Info) initializeGPUInfo(gOpt appconfig.DeviceOptions, useFakeGPUs bool) error {
	gpuCount, err := dcgmprovider.Client().GetAllDeviceCount()
	if err != nil {
		return err
	}
	s.gpuCount = gpuCount

	for i := uint(0); i < s.gpuCount; i++ {
		// TODO (roarora): Use of array to store GPUs makes it harder to ignore GPUs (including GPU Instances) which
		//                 should be filtered out based on `Major` attribute in Device Options. Fix it!

		// Default mig enabled to false
		s.gpus[i].MigEnabled = false
		s.gpus[i].DeviceInfo, err = dcgmprovider.Client().GetDeviceInfo(i)
		if err != nil {
			if useFakeGPUs {
				s.gpus[i].DeviceInfo.GPU = i
				s.gpus[i].DeviceInfo.UUID = fmt.Sprintf("fake%d", i)
			} else {
				return err
			}
		}
	}

	hierarchy, err := dcgmprovider.Client().GetGPUInstanceHierarchy()
	if err != nil {
		return err
	}

	if hierarchy.Count > 0 {
		var entities []dcgm.GroupEntityPair

		gpuID := uint(0)
		instanceIndex := 0
		for i := uint(0); i < hierarchy.Count; i++ {
			entityID := hierarchy.EntityList[i].Entity.EntityId

			if hierarchy.EntityList[i].Parent.EntityGroupId == dcgm.FE_GPU {

				// We are adding a GPU instance
				gpuID = hierarchy.EntityList[i].Parent.EntityId

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
				// TODO (roarora): Fix this implementation as it expects Instances and Compute Instances to be reported
				//                 in a certain sequence if, that is not the case results are incorrect.

				// Add the compute instance, gpuId is recorded previously
				ciInfo := ComputeInstanceInfo{hierarchy.EntityList[i].Info, "", entityID}
				s.gpus[gpuID].GPUInstances[instanceIndex].ComputeInstances = append(s.gpus[gpuID].GPUInstances[instanceIndex].ComputeInstances,
					ciInfo)
			}
		}

		err = s.populateMigProfileNames(entities)
		if err != nil {
			return err
		}
	}

	s.gOpt = gOpt
	err = s.verifyDevicePresence()
	if err == nil {
		slog.Debug(fmt.Sprintf(deviceInitMessage, s.infoType))
	}
	return err
}

func (s *Info) initializeCPUInfo(cOpt appconfig.DeviceOptions) error {
	hierarchy, err := dcgmprovider.Client().GetCPUHierarchy()
	if err != nil {
		return err
	}

	if hierarchy.NumCPUs <= 0 {
		return fmt.Errorf("no cpus to monitor")
	}

	for i := 0; i < int(hierarchy.NumCPUs); i++ {
		// monitor only the CPUs as per the device options input
		if cOpt.Flex || s.shouldMonitor(cOpt.MajorRange, hierarchy.CPUs[i].CPUID) {
			cores := getCoreArray(hierarchy.CPUs[i].OwnedCores)

			monitoredCores := make([]uint, 0)
			for _, core := range cores {
				// monitor only the CPU cores as per the device options input
				if cOpt.Flex || s.shouldMonitor(cOpt.MinorRange, core) {
					monitoredCores = append(monitoredCores, core)
				}
			}

			cpu := CPUInfo{
				hierarchy.CPUs[i].CPUID,
				monitoredCores,
			}

			s.cpus = append(s.cpus, cpu)
		}
	}

	s.cOpt = cOpt

	// ensures all the CPUs and Cores to monitor have been discovered
	err = s.verifyCPUDevicePresence()
	if err != nil {
		return err
	}

	// Ensure correct CPUs and Cores are monitored
	slog.Debug(fmt.Sprintf(deviceInitMessage, s.infoType))
	return nil
}

func (s *Info) initializeNvSwitchInfo(sOpt appconfig.DeviceOptions) error {
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
		// monitor only the Switches as per the device options input
		if sOpt.Flex || s.shouldMonitor(sOpt.MajorRange, switches[i]) {

			var matchingLinks []dcgm.NvLinkStatus
			for _, link := range links {
				// monitor only the NV Link as per the device options input
				if sOpt.Flex || s.shouldMonitor(sOpt.MinorRange, link.Index) {
					if link.ParentType == dcgm.FE_SWITCH && link.ParentId == switches[i] {
						matchingLinks = append(matchingLinks, link)
					}
				}
			}

			sw := SwitchInfo{
				switches[i],
				matchingLinks,
			}

			s.switches = append(s.switches, sw)
		}
	}

	s.sOpt = sOpt
	err = s.verifySwitchDevicePresence()
	if err == nil {
		slog.Debug(fmt.Sprintf(deviceInitMessage, s.infoType))
	}

	return err
}

func (s *Info) setGPUInstanceProfileName(entityID uint, profileName string) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		for j := range s.gpus[i].GPUInstances {
			if s.gpus[i].GPUInstances[j].EntityId == entityID {
				s.gpus[i].GPUInstances[j].ProfileName = profileName
				return true
			}
		}
	}

	return false
}

func (s *Info) setMigProfileNames(values []dcgm.FieldValue_v2) error {
	var err error
	var errFound bool
	errStr := "cannot find match for entities:"

	for _, v := range values {
		if !s.setGPUInstanceProfileName(v.EntityID, dcgmprovider.Client().Fv2_String(v)) {
			errStr = fmt.Sprintf("%s group %d, id %d", errStr, v.EntityGroupId, v.EntityID)
			errFound = true
		}
	}

	if errFound {
		err = fmt.Errorf("%s", errStr)
	}

	return err
}

func (s *Info) populateMigProfileNames(entities []dcgm.GroupEntityPair) error {
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

	return s.setMigProfileNames(values)
}

func (s *Info) gpuIDExists(gpuID int) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		if s.gpus[i].DeviceInfo.GPU == uint(gpuID) {
			return true
		}
	}
	return false
}

func (s *Info) gpuInstanceIDExists(gpuInstanceID int) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		for _, instance := range s.gpus[i].GPUInstances {
			if instance.EntityId == uint(gpuInstanceID) {
				return true
			}
		}
	}
	return false
}

func (s *Info) cpuIDExists(cpuId int) bool {
	for _, cpu := range s.cpus {
		if cpu.EntityId == uint(cpuId) {
			return true
		}
	}
	return false
}

func (s *Info) cpuCoreIDExists(coreId int) bool {
	for _, cpu := range s.cpus {
		for _, core := range cpu.Cores {
			if core == uint(coreId) {
				return true
			}
		}
	}
	return false
}

func (s *Info) switchIDExists(switchId int) bool {
	for _, sw := range s.switches {
		if sw.EntityId == uint(switchId) {
			return true
		}
	}
	return false
}

func (s *Info) linkIDExists(linkId int) bool {
	for _, sw := range s.switches {
		for _, link := range sw.NvLinks {
			if link.Index == uint(linkId) {
				return true
			}
		}
	}
	return false
}

func (s *Info) verifyDevicePresence() error {
	if s.gOpt.Flex {
		return nil
	}

	if len(s.gOpt.MajorRange) > 0 && s.gOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified gpus
		for _, gpuID := range s.gOpt.MajorRange {
			if !s.gpuIDExists(gpuID) {
				return fmt.Errorf("couldn't find requested GPU ID '%d'", gpuID)
			}
		}
	}

	if len(s.gOpt.MinorRange) > 0 && s.gOpt.MinorRange[0] != -1 {
		for _, gpuInstanceID := range s.gOpt.MinorRange {
			if !s.gpuInstanceIDExists(gpuInstanceID) {
				return fmt.Errorf("couldn't find requested GPU instance ID '%d'", gpuInstanceID)
			}
		}
	}

	return nil
}

func (s *Info) verifyCPUDevicePresence() error {
	if s.cOpt.Flex {
		return nil
	}

	if len(s.cOpt.MajorRange) > 0 && s.cOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified CPUs
		for _, cpuID := range s.cOpt.MajorRange {
			if !s.cpuIDExists(cpuID) {
				return fmt.Errorf("couldn't find requested CPU ID '%d'", cpuID)
			}
		}
	}

	if len(s.cOpt.MinorRange) > 0 && s.cOpt.MinorRange[0] != -1 {
		for _, coreID := range s.cOpt.MinorRange {
			if !s.cpuCoreIDExists(coreID) {
				return fmt.Errorf("couldn't find requested CPU core '%d'", coreID)
			}
		}
	}

	return nil
}

func (s *Info) shouldMonitor(monitoringRange []int, val uint) bool {
	if len(monitoringRange) > 0 {
		if monitoringRange[0] == -1 {
			return true
		} else {
			return slices.Contains(monitoringRange, int(val))
		}
	}

	return false
}

func (s *Info) verifySwitchDevicePresence() error {
	if s.sOpt.Flex {
		return nil
	}

	if len(s.sOpt.MajorRange) > 0 && s.sOpt.MajorRange[0] != -1 {
		// Verify we can find all the specified switches
		for _, swID := range s.sOpt.MajorRange {
			if !s.switchIDExists(swID) {
				return fmt.Errorf("couldn't find requested NvSwitch ID '%d'", swID)
			}
		}
	}

	if len(s.sOpt.MinorRange) > 0 && s.sOpt.MinorRange[0] != -1 {
		for _, linkID := range s.sOpt.MinorRange {
			if !s.linkIDExists(linkID) {
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
		bits[i] = bitmask[i]
	}

	b := bitset.From(bits)

	for i := uint(0); i < dcgm.MAX_NUM_CPU_CORES; i++ {
		if b.Test(i) {
			cores = append(cores, i)
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
