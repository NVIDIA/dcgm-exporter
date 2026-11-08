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

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChartRollingUpdateValues checks the default and custom update strategies,
// each with exactly one of maxUnavailable and maxSurge nonzero, as Kubernetes
// requires for a DaemonSet.
func TestChartRollingUpdateValues(t *testing.T) {
	tests := []struct {
		name            string
		values          map[string]interface{}
		wantUnavailable int
		wantSurge       int
	}{
		{name: "defaults", wantUnavailable: 1, wantSurge: 0},
		{
			name:            "custom maxUnavailable",
			values:          map[string]interface{}{"rollingUpdate": map[string]interface{}{"maxUnavailable": 3, "maxSurge": 0}},
			wantUnavailable: 3,
			wantSurge:       0,
		},
		{
			name:            "custom maxSurge",
			values:          map[string]interface{}{"rollingUpdate": map[string]interface{}{"maxUnavailable": 0, "maxSurge": 1}},
			wantUnavailable: 0,
			wantSurge:       1,
		},
		{
			name:            "null maxSurge defaults to 0",
			values:          map[string]interface{}{"rollingUpdate": map[string]interface{}{"maxUnavailable": 3, "maxSurge": nil}},
			wantUnavailable: 3,
			wantSurge:       0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy := requireResourceKind(t, renderChart(t, tt.values), "DaemonSet").Spec.UpdateStrategy

			assert.Equal(t, "RollingUpdate", strategy.Type)
			assert.Equal(t, tt.wantUnavailable, strategy.RollingUpdate.MaxUnavailable)
			assert.Equal(t, tt.wantSurge, strategy.RollingUpdate.MaxSurge)
		})
	}
}

// TestChartRejectsInvalidRollingUpdate checks that the chart fails the render
// when maxUnavailable and maxSurge are both nonzero or both zero, which
// Kubernetes rejects for a DaemonSet.
func TestChartRejectsInvalidRollingUpdate(t *testing.T) {
	tests := []struct {
		name        string
		unavailable interface{}
		surge       interface{}
		want        string
	}{
		{
			name:        "both nonzero",
			unavailable: 3,
			surge:       1,
			want:        "rollingUpdate.maxUnavailable (3) and rollingUpdate.maxSurge (1) cannot both be nonzero",
		},
		{
			name:        "both nonzero percentages",
			unavailable: "10%",
			surge:       "20%",
			want:        "rollingUpdate.maxUnavailable (10%) and rollingUpdate.maxSurge (20%) cannot both be nonzero",
		},
		{
			name:        "both zero",
			unavailable: 0,
			surge:       0,
			want:        "rollingUpdate.maxUnavailable and rollingUpdate.maxSurge cannot both be 0",
		},
		{
			name:        "zero percentage and zero",
			unavailable: "0%",
			surge:       0,
			want:        "rollingUpdate.maxUnavailable and rollingUpdate.maxSurge cannot both be 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := renderChartError(t, map[string]interface{}{
				"rollingUpdate": map[string]interface{}{"maxUnavailable": tt.unavailable, "maxSurge": tt.surge},
			})
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// TestChartResourcesAndInitContainers checks that resources merge key by key
// over the default requests and limits, as Helm merges maps, that a null key
// removes a default, and that initContainers reach the pod.
func TestChartResourcesAndInitContainers(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"resources": map[string]interface{}{
			"limits":   map[string]interface{}{"cpu": "1", "memory": "1Gi"},
			"requests": map[string]interface{}{"cpu": "500m", "ephemeral-storage": nil},
		},
		"initContainers": []interface{}{
			map[string]interface{}{"name": "wait-for-driver", "image": "busybox:1.36"},
		},
	})
	daemonSet := requireResourceKind(t, resources, "DaemonSet")

	exporter := requireContainer(t, daemonSet, "exporter")
	assert.Equal(t, map[string]string{"cpu": "1", "memory": "1Gi"}, exporter.Resources.Limits)
	assert.Equal(t, map[string]string{"cpu": "500m", "memory": "128Mi"}, exporter.Resources.Requests)

	require.Len(t, daemonSet.Spec.Template.Spec.InitContainers, 1)
	assert.Equal(t, "wait-for-driver", daemonSet.Spec.Template.Spec.InitContainers[0].Name)
}
