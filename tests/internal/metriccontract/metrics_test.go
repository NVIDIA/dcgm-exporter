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

package metriccontract

import (
	"math"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// TestGPULinkFieldContractRejectsWrongPhysicalLink rejects wrong link labels, duplicate matches, and non-finite values.
func TestGPULinkFieldContractRejectsWrongPhysicalLink(t *testing.T) {
	contract := GPULinkContract{
		GPU: 0, GPUUUID: "GPU-test", Link: 17,
		Fields: []GPULinkField{{ID: 1011, Name: "DCGM_FI_PROF_NVLINK_TX_BYTES"}},
	}
	require.NoError(t, contract.Validate())
	families, err := ParseText([]byte(`# HELP DCGM_FI_PROF_NVLINK_TX_BYTES GPU link bytes.
# TYPE DCGM_FI_PROF_NVLINK_TX_BYTES gauge
DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",gpu_uuid="GPU-test",nvlink="1"} 12
`))
	require.NoError(t, err)
	require.Error(t, ValidateExactlyOneLabelMatch(families, []string{contract.Fields[0].Name},
		map[string]string{"gpu": "0", "gpu_uuid": "GPU-test", "nvlink": "17"}))
	require.NoError(t, ValidateExactlyOneLabelMatch(families, []string{contract.Fields[0].Name},
		map[string]string{"gpu": "0", "gpu_uuid": "GPU-test", "nvlink": "1"}))
	families[contract.Fields[0].Name].Metric = append(families[contract.Fields[0].Name].Metric,
		families[contract.Fields[0].Name].Metric[0])
	require.Error(t, ValidateExactlyOneLabelMatch(families, []string{contract.Fields[0].Name},
		map[string]string{"gpu": "0", "gpu_uuid": "GPU-test", "nvlink": "1"}))
	nonFinite := math.NaN()
	families[contract.Fields[0].Name].Metric[0].Gauge.Value = &nonFinite
	require.Error(t, ValidateContracts(families, []FamilyContract{{
		Name: contract.Fields[0].Name, Type: dto.MetricType_GAUGE, CheckType: true, Finite: true,
	}}))
}

const sampleMetrics = `
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
# HELP DCGM_FI_DEV_FB_RESERVED_BYTES FB reserved.
# TYPE DCGM_FI_DEV_FB_RESERVED_BYTES gauge
DCGM_FI_DEV_FB_RESERVED_BYTES{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 390070272
# HELP DCGM_FI_DEV_VGPU_LICENSE_STATUS vGPU license status.
# TYPE DCGM_FI_DEV_VGPU_LICENSE_STATUS gauge
DCGM_FI_DEV_VGPU_LICENSE_STATUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
`

// TestValidateBaselineGPUFamilies verifies the default baseline contract accepts valid samples.
func TestValidateBaselineGPUFamilies(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	require.NoError(t, ValidateContracts(families, BaselineGPUFamilies()))
	require.NoError(t, ValidateAnyLabel(families, BaselineGPUFamilyNames(), GPUHostnameLabels))
	require.NoError(t, ValidateLabels(families, BaselineGPUFamilyNames(), KubernetesLabels))
	require.NoError(t, ValidateLabelValues(families, BaselineGPUFamilyNames(), map[string]string{
		"pod":       "cuda-workload",
		"namespace": "dcgm-exporter",
		"container": "cuda-workload",
	}))
	require.NoError(t, ValidateAtLeastOneDCGMGPUFamily(families))
}

// TestValidateBaselineGPUFamiliesAllowsMissingOptionalFields verifies the baseline works on GPUs
// where DCGM omits optional default-counters fields.
func TestValidateBaselineGPUFamiliesAllowsMissingOptionalFields(t *testing.T) {
	families, err := ParseText([]byte(metricsWithoutFamily(sampleMetrics, "DCGM_FI_DEV_MEMORY_TEMP_CELSIUS")))
	require.NoError(t, err)

	require.NoError(t, ValidateContracts(families, BaselineGPUFamilies()))
}

// TestValidateBaselineGPUFamiliesRejectsMissingMandatoryFields rejects a baseline scrape missing GPU temperature.
func TestValidateBaselineGPUFamiliesRejectsMissingMandatoryFields(t *testing.T) {
	families, err := ParseText([]byte(metricsWithoutFamily(sampleMetrics, "DCGM_FI_DEV_GPU_TEMP_CELSIUS")))
	require.NoError(t, err)

	require.ErrorContains(t, ValidateContracts(families, BaselineGPUFamilies()), "DCGM_FI_DEV_GPU_TEMP")
}

// TestReadDefaultCounterRowsDiscoversEnabledRows verifies comments and blank rows are ignored.
func TestReadDefaultCounterRowsDiscoversEnabledRows(t *testing.T) {
	rows, err := ReadDefaultCounterRows(strings.NewReader(`
# comment
DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature

# DCGM_FI_DEV_POWER_USAGE, gauge, disabled
DCGM_FI_DRIVER_VERSION, label, Driver version
`))
	require.NoError(t, err)

	require.Equal(t, []DefaultCounterRow{
		{Name: "DCGM_FI_DEV_GPU_TEMP", Type: dto.MetricType_GAUGE, Help: "GPU temperature"},
		{Name: "DCGM_FI_DRIVER_VERSION", Type: dto.MetricType_UNTYPED, Help: "Driver version"},
	}, rows)
}

// TestReadDefaultCounterRowsRejectsWrongFieldCounts checks rows with too few or too many CSV fields fail.
func TestReadDefaultCounterRowsRejectsWrongFieldCounts(t *testing.T) {
	for _, row := range []string{
		"DCGM_FI_DEV_GPU_TEMP,gauge",
		"DCGM_FI_DEV_GPU_TEMP,gauge,GPU temperature,unexpected",
	} {
		t.Run(row, func(t *testing.T) {
			_, err := ReadDefaultCounterRows(strings.NewReader(row))
			require.ErrorContains(t, err, "expected exactly 3")
		})
	}
}

// TestValidateDefaultCounterRowsRequiresSupportedFields verifies supported missing rows fail.
func TestValidateDefaultCounterRowsRequiresSupportedFields(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	rows := []DefaultCounterRow{
		{Name: "DCGM_FI_DEV_GPU_TEMP", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Type: dto.MetricType_COUNTER},
	}
	err = ValidateDefaultCounterRows(families, rows, DefaultCounterOptions{
		FieldSupport: testFieldSupport(t, rows, fieldSupportDocument{
			Supported: []string{"DCGM_FI_DEV_GPU_TEMP", "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS"},
		}),
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), `"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS" is missing, but DCGM returns a value`)
}

// testFieldSupport builds field support evidence for rows and fails the test
// when the document does not match them.
func testFieldSupport(t *testing.T, rows []DefaultCounterRow, document fieldSupportDocument) FieldSupport {
	t.Helper()

	support, err := newFieldSupport(document, rows)
	require.NoError(t, err)
	return support
}

// TestValidateDefaultCounterRowsRejectsMalformedPresentFields verifies a row
// present in exporter output must still satisfy its metric contract.
func TestValidateDefaultCounterRowsRejectsMalformedPresentFields(t *testing.T) {
	families, err := ParseText([]byte(`
# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0"} -1
`))
	require.NoError(t, err)

	rows := []DefaultCounterRow{{Name: "DCGM_FI_DEV_GPU_TEMP", Type: dto.MetricType_GAUGE}}
	for _, support := range []FieldSupport{
		{},
		testFieldSupport(t, rows, fieldSupportDocument{Unsupported: map[string]string{"DCGM_FI_DEV_GPU_TEMP": "N/A"}}),
		testFieldSupport(t, rows, fieldSupportDocument{Supported: []string{"DCGM_FI_DEV_GPU_TEMP"}}),
	} {
		err := ValidateDefaultCounterRows(families, rows, DefaultCounterOptions{FieldSupport: support})
		require.Error(t, err, "support state %#v accepted malformed output", support)
		require.ErrorContains(t, err, "negative sample")
	}
}

// TestValidateDefaultCounterRowsRequiresIdentityOnEverySeries verifies each
// emitted series retains the GPU identity labels used by consumers.
func TestValidateDefaultCounterRowsRequiresIdentityOnEverySeries(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics + `
DCGM_FI_DEV_GPU_TEMP{gpu="1",UUID="GPU-1",Hostname="node-a"} 42
`))
	require.NoError(t, err)
	err = ValidateDefaultCounterRows(families, []DefaultCounterRow{{Name: "DCGM_FI_DEV_GPU_TEMP", Type: dto.MetricType_GAUGE}}, DefaultCounterOptions{})
	require.ErrorContains(t, err, `without non-empty label "device"`)
	require.ErrorContains(t, err, `without non-empty label "modelName"`)
}

