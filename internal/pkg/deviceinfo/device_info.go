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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/bits-and-blooms/bitset"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

// ErrNoEntities reports successful discovery of an absent optional device type.
// Watch-list setup may skip it, while native discovery failures must reject a candidate.
var ErrNoEntities = errors.New("no entities to monitor")

const (
	deviceInitMessage                  = "System entities of type %s initialized"
	migGPUInstanceWithoutCIWarning     = "MIG GPU instance has no compute instances; FB metrics will be unavailable if this instance is monitored"
	migGPUInstanceWithoutCIWarningHint = "create at least one MIG compute instance for this GPU instance"
	gpuIdentityFallbackWarning         = "DCGM device-attributes API unavailable; read GPU identity from DCGM fields instead"
	gpuPlaceholderUUIDWarning          = "DCGM GPU UUID unavailable; using placeholder UUIDs GPU-<id> built from the GPU entity ID"
)

var (
	// ErrNoCPUsToMonitor identifies a host without CPU entities. Callers may
	// continue when CPU metrics are optional, while still rejecting bad CPU
	// selectors on hosts that do report CPUs.
	ErrNoCPUsToMonitor = fmt.Errorf("no cpus to monitor: %w", ErrNoEntities)
	// ErrNoSwitchesToMonitor identifies a host without NVSwitch entities.
	// Callers may continue when switch metrics are optional, while still
	// rejecting invalid switch or link selectors on switch-equipped hosts.
	ErrNoSwitchesToMonitor = fmt.Errorf("no switches to monitor: %w", ErrNoEntities)
)

// IsOptionalTopologyError reports whether err means that this host lacks an
// optional CPU or NVSwitch topology. Registry construction uses it to keep
// ordinary GPU-only hosts running without hiding invalid selected-device IDs.
func IsOptionalTopologyError(err error) bool {
	return errors.Is(err, ErrNoCPUsToMonitor) || errors.Is(err, ErrNoSwitchesToMonitor)
}

// Info stores the topology discovered for one registry and the selectors used
// to choose it. Initialize returns it, and collectors read it through Provider.
// A link registry can include both GPU and NVSwitch topology.
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

// GPUCount returns the number of discovered GPUs in this topology.
func (s *Info) GPUCount() uint {
	return s.gpuCount
}

// GPUs returns the discovered GPUs in DCGM enumeration order.
func (s *Info) GPUs() []GPUInfo {
	return s.gpus[:s.gpuCount]
}

// GPU returns the GPU at index i.
func (s *Info) GPU(i uint) GPUInfo {
	return s.gpus[i]
}

// Switches returns the selected NVSwitches and their selected links.
func (s *Info) Switches() []SwitchInfo {
	return s.switches
}

// Switch returns the switch at index i.
func (s *Info) Switch(i uint) SwitchInfo {
	return s.switches[i]
}

// CPUs returns the selected CPUs and their selected cores.
func (s *Info) CPUs() []CPUInfo {
	return s.cpus
}

// CPU returns the CPU at index i.
func (s *Info) CPU(i uint) CPUInfo {
	return s.cpus[i]
}

// GOpts returns the GPU selectors used to build this topology.
func (s *Info) GOpts() appconfig.DeviceOptions {
	return s.gOpt
}

// SOpts returns the NVSwitch and link selectors used to build this topology.
func (s *Info) SOpts() appconfig.DeviceOptions {
	return s.sOpt
}

// COpts returns the CPU and core selectors used to build this topology.
func (s *Info) COpts() appconfig.DeviceOptions {
	return s.cOpt
}

// InfoType returns the DCGM entity group represented by this topology.
func (s *Info) InfoType() dcgm.Field_Entity_Group {
	return s.infoType
}

// Initialize discovers the topology selected for entityType and returns it as
// an Info. Startup calls it once per registry; link setup attempts both GPU and
// switch discovery so the result can include NVLinks attached to either entity.
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
		if err != nil {
			if !IsOptionalTopologyError(err) {
				return deviceInfo, err
			}
			slog.Warn("Failed to initialize NvSwitch/NvLink info", "error", err)
		}
		err = deviceInfo.initializeGPUInfo(gOpt, useFakeGPUs)
		if err != nil {
			slog.Warn("Failed to initialize GPU/NvLink info", "error", err)
		}
		err = nil
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

