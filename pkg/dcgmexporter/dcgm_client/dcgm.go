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

func (d DCGMClientImpl) EntityGetLatestValues(entityGroup dcgm.Field_Entity_Group, entityId uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1,
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

func NewDeviceFields(counters []common.Counter, entityType dcgm.Field_Entity_Group) []dcgm.Short {
	var deviceFields []dcgm.Short
	for _, f := range counters {
		meta := Client().FieldGetById(f.FieldID)

		if meta.EntityLevel == entityType || meta.EntityLevel == dcgm.FE_NONE {
			deviceFields = append(deviceFields, f.FieldID)
		} else if entityType == dcgm.FE_GPU && (meta.EntityLevel == dcgm.FE_GPU_CI || meta.EntityLevel == dcgm.FE_GPU_I || meta.EntityLevel == dcgm.FE_VGPU) {
			deviceFields = append(deviceFields, f.FieldID)
		} else if entityType == dcgm.FE_CPU && (meta.EntityLevel == dcgm.FE_CPU || meta.EntityLevel == dcgm.FE_CPU_CORE) {
			deviceFields = append(deviceFields, f.FieldID)
		}
	}

	return deviceFields
}

func NewFieldGroup(deviceFields []dcgm.Short) (dcgm.FieldHandle, func(), error) {
	name := fmt.Sprintf("gpu-collector-fieldgroup-%d", rand.Uint64())
	fieldGroup, err := Client().FieldGroupCreate(name, deviceFields)
	if err != nil {
		return dcgm.FieldHandle{}, func() {}, err
	}

	return fieldGroup, func() {
		err := Client().FieldGroupDestroy(fieldGroup)
		if err != nil {
			logrus.WithError(err).Warn("Cannot destroy field group.")
		}
	}, nil
}

func WatchFieldGroup(
	group dcgm.GroupHandle, field dcgm.FieldHandle, updateFreq int64, maxKeepAge float64, maxKeepSamples int32,
) error {
	err := Client().WatchFieldsWithGroupEx(field, group, updateFreq, maxKeepAge, maxKeepSamples)
	if err != nil {
		return err
	}

	return nil
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
