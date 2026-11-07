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
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
)

// DeviceTypesToWatch lists the entity groups for which the manager builds watch lists.
var DeviceTypesToWatch = []dcgm.Field_Entity_Group{
	dcgm.FE_GPU,
	dcgm.FE_SWITCH,
	dcgm.FE_LINK,
	dcgm.FE_CPU,
	dcgm.FE_CPU_CORE,
}

// WatchList describes the fields, device topology, and named scopes required
// for one collector. Watch creates a fresh owned result each time; the list
// itself remains reusable for profiling-watch rearm operations.
type WatchList struct {
	deviceInfo             deviceinfo.Provider
	deviceFields           []dcgm.Short
	watchFields            []dcgm.Short
	computeInstanceFields  []dcgm.Short
	fieldsAtMultipleScopes []dcgm.Short
	gpuLevelFields         []dcgm.Short
	entityScope            devicewatcher.WatchScope
	fieldWatchGroups       []devicewatcher.FieldWatchGroup
	labelDeviceFields      []dcgm.Short
	watcher                devicewatcher.WatchInstaller
	collectInterval        int64
	maxKeepAge             float64
	maxKeepSamples         int32
	scopedWatchGroups      []scopedWatchGroups
}

// NewWatchList builds a compatibility watch-list description with the default
// collection and retention policy. Collector constructors use it in focused
// tests; runtime setup normally uses WatchListManager.
func NewWatchList(
	deviceInfo deviceinfo.Provider, deviceFields, labelDeviceFields []dcgm.Short,
	watcher devicewatcher.WatchInstaller, collectInterval int64,
) *WatchList {
	watchFields := buildWatchFields(deviceFields, labelDeviceFields)
	return newWatchListWithGroups(
		deviceInfo,
		deviceFields,
		labelDeviceFields,
		nil,
		nil,
		nil,
		defaultFieldWatchGroups(watchFields, collectInterval),
		watcher,
		collectInterval,
		appconfig.DefaultWatchMaxKeepAge.Seconds(),
		int32(appconfig.DefaultWatchMaxSamples),
	)
}

// NewWatchListWithGroups creates a watch list with pre-resolved field watch groups.
func NewWatchListWithGroups(
	deviceInfo deviceinfo.Provider,
	deviceFields, labelDeviceFields []dcgm.Short,
	fieldWatchGroups []devicewatcher.FieldWatchGroup,
	watcher devicewatcher.WatchInstaller,
	collectInterval int64,
) *WatchList {
	return newWatchListWithGroups(
		deviceInfo,
		deviceFields,
		labelDeviceFields,
		nil,
		nil,
		nil,
		fieldWatchGroups,
		watcher,
		collectInterval,
		appconfig.DefaultWatchMaxKeepAge.Seconds(),
		int32(appconfig.DefaultWatchMaxSamples),
	)
}

// newWatchListWithGroups builds a watch list with explicit default retention for unmatched and label fields.
// The public compatibility constructors and WatchListManager call it after choosing the appropriate policy.
func newWatchListWithGroups(
	deviceInfo deviceinfo.Provider,
	deviceFields, labelDeviceFields []dcgm.Short,
	computeInstanceFields []dcgm.Short,
	fieldsAtMultipleScopes []dcgm.Short,
	gpuLevelFields []dcgm.Short,
	fieldWatchGroups []devicewatcher.FieldWatchGroup,
	watcher devicewatcher.WatchInstaller,
	collectInterval int64,
	maxKeepAge float64,
	maxKeepSamples int32,
) *WatchList {
	watchList := &WatchList{
		deviceInfo:             deviceInfo,
		deviceFields:           deviceFields,
		watchFields:            buildWatchFields(deviceFields, labelDeviceFields),
		computeInstanceFields:  append([]dcgm.Short(nil), computeInstanceFields...),
		fieldsAtMultipleScopes: append([]dcgm.Short(nil), fieldsAtMultipleScopes...),
		gpuLevelFields:         append([]dcgm.Short(nil), gpuLevelFields...),
		fieldWatchGroups:       fieldWatchGroups,
		labelDeviceFields:      labelDeviceFields,
		watcher:                watcher,
		collectInterval:        collectInterval,
		maxKeepAge:             maxKeepAge,
		maxKeepSamples:         maxKeepSamples,
	}
	watchList.fieldWatchGroups = watchList.fieldWatchGroupsForFields(watchList.watchFields)
	return watchList
}

