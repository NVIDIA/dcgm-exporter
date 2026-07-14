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
	"maps"
	"sync"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// containerMapper adds host runtime container labels to GPU metrics.
type containerMapper struct {
	runtime     containerRuntime
	warnErrOnce sync.Once
}

// newContainerMapper creates a container mapper using the configured runtime socket.
func newContainerMapper(c *appconfig.Config) *containerMapper {
	collectInterval := time.Duration(c.CollectInterval) * time.Millisecond
	return &containerMapper{
		runtime: newDockerCompatibleRuntime(c.ContainerRuntimeSocket, collectInterval),
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

	containersByGPU, err := m.runtime.ContainersByGPU(context.Background(), deviceInfo)
	if err != nil {
		m.warnErrOnce.Do(func() {
			slog.Warn("container mapper failed to collect runtime container labels; skipping enrichment", slog.String(logging.ErrorKey, err.Error()))
		})
		slog.Debug("container mapper runtime collection failed", slog.String(logging.ErrorKey, err.Error()))
		return nil
	}
	if len(containersByGPU) == 0 {
		return nil
	}

	for counter := range metrics {
		var transformed []collector.Metric
		for _, metric := range metrics[counter] {
			transformed = append(transformed, metric)
			if _, exists := metric.Attributes[containerAttribute]; exists {
				continue
			}
			for _, info := range containersForMetric(containersByGPU, metric) {
				if info.Name == "" {
					continue
				}
				copyMetric := metric
				copyMetric.Labels = maps.Clone(metric.Labels)
				copyMetric.Attributes = maps.Clone(metric.Attributes)
				if copyMetric.Attributes == nil {
					copyMetric.Attributes = make(map[string]string)
				}
				copyMetric.Attributes[containerAttribute] = info.Name
				transformed = append(transformed, copyMetric)
			}
		}
		metrics[counter] = transformed
	}
	return nil
}

// containersForMetric finds containers for the metric's GPU keys and deduplicates labels.
func containersForMetric(containersByGPU map[string][]containerInfo, metric collector.Metric) []containerInfo {
	keys := []string{metric.GPUUUID, metric.GPU, metric.GPUDevice}
	if metric.MigProfile != "" && metric.GPU != "" && metric.GPUInstanceID != "" {
		keys = append([]string{metric.GPU + "." + metric.GPUInstanceID}, keys...)
	}
	seen := make(map[string]struct{})
	var containers []containerInfo
	for _, key := range keys {
		if key == "" {
			continue
		}
		for _, info := range containersByGPU[key] {
			if info.Name == "" {
				continue
			}
			if _, exists := seen[info.Name]; exists {
				continue
			}
			seen[info.Name] = struct{}{}
			containers = append(containers, info)
		}
	}
	return containers
}
