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
)

type watchedEntityKey struct {
	parentID uint
	childID  uint
}

var fakeProfileName = "2fake.4gb"

var (
	gpuInstanceInfo1 = deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    0,
	}

	gpuInstanceInfo2 = deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlInstanceId: 1, NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    14,
	}

	nvLinkVal1 = dcgm.NvLinkStatus{
		State: 2,
		Index: 0,
	}

	nvLinkVal2 = dcgm.NvLinkStatus{
		State: 3,
		Index: 1,
	}
)

func MockGPUDeviceInfo(
	ctrl *gomock.Controller, gpuCount int, gpuToGpuInstanceInfos map[int][]deviceinfo.GPUInstanceInfo,
) *mockdeviceinfo.MockProvider {
	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)

	mockGPUs := make([]deviceinfo.GPUInfo, 0)

	for i := range gpuCount {
		gpuInfo := deviceinfo.GPUInfo{}
		gpuInfo.DeviceInfo.GPU = uint(i)

		if gpuInstanceInfos, exist := gpuToGpuInstanceInfos[i]; exist {
			gpuInfo.GPUInstances = gpuInstanceInfos
		}

		mockGPUs = append(mockGPUs, gpuInfo)
		mockSystemInfo.EXPECT().GPU(uint(i)).Return(gpuInfo).AnyTimes()
	}

	mockSystemInfo.EXPECT().GPUCount().Return(uint(gpuCount)).AnyTimes()
	mockSystemInfo.EXPECT().GPUs().Return(mockGPUs).AnyTimes()
	mockSystemInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()

	return mockSystemInfo
}

func MockCPUDeviceInfo(
	ctrl *gomock.Controller, cpuCount int, cpuToCores map[int][]uint, watchedCPUs map[uint]bool,
	watchedCores map[watchedEntityKey]bool, infoType dcgm.Field_Entity_Group,
) *mockdeviceinfo.MockProvider {
	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)

	mockCPUs := make([]deviceinfo.CPUInfo, 0)

	for i := range cpuCount {
		cpuInfo := deviceinfo.CPUInfo{}
		cpuInfo.EntityId = uint(i)

		if cores, exist := cpuToCores[i]; exist {
			cpuInfo.Cores = []uint{}

			for _, core := range cores {
				cpuInfo.Cores = append(cpuInfo.Cores, core)

				mockSystemInfo.EXPECT().IsCoreWatched(core,
					uint(i)).Return(watchedCores[watchedEntityKey{uint(i), core}]).AnyTimes()
			}
		}

		mockSystemInfo.EXPECT().IsCPUWatched(cpuInfo.EntityId).Return(watchedCPUs[cpuInfo.EntityId]).AnyTimes()
		mockSystemInfo.EXPECT().CPU(uint(i)).Return(cpuInfo).AnyTimes()

		mockCPUs = append(mockCPUs, cpuInfo)
	}

	mockSystemInfo.EXPECT().CPUs().Return(mockCPUs).AnyTimes()
	mockSystemInfo.EXPECT().InfoType().Return(infoType).AnyTimes()

	return mockSystemInfo
}

