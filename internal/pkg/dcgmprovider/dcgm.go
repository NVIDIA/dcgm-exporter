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
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

var dcgmInterface DCGM

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
		cleanup, err := dcgm.Init(dcgm.Standalone, config.RemoteHEInfo, "0")
		if err != nil {
			cleanup()
			slog.Error(err.Error())
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
		cleanup, err := dcgm.Init(dcgm.Embedded)
		if err != nil {
			slog.Error(err.Error())
			os.Exit(1)
		}
		client.shutdown = cleanup
	}

	// Initialize the DcgmFields module
	if val := dcgm.FieldsInit(); val < 0 {
		slog.Error(fmt.Sprintf("Failed to initialize DCGM Fields module; err: %d", val))
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

func (d dcgmProvider) AddLinkEntityToGroup(groupID dcgm.GroupHandle, index uint, parentID uint) error {
	return dcgm.AddLinkEntityToGroup(groupID, index, parentID)
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

func (d dcgmProvider) FieldGetByID(fieldID dcgm.Short) dcgm.FieldMeta {
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

func (d dcgmProvider) GetCPUHierarchy() (dcgm.CPUHierarchy_v1, error) {
	return dcgm.GetCPUHierarchy()
}

func (d dcgmProvider) GetDeviceInfo(gpuID uint) (dcgm.Device, error) {
	return dcgm.GetDeviceInfo(gpuID)
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

func (d dcgmProvider) LinkGetLatestValues(index uint, parentID uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error,
) {
	return dcgm.LinkGetLatestValues(index, parentID, fields)
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
