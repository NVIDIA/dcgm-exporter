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
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runGPUSelectionEffect runs the exporter with GPUs selected that are not a
// prefix of the host's list (one GPU on a two-GPU host, two otherwise), as the
// --devices flag and as the metrics entity key, and checks each run reports
// only those GPUs and matches the other form. It skips with fewer than two GPUs.
func runGPUSelectionEffect(t testing.TB) {
	base := startSettingBaseline(t)
	if len(base.inventory) < 2 {
		t.Skipf("GPU selection needs at least two GPUs; nvidia-smi reports %d", len(base.inventory))
	}

	runSettingForms(t, base, []settingForm{
		{
			name: "--devices",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--devices", "g:"+strings.Join(selectedGPUs(base.gpus), ","))
			},
			check: checkOnlySelectedGPUs,
		},
		{
			name: "metrics[].entities.gpu",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				entities := map[string]any{
					"gpu": map[string]any{"policy": "explicit", "gpus": gpuIndices(t, selectedGPUs(base.gpus))},
				}
				return []string{"--config-file", writeSettingConfig(t, metricsEntryConfig(base.collectors, port, entities))}
			},
			check: checkOnlySelectedGPUs,
		},
	})
}

// selectedGPUs picks GPUs from gpus that are not a prefix of the list, so a
// selection that keeps the first GPUs by mistake fails: the second GPU on a
// two-GPU host, otherwise the second and the last.
func selectedGPUs(gpus []string) []string {
	if len(gpus) < 2 {
		return nil
	}

	if len(gpus) == 2 {
		return []string{gpus[1]}
	}
	return []string{gpus[1], gpus[len(gpus)-1]}
}

// gpuIndices converts GPU IDs to the numbers a config file lists.
func gpuIndices(t testing.TB, gpus []string) []int {
	t.Helper()
	indices := make([]int, 0, len(gpus))
	for _, gpu := range gpus {
		index, err := strconv.Atoi(gpu)
		require.NoErrorf(t, err, "GPU ID %q", gpu)
		indices = append(indices, index)
	}
	return indices
}

// metricsEntryConfig is a version 2 document with one metrics entry that
// collects the counter file every second, with enrichment off, the given
// entities, and a Prometheus output on port.
func metricsEntryConfig(collectors string, port int, entities map[string]any) map[string]any {
	return map[string]any{
		"version": 2,
		"metrics": []any{map[string]any{
			"name":       "gpu",
			"file":       collectors,
			"every":      "1s",
			"entities":   entities,
			"enrichment": map[string]any{"kubernetes": false, "container": false, "hpc_jobs": false},
		}},
		"outputs": map[string]any{"prometheus": prometheusOutput(fmt.Sprintf(":%d", port))},
	}
}

// checkOnlySelectedGPUs checks the baseline reports every GPU nvidia-smi
// reports, and the run reports exactly the selected ones.
func checkOnlySelectedGPUs(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	require.Lenf(t, base.gpus, len(base.inventory), "the baseline must report every GPU nvidia-smi reports; it reports %v", base.gpus)

	problems := onlySelectedGPUProblems(base.families, run.families, selectedGPUs(base.gpus))
	require.Empty(t, problems, "GPU selection")
}

// onlySelectedGPUProblems checks every family: the run must have exactly the
// baseline's series of the selected GPUs, with the same UUIDs and label names
// and values, and none of the other GPUs. A family without a gpu label must not change.
func onlySelectedGPUProblems(base, run map[string]*io_prometheus_client.MetricFamily, selected []string) []string {
	var problems []string
	for _, name := range unionFamilyNames(base, run) {
		baseFamily, runFamily := base[name], run[name]
		if !familyHasLabel(baseFamily, "gpu") && !familyHasLabel(runFamily, "gpu") {
			if !slices.Equal(seriesLabelSets(baseFamily), seriesLabelSets(runFamily)) {
				problems = append(problems, fmt.Sprintf("%s has no gpu label and changed: baseline %v, run %v",
					name, seriesLabelSets(baseFamily), seriesLabelSets(runFamily)))
			}
			continue
		}

		want, got := gpuSeriesCounts(baseFamily, selected), gpuSeriesCounts(runFamily, nil)
		if !reflect.DeepEqual(want, got) {
			problems = append(problems, fmt.Sprintf("%s series per GPU: want %v (baseline, GPUs %v), got %v", name, want, selected, got))
		}

		wantUUIDs, gotUUIDs := gpuUUIDs(baseFamily, selected), gpuUUIDs(runFamily, nil)
		if !reflect.DeepEqual(wantUUIDs, gotUUIDs) {
			problems = append(problems, fmt.Sprintf("%s UUIDs per GPU: want %v, got %v", name, wantUUIDs, gotUUIDs))
		}

		wantLabels, gotLabels := gpuLabelSets(baseFamily, selected), gpuLabelSets(runFamily, nil)
		if !reflect.DeepEqual(wantLabels, gotLabels) {
			problems = append(problems, fmt.Sprintf("%s labels per GPU: want %v, got %v", name, wantLabels, gotLabels))
		}
	}
	return problems
}

