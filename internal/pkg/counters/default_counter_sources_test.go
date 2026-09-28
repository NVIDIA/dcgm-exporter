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

const (
	totalEnergyConsumptionField = "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION"
	powerManagementLimitField   = "DCGM_FI_DEV_POWER_MGMT_LIMIT"
	enforcedPowerLimitField     = "DCGM_FI_DEV_ENFORCED_POWER_LIMIT"
	retiredPendingField         = "DCGM_FI_DEV_RETIRED_PENDING"
	nvlinkBandwidthTotalField   = "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL"
	nvlinkBandwidthL0Field      = "DCGM_FI_DEV_NVLINK_BANDWIDTH_L0"

	totalEnergyConsumptionHelp = "Total energy consumption since the driver was last reloaded (in mJ)."
	powerManagementLimitHelp   = "Configured power management limit (in W)."
	enforcedPowerLimitHelp     = "Effective power limit enforced by the driver after all limiters (in W)."
	retiredPendingHelp         = "Whether pages are pending retirement (1 means pending and 0 means not pending)."
	nvlinkBandwidthTotalHelp   = "Aggregate NVLink throughput across all lanes (in MB/s)."
	nvlinkBandwidthL0Help      = "NVLink lane 0 throughput (in MB/s)."

	nvlinkBandwidthTotalRow       = nvlinkBandwidthTotalField + ", gauge, " + nvlinkBandwidthTotalHelp
	commentedNVLinkBandwidthL0Row = "# " + nvlinkBandwidthL0Field + ", gauge, " + nvlinkBandwidthL0Help
)

type metricMetadata struct {
	promType  string
	help      string
	commented bool
}

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

func TestShippedMetricMetadata(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected map[string]metricMetadata
	}{
		{
			name: "default counters",
			path: "../../../etc/default-counters.csv",
			expected: map[string]metricMetadata{
				totalEnergyConsumptionField: {promType: "counter", help: totalEnergyConsumptionHelp},
				powerManagementLimitField:   {promType: "gauge", help: powerManagementLimitHelp, commented: true},
				enforcedPowerLimitField:     {promType: "gauge", help: enforcedPowerLimitHelp, commented: true},
				retiredPendingField:         {promType: "gauge", help: retiredPendingHelp, commented: true},
				nvlinkBandwidthTotalField:   {promType: "gauge", help: nvlinkBandwidthTotalHelp},
				nvlinkBandwidthL0Field:      {promType: "gauge", help: nvlinkBandwidthL0Help, commented: true},
			},
		},
		{
			name: "DCP metrics included",
			path: "../../../etc/dcp-metrics-included.csv",
			expected: map[string]metricMetadata{
				totalEnergyConsumptionField: {promType: "counter", help: totalEnergyConsumptionHelp},
				powerManagementLimitField:   {promType: "gauge", help: powerManagementLimitHelp, commented: true},
				enforcedPowerLimitField:     {promType: "gauge", help: enforcedPowerLimitHelp, commented: true},
				retiredPendingField:         {promType: "gauge", help: retiredPendingHelp, commented: true},
				nvlinkBandwidthTotalField:   {promType: "gauge", help: nvlinkBandwidthTotalHelp},
				nvlinkBandwidthL0Field:      {promType: "gauge", help: nvlinkBandwidthL0Help, commented: true},
			},
		},
		{
			name: "Helm metrics configmap",
			path: "../../../deployment/templates/metrics-configmap.yaml",
			expected: map[string]metricMetadata{
				totalEnergyConsumptionField: {promType: "counter", help: totalEnergyConsumptionHelp},
				powerManagementLimitField:   {promType: "gauge", help: powerManagementLimitHelp, commented: true},
				enforcedPowerLimitField:     {promType: "gauge", help: enforcedPowerLimitHelp, commented: true},
				retiredPendingField:         {promType: "gauge", help: retiredPendingHelp, commented: true},
				nvlinkBandwidthTotalField:   {promType: "gauge", help: nvlinkBandwidthTotalHelp},
				nvlinkBandwidthL0Field:      {promType: "gauge", help: nvlinkBandwidthL0Help, commented: true},
			},
		},
		{
			name: "Helm values sample",
			path: "../../../deployment/values.yaml",
			expected: map[string]metricMetadata{
				totalEnergyConsumptionField: {promType: "counter", help: totalEnergyConsumptionHelp, commented: true},
				powerManagementLimitField:   {promType: "gauge", help: powerManagementLimitHelp, commented: true},
				enforcedPowerLimitField:     {promType: "gauge", help: enforcedPowerLimitHelp, commented: true},
				retiredPendingField:         {promType: "gauge", help: retiredPendingHelp, commented: true},
				nvlinkBandwidthTotalField:   {promType: "gauge", help: nvlinkBandwidthTotalHelp, commented: true},
				nvlinkBandwidthL0Field:      {promType: "gauge", help: nvlinkBandwidthL0Help, commented: true},
			},
		},
		{
			name: "host integration fixture",
			path: "../../../tests/host/testdata/default-counters.csv",
			expected: map[string]metricMetadata{
				totalEnergyConsumptionField: {promType: "counter", help: totalEnergyConsumptionHelp},
				powerManagementLimitField:   {promType: "gauge", help: powerManagementLimitHelp, commented: true},
				enforcedPowerLimitField:     {promType: "gauge", help: enforcedPowerLimitHelp, commented: true},
				retiredPendingField:         {promType: "gauge", help: retiredPendingHelp, commented: true},
				nvlinkBandwidthTotalField:   {promType: "gauge", help: nvlinkBandwidthTotalHelp},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for fieldName, expected := range tt.expected {
				t.Run(fieldName, func(t *testing.T) {
					require.Equal(t, expected, findMetricMetadata(t, tt.path, fieldName))
				})
			}
		})
	}
}

