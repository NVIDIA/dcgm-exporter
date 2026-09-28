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

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/devicewatcher/mock_device_watcher.go -package=devicewatcher -copyright_file=../../../hack/header.txt . Watcher

package devicewatcher

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// FieldWatchGroup contains the DCGM field IDs that should share one watch interval and retention policy.
// A zero-valued retention pair selects the legacy 10-minute age and unlimited-sample policy.
type FieldWatchGroup struct {
	Name           string
	Fields         []dcgm.Short
	IntervalMSec   int64
	MaxKeepAge     float64 // Seconds; 0 disables the age limit when MaxKeepSamples is positive.
	MaxKeepSamples int32   // 0 disables the sample limit when MaxKeepAge is positive.
}

// ResolvedFields contains fields accepted for an entity and the subsets that
// need compute-instance or multiple-entity collection.
type ResolvedFields struct {
	// Fields contains every counter field supported by the requested entity type.
	Fields []dcgm.Short
	// ComputeInstanceFields contains the fields that require GPU compute-instance reads.
	ComputeInstanceFields []dcgm.Short
	// FieldsAtMultipleScopes contains compute-instance fields that must also be read from parent GPUs.
	FieldsAtMultipleScopes []dcgm.Short
}

// Watcher resolves device fields and starts DCGM field watches for entity devices.
type Watcher interface {
	// GetDeviceFields resolves supported counter fields and records their required collection scopes.
	GetDeviceFields([]counters.Counter, dcgm.Field_Entity_Group) ResolvedFields
	// WatchDeviceFields uses the legacy retention policy and returns an error when
	// the requested fields require more than one physical DCGM field group.
	WatchDeviceFields([]dcgm.Short, deviceinfo.Provider, int64) ([]dcgm.GroupHandle, dcgm.FieldHandle, []func(), error)
	WatchDeviceFieldGroups([]FieldWatchGroup, deviceinfo.Provider) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error)
	// WatchDeviceFieldGroupsForComputeInstanceFields watches selected whole GPUs
	// and child compute instances, excluding GPU instances.
	WatchDeviceFieldGroupsForComputeInstanceFields(
		[]FieldWatchGroup,
		deviceinfo.Provider,
	) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error)
	// WatchDeviceFieldGroupsForParentGPUs watches only unselected parent GPUs for multi-scope fields.
	WatchDeviceFieldGroupsForParentGPUs(
		[]FieldWatchGroup,
		deviceinfo.Provider,
	) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error)
}
