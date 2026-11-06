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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChartNamesFollowTheOverrides checks that fullnameOverride and
// namespaceOverride set the name and namespace of every object the chart
// creates, and that the metrics ConfigMap is named after the release so two
// releases can share a namespace.
func TestChartNamesFollowTheOverrides(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"fullnameOverride":  "gpu-metrics",
		"namespaceOverride": "monitoring",
	})

	for _, kind := range []string{"DaemonSet", "Service", "ServiceAccount", "ServiceMonitor"} {
		resource := requireResourceKind(t, resources, kind)
		assert.Equal(t, "gpu-metrics", resource.Metadata.Name, kind)
		assert.Equal(t, "monitoring", resource.Metadata.Namespace, kind)
	}

	metrics := requireResourceName(t, resources, "ConfigMap", "gpu-metrics-metrics")
	assert.Equal(t, "monitoring", metrics.Metadata.Namespace)

	volume := requireVolume(t, requireResourceKind(t, resources, "DaemonSet"), metricsVolumeName)
	assert.Equal(t, "gpu-metrics-metrics", volume.ConfigMap.Name)
}

// TestChartNameOverrideKeepsTheReleaseName checks that nameOverride changes the
// chart name in labels and, when the release name does not contain it, in the
// fullname.
func TestChartNameOverrideKeepsTheReleaseName(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{"nameOverride": "gpu-exporter"})

	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	assert.Equal(t, "dcgm-exporter-gpu-exporter", daemonSet.Metadata.Name)
	assert.Equal(t, "gpu-exporter", daemonSet.Metadata.Labels["app.kubernetes.io/name"])
	assert.Equal(t, "dcgm-exporter", daemonSet.Metadata.Labels["app.kubernetes.io/instance"])
}

// TestChartNamesAreCutAtTheNameLimit checks that a long name is cut to 63
// characters and drops a trailing dash, and that a metrics ConfigMap name that
// would pass 63 characters is cut and ends with a hash of the fullname, so
// fullnames that differ only after the cut keep different names.
func TestChartNamesAreCutAtTheNameLimit(t *testing.T) {
	tests := []struct {
		name         string
		values       map[string]interface{}
		wantFullname string
		wantMetrics  string
	}{
		{
			name:         "long fullnameOverride",
			values:       map[string]interface{}{"fullnameOverride": strings.Repeat("a", 70)},
			wantFullname: strings.Repeat("a", 63),
			wantMetrics:  hashedMetricsName(strings.Repeat("a", 46), strings.Repeat("a", 63)),
		},
		{
			name:         "cut that ends in a dash",
			values:       map[string]interface{}{"fullnameOverride": strings.Repeat("a", 62) + "-bbbbb"},
			wantFullname: strings.Repeat("a", 62),
			wantMetrics:  hashedMetricsName(strings.Repeat("a", 46), strings.Repeat("a", 62)),
		},
		{
			name:         "long nameOverride after the release name",
			values:       map[string]interface{}{"nameOverride": strings.Repeat("a", 48) + "-bbbbb"},
			wantFullname: "dcgm-exporter-" + strings.Repeat("a", 48),
			wantMetrics:  hashedMetricsName("dcgm-exporter-"+strings.Repeat("a", 32), "dcgm-exporter-"+strings.Repeat("a", 48)),
		},
		{
			name:         "metrics cut that ends in a dash",
			values:       map[string]interface{}{"fullnameOverride": strings.Repeat("a", 45) + "-bbbbbbbbbb"},
			wantFullname: strings.Repeat("a", 45) + "-bbbbbbbbbb",
			wantMetrics:  hashedMetricsName(strings.Repeat("a", 45), strings.Repeat("a", 45)+"-bbbbbbbbbb"),
		},
		{
			name:         "fullname that just fits the suffix",
			values:       map[string]interface{}{"fullnameOverride": strings.Repeat("a", 55)},
			wantFullname: strings.Repeat("a", 55),
			wantMetrics:  strings.Repeat("a", 55) + "-metrics",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resources := renderChart(t, tt.values)

			daemonSet := requireResourceKind(t, resources, "DaemonSet")
			assert.Equal(t, tt.wantFullname, daemonSet.Metadata.Name)

			requireResourceName(t, resources, "ConfigMap", tt.wantMetrics)
			assert.Equal(t, tt.wantMetrics, requireVolume(t, daemonSet, metricsVolumeName).ConfigMap.Name)
		})
	}

	// Two releases whose fullnames differ only after the cut must not share
	// a metrics ConfigMap.
	t.Run("fullnames that differ after the cut", func(t *testing.T) {
		one := renderChart(t, map[string]interface{}{"fullnameOverride": strings.Repeat("a", 55) + "-one"})
		two := renderChart(t, map[string]interface{}{"fullnameOverride": strings.Repeat("a", 55) + "-two"})

		oneName := requireVolume(t, requireResourceKind(t, one, "DaemonSet"), metricsVolumeName).ConfigMap.Name
		twoName := requireVolume(t, requireResourceKind(t, two, "DaemonSet"), metricsVolumeName).ConfigMap.Name
		assert.NotEqual(t, oneName, twoName)
		assert.LessOrEqual(t, len(oneName), 63)
	})
}

// hashedMetricsName returns the metrics ConfigMap name the chart gives a
// fullname too long for the suffix: the cut prefix, the first 8 hex
// characters of the fullname's SHA-256, and "-metrics".
func hashedMetricsName(prefix, fullname string) string {
	sum := sha256.Sum256([]byte(fullname))

	return fmt.Sprintf("%s-%s-metrics", prefix, hex.EncodeToString(sum[:])[:8])
}
