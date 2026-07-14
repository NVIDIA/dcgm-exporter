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

package dcgmprovider

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

var (
	dcgmInterface DCGM

	// These unexported function variables are test seams for DCGM initialization paths.
	// Production code leaves them bound to the go-dcgm package functions.
	initStandaloneDCGMFunc = func(remoteHostengineInfo string, socketFlag string) (func(), error) {
		return dcgm.Init(dcgm.Standalone, remoteHostengineInfo, socketFlag)
	}
	initEmbeddedDCGMFunc = func() (func(), error) {
		return dcgm.Init(dcgm.Embedded)
	}
	dcgmFieldsInitFunc    = dcgm.FieldsInit
	getCPUHierarchyV2Func = dcgm.GetCPUHierarchy_v2
	// Internal v1 fallback seam; callers and mocks use GetCPUHierarchy.
	getCPUHierarchyV1Func = dcgm.GetCPUHierarchy
)

const (
	// Legacy go-dcgm fallback strings from DCGM's dcgmlib/src/dcgm_errors.c.
	cpuHierarchyV2VersionMismatchText  = "api version mismatch"
	cpuHierarchyV2FunctionNotFoundText = "the requested function was not found"
)

// Initialize sets up the Singleton DCGM interface using the provided configuration.
func Initialize(config *appconfig.Config) {
	dcgmInterface = newDCGMProvider(config)
}

// reset clears the current DCGM interface instance.
func reset() {
	dcgmInterface = nil
}

// Client retrieves the current DCGM interface instance.
func Client() DCGM {
	return dcgmInterface
}

// SetClient sets the current DCGM interface instance to the provided one.
func SetClient(d DCGM) {
	dcgmInterface = d
}

// dcgmProvider implements DCGM Interface
type dcgmProvider struct {
	shutdown      func()
	moduleCleanup func()
}

// newDCGMProvider initializes a new DCGM provider based on the provided configuration
func newDCGMProvider(config *appconfig.Config) DCGM {
	// Check if a DCGM client already exists and return it if so.
	if Client() != nil {
		slog.Info("DCGM already initialized")
		return Client()
	}

	client := dcgmProvider{}

	// Connect to a remote DCGM host engine if configured.
	if config.UseRemoteHE {
		slog.Info("Attempting to connect to remote hostengine at " + config.RemoteHEInfo)
		cleanup, err := initStandaloneDCGMFunc(config.RemoteHEInfo, "0")
		if err != nil {
			// Don't call cleanup on error - initialization failed, nothing to clean up
			logDCGMInitFailure(
				config,
				dcgmInitModeStandalone,
				err,
				slog.String("hint", remoteHostengineHint),
			)
			os.Exit(1)
		}
		client.shutdown = cleanup
	} else {
		if config.EnableDCGMLog {
			os.Setenv("__DCGM_DBG_FILE", "-")
			os.Setenv("__DCGM_DBG_LVL", config.DCGMLogLevel)
		}

		// Initialize a local/embedded DCGM instance.
		slog.Info("Attempting to initialize DCGM.")
		cleanup, err := initEmbeddedDCGMFunc()
		if err != nil {
			logDCGMInitFailure(config, dcgmInitModeEmbedded, err)
			os.Exit(1)
		}
		client.shutdown = cleanup
	}

	// Initialize the DcgmFields module
	if val := dcgmFieldsInitFunc(); val < 0 {
		logDCGMFieldsInitFailure(config, val)
		os.Exit(1)
	} else {
		slog.Info("Initialized DCGM Fields module.")
	}

	return client
}

func (d dcgmProvider) AddEntityToGroup(
	groupID dcgm.GroupHandle, entityGroupID dcgm.Field_Entity_Group,
	entityID uint,
) error {
	return dcgm.AddEntityToGroup(groupID, entityGroupID, entityID)
}

func (d dcgmProvider) AddLinkEntityToGroup(groupID dcgm.GroupHandle, index uint, entityGroupID dcgm.Field_Entity_Group, parentID uint) error {
	return dcgm.AddLinkEntityToGroup(groupID, index, entityGroupID, parentID)
}

func (d dcgmProvider) CreateFakeEntities(entities []dcgm.MigHierarchyInfo) ([]uint, error) {
	return dcgm.CreateFakeEntities(entities)
}

