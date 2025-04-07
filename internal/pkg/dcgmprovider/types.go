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

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/dcgmprovider/mock_client.go -package=dcgmprovider -copyright_file=../../../hack/header.txt . DCGM

package dcgmprovider

import (
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

var _ DCGM = &dcgmProvider{}

type DCGM interface {
	AddEntityToGroup(dcgm.GroupHandle, dcgm.Field_Entity_Group, uint) error
	AddLinkEntityToGroup(dcgm.GroupHandle, uint, uint) error
	CreateFakeEntities(entities []dcgm.MigHierarchyInfo) ([]uint, error)
	CreateGroup(string) (dcgm.GroupHandle, error)
	DestroyGroup(groupID dcgm.GroupHandle) error
	EntitiesGetLatestValues([]dcgm.GroupEntityPair, []dcgm.Short, uint) ([]dcgm.FieldValue_v2, error)
	EntityGetLatestValues(dcgm.Field_Entity_Group, uint, []dcgm.Short) ([]dcgm.FieldValue_v1, error)
	Fv2_String(fv dcgm.FieldValue_v2) string
	FieldGetByID(dcgm.Short) dcgm.FieldMeta
	FieldGroupCreate(string, []dcgm.Short) (dcgm.FieldHandle, error)
	FieldGroupDestroy(dcgm.FieldHandle) error
	GetAllDeviceCount() (uint, error)
	GetCPUHierarchy() (dcgm.CPUHierarchy_v1, error)
	GetDeviceInfo(uint) (dcgm.Device, error)
	GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error)
	GetGPUInstanceHierarchy() (dcgm.MigHierarchy_v2, error)
	GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error)
	GetSupportedDevices() ([]uint, error)
	GetSupportedMetricGroups(uint) ([]dcgm.MetricGroup, error)
	GetValuesSince(dcgm.GroupHandle, dcgm.FieldHandle, time.Time) ([]dcgm.FieldValue_v2, time.Time, error)
	GroupAllGPUs() dcgm.GroupHandle
	InjectFieldValue(gpu uint, fieldID dcgm.Short, fieldType uint, status int, ts int64, value interface{}) error
	LinkGetLatestValues(uint, uint, []dcgm.Short) ([]dcgm.FieldValue_v1, error)
	NewDefaultGroup(string) (dcgm.GroupHandle, error)
	UpdateAllFields() error
	WatchFieldsWithGroupEx(dcgm.FieldHandle, dcgm.GroupHandle, int64, float64, int32) error
	Cleanup()
	HealthSet(groupID dcgm.GroupHandle, systems dcgm.HealthSystem) error
	HealthGet(groupID dcgm.GroupHandle) (dcgm.HealthSystem, error)
	HealthCheck(groupID dcgm.GroupHandle) (dcgm.HealthResponse, error)
	GetGroupInfo(groupID dcgm.GroupHandle) (*dcgm.GroupInfo, error)
}
