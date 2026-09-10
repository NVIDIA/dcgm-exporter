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

package transformation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
)

type mockPIDMapper struct {
	result   map[uint32]*PodInfo
	errByPID map[uint32]error
}

func (m *mockPIDMapper) getPodUIDForPID(pid uint32) (string, error) {
	if err := m.errByPID[pid]; err != nil {
		return "", err
	}
	if pod := m.result[pid]; pod != nil {
		return pod.UID, nil
	}
	return "", nil
}

func (m *mockPIDMapper) buildPIDToPodMap(pids []uint32, pods []PodInfo) map[uint32]*PodInfo {
	return m.result
}

// TestPodResourcesMemoryAttributionLimitations covers issue #725 in both modes.
func TestPodResourcesMemoryAttributionLimitations(t *testing.T) {
	const (
		gpuA   = "GPU-00000000-0000-0000-0000-000000000000"
		gpuB   = "GPU-11111111-1111-1111-1111-111111111111"
		podUID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	pod := PodInfo{Name: "workload-1", Namespace: "default", UID: podUID, VGPU: "5"}
	secondSlot := pod
	secondSlot.VGPU = "19"

	for _, tc := range []struct {
		name         string
		deviceToPods map[string][]PodInfo
		direct       bool
		wantSamples  int
		wantQueries  int
	}{
		{
			name:         "two logical slots must produce one Pod memory sample",
			deviceToPods: map[string][]PodInfo{gpuB: {pod, secondSlot}},
			direct:       true, wantSamples: 1, wantQueries: 1,
		},
		{
			name:         "allocation on GPU A must not hide a process on GPU B",
			deviceToPods: map[string][]PodInfo{gpuA: {pod}},
			direct:       true, wantSamples: 1, wantQueries: 1,
		},
		{
			name:         "default mode preserves logical slot samples",
			deviceToPods: map[string][]PodInfo{gpuB: {pod, secondSlot}},
			wantSamples:  2, wantQueries: 1,
		},
		{
			name:         "default mode preserves candidate filtering",
			deviceToPods: map[string][]PodInfo{gpuA: {pod}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mocknvmlprovider.NewMockNVML(ctrl)
			client.EXPECT().GetDeviceProcessMemory(gpuB).Return(map[uint32]uint64{
				101: 6 * 1024 * 1024,
				102: 4 * 1024 * 1024,
			}, nil).Times(tc.wantQueries)
			client.EXPECT().GetDeviceProcessUtilization(gpuB).Return(map[uint32]uint32{101: 10, 102: 20}, nil).Times(tc.wantQueries)
			devInfo := mockdeviceinfo.NewMockProvider(ctrl)
			devInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
			devInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
				DeviceInfo: dcgm.Device{UUID: gpuB},
			}).AnyTimes()

			// Seed the cgroup cache so no host /proc files are required.
			pidMapper := newPIDToPodMapper()
			pidMapper.pidToUID[101] = podUID
			pidMapper.pidToUID[102] = podUID
			processCollector := &perProcessCollector{client: client, pidMapper: pidMapper, cgroupDirect: tc.direct}
			data := processCollector.Collect(map[string]string{gpuB: gpuB}, tc.deviceToPods, devInfo)

			mapper := &PodMapper{Config: &appconfig.Config{
				Kubernetes: true, KubernetesVirtualGPUs: true, KubernetesEnablePodUID: true,
			}}
			counter := counters.Counter{FieldName: metricFBUsed}
			original := collector.Metric{
				Counter: counter, GPUUUID: gpuB, Value: "100",
				Attributes: map[string]string{}, Labels: map[string]string{},
			}
			metrics, err := mapper.createPerProcessMetrics(original, counter, original, data,
				func() dcgm.Field_Entity_Group { return dcgm.FE_GPU })
			require.NoError(t, err)
			require.Len(t, metrics, tc.wantSamples)
			for _, metric := range metrics {
				assert.Equal(t, gpuB, metric.GPUUUID)
				assert.Equal(t, podUID, metric.Attributes[uidAttribute])
				assert.Equal(t, "10", metric.Value)
				if tc.direct {
					assert.NotContains(t, metric.Attributes, vgpuAttribute)
					assert.NotContains(t, metric.Labels, vgpuAttribute)
					assert.NotContains(t, metric.Attributes, containerAttribute)
				} else {
					assert.Contains(t, metric.Attributes, vgpuAttribute)
				}
			}

			// GPU_UTIL must continue to use the original candidate and slot mapping.
			util, err := mapper.createPerProcessMetrics(original, counters.Counter{FieldName: metricGPUUtil}, original, data,
				func() dcgm.Field_Entity_Group { return dcgm.FE_GPU })
			require.NoError(t, err)
			require.Len(t, util, len(tc.deviceToPods[gpuB]))
			for _, metric := range util {
				assert.Equal(t, "30", metric.Value)
				assert.Contains(t, metric.Attributes, vgpuAttribute)
			}
		})
	}
}

