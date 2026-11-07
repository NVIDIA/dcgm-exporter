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
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

// monitoredEntitiesCase describes one entity topology and the entities GetMonitoredEntities should return for it.
type monitoredEntitiesCase struct {
	name string
	// mockFunc builds this case's mocked entity topology; it receives the subtest so mock expectations are checked there.
	mockFunc func(t *testing.T) *mockdeviceinfo.MockProvider
	want     []Info
}

// TestGetMonitoredEntities checks which entities are monitored for each supported entity type and selection mode.
func TestGetMonitoredEntities(t *testing.T) {
	t.Run("flexible GPU selection", func(t *testing.T) {
		runMonitoredEntitiesCases(t, monitoredEntitiesFlexGPUCases())
	})
	t.Run("GPU and GPU instance ranges", func(t *testing.T) {
		runMonitoredEntitiesCases(t, monitoredEntitiesGPURangeCases())
	})
	t.Run("switches and NVLinks", func(t *testing.T) {
		runMonitoredEntitiesCases(t, monitoredEntitiesSwitchCases())
	})
	t.Run("CPUs and CPU cores", func(t *testing.T) {
		runMonitoredEntitiesCases(t, monitoredEntitiesCPUCases())
	})
}

// monitoredEntitiesFlexGPUCases defines the Flex scenarios, which monitor a GPU's instances when it has any and the whole GPU otherwise.
func monitoredEntitiesFlexGPUCases() []monitoredEntitiesCase {
	return []monitoredEntitiesCase{
		{
			name: "GPU Count 2, Flex = true",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
			name: "GPU Count 2, GPU Instance Count 1 each, Flex = true",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
				},
			},
		},
		{
			name: "GPU Count 2, GPU Instance Count 2 and 0, Flex = true",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
					ParentType:   dcgm.FE_NONE,
				},
			},
		},
	}
}

// monitoredEntitiesGPURangeCases defines the GPU and GPU instance scenarios where Flex is off and the major and minor ranges choose the entities.
func monitoredEntitiesGPURangeCases() []monitoredEntitiesCase {
	return []monitoredEntitiesCase{
		{
			name: "GPU Count 2, Flex = false, Major -1, Minor -1",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major -1, Minor 14",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_GPU,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major 1, Minor -1",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Major 0, Minor 14",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_GPU,
				},
			},
		},
		{
			name: "GPU Count 2, Flex = false, Minor -1",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
				},
			},
		},
	}
}

// monitoredEntitiesSwitchCases defines the switch and switch NVLink scenarios; only watched switches and watched links that are up are returned.
func monitoredEntitiesSwitchCases() []monitoredEntitiesCase {
	return []monitoredEntitiesCase{
		{
			name: "Switch Count 2, Watched 1",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_SWITCH,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
					ParentType:   dcgm.FE_SWITCH,
				},
			},
		},
	}
}

// monitoredEntitiesCPUCases defines the CPU and CPU core scenarios, where only watched entities are returned.
func monitoredEntitiesCPUCases() []monitoredEntitiesCase {
	return []monitoredEntitiesCase{
		{
			name: "CPU Count 3, watched = mix",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_NONE,
				},
			},
		},
		{
			name: "CPU Count 2, Core Count 4, CPU Watched = true, Core Watched = mix",
			mockFunc: func(t *testing.T) *mockdeviceinfo.MockProvider {
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
					ParentType:   dcgm.FE_CPU,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
					ParentType:   dcgm.FE_CPU,
				},
			},
		},
	}
}

