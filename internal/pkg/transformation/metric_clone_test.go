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

func TestCloneMetric(t *testing.T) {
	tests := []struct {
		name       string
		labels     map[string]string
		attributes map[string]string
	}{
		{name: "nil maps"},
		{name: "labels only", labels: map[string]string{"label": "value"}},
		{name: "attributes only", attributes: map[string]string{"attribute": "value"}},
		{
			name:       "both maps",
			labels:     map[string]string{"label": "value"},
			attributes: map[string]string{"attribute": "value"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := collector.Metric{
				Value:      "42",
				GPU:        "1",
				GPUUUID:    "GPU-1",
				Hostname:   "host",
				Labels:     test.labels,
				Attributes: test.attributes,
			}
			first := cloneMetric(source)
			second := cloneMetric(source)

			assert.Equal(t, source, first)
			assert.Equal(t, source, second)

			if first.Labels != nil {
				first.Labels["label"] = "changed"
				assert.Equal(t, "value", source.Labels["label"])
				assert.Equal(t, "value", second.Labels["label"])
			} else {
				assert.Nil(t, source.Labels)
				assert.Nil(t, second.Labels)
			}
			if first.Attributes != nil {
				first.Attributes["attribute"] = "changed"
				assert.Equal(t, "value", source.Attributes["attribute"])
				assert.Equal(t, "value", second.Attributes["attribute"])
			} else {
				assert.Nil(t, source.Attributes)
				assert.Nil(t, second.Attributes)
			}
		})
	}
}
