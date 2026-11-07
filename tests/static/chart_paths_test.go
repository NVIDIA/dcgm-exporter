/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package static_test

// Paths and names of the files the chart mounts. Chart tests share these.
const (
	chartFilesDirectory  = "/etc/dcgm-exporter/chart"
	configDirectory      = chartFilesDirectory + "/config"
	metricsDirectory     = chartFilesDirectory + "/metrics"
	metricsFileName      = "default-counters.csv"
	chartMetricsFile     = metricsDirectory + "/" + metricsFileName
	webConfigDirectory   = chartFilesDirectory + "/web-config"
	webConfigPath        = webConfigDirectory + "/" + webConfigKey
	metricsVolumeName    = "dcgm-exporter-metrics"
	metricsConfigMapName = "dcgm-exporter-metrics"
	tlsVolumeName        = "tls"
	tlsDirectory         = "/etc/dcgm-exporter/tls"
	debugDumpsVolumeName = "debug-dumps"
	debugDumpsDirectory  = "/tmp/dcgm-exporter-debug"
	// imageMetricsFile is the image's own metrics list, which older values
	// files mounted the chart's list over.
	imageMetricsFile = "/etc/dcgm-exporter/default-counters.csv"
)