func (d dcgmProvider) CreateGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.CreateGroup(groupName)
}

func (d dcgmProvider) DestroyGroup(groupId dcgm.GroupHandle) error {
	return dcgm.DestroyGroup(groupId)
}

func (d dcgmProvider) EntitiesGetLatestValues(
	entities []dcgm.GroupEntityPair, fields []dcgm.Short, flags uint,
) ([]dcgm.FieldValue_v2, error) {
	return dcgm.EntitiesGetLatestValues(entities, fields, flags)
}

func (d dcgmProvider) EntityGetLatestValues(
	entityGroup dcgm.Field_Entity_Group, entityID uint, fields []dcgm.Short,
) ([]dcgm.FieldValue_v1,
	error,
) {
	return dcgm.EntityGetLatestValues(entityGroup, entityID, fields)
}

func (d dcgmProvider) Fv2_String(fv dcgm.FieldValue_v2) string {
	return dcgm.Fv2_String(fv)
}

func (d dcgmProvider) FieldGetByID(fieldID dcgm.Short) (dcgm.FieldMeta, error) {
	return dcgm.FieldGetByID(fieldID)
}

func (d dcgmProvider) FieldGroupCreate(fieldsGroupName string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	return dcgm.FieldGroupCreate(fieldsGroupName, fields)
}

func (d dcgmProvider) FieldGroupDestroy(fieldsGroup dcgm.FieldHandle) error {
	return dcgm.FieldGroupDestroy(fieldsGroup)
}

func (d dcgmProvider) GetAllDeviceCount() (uint, error) {
	return dcgm.GetAllDeviceCount()
}

// GetCPUHierarchy returns the CPU hierarchy in the v2 shape, falling back to v1
// with empty CPU serials when v2 is unavailable.
func (d dcgmProvider) GetCPUHierarchy() (dcgm.CPUHierarchy_v2, error) {
	hierarchy, err := getCPUHierarchyV2Func()
	if err == nil {
		return hierarchy, nil
	}

	if !isCPUHierarchyV2Unsupported(err) {
		return hierarchy, err
	}

	slog.Warn("Falling back to dcgmGetCpuHierarchy after dcgmGetCpuHierarchy_v2 failed",
		slog.String("error", err.Error()))

	v1Hierarchy, fallbackErr := getCPUHierarchyV1Func()
	if fallbackErr != nil {
		return dcgm.CPUHierarchy_v2{}, fmt.Errorf("fallback to dcgmGetCpuHierarchy failed after dcgmGetCpuHierarchy_v2 was unsupported: %w", fallbackErr)
	}

	return cpuHierarchyV1AsV2(v1Hierarchy), nil
}

// isCPUHierarchyV2Unsupported reports whether a v2 CPU hierarchy error can
// safely fall back to the legacy v1 hierarchy call.
func isCPUHierarchyV2Unsupported(err error) bool {
	var dcgmErr *dcgm.Error
	if errors.As(err, &dcgmErr) {
		switch int(dcgmErr.Code) {
		case dcgm.DCGM_ST_VER_MISMATCH, dcgm.DCGM_ST_FUNCTION_NOT_FOUND:
			return true
		}
	}

	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, cpuHierarchyV2VersionMismatchText) ||
		strings.Contains(errText, cpuHierarchyV2FunctionNotFoundText)
}

// cpuHierarchyV1AsV2 adapts the legacy CPU hierarchy shape to the v2 shape
// without serial values.
func cpuHierarchyV1AsV2(v1Hierarchy dcgm.CPUHierarchy_v1) dcgm.CPUHierarchy_v2 {
	numCPUs := min(v1Hierarchy.NumCPUs, dcgm.MAX_NUM_CPUS)
	hierarchy := dcgm.CPUHierarchy_v2{
		NumCPUs: numCPUs,
	}
	for i := uint(0); i < numCPUs; i++ {
		hierarchy.CPUs[i] = dcgm.CPUHierarchyCPU_v2{
			CPUID:      v1Hierarchy.CPUs[i].CPUID,
			OwnedCores: append([]uint64(nil), v1Hierarchy.CPUs[i].OwnedCores...),
		}
	}

	return hierarchy
}

func (d dcgmProvider) GetDeviceInfo(gpuID uint) (dcgm.Device, error) {
	return dcgm.GetDeviceInfo(gpuID)
}

