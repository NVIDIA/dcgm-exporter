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
	powerLimitRequestedField = "DCGM_FI_DEV_BOARD_POWER_LIMIT_REQUESTED_WATTS"
	powerLimitEnforcedField  = "DCGM_FI_DEV_BOARD_POWER_LIMIT_ENFORCED_WATTS"
	pageRetiredPendingField  = "DCGM_FI_DEV_PAGE_RETIRED_PENDING"
	nvlinkThroughputField    = "DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND"
	framebufferCapacityField = "DCGM_FI_DEV_FB_CAPACITY_BYTES"

	powerLimitRequestedHelp = "Configured board power limit (in W)."
	powerLimitEnforcedHelp  = "Effective board power limit enforced by the driver after all limiters (in W)."
	pageRetiredPendingHelp  = "Whether pages are pending retirement (1 means pending and 0 means not pending)."
	nvlinkThroughputHelp    = "Total NVLink throughput across all lanes in bytes per second."
	framebufferCapacityHelp = "Total framebuffer capacity in bytes."
)

// metricMetadata holds the type, help text, and comment state for one metric field.
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

// TestShippedMetricMetadata expects selected fields in the default and DCP CSVs to have
// the listed type, help text and commented-out state.
func TestShippedMetricMetadata(t *testing.T) {
	expected := map[string]metricMetadata{
		powerLimitRequestedField: {promType: "gauge", help: powerLimitRequestedHelp, commented: true},
		powerLimitEnforcedField:  {promType: "gauge", help: powerLimitEnforcedHelp, commented: true},
		pageRetiredPendingField:  {promType: "gauge", help: pageRetiredPendingHelp, commented: true},
		nvlinkThroughputField:    {promType: "gauge", help: nvlinkThroughputHelp},
		framebufferCapacityField: {promType: "gauge", help: framebufferCapacityHelp},
	}

	for _, path := range []string{
		"../../../etc/default-counters.csv",
		"../../../etc/dcp-metrics-included.csv",
	} {
		t.Run(path, func(t *testing.T) {
			for fieldName, want := range expected {
				t.Run(fieldName, func(t *testing.T) {
					require.Equal(t, want, findMetricMetadata(t, path, fieldName))
				})
			}
		})
	}
}

