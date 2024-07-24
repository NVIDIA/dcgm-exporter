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
	"os"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

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
		logrus.Info("DCGM already initialized")
		return Client()
	}

	client := dcgmProvider{}

	// Connect to a remote DCGM host engine if configured.
	if config.UseRemoteHE {
		logrus.Info("Attempting to connect to remote hostengine at ", config.RemoteHEInfo)
		cleanup, err := dcgm.Init(dcgm.Standalone, config.RemoteHEInfo, "0")
		if err != nil {
			cleanup()
			logrus.Fatal(err)
		}
		client.shutdown = cleanup
	} else {
		if config.EnableDCGMLog {
			os.Setenv("__DCGM_DBG_FILE", "-")
			os.Setenv("__DCGM_DBG_LVL", config.DCGMLogLevel)
		}

		// Initialize a local/embedded DCGM instance.
		logrus.Info("Attempting to initialize DCGM.")
		cleanup, err := dcgm.Init(dcgm.Embedded)
		if err != nil {
			logrus.Fatal(err)
		}
		client.shutdown = cleanup
	}

	// Initialize the DcgmFields module
	if val := dcgm.FieldsInit(); val < 0 {
		logrus.Fatalf("Failed to initialize DCGM Fields module; err: %d", val)
	} else {
		logrus.Infof("Initialized DCGM Fields module.")
	}

	return client
}

func (d dcgmProvider) AddEntityToGroup(
	groupId dcgm.GroupHandle, entityGroupId dcgm.Field_Entity_Group,
	entityId uint,
) error {
	return dcgm.AddEntityToGroup(groupId, entityGroupId, entityId)
}

func (d dcgmProvider) AddLinkEntityToGroup(groupId dcgm.GroupHandle, index uint, parentId uint) error {
	return dcgm.AddLinkEntityToGroup(groupId, index, parentId)
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
	entityGroup dcgm.Field_Entity_Group, entityId uint, fields []dcgm.Short,
) ([]dcgm.FieldValue_v1,
	error,
) {
	return dcgm.EntityGetLatestValues(entityGroup, entityId, fields)
}

func (d dcgmProvider) Fv2_String(fv dcgm.FieldValue_v2) string {
	return dcgm.Fv2_String(fv)
}

func (d dcgmProvider) FieldGetById(fieldId dcgm.Short) dcgm.FieldMeta {
	return dcgm.FieldGetById(fieldId)
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

func (d dcgmProvider) GetCpuHierarchy() (dcgm.CpuHierarchy_v1, error) {
	return dcgm.GetCpuHierarchy()
}

func (d dcgmProvider) GetDeviceInfo(gpuId uint) (dcgm.Device, error) {
	return dcgm.GetDeviceInfo(gpuId)
}

func (d dcgmProvider) GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error) {
	return dcgm.GetEntityGroupEntities(entityGroup)
}

func (d dcgmProvider) GetGpuInstanceHierarchy() (dcgm.MigHierarchy_v2, error) {
	return dcgm.GetGpuInstanceHierarchy()
}

func (d dcgmProvider) GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error) {
	return dcgm.GetNvLinkLinkStatus()
}

func (d dcgmProvider) GetSupportedDevices() ([]uint, error) {
	return dcgm.GetSupportedDevices()
}

func (d dcgmProvider) GetSupportedMetricGroups(gpuId uint) ([]dcgm.MetricGroup, error) {
	return dcgm.GetSupportedMetricGroups(gpuId)
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
	gpu uint, fieldID uint, fieldType uint, status int, ts int64, value interface{},
) error {
	return dcgm.InjectFieldValue(gpu, fieldID, fieldType, status, ts, value)
}

func (d dcgmProvider) LinkGetLatestValues(index uint, parentId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error,
) {
	return dcgm.LinkGetLatestValues(index, parentId, fields)
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
	logrus.Info("Attempting to terminate DCGM Fields module.")
	if val := dcgm.FieldsTerm(); val < 0 {
		logrus.Errorf("Failed to terminate DCGM Fields module; err: %d", val)
	}

	// Shuts down the DCGM instance.
	logrus.Info("Attempting to terminate DCGM.")
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