// DeviceInfo returns the resolved topology used to form this list's groups.
func (d *WatchList) DeviceInfo() deviceinfo.Provider {
	return d.deviceInfo
}

// DeviceFields returns the non-label fields that this list resolves and watches.
func (d *WatchList) DeviceFields() []dcgm.Short {
	return d.deviceFields
}

// SetDeviceFields replaces fields for a rearm or test scenario and includes
// label fields in the new watch groups.
func (d *WatchList) SetDeviceFields(deviceFields []dcgm.Short) {
	d.scopedWatchGroups = nil
	d.deviceFields = deviceFields
	d.computeInstanceFields = nil
	d.fieldsAtMultipleScopes = nil
	d.entityScope = devicewatcher.WatchConfiguredEntities
	d.watchFields = buildWatchFields(d.deviceFields, d.labelDeviceFields)
	d.fieldWatchGroups = d.fieldWatchGroupsForFields(d.watchFields)
}

// SetDeviceFieldsForAllGPUScopes keeps labels out of historical reads while
// watching the source fields once across all required GPU entity scopes.
func (d *WatchList) SetDeviceFieldsForAllGPUScopes(deviceFields []dcgm.Short) {
	d.setHistoricalFields(deviceFields, devicewatcher.WatchAllGPUEntities)
}

// SetDeviceFieldsForPhysicalGPUs keeps labels out of the watch request and
// watches the source fields on physical GPUs only. Event collectors whose
// source field DCGM declares at GPU level use it, so on MIG GPUs they report
// one series per GPU.
func (d *WatchList) SetDeviceFieldsForPhysicalGPUs(deviceFields []dcgm.Short) {
	d.setHistoricalFields(deviceFields, devicewatcher.WatchPhysicalGPUs)
}

// MonitoredEntities keeps historical metric output aligned with its watch scope.
func (d *WatchList) MonitoredEntities() []devicemonitoring.Info {
	switch d.entityScope {
	case devicewatcher.WatchAllGPUEntities:
		return devicemonitoring.GetMonitoredEntitiesAtAllGPUScopes(d.deviceInfo)
	case devicewatcher.WatchPhysicalGPUs:
		return devicemonitoring.GetPhysicalGPUs(d.deviceInfo)
	default:
		return devicemonitoring.GetMonitoredEntities(d.deviceInfo)
	}
}

// LabelDeviceFields returns fields used only to add labels to exported metrics.
func (d *WatchList) LabelDeviceFields() []dcgm.Short {
	return d.labelDeviceFields
}

// ComputeInstanceFields returns fields that must be collected from a GPU
// compute-instance entity.
func (d *WatchList) ComputeInstanceFields() []dcgm.Short {
	return append([]dcgm.Short(nil), d.computeInstanceFields...)
}

// FieldsAtMultipleScopes returns fields that DCGM stores on more than one GPU
// entity scope.
func (d *WatchList) FieldsAtMultipleScopes() []dcgm.Short {
	return append([]dcgm.Short(nil), d.fieldsAtMultipleScopes...)
}

// GPULevelFields returns the watched fields that DCGM declares at GPU level
// when the device selection includes MIG GPU instances. These fields are
// watched and read on physical GPUs only, never on GPU instances or compute
// instances. It returns nil when no GPU instance is selected, because then
// every selected entity is already a physical GPU.
func (d *WatchList) GPULevelFields() []dcgm.Short {
	if d.entityScope != devicewatcher.WatchConfiguredEntities || len(d.gpuLevelFields) == 0 {
		return nil
	}
	if !devicemonitoring.SelectsGPUInstances(d.deviceInfo) {
		return nil
	}

	return intersectFields(d.watchFields, d.gpuLevelFields)
}

