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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
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

// Initialize creates the process-wide DCGM provider from config. The command
// layer calls it at startup and recovery, and decides whether a returned error
// ends the process or schedules another recovery attempt.
func Initialize(config *appconfig.Config) error {
	provider, err := newDCGMProvider(config)
	if err != nil {
		return err
	}
	dcgmInterface = provider
	return nil
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

// dcgmProvider adapts the go-dcgm singleton to the exporter DCGM interface.
// Initialize publishes it only after DCGM and its fields module are ready.
type dcgmProvider struct {
	shutdown func()
}

// newDCGMProvider initializes a DCGM provider without publishing it. It owns
// a successful DCGM shutdown callback until FieldsInit succeeds, after which
// Initialize transfers the completed provider to the process-wide client.
func newDCGMProvider(config *appconfig.Config) (DCGM, error) {
	// Check if a DCGM client already exists and return it if so.
	if Client() != nil {
		slog.Info("DCGM already initialized")
		return Client(), nil
	}

	cleanup, err := initializeDCGMSession(config)
	if err != nil {
		return nil, err
	}
	client := dcgmProvider{shutdown: cleanup}

	// Initialize the DcgmFields module
	if val := dcgmFieldsInitFunc(); val < 0 {
		logDCGMFieldsInitFailure(config, val)
		client.shutdown()
		return nil, fmt.Errorf("initialize DCGM fields module: status %d", val)
	} else {
		slog.Info("Initialized DCGM Fields module.")
	}

	return newWatchClient(client), nil
}

// initializeDCGMSession starts embedded DCGM or connects to a remote host engine.
// It releases partial initialization on failure and returns the successful
// session's shutdown callback to the provider setup code.
func initializeDCGMSession(config *appconfig.Config) (func(), error) {
	// Connect to a remote DCGM host engine if configured.
	if config.UseRemoteHE {
		slog.Info("Attempting to connect to remote hostengine at " + config.RemoteHEInfo)
		cleanup, err := initStandaloneDCGMFunc(config.RemoteHEInfo, "0")
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			logDCGMInitFailure(
				config,
				dcgmInitModeStandalone,
				err,
				slog.String("hint", remoteHostengineHint),
			)
			return nil, fmt.Errorf("initialize standalone DCGM: %w", err)
		}
		return cleanup, nil
	} else {
		if config.EnableDCGMLog {
			os.Setenv("__DCGM_DBG_FILE", "-")
			os.Setenv("__DCGM_DBG_LVL", config.DCGMLogLevel)
		}

		// Initialize a local/embedded DCGM instance.
		slog.Info("Attempting to initialize DCGM.")
		cleanup, err := initEmbeddedDCGMFunc()
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			logDCGMInitFailure(config, dcgmInitModeEmbedded, err)
			return nil, fmt.Errorf("initialize embedded DCGM: %w", err)
		}
		return cleanup, nil
	}
}

// AddEntityToGroup forwards entity membership to the active DCGM session.
func (d dcgmProvider) AddEntityToGroup(
	groupID dcgm.GroupHandle, entityGroupID dcgm.Field_Entity_Group,
	entityID uint,
) error {
	return dcgm.AddEntityToGroup(groupID, entityGroupID, entityID)
}

// AddLinkEntityToGroup forwards link membership to the active DCGM session.
func (d dcgmProvider) AddLinkEntityToGroup(groupID dcgm.GroupHandle, index uint, entityGroupID dcgm.Field_Entity_Group, parentID uint) error {
	return dcgm.AddLinkEntityToGroup(groupID, index, entityGroupID, parentID)
}

// CreateFakeEntities asks DCGM to create the supplied synthetic MIG entities.
func (d dcgmProvider) CreateFakeEntities(entities []dcgm.MigHierarchyInfo) ([]uint, error) {
	return dcgm.CreateFakeEntities(entities)
}

// CreateGroup creates a group through the active DCGM session.
func (d dcgmProvider) CreateGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.CreateGroup(groupName)
}

// DestroyGroup releases a group through the active DCGM session.
func (d dcgmProvider) DestroyGroup(groupId dcgm.GroupHandle) error {
	return dcgm.DestroyGroup(groupId)
}

// EntitiesGetLatestValues reads latest fields for the supplied typed entities.
func (d dcgmProvider) EntitiesGetLatestValues(
	entities []dcgm.GroupEntityPair, fields []dcgm.Short, flags uint,
) ([]dcgm.FieldValue_v2, error) {
	return dcgm.EntitiesGetLatestValues(entities, fields, flags)
}

