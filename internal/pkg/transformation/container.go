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

package transformation

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// containerMapper adds host runtime container labels to GPU metrics.
type containerMapper struct {
	runtime             containerRuntime
	unavailableWarnings warningLimiter
}

// newContainerMapper creates a container mapper using the configured runtime socket.
func newContainerMapper(c *appconfig.Config) *containerMapper {
	collectInterval := time.Duration(c.CollectInterval) * time.Millisecond
	runtime := newDockerCompatibleRuntime(c.ContainerRuntimeSocket, collectInterval)
	runtime.preserveIdentity = c.EnableOTLP

	return &containerMapper{
		runtime: runtime,
	}
}

// Name identifies the transformation in tests and logs.
func (m *containerMapper) Name() string { return "containerMapper" }

// Process appends one labeled metric copy for each container mapped to a metric GPU.
func (m *containerMapper) Process(metrics collector.MetricsByCounter, deviceInfo deviceinfo.Provider) error {
	if m == nil || m.runtime == nil {
		return nil
	}

	if deviceInfo == nil || deviceInfo.GPUCount() == 0 {
		return nil
	}

	// A runtime that cannot be read must not fail the scrape: warn at most once
	// per interval and serve the metrics without container labels.
	containersByGPU, err := m.runtime.ContainersByGPU(context.Background(), deviceInfo)
	if err != nil {
		if m.unavailableWarnings.allow() {
			slog.Warn("container mapper failed to collect runtime container labels; skipping enrichment. "+
				"The exporter keeps checking.", slog.String(logging.ErrorKey, err.Error()))
		}

		slog.Debug("container mapper runtime collection failed", slog.String(logging.ErrorKey, err.Error()))

		return nil
	}

	if len(containersByGPU) == 0 {
		return nil
	}

	addContainerCopies(metrics, containersByGPU)

	return nil
}

// addContainerCopies appends one labeled copy of each metric for every
// container on its GPU, keeping the original sample.
func addContainerCopies(metrics collector.MetricsByCounter, containersByGPU map[string][]containerInfo) {
	for counter := range metrics {
		var transformed []collector.Metric
		for _, metric := range metrics[counter] {
			transformed = append(transformed, metric)

			// A sample that already carries a container label is not copied again.
			if _, exists := metric.Attributes[containerAttribute]; exists {
				continue
			}

			for _, info := range containersForMetric(containersByGPU, metric) {
				transformed = append(transformed, metricWithContainer(metric, info))
			}
		}

		metrics[counter] = transformed
	}
}

// metricWithContainer creates an isolated workload sample with the legacy label and preserved raw identities.
func metricWithContainer(metric collector.Metric, info containerInfo) collector.Metric {
	metric = cloneMetric(metric)
	if metric.Attributes == nil {
		metric.Attributes = make(map[string]string)
	}
	metric.Attributes[containerAttribute] = info.Name
	if metric.Source != nil {
		metric.Source.Containers = slices.Clone(info.Identities)
	}
	return metric
}

// containersForMetric finds containers for the metric's GPU keys and deduplicates labels.
func containersForMetric(containersByGPU map[string][]containerInfo, metric collector.Metric) []containerInfo {
	keys := []string{metric.GPUUUID, metric.GPU, metric.GPUDevice}
	if metric.MigProfile != "" && metric.GPU != "" && metric.GPUInstanceID != "" {
		keys = append([]string{metric.GPU + "." + metric.GPUInstanceID}, keys...)
	}
	indexByName := make(map[string]int, len(containersByGPU[keys[0]]))
	var containers []containerInfo
	for _, key := range keys {
		if key == "" {
			continue
		}
		for _, info := range containersByGPU[key] {
			if info.Name == "" {
				continue
			}
			if index, exists := indexByName[info.Name]; exists {
				containers[index].Identities = mergeContainerIdentities(containers[index].Identities, info.Identities)
				continue
			}
			indexByName[info.Name] = len(containers)
			info.Identities = slices.Clone(info.Identities)
			containers = append(containers, info)
		}
	}
	return containers
}
