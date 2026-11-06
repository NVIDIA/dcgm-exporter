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
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

const (
	computeInstanceTestWatchIntervalMSec = 1000
	computeInstanceTestWatchIntervalUsec = int64(computeInstanceTestWatchIntervalMSec * 1000)
	computeInstanceTestMaxKeepAge        = 600.0
)

func TestDetachedGPUDoesNotCreateNVLinkWatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mockdcgm.NewMockDCGM(ctrl)
	info := mockdeviceinfo.NewMockProvider(ctrl)
	info.EXPECT().GPUs().Return([]deviceinfo.GPUInfo{{
		Status: dcgm.EntityStatusDetached, DeviceInfo: dcgm.Device{GPU: 0},
		NvLinks: []dcgm.NvLinkStatus{{Index: 3, ParentId: 0, ParentType: dcgm.FE_GPU, State: dcgm.LS_UP}},
	}})
	info.EXPECT().Switches().Return(nil)
	groups, err := NewDeviceWatcherWithClient(client).createNVLinkGroups(info)
	require.NoError(t, err)
	require.Empty(t, groups)
}

// watchDeviceFieldsCase describes one entity topology and its expected DCGM watch calls.
type watchDeviceFieldsCase struct {
	name string
	// mockDeviceInfoFunc builds this case's mocked entity topology.
	mockDeviceInfoFunc func(*gomock.Controller) *mockdeviceinfo.MockProvider
	// mockDCGMFunc declares the watch setup and cleanup calls expected for this case.
	mockDCGMFunc func(*mockdcgm.MockDCGM, []dcgm.GroupHandle, dcgm.FieldHandle)
	// expectGroupIDs supplies the group handles returned by the mocked DCGM calls.
	expectGroupIDs func() []dcgm.GroupHandle
	// expectFieldGroupID supplies the field-group handle returned by the mocked DCGM call.
	expectFieldGroupID func() dcgm.FieldHandle
	// wantErr reports whether WatchDeviceFields should reject this case.
	wantErr bool
}

// TestDeviceWatcher_WatchDeviceFields verifies watch setup for each supported entity type.
func TestDeviceWatcher_WatchDeviceFields(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	t.Run("switch links", func(t *testing.T) {
		runWatchDeviceFieldsCases(t, watchDeviceFieldsSwitchLinkCases())
	})
	t.Run("GPU entities", func(t *testing.T) {
		runWatchDeviceFieldsCases(t, watchDeviceFieldsGPUCases())
	})
	t.Run("CPU cores", func(t *testing.T) {
		runWatchDeviceFieldsCases(t, watchDeviceFieldsCPUCoreCases())
	})
	t.Run("CPU entities", func(t *testing.T) {
		runWatchDeviceFieldsCases(t, watchDeviceFieldsCPUCases())
	})
	t.Run("switch entities", func(t *testing.T) {
		runWatchDeviceFieldsCases(t, watchDeviceFieldsSwitchCases())
	})
}

// watchDeviceFieldsSwitchLinkCases defines the switch links scenarios for WatchDeviceFields.
func watchDeviceFieldsSwitchLinkCases() []watchDeviceFieldsCase {
	tests := []watchDeviceFieldsCase{
		{
			name: "Watch Switch Links",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
				mockDeviceInfo := testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
				mockDeviceInfo.EXPECT().SOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

				return mockDeviceInfo
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				// The links of both switches share one group and one watch.
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(1)).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					600.0, int32(0)).Return(nil)

				mockDCGM.EXPECT().UnwatchFields(mockFieldGroupHandle, mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch Switch Links when No Switches watched",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
			},
			wantErr: false,
		},
		{
			name: "Watch Switch Links but got AddLinkEntityToGroup Error",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
				mockDeviceInfo := testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
				mockDeviceInfo.EXPECT().SOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

				return mockDeviceInfo
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				// A refused link is skipped; the shared group is still watched.
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH,
					uint(1)).Return(fmt.Errorf("some error"))

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)

				mockDCGM.EXPECT().UnwatchFields(mockFieldGroupHandle, mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch Switch Links but got FieldGroupCreate Error",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle,
					fmt.Errorf("some error"))
			},
			wantErr: true,
		},
		{
			name: "Watch Switch Links but got WatchFieldsWithGroupExNoRefresh Error",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
				mockDeviceInfo := testutils.MockSwitchDeviceInfo(ctrl, 5, switchToNvLinks, watchedSwitches, watchedLinks,
					dcgm.FE_LINK)
				mockDeviceInfo.EXPECT().SOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

				return mockDeviceInfo
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)

				// The only watch fails, so no installed watch is left to unwatch.
				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{}, fmt.Errorf("unknown field")).AnyTimes()
			},
			wantErr: true,
		},
	}
	return tests
}

// watchDeviceFieldsGPUCases defines the GPU entities scenarios for WatchDeviceFields.
func watchDeviceFieldsGPUCases() []watchDeviceFieldsCase {
	tests := []watchDeviceFieldsCase{
		{
			name: "Watch GPUs",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU, uint(1)).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)

				mockDCGM.EXPECT().UnwatchFields(mockFieldGroupHandle, mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch GPUs when No GPUs or GPU Instances to monitor",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, _ []dcgm.GroupHandle, _ dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch GPUs but got AddEntityToGroup Error",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, _ dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_GPU,
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
			},
			wantErr: true,
		},
	}
	return tests
}

// watchDeviceFieldsCPUCoreCases defines the CPU cores scenarios for WatchDeviceFields.
func watchDeviceFieldsCPUCoreCases() []watchDeviceFieldsCase {
	tests := []watchDeviceFieldsCase{
		{
			name: "Watch CPU Cores",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: true,
					{ParentID: 1, ChildID: 3}: true,
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				// The cores of both CPUs share one group and one watch.
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(2)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(3)).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)

				mockDCGM.EXPECT().UnwatchFields(mockFieldGroupHandle, mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "No CPU cores to watch",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
			},
			wantErr: false,
		},
		{
			name: "Watch CPU cores when Create Group Error",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: true,
					{ParentID: 1, ChildID: 3}: true,
				}

				return testutils.MockCPUDeviceInfo(ctrl, 2, cpuToCores, watchedCPUs, watchedCores,
					dcgm.FE_CPU_CORE)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				mockGroupHandle1 := dcgm.GroupHandle{}
				mockGroupHandle1.SetHandle(uintptr(1))

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], fmt.Errorf("random error"))
			},
			wantErr: true,
		},
	}
	return tests
}

// watchDeviceFieldsCPUCases defines the CPU entities scenarios for WatchDeviceFields.
func watchDeviceFieldsCPUCases() []watchDeviceFieldsCase {
	tests := []watchDeviceFieldsCase{
		{
			name: "Watch CPUs",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU, uint(1)).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)

				mockDCGM.EXPECT().UnwatchFields(mockFieldGroupHandle, mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch CPUs when CPUs to monitor",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, _ []dcgm.GroupHandle, _ dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch CPUs but got AddEntityToGroup Error",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, _ dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU,
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
			},
			wantErr: true,
		},
	}
	return tests
}

// watchDeviceFieldsSwitchCases defines the switch entities scenarios for WatchDeviceFields.
func watchDeviceFieldsSwitchCases() []watchDeviceFieldsCase {
	tests := []watchDeviceFieldsCase{
		{
			name: "Watch Switches",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_SWITCH, uint(1)).Return(nil)

				mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).Return(mockFieldGroupHandle, nil)

				mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandles[0], gomock.Any(),
					gomock.Any(), gomock.Any()).Return(nil)

				mockDCGM.EXPECT().UnwatchFields(mockFieldGroupHandle, mockGroupHandles[0]).Return(nil)
				mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Watch Switches when no switches are available",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
				return testutils.MockSwitchDeviceInfo(ctrl, 0, nil, nil, nil,
					dcgm.FE_SWITCH)
			},
			expectGroupIDs: func() []dcgm.GroupHandle {
				return []dcgm.GroupHandle{}
			},
			expectFieldGroupID: func() dcgm.FieldHandle {
				return dcgm.FieldHandle{}
			},
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, _ []dcgm.GroupHandle, _ dcgm.FieldHandle) {},
			wantErr:      false,
		},
		{
			name: "Watch Switches when Create Group error",
			mockDeviceInfoFunc: func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
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
			mockDCGMFunc: func(mockDCGM *mockdcgm.MockDCGM, mockGroupHandles []dcgm.GroupHandle, mockFieldGroupHandle dcgm.FieldHandle) {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], fmt.Errorf("random error"))
			},
			wantErr: true,
		},
	}
	return tests
}

// runWatchDeviceFieldsCases gives each case an isolated mock client and checks its watch cleanup.
func runWatchDeviceFieldsCases(t *testing.T, tests []watchDeviceFieldsCase) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			dcgmprovider.SetClient(mockDCGM)

			mockDeviceInfo := tt.mockDeviceInfoFunc(ctrl)
			mockGroupIDs := tt.expectGroupIDs()
			mockFieldGroupIDs := tt.expectFieldGroupID()
			tt.mockDCGMFunc(mockDCGM, mockGroupIDs, mockFieldGroupIDs)

			d := NewDeviceWatcher()
			inputFields := []dcgm.Short{1, 2, 3, 4}
			_, _, gotFuncs, err := d.WatchDeviceFields(inputFields, mockDeviceInfo, 1000000)
			// Release groups from successful watches before checking the case result.
			for _, gotFunc := range gotFuncs {
				gotFunc()
			}

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
			} else {
				assert.NotNil(t, err, "expected an error.")
				assert.Nil(t, gotFuncs, "expected cleanup functions to be nil")
			}

			ctrl.Finish()
		})
	}
}