// runMonitoredEntitiesCases runs each case as a subtest and compares GetMonitoredEntities with its expected entities.
func runMonitoredEntitiesCases(t *testing.T, tests []monitoredEntitiesCase) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deviceInfo := tt.mockFunc(t)

			got := GetMonitoredEntities(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

func TestGetMonitoredEntitiesSkipsDetachedGPU(t *testing.T) {
	activeGPU := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 7}
	activeInstance := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 17}
	activeComputeInstance := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 27}
	tests := []struct {
		name    string
		options appconfig.DeviceOptions
		read    func(deviceinfo.Provider) []Info
		want    []dcgm.GroupEntityPair
	}{
		{"flexible", appconfig.DeviceOptions{Flex: true}, GetMonitoredEntities, []dcgm.GroupEntityPair{activeInstance}},
		{"whole GPUs", appconfig.DeviceOptions{MajorRange: []int{-1}}, GetMonitoredEntities, []dcgm.GroupEntityPair{activeGPU}},
		{"explicit detached GPU", appconfig.DeviceOptions{MajorRange: []int{0}}, GetMonitoredEntities, nil},
		{"explicit instances", appconfig.DeviceOptions{MinorRange: []int{10, 17}}, GetMonitoredEntities, []dcgm.GroupEntityPair{activeInstance}},
		{
			"compute children",
			appconfig.DeviceOptions{Flex: true},
			GetMonitoredEntitiesIncludingComputeInstances,
			[]dcgm.GroupEntityPair{activeInstance, activeComputeInstance},
		},
		{
			"compute field scope",
			appconfig.DeviceOptions{Flex: true},
			GetMonitoredEntitiesForComputeInstanceFields,
			[]dcgm.GroupEntityPair{activeComputeInstance},
		},
		{"parent scope", appconfig.DeviceOptions{Flex: true}, GetParentGPUsForMonitoredGPUInstances, []dcgm.GroupEntityPair{activeGPU}},
		{"physical GPUs", appconfig.DeviceOptions{Flex: true}, GetPhysicalGPUs, []dcgm.GroupEntityPair{activeGPU}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpus := []deviceinfo.GPUInfo{
				{
					Status: dcgm.EntityStatusDetached, DeviceInfo: dcgm.Device{GPU: 0}, MigEnabled: true,
					GPUInstances: []deviceinfo.GPUInstanceInfo{{
						EntityId:         10,
						ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 20}},
					}},
				},
				{
					Status: dcgm.EntityStatusOk, DeviceInfo: dcgm.Device{GPU: 7}, MigEnabled: true,
					GPUInstances: []deviceinfo.GPUInstanceInfo{{
						EntityId:         17,
						ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 27}},
					}},
				},
			}

			info := mockdeviceinfo.NewMockProvider(gomock.NewController(t))
			info.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
			info.EXPECT().GOpts().Return(tt.options).AnyTimes()
			info.EXPECT().GPUCount().Return(uint(len(gpus))).AnyTimes()
			for index, gpu := range gpus {
				info.EXPECT().GPU(uint(index)).Return(gpu).AnyTimes()
			}

			var pairs []dcgm.GroupEntityPair
			for _, entity := range tt.read(info) {
				pairs = append(pairs, entity.Entity)
			}

			require.Equal(t, tt.want, pairs)
		})
	}
}

func TestOtherGPUStatusesKeepExistingSelection(t *testing.T) {
	for _, status := range []dcgm.EntityStatus{
		dcgm.EntityStatusUnknown, dcgm.EntityStatusOk, dcgm.EntityStatusUnsupported,
		dcgm.EntityStatusInaccessible, dcgm.EntityStatusLost, dcgm.EntityStatusFake, dcgm.EntityStatusDisabled,
	} {
		t.Run(status.String(), func(t *testing.T) {
			info := mockdeviceinfo.NewMockProvider(gomock.NewController(t))
			info.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			info.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
			info.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{Status: status, DeviceInfo: dcgm.Device{GPU: 3}}).AnyTimes()
			entities := GetMonitoredEntities(info)
			require.Len(t, entities, 1)
			require.Equal(t, uint(3), entities[0].Entity.EntityId)
		})
	}
}

func TestDetachedGPUNVLinksKeepSwitchLinks(t *testing.T) {
	info := mockdeviceinfo.NewMockProvider(gomock.NewController(t))
	info.EXPECT().InfoType().Return(dcgm.FE_LINK).AnyTimes()
	info.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
	info.EXPECT().GPU(uint(0)).Return(deviceinfo.GPUInfo{
		Status: dcgm.EntityStatusDetached, DeviceInfo: dcgm.Device{GPU: 0},
		NvLinks: []dcgm.NvLinkStatus{{Index: 3, ParentId: 0, ParentType: dcgm.FE_GPU, State: dcgm.LS_UP}},
	}).AnyTimes()
	info.EXPECT().Switches().Return([]deviceinfo.SwitchInfo{{
		EntityId: 13,
		NvLinks:  []dcgm.NvLinkStatus{{Index: 4, ParentId: 13, ParentType: dcgm.FE_SWITCH, State: dcgm.LS_UP}},
	}})
	info.EXPECT().IsSwitchWatched(uint(13)).Return(true)
	info.EXPECT().IsLinkWatched(uint(4), uint(13)).Return(true)
	entities := GetMonitoredEntities(info)
	require.Len(t, entities, 1)
	require.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: 4}, entities[0].Entity)
	require.Equal(t, dcgm.FE_SWITCH, entities[0].ParentType)
}

