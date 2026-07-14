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

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	. "github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/utils"
)

type DeviceWatcher struct{}

// WatchResources holds all DCGM resources that need cleanup
type WatchResources struct {
	groups      []dcgm.GroupHandle
	fieldGroups []dcgm.FieldHandle
	hasWatch    bool // tracks if WatchFields was called
}

// Cleanup releases all DCGM resources in the correct order
func (r *WatchResources) Cleanup() {
	// Cleanup order: UnwatchFields -> FieldGroupDestroy -> DestroyGroup
	// This is the reverse of creation order

	// Check if DCGM client is still available (may be nil during shutdown)
	client := dcgmprovider.Client()
	if client == nil {
		return
	}

	// 1. Unwatch all fields for all groups
	if r.hasWatch {
		for _, group := range r.groups {
			for _, fieldGroup := range r.fieldGroups {
				if unwatchErr := client.UnwatchFields(fieldGroup, group); unwatchErr != nil {
					// Ignore benign errors that happen when DCGM shuts down before our cleanup
					errMsg := unwatchErr.Error()
					if !strings.Contains(errMsg, DCGM_ST_NOT_CONFIGURED) &&
						!strings.Contains(errMsg, DCGM_ST_FIELD_NOT_WATCHED) {
						slog.Warn("Failed to unwatch fields", slog.String(ErrorKey, errMsg))
					}
				}
			}
		}
	}

	// 2. Destroy field group
	for _, fieldGroup := range r.fieldGroups {
		if err := client.FieldGroupDestroy(fieldGroup); err != nil {
			if !strings.Contains(err.Error(), DCGM_ST_NOT_CONFIGURED) {
				slog.Warn("Cannot destroy field group", slog.String(ErrorKey, err.Error()))
			}
		}
	}

	// 3. Destroy all groups
	for _, group := range r.groups {
		if destroyErr := client.DestroyGroup(group); destroyErr != nil {
			if !strings.Contains(destroyErr.Error(), DCGM_ST_NOT_CONFIGURED) {
				slog.LogAttrs(
					context.Background(), slog.LevelWarn, "cannot destroy group",
					slog.Any(GroupIDKey, group),
					slog.String(ErrorKey, destroyErr.Error()),
				)
			}
		}
	}
}

func NewDeviceWatcher() *DeviceWatcher {
	return &DeviceWatcher{}
}