// TestDeviceWatcher_WatchDeviceFieldsPreservesMicrosecondInterval checks that an interval given in
// microseconds is passed to DCGM rounded down to whole milliseconds, and that the cleanup unwatches
// and destroys the groups.
func TestDeviceWatcher_WatchDeviceFieldsPreservesMicrosecondInterval(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(uintptr(2))
	fields := []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}
	const updateFreqInUsec = int64(1_234_567)

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), fields).Return(fieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(fieldGroup, groupHandle, int64(1_234_000), maxKeepAge, int32(maxKeepSamples)).
		Return(nil)
	mockDCGM.EXPECT().UnwatchFields(fieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(groupHandle).Return(nil)

	_, gotFieldGroup, cleanups, err := NewDeviceWatcher().WatchDeviceFields(
		fields,
		deviceInfo,
		updateFreqInUsec,
	)

	require.NoError(t, err)
	assert.Equal(t, fieldGroup, gotFieldGroup)
	require.Len(t, cleanups, 1)
	cleanups[0]()
}

// TestDeviceWatcher_WatchDeviceFieldGroupsUsesGroupIntervals checks that each field group is watched with
// its own interval, keep age and sample limit on one shared entity group.
func TestDeviceWatcher_WatchDeviceFieldGroupsUsesGroupIntervals(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	fastFieldGroup := dcgm.FieldHandle{}
	fastFieldGroup.SetHandle(uintptr(2))
	slowFieldGroup := dcgm.FieldHandle{}
	slowFieldGroup.SetHandle(uintptr(3))

	fastFields := []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}
	slowFields := []dcgm.Short{dcgm.DCGM_FI_DEV_POWER_USAGE}

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), fastFields).Return(fastFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(fastFieldGroup, groupHandle, int64(5_000_000), 0.0, int32(2)).
		Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), slowFields).Return(slowFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(slowFieldGroup, groupHandle, int64(60_000_000), 300.0, int32(10)).
		Return(nil)
	mockDCGM.EXPECT().UnwatchFields(fastFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().UnwatchFields(slowFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(fastFieldGroup).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(slowFieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(groupHandle).Return(nil)

	gotGroups, gotFieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{
			{Name: "fast", Fields: fastFields, IntervalMSec: 5000, MaxKeepAge: 0, MaxKeepSamples: 2},
			{Name: "slow", Fields: slowFields, IntervalMSec: 60000, MaxKeepAge: 300, MaxKeepSamples: 10},
		},
		deviceInfo,
	)

	require.NoError(t, err)
	assert.Equal(t, []dcgm.GroupHandle{groupHandle}, gotGroups)
	assert.Equal(t, []dcgm.FieldHandle{fastFieldGroup, slowFieldGroup}, gotFieldGroups)
	require.Len(t, cleanups, 1)
	cleanups[0]()
}

// TestEntitiesForScope checks that selected GPUs, instances, and missing parents stay in separate scopes.
func TestEntitiesForScope(t *testing.T) {
	tests := []struct {
		name    string
		scope   WatchScope
		want    []dcgm.GroupEntityPair
		wantErr bool
	}{
		{name: "selected physical GPU", scope: WatchSelectedGPUs, want: []dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU, EntityId: 1}}},
		{name: "selected GPU instance", scope: WatchGPUInstances, want: []dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7}}},
		{name: "selected compute instance", scope: WatchOnlyComputeInstances, want: []dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21}}},
		{name: "missing parent", scope: WatchUnselectedParentGPUs, want: []dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU, EntityId: 0}}},
		{name: "invalid scope", scope: WatchScope(99), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			instance := deviceinfo.GPUInstanceInfo{EntityId: 7, ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 21}}}
			info := testutils.MockGPUDeviceInfo(ctrl, 2, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			entities, err := EntitiesForScope(info, tt.scope)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got := make([]dcgm.GroupEntityPair, 0, len(entities))
			for _, entity := range entities {
				got = append(got, entity.Entity)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestDeviceWatcher_WatchesComputeInstanceFieldsOnWholeGPUsAndComputeInstances checks that compute
// instance fields are watched on GPUs without MIG and on each compute instance, not on GPUs split into instances.
func TestDeviceWatcher_WatchesComputeInstanceFieldsOnWholeGPUsAndComputeInstances(t *testing.T) {
	groupName := t.Name()
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	instance := deviceinfo.GPUInstanceInfo{
		EntityId: 7,
		ComputeInstances: []deviceinfo.ComputeInstanceInfo{
			{EntityId: 21},
			{EntityId: 22},
		},
	}
	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	group := dcgm.GroupHandle{}
	group.SetHandle(uintptr(1))
	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(uintptr(2))
	fields := []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED}
	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(group, nil)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(1)).Return(nil)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU_CI, uint(21)).Return(nil)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU_CI, uint(22)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), fields).Return(fieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(
			fieldGroup,
			group,
			computeInstanceTestWatchIntervalUsec,
			computeInstanceTestMaxKeepAge,
			int32(0),
		).
		Return(nil)
	mockDCGM.EXPECT().UnwatchFields(fieldGroup, group).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(group).Return(nil)

	_, _, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroupsForComputeInstanceFields(
		[]FieldWatchGroup{{
			Name:         groupName,
			Fields:       fields,
			IntervalMSec: computeInstanceTestWatchIntervalMSec,
			MaxKeepAge:   computeInstanceTestMaxKeepAge,
		}},
		deviceInfo,
	)

	require.NoError(t, err)
	require.Len(t, cleanups, 1)
	cleanups[0]()
}

// TestDeviceWatcher_WatchesFieldsOnMissingParentGPUs checks that fields at both scopes are watched on
// the parent GPU of a monitored GPU instance.
func TestDeviceWatcher_WatchesFieldsOnMissingParentGPUs(t *testing.T) {
	groupName := t.Name()
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	instance := deviceinfo.GPUInstanceInfo{EntityId: 7}
	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	group := dcgm.GroupHandle{}
	group.SetHandle(uintptr(1))
	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(uintptr(2))
	fields := []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}
	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(group, nil)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), fields).Return(fieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(
			fieldGroup,
			group,
			computeInstanceTestWatchIntervalUsec,
			computeInstanceTestMaxKeepAge,
			int32(0),
		).
		Return(nil)
	mockDCGM.EXPECT().UnwatchFields(fieldGroup, group).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(group).Return(nil)

	_, _, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroupsForParentGPUs(
		[]FieldWatchGroup{{
			Name:         groupName,
			Fields:       fields,
			IntervalMSec: computeInstanceTestWatchIntervalMSec,
			MaxKeepAge:   computeInstanceTestMaxKeepAge,
		}},
		deviceInfo,
	)

	require.NoError(t, err)
	require.Len(t, cleanups, 1)
	cleanups[0]()
}

// TestDeviceWatcher_GetDeviceFieldsClassifiesComputeInstanceFields checks that compute instance fields
// stay in the GPU field list and are also reported as compute instance fields, that a field DCGM
// exposes at both scopes, XID errors, is also reported in the both-scopes list, and that a field DCGM
// declares at GPU level is reported as a GPU-level field.
func TestDeviceWatcher_GetDeviceFieldsClassifiesComputeInstanceFields(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	mockDCGM.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_GPU_TEMP).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU}, nil)
	mockDCGM.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_FB_USED).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU_CI}, nil)
	mockDCGM.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_XID_ERRORS).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU_CI}, nil)

	got := NewDeviceWatcher().GetDeviceFields([]counters.Counter{
		{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP},
		{FieldID: dcgm.DCGM_FI_DEV_FB_USED},
		{FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS},
	}, dcgm.FE_GPU)

	assert.Equal(t, []dcgm.Short{
		dcgm.DCGM_FI_DEV_GPU_TEMP,
		dcgm.DCGM_FI_DEV_FB_USED,
		dcgm.DCGM_FI_DEV_XID_ERRORS,
	}, got.Fields)
	assert.Equal(t, []dcgm.Short{
		dcgm.DCGM_FI_DEV_FB_USED,
		dcgm.DCGM_FI_DEV_XID_ERRORS,
	}, got.ComputeInstanceFields)
	assert.Equal(t, []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}, got.FieldsAtMultipleScopes)
	assert.Equal(t, []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}, got.GPULevelFields)
}

// TestRetentionWithCompatibilityDefault guards legacy zero-value callers while preserving configured values.
func TestRetentionWithCompatibilityDefault(t *testing.T) {
	tests := []struct {
		name            string
		keepAge         float64
		keepSamples     int32
		wantKeepAge     float64
		wantKeepSamples int32
	}{
		{
			name:            "zero value uses legacy retention",
			wantKeepAge:     maxKeepAge,
			wantKeepSamples: maxKeepSamples,
		},
		{
			name:            "configured retention passes through",
			keepAge:         30,
			keepSamples:     2,
			wantKeepAge:     30,
			wantKeepSamples: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKeepAge, gotKeepSamples := retentionWithCompatibilityDefault(tt.keepAge, tt.keepSamples)
			assert.Equal(t, tt.wantKeepAge, gotKeepAge)
			assert.Equal(t, tt.wantKeepSamples, gotKeepSamples)
		})
	}
}

// TestDeviceWatcher_WatchDeviceFieldGroupsCleansUpStartedWatchOnLaterFailure checks that when a later
// field group cannot be created, the watch already started is undone and nothing is returned but the error.
func TestDeviceWatcher_WatchDeviceFieldGroupsCleansUpStartedWatchOnLaterFailure(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	fastFieldGroup := dcgm.FieldHandle{}
	fastFieldGroup.SetHandle(uintptr(2))

	fastFields := []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}
	slowFields := []dcgm.Short{dcgm.DCGM_FI_DEV_POWER_USAGE}

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), fastFields).Return(fastFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(fastFieldGroup, groupHandle, int64(5_000_000), gomock.Any(), gomock.Any()).
		Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), slowFields).Return(dcgm.FieldHandle{}, fmt.Errorf("boom"))
	mockDCGM.EXPECT().UnwatchFields(fastFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(fastFieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(groupHandle).Return(nil)

	gotGroups, gotFieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{
			{Name: "fast", Fields: fastFields, IntervalMSec: 5000},
			{Name: "slow", Fields: slowFields, IntervalMSec: 60000},
		},
		deviceInfo,
	)

	require.Error(t, err)
	assert.Nil(t, gotGroups)
	assert.Nil(t, gotFieldGroups)
	assert.Nil(t, cleanups)
}

