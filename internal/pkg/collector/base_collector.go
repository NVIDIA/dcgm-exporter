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

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

type baseExpCollector struct {
	deviceWatchList devicewatchlistmanager.WatchList // Device info and fields used for counters and labels
	counter         counters.Counter                 // Counter for a specific collector type
	labelsCounters  []counters.Counter               // Counters used for labels
	hostname        string                           // Hostname
	config          *appconfig.Config                // Configuration settings
	cleanups        []func()                         // Cleanup functions
}

func (c *baseExpCollector) createMetric(
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
	return m
}

func (c *baseExpCollector) getLabelsFromCounters(mi devicemonitoring.Info, labels map[string]string) error {
	latestValues, err := dcgmprovider.Client().EntityGetLatestValues(mi.Entity.EntityGroupId, mi.Entity.EntityId,
		c.deviceWatchList.LabelDeviceFields())
	if err != nil {
		return err
	}
	// Extract Labels
	for _, val := range latestValues {
		v := toString(val)
		// Filter out counters with no value and ignored fields for this entity
		if v == skipDCGMValue {
			continue
		}

		counter, err := findCounterField(c.labelsCounters, val.FieldID)
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

func (c *baseExpCollector) Cleanup() {
	for _, cleanup := range c.cleanups {
		cleanup()
	}
}
