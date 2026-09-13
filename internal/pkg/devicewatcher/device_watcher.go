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
	"sort"
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
	// DCP/profiling fields (DCGM_FI_PROF_*) are validated for an entire DCGM
	// watch group at registration time: if any GPU model in the group lacks
	// a requested profiling field, the whole watch call fails. Ordinary
	// fields don't have this problem - unsupported ones just come back as a
	// per-entity NOT_SUPPORTED value at scrape time (see isBlankValue in the
	// collector package). So the single-shared-group path below is fine for
	// almost every field on almost every node; it only breaks on a node
	// mixing GPU models where at least one profiling field isn't supported
	// everywhere. Route that specific case through a per-model partition
	// instead of touching the common path.
	if deviceInfo.InfoType() == dcgm.FE_GPU && anyDCPField(fieldWatchGroups) {
		if handled, groups, fieldGroups, cleanups, err := d.watchFieldGroupsPartitionedByModel(
			fieldWatchGroups, deviceInfo,
		); handled {
			return groups, fieldGroups, cleanups, err
		}
	}

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

// anyDCPField reports whether any field across the given watch groups is a
// DCP/profiling field (DCGM_FI_PROF_*).
func anyDCPField(fieldWatchGroups []FieldWatchGroup) bool {
	for _, fieldWatchGroup := range fieldWatchGroups {
		for _, fieldID := range fieldWatchGroup.Fields {
			if counters.IsDCPField(uint(fieldID)) {
				return true
			}
		}
	}
	return false
}

// watchFieldGroupsPartitionedByModel watches GPU fields one DCGM group per
// GPU model instead of one shared group for the whole node. handled is false
// when the node only has one GPU model present, so the caller should fall
// back to its normal single-group path unchanged.
//
// This exists because DCP/profiling fields fail the entire group's watch
// registration if any member GPU doesn't support them (unlike ordinary
// fields, which just report a per-entity NOT_SUPPORTED value). On a node
// mixing GPU models, that turns "one model doesn't support this profiling
// field" into "the exporter won't start at all". Partitioning by model and
// asking DCGM what each model actually supports keeps a field enabled
// wherever it works instead of dropping it - or crashing - node-wide.
func (d *DeviceWatcher) watchFieldGroupsPartitionedByModel(
	fieldWatchGroups []FieldWatchGroup, deviceInfo deviceinfo.Provider,
) (handled bool, groups []dcgm.GroupHandle, fieldGroups []dcgm.FieldHandle, cleanups []func(), err error) {
	monitoringInfo := devicemonitoring.GetMonitoredEntities(deviceInfo)
	models, byModel := partitionMonitoredEntitiesByModel(monitoringInfo)
	if len(models) < 2 {
		return false, nil, nil, nil, nil
	}

	resources := &WatchResources{}
	for _, model := range models {
		entities := byModel[model]

		groupID, _, gerr := createGroupFromEntities(entities)
		if gerr != nil {
			resources.Cleanup()
			return true, nil, nil, nil, gerr
		}
		resources.groups = append(resources.groups, *groupID)

		supported, serr := supportedDCPFields(entities[0].DeviceInfo.GPU)
		if serr != nil {
			// Can't determine this model's profiling capability. Fall back
			// to watching only what we know is safe for every model rather
			// than risk the same all-or-nothing failure this path exists
			// to avoid.
			slog.Warn("Could not query DCP metric group support for GPU model; "+
				"watching non-profiling fields only for it",
				slog.String("gpu_model", model), slog.String(ErrorKey, serr.Error()))
			supported = map[dcgm.Short]bool{}
		}

		for _, fieldWatchGroup := range fieldWatchGroups {
			fields := filterFieldsForModel(dedupeFields(fieldWatchGroup.Fields), supported)
			if len(fields) == 0 {
				continue
			}

			fieldGroup, ferr := newFieldGroupSimple(fields)
			if ferr != nil {
				resources.Cleanup()
				return true, nil, nil, nil, ferr
			}
			resources.fieldGroups = append(resources.fieldGroups, fieldGroup)

			logWatchFieldsCall(deviceInfo, *groupID, fieldGroup, fields)
			if werr := watchFieldGroupSimple(*groupID, fieldGroup, fieldWatchGroup.IntervalMSec*1000); werr != nil {
				logWatchFieldsFailure(deviceInfo, *groupID, fieldGroup, fields, werr)
				resources.Cleanup()
				return true, nil, nil, nil, werr
			}
			resources.hasWatch = true
		}
	}

	cleanup := func() { resources.Cleanup() }
	return true, resources.groups, resources.fieldGroups, []func(){cleanup}, nil
}

// partitionMonitoredEntitiesByModel groups monitored entities by their
// parent GPU's model name. models is sorted for deterministic group
// creation order (matters for tests, harmless in production).
func partitionMonitoredEntitiesByModel(
	monitoringInfo []devicemonitoring.Info,
) ([]string, map[string][]devicemonitoring.Info) {
	byModel := make(map[string][]devicemonitoring.Info)
	for _, mi := range monitoringInfo {
		model := mi.DeviceInfo.Identifiers.Model
		byModel[model] = append(byModel[model], mi)
	}

	models := make([]string, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Strings(models)

	return models, byModel
}

// supportedDCPFields returns the DCP/profiling field IDs DCGM reports as
// supported for the GPU model represented by gpuID. Every GPU of the same
// model supports the same profiling fields, so one representative GPU per
// model is enough - no need to query every GPU individually.
func supportedDCPFields(gpuID uint) (map[dcgm.Short]bool, error) {
	metricGroups, err := dcgmprovider.Client().GetSupportedMetricGroups(gpuID)
	if err != nil {
		return nil, err
	}

	supported := make(map[dcgm.Short]bool)
	for _, metricGroup := range metricGroups {
		for _, fieldID := range metricGroup.FieldIds {
			supported[dcgm.Short(fieldID)] = true
		}
	}

	return supported, nil
}

// filterFieldsForModel drops DCP fields a model's profiling query didn't
// report as supported. Non-DCP fields always pass through unfiltered: DCGM
// already reports those as a per-entity NOT_SUPPORTED value at scrape time
// instead of failing the watch, so there's nothing to filter for them.
func filterFieldsForModel(fields []dcgm.Short, supported map[dcgm.Short]bool) []dcgm.Short {
	filtered := make([]dcgm.Short, 0, len(fields))
	for _, fieldID := range fields {
		if !counters.IsDCPField(uint(fieldID)) || supported[fieldID] {
			filtered = append(filtered, fieldID)
		}
	}
	return filtered
}

func (d *DeviceWatcher) createGenericGroup(deviceInfo deviceinfo.Provider) (*dcgm.GroupHandle, func(),
	error,
) {
	monitoringInfo := devicemonitoring.GetMonitoredEntities(deviceInfo)
	if len(monitoringInfo) == 0 {
		return nil, doNothing, nil
	}

	return createGroupFromEntities(monitoringInfo)
}

// createGroupFromEntities creates one DCGM group containing exactly the given
// entities. Used both for the single-group path (all monitored entities) and
// for the per-model partitioned path (one model's entities at a time).
func createGroupFromEntities(monitoringInfo []devicemonitoring.Info) (*dcgm.GroupHandle, func(), error) {
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