func TestShippedNVLinkBandwidthRowsUseCanonicalSpacing(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected []string
	}{
		{
			name: "default counters",
			path: "../../../etc/default-counters.csv",
			expected: []string{
				nvlinkBandwidthTotalRow,
				commentedNVLinkBandwidthL0Row,
			},
		},
		{
			name: "DCP metrics included",
			path: "../../../etc/dcp-metrics-included.csv",
			expected: []string{
				nvlinkBandwidthTotalRow,
				commentedNVLinkBandwidthL0Row,
			},
		},
		{
			name: "Helm metrics configmap",
			path: "../../../deployment/templates/metrics-configmap.yaml",
			expected: []string{
				nvlinkBandwidthTotalRow,
				commentedNVLinkBandwidthL0Row,
			},
		},
		{
			name: "Helm values sample",
			path: "../../../deployment/values.yaml",
			expected: []string{
				"# " + nvlinkBandwidthTotalRow,
				commentedNVLinkBandwidthL0Row,
			},
		},
		{
			name: "host integration fixture",
			path: "../../../tests/host/testdata/default-counters.csv",
			expected: []string{
				nvlinkBandwidthTotalRow,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := stdos.ReadFile(tt.path)
			require.NoError(t, err)

			lines := strings.Split(string(data), "\n")
			for i := range lines {
				lines[i] = strings.TrimSpace(lines[i])
			}

			for _, expected := range tt.expected {
				require.Contains(t, lines, expected)
			}
		})
	}
}

func TestLegacyCompatibilityMetricMetadata(t *testing.T) {
	const path = "../../../etc/1.x-compatibility-metrics.csv"

	expected := map[string]metricMetadata{
		"dcgm_total_energy_consumption": {
			promType: "counter",
			help:     "Total energy consumption since boot (in mJ).",
		},
		"dcgm_retired_pages_pending": {
			promType:  "counter",
			help:      "Total number of pages pending retirement.",
			commented: true,
		},
		"dcgm_nvlink_bandwidth_total": {
			promType: "counter",
			help:     "Total number of NVLink bandwidth counters for all lanes",
		},
	}

	for fieldName, want := range expected {
		t.Run(fieldName, func(t *testing.T) {
			require.Equal(t, want, findMetricMetadata(t, path, fieldName))
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

func TestFabricHealthOptionalMetricTypes(t *testing.T) {
	fields := []string{
		"DCGM_FI_DEV_FABRIC_HEALTH_SUMMARY",
		"DCGM_FI_DEV_FABRIC_HEALTH_MASK",
	}

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
		for _, field := range fields {
			t.Run(tt.name+"/"+field, func(t *testing.T) {
				metadata := findMetricMetadata(t, tt.path, field)
				require.Equal(t, "gauge", metadata.promType)
				require.True(t, metadata.commented)
			})
		}
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

func findMetricMetadata(t *testing.T, path string, fieldName string) metricMetadata {
	t.Helper()

	data, err := stdos.ReadFile(path)
	require.NoError(t, err)

	var matches []metricMetadata
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		commented := strings.HasPrefix(trimmed, "#")
		trimmed = strings.TrimPrefix(trimmed, "#")
		trimmed = strings.TrimSpace(trimmed)

		if !strings.HasPrefix(trimmed, fieldName+",") {
			continue
		}

		record, err := csv.NewReader(strings.NewReader(trimmed)).Read()
		require.NoError(t, err)
		require.Len(t, record, 3)

		matches = append(matches, metricMetadata{
			promType:  strings.TrimSpace(record[1]),
			help:      strings.TrimSpace(record[2]),
			commented: commented,
		})
	}

	require.Lenf(t, matches, 1, "field %s should appear exactly once in %s", fieldName, path)
	return matches[0]
}