// initializeGPUInfo discovers GPUs, NVLinks, and their MIG hierarchy, then
// validates GPU selectors. Initialize uses it for GPU and link registries.
func (s *Info) initializeGPUInfo(gOpt appconfig.DeviceOptions, useFakeGPUs bool) error {
	gpuCount, err := dcgmprovider.Client().GetAllDeviceCount()
	if err != nil {
		return err
	}
	s.gpuCount = gpuCount

	if err := s.discoverGPUs(useFakeGPUs); err != nil {
		return err
	}

	s.attachGPULinks(gOpt)
	if err := s.discoverMIGInstances(); err != nil {
		return err
	}

	s.gOpt = gOpt
	err = s.verifyDevicePresence()
	if err == nil {
		slog.Debug(fmt.Sprintf(deviceInitMessage, s.infoType))
	}
	return err
}

// discoverGPUs reads the status and identity of every GPU DCGM reports, then
// logs one warning for the GPUs that used the identity fallback.
func (s *Info) discoverGPUs(useFakeGPUs bool) error {
	var fallbacks gpuIdentityFallbacks
	for i := uint(0); i < s.gpuCount; i++ {
		if err := s.discoverGPU(i, useFakeGPUs, &fallbacks); err != nil {
			return err
		}
	}

	fallbacks.warn()
	return nil
}

// discoverGPU reads one GPU's status and identity. A detached GPU keeps only
// its stable ID for selectors. When DCGM cannot return device attributes, fake
// GPUs get a fake UUID and real GPUs use minimalGPUDeviceInfo, which fallbacks
// records.
func (s *Info) discoverGPU(gpuID uint, useFakeGPUs bool, fallbacks *gpuIdentityFallbacks) error {
	gpu := &s.gpus[gpuID]
	gpu.Status = dcgmprovider.Client().GetGPUStatus(gpuID)
	if gpu.Status == dcgm.EntityStatusDetached {
		// Retain its stable ID for selectors without querying detached hardware.
		gpu.DeviceInfo = dcgm.Device{GPU: gpuID}
		return nil
	}
	// TODO (roarora): Use of array to store GPUs makes it harder to ignore GPUs (including GPU Instances) which
	//                 should be filtered out based on `Major` attribute in Device Options. Fix it!

	// Default mig enabled to false
	gpu.MigEnabled = false
	var err error
	gpu.DeviceInfo, err = dcgmprovider.Client().GetDeviceInfo(gpuID)
	if err == nil {
		return nil
	}

	// Detach can complete between the status and metadata queries.
	if dcgmprovider.Client().GetGPUStatus(gpuID) == dcgm.EntityStatusDetached {
		gpu.Status = dcgm.EntityStatusDetached
		gpu.DeviceInfo = dcgm.Device{GPU: gpuID}
		return nil
	}

	if useFakeGPUs {
		gpu.DeviceInfo.GPU = gpuID
		gpu.DeviceInfo.UUID = fmt.Sprintf("fake%d", gpuID)
		gpu.SyntheticGPUUUID = true
		return nil
	}

	device, placeholderUUID, fallbackErr := minimalGPUDeviceInfo(gpuID, err)
	if fallbackErr != nil {
		return fallbackErr
	}

	gpu.DeviceInfo, gpu.SyntheticGPUUUID = device, placeholderUUID
	fallbacks.add(gpuID, placeholderUUID, err)
	return nil
}

// attachGPULinks stores each selected GPU's NVLinks from DCGM link status. GPUs
// keep no links when DCGM cannot report link status.
func (s *Info) attachGPULinks(gOpt appconfig.DeviceOptions) {
	links, err := dcgmprovider.Client().GetNvLinkLinkStatus()
	if err != nil {
		return
	}

	for i := range s.gpus {
		// monitor only the nvlinks as per the device options input
		if gOpt.Flex || s.shouldMonitor(gOpt.MajorRange, s.gpus[i].DeviceInfo.GPU) {
			s.gpus[i].NvLinks = gpuLinks(links, s.gpus[i].DeviceInfo.GPU)
		}
	}
}

