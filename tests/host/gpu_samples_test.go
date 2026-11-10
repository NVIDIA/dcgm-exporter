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

package host

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// gpuScrape is one exporter scrape, read as samples per family.
type gpuScrape map[string]*io_prometheus_client.MetricFamily

// scrapeGPUMetrics reads one scrape from metricsURL and fails the test when
// the exporter does not answer with parseable metrics.
func scrapeGPUMetrics(t testing.TB, metricsURL string, process *hostExporterProcess) gpuScrape {
	t.Helper()
	require.NoError(t, process.running(), "the exporter stopped\n%s", process.output.String())

	families, status, err := fetchMetricFamilies(metricsURL)
	require.NoErrorf(t, err, "scrape %s\n%s", metricsURL, process.output.String())
	require.Equalf(t, http.StatusOK, status, "scrape %s", metricsURL)

	return gpuScrape(families)
}

// sum adds the values of family samples on gpu whose labels include every
// label in match. A family or sample that is absent adds nothing, which is
// how the exporter reports a count with no events for a label.
func (s gpuScrape) sum(family, gpu string, match map[string]string) float64 {
	total := 0.0
	for _, metric := range s[family].GetMetric() {
		labels := labelMap(metric)
		if labels["gpu"] != gpu || !labelsInclude(labels, match) {
			continue
		}

		total += metricValue(metric)
	}
	return total
}

// value returns the single sample of family on gpu and whether there is exactly one.
func (s gpuScrape) value(family, gpu string) (float64, bool) {
	var values []float64
	for _, metric := range s[family].GetMetric() {
		if labelMap(metric)["gpu"] == gpu {
			values = append(values, metricValue(metric))
		}
	}

	if len(values) != 1 {
		return 0, false
	}
	return values[0], true
}

// gpus returns the gpu label values of family in numeric order, each once.
func (s gpuScrape) gpus(family string) []string {
	return labelValues(map[string]*io_prometheus_client.MetricFamily{family: s[family]}, "gpu")
}

// gpusWithUUID returns the gpu label of every sample of family whose UUID
// label is uuid.
func (s gpuScrape) gpusWithUUID(family, uuid string) []string {
	var gpus []string
	for _, metric := range s[family].GetMetric() {
		labels := labelMap(metric)
		if firstLabel(labels, uuidLabels) == uuid {
			gpus = append(gpus, labels["gpu"])
		}
	}
	return gpus
}

// metricValue returns the value of a gauge, counter or untyped sample.
func metricValue(metric *io_prometheus_client.Metric) float64 {
	switch {
	case metric.GetGauge() != nil:
		return metric.GetGauge().GetValue()
	case metric.GetCounter() != nil:
		return metric.GetCounter().GetValue()
	default:
		return metric.GetUntyped().GetValue()
	}
}

// labelsInclude reports whether labels holds every name and value in match.
func labelsInclude(labels, match map[string]string) bool {
	for name, value := range match {
		if labels[name] != value {
			return false
		}
	}
	return true
}

// seriesSelector names a family and label match the way PromQL writes them,
// for failure messages.
func seriesSelector(family string, match map[string]string) string {
	if len(match) == 0 {
		return family
	}

	pairs := make([]string, 0, len(match))
	for name, value := range match {
		pairs = append(pairs, fmt.Sprintf("%s=%q", name, value))
	}
	slices.Sort(pairs)
	return family + "{" + strings.Join(pairs, ",") + "}"
}

// writeScenarioCounters writes rows to a counters file in a temporary
// directory and returns its path.
func writeScenarioCounters(t testing.TB, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "counters.csv")
	writeCounters(t, path, rows...)
	return path
}

// parseTestScrape parses Prometheus text for unit tests.
func parseTestScrape(t *testing.T, text string) gpuScrape {
	t.Helper()
	families, err := parseMetricFamilies(text)
	require.NoError(t, err)
	return gpuScrape(families)
}

// TestGPUScrapeSumsMatchingSamplesOfOneGPU checks that sums, single values,
// GPU lists and UUID lookups read only the samples of the named GPU and labels.
func TestGPUScrapeSumsMatchingSamplesOfOneGPU(t *testing.T) {
	scrape := parseTestScrape(t, `# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge
DCGM_EXP_XID_ERRORS_COUNT{gpu="0",UUID="GPU-a",xid="31"} 2
DCGM_EXP_XID_ERRORS_COUNT{gpu="0",UUID="GPU-a",xid="43"} 5
DCGM_EXP_XID_ERRORS_COUNT{gpu="1",UUID="GPU-b",xid="31"} 7
DCGM_EXP_XID_ERRORS_COUNT{gpu="10",UUID="GPU-c"} 0
DCGM_EXP_XID_ERRORS_COUNT{gpu="2",UUID="GPU-d"} 0
# TYPE DCGM_FI_DEV_GPU_UTIL_RATIO gauge
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="0",UUID="GPU-a"} 0.25
`)

	tests := []struct {
		name   string
		family string
		gpu    string
		match  map[string]string
		want   float64
	}{
		{name: "one label value", family: "DCGM_EXP_XID_ERRORS_COUNT", gpu: "0", match: map[string]string{"xid": "31"}, want: 2},
		{name: "all label values", family: "DCGM_EXP_XID_ERRORS_COUNT", gpu: "0", want: 7},
		{name: "other GPU", family: "DCGM_EXP_XID_ERRORS_COUNT", gpu: "1", match: map[string]string{"xid": "31"}, want: 7},
		{name: "absent label value", family: "DCGM_EXP_XID_ERRORS_COUNT", gpu: "10", match: map[string]string{"xid": "31"}, want: 0},
		{name: "absent family", family: "DCGM_EXP_XID_ERRORS_TOTAL", gpu: "0", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.want, scrape.sum(tc.family, tc.gpu, tc.match), 1e-9)
		})
	}

	value, ok := scrape.value("DCGM_FI_DEV_GPU_UTIL_RATIO", "0")
	require.True(t, ok)
	require.InDelta(t, 0.25, value, 1e-9)

	_, ok = scrape.value("DCGM_EXP_XID_ERRORS_COUNT", "0")
	require.False(t, ok, "two samples on one GPU are not one value")
	require.Equal(t, []string{"0", "1", "2", "10"}, scrape.gpus("DCGM_EXP_XID_ERRORS_COUNT"))
	require.Empty(t, scrape.gpus("DCGM_EXP_XID_ERRORS_TOTAL"))
	require.Equal(t, []string{"0", "0"}, scrape.gpusWithUUID("DCGM_EXP_XID_ERRORS_COUNT", "GPU-a"))
	require.Equal(t, `DCGM_EXP_XID_ERRORS_COUNT{gpu="0",xid="31"}`,
		seriesSelector("DCGM_EXP_XID_ERRORS_COUNT", map[string]string{"xid": "31", "gpu": "0"}))
}