// TestDeviceWatcher_WatchDeviceFieldsLogsFieldDetailsOnWatchFailure checks that a failed watch logs
// the unique field IDs and names, the groups, the device option mode and the error, and returns no cleanups.
func TestDeviceWatcher_WatchDeviceFieldsLogsFieldDetailsOnWatchFailure(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previousLogger)

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	mockDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{MajorRange: []int{-1}}).AnyTimes()

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(42))
	mockFieldGroupHandle := dcgm.FieldHandle{}
	mockFieldGroupHandle.SetHandle(uintptr(43))
	fieldIDs := []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DRIVER_VERSION}

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Eq(fieldIDs)).Return(mockFieldGroupHandle, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupExNoRefresh(mockFieldGroupHandle, mockGroupHandle, gomock.Any(), gomock.Any(), gomock.Any()).
		Return(fmt.Errorf("watch failed"))
	mockDCGM.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_GPU_TEMP).
		Return(dcgm.FieldMeta{Tag: "DCGM_FI_DEV_GPU_TEMP"}, nil).AnyTimes()
	mockDCGM.EXPECT().FieldGetByID(dcgm.DCGM_FI_DRIVER_VERSION).
		Return(dcgm.FieldMeta{Tag: "DCGM_FI_DRIVER_VERSION"}, nil).AnyTimes()
	mockDCGM.EXPECT().FieldGroupDestroy(mockFieldGroupHandle).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(mockGroupHandle).Return(nil)

	d := NewDeviceWatcher()
	_, _, cleanups, err := d.WatchDeviceFields(
		[]dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DRIVER_VERSION, dcgm.DCGM_FI_DEV_GPU_TEMP},
		mockDeviceInfo,
		1000000,
	)

	assert.Error(t, err)
	assert.Nil(t, cleanups)

	var failureRecord map[string]any
	for _, rawRecord := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		assert.NoError(t, json.Unmarshal(rawRecord, &record))
		if record["msg"] == "Failed to watch DCGM fields" {
			failureRecord = record
			break
		}
	}

	if assert.NotNil(t, failureRecord) {
		assert.Equal(t, dcgm.FE_NONE.String(), failureRecord["entity_type"])
		assert.Equal(t, []any{float64(dcgm.DCGM_FI_DEV_GPU_TEMP), float64(dcgm.DCGM_FI_DRIVER_VERSION)},
			failureRecord["field_ids"])
		assert.Equal(t, []any{"DCGM_FI_DEV_GPU_TEMP", "DCGM_FI_DRIVER_VERSION"}, failureRecord["field_names"])
		assert.Contains(t, failureRecord, "field_group")
		assert.Contains(t, failureRecord, logging.GroupIDKey)
		assert.Equal(t, "g", failureRecord["device_option_mode"])
		assert.Equal(t, "watch failed", failureRecord[logging.ErrorKey])
	}
}

// TestDeviceWatcher_createGenericGroupWithoutComputeInstances checks that the group gets every monitored
// GPU, GPU instance, CPU or switch, that DCGM errors when creating or filling the group are returned,
// and that a failed group destroy is not.
func TestDeviceWatcher_createGenericGroupWithoutComputeInstances(t *testing.T) {
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
			gotGroupID, gotFunc, err := d.createGenericGroupForTest(mockDeviceInfo, false, false)
			gotFunc() // Ensure DestroyGroup function gets called

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockGroupID, gotGroupID, "expected group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected an error.")
			}
		})
	}
}

// TestDeviceWatcher_createCPUCoreGroups checks that the watched cores of all CPUs share one group, that
// CPUs without watched cores add nothing, and that DCGM errors are returned.
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
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: true,
					{ParentID: 1, ChildID: 3}: true,
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
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(2)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(3)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

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
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: false,
					{ParentID: 1, ChildID: 3}: false,
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
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: false,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: true,
					{ParentID: 1, ChildID: 3}: false,
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
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(2)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

				return doNothing
			},
			wantErr: false,
		},
		{
			name: "Random Unit Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: true,
					{ParentID: 1, ChildID: 3}: true,
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
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: true,
					{ParentID: 1, ChildID: 3}: true,
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
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], fmt.Errorf("random error"))
				return doNothing
			},
			wantErr: true,
		},
		{
			name: "AddEntityToGroup Error",
			mockDeviceInfoFunc: func() *mockdeviceinfo.MockProvider {
				cpuToCores := make(map[int][]uint)
				cpuToCores[0] = []uint{0, 1}
				cpuToCores[1] = []uint{2, 3}

				watchedCPUs := map[uint]bool{0: true, 1: true}
				watchedCores := map[testutils.WatchedEntityKey]bool{
					{ParentID: 0, ChildID: 0}: true,
					{ParentID: 0, ChildID: 1}: true,
					{ParentID: 1, ChildID: 2}: true,
					{ParentID: 1, ChildID: 3}: true,
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
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE, uint(2)).Return(nil)
				mockDCGM.EXPECT().AddEntityToGroup(mockGroupHandles[0], dcgm.FE_CPU_CORE,
					uint(3)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(fmt.Errorf("some other error"))
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
			gotGroupIDs, gotFuncs, err := d.createCPUCoreGroupsForTest(mockDeviceInfo)
			// Ensure DestroyGroup functions gets called
			for _, gotFunc := range gotFuncs {
				gotFunc()
			}

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockGroupIDs, gotGroupIDs, "expected group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected an error.")
			}
		})
	}
}

// TestDeviceWatcher_createNVLinkGroups checks that each switch with watched links that are up gets one
// group holding those links, that switches without any get no group, and that DCGM errors are returned.
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

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

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
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
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

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)

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
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
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

				return []dcgm.GroupHandle{mockGroupHandle1}
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

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], fmt.Errorf("random error"))
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

				return []dcgm.GroupHandle{mockGroupHandle1}
			},
			mockDCGMFunc: func(mockGroupHandles []dcgm.GroupHandle) func() {
				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(mockGroupHandles[0], nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH, uint(0)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(0), dcgm.FE_SWITCH, uint(1)).Return(nil)
				mockDCGM.EXPECT().AddLinkEntityToGroup(mockGroupHandles[0], uint(1), dcgm.FE_SWITCH,
					uint(1)).Return(fmt.Errorf("some error"))
				mockDCGM.EXPECT().DestroyGroup(mockGroupHandles[0]).Return(nil)
				return doNothing
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDeviceInfo := tt.mockDeviceInfoFunc()
			mockGroupIDs := tt.expectGroupIDs()
			f := tt.mockDCGMFunc(mockGroupIDs)
			defer f()

			d := &DeviceWatcher{}
			gotGroupIDs, gotFuncs, err := d.createNVLinkGroupsForTest(mockDeviceInfo)
			// Ensure DestroyGroup functions gets called
			for _, gotFunc := range gotFuncs {
				gotFunc()
			}

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockGroupIDs, gotGroupIDs, "expected group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected an error.")
			}
		})
	}
}

// batchingDCGM is a mock DCGM client that also unwatches in batches, as the
// client dcgmprovider.Client returns does. unwatcher holds the batch
// expectations.
type batchingDCGM struct {
	*mockdcgm.MockDCGM
	unwatcher *mockdcgm.MockFieldGroupUnwatcher
}

// newBatchingDCGM returns a batching mock client on ctrl.
func newBatchingDCGM(ctrl *gomock.Controller) *batchingDCGM {
	return &batchingDCGM{MockDCGM: mockdcgm.NewMockDCGM(ctrl), unwatcher: mockdcgm.NewMockFieldGroupUnwatcher(ctrl)}
}

// UnwatchFieldGroups forwards the batch to the unwatcher mock.
func (c *batchingDCGM) UnwatchFieldGroups(watches []dcgmprovider.FieldGroupWatch) error {
	return c.unwatcher.UnwatchFieldGroups(watches)
}

// groupEntity names one entity added to a DCGM group: a GPU or switch link,
// or a CPU core (parentType FE_NONE).
type groupEntity struct {
	parentType dcgm.Field_Entity_Group
	parent     uint
	index      uint
}

// groupRecorder backs a batching mock client with live groups, field groups
// and watches, records which entities went into which group, and can fail one
// CreateGroup call, one watch call, or refuse chosen entity additions. It
// sets no UpdateAllFields expectation, so an UpdateAllFields call of the
// exporter's own fails the test.
type groupRecorder struct {
	// failCreateAt makes the CreateGroup call with this 1-based number fail.
	failCreateAt int
	// failWatchAt makes the watch call with this 1-based number fail.
	failWatchAt int
	// refuseAdd reports whether the entity addition with this 1-based number
	// is refused.
	refuseAdd func(attempt int) bool
	// unwatchErr is returned by every UnwatchFieldGroups call.
	unwatchErr error

	nextHandle     uintptr
	groupOrder     []uintptr
	liveGroups     map[uintptr]bool
	liveFields     map[uintptr]bool
	liveWatches    map[dcgmprovider.FieldGroupWatch]bool
	members        map[uintptr][]groupEntity
	attempts       map[uintptr][]int
	addAttempts    int
	createCalls    int
	fieldGroups    int
	watchCalls     int
	unwatchBatches [][]dcgmprovider.FieldGroupWatch
}

