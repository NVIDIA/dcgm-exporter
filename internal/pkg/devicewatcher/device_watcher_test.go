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

package devicewatcher

import (
	"crypto/rand"
	"fmt"
	"slices"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestDeviceWatcher_WatchDeviceFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	tests := []struct {
		name               string
		mockDeviceInfoFunc func() *mockdeviceinfo.MockProvider
		mockDCGMFunc       func([]dcgm.GroupHandle, dcgm.FieldHandle)
		expectGroupIDs     func() []dcgm.GroupHandle
		expectFieldGroupID func() dcgm.FieldHandle
		wantErr            bool
	}{
		{
			name: "Watch Switch Links",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(0), uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(1), uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[1], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch Switch Links when No Switches watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				watchedSwitches := map[uint]bool{0: false, 1: false}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, nil, watchedSwitches, nil,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return nil
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch Switch Links but got AddLinkEntityToGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(0), uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(1),
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "Watch Switch Links but got FieldGroupCreate Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(0), uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(1), uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle,
					fmt.Errorf("some error"))
			},
			wantErr: true,
		},
		{
			name: "Watch Switch Links but got WatchFieldsWithGroupEx Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(0), uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(1), uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[1], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(fmt.Errorf("some error"))
			},
			wantErr: true,
		},
		{
			name: "Watch GPUs",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch GPUs when No GPUs or GPU Instances to monitor",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 0, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return []dcgm.GroupHandle{}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(_ []dcgm.GroupHandle, _ dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch GPUs but got AddEntityToGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, _ dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU,
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
			},
			wantErr: true,
		},
		{
			name: "Watch CPU Cores",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[1], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[1], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[1], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "No CPU cores to watch",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				watchedCPUs := map[uint]bool{0: false, 1: false}
				mockGPUDeviceInfo := testutils.MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil,
					dcgm.FE_CPU_CORE)

				return mockGPUDeviceInfo
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return nil
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch CPU cores when Create Group Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], fmt.Errorf("random error"))
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "Watch CPUs",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch CPUs when CPUs to monitor",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)

				watchedCPUs := map[uint]bool{0: false, 1: false}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return []dcgm.GroupHandle{}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(_ []dcgm.GroupHandle, _ dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch CPUs but got AddEntityToGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)

				watchedCPUs := map[uint]bool{0: false, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, _ dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU,
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
			},
			wantErr: true,
		},
		{
			name: "Watch Switches",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_SWITCH)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupEx(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch CPUs when no switches available",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				return testutils.MockSwitchDeviceInfo(ctrl, 0, nil, nil, nil,
					dcgm.FE_SWITCH)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return []dcgm.GroupHandle{}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(_ []dcgm.GroupHandle, _ dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch CPUs when Create Group error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_SWITCH)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], fmt.Errorf("random error"))
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDeviceInfo := tt.mockDeviceInfoFunc()
			mockGroupIDs := tt.expectGroupIDs()
			mockFieldGroupIDs := tt.expectFieldGroupID()
			tt.mockDCGMFunc(mockGroupIDs, mockFieldGroupIDs)

			d := NewDeviceWatcher()
			inputFields := []dcgm.Short{1, 2, 3, 4}
			_, _, gotFuncs, err := d.WatchDeviceFields(inputFields, mockDeviceInfo, 1000000)
			// Ensure DestroyGroup functions gets called
			for _, gotFunc := range gotFuncs {
				gotFunc()
			}

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
			} else {
				assert.NotNil(t, err, "expected no error.")
				assert.Nil(t, gotFuncs, "expected cleanup functions to be nil")
			}
		})
	}
}

