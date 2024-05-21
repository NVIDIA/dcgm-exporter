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

package devicemonitoring

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestGetMonitoredEntities(t *testing.T) {
	tests := []struct {
		name     string
		mockFunc func() *mockdeviceinfo.MockProvider
		want     []Info
	}{
		{
			name: "GPU Count 2, Flex = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major -1, Minor -1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{-1},
					MinorRange: []int{-1},
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major -1, Minor 14",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{-1},
					MinorRange: []int{14},
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major 1, Minor -1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{1},
					MinorRange: []int{-1},
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major 0, Minor 14",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{0},
					MinorRange: []int{14},
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Minor -1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MinorRange: []int{-1},
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 1 each, Flex = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 2 and 0, Flex = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{
					testutils.MockGPUInstanceInfo1,
					testutils.MockGPUInstanceInfo2,
				}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "Switch Count 2, Watched 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: false, 1: true}
				return testutils.MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "Switch Count 5, Link Count 4, Switch Watched = true, Link Watched = true, link-up = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)

				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: true,
					{1, 0}: true,
					{1, 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
				},
			},
		},
		{
			name: "CPU Count 3, watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: false, 1: true, 2: false}
				return testutils.MockCPUDeviceInfo(ctrl, 3, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "CPU Count 2, Core Count 4, CPU Watched = true, Core Watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: false,
					{1, 0}: false,
					{1, 1}: true,
				}
				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
				},
			},
		},
	}
	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := GetMonitoredEntities(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorAllGPUs(t *testing.T) {
	tests := []struct {
		name     string
		mockFunc func() *mockdeviceinfo.MockProvider
		want     []Info
	}{
		{
			name: "GPU Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 0, nil)
			},
			want: nil,
		},
		{
			name: "GPU Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deviceInfo := tt.mockFunc()
			got := monitorAllGPUs(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorAllGPUInstances(t *testing.T) {
	tests := []struct {
		name        string
		mockFunc    func() *mockdeviceinfo.MockProvider
		addFlexibly bool
		want        []Info
	}{
		{
			name: "GPU Count 0, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 0, nil)
			},
			addFlexibly: true,
			want:        nil,
		},
		{
			name: "GPU Count 0, addFlexibly false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 0, nil)
			},
			addFlexibly: false,
			want:        nil,
		},
		{
			name: "GPU Count 1, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, nil)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 1, addFlexibly false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, nil)
			},
			addFlexibly: false,
			want:        nil,
		},
		{
			name: "GPU Count 2, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, nil)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, addFlexibly false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, nil)
			},
			addFlexibly: false,
			want:        nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 1, GPU Instance Count 2",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{
					testutils.MockGPUInstanceInfo1,
					testutils.MockGPUInstanceInfo2,
				}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 1 each",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 2 and 0, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{
					testutils.MockGPUInstanceInfo1,
					testutils.MockGPUInstanceInfo2,
				}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 2 and 0, addFlexibly false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{
					testutils.MockGPUInstanceInfo1,
					testutils.MockGPUInstanceInfo2,
				}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			addFlexibly: false,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
					},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &testutils.MockGPUInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deviceInfo := tt.mockFunc()
			got := monitorAllGPUInstances(deviceInfo, tt.addFlexibly)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorAllSwitches(t *testing.T) {
	tests := []struct {
		name     string
		mockFunc func() *mockdeviceinfo.MockProvider
		want     []Info
	}{
		{
			name: "Switch Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockSwitchDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: true}
				return testutils.MockSwitchDeviceInfo(ctrl, 1, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "Switch Count 1, watched = false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: false}
				return testutils.MockSwitchDeviceInfo(ctrl, 1, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 2, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: true, 1: true}
				return testutils.MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "Switch Count 2, watched = false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: false, 1: false}
				return testutils.MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 3, watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: false, 1: true, 2: false}
				return testutils.MockSwitchDeviceInfo(ctrl, 3, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_SWITCH, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
	}
	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := monitorAllSwitches(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorAllLinks(t *testing.T) {
	tests := []struct {
		name     string
		mockFunc func() *mockdeviceinfo.MockProvider
		want     []Info
	}{
		{
			name: "Switch Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockSwitchDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 2, Link Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: true, 1: true}
				return testutils.MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, Link Count 1, Switch Watched = true, Link Watched = true, Link Up = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{0, 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
			},
		},
		{
			name: "Switch Count 1, Link Count 1, Switch Watched = false, Link Watched = true, Link Up = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: false}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{0, 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, Link Count 1, Switch Watched = true, Link Watched = false, Link Up = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{0, 1}: false,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, Link Count 1, Switch Watched = true, Link Watched = true, Link Up = false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1}

				watchedSwitches := map[uint]bool{0: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{0, 0}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			want: nil,
		},
		{
			name: "Switch Count 2, Link Count 2, Switch Watched = true, Link Watched = true, Link Up = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)

				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{0, 1}: true,
					{1, 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 2, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
				},
			},
		},
		{
			name: "Switch Count 5, Link Count 4, Switch Watched = true, Link Watched = mix, Link Up = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)

				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: false,
					{1, 0}: true,
					{1, 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
				},
			},
		},
	}
	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := monitorAllLinks(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorAllCPUs(t *testing.T) {
	tests := []struct {
		name     string
		mockFunc func() *mockdeviceinfo.MockProvider
		want     []Info
	}{
		{
			name: "CPU Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockCPUDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_CPU)
			},
			want: nil,
		},
		{
			name: "CPU Count 1, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: true}
				return testutils.MockCPUDeviceInfo(ctrl, 1, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "CPU Count 1, watched = false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: false}
				return testutils.MockCPUDeviceInfo(ctrl, 1, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: nil,
		},
		{
			name: "CPU Count 2, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: true, 1: true}
				return testutils.MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "CPU Count 2, watched = false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: false, 1: false}
				return testutils.MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: nil,
		},
		{
			name: "Switch Count 3, watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: false, 1: true, 2: false}
				return testutils.MockCPUDeviceInfo(ctrl, 3, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
	}
	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := monitorAllCPUs(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorAllCPUCores(t *testing.T) {
	tests := []struct {
		name     string
		mockFunc func() *mockdeviceinfo.MockProvider
		want     []Info
	}{
		{
			name: "CPU Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockCPUDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_CPU_CORE)
			},
			want: nil,
		},
		{
			name: "CPU Count 2, Core Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: true, 1: true}
				return testutils.MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil, dcgm.FE_CPU_CORE)
			},
			want: nil,
		},
		{
			name: "CPU Count 1, Core Count 1, CPU Watched = true, Core Watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{1}

				watchedCPUs := map[uint]bool{0: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{0, 1}: true,
				}
				return testutils.MockCPUDeviceInfo(ctrl, 1, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
			},
		},
		{
			name: "CPU Count 1, Core Count 1, CPU Watched = false, Core Watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{1}

				watchedCPUs := map[uint]bool{0: false}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{0, 1}: true,
				}
				return testutils.MockCPUDeviceInfo(ctrl, 1, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
			},
			want: nil,
		},
		{
			name: "CPU Count 1, Core Count 1, CPU Watched = true, Core Watched = false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{1}

				watchedCPUs := map[uint]bool{0: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{0, 1}: false,
				}
				return testutils.MockCPUDeviceInfo(ctrl, 1, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
			},
			want: nil,
		},
		{
			name: "CPU Count 2, Core Count 4, CPU Watched = true, Core Watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: true,
					{1, 0}: true,
					{1, 1}: true,
				}
				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
				},
			},
		},
		{
			name: "CPU Count 2, Core Count 4, CPU Watched = true, Core Watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: false,
					{1, 0}: false,
					{1, 1}: true,
				}
				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
			},
			want: []Info{
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
				},
			},
		},
	}
	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := monitorAllCPUCores(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorGPU(t *testing.T) {
	tests := []struct {
		name     string
		mockFunc func() *mockdeviceinfo.MockProvider
		gpuID    int
		want     *Info
	}{
		{
			name: "GPU Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 0, nil)
			},
			gpuID: 0,
			want:  nil,
		},
		{
			name: "GPU Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuID: 0,
			want: &Info{
				Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(0)},
				DeviceInfo: dcgm.Device{
					GPU: uint(0),
				},
				InstanceInfo: nil,
				ParentId:     PARENT_ID_IGNORED,
			},
		},
		{
			name: "GPU Count 1, gpuID mismatch",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuID: 1000,
			want:  nil,
		},
		{
			name: "GPU Count 2, one GPU ID match",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			gpuID: 1,
			want: &Info{
				Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
				DeviceInfo: dcgm.Device{
					GPU: uint(1),
				},
				InstanceInfo: nil,
				ParentId:     PARENT_ID_IGNORED,
			},
		},
	}

	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := monitorGPU(deviceInfo, tt.gpuID)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func Test_monitorGPUInstance(t *testing.T) {
	tests := []struct {
		name          string
		mockFunc      func() *mockdeviceinfo.MockProvider
		gpuInstanceID int
		want          *Info
	}{
		{
			name: "GPU Count 0, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 0, nil)
			},
			gpuInstanceID: 0,
			want:          nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, nil)
			},
			gpuInstanceID: 0,
			want:          nil,
		},
		{
			name: "GPU Count 2, GPU Instance Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, nil)
			},
			gpuInstanceID: 0,
			want:          nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuInstanceID: 0,
			want: &Info{
				Entity: dcgm.GroupEntityPair{
					EntityGroupId: dcgm.FE_GPU_I,
					EntityId:      testutils.MockGPUInstanceInfo1.EntityId,
				},
				DeviceInfo: dcgm.Device{
					GPU: uint(0),
				},
				InstanceInfo: &testutils.MockGPUInstanceInfo1,
				ParentId:     PARENT_ID_IGNORED,
			},
		},
		{
			name: "GPU Count 1, GPU Instance Count 1, GPU Instance ID mismatch",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuInstanceID: 1000,
			want:          nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 2, one match",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{
					testutils.MockGPUInstanceInfo1,
					testutils.MockGPUInstanceInfo2,
				}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuInstanceID: 14,
			want: &Info{
				Entity: dcgm.GroupEntityPair{
					EntityGroupId: dcgm.FE_GPU_I,
					EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
				},
				DeviceInfo: dcgm.Device{
					GPU: uint(0),
				},
				InstanceInfo: &testutils.MockGPUInstanceInfo2,
				ParentId:     PARENT_ID_IGNORED,
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 1 each, one match",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				ctrl := gomock.NewController(t)
				return testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			gpuInstanceID: 14,
			want: &Info{
				Entity: dcgm.GroupEntityPair{
					EntityGroupId: dcgm.FE_GPU_I,
					EntityId:      testutils.MockGPUInstanceInfo2.EntityId,
				},
				DeviceInfo: dcgm.Device{
					GPU: uint(1),
				},
				InstanceInfo: &testutils.MockGPUInstanceInfo2,
				ParentId:     PARENT_ID_IGNORED,
			},
		},
	}
	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := monitorGPUInstance(deviceInfo, tt.gpuInstanceID)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}
