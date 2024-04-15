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
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

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
)

func MockDeviceInfo(ctrl *gomock.Controller, gpuInstanceInfos [][]deviceinfo.GPUInstanceInfo) *mockdeviceinfo.
	MockProvider {
	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)

	var mockGPUs []deviceinfo.GPUInfo

	for i, gpuInstanceInfo := range gpuInstanceInfos {
		gpuInfo := deviceinfo.GPUInfo{}
		gpuInfo.DeviceInfo.GPU = uint(i)
		gpuInfo.GPUInstances = gpuInstanceInfo
		mockSystemInfo.EXPECT().GPU(uint(i)).Return(gpuInfo).AnyTimes()
	}

	mockSystemInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
	mockSystemInfo.EXPECT().GPUs().Return(mockGPUs).AnyTimes()
	mockSystemInfo.EXPECT().InfoType().Return(dcgm.FE_NONE).AnyTimes()

	return mockSystemInfo
}

func MockSwitchDeviceInfo(
	ctrl *gomock.Controller, infoType dcgm.Field_Entity_Group,
) *mockdeviceinfo.MockProvider {
	mockSwitches := []deviceinfo.SwitchInfo{
		{
			EntityId: 0,
			NvLinks: []dcgm.NvLinkStatus{
				{
					ParentId:   0,
					ParentType: dcgm.FE_SWITCH,
					State:      2,
					Index:      0,
				},
				{
					ParentId:   0,
					ParentType: dcgm.FE_SWITCH,
					State:      3,
					Index:      1,
				},
			},
		},
		{
			EntityId: 1,
			NvLinks: []dcgm.NvLinkStatus{
				{
					ParentId:   1,
					ParentType: dcgm.FE_SWITCH,
					State:      2,
					Index:      0,
				},
				{
					ParentId:   1,
					ParentType: dcgm.FE_SWITCH,
					State:      3,
					Index:      1,
				},
			},
		},
	}

	mockSystemInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockSystemInfo.EXPECT().InfoType().Return(infoType).AnyTimes()
	mockSystemInfo.EXPECT().IsSwitchWatched(gomock.Any()).Return(true).AnyTimes()
	mockSystemInfo.EXPECT().IsLinkWatched(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	mockSystemInfo.EXPECT().Switches().Return(mockSwitches).AnyTimes()

	return mockSystemInfo
}

func TestMonitoredEntities(t *testing.T) {
	gOpts := appconfig.DeviceOptions{
		Flex: true,
	}

	gpuInstanceInfos := make([][]deviceinfo.GPUInstanceInfo, 2)
	gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo1}
	gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{gpuInstanceInfo2}

	ctrl := gomock.NewController(t)
	mockDeviceInfo := MockDeviceInfo(ctrl, gpuInstanceInfos)
	mockDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

	monitoring := GetMonitoredEntities(mockDeviceInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored entities but found %d", len(monitoring)))
	instanceCount := 0
	gpuCount := 0
	for _, mi := range monitoring {
		if mi.Entity.EntityGroupId == dcgm.FE_GPU_I {
			instanceCount = instanceCount + 1
			require.NotEqual(t, mi.InstanceInfo, nil, "Expected InstanceInfo to be populated but it wasn't")
			require.Equal(t, mi.InstanceInfo.ProfileName, fakeProfileName,
				"Expected profile named '%s' but found '%s'",
				fakeProfileName, mi.InstanceInfo.ProfileName)
			if mi.Entity.EntityId != uint(0) {
				// One of these should be 0, the other should be 14
				require.Equal(t, mi.Entity.EntityId, uint(14), "Expected 14 as EntityId but found %s",
					monitoring[1].Entity.EntityId)
			}
		} else {
			gpuCount = gpuCount + 1
			require.Equal(t, mi.InstanceInfo, (*deviceinfo.GPUInstanceInfo)(nil),
				"Expected InstanceInfo to be nil but it wasn't")
		}
	}
	require.Equal(t, instanceCount, 2, "Expected 2 GPU instances but found %d", instanceCount)
	require.Equal(t, gpuCount, 0, "Expected 0 gpus but found %d", gpuCount)

	newGpuInstanceInfos := make([][]deviceinfo.GPUInstanceInfo, 2)
	gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{}
	gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{}

	mockDeviceInfo = MockDeviceInfo(ctrl, newGpuInstanceInfos)
	mockDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()
	monitoring = GetMonitoredEntities(mockDeviceInfo)
	require.Equal(t, 2, len(monitoring), fmt.Sprintf("Should have 2 monitored entities but found %d", len(monitoring)))
	for i, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_GPU, "Expected FE_GPU but found %d", mi.Entity.EntityGroupId)
		require.Equal(t, uint(i), mi.DeviceInfo.GPU, "Expected GPU %d but found %d", i, mi.DeviceInfo.GPU)
		require.Equal(t, (*deviceinfo.GPUInstanceInfo)(nil), mi.InstanceInfo,
			"Expected InstanceInfo not to be populated but it was")
	}
}

func TestMonitoredSwitches(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDeviceInfo := MockSwitchDeviceInfo(ctrl, dcgm.FE_SWITCH)

	/* test that only switches are returned */
	monitoring := GetMonitoredEntities(mockDeviceInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored switches but found %d", len(monitoring)))
	for _, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_SWITCH,
			fmt.Sprintf("Should have only returned switches but returned %d", mi.Entity.EntityGroupId))
	}

	/* test that only "up" links are monitored and 1 from each switch */
	mockDeviceInfo = MockSwitchDeviceInfo(ctrl, dcgm.FE_LINK)
	monitoring = GetMonitoredEntities(mockDeviceInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored links but found %d", len(monitoring)))
	for i, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_LINK,
			fmt.Sprintf("Should have only returned links but returned %d", mi.Entity.EntityGroupId))
		require.Equal(t, mi.ParentId, uint(i), "Link should reference switch parent")
	}
}
