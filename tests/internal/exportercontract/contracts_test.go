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

package exportercontract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

const baselineMetrics = `
# HELP DCGM_FI_DEV_SM_CLOCK_HERTZ SM clock.
# TYPE DCGM_FI_DEV_SM_CLOCK_HERTZ gauge
DCGM_FI_DEV_SM_CLOCK_HERTZ{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 210000000
# HELP DCGM_FI_DEV_MEMORY_CLOCK_HERTZ Memory clock.
# TYPE DCGM_FI_DEV_MEMORY_CLOCK_HERTZ gauge
DCGM_FI_DEV_MEMORY_CLOCK_HERTZ{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 405000000
# HELP DCGM_FI_DEV_MEMORY_TEMP_CELSIUS Memory temperature.
# TYPE DCGM_FI_DEV_MEMORY_TEMP_CELSIUS gauge
DCGM_FI_DEV_MEMORY_TEMP_CELSIUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_GPU_TEMP_CELSIUS GPU temperature.
# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge
DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 41
# HELP DCGM_FI_DEV_BOARD_POWER_WATTS Power usage.
# TYPE DCGM_FI_DEV_BOARD_POWER_WATTS gauge
DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 77
# HELP DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL Total energy.
# TYPE DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL counter
DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 196764
# HELP DCGM_FI_DEV_PCIE_REPLAY_TOTAL PCIe replay counter.
# TYPE DCGM_FI_DEV_PCIE_REPLAY_TOTAL counter
DCGM_FI_DEV_PCIE_REPLAY_TOTAL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_GPU_UTIL_RATIO GPU util.
# TYPE DCGM_FI_DEV_GPU_UTIL_RATIO gauge
DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_MEMORY_UTIL_RATIO Memory copy util.
# TYPE DCGM_FI_DEV_MEMORY_UTIL_RATIO gauge
DCGM_FI_DEV_MEMORY_UTIL_RATIO{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0.37
# HELP DCGM_FI_DEV_ENC_UTIL_RATIO Encoder util.
# TYPE DCGM_FI_DEV_ENC_UTIL_RATIO gauge
DCGM_FI_DEV_ENC_UTIL_RATIO{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_DEC_UTIL_RATIO Decoder util.
# TYPE DCGM_FI_DEV_DEC_UTIL_RATIO gauge
DCGM_FI_DEV_DEC_UTIL_RATIO{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_FB_FREE_BYTES FB free.
# TYPE DCGM_FI_DEV_FB_FREE_BYTES gauge
DCGM_FI_DEV_FB_FREE_BYTES{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 6924140544
# HELP DCGM_FI_DEV_FB_USED_BYTES FB used.
# TYPE DCGM_FI_DEV_FB_USED_BYTES gauge
DCGM_FI_DEV_FB_USED_BYTES{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 1073741824
# HELP DCGM_FI_DEV_VGPU_LICENSE_STATUS vGPU license status.
# TYPE DCGM_FI_DEV_VGPU_LICENSE_STATUS gauge
DCGM_FI_DEV_VGPU_LICENSE_STATUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
`

const customMetrics = `
# HELP DCGM_FI_DEV_GPU_TEMP_CELSIUS GPU temperature.
# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge
DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload",DCGM_FI_DRIVER_VERSION="580.105.08"} 41
# HELP DCGM_FI_DEV_FB_USED_BYTES FB used.
# TYPE DCGM_FI_DEV_FB_USED_BYTES gauge
DCGM_FI_DEV_FB_USED_BYTES{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload",DCGM_FI_DRIVER_VERSION="580.105.08"} 1024
`

// TestBaselineMetricsAcceptsDefaultExporterMetrics accepts the baseline GPU families with host and workload labels.
func TestBaselineMetricsAcceptsDefaultExporterMetrics(t *testing.T) {
	require.NoError(t, BaselineMetrics([]byte(baselineMetrics), BaselineOptions{
		RequireHostname:         true,
		RequireKubernetesLabels: true,
		KubernetesLabels:        kubernetesLabels(),
	}))
}

