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
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// xidCollector counts the sampled XID values of each entity in the configured
// window, one series per XID.
type xidCollector struct {
	expCollector
}

// GetMetrics reads and renders the current XID error window.
func (c *xidCollector) GetMetrics() (MetricsByCounter, error) {
	return c.expCollector.getMetrics()
}

// NewXIDCollector creates the windowed XID collector with the current process
// client for direct callers.
func NewXIDCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	return newXIDCollectorWithClient(dcgmprovider.Client(), counterList, hostname, config, deviceWatchList)
}

// newXIDCollectorWithClient creates the windowed XID collector with client so
// its metrics and watch cleanup stay in one DCGM generation.
func newXIDCollectorWithClient(
	client dcgmprovider.DCGM,
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpXIDErrorsCountEnabled(counterList) {
		slog.Error(counters.DCGMExpXIDErrorsCount + " collector is disabled")
		return nil, fmt.Errorf(counters.DCGMExpXIDErrorsCount + " collector is disabled")
	}

	source, err := countSource(counters.DCGMExpXIDErrorsCount)
	if err != nil {
		return nil, err
	}

	collector := xidCollector{}
	deviceWatchList.SetDeviceFieldsForAllGPUScopes([]dcgm.Short{source.FieldID})

	collector.expCollector, err = newExpCollectorWithClient(
		client,
		counterList.LabelCounters(),
		hostname,
		config,
		deviceWatchList,
	)
	if err != nil {
		return nil, err
	}
	collector.sourceFields = map[dcgm.Short]string{source.FieldID: source.Name}

	collector.counter = counterList[slices.IndexFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpXIDErrorsCount
	})]

	collector.labelFiller = func(metricValueLabels map[string]string, entityValue int64) {
		metricValueLabels[XIDLabel] = fmt.Sprint(entityValue)
	}

	collector.windowSize = config.XIDCountWindowSize

	return &collector, nil
}

// IsDCGMExpXIDErrorsCountEnabled reports whether the windowed XID counter is selected.
func IsDCGMExpXIDErrorsCountEnabled(counterList counters.CounterList) bool {
	return slices.ContainsFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpXIDErrorsCount
	})
}