func (d *DeviceWatcher) GetDeviceFields(counters []counters.Counter, entityType dcgm.Field_Entity_Group) []dcgm.Short {
	var deviceFields []dcgm.Short
	var failedCount int
	for _, counter := range counters {
		fieldMeta, err := dcgmprovider.Client().FieldGetByID(counter.FieldID)
		if err != nil {
			failedCount++
			slog.Debug(
				"FieldGetByID failed; skipping field",
				slog.Any("field_id", counter.FieldID),
				slog.String(ErrorKey, err.Error()),
			)
			continue
		}

		if shouldIncludeField(entityType, fieldMeta.EntityLevel) {
			deviceFields = append(deviceFields, counter.FieldID)
		}
	}

	if failedCount > 0 {
		slog.Warn(
			"Some fields were skipped because FieldGetByID failed",
			slog.Int("failed_count", failedCount),
			slog.Int("total_count", len(counters)),
			slog.Any("entity_type", entityType),
		)
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
	fieldWatchGroups := []FieldWatchGroup{
		{
			Name:         "default",
			Fields:       deviceFields,
			IntervalMSec: updateFreqInUsec / 1000,
		},
	}
	groups, fieldGroups, cleanups, err := d.WatchDeviceFieldGroups(fieldWatchGroups, deviceInfo)
	if len(fieldGroups) == 0 {
		return groups, dcgm.FieldHandle{}, cleanups, err
	}
	return groups, fieldGroups[0], cleanups, err
}

// WatchDeviceFieldGroups creates one DCGM field group per configured watch interval.
func (d *DeviceWatcher) WatchDeviceFieldGroups(
	fieldWatchGroups []FieldWatchGroup, deviceInfo deviceinfo.Provider,
) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error) {
	resources := &WatchResources{}

	// Create groups based on device type
	var err error
	switch deviceInfo.InfoType() {
	case dcgm.FE_LINK:
		resources.groups, err = d.createNVLinkGroupsSimple(deviceInfo)
	case dcgm.FE_CPU_CORE:
		resources.groups, err = d.createCPUCoreGroupsSimple(deviceInfo)
	default:
		resources.groups, err = d.createGroupsSimple(deviceInfo)
	}
	if err != nil {
		resources.Cleanup()
		return nil, nil, nil, err
	} else if len(resources.groups) == 0 {
		return nil, nil, nil, nil
	}

	for _, fieldWatchGroup := range fieldWatchGroups {
		fields := dedupeFields(fieldWatchGroup.Fields)
		if len(fields) == 0 {
			continue
		}

		fieldGroup, err := newFieldGroupSimple(fields)
		if err != nil {
			resources.Cleanup()
			return nil, nil, nil, err
		}
		resources.fieldGroups = append(resources.fieldGroups, fieldGroup)

		// Watch fields for all groups
		for _, group := range resources.groups {
			logWatchFieldsCall(deviceInfo, group, fieldGroup, fields)
			err = watchFieldGroupSimple(group, fieldGroup, fieldWatchGroup.IntervalMSec*1000)
			if err != nil {
				logWatchFieldsFailure(deviceInfo, group, fieldGroup, fields, err)
				resources.Cleanup()
				return nil, nil, nil, err
			}
			resources.hasWatch = true
		}
	}

	// Return single cleanup function
	cleanup := func() { resources.Cleanup() }
	return resources.groups, resources.fieldGroups, []func(){cleanup}, nil
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
			cleanup()
			return nil, doNothing, err
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
					for _, cleanup := range cleanups {
						cleanup()
					}
					return nil, nil, err
				}

				cleanups = append(cleanups, cleanup)
				groups = append(groups, groupID)
			}

			groupCoreCount++

			err = dcgmprovider.Client().AddEntityToGroup(groupID, dcgm.FE_CPU_CORE, core)
			if err != nil {
				for _, cleanup := range cleanups {
					cleanup()
				}
				return nil, nil, err
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

	/* Create per-gpu link groups */
	for _, gpu := range deviceInfo.GPUs() {

		var groupLinkCount int
		var groupID dcgm.GroupHandle
		for _, link := range gpu.NvLinks {
			if groupLinkCount == 0 {
				var cleanup func()

				groupID, cleanup, err = createGroup()
				if err != nil {
					for _, cleanup := range cleanups {
						cleanup()
					}
					return nil, nil, err
				}

				cleanups = append(cleanups, cleanup)
				groups = append(groups, groupID)
			}

			groupLinkCount++

			err = dcgmprovider.Client().AddLinkEntityToGroup(groupID, link.Index, dcgm.FE_GPU, gpu.DeviceInfo.GPU)
			if err != nil {
				slog.Warn(fmt.Sprintf("could not add link %d on GPU %d to group %d: %s", link.Index, gpu.DeviceInfo.GPU, groupID, err))
			}
		}
	}

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
					for _, cleanup := range cleanups {
						cleanup()
					}
					return nil, nil, err
				}

				cleanups = append(cleanups, cleanup)
				groups = append(groups, groupID)
			}

			groupLinkCount++

			err = dcgmprovider.Client().AddLinkEntityToGroup(groupID, link.Index, dcgm.FE_SWITCH, link.ParentId)
			if err != nil {
				slog.Warn(fmt.Sprintf("could not add link %d on NvSwitch %d to group %d: %s", link.Index, link.ParentId, groupID, err))
			}
		}
	}

	return groups, cleanups, nil
}

// Simplified create functions that don't return cleanup callbacks

func (d *DeviceWatcher) createGroupsSimple(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, error) {
	group, err := d.createGenericGroupSimple(deviceInfo)
	if err != nil {
		return nil, err
	}
	if group != nil {
		return []dcgm.GroupHandle{*group}, nil
	}
	return nil, nil
}

func (d *DeviceWatcher) createNVLinkGroupsSimple(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, error) {
	groups, _, err := d.createNVLinkGroups(deviceInfo)
	return groups, err
}

func (d *DeviceWatcher) createCPUCoreGroupsSimple(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, error) {
	groups, _, err := d.createCPUCoreGroups(deviceInfo)
	return groups, err
}

