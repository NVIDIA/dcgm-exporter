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

package transformation

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

type mockPIDMapper struct {
	result map[uint32]*PodInfo
}

func (m *mockPIDMapper) buildPIDToPodMap(pids []uint32, pods []PodInfo) map[uint32]*PodInfo {
	return m.result
}

func TestGetGPUUUIDToDeviceID(t *testing.T) {
	gpu0UUID := "GPU-00000000-0000-0000-0000-000000000000"
	gpu1UUID := "GPU-11111111-1111-1111-1111-111111111111"

	tests := []struct {
		name     string
		idType   appconfig.KubernetesGPUIDType
		expected map[string]string
	}{
		{
			name:   "device name type",
			idType: appconfig.DeviceName,
			expected: map[string]string{
				gpu0UUID: "nvidia0",
				gpu1UUID: "nvidia1",
			},
		},
		{
			name:   "GPU UUID type",
			idType: appconfig.GPUUID,
			expected: map[string]string{
				gpu0UUID: gpu0UUID,
				gpu1UUID: gpu1UUID,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
			mockDevInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
			mockDevInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
				DeviceInfo: dcgm.Device{UUID: gpu0UUID, GPU: 0},
			}).AnyTimes()
			mockDevInfo.EXPECT().GPU(uint(1)).Return(deviceinfo.GPUInfo{
				DeviceInfo: dcgm.Device{UUID: gpu1UUID, GPU: 1},
			}).AnyTimes()

			result := getGPUUUIDToDeviceID(mockDevInfo, tc.idType)

			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestIsPerProcessMetric(t *testing.T) {
	tests := []struct {
		fieldName string
		expected  bool
	}{
		{metricGPUUtil, true},
		{metricFBUsed, true},
		{metricGREngineActive, true},
		{"DCGM_FI_DEV_POWER_USAGE", false},
		{"DCGM_FI_DEV_GPU_TEMP", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.fieldName, func(t *testing.T) {
			result := isPerProcessMetric(tc.fieldName)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestPerProcessMetrics_GetAllPIDs(t *testing.T) {
	metrics := &perProcessMetrics{
		pidToSMUtil: map[uint32]uint32{
			1001: 50,
			1002: 30,
		},
		pidToMemory: map[uint32]uint64{
			1002: 1024,
			1003: 2048,
		},
	}

	pids := metrics.getAllPIDs()

	assert.Len(t, pids, 3)
	assert.Contains(t, pids, uint32(1001))
	assert.Contains(t, pids, uint32(1002))
	assert.Contains(t, pids, uint32(1003))
}

func TestPerProcessMetrics_GetValueForMetric(t *testing.T) {
	metrics := &perProcessMetrics{
		pidToSMUtil: map[uint32]uint32{
			1001: 50,
			1002: 100,
		},
		pidToMemory: map[uint32]uint64{
			1001: 1024 * 1024 * 1024,
			1002: 512 * 1024 * 1024,
		},
	}

	tests := []struct {
		name      string
		fieldName string
		pid       uint32
		expected  string
		hasValue  bool
	}{
		{
			name:      "GPU util - 50%",
			fieldName: metricGPUUtil,
			pid:       1001,
			expected:  "50",
			hasValue:  true,
		},
		{
			name:      "GPU util - 100%",
			fieldName: metricGPUUtil,
			pid:       1002,
			expected:  "100",
			hasValue:  true,
		},
		{
			name:      "FB used - 1GB",
			fieldName: metricFBUsed,
			pid:       1001,
			expected:  "1024",
			hasValue:  true,
		},
		{
			name:      "FB used - 512MB",
			fieldName: metricFBUsed,
			pid:       1002,
			expected:  "512",
			hasValue:  true,
		},
		{
			name:      "unknown metric",
			fieldName: "DCGM_FI_DEV_POWER_USAGE",
			pid:       1001,
			expected:  "",
			hasValue:  false,
		},
		{
			name:      "unknown PID",
			fieldName: metricGPUUtil,
			pid:       9999,
			expected:  "",
			hasValue:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value, hasValue := metrics.getValueForMetric(tc.fieldName, tc.pid)
			assert.Equal(t, tc.hasValue, hasValue)
			if tc.hasValue {
				assert.Equal(t, tc.expected, value)
			}
		})
	}
}

func TestPerProcessMetrics_EmptyMaps(t *testing.T) {
	metrics := &perProcessMetrics{
		pidToSMUtil: make(map[uint32]uint32),
		pidToMemory: make(map[uint32]uint64),
	}

	pids := metrics.getAllPIDs()
	assert.Len(t, pids, 0)

	value, hasValue := metrics.getValueForMetric(metricGPUUtil, 1001)
	assert.False(t, hasValue)
	assert.Equal(t, "", value)
}

func TestPerProcessCollector_Collect(t *testing.T) {
	gpu0UUID := "GPU-00000000-0000-0000-0000-000000000000"
	gpu1UUID := "GPU-11111111-1111-1111-1111-111111111111"
	podUID0 := "a9c80282-3f6b-4d5b-84d5-a137a6668011"
	podUID1 := "b9c80282-3f6b-4d5b-84d5-b137a6668022"

	pod0 := &PodInfo{Name: "test-pod", Namespace: "default", UID: podUID0, Container: "app"}
	pod1 := &PodInfo{Name: "pod1", Namespace: "ns1", UID: podUID1}

	tests := []struct {
		name         string
		setupMocks   func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider)
		gpuDeviceMap map[string]string
		deviceToPods map[string][]PodInfo
		pidToPod     map[uint32]*PodInfo
		validate     func(t *testing.T, result *perProcessDataMap)
	}{
		{
			name: "nil devInfo returns empty",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
				return mockNVML, nil
			},
			validate: func(t *testing.T, result *perProcessDataMap) {
				assert.Empty(t, result.metrics)
				assert.Empty(t, result.pidToPod)
				assert.Empty(t, result.deviceToPods)
			},
		},
		{
			name: "nil client returns empty",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
				return nil, mockDevInfo
			},
			validate: func(t *testing.T, result *perProcessDataMap) {
				assert.Empty(t, result.metrics)
				assert.Empty(t, result.pidToPod)
				assert.Empty(t, result.deviceToPods)
			},
		},
		{
			name: "single regular GPU with processes",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
				mockNVML.EXPECT().GetDeviceProcessMemory(gpu0UUID).Return(map[uint32]uint64{
					1001: 1024 * 1024 * 1024,
					1002: 512 * 1024 * 1024,
				}, nil)
				mockNVML.EXPECT().GetDeviceProcessUtilization(gpu0UUID).Return(map[uint32]uint32{
					1001: 50,
					1002: 30,
				}, nil)

				mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
				mockDevInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
				mockDevInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo:   dcgm.Device{UUID: gpu0UUID, GPU: 0},
					GPUInstances: nil,
				}).AnyTimes()
				return mockNVML, mockDevInfo
			},
			gpuDeviceMap: map[string]string{gpu0UUID: "nvidia0"},
			deviceToPods: map[string][]PodInfo{
				"nvidia0": {{Name: "test-pod", Namespace: "default", UID: podUID0, Container: "app"}},
			},
			pidToPod: map[uint32]*PodInfo{1001: pod0},
			validate: func(t *testing.T, result *perProcessDataMap) {
				assert.Contains(t, result.metrics, gpu0UUID)

				gpuMetrics := result.metrics[gpu0UUID]
				assert.Equal(t, uint32(50), gpuMetrics.pidToSMUtil[1001])
				assert.Equal(t, uint32(30), gpuMetrics.pidToSMUtil[1002])
				assert.Equal(t, uint64(1024*1024*1024), gpuMetrics.pidToMemory[1001])
				assert.Equal(t, uint64(512*1024*1024), gpuMetrics.pidToMemory[1002])

				assert.Len(t, result.pidToPod, 1)
				assert.Equal(t, "test-pod", result.pidToPod[1001].Name)
				assert.Equal(t, "default", result.pidToPod[1001].Namespace)

				assert.Contains(t, result.deviceToPods, gpu0UUID)
				assert.Len(t, result.deviceToPods[gpu0UUID], 1)
				assert.Equal(t, "test-pod", result.deviceToPods[gpu0UUID][0].Name)
			},
		},
		{
			name: "no pods using GPU skips collection",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)

				mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
				mockDevInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
				mockDevInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo:   dcgm.Device{UUID: gpu0UUID, GPU: 0},
					GPUInstances: nil,
				}).AnyTimes()
				return mockNVML, mockDevInfo
			},
			gpuDeviceMap: map[string]string{gpu0UUID: "nvidia0"},
			deviceToPods: map[string][]PodInfo{},
			validate: func(t *testing.T, result *perProcessDataMap) {
				assert.Empty(t, result.metrics)
				assert.Empty(t, result.pidToPod)
				assert.Empty(t, result.deviceToPods)
			},
		},
		{
			name: "MIG-enabled GPU",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
				mockNVML.EXPECT().GetAllMIGDevicesProcessMemory(gpu0UUID).Return(map[uint]map[uint32]uint64{
					1: {2001: 256 * 1024 * 1024},
				}, nil)

				mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
				mockDevInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
				mockDevInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{UUID: gpu0UUID, GPU: 0},
					MigEnabled: true,
					GPUInstances: []deviceinfo.GPUInstanceInfo{
						{Info: dcgm.MigEntityInfo{NvmlInstanceId: 1}, EntityId: 100},
					},
				}).AnyTimes()
				return mockNVML, mockDevInfo
			},
			gpuDeviceMap: map[string]string{gpu0UUID: "nvidia0"},
			deviceToPods: map[string][]PodInfo{
				"0-1": {{Name: "mig-pod", Namespace: "default", UID: podUID0, Container: "app"}},
			},
			pidToPod: map[uint32]*PodInfo{2001: {Name: "mig-pod", Namespace: "default", UID: podUID0}},
			validate: func(t *testing.T, result *perProcessDataMap) {
				migKey := getMIGMetricsKey(gpu0UUID, "1")
				assert.Contains(t, result.metrics, migKey)

				migMetrics := result.metrics[migKey]
				assert.Equal(t, uint64(256*1024*1024), migMetrics.pidToMemory[2001])
				assert.Nil(t, migMetrics.pidToSMUtil)

				assert.Len(t, result.pidToPod, 1)
				assert.Equal(t, "mig-pod", result.pidToPod[2001].Name)

				assert.Contains(t, result.deviceToPods, migKey)
				assert.Len(t, result.deviceToPods[migKey], 1)
				assert.Equal(t, "mig-pod", result.deviceToPods[migKey][0].Name)
			},
		},
		{
			name: "multiple regular GPUs",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
				mockNVML.EXPECT().GetDeviceProcessMemory(gpu0UUID).Return(map[uint32]uint64{1001: 100}, nil)
				mockNVML.EXPECT().GetDeviceProcessUtilization(gpu0UUID).Return(map[uint32]uint32{1001: 10}, nil)
				mockNVML.EXPECT().GetDeviceProcessMemory(gpu1UUID).Return(map[uint32]uint64{2001: 200}, nil)
				mockNVML.EXPECT().GetDeviceProcessUtilization(gpu1UUID).Return(map[uint32]uint32{2001: 20}, nil)

				mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
				mockDevInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
				mockDevInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{UUID: gpu0UUID, GPU: 0},
				}).AnyTimes()
				mockDevInfo.EXPECT().GPU(uint(1)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{UUID: gpu1UUID, GPU: 1},
				}).AnyTimes()
				return mockNVML, mockDevInfo
			},
			gpuDeviceMap: map[string]string{
				gpu0UUID: "nvidia0",
				gpu1UUID: "nvidia1",
			},
			deviceToPods: map[string][]PodInfo{
				"nvidia0": {{Name: "pod0", Namespace: "ns0", UID: podUID0}},
				"nvidia1": {{Name: "pod1", Namespace: "ns1", UID: podUID1}},
			},
			pidToPod: map[uint32]*PodInfo{
				1001: pod0,
				2001: pod1,
			},
			validate: func(t *testing.T, result *perProcessDataMap) {
				assert.Len(t, result.metrics, 2)
				assert.Contains(t, result.metrics, gpu0UUID)
				assert.Contains(t, result.metrics, gpu1UUID)

				assert.Equal(t, uint32(10), result.metrics[gpu0UUID].pidToSMUtil[1001])
				assert.Equal(t, uint64(100), result.metrics[gpu0UUID].pidToMemory[1001])
				assert.Equal(t, uint32(20), result.metrics[gpu1UUID].pidToSMUtil[2001])
				assert.Equal(t, uint64(200), result.metrics[gpu1UUID].pidToMemory[2001])

				assert.Len(t, result.pidToPod, 2)
				assert.Equal(t, "test-pod", result.pidToPod[1001].Name)
				assert.Equal(t, "pod1", result.pidToPod[2001].Name)

				assert.Len(t, result.deviceToPods, 2)
				assert.Contains(t, result.deviceToPods, gpu0UUID)
				assert.Contains(t, result.deviceToPods, gpu1UUID)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			client, devInfo := tc.setupMocks(ctrl)

			collector := &perProcessCollector{
				client:    client,
				pidMapper: &mockPIDMapper{result: tc.pidToPod},
			}

			result := collector.Collect(tc.gpuDeviceMap, tc.deviceToPods, devInfo)

			assert.NotNil(t, result)
			tc.validate(t, result)
		})
	}
}