// InheritedLabelFields returns the label fields that DCGM declares at GPU
// level when the device selection includes MIG GPU instances. Series of GPU
// instances and compute instances take these label values from a read of their
// physical GPU. It returns nil when no GPU instance is selected.
func (d *WatchList) InheritedLabelFields() []dcgm.Short {
	if len(d.gpuLevelFields) == 0 || !devicemonitoring.SelectsGPUInstances(d.deviceInfo) {
		return nil
	}

	return intersectFields(d.labelDeviceFields, d.gpuLevelFields)
}

// IsEmpty reports whether this list has any non-label device fields to collect.
func (d *WatchList) IsEmpty() bool {
	return len(d.deviceFields) == 0
}

// Watch installs every scope needed by this list and returns one result that
// owns the exact DCGM pairs. On failure it closes earlier successful scopes,
// so callers never receive partial ownership. When the hostengine refuses
// fields, the explanation names the refused fields of every scope, because
// fieldWatchGroups holds the fields of all of them.
func (d *WatchList) Watch() (*devicewatcher.WatchResult, error) {
	result, err := d.watchAllScopes()
	if err != nil {
		return nil, d.watcher.ExplainWatchFailure(d.fieldWatchGroups, err)
	}

	return result, nil
}

// watchAllScopes watches GPU-level fields on physical GPUs when MIG instances
// are selected, and every other field through watchInstanceScopes.
func (d *WatchList) watchAllScopes() (*devicewatcher.WatchResult, error) {
	if len(d.scopedWatchGroups) > 0 {
		return d.watchScopedGroups()
	}

	gpuLevelFields := d.GPULevelFields()
	if len(gpuLevelFields) == 0 {
		return d.watchInstanceScopes()
	}

	physicalResult, err := d.watchScope(
		d.fieldWatchGroupsForFields(d.physicalGPUFields(gpuLevelFields)),
		devicewatcher.WatchPhysicalGPUs,
	)
	if err != nil {
		return nil, err
	}

	rest := d.withoutFields(gpuLevelFields)
	if rest.IsEmpty() {
		return physicalResult, nil
	}

	restResult, err := rest.watchInstanceScopes()
	if err != nil {
		physicalResult.Close()
		return nil, err
	}

	return devicewatcher.JoinWatchResults(physicalResult, restResult), nil
}

// physicalGPUFields returns the fields watched on physical GPUs: the GPU-level
// source fields with every label field, because physical GPUs then report
// series of their own. When only label fields are GPU-level, physical GPUs
// watch just those labels for the instance series that take them.
func (d *WatchList) physicalGPUFields(gpuLevelFields []dcgm.Short) []dcgm.Short {
	sourceFields := intersectFields(d.deviceFields, gpuLevelFields)
	if len(sourceFields) == 0 {
		return gpuLevelFields
	}

	return buildWatchFields(sourceFields, d.labelDeviceFields)
}

// withoutFields returns a copy of this list without fields, for the scopes that
// include MIG instances. The copy shares the device topology and watcher.
func (d *WatchList) withoutFields(fields []dcgm.Short) *WatchList {
	rest := *d
	rest.deviceFields = fieldsOutsideScope(d.deviceFields, fields)
	rest.labelDeviceFields = fieldsOutsideScope(d.labelDeviceFields, fields)
	rest.watchFields = buildWatchFields(rest.deviceFields, rest.labelDeviceFields)
	rest.fieldWatchGroups = d.fieldWatchGroupsForFields(rest.watchFields)
	rest.gpuLevelFields = nil

	return &rest
}

