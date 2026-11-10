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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// chartName is the name in the chart's Chart.yaml.
	chartName = "dcgm-exporter"
	// maxChartNameLength is the longest name the chart gives an object (63),
	// including the metrics ConfigMap name with its suffix and any hash.
	maxChartNameLength = 63
	// metricsConfigMapSuffix ends the name of the metrics ConfigMap.
	metricsConfigMapSuffix = "-metrics"
)

// chartFullName returns the name the chart gives its objects for a Helm
// release: the release name when it contains the chart name, otherwise
// "<release>-<chart>", cut to 63 characters. It mirrors the chart's
// dcgm-exporter.fullname template with no name overrides.
func chartFullName(releaseName string) string {
	name := releaseName
	if !strings.Contains(releaseName, chartName) {
		name = fmt.Sprintf("%s-%s", releaseName, chartName)
	}

	return cutChartName(name, maxChartNameLength)
}

// chartMetricsConfigMapName returns the name of the ConfigMap that holds the
// metrics list for a Helm release: the fullname and the "-metrics" suffix, or,
// when that passes 63 characters, the fullname cut to 46 characters, the first
// 8 hex characters of its SHA-256, and the suffix. It mirrors the chart's
// dcgm-exporter.metricsConfigMapName template.
func chartMetricsConfigMapName(releaseName string) string {
	fullName := chartFullName(releaseName)
	if len(fullName)+len(metricsConfigMapSuffix) <= maxChartNameLength {
		return fullName + metricsConfigMapSuffix
	}

	sum := sha256.Sum256([]byte(fullName))
	return fmt.Sprintf("%s-%s%s", cutChartName(fullName, 46), hex.EncodeToString(sum[:])[:8], metricsConfigMapSuffix)
}

// cutChartName cuts name to at most limit characters and drops a trailing
// dash, as the chart's trunc and trimSuffix calls do.
func cutChartName(name string, limit int) string {
	if len(name) > limit {
		name = name[:limit]
	}

	return strings.TrimSuffix(name, "-")
}