// gpuLinks returns the links that belong to one GPU. It keeps DCGM's physical
// link index, which FE_LINK reads and the nvlink label use.
func gpuLinks(links []dcgm.NvLinkStatus, gpuID uint) []dcgm.NvLinkStatus {
	var matchingLinks []dcgm.NvLinkStatus
	for _, link := range links {
		if link.ParentType == dcgm.FE_GPU && link.ParentId == gpuID {
			matchingLinks = append(matchingLinks, link)
		}
	}

	return matchingLinks
}

// gpuInstanceRef locates an appended GPU instance while addComputeInstances
// attaches its compute instances.
type gpuInstanceRef struct {
	gpuID         uint
	instanceIndex int
}

// discoverMIGInstances adds the MIG GPU and compute instances from DCGM's
// hierarchy and resolves their profile names. A host where DCGM cannot reach
// NVML continues without MIG discovery.
func (s *Info) discoverMIGInstances() error {
	hierarchy, err := dcgmprovider.Client().GetGPUInstanceHierarchy()
	if err != nil {
		if !isMIGHierarchyNVMLUnavailable(err) {
			return err
		}
		slog.Info("MIG hierarchy unavailable because NVML is not loaded; continuing without MIG device discovery",
			"error", err)
	}

	if hierarchy.Count == 0 {
		return nil
	}

	gpuInstanceRefs, entities := s.addGPUInstances(hierarchy)
	s.addComputeInstances(hierarchy, gpuInstanceRefs)
	return s.populateMigProfileNames(entities)
}

// addGPUInstances appends each GPU instance in hierarchy to its GPU and marks
// the GPU as MIG-enabled. It returns where each instance was stored, and the
// instances on attached GPUs whose profile names still need reading.
func (s *Info) addGPUInstances(hierarchy dcgm.MigHierarchy_v2) (map[uint]gpuInstanceRef, []dcgm.GroupEntityPair) {
	var entities []dcgm.GroupEntityPair
	gpuInstanceRefs := make(map[uint]gpuInstanceRef)

	for i := uint(0); i < hierarchy.Count; i++ {
		item := hierarchy.EntityList[i]
		if item.Parent.EntityGroupId != dcgm.FE_GPU {
			continue
		}

		entityID := item.Entity.EntityId
		gpuID := item.Parent.EntityId
		instanceInfo := GPUInstanceInfo{
			Info:        item.Info,
			ProfileName: "",
			EntityId:    entityID,
		}
		s.gpus[gpuID].MigEnabled = true
		s.gpus[gpuID].GPUInstances = append(s.gpus[gpuID].GPUInstances, instanceInfo)
		gpuInstanceRefs[entityID] = gpuInstanceRef{
			gpuID:         gpuID,
			instanceIndex: len(s.gpus[gpuID].GPUInstances) - 1,
		}
		if s.gpus[gpuID].Status != dcgm.EntityStatusDetached {
			entities = append(entities, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: entityID})
		}
	}

	return gpuInstanceRefs, entities
}

// addComputeInstances attaches each compute instance in hierarchy to the GPU
// instance addGPUInstances stored for it, skipping instances it did not store.
func (s *Info) addComputeInstances(hierarchy dcgm.MigHierarchy_v2, gpuInstanceRefs map[uint]gpuInstanceRef) {
	for i := uint(0); i < hierarchy.Count; i++ {
		item := hierarchy.EntityList[i]
		if item.Parent.EntityGroupId != dcgm.FE_GPU_I {
			continue
		}

		instanceRef, found := gpuInstanceRefs[item.Parent.EntityId]
		if !found {
			continue
		}

		ciInfo := ComputeInstanceInfo{
			InstanceInfo: item.Info,
			ProfileName:  "",
			EntityId:     item.Entity.EntityId,
		}
		gpuInstance := &s.gpus[instanceRef.gpuID].GPUInstances[instanceRef.instanceIndex]
		gpuInstance.ComputeInstances = append(gpuInstance.ComputeInstances, ciInfo)
	}
}