func TestDeviceWatcher_createGenericGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	tests := []struct {
		name               string
		mockDeviceInfoFunc func() *mockdeviceinfo.MockProvider
		mockDCGMFunc       func(dcgm.GroupHandle) func()
		expectGroupID      func() *dcgm.GroupHandle
		wantErr            bool
	}{
		{
			name: "Create Group for GPUs",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupID: func() *dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return &mockGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandle dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Create Group for GPU Instances",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[0] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupID: func() *dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return &mockGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandle dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU_I,
					testutils.MockGPUInstanceInfo1.EntityId).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU_I,
					testutils.MockGPUInstanceInfo2.EntityId).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Create Group for CPUs",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU)
			},
			expectGroupID: func() *dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return &mockGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandle dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_CPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_CPU, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Create Group for Switches",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_SWITCH)
			},
			expectGroupID: func() *dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return &mockGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandle dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "No GPUs or GPU Instances to monitor",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 0, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupID: func() *dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(_ dcgm.GroupHandle) func() {
				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Random Unit Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupID: func() *dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(_ dcgm.GroupHandle) func() {
				// Simulate a failure in rand.Reader using mock rand.Reader
				mockReader := &testutils.MockReader{Err: fmt.Errorf("mock error")}

				originalReader := rand.Reader
				rand.Reader = mockReader
				return func() {
					rand.Reader = originalReader
				}
			},
			wantErr: true,
		},
		{
			name: "Create Group Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupID: func() *dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(_ dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(dcgm.GroupHandle{}, fmt.Errorf("random error"))

				return doNothing
			},
			wantErr: true,
		},
		{
			name: "AddEntityToGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupID: func() *dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return &mockGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandle dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU,
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(fmt.Errorf("some other error"))

				return doNothing
			},
			wantErr: true,
		},
		{
			name: "DestroyGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				gOpts := appconfig.DeviceOptions{
					Flex: true,
				}

				mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
				mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

				return mockGPUDeviceInfo
			},
			expectGroupID: func() *dcgm.GroupHandle {
				mockGroupHandle := dcgm.GroupHandle{}
				mockGroupHandle.SetHandle(uintptr(1))

				return &mockGroupHandle
			},
			mockDCGMFunc: func(mockGroupHandle dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(fmt.Errorf("some error"))

				return doNothing
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDeviceInfo := tt.mockDeviceInfoFunc()
			mockGroupID := tt.expectGroupID()
			inputGroupID := dcgm.GroupHandle{}
			if mockGroupID != nil {
				inputGroupID = *mockGroupID
			}

			f := tt.mockDCGMFunc(inputGroupID)
			defer f()

			d := &DeviceWatcher{}
			gotGroupID, gotFunc, err := d.createGenericGroup(mockDeviceInfo)
			gotFunc() // Ensure DestroyGroup function gets called

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockGroupID, gotGroupID, "expected group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected no error.")
			}
		})
	}
}

func TestDeviceWatcher_createCPUCoreGroups(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	tests := []struct {
		name               string
		mockDeviceInfoFunc func() *mockdeviceinfo.MockProvider
		mockDCGMFunc       func(mockGroupHandles []dcgm.GroupHandle) func()
		expectGroupIDs     func() []dcgm.GroupHandle
		wantErr            bool
	}{
		{
			name: "Create Group for CPU Cores",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[1], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[1], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "No CPU watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				watchedCPUs := map[uint]bool{0: false, 1: false}
				mockGPUDeviceInfo := testutils.MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil,
					dcgm.FE_CPU_CORE)

				return mockGPUDeviceInfo
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Only CPUs watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				watchedCPUs := map[uint]bool{0: true, 1: true}
				return testutils.MockCPUDeviceInfo(ctrl, 2, nil, watchedCPUs, nil,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Only 1 Core watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: false,
					{ParentID: 1, ChildID: 1}: false,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "One Core Each watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: false,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[1], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Random Unit Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(_ []dcgm.GroupHandle) func() {
				// Simulate a failure in rand.Reader using mock rand.Reader
				mockReader := &testutils.MockReader{Err: fmt.Errorf("mock error")}

				originalReader := rand.Reader
				rand.Reader = mockReader
				return func() {
					rand.Reader = originalReader
				}
			},
			wantErr: true,
		},
		{
			name: "Create Group Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], fmt.Errorf("random error"))
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				return doNothing
			},
			wantErr: true,
		},
		{
			name: "AddEntityToGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{0, 1}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[1], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[1], dcgm.FE_CPU_CORE,
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)
				return doNothing
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDeviceInfo := tt.mockDeviceInfoFunc()
			mockGroupIDs := tt.expectGroupIDs()
			f := tt.mockDCGMFunc(mockGroupIDs)
			defer f()

			d := &DeviceWatcher{}
			gotGroupIDs, gotFuncs, err := d.createCPUCoreGroups(mockDeviceInfo)
			// Ensure DestroyGroup functions gets called
			for _, gotFunc := range gotFuncs {
				gotFunc()
			}

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockGroupIDs, gotGroupIDs, "expected group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected no error.")
			}
		})
	}
}

