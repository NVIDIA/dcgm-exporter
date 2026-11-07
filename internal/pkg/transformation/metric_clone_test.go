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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
)

// TestCloneMetric checks map mutation cannot affect the source or another metric copy.
func TestCloneMetric(t *testing.T) {
	// Map-presence cases check both mutable maps independently.
	tests := []struct {
		name       string
		labels     map[string]string
		attributes map[string]string
		identity   *collector.SourceIdentity
	}{
		{name: "nil maps"},
		{name: "labels only", labels: map[string]string{"label": "value"}},
		{name: "attributes only", attributes: map[string]string{"attribute": "value"}},
		{
			name:       "both maps",
			labels:     map[string]string{"label": "value"},
			attributes: map[string]string{"attribute": "value"},
			identity: &collector.SourceIdentity{
				GPUModelName: "original", PodLabels: map[string]string{"a/b": "value"},
				NVLinkIndex: new(int64(1)), NVSwitchIndex: new(int64(2)),
				PCIeGeneration: new(int64(3)), PCIeMaxGeneration: new(int64(4)),
				PCIeWidth: new(int64(8)), PCIeMaxWidth: new(int64(16)),
				Containers: []collector.ContainerIdentity{{ID: "full", Name: "raw"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := collector.Metric{
				Value:      "42",
				Timestamp:  1_700_000_000_123_456,
				GPU:        "1",
				GPUUUID:    "GPU-1",
				Hostname:   "host",
				Labels:     test.labels,
				Attributes: test.attributes,
				Source:     test.identity,
			}
			first := cloneMetric(source)
			second := cloneMetric(source)

			assert.Equal(t, source, first)
			assert.Equal(t, source, second)
			if first.Source != nil {
				first.Source.GPUModelName = "changed"
				first.Source.PodLabels["a/b"] = "changed"
				first.Source.Containers[0].ID = "changed"
				for _, pair := range [][2]*int64{
					{first.Source.NVLinkIndex, source.Source.NVLinkIndex},
					{first.Source.NVSwitchIndex, source.Source.NVSwitchIndex},
					{first.Source.PCIeMaxGeneration, source.Source.PCIeMaxGeneration},
					{first.Source.PCIeMaxWidth, source.Source.PCIeMaxWidth},
					{first.Source.PCIeGeneration, source.Source.PCIeGeneration},
					{first.Source.PCIeWidth, source.Source.PCIeWidth},
				} {
					original := *pair[1]
					*pair[0] = 42
					assert.Equal(t, original, *pair[1])
				}
				assert.Equal(t, "original", source.Source.GPUModelName)
				assert.Equal(t, "value", source.Source.PodLabels["a/b"])
				assert.Equal(t, "full", source.Source.Containers[0].ID)
				assert.Equal(t, source.Source, second.Source)
			}

			if first.Labels != nil {
				first.Labels["label"] = "changed"
				assert.Equal(t, "value", source.Labels["label"])
				assert.Equal(t, "value", second.Labels["label"])
			}
			if first.Attributes != nil {
				first.Attributes["attribute"] = "changed"
				assert.Equal(t, "value", source.Attributes["attribute"])
				assert.Equal(t, "value", second.Attributes["attribute"])
			}
		})
	}
}
