/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
)

// watchPolicy holds the interval and history limits shared by a field group.
type watchPolicy struct {
	name       string
	interval   int64
	maxAge     float64
	maxSamples int32
}

// mergeWatchGroups adds OTLP fields while preserving Prometheus watch policies.
func mergeWatchGroups(prometheus, selected []devicewatcher.FieldWatchGroup) ([]devicewatcher.FieldWatchGroup, error) {
	if len(selected) == 0 {
		return prometheus, nil
	}

	policies := make(map[dcgm.Short]watchPolicy)
	order := addWatchPolicies(policies, nil, selected)
	for _, group := range prometheus {
		policy := watchPolicy{
			name: group.Name, interval: group.IntervalMSec,
			maxAge: group.MaxKeepAge, maxSamples: group.MaxKeepSamples,
		}
		for _, field := range group.Fields {
			policies[field] = policy
		}
	}

	order = unionFields(groupFields(prometheus), order)
	groups := make([]devicewatcher.FieldWatchGroup, 0)
	positions := make(map[watchPolicy]int)
	for _, field := range order {
		policy := policies[field]
		if policy.interval <= 0 || policy.maxAge < 0 || policy.maxSamples < 0 || (policy.maxAge == 0 && policy.maxSamples == 0) {
			return nil, fmt.Errorf(
				"field %d has invalid merged watch policy from %q: interval=%dms, max age=%gs, max samples=%d; "+
					"interval must be positive, limits nonnegative, and at least one history limit must be finite",
				field, policy.name, policy.interval, policy.maxAge, policy.maxSamples,
			)
		}

		key := policy
		key.name = ""
		index, exists := positions[key]
		if !exists {
			index = len(groups)
			positions[key] = index
			groups = append(groups, devicewatcher.FieldWatchGroup{
				Name: policy.name, IntervalMSec: policy.interval,
				MaxKeepAge: policy.maxAge, MaxKeepSamples: policy.maxSamples,
			})
		}
		groups[index].Fields = append(groups[index].Fields, field)
	}

	return groups, nil
}

// addWatchPolicies resolves overlapping OTLP collection requests in first-seen field order.
func addWatchPolicies(policies map[dcgm.Short]watchPolicy, order []dcgm.Short, groups []devicewatcher.FieldWatchGroup) []dcgm.Short {
	for _, group := range groups {
		incoming := watchPolicy{
			name: group.Name, interval: group.IntervalMSec,
			maxAge: group.MaxKeepAge, maxSamples: group.MaxKeepSamples,
		}
		for _, field := range group.Fields {
			policy, exists := policies[field]
			if !exists {
				order = append(order, field)
				policies[field] = incoming
				continue
			}

			policy.interval = min(policy.interval, incoming.interval)
			policy.maxAge = mergeRetentionBound(policy.maxAge, incoming.maxAge)
			policy.maxSamples = mergeRetentionBound(policy.maxSamples, incoming.maxSamples)
			if policy.maxAge == 0 && policy.maxSamples == 0 {
				policy.name += " and " + incoming.name
			}
			policies[field] = policy
		}
	}

	return order
}

// mergeRetentionBound selects the larger limit, with zero retaining its unbounded meaning.
func mergeRetentionBound[T ~int32 | ~float64](a, b T) T {
	if a == 0 || b == 0 {
		return 0
	}

	return max(a, b)
}

// gpuWatchScopes keeps physical-GPU reads before instance reads so instance
// identities can reuse PCIe attributes collected from their parent GPU.
var gpuWatchScopes = []devicewatcher.WatchScope{
	devicewatcher.WatchSelectedGPUs,
	devicewatcher.WatchUnselectedParentGPUs,
	devicewatcher.WatchGPUInstances,
	devicewatcher.WatchOnlyComputeInstances,
}

// scopedWatchGroups keeps one entity scope's watches and Prometheus selection together.
type scopedWatchGroups struct {
	scope            devicewatcher.WatchScope
	groups           []devicewatcher.FieldWatchGroup
	prometheusFields []dcgm.Short
}