func TestDeviceWatcher_createNVLinkGroups(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	tests := []struct {
		name               string
		mockDeviceInfoFunc func() *mockdeviceinfo.MockProvider
		mockDCGMFunc       func(mockGroupHandles []dcgm.GroupHandle) func()
		expectGroupIDs     func() []dcgm.GroupHandle
		wantErr            bool
	}{
		{
			name: "Create Group for Switch Links",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(0), uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(1), uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "No Switches watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				watchedSwitches := map[uint]bool{0: false, 1: false}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, nil, watchedSwitches, nil,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Only Switches watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				watchedSwitches := map[uint]bool{0: true, 1: true}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, nil, watchedSwitches, nil,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Only 1 NV Link watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: false,
					{ParentID: 1, ChildID: 1}: false,
				}

				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "One NV Link Each watched",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: false,
				}

				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(0), uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "One NV Link Each watched but one link down",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: false,
				}

				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "One NV Link Each watched but all watched NV links down",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				mockLink2 := testutils.MockNVLinkVal2
				mockLink2.State = 2

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, mockLink2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{testutils.MockNVLinkVal1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: false,
				}

				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return nil
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Random Unit Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(_ []dcgm.GroupHandle) func() {
				// Simulate a failure in rand.Reader using mock rand.Reader
				mockReader := &testutils.MockReader{Err: fmt.Errorf("mock error")}

				originalReader := rand.Reader
				rand.Reader = mockReader
				return func() {
					rand.Reader = originalReader
				}
			},
			wantErr: true,
		},
		{
			name: "Create Group Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], fmt.Errorf("random error"))
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				return doNothing
			},
			wantErr: true,
		},
		{
			name: "AddLinkEntityToGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				mockLink1 := testutils.MockNVLinkVal1
				mockLink1.State = 3

				switchToNvLinks := make(map[int][]dcgm.NvLinkStatus)
				switchToNvLinks[0] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}
				switchToNvLinks[1] = []dcgm.NvLinkStatus{mockLink1, testutils.MockNVLinkVal2}

				watchedSwitches := map[uint]bool{0: true, 1: true}
				watchedLinks := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 0}: true,
					{ParentID: 1, ChildID: 1}: true,
				}
				return testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				mockGroupHandle2 := dcgm.GroupHandle{}
				mockGroupHandle2.SetHandle(uintptr(2))

				return []dcgm.GroupHandle{mockGroupHandle1, mockGroupHandle2}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[1], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(0), uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[1], uint(1),
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[1]).Return(nil)
				return doNothing
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDeviceInfo := tt.mockDeviceInfoFunc()
			mockGroupIDs := tt.expectGroupIDs()
			f := tt.mockDCGMFunc(mockGroupIDs)
			defer f()

			d := &DeviceWatcher{}
			gotGroupIDs, gotFuncs, err := d.createNVLinkGroups(mockDeviceInfo)
			// Ensure DestroyGroup functions gets called
			for _, gotFunc := range gotFuncs {
				gotFunc()
			}

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockGroupIDs, gotGroupIDs, "expected group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected no error.")
			}
		})
	}
}

