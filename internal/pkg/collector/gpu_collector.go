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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmerrors"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

const unknownErr = "Unknown Error"

// maxLatestValueFieldBatch matches DCGM_MAX_FIELD_IDS_PER_FIELD_GROUP, the
// fixed field-ID capacity used by DCGM's latest-value request structs.
const maxLatestValueFieldBatch = 128

// DCGMCollector owns the watched DCGM fields for one entity type and converts
// their latest values into exporter metrics during each Prometheus collection.
type DCGMCollector struct {
	mu                          sync.Mutex
	client                      dcgmprovider.DCGM
	counters                    []counters.Counter
	watchResult                 *devicewatcher.WatchResult
	useOldNamespace             bool
	deviceWatchList             devicewatchlistmanager.WatchList
	scrapeFields                []dcgm.Short
	nonComputeInstanceFields    []dcgm.Short
	computeInstanceScrapeFields []dcgm.Short
	parentGPUScrapeFields       []dcgm.Short
	inheritedLabelFields        []dcgm.Short
	hostname                    string
	replaceBlanksInModelName    bool
	enableOTLP                  bool

	profilingStaleWindows map[dcgm.Short]time.Duration
	profilingSamples      map[profilingSampleKey]profilingSample
	nextProfilingRepair   time.Time
	now                   func() time.Time
}

// dcgmClient returns the client captured when this collector and its watches
// were created. The fallback keeps direct test fixtures compatible while the
// registry factory always supplies a concrete generation client.
func (c *DCGMCollector) dcgmClient() dcgmprovider.DCGM {
	if c.client != nil {
		return c.client
	}
	return dcgmprovider.Client()
}

// profilingSampleKey identifies one profiling stream within a collector.
type profilingSampleKey struct {
	entityGroup dcgm.Field_Entity_Group
	entityID    uint
	fieldID     dcgm.Short
}

// profilingSample records how long a profiling timestamp has remained unchanged.
type profilingSample struct {
	timestamp      int64
	unchangedSince time.Time
}

// profilingRepairReason describes the stale signal and its repair backoff.
type profilingRepairReason struct {
	key       profilingSampleKey
	status    int
	timestamp int64
	staleFor  time.Duration
	backoff   time.Duration
	apiError  error
}

// String formats a profiling repair reason for logs and returned errors.
func (r *profilingRepairReason) String() string {
	if r.apiError != nil {
		return fmt.Sprintf("DCGM returned a repairable profiling watch error: %v", r.apiError)
	}
	if isRepairableProfilingStatus(r.status) {
		return fmt.Sprintf("profiling field %d for entity %s:%d returned status %d",
			r.key.fieldID, r.key.entityGroup, r.key.entityID, r.status)
	}
	return fmt.Sprintf("profiling field %d for entity %s:%d has had timestamp %d for %s",
		r.key.fieldID, r.key.entityGroup, r.key.entityID, r.timestamp, r.staleFor)
}

