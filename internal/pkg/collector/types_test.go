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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
)

// watchResultFromCleanups adapts assertions that only need to observe Close
// calls while collector production code owns a WatchResult instead of callbacks.
func watchResultFromCleanups(cleanups []func()) *devicewatcher.WatchResult {
	return devicewatcher.ResultFromBindings(nil, func() {
		for index := len(cleanups) - 1; index >= 0; index-- {
			cleanups[index]()
		}
	})
}

// TestEntityCollectorTupleAccessors returns the entity and collector set through their accessors.
func TestEntityCollectorTupleAccessors(t *testing.T) {
	collector := new(DCGMCollector)
	tuple := EntityCollectorTuple{}

	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(collector)

	assert.Equal(t, dcgm.FE_GPU, tuple.Entity())
	assert.Same(t, collector, tuple.Collector())
}

// TestMetricsByCounter_GoString pins the debug representation for empty and single-counter metric sets.
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
			expected: `MetricsByCounter{"DCGM_FI_DEV_GPU_TEMP": []collector.Metric{collector.Metric{Counter:counters.Counter{FieldID:0x96, FieldName:"DCGM_FI_DEV_GPU_TEMP", PromType:"gauge", Help:"Temperature Help info"}, Value:"42", GPU:"0", GPUUUID:"GPU-00000000-0000-0000-0000-000000000000", GPUDevice:"nvidia0", CPUSerial:"", GPUModelName:"NVIDIA T400 4GB", GPUPCIBusID:"", UUID:"UUID", MigProfile:"", NvSwitch:"", NvLink:"", GPUInstanceID:"", GPUComputeInstanceID:"", Hostname:"testhost", Labels:map[string]string{}, Attributes:map[string]string{}, ParentType:0x0}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.metrics.GoString()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestMetricsByCounterGoStringOmitsAbsentIdentityFields keeps debug output
// focused on the identities that apply to the sampled entity.
func TestMetricsByCounterGoStringOmitsAbsentIdentityFields(t *testing.T) {
	counter := counters.Counter{FieldName: "CPU_METRIC"}
	tests := []struct {
		name      string
		timestamp int64
	}{
		{name: "zero timestamp"},
		{name: "source timestamp", timestamp: 1_700_000_000_123_456},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := MetricsByCounter{
				counter: {{Counter: counter, CPU: "2", Timestamp: tt.timestamp}},
			}
			result := metrics.GoString()
			assert.Contains(t, result, `CPU:"2"`)
			assert.NotContains(t, result, "UUIDLabelName")
			assert.NotContains(t, result, "CPUCore")
			if tt.timestamp == 0 {
				assert.NotContains(t, result, "Timestamp")
			} else {
				assert.Contains(t, result, "Timestamp:1700000000123456")
			}
		})
	}
}

// TestMetricsByCounter_GoString_MultipleCounters verifies debug output includes each counter without depending on map order.
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
	require.Contains(t, result, `"DCGM_FI_DEV_GPU_TEMP": []collector.Metric{collector.Metric{Counter:counters.Counter{FieldID:0x96, FieldName:"DCGM_FI_DEV_GPU_TEMP", PromType:"gauge", Help:"Temperature Help info"}, Value:"42", GPU:"0", GPUUUID:"GPU-00000000-0000-0000-0000-000000000000", GPUDevice:"nvidia0", CPUSerial:"", GPUModelName:"NVIDIA T400 4GB", GPUPCIBusID:"", UUID:"UUID", MigProfile:"", NvSwitch:"", NvLink:"", GPUInstanceID:"", GPUComputeInstanceID:"", Hostname:"testhost", Labels:map[string]string{}, Attributes:map[string]string{}, ParentType:0x0}}`)
	require.Contains(t, result, `"DCGM_FI_DEV_POWER_USAGE": []collector.Metric{collector.Metric{Counter:counters.Counter{FieldID:0x9b, FieldName:"DCGM_FI_DEV_POWER_USAGE", PromType:"gauge", Help:"Power usage info"}, Value:"150", GPU:"0", GPUUUID:"GPU-00000000-0000-0000-0000-000000000000", GPUDevice:"nvidia0", CPUSerial:"", GPUModelName:"NVIDIA T400 4GB", GPUPCIBusID:"", UUID:"UUID", MigProfile:"", NvSwitch:"", NvLink:"", GPUInstanceID:"", GPUComputeInstanceID:"", Hostname:"testhost", Labels:map[string]string{}, Attributes:map[string]string{}, ParentType:0x0}}`)
	require.Contains(t, result, "MetricsByCounter{")
	require.Contains(t, result, "}")

	// Verify the structure is correct
	assert.True(t, strings.HasPrefix(result, "MetricsByCounter{"))
	assert.True(t, strings.HasSuffix(result, "}"))
}

// TestMetricsByCounter_String verifies the JSON string includes metric names, values, and GPU identity.
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

// TestMetricsByCounter_ToBase64 returns encoded output instead of the original JSON text.
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

// TestMetricsByCounter_MarshalJSON serializes metric names and values as nonempty JSON.
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

// TestMetricsByCounter_UnmarshalJSON restores counter metadata and sampled metric values from JSON.
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

// TestMetricsByCounterJSONRoundTripPreservesComputeInstanceID verifies that JSON preserves
// compute-instance IDs and timestamps, omitting timestamps when they are zero.
func TestMetricsByCounterJSONRoundTripPreservesComputeInstanceID(t *testing.T) {
	counter := counters.Counter{
		FieldID:   dcgm.DCGM_FI_DEV_FB_USED,
		FieldName: "DCGM_FI_DEV_FB_USED",
		PromType:  "gauge",
	}
	tests := []struct {
		name      string
		timestamp int64
	}{
		{name: "zero timestamp"},
		{name: "source timestamp", timestamp: 1_700_000_000_123_456},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := MetricsByCounter{
				counter: {{
					Counter:              counter,
					Value:                "42",
					Timestamp:            tt.timestamp,
					GPUComputeInstanceID: "2",
					Hostname:             "testhost",
					Labels:               map[string]string{},
					Attributes:           map[string]string{},
				}},
			}

			data, err := metrics.MarshalJSON()
			require.NoError(t, err)
			if tt.timestamp == 0 {
				assert.NotContains(t, string(data), `"timestamp"`)
			} else {
				assert.Contains(t, string(data), `"timestamp":1700000000123456`)
			}

			var got MetricsByCounter
			require.NoError(t, got.UnmarshalJSON(data))
			require.Len(t, got, 1)
			require.Len(t, got[counter], 1)
			assert.Equal(t, "2", got[counter][0].GPUComputeInstanceID)
			assert.Equal(t, tt.timestamp, got[counter][0].Timestamp)
		})
	}
}

// TestMetric_GetIDOfType selects the configured GPU identity, formats a MIG identity, and rejects unsupported ID types.
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