// watchInstanceScopes installs the configured scope or, when this list has
// compute-instance fields, separate base, compute-instance and parent-GPU scopes.
func (d *WatchList) watchInstanceScopes() (*devicewatcher.WatchResult, error) {
	if len(d.computeInstanceFields) == 0 {
		result, err := d.watchScope(
			d.fieldWatchGroups,
			d.entityScope,
		)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	return d.watchComputeInstanceFields()
}

// watchComputeInstanceFields keeps fields and entities at matching scopes so
// DCGM does not create an unnecessary entity-by-field Cartesian product.
func (d *WatchList) watchComputeInstanceFields() (*devicewatcher.WatchResult, error) {
	baseFields := fieldsOutsideScope(d.deviceFields, d.computeInstanceFields)
	baseFields = buildWatchFields(append(baseFields, d.fieldsAtMultipleScopes...), d.labelDeviceFields)
	results := make([]*devicewatcher.WatchResult, 0, 3)
	if len(baseFields) > 0 {
		result, err := d.watchScope(
			d.fieldWatchGroupsForFields(baseFields),
			devicewatcher.WatchConfiguredEntities,
		)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}

	computeInstanceFields := buildWatchFields(d.computeInstanceFields, d.labelDeviceFields)
	computeResult, err := d.watchScope(
		d.fieldWatchGroupsForFields(computeInstanceFields),
		devicewatcher.WatchComputeInstanceEntities,
	)
	if err != nil {
		devicewatcher.JoinWatchResults(results...).Close()
		return nil, err
	}
	results = append(results, computeResult)

	if len(d.fieldsAtMultipleScopes) == 0 {
		return devicewatcher.JoinWatchResults(results...), nil
	}

	parentFields := buildWatchFields(d.fieldsAtMultipleScopes, d.labelDeviceFields)
	parentWatchGroups := d.fieldWatchGroupsForFields(parentFields)
	parentResult, err := d.watchScope(
		parentWatchGroups,
		devicewatcher.WatchUnselectedParentGPUs,
	)
	if err != nil {
		devicewatcher.JoinWatchResults(results...).Close()
		return nil, err
	}

	results = append(results, parentResult)
	return devicewatcher.JoinWatchResults(results...), nil
}

// watchScope installs one owned result for a collector scope. WatchList keeps
// field resolution and native acquisition behind one compile-time contract.
func (d *WatchList) watchScope(
	fieldWatchGroups []devicewatcher.FieldWatchGroup,
	scope devicewatcher.WatchScope,
) (*devicewatcher.WatchResult, error) {
	return d.watcher.Watch(fieldWatchGroups, d.deviceInfo, scope)
}

// fieldsOutsideScope returns the fields that are not in scopedFields, keeping
// their order.
func fieldsOutsideScope(fields, scopedFields []dcgm.Short) []dcgm.Short {
	scoped := make(map[dcgm.Short]struct{}, len(scopedFields))
	for _, field := range scopedFields {
		scoped[field] = struct{}{}
	}

	result := make([]dcgm.Short, 0, len(fields))
	for _, field := range fields {
		if _, ok := scoped[field]; !ok {
			result = append(result, field)
		}
	}
	return result
}

// intersectFields returns the fields of first that are also in second, in
// first's order.
func intersectFields(first, second []dcgm.Short) []dcgm.Short {
	return fieldsOutsideScope(first, fieldsOutsideScope(first, second))
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
			Name:           watchGroup.Name,
			Fields:         fields,
			IntervalMSec:   watchGroup.IntervalMSec,
			MaxKeepAge:     watchGroup.MaxKeepAge,
			MaxKeepSamples: watchGroup.MaxKeepSamples,
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
		Name:           "default",
		Fields:         defaultFields,
		IntervalMSec:   d.collectInterval,
		MaxKeepAge:     d.maxKeepAge,
		MaxKeepSamples: d.maxKeepSamples,
	})
	return result
}

// buildWatchFields combines metric and label fields without duplicate IDs.
func buildWatchFields(deviceFields, labelDeviceFields []dcgm.Short) []dcgm.Short {
	if len(deviceFields) == 0 {
		return nil
	}

	fields := make([]dcgm.Short, 0, len(deviceFields)+len(labelDeviceFields))
	fields = append(fields, deviceFields...)
	fields = append(fields, labelDeviceFields...)
	return dedupeFields(fields)
}

