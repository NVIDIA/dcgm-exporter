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
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// expCollector supplies shared windowed DCGM-event collection for exporter
// counters such as XID and clock-event counts.
type expCollector struct {
	baseExpCollector
	fieldValueParser func(val int64) []int64        // Function to parse the field value
	labelFiller      func(map[string]string, int64) // Function to fill labels
	windowSize       int                            // Window size
	sourceFields     map[dcgm.Short]string          // Source DCGM fields backing this exporter counter
}

// getMetrics reads the current event window through the captured client and
// turns its values into this exporter's metric family.
func (c *expCollector) getMetrics() (MetricsByCounter, error) {
	err := c.dcgmClient().UpdateAllFields()
	if err != nil {
		return nil, err
	}

	observed := c.currentTime()
	window := observed.Add(-time.Duration(c.windowSize) * time.Millisecond)
	mapEntityIDToValues, err := c.readWindowValues(window)
	if err != nil {
		return nil, fmt.Errorf("read exporter event window: %w", err)
	}

	monitoringInfo := c.deviceWatchList.MonitoredEntities()
	metrics := make(MetricsByCounter)
	useOld := c.config.UseOldNamespace
	uuid := "UUID"
	if useOld {
		uuid = "uuid"
	}
	for _, mi := range monitoringInfo {
		labels := map[string]string{WindowSizeInMSLabel: fmt.Sprint(c.windowSize)}
		if len(c.labelsCounters) > 0 && len(c.deviceWatchList.LabelDeviceFields()) > 0 {
			err := c.getLabelsFromCounters(mi, labels)
			if err != nil {
				return nil, err
			}
		}
		entityValues := mapEntityIDToValues[mi.Entity]
		if len(entityValues) == 0 {
			// Emit a zero placeholder when this entity has no parsed events.
			m := c.createMetric(labels, mi, uuid, 0, c.deviceWatchList.DeviceInfo(), observed.UnixMicro())
			metrics[c.counter] = append(metrics[c.counter], m)
			continue
		}
		for entityValue, val := range entityValues {
			metricValueLabels := maps.Clone(labels)
			c.labelFiller(metricValueLabels, entityValue)
			m := c.createMetric(metricValueLabels, mi, uuid, val, c.deviceWatchList.DeviceInfo(), observed.UnixMicro())
			metrics[c.counter] = append(metrics[c.counter], m)
		}
	}

	return metrics, nil
}

// readWindowValues combines sampled event counts from every watched field group.
func (c *expCollector) readWindowValues(window time.Time) (map[dcgm.GroupEntityPair]map[int64]int, error) {
	counts := make(map[dcgm.GroupEntityPair]map[int64]int)
	for _, binding := range c.watchBindings() {
		values, _, err := c.dcgmClient().GetValuesSince(binding.Group, binding.FieldGroup, window)
		if err != nil {
			return nil, err
		}

		c.accumulateWindowValues(counts, values)
	}

	return counts, nil
}

// accumulateWindowValues counts valid, nonblank samples by entity and parsed event value.
func (c *expCollector) accumulateWindowValues(counts map[dcgm.GroupEntityPair]map[int64]int, values []dcgm.FieldValue_v2) {
	for _, val := range values {
		if val.Status != 0 {
			continue
		}
		if isBlankValue(val) {
			if isDebugLoggingEnabled() {
				logBlankValueSkipped(val.FieldID, c.sourceFieldName(val.FieldID), val.EntityGroupId, val.EntityID)
			}
			continue
		}
		key := dcgm.GroupEntityPair{EntityGroupId: val.EntityGroupId, EntityId: val.EntityID}
		if counts[key] == nil {
			counts[key] = make(map[int64]int)
		}
		for _, value := range c.fieldValueParser(val.Int64()) {
			counts[key][value]++
		}
	}
}

// countSource returns the DCGM field an exporter count family reads and the
// field name used in configuration and logs. The total family of the same
// events reads the same field, so its collector looks up the count family.
func countSource(countFamily string) (counters.CountSource, error) {
	source, ok := counters.CountSourceOf(countFamily)
	if !ok {
		return counters.CountSource{}, fmt.Errorf("%s has no DCGM source field", countFamily)
	}

	return source, nil
}

// sourceFieldName returns the configured name of a source field, then a
// label-field name, then "unknown".
func (c *expCollector) sourceFieldName(fieldID dcgm.Short) string {
	if fieldName, ok := c.sourceFields[fieldID]; ok {
		return fieldName
	}

	return counterFieldNameOrUnknown(c.labelsCounters, fieldID)
}

// newExpCollector preserves the compatibility constructor for callers that
// use the process client. Factory-created collectors use newExpCollectorWithClient.
func newExpCollector(
	labelsCounters []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (expCollector, error) {
	return newExpCollectorWithClient(
		dcgmprovider.Client(),
		labelsCounters,
		hostname,
		config,
		deviceWatchList,
	)
}

// newExpCollectorWithClient creates the common exporter-counter implementation
// with the client that owns its watches and later DCGM reads. The receiving
// family owns the collector only after all watches are installed successfully.
func newExpCollectorWithClient(
	client dcgmprovider.DCGM,
	labelsCounters []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (expCollector, error) {
	collector := expCollector{
		baseExpCollector: baseExpCollector{
			client:          client,
			deviceWatchList: deviceWatchList,
			hostname:        hostname,
			config:          config,
			labelsCounters:  labelsCounters,
		},

		fieldValueParser: func(val int64) []int64 {
			return []int64{val}
		},
		labelFiller: func(metricValueLabels map[string]string, entityValue int64) {
			// No-op default. Windowed collectors replace labelFiller to add their
			// xid or clock_event label; cumulative collectors do not use it.
		},
	}

	var err error

	watchResult, err := collector.deviceWatchList.Watch()
	if err != nil {
		slog.Warn(fmt.Sprintf("Failed to watch metrics: %s", err))
		return expCollector{}, fmt.Errorf("watch exporter metrics: %w", err)
	}
	collector.watchResult = watchResult

	return collector, nil
}