// EntityGetLatestValues reads latest fields for one entity group and ID.
func (d dcgmProvider) EntityGetLatestValues(
	entityGroup dcgm.Field_Entity_Group, entityID uint, fields []dcgm.Short,
) ([]dcgm.FieldValue_v1,
	error,
) {
	return dcgm.EntityGetLatestValues(entityGroup, entityID, fields)
}

// Fv2_String formats a DCGM field value for logs and diagnostics.
func (d dcgmProvider) Fv2_String(fv dcgm.FieldValue_v2) string {
	return dcgm.Fv2_String(fv)
}

// FieldGetByID returns field metadata from the active DCGM session.
func (d dcgmProvider) FieldGetByID(fieldID dcgm.Short) (dcgm.FieldMeta, error) {
	return dcgm.FieldGetByID(fieldID)
}

// FieldGroupCreate creates a field group through the active DCGM session.
func (d dcgmProvider) FieldGroupCreate(fieldsGroupName string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	return dcgm.FieldGroupCreate(fieldsGroupName, fields)
}

// FieldGroupDestroy releases a field group through the active DCGM session.
func (d dcgmProvider) FieldGroupDestroy(fieldsGroup dcgm.FieldHandle) error {
	return dcgm.FieldGroupDestroy(fieldsGroup)
}

// GetAllDeviceCount returns the device count reported by DCGM.
func (d dcgmProvider) GetAllDeviceCount() (uint, error) {
	return dcgm.GetAllDeviceCount()
}

// GetGPUStatus returns DCGM's lifecycle state, or Unknown when its query fails.
func (d dcgmProvider) GetGPUStatus(gpuID uint) dcgm.EntityStatus {
	return dcgm.GetGPUStatus(gpuID)
}

// GetCPUHierarchy returns the CPU hierarchy in the v2 shape, falling back to v1
// with empty CPU serials when v2 is unavailable.
//
// DCGM version fallback: a DCGM library or hostengine without
// dcgmGetCpuHierarchy_v2 rejects it, and the v1 call still lists CPUs and
// cores. The fallback logs a warning each time it is used. Remove it once
// every DCGM library and hostengine the exporter must work with provides
// dcgmGetCpuHierarchy_v2.
func (d dcgmProvider) GetCPUHierarchy() (dcgm.CPUHierarchy_v2, error) {
	hierarchy, err := getCPUHierarchyV2Func()
	if err == nil {
		return hierarchy, nil
	}

	if !isCPUHierarchyV2Unsupported(err) {
		return hierarchy, err
	}

	slog.Warn("DCGM CPU hierarchy v2 unavailable; listing CPUs and cores with the v1 call instead, "+
		"without CPU serial numbers",
		slog.String("error", err.Error()),
		slog.String("hint", dcgmerrors.SupportedVersionHint))

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

// GetDeviceInfo retrieves the DCGM device record for gpuID.
func (d dcgmProvider) GetDeviceInfo(gpuID uint) (dcgm.Device, error) {
	return dcgm.GetDeviceInfo(gpuID)
}

// GetErrorMeta returns DCGM-owned metadata for a health or diagnostic error code.
func (d dcgmProvider) GetErrorMeta(code dcgm.HealthCheckErrorCode) *dcgm.ErrorMeta {
	return dcgm.GetErrorMeta(code)
}

// GetGPUInstanceHierarchy retrieves DCGM's current MIG hierarchy.
func (d dcgmProvider) GetGPUInstanceHierarchy() (dcgm.MigHierarchy_v2, error) {
	return dcgm.GetGPUInstanceHierarchy()
}

// GetMultipleValuesForField reads retained values for one device and field over a time range.
func (d dcgmProvider) GetMultipleValuesForField(
	gpuID uint, fieldID dcgm.Short, maxSamples int, startTime, endTime time.Time,
) ([]dcgm.FieldValue_v1, error) {
	return dcgm.GetMultipleValuesForField(gpuID, fieldID, maxSamples, startTime, endTime)
}

// GetNvLinkLinkStatus retrieves current link status from DCGM.
func (d dcgmProvider) GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error) {
	return dcgm.GetNvLinkLinkStatus()
}

// GetAllFieldPolicies retrieves the policies currently configured in DCGM.
func (d dcgmProvider) GetAllFieldPolicies() ([]dcgm.FieldPolicy, error) {
	policies, err := dcgm.GetAllFieldPolicies()
	if err != nil {
		return nil, fmt.Errorf("get field policies from DCGM: %w", err)
	}
	return policies, nil
}

