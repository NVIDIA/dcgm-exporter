/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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
	"errors"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// A DCGM reinitialization must replace cached failures and remove old models.
func TestRefreshDCPSupport_RefreshReplacesFailedAndRemovedModels(t *testing.T) {
	_, mock := withMockDCGM(t)
	expectDevices(mock, a30Model, rtx2000Model)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("busy"))
	mock.EXPECT().GetSupportedMetricGroups(uint(1)).Return(metricGroups(smActive), nil)
	_, _, err := RefreshDCPSupport()
	require.NoError(t, err)
	require.Empty(t, FilterUnwatchedDCPFields(modelDevice(a30Model), []dcgm.Short{fp64Active}))
	expectDevices(mock, a30Model)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)
	groups, models, err := RefreshDCPSupport()
	require.NoError(t, err)
	require.Equal(t, []string{a30Model}, models)
	require.Equal(t, metricGroups(fp64Active), groups)
	require.Equal(t, []dcgm.Short{fp64Active}, FilterUnwatchedDCPFields(modelDevice(a30Model), []dcgm.Short{fp64Active}))
	dcpSupport.RLock()
	_, stale := dcpSupport.byModel[rtx2000Model]
	dcpSupport.RUnlock()
	require.False(t, stale, "refresh must remove models absent from the new topology")
}

// Partitioning must preserve the entity scopes chosen by each watch entry point.
func TestWatchDeviceFieldGroups_MixedMIGEntityScopes(t *testing.T) {
	for _, scope := range []string{"default", "compute", "parent"} {
		t.Run(scope, func(t *testing.T) {
			ctrl, mock := withMockDCGM(t)
			devices := mockdeviceinfo.NewMockProvider(ctrl)
			gpus := []deviceinfo.GPUInfo{
				{DeviceInfo: dcgm.Device{GPU: 0, Identifiers: dcgm.DeviceIdentifiers{Model: a30Model}}, GPUInstances: []deviceinfo.GPUInstanceInfo{{EntityId: 7, ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 21}}}}},
				{DeviceInfo: dcgm.Device{GPU: 1, Identifiers: dcgm.DeviceIdentifiers{Model: rtx2000Model}}},
			}
			devices.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
			devices.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			devices.EXPECT().GPUCount().Return(uint(2)).AnyTimes()
			for i := range gpus {
				devices.EXPECT().GPU(uint(i)).Return(gpus[i]).AnyTimes()
			}
			a30Group, rtxGroup := dcgm.GroupHandle{}, dcgm.GroupHandle{}
			a30Group.SetHandle(1)
			rtxGroup.SetHandle(2)
			a30Fields, rtxFields := dcgm.FieldHandle{}, dcgm.FieldHandle{}
			a30Fields.SetHandle(10)
			rtxFields.SetHandle(20)
			mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)
			mock.EXPECT().CreateGroup(gomock.Any()).Return(a30Group, nil)
			mock.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed, fp64Active}).Return(a30Fields, nil)
			mock.EXPECT().WatchFieldsWithGroupEx(a30Fields, a30Group, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			entity, id := dcgm.FE_GPU_I, uint(7)
			if scope == "compute" {
				entity, id = dcgm.FE_GPU_CI, 21
			}
			if scope == "parent" {
				entity, id = dcgm.FE_GPU, 0
			}
			mock.EXPECT().AddEntityToGroup(a30Group, entity, id).Return(nil)
			if scope != "parent" {
				mock.EXPECT().CreateGroup(gomock.Any()).Return(rtxGroup, nil)
				mock.EXPECT().AddEntityToGroup(rtxGroup, dcgm.FE_GPU, uint(1)).Return(nil)
				mock.EXPECT().GetSupportedMetricGroups(uint(1)).Return(nil, nil)
				mock.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed}).Return(rtxFields, nil)
				mock.EXPECT().WatchFieldsWithGroupEx(rtxFields, rtxGroup, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			}
			mock.EXPECT().UnwatchFields(a30Fields, a30Group).Return(nil)
			mock.EXPECT().FieldGroupDestroy(a30Fields).Return(nil)
			mock.EXPECT().DestroyGroup(a30Group).Return(nil)
			if scope != "parent" {
				mock.EXPECT().UnwatchFields(rtxFields, rtxGroup).Return(nil)
				mock.EXPECT().FieldGroupDestroy(rtxFields).Return(nil)
				mock.EXPECT().DestroyGroup(rtxGroup).Return(nil)
			}
			watcher := NewDeviceWatcher()
			watch := watcher.WatchDeviceFieldGroups
			if scope == "compute" {
				watch = watcher.WatchDeviceFieldGroupsForComputeInstanceFields
			}
			if scope == "parent" {
				watch = watcher.WatchDeviceFieldGroupsForParentGPUs
			}
			_, _, cleanups, err := watch([]FieldWatchGroup{{Name: "mixed-model-regression", Fields: []dcgm.Short{fbUsed, fp64Active}, IntervalMSec: 1000}}, devices)
			require.NoError(t, err)
			for _, cleanup := range cleanups {
				cleanup()
			}
		})
	}
}

// Filtering must happen before chunking, independently for each GPU model.
func TestWatchDeviceFieldGroups_MixedModelsChunkAfterFiltering(t *testing.T) {
	ctrl, mock := withMockDCGM(t)
	devices := mockGPUDeviceInfo(ctrl, a30Model, rtx2000Model)
	fields := make([]dcgm.Short, 129)
	for i := range 128 {
		fields[i] = dcgm.Short(i + 1)
	}
	fields[128] = fp64Active
	mock.EXPECT().CreateGroup(gomock.Any()).Return(dcgm.GroupHandle{}, nil).Times(2)
	mock.EXPECT().AddEntityToGroup(gomock.Any(), dcgm.FE_GPU, gomock.Any()).Return(nil).Times(2)
	mock.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)
	mock.EXPECT().GetSupportedMetricGroups(uint(1)).Return(nil, nil)
	mock.EXPECT().FieldGroupCreate(gomock.Any(), fields[:127]).Return(dcgm.FieldHandle{}, nil).Times(2)
	mock.EXPECT().FieldGroupCreate(gomock.Any(), fields[127:]).Return(dcgm.FieldHandle{}, nil)
	mock.EXPECT().FieldGroupCreate(gomock.Any(), fields[127:128]).Return(dcgm.FieldHandle{}, nil)
	mock.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), int64(1000000), gomock.Any(), gomock.Any()).Return(nil).Times(4)
	expectCleanup(mock)
	groups, fieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{{Name: "mixed-model-regression", Fields: fields, IntervalMSec: 1000}}, devices)
	require.NoError(t, err)
	require.Len(t, groups, 2)
	require.Len(t, fieldGroups, 4)
	for _, cleanup := range cleanups {
		cleanup()
	}
}
