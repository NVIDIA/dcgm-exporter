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

package devicewatcher

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	. "github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

type DeviceWatcher struct{}

func NewDeviceWatcher() *DeviceWatcher {
	return &DeviceWatcher{}
}

func (d *DeviceWatcher) GetDeviceFields(counters []counters.Counter, entityType dcgm.Field_Entity_Group) []dcgm.Short {
	var deviceFields []dcgm.Short
	for _, counter := range counters {
		fieldMeta := dcgmprovider.Client().FieldGetByID(counter.FieldID)

		if shouldIncludeField(entityType, fieldMeta.EntityLevel) {
			deviceFields = append(deviceFields, counter.FieldID)
		}
	}

	return deviceFields
}

func shouldIncludeField(entityType, fieldLevel dcgm.Field_Entity_Group) bool {
	if fieldLevel == entityType || fieldLevel == dcgm.FE_NONE {
		return true
	}

	switch entityType {
	case dcgm.FE_GPU:
		return fieldLevel == dcgm.FE_GPU_CI || fieldLevel == dcgm.FE_GPU_I || fieldLevel == dcgm.FE_VGPU
	case dcgm.FE_CPU:
		return fieldLevel == dcgm.FE_CPU_CORE
	case dcgm.FE_SWITCH:
		return fieldLevel == dcgm.FE_LINK
	default:
		return false
	}
}

func (d *DeviceWatcher) WatchDeviceFields(
	deviceFields []dcgm.Short, deviceInfo deviceinfo.Provider, updateFreqInUsec int64,
) ([]dcgm.GroupHandle, dcgm.FieldHandle, []func(), error) {
	var err error
	var cleanups []func()
	var groups []dcgm.GroupHandle

	switch deviceInfo.InfoType() {
	case dcgm.FE_LINK:
		// This handles NV link case only.
		groups, cleanups, err = d.createNVLinkGroups(deviceInfo)
	case dcgm.FE_CPU_CORE:
		// This handles CPU Core case only.
		groups, cleanups, err = d.createCPUCoreGroups(deviceInfo)
	default:
		// This handles GPUs (including GPU Instances), CPUs and Switches cases.
		groups, cleanups, err = d.createGroups(deviceInfo)
	}
	if err != nil {
		return nil, dcgm.FieldHandle{}, utils.CleanupOnError(cleanups), err
	} else if len(groups) == 0 {
		return nil, dcgm.FieldHandle{}, cleanups, nil
	}

	fieldGroup, cleanup, fieldGroupErr := newFieldGroup(deviceFields)
	if fieldGroupErr != nil {
		return nil, dcgm.FieldHandle{}, utils.CleanupOnError(cleanups), fieldGroupErr
	}
	cleanups = append(cleanups, cleanup)

	for _, group := range groups {
		err = watchFieldGroup(group, fieldGroup, updateFreqInUsec)
		if err != nil {
			return nil, dcgm.FieldHandle{}, utils.CleanupOnError(cleanups), err
		}
	}

	return groups, fieldGroup, cleanups, nil
}

func (d *DeviceWatcher) createGroups(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, []func(),
	error,
) {
	if group, cleanup, err := d.createGenericGroup(deviceInfo); err != nil {
		return []dcgm.GroupHandle{}, []func(){cleanup}, err
	} else if group != nil {
		return []dcgm.GroupHandle{*group}, []func(){cleanup}, nil
	}

	return []dcgm.GroupHandle{}, []func(){}, nil
}

func (d *DeviceWatcher) createGenericGroup(deviceInfo deviceinfo.Provider) (*dcgm.GroupHandle, func(),
	error,
) {
	monitoringInfo := devicemonitoring.GetMonitoredEntities(deviceInfo)
	if len(monitoringInfo) == 0 {
		return nil, doNothing, nil
	}

	groupID, cleanup, err := createGroup()
	if err != nil {
		return nil, cleanup, err
	}

	for _, mi := range monitoringInfo {
		err := dcgmprovider.Client().AddEntityToGroup(groupID, mi.Entity.EntityGroupId, mi.Entity.EntityId)
		if err != nil {
			return &groupID, cleanup, err
		}
	}

	return &groupID, cleanup, nil
}