// GetSupportedDevices lists GPU IDs supported by the active DCGM session.
func (d dcgmProvider) GetSupportedDevices() ([]uint, error) {
	return dcgm.GetSupportedDevices()
}

// GetSupportedMetricGroups lists the metric groups available for gpuID.
func (d dcgmProvider) GetSupportedMetricGroups(gpuID uint) ([]dcgm.MetricGroup, error) {
	return dcgm.GetSupportedMetricGroups(gpuID)
}

// GetValuesSince reads samples after sinceTime and returns DCGM's next cursor.
func (d dcgmProvider) GetValuesSince(
	gpuGroup dcgm.GroupHandle, fieldGroup dcgm.FieldHandle, sinceTime time.Time,
) ([]dcgm.FieldValue_v2, time.Time, error) {
	return dcgm.GetValuesSince(gpuGroup, fieldGroup, sinceTime)
}

// GroupAllGPUs returns DCGM's built-in group handle containing all GPUs.
func (d dcgmProvider) GroupAllGPUs() dcgm.GroupHandle {
	return dcgm.GroupAllGPUs()
}

// InjectFieldValue injects a synthetic sample into DCGM for one GPU field.
func (d dcgmProvider) InjectFieldValue(
	gpu uint, fieldID dcgm.Short, fieldType uint, status int, ts int64, value interface{},
) error {
	return dcgm.InjectFieldValue(gpu, fieldID, fieldType, status, ts, value)
}

// LinkGetLatestValues reads latest field values for a link under its parent entity.
func (d dcgmProvider) LinkGetLatestValues(index uint, parentType dcgm.Field_Entity_Group, parentID uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error,
) {
	return dcgm.LinkGetLatestValues(index, parentType, parentID, fields)
}

// NewDefaultGroup creates a DCGM group that holds all GPUs.
func (d dcgmProvider) NewDefaultGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.NewDefaultGroup(groupName)
}

// UpdateAllFields asks DCGM to refresh currently watched fields.
func (d dcgmProvider) UpdateAllFields() error {
	return dcgm.UpdateAllFields()
}

// WatchFieldValue installs a direct watch for one entity and field.
func (d dcgmProvider) WatchFieldValue(
	gpuID uint, fieldID dcgm.Short, updateFreq, maxKeepAge time.Duration, maxKeepSamples int,
) error {
	return dcgm.WatchFieldValue(gpuID, fieldID, updateFreq, maxKeepAge, maxKeepSamples)
}

// WatchFieldsWithGroupExNoRefresh installs a field-group watch for a DCGM
// group without asking DCGM to update every watched field afterwards.
func (d dcgmProvider) WatchFieldsWithGroupExNoRefresh(
	fieldsGroup dcgm.FieldHandle, group dcgm.GroupHandle, updateFreq int64, maxKeepAge float64,
	maxKeepSamples int32,
) error {
	return dcgm.WatchFieldsWithGroupExNoRefresh(fieldsGroup, group, updateFreq, maxKeepAge, maxKeepSamples)
}

// UnwatchFields removes a field-group watch from a DCGM group.
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

// HealthSet configures the health systems checked for groupID.
func (d dcgmProvider) HealthSet(groupID dcgm.GroupHandle, systems dcgm.HealthSystem) error {
	return dcgm.HealthSet(groupID, systems)
}

// HealthGet returns the health systems configured for groupID.
func (d dcgmProvider) HealthGet(groupID dcgm.GroupHandle) (dcgm.HealthSystem, error) {
	return dcgm.HealthGet(groupID)
}

// HealthCheck runs the configured health checks for groupID.
func (d dcgmProvider) HealthCheck(groupID dcgm.GroupHandle) (dcgm.HealthResponse, error) {
	return dcgm.HealthCheck(groupID)
}

// HealthGetSupportedSystems returns the health watches the connected DCGM
// hostengine supports. It returns an error when the loaded DCGM library has no
// such query.
func (d dcgmProvider) HealthGetSupportedSystems() (dcgm.HealthSystem, error) {
	return dcgm.HealthGetSupportedSystems()
}

// GetGroupInfo retrieves the metadata and members of groupID.
func (d dcgmProvider) GetGroupInfo(groupID dcgm.GroupHandle) (*dcgm.GroupInfo, error) {
	return dcgm.GetGroupInfo(groupID)
}

// GetNvLinkP2PStatus retrieves the current NVLink peer-to-peer matrix.
func (d dcgmProvider) GetNvLinkP2PStatus() (dcgm.NvLinkP2PStatus, error) {
	return dcgm.GetNvLinkP2PStatus()
}
