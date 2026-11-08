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

// WatchScope identifies the entities covered by one watch request. WatchList
// uses these names to keep ordinary, compute-instance, and parent-GPU watches
// separate without passing ambiguous boolean flags through the call stack.
type WatchScope uint8

const (
	// WatchConfiguredEntities watches the entities selected in the device options.
	WatchConfiguredEntities WatchScope = iota
	// WatchComputeInstanceEntities watches selected GPUs and their compute instances.
	WatchComputeInstanceEntities
	// WatchUnselectedParentGPUs watches parents that are not already covered by selection.
	WatchUnselectedParentGPUs
	// WatchAllGPUEntities watches selected GPUs, GPU instances, their compute
	// instances, and missing parent GPUs in one group for historical collection.
	WatchAllGPUEntities
	// These disjoint scopes let shared watch policies vary by physical GPU and instance.
	WatchSelectedGPUs
	WatchGPUInstances
	WatchOnlyComputeInstances
	// WatchPhysicalGPUs watches selected GPUs and the parent GPUs of selected
	// GPU instances, never the instances themselves. Fields DCGM declares at
	// GPU level use it when MIG instances are selected.
	WatchPhysicalGPUs
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
// need GPU-level, compute-instance or multiple-entity collection.
type ResolvedFields struct {
	// Fields contains every counter field supported by the requested entity type.
	Fields []dcgm.Short
	// ComputeInstanceFields contains the fields that require GPU compute-instance reads.
	ComputeInstanceFields []dcgm.Short
	// FieldsAtMultipleScopes contains compute-instance fields that must also be read from parent GPUs.
	FieldsAtMultipleScopes []dcgm.Short
	// GPULevelFields contains the fields DCGM declares at GPU level. DCGM keeps
	// their values on the physical GPU, so they are never watched or read on
	// MIG instances.
	GPULevelFields []dcgm.Short
}

// Watcher resolves counter fields for a device entity. Production watchers
// also implement ResultWatcher; keeping field resolution separate lets tests
// provide a small fake without taking ownership of native resources.
type Watcher interface {
	// GetDeviceFields resolves supported counter fields and records their required collection scopes.
	GetDeviceFields([]counters.Counter, dcgm.Field_Entity_Group) ResolvedFields
}

// ResultWatcher creates one owned DCGM watch result for a requested scope.
// WatchList requires this production capability before it can install watches.
type ResultWatcher interface {
	// Watch creates all watches for one scope. On failure it closes every resource
	// it created, so a non-nil result always represents complete caller ownership.
	Watch([]FieldWatchGroup, deviceinfo.Provider, WatchScope) (*WatchResult, error)
	// ExplainWatchFailure adds a plain explanation to a failed watch of groups
	// when the hostengine refused some of their fields, and otherwise returns the
	// error unchanged.
	ExplainWatchFailure([]FieldWatchGroup, error) error
}

// WatchInstaller combines field resolution with creating owned watch results.
// Watch-list construction needs both capabilities: it resolves supported fields
// before it installs the corresponding native DCGM watches.
type WatchInstaller interface {
	Watcher
	ResultWatcher
}