// newGroupRecorder returns an empty recorder; set its failure fields before
// calling mock.
func newGroupRecorder() *groupRecorder {
	return &groupRecorder{
		liveGroups:  map[uintptr]bool{},
		liveFields:  map[uintptr]bool{},
		liveWatches: map[dcgmprovider.FieldGroupWatch]bool{},
		members:     map[uintptr][]groupEntity{},
		attempts:    map[uintptr][]int{},
	}
}

// addEntity records one entity addition to group and refuses it when
// refuseAdd says so.
func (r *groupRecorder) addEntity(group dcgm.GroupHandle, entity groupEntity) error {
	r.addAttempts++
	r.attempts[group.GetHandle()] = append(r.attempts[group.GetHandle()], r.addAttempts)
	if r.refuseAdd != nil && r.refuseAdd(r.addAttempts) {
		return errors.New("entity refused")
	}

	r.members[group.GetHandle()] = append(r.members[group.GetHandle()], entity)
	return nil
}

// mock returns a batching mock client whose group, watch and cleanup calls go
// to r.
func (r *groupRecorder) mock(ctrl *gomock.Controller) *batchingDCGM {
	client := newBatchingDCGM(ctrl)

	// Groups and their entities.
	client.EXPECT().CreateGroup(gomock.Any()).DoAndReturn(func(string) (dcgm.GroupHandle, error) {
		r.createCalls++
		if r.createCalls == r.failCreateAt {
			return dcgm.GroupHandle{}, errors.New("group limit reached")
		}

		r.nextHandle++
		r.liveGroups[r.nextHandle] = true
		r.groupOrder = append(r.groupOrder, r.nextHandle)
		group := dcgm.GroupHandle{}
		group.SetHandle(r.nextHandle)
		return group, nil
	}).AnyTimes()
	client.EXPECT().AddLinkEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(group dcgm.GroupHandle, index uint, parentType dcgm.Field_Entity_Group, parent uint) error {
			return r.addEntity(group, groupEntity{parentType, parent, index})
		}).AnyTimes()
	client.EXPECT().AddEntityToGroup(gomock.Any(), dcgm.FE_CPU_CORE, gomock.Any()).DoAndReturn(
		func(group dcgm.GroupHandle, _ dcgm.Field_Entity_Group, core uint) error {
			return r.addEntity(group, groupEntity{dcgm.FE_NONE, 0, core})
		}).AnyTimes()

	// Field groups and watches.
	client.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(func(string, []dcgm.Short) (dcgm.FieldHandle, error) {
		r.fieldGroups++
		r.nextHandle++
		r.liveFields[r.nextHandle] = true
		fieldGroup := dcgm.FieldHandle{}
		fieldGroup.SetHandle(r.nextHandle)
		return fieldGroup, nil
	}).AnyTimes()
	client.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(fieldGroup dcgm.FieldHandle, group dcgm.GroupHandle, _ int64, _ float64, _ int32) error {
			r.watchCalls++
			if r.watchCalls == r.failWatchAt {
				return errors.New("watch refused")
			}

			r.liveWatches[dcgmprovider.FieldGroupWatch{FieldGroup: fieldGroup, Group: group}] = true
			return nil
		}).AnyTimes()

	// Teardown.
	client.unwatcher.EXPECT().UnwatchFieldGroups(gomock.Any()).DoAndReturn(func(watches []dcgmprovider.FieldGroupWatch) error {
		r.unwatchBatches = append(r.unwatchBatches, watches)
		for _, watch := range watches {
			delete(r.liveWatches, watch)
		}
		return r.unwatchErr
	}).AnyTimes()
	client.EXPECT().FieldGroupDestroy(gomock.Any()).DoAndReturn(func(fieldGroup dcgm.FieldHandle) error {
		delete(r.liveFields, fieldGroup.GetHandle())
		return nil
	}).AnyTimes()
	client.EXPECT().DestroyGroup(gomock.Any()).DoAndReturn(func(group dcgm.GroupHandle) error {
		delete(r.liveGroups, group.GetHandle())
		return nil
	}).AnyTimes()
	client.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{}, errors.New("unknown field")).AnyTimes()

	return client
}

// groupSizes returns how many entities each created group holds, in creation
// order.
func (r *groupRecorder) groupSizes() []int {
	var sizes []int
	for _, handle := range r.groupOrder {
		sizes = append(sizes, len(r.members[handle]))
	}
	return sizes
}

// allMembers returns every entity added to any group, in creation order.
func (r *groupRecorder) allMembers() []groupEntity {
	var members []groupEntity
	for _, handle := range r.groupOrder {
		members = append(members, r.members[handle]...)
	}
	return members
}

// gpuLinkTopology returns GPUs holding total supported links, 36 per GPU as
// on current NVLink GPUs (DCGM allows at most 32 GPUs), and the link entities
// a watch must cover.
func gpuLinkTopology(total int) ([]deviceinfo.GPUInfo, []groupEntity) {
	const linksPerGPU = 36
	var infos []deviceinfo.GPUInfo
	var want []groupEntity
	for gpu := 0; total > 0; gpu++ {
		info := deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: uint(gpu)}}
		for link := 0; link < linksPerGPU && total > 0; link++ {
			info.NvLinks = append(info.NvLinks, dcgm.NvLinkStatus{Index: uint(link), ParentId: uint(gpu), ParentType: dcgm.FE_GPU, State: dcgm.LS_UP})
			want = append(want, groupEntity{dcgm.FE_GPU, uint(gpu), uint(link)})
			total--
		}
		infos = append(infos, info)
	}
	return infos, want
}

// switchLinkTopology returns switches holding total up links, 64 per switch,
// and the link entities a watch must cover.
func switchLinkTopology(total int) ([]deviceinfo.SwitchInfo, []groupEntity) {
	const linksPerSwitch = 64
	var infos []deviceinfo.SwitchInfo
	var want []groupEntity
	for sw := 0; total > 0; sw++ {
		info := deviceinfo.SwitchInfo{EntityId: uint(sw)}
		for link := 0; link < linksPerSwitch && total > 0; link++ {
			info.NvLinks = append(info.NvLinks, dcgm.NvLinkStatus{Index: uint(link), ParentId: uint(sw), ParentType: dcgm.FE_SWITCH, State: dcgm.LS_UP})
			want = append(want, groupEntity{dcgm.FE_SWITCH, uint(sw), uint(link)})
			total--
		}
		infos = append(infos, info)
	}
	return infos, want
}

// cpuCoreTopology returns CPUs with the given core counts, cores numbered
// across all CPUs as DCGM does, and the core entities a watch must cover.
func cpuCoreTopology(coreCounts []int) ([]deviceinfo.CPUInfo, []groupEntity) {
	var cpus []deviceinfo.CPUInfo
	var want []groupEntity
	nextCore := uint(0)
	for cpu, count := range coreCounts {
		info := deviceinfo.CPUInfo{EntityId: uint(cpu)}
		for range count {
			info.Cores = append(info.Cores, nextCore)
			want = append(want, groupEntity{dcgm.FE_NONE, 0, nextCore})
			nextCore++
		}
		cpus = append(cpus, info)
	}
	return cpus, want
}

// groupTopology describes the entities of one test scope: NVLink links of
// GPUs and switches, or the cores of CPUs.
type groupTopology struct {
	gpuLinks    int
	switchLinks int
	cpuCores    []int
}