// minimalGPUDeviceInfo builds enough GPU identity metadata for remote-hostengine
// metrics when the richer DCGM device-attributes API is unavailable.
//
// DCGM version fallback: a DCGM hostengine without the device-attributes API
// returns DCGM_ST_FUNCTION_NOT_FOUND. The name, UUID and PCI bus ID fields give
// the identity the labels need, and a placeholder UUID is used when those
// fields are unavailable too. discoverGPUs logs one warning for the GPUs that
// used it (gpuIdentityFallbacks). Remove it once every DCGM hostengine
// the exporter must work with provides the device-attributes API.
func minimalGPUDeviceInfo(gpuID uint, deviceInfoErr error) (dcgm.Device, bool, error) {
	diagnostic, ok := dcgmerrors.Classify(deviceInfoErr)
	if !ok || diagnostic.Code != dcgm.DCGM_ST_FUNCTION_NOT_FOUND {
		return dcgm.Device{}, false, deviceInfoErr
	}

	fields := []dcgm.Short{
		dcgm.DCGM_FI_DEV_NAME,
		dcgm.DCGM_FI_DEV_UUID,
		dcgm.DCGM_FI_DEV_PCI_BUSID,
	}
	values, err := dcgmprovider.Client().EntityGetLatestValues(dcgm.FE_GPU, gpuID, fields)
	if err != nil {
		diagnostic, ok := dcgmerrors.Classify(err)
		if !ok || diagnostic.Code != dcgm.DCGM_ST_FUNCTION_NOT_FOUND {
			return dcgm.Device{}, false, fmt.Errorf("query minimal GPU identity for gpu %d: %w", gpuID, err)
		}
		return syntheticMinimalGPUDeviceInfo(gpuID), true, nil
	}

	device := syntheticMinimalGPUDeviceInfo(gpuID)
	synthetic := true
	for _, value := range values {
		fieldValue := normalizeDCGMStringField(value.String())
		switch value.FieldID {
		case dcgm.DCGM_FI_DEV_NAME:
			device.Identifiers.Model = fieldValue
		case dcgm.DCGM_FI_DEV_UUID:
			device.UUID = fieldValue
			synthetic = fieldValue == "" || value.Status != dcgm.DCGM_ST_OK || value.FieldType != dcgm.DCGM_FT_STRING
		case dcgm.DCGM_FI_DEV_PCI_BUSID:
			device.PCI.BusID = fieldValue
		}
	}

	if device.UUID == "" {
		device.UUID = syntheticGPUUUID(gpuID)
	}

	return device, synthetic, nil
}

// gpuIdentityFallbacks collects the GPUs whose identity came from the DCGM
// version fallback in minimalGPUDeviceInfo, so one warning covers them all.
type gpuIdentityFallbacks struct {
	fieldIdentityGPUs   []uint
	placeholderUUIDGPUs []uint
	cause               error
}

// add records a GPU that used the identity fallback, and whether it got a
// placeholder UUID. cause is the device-attributes error that started it.
func (f *gpuIdentityFallbacks) add(gpuID uint, placeholderUUID bool, cause error) {
	f.fieldIdentityGPUs = append(f.fieldIdentityGPUs, gpuID)
	if placeholderUUID {
		f.placeholderUUIDGPUs = append(f.placeholderUUIDGPUs, gpuID)
	}
	f.cause = cause
}

// warn logs one warning for the GPUs that read identity from DCGM fields, and
// one for those labelled with a placeholder UUID. It logs nothing when no GPU
// used the fallback.
func (f gpuIdentityFallbacks) warn() {
	if len(f.fieldIdentityGPUs) == 0 {
		return
	}

	slog.Warn(gpuIdentityFallbackWarning,
		"gpu_ids", f.fieldIdentityGPUs,
		"error", f.cause,
		"hint", dcgmerrors.SupportedVersionHint)
	if len(f.placeholderUUIDGPUs) > 0 {
		slog.Warn(gpuPlaceholderUUIDWarning,
			"gpu_ids", f.placeholderUUIDGPUs,
			"hint", dcgmerrors.SupportedVersionHint)
	}
}

// syntheticMinimalGPUDeviceInfo builds the smallest stable GPU identity needed
// to collect and label remote-hostengine metrics when DCGM identity fields are
// not available through the remote connection.
func syntheticMinimalGPUDeviceInfo(gpuID uint) dcgm.Device {
	return dcgm.Device{
		GPU:           gpuID,
		UUID:          syntheticGPUUUID(gpuID),
		DCGMSupported: "Yes",
	}
}

