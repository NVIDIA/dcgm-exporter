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
	"maps"
	"slices"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
)

// cloneMetric detaches mutable label and attribute maps before enriching a copy.
func cloneMetric(metric collector.Metric) collector.Metric {
	clone := metric
	clone.Labels = maps.Clone(metric.Labels)
	clone.Attributes = maps.Clone(metric.Attributes)

	if metric.Source != nil {
		source := *metric.Source
		source.PodLabels = maps.Clone(source.PodLabels)
		source.Containers = slices.Clone(source.Containers)
		source.NVLinkIndex = cloneIdentityNumber(source.NVLinkIndex)
		source.NVSwitchIndex = cloneIdentityNumber(source.NVSwitchIndex)
		source.PCIeMaxGeneration = cloneIdentityNumber(source.PCIeMaxGeneration)
		source.PCIeMaxWidth = cloneIdentityNumber(source.PCIeMaxWidth)
		source.PCIeGeneration = cloneIdentityNumber(source.PCIeGeneration)
		source.PCIeWidth = cloneIdentityNumber(source.PCIeWidth)
		clone.Source = &source
	}

	return clone
}

// cloneIdentityNumber detaches an optional numeric identity so enriched metric copies cannot mutate their source.
func cloneIdentityNumber(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
