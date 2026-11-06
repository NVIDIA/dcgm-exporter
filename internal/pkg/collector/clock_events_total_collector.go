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

package collector

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// clockEventsTotalKey identifies one clock-reason total for a DCGM entity.
type clockEventsTotalKey struct {
	entity dcgm.GroupEntityPair
	reason clockEventBitmask
}

// clockEventsTotalCollector counts each reason bit that turns on between two
// consecutive DCGM samples of an entity. An entity's first sample only sets the
// baseline.
type clockEventsTotalCollector struct {
	expCollector
	poller cumulativeCollectorPoller

	// initialSince is the first GetValuesSince cursor; later polls use DCGM's returned cursor.
	initialSince time.Time

	// collectMu serializes concurrent collectNewEvents calls so cursor and total
	// updates cannot overlap.
	collectMu sync.Mutex
	stateMu   sync.RWMutex
	cursors   map[cumulativeWatchCursorKey]time.Time
	totals    map[clockEventsTotalKey]int
	previous  map[dcgm.GroupEntityPair]clockEventBitmask
}

// GetMetrics renders the accumulated reason totals for currently monitored entities.
func (c *clockEventsTotalCollector) GetMetrics() (MetricsByCounter, error) {
	totals := c.snapshotTotals()
	observed := c.currentTime().UnixMicro()
	metrics := make(MetricsByCounter)
	monitoringInfo := c.deviceWatchList.MonitoredEntities()

	uuid := "UUID"
	if c.config.UseOldNamespace {
		uuid = "uuid"
	}

	for _, mi := range monitoringInfo {
		labels := map[string]string{}
		if len(c.labelsCounters) > 0 && len(c.deviceWatchList.LabelDeviceFields()) > 0 {
			if err := c.getLabelsFromCounters(mi, labels); err != nil {
				return nil, fmt.Errorf("get labels for clock events total metric: %w", err)
			}
		}

		for key, total := range totals {
			if key.entity != mi.Entity {
				continue
			}

			metricValueLabels := maps.Clone(labels)
			metricValueLabels[ClockEventLabel] = key.reason.String()
			metric := c.createMetric(metricValueLabels, mi, uuid, total, c.deviceWatchList.DeviceInfo(), observed)
			metric.StartTimestamp = c.initialSince.UnixMicro()
			metrics[c.counter] = append(metrics[c.counter], metric)
		}
	}

	return metrics, nil
}

// collectNewEvents reads each watched field group and commits its results only after all reads succeed.
func (c *clockEventsTotalCollector) collectNewEvents() error {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	if err := c.dcgmClient().UpdateAllFields(); err != nil {
		return fmt.Errorf("update fields for clock events total collector: %w", err)
	}

	polls := make([]cumulativeWatchPoll, 0)
	for _, binding := range c.watchBindings() {
		since := cumulativeWatchCursor(&c.stateMu, c.cursors, c.initialSince, binding.Group, binding.FieldGroup)
		values, nextSince, err := c.dcgmClient().GetValuesSince(
			binding.Group,
			binding.FieldGroup,
			since,
		)
		if err != nil {
			return newCumulativePollContextError(
				"get clock event values since cursor", binding.Group, binding.FieldGroup, since, err,
			)
		}

		polls = append(polls, cumulativeWatchPoll{
			group:      binding.Group,
			fieldGroup: binding.FieldGroup,
			values:     values,
			nextSince:  nextSince,
		})
	}

	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	for _, poll := range polls {
		c.accumulateGroupEvents(poll.group, poll.fieldGroup, poll.values, poll.nextSince)
	}

	return nil
}