func TestCgroupMemoryAggregation(t *testing.T) {
	mapper := &mockPIDMapper{
		result:   map[uint32]*PodInfo{1: {UID: "pod-1"}, 2: {UID: "pod-1"}, 3: {UID: "pod-2"}},
		errByPID: map[uint32]error{3: errors.New("process exited")},
	}
	c := &perProcessCollector{pidMapper: mapper}
	memory := c.aggregatePodMemory(map[uint32]uint64{
		1: 1536 * 1024, 2: 1536 * 1024, 3: 5 * 1024 * 1024, 4: 6 * 1024 * 1024,
	})
	assert.Equal(t, map[string]uint64{"pod-1": 3 * 1024 * 1024}, memory)
}

func TestCgroupMemoryEmptyCollectionDoesNotFallBackToSlots(t *testing.T) {
	for _, name := range []string{"no processes", "NVML error", "nil client"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			devInfo := mockdeviceinfo.NewMockProvider(ctrl)
			devInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
			devInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{UUID: "GPU-1"}}).AnyTimes()
			pod := PodInfo{UID: "pod-1", VGPU: "5"}
			c := &perProcessCollector{cgroupDirect: true, pidMapper: &mockPIDMapper{result: map[uint32]*PodInfo{1: &pod}}}
			if name != "nil client" {
				client := mocknvmlprovider.NewMockNVML(ctrl)
				var memory map[uint32]uint64
				var err error
				if name == "NVML error" {
					memory, err = map[uint32]uint64{1: 10 * 1024 * 1024}, errors.New("NVML unavailable")
				}
				client.EXPECT().GetDeviceProcessMemory("GPU-1").Return(memory, err)
				client.EXPECT().GetDeviceProcessUtilization("GPU-1").Return(nil, nil)
				c.client = client
			}
			data := c.Collect(map[string]string{"GPU-1": "GPU-1"}, map[string][]PodInfo{"GPU-1": {pod}}, devInfo)
			mapper := &PodMapper{Config: &appconfig.Config{}}
			original := collector.Metric{GPUUUID: "GPU-1", Value: "100"}
			metrics, err := mapper.createPerProcessMetrics(original, counters.Counter{FieldName: metricFBUsed}, original, data,
				func() dcgm.Field_Entity_Group { return dcgm.FE_GPU })
			require.NoError(t, err)
			assert.NotNil(t, metrics, "an empty but handled result prevents logical-slot fallback")
			assert.Empty(t, metrics)
		})
	}
}

func TestCgroupMemoryPreservesMIGCollection(t *testing.T) {
	for _, withInstances := range []bool{false, true} {
		t.Run(fmt.Sprintf("instances=%t", withInstances), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mocknvmlprovider.NewMockNVML(ctrl)
			gpu := deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{UUID: "GPU-1"}, MigEnabled: true}
			if withInstances {
				gpu.GPUInstances = []deviceinfo.GPUInstanceInfo{{Info: dcgm.MigEntityInfo{NvmlInstanceId: 7}}}
				client.EXPECT().GetAllMIGDevicesProcessMemory("GPU-1").Return(map[uint]map[uint32]uint64{7: {1: 1024 * 1024}}, nil).Times(2)
			} else {
				client.EXPECT().GetDeviceProcessMemory("GPU-1").Return(map[uint32]uint64{1: 1024 * 1024}, nil).Times(2)
				client.EXPECT().GetDeviceProcessUtilization("GPU-1").Return(nil, nil).Times(2)
			}
			devInfo := mockdeviceinfo.NewMockProvider(ctrl)
			devInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
			devInfo.EXPECT().GPU(uint(0)).Return(gpu).AnyTimes()
			pod := PodInfo{UID: "pod-1", VGPU: "5"}
			c := &perProcessCollector{client: client, pidMapper: &mockPIDMapper{result: map[uint32]*PodInfo{1: &pod}}}
			deviceMap := map[string]string{"GPU-1": "GPU-1"}
			pods := map[string][]PodInfo{"GPU-1": {pod}, "0-7": {pod}}
			before := c.Collect(deviceMap, pods, devInfo)
			c.cgroupDirect = true
			after := c.Collect(deviceMap, pods, devInfo)
			assert.Equal(t, before, after)
			assert.Empty(t, after.podMemory)
		})
	}
}