// mockTopology returns a device provider for topology and the entities a
// watch must cover. CPU cores are numbered across all CPUs, as DCGM does.
func mockTopology(ctrl *gomock.Controller, topology groupTopology) (*mockdeviceinfo.MockProvider, []groupEntity) {
	deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)

	if len(topology.cpuCores) > 0 {
		cpus, want := cpuCoreTopology(topology.cpuCores)

		deviceInfo.EXPECT().InfoType().Return(dcgm.FE_CPU_CORE).AnyTimes()
		deviceInfo.EXPECT().COpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
		deviceInfo.EXPECT().IsCPUWatched(gomock.Any()).Return(true).AnyTimes()
		deviceInfo.EXPECT().IsCoreWatched(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
		deviceInfo.EXPECT().CPUs().Return(cpus).AnyTimes()
		return deviceInfo, want
	}

	gpus, gpuWant := gpuLinkTopology(topology.gpuLinks)
	switches, switchWant := switchLinkTopology(topology.switchLinks)
	want := slices.Concat(gpuWant, switchWant)

	deviceInfo.EXPECT().InfoType().Return(dcgm.FE_LINK).AnyTimes()
	deviceInfo.EXPECT().SOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	deviceInfo.EXPECT().GPUs().Return(gpus).AnyTimes()
	deviceInfo.EXPECT().Switches().Return(switches).AnyTimes()
	deviceInfo.EXPECT().IsSwitchWatched(gomock.Any()).Return(true).AnyTimes()
	deviceInfo.EXPECT().IsLinkWatched(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	return deviceInfo, want
}

// TestDeviceWatcher_WatchSharesGroupsUpToTheLimit checks that one watch puts
// the links of all GPUs, the links of all switches and the cores of all CPUs
// into as few groups as the 1024-entity limit allows, watches every entity
// exactly once, makes field groups x groups watch calls and no UpdateAllFields
// call of its own, and unwatches all of its watches in one call on close.
func TestDeviceWatcher_WatchSharesGroupsUpToTheLimit(t *testing.T) {
	// 130 fields need two field groups; with the second watch group that
	// makes three field groups for every DCGM group.
	watchGroups := []FieldWatchGroup{
		{Name: "fast", Fields: testFieldIDs(130), IntervalMSec: 1000},
		{Name: "slow", Fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}, IntervalMSec: 30000},
	}
	const fieldGroups = 3

	tests := []struct {
		name       string
		topology   groupTopology
		wantGroups []int
	}{
		{name: "links of 1 GPU", topology: groupTopology{gpuLinks: 36}, wantGroups: []int{36}},
		{name: "links of 4 GPUs", topology: groupTopology{gpuLinks: 4 * 36}, wantGroups: []int{144}},
		{name: "links of 8 GPUs", topology: groupTopology{gpuLinks: 8 * 36}, wantGroups: []int{288}},
		{name: "links of 2 switches", topology: groupTopology{switchLinks: 128}, wantGroups: []int{128}},
		{name: "GPU links and switch links stay in separate groups", topology: groupTopology{gpuLinks: 8 * 36, switchLinks: 128}, wantGroups: []int{288, 128}},
		{name: "exactly 1024 GPU links", topology: groupTopology{gpuLinks: 1024}, wantGroups: []int{1024}},
		{name: "1025 GPU links", topology: groupTopology{gpuLinks: 1025}, wantGroups: []int{1024, 1}},
		{name: "links of 32 GPUs", topology: groupTopology{gpuLinks: 32 * 36}, wantGroups: []int{1024, 128}},
		{name: "more than 1024 switch links", topology: groupTopology{switchLinks: 1088}, wantGroups: []int{1024, 64}},
		{name: "cores of 2 CPUs", topology: groupTopology{cpuCores: []int{72, 72}}, wantGroups: []int{144}},
		// DCGM numbers at most 1024 cores, so this checks only the limit logic.
		{name: "defensive: more than 1024 cores", topology: groupTopology{cpuCores: []int{550, 550}}, wantGroups: []int{1024, 76}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			recorder := newGroupRecorder()
			client := recorder.mock(ctrl)
			deviceInfo, want := mockTopology(ctrl, tt.topology)

			result, err := NewDeviceWatcherWithClient(client).Watch(watchGroups, deviceInfo, WatchConfiguredEntities)
			require.NoError(t, err)

			assert.Equal(t, tt.wantGroups, recorder.groupSizes(), "entities per group")
			assert.ElementsMatch(t, want, recorder.allMembers(), "every entity is watched exactly once")

			groups := len(tt.wantGroups)
			assert.Equal(t, fieldGroups, recorder.fieldGroups)
			assert.Equal(t, fieldGroups*groups, recorder.watchCalls, "one watch call per field group and DCGM group")
			assert.Len(t, result.Bindings(), fieldGroups*groups)

			result.Close()
			require.Len(t, recorder.unwatchBatches, 1, "close unwatches every watch in one call")
			assert.Len(t, recorder.unwatchBatches[0], fieldGroups*groups)
			assert.Empty(t, recorder.liveWatches)
			assert.Empty(t, recorder.liveFields)
			assert.Empty(t, recorder.liveGroups)
		})
	}
}

// TestDeviceWatcher_WatchCountsRefusedEntitiesTowardTheLimit refuses 10 of
// the first 1024 link additions and expects the next group to start at the
// 1025th attempt anyway, so no group is ever offered more than 1024 entities.
func TestDeviceWatcher_WatchCountsRefusedEntitiesTowardTheLimit(t *testing.T) {
	ctrl := gomock.NewController(t)
	recorder := newGroupRecorder()
	recorder.refuseAdd = func(attempt int) bool { return attempt > 100 && attempt <= 110 }
	client := recorder.mock(ctrl)
	deviceInfo, _ := mockTopology(ctrl, groupTopology{gpuLinks: 1030})
	watchGroups := []FieldWatchGroup{{Name: "links", Fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}, IntervalMSec: 1000}}

	result, err := NewDeviceWatcherWithClient(client).Watch(watchGroups, deviceInfo, WatchConfiguredEntities)
	require.NoError(t, err)
	defer result.Close()

	require.Len(t, recorder.groupOrder, 2)
	assert.Len(t, recorder.attempts[recorder.groupOrder[0]], 1024)
	assert.Equal(t, []int{1025, 1026, 1027, 1028, 1029, 1030}, recorder.attempts[recorder.groupOrder[1]])
	assert.Equal(t, []int{1014, 6}, recorder.groupSizes(), "refused links are skipped")
}

// TestDeviceWatcher_WatchGroupCreationFailureLeavesNothingBehind fails a
// CreateGroup call after an earlier shared group exists and expects Watch to
// return the error and no result, destroy every group it created, and create
// no field group and no watch.
func TestDeviceWatcher_WatchGroupCreationFailureLeavesNothingBehind(t *testing.T) {
	tests := []struct {
		name     string
		topology groupTopology
	}{
		{name: "second GPU link group", topology: groupTopology{gpuLinks: 32 * 36}},
		{name: "switch link group after the GPU link group", topology: groupTopology{gpuLinks: 36, switchLinks: 64}},
		{name: "second CPU core group", topology: groupTopology{cpuCores: []int{550, 550}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			recorder := newGroupRecorder()
			recorder.failCreateAt = 2
			client := recorder.mock(ctrl)
			deviceInfo, _ := mockTopology(ctrl, tt.topology)
			watchGroups := []FieldWatchGroup{{Name: "links", Fields: testFieldIDs(130), IntervalMSec: 1000}}

			result, err := NewDeviceWatcherWithClient(client).Watch(watchGroups, deviceInfo, WatchConfiguredEntities)

			require.ErrorContains(t, err, "group limit reached")
			assert.Nil(t, result)
			assert.Len(t, recorder.groupOrder, 1, "the first group was created")
			assert.Empty(t, recorder.liveGroups, "every created group is destroyed")
			assert.Zero(t, recorder.fieldGroups, "no field group is created")
			assert.Zero(t, recorder.watchCalls, "no watch is installed")
			assert.Empty(t, recorder.unwatchBatches, "nothing is unwatched")
		})
	}
}

// TestDeviceWatcher_WatchFailureLeavesNothingBehind fails one watch call of a
// scope with shared link groups and expects the watches already installed to
// be unwatched in one call and every group and field group destroyed, with no
// UpdateAllFields call of the exporter's own, even when the unwatch call also
// reports a failed restore.
func TestDeviceWatcher_WatchFailureLeavesNothingBehind(t *testing.T) {
	tests := []struct {
		name       string
		failAt     int
		unwatchErr error
	}{
		{name: "first watch call fails", failAt: 1},
		{name: "second watch call fails", failAt: 2},
		{name: "last watch call fails", failAt: 4},
		{name: "last watch call fails and restore fails", failAt: 4, unwatchErr: &dcgmprovider.RestoreError{Err: errors.New("host engine lost")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			recorder := newGroupRecorder()
			recorder.failWatchAt = tt.failAt
			recorder.unwatchErr = tt.unwatchErr
			client := recorder.mock(ctrl)
			deviceInfo, _ := mockTopology(ctrl, groupTopology{gpuLinks: 32 * 36})

			// Two field groups on two link groups make four watch calls.
			watchGroups := []FieldWatchGroup{{Name: "links", Fields: testFieldIDs(130), IntervalMSec: 1000}}
			result, err := NewDeviceWatcherWithClient(client).Watch(watchGroups, deviceInfo, WatchConfiguredEntities)

			require.ErrorContains(t, err, "watch refused")
			assert.Nil(t, result)
			assert.Empty(t, recorder.liveWatches, "no watch is left behind")
			assert.Empty(t, recorder.liveGroups, "no group is left behind")
			assert.Empty(t, recorder.liveFields, "no field group is left behind")
			if tt.failAt == 1 {
				assert.Empty(t, recorder.unwatchBatches, "nothing was installed, so nothing is unwatched")
			} else {
				assert.Len(t, recorder.unwatchBatches, 1, "installed watches are unwatched in one call")
			}
		})
	}
}

// TestWatchResultCloseJoinedUnwatchesOnce closes a joined result holding two
// DCGM-built scopes, a nested join and a result with its own closer, and
// expects one UnwatchFieldGroups batch with every DCGM-built binding in
// creation order, then each scope's field groups and groups destroyed in
// reverse order, the closer called in its place, and later Close calls of
// the joined result or its parts doing nothing.
func TestWatchResultCloseJoinedUnwatchesOnce(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newBatchingDCGM(ctrl)
	handles := func(group, fieldGroup uintptr) (dcgm.GroupHandle, dcgm.FieldHandle) {
		g, f := dcgm.GroupHandle{}, dcgm.FieldHandle{}
		g.SetHandle(group)
		f.SetHandle(fieldGroup)
		return g, f
	}
	scope := func(group dcgm.GroupHandle, fieldGroup dcgm.FieldHandle) *WatchResult {
		return &WatchResult{
			client:      client,
			groups:      []dcgm.GroupHandle{group},
			fieldGroups: []dcgm.FieldHandle{fieldGroup},
			bindings:    []WatchBinding{{Group: group, FieldGroup: fieldGroup}},
		}
	}

	firstGroup, firstFields := handles(1, 11)
	secondGroup, secondFields := handles(2, 12)
	thirdGroup, thirdFields := handles(3, 13)
	var events []string
	first := scope(firstGroup, firstFields)
	closer := ResultFromBindings(nil, func() { events = append(events, "closer") })
	second := scope(secondGroup, secondFields)
	third := scope(thirdGroup, thirdFields)
	joined := JoinWatchResults(first, closer, JoinWatchResults(second, third))

	client.unwatcher.EXPECT().UnwatchFieldGroups([]dcgmprovider.FieldGroupWatch{
		{FieldGroup: firstFields, Group: firstGroup},
		{FieldGroup: secondFields, Group: secondGroup},
		{FieldGroup: thirdFields, Group: thirdGroup},
	}).DoAndReturn(func([]dcgmprovider.FieldGroupWatch) error {
		events = append(events, "unwatch")
		return nil
	}).Times(1)
	client.EXPECT().FieldGroupDestroy(gomock.Any()).DoAndReturn(func(fieldGroup dcgm.FieldHandle) error {
		events = append(events, fmt.Sprintf("fields %d", fieldGroup.GetHandle()))
		return nil
	}).Times(3)
	client.EXPECT().DestroyGroup(gomock.Any()).DoAndReturn(func(group dcgm.GroupHandle) error {
		events = append(events, fmt.Sprintf("group %d", group.GetHandle()))
		return nil
	}).Times(3)

	joined.Close()
	joined.Close()
	first.Close()
	third.Close()

	assert.Equal(t, []string{
		"unwatch",
		"fields 13", "group 3",
		"fields 12", "group 2",
		"closer",
		"fields 11", "group 1",
	}, events)
}