func (d *DeviceWatcher) createGenericGroupSimple(deviceInfo deviceinfo.Provider) (*dcgm.GroupHandle, error) {
	group, _, err := d.createGenericGroup(deviceInfo)
	return group, err
}

func newFieldGroupSimple(deviceFields []dcgm.Short) (dcgm.FieldHandle, error) {
	newFieldGroupNumber, err := utils.RandUint64()
	if err != nil {
		return dcgm.FieldHandle{}, err
	}

	name := fmt.Sprintf("gpu-collector-fieldgroup-%d", newFieldGroupNumber)
	fieldGroup, err := dcgmprovider.Client().FieldGroupCreate(name, dedupeFields(deviceFields))
	if err != nil {
		return dcgm.FieldHandle{}, err
	}

	return fieldGroup, nil
}

func dedupeFields(fields []dcgm.Short) []dcgm.Short {
	if len(fields) == 0 {
		return nil
	}

	seen := make(map[dcgm.Short]struct{}, len(fields))
	deduped := make([]dcgm.Short, 0, len(fields))
	for _, field := range fields {
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		deduped = append(deduped, field)
	}

	return deduped
}

func logWatchFieldsCall(
	deviceInfo deviceinfo.Provider,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	fieldIDs []dcgm.Short,
) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}

	slog.LogAttrs(
		context.Background(),
		slog.LevelDebug,
		"Watching DCGM fields",
		watchLogAttrs(deviceInfo, group, fieldGroup, fieldIDs)...,
	)
}

func logWatchFieldsFailure(
	deviceInfo deviceinfo.Provider,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	fieldIDs []dcgm.Short,
	err error,
) {
	attrs := append(
		watchLogAttrs(deviceInfo, group, fieldGroup, fieldIDs),
		slog.String(ErrorKey, err.Error()),
	)
	slog.LogAttrs(context.Background(), slog.LevelError, "Failed to watch DCGM fields", attrs...)
}

func watchLogAttrs(
	deviceInfo deviceinfo.Provider,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	fieldIDs []dcgm.Short,
) []slog.Attr {
	options := deviceOptionsForInfoType(deviceInfo)
	return []slog.Attr{
		slog.String("entity_type", deviceInfo.InfoType().String()),
		slog.Any(GroupIDKey, group),
		slog.Any("field_group", fieldGroup),
		slog.Any("field_ids", fieldIDs),
		slog.Any("field_names", fieldNames(fieldIDs)),
		slog.String("device_option_mode", deviceOptionMode(options)),
		slog.Any("device_options", options),
	}
}

func fieldNames(fieldIDs []dcgm.Short) []string {
	names := make([]string, 0, len(fieldIDs))
	client := dcgmprovider.Client()
	if client == nil {
		return names
	}

	for _, fieldID := range fieldIDs {
		meta, err := client.FieldGetByID(fieldID)
		if err == nil && meta.Tag != "" {
			names = append(names, meta.Tag)
		}
	}

	return names
}

func deviceOptionsForInfoType(deviceInfo deviceinfo.Provider) appconfig.DeviceOptions {
	switch deviceInfo.InfoType() {
	case dcgm.FE_SWITCH, dcgm.FE_LINK:
		return deviceInfo.SOpts()
	case dcgm.FE_CPU, dcgm.FE_CPU_CORE:
		return deviceInfo.COpts()
	default:
		return deviceInfo.GOpts()
	}
}

func deviceOptionMode(options appconfig.DeviceOptions) string {
	switch {
	case options.Flex:
		return "f"
	case len(options.MajorRange) > 0:
		return "g"
	case len(options.MinorRange) > 0:
		return "i"
	default:
		return ""
	}
}

func watchFieldGroupSimple(group dcgm.GroupHandle, field dcgm.FieldHandle, updateFreq int64) error {
	return dcgmprovider.Client().WatchFieldsWithGroupEx(field, group, updateFreq, maxKeepAge, maxKeepSamples)
}

// Legacy functions kept for backward compatibility

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
			slog.LogAttrs(
				context.Background(), slog.LevelWarn, "cannot destroy group",
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
			slog.Warn(
				"Cannot destroy field group.",
				slog.String(ErrorKey, err.Error()),
			)
		}
	}

	return fieldGroup, cleanup, nil
}