func TestGetGPUUUIDToDeviceID(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
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
	t.Parallel()
	tests := []struct {
		fieldName string
		expected  bool
	}{
		{metricGPUUtil, true},
		{metricFBUsed, true},
		{"DCGM_FI_DEV_POWER_USAGE", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.fieldName, func(t *testing.T) {
			t.Parallel()
			result := isPerProcessMetric(tc.fieldName)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestPerProcessMetrics_GetAllPIDs(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	metrics := &perProcessMetrics{
		pidToSMUtil: map[uint32]uint32{
			1001: 50,
			1002: 0,
		},
		pidToMemory: map[uint32]uint64{
			1001: 1024 * 1024 * 1024,
			1002: 0,
		},
	}

	tests := []struct {
		name      string
		fieldName string
		pid       uint32
		expected  uint64
		hasValue  bool
	}{
		{
			name:      "GPU util",
			fieldName: metricGPUUtil,
			pid:       1001,
			expected:  50,
			hasValue:  true,
		},
		{
			name:      "GPU util zero",
			fieldName: metricGPUUtil,
			pid:       1002,
			expected:  0,
			hasValue:  true,
		},
		{
			name:      "FB used",
			fieldName: metricFBUsed,
			pid:       1001,
			expected:  1024,
			hasValue:  true,
		},
		{
			name:      "FB used zero",
			fieldName: metricFBUsed,
			pid:       1002,
			expected:  0,
			hasValue:  true,
		},
		{
			name:      "unknown metric",
			fieldName: "DCGM_FI_DEV_POWER_USAGE",
			pid:       1001,
			expected:  0,
			hasValue:  false,
		},
		{
			name:      "unknown PID",
			fieldName: metricGPUUtil,
			pid:       9999,
			expected:  0,
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
	t.Parallel()
	metrics := &perProcessMetrics{
		pidToSMUtil: make(map[uint32]uint32),
		pidToMemory: make(map[uint32]uint64),
	}

	pids := metrics.getAllPIDs()
	assert.Len(t, pids, 0)

	value, hasValue := metrics.getValueForMetric(metricGPUUtil, 1001)
	assert.False(t, hasValue)
	assert.Equal(t, uint64(0), value)
}

func TestPerProcessCollector_Collect(t *testing.T) {
	t.Parallel()
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
				"nvidia0": {{Name: "test-pod", Namespace: "default", UID: podUID0}},
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
		{
			name: "GetDeviceProcessMemory error - still collects utilization",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
				mockNVML.EXPECT().GetDeviceProcessMemory(gpu0UUID).Return(nil, fmt.Errorf("nvml error"))
				mockNVML.EXPECT().GetDeviceProcessUtilization(gpu0UUID).Return(map[uint32]uint32{1001: 50}, nil)

				mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
				mockDevInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
				mockDevInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{UUID: gpu0UUID, GPU: 0},
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
				assert.Nil(t, result.metrics[gpu0UUID].pidToMemory)
				assert.Equal(t, uint32(50), result.metrics[gpu0UUID].pidToSMUtil[1001])
			},
		},
		{
			name: "GetDeviceProcessUtilization error - still collects memory",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
				mockNVML.EXPECT().GetDeviceProcessMemory(gpu0UUID).Return(map[uint32]uint64{1001: 1024}, nil)
				mockNVML.EXPECT().GetDeviceProcessUtilization(gpu0UUID).Return(nil, fmt.Errorf("nvml error"))

				mockDevInfo := mockdeviceinfo.NewMockProvider(ctrl)
				mockDevInfo.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
				mockDevInfo.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
					DeviceInfo: dcgm.Device{UUID: gpu0UUID, GPU: 0},
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
				assert.Equal(t, uint64(1024), result.metrics[gpu0UUID].pidToMemory[1001])
				assert.Nil(t, result.metrics[gpu0UUID].pidToSMUtil)
			},
		},
		{
			name: "GetAllMIGDevicesProcessMemory error - returns empty data for that GPU",
			setupMocks: func(ctrl *gomock.Controller) (nvmlprovider.NVML, deviceinfo.Provider) {
				mockNVML := mocknvmlprovider.NewMockNVML(ctrl)
				mockNVML.EXPECT().GetAllMIGDevicesProcessMemory(gpu0UUID).Return(nil, fmt.Errorf("nvml error"))

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
			pidToPod: map[uint32]*PodInfo{},
			validate: func(t *testing.T, result *perProcessDataMap) {
				assert.Empty(t, result.metrics)
				assert.Empty(t, result.pidToPod)
				assert.Empty(t, result.deviceToPods)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
