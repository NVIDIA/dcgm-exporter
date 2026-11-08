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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// gpuLinkField is a GPU field DCGM also reads per NVLink. DCGM serves it on
// NVLink 5 and later; on older links it has no value and adds no series. The
// scenario adds it so the scrape has GPU link series where DCGM serves them,
// and checks the link part only when it does.
const gpuLinkField = "DCGM_FI_DEV_NVLINK_TX_PACKET_TOTAL"

// runModelNameLabelsEffect runs the exporter with the default model name and
// with --replace-blanks-in-model-name, and checks every model name label
// against the nvidia-smi name, and that the forms match each other. It skips
// when no GPU name has a blank.
func runModelNameLabelsEffect(t testing.TB) {
	base := startSettingBaseline(t)
	if reason := skipWithoutBlankInGPUName(base); reason != "" {
		t.Skip(reason)
	}

	activeLinks := hasActiveNVLink(t)
	runSettingForms(t, base, []settingForm{
		{
			name: "default model name",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return baselineArgs(modelNameCollectors(t, base, activeLinks), port)
			},
			check: modelNameCheck(activeLinks, func(name string) string { return name }),
		},
		{
			name: "--replace-blanks-in-model-name",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(modelNameCollectors(t, base, activeLinks), port), "--replace-blanks-in-model-name")
			},
			check: modelNameCheck(activeLinks, dashedModelName),
		},
	})
}

// hasActiveNVLink reports whether nvidia-smi shows an active NVLink. When
// nvidia-smi cannot report link state, it logs why and reports none, so only
// the GPU link part of the model name case is left out. If nvidia-smi returns
// output that the parser cannot read, the test fails.
func hasActiveNVLink(t testing.TB) bool {
	t.Helper()
	linkStatus, err := nvidiasmi.LocalRunner(context.Background(), "nvlink", "-s")
	if err != nil {
		t.Logf("no NVLink state from nvidia-smi, so GPU link series are not checked: %v", err)
		return false
	}
	active, err := nvidiasmi.ActiveNVLinks(string(linkStatus))
	require.NoErrorf(t, err, "parse nvidia-smi nvlink -s output:\n%s", linkStatus)

	return len(active) > 0
}

// modelNameCollectors is the baseline counter file, plus a GPU link field
// when the host has an active NVLink (activeLinks) so the scrape also has link series.
func modelNameCollectors(t testing.TB, base *settingBaseline, activeLinks bool) string {
	t.Helper()
	if !activeLinks {
		return base.collectors
	}

	contents, err := os.ReadFile(base.collectors)
	require.NoError(t, err)

	row := fmt.Sprintf("\n%s, counter, NVLink packets sent per link.\n", gpuLinkField)
	path := filepath.Join(t.TempDir(), "model-name-counters.csv")
	// #nosec G703 -- path is in this test's own temporary directory.
	require.NoError(t, os.WriteFile(path, append(contents, row...), 0o600))
	return path
}

// skipWithoutBlankInGPUName returns a reason when no GPU name on the host has
// a blank, because then the setting cannot change any label.
func skipWithoutBlankInGPUName(base *settingBaseline) string {
	for _, gpu := range base.inventory {
		if strings.ContainsAny(gpu.Name, " \t") {
			return ""
		}
	}
	return "no GPU name reported by nvidia-smi has a blank, so replacing blanks changes nothing"
}

// dashedModelName is the documented form of a model name with
// --replace-blanks-in-model-name: each run of blanks becomes one dash.
func dashedModelName(name string) string {
	return strings.Join(strings.Fields(name), "-")
}

// modelNameCheck returns a check that every model name label equals expected
// applied to the nvidia-smi name of its GPU. GPU series must carry modelName.
// GPU link series carry model_name where DCGM serves them; the check logs
// when activeLinks is set and there are none.
func modelNameCheck(activeLinks bool, expected func(string) string) func(testing.TB, *settingBaseline, settingRun) {
	return func(t testing.TB, base *settingBaseline, run settingRun) {
		t.Helper()
		counts, problems := modelNameProblems(run.families, inventoryNames(base.inventory), expected)
		require.Empty(t, problems, "model name labels")
		require.Positive(t, counts["modelName"], "no GPU series carries modelName")

		// Both forms serve the same series, so link series in one form are in the other.
		if activeLinks && counts["model_name"] == 0 {
			t.Logf("nvidia-smi shows an active NVLink but DCGM serves no GPU link series of %s; "+
				"model_name on GPU link series is not checked on this host", gpuLinkField)
		}
	}
}