// gpuSeriesCounts counts the series of family per gpu label value, keeping
// only the GPUs in keep, or every GPU when keep is nil.
func gpuSeriesCounts(family *io_prometheus_client.MetricFamily, keep []string) map[string]int {
	counts := map[string]int{}
	for _, metric := range family.GetMetric() {
		gpu, ok := labelMap(metric)["gpu"]
		if ok && (keep == nil || slices.Contains(keep, gpu)) {
			counts[gpu]++
		}
	}
	return counts
}

// gpuUUIDs maps each gpu label value of family to the sorted UUIDs its series
// carry, keeping only the GPUs in keep, or every GPU when keep is nil.
func gpuUUIDs(family *io_prometheus_client.MetricFamily, keep []string) map[string][]string {
	uuids := map[string][]string{}
	for _, metric := range family.GetMetric() {
		labels := labelMap(metric)
		gpu, ok := labels["gpu"]
		if !ok || (keep != nil && !slices.Contains(keep, gpu)) {
			continue
		}

		if uuid := firstLabel(labels, uuidLabels); !slices.Contains(uuids[gpu], uuid) {
			uuids[gpu] = append(uuids[gpu], uuid)
		}
	}

	for gpu := range uuids {
		slices.Sort(uuids[gpu])
	}
	return uuids
}

// gpuLabelSets maps each gpu label value of family to the sorted label sets of
// its series, each written as name=value pairs, keeping only the GPUs in keep,
// or every GPU when keep is nil.
func gpuLabelSets(family *io_prometheus_client.MetricFamily, keep []string) map[string][]string {
	sets := map[string][]string{}
	for _, metric := range family.GetMetric() {
		labels := labelMap(metric)
		gpu, ok := labels["gpu"]
		if !ok || (keep != nil && !slices.Contains(keep, gpu)) {
			continue
		}

		pairs := make([]string, 0, len(labels))
		for name, value := range labels {
			pairs = append(pairs, name+"="+value)
		}
		slices.Sort(pairs)
		sets[gpu] = append(sets[gpu], strings.Join(pairs, ","))
	}

	for gpu := range sets {
		slices.Sort(sets[gpu])
	}
	return sets
}

// familyHasLabel reports whether any series of family, which may be nil, has label.
func familyHasLabel(family *io_prometheus_client.MetricFamily, label string) bool {
	return slices.ContainsFunc(family.GetMetric(), func(metric *io_prometheus_client.Metric) bool {
		_, ok := labelMap(metric)[label]
		return ok
	})
}

// seriesLabelSets lists the sorted label names of each series of family,
// which may be nil.
func seriesLabelSets(family *io_prometheus_client.MetricFamily) []string {
	sets := []string{}
	for _, metric := range family.GetMetric() {
		names := make([]string, 0, len(metric.GetLabel()))
		for _, label := range metric.GetLabel() {
			names = append(names, label.GetName())
		}
		slices.Sort(names)
		sets = append(sets, strings.Join(names, ","))
	}
	slices.Sort(sets)
	return sets
}

// unionFamilyNames lists, in order, every family name in either scrape once.
func unionFamilyNames(left, right map[string]*io_prometheus_client.MetricFamily) []string {
	names := append(sortedFamilyNames(left), sortedFamilyNames(right)...)
	slices.Sort(names)
	return slices.Compact(names)
}