func Test_newFieldGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	tests := []struct {
		name               string
		mockDCGMFunc       func(dcgm.FieldHandle) func()
		expectFieldGroupID func() dcgm.FieldHandle
		wantErr            bool
	}{
		{
			name: "Create Group for Switch Links",
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockFieldGroupHandle dcgm.FieldHandle) func() {
				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Random Unit Error",
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockFieldGroupHandle dcgm.FieldHandle) func() {
				// Simulate a failure in rand.Reader using mock rand.Reader
				mockReader := &testutils.MockReader{Err: fmt.Errorf("mock error")}

				originalReader := rand.Reader
				rand.Reader = mockReader
				return func() {
					rand.Reader = originalReader
				}
			},
			wantErr: true,
		},
		{
			name: "Field Group Create Error",
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockFieldGroupHandle dcgm.FieldHandle) func() {
				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle,
					fmt.Errorf("random error"))

				return doNothing
			},
			wantErr: true,
		},
		{
			name: "Field Group Destroy Error",
			expectFieldGroupID: func() dcgm.FieldHandle {
				mockFieldGroupHandle := dcgm.FieldHandle{}
				mockFieldGroupHandle.SetHandle(uintptr(1))

				return mockFieldGroupHandle
			},
			mockDCGMFunc: func(mockFieldGroupHandle dcgm.FieldHandle) func() {
				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(fmt.Errorf("some other error"))

				return doNothing
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFieldGroupIDs := tt.expectFieldGroupID()
			f := tt.mockDCGMFunc(mockFieldGroupIDs)
			defer f()

			input := []dcgm.Short{1, 2, 3, 4}
			gotFieldGroupIDs, gotFunc, err := newFieldGroup(input)
			gotFunc() // Ensure DestroyGroup functions gets called

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockFieldGroupIDs, gotFieldGroupIDs, "expected field group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected no error.")
			}
		})
	}
}

func TestDeviceWatcher_GetDeviceFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	type args struct {
		counterList []counters.Counter
		entityType  dcgm.Field_Entity_Group
	}
	tests := []struct {
		name         string
		args         args
		mockDCGMFunc func([]dcgm.Short)
		want         func() []dcgm.Short
	}{
		{
			name: "GPU, GPU Instance and VGPU Counters",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_GPU,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID])
				}
			},
			want: func() []dcgm.Short {
				return append(testutils.SampleGPUFieldIDs, testutils.SampleDriverVersionCounter.FieldID)
			},
		},
		{
			name: "GPU Instance Counters",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_GPU_I,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID])
				}
			},
			want: func() []dcgm.Short {
				return []dcgm.Short{
					testutils.SampleGPUPowerUsageCounter.FieldID,
					testutils.SampleDriverVersionCounter.FieldID,
				}
			},
		},
		{
			name: "VGPU Counters",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_VGPU,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID])
				}
			},
			want: func() []dcgm.Short {
				return []dcgm.Short{
					testutils.SampleVGPULicenseStatusCounter.FieldID,
					testutils.SampleDriverVersionCounter.FieldID,
				}
			},
		},
		{
			name: "CPU and CPU Core Counters",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_CPU,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID])
				}
			},
			want: func() []dcgm.Short {
				return []dcgm.Short{
					testutils.SampleCPUUtilTotalCounter.FieldID,
					testutils.SampleDriverVersionCounter.FieldID,
				}
			},
		},
		{
			name: "Switch and NV Link Counters",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_SWITCH,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID])
				}
			},
			want: func() []dcgm.Short {
				return []dcgm.Short{
					testutils.SampleSwitchCurrentTempCounter.FieldID,
					testutils.SampleSwitchLinkFlitErrorsCounter.FieldID,
					testutils.SampleDriverVersionCounter.FieldID,
				}
			},
		},
		{
			name: "NV Link Counters",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_LINK,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID])
				}
			},
			want: func() []dcgm.Short {
				return []dcgm.Short{
					testutils.SampleSwitchLinkFlitErrorsCounter.FieldID,
					testutils.SampleDriverVersionCounter.FieldID,
				}
			},
		},
		{
			name: "Invalid Entity Type",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_COUNT,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID])
				}
			},
			want: func() []dcgm.Short {
				return []dcgm.Short{
					testutils.SampleDriverVersionCounter.FieldID,
				}
			},
		},
		{
			name: "No Counters",
			args: args{
				counterList: []counters.Counter{},
				entityType:  dcgm.FE_GPU,
			},
			mockDCGMFunc: func(_ []dcgm.Short) {},
			want: func() []dcgm.Short {
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mockDCGMFunc(testutils.SampleAllFieldIDs)

			d := &DeviceWatcher{}
			want := tt.want()
			got := d.GetDeviceFields(tt.args.counterList, tt.args.entityType)

			slices.Sort(want)
			slices.Sort(got)
			assert.Equal(t, want, got, "Device fields mismatch")
		})
	}
}
