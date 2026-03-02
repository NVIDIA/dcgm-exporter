/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package collector

import (
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

func TestMetricsByCounter_GoString(t *testing.T) {
	tests := []struct {
		name     string
		metrics  MetricsByCounter
		expected string
	}{
		{
			name:     "Empty metrics",
			metrics:  MetricsByCounter{},
			expected: "MetricsByCounter{}",
		},
		{
			name: "Single counter with single metric",
			metrics: MetricsByCounter{
				{
					FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
					FieldName: "DCGM_FI_DEV_GPU_TEMP",
					PromType:  "gauge",
					Help:      "Temperature Help info",
				}: {
					{
						Counter: counters.Counter{
							FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
							FieldName: "DCGM_FI_DEV_GPU_TEMP",
							PromType:  "gauge",
							Help:      "Temperature Help info",
						},
						Value:        "42",
						GPU:          "0",
						GPUDevice:    "nvidia0",
						GPUModelName: "NVIDIA T400 4GB",
						Hostname:     "testhost",
						UUID:         "UUID",
						GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
						Labels:       map[string]string{},
						Attributes:   map[string]string{},
					},
				},
			},
			expected: `MetricsByCounter{"DCGM_FI_DEV_GPU_TEMP": []collector.Metric{collector.Metric{Counter:counters.Counter{FieldID:0x96, FieldName:"DCGM_FI_DEV_GPU_TEMP", PromType:"gauge", Help:"Temperature Help info"}, Value:"42", GPU:"0", GPUUUID:"GPU-00000000-0000-0000-0000-000000000000", GPUDevice:"nvidia0", GPUModelName:"NVIDIA T400 4GB", GPUPCIBusID:"", UUID:"UUID", MigProfile:"", NvSwitch:"", NvLink:"", GPUInstanceID:"", Hostname:"testhost", Labels:map[string]string{}, Attributes:map[string]string{}, ParentType:0x0}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.metrics.GoString()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMetricsByCounter_GoString_MultipleCounters(t *testing.T) {
	metrics := MetricsByCounter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		}: {
			{
				Counter: counters.Counter{
					FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
					FieldName: "DCGM_FI_DEV_GPU_TEMP",
					PromType:  "gauge",
					Help:      "Temperature Help info",
				},
				Value:        "42",
				GPU:          "0",
				GPUDevice:    "nvidia0",
				GPUModelName: "NVIDIA T400 4GB",
				Hostname:     "testhost",
				UUID:         "UUID",
				GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
				Labels:       map[string]string{},
				Attributes:   map[string]string{},
			},
		},
		{
			FieldID:   dcgm.DCGM_FI_DEV_POWER_USAGE,
			FieldName: "DCGM_FI_DEV_POWER_USAGE",
			PromType:  "gauge",
			Help:      "Power usage info",
		}: {
			{
				Counter: counters.Counter{
					FieldID:   dcgm.DCGM_FI_DEV_POWER_USAGE,
					FieldName: "DCGM_FI_DEV_POWER_USAGE",
					PromType:  "gauge",
					Help:      "Power usage info",
				},
				Value:        "150",
				GPU:          "0",
				GPUDevice:    "nvidia0",
				GPUModelName: "NVIDIA T400 4GB",
				Hostname:     "testhost",
				UUID:         "UUID",
				GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
				Labels:       map[string]string{},
				Attributes:   map[string]string{},
			},
		},
	}

	result := metrics.GoString()

	// Since Go maps don't guarantee order, we need to check that both counters are present
	require.Contains(t, result, `"DCGM_FI_DEV_GPU_TEMP": []collector.Metric{collector.Metric{Counter:counters.Counter{FieldID:0x96, FieldName:"DCGM_FI_DEV_GPU_TEMP", PromType:"gauge", Help:"Temperature Help info"}, Value:"42", GPU:"0", GPUUUID:"GPU-00000000-0000-0000-0000-000000000000", GPUDevice:"nvidia0", GPUModelName:"NVIDIA T400 4GB", GPUPCIBusID:"", UUID:"UUID", MigProfile:"", NvSwitch:"", NvLink:"", GPUInstanceID:"", Hostname:"testhost", Labels:map[string]string{}, Attributes:map[string]string{}, ParentType:0x0}}`)
	require.Contains(t, result, `"DCGM_FI_DEV_POWER_USAGE": []collector.Metric{collector.Metric{Counter:counters.Counter{FieldID:0x9b, FieldName:"DCGM_FI_DEV_POWER_USAGE", PromType:"gauge", Help:"Power usage info"}, Value:"150", GPU:"0", GPUUUID:"GPU-00000000-0000-0000-0000-000000000000", GPUDevice:"nvidia0", GPUModelName:"NVIDIA T400 4GB", GPUPCIBusID:"", UUID:"UUID", MigProfile:"", NvSwitch:"", NvLink:"", GPUInstanceID:"", Hostname:"testhost", Labels:map[string]string{}, Attributes:map[string]string{}, ParentType:0x0}}`)
	require.Contains(t, result, "MetricsByCounter{")
	require.Contains(t, result, "}")

	// Verify the structure is correct
	assert.True(t, strings.HasPrefix(result, "MetricsByCounter{"))
	assert.True(t, strings.HasSuffix(result, "}"))
}

func TestMetricsByCounter_String(t *testing.T) {
	metrics := MetricsByCounter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		}: {
			{
				Counter: counters.Counter{
					FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
					FieldName: "DCGM_FI_DEV_GPU_TEMP",
					PromType:  "gauge",
					Help:      "Temperature Help info",
				},
				Value:        "42",
				GPU:          "0",
				GPUDevice:    "nvidia0",
				GPUModelName: "NVIDIA T400 4GB",
				Hostname:     "testhost",
				UUID:         "UUID",
				GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
				Labels:       map[string]string{},
				Attributes:   map[string]string{},
			},
		},
	}

	result := metrics.String()
	require.Contains(t, result, `"metrics"`)
	require.Contains(t, result, `"DCGM_FI_DEV_GPU_TEMP"`)
	require.Contains(t, result, `"value":"42"`)
	require.Contains(t, result, `"gpu":"0"`)
}

func TestMetricsByCounter_ToBase64(t *testing.T) {
	metrics := MetricsByCounter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		}: {
			{
				Counter: counters.Counter{
					FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
					FieldName: "DCGM_FI_DEV_GPU_TEMP",
					PromType:  "gauge",
					Help:      "Temperature Help info",
				},
				Value:        "42",
				GPU:          "0",
				GPUDevice:    "nvidia0",
				GPUModelName: "NVIDIA T400 4GB",
				Hostname:     "testhost",
				UUID:         "UUID",
				GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
				Labels:       map[string]string{},
				Attributes:   map[string]string{},
			},
		},
	}

	result := metrics.ToBase64()
	require.NotEmpty(t, result)
	// Base64 should not contain the original JSON string directly
	require.NotContains(t, result, `"metrics"`)
	require.NotContains(t, result, `"DCGM_FI_DEV_GPU_TEMP"`)
}

func TestMetricsByCounter_MarshalJSON(t *testing.T) {
	metrics := MetricsByCounter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		}: {
			{
				Counter: counters.Counter{
					FieldID:   dcgm.DCGM_FI_DEV_GPU_TEMP,
					FieldName: "DCGM_FI_DEV_GPU_TEMP",
					PromType:  "gauge",
					Help:      "Temperature Help info",
				},
				Value:        "42",
				GPU:          "0",
				GPUDevice:    "nvidia0",
				GPUModelName: "NVIDIA T400 4GB",
				Hostname:     "testhost",
				UUID:         "UUID",
				GPUUUID:      "GPU-00000000-0000-0000-0000-000000000000",
				Labels:       map[string]string{},
				Attributes:   map[string]string{},
			},
		},
	}

	jsonData, err := metrics.MarshalJSON()
	require.NoError(t, err)
	require.NotEmpty(t, jsonData)

	jsonStr := string(jsonData)
	require.Contains(t, jsonStr, `"metrics"`)
	require.Contains(t, jsonStr, `"DCGM_FI_DEV_GPU_TEMP"`)
	require.Contains(t, jsonStr, `"value":"42"`)
}

func TestMetricsByCounter_UnmarshalJSON(t *testing.T) {
	jsonData := `{
		"metrics": {
			"DCGM_FI_DEV_GPU_TEMP": [
				{
					"counter": {
						"field_id": 2000,
						"field_name": "DCGM_FI_DEV_GPU_TEMP",
						"prom_type": "gauge",
						"help": "Temperature Help info"
					},
					"value": "42",
					"gpu": "0",
					"gpu_uuid": "GPU-00000000-0000-0000-0000-000000000000",
					"gpu_device": "nvidia0",
					"gpu_model": "NVIDIA T400 4GB",
					"pci_bus_id": "",
					"uuid": "UUID",
					"mig_profile": "",
					"gpu_instance_id": "",
					"hostname": "testhost",
					"labels": {},
					"attributes": {}
				}
			]
		}
	}`

	var metrics MetricsByCounter
	err := metrics.UnmarshalJSON([]byte(jsonData))
	require.NoError(t, err)
	require.Len(t, metrics, 1)

	// Check that the counter was properly reconstructed
	for counter, metricList := range metrics {
		assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", counter.FieldName)
		assert.Equal(t, dcgm.Short(2000), counter.FieldID)
		assert.Equal(t, "gauge", counter.PromType)
		require.Len(t, metricList, 1)
		assert.Equal(t, "42", metricList[0].Value)
		assert.Equal(t, "0", metricList[0].GPU)
	}
}

func TestMetric_GetIDOfType(t *testing.T) {
	tests := []struct {
		name     string
		metric   Metric
		idType   appconfig.KubernetesGPUIDType
		expected string
		hasError bool
	}{
		{
			name: "GPU UUID type",
			metric: Metric{
				GPUUUID: "GPU-00000000-0000-0000-0000-000000000000",
			},
			idType:   appconfig.GPUUID,
			expected: "GPU-00000000-0000-0000-0000-000000000000",
			hasError: false,
		},
		{
			name: "Device name type",
			metric: Metric{
				GPUDevice: "nvidia0",
			},
			idType:   appconfig.DeviceName,
			expected: "nvidia0",
			hasError: false,
		},
		{
			name: "MIG device with profile",
			metric: Metric{
				GPU:           "0",
				GPUInstanceID: "1",
				MigProfile:    "1g.5gb",
			},
			idType:   appconfig.GPUUID,
			expected: "0-1",
			hasError: false,
		},
		{
			name: "Unsupported ID type",
			metric: Metric{
				GPUUUID: "GPU-00000000-0000-0000-0000-000000000000",
			},
			idType:   "unsupported",
			expected: "",
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.metric.GetIDOfType(tt.idType)
			if tt.hasError {
				assert.Error(t, err)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