// ReadRequest keeps watched fields separate from the Prometheus metric selection.
type ReadRequest struct {
	Entity           devicemonitoring.Info
	Fields           []dcgm.Short
	PrometheusFields []dcgm.Short
}

// HasOTLPWatches reports whether OTLP sources expanded this list's watch plan.
func (d *WatchList) HasOTLPWatches() bool {
	return len(d.scopedWatchGroups) > 0
}

// Reads returns the shared plan's entity reads, or nil when no shared plan exists.
func (d *WatchList) Reads() ([]ReadRequest, error) {
	var reads []ReadRequest
	for _, scoped := range d.scopedWatchGroups {
		entities, err := devicewatcher.EntitiesForScope(d.deviceInfo, scoped.scope)
		if err != nil {
			return nil, err
		}

		fields := groupFields(scoped.groups)
		for _, entity := range entities {
			reads = append(reads, ReadRequest{Entity: entity, Fields: fields, PrometheusFields: scoped.prometheusFields})
		}
	}

	return reads, nil
}

// watchScopedGroups installs every scope and releases earlier watches if a later scope fails.
func (d *WatchList) watchScopedGroups() (*devicewatcher.WatchResult, error) {
	var results []*devicewatcher.WatchResult
	for _, scoped := range d.scopedWatchGroups {
		result, err := d.watchScope(scoped.groups, scoped.scope)
		if err != nil {
			devicewatcher.JoinWatchResults(results...).Close()
			return nil, err
		}

		results = append(results, result)
	}

	return devicewatcher.JoinWatchResults(results...), nil
}

// groupFields returns field IDs once, in their first-seen order.
func groupFields(groups []devicewatcher.FieldWatchGroup) []dcgm.Short {
	var fields []dcgm.Short
	for _, group := range groups {
		fields = append(fields, group.Fields...)
	}

	return dedupeFields(fields)
}

// setHistoricalFields narrows backing fields without letting physical policies override instance policies.
func (d *WatchList) setHistoricalFields(fields []dcgm.Short, scope devicewatcher.WatchScope) {
	var groups []scopedWatchGroups
	for _, scoped := range d.scopedWatchGroups {
		if scope == devicewatcher.WatchPhysicalGPUs &&
			scoped.scope != devicewatcher.WatchSelectedGPUs && scoped.scope != devicewatcher.WatchUnselectedParentGPUs {
			continue
		}

		list := *d
		list.fieldWatchGroups = scoped.groups
		groups = append(groups, scopedWatchGroups{scope: scoped.scope, groups: list.fieldWatchGroupsForFields(fields)})
	}

	d.scopedWatchGroups = groups
	d.deviceFields = fields
	d.computeInstanceFields = nil
	d.fieldsAtMultipleScopes = nil
	d.entityScope = scope
	d.watchFields = dedupeFields(fields)
	d.fieldWatchGroups = d.fieldWatchGroupsForFields(d.watchFields)
}