// GetErrorMeta returns DCGM-owned metadata for a health or diagnostic error code.
func (d dcgmProvider) GetErrorMeta(code dcgm.HealthCheckErrorCode) *dcgm.ErrorMeta {
	return dcgm.GetErrorMeta(code)
}

func (d dcgmProvider) GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error) {
	return dcgm.GetEntityGroupEntities(entityGroup)
}

func (d dcgmProvider) GetGPUInstanceHierarchy() (dcgm.MigHierarchy_v2, error) {
	return dcgm.GetGPUInstanceHierarchy()
}

func (d dcgmProvider) GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error) {
	return dcgm.GetNvLinkLinkStatus()
}

func (d dcgmProvider) GetSupportedDevices() ([]uint, error) {
	return dcgm.GetSupportedDevices()
}

func (d dcgmProvider) GetSupportedMetricGroups(gpuID uint) ([]dcgm.MetricGroup, error) {
	return dcgm.GetSupportedMetricGroups(gpuID)
}

func (d dcgmProvider) GetValuesSince(
	gpuGroup dcgm.GroupHandle, fieldGroup dcgm.FieldHandle, sinceTime time.Time,
) ([]dcgm.FieldValue_v2, time.Time, error) {
	return dcgm.GetValuesSince(gpuGroup, fieldGroup, sinceTime)
}

func (d dcgmProvider) GroupAllGPUs() dcgm.GroupHandle {
	return dcgm.GroupAllGPUs()
}

func (d dcgmProvider) InjectFieldValue(
	gpu uint, fieldID dcgm.Short, fieldType uint, status int, ts int64, value interface{},
) error {
	return dcgm.InjectFieldValue(gpu, fieldID, fieldType, status, ts, value)
}

func (d dcgmProvider) LinkGetLatestValues(index uint, parentType dcgm.Field_Entity_Group, parentID uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error,
) {
	return dcgm.LinkGetLatestValues(index, parentType, parentID, fields)
}

func (d dcgmProvider) NewDefaultGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.NewDefaultGroup(groupName)
}

func (d dcgmProvider) UpdateAllFields() error {
	return dcgm.UpdateAllFields()
}

func (d dcgmProvider) WatchFieldsWithGroupEx(
	fieldsGroup dcgm.FieldHandle, group dcgm.GroupHandle, updateFreq int64, maxKeepAge float64,
	maxKeepSamples int32,
) error {
	return dcgm.WatchFieldsWithGroupEx(fieldsGroup, group, updateFreq, maxKeepAge, maxKeepSamples)
}

func (d dcgmProvider) UnwatchFields(fieldsGroup dcgm.FieldHandle, group dcgm.GroupHandle) error {
	return dcgm.UnwatchFields(fieldsGroup, group)
}

// Cleanup performs cleanup operations for the DCGM provider, including terminating modules and shutting down DCGM.
func (d dcgmProvider) Cleanup() {
	// Terminates the DcgmFields module
	slog.Info("Attempting to terminate DCGM Fields module.")
	if val := dcgm.FieldsTerm(); val < 0 {
		slog.Error(fmt.Sprintf("Failed to terminate DCGM Fields module; err: %d", val))
	}

	// Shuts down the DCGM instance.
	slog.Info("Attempting to terminate DCGM.")
	d.shutdown()

	reset()
}

func (d dcgmProvider) HealthSet(groupID dcgm.GroupHandle, systems dcgm.HealthSystem) error {
	return dcgm.HealthSet(groupID, systems)
}

func (d dcgmProvider) HealthGet(groupID dcgm.GroupHandle) (dcgm.HealthSystem, error) {
	return dcgm.HealthGet(groupID)
}

func (d dcgmProvider) HealthCheck(groupID dcgm.GroupHandle) (dcgm.HealthResponse, error) {
	return dcgm.HealthCheck(groupID)
}

func (d dcgmProvider) GetGroupInfo(groupID dcgm.GroupHandle) (*dcgm.GroupInfo, error) {
	return dcgm.GetGroupInfo(groupID)
}

func (d dcgmProvider) GetNvLinkP2PStatus() (dcgm.NvLinkP2PStatus, error) {
	return dcgm.GetNvLinkP2PStatus()
}