// renamedDefaultMetrics lists the deprecated DCGM fields whose current names
// the shipped profiles enable. The deprecated rows are no longer shipped.
var renamedDefaultMetrics = []struct {
	oldName, oldType, oldHelp string
	newName, newType, newHelp string
}{
	{
		"DCGM_FI_DEV_SM_CLOCK", "gauge", "SM clock frequency (in MHz).",
		"DCGM_FI_DEV_SM_CLOCK_HERTZ", "gauge", "SM clock frequency in hertz.",
	},
	{
		"DCGM_FI_DEV_MEM_CLOCK", "gauge", "Memory clock frequency (in MHz).",
		"DCGM_FI_DEV_MEMORY_CLOCK_HERTZ", "gauge", "Memory clock frequency in hertz.",
	},
	{
		"DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", "counter", "Total energy consumption since the driver was last reloaded (in mJ).",
		"DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL", "counter", "GPU energy consumed since the driver was reloaded in joules.",
	},
	{
		"DCGM_FI_DEV_GPU_UTIL", "gauge", "GPU utilization (in %).",
		"DCGM_FI_DEV_GPU_UTIL_RATIO", "gauge", "GPU utilization as a ratio.",
	},
	{
		"DCGM_FI_DEV_MEM_COPY_UTIL", "gauge", "Memory utilization (in %).",
		"DCGM_FI_DEV_MEMORY_UTIL_RATIO", "gauge", "Memory utilization as a ratio.",
	},
	{
		"DCGM_FI_DEV_ENC_UTIL", "gauge", "Encoder utilization (in %).",
		"DCGM_FI_DEV_ENC_UTIL_RATIO", "gauge", "Encoder utilization as a ratio.",
	},
	{
		"DCGM_FI_DEV_DEC_UTIL", "gauge", "Decoder utilization (in %).",
		"DCGM_FI_DEV_DEC_UTIL_RATIO", "gauge", "Decoder utilization as a ratio.",
	},
	{
		"DCGM_FI_DEV_FB_FREE", "gauge", "Framebuffer memory free (in MiB).",
		"DCGM_FI_DEV_FB_FREE_BYTES", "gauge", "Free framebuffer memory in bytes.",
	},
	{
		"DCGM_FI_DEV_FB_USED", "gauge", "Framebuffer memory used (in MiB).",
		"DCGM_FI_DEV_FB_USED_BYTES", "gauge", "Used framebuffer memory in bytes.",
	},
	{
		"DCGM_FI_DEV_FB_RESERVED", "gauge", "Framebuffer memory reserved (in MiB).",
		"DCGM_FI_DEV_FB_RESERVED_BYTES", "gauge", "Reserved framebuffer memory in bytes.",
	},
	{
		"DCGM_FI_DEV_MEMORY_TEMP", "gauge", "Memory temperature (in C).",
		"DCGM_FI_DEV_MEMORY_TEMP_CELSIUS", "gauge", "Memory temperature (in C).",
	},
	{
		"DCGM_FI_DEV_GPU_TEMP", "gauge", "GPU temperature (in C).",
		"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "gauge", "GPU temperature (in C).",
	},
	{
		"DCGM_FI_DEV_POWER_USAGE", "gauge", "Power draw (in W).",
		"DCGM_FI_DEV_BOARD_POWER_WATTS", "gauge", "Power draw (in W).",
	},
	{
		"DCGM_FI_DEV_PCIE_REPLAY_COUNTER", "counter", "Total number of PCIe retries.",
		"DCGM_FI_DEV_PCIE_REPLAY_TOTAL", "counter", "Total number of PCIe retries.",
	},
	{
		"DCGM_FI_DEV_XID_ERRORS", "gauge", "Value of the last XID error encountered.",
		"DCGM_FI_DEV_XID_ERROR", "gauge", "Value of the last XID error encountered.",
	},
	{
		"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", "counter", "Number of remapped rows for uncorrectable errors",
		"DCGM_FI_DEV_ROW_REMAP_UNCORRECTABLE_TOTAL", "counter", "Number of remapped rows for uncorrectable errors",
	},
	{
		"DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", "counter", "Number of remapped rows for correctable errors",
		"DCGM_FI_DEV_ROW_REMAP_CORRECTABLE_TOTAL", "counter", "Number of remapped rows for correctable errors",
	},
	{
		"DCGM_FI_DEV_ROW_REMAP_FAILURE", "gauge", "Whether remapping of rows has failed",
		"DCGM_FI_DEV_ROW_REMAP_FAILED", "gauge", "Whether remapping of rows has failed",
	},
	{
		"DCGM_FI_DRIVER_VERSION", "label", "Driver Version",
		"DCGM_FI_SYSTEM_DRIVER_VERSION", "label", "Driver Version",
	},
	{
		"DCGM_FI_PROF_GR_ENGINE_ACTIVE", "gauge", "Ratio of time the graphics engine is active.",
		"DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO", "gauge", "Ratio of time the graphics engine is active.",
	},
	{
		"DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", "gauge", "Ratio of cycles the tensor (HMMA) pipe is active.",
		"DCGM_FI_PROF_TENSOR_UTIL_RATIO", "gauge", "Ratio of cycles the tensor (HMMA) pipe is active.",
	},
	{
		"DCGM_FI_PROF_DRAM_ACTIVE", "gauge", "Ratio of cycles the device memory interface is active sending or receiving data.",
		"DCGM_FI_PROF_DRAM_UTIL_RATIO", "gauge", "Ratio of cycles the device memory interface is active sending or receiving data.",
	},
}

func TestShippedProfilesUseCurrentFieldNames(t *testing.T) {
	for _, path := range []string{
		"../../../etc/default-counters.csv",
		"../../../etc/dcp-metrics-included.csv",
	} {
		t.Run(path, func(t *testing.T) {
			data, err := stdos.ReadFile(path)
			require.NoError(t, err)

			for _, row := range renamedDefaultMetrics {
				require.Equal(t,
					metricMetadata{promType: row.newType, help: row.newHelp},
					findMetricMetadata(t, path, row.newName))
				require.NotRegexp(t, `(?m)^#?\s*`+row.oldName+`,`, string(data),
					"the deprecated %s row must not ship, even commented out", row.oldName)
			}
		})
	}
}

// unitChangedFields are the deprecated names whose replacement reports a
// different unit and therefore has a different DCGM field ID.
var unitChangedFields = map[string]bool{
	"DCGM_FI_DEV_SM_CLOCK": true, "DCGM_FI_DEV_MEM_CLOCK": true,
	"DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION": true, "DCGM_FI_DEV_GPU_UTIL": true,
	"DCGM_FI_DEV_MEM_COPY_UTIL": true, "DCGM_FI_DEV_ENC_UTIL": true,
	"DCGM_FI_DEV_DEC_UTIL": true, "DCGM_FI_DEV_FB_FREE": true,
	"DCGM_FI_DEV_FB_USED": true, "DCGM_FI_DEV_FB_RESERVED": true,
}