// TestValidateDCGMSamplesRejectsInvalidOutput verifies the sample validator
// rejects values that cannot represent valid DCGM observations.
func TestValidateDCGMSamplesRejectsInvalidOutput(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing GPU UUID",
			body: "# TYPE DCGM_FI_DEV_GPU_TEMP gauge\nDCGM_FI_DEV_GPU_TEMP{gpu=\"0\"} 1\n",
			want: "without GPU UUID",
		},
		{
			name: "duplicate series",
			body: "# TYPE DCGM_FI_DEV_GPU_TEMP gauge\nDCGM_FI_DEV_GPU_TEMP{gpu=\"0\",UUID=\"GPU-0\"} 1\nDCGM_FI_DEV_GPU_TEMP{gpu=\"0\",UUID=\"GPU-0\"} 2\n",
			want: "duplicates label set",
		},
		{
			name: "non-finite value",
			body: "# TYPE DCGM_FI_DEV_GPU_TEMP gauge\nDCGM_FI_DEV_GPU_TEMP{gpu=\"0\",UUID=\"GPU-0\"} NaN\n",
			want: "non-finite",
		},
		{
			name: "utilization ratio bound",
			body: "# TYPE DCGM_FI_DEV_GPU_UTIL_RATIO gauge\nDCGM_FI_DEV_GPU_UTIL_RATIO{gpu=\"0\",UUID=\"GPU-0\"} 1.01\n",
			want: "above maximum",
		},
		{
			name: "utilization bound",
			body: "# TYPE DCGM_FI_DEV_GPU_UTIL gauge\nDCGM_FI_DEV_GPU_UTIL{gpu=\"0\",UUID=\"GPU-0\"} 101\n",
			want: "above maximum",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			families, err := ParseText([]byte(test.body))
			require.NoError(t, err)
			require.ErrorContains(t, ValidateDCGMSamples(families), test.want)
		})
	}
}

