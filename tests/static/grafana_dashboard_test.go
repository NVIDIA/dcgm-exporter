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
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// dashboardMetricName matches a DCGM or exporter metric name inside a query.
var dashboardMetricName = regexp.MustCompile(`\bDCGM_[A-Z0-9_]+\b`)

// TestGrafanaDashboardUsesDefaultMetrics checks that every DCGM metric name the
// checked-in dashboard queries is enabled in etc/default-counters.csv, so a
// renamed or removed default metric fails here instead of leaving an empty
// panel.
func TestGrafanaDashboardUsesDefaultMetrics(t *testing.T) {
	data, err := os.ReadFile(repoPath(t, "grafana", "dcgm-exporter-dashboard.json"))
	require.NoError(t, err)

	var dashboard any
	require.NoError(t, json.Unmarshal(data, &dashboard))

	queried := map[string]bool{}
	collectMetricNames(dashboard, queried)
	require.NotEmpty(t, queried, "the dashboard queries no DCGM metric")

	enabled := defaultCounterNames(t)

	var missing []string
	for name := range queried {
		if !enabled[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing, "the dashboard queries metrics that etc/default-counters.csv does not enable")
}

// collectMetricNames adds every metric name found in a string anywhere in the
// decoded JSON value.
func collectMetricNames(value any, names map[string]bool) {
	switch v := value.(type) {
	case string:
		for _, name := range dashboardMetricName.FindAllString(v, -1) {
			names[name] = true
		}
	case []any:
		for _, item := range v {
			collectMetricNames(item, names)
		}
	case map[string]any:
		for _, item := range v {
			collectMetricNames(item, names)
		}
	}
}

// defaultCounterNames returns the field names enabled in etc/default-counters.csv.
func defaultCounterNames(t *testing.T) map[string]bool {
	t.Helper()
	file, err := os.Open(repoPath(t, "etc", "default-counters.csv"))
	require.NoError(t, err)
	defer file.Close()

	rows, err := metriccontract.ReadDefaultCounterRows(file)
	require.NoError(t, err)

	names := map[string]bool{}
	for _, row := range rows {
		names[row.Name] = true
	}
	return names
}