func MockSwitchDeviceInfo(
	ctrl *gomock.Controller, switchCount int, switchToNvLinks map[int][]dcgm.NvLinkStatus,
	watchedSwitches map[uint]bool, watchedLinks map[watchedEntityKey]bool, infoType dcgm.Field_Entity_Group,
) *mockdeviceinfo.MockProvider {
	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)

	mockSwitches := make([]deviceinfo.SwitchInfo, 0)

	for i := range switchCount {
		switchInfo := deviceinfo.SwitchInfo{}
		switchInfo.EntityId = uint(i)

		if nvLinks, exist := switchToNvLinks[i]; exist {
			switchInfo.NvLinks = []dcgm.NvLinkStatus{}

			for _, nvLink := range nvLinks {
				nvLink.ParentId = uint(i)
				nvLink.ParentType = dcgm.FE_SWITCH
				switchInfo.NvLinks = append(switchInfo.NvLinks, nvLink)

				mockSystemInfo.EXPECT().IsLinkWatched(nvLink.Index,
					uint(i)).Return(watchedLinks[watchedEntityKey{uint(i), nvLink.Index}]).AnyTimes()
			}
		}

		mockSystemInfo.EXPECT().IsSwitchWatched(switchInfo.EntityId).Return(watchedSwitches[switchInfo.EntityId]).AnyTimes()
		mockSystemInfo.EXPECT().Switch(uint(i)).Return(switchInfo).AnyTimes()

		mockSwitches = append(mockSwitches, switchInfo)
	}

	mockSystemInfo.EXPECT().Switches().Return(mockSwitches).AnyTimes()
	mockSystemInfo.EXPECT().InfoType().Return(infoType).AnyTimes()

	return mockSystemInfo
}

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

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, nil)
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
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{-1},
					MinorRange: []int{-1},
				}

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
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
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major -1, Minor 14",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{-1},
					MinorRange: []int{14},
				}

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
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
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major 1, Minor -1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{1},
					MinorRange: []int{-1},
				}

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
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
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major 0, Minor 14",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MajorRange: []int{0},
					MinorRange: []int{14},
				}

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
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
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Minor -1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex:       false,
					MinorRange: []int{-1},
				}

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 1 each, Flex = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 2 and 0, Flex = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1, gpuInstanceInfo2}

				ctrl := gomock.NewController(t)

				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo2,
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
				return MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
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

				switchToNvLinks[0] = []dcgm.NvLinkStatus{nvLinkVal1, nvLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{nvLinkVal1, nvLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[watchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: true,
					{1, 0}: true,
					{1, 1}: true,
				}
				return MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks, dcgm.FE_LINK)
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
			name: "Switch Count 3, watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: false, 1: true, 2: false}
				return MockCPUDeviceInfo(ctrl, 3, nil, watchedCPUs, nil, dcgm.FE_CPU)
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
				watchedCores := map[watchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: false,
					{1, 0}: false,
					{1, 1}: true,
				}
				return MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
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
				return MockGPUDeviceInfo(ctrl, 0, nil)
			},
			want: nil,
		},
		{
			name: "GPU Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
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
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
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
				return MockGPUDeviceInfo(ctrl, 0, nil)
			},
			addFlexibly: true,
			want:        nil,
		},
		{
			name: "GPU Count 0, addFlexibly false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 0, nil)
			},
			addFlexibly: false,
			want:        nil,
		},
		{
			name: "GPU Count 1, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, nil)
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
				return MockGPUDeviceInfo(ctrl, 1, nil)
			},
			addFlexibly: false,
			want:        nil,
		},
		{
			name: "GPU Count 2, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, nil)
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
				return MockGPUDeviceInfo(ctrl, 2, nil)
			},
			addFlexibly: false,
			want:        nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 1, GPU Instance Count 2",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1, gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 1 each",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: &gpuInstanceInfo2,
					ParentId:     PARENT_ID_IGNORED,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 2 and 0, addFlexibly true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1, gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			addFlexibly: true,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo2,
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
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1, gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			addFlexibly: false,
			want: []Info{
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo1,
					ParentId:     PARENT_ID_IGNORED,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
					DeviceInfo: dcgm.Device{
						GPU: uint(0),
					},
					InstanceInfo: &gpuInstanceInfo2,
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
				return MockSwitchDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: true}
				return MockSwitchDeviceInfo(ctrl, 1, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
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
				return MockSwitchDeviceInfo(ctrl, 1, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 2, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: true, 1: true}
				return MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
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
				return MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 3, watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: false, 1: true, 2: false}
				return MockSwitchDeviceInfo(ctrl, 3, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
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
				return MockSwitchDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 2, Link Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedSwitches := map[uint]bool{0: true, 1: true}
				return MockSwitchDeviceInfo(ctrl, 2, nil, watchedSwitches, nil, dcgm.FE_SWITCH)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, Link Count 1, Switch Watched = true, Link Watched = true, Link Up = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{nvLinkVal2}

				watchedSwitches := map[uint]bool{0: true}
				watchedLinks := map[watchedEntityKey]bool{
					{0, 1}: true,
				}
				return MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks, dcgm.FE_LINK)
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
				switchToNvLinks[0] = []dcgm.NvLinkStatus{nvLinkVal2}

				watchedSwitches := map[uint]bool{0: false}
				watchedLinks := map[watchedEntityKey]bool{
					{0, 1}: true,
				}
				return MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks, dcgm.FE_LINK)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, Link Count 1, Switch Watched = true, Link Watched = false, Link Up = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{nvLinkVal2}

				watchedSwitches := map[uint]bool{0: true}
				watchedLinks := map[watchedEntityKey]bool{
					{0, 1}: false,
				}
				return MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks, dcgm.FE_LINK)
			},
			want: nil,
		},
		{
			name: "Switch Count 1, Link Count 1, Switch Watched = true, Link Watched = true, Link Up = false",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{nvLinkVal1}

				watchedSwitches := map[uint]bool{0: true}
				watchedLinks := map[watchedEntityKey]bool{
					{0, 0}: true,
				}
				return MockSwitchDeviceInfo(ctrl, 1, switchToNvLinks, watchedSwitches, watchedLinks, dcgm.FE_LINK)
			},
			want: nil,
		},
		{
			name: "Switch Count 2, Link Count 2, Switch Watched = true, Link Watched = true, Link Up = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)

				switchToNvLinks[0] = []dcgm.NvLinkStatus{nvLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{nvLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[watchedEntityKey]bool{
					{0, 1}: true,
					{1, 1}: true,
				}
				return MockSwitchDeviceInfo(ctrl, 2, switchToNvLinks, watchedSwitches, watchedLinks, dcgm.FE_LINK)
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

				switchToNvLinks[0] = []dcgm.NvLinkStatus{nvLinkVal1, nvLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{nvLinkVal1, nvLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[watchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: false,
					{1, 0}: true,
					{1, 1}: true,
				}
				return MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks, dcgm.FE_LINK)
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
				return MockCPUDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_CPU)
			},
			want: nil,
		},
		{
			name: "CPU Count 1, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: true}
				return MockCPUDeviceInfo(ctrl, 1, nil, watchedCPUs, nil, dcgm.FE_CPU)
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
				return MockCPUDeviceInfo(ctrl, 1, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: nil,
		},
		{
			name: "CPU Count 2, watched = true",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: true, 1: true}
				return MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil, dcgm.FE_CPU)
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
				return MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil, dcgm.FE_CPU)
			},
			want: nil,
		},
		{
			name: "Switch Count 3, watched = mix",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: false, 1: true, 2: false}
				return MockCPUDeviceInfo(ctrl, 3, nil, watchedCPUs, nil, dcgm.FE_CPU)
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
				return MockCPUDeviceInfo(ctrl, 0, nil, nil, nil, dcgm.FE_CPU_CORE)
			},
			want: nil,
		},
		{
			name: "CPU Count 2, Core Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				watchedCPUs := map[uint]bool{0: true, 1: true}
				return MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil, dcgm.FE_CPU_CORE)
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
				watchedCores := map[watchedEntityKey]bool{
					{0, 1}: true,
				}
				return MockCPUDeviceInfo(ctrl, 1, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
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
				watchedCores := map[watchedEntityKey]bool{
					{0, 1}: true,
				}
				return MockCPUDeviceInfo(ctrl, 1, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
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
				watchedCores := map[watchedEntityKey]bool{
					{0, 1}: false,
				}
				return MockCPUDeviceInfo(ctrl, 1, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
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
				watchedCores := map[watchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: true,
					{1, 0}: true,
					{1, 1}: true,
				}
				return MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
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
				watchedCores := map[watchedEntityKey]bool{
					{0, 0}: true,
					{0, 1}: false,
					{1, 0}: false,
					{1, 1}: true,
				}
				return MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores, dcgm.FE_CPU_CORE)
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
				return MockGPUDeviceInfo(ctrl, 0, nil)
			},
			gpuID: 0,
			want:  nil,
		},
		{
			name: "GPU Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
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
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuID: 1000,
			want:  nil,
		},
		{
			name: "GPU Count 2, one GPU ID match",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
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
				return MockGPUDeviceInfo(ctrl, 0, nil)
			},
			gpuInstanceID: 0,
			want:          nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, nil)
			},
			gpuInstanceID: 0,
			want:          nil,
		},
		{
			name: "GPU Count 2, GPU Instance Count 0",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, nil)
			},
			gpuInstanceID: 0,
			want:          nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 1",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuInstanceID: 0,
			want: &Info{
				Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo1.EntityId},
				DeviceInfo: dcgm.Device{
					GPU: uint(0),
				},
				InstanceInfo: &gpuInstanceInfo1,
				ParentId:     PARENT_ID_IGNORED,
			},
		},
		{
			name: "GPU Count 1, GPU Instance Count 1, GPU Instance ID mismatch",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuInstanceID: 1000,
			want:          nil,
		},
		{
			name: "GPU Count 1, GPU Instance Count 2, one match",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1, gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 1, gpuInstanceInfos)
			},
			gpuInstanceID: 14,
			want: &Info{
				Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
				DeviceInfo: dcgm.Device{
					GPU: uint(0),
				},
				InstanceInfo: &gpuInstanceInfo2,
				ParentId:     PARENT_ID_IGNORED,
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 1 each, one match",
			mockFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

				ctrl := gomock.NewController(t)
				return MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
			},
			gpuInstanceID: 14,
			want: &Info{
				Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: gpuInstanceInfo2.EntityId},
				DeviceInfo: dcgm.Device{
					GPU: uint(1),
				},
				InstanceInfo: &gpuInstanceInfo2,
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
