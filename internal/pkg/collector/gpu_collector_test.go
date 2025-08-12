/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
	"fmt"
	"reflect"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

func TestToMetric(t *testing.T) {
	fieldValue := [4096]byte{}
	fieldValue[0] = 42
	values := []dcgm.FieldValue_v1{
		{
			FieldID:   150,
			FieldType: dcgm.DCGM_FT_INT64,
			Value:     fieldValue,
		},
	}

	c := []counters.Counter{
		{
			FieldID:   150,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		},
	}

	d := dcgm.Device{
		UUID: "fake0",
		Identifiers: dcgm.DeviceIdentifiers{
			Model: "NVIDIA T400 4GB",
		},
		PCI: dcgm.PCIInfo{
			BusID: "00000000:0000:0000.0",
		},
	}

	var instanceInfo *deviceinfo.GPUInstanceInfo = nil

	type testCase struct {
		replaceBlanksInModelName bool
		expectedGPUModelName     string
	}

	testCases := []testCase{
		{
			replaceBlanksInModelName: true,
			expectedGPUModelName:     "NVIDIA-T400-4GB",
		},
		{
			replaceBlanksInModelName: false,
			expectedGPUModelName:     "NVIDIA T400 4GB",
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("When replaceBlanksInModelName is %t", tc.replaceBlanksInModelName), func(t *testing.T) {
			metrics := make(map[counters.Counter][]Metric)
			toMetric(metrics, values, c, d, instanceInfo, false, "", tc.replaceBlanksInModelName)
			assert.Len(t, metrics, 1)
			// We get metric value with 0 index
			metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]
			assert.Equal(t, "42", metricValues[0].Value)
			assert.Equal(t, tc.expectedGPUModelName, metricValues[0].GPUModelName)

			assert.Equal(t, d.UUID, metricValues[0].GPUUUID)
			assert.Equal(t, d.PCI.BusID, metricValues[0].GPUPCIBusID)
		})
	}
}

func TestToMetricWhenDCGM_FI_DEV_XID_ERRORSField(t *testing.T) {
	c := []counters.Counter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
			FieldName: "DCGM_FI_DEV_GPU_TEMP",
			PromType:  "gauge",
			Help:      "Temperature Help info",
		},
	}

	d := dcgm.Device{
		UUID: "fake0",
		Identifiers: dcgm.DeviceIdentifiers{
			Model: "NVIDIA T400 4GB",
		},
		PCI: dcgm.PCIInfo{
			BusID: "00000000:0000:0000.0",
		},
	}

	var instanceInfo *deviceinfo.GPUInstanceInfo = nil

	type testCase struct {
		name        string
		fieldValue  byte
		expectedErr string
	}

	testCases := []testCase{
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has no error",
			fieldValue:  0,
			expectedErr: xidErrCodeToText[0],
		},
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has known value",
			fieldValue:  42,
			expectedErr: xidErrCodeToText[42],
		},
		{
			name:        "when DCGM_FI_DEV_XID_ERRORS has unknown value",
			fieldValue:  255,
			expectedErr: unknownErr,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fieldValue := [4096]byte{}
			fieldValue[0] = tc.fieldValue
			values := []dcgm.FieldValue_v1{
				{
					FieldID:   dcgm.DCGM_FI_DEV_XID_ERRORS,
					FieldType: dcgm.DCGM_FT_INT64,
					Value:     fieldValue,
				},
			}

			metrics := make(map[counters.Counter][]Metric)
			toMetric(metrics, values, c, d, instanceInfo, false, "", false)
			assert.Len(t, metrics, 1)
			// We get metric value with 0 index
			metricValues := metrics[reflect.ValueOf(metrics).MapKeys()[0].Interface().(counters.Counter)]
			assert.Equal(t, fmt.Sprint(tc.fieldValue), metricValues[0].Value)
			assert.Contains(t, metricValues[0].Attributes, "err_code")
			assert.Equal(t, fmt.Sprint(tc.fieldValue), metricValues[0].Attributes["err_code"])
			assert.Contains(t, metricValues[0].Attributes, "err_msg")
			assert.Equal(t, tc.expectedErr, metricValues[0].Attributes["err_msg"])

			assert.Equal(t, d.UUID, metricValues[0].GPUUUID)
			assert.Equal(t, d.PCI.BusID, metricValues[0].GPUPCIBusID)
		})
	}
}
