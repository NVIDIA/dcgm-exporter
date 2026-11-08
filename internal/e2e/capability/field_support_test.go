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

package capability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// dmonRunner answers dcgmi dmon commands by their -e field list and records
// every list it was asked for.
type dmonRunner struct {
	results map[string]e2eexec.Result
	asked   []string
}

// Run returns the result stored for the command's field list, or a failed
// command when there is none.
func (r *dmonRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	fields := ""
	for i, arg := range command.Args {
		if arg == "-e" && i+1 < len(command.Args) {
			fields = command.Args[i+1]
		}
	}
	r.asked = append(r.asked, fields)

	if result, ok := r.results[fields]; ok {
		return result
	}
	return e2eexec.Result{ExitCode: 1, Stderr: []byte("no fixture for fields " + fields)}
}

// dmonOutput returns a successful command result with the given output.
func dmonOutput(text string) e2eexec.Result {
	return e2eexec.Result{Stdout: []byte(text)}
}

// TestProbeFieldSupportClassifiesDmonColumns checks supported, blank, N/A and
// sentinel columns across GPUs and samples, including a string label field.
func TestProbeFieldSupportClassifiesDmonColumns(t *testing.T) {
	runner := &dmonRunner{results: map[string]e2eexec.Result{
		"1,150,1648,393": dmonOutput(`#Entity   DRVER       TMPTR  NVLTP  RRUNC
ID
GPU 0     580.82.07   35     N/A    N/A
GPU 1     580.82.07   36     N/A    2147483632
GPU 0     580.82.07   35     N/A    N/A
GPU 1     580.82.07   36     N/A    N/A
`),
		"1009": dmonOutput("#Entity PCITX\nID\nGPU 0   N/A\nGPU 1   N/A\nGPU 0   1234\n"),
	}}

	got := ProbeFieldSupport(context.Background(), runner, ProbeOptions{HostDCGMAddress: "localhost:5555"}, []DCGMField{
		{Name: "DCGM_FI_SYSTEM_DRIVER_VERSION", ID: 1},
		{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", ID: 150},
		{Name: "DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND", ID: 1648},
		{Name: "DCGM_FI_DEV_ROW_REMAP_UNCORRECTABLE_TOTAL", ID: 393},
		{Name: "DCGM_FI_PROF_PCIE_TX_BYTES", ID: 1009},
	})

	require.Equal(t, []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_PROF_PCIE_TX_BYTES", "DCGM_FI_SYSTEM_DRIVER_VERSION"}, got.Supported)
	require.Len(t, got.Unsupported, 2)
	require.Equal(t,
		"DCGM returned no value (N/A) on every GPU during the probe (dcgmi dmon -e 1,150,1648,393, field 1648, 3 samples, 4 GPU sample lines)",
		got.Unsupported["DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND"])
	require.Contains(t, got.Unsupported, "DCGM_FI_DEV_ROW_REMAP_UNCORRECTABLE_TOTAL")
	require.Empty(t, got.Unclassified)
	require.Equal(t, []string{"1,150,1648,393", "1009"}, runner.asked)
}

// TestProbeFieldSupportSplitsARefusedBatch checks that a batch DCGM refuses is
// probed one field at a time, so one refused field does not hide the others,
// and that profiling fields are probed apart from device fields.
func TestProbeFieldSupportSplitsARefusedBatch(t *testing.T) {
	refused := e2eexec.Result{ExitCode: 1, Stdout: []byte("Error setting watches. Result: The requested operation is not supported on this device.")}
	runner := &dmonRunner{results: map[string]e2eexec.Result{
		"150,526": refused,
		"150":     dmonOutput("#Entity TMPTR\nID\nGPU 0   35\n"),
		"526":     refused,
		"1001":    {ExitCode: 1, Stderr: []byte("Error: the profiling module could not be loaded")},
	}}

	got := ProbeFieldSupport(context.Background(), runner, ProbeOptions{HostDCGMAddress: "localhost:5555"}, []DCGMField{
		{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", ID: 150},
		{Name: "DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO", ID: 1001},
		{Name: "DCGM_FI_DEV_VGPU_LICENSE_STATUS", ID: 526},
	})

	require.Equal(t, []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"}, got.Supported)
	require.Equal(t, map[string]string{
		"DCGM_FI_DEV_VGPU_LICENSE_STATUS": "DCGM refused to watch field 526 (dcgmi dmon -e 526): " +
			"Error setting watches. Result: The requested operation is not supported on this device.",
	}, got.Unsupported)
	require.Contains(t, got.Unclassified["DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO"], "DCGM field support probe was unavailable")
	require.Equal(t, []string{"150,526", "150", "526", "1001"}, runner.asked)
}

// TestProbeFieldSupportLeavesFailedProbesUnclassified checks that a probe
// that cannot run, or output that cannot be read, never classifies a field.
func TestProbeFieldSupportLeavesFailedProbesUnclassified(t *testing.T) {
	fields := []DCGMField{{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", ID: 150}, {Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", ID: 155}}
	tests := []struct {
		name   string
		result e2eexec.Result
		opts   ProbeOptions
		want   string
	}{
		{
			name:   "container runtime operation not supported",
			result: e2eexec.Result{ExitCode: 1, Stderr: []byte("socket: operation not supported")},
			opts:   ProbeOptions{HostDCGMAddress: "localhost:5555"},
			want:   "DCGM field support probe was unavailable",
		},
		{
			name:   "hostengine unreachable",
			result: e2eexec.Result{ExitCode: 1, Stderr: []byte("Error: unable to establish a connection to the specified host: localhost:5555")},
			opts:   ProbeOptions{HostDCGMAddress: "localhost:5555"},
			want:   "DCGM field support probe was unavailable",
		},
		{
			name:   "column count does not match",
			result: dmonOutput("#Entity TMPTR POWER\nGPU 0   35\n"),
			opts:   ProbeOptions{HostDCGMAddress: "localhost:5555"},
			want:   `could not read dcgmi dmon -e 150,155 output: line "GPU 0   35" has 1 values for 2 fields`,
		},
		{
			name: "dry run",
			opts: ProbeOptions{DryRun: true},
			want: "does not run in dry-run mode",
		},
		{
			name:   "no DCGM image",
			result: e2eexec.Result{ExitCode: 1},
			opts:   ProbeOptions{},
			want:   "could not resolve the configured DCGM image",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &dmonRunner{results: map[string]e2eexec.Result{"150,155": test.result}}

			got := ProbeFieldSupport(context.Background(), runner, test.opts, fields)

			require.Empty(t, got.Supported)
			require.Empty(t, got.Unsupported)
			require.Len(t, got.Unclassified, 2)
			for name, reason := range got.Unclassified {
				require.Contains(t, reason, test.want, name)
			}
		})
	}
}

// fieldSupportGolden is the document the suites' parser test reads too, so a
// change to either side of the format fails one of the two tests.
var fieldSupportGolden = filepath.Join("..", "..", "..", "tests", "internal", "metriccontract", "testdata", "field-support-document.json")

// TestFieldSupportJSONIsTheSuiteDocument checks the JSON form the suites read:
// supported and unsupported rows only, keyed by CSV name.
func TestFieldSupportJSONIsTheSuiteDocument(t *testing.T) {
	want, err := os.ReadFile(fieldSupportGolden)
	require.NoError(t, err)
	support := FieldSupport{
		Supported: []string{"DCGM_FI_DEV_GPU_UTIL_RATIO", "DCGM_FI_SYSTEM_DRIVER_VERSION"},
		Unsupported: map[string]string{
			"DCGM_FI_DEV_XID_ERROR": "DCGM returned no value (N/A) on every GPU during the probe " +
				"(dcgmi dmon -e 230, field 230, 3 samples, 24 GPU sample lines)",
		},
		Unclassified: map[string]string{"DCGM_EXP_XID_ERRORS_COUNT": "not a DCGM field"},
	}

	data, err := json.Marshal(support)

	require.NoError(t, err)
	require.JSONEq(t, string(want), string(data))
	require.False(t, strings.Contains(string(data), "DCGM_EXP"))
}

// TestProbeFieldSupportSplitsARefusedBatchInHalves checks that a refused batch
// is probed again in halves until the refused field is alone.
func TestProbeFieldSupportSplitsARefusedBatchInHalves(t *testing.T) {
	refused := e2eexec.Result{ExitCode: 1, Stdout: []byte("Error setting watches. Result: The requested operation is not supported on this device.")}
	runner := &dmonRunner{results: map[string]e2eexec.Result{
		"150,155,526,1613": refused,
		"150,155":          dmonOutput("#Entity TMPTR POWER\nID\nGPU 0   35    90.1\n"),
		"526,1613":         refused,
		"526":              refused,
		"1613":             dmonOutput("#Entity GPUTL\nID\nGPU 0   0.000\n"),
	}}

	got := ProbeFieldSupport(context.Background(), runner, ProbeOptions{HostDCGMAddress: "localhost:5555"}, []DCGMField{
		{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", ID: 150},
		{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", ID: 155},
		{Name: "DCGM_FI_DEV_VGPU_LICENSE_STATUS", ID: 526},
		{Name: "DCGM_FI_DEV_GPU_UTIL_RATIO", ID: 1613},
	})

	require.Equal(t, []string{"DCGM_FI_DEV_BOARD_POWER_WATTS", "DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_GPU_UTIL_RATIO"}, got.Supported)
	require.Contains(t, got.Unsupported["DCGM_FI_DEV_VGPU_LICENSE_STATUS"], "DCGM refused to watch field 526")
	require.Equal(t, []string{"150,155,526,1613", "150,155", "526,1613", "526", "1613"}, runner.asked)
}

// TestDmonValueMissing checks the values that count as no value.
func TestDmonValueMissing(t *testing.T) {
	for value, want := range map[string]bool{
		"N/A":                 true,
		"n/a":                 true,
		"2147483632":          true,
		"2147483635":          true,
		"9223372036854775794": true,
		"140737488355328":     true,
		"140737488355331":     true,
		"0":                   false,
		"2147483631":          false,
		"140737488355332":     false,
		"580.82.07":           false,
		"35":                  false,
	} {
		require.Equal(t, want, dmonValueMissing(value), value)
	}
}

// classifyDmonFixture classifies fields from a dcgmi dmon output file in
// testdata, as probeFieldBatch does for a batch that printed a table.
func classifyDmonFixture(t *testing.T, name string, fields []DCGMField) FieldSupport {
	t.Helper()

	output, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	rows, err := parseDmonRows(string(output), len(fields))
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	support := FieldSupport{Unsupported: map[string]string{}, Unclassified: map[string]string{}}
	classifyDmonColumns(fields, rows, "dcgmi dmon -e test", &support)
	return support
}

// TestClassifyRealB200DmonOutput checks the classification of real dcgmi dmon
// output: values make a field supported even when the first sample is N/A, and
// event fields with no event are N/A on every GPU, which is reported as "no
// value", not as proof that DCGM lacks the field.
func TestClassifyRealB200DmonOutput(t *testing.T) {
	gpuFields := classifyDmonFixture(t, "dmon-b200-gpu-fields.txt", []DCGMField{
		{Name: "DCGM_FI_SYSTEM_DRIVER_VERSION", ID: 1},
		{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", ID: 150},
		{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", ID: 155},
		// The capture read the percent utilization field 203 for the utilization row.
		{Name: "DCGM_FI_DEV_GPU_UTIL_RATIO", ID: 203},
		{Name: "DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO", ID: 1001},
	})
	require.ElementsMatch(t, []string{
		"DCGM_FI_SYSTEM_DRIVER_VERSION",
		"DCGM_FI_DEV_GPU_TEMP_CELSIUS",
		"DCGM_FI_DEV_BOARD_POWER_WATTS",
		"DCGM_FI_DEV_GPU_UTIL_RATIO",
		"DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO",
	}, gpuFields.Supported)
	require.Empty(t, gpuFields.Unsupported)

	eventFields := classifyDmonFixture(t, "dmon-b200-event-fields.txt", []DCGMField{
		{Name: "DCGM_FI_DEV_XID_ERROR", ID: 230},
		{Name: "DCGM_FI_DEV_RETIRED_SBE", ID: 390},
	})
	require.Empty(t, eventFields.Supported)
	require.Len(t, eventFields.Unsupported, 2)
	for name, reason := range eventFields.Unsupported {
		require.True(t, strings.HasPrefix(reason, "DCGM returned no value (N/A) on every GPU during the probe"), "%s: %s", name, reason)
	}
}

// TestFieldSupportWithoutProfiling checks that supported profiling rows move
// to Unclassified while device rows and unsupported profiling rows stay.
func TestFieldSupportWithoutProfiling(t *testing.T) {
	support := FieldSupport{
		Supported:    []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_PROF_PCIE_TX_BYTES"},
		Unsupported:  map[string]string{"DCGM_FI_PROF_PCIE_RX_BYTES": "no value"},
		Unclassified: map[string]string{},
	}

	support.WithoutProfiling("needs privilege")

	require.Equal(t, []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"}, support.Supported)
	require.Equal(t, map[string]string{"DCGM_FI_PROF_PCIE_TX_BYTES": "needs privilege"}, support.Unclassified)
	require.Contains(t, support.Unsupported, "DCGM_FI_PROF_PCIE_RX_BYTES")
}
