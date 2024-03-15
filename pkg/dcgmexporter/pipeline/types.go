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

package pipeline

import (
	"text/template"

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/collector"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo"
)

type MetricsPipeline struct {
	config *common.Config

	transformations      []Transform
	migMetricsFormat     *template.Template
	switchMetricsFormat  *template.Template
	linkMetricsFormat    *template.Template
	cpuMetricsFormat     *template.Template
	cpuCoreMetricsFormat *template.Template

	counters        []common.Counter
	gpuCollector    *collector.DCGMCollector
	switchCollector *collector.DCGMCollector
	linkCollector   *collector.DCGMCollector
	cpuCollector    *collector.DCGMCollector
	coreCollector   *collector.DCGMCollector
}

type Transform interface {
	Process(metrics collector.MetricsByCounter, sysInfo sysinfo.SystemInfoInterface) error
	Name() string
}
