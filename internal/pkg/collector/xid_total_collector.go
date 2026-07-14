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

type xidTotalKey struct {
	entity dcgm.GroupEntityPair
	xid    int64
}

type xidTotalCollector struct {
	expCollector
	poller cumulativeCollectorPoller

	// initialSince is the first GetValuesSince cursor; later polls use DCGM's returned cursor.
	initialSince time.Time

	// collectMu serializes concurrent collectNewEvents calls so cursor and total
	// updates cannot overlap.
	collectMu sync.Mutex
	stateMu   sync.RWMutex
	cursors   map[dcgm.GroupHandle]time.Time
	totals    map[xidTotalKey]int
}

func (c *xidTotalCollector) GetMetrics() (MetricsByCounter, error) {
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
				return nil, fmt.Errorf("get labels for xid total metric: %w", err)
			}
		}

		for key, total := range totals {
			if key.entity != mi.Entity {
				continue
			}

			metricValueLabels := maps.Clone(labels)
			metricValueLabels["xid"] = fmt.Sprint(key.xid)
			metrics[c.counter] = append(metrics[c.counter], c.createMetric(metricValueLabels, mi, uuid, total))
		}
	}

	return metrics, nil
}

func (c *xidTotalCollector) collectNewEvents() error {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	if err := dcgmprovider.Client().UpdateAllFields(); err != nil {
		return fmt.Errorf("update fields for xid total collector: %w", err)
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
			return newCumulativePollContextError("get xid values since cursor", group, fieldGroup, since, err)
		}

		c.accumulateGroupEvents(group, values, nextSince)
	}

	return nil
}

func (c *xidTotalCollector) accumulateGroupEvents(
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

		xid := val.Int64()
		if xid == 0 {
			continue
		}

		entity := dcgm.GroupEntityPair{EntityGroupId: val.EntityGroupId, EntityId: val.EntityID}
		c.totals[xidTotalKey{entity: entity, xid: xid}]++
	}
	c.cursors[group] = nextSince
}

func (c *xidTotalCollector) snapshotTotals() map[xidTotalKey]int {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	return maps.Clone(c.totals)
}

func (c *xidTotalCollector) cursorForGroup(group dcgm.GroupHandle) time.Time {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	if cursor, exists := c.cursors[group]; exists {
		return cursor
	}
	return c.initialSince
}

func (c *xidTotalCollector) Cleanup() {
	c.poller.Cleanup()
}

func NewXIDTotalCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpXIDErrorsTotalEnabled(counterList) {
		slog.Error(counters.DCGMExpXIDErrorsTotal + " collector is disabled")
		return nil, fmt.Errorf("%s collector is disabled", counters.DCGMExpXIDErrorsTotal)
	}

	initialSince := time.Now()
	deviceWatchList.SetDeviceFieldsWithoutLabelWatches([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})
	expCollector, err := newExpCollector(
		counterList.LabelCounters(),
		hostname,
		config,
		deviceWatchList,
	)
	if err != nil {
		return nil, fmt.Errorf("create xid total collector watch: %w", err)
	}

	collector := &xidTotalCollector{
		expCollector: expCollector,
		initialSince: initialSince,
		cursors:      map[dcgm.GroupHandle]time.Time{},
		totals:       map[xidTotalKey]int{},
	}
	collector.poller = newCumulativeCollectorPoller(
		counters.DCGMExpXIDErrorsTotal,
		cumulativeCollectorPollInterval(counters.DCGMExpXIDErrorsTotal, config.CollectInterval),
		collector.collectNewEvents,
		collector.expCollector.Cleanup,
	)
	collector.sourceFields = map[dcgm.Short]string{
		dcgm.DCGM_FI_DEV_XID_ERRORS: "DCGM_FI_DEV_XID_ERRORS",
	}
	collector.counter = counterList[slices.IndexFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpXIDErrorsTotal
	})]
	collector.poller.start()

	return collector, nil
}

// IsDCGMExpXIDErrorsTotalEnabled checks if the DCGM_EXP_XID_ERRORS_TOTAL counter exists.
func IsDCGMExpXIDErrorsTotalEnabled(counterList counters.CounterList) bool {
	return slices.ContainsFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpXIDErrorsTotal
	})
}
