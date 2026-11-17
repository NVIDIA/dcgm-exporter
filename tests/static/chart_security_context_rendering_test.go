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
	"sigs.k8s.io/yaml"
)

// TestChartSecurityContextRenderContract checks the exporter container's
// security context, key by key, for the default values, for the non-root
// setup the values.yaml note describes, and for non-root values that leave out
// capabilities.add. Helm merges maps and replaces lists, so those values keep
// the default SYS_ADMIN, while any list for capabilities.add replaces it.
func TestChartSecurityContextRenderContract(t *testing.T) {
	tests := []struct {
		name   string
		values string
		want   string
	}{
		{
			name: "defaults",
			want: `{runAsNonRoot: false, runAsUser: 0, capabilities: {add: [SYS_ADMIN], drop: [ALL]}, allowPrivilegeEscalation: false}`,
		},
		{
			name:   "non-root without SYS_ADMIN",
			values: `securityContext: {runAsNonRoot: true, runAsUser: 1000, capabilities: {add: []}}`,
			want:   `{runAsNonRoot: true, runAsUser: 1000, capabilities: {add: [], drop: [ALL]}, allowPrivilegeEscalation: false}`,
		},
		{
			name:   "non-root values without capabilities.add keep SYS_ADMIN",
			values: `securityContext: {runAsNonRoot: true, runAsUser: 1000}`,
			want:   `{runAsNonRoot: true, runAsUser: 1000, capabilities: {add: [SYS_ADMIN], drop: [ALL]}, allowPrivilegeEscalation: false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var values, want map[string]interface{}
			require.NoError(t, yaml.Unmarshal([]byte(tt.values), &values))
			require.NoError(t, yaml.Unmarshal([]byte(tt.want), &want))

			exporter := requireContainer(t, requireResourceKind(t, renderChart(t, values), "DaemonSet"), "exporter")
			assert.Equal(t, want, exporter.SecurityContext)
		})
	}
}

// TestRawManifestSecurityContextMatchesTheChartDefaults keeps the two default
// install paths aligned: the exporter container in the raw manifest has the
// security context the chart renders from its default values.
func TestRawManifestSecurityContextMatchesTheChartDefaults(t *testing.T) {
	chart := requireContainer(t, requireResourceKind(t, renderChart(t, nil), "DaemonSet"), "exporter")
	require.NotEmpty(t, chart.SecurityContext, "the chart renders no securityContext")

	manifest := readRepositoryFile(t, "deployment", "examples", "dcgm-exporter.yaml")
	raw := requireContainer(t, requireResourceKind(t, parseManifest(t, manifest), "DaemonSet"), "dcgm-exporter")

	assert.Equal(t, chart.SecurityContext, raw.SecurityContext)
}
