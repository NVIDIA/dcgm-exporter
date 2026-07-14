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

// DCGMCollector owns the watched DCGM fields for one entity type and converts
// their latest values into exporter metrics during each Prometheus collection.
type DCGMCollector struct {
	mu                       sync.Mutex
	counters                 []counters.Counter
	cleanups                 []func()
	useOldNamespace          bool
	deviceWatchList          devicewatchlistmanager.WatchList
	hostname                 string
	replaceBlanksInModelName bool

	profilingStaleWindows map[dcgm.Short]time.Duration
	profilingSamples      map[profilingSampleKey]profilingSample
	nextProfilingRepair   time.Time
	now                   func() time.Time
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

func NewDCGMCollector(
	c []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (*DCGMCollector, error) {
	if deviceWatchList.IsEmpty() {
		return nil, errors.New("deviceWatchList is empty")
	}

	collector := &DCGMCollector{
		counters:        c,
		deviceWatchList: deviceWatchList,
		hostname:        hostname,
	}

	if config == nil {
		slog.Warn("Config is empty")
		return collector, nil
	}

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

	cleanups, err := collector.deviceWatchList.Watch()
	if err != nil {
		runCleanups(cleanups)
		return nil, err
	}

	collector.cleanups = cleanups

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
		return fallback, nil
	}

	// The retry records fresh status/timestamp state but cannot trigger another repair.
	metrics, retryReason, err := c.getMetricsOnce()
	if err != nil {
		slog.Error("Failed to collect metrics after repairing DCGM profiling watch", slog.String("error", err.Error()))
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
	monitoringInfo := devicemonitoring.GetMonitoredEntities(c.deviceWatchList.DeviceInfo())
	metrics := make(MetricsByCounter)
	var firstReason *profilingRepairReason

	for _, mi := range monitoringInfo {
		vals, err := c.latestValues(mi)
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

		c.addMetrics(metrics, vals, mi)
	}

	return metrics, firstReason, nil
}

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
	fields := fieldsToScrape(c.deviceWatchList)

	if mi.Entity.EntityGroupId == dcgm.FE_LINK {
		return dcgmprovider.Client().LinkGetLatestValues(
			mi.Entity.EntityId,
			mi.ParentType,
			mi.ParentId,
			fields,
		)
	}

	return dcgmprovider.Client().EntityGetLatestValues(
		mi.Entity.EntityGroupId,
		mi.Entity.EntityId,
		fields,
	)
}

// addMetrics renders values with the labels and identity fields for their entity type.
func (c *DCGMCollector) addMetrics(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
) {
	// InstanceInfo is nil for whole-GPU entities.
	switch c.deviceWatchList.DeviceInfo().InfoType() {
	case dcgm.FE_LINK:
		if mi.ParentType == dcgm.FE_SWITCH {
			toSwitchMetric(metrics, values, c.counters, mi, c.useOldNamespace, c.hostname)
		} else {
			toGPUNvLinkMetric(metrics, values, c.counters, mi, c.hostname)
		}
	case dcgm.FE_SWITCH:
		toSwitchMetric(metrics, values, c.counters, mi, c.useOldNamespace, c.hostname)
	case dcgm.FE_CPU:
		serial := cpuSerialForEntity(c.deviceWatchList.DeviceInfo(), mi.Entity.EntityId)
		toCPUMetric(metrics, values, c.counters, mi, c.useOldNamespace, c.hostname, serial)
	case dcgm.FE_CPU_CORE:
		toCPUMetric(metrics, values, c.counters, mi, c.useOldNamespace, c.hostname, "")
	default:
		toMetric(metrics,
			values,
			c.counters,
			mi,
			c.useOldNamespace,
			c.hostname,
			c.replaceBlanksInModelName)
	}
}

// cleanupWatches runs and clears the collector-owned watch cleanup callbacks.
func (c *DCGMCollector) cleanupWatches() {
	cleanups := c.cleanups
	c.cleanups = nil
	runCleanups(cleanups)
}

// runCleanups invokes the cleanup callbacks returned by a watch operation.
func runCleanups(cleanups []func()) {
	for _, cleanup := range cleanups {
		cleanup()
	}
}

// rearmProfilingWatch replaces the current watches and refreshes their first samples.
func (c *DCGMCollector) rearmProfilingWatch() error {
	// Tear down first because DCGM watch identity is connection-scoped; cleaning an
	// old watch after replacement can remove the replacement too.
	c.cleanupWatches()

	cleanups, err := c.deviceWatchList.Watch()
	if err != nil {
		runCleanups(cleanups)
		return fmt.Errorf("failed to recreate DCGM field watches: %w", err)
	}
	c.cleanups = cleanups

	if err := dcgmprovider.Client().UpdateAllFields(); err != nil {
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

	// Each profiling field inherits the interval of its resolved watch group.
	for _, watchGroup := range watchGroups {
		for _, fieldID := range watchGroup.Fields {
			if _, ok := windows[fieldID]; !ok {
				continue
			}
			if watchGroup.IntervalMSec <= 0 {
				return nil, fmt.Errorf("profiling field %d has invalid watch interval %dms", fieldID, watchGroup.IntervalMSec)
			}
			windows[fieldID] = 2 * time.Duration(watchGroup.IntervalMSec) * time.Millisecond
		}
	}

	// A watched profiling field without an interval cannot be checked safely.
	for fieldID, window := range windows {
		if window == 0 {
			return nil, fmt.Errorf("profiling field %d has no configured watch interval", fieldID)
		}
	}

	return windows, nil
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

// handleScrapeError logs actionable DCGM diagnostics and preserves fatal exit behavior.
func handleScrapeError(err error) {
	if diagnostic, ok := dcgmerrors.Classify(err); ok {
		slog.Error(
			diagnostic.Message,
			slog.String("status", diagnostic.Status),
			slog.String("error", err.Error()),
			slog.String("hint", diagnostic.Hint),
		)
		if isFatalScrapeDCGMStatus(diagnostic.Code) {
			os.Exit(1)
		}
	}
}

func fieldsToScrape(deviceWatchList devicewatchlistmanager.WatchList) []dcgm.Short {
	fields := append([]dcgm.Short{}, deviceWatchList.DeviceFields()...)
	fields = append(fields, deviceWatchList.LabelDeviceFields()...)
	return dedupeFieldIDs(fields)
}

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

func isFatalScrapeDCGMStatus(code int) bool {
	switch code {
	case dcgm.DCGM_ST_CONNECTION_NOT_VALID,
		dcgm.DCGM_ST_UNINITIALIZED,
		dcgm.DCGM_ST_LIBRARY_NOT_FOUND,
		dcgm.DCGM_ST_INIT_ERROR,
		dcgm.DCGM_ST_NVML_NOT_LOADED:
		return true
	default:
		return false
	}
}

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

func toSwitchMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1, c []counters.Counter, mi devicemonitoring.Info, useOld bool, hostname string,
) {
	labels := labelsFromValues(values, c, hostname)

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c, val.FieldID)
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
		if useOld {
			uuid = "uuid"
		}
		m := Metric{
			Counter:    counter,
			Value:      v,
			UUID:       uuid,
			NvLink:     fmt.Sprintf("%d", mi.Entity.EntityId),
			NvSwitch:   fmt.Sprintf("nvswitch%d", mi.ParentId),
			Hostname:   hostname,
			Labels:     labels,
			Attributes: nil,
			ParentType: mi.ParentType,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func toCPUMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1, c []counters.Counter, mi devicemonitoring.Info, useOld bool, hostname string, cpuSerial string,
) {
	labels := labelsFromValues(values, c, hostname)

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c, val.FieldID)
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
		if useOld {
			uuid = "uuid"
		}
		m := Metric{
			Counter:      counter,
			Value:        v,
			UUID:         uuid,
			GPU:          fmt.Sprintf("%d", mi.Entity.EntityId),
			GPUUUID:      "",
			GPUDevice:    fmt.Sprintf("%d", mi.ParentId),
			GPUModelName: "",
			GPUPCIBusID:  "",
			Hostname:     hostname,
			CPUSerial:    cpuSerial,
			Labels:       labels,
			Attributes:   nil,
			ParentType:   mi.ParentType,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func toGPUNvLinkMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	c []counters.Counter,
	mi devicemonitoring.Info,
	hostname string,
) {
	labels := labelsFromValues(values, c, hostname)

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity

		counter, err := findCounterField(c, val.FieldID)
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
			Counter:      counter,
			Value:        v,
			UUID:         uuid,
			GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
			GPUUUID:      mi.DeviceInfo.UUID,
			NvLink:       fmt.Sprintf("%d", mi.Entity.EntityId),
			GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
			GPUModelName: getGPUModel(mi.DeviceInfo, false),
			GPUPCIBusID:  mi.DeviceInfo.PCI.BusID,
			Hostname:     hostname,
			Labels:       labels,
			Attributes:   attrs,
			ParentType:   mi.ParentType,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

func toMetric(
	metrics MetricsByCounter,
	values []dcgm.FieldValue_v1,
	c []counters.Counter,
	mi devicemonitoring.Info,
	useOld bool,
	hostname string,
	replaceBlanksInModelName bool,
) {
	labels := labelsFromValues(values, c, hostname)

	for _, val := range values {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == skipDCGMValue {
			logFieldValueSkipped(
				val,
				counterFieldNameOrUnknown(c, val.FieldID),
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
			)
			continue
		}
		counter, err := findCounterField(c, val.FieldID)
		if err != nil {
			continue
		}

		if counter.IsLabel() {
			continue
		}
		uuid := "UUID"
		if useOld {
			uuid = "uuid"
		}

		gpuModel := getGPUModel(mi.DeviceInfo, replaceBlanksInModelName)

		attrs := map[string]string{}
		if counter.FieldID == dcgm.DCGM_FI_DEV_XID_ERRORS {
			errCode := int(val.Int64())
			attrs["err_code"] = strconv.Itoa(errCode)
			if 0 <= errCode && errCode < len(xidErrCodeToText) {
				attrs["err_msg"] = xidErrCodeToText[errCode]
			} else {
				attrs["err_msg"] = unknownErr
			}
		}

		m := Metric{
			Counter: counter,
			Value:   v,

			UUID:         uuid,
			GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
			GPUUUID:      mi.DeviceInfo.UUID,
			GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
			GPUModelName: gpuModel,
			GPUPCIBusID:  mi.DeviceInfo.PCI.BusID,
			Hostname:     hostname,

			Labels:     labels,
			Attributes: attrs,
			ParentType: mi.ParentType,
		}
		if mi.InstanceInfo != nil {
			m.MigProfile = mi.InstanceInfo.ProfileName
			m.GPUInstanceID = fmt.Sprintf("%d", mi.InstanceInfo.Info.NvmlInstanceId)
		} else {
			m.MigProfile = ""
			m.GPUInstanceID = ""
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}
}

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

func getGPUModel(d dcgm.Device, replaceBlanksInModelName bool) string {
	gpuModel := d.Identifiers.Model

	if replaceBlanksInModelName {
		parts := strings.Fields(gpuModel)
		gpuModel = strings.Join(parts, " ")
		gpuModel = strings.ReplaceAll(gpuModel, " ", "-")
	}
	return gpuModel
}

func addMetricLabel(labels map[string]string, counter counters.Counter, value string, hostname string) {
	if hostname == "" && strings.EqualFold(counter.FieldName, "hostname") {
		return
	}

	labels[counter.FieldName] = value
}

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
		return fmt.Sprintf("%d", v)
	case dcgm.DCGM_FT_DOUBLE:
		v := value.Float64()
		if isFloat64Blank(v) {
			return skipDCGMValue
		}
		return fmt.Sprintf("%f", v)
	case dcgm.DCGM_FT_STRING:
		v := value.String()
		if isStringBlank(v) {
			return skipDCGMValue
		}
		return v
	}

	return skipDCGMValue
}
