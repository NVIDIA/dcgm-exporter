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

package devicewatchlistmanager

import (
	"fmt"
	"path"
	"sort"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
)

// DeviceTypesToWatch supported entity group types
var DeviceTypesToWatch = []dcgm.Field_Entity_Group{
	dcgm.FE_GPU,
	dcgm.FE_SWITCH,
	dcgm.FE_LINK,
	dcgm.FE_CPU,
	dcgm.FE_CPU_CORE,
}

type WatchList struct {
	deviceInfo        deviceinfo.Provider
	deviceFields      []dcgm.Short
	watchFields       []dcgm.Short
	fieldWatchGroups  []devicewatcher.FieldWatchGroup
	deviceGroups      []dcgm.GroupHandle
	deviceFieldGroups []dcgm.FieldHandle
	labelDeviceFields []dcgm.Short
	watcher           devicewatcher.Watcher
	collectInterval   int64
}

func NewWatchList(
	deviceInfo deviceinfo.Provider, deviceFields, labelDeviceFields []dcgm.Short,
	watcher devicewatcher.Watcher, collectInterval int64,
) *WatchList {
	watchFields := buildWatchFields(deviceFields, labelDeviceFields)
	return NewWatchListWithGroups(
		deviceInfo,
		deviceFields,
		labelDeviceFields,
		defaultFieldWatchGroups(watchFields, collectInterval),
		watcher,
		collectInterval,
	)
}

// NewWatchListWithGroups creates a watch list with pre-resolved field watch groups.
func NewWatchListWithGroups(
	deviceInfo deviceinfo.Provider,
	deviceFields, labelDeviceFields []dcgm.Short,
	fieldWatchGroups []devicewatcher.FieldWatchGroup,
	watcher devicewatcher.Watcher,
	collectInterval int64,
) *WatchList {
	watchList := &WatchList{
		deviceInfo:        deviceInfo,
		deviceFields:      deviceFields,
		watchFields:       buildWatchFields(deviceFields, labelDeviceFields),
		fieldWatchGroups:  fieldWatchGroups,
		labelDeviceFields: labelDeviceFields,
		watcher:           watcher,
		collectInterval:   collectInterval,
	}
	watchList.fieldWatchGroups = watchList.fieldWatchGroupsForFields(watchList.watchFields)
	return watchList
}

func (d *WatchList) DeviceInfo() deviceinfo.Provider {
	return d.deviceInfo
}

func (d *WatchList) DeviceFields() []dcgm.Short {
	return d.deviceFields
}

func (d *WatchList) SetDeviceFields(deviceFields []dcgm.Short) {
	d.deviceFields = deviceFields
	d.watchFields = buildWatchFields(d.deviceFields, d.labelDeviceFields)
	d.fieldWatchGroups = d.fieldWatchGroupsForFields(d.watchFields)
}

func (d *WatchList) SetDeviceFieldsWithoutLabelWatches(deviceFields []dcgm.Short) {
	d.deviceFields = deviceFields
	d.watchFields = dedupeFields(d.deviceFields)
	d.fieldWatchGroups = d.fieldWatchGroupsForFields(d.watchFields)
}

func (d *WatchList) LabelDeviceFields() []dcgm.Short {
	return d.labelDeviceFields
}

func (d *WatchList) IsEmpty() bool {
	return len(d.deviceFields) == 0
}

func (d *WatchList) Watch() ([]func(), error) {
	var cleanups []func()
	var err error

	d.deviceGroups, d.deviceFieldGroups, cleanups, err = d.watcher.WatchDeviceFieldGroups(
		d.fieldWatchGroups,
		d.deviceInfo,
	)
	return cleanups, err
}

func (d *WatchList) DeviceGroups() []dcgm.GroupHandle {
	return d.deviceGroups
}

func (d *WatchList) DeviceFieldGroup() dcgm.FieldHandle {
	if len(d.deviceFieldGroups) == 0 {
		return dcgm.FieldHandle{}
	}
	return d.deviceFieldGroups[0]
}

// DeviceFieldGroups returns every field group watched for this entity.
func (d *WatchList) DeviceFieldGroups() []dcgm.FieldHandle {
	return d.deviceFieldGroups
}

// FieldWatchGroups returns the resolved watch intervals for this entity.
func (d *WatchList) FieldWatchGroups() []devicewatcher.FieldWatchGroup {
	return d.fieldWatchGroups
}

