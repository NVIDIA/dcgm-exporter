/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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
	"maps"
	"slices"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// IsDCGMExpP2PStatusEnabled checks if the DCGM_EXP_P2P_STATUS counter exists
func IsDCGMExpP2PStatusEnabled(counterList counters.CounterList) bool {
	return slices.ContainsFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpP2PStatus
	})
}

type p2pStatusCollector struct {
	baseExpCollector
	deviceInfoProvider deviceinfo.Provider
}

func (c *p2pStatusCollector) GetMetrics() (MetricsByCounter, error) {
	p2pStatus, err := dcgmprovider.Client().GetNvLinkP2PStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to get P2P status: %v", err)
	}

	monitoringInfo := devicemonitoring.GetMonitoredEntities(c.deviceInfoProvider)

	metrics := make(MetricsByCounter)
	metrics[c.counter] = make([]Metric, 0)

	useOld := c.config.UseOldNamespace
	uuid := "UUID"
	if useOld {
		uuid = "uuid"
	}

	labels := map[string]string{}

	for i, status := range p2pStatus.Gpus {
		for j, link := range status {
			if i == j {
				continue
			}

			if len(c.labelsCounters) > 0 && len(c.deviceWatchList.LabelDeviceFields()) > 0 {
				err := c.getLabelsFromCounters(monitoringInfo[i], labels)
				if err != nil {
					return nil, err
				}
			}

			metricValueLabels := maps.Clone(labels)
			metricValueLabels[PeerGPULabel] = strconv.Itoa(j)
			metricValueLabels[LinkStatusLabel] = p2pStatusToString(uint64(link))
			// Safe conversion from link to int, assuming link values are small
			linkValue := int(uint64(link)) //nolint:gosec // link values are small in practice
			m := c.createMetric(metricValueLabels, monitoringInfo[i], uuid, linkValue)
			metrics[c.counter] = append(metrics[c.counter], m)
		}
	}

	return metrics, nil
}

func p2pStatusToString(status uint64) string {
	switch status {
	case 0:
		return LinkStatusOK
	case 1:
		return LinkStatusChipsetNotSupported
	case 2:
		return LinkStatusTopologyNotSupported
	case 3:
		return LinkStatusDisabledByRegKey
	case 4:
		return LinkStatusNotSupported
	default:
		return LinkStatusUnknown
	}
}

// NewP2PStatusCollector creates a new P2P status collector
func NewP2PStatusCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpP2PStatusEnabled(counterList) {
		slog.Error(counters.DCGMExpP2PStatus + " collector is disabled")
		return nil, errors.New(counters.DCGMExpP2PStatus + " collector is disabled")
	}

	deviceInfoProvider, err := deviceinfo.Initialize(appconfig.DeviceOptions{
		MinorRange: []int{-1},
		MajorRange: []int{-1},
	},
		appconfig.DeviceOptions{},
		appconfig.DeviceOptions{},
		config.UseFakeGPUs, dcgm.FE_GPU)
	if err != nil {
		return nil, err
	}

	return &p2pStatusCollector{
		baseExpCollector: baseExpCollector{
			counter: counterList[slices.IndexFunc(counterList, func(c counters.Counter) bool {
				return c.FieldName == counters.DCGMExpP2PStatus
			})],
			labelsCounters:  counterList.LabelCounters(),
			hostname:        hostname,
			config:          config,
			deviceWatchList: deviceWatchList,
		},
		deviceInfoProvider: deviceInfoProvider,
	}, nil
}
