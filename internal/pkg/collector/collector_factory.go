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

package collector

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// Factory builds the collectors for one registry from the configured counters.
type Factory interface {
	// NewCollectors builds the collectors needed for one registry. The caller
	// registers the returned collectors only when err is nil; on error, this method
	// has already requested cleanup for every collector it created and returns no
	// candidates. When cleanup must finish after a bounded stop request, err
	// implements CleanupCompletionError; the runtime owner waits for that signal
	// before releasing the native client.
	//
	// Side effects: each returned collector installs DCGM field watches and
	// GPU groups. These are released by (*registry.Registry).Cleanup after
	// the collector is registered. Callers MUST register every returned
	// collector and MUST call this method exactly once per registry
	// lifecycle — calling it a second time without registering leaks DCGM
	// field groups and GPU groups, surfacing as "RemoveFieldWatch returned
	// -16 (NOT_WATCHED)" warnings in nv-hostengine.log.
	NewCollectors() ([]EntityCollectorTuple, error)
}

// collectorFactory builds collector families from one counter set and device
// topology. buildRegistry creates one factory for each candidate registry.
type collectorFactory struct {
	client                 dcgmprovider.DCGM
	counterSet             *counters.CounterSet
	deviceWatchListManager devicewatchlistmanager.Manager
	hostname               string
	config                 *appconfig.Config
}

// exporterFamily describes one exporter-owned metric family. The list keeps
// its enable check and constructor together, so adding a family has one
// obvious registration point instead of parallel condition and switch edits.
type exporterFamily struct {
	name    string
	enabled func(counters.CounterList) bool
	build   func(*collectorFactory, devicewatchlistmanager.WatchList) (Collector, error)
}

// exporterFamilies is the complete, static set of exporter-owned collectors.
// Constructors receive the resolved GPU watch list after the factory confirms
// the family is enabled, which gives every family the same registration path.
var exporterFamilies = []exporterFamily{
	{
		name:    counters.DCGMExpClockEventsCount,
		enabled: IsDCGMExpClockEventsCountEnabled,
		build: func(cf *collectorFactory, watchList devicewatchlistmanager.WatchList) (Collector, error) {
			return newClockEventsCollectorWithClient(cf.client, cf.counterSet.ExporterCounters, cf.hostname, cf.config, watchList)
		},
	},
	{
		name:    counters.DCGMExpClockEventsTotal,
		enabled: IsDCGMExpClockEventsTotalEnabled,
		build: func(cf *collectorFactory, watchList devicewatchlistmanager.WatchList) (Collector, error) {
			return newClockEventsTotalCollectorWithClient(cf.client, cf.counterSet.ExporterCounters, cf.hostname, cf.config, watchList)
		},
	},
	{
		name:    counters.DCGMExpXIDErrorsCount,
		enabled: IsDCGMExpXIDErrorsCountEnabled,
		build: func(cf *collectorFactory, watchList devicewatchlistmanager.WatchList) (Collector, error) {
			return newXIDCollectorWithClient(cf.client, cf.counterSet.ExporterCounters, cf.hostname, cf.config, watchList)
		},
	},
	{
		name:    counters.DCGMExpXIDErrorsTotal,
		enabled: IsDCGMExpXIDErrorsTotalEnabled,
		build: func(cf *collectorFactory, watchList devicewatchlistmanager.WatchList) (Collector, error) {
			return newXIDTotalCollectorWithClient(cf.client, cf.counterSet.ExporterCounters, cf.hostname, cf.config, watchList)
		},
	},
	{
		name:    counters.DCGMExpGPUHealthStatus,
		enabled: IsDCGMExpGPUHealthStatusEnabled,
		build: func(cf *collectorFactory, watchList devicewatchlistmanager.WatchList) (Collector, error) {
			return newGPUHealthStatusCollectorWithClient(cf.client, cf.counterSet.ExporterCounters, cf.hostname, cf.config, watchList)
		},
	},
	{
		name:    counters.DCGMExpP2PStatus,
		enabled: IsDCGMExpP2PStatusEnabled,
		build: func(cf *collectorFactory, watchList devicewatchlistmanager.WatchList) (Collector, error) {
			return newP2PStatusCollectorWithClient(cf.client, cf.counterSet.ExporterCounters, cf.hostname, cf.config, watchList)
		},
	},
}