// fieldWatchGroupsForFields narrows existing watch groups to a new field set.
func (d *WatchList) fieldWatchGroupsForFields(deviceFields []dcgm.Short) []devicewatcher.FieldWatchGroup {
	if len(deviceFields) == 0 {
		return nil
	}

	remaining := map[dcgm.Short]struct{}{}
	for _, fieldID := range deviceFields {
		remaining[fieldID] = struct{}{}
	}

	result := make([]devicewatcher.FieldWatchGroup, 0, len(d.fieldWatchGroups)+1)
	for _, watchGroup := range d.fieldWatchGroups {
		fields := make([]dcgm.Short, 0, len(watchGroup.Fields))
		for _, fieldID := range watchGroup.Fields {
			if _, ok := remaining[fieldID]; !ok {
				continue
			}
			fields = append(fields, fieldID)
			delete(remaining, fieldID)
		}
		if len(fields) == 0 {
			continue
		}
		result = append(result, devicewatcher.FieldWatchGroup{
			Name:         watchGroup.Name,
			Fields:       fields,
			IntervalMSec: watchGroup.IntervalMSec,
		})
	}

	if len(remaining) == 0 {
		return result
	}

	defaultFields := make([]dcgm.Short, 0, len(remaining))
	for _, fieldID := range deviceFields {
		if _, ok := remaining[fieldID]; ok {
			defaultFields = append(defaultFields, fieldID)
		}
	}
	result = append(result, devicewatcher.FieldWatchGroup{
		Name:         "default",
		Fields:       defaultFields,
		IntervalMSec: d.collectInterval,
	})
	return result
}

func buildWatchFields(deviceFields, labelDeviceFields []dcgm.Short) []dcgm.Short {
	if len(deviceFields) == 0 {
		return nil
	}

	fields := make([]dcgm.Short, 0, len(deviceFields)+len(labelDeviceFields))
	fields = append(fields, deviceFields...)
	fields = append(fields, labelDeviceFields...)
	return dedupeFields(fields)
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

// WatchListManager manages multiple entities and their corresponding WatchLists, counters to watch
// and device options.
type WatchListManager struct {
	entityWatchLists map[dcgm.Field_Entity_Group]WatchList
	counters         counters.CounterList
	gOpts            appconfig.DeviceOptions
	sOpts            appconfig.DeviceOptions
	cOpts            appconfig.DeviceOptions
	useFakeGPUs      bool
	watchGroups      []appconfig.WatchGroup
}

// NewWatchListManager creates a new instance of the WatchListManager
func NewWatchListManager(
	counters counters.CounterList, config *appconfig.Config,
) *WatchListManager {
	return &WatchListManager{
		entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
		counters:         counters,
		gOpts:            config.GPUDeviceOptions,
		sOpts:            config.SwitchDeviceOptions,
		cOpts:            config.CPUDeviceOptions,
		useFakeGPUs:      config.UseFakeGPUs,
		watchGroups:      config.WatchGroups,
	}
}

// CreateEntityWatchList identifies an entity's device fields, label field to monitor
// and loads its device information.
func (e *WatchListManager) CreateEntityWatchList(
	entityType dcgm.Field_Entity_Group, watcher devicewatcher.Watcher, collectInterval int64,
) error {
	deviceFields := watcher.GetDeviceFields(e.counters.NonLabelCounters(), entityType)

	labelDeviceFields := watcher.GetDeviceFields(e.counters.LabelCounters(), entityType)

	deviceInfo, err := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
	if err != nil {
		return err
	}

	if entityType == dcgm.FE_GPU {
		deviceInfo.WarnMIGInstancesWithoutComputeInstances()
	}

	watchFields := buildWatchFields(deviceFields, labelDeviceFields)
	fieldWatchGroups, err := e.partitionFieldWatchGroups(watchFields, collectInterval)
	if err != nil {
		return err
	}

	e.entityWatchLists[entityType] = *NewWatchListWithGroups(
		deviceInfo,
		deviceFields,
		labelDeviceFields,
		fieldWatchGroups,
		watcher,
		collectInterval,
	)

	return err
}

// EntityWatchList returns a given entity's WatchList and true if such WatchList exists otherwise
// an empty WatchList and false.
func (e *WatchListManager) EntityWatchList(deviceType dcgm.Field_Entity_Group) (WatchList, bool) {
	entityWatchList, exists := e.entityWatchLists[deviceType]
	return entityWatchList, exists
}

// defaultFieldWatchGroups assigns all fields to the base collection interval.
func defaultFieldWatchGroups(deviceFields []dcgm.Short, collectInterval int64) []devicewatcher.FieldWatchGroup {
	if len(deviceFields) == 0 {
		return nil
	}
	return []devicewatcher.FieldWatchGroup{
		{
			Name:         "default",
			Fields:       append([]dcgm.Short(nil), deviceFields...),
			IntervalMSec: collectInterval,
		},
	}
}

// partitionFieldWatchGroups resolves configured name patterns against entity fields.
func (e *WatchListManager) partitionFieldWatchGroups(
	deviceFields []dcgm.Short, collectInterval int64,
) ([]devicewatcher.FieldWatchGroup, error) {
	if len(e.watchGroups) == 0 {
		return defaultFieldWatchGroups(deviceFields, collectInterval), nil
	}

	fieldNames := counterNamesByFieldID(e.counters)
	assigned := map[dcgm.Short]struct{}{}
	result := make([]devicewatcher.FieldWatchGroup, 0, len(e.watchGroups)+1)

	for _, watchGroup := range e.watchGroups {
		fields := make([]dcgm.Short, 0)
		for _, fieldID := range deviceFields {
			if _, ok := assigned[fieldID]; ok {
				continue
			}
			fieldName := fieldNames[fieldID]
			matches, err := watchGroupMatchesFieldPattern(watchGroup, fieldName)
			if err != nil {
				return nil, err
			}
			if matches {
				fields = append(fields, fieldID)
				assigned[fieldID] = struct{}{}
			}
		}
		if len(fields) > 0 {
			result = append(result, devicewatcher.FieldWatchGroup{
				Name:         watchGroup.Name,
				Fields:       fields,
				IntervalMSec: int64(watchGroup.Interval),
			})
		}
	}

	var defaultFields []dcgm.Short
	for _, fieldID := range deviceFields {
		if _, ok := assigned[fieldID]; !ok {
			defaultFields = append(defaultFields, fieldID)
		}
	}
	if len(defaultFields) > 0 {
		result = append(result, devicewatcher.FieldWatchGroup{
			Name:         "default",
			Fields:       defaultFields,
			IntervalMSec: collectInterval,
		})
	}

	return result, nil
}

// ValidateWatchGroups verifies configured watch groups match fields without overlap.
func ValidateWatchGroups(counters counters.CounterList, watchGroups []appconfig.WatchGroup) error {
	if len(watchGroups) == 0 {
		return nil
	}
	if err := validateWatchGroupPatterns(watchGroups); err != nil {
		return err
	}

	fieldNames := counterNamesByFieldID(counters)
	groupMatches := make([]int, len(watchGroups))
	fieldMatches := map[dcgm.Short]string{}

	for _, fieldID := range sortedFieldIDs(fieldNames) {
		fieldName := fieldNames[fieldID]
		for i, watchGroup := range watchGroups {
			matches, err := watchGroupMatchesFieldPattern(watchGroup, fieldName)
			if err != nil {
				return err
			}
			if !matches {
				continue
			}
			groupMatches[i]++
			if matchedGroup, ok := fieldMatches[fieldID]; ok {
				return fmt.Errorf(
					"field %s matches multiple watch groups: %s and %s",
					fieldName,
					matchedGroup,
					watchGroup.Name,
				)
			}
			fieldMatches[fieldID] = watchGroup.Name
		}
	}

	for i, matchCount := range groupMatches {
		if matchCount == 0 {
			return fmt.Errorf("watch group %q matched no configured fields", watchGroups[i].Name)
		}
	}

	return nil
}

func validateWatchGroupPatterns(watchGroups []appconfig.WatchGroup) error {
	for _, watchGroup := range watchGroups {
		for _, pattern := range watchGroup.Fields {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("watch group %q has invalid field pattern %q: %w", watchGroup.Name, pattern, err)
			}
		}
	}
	return nil
}