// SplitInstanceFields separates instance fields from compute-instance fields.
// Multi-scope fields stay in both sets; labels follow each set, and GPU-only
// fields stay out of both when MIG instances are selected. An empty instance
// selection stays non-nil so callers do not fall back to the full field list.
func SplitInstanceFields(
	fields, computeInstanceFields, fieldsAtMultipleScopes, labelFields, gpuLevelFields []dcgm.Short,
) ([]dcgm.Short, []dcgm.Short) {
	if len(gpuLevelFields) > 0 {
		fields = fieldsOutsideScope(fields, gpuLevelFields)
		computeInstanceFields = fieldsOutsideScope(computeInstanceFields, gpuLevelFields)
		labelFields = fieldsOutsideScope(labelFields, gpuLevelFields)
	}

	if len(computeInstanceFields) == 0 {
		return slices.Clone(fields), nil
	}

	computeInstanceSet := make(map[dcgm.Short]struct{}, len(computeInstanceFields))
	for _, fieldID := range computeInstanceFields {
		computeInstanceSet[fieldID] = struct{}{}
	}
	multiScopeSet := make(map[dcgm.Short]struct{}, len(fieldsAtMultipleScopes))
	for _, fieldID := range fieldsAtMultipleScopes {
		multiScopeSet[fieldID] = struct{}{}
	}
	nonComputeInstanceFields := make([]dcgm.Short, 0, len(fields))
	for _, fieldID := range fields {
		_, isComputeInstanceField := computeInstanceSet[fieldID]
		_, isMultiScopeField := multiScopeSet[fieldID]
		if !isComputeInstanceField || isMultiScopeField {
			nonComputeInstanceFields = append(nonComputeInstanceFields, fieldID)
		}
	}

	computeInstanceFields = append([]dcgm.Short{}, computeInstanceFields...)
	computeInstanceFields = append(computeInstanceFields, labelFields...)
	return nonComputeInstanceFields, dedupeFields(computeInstanceFields)
}

// prometheusGroups captures the original fields and policies for each disjoint GPU scope.
func (d *WatchList) prometheusGroups() map[devicewatcher.WatchScope][]devicewatcher.FieldWatchGroup {
	gpuLevelFields := d.GPULevelFields()
	rest := d.withoutFields(gpuLevelFields)
	instanceFields, computeFields := SplitInstanceFields(
		d.watchFields, d.computeInstanceFields, d.fieldsAtMultipleScopes, d.labelDeviceFields, gpuLevelFields,
	)
	if len(intersectFields(instanceFields, d.deviceFields)) == 0 {
		instanceFields = nil
	}

	parentFields := buildWatchFields(rest.fieldsAtMultipleScopes, rest.labelDeviceFields)
	if len(gpuLevelFields) > 0 {
		parentFields = unionFields(d.physicalGPUFields(gpuLevelFields), parentFields)
	}
	return map[devicewatcher.WatchScope][]devicewatcher.FieldWatchGroup{
		devicewatcher.WatchSelectedGPUs:         d.fieldWatchGroupsForFields(d.watchFields),
		devicewatcher.WatchGPUInstances:         d.fieldWatchGroupsForFields(instanceFields),
		devicewatcher.WatchOnlyComputeInstances: d.fieldWatchGroupsForFields(computeFields),
		devicewatcher.WatchUnselectedParentGPUs: d.fieldWatchGroupsForFields(parentFields),
	}
}

// addOTLPWatchPlan merges collection demands per scope while retaining the original metric fields.
func (e *WatchListManager) addOTLPWatchPlan(list *WatchList, watcher devicewatcher.WatchInstaller) error {
	if len(e.otlpCollections) == 0 {
		return nil
	}

	prometheus := list.prometheusGroups()
	selected := make(map[devicewatcher.WatchScope][]devicewatcher.FieldWatchGroup)
	for _, collection := range e.otlpCollections {
		if err := e.addCollectionGroups(selected, collection, list, watcher); err != nil {
			return err
		}
	}

	if len(selected) == 0 {
		return nil
	}

	var allGroups []devicewatcher.FieldWatchGroup
	for _, scope := range gpuWatchScopes {
		groups, err := mergeWatchGroups(prometheus[scope], selected[scope])
		if err != nil {
			return err
		}

		if len(groups) == 0 {
			continue
		}

		list.scopedWatchGroups = append(list.scopedWatchGroups, scopedWatchGroups{
			scope: scope, groups: groups, prometheusFields: groupFields(prometheus[scope]),
		})
		allGroups = append(allGroups, groups...)
	}

	list.fieldWatchGroups = allGroups
	return nil
}

