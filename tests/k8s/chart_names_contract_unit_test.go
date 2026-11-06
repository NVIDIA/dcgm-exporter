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

package k8s

import (
	"strings"
	"testing"
)

// TestChartFullName checks the release-name rule of the chart's fullname template.
func TestChartFullName(t *testing.T) {
	long := strings.Repeat("a", 70)

	tests := []struct {
		name    string
		release string
		want    string
	}{
		{name: "release holds the chart name", release: "dcgm-exporter-1700000000", want: "dcgm-exporter-1700000000"},
		{name: "release without the chart name", release: "metrics", want: "metrics-dcgm-exporter"},
		{name: "long release is cut to 63", release: long, want: long[:63]},
		{name: "a trailing dash is trimmed", release: strings.Repeat("a", 62) + "-b", want: strings.Repeat("a", 62)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chartFullName(tt.release); got != tt.want {
				t.Errorf("chartFullName(%q) = %q, want %q", tt.release, got, tt.want)
			}
		})
	}
}

// TestChartMetricsConfigMapName checks that the metrics ConfigMap is the chart's
// fullname with the -metrics suffix, and that a fullname too long for the
// suffix is cut and followed by a hash so the whole name fits in 63 characters.
func TestChartMetricsConfigMapName(t *testing.T) {
	tests := []struct {
		name    string
		release string
		want    string
	}{
		{name: "short release", release: "web", want: "web-dcgm-exporter-metrics"},
		{name: "fullname that just fits the suffix", release: strings.Repeat("a", 41) + "-dcgm-exporter", want: strings.Repeat("a", 41) + "-dcgm-exporter-metrics"},
		{name: "long release is cut and hashed", release: strings.Repeat("a", 70), want: strings.Repeat("a", 46) + "-7d3e74a0-metrics"},
		{name: "a trailing dash is trimmed before the hash", release: strings.Repeat("a", 45) + "-dcgm-exporter", want: strings.Repeat("a", 45) + "-516a4da9-metrics"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chartMetricsConfigMapName(tt.release)
			if got != tt.want {
				t.Errorf("chartMetricsConfigMapName(%q) = %q, want %q", tt.release, got, tt.want)
			}

			if len(got) > maxChartNameLength {
				t.Errorf("chartMetricsConfigMapName(%q) has %d characters, want at most %d", tt.release, len(got), maxChartNameLength)
			}
		})
	}
}
