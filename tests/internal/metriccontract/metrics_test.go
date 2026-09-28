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
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

const sampleMetrics = `
# HELP DCGM_FI_DEV_SM_CLOCK SM clock.
# TYPE DCGM_FI_DEV_SM_CLOCK gauge
DCGM_FI_DEV_SM_CLOCK{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 210
# HELP DCGM_FI_DEV_MEM_CLOCK Memory clock.
# TYPE DCGM_FI_DEV_MEM_CLOCK gauge
DCGM_FI_DEV_MEM_CLOCK{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 405
# HELP DCGM_FI_DEV_MEMORY_TEMP Memory temperature.
# TYPE DCGM_FI_DEV_MEMORY_TEMP gauge
DCGM_FI_DEV_MEMORY_TEMP{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature.
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 41
# HELP DCGM_FI_DEV_POWER_USAGE Power usage.
# TYPE DCGM_FI_DEV_POWER_USAGE gauge
DCGM_FI_DEV_POWER_USAGE{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 77
# HELP DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION Total energy.
# TYPE DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION counter
DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 196764132
# HELP DCGM_FI_DEV_PCIE_REPLAY_COUNTER PCIe replay counter.
# TYPE DCGM_FI_DEV_PCIE_REPLAY_COUNTER counter
DCGM_FI_DEV_PCIE_REPLAY_COUNTER{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_GPU_UTIL GPU util.
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_MEM_COPY_UTIL Memory copy util.
# TYPE DCGM_FI_DEV_MEM_COPY_UTIL gauge
DCGM_FI_DEV_MEM_COPY_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 37
# HELP DCGM_FI_DEV_ENC_UTIL Encoder util.
# TYPE DCGM_FI_DEV_ENC_UTIL gauge
DCGM_FI_DEV_ENC_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_DEC_UTIL Decoder util.
# TYPE DCGM_FI_DEV_DEC_UTIL gauge
DCGM_FI_DEV_DEC_UTIL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 0
# HELP DCGM_FI_DEV_FB_FREE FB free.
# TYPE DCGM_FI_DEV_FB_FREE gauge
DCGM_FI_DEV_FB_FREE{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 6604
# HELP DCGM_FI_DEV_FB_USED FB used.
# TYPE DCGM_FI_DEV_FB_USED gauge
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 1024
# HELP DCGM_FI_DEV_FB_RESERVED FB reserved.
# TYPE DCGM_FI_DEV_FB_RESERVED gauge
DCGM_FI_DEV_FB_RESERVED{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA B200",Hostname="node-a",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload"} 372
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
	families, err := ParseText([]byte(metricsWithoutFamily(sampleMetrics, "DCGM_FI_DEV_FB_RESERVED")))
	require.NoError(t, err)

	require.NoError(t, ValidateContracts(families, BaselineGPUFamilies()))
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
		{Name: "DCGM_FI_DEV_GPU_TEMP", Type: dto.MetricType_GAUGE},
		{Name: "DCGM_FI_DRIVER_VERSION", Type: dto.MetricType_UNTYPED},
	}, rows)
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
		SupportedFields: map[string]bool{
			"DCGM_FI_DEV_GPU_TEMP":                    true,
			"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS": true,
		},
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS")
}

// TestValidateDefaultCounterRowsSkipsUnsupportedFields verifies unsupported fields are not required.
func TestValidateDefaultCounterRowsSkipsUnsupportedFields(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics))
	require.NoError(t, err)

	var out strings.Builder
	err = ValidateDefaultCounterRows(families, []DefaultCounterRow{
		{Name: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", Type: dto.MetricType_COUNTER},
	}, DefaultCounterOptions{
		SupportedFields: map[string]bool{"DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS": false},
		SkipWriter:      &out,
	})

	require.NoError(t, err)
	require.Contains(t, out.String(), "reported unsupported")
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
	require.Contains(t, out.String(), "field support unknown")
}

// TestValidateDefaultCounterRowsChecksLabelRows verifies label-only CSV rows are validated as labels.
func TestValidateDefaultCounterRowsChecksLabelRows(t *testing.T) {
	families, err := ParseText([]byte(sampleMetrics + `
DCGM_FI_DEV_GPU_TEMP{gpu="1",UUID="GPU-1",device="nvidia1",modelName="NVIDIA B200",DCGM_FI_DRIVER_VERSION="595.58"} 41
`))
	require.NoError(t, err)

	err = ValidateDefaultCounterRows(families, []DefaultCounterRow{
		{Name: "DCGM_FI_DRIVER_VERSION", Type: dto.MetricType_UNTYPED},
	}, DefaultCounterOptions{SupportedFields: map[string]bool{"DCGM_FI_DRIVER_VERSION": true}})
	require.NoError(t, err)
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
	families, err := ParseText([]byte(sampleMetrics + `
	# HELP DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL Total NVLink bandwidth.
	# TYPE DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL gauge
	DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL{gpu="0",UUID="GPU-0",device="nvidia0",modelName="NVIDIA GB200",Hostname="node-a"} 0
	`))
	require.NoError(t, err)

	report := DetectCapabilities(families)
	require.True(t, report.HasNVLink)
	require.NoError(t, ValidateCapabilityContracts(families, report))
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
