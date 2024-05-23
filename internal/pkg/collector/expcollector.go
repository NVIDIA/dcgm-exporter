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
	"maps"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

var expCollectorFieldGroupIdx atomic.Uint32

type expCollector struct {
	deviceWatchList  devicewatchlistmanager.WatchList // Device info and fields used for counters and labels
	counter          counters.Counter                 // Counter for a specific collector type
	labelsCounters   []counters.Counter               // C  ounters used for labels
	hostname         string                           // Hostname
	config           *appconfig.Config                // Configuration settings
	cleanups         []func()                         // Cleanup functions
	fieldValueParser func(val int64) []int64          // Function to parse the field value
	labelFiller      func(map[string]string, int64)   // Function to fill labels
	windowSize       int                              // Window size
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
				if _, exists := mapEntityIDToValues[val.EntityId]; !exists {
					mapEntityIDToValues[val.EntityId] = map[int64]int{}
				}
				for _, v := range c.fieldValueParser(val.Int64()) {
					mapEntityIDToValues[val.EntityId][v] += 1
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

func (c *expCollector) createMetric(
	labels map[string]string, mi devicemonitoring.Info, uuid string, val int,
) Metric {
	gpuModel := getGPUModel(mi.DeviceInfo, c.config.ReplaceBlanksInModelName)

	m := Metric{
		Counter:      c.counter,
		Value:        fmt.Sprint(val),
		UUID:         uuid,
		GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
		GPUUUID:      mi.DeviceInfo.UUID,
		GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
		GPUModelName: gpuModel,
		Hostname:     c.hostname,

		Labels:     labels,
		Attributes: map[string]string{},
	}
	if mi.InstanceInfo != nil {
		m.MigProfile = mi.InstanceInfo.ProfileName
		m.GPUInstanceID = fmt.Sprintf("%d", mi.InstanceInfo.Info.NvmlInstanceId)
	} else {
		m.MigProfile = ""
		m.GPUInstanceID = ""
	}
	return m
}

func (c *expCollector) getLabelsFromCounters(mi devicemonitoring.Info, labels map[string]string) error {
	latestValues, err := dcgmprovider.Client().EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId,
		c.deviceWatchList.LabelDeviceFields())
	if err != nil {
		return err
	}
	// Extract Labels
	for _, val := range latestValues {
		v := ToString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == SkipDCGMValue {
			continue
		}

		counter, err := FindCounterField(c.labelsCounters, val.FieldId)
		if err != nil {
			continue
		}

		if counter.IsLabel() {
			labels[counter.FieldName] = v
			continue
		}
	}
	return nil
}

func (c *expCollector) Cleanup() {
	for _, cleanup := range c.cleanups {
		cleanup()
	}
}

// newExpCollector is a constructor for the expCollector
func newExpCollector(
	labelsCounters []counters.Counter,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (expCollector, error) {
	collector := expCollector{
		deviceWatchList: deviceWatchList,
		hostname:        hostname,
		config:          config,
		labelsCounters:  labelsCounters,
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
		logrus.Warnf("Failed to watch metrics: %s", err)
		return expCollector{}, err
	}

	return collector, nil
}