// syntheticGPUUUID returns a deterministic placeholder for remote hostengine
// paths where DCGM exposes entity IDs but not GPU UUID identity fields.
func syntheticGPUUUID(gpuID uint) string {
	return fmt.Sprintf("GPU-%d", gpuID)
}

// normalizeDCGMStringField converts DCGM string sentinels into empty values.
func normalizeDCGMStringField(value string) string {
	switch value {
	case "", dcgm.DCGM_FT_STR_BLANK, dcgm.DCGM_FT_STR_NOT_FOUND, dcgm.DCGM_FT_STR_NOT_SUPPORTED, dcgm.DCGM_FT_STR_NOT_PERMISSIONED:
		return ""
	default:
		return value
	}
}

// WarnMIGInstancesWithoutComputeInstances logs one warning for every MIG GPU
// instance that has no compute instances. FB metrics are resolved through a
// compute instance's NVML handle, so an empty compute instance list can make
// FB metrics unavailable for that GPU instance.
func (s *Info) WarnMIGInstancesWithoutComputeInstances() {
	for gpuIndex := uint(0); gpuIndex < s.gpuCount; gpuIndex++ {
		gpu := s.gpus[gpuIndex]
		if !gpu.MigEnabled {
			continue
		}

		for _, instance := range gpu.GPUInstances {
			if len(instance.ComputeInstances) > 0 {
				continue
			}

			slog.Warn(migGPUInstanceWithoutCIWarning,
				slog.Uint64("gpu_id", uint64(gpu.DeviceInfo.GPU)),
				slog.Uint64("gpu_instance_entity_id", uint64(instance.EntityId)),
				slog.Uint64("nvml_instance_id", uint64(instance.Info.NvmlInstanceId)),
				slog.String("mig_profile", instance.ProfileName),
				slog.String("hint", migGPUInstanceWithoutCIWarningHint))
		}
	}
}

// initializeCPUInfo discovers selected CPUs and CPU cores, then validates the
// selectors. Initialize uses it for CPU and CPU-core registries.
func (s *Info) initializeCPUInfo(cOpt appconfig.DeviceOptions) error {
	s.cOpt = cOpt
	hierarchy, err := dcgmprovider.Client().GetCPUHierarchy()
	if err != nil {
		if !isOptionalModuleUnavailable(err) {
			return fmt.Errorf("query CPU hierarchy: %w", err)
		}
		return optionalTopologyDiscoveryError(err, ErrNoCPUsToMonitor, s.verifyCPUDevicePresence())
	}

	if hierarchy.NumCPUs <= 0 {
		return optionalTopologyDiscoveryError(nil, ErrNoCPUsToMonitor, s.verifyCPUDevicePresence())
	}

	s.populateCPUInfo(hierarchy, cOpt)
	if err := s.verifyCPUDevicePresence(); err != nil {
		return err
	}

	slog.Debug(fmt.Sprintf(deviceInitMessage, s.infoType))
	return nil
}

// populateCPUInfo selects CPUs and cores from the discovered hierarchy for metric collection.
func (s *Info) populateCPUInfo(hierarchy dcgm.CPUHierarchy_v2, cOpt appconfig.DeviceOptions) {
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
				EntityId: hierarchy.CPUs[i].CPUID,
				Cores:    monitoredCores,
				Serial:   hierarchy.CPUs[i].Serial,
			}

			s.cpus = append(s.cpus, cpu)
		}
	}
}

// initializeNvSwitchInfo discovers selected NVSwitches and their NVLink ports,
// then validates the selectors. Initialize uses it for switch and link
// registries.
func (s *Info) initializeNvSwitchInfo(sOpt appconfig.DeviceOptions) error {
	s.sOpt = sOpt
	switches, err := dcgmprovider.Client().GetEntityGroupEntities(dcgm.FE_SWITCH)
	if err != nil {
		return s.switchDiscoveryError(err)
	}

	if len(switches) <= 0 {
		return optionalTopologyDiscoveryError(nil, ErrNoSwitchesToMonitor, s.verifySwitchDevicePresence())
	}

	links, err := dcgmprovider.Client().GetNvLinkLinkStatus()
	if err != nil {
		return fmt.Errorf("query NVLink link status: %w", err)
	}

	s.populateNvSwitchInfo(switches, links, sOpt)
	if err := s.verifySwitchDevicePresence(); err != nil {
		return err
	}

	slog.Debug(fmt.Sprintf(deviceInitMessage, s.infoType))
	return nil
}