func (d *DeviceWatcher) createCPUCoreGroups(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, []func(),
	error,
) {
	var groups []dcgm.GroupHandle
	var cleanups []func()
	var err error

	for _, cpu := range deviceInfo.CPUs() {
		if !deviceInfo.IsCPUWatched(cpu.EntityId) {
			continue
		}

		var groupCoreCount int
		var groupID dcgm.GroupHandle
		for _, core := range cpu.Cores {
			if !deviceInfo.IsCoreWatched(core, cpu.EntityId) {
				continue
			}

			// Create per-cpu core groups or after max number of CPU cores have been added to current group
			if groupCoreCount%dcgm.DCGM_GROUP_MAX_ENTITIES == 0 {
				var cleanup func()

				groupID, cleanup, err = createGroup()
				if err != nil {
					return nil, cleanups, err
				}

				cleanups = append(cleanups, cleanup)
				groups = append(groups, groupID)
			}

			groupCoreCount++

			err = dcgmprovider.Client().AddEntityToGroup(groupID, dcgm.FE_CPU_CORE, core)
			if err != nil {
				return groups, cleanups, err
			}
		}
	}

	return groups, cleanups, nil
}

func (d *DeviceWatcher) createNVLinkGroups(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, []func(),
	error,
) {
	var groups []dcgm.GroupHandle
	var cleanups []func()
	var err error

	/* Create per-switch link groups */
	for _, sw := range deviceInfo.Switches() {
		if !deviceInfo.IsSwitchWatched(sw.EntityId) {
			continue
		}

		var groupLinkCount int
		var groupID dcgm.GroupHandle
		for _, link := range sw.NvLinks {
			if link.State != dcgm.LS_UP {
				continue
			}

			if !deviceInfo.IsLinkWatched(link.Index, sw.EntityId) {
				continue
			}

			// Create per-switch link groups
			if groupLinkCount == 0 {
				var cleanup func()

				groupID, cleanup, err = createGroup()
				if err != nil {
					return nil, cleanups, err
				}

				cleanups = append(cleanups, cleanup)
				groups = append(groups, groupID)
			}

			groupLinkCount++

			err = dcgmprovider.Client().AddLinkEntityToGroup(groupID, link.Index, link.ParentId)
			if err != nil {
				return groups, cleanups, err
			}
		}
	}

	return groups, cleanups, nil
}

func createGroup() (dcgm.GroupHandle, func(), error) {
	newGroupNumber, err := utils.RandUint64()
	if err != nil {
		return dcgm.GroupHandle{}, doNothing, err
	}

	groupID, err := dcgmprovider.Client().CreateGroup(fmt.Sprintf("gpu-collector-group-%d", newGroupNumber))
	if err != nil {
		return dcgm.GroupHandle{}, doNothing, err
	}

	cleanup := func() {
		destroyErr := dcgmprovider.Client().DestroyGroup(groupID)
		if destroyErr != nil && !strings.Contains(destroyErr.Error(), DCGM_ST_NOT_CONFIGURED) {
			slog.LogAttrs(context.Background(), slog.LevelWarn, "cannot destroy group",
				slog.Any(GroupIDKey, groupID),
				slog.String(ErrorKey, destroyErr.Error()),
			)
		}
	}
	return groupID, cleanup, nil
}

func newFieldGroup(deviceFields []dcgm.Short) (dcgm.FieldHandle, func(), error) {
	newFieldGroupNumber, err := utils.RandUint64()
	if err != nil {
		return dcgm.FieldHandle{}, doNothing, err
	}

	name := fmt.Sprintf("gpu-collector-fieldgroup-%d", newFieldGroupNumber)
	fieldGroup, err := dcgmprovider.Client().FieldGroupCreate(name, deviceFields)
	if err != nil {
		return dcgm.FieldHandle{}, doNothing, err
	}

	cleanup := func() {
		err := dcgmprovider.Client().FieldGroupDestroy(fieldGroup)
		if err != nil {
			slog.Warn("Cannot destroy field group.",
				slog.String(ErrorKey, err.Error()),
			)
		}
	}

	return fieldGroup, cleanup, nil
}

func watchFieldGroup(
	group dcgm.GroupHandle, field dcgm.FieldHandle, updateFreq int64,
) error {
	err := dcgmprovider.Client().WatchFieldsWithGroupEx(field, group, updateFreq, maxKeepAge, maxKeepSamples)
	if err != nil {
		return err
	}

	return nil
}
