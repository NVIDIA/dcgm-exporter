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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

type clockEventsTotalKey struct {
	entity dcgm.GroupEntityPair
	reason clockEventBitmask
}

type clockEventsTotalCollector struct {
	expCollector
	poller cumulativeCollectorPoller

	// initialSince is the first GetValuesSince cursor; later polls use DCGM's returned cursor.
	initialSince time.Time

	// collectMu serializes concurrent collectNewEvents calls so cursor and total
	// updates cannot overlap.
	collectMu sync.Mutex
	stateMu   sync.RWMutex
	cursors   map[dcgm.GroupHandle]time.Time
	totals    map[clockEventsTotalKey]int
	previous  map[dcgm.GroupEntityPair]clockEventBitmask
}

func (c *clockEventsTotalCollector) GetMetrics() (MetricsByCounter, error) {
	totals := c.snapshotTotals()
	metrics := make(MetricsByCounter)
	monitoringInfo := devicemonitoring.GetMonitoredEntities(c.deviceWatchList.DeviceInfo())

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
			metricValueLabels["clock_event"] = key.reason.String()
			metrics[c.counter] = append(metrics[c.counter], c.createMetric(metricValueLabels, mi, uuid, total))
		}
	}

	return metrics, nil
}

func (c *clockEventsTotalCollector) collectNewEvents() error {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	if err := dcgmprovider.Client().UpdateAllFields(); err != nil {
		return fmt.Errorf("update fields for clock events total collector: %w", err)
	}

	fieldGroup := c.deviceWatchList.DeviceFieldGroup()
	for _, group := range c.deviceWatchList.DeviceGroups() {
		since := c.cursorForGroup(group)
		values, nextSince, err := dcgmprovider.Client().GetValuesSince(
			group,
			fieldGroup,
			since,
		)
		if err != nil {
			return newCumulativePollContextError("get clock event values since cursor", group, fieldGroup, since, err)
		}

		c.accumulateGroupEvents(group, values, nextSince)
	}

	return nil
}

func (c *clockEventsTotalCollector) accumulateGroupEvents(
	group dcgm.GroupHandle,
	values []dcgm.FieldValue_v2,
	nextSince time.Time,
) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

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
	c.cursors[group] = nextSince
}

func (c *clockEventsTotalCollector) snapshotTotals() map[clockEventsTotalKey]int {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	return maps.Clone(c.totals)
}

func (c *clockEventsTotalCollector) cursorForGroup(group dcgm.GroupHandle) time.Time {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	if cursor, exists := c.cursors[group]; exists {
		return cursor
	}
	return c.initialSince
}

func (c *clockEventsTotalCollector) Cleanup() {
	c.poller.Cleanup()
}

func NewClockEventsTotalCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpClockEventsTotalEnabled(counterList) {
		slog.Error(counters.DCGMExpClockEventsTotal + " collector is disabled")
		return nil, fmt.Errorf("%s collector is disabled", counters.DCGMExpClockEventsTotal)
	}

	initialSince := time.Now()
	deviceWatchList.SetDeviceFieldsWithoutLabelWatches([]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})
	expCollector, err := newExpCollector(
		counterList.LabelCounters(),
		hostname,
		config,
		deviceWatchList,
	)
	if err != nil {
		return nil, fmt.Errorf("create clock events total collector watch: %w", err)
	}

	collector := &clockEventsTotalCollector{
		expCollector: expCollector,
		initialSince: initialSince,
		cursors:      map[dcgm.GroupHandle]time.Time{},
		totals:       map[clockEventsTotalKey]int{},
		previous:     map[dcgm.GroupEntityPair]clockEventBitmask{},
	}
	collector.poller = newCumulativeCollectorPoller(
		counters.DCGMExpClockEventsTotal,
		cumulativeCollectorPollInterval(counters.DCGMExpClockEventsTotal, config.CollectInterval),
		collector.collectNewEvents,
		collector.expCollector.Cleanup,
	)
	collector.sourceFields = map[dcgm.Short]string{
		dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
	}
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