// modelNameLabels are the label names that carry the GPU model: modelName on
// GPU series and model_name on GPU link series.
var modelNameLabels = []string{"modelName", "model_name"}

// uuidLabels are the label names that carry the GPU UUID next to a model name.
var uuidLabels = []string{"UUID", "gpu_uuid"}

// modelNameProblems checks every model name label in families equals expected
// applied to the name of its GPU in names (keyed by upper-case UUID), without
// regard to case. It returns how many labels of each name it checked and
// every mismatch.
func modelNameProblems(
	families map[string]*io_prometheus_client.MetricFamily, names map[string]string, expected func(string) string,
) (map[string]int, []string) {
	counts := map[string]int{}
	var problems []string
	for _, familyName := range sortedFamilyNames(families) {
		for _, metric := range families[familyName].GetMetric() {
			labels := labelMap(metric)
			for _, labelName := range modelNameLabels {
				value, ok := labels[labelName]
				if !ok {
					continue
				}

				counts[labelName]++
				uuid := firstLabel(labels, uuidLabels)
				smiName, known := names[strings.ToUpper(uuid)]
				switch {
				case !known:
					problems = append(problems, fmt.Sprintf("%s %s=%q has GPU UUID %q, which nvidia-smi does not report",
						familyName, labelName, value, uuid))
				case !strings.EqualFold(expected(smiName), value):
					problems = append(problems, fmt.Sprintf("%s %s=%q for GPU %s; want %q from the nvidia-smi name %q",
						familyName, labelName, value, uuid, expected(smiName), smiName))
				}
			}
		}
	}
	return counts, problems
}

// inventoryNames maps each GPU UUID, in upper case, to its nvidia-smi name.
func inventoryNames(inventory []nvidiasmi.GPU) map[string]string {
	names := make(map[string]string, len(inventory))
	for _, gpu := range inventory {
		names[strings.ToUpper(gpu.UUID)] = gpu.Name
	}
	return names
}

// TestModelNameProblems checks the default and dashed model names on GPU and
// GPU link series, and a series whose GPU nvidia-smi does not report.
func TestModelNameProblems(t *testing.T) {
	names := inventoryNames(nil)
	names["GPU-A"] = "NVIDIA  B200 SXM"
	identity := func(name string) string { return name }

	tests := []struct {
		name       string
		text       string
		expected   func(string) string
		wantCounts map[string]int
		want       string
	}{
		{
			"default keeps the name", `DCGM_A{UUID="GPU-a",modelName="NVIDIA  B200 SXM"} 1` + "\n", identity,
			map[string]int{"modelName": 1},
			"",
		},
		{
			"dashed on GPU and link series", `DCGM_A{UUID="GPU-a",modelName="NVIDIA-B200-SXM"} 1` + "\n" +
				`DCGM_L{gpu_uuid="GPU-a",model_name="nvidia-b200-sxm"} 1` + "\n", dashedModelName,
			map[string]int{"modelName": 1, "model_name": 1},
			"",
		},
		{
			"link series keeps blanks", `DCGM_A{UUID="GPU-a",modelName="NVIDIA-B200-SXM"} 1` + "\n" +
				`DCGM_L{gpu_uuid="GPU-a",model_name="NVIDIA  B200 SXM"} 1` + "\n", dashedModelName,
			map[string]int{"modelName": 1, "model_name": 1},
			`DCGM_L model_name="NVIDIA  B200 SXM"`,
		},
		{
			"default changed the name", `DCGM_A{UUID="GPU-a",modelName="NVIDIA-B200-SXM"} 1` + "\n", identity,
			map[string]int{"modelName": 1},
			"want \"NVIDIA  B200 SXM\"",
		},
		{
			"unknown UUID", `DCGM_A{UUID="GPU-x",modelName="NVIDIA B200"} 1` + "\n", identity,
			map[string]int{"modelName": 1},
			"which nvidia-smi does not report",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counts, problems := modelNameProblems(mustParseFamilies(t, tt.text), names, tt.expected)
			assert.Equal(t, tt.wantCounts, counts)
			if tt.want == "" {
				assert.Empty(t, problems)
				return
			}

			require.Len(t, problems, 1)
			assert.Truef(t, strings.Contains(problems[0], tt.want), "%q does not contain %q", problems[0], tt.want)
		})
	}
}

// TestDashedModelName checks each run of blanks becomes one dash.
func TestDashedModelName(t *testing.T) {
	assert.Equal(t, "NVIDIA-B200-SXM", dashedModelName(" NVIDIA  B200\tSXM "))
	assert.Equal(t, "B200", dashedModelName("B200"))
}
