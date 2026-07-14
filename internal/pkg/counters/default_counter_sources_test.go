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

package counters

import (
	"encoding/csv"
	stdos "os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const nvlinkBandwidthTotalField = "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL"

var profilingTotalFields = []string{
	"DCGM_FI_PROF_SM_CYCLES_ELAPSED_TOTAL",
	"DCGM_FI_PROF_SM_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_MMA_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_DMMA_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_HMMA_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_IMMA_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_DFMA_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_PCIE_TX_BYTES_TOTAL",
	"DCGM_FI_PROF_PCIE_RX_BYTES_TOTAL",
	"DCGM_FI_PROF_INT_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_FP64_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_FP32_CYCLES_ACTIVE_TOTAL",
	"DCGM_FI_PROF_FP16_CYCLES_ACTIVE_TOTAL",
}

func TestNVLinkBandwidthTotalDefaultMetricType(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "default counters", path: "../../../etc/default-counters.csv"},
		{name: "DCP metrics included", path: "../../../etc/dcp-metrics-included.csv"},
		{name: "Helm metrics configmap", path: "../../../deployment/templates/metrics-configmap.yaml"},
		{name: "Helm values sample", path: "../../../deployment/values.yaml"},
		{name: "integration default counters", path: "../../../tests/host/testdata/default-counters.csv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			promType := findPromType(t, tt.path, nvlinkBandwidthTotalField)
			require.Equal(t, "gauge", promType)
		})
	}
}

func TestProfilingTotalFieldsAreCommentedOnly(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "default counters", path: "../../../etc/default-counters.csv"},
		{name: "DCP metrics included", path: "../../../etc/dcp-metrics-included.csv"},
		{name: "Helm metrics configmap", path: "../../../deployment/templates/metrics-configmap.yaml"},
		{name: "Helm values sample", path: "../../../deployment/values.yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertFieldsCommentedOnly(t, tt.path, profilingTotalFields)
		})
	}
}

func assertFieldsCommentedOnly(t *testing.T, path string, fieldNames []string) {
	t.Helper()

	data, err := stdos.ReadFile(path)
	require.NoError(t, err)

	foundCommented := make(map[string]bool, len(fieldNames))
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		commented := strings.HasPrefix(trimmed, "#")
		metricLine := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))

		for _, fieldName := range fieldNames {
			if strings.HasPrefix(trimmed, fieldName+",") {
				t.Fatalf("%s has active default row for optional profiling total field %s", path, fieldName)
			}
			if commented && strings.HasPrefix(metricLine, fieldName+",") {
				foundCommented[fieldName] = true
			}
		}
	}

	for _, fieldName := range fieldNames {
		require.Truef(t, foundCommented[fieldName], "%s should document %s as a commented optional row", path, fieldName)
	}
}

func findPromType(t *testing.T, path string, fieldName string) string {
	t.Helper()

	data, err := stdos.ReadFile(path)
	require.NoError(t, err)

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "#")
		trimmed = strings.TrimSpace(trimmed)

		if !strings.HasPrefix(trimmed, fieldName+",") {
			continue
		}

		record, err := csv.NewReader(strings.NewReader(trimmed)).Read()
		require.NoError(t, err)
		require.Len(t, record, 3)

		return strings.TrimSpace(record[1])
	}

	t.Fatalf("field %s not found in %s", fieldName, path)
	return ""
}