// TestValidateDCGMSamplesRejectsUnavailableSentinels checks DCGM's int32, int64, and fp64 sentinel ranges.
func TestValidateDCGMSamplesRejectsUnavailableSentinels(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "int32 blank", value: "2147483632"},
		{name: "int32 permission denied", value: "2147483635"},
		{name: "int64 blank", value: "9223372036854775792"},
		{name: "int64 permission denied", value: "9223372036854775795"},
		{name: "fp64 blank", value: "140737488355328"},
		{name: "fp64 permission denied", value: "140737488355331"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := "# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge\n" +
				"DCGM_FI_DEV_MEMORY_TEMP{gpu=\"0\",UUID=\"GPU-0\"} " + test.value + "\n"
			families, err := ParseText([]byte(body))
			require.NoError(t, err)
			require.ErrorContains(t, ValidateDCGMSamples(families), "DCGM unavailable sentinel")
		})
	}
}

// TestValidateDefaultCounterRowsSkipsUnsupportedFields verifies unsupported fields are not required.
func TestValidateDefaultCounterRowsSkipsUnsupportedFields(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	var out strings.Builder
	rows := []DefaultCounterRow{{Name: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Type: dto.MetricType_COUNTER}}
	err = ValidateDefaultCounterRows(families, rows, DefaultCounterOptions{
		FieldSupport: testFieldSupport(t, rows, fieldSupportDocument{
			Unsupported: map[string]string{"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS": "N/A on every GPU"},
		}),
		SkipWriter: &out,
	})

	require.NoError(t, err)
	require.Contains(t, out.String(), "not required: the DCGM field support evidence has no value for it: N/A on every GPU")
}

