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
	"math/rand"
	"os"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/pkg/common"
)

var dcgmProvider DCGMProvider

func Initialize(config *common.Config) {
	dcgmProvider = newDCGMProvider(config)
}

func reset() {
	dcgmProvider = nil
}

func Client() DCGMProvider {
	return dcgmProvider
}

func SetClient(d DCGMProvider) {
	dcgmProvider = d
}

type dcgmProviderImpl struct {
	shutdown      func()
	moduleCleanup func()
}

func newDCGMProvider(config *common.Config) DCGMProvider {
	if Client() != nil {
		logrus.Info("DCGM already initialized")
		return Client()
	}

	client := dcgmProviderImpl{}

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

		logrus.Info("Attempting to initialize DCGM.")
		cleanup, err := dcgm.Init(dcgm.Embedded)
		if err != nil {
			cleanup()
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

	// Initialize NVML Provider
	nvmlprovider.Initialize()

	return client
}

func (d dcgmProviderImpl) AddEntityToGroup(
	groupId dcgm.GroupHandle, entityGroupId dcgm.Field_Entity_Group,
	entityId uint,
) error {
	return dcgm.AddEntityToGroup(groupId, entityGroupId, entityId)
}

func (d dcgmProviderImpl) AddLinkEntityToGroup(groupId dcgm.GroupHandle, index uint, parentId uint) error {
	return dcgm.AddLinkEntityToGroup(groupId, index, parentId)
}

func (d dcgmProviderImpl) CreateGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.CreateGroup(groupName)
}

func (d dcgmProviderImpl) DestroyGroup(groupId dcgm.GroupHandle) error {
	return dcgm.DestroyGroup(groupId)
}

func (d dcgmProviderImpl) EntitiesGetLatestValues(
	entities []dcgm.GroupEntityPair, fields []dcgm.Short, flags uint,
) ([]dcgm.FieldValue_v2, error) {
	return dcgm.EntitiesGetLatestValues(entities, fields, flags)
}

func (d dcgmProviderImpl) EntityGetLatestValues(
	entityGroup dcgm.Field_Entity_Group, entityId uint, fields []dcgm.Short,
) ([]dcgm.FieldValue_v1,
	error) {
	return dcgm.EntityGetLatestValues(entityGroup, entityId, fields)
}

func (d dcgmProviderImpl) FieldGetById(fieldId dcgm.Short) dcgm.FieldMeta {
	return dcgm.FieldGetById(fieldId)
}

func (d dcgmProviderImpl) FieldGroupCreate(fieldsGroupName string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	return dcgm.FieldGroupCreate(fieldsGroupName, fields)
}

func (d dcgmProviderImpl) FieldGroupDestroy(fieldsGroup dcgm.FieldHandle) error {
	return dcgm.FieldGroupDestroy(fieldsGroup)
}

func (d dcgmProviderImpl) GetAllDeviceCount() (uint, error) {
	return dcgm.GetAllDeviceCount()
}

func (d dcgmProviderImpl) GetCpuHierarchy() (dcgm.CpuHierarchy_v1, error) {
	return dcgm.GetCpuHierarchy()
}

func (d dcgmProviderImpl) GetDeviceInfo(gpuId uint) (dcgm.Device, error) {
	return dcgm.GetDeviceInfo(gpuId)
}

func (d dcgmProviderImpl) GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error) {
	return dcgm.GetEntityGroupEntities(entityGroup)
}

func (d dcgmProviderImpl) GetGpuInstanceHierarchy() (dcgm.MigHierarchy_v2, error) {
	return dcgm.GetGpuInstanceHierarchy()
}

func (d dcgmProviderImpl) GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error) {
	return dcgm.GetNvLinkLinkStatus()
}

func (d dcgmProviderImpl) GetSupportedDevices() ([]uint, error) {
	return dcgm.GetSupportedDevices()
}

func (d dcgmProviderImpl) GetSupportedMetricGroups(gpuId uint) ([]dcgm.MetricGroup, error) {
	return dcgm.GetSupportedMetricGroups(gpuId)
}

func (d dcgmProviderImpl) GetValuesSince(
	gpuGroup dcgm.GroupHandle, fieldGroup dcgm.FieldHandle, sinceTime time.Time,
) ([]dcgm.FieldValue_v2, time.Time, error) {
	return dcgm.GetValuesSince(gpuGroup, fieldGroup, sinceTime)
}

func (d dcgmProviderImpl) GroupAllGPUs() dcgm.GroupHandle {
	return dcgm.GroupAllGPUs()
}

func (d dcgmProviderImpl) LinkGetLatestValues(index uint, parentId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error) {
	return dcgm.LinkGetLatestValues(index, parentId, fields)
}

func (d dcgmProviderImpl) NewDefaultGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.NewDefaultGroup(groupName)
}

func (d dcgmProviderImpl) UpdateAllFields() error {
	return dcgm.UpdateAllFields()
}

func (d dcgmProviderImpl) WatchFieldsWithGroupEx(
	fieldsGroup dcgm.FieldHandle, group dcgm.GroupHandle, updateFreq int64, maxKeepAge float64,
	maxKeepSamples int32,
) error {
	return dcgm.WatchFieldsWithGroupEx(fieldsGroup, group, updateFreq, maxKeepAge, maxKeepSamples)
}

func (d dcgmProviderImpl) Cleanup() {
	// Terminates the DcgmFields module
	logrus.Info("Attempting to terminate DCGM Fields module.")
	if val := dcgm.FieldsTerm(); val < 0 {
		logrus.Errorf("Failed to terminate DCGM Fields module; err: %d", val)
	}

	logrus.Info("Attempting to terminate DCGM.")
	d.shutdown()

	reset()
}

func NewGroup() (dcgm.GroupHandle, func(), error) {
	group, err := Client().NewDefaultGroup(fmt.Sprintf("gpu-collector-group-%d", rand.Uint64()))
	if err != nil {
		return dcgm.GroupHandle{}, func() {}, err
	}

	return group, func() {
		err := Client().DestroyGroup(group)
		if err != nil {
			logrus.WithError(err).Warn("Cannot destroy field group.")
		}
	}, nil
}