// switchDiscoveryError turns NVSwitch lookup failures into optional or startup errors.
func (s *Info) switchDiscoveryError(err error) error {
	if !isOptionalModuleUnavailable(err) {
		return fmt.Errorf("query NVSwitch entities: %w", err)
	}
	return optionalTopologyDiscoveryError(err, ErrNoSwitchesToMonitor, s.verifySwitchDevicePresence())
}

// optionalTopologyDiscoveryError keeps a missing-topology error and its cause for startup checks.
func optionalTopologyDiscoveryError(cause, missingTopologyErr, selectorErr error) error {
	if selectorErr == nil {
		selectorErr = missingTopologyErr
	}
	if cause == nil {
		return selectorErr
	}
	return fmt.Errorf("%w: %w", selectorErr, cause)
}

// populateNvSwitchInfo selects discovered switches and their links for metric collection.
func (s *Info) populateNvSwitchInfo(switches []uint, links []dcgm.NvLinkStatus, sOpt appconfig.DeviceOptions) {
	for i := 0; i < len(switches); i++ {
		// monitor only the Switches as per the device options input
		if sOpt.Flex || s.shouldMonitor(sOpt.MajorRange, switches[i]) {
			matchingLinks := s.matchingNvSwitchLinks(switches[i], links, sOpt)
			s.switches = append(s.switches, SwitchInfo{switches[i], matchingLinks})
		}
	}
}

// matchingNvSwitchLinks returns selected links that belong to one switch.
func (s *Info) matchingNvSwitchLinks(switchID uint, links []dcgm.NvLinkStatus, sOpt appconfig.DeviceOptions) []dcgm.NvLinkStatus {
	var matchingLinks []dcgm.NvLinkStatus
	for _, link := range links {
		// monitor only the NV Link as per the device options input
		if sOpt.Flex || s.shouldMonitor(sOpt.MinorRange, link.Index) {
			if link.ParentType == dcgm.FE_SWITCH && link.ParentId == switchID {
				matchingLinks = append(matchingLinks, link)
			}
		}
	}
	return matchingLinks
}

// isOptionalModuleUnavailable preserves typed status checks while supporting
// older go-dcgm paths that return the library's module-unavailable message.
func isOptionalModuleUnavailable(err error) bool {
	var dcgmErr *dcgm.Error
	if errors.As(err, &dcgmErr) {
		return dcgmErr.Code == dcgm.DCGM_ST_MODULE_NOT_LOADED
	}

	return strings.Contains(strings.ToLower(err.Error()), "module of dcgm that is not currently loaded")
}

// setGPUInstanceProfileName stores a resolved profile name on entityID. It
// returns false when the sampled DCGM entity is not a discovered GPU instance.
func (s *Info) setGPUInstanceProfileName(entityID uint, dcgmProfileName string) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		for j := range s.gpus[i].GPUInstances {
			if s.gpus[i].GPUInstances[j].EntityId == entityID {
				s.gpus[i].GPUInstances[j].ProfileName = s.getGPUInstanceProfileName(
					s.gpus[i], s.gpus[i].GPUInstances[j], dcgmProfileName)
				return true
			}
		}
	}

	return false
}

// getGPUInstanceProfileName prefers NVML's profile name and retains DCGM's
// value as a safe fallback. setGPUInstanceProfileName uses it after sampling
// the MIG instance name field.
func (s *Info) getGPUInstanceProfileName(gpu GPUInfo, instance GPUInstanceInfo, dcgmProfileName string) string {
	parentGPUUUID := instance.Info.GpuUuid
	if parentGPUUUID == "" {
		parentGPUUUID = gpu.DeviceInfo.UUID
	}

	nvmlProfileName, err := nvmlprovider.Client().GetGPUInstanceProfileName(parentGPUUUID, instance.Info.NvmlMigProfileId)
	if err != nil {
		slog.Debug("Falling back to DCGM MIG profile name", "entityID", instance.EntityId, "error", err)
		return dcgmProfileName
	}
	// Preserve the DCGM fallback if another NVML implementation violates the
	// concrete provider's non-empty-name contract.
	if nvmlProfileName == "" {
		slog.Debug("Falling back to DCGM MIG profile name", "entityID", instance.EntityId, "error", "NVML profile name is empty")
		return dcgmProfileName
	}

	return nvmlProfileName
}

