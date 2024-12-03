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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

type xidCollector struct {
	expCollector
}

func (c *xidCollector) GetMetrics() (MetricsByCounter, error) {
	return c.expCollector.getMetrics()
}

func NewXIDCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpXIDErrorsCountEnabled(counterList) {
		slog.Error(counters.DCGMExpXIDErrorsCount + " collector is disabled")
		return nil, fmt.Errorf(counters.DCGMExpXIDErrorsCount + " collector is disabled")
	}

	collector := xidCollector{}
	var err error
	deviceWatchList.SetDeviceFields([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})

	collector.expCollector, err = newExpCollector(
		counterList.LabelCounters(),
		hostname,
		config,
		deviceWatchList,
	)
	if err != nil {
		return nil, err
	}

	collector.counter = counterList[slices.IndexFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpXIDErrorsCount
	})]

	collector.labelFiller = func(metricValueLabels map[string]string, entityValue int64) {
		metricValueLabels["xid"] = fmt.Sprint(entityValue)
	}

	collector.windowSize = config.XIDCountWindowSize

	return &collector, nil
}

func IsDCGMExpXIDErrorsCountEnabled(counterList counters.CounterList) bool {
	return slices.ContainsFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpXIDErrorsCount
	})
}