// TestGetMonitoredEntitiesIncludingComputeInstances checks that a GPU instance is returned followed by
// each of its compute instances, with the GPU instance as parent and their MIG IDs filled in.
func TestGetMonitoredEntitiesIncludingComputeInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	instance := deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{NvmlInstanceId: 3},
		ProfileName: "1g.10gb",
		EntityId:    7,
		ComputeInstances: []deviceinfo.ComputeInstanceInfo{
			{InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 0}, EntityId: 21},
			{InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 1}, EntityId: 22},
		},
	}
	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	got := GetMonitoredEntitiesIncludingComputeInstances(deviceInfo)

	require.Len(t, got, 3)
	assert.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7}, got[0].Entity)
	for index, want := range []struct {
		entityID uint
		nvmlID   uint
	}{
		{entityID: 21, nvmlID: 0},
		{entityID: 22, nvmlID: 1},
	} {
		computeInstance := got[index+1]
		assert.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: want.entityID}, computeInstance.Entity)
		assert.Equal(t, dcgm.FE_GPU_I, computeInstance.ParentType)
		assert.Equal(t, uint(7), computeInstance.ParentId)
		assert.Equal(t, uint(3), computeInstance.InstanceInfo.Info.NvmlInstanceId)
		assert.Equal(t, want.nvmlID, computeInstance.ComputeInstanceInfo.InstanceInfo.NvmlComputeInstanceId)
	}
}

// TestGetMonitoredEntitiesForComputeInstanceFieldsIncludesWholeGPUsAndComputeInstances checks that
// GPUs without MIG are returned as whole GPUs and a GPU split into instances is replaced by its compute instances.
func TestGetMonitoredEntitiesForComputeInstanceFieldsIncludesWholeGPUsAndComputeInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	instance := deviceinfo.GPUInstanceInfo{
		EntityId: 7,
		ComputeInstances: []deviceinfo.ComputeInstanceInfo{
			{EntityId: 21},
			{EntityId: 22},
		},
	}
	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	got := GetMonitoredEntitiesForComputeInstanceFields(deviceInfo)

	require.Len(t, got, 3)
	assert.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1}, got[0].Entity)
	assert.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21}, got[1].Entity)
	assert.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 22}, got[2].Entity)
}

// TestGetParentGPUsForMonitoredGPUInstances checks that the GPU holding a monitored GPU instance is
// returned as a whole GPU entity.
func TestGetParentGPUsForMonitoredGPUInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	instance := deviceinfo.GPUInstanceInfo{EntityId: 7}
	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	got := GetParentGPUsForMonitoredGPUInstances(deviceInfo)

	require.Len(t, got, 1)
	assert.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}, got[0].Entity)
}

// TestGetParentGPUsForMonitoredGPUInstancesSkipsSelectedGPUs checks that no parent GPUs are returned
// when the GPUs themselves are already selected for monitoring.
func TestGetParentGPUsForMonitoredGPUInstancesSkipsSelectedGPUs(t *testing.T) {
	ctrl := gomock.NewController(t)
	instance := deviceinfo.GPUInstanceInfo{EntityId: 7}
	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{
		MajorRange: []int{-1},
		MinorRange: []int{-1},
	}).AnyTimes()

	assert.Empty(t, GetParentGPUsForMonitoredGPUInstances(deviceInfo))
}