// TestWatchResultCloseJoinedSkipsClosedParts closes one part before the
// joined result and expects the joined close to unwatch only the open part.
func TestWatchResultCloseJoinedSkipsClosedParts(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newBatchingDCGM(ctrl)
	part := func(handle uintptr) *WatchResult {
		group, fieldGroup := dcgm.GroupHandle{}, dcgm.FieldHandle{}
		group.SetHandle(handle)
		fieldGroup.SetHandle(handle + 10)
		return &WatchResult{
			client:      client,
			groups:      []dcgm.GroupHandle{group},
			fieldGroups: []dcgm.FieldHandle{fieldGroup},
			bindings:    []WatchBinding{{Group: group, FieldGroup: fieldGroup}},
		}
	}
	first, second := part(1), part(2)
	joined := JoinWatchResults(first, second)

	var batches [][]dcgmprovider.FieldGroupWatch
	client.unwatcher.EXPECT().UnwatchFieldGroups(gomock.Any()).DoAndReturn(func(watches []dcgmprovider.FieldGroupWatch) error {
		batches = append(batches, watches)
		return nil
	}).Times(2)
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).Times(2)
	client.EXPECT().DestroyGroup(gomock.Any()).Return(nil).Times(2)

	first.Close()
	joined.Close()

	require.Len(t, batches, 2)
	assert.Equal(t, first.bindings[0].Group, batches[0][0].Group)
	require.Len(t, batches[1], 1, "the joined close skips the part closed earlier")
	assert.Equal(t, second.bindings[0].Group, batches[1][0].Group)
}

// singleWatchResult returns a DCGM-built result on client with one group,
// one field group and their one binding, all with handle.
func singleWatchResult(client dcgmprovider.DCGM, handle uintptr) *WatchResult {
	group, fieldGroup := dcgm.GroupHandle{}, dcgm.FieldHandle{}
	group.SetHandle(handle)
	fieldGroup.SetHandle(handle)

	return &WatchResult{
		client:      client,
		groups:      []dcgm.GroupHandle{group},
		fieldGroups: []dcgm.FieldHandle{fieldGroup},
		bindings:    []WatchBinding{{Group: group, FieldGroup: fieldGroup}},
	}
}

// expectGroupTeardown lets client destroy any field group and group.
func expectGroupTeardown(client *mockdcgm.MockDCGM) {
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	client.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
}

// TestWatchResultCloseBatchesPerClient closes a joined result whose parts
// use two batching clients and one client without batch unwatch, and expects
// one batch for each batching client with its own watches in creation order,
// and one UnwatchFields call per binding for the other client.
func TestWatchResultCloseBatchesPerClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	first, second := newBatchingDCGM(ctrl), newBatchingDCGM(ctrl)
	plain := mockdcgm.NewMockDCGM(ctrl)
	expectGroupTeardown(first.MockDCGM)
	expectGroupTeardown(second.MockDCGM)
	expectGroupTeardown(plain)

	parts := []*WatchResult{
		singleWatchResult(first, 1),
		singleWatchResult(second, 2),
		singleWatchResult(plain, 3),
		singleWatchResult(first, 4),
		singleWatchResult(plain, 5),
	}
	watchOf := func(part *WatchResult) dcgmprovider.FieldGroupWatch {
		return dcgmprovider.FieldGroupWatch{FieldGroup: part.bindings[0].FieldGroup, Group: part.bindings[0].Group}
	}

	first.unwatcher.EXPECT().UnwatchFieldGroups([]dcgmprovider.FieldGroupWatch{watchOf(parts[0]), watchOf(parts[3])}).Return(nil).Times(1)
	second.unwatcher.EXPECT().UnwatchFieldGroups([]dcgmprovider.FieldGroupWatch{watchOf(parts[1])}).Return(nil).Times(1)
	plain.EXPECT().UnwatchFields(parts[2].bindings[0].FieldGroup, parts[2].bindings[0].Group).Return(nil).Times(1)
	plain.EXPECT().UnwatchFields(parts[4].bindings[0].FieldGroup, parts[4].bindings[0].Group).Return(nil).Times(1)

	JoinWatchResults(parts...).Close()
}

// TestWatchResultClosePartWaitsForJoinedClose closes a part while the joined
// result is still unwatching it and expects the part's Close to return only
// after the joined close has destroyed the part's groups.
func TestWatchResultClosePartWaitsForJoinedClose(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newBatchingDCGM(ctrl)
	group, fieldGroup := dcgm.GroupHandle{}, dcgm.FieldHandle{}
	group.SetHandle(1)
	fieldGroup.SetHandle(11)
	part := &WatchResult{
		client:      client,
		groups:      []dcgm.GroupHandle{group},
		fieldGroups: []dcgm.FieldHandle{fieldGroup},
		bindings:    []WatchBinding{{Group: group, FieldGroup: fieldGroup}},
	}
	joined := JoinWatchResults(part)

	unwatching, release := make(chan struct{}), make(chan struct{})
	var destroyed atomic.Bool
	client.unwatcher.EXPECT().UnwatchFieldGroups(gomock.Any()).DoAndReturn(func([]dcgmprovider.FieldGroupWatch) error {
		close(unwatching)
		<-release
		return nil
	}).Times(1)
	client.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil).Times(1)
	client.EXPECT().DestroyGroup(group).DoAndReturn(func(dcgm.GroupHandle) error {
		destroyed.Store(true)
		return nil
	}).Times(1)

	joinedDone := make(chan struct{})
	go func() {
		joined.Close()
		close(joinedDone)
	}()
	<-unwatching

	partDone := make(chan struct{})
	go func() {
		part.Close()
		close(partDone)
	}()

	select {
	case <-partDone:
		t.Fatal("part Close returned while the joined close was still unwatching it")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-partDone
	assert.True(t, destroyed.Load(), "part Close returned before its groups were destroyed")
	<-joinedDone
}

// TestDeviceWatcher_createNVLinkGroupsSkipsUnsupportedGPULinks preserves supported physical IDs and omits empty GPU groups.
func TestDeviceWatcher_createNVLinkGroupsSkipsUnsupportedGPULinks(t *testing.T) {
	tests := []struct {
		name            string
		links           []dcgm.NvLinkStatus
		wantLinkIndices []uint
	}{
		{
			name: "preserve supported sparse physical indices",
			links: []dcgm.NvLinkStatus{
				{Index: 18, State: dcgm.LS_NOT_SUPPORTED},
				{Index: 0, State: dcgm.LS_UP},
				{Index: 17, State: dcgm.LS_DOWN},
				{Index: 35, State: dcgm.LS_DISABLED},
			},
			wantLinkIndices: []uint{0, 17, 35},
		},
		{
			name: "no group for only unsupported links",
			links: []dcgm.NvLinkStatus{
				{Index: 18, State: dcgm.LS_NOT_SUPPORTED},
				{Index: 19, State: dcgm.LS_NOT_SUPPORTED},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockDCGM := mockdcgm.NewMockDCGM(ctrl)
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(mockDCGM)
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })

			deviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
			deviceInfo.EXPECT().GPUs().Return([]deviceinfo.GPUInfo{{
				DeviceInfo: dcgm.Device{GPU: 0},
				NvLinks:    tt.links,
			}}).AnyTimes()
			deviceInfo.EXPECT().Switches().Return(nil).AnyTimes()

			var wantGroupIDs []dcgm.GroupHandle
			if len(tt.wantLinkIndices) > 0 {
				groupID := dcgm.GroupHandle{}
				groupID.SetHandle(uintptr(1))
				wantGroupIDs = []dcgm.GroupHandle{groupID}

				mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupID, nil)
				for _, index := range tt.wantLinkIndices {
					mockDCGM.EXPECT().AddLinkEntityToGroup(groupID, index, dcgm.FE_GPU, uint(0)).Return(nil)
				}
				mockDCGM.EXPECT().DestroyGroup(groupID).Return(nil)
			}

			watcher := &DeviceWatcher{}
			gotGroupIDs, cleanupFuncs, err := watcher.createNVLinkGroupsForTest(deviceInfo)
			for _, cleanup := range cleanupFuncs {
				cleanup()
			}

			require.NoError(t, err)
			assert.Equal(t, wantGroupIDs, gotGroupIDs)
		})
	}
}

// Test_newFieldGroup checks that a field group is created and its handle returned, that a failure reading
// random bytes for the group name or creating the group is an error, and that a failure destroying the group
// in cleanup is not.
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
			gotFieldGroupIDs, gotFunc, err := newFieldGroupForTest(input)
			gotFunc() // Ensure DestroyGroup functions gets called

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				assert.Equal(t, mockFieldGroupIDs, gotFieldGroupIDs, "expected field group IDs to be the same.")
			} else {
				assert.NotNil(t, err, "expected an error.")
			}
		})
	}
}

