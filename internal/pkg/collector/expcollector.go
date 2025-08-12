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

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

type expCollector struct {
	baseExpCollector
	fieldValueParser func(val int64) []int64        // Function to parse the field value
	labelFiller      func(map[string]string, int64) // Function to fill labels
	windowSize       int                            // Window size
}

func (c *expCollector) getMetrics() (MetricsByCounter, error) {
	err := dcgmprovider.Client().UpdateAllFields()
	if err != nil {
		return nil, err
	}

	mapEntityIDToValues := map[uint]map[int64]int{}

	window := time.Now().Add(-time.Duration(c.windowSize) * time.Millisecond)

	for _, group := range c.deviceWatchList.DeviceGroups() {
		values, _, err := dcgmprovider.Client().GetValuesSince(group, c.deviceWatchList.DeviceFieldGroup(), window)
		if err != nil {
			return nil, err
		}

		for _, val := range values {
			if val.Status == 0 {
				if _, exists := mapEntityIDToValues[val.EntityID]; !exists {
					mapEntityIDToValues[val.EntityID] = map[int64]int{}
				}

				for _, v := range c.fieldValueParser(val.Int64()) {
					mapEntityIDToValues[val.EntityID][v] += 1
				}
			}
		}
	}

	labels := map[string]string{}
	labels[windowSizeInMSLabel] = fmt.Sprint(c.windowSize)

	monitoringInfo := devicemonitoring.GetMonitoredEntities(c.deviceWatchList.DeviceInfo())
	metrics := make(MetricsByCounter)
	useOld := c.config.UseOldNamespace
	uuid := "UUID"
	if useOld {
		uuid = "uuid"
	}
	for _, mi := range monitoringInfo {
		if len(c.labelsCounters) > 0 && len(c.deviceWatchList.LabelDeviceFields()) > 0 {
			err := c.getLabelsFromCounters(mi, labels)
			if err != nil {
				return nil, err
			}
		}
		entityValues, exists := mapEntityIDToValues[mi.DeviceInfo.GPU]
		if exists {
			for entityValue, val := range entityValues {

				metricValueLabels := maps.Clone(labels)
				c.labelFiller(metricValueLabels, entityValue)

				m := c.createMetric(metricValueLabels, mi, uuid, val)

				metrics[c.counter] = append(metrics[c.counter], m)
			}
		} else {
			// Create metric with Zero value if group (mapEntityIDToValues) is empty
			m := c.createMetric(labels, mi, uuid, 0)
			metrics[c.counter] = append(metrics[c.counter], m)
		}
	}

	return metrics, nil
}

// newExpCollector is a constructor for the expCollector
func newExpCollector(
	labelsCounters []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (expCollector, error) {
	collector := expCollector{
		baseExpCollector: baseExpCollector{
			deviceWatchList: deviceWatchList,
			hostname:        hostname,
			config:          config,
			labelsCounters:  labelsCounters,
		},

		fieldValueParser: func(val int64) []int64 {
			return []int64{val}
		},
		labelFiller: func(metricValueLabels map[string]string, entityValue int64) {
			// This function is intentionally left blank
		},
	}

	var err error

	collector.cleanups, err = collector.deviceWatchList.Watch()
	if err != nil {
		slog.Warn(fmt.Sprintf("Failed to watch metrics: %s", err))
		return expCollector{}, err
	}

	return collector, nil
}