// TestGetPhysicalGPUs checks that physical GPUs are the selected whole GPUs
// plus the parent of each selected GPU instance, each GPU once, and that
// SelectsGPUInstances reports whether any GPU instance is selected. GPU 0 has
// GPU instances 7 and 8; GPU 1 is not in MIG mode.
func TestGetPhysicalGPUs(t *testing.T) {
	gpu := func(id uint) dcgm.GroupEntityPair {
		return dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: id}
	}
	for _, tt := range []struct {
		name          string
		options       appconfig.DeviceOptions
		want          []dcgm.GroupEntityPair
		wantInstances bool
	}{
		{"flex", appconfig.DeviceOptions{Flex: true}, []dcgm.GroupEntityPair{gpu(1), gpu(0)}, true},
		{"whole GPUs", appconfig.DeviceOptions{MajorRange: []int{-1}}, []dcgm.GroupEntityPair{gpu(0), gpu(1)}, false},
		{"one instance", appconfig.DeviceOptions{MinorRange: []int{8}}, []dcgm.GroupEntityPair{gpu(0)}, true},
		{"GPU and its instances", appconfig.DeviceOptions{MajorRange: []int{0}, MinorRange: []int{-1}}, []dcgm.GroupEntityPair{gpu(0)}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instances := []deviceinfo.GPUInstanceInfo{{EntityId: 7}, {EntityId: 8}}
			deviceInfo := testutils.MockGPUDeviceInfo(gomock.NewController(t), 2, map[int][]deviceinfo.GPUInstanceInfo{0: instances})
			deviceInfo.EXPECT().GOpts().Return(tt.options).AnyTimes()

			var got []dcgm.GroupEntityPair
			for _, entity := range GetPhysicalGPUs(deviceInfo) {
				got = append(got, entity.Entity)
			}

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantInstances, SelectsGPUInstances(deviceInfo))
		})
	}
}

// Test_monitorAllGPUs checks that every GPU becomes one GPU entity with its device info and no
// parent, even when it has GPU instances.
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

// Test_monitorAllGPUInstances checks that every GPU instance is returned with its GPU as parent type,
// and that GPUs without instances are added as whole GPUs only when adding flexibly.
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
					ParentType:   dcgm.FE_NONE,
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
					ParentType:   dcgm.FE_NONE,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
					ParentType:   dcgm.FE_NONE,
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
				},
				{
					Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(1)},
					DeviceInfo: dcgm.Device{
						GPU: uint(1),
					},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
					ParentType:   dcgm.FE_NONE,
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
					ParentType:   dcgm.FE_GPU,
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
					ParentType:   dcgm.FE_GPU,
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

// Test_monitorAllSwitches checks that only watched switches are returned, as switch entities with no parent.
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

// Test_monitorAllLinks checks that a link is returned, with its switch as parent, only when the switch and
// the link are watched and the link is up.
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
					ParentType:   dcgm.FE_SWITCH,
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
					ParentType:   dcgm.FE_SWITCH,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_LINK, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
					ParentType:   dcgm.FE_SWITCH,
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
					ParentType:   dcgm.FE_SWITCH,
				},
			},
		},
	}
	for _, tt := range tests {
		deviceInfo := tt.mockFunc()
		t.Run(tt.name, func(t *testing.T) {
			got := monitorAllNvSwitchNvLinks(deviceInfo)
			assert.Equalf(t, tt.want, got, "Unexpected Output")
		})
	}
}

// Test_monitorAllCPUs checks that only watched CPUs are returned, as CPU entities with no parent.
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
					ParentType:   dcgm.FE_NONE,
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
					ParentType:   dcgm.FE_NONE,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     PARENT_ID_IGNORED,
					ParentType:   dcgm.FE_NONE,
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
					ParentType:   dcgm.FE_NONE,
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

// Test_monitorAllCPUCores checks that a core is returned, with its CPU as parent, only when both the CPU
// and the core are watched.
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
					ParentType:   dcgm.FE_CPU,
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
					ParentType:   dcgm.FE_CPU,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     0,
					ParentType:   dcgm.FE_CPU,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(0)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
					ParentType:   dcgm.FE_CPU,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
					ParentType:   dcgm.FE_CPU,
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
					ParentType:   dcgm.FE_CPU,
				},
				{
					Entity:       dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_CPU_CORE, EntityId: uint(1)},
					DeviceInfo:   dcgm.Device{},
					InstanceInfo: nil,
					ParentId:     1,
					ParentType:   dcgm.FE_CPU,
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

// Test_monitorGPU checks that the GPU with the given ID is returned as a GPU entity, and nil when no
// GPU has that ID.
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

// Test_monitorGPUInstance checks that the GPU instance with the given entity ID is returned with its GPU's
// device info and GPU as parent type, and nil when none matches.
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
				ParentType:   dcgm.FE_GPU,
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
				ParentType:   dcgm.FE_GPU,
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
				ParentType:   dcgm.FE_GPU,
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