// dedupeFields preserves the first occurrence of each field ID.
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

// WatchListManager builds one WatchList description per supported DCGM entity
// from the resolved counters, device options, and watch-retention policy.
type WatchListManager struct {
	entityWatchLists map[dcgm.Field_Entity_Group]WatchList
	counters         counters.CounterList
	gOpts            appconfig.DeviceOptions
	sOpts            appconfig.DeviceOptions
	cOpts            appconfig.DeviceOptions
	useFakeGPUs      bool
	watchGroups      []appconfig.WatchGroup
	watchRetention   appconfig.WatchRetention
	otlpCollections  []appconfig.OTLPWatchCollection
}

// NewWatchListManager creates descriptions that use config's resolved device
// selection and retention settings for the supplied counter set.
func NewWatchListManager(
	counters counters.CounterList, config *appconfig.Config,
) *WatchListManager {
	manager := &WatchListManager{
		entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
		counters:         counters,
		gOpts:            config.GPUDeviceOptions,
		sOpts:            config.SwitchDeviceOptions,
		cOpts:            config.CPUDeviceOptions,
		useFakeGPUs:      config.UseFakeGPUs,
		watchGroups:      config.WatchGroups,
		watchRetention:   config.WatchRetention,
	}
	if config.EnableOTLP {
		manager.otlpCollections = config.OTLPWatchCollections
	}
	return manager
}

// CreateEntityWatchList identifies an entity's device fields, label field to monitor
// and loads its device information.
func (e *WatchListManager) CreateEntityWatchList(
	entityType dcgm.Field_Entity_Group, watcher devicewatcher.WatchInstaller, collectInterval int64,
) error {
	deviceFields := watcher.GetDeviceFields(e.counters.NonLabelCounters(), entityType)
	if len(deviceFields.Fields) == 0 && entityType != dcgm.FE_GPU {
		// Labels only annotate samples. The GPU watch list also supports exporter-owned GPU collectors.
		return nil
	}

	labelDeviceFields := watcher.GetDeviceFields(e.counters.LabelCounters(), entityType)

	deviceInfo, err := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
	if errors.Is(err, deviceinfo.ErrNoEntities) {
		return nil
	}
	if err != nil {
		return err
	}

	if entityType == dcgm.FE_GPU {
		deviceInfo.WarnMIGInstancesWithoutComputeInstances()
	}

	watchFields := buildWatchFields(deviceFields.Fields, labelDeviceFields.Fields)
	computeInstanceFields := []dcgm.Short(nil)
	fieldsAtMultipleScopes := []dcgm.Short(nil)
	gpuLevelFields := []dcgm.Short(nil)
	if entityType == dcgm.FE_GPU {
		computeInstanceFields = buildWatchFields(
			deviceFields.ComputeInstanceFields,
			labelDeviceFields.ComputeInstanceFields,
		)
		fieldsAtMultipleScopes = buildWatchFields(
			deviceFields.FieldsAtMultipleScopes,
			labelDeviceFields.FieldsAtMultipleScopes,
		)
		gpuLevelFields = dedupeFields(append(
			append([]dcgm.Short(nil), deviceFields.GPULevelFields...),
			labelDeviceFields.GPULevelFields...,
		))
	}
	fieldWatchGroups, err := e.partitionFieldWatchGroups(watchFields, collectInterval)
	if err != nil {
		return err
	}

	watchRetention := effectiveRetention(e.watchRetention)
	maxKeepSamples, err := watchRetention.DCGMMaxKeepSamples()
	if err != nil {
		return fmt.Errorf("invalid default field watch retention: %w", err)
	}
	watchList := newWatchListWithGroups(
		deviceInfo,
		deviceFields.Fields,
		labelDeviceFields.Fields,
		computeInstanceFields,
		fieldsAtMultipleScopes,
		gpuLevelFields,
		fieldWatchGroups,
		watcher,
		collectInterval,
		watchRetention.MaxAge.Seconds(),
		maxKeepSamples,
	)
	if entityType == dcgm.FE_GPU {
		if err := e.addOTLPWatchPlan(watchList, watcher); err != nil {
			return err
		}
	}
	e.entityWatchLists[entityType] = *watchList
	return nil
}