// Test_newFieldGroupSimpleDedupesFields passes field IDs with repeats and expects DCGM to be asked for a
// group of each ID once, in first-seen order.
func Test_newFieldGroupSimpleDedupesFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	mockFieldGroupHandle := dcgm.FieldHandle{}
	mockFieldGroupHandle.SetHandle(uintptr(1))
	mockDCGM.EXPECT().
		FieldGroupCreate(gomock.Any(), gomock.Eq([]dcgm.Short{1, 2, 3})).
		Return(mockFieldGroupHandle, nil)

	got, err := newFieldGroupWithClient(dcgmprovider.Client(), []dcgm.Short{1, 2, 1, 3, 2})

	assert.NoError(t, err)
	assert.Equal(t, mockFieldGroupHandle, got)
}

// TestWatchResultCloseUsesCreatingClient verifies Close uses the creating
// client and only unwatches the recorded group and field-group pairs, even
// when a caller closes the result twice.
func TestWatchResultCloseUsesCreatingClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	creatingClient := mockdcgm.NewMockDCGM(ctrl)
	replacementClient := mockdcgm.NewMockDCGM(ctrl)
	previousClient := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(previousClient) })

	firstGroup := dcgm.GroupHandle{}
	firstGroup.SetHandle(uintptr(1))
	secondGroup := dcgm.GroupHandle{}
	secondGroup.SetHandle(uintptr(2))
	firstFieldGroup := dcgm.FieldHandle{}
	firstFieldGroup.SetHandle(uintptr(3))
	secondFieldGroup := dcgm.FieldHandle{}
	secondFieldGroup.SetHandle(uintptr(4))
	creatingClient.EXPECT().UnwatchFields(firstFieldGroup, firstGroup).Return(nil)
	creatingClient.EXPECT().UnwatchFields(secondFieldGroup, secondGroup).Return(nil)
	creatingClient.EXPECT().FieldGroupDestroy(firstFieldGroup).Return(nil)
	creatingClient.EXPECT().FieldGroupDestroy(secondFieldGroup).Return(nil)
	creatingClient.EXPECT().DestroyGroup(firstGroup).Return(nil)
	creatingClient.EXPECT().DestroyGroup(secondGroup).Return(nil)

	resources := WatchResult{
		client:      creatingClient,
		groups:      []dcgm.GroupHandle{firstGroup, secondGroup},
		fieldGroups: []dcgm.FieldHandle{firstFieldGroup, secondFieldGroup},
		bindings: []WatchBinding{
			{Group: firstGroup, FieldGroup: firstFieldGroup},
			{Group: secondGroup, FieldGroup: secondFieldGroup},
		},
	}
	dcgmprovider.SetClient(replacementClient)
	resources.Close()
	resources.Close()
}

// TestWatchResultCloseLogsOnlyRealUnwatchFailures checks that Close logs each
// failure the batch unwatch reports, except the statuses DCGM returns after it
// has shut down and restore failures the client has already logged, and still
// destroys the groups.
func TestWatchResultCloseLogsOnlyRealUnwatchFailures(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantLogs []string
	}{
		{name: "no failure"},
		{name: "only benign failures", err: errors.Join(
			errors.New("unwatch field group 3 on group 1: "+DCGM_ST_NOT_CONFIGURED),
			errors.New("unwatch field group 4 on group 2: "+DCGM_ST_FIELD_NOT_WATCHED),
		)},
		{name: "one real failure among benign ones", err: errors.Join(
			errors.New("unwatch field group 3 on group 1: "+DCGM_ST_NOT_CONFIGURED),
			errors.New("unwatch field group 4 on group 2: host engine lost"),
		), wantLogs: []string{"unwatch field group 4 on group 2: host engine lost"}},
		{name: "single error that is not joined", err: errors.New("host engine lost"), wantLogs: []string{"host engine lost"}},
		{name: "restore failure the client already logged", err: errors.Join(
			errors.New("unwatch field group 3 on group 1: host engine lost"),
			&dcgmprovider.RestoreError{Err: errors.New("restore DCGM watch: host engine lost")},
		), wantLogs: []string{"unwatch field group 3 on group 1: host engine lost"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			ctrl := gomock.NewController(t)
			client := newBatchingDCGM(ctrl)
			group, fieldGroup := dcgm.GroupHandle{}, dcgm.FieldHandle{}
			group.SetHandle(1)
			fieldGroup.SetHandle(3)
			client.unwatcher.EXPECT().UnwatchFieldGroups([]dcgmprovider.FieldGroupWatch{{FieldGroup: fieldGroup, Group: group}}).Return(tt.err)
			client.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil)
			client.EXPECT().DestroyGroup(group).Return(nil)

			result := &WatchResult{
				client:      client,
				groups:      []dcgm.GroupHandle{group},
				fieldGroups: []dcgm.FieldHandle{fieldGroup},
				bindings:    []WatchBinding{{Group: group, FieldGroup: fieldGroup}},
			}
			result.Close()

			var got []string
			for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var record map[string]any
				require.NoError(t, json.Unmarshal(line, &record))
				assert.Equal(t, "Failed to unwatch fields", record["msg"])
				got = append(got, fmt.Sprint(record[logging.ErrorKey]))
			}
			assert.Equal(t, tt.wantLogs, got)
		})
	}
}

// TestDeviceWatcher_GetDeviceFields checks that, for each entity type, only the counters whose fields DCGM
// reports at that level are watched, plus driver-level fields such as the driver version.
func TestDeviceWatcher_GetDeviceFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	// args selects the counters and entity type used to derive watched field IDs.
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
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID], nil)
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
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID], nil)
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
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID], nil)
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
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID], nil)
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
			name: "Switch Counters",
			args: args{
				counterList: testutils.SampleCounters,
				entityType:  dcgm.FE_SWITCH,
			},
			mockDCGMFunc: func(fieldIDs []dcgm.Short) {
				for _, fieldID := range fieldIDs {
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID], nil)
				}
			},
			want: func() []dcgm.Short {
				return []dcgm.Short{
					testutils.SampleSwitchCurrentTempCounter.FieldID,
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
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID], nil)
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
					mockDCGM.EXPECT().FieldGetByID(fieldID).Return(testutils.SampleFieldIDToFieldMeta[fieldID], nil)
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
			slices.Sort(got.Fields)
			assert.Equal(t, want, got.Fields, "Device fields mismatch")
		})
	}
}

// TestDeviceWatcher_GetDeviceFields_FieldGetByIDError asserts that when
// FieldGetByID returns an error for a field, that field is silently skipped
// and the rest of the counter list is still processed. This exercises the
// error path introduced when go-dcgm's FieldGetByID gained an error return.
func TestDeviceWatcher_GetDeviceFields_FieldGetByIDError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	// Two counters: the first fails FieldGetByID; the second succeeds and
	// should still be returned by GetDeviceFields.
	failingCounter := testutils.SampleGPUTempCounter
	goodCounter := testutils.SampleGPUPowerUsageCounter

	mockDCGM.EXPECT().
		FieldGetByID(failingCounter.FieldID).
		Return(dcgm.FieldMeta{}, fmt.Errorf("field lookup failed"))
	mockDCGM.EXPECT().
		FieldGetByID(goodCounter.FieldID).
		Return(testutils.SampleFieldIDToFieldMeta[goodCounter.FieldID], nil)

	d := &DeviceWatcher{}
	got := d.GetDeviceFields(
		[]counters.Counter{failingCounter, goodCounter},
		dcgm.FE_GPU,
	)

	assert.Equal(t, []dcgm.Short{goodCounter.FieldID}, got.Fields,
		"failing field should be skipped; remaining field should be returned")
}

// testFieldIDs returns distinct field IDs for field-group split tests.
func testFieldIDs(count int) []dcgm.Short {
	fields := make([]dcgm.Short, count)
	for i := range count {
		fields[i] = dcgm.Short(1000 + i)
	}
	return fields
}