// TestValidateDefaultCounterRowsSkipsUnknownMissingFields verifies unknown missing rows are explicit skips.
func TestValidateDefaultCounterRowsSkipsUnknownMissingFields(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	var out strings.Builder
	err = ValidateDefaultCounterRows(families, []DefaultCounterRow{
		{Name: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Type: dto.MetricType_COUNTER},
	}, DefaultCounterOptions{SkipWriter: &out})

	require.NoError(t, err)
	require.Contains(t, out.String(), "default metric family was not emitted and no DCGM field support evidence for this run")
	require.Contains(t, out.String(), "coverage unqualified")
}

// TestValidateDefaultCounterRowsChecksLabelRows verifies label-only CSV rows are validated as labels.
func TestValidateDefaultCounterRowsChecksLabelRows(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics + `
DCGM_FI_DEV_GPU_TEMP{gpu="1",UUID="GPU-1",device="nvidia1",modelName="NVIDIA B200",DCGM_FI_DRIVER_VERSION="595.58"} 41
`))
	require.NoError(t, err)

	rows := []DefaultCounterRow{{Name: "DCGM_FI_DRIVER_VERSION", Type: dto.MetricType_UNTYPED}}
	err = ValidateDefaultCounterRows(families, rows, DefaultCounterOptions{
		FieldSupport: testFieldSupport(t, rows, fieldSupportDocument{Supported: []string{"DCGM_FI_DRIVER_VERSION"}}),
	})
	require.NoError(t, err)
}

// TestValidateDefaultCounterRowsClassifiesMissingRows checks the outcome of a
// missing family or label row for each field support state.
func TestValidateDefaultCounterRowsClassifiesMissingRows(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	family := DefaultCounterRow{Name: "DCGM_FI_DEV_ROW_REMAP_FAILED", Type: dto.MetricType_GAUGE}
	label := DefaultCounterRow{Name: "DCGM_FI_SYSTEM_DRIVER_VERSION", Type: dto.MetricType_UNTYPED}
	rows := []DefaultCounterRow{family, label}
	tests := []struct {
		name     string
		row      DefaultCounterRow
		document *fieldSupportDocument
		wantErr  string
		wantSkip string
	}{
		{
			name:     "supported family fails",
			row:      family,
			document: &fieldSupportDocument{Supported: []string{family.Name}},
			wantErr:  `default metric family "DCGM_FI_DEV_ROW_REMAP_FAILED" is missing, but DCGM returns a value for it`,
		},
		{
			name:     "supported label fails",
			row:      label,
			document: &fieldSupportDocument{Supported: []string{label.Name}},
			wantErr:  `default label field "DCGM_FI_SYSTEM_DRIVER_VERSION" is missing, but DCGM returns a value for it`,
		},
		{
			name:     "unsupported family is a skip with the reason",
			row:      family,
			document: &fieldSupportDocument{Unsupported: map[string]string{family.Name: "dcgmi dmon returned N/A"}},
			wantSkip: "skipping default counter DCGM_FI_DEV_ROW_REMAP_FAILED; not required: the DCGM field support evidence has no value for it: dcgmi dmon returned N/A",
		},
		{
			name:     "row missing from present evidence is unqualified",
			row:      family,
			document: &fieldSupportDocument{Supported: []string{label.Name}},
			wantSkip: "default metric family was not emitted and the DCGM field support evidence does not classify this row; coverage unqualified",
		},
		{
			name:     "label without evidence is unqualified",
			row:      label,
			wantSkip: "default label field was not emitted and no DCGM field support evidence for this run",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var support FieldSupport
			if test.document != nil {
				support = testFieldSupport(t, rows, *test.document)
			}

			var out strings.Builder
			err := ValidateDefaultCounterRows(families, []DefaultCounterRow{test.row}, DefaultCounterOptions{
				FieldSupport: support,
				SkipWriter:   &out,
			})

			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.Empty(t, out.String())
				return
			}
			require.NoError(t, err)
			require.Contains(t, out.String(), test.wantSkip)
		})
	}
}