// EntityWatchList returns a given entity's WatchList and true if such WatchList exists otherwise
// an empty WatchList and false.
func (e *WatchListManager) EntityWatchList(deviceType dcgm.Field_Entity_Group) (WatchList, bool) {
	entityWatchList, exists := e.entityWatchLists[deviceType]
	return entityWatchList, exists
}

// defaultFieldWatchGroups assigns all fields to the base collection interval.
func defaultFieldWatchGroups(deviceFields []dcgm.Short, collectInterval int64) []devicewatcher.FieldWatchGroup {
	return defaultFieldWatchGroupsWithRetention(
		deviceFields,
		collectInterval,
		appconfig.DefaultWatchMaxKeepAge.Seconds(),
		int32(appconfig.DefaultWatchMaxSamples),
	)
}

// defaultFieldWatchGroupsWithRetention assigns fields to one base-interval group with the supplied retention policy.
// Partitioning uses it when no YAML watch groups exist, while compatibility callers supply the legacy policy.
func defaultFieldWatchGroupsWithRetention(
	deviceFields []dcgm.Short,
	collectInterval int64,
	maxKeepAge float64,
	maxKeepSamples int32,
) []devicewatcher.FieldWatchGroup {
	if len(deviceFields) == 0 {
		return nil
	}
	return []devicewatcher.FieldWatchGroup{
		{
			Name:           "default",
			Fields:         append([]dcgm.Short(nil), deviceFields...),
			IntervalMSec:   collectInterval,
			MaxKeepAge:     maxKeepAge,
			MaxKeepSamples: maxKeepSamples,
		},
	}
}

// partitionFieldWatchGroups resolves configured name patterns against entity fields.
func (e *WatchListManager) partitionFieldWatchGroups(
	deviceFields []dcgm.Short, collectInterval int64,
) ([]devicewatcher.FieldWatchGroup, error) {
	watchRetention := effectiveRetention(e.watchRetention)
	defaultMaxKeepSamples, err := watchRetention.DCGMMaxKeepSamples()
	if err != nil {
		return nil, fmt.Errorf("invalid default field watch retention: %w", err)
	}

	// Without watch groups every field goes in one default group.
	if len(e.watchGroups) == 0 {
		return defaultFieldWatchGroupsWithRetention(
			deviceFields,
			collectInterval,
			watchRetention.MaxAge.Seconds(),
			defaultMaxKeepSamples,
		), nil
	}

	policy, err := newFieldWatchPolicy(
		e.watchGroups, e.watchRetention, time.Duration(collectInterval)*time.Millisecond)
	if err != nil {
		return nil, err
	}

	// Each field goes to the first group that matches it, or to the default group.
	fieldNames := counterNamesByFieldID(e.counters)
	groupFields := make([][]dcgm.Short, len(e.watchGroups))
	var defaultFields []dcgm.Short
	for _, fieldID := range deviceFields {
		watch := policy.of(fieldNames[fieldID])
		if watch.Group < 0 {
			defaultFields = append(defaultFields, fieldID)
			continue
		}

		groupFields[watch.Group] = append(groupFields[watch.Group], fieldID)
	}

	// Groups that took no field are dropped.
	result := make([]devicewatcher.FieldWatchGroup, 0, len(e.watchGroups)+1)
	for groupIndex, watchGroup := range e.watchGroups {
		if len(groupFields[groupIndex]) == 0 {
			continue
		}

		retention := watchGroup.Retention.Resolve(watchRetention)
		maxKeepSamples, err := retention.DCGMMaxKeepSamples()
		if err != nil {
			return nil, fmt.Errorf("invalid %s retention: %w", watchGroup.Describe(), err)
		}

		result = append(result, devicewatcher.FieldWatchGroup{
			Name:           watchGroup.Name,
			Fields:         groupFields[groupIndex],
			IntervalMSec:   int64(watchGroup.Interval),
			MaxKeepAge:     retention.MaxAge.Seconds(),
			MaxKeepSamples: maxKeepSamples,
		})
	}

	// Fields no group matched are watched together at the default interval.
	if len(defaultFields) > 0 {
		result = append(result, devicewatcher.FieldWatchGroup{
			Name:           "default",
			Fields:         defaultFields,
			IntervalMSec:   collectInterval,
			MaxKeepAge:     watchRetention.MaxAge.Seconds(),
			MaxKeepSamples: defaultMaxKeepSamples,
		})
	}

	return result, nil
}