// TestDeviceWatcher_WatchDeviceFieldGroupsSplitsOversizedLogicalGroup checks that a group with more
// fields than DCGM allows in one field group is watched as several field groups at the same interval.
func TestDeviceWatcher_WatchDeviceFieldGroupsSplitsOversizedLogicalGroup(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	firstFieldGroup := dcgm.FieldHandle{}
	firstFieldGroup.SetHandle(uintptr(10))
	secondFieldGroup := dcgm.FieldHandle{}
	secondFieldGroup.SetHandle(uintptr(11))

	largeFields := testFieldIDs(129)
	chunkOne := largeFields[:127]
	chunkTwo := largeFields[127:]

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkOne).Return(firstFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(firstFieldGroup, groupHandle, int64(30_000_000), maxKeepAge, int32(maxKeepSamples)).
		Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkTwo).Return(secondFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(secondFieldGroup, groupHandle, int64(30_000_000), maxKeepAge, int32(maxKeepSamples)).
		Return(nil)
	mockDCGM.EXPECT().UnwatchFields(firstFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().UnwatchFields(secondFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(firstFieldGroup).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(secondFieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(groupHandle).Return(nil)

	gotGroups, gotFieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{
			{Name: "large", Fields: largeFields, IntervalMSec: 30000},
		},
		deviceInfo,
	)

	require.NoError(t, err)
	assert.Equal(t, []dcgm.GroupHandle{groupHandle}, gotGroups)
	assert.Equal(t, []dcgm.FieldHandle{firstFieldGroup, secondFieldGroup}, gotFieldGroups)
	require.Len(t, cleanups, 1)
	cleanups[0]()
}

// TestDeviceWatcher_WatchDeviceFieldGroupsSplitSecondChunkFailureCleansUp checks that when the second
// part of a split group fails, the first part is unwatched and the error names the group and part.
func TestDeviceWatcher_WatchDeviceFieldGroupsSplitSecondChunkFailureCleansUp(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	firstFieldGroup := dcgm.FieldHandle{}
	firstFieldGroup.SetHandle(uintptr(10))

	largeFields := testFieldIDs(129)
	chunkOne := largeFields[:127]
	chunkTwo := largeFields[127:]

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkOne).Return(firstFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(firstFieldGroup, groupHandle, int64(30_000_000), maxKeepAge, int32(maxKeepSamples)).
		Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkTwo).Return(dcgm.FieldHandle{}, fmt.Errorf("boom"))
	mockDCGM.EXPECT().UnwatchFields(firstFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(firstFieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(groupHandle).Return(nil)

	gotGroups, gotFieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{
			{Name: "large", Fields: largeFields, IntervalMSec: 30000},
		},
		deviceInfo,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, `logical group "large" chunk 2/2`)
	assert.Nil(t, gotGroups)
	assert.Nil(t, gotFieldGroups)
	assert.Nil(t, cleanups)
}

// TestDeviceWatcher_WatchDeviceFieldGroupsWrapsWatchFailureContext checks that a failed watch returns
// the DCGM error with the group name, part number and handles, after undoing the parts already started.
func TestDeviceWatcher_WatchDeviceFieldGroupsWrapsWatchFailureContext(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	firstFieldGroup := dcgm.FieldHandle{}
	firstFieldGroup.SetHandle(uintptr(10))
	secondFieldGroup := dcgm.FieldHandle{}
	secondFieldGroup.SetHandle(uintptr(11))

	largeFields := testFieldIDs(129)
	chunkOne := largeFields[:127]
	chunkTwo := largeFields[127:]
	watchErr := errors.New("watch failed")

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkOne).Return(firstFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(firstFieldGroup, groupHandle, int64(30_000_000), maxKeepAge, int32(maxKeepSamples)).
		Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkTwo).Return(secondFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(secondFieldGroup, groupHandle, int64(30_000_000), maxKeepAge, int32(maxKeepSamples)).
		Return(watchErr)
	mockDCGM.EXPECT().FieldGetByID(chunkTwo[0]).Return(dcgm.FieldMeta{}, nil)
	mockDCGM.EXPECT().FieldGetByID(chunkTwo[1]).Return(dcgm.FieldMeta{}, nil)
	mockDCGM.EXPECT().UnwatchFields(firstFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(firstFieldGroup).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(secondFieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(groupHandle).Return(nil)

	gotGroups, gotFieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{
			{Name: "large", Fields: largeFields, IntervalMSec: 30000},
		},
		deviceInfo,
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, watchErr)
	assert.ErrorContains(t, err, `logical group "large" chunk 2/2`)
	assert.ErrorContains(t, err, "group_handle=1")
	assert.ErrorContains(t, err, "field_group_handle=11")
	assert.Nil(t, gotGroups)
	assert.Nil(t, gotFieldGroups)
	assert.Nil(t, cleanups)
}

// TestDeviceWatcher_WatchDeviceFieldsRejectsSplitFieldGroups checks that WatchDeviceFields, which
// returns a single field group, fails and returns nothing when the fields had to be split.
func TestDeviceWatcher_WatchDeviceFieldsRejectsSplitFieldGroups(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	deviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	firstFieldGroup := dcgm.FieldHandle{}
	firstFieldGroup.SetHandle(uintptr(10))
	secondFieldGroup := dcgm.FieldHandle{}
	secondFieldGroup.SetHandle(uintptr(11))

	largeFields := testFieldIDs(129)
	chunkOne := largeFields[:127]
	chunkTwo := largeFields[127:]

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(groupHandle, nil)
	mockDCGM.EXPECT().AddEntityToGroup(groupHandle, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkOne).Return(firstFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(firstFieldGroup, groupHandle, int64(30_000_000), maxKeepAge, int32(maxKeepSamples)).
		Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), chunkTwo).Return(secondFieldGroup, nil)
	mockDCGM.EXPECT().
		WatchFieldsWithGroupExNoRefresh(secondFieldGroup, groupHandle, int64(30_000_000), maxKeepAge, int32(maxKeepSamples)).
		Return(nil)
	mockDCGM.EXPECT().UnwatchFields(firstFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().UnwatchFields(secondFieldGroup, groupHandle).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(firstFieldGroup).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(secondFieldGroup).Return(nil)
	mockDCGM.EXPECT().DestroyGroup(groupHandle).Return(nil)

	gotGroups, gotFieldGroup, cleanups, err := NewDeviceWatcher().WatchDeviceFields(
		largeFields,
		deviceInfo,
		30_000_123,
	)

	require.Error(t, err)
	assert.ErrorContains(t, err, "test adapter cannot represent split field groups")
	assert.Nil(t, gotGroups)
	assert.Equal(t, dcgm.FieldHandle{}, gotFieldGroup)
	assert.Nil(t, cleanups)
}

// doNothing satisfies legacy cleanup expectations in tests that exercise a
// failure path where production code already rolled back the resource.
func doNothing() {}

// WatchDeviceFields adapts retired tests to the result-based production API.
// New production callers use Watch and own its complete WatchResult directly.
func (d *DeviceWatcher) WatchDeviceFields(
	fields []dcgm.Short,
	deviceInfo deviceinfo.Provider,
	updateFrequencyUsec int64,
) ([]dcgm.GroupHandle, dcgm.FieldHandle, []func(), error) {
	result, err := d.Watch([]FieldWatchGroup{{
		Name:           "default",
		Fields:         fields,
		IntervalMSec:   updateFrequencyUsec / 1000,
		MaxKeepAge:     maxKeepAge,
		MaxKeepSamples: maxKeepSamples,
	}}, deviceInfo, WatchConfiguredEntities)
	if err != nil {
		return nil, dcgm.FieldHandle{}, nil, err
	}
	if len(result.fieldGroups) > 1 {
		result.Close()
		return nil, dcgm.FieldHandle{}, nil, fmt.Errorf("test adapter cannot represent split field groups")
	}
	if len(result.fieldGroups) == 0 {
		return result.groups, dcgm.FieldHandle{}, []func(){result.Close}, nil
	}
	return result.groups, result.fieldGroups[0], []func(){result.Close}, nil
}

// WatchDeviceFieldGroups adapts retired tests to a configured-entity result.
func (d *DeviceWatcher) WatchDeviceFieldGroups(
	fieldWatchGroups []FieldWatchGroup,
	deviceInfo deviceinfo.Provider,
) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error) {
	result, err := d.Watch(fieldWatchGroups, deviceInfo, WatchConfiguredEntities)
	if err != nil {
		return nil, nil, nil, err
	}
	return result.groups, result.fieldGroups, []func(){result.Close}, nil
}

// WatchDeviceFieldGroupsForComputeInstanceFields adapts retired tests to a
// named compute-instance scope result.
func (d *DeviceWatcher) WatchDeviceFieldGroupsForComputeInstanceFields(
	fieldWatchGroups []FieldWatchGroup,
	deviceInfo deviceinfo.Provider,
) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error) {
	result, err := d.Watch(fieldWatchGroups, deviceInfo, WatchComputeInstanceEntities)
	if err != nil {
		return nil, nil, nil, err
	}
	return result.groups, result.fieldGroups, []func(){result.Close}, nil
}

// WatchDeviceFieldGroupsForParentGPUs adapts retired tests to a named parent-
// GPU scope result.
func (d *DeviceWatcher) WatchDeviceFieldGroupsForParentGPUs(
	fieldWatchGroups []FieldWatchGroup,
	deviceInfo deviceinfo.Provider,
) ([]dcgm.GroupHandle, []dcgm.FieldHandle, []func(), error) {
	result, err := d.Watch(fieldWatchGroups, deviceInfo, WatchUnselectedParentGPUs)
	if err != nil {
		return nil, nil, nil, err
	}
	return result.groups, result.fieldGroups, []func(){result.Close}, nil
}

// createGenericGroupForTest supplies cleanup expected by direct helper tests.
func (d *DeviceWatcher) createGenericGroupForTest(
	deviceInfo deviceinfo.Provider,
	computeInstances bool,
	parentGPUs bool,
) (*dcgm.GroupHandle, func(), error) {
	scope := WatchConfiguredEntities
	if computeInstances {
		scope = WatchComputeInstanceEntities
	}
	if parentGPUs {
		scope = WatchUnselectedParentGPUs
	}
	group, err := d.createGenericGroup(deviceInfo, scope)
	if err != nil || group == nil {
		return group, doNothing, err
	}
	result := &WatchResult{client: d.dcgmClient(), groups: []dcgm.GroupHandle{*group}}
	return group, result.Close, nil
}

// createCPUCoreGroupsForTest supplies cleanup expected by direct helper tests.
func (d *DeviceWatcher) createCPUCoreGroupsForTest(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, []func(), error) {
	groups, err := d.createCPUCoreGroups(deviceInfo)
	if err != nil {
		return nil, nil, err
	}
	result := &WatchResult{client: d.dcgmClient(), groups: groups}
	return groups, []func(){result.Close}, nil
}

// createNVLinkGroupsForTest supplies cleanup expected by direct helper tests.
func (d *DeviceWatcher) createNVLinkGroupsForTest(deviceInfo deviceinfo.Provider) ([]dcgm.GroupHandle, []func(), error) {
	groups, err := d.createNVLinkGroups(deviceInfo)
	if err != nil {
		return nil, nil, err
	}
	result := &WatchResult{client: d.dcgmClient(), groups: groups}
	return groups, []func(){result.Close}, nil
}

// newFieldGroupForTest supplies cleanup expected by the old field-group tests.
func newFieldGroupForTest(deviceFields []dcgm.Short) (dcgm.FieldHandle, func(), error) {
	fieldGroup, err := newFieldGroupWithClient(dcgmprovider.Client(), deviceFields)
	if err != nil {
		return dcgm.FieldHandle{}, doNothing, err
	}
	result := &WatchResult{client: dcgmprovider.Client(), fieldGroups: []dcgm.FieldHandle{fieldGroup}}
	return fieldGroup, result.Close, nil
}