// setMigProfileNames resolves sampled profile names using NVML when available
// and DCGM as fallback. It reports values that did not match the hierarchy.
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

// populateMigProfileNames samples profile names for discovered GPU instances
// and adds them to the MIG hierarchy. initializeGPUInfo calls it after the
// hierarchy is complete.
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

// gpuIDExists reports whether GPU gpuID appears in the discovered topology.
func (s *Info) gpuIDExists(gpuID int) bool {
	for i := uint(0); i < s.gpuCount; i++ {
		if s.gpus[i].DeviceInfo.GPU == uint(gpuID) {
			return true
		}
	}
	return false
}

// gpuInstanceIDExists reports whether GPU instance gpuInstanceID was discovered.
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

// cpuIDExists reports whether cpuID appears in the selected CPU topology.
func (s *Info) cpuIDExists(cpuID int) bool {
	for _, cpu := range s.cpus {
		if cpu.EntityId == uint(cpuID) {
			return true
		}
	}
	return false
}

// cpuCoreIDExists checks a minor selector across all selected CPUs because
// the selector does not name a parent CPU.
func (s *Info) cpuCoreIDExists(coreID int) bool {
	for _, cpu := range s.cpus {
		for _, core := range cpu.Cores {
			if core == uint(coreID) {
				return true
			}
		}
	}
	return false
}

// switchIDExists lets selector validation reject a requested switch absent
// from the topology returned by DCGM.
func (s *Info) switchIDExists(switchID int) bool {
	for _, sw := range s.switches {
		if sw.EntityId == uint(switchID) {
			return true
		}
	}
	return false
}

// linkIDExists checks a minor selector across all selected switches because
// the selector does not name a parent switch.
func (s *Info) linkIDExists(linkID int) bool {
	for _, sw := range s.switches {
		for _, link := range sw.NvLinks {
			if link.Index == uint(linkID) {
				return true
			}
		}
	}
	return false
}

// verifyDevicePresence checks that every explicit GPU and MIG selector was
// discovered. initializeGPUInfo calls it after building the GPU hierarchy.
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

// isMIGHierarchyNVMLUnavailable reports whether DCGM could not answer the MIG
// hierarchy query because local NVML is not loaded. Remote hostengine mode can
// still collect full-GPU metrics in that state, so this condition should not
// suppress all GPU metric collection.
func isMIGHierarchyNVMLUnavailable(err error) bool {
	if diagnostic, ok := dcgmerrors.Classify(err); ok {
		return diagnostic.Code == dcgm.DCGM_ST_NVML_NOT_LOADED
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "nvml") &&
		(strings.Contains(message, "not loaded") ||
			strings.Contains(message, "doesn't exist") ||
			strings.Contains(message, "does not exist"))
}

// verifyCPUDevicePresence checks that every explicit CPU and core selector was
// discovered. initializeCPUInfo calls it after building the CPU topology.
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

// shouldMonitor reports whether val belongs to a selector range. The -1
// sentinel selects every value; an empty range selects none.
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

// verifySwitchDevicePresence checks that every explicit NVSwitch and link
// selector was discovered. initializeNvSwitchInfo calls it after topology
// discovery.
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

// IsCPUWatched reports whether cpuID was discovered and selected for metrics.
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

// IsCoreWatched reports whether coreID is selected for metrics. Flexible selection
// accepts any IDs; otherwise the CPU must be discovered and selected, and the
// minor range must select the core or contain the wildcard.
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

// IsSwitchWatched reports whether switchID is selected for metrics. Switch
// collection uses it to avoid emitting unconfigured NVSwitch entities.
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

// IsLinkWatched reports whether linkIndex is selected for metrics. Flexible
// selection accepts any IDs. Otherwise the switch must be discovered and selected;
// a minor-range wildcard accepts any link index, while an explicit index must
// also exist on the switch.
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

// getCoreArray expands DCGM's CPU-core bitmask into core IDs. initializeCPUInfo
// uses it before applying the configured core selector.
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