// collectorRollbackError keeps a factory error and the completion signal for
// collectors that needed asynchronous cleanup during rollback. Its caller owns
// waiting with the application lifecycle deadline before native teardown.
type collectorRollbackError struct {
	err  error
	done <-chan struct{}
}

// Error returns the original collector construction failure.
func (e *collectorRollbackError) Error() string {
	return e.err.Error()
}

// Unwrap lets callers match the original construction error with errors.Is.
func (e *collectorRollbackError) Unwrap() error {
	return e.err
}

// CleanupComplete reports that every asynchronously stopping rollback
// collector has released its native watches.
func (e *collectorRollbackError) CleanupComplete() <-chan struct{} {
	return e.done
}

// InitCollectorFactory creates the factory used by buildRegistry to construct
// all collectors for a single resolved configuration and device topology.
func InitCollectorFactory(
	counterSet *counters.CounterSet,
	deviceWatchListManager devicewatchlistmanager.Manager,
	hostname string,
	config *appconfig.Config,
) Factory {
	return InitCollectorFactoryWithClient(
		dcgmprovider.Client(),
		counterSet,
		deviceWatchListManager,
		hostname,
		config,
	)
}

// InitCollectorFactoryWithClient creates the factory for one registry
// generation using client for all collector construction. The application keeps
// that client alive until the registry reports retirement completion.
func InitCollectorFactoryWithClient(
	client dcgmprovider.DCGM,
	counterSet *counters.CounterSet,
	deviceWatchListManager devicewatchlistmanager.Manager,
	hostname string,
	config *appconfig.Config,
) Factory {
	return &collectorFactory{
		client:                 client,
		counterSet:             counterSet,
		deviceWatchListManager: deviceWatchListManager,
		hostname:               hostname,
		config:                 config,
	}
}

// dcgmEntityTypes lists the DCGM entity types that get a collector, in build order.
var dcgmEntityTypes = []dcgm.Field_Entity_Group{
	dcgm.FE_GPU,
	dcgm.FE_SWITCH,
	dcgm.FE_LINK,
	dcgm.FE_CPU,
	dcgm.FE_CPU_CORE,
}

// NewCollectors builds all enabled collector families. It keeps ownership of
// each collector until the complete build succeeds, then hands the slice to
// buildRegistry for registration and later cleanup.
func (cf *collectorFactory) NewCollectors() ([]EntityCollectorTuple, error) {
	slog.Debug("Counters are being initialized.",
		slog.String(logging.DumpKey, fmt.Sprintf("%+v", cf.counterSet.DCGMCounters)))

	entityCollectorTuples, err := cf.newDCGMCollectors()
	if err != nil {
		return nil, err
	}

	for _, family := range exporterFamilies {
		if !family.enabled(cf.counterSet.ExporterCounters) {
			continue
		}
		newCollector, err := cf.enableExporterFamily(family)
		if err != nil {
			slog.Error(fmt.Sprintf("collector '%s' cannot be initialized; err: %v", family.name, err))
			return rollbackCollectors(entityCollectorTuples, buildCollectorError(family.name, err))
		}
		if newCollector == nil {
			continue
		}
		entityCollectorTuples = append(entityCollectorTuples, EntityCollectorTuple{
			entity:    dcgm.FE_GPU,
			collector: newCollector,
		})
	}

	if IsDCGMExpSystemHealthStatusEnabled(cf.counterSet.ExporterCounters) {
		newCollector, err := newSystemHealthStatusCollectorWithClient(cf.client, cf.counterSet.ExporterCounters, cf.hostname)
		if err != nil {
			return rollbackCollectors(entityCollectorTuples, buildCollectorError(counters.DCGMExpSystemHealthStatus, err))
		}
		entityCollectorTuples = append(entityCollectorTuples, EntityCollectorTuple{entity: dcgm.FE_NONE, collector: newCollector})
	}

	if IsDCGMExpFieldPolicyEnabled(cf.counterSet.ExporterCounters) {
		newCollector, err := NewFieldPolicyCollector(cf.counterSet.ExporterCounters, cf.hostname)
		if err != nil {
			return rollbackCollectors(entityCollectorTuples, buildCollectorError(counters.DCGMExpFieldPolicyEnabled, err))
		}
		entityCollectorTuples = append(entityCollectorTuples, EntityCollectorTuple{entity: dcgm.FE_NONE, collector: newCollector})
	}

	return entityCollectorTuples, nil
}

