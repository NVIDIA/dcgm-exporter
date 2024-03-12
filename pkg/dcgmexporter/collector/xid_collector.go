/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
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
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	dcgmClient "github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/dcgm_client"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/metrics"
)

type xidCollector struct {
	expCollector
}

func (c *xidCollector) GetMetrics() (MetricsByCounter, error) {
	return c.expCollector.GetMetrics()
}

func NewXIDCollector(
	counters []common.Counter,
	hostname string,
	config *common.Config,
	fieldEntityGroupTypeSystemInfo dcgmClient.FieldEntityGroupTypeSystemInfoItem,
) (Collector, error) {
	if !IsDCGMExpXIDErrorsCountEnabled(counters) {
		logrus.Error(metrics.DCGMExpXIDErrorsCount + " collector is disabled")
		return nil, fmt.Errorf(metrics.DCGMExpXIDErrorsCount + " collector is disabled")
	}

	collector := xidCollector{}
	collector.expCollector = newExpCollector(counters,
		hostname,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		config,
		fieldEntityGroupTypeSystemInfo)

	collector.counter = counters[slices.IndexFunc(counters, func(c common.Counter) bool {
		return c.FieldName == metrics.DCGMExpXIDErrorsCount
	})]

	collector.labelFiller = func(metricValueLabels map[string]string, entityValue int64) {
		metricValueLabels["xid"] = fmt.Sprint(entityValue)
	}

	collector.windowSize = config.XIDCountWindowSize

	return &collector, nil
}

func IsDCGMExpXIDErrorsCountEnabled(counters []common.Counter) bool {
	return slices.ContainsFunc(counters, func(c common.Counter) bool {
		return c.FieldName == metrics.DCGMExpXIDErrorsCount
	})
}