// ValidateWatchGroups verifies configured watch groups match fields without overlap.
func ValidateWatchGroups(counters counters.CounterList, watchGroups []appconfig.WatchGroup) error {
	if len(watchGroups) == 0 {
		return nil
	}

	if err := appconfig.ValidateWatchGroupPatterns(watchGroups); err != nil {
		return err
	}

	groupMatches, err := matchWatchGroupFields(counterNamesByFieldID(counters), watchGroups)
	if err != nil {
		return err
	}

	for i, matchCount := range groupMatches {
		if matchCount == 0 {
			return &UnmatchedWatchGroupError{Group: watchGroups[i]}
		}
	}

	return nil
}

// UnmatchedWatchGroupError reports a watch group that selects none of the
// configured fields. A group read from a version 2 file is an override and the
// message says so; a version 1 group keeps the watch group wording.
type UnmatchedWatchGroupError struct {
	Group appconfig.WatchGroup
}

// Error names the override or watch group.
func (e *UnmatchedWatchGroupError) Error() string {
	if e.Group.Path == "" {
		return e.Group.Describe() + " matched no configured fields"
	}

	return fmt.Sprintf("%s%s matched no configured fields", e.Group.Describe(), groupPathSuffix(e.Group))
}

// groupPathSuffix names the YAML location of a version 2 override whose name
// differs from its path. Version 1 groups and path-named groups add nothing.
func groupPathSuffix(watchGroup appconfig.WatchGroup) string {
	if watchGroup.Path == "" || watchGroup.Path == watchGroup.Name {
		return ""
	}
	return " (" + watchGroup.Path + ")"
}

// matchWatchGroupFields counts matches in stable field order and rejects the
// first overlap, preserving deterministic diagnostics for static inspection.
func matchWatchGroupFields(fieldNames map[dcgm.Short]string, watchGroups []appconfig.WatchGroup) ([]int, error) {
	groupMatches := make([]int, len(watchGroups))
	fieldMatches := map[dcgm.Short]string{}

	matchers, err := appconfig.WatchGroupMatchers(watchGroups)
	if err != nil {
		return nil, err
	}

	for _, fieldID := range sortedFieldIDs(fieldNames) {
		fieldName := fieldNames[fieldID]

		for i, watchGroup := range watchGroups {
			if fieldName == "" || !MatchesField(matchers[i], fieldName) {
				continue
			}

			groupMatches[i]++

			if matchedGroup, ok := fieldMatches[fieldID]; ok {
				return nil, fmt.Errorf(
					"field %s matches multiple %s: %s and %s",
					fieldName,
					groupNoun(watchGroup),
					matchedGroup,
					watchGroup.Name+groupPathSuffix(watchGroup),
				)
			}

			fieldMatches[fieldID] = watchGroup.Name + groupPathSuffix(watchGroup)
		}
	}

	return groupMatches, nil
}

// groupNoun is the plural name of the groups in a message: overrides for the
// config file, watch groups for the older config format.
func groupNoun(watchGroup appconfig.WatchGroup) string {
	if watchGroup.Path != "" {
		return "overrides"
	}

	return "watch groups"
}

// sortedFieldIDs returns field IDs in numeric order for stable diagnostics.
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
