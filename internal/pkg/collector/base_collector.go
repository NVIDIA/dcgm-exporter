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
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// baseExpCollector shares one registry generation's watch state across the
// exporter event collectors. Its captured client prevents a later GPU reset
// from reading or cleaning up handles that belong to this generation.
type baseExpCollector struct {
	now             func() time.Time
	client          dcgmprovider.DCGM                // DCGM session that owns this generation's watches and cleanup handles.
	deviceWatchList devicewatchlistmanager.WatchList // Device info and fields used for counters and labels
	counter         counters.Counter                 // Counter for a specific collector type
	labelsCounters  []counters.Counter               // Counters used for labels
	hostname        string                           // Hostname
	config          *appconfig.Config                // Configuration settings
	watchResult     *devicewatcher.WatchResult       // Exact DCGM watches owned by this collector.
}

// currentTime uses the collector clock for start times, observation times, and window boundaries.
func (c *baseExpCollector) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

// dcgmClient returns the client captured by the creating factory. The fallback
// keeps manually constructed test collectors compatible; production collector
// construction always supplies the explicit registry-generation client.
func (c *baseExpCollector) dcgmClient() dcgmprovider.DCGM {
	if c.client != nil {
		return c.client
	}
	return dcgmprovider.Client()
}

// watchBindings returns this collector's exact installed watch pairs. Direct
// metric-only test fixtures may not own a watch result, in which case there is
// nothing to read from DCGM.
func (c *baseExpCollector) watchBindings() []devicewatcher.WatchBinding {
	if c.watchResult == nil {
		return nil
	}
	return c.watchResult.Bindings()
}

// createMetric builds one exporter-owned metric from a monitored entity and
// the labels collected by the concrete exporter-counter family. The devices
// snapshot must own mi.DeviceInfo so reported/synthetic provenance stays aligned.
// timestamp is in Unix microseconds, like Metric.Timestamp.
func (c *baseExpCollector) createMetric(
	labels map[string]string, mi devicemonitoring.Info, uuid string, val int, devices deviceinfo.Provider, timestamp int64,
) Metric {
	gpuModel := getGPUModel(mi.DeviceInfo, c.config.ReplaceBlanksInModelName)

	m := Metric{
		Counter:      c.counter,
		Value:        fmt.Sprint(val),
		Timestamp:    timestamp,
		UUID:         uuid,
		GPU:          fmt.Sprintf("%d", mi.DeviceInfo.GPU),
		GPUUUID:      mi.DeviceInfo.UUID,
		GPUDevice:    fmt.Sprintf("nvidia%d", mi.DeviceInfo.GPU),
		GPUModelName: gpuModel,
		GPUPCIBusID:  mi.DeviceInfo.PCI.BusID,
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
	if mi.ComputeInstanceInfo != nil {
		m.GPUComputeInstanceID = fmt.Sprint(mi.ComputeInstanceInfo.InstanceInfo.NvmlComputeInstanceId)
	}
	if c.config.EnableOTLP {
		m.Source = sourceIdentity(mi, nil, devices)
	}
	return m
}

// getLabelsFromCounters reads labels through the same client that installed
// their watches, so a later DCGM session cannot be asked for old handles.
func (c *baseExpCollector) getLabelsFromCounters(mi devicemonitoring.Info, labels map[string]string) error {
	latestValues, err := c.dcgmClient().EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId,
		c.deviceWatchList.LabelDeviceFields())
	if err != nil {
		return err
	}
	// Extract Labels
	for _, val := range latestValues {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == skipDCGMValue {
			logFieldValueSkipped(
				val,
				counterFieldNameOrUnknown(c.labelsCounters, val.FieldID),
				mi.Entity.EntityGroupId,
				mi.Entity.EntityId,
			)
			continue
		}

		counter, err := findCounterField(c.labelsCounters, val.FieldID)
		if err != nil {
			continue
		}

		if counter.IsLabel() {
			addMetricLabel(labels, counter, v, c.hostname)
			continue
		}
	}
	return nil
}

// Cleanup releases this generation's watches after registry retirement has
// excluded scrapes that could still read them.
func (c *baseExpCollector) Cleanup() {
	if c.watchResult != nil {
		c.watchResult.Close()
		c.watchResult = nil
	}
}