// GetGPUInstanceIdentifier returns the stable GPU-and-instance identifier used
// in metric labels. It finds the GPU UUID in a Provider before composing it.
func GetGPUInstanceIdentifier(deviceInfo Provider, gpuuuid string, gpuInstanceID uint) string {
	for i := uint(0); i < deviceInfo.GPUCount(); i++ {
		if deviceInfo.GPU(i).DeviceInfo.UUID == gpuuuid {
			identifier := fmt.Sprintf("%d-%d", deviceInfo.GPU(i).DeviceInfo.GPU, gpuInstanceID)
			return identifier
		}
	}

	return ""
}

// ToBase64 returns Info's JSON representation encoded as base64. Diagnostic
// output uses it to carry topology in a single text value.
func (s *Info) ToBase64() string {
	jsonData, err := json.Marshal(s)
	if err != nil {
		return fmt.Sprintf("error marshaling device info: %v", err)
	}
	return base64.StdEncoding.EncodeToString(jsonData)
}

// ToBase64Provider encodes an Info-backed Provider for diagnostics. A different
// Provider implementation intentionally returns an empty JSON object because it
// may not expose every field required by Info's JSON contract.
func ToBase64Provider(provider Provider) string {
	if info, ok := provider.(*Info); ok {
		return info.ToBase64()
	}

	// Log warning and return empty JSON when type assertion fails
	slog.Warn("Failed to convert deviceinfo.Provider to *Info, returning empty JSON")
	return base64.StdEncoding.EncodeToString([]byte("{}"))
}

// MarshalJSON writes Info's public topology and selector fields. ToBase64 and
// configuration diagnostics use this stable, fixed-array-free representation.
func (s *Info) MarshalJSON() ([]byte, error) {
	result := map[string]any{
		"gpu_count":      s.GPUCount(),
		"gpus":           s.GPUs(),
		"switches":       s.Switches(),
		"cpus":           s.CPUs(),
		"gpu_options":    s.GOpts(),
		"switch_options": s.SOpts(),
		"cpu_options":    s.COpts(),
		"info_type":      s.InfoType(),
	}
	return json.Marshal(result)
}

// UnmarshalJSON restores the JSON representation written by MarshalJSON. It
// rejects counts that disagree with the GPU list before filling fixed storage.
func (s *Info) UnmarshalJSON(data []byte) error {
	// result mirrors the public JSON shape before validation copies it into
	// Info's fixed GPU storage.
	var result struct {
		GPUCount      uint                    `json:"gpu_count"`
		GPUs          []GPUInfo               `json:"gpus"`
		Switches      []SwitchInfo            `json:"switches"`
		CPUs          []CPUInfo               `json:"cpus"`
		GPUOptions    appconfig.DeviceOptions `json:"gpu_options"`
		SwitchOptions appconfig.DeviceOptions `json:"switch_options"`
		CPUOptions    appconfig.DeviceOptions `json:"cpu_options"`
		InfoType      dcgm.Field_Entity_Group `json:"info_type"`
	}

	err := json.Unmarshal(data, &result)
	if err != nil {
		return fmt.Errorf("failed to unmarshal device info: %w", err)
	}

	// Populate the Info struct fields
	s.gOpt = result.GPUOptions
	s.sOpt = result.SwitchOptions
	s.cOpt = result.CPUOptions
	s.infoType = result.InfoType

	// Copy GPUs to the fixed-size array
	if len(result.GPUs) > int(dcgm.MAX_NUM_DEVICES) {
		return fmt.Errorf("too many GPUs: %d, maximum allowed: %d", len(result.GPUs), dcgm.MAX_NUM_DEVICES)
	}

	// Verify consistency between original gpu_count from JSON and actual GPU slice length
	// This prevents silent overwrites and ensures data consistency
	if result.GPUCount != uint(len(result.GPUs)) {
		return fmt.Errorf("data inconsistency: gpu_count field (%d) does not match actual GPU slice length (%d)", result.GPUCount, len(result.GPUs))
	}

	// Only set gpuCount after validation to ensure consistency
	s.gpuCount = result.GPUCount
	copy(s.gpus[:], result.GPUs)

	// Copy other slices
	s.switches = result.Switches
	s.cpus = result.CPUs

	return nil
}