// buildCollectorError reports that the collector for one metric family could
// not be built, keeping err for errors.Is and errors.As.
func buildCollectorError(name string, err error) error {
	return fmt.Errorf("build collector %s: %w", name, err)
}

// newDCGMCollectors builds collectors needed by either output and rolls back partial failures.
func (cf *collectorFactory) newDCGMCollectors() ([]EntityCollectorTuple, error) {
	selectedCounters := len(cf.counterSet.DCGMCounters) > 0
	if !selectedCounters && (cf.config == nil || !cf.config.EnableOTLP) {
		return nil, nil
	}

	var built []EntityCollectorTuple
	var refusedFields error

	for _, entityType := range dcgmEntityTypes {
		list, exists := cf.deviceWatchListManager.EntityWatchList(entityType)
		if !exists || list.IsEmpty() {
			continue
		}

		if !selectedCounters && !list.HasOTLPWatches() {
			continue
		}

		newCollector, err := cf.enableDCGMCollector(list, entityType, &refusedFields)
		if err != nil {
			return rollbackCollectors(built, err)
		}
		if newCollector != nil {
			built = append(built, EntityCollectorTuple{entity: entityType, collector: newCollector})
		}
	}

	return built, nil
}

// dcgmCollectorBuildError wraps a DCGM collector failure for the caller. The
// stored refused-fields explanation covers every entity watch list, so it is not
// attributed to one entity type.
func dcgmCollectorBuildError(entityType dcgm.Field_Entity_Group, err, refusedFields error) error {
	if refusedFields != nil && errors.Is(err, refusedFields) {
		return fmt.Errorf("build DCGM collectors: %w", err)
	}
	return fmt.Errorf("build DCGM collector for entity type %s: %w", entityType.String(), err)
}

// logDCGMCollectorFailure logs why one entity type's DCGM collector failed and
// returns the error to report. A refused-fields failure is widened to every
// entity watch list and logged in full once; a later failure that shares it
// points back to it.
func (cf *collectorFactory) logDCGMCollectorFailure(
	entityType dcgm.Field_Entity_Group,
	err error,
	refusedFields *error,
) error {
	alreadyExplained := *refusedFields != nil
	explained := cf.explainRefusedFields(err, refusedFields)
	switch {
	case explained == nil:
		slog.Error(fmt.Sprintf("DCGM collector for entity type '%s' cannot be initialized; err: %v",
			entityType.String(), err))
		return err
	case alreadyExplained:
		slog.Error(fmt.Sprintf("DCGM collector for entity type '%s' cannot be initialized; "+
			"see the refused-fields error above", entityType.String()))
	default:
		slog.Error(fmt.Sprintf("DCGM collectors cannot be initialized; err: %v", explained))
	}
	return explained
}

// explainRefusedFields returns the explanation for a failure caused by fields
// the hostengine refuses, or nil for any other error. WatchList.Watch explains
// only its own list (the profiling repair path also uses it); here the first
// explanation is widened at start-up to every entity watch list that has device
// fields, and is kept in *stored so later failures reuse it without probing
// again. If widening fails, it returns nil and stores nothing, so the caller
// keeps the watch's own explanation.
func (cf *collectorFactory) explainRefusedFields(err error, stored *error) error {
	var refusedErr *devicewatcher.RefusedFieldsError
	if !errors.As(err, &refusedErr) {
		return nil
	}
	if *stored != nil {
		return *stored
	}

	explained := devicewatcher.ExplainRefusedFields(cf.client, cf.refusedFieldsProbeGroups(), refusedErr.Unwrap())
	if !errors.As(explained, &refusedErr) {
		// Widening failed: return nil so the caller logs this watch's own
		// explanation under its entity heading, and store nothing, so a later
		// failure still reports its own fields.
		return nil
	}
	*stored = explained
	return explained
}