// NewDCGMCollector creates a collector using the current process client for
// direct callers. Registry factories use the client-specific helper below.
func NewDCGMCollector(
	c []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (*DCGMCollector, error) {
	return newDCGMCollectorWithClient(dcgmprovider.Client(), c, hostname, config, deviceWatchList)
}

// newDCGMCollectorWithClient creates one scraper and its field watches with
// client. Collector factories use it so cleanup and later reads retain the
// same native session across reload generations.
func newDCGMCollectorWithClient(
	client dcgmprovider.DCGM,
	c []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (*DCGMCollector, error) {
	if deviceWatchList.IsEmpty() {
		return nil, errors.New("deviceWatchList is empty")
	}

	scrapeFields := fieldsToScrape(deviceWatchList)
	computeInstanceFields := deviceWatchList.ComputeInstanceFields()
	fieldsAtMultipleScopes := deviceWatchList.FieldsAtMultipleScopes()
	gpuLevelFields := deviceWatchList.GPULevelFields()
	nonComputeInstanceFields, computeInstanceScrapeFields := devicewatchlistmanager.SplitInstanceFields(
		scrapeFields,
		computeInstanceFields,
		fieldsAtMultipleScopes,
		deviceWatchList.LabelDeviceFields(),
		gpuLevelFields,
	)
	gpuLevelSourceFields := withoutFieldIDs(gpuLevelFields, deviceWatchList.LabelDeviceFields())

	parentGPUScrapeFields := []dcgm.Short(nil)
	if parentFields := dedupeFieldIDs(append(fieldsAtMultipleScopes, gpuLevelSourceFields...)); len(parentFields) > 0 {
		parentGPUScrapeFields = dedupeFieldIDs(append(parentFields, deviceWatchList.LabelDeviceFields()...))
	}

	collector := &DCGMCollector{
		client:                      client,
		counters:                    c,
		deviceWatchList:             deviceWatchList,
		scrapeFields:                scrapeFields,
		nonComputeInstanceFields:    nonComputeInstanceFields,
		computeInstanceScrapeFields: computeInstanceScrapeFields,
		parentGPUScrapeFields:       parentGPUScrapeFields,
		inheritedLabelFields:        deviceWatchList.InheritedLabelFields(),
		hostname:                    hostname,
	}

	if config == nil {
		slog.Warn("Config is empty")
		return collector, nil
	}

	collector.enableOTLP = config.EnableOTLP
	collector.useOldNamespace = config.UseOldNamespace
	collector.replaceBlanksInModelName = config.ReplaceBlanksInModelName

	// Track only profiling fields that this entity collector actually watches.
	profilingStaleWindows, err := resolveProfilingStaleWindows(
		c,
		collector.deviceWatchList.DeviceFields(),
		collector.deviceWatchList.FieldWatchGroups(),
	)
	if err != nil {
		return nil, err
	}
	collector.profilingStaleWindows = profilingStaleWindows
	if len(profilingStaleWindows) > 0 {
		collector.profilingSamples = make(map[profilingSampleKey]profilingSample)
	}

	watchResult, err := collector.deviceWatchList.Watch()
	if err != nil {
		return nil, err
	}

	collector.watchResult = watchResult

	return collector, nil
}

// Cleanup releases this collector's DCGM watch resources and is safe to call repeatedly.
func (c *DCGMCollector) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cleanupWatches()
}

// GetMetrics collects one scrape and repairs a stale profiling watch once when needed.
func (c *DCGMCollector) GetMetrics() (MetricsByCounter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	metrics, reason, err := c.getMetricsOnce()
	if err != nil {
		return nil, err
	}
	if reason == nil {
		return metrics, nil
	}

	return c.repairProfilingWatch(reason, nonProfilingMetrics(metrics))
}

// repairProfilingWatch rearms a stale watch and retries the discarded scrape once.
func (c *DCGMCollector) repairProfilingWatch(
	reason *profilingRepairReason,
	fallback MetricsByCounter,
) (MetricsByCounter, error) {
	// Start the cooldown before attempting repair so repeated failures cannot churn watches.
	now := c.currentTime()
	if now.Before(c.nextProfilingRepair) {
		slog.Error(
			"DCGM profiling watch repair is rate limited",
			slog.String("reason", reason.String()),
			slog.Duration("retryAfter", c.nextProfilingRepair.Sub(now)),
		)
		return fallback, nil
	}
	c.nextProfilingRepair = now.Add(reason.backoff)

	// Rewatch before retrying so stale values are never returned to Prometheus.
	slog.Warn(
		"Repairing stale DCGM profiling watch",
		slog.String("reason", reason.String()),
		slog.Duration("repairBackoff", reason.backoff),
	)
	if err := c.rearmProfilingWatch(); err != nil {
		slog.Error("Failed to repair DCGM profiling watch", slog.String("error", err.Error()))
		if dcgmerrors.IsFatalScrapeError(err) {
			return nil, err
		}
		return fallback, nil
	}

	// The retry records fresh status/timestamp state but cannot trigger another repair.
	metrics, retryReason, err := c.getMetricsOnce()
	if err != nil {
		slog.Error("Failed to collect metrics after repairing DCGM profiling watch", slog.String("error", err.Error()))
		if dcgmerrors.IsFatalScrapeError(err) {
			return nil, err
		}
		return fallback, nil
	}
	if retryReason != nil {
		slog.Error("DCGM profiling watch remains stale after repair", slog.String("reason", retryReason.String()))
		if retryFallback := nonProfilingMetrics(metrics); len(retryFallback) > 0 {
			fallback = retryFallback
		}
		return fallback, nil
	}

	slog.Info("Repaired stale DCGM profiling watch")
	return metrics, nil
}

// getMetricsOnce performs one scrape and reports stale profiling state separately from API errors.
func (c *DCGMCollector) getMetricsOnce() (MetricsByCounter, *profilingRepairReason, error) {
	readTimestamp := c.currentTime().UnixMicro()
	reads, err := c.collectionReads()
	if err != nil {
		return nil, nil, err
	}

	metrics := make(MetricsByCounter)
	var parents map[uint]*SourceIdentity
	shared := c.deviceWatchList.HasOTLPWatches()
	if shared {
		parents = make(map[uint]*SourceIdentity)
	}
	var firstReason *profilingRepairReason
	gpuReads := make(map[uint][]dcgm.FieldValue_v1)

	for _, read := range reads {
		mi := read.Entity
		vals, err := c.valuesForRead(scrapeRead{entity: mi, fields: read.Fields}, gpuReads)
		if err != nil {
			if reason := c.repairReasonForError(err); reason != nil {
				return metrics, reason, nil
			}

			handleScrapeError(err)
			return nil, nil, err
		}

		if reason := c.observeProfilingSamples(mi.Entity, vals); reason != nil && firstReason == nil {
			firstReason = reason
		}

		if shared {
			c.addScopedMetrics(metrics, read, vals, readTimestamp, parents)
		} else {
			c.addMetrics(metrics, vals, mi, readTimestamp)
		}
	}

	return metrics, firstReason, nil
}

// collectionReads chooses the resolved scoped plan or Prometheus entity and parent reads.
func (c *DCGMCollector) collectionReads() ([]devicewatchlistmanager.ReadRequest, error) {
	if c.deviceWatchList.HasOTLPWatches() {
		return c.deviceWatchList.Reads()
	}

	prometheus := c.scrapeReads()
	reads := make([]devicewatchlistmanager.ReadRequest, 0, len(prometheus))
	for _, read := range prometheus {
		reads = append(reads, devicewatchlistmanager.ReadRequest{Entity: read.entity, Fields: read.fields})
	}

	return reads, nil
}

// addScopedMetrics shares parent PCIe identity within one collection and converts the selected metrics.
func (c *DCGMCollector) addScopedMetrics(
	metrics MetricsByCounter,
	read devicewatchlistmanager.ReadRequest,
	values []dcgm.FieldValue_v1,
	readTimestamp int64,
	parents map[uint]*SourceIdentity,
) {
	var source *SourceIdentity
	if c.enableOTLP {
		source = sourceIdentity(read.Entity, values, c.deviceWatchList.DeviceInfo())
		if read.Entity.Entity.EntityGroupId == dcgm.FE_GPU {
			parents[read.Entity.DeviceInfo.GPU] = source
		} else {
			source.preserveParentPCIe(parents[read.Entity.DeviceInfo.GPU])
		}
	}
	selected := countersForFields(c.counters, read.PrometheusFields)
	c.toSelectedMetrics(metrics, values, read.Entity, readTimestamp, source, selected)
}

// countersForFields keeps selected metrics and their configured label counters.
func countersForFields(all []counters.Counter, fields []dcgm.Short) []counters.Counter {
	allowed := make(map[dcgm.Short]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	var selected []counters.Counter
	for _, counter := range all {
		if _, ok := allowed[counter.FieldID]; ok || counter.IsLabel() {
			selected = append(selected, counter)
		}
	}
	return selected
}

// scrapeRead is one entity and the fields one scrape reads on it.
type scrapeRead struct {
	entity devicemonitoring.Info
	fields []dcgm.Short
}

// scrapeReads lists the reads of one scrape: each unselected parent GPU of a
// selected GPU instance with the fields DCGM keeps on physical GPUs, then each
// selected entity with the fields of its scope. Parent GPUs come first so their
// label values are at hand when the instances that inherit them are read.
func (c *DCGMCollector) scrapeReads() []scrapeRead {
	deviceInfo := c.deviceWatchList.DeviceInfo()
	var reads []scrapeRead
	if len(c.parentGPUScrapeFields) > 0 {
		for _, mi := range devicemonitoring.GetParentGPUsForMonitoredGPUInstances(deviceInfo) {
			reads = append(reads, scrapeRead{entity: mi, fields: c.parentGPUScrapeFields})
		}
	}

	var monitoringInfo []devicemonitoring.Info
	if len(c.computeInstanceScrapeFields) > 0 {
		monitoringInfo = devicemonitoring.GetMonitoredEntitiesIncludingComputeInstances(deviceInfo)
	} else {
		monitoringInfo = devicemonitoring.GetMonitoredEntities(deviceInfo)
	}
	for _, mi := range monitoringInfo {
		reads = append(reads, scrapeRead{entity: mi, fields: c.fieldsForScrape(mi)})
	}
	return reads
}

// valuesForRead reads one entity's fields and adds its inherited GPU labels.
func (c *DCGMCollector) valuesForRead(read scrapeRead, gpuReads map[uint][]dcgm.FieldValue_v1) ([]dcgm.FieldValue_v1, error) {
	values, err := c.latestValuesForFields(read.entity, read.fields)
	if err != nil {
		return nil, err
	}

	return c.withInheritedLabels(read, values, gpuReads)
}

// withInheritedLabels adds to the values of a GPU-instance or compute-instance
// read the GPU-level label values of its physical GPU. gpuReads holds the
// values of physical GPUs read so far in this scrape; a physical GPU that was
// not read yet is read for its labels only. Physical GPU reads are recorded and
// returned unchanged, as are reads without fields.
func (c *DCGMCollector) withInheritedLabels(
	read scrapeRead,
	values []dcgm.FieldValue_v1,
	gpuReads map[uint][]dcgm.FieldValue_v1,
) ([]dcgm.FieldValue_v1, error) {
	if len(c.inheritedLabelFields) == 0 || len(read.fields) == 0 {
		return values, nil
	}

	gpu := read.entity.DeviceInfo.GPU
	if read.entity.Entity.EntityGroupId == dcgm.FE_GPU {
		gpuReads[gpu] = values
		return values, nil
	}

	gpuValues, ok := gpuReads[gpu]
	if !ok {
		physicalGPU := devicemonitoring.Info{Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: gpu}}
		var err error
		gpuValues, err = c.latestValuesForFields(physicalGPU, c.inheritedLabelFields)
		if err != nil {
			return nil, err
		}
		gpuReads[gpu] = gpuValues
	}

	inherited := make([]dcgm.FieldValue_v1, 0, len(c.inheritedLabelFields))
	for _, value := range gpuValues {
		if slices.Contains(c.inheritedLabelFields, value.FieldID) {
			inherited = append(inherited, value)
		}
	}
	return append(values, inherited...), nil
}

// nonProfilingMetrics retains the scrape's non-profiling families during a profiling retry.
func nonProfilingMetrics(metrics MetricsByCounter) MetricsByCounter {
	healthy := make(MetricsByCounter)
	for counter, values := range metrics {
		if !counter.IsProfilingMetric() {
			healthy[counter] = values
		}
	}
	return healthy
}

// latestValues reads one monitored entity through the DCGM API appropriate for its type.
func (c *DCGMCollector) latestValues(mi devicemonitoring.Info) ([]dcgm.FieldValue_v1, error) {
	return c.latestValuesForFields(mi, c.fieldsForScrape(mi))
}

// latestValuesForFields reads the supplied fields for one entity in batches that
// DCGM accepts. Callers use it when an entity needs a different field set from
// the collector's default scrape fields.
func (c *DCGMCollector) latestValuesForFields(
	mi devicemonitoring.Info,
	fields []dcgm.Short,
) ([]dcgm.FieldValue_v1, error) {
	values := make([]dcgm.FieldValue_v1, 0, len(fields))
	for start := 0; start < len(fields); start += maxLatestValueFieldBatch {
		end := min(start+maxLatestValueFieldBatch, len(fields))
		batch := fields[start:end]

		var (
			batchValues []dcgm.FieldValue_v1
			err         error
		)
		if mi.Entity.EntityGroupId == dcgm.FE_LINK {
			batchValues, err = c.dcgmClient().LinkGetLatestValues(
				mi.Entity.EntityId,
				mi.ParentType,
				mi.ParentId,
				batch,
			)
		} else {
			batchValues, err = c.dcgmClient().EntityGetLatestValues(
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
				batch,
			)
		}
		if err != nil {
			return nil, fmt.Errorf(
				"get latest values for entity group %d entity %d field batch [%d:%d]: %w",
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
				start,
				end,
				err,
			)
		}
		values = append(values, batchValues...)
	}
	return values, nil
}

// fieldsForScrape resolves the fields to read at the scopes selected for this entity.
func (c *DCGMCollector) fieldsForScrape(mi devicemonitoring.Info) []dcgm.Short {
	if mi.Entity.EntityGroupId == dcgm.FE_GPU_CI {
		return c.computeInstanceScrapeFields
	}
	// A selected whole GPU keeps the legacy full field set. DCGM reports some
	// fields at both GPU and compute-instance scope even when their metadata uses
	// FE_GPU_CI. Parent-only MIG reads bypass this method and remain narrow.
	if mi.Entity.EntityGroupId == dcgm.FE_GPU && c.scrapeFields != nil {
		return c.scrapeFields
	}
	if c.nonComputeInstanceFields != nil {
		return c.nonComputeInstanceFields
	}
	if c.scrapeFields != nil {
		return c.scrapeFields
	}
	return fieldsToScrape(c.deviceWatchList)
}

// addMetrics renders values with the labels and identity fields for their entity type.
func (c *DCGMCollector) addMetrics(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
	readTimestamp int64,
) {
	var source *SourceIdentity
	if c.enableOTLP {
		source = sourceIdentity(mi, values, c.deviceWatchList.DeviceInfo())
	}

	// InstanceInfo is nil for whole-GPU entities.
	switch c.deviceWatchList.DeviceInfo().InfoType() {
	case dcgm.FE_LINK:
		if mi.ParentType == dcgm.FE_SWITCH {
			c.toSwitchMetric(metrics, values, mi, readTimestamp, source)
		} else {
			c.toGPUNvLinkMetric(metrics, values, mi, readTimestamp, source)
		}
	case dcgm.FE_SWITCH:
		c.toSwitchMetric(metrics, values, mi, readTimestamp, source)
	case dcgm.FE_CPU:
		serial := cpuSerialForEntity(c.deviceWatchList.DeviceInfo(), mi.Entity.EntityId)
		c.toCPUMetric(metrics, values, mi, serial, readTimestamp, source)
	case dcgm.FE_CPU_CORE:
		c.toCPUMetric(metrics, values, mi, "", readTimestamp, source)
	default:
		c.toMetric(metrics, values, mi, readTimestamp, source)
	}
}

// cleanupWatches closes and clears the collector-owned watch result.
func (c *DCGMCollector) cleanupWatches() {
	if c.watchResult != nil {
		c.watchResult.Close()
		c.watchResult = nil
	}
}

// rearmProfilingWatch replaces the current watches and refreshes their first samples.
func (c *DCGMCollector) rearmProfilingWatch() error {
	// Tear down first because DCGM watch identity is connection-scoped; cleaning an
	// old watch after replacement can remove the replacement too.
	c.cleanupWatches()

	watchResult, err := c.deviceWatchList.Watch()
	if err != nil {
		return fmt.Errorf("failed to recreate DCGM field watches: %w", err)
	}
	c.watchResult = watchResult

	if err := c.dcgmClient().UpdateAllFields(); err != nil {
		return fmt.Errorf("failed to update fields after recreating DCGM field watches: %w", err)
	}

	return nil
}

// resolveProfilingStaleWindows maps watched profiling fields to twice their watch interval.
func resolveProfilingStaleWindows(
	configuredCounters []counters.Counter,
	watchedFields []dcgm.Short,
	watchGroups []devicewatcher.FieldWatchGroup,
) (map[dcgm.Short]time.Duration, error) {
	// Counter names are authoritative for identifying profiling metrics.
	profilingFields := make(map[dcgm.Short]struct{})
	for _, counter := range configuredCounters {
		if counter.IsProfilingMetric() {
			profilingFields[counter.FieldID] = struct{}{}
		}
	}

	// Limit tracking to fields this entity collector actually watches.
	windows := make(map[dcgm.Short]time.Duration)
	for _, fieldID := range watchedFields {
		if _, ok := profilingFields[fieldID]; ok {
			windows[fieldID] = 0
		}
	}
	if len(windows) == 0 {
		return nil, nil
	}

	if err := setProfilingWindows(windows, watchGroups); err != nil {
		return nil, err
	}
	for fieldID, window := range windows {
		if window == 0 {
			return nil, fmt.Errorf("profiling field %d has no configured watch interval", fieldID)
		}
	}
	return windows, nil
}

// setProfilingWindows validates group intervals and sets each tracked field's freshness window.
func setProfilingWindows(windows map[dcgm.Short]time.Duration, watchGroups []devicewatcher.FieldWatchGroup) error {
	for _, watchGroup := range watchGroups {
		for _, fieldID := range watchGroup.Fields {
			if _, ok := windows[fieldID]; !ok {
				continue
			}
			if watchGroup.IntervalMSec <= 0 {
				return fmt.Errorf("profiling field %d has invalid watch interval %dms", fieldID, watchGroup.IntervalMSec)
			}
			// A slower entity scope must not appear stale because another scope updates faster.
			windows[fieldID] = max(windows[fieldID], 2*time.Duration(watchGroup.IntervalMSec)*time.Millisecond)
		}
	}
	return nil
}

// observeProfilingSamples returns the first status- or timestamp-based repair signal.
func (c *DCGMCollector) observeProfilingSamples(
	entity dcgm.GroupEntityPair,
	values []dcgm.FieldValue_v1,
) *profilingRepairReason {
	if len(c.profilingStaleWindows) == 0 {
		return nil
	}

	now := c.currentTime()
	for _, value := range values {
		staleWindow, ok := c.profilingStaleWindows[value.FieldID]
		if !ok {
			continue
		}

		key := profilingSampleKey{
			entityGroup: entity.EntityGroupId,
			entityID:    entity.EntityId,
			fieldID:     value.FieldID,
		}
		if reason := c.observeProfilingSample(key, value, staleWindow, now); reason != nil {
			return reason
		}
	}

	return nil
}

// observeProfilingSample applies status policy and timestamp freshness to one stream.
func (c *DCGMCollector) observeProfilingSample(
	key profilingSampleKey,
	value dcgm.FieldValue_v1,
	staleWindow time.Duration,
	now time.Time,
) *profilingRepairReason {
	// Explicit watch-lifecycle statuses repair immediately. Other non-OK statuses
	// reset freshness tracking and retain their existing scrape behavior.
	if isRepairableProfilingStatus(value.Status) {
		return &profilingRepairReason{
			key:       key,
			status:    value.Status,
			timestamp: value.TS,
			backoff:   staleWindow,
		}
	}
	if value.Status != dcgm.DCGM_ST_OK {
		delete(c.profilingSamples, key)
		return nil
	}

	// A new or advancing timestamp starts a fresh staleness window.
	sample, found := c.profilingSamples[key]
	if !found || sample.timestamp != value.TS {
		c.recordProfilingBaseline(key, value, staleWindow, now, found)
		return nil
	}

	// An unchanged timestamp becomes stale only after the full field-specific window.
	staleFor := now.Sub(sample.unchangedSince)
	if staleFor < staleWindow {
		return nil
	}

	return &profilingRepairReason{
		key:       key,
		status:    value.Status,
		timestamp: value.TS,
		staleFor:  staleFor,
		backoff:   staleWindow,
	}
}

// recordProfilingBaseline stores a new timestamp and logs the stream's first sample.
func (c *DCGMCollector) recordProfilingBaseline(
	key profilingSampleKey,
	value dcgm.FieldValue_v1,
	staleWindow time.Duration,
	now time.Time,
	found bool,
) {
	if !found {
		slog.Debug(
			"Established DCGM profiling sample baseline",
			slog.String("entityGroup", key.entityGroup.String()),
			slog.Uint64("entityID", uint64(key.entityID)),
			slog.Uint64("fieldID", uint64(key.fieldID)),
			slog.Int("status", value.Status),
			slog.Int64("timestamp", value.TS),
			slog.Duration("sampleAge", now.Sub(time.UnixMicro(value.TS))),
			slog.Duration("staleWindow", staleWindow),
		)
	}
	c.profilingSamples[key] = profilingSample{timestamp: value.TS, unchangedSince: now}
}

// repairReasonForError converts repairable DCGM API errors into a watch repair signal.
func (c *DCGMCollector) repairReasonForError(err error) *profilingRepairReason {
	if len(c.profilingStaleWindows) == 0 {
		return nil
	}

	code, ok := dcgmErrorCode(err)
	if !ok || !isRepairableProfilingStatus(code) {
		return nil
	}

	return &profilingRepairReason{
		status:   code,
		backoff:  c.maxProfilingStaleWindow(),
		apiError: err,
	}
}

// maxProfilingStaleWindow returns the most conservative backoff for an API-level error.
func (c *DCGMCollector) maxProfilingStaleWindow() time.Duration {
	var maxWindow time.Duration
	for _, window := range c.profilingStaleWindows {
		if window > maxWindow {
			maxWindow = window
		}
	}
	return maxWindow
}

// currentTime returns the collector clock, using a test clock when configured.
func (c *DCGMCollector) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// dcgmErrorCode extracts a status code through the shared classifier or typed DCGM error.
func dcgmErrorCode(err error) (int, bool) {
	if diagnostic, ok := dcgmerrors.Classify(err); ok {
		return diagnostic.Code, true
	}

	var dcgmErr *dcgm.Error
	if !errors.As(err, &dcgmErr) {
		return 0, false
	}
	return int(dcgmErr.Code), true
}

// isRepairableProfilingStatus reports whether a status identifies a stale or missing watch.
func isRepairableProfilingStatus(status int) bool {
	return status == dcgm.DCGM_ST_NOT_WATCHED || status == dcgm.DCGM_ST_STALE_DATA
}

// handleScrapeError logs DCGM diagnostics while leaving application shutdown
// policy to the server and command loop that receive the returned scrape error.
func handleScrapeError(err error) {
	if diagnostic, ok := dcgmerrors.Classify(err); ok {
		slog.Error(
			diagnostic.Message,
			slog.String("status", diagnostic.Status),
			slog.String("error", err.Error()),
			slog.String("hint", diagnostic.Hint),
		)
	}
}

// fieldsToScrape returns the unique fields watched by a resolved device watch list.
func fieldsToScrape(deviceWatchList devicewatchlistmanager.WatchList) []dcgm.Short {
	fields := append([]dcgm.Short{}, deviceWatchList.DeviceFields()...)
	fields = append(fields, deviceWatchList.LabelDeviceFields()...)
	return dedupeFieldIDs(fields)
}

// withoutFieldIDs returns fields without the IDs in removed, keeping their
// order. It returns fields itself when nothing is removed.
func withoutFieldIDs(fields, removed []dcgm.Short) []dcgm.Short {
	if len(removed) == 0 {
		return fields
	}

	kept := make([]dcgm.Short, 0, len(fields))
	for _, field := range fields {
		if !slices.Contains(removed, field) {
			kept = append(kept, field)
		}
	}
	return kept
}

// dedupeFieldIDs preserves the first occurrence of each DCGM field ID.
func dedupeFieldIDs(fields []dcgm.Short) []dcgm.Short {
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

// findCounterField returns the counter for fieldID or an error when none is configured.
func findCounterField(c []counters.Counter, fieldID dcgm.Short) (counters.Counter, error) {
	for i := 0; i < len(c); i++ {
		if c[i].FieldID == fieldID {
			return c[i], nil
		}
	}

	return counters.Counter{}, fmt.Errorf("could not find counter corresponding to field ID '%d'", fieldID)
}

// cpuSerialForEntity returns the serial recorded for the given CPU entity, or
// "" when the entity is unknown or has no serial. Only FE_CPU metrics resolve a
// serial; FE_CPU_CORE metrics intentionally pass "".
func cpuSerialForEntity(deviceInfo deviceinfo.Provider, entityID uint) string {
	for _, cpu := range deviceInfo.CPUs() {
		if cpu.EntityId == entityID {
			return cpu.Serial
		}
	}

	return ""
}

// metricTimestamp preserves native sample times and substitutes only missing ones.
func metricTimestamp(sourceTimestamp, readTimestamp int64) int64 {
	if sourceTimestamp != 0 {
		return sourceTimestamp
	}
	return readTimestamp
}

// toSwitchMetric translates switch and switch NVLink samples using the collector's label settings.
func (c *DCGMCollector) toSwitchMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
	readTimestamp int64,
	source *SourceIdentity,
) {
	labels := labelsFromValues(values, c.counters, c.hostname)
	nvSwitchID := mi.Entity.EntityId
	nvLinkID := ""
	if mi.Entity.EntityGroupId == dcgm.FE_LINK {
		nvSwitchID = mi.ParentId
		nvLinkID = fmt.Sprintf("%d", mi.Entity.EntityId)
	}

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c.counters, val.FieldID)
		if err != nil {
			continue
		}
		if v == skipDCGMValue {
			logFieldValueSkipped(
				val,
				counter.FieldName,
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
			)
			continue
		}

		if counter.IsLabel() {
			continue
		}
		uuid := "UUID"
		if c.useOldNamespace {
			uuid = "uuid"
		}
		m := Metric{
			Source:     source,
			Counter:    counter,
			Value:      v,
			Timestamp:  metricTimestamp(val.TS, readTimestamp),
			UUID:       uuid,
			NvLink:     nvLinkID,
			NvSwitch:   fmt.Sprintf("nvswitch%d", nvSwitchID),
			Hostname:   c.hostname,
			Labels:     labels,
			Attributes: nil,
			ParentType: mi.ParentType,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

// toCPUMetric translates one CPU or CPU-core entity sample into metrics with
// explicit CPU and core identities for the renderer.
func (c *DCGMCollector) toCPUMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
	cpuSerial string,
	readTimestamp int64,
	source *SourceIdentity,
) {
	labels := labelsFromValues(values, c.counters, c.hostname)
	cpu := fmt.Sprintf("%d", mi.Entity.EntityId)
	cpuCore := ""
	if mi.Entity.EntityGroupId == dcgm.FE_CPU_CORE {
		cpu = fmt.Sprintf("%d", mi.ParentId)
		cpuCore = fmt.Sprintf("%d", mi.Entity.EntityId)
	}

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c.counters, val.FieldID)
		if err != nil {
			continue
		}
		if v == skipDCGMValue {
			logFieldValueSkipped(
				val,
				counter.FieldName,
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
			)
			continue
		}

		if counter.IsLabel() {
			continue
		}
		uuid := "UUID"
		if c.useOldNamespace {
			uuid = "uuid"
		}
		m := Metric{
			Source:       source,
			Counter:      counter,
			Value:        v,
			Timestamp:    metricTimestamp(val.TS, readTimestamp),
			UUID:         uuid,
			CPU:          cpu,
			CPUCore:      cpuCore,
			GPUUUID:      "",
			GPUDevice:    fmt.Sprintf("%d", mi.ParentId),
			GPUModelName: "",
			GPUPCIBusID:  "",
			Hostname:     c.hostname,
			CPUSerial:    cpuSerial,
			Labels:       labels,
			Attributes:   nil,
			ParentType:   mi.ParentType,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

// toGPUNvLinkMetric adds one metric per non-label value for an NVLink link
// that belongs to a GPU. The model name follows the replace-blanks setting.
func (c *DCGMCollector) toGPUNvLinkMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
	readTimestamp int64,
	source *SourceIdentity,
) {
	labels := labelsFromValues(values, c.counters, c.hostname)

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c.counters, val.FieldID)
		if err != nil {
			continue
		}
		if v == skipDCGMValue {
			logFieldValueSkipped(
				val,
				counter.FieldName,
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
			)
			continue
		}

		if counter.IsLabel() {
			continue
		}
		uuid := "UUID"
		attrs := map[string]string{}

		m := Metric{
			Source:       source,
			Counter:      counter,
			Value:        v,
			Timestamp:    metricTimestamp(val.TS, readTimestamp),
			UUID:         uuid,
			GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
			GPUUUID:      mi.DeviceInfo.UUID,
			NvLink:       fmt.Sprintf("%d", mi.Entity.EntityId),
			GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
			GPUModelName: getGPUModel(mi.DeviceInfo, c.replaceBlanksInModelName),
			GPUPCIBusID:  mi.DeviceInfo.PCI.BusID,
			Hostname:     c.hostname,
			Labels:       labels,
			Attributes:   attrs,
			ParentType:   mi.ParentType,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

// toMetric converts one supported entity sample into metrics for its configured counters.
func (c *DCGMCollector) toMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
	readTimestamp int64,
	source *SourceIdentity,
) {
	c.toSelectedMetrics(metrics, values, mi, readTimestamp, source, c.counters)
}

// toSelectedMetrics applies Prometheus value and label rules to the supplied counter selection.
func (c *DCGMCollector) toSelectedMetrics(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
	readTimestamp int64,
	source *SourceIdentity,
	selected []counters.Counter,
) {
	labels := labelsFromValues(values, selected, c.hostname)
	uuid := "UUID"
	if c.useOldNamespace {
		uuid = "uuid"
	}
	gpu := strconv.FormatUint(uint64(mi.DeviceInfo.GPU), 10)
	gpuDevice := "nvidia" + gpu
	gpuModel := getGPUModel(mi.DeviceInfo, c.replaceBlanksInModelName)
	migProfile := ""
	gpuInstanceID := ""
	computeInstanceID := ""
	if mi.InstanceInfo != nil {
		migProfile = mi.InstanceInfo.ProfileName
		gpuInstanceID = strconv.FormatUint(uint64(mi.InstanceInfo.Info.NvmlInstanceId), 10)
	}
	if mi.ComputeInstanceInfo != nil {
		computeInstanceID = strconv.FormatUint(uint64(mi.ComputeInstanceInfo.InstanceInfo.NvmlComputeInstanceId), 10)
	}

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == skipDCGMValue {
			logFieldValueSkipped(
				val,
				counterFieldNameOrUnknown(selected, val.FieldID),
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
			)
			continue
		}
		counter, err := findCounterField(selected, val.FieldID)
		if err != nil {
			continue
		}

		if counter.IsLabel() {
			continue
		}
		attrs := map[string]string{}
		if counter.FieldID == dcgm.DCGM_FI_DEV_XID_ERRORS {
			errCode := int(val.Int64())
			attrs["err_code"] = strconv.Itoa(errCode)
			attrs["err_msg"] = xidErrorMessage(errCode)
		}

		m := Metric{
			Source:    source,
			Counter:   counter,
			Value:     v,
			Timestamp: metricTimestamp(val.TS, readTimestamp),

			UUID:         uuid,
			GPU:          gpu,
			GPUUUID:      mi.DeviceInfo.UUID,
			GPUDevice:    gpuDevice,
			GPUModelName: gpuModel,
			GPUPCIBusID:  mi.DeviceInfo.PCI.BusID,
			Hostname:     c.hostname,

			Labels:               labels,
			Attributes:           attrs,
			ParentType:           mi.ParentType,
			MigProfile:           migProfile,
			GPUInstanceID:        gpuInstanceID,
			GPUComputeInstanceID: computeInstanceID,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

// xidErrorMessage returns the description of a known XID, or the unknown-error text.
func xidErrorMessage(errCode int) string {
	if 0 <= errCode && errCode < len(xidErrCodeToText) {
		return xidErrCodeToText[errCode]
	}
	return unknownErr
}

// labelsFromValues builds labels from the label counters present in values,
// skipping failed or blank samples. A "hostname" label counter is dropped when
// hostname is empty.
func labelsFromValues(values []dcgm.FieldValue_v1, c []counters.Counter, hostname string) map[string]string {
	labels := map[string]string{}
	for _, val := range values {
		counter, err := findCounterField(c, val.FieldID)
		if err != nil || !counter.IsLabel() {
			continue
		}

		v := toString(val)
		if v == skipDCGMValue {
			continue
		}
		addMetricLabel(labels, counter, v, hostname)
	}

	return labels
}

// getGPUModel returns the model, optionally collapsing whitespace and replacing spaces with hyphens.
func getGPUModel(d dcgm.Device, replaceBlanksInModelName bool) string {
	gpuModel := d.Identifiers.Model

	if replaceBlanksInModelName {
		parts := strings.Fields(gpuModel)
		gpuModel = strings.Join(parts, " ")
		gpuModel = strings.ReplaceAll(gpuModel, " ", "-")
	}
	return gpuModel
}

// addMetricLabel stores value under the counter's field name, except that a
// "hostname" label is dropped when hostname is empty.
func addMetricLabel(labels map[string]string, counter counters.Counter, value string, hostname string) {
	if hostname == "" && strings.EqualFold(counter.FieldName, "hostname") {
		return
	}

	labels[counter.FieldName] = value
}

// toString formats an OK DCGM value by its field type. It returns skipDCGMValue
// for failed, blank, or unsupported values.
func toString(value dcgm.FieldValue_v1) string {
	if value.Status != dcgm.DCGM_ST_OK {
		return skipDCGMValue
	}

	switch value.FieldType {
	case dcgm.DCGM_FT_INT64:
		v := value.Int64()
		if isInt64Blank(v) {
			return skipDCGMValue
		}
		return strconv.FormatInt(v, 10)
	case dcgm.DCGM_FT_DOUBLE:
		v := value.Float64()
		if isFloat64Blank(v) {
			return skipDCGMValue
		}
		return strconv.FormatFloat(v, 'f', 6, 64)
	case dcgm.DCGM_FT_STRING:
		v := value.String()
		if isStringBlank(v) {
			return skipDCGMValue
		}
		return v
	}

	return skipDCGMValue
}