// addCollectionGroups resolves one collection's fields, scopes, and inherited retention into watch demands.
func (e *WatchListManager) addCollectionGroups(
	selected map[devicewatcher.WatchScope][]devicewatcher.FieldWatchGroup,
	collection appconfig.OTLPWatchCollection,
	list *WatchList,
	watcher devicewatcher.WatchInstaller,
) error {
	physical, flexible, err := sourceCounters(collection.Sources)
	if err != nil {
		return fmt.Errorf("collection %q: %w", collection.Name, err)
	}

	gpuFields := watcher.GetDeviceFields(physical, dcgm.FE_GPU)
	flexFields := watcher.GetDeviceFields(flexible, dcgm.FE_GPU)

	retention := collection.Retention.Resolve(effectiveRetention(e.watchRetention))
	samples, err := retention.DCGMMaxKeepSamples()
	if err != nil {
		return fmt.Errorf("collection %q retention: %w", collection.Name, err)
	}

	instanceFields, computeFields := SplitInstanceFields(
		flexFields.Fields, flexFields.ComputeInstanceFields, flexFields.FieldsAtMultipleScopes, nil, flexFields.GPULevelFields,
	)
	fields := map[devicewatcher.WatchScope][]dcgm.Short{
		devicewatcher.WatchSelectedGPUs:         unionFields(gpuFields.Fields, flexFields.Fields),
		devicewatcher.WatchGPUInstances:         instanceFields,
		devicewatcher.WatchOnlyComputeInstances: computeFields,
		devicewatcher.WatchUnselectedParentGPUs: unionFields(gpuFields.Fields, unionFields(flexFields.GPULevelFields, flexFields.FieldsAtMultipleScopes)),
	}
	for _, scope := range gpuWatchScopes {
		if len(fields[scope]) == 0 {
			continue
		}
		selected[scope] = append(selected[scope], devicewatcher.FieldWatchGroup{
			Name: collection.Name, Fields: fields[scope], IntervalMSec: int64(collection.Interval),
			MaxKeepAge: retention.MaxAge.Seconds(), MaxKeepSamples: samples,
		})
	}

	list.deviceFields = unionFields(list.deviceFields, unionFields(gpuFields.Fields, flexFields.Fields))
	list.computeInstanceFields = unionFields(list.computeInstanceFields, flexFields.ComputeInstanceFields)
	list.fieldsAtMultipleScopes = unionFields(list.fieldsAtMultipleScopes, flexFields.FieldsAtMultipleScopes)
	list.watchFields = buildWatchFields(list.deviceFields, list.labelDeviceFields)
	return nil
}

// unionFields returns a deduplicated copy of both field lists.
func unionFields(first, second []dcgm.Short) []dcgm.Short {
	fields := append([]dcgm.Short(nil), first...)
	return dedupeFields(append(fields, second...))
}

// sourceCounters separates physical-GPU dependencies from sources that retain native instance scopes.
func sourceCounters(sources []signalcatalog.WatchSource) (counters.CounterList, counters.CounterList, error) {
	var physical, flexible counters.CounterList
	for _, selected := range sources {
		fields, err := countersForSource(selected.Source)
		if err != nil {
			return nil, nil, err
		}

		if selected.Entity == signalcatalog.EntityGPU {
			physical = append(physical, fields...)
		} else {
			flexible = append(flexible, fields...)
		}
	}

	return physical, flexible, nil
}

// countersForSource reuses exporter backing fields or retains the selected native source.
func countersForSource(source signalcatalog.Source) (counters.CounterList, error) {
	if source.Kind == signalcatalog.SourceDCGM {
		return counters.CounterList{{FieldID: dcgm.Short(source.FieldID), FieldName: source.Name}}, nil
	}

	family, ok := counters.DCGMFields[source.Name]
	if !ok {
		return nil, fmt.Errorf("unknown exporter watch source %q", source.Name)
	}

	set := counters.CounterSet{ExporterCounters: counters.CounterList{{FieldID: dcgm.Short(family), FieldName: source.Name}}}
	return set.WatchCounters(), nil
}