func sortedFieldIDs(fieldNames map[dcgm.Short]string) []dcgm.Short {
	fieldIDs := make([]dcgm.Short, 0, len(fieldNames))
	for fieldID := range fieldNames {
		fieldIDs = append(fieldIDs, fieldID)
	}
	sort.Slice(fieldIDs, func(i, j int) bool {
		return fieldIDs[i] < fieldIDs[j]
	})
	return fieldIDs
}

// counterNamesByFieldID indexes configured counter names by DCGM field ID.
func counterNamesByFieldID(counters counters.CounterList) map[dcgm.Short]string {
	names := map[dcgm.Short]string{}
	for _, counter := range counters {
		if counter.FieldName == "" {
			continue
		}
		names[counter.FieldID] = counter.FieldName
	}
	return names
}

// watchGroupMatchesFieldPattern reports whether a field name matches any group pattern.
func watchGroupMatchesFieldPattern(watchGroup appconfig.WatchGroup, fieldName string) (bool, error) {
	if fieldName == "" {
		return false, nil
	}

	for _, pattern := range watchGroup.Fields {
		matches, err := path.Match(pattern, fieldName)
		if err != nil {
			return false, fmt.Errorf("watch group %q has invalid field pattern %q: %w", watchGroup.Name, pattern, err)
		}
		if matches {
			return true, nil
		}
	}
	return false, nil
}