// mustParseFamilies parses Prometheus text for the check tests.
func mustParseFamilies(t *testing.T, text string) map[string]*io_prometheus_client.MetricFamily {
	t.Helper()
	families, err := parseMetricFamilies(text)
	require.NoError(t, err)
	return families
}

// TestSelectedGPUs checks the selection never keeps only a prefix of the GPUs.
func TestSelectedGPUs(t *testing.T) {
	tests := []struct {
		gpus []string
		want []string
	}{
		{nil, nil},
		{[]string{"0"}, nil},
		{[]string{"0", "1"}, []string{"1"}},
		{[]string{"0", "1", "2"}, []string{"1", "2"}},
		{[]string{"0", "1", "2", "3", "4", "5", "6", "7"}, []string{"1", "7"}},
	}

	for _, tt := range tests {
		assert.Equalf(t, tt.want, selectedGPUs(tt.gpus), "selectedGPUs(%v)", tt.gpus)
	}
}

// TestLabelValuesSortsNumbersNumerically checks GPU IDs come back as 2, 10,
// not 10, 2, and each value once.
func TestLabelValuesSortsNumbersNumerically(t *testing.T) {
	families := mustParseFamilies(t, `DCGM_A{gpu="10"} 1
DCGM_A{gpu="2"} 1
DCGM_B{gpu="2"} 1
DCGM_B{gpu="0"} 1
`)

	assert.Equal(t, []string{"0", "2", "10"}, labelValues(families, "gpu"))
}

// TestOnlySelectedGPUProblems checks each way a run can differ from the
// baseline's selected GPUs.
func TestOnlySelectedGPUProblems(t *testing.T) {
	const base = `DCGM_A{gpu="0",UUID="GPU-a"} 1
DCGM_A{gpu="1",UUID="GPU-b"} 1
DCGM_A{gpu="2",UUID="GPU-c"} 1
DCGM_DRIVER{driver="1"} 1
`
	tests := []struct {
		name string
		run  string
		want string
	}{
		{"only the selected GPU", "DCGM_A{gpu=\"1\",UUID=\"GPU-b\"} 1\nDCGM_DRIVER{driver=\"1\"} 1\n", ""},
		{
			"an extra GPU", "DCGM_A{gpu=\"0\",UUID=\"GPU-a\"} 1\nDCGM_A{gpu=\"1\",UUID=\"GPU-b\"} 1\nDCGM_DRIVER{driver=\"1\"} 1\n",
			"DCGM_A series per GPU",
		},
		{"a missing selected GPU", "DCGM_DRIVER{driver=\"1\"} 1\n", "DCGM_A series per GPU"},
		{"a changed UUID", "DCGM_A{gpu=\"1\",UUID=\"GPU-z\"} 1\nDCGM_DRIVER{driver=\"1\"} 1\n", "DCGM_A UUIDs per GPU"},
		{
			"a family without a gpu label changed", "DCGM_A{gpu=\"1\",UUID=\"GPU-b\"} 1\nDCGM_DRIVER{driver=\"1\",extra=\"x\"} 1\n",
			"DCGM_DRIVER has no gpu label and changed",
		},
		{"a dropped label", "DCGM_A{gpu=\"1\"} 1\nDCGM_DRIVER{driver=\"1\"} 1\n", "DCGM_A labels per GPU"},
		{
			"an added label", "DCGM_A{gpu=\"1\",UUID=\"GPU-b\",modelName=\"x\"} 1\nDCGM_DRIVER{driver=\"1\"} 1\n",
			"DCGM_A labels per GPU",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := onlySelectedGPUProblems(mustParseFamilies(t, base), mustParseFamilies(t, tt.run), []string{"1"})
			if tt.want == "" {
				assert.Empty(t, problems)
				return
			}

			// A wrong GPU set also changes the UUIDs per GPU, so look for the named problem.
			assert.Truef(t, slices.ContainsFunc(problems, func(problem string) bool {
				return strings.Contains(problem, tt.want)
			}), "problems %q do not include %q", problems, tt.want)
		})
	}
}