// TestValidateDefaultCounterRowsExactCSV checks that an exact CSV run fails a
// DCGM_FI_* family that is not a row and a HELP text that differs from the
// CSV help, and that other runs do not check either.
func TestValidateDefaultCounterRowsExactCSV(t *testing.T) {
	families, err := ParseText([]byte(`
# HELP DCGM_FI_DEV_GPU_TEMP_CELSIUS GPU temperature (in C).
# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge
DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200"} 41
# HELP DCGM_FI_DEV_BOARD_POWER_WATTS Board power.
# TYPE DCGM_FI_DEV_BOARD_POWER_WATTS gauge
DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200"} 77
# HELP DCGM_EXP_XID_ERRORS_TOTAL XID errors.
# TYPE DCGM_EXP_XID_ERRORS_TOTAL counter
DCGM_EXP_XID_ERRORS_TOTAL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200"} 0
`))
	require.NoError(t, err)

	tests := []struct {
		name     string
		rows     []DefaultCounterRow
		exact    bool
		wantErrs []string
	}{
		{
			name: "matching CSV",
			rows: []DefaultCounterRow{
				{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", Type: dto.MetricType_GAUGE, Help: "GPU temperature (in C)."},
				{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", Type: dto.MetricType_GAUGE, Help: "Board power."},
			},
			exact: true,
		},
		{
			name: "family outside the CSV",
			rows: []DefaultCounterRow{
				{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", Type: dto.MetricType_GAUGE, Help: "GPU temperature (in C)."},
			},
			exact:    true,
			wantErrs: []string{`metric family "DCGM_FI_DEV_BOARD_POWER_WATTS" is not a metric row of the CSV`},
		},
		{
			name: "label row emitted as a family",
			rows: []DefaultCounterRow{
				{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", Type: dto.MetricType_GAUGE, Help: "GPU temperature (in C)."},
				{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", Type: dto.MetricType_UNTYPED, Help: "Board power."},
			},
			exact:    true,
			wantErrs: []string{`metric family "DCGM_FI_DEV_BOARD_POWER_WATTS" is not a metric row of the CSV`},
		},
		{
			name: "HELP differs from the CSV",
			rows: []DefaultCounterRow{
				{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", Type: dto.MetricType_GAUGE, Help: "GPU temperature (in C)."},
				{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", Type: dto.MetricType_GAUGE, Help: "Power draw (in W)."},
			},
			exact:    true,
			wantErrs: []string{`metric family "DCGM_FI_DEV_BOARD_POWER_WATTS" has HELP "Board power.", expected the CSV help "Power draw (in W)."`},
		},
		{
			name: "not an exact CSV run",
			rows: []DefaultCounterRow{
				{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", Type: dto.MetricType_GAUGE, Help: "Power draw (in W)."},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDefaultCounterRows(families, test.rows, DefaultCounterOptions{ExactCSV: test.exact})

			if len(test.wantErrs) == 0 {
				require.NoError(t, err)
				return
			}
			for _, want := range test.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

// TestValidateFamilyPrefix verifies prefix-based capability contracts require samples and labels.
func TestValidateFamilyPrefix(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics + `
# HELP DCGM_FI_PROF_GR_ENGINE_ACTIVE Ratio active.
# TYPE DCGM_FI_PROF_GR_ENGINE_ACTIVE gauge
DCGM_FI_PROF_GR_ENGINE_ACTIVE{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200"} 0.1
`))
	require.NoError(t, err)
	require.NoError(t, ValidateFamilyPrefix(families, "DCGM_FI_PROF_", []string{"gpu", "UUID"}, true, "profiling/DCP"))
}

// TestValidateFamilyPrefixReportsMissingLabels rejects a selected family without UUID identity.
func TestValidateFamilyPrefixReportsMissingLabels(t *testing.T) {
	families, err := ParseText([]byte(`
# HELP DCGM_FI_DEV_C2C_LINK_COUNT Number of C2C links.
# TYPE DCGM_FI_DEV_C2C_LINK_COUNT gauge
DCGM_FI_DEV_C2C_LINK_COUNT{gpu="0"} 1
`))
	require.NoError(t, err)
	err = ValidateFamilyPrefix(families, "DCGM_FI_DEV_C2C", []string{"gpu", "UUID"}, true, "C2C")
	require.Error(t, err)
	require.Contains(t, err.Error(), "UUID")
}

// TestValidateContractsReportsMissingFamily verifies missing metric families are reported.
func TestValidateContractsReportsMissingFamily(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	err = ValidateContracts(families, []FamilyContract{
		{Name: "DCGM_FI_DEV_DOES_NOT_EXIST", Type: dto.MetricType_GAUGE, CheckType: true},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DCGM_FI_DEV_DOES_NOT_EXIST")
}

// TestValidateContractsWithoutLabels verifies WithoutLabels needs one sample
// that has the wanted labels and lacks the excluded ones.
func TestValidateContractsWithoutLabels(t *testing.T) {
	const header = "# HELP DCGM_FI_DEV_CPU_UTIL_TOTAL CPU utilization.\n# TYPE DCGM_FI_DEV_CPU_UTIL_TOTAL gauge\n"
	cpuOnly := FamilyContract{Name: "DCGM_FI_DEV_CPU_UTIL_TOTAL", Labels: []string{"cpu"}, WithoutLabels: []string{"cpucore"}}

	tests := []struct {
		name    string
		samples string
		wantErr bool
	}{
		{name: "CPU and core samples", samples: "DCGM_FI_DEV_CPU_UTIL_TOTAL{cpu=\"0\"} 1\nDCGM_FI_DEV_CPU_UTIL_TOTAL{cpu=\"0\",cpucore=\"1\"} 1\n"},
		{name: "empty excluded label counts as absent", samples: "DCGM_FI_DEV_CPU_UTIL_TOTAL{cpu=\"0\",cpucore=\"\"} 1\n"},
		{name: "only core samples", samples: "DCGM_FI_DEV_CPU_UTIL_TOTAL{cpu=\"0\",cpucore=\"1\"} 1\n", wantErr: true},
		{name: "no wanted label", samples: "DCGM_FI_DEV_CPU_UTIL_TOTAL{hostname=\"node-a\"} 1\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			families, err := ParseText([]byte(header + tt.samples))
			require.NoError(t, err)

			err = ValidateContracts(families, []FamilyContract{cpuOnly})
			if tt.wantErr {
				require.ErrorContains(t, err, "without labels [cpucore]")
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestDetectCapabilities verifies optional capabilities are inferred from labels and family names.
func TestDetectCapabilities(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics + `
	# HELP DCGM_FI_PROF_GR_ENGINE_ACTIVE Ratio active.
	# TYPE DCGM_FI_PROF_GR_ENGINE_ACTIVE gauge
	DCGM_FI_PROF_GR_ENGINE_ACTIVE{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",GPU_I_ID="1",GPU_I_PROFILE="1g.10gb"} 0.1
	# HELP DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS NVSwitch fatal errors.
	# TYPE DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS gauge
	DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS{nvswitch="0",gpu="0",gpu_uuid="GPU-0",device="nvidia0",model_name="NVIDIA B200",hostname="node-a"} 0
	# HELP DCGM_FI_DEV_XID_ERRORS XID errors.
	# TYPE DCGM_FI_DEV_XID_ERRORS gauge
	DCGM_FI_DEV_XID_ERRORS{modelName="NVIDIA B200"} 17
	DCGM_FI_DEV_XID_ERRORS{gpu="1",UUID="GPU-1",modelName="NVIDIA B200"} 19
	`))
	require.NoError(t, err)

	report := DetectCapabilities(families)
	require.Equal(t, []string{"NVIDIA B200"}, report.Models)
	require.True(t, report.HasB200)
	require.True(t, report.HasMIG)
	require.True(t, report.HasNVLink)
	require.True(t, report.HasProfiling)
	require.NoError(t, ValidateCapabilityContracts(families, report))

	var out strings.Builder
	WriteCapabilityReport(&out, report)
	require.Contains(t, out.String(), "detected GPU models")
}

// TestDetectCapabilitiesKeepsGB200DistinctFromB200 verifies GB200 detection does not imply B200.
func TestDetectCapabilitiesKeepsGB200DistinctFromB200(t *testing.T) {
	families, err := ParseText([]byte(`
	# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
	# TYPE DCGM_FI_DEV_GPU_TEMP gauge
	DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",modelName="NVIDIA GB200"} 41
	`))
	require.NoError(t, err)

	report := DetectCapabilities(families)
	require.False(t, report.HasB200)
	require.True(t, report.HasGB200)
	require.NoError(t, ValidateCapabilityContracts(families, report))
}

// TestDetectCapabilitiesTreatsNVSwitchFamilyAsNVLink verifies NVSwitch metrics enable link checks.
func TestDetectCapabilitiesTreatsNVSwitchFamilyAsNVLink(t *testing.T) {
	families, err := ParseText([]byte(`
	# HELP DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS NVSwitch fatal errors.
	# TYPE DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS gauge
	DCGM_FI_DEV_NVSWITCH_FATAL_ERRORS{gpu="0",UUID="GPU-0"} 0
	`))
	require.NoError(t, err)

	report := DetectCapabilities(families)
	require.True(t, report.HasNVLink)
}

// TestValidateCapabilityContractsAcceptsAggregateNVLinkMetrics verifies GPU-scope
// NVLink aggregate counters do not require per-link labels.
func TestValidateCapabilityContractsAcceptsAggregateNVLinkMetrics(t *testing.T) {
	for _, family := range []string{
		"DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL",
		"DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND",
	} {
		t.Run(family, func(t *testing.T) {
			families, err := ParseText([]byte(sampleMetrics + `
	# HELP ` + family + ` Total NVLink throughput.
	# TYPE ` + family + ` gauge
	` + family + `{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA GB200",Hostname="node-a"} 0
	`))
			require.NoError(t, err)

			report := DetectCapabilities(families)
			require.True(t, report.HasNVLink)
			require.NoError(t, ValidateCapabilityContracts(families, report))
		})
	}
}

// TestValidateCapabilityContractsReportsMissingDetectedFamilies verifies detected capabilities are enforced.
func TestValidateCapabilityContractsReportsMissingDetectedFamilies(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	err = ValidateCapabilityContracts(families, CapabilityReport{HasProfiling: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "profiling/DCP")
}

// TestValidateLabelsAcceptsDriverVersionLabel verifies label-only counters can be required as labels.
func TestValidateLabelsAcceptsDriverVersionLabel(t *testing.T) {
	families, err := ParseText([]byte(`
	# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
	# TYPE DCGM_FI_DEV_GPU_TEMP gauge
	DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",DCGM_FI_DRIVER_VERSION="595.58"} 41
	`))
	require.NoError(t, err)

	require.NoError(t, ValidateLabels(families, []string{"DCGM_FI_DEV_GPU_TEMP"}, []string{"DCGM_FI_DRIVER_VERSION"}))
}

// metricsWithoutFamily removes a selected family and its metadata from a text fixture.
func metricsWithoutFamily(metrics string, family string) string {
	var kept []string
	for _, line := range strings.Split(metrics, "\n") {
		if strings.Contains(line, family) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestValidateAnyLabelAcceptsLowercaseHostname verifies either hostname casing satisfies host identity.
func TestValidateAnyLabelAcceptsLowercaseHostname(t *testing.T) {
	families, err := ParseText([]byte(`
	# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
	# TYPE DCGM_FI_DEV_GPU_TEMP gauge
	DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",hostname="node-a"} 41
	`))
	require.NoError(t, err)

	require.NoError(t, ValidateContracts(families, []FamilyContract{
		{
			Name:        "DCGM_FI_DEV_GPU_TEMP",
			Type:        dto.MetricType_GAUGE,
			CheckType:   true,
			Labels:      GPUIdentityLabels,
			NonNegative: true,
		},
	}))
	require.NoError(t, ValidateAnyLabel(families, []string{"DCGM_FI_DEV_GPU_TEMP"}, GPUHostnameLabels))
}

// TestValidateModelSamplesAcceptsLaterSampleWithIdentity verifies model checks scan all samples.
func TestValidateModelSamplesAcceptsLaterSampleWithIdentity(t *testing.T) {
	families, err := ParseText([]byte(`
	# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
	# TYPE DCGM_FI_DEV_GPU_TEMP gauge
	DCGM_FI_DEV_GPU_TEMP{modelName="NVIDIA B200"} 41
	DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",modelName="NVIDIA B200"} 42
	`))
	require.NoError(t, err)

	require.NoError(t, validateModelSamples(families, "B200"))
}

// TestValidateLabelValuesRequiresExpectedValuesOnSameSample verifies workload labels must co-occur.
func TestValidateLabelValuesRequiresExpectedValuesOnSameSample(t *testing.T) {
	families, err := ParseText([]byte(`
	# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
	# TYPE DCGM_FI_DEV_GPU_TEMP gauge
	DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",pod="wrong-pod",namespace="dcgm-exporter",container="cuda-workload"} 41
	DCGM_FI_DEV_GPU_TEMP{gpu="1",UUID="GPU-1",pod="cuda-workload",namespace="wrong-namespace",container="cuda-workload"} 42
	`))
	require.NoError(t, err)

	err = ValidateLabelValues(families, []string{"DCGM_FI_DEV_GPU_TEMP"}, map[string]string{
		"pod":       "cuda-workload",
		"namespace": "dcgm-exporter",
		"container": "cuda-workload",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DCGM_FI_DEV_GPU_TEMP")
	require.Contains(t, err.Error(), "label values")
}

// TestValidateDCGMSamplesAllowsPodSumAboveGPUBound verifies a pod's summed
// utilization may exceed the per-GPU bound.
func TestValidateDCGMSamplesAllowsPodSumAboveGPUBound(t *testing.T) {
	families, err := ParseText([]byte("# TYPE DCGM_FI_DEV_GPU_UTIL_RATIO gauge\n" +
		`DCGM_FI_DEV_GPU_UTIL_RATIO{gpu="0",UUID="GPU-0",pod="a",namespace="n",container="c"} 1.2` + "\n"))
	require.NoError(t, err)
	require.NoError(t, ValidateDCGMSamples(families))
}

// TestValidateDCGMSamplesKeepsBoundForOtherPodFamilies verifies the pod exception is limited to summed utilization.
func TestValidateDCGMSamplesKeepsBoundForOtherPodFamilies(t *testing.T) {
	families, err := ParseText([]byte("# TYPE DCGM_FI_DEV_ENC_UTIL_RATIO gauge\n" +
		`DCGM_FI_DEV_ENC_UTIL_RATIO{gpu="0",UUID="GPU-0",pod="a",namespace="n",container="c"} 1.2` + "\n"))
	require.NoError(t, err)
	require.ErrorContains(t, ValidateDCGMSamples(families), "above maximum")
}