// TestDeprecatedFieldNamesStillResolve checks that a custom collectors file can
// keep a deprecated name. A name-only rename keeps its field ID; a unit change
// moves to a new field ID.
func TestDeprecatedFieldNamesStillResolve(t *testing.T) {
	for _, row := range renamedDefaultMetrics {
		if strings.HasPrefix(row.oldName, "DCGM_FI_PROF_") {
			continue // Profiling fields also need a DCP capability snapshot.
		}
		t.Run(row.oldName, func(t *testing.T) {
			oldSet, err := ExtractCounters([][]string{{row.oldName, row.oldType, row.oldHelp}}, DCPCapabilities{})
			require.NoError(t, err)
			newSet, err := ExtractCounters([][]string{{row.newName, row.newType, row.newHelp}}, DCPCapabilities{})
			require.NoError(t, err)
			require.Len(t, oldSet.DCGMCounters, 1)
			require.Len(t, newSet.DCGMCounters, 1)
			require.Equal(t, row.oldName, oldSet.DCGMCounters[0].FieldName)
			if unitChangedFields[row.oldName] {
				require.NotEqual(t, oldSet.DCGMCounters[0].FieldID, newSet.DCGMCounters[0].FieldID)
			} else {
				require.Equal(t, oldSet.DCGMCounters[0].FieldID, newSet.DCGMCounters[0].FieldID)
			}
		})
	}
}

func TestShippedNVLinkThroughputIsOn(t *testing.T) {
	for _, path := range []string{
		"../../../etc/default-counters.csv",
		"../../../etc/dcp-metrics-included.csv",
	} {
		t.Run(path, func(t *testing.T) {
			metadata := findMetricMetadata(t, path, nvlinkThroughputField)
			require.False(t, metadata.commented,
				"%s must keep %s enabled by default", path, nvlinkThroughputField)
			require.Equal(t, "gauge", metadata.promType,
				"%s must expose %s as a gauge", path, nvlinkThroughputField)
		})
	}
}

// TestLegacyCompatibilityMetricMetadata expects selected fields in the 1.x compatibility
// CSV to have the listed type, help text and commented-out state.
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

// TestProfilingTotalFieldsAreCommentedOnly expects the default and DCP CSVs to list each
// profiling total field as a commented-out row and never as an active one.
func TestProfilingTotalFieldsAreCommentedOnly(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "default counters", path: "../../../etc/default-counters.csv"},
		{name: "DCP metrics included", path: "../../../etc/dcp-metrics-included.csv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertFieldsCommentedOnly(t, tt.path, profilingTotalFields)
		})
	}
}

// TestFabricHealthMetricUsesSummary expects the default and DCP CSVs to list the fabric
// health summary as a commented-out gauge and not to mention the fabric health mask.
func TestFabricHealthMetricUsesSummary(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "default counters", path: "../../../etc/default-counters.csv"},
		{name: "DCP metrics included", path: "../../../etc/dcp-metrics-included.csv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := findMetricMetadata(t, tt.path, "DCGM_FI_DEV_FABRIC_HEALTH_SUMMARY")
			require.Equal(t, "gauge", metadata.promType)
			require.True(t, metadata.commented)

			data, err := stdos.ReadFile(tt.path)
			require.NoError(t, err)
			require.NotContains(t, string(data), "DCGM_FI_DEV_FABRIC_HEALTH_MASK")
		})
	}
}

// assertFieldsCommentedOnly checks that the requested fields appear only as commented rows.
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

// findMetricMetadata reads the type, help text, and comment state for one field.
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

// TestSystemHealthStatusDefaultSourcesMatch checks that the DCP metrics
// file documents DCGM_EXP_SYSTEM_HEALTH_STATUS as a commented row with the
// same type and help text as etc/default-counters.csv.
func TestSystemHealthStatusDefaultSourcesMatch(t *testing.T) {
	const defaultCountersPath = "../../../etc/default-counters.csv"

	expected := findMetricMetadata(t, defaultCountersPath, DCGMExpSystemHealthStatus)
	require.True(t, expected.commented)

	tests := []struct {
		name string
		path string
	}{
		{name: "DCP metrics included", path: "../../../etc/dcp-metrics-included.csv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := findMetricMetadata(t, tt.path, DCGMExpSystemHealthStatus)
			require.True(t, actual.commented)
			require.Equalf(
				t,
				expected,
				actual,
				"%s must match %s for %s",
				tt.path,
				defaultCountersPath,
				DCGMExpSystemHealthStatus,
			)
		})
	}
}
