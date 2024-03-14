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

package dcgm_client

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
)

var dcgmClient DCGMClient

func Initialize(config *common.Config) {
	dcgmClient = newDCGMClient(config)
}

func Client() DCGMClient {
	return dcgmClient
}

func SetClient(d DCGMClient) {
	dcgmClient = d
}

type DCGMClientImpl struct {
	shutdown      func()
	moduleCleanup func()
}

func newDCGMClient(config *common.Config) DCGMClient {
	client := DCGMClientImpl{}

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

	return client
}

func (d DCGMClientImpl) AddEntityToGroup(
	groupId dcgm.GroupHandle, entityGroupId dcgm.Field_Entity_Group,
	entityId uint,
) error {
	return dcgm.AddEntityToGroup(groupId, entityGroupId, entityId)
}

func (d DCGMClientImpl) AddLinkEntityToGroup(groupId dcgm.GroupHandle, index uint, parentId uint) error {
	return dcgm.AddLinkEntityToGroup(groupId, index, parentId)
}

func (d DCGMClientImpl) CreateGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.CreateGroup(groupName)
}

func (d DCGMClientImpl) DestroyGroup(groupId dcgm.GroupHandle) error {
	return dcgm.DestroyGroup(groupId)
}

func (d DCGMClientImpl) EntitiesGetLatestValues(
	entities []dcgm.GroupEntityPair, fields []dcgm.Short, flags uint,
) ([]dcgm.FieldValue_v2, error) {
	return dcgm.EntitiesGetLatestValues(entities, fields, flags)
}

func (d DCGMClientImpl) EntityGetLatestValues(
	entityGroup dcgm.Field_Entity_Group, entityId uint, fields []dcgm.Short,
) ([]dcgm.FieldValue_v1,
	error) {
	return dcgm.EntityGetLatestValues(entityGroup, entityId, fields)
}

func (d DCGMClientImpl) FieldGetById(fieldId dcgm.Short) dcgm.FieldMeta {
	return dcgm.FieldGetById(fieldId)
}

func (d DCGMClientImpl) FieldGroupCreate(fieldsGroupName string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
	return dcgm.FieldGroupCreate(fieldsGroupName, fields)
}

func (d DCGMClientImpl) FieldGroupDestroy(fieldsGroup dcgm.FieldHandle) error {
	return dcgm.FieldGroupDestroy(fieldsGroup)
}

func (d DCGMClientImpl) GetAllDeviceCount() (uint, error) {
	return dcgm.GetAllDeviceCount()
}

func (d DCGMClientImpl) GetCpuHierarchy() (dcgm.CpuHierarchy_v1, error) {
	return dcgm.GetCpuHierarchy()
}

func (d DCGMClientImpl) GetDeviceInfo(gpuId uint) (dcgm.Device, error) {
	return dcgm.GetDeviceInfo(gpuId)
}

func (d DCGMClientImpl) GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error) {
	return dcgm.GetEntityGroupEntities(entityGroup)
}

func (d DCGMClientImpl) GetGpuInstanceHierarchy() (dcgm.MigHierarchy_v2, error) {
	return dcgm.GetGpuInstanceHierarchy()
}

func (d DCGMClientImpl) GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error) {
	return dcgm.GetNvLinkLinkStatus()
}

func (d DCGMClientImpl) GetSupportedDevices() ([]uint, error) {
	return dcgm.GetSupportedDevices()
}

func (d DCGMClientImpl) GetSupportedMetricGroups(gpuId uint) ([]dcgm.MetricGroup, error) {
	return dcgm.GetSupportedMetricGroups(gpuId)
}

func (d DCGMClientImpl) GetValuesSince(
	gpuGroup dcgm.GroupHandle, fieldGroup dcgm.FieldHandle, sinceTime time.Time,
) ([]dcgm.FieldValue_v2, time.Time, error) {
	return dcgm.GetValuesSince(gpuGroup, fieldGroup, sinceTime)
}

func (d DCGMClientImpl) GroupAllGPUs() dcgm.GroupHandle {
	return dcgm.GroupAllGPUs()
}

func (d DCGMClientImpl) LinkGetLatestValues(index uint, parentId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
	error) {
	return dcgm.LinkGetLatestValues(index, parentId, fields)
}

func (d DCGMClientImpl) NewDefaultGroup(groupName string) (dcgm.GroupHandle, error) {
	return dcgm.NewDefaultGroup(groupName)
}

func (d DCGMClientImpl) UpdateAllFields() error {
	return dcgm.UpdateAllFields()
}

func (d DCGMClientImpl) WatchFieldsWithGroupEx(
	fieldsGroup dcgm.FieldHandle, group dcgm.GroupHandle, updateFreq int64, maxKeepAge float64,
	maxKeepSamples int32,
) error {
	return dcgm.WatchFieldsWithGroupEx(fieldsGroup, group, updateFreq, maxKeepAge, maxKeepSamples)
}

func (d DCGMClientImpl) Cleanup() {
	// Terminates the DcgmFields module
	if val := dcgm.FieldsTerm(); val < 0 {
		logrus.Errorf("Failed to terminate DCGM Fields module; err: %d", val)
	}

	d.shutdown()
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