// refusedFieldsProbeGroups returns the watch groups of every entity watch list
// that has device fields. Each group is named after its entity, and a field
// already in an earlier group is left out, so it is probed only once.
func (cf *collectorFactory) refusedFieldsProbeGroups() []devicewatcher.FieldWatchGroup {
	var groups []devicewatcher.FieldWatchGroup
	seen := make(map[dcgm.Short]struct{})
	for _, entityType := range dcgmEntityTypes {
		watchList, exists := cf.deviceWatchListManager.EntityWatchList(entityType)
		if !exists || len(watchList.DeviceFields()) == 0 {
			continue
		}
		for _, group := range watchList.FieldWatchGroups() {
			fields := unseenFields(group.Fields, seen)
			if len(fields) == 0 {
				continue
			}
			group.Name = entityType.String() + " " + group.Name
			group.Fields = fields
			groups = append(groups, group)
		}
	}
	return groups
}

// unseenFields returns the fields that are not in seen yet and adds them to it.
func unseenFields(fields []dcgm.Short, seen map[dcgm.Short]struct{}) []dcgm.Short {
	var unseen []dcgm.Short
	for _, field := range fields {
		if _, dup := seen[field]; !dup {
			seen[field] = struct{}{}
			unseen = append(unseen, field)
		}
	}
	return unseen
}

// rollbackCollectors requests cleanup for successful candidate collectors in
// reverse build order and returns err for the application to handle.
// Constructors clean their own failed candidate, while this factory owns only
// earlier successes. A returned CleanupCompletionError means the owner must
// wait before closing native providers.
func rollbackCollectors(collectors []EntityCollectorTuple, err error) ([]EntityCollectorTuple, error) {
	completionSignals := make([]<-chan struct{}, 0)
	for index := len(collectors) - 1; index >= 0; index-- {
		collector := collectors[index].Collector()
		collector.Cleanup()
		if completionAware, ok := collector.(CleanupCompleter); ok {
			completionSignals = append(completionSignals, completionAware.CleanupComplete())
		}
	}
	if len(completionSignals) == 0 {
		return nil, err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, completion := range completionSignals {
			<-completion
		}
	}()
	return nil, &collectorRollbackError{err: err, done: done}
}

// enableDCGMCollector installs one collector. With startup validation disabled,
// a failed Prometheus-only collector returns (nil, nil). refusedFields must be
// non-nil and retains the shared refused-field diagnostic across entity types.
func (cf *collectorFactory) enableDCGMCollector(
	entityWatchList devicewatchlistmanager.WatchList, entityType dcgm.Field_Entity_Group, refusedFields *error,
) (Collector, error) {
	newCollector, err := newDCGMCollectorWithClient(cf.client, cf.counterSet.DCGMCounters, cf.hostname, cf.config,
		entityWatchList)
	if err != nil {
		err = cf.logDCGMCollectorFailure(entityType, err, refusedFields)
		if entityWatchList.HasOTLPWatches() || cf.config == nil || !cf.config.DisableStartupValidate {
			return nil, dcgmCollectorBuildError(entityType, err, *refusedFields)
		}
		return nil, nil
	}

	return newCollector, nil
}

// enableExporterFamily creates one enabled exporter-owned family using the
// resolved GPU watch list shared by all exporter counter collectors.
func (cf *collectorFactory) enableExporterFamily(family exporterFamily) (Collector, error) {
	item, exists := cf.deviceWatchListManager.EntityWatchList(dcgm.FE_GPU)
	if !exists {
		return nil, fmt.Errorf("entity type '%s' does not exist", dcgm.FE_GPU.String())
	}
	// GPU-specific collectors have no work until selected GPUs are attached.
	// In particular, DCGM cannot create health or policy groups while detached.
	if len(devicemonitoring.GetMonitoredEntities(item.DeviceInfo())) == 0 {
		return nil, nil
	}
	newCollector, err := family.build(cf, item)
	if err != nil {
		return nil, err
	}

	slog.Info(fmt.Sprintf("collector '%s' initialized", family.name))
	return newCollector, nil
}