// accumulateGroupEvents updates reason transitions and the cursor for one successful watch read.
func (c *clockEventsTotalCollector) accumulateGroupEvents(
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
	values []dcgm.FieldValue_v2,
	nextSince time.Time,
) {
	for _, val := range values {
		if val.Status != 0 || isBlankValue(val) {
			continue
		}

		entity := dcgm.GroupEntityPair{EntityGroupId: val.EntityGroupId, EntityId: val.EntityID}
		current := clockEventBitmask(val.Int64())
		if _, exists := c.previous[entity]; !exists {
			// Prime the baseline without counting; a reason already active at startup
			// is not a new transition observed by this collector.
			c.previous[entity] = current
			continue
		}

		newlyActive := current &^ c.previous[entity]
		for _, reason := range parseClockEventReasons(int64(newlyActive)) {
			c.totals[clockEventsTotalKey{entity: entity, reason: reason}]++
		}
		c.previous[entity] = current
	}
	c.cursors[newCumulativeWatchCursorKey(group, fieldGroup)] = nextSince
}

// snapshotTotals returns an independent copy so metric rendering can proceed without the state lock.
func (c *clockEventsTotalCollector) snapshotTotals() map[clockEventsTotalKey]int {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	return maps.Clone(c.totals)
}

// Cleanup stops the poller, waiting at most a bounded time, so it can return
// while a DCGM call is still running. Wait for CleanupComplete before tearing
// down DCGM.
func (c *clockEventsTotalCollector) Cleanup() {
	c.poller.Cleanup()
}

// CleanupComplete reports when the poller has stopped and released this
// collector's DCGM watches. Registry retirement waits for it before provider
// teardown, even when Cleanup returned after its bounded stop request.
func (c *clockEventsTotalCollector) CleanupComplete() <-chan struct{} {
	return c.poller.CleanupComplete()
}

// NewClockEventsTotalCollector creates the cumulative clock-event collector
// with the current process client for direct callers.
func NewClockEventsTotalCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	return newClockEventsTotalCollectorWithClient(dcgmprovider.Client(), counterList, hostname, config, deviceWatchList)
}

// newClockEventsTotalCollectorWithClient creates the cumulative clock-event
// collector with client, which remains valid until poller cleanup completes.
func newClockEventsTotalCollectorWithClient(
	client dcgmprovider.DCGM,
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpClockEventsTotalEnabled(counterList) {
		slog.Error(counters.DCGMExpClockEventsTotal + " collector is disabled")
		return nil, fmt.Errorf("%s collector is disabled", counters.DCGMExpClockEventsTotal)
	}

	source, err := countSource(counters.DCGMExpClockEventsCount)
	if err != nil {
		return nil, err
	}

	collector := &clockEventsTotalCollector{
		cursors:  map[cumulativeWatchCursorKey]time.Time{},
		totals:   map[clockEventsTotalKey]int{},
		previous: map[dcgm.GroupEntityPair]clockEventBitmask{},
	}
	collector.initialSince = collector.currentTime()
	// DCGM declares the clock event reasons field at GPU level, so it is
	// watched and reported on physical GPUs, never on MIG instances.
	deviceWatchList.SetDeviceFieldsForPhysicalGPUs([]dcgm.Short{source.FieldID})
	expCollector, err := newExpCollectorWithClient(
		client,
		counterList.LabelCounters(),
		hostname,
		config,
		deviceWatchList,
	)
	if err != nil {
		return nil, fmt.Errorf("create clock events total collector watch: %w", err)
	}

	collector.expCollector = expCollector

	collector.poller = newCumulativeCollectorPoller(
		counters.DCGMExpClockEventsTotal,
		cumulativeCollectorPollInterval(counters.DCGMExpClockEventsTotal, config.CollectInterval),
		collector.collectNewEvents,
		collector.expCollector.Cleanup,
	)
	collector.sourceFields = map[dcgm.Short]string{source.FieldID: source.Name}
	collector.counter = counterList[slices.IndexFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpClockEventsTotal
	})]
	collector.poller.start()

	return collector, nil
}

// IsDCGMExpClockEventsTotalEnabled checks if the DCGM_EXP_CLOCK_EVENTS_TOTAL counter exists.
func IsDCGMExpClockEventsTotalEnabled(counterList counters.CounterList) bool {
	return slices.ContainsFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpClockEventsTotal
	})
}