// TestBaselineMetricsExactCSVRejectsFamiliesOutsideTheCSV checks that an exact
// CSV run fails a DCGM_FI_* family that is not a CSV row, and that other runs
// accept it.
func TestBaselineMetricsExactCSVRejectsFamiliesOutsideTheCSV(t *testing.T) {
	families, err := metriccontract.ParseText([]byte(baselineMetrics))
	require.NoError(t, err)

	rows := []metriccontract.DefaultCounterRow{}
	for name, family := range families {
		if name != "DCGM_FI_DEV_GPU_TEMP_CELSIUS" {
			rows = append(rows, metriccontract.DefaultCounterRow{Name: name, Type: family.GetType(), Help: family.GetHelp()})
		}
	}
	opts := BaselineOptions{
		RequireHostname:         true,
		RequireKubernetesLabels: true,
		KubernetesLabels:        kubernetesLabels(),
		DefaultCounterRows:      rows,
	}
	require.NoError(t, BaselineMetrics([]byte(baselineMetrics), opts))

	opts.ExactCSV = true
	require.ErrorContains(t, BaselineMetrics([]byte(baselineMetrics), opts),
		`metric family "DCGM_FI_DEV_GPU_TEMP_CELSIUS" is not a metric row of the CSV`)
}

// TestBaselineMetricsRejectsMalformedDCGMSamples rejects a baseline scrape
// whose DCGM samples carry a DCGM unavailable sentinel, exceed a documented
// bound, or repeat a label set.
func TestBaselineMetricsRejectsMalformedDCGMSamples(t *testing.T) {
	const temperatureSample = `DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 41`
	const ratioSample = `DCGM_FI_DEV_MEMORY_UTIL_RATIO{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0.37`
	tests := []struct {
		name    string
		metrics string
		wantErr string
	}{
		{
			name:    "unavailable sentinel",
			metrics: strings.Replace(baselineMetrics, temperatureSample, strings.TrimSuffix(temperatureSample, " 41")+" 2147483632", 1),
			wantErr: "sentinel",
		},
		{
			name:    "ratio above one",
			metrics: strings.Replace(baselineMetrics, ratioSample, strings.TrimSuffix(ratioSample, " 0.37")+" 37", 1),
			wantErr: "DCGM_FI_DEV_MEMORY_UTIL_RATIO",
		},
		{
			name:    "repeated label set",
			metrics: strings.Replace(baselineMetrics, temperatureSample, temperatureSample+"\n"+strings.TrimSuffix(temperatureSample, " 41")+" 42", 1),
			wantErr: "duplicate",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NotEqual(t, baselineMetrics, test.metrics, "fixture edit did not apply")
			err := BaselineMetrics([]byte(test.metrics), BaselineOptions{
				RequireHostname:         true,
				RequireKubernetesLabels: true,
				KubernetesLabels:        kubernetesLabels(),
			})
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

// TestNoHostnameMetricsRejectsHostnameLabels rejects host identity in a response configured without hostname labels.
func TestNoHostnameMetricsRejectsHostnameLabels(t *testing.T) {
	require.Error(t, NoHostnameMetrics([]byte(baselineMetrics)))
}

// TestCustomMetricsRequiresReplacementSet accepts the custom family set and rejects the default family set.
func TestCustomMetricsRequiresReplacementSet(t *testing.T) {
	require.NoError(t, CustomMetrics([]byte(customMetrics), kubernetesLabels()))
	require.Error(t, CustomMetrics([]byte(baselineMetrics), kubernetesLabels()))
}

// kubernetesLabels returns the exact workload identity used by metric fixtures.
func kubernetesLabels() map[string]string {
	return map[string]string{
		"pod":       "cuda-workload",
		"namespace": "dcgm-exporter",
		"container": "cuda-workload",
	}
}
