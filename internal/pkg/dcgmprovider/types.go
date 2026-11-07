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

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/dcgmprovider/mock_client.go -package=dcgmprovider -copyright_file=../../../hack/header.txt . DCGM,FieldGroupUnwatcher

package dcgmprovider

import (
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

var _ DCGM = &dcgmProvider{}

// FieldGroupWatch names one installed watch: a field group watched on an
// entity group.
type FieldGroupWatch struct {
	FieldGroup dcgm.FieldHandle
	Group      dcgm.GroupHandle
}

// FieldGroupUnwatcher removes several field-group watches in one call. The
// client that Client returns implements it, so callers can batch an unwatch
// and have surviving watches restored once.
type FieldGroupUnwatcher interface {
	// UnwatchFieldGroups removes each field-group watch in turn and keeps going
	// after a failure. It returns, joined with errors.Join, one error per failed
	// unwatch, plus a *RestoreError when restoring the surviving watches
	// afterwards fails.
	UnwatchFieldGroups([]FieldGroupWatch) error
}

// DCGM is the subset of the go-dcgm API the exporter uses, so tests can
// replace the active DCGM session.
type DCGM interface {
	// AddEntityToGroup adds an entity to an existing DCGM group.
	AddEntityToGroup(dcgm.GroupHandle, dcgm.Field_Entity_Group, uint) error
	// AddLinkEntityToGroup adds a switch or GPU link to an existing group.
	AddLinkEntityToGroup(dcgm.GroupHandle, uint, dcgm.Field_Entity_Group, uint) error
	// CreateFakeEntities creates the fake entities described by the MIG hierarchy.
	CreateFakeEntities(entities []dcgm.MigHierarchyInfo) ([]uint, error)
	// CreateGroup creates a DCGM group and returns its handle for later destruction.
	CreateGroup(string) (dcgm.GroupHandle, error)
	// DestroyGroup releases a DCGM group created by this client.
	DestroyGroup(groupID dcgm.GroupHandle) error
	// EntitiesGetLatestValues reads the requested fields from a set of typed entities.
	EntitiesGetLatestValues([]dcgm.GroupEntityPair, []dcgm.Short, uint) ([]dcgm.FieldValue_v2, error)
	// EntityGetLatestValues reads the requested fields for one typed entity.
	EntityGetLatestValues(dcgm.Field_Entity_Group, uint, []dcgm.Short) ([]dcgm.FieldValue_v1, error)
	// Fv2_String formats a typed DCGM field value for diagnostics.
	Fv2_String(fv dcgm.FieldValue_v2) string
	// FieldGetByID returns metadata for a DCGM field ID.
	FieldGetByID(dcgm.Short) (dcgm.FieldMeta, error)
	// FieldGroupCreate creates a field group and returns its handle for later destruction.
	FieldGroupCreate(string, []dcgm.Short) (dcgm.FieldHandle, error)
	// FieldGroupDestroy releases a field group created by this client.
	FieldGroupDestroy(dcgm.FieldHandle) error
	// GetAllDeviceCount returns the number of GPUs DCGM knows about, including
	// unsupported ones.
	GetAllDeviceCount() (uint, error)
	// GetGPUStatus returns the GPU's DCGM lifecycle state. A failed query returns
	// EntityStatusUnknown rather than confirming that the GPU is detached.
	GetGPUStatus(uint) dcgm.EntityStatus
	// GetCPUHierarchy returns the v2-shaped hierarchy.
	// Legacy v1 fallback stays internal to dcgmProvider.
	GetCPUHierarchy() (dcgm.CPUHierarchy_v2, error)
	// GetDeviceInfo returns DCGM metadata for one GPU ID.
	GetDeviceInfo(uint) (dcgm.Device, error)
	// GetErrorMeta returns DCGM-owned metadata for a health or diagnostic error code.
	GetErrorMeta(dcgm.HealthCheckErrorCode) *dcgm.ErrorMeta
	// GetEntityGroupEntities lists entity IDs belonging to one DCGM entity group.
	GetEntityGroupEntities(entityGroup dcgm.Field_Entity_Group) ([]uint, error)
	// GetGPUInstanceHierarchy returns the current MIG hierarchy.
	GetGPUInstanceHierarchy() (dcgm.MigHierarchy_v2, error)
	// GetMultipleValuesForField reads retained history for one entity and field over a time range.
	GetMultipleValuesForField(uint, dcgm.Short, int, time.Time, time.Time) ([]dcgm.FieldValue_v1, error)
	// GetNvLinkLinkStatus returns the current NVLink status for supported links.
	GetNvLinkLinkStatus() ([]dcgm.NvLinkStatus, error)
	// GetAllFieldPolicies returns all field-policy settings currently held by DCGM.
	GetAllFieldPolicies() ([]dcgm.FieldPolicy, error)
	// GetSupportedDevices lists GPU IDs supported by this DCGM session.
	GetSupportedDevices() ([]uint, error)
	// GetSupportedMetricGroups lists the metric groups supported by one GPU.
	GetSupportedMetricGroups(uint) ([]dcgm.MetricGroup, error)
	// GetValuesSince reads field samples after since and returns the next cursor.
	GetValuesSince(dcgm.GroupHandle, dcgm.FieldHandle, time.Time) ([]dcgm.FieldValue_v2, time.Time, error)
	// GroupAllGPUs returns DCGM's all-GPUs group handle.
	GroupAllGPUs() dcgm.GroupHandle
	// InjectFieldValue injects a synthetic field sample for one GPU.
	InjectFieldValue(gpu uint, fieldID dcgm.Short, fieldType uint, status int, ts int64, value interface{}) error
	// LinkGetLatestValues reads the requested fields for one link and its parent entity.
	LinkGetLatestValues(uint, dcgm.Field_Entity_Group, uint, []dcgm.Short) ([]dcgm.FieldValue_v1, error)
	// NewDefaultGroup creates a DCGM group that holds all GPUs. The caller
	// destroys it with DestroyGroup.
	NewDefaultGroup(string) (dcgm.GroupHandle, error)
	// UpdateAllFields asks DCGM to refresh watched field samples.
	UpdateAllFields() error
	// WatchFieldValue installs a direct field watch for one entity.
	WatchFieldValue(uint, dcgm.Short, time.Duration, time.Duration, int) error
	// WatchFieldsWithGroupExNoRefresh installs a field-group watch for an entity
	// group and does not ask DCGM to update every watched field afterwards.
	// When the call adds an (entity, field) pair that has no sample yet, DCGM
	// runs one update and waits for it. That update skips fields a module
	// pushes (profiling, NVSwitch, CPU); those arrive when the module pushes
	// them. A field another watch already samples keeps its value until its
	// next update.
	WatchFieldsWithGroupExNoRefresh(dcgm.FieldHandle, dcgm.GroupHandle, int64, float64, int32) error
	// UnwatchFields removes a field-group watch from an entity group.
	UnwatchFields(dcgm.FieldHandle, dcgm.GroupHandle) error
	// Cleanup releases the DCGM session and modules owned by the provider.
	Cleanup()
	// HealthSet selects which health systems DCGM checks for the group.
	HealthSet(groupID dcgm.GroupHandle, systems dcgm.HealthSystem) error
	// HealthGet returns the health systems currently selected for the group.
	HealthGet(groupID dcgm.GroupHandle) (dcgm.HealthSystem, error)
	// HealthCheck runs the selected health checks for the group.
	HealthCheck(groupID dcgm.GroupHandle) (dcgm.HealthResponse, error)
	// HealthGetSupportedSystems returns the health watches the connected hostengine
	// supports. An error for which dcgm.IsFeatureUnavailable reports true means the
	// loaded DCGM library predates this query.
	HealthGetSupportedSystems() (dcgm.HealthSystem, error)
	// GetGroupInfo returns membership and metadata for a DCGM group.
	GetGroupInfo(groupID dcgm.GroupHandle) (*dcgm.GroupInfo, error)
	// GetNvLinkP2PStatus returns the current GPU peer-to-peer status matrix.
	GetNvLinkP2PStatus() (dcgm.NvLinkP2PStatus, error)
}
