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

// Regression coverage for https://github.com/NVIDIA/dcgm-exporter/issues/657:
// a node mixing GPU models (e.g. A30 + RTX 2000) failed to start because one
// shared DCGM group watched DCP fields that only some models support.

import (
	"errors"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
)

const (
	a30Model     = "NVIDIA A30"
	rtx2000Model = "NVIDIA RTX 2000 Ada Generation"

	// fbUsed is an ordinary field and is never filtered.
	fbUsed = dcgm.DCGM_FI_DEV_FB_USED
	// fp64Active is the DCP field from the original report: A30 only.
	fp64Active = dcgm.DCGM_FI_PROF_PIPE_FP64_ACTIVE
	// smActive is supported by both models.
	smActive = dcgm.DCGM_FI_PROF_SM_ACTIVE
	// grEngineActive stands in for a DCP field only the RTX 2000 supports.
	grEngineActive = dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE
)

func metricGroups(fields ...dcgm.Short) []dcgm.MetricGroup {
	ids := make([]uint, 0, len(fields))
	for _, fieldID := range fields {
		ids = append(ids, uint(fieldID))
	}
	return []dcgm.MetricGroup{{FieldIds: ids}}
}

func modelDevice(model string) dcgm.Device {
	return dcgm.Device{Identifiers: dcgm.DeviceIdentifiers{Model: model}}
}

func withMockDCGM(t *testing.T) (*gomock.Controller, *mockdcgm.MockDCGM) {
	t.Helper()
	realDCGM := dcgmprovider.Client()
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)
	ResetDCPSupport()
	t.Cleanup(func() {
		dcgmprovider.SetClient(realDCGM)
		ResetDCPSupport()
	})
	return ctrl, mockDCGM
}

func expectDevices(mockDCGM *mockdcgm.MockDCGM, models ...string) {
	mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(len(models)), nil)
	for i, model := range models {
		mockDCGM.EXPECT().GetDeviceInfo(uint(i)).Return(
			dcgm.Device{GPU: uint(i), Identifiers: dcgm.DeviceIdentifiers{Model: model}}, nil,
		)
	}
}

func mockGPUDeviceInfo(ctrl *gomock.Controller, models ...string) *mockdeviceinfo.MockProvider {
	gpus := make([]deviceinfo.GPUInfo, 0, len(models))
	for i, model := range models {
		gpus = append(gpus, deviceinfo.GPUInfo{
			DeviceInfo: dcgm.Device{GPU: uint(i), Identifiers: dcgm.DeviceIdentifiers{Model: model}},
		})
	}

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(len(gpus))).AnyTimes()
	mockDeviceInfo.EXPECT().GPUs().Return(gpus).AnyTimes()
	for i := range gpus {
		mockDeviceInfo.EXPECT().GPU(uint(i)).Return(gpus[i]).AnyTimes()
	}
	return mockDeviceInfo
}

func expectCleanup(mockDCGM *mockdcgm.MockDCGM) {
	mockDCGM.EXPECT().UnwatchFields(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockDCGM.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	mockDCGM.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
}

func TestAnyDCPField(t *testing.T) {
	tests := []struct {
		name   string
		groups []FieldWatchGroup
		want   bool
	}{
		{"no groups", nil, false},
		{"only ordinary fields", []FieldWatchGroup{{Fields: []dcgm.Short{fbUsed}}}, false},
		{"DCP field among ordinary ones", []FieldWatchGroup{{Fields: []dcgm.Short{fbUsed, fp64Active}}}, true},
		{"DCP field in a later group", []FieldWatchGroup{{Fields: []dcgm.Short{fbUsed}}, {Fields: []dcgm.Short{fp64Active}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, anyDCPField(tt.groups))
		})
	}
}

func TestPartitionMonitoredEntitiesByModel(t *testing.T) {
	info := func(id uint, model string) devicemonitoring.Info {
		return devicemonitoring.Info{
			Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: id},
			DeviceInfo: dcgm.Device{GPU: id, Identifiers: dcgm.DeviceIdentifiers{Model: model}},
		}
	}
	rtx0, a30, rtx2 := info(0, rtx2000Model), info(1, a30Model), info(2, rtx2000Model)

	models, byModel := partitionMonitoredEntitiesByModel([]devicemonitoring.Info{rtx0, a30, rtx2})

	require.Equal(t, []string{a30Model, rtx2000Model}, models)
	assert.Equal(t, []devicemonitoring.Info{a30}, byModel[a30Model])
	assert.Equal(t, []devicemonitoring.Info{rtx0, rtx2}, byModel[rtx2000Model])
}

func TestFilterFieldsForModel(t *testing.T) {
	tests := []struct {
		name      string
		fields    []dcgm.Short
		supported map[dcgm.Short]bool
		want      []dcgm.Short
	}{
		{"ordinary field kept without DCP support", []dcgm.Short{fbUsed}, nil, []dcgm.Short{fbUsed}},
		{"unsupported DCP field dropped", []dcgm.Short{fp64Active, fbUsed, smActive}, map[dcgm.Short]bool{smActive: true}, []dcgm.Short{fbUsed, smActive}},
		{"supported DCP field kept", []dcgm.Short{fbUsed, fp64Active}, map[dcgm.Short]bool{fp64Active: true}, []dcgm.Short{fbUsed, fp64Active}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, filterFieldsForModel(tt.fields, tt.supported))
		})
	}
}

// TestRefreshDCPSupport_UnionAcrossModels: GPU 0 no longer decides DCP
// support for the whole node; every model contributes, queried once each.
func TestRefreshDCPSupport_UnionAcrossModels(t *testing.T) {
	_, mockDCGM := withMockDCGM(t)
	expectDevices(mockDCGM, a30Model, rtx2000Model, a30Model)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active, smActive), nil).Times(1)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return(metricGroups(smActive, grEngineActive), nil).Times(1)

	groups, models, err := RefreshDCPSupport()

	require.NoError(t, err)
	assert.Equal(t, []string{a30Model, rtx2000Model}, models)
	assert.Equal(t, map[dcgm.Short]bool{fp64Active: true, smActive: true, grEngineActive: true}, dcpFieldSet(groups))
	all := []dcgm.Short{fbUsed, fp64Active, smActive, grEngineActive}
	assert.Equal(t, []dcgm.Short{fbUsed, fp64Active, smActive}, FilterUnwatchedDCPFields(modelDevice(a30Model), all))
	assert.Equal(t, []dcgm.Short{fbUsed, smActive, grEngineActive}, FilterUnwatchedDCPFields(modelDevice(rtx2000Model), all))
}

// TestRefreshDCPSupport_GPU0FailureKeepsOtherModels: a failed query for one
// model disables DCP for that model only, and fails closed for it.
func TestRefreshDCPSupport_GPU0FailureKeepsOtherModels(t *testing.T) {
	_, mockDCGM := withMockDCGM(t)
	expectDevices(mockDCGM, a30Model, rtx2000Model)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("profiling busy"))
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return(metricGroups(smActive), nil)

	groups, models, err := RefreshDCPSupport()

	require.NoError(t, err)
	assert.Equal(t, []string{rtx2000Model}, models)
	assert.Equal(t, map[dcgm.Short]bool{smActive: true}, dcpFieldSet(groups))
	assert.Equal(t, []dcgm.Short{fbUsed}, FilterUnwatchedDCPFields(modelDevice(a30Model), []dcgm.Short{fbUsed, smActive}))
}

func TestRefreshDCPSupport_AllModelsFail(t *testing.T) {
	_, mockDCGM := withMockDCGM(t)
	expectDevices(mockDCGM, a30Model, rtx2000Model)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(nil, errors.New("not supported"))
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return(nil, errors.New("not supported"))

	_, _, err := RefreshDCPSupport()

	require.Error(t, err)
}

func TestRefreshDCPSupport_EnumerationFailureFallsBackToGPU0(t *testing.T) {
	_, mockDCGM := withMockDCGM(t)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)
	mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(0), errors.New("no gpus"))

	groups, models, err := RefreshDCPSupport()

	require.NoError(t, err)
	assert.Equal(t, []string{"unknown"}, models)
	assert.Equal(t, metricGroups(fp64Active), groups)
	// Nothing cached, so scrape filtering passes fields through.
	assert.Equal(t, []dcgm.Short{fbUsed, fp64Active}, FilterUnwatchedDCPFields(modelDevice(a30Model), []dcgm.Short{fbUsed, fp64Active}))
}

// TestWatchDeviceFieldGroups_PartitionsMixedGPUModels reproduces #657: the
// RTX 2000 lacks fp64Active, so it must get its own group without it, and the
// scrape filter must agree with what was watched.
func TestWatchDeviceFieldGroups_PartitionsMixedGPUModels(t *testing.T) {
	ctrl, mockDCGM := withMockDCGM(t)
	deviceInfo := mockGPUDeviceInfo(ctrl, a30Model, rtx2000Model)

	a30Group, rtxGroup := dcgm.GroupHandle{}, dcgm.GroupHandle{}
	a30Group.SetHandle(1)
	rtxGroup.SetHandle(2)
	gomock.InOrder(
		mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(a30Group, nil),
		mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(rtxGroup, nil),
	)
	mockDCGM.EXPECT().AddEntityToGroup(a30Group, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().AddEntityToGroup(rtxGroup, dcgm.FE_GPU, uint(1)).Return(nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return(nil, nil)

	a30FieldGroup, rtxFieldGroup := dcgm.FieldHandle{}, dcgm.FieldHandle{}
	a30FieldGroup.SetHandle(10)
	rtxFieldGroup.SetHandle(20)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed, fp64Active}).Return(a30FieldGroup, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(a30FieldGroup, a30Group, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed}).Return(rtxFieldGroup, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(rtxFieldGroup, rtxGroup, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	groups, fieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{fbUsed, fp64Active}, IntervalMSec: 1000}},
		deviceInfo,
	)

	require.NoError(t, err)
	assert.Equal(t, []dcgm.GroupHandle{a30Group, rtxGroup}, groups)
	assert.Equal(t, []dcgm.FieldHandle{a30FieldGroup, rtxFieldGroup}, fieldGroups)
	assert.Equal(t, []dcgm.Short{fbUsed, fp64Active}, FilterUnwatchedDCPFields(modelDevice(a30Model), []dcgm.Short{fbUsed, fp64Active}))
	assert.Equal(t, []dcgm.Short{fbUsed}, FilterUnwatchedDCPFields(modelDevice(rtx2000Model), []dcgm.Short{fbUsed, fp64Active}))

	// Cleanup unwatches each field group only on the group it was watched on;
	// any cross-model UnwatchFields call would fail as unexpected.
	require.Len(t, cleanups, 1)
	mockDCGM.EXPECT().UnwatchFields(a30FieldGroup, a30Group).Return(nil)
	mockDCGM.EXPECT().UnwatchFields(rtxFieldGroup, rtxGroup).Return(nil)
	mockDCGM.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).Times(2)
	mockDCGM.EXPECT().DestroyGroup(gomock.Any()).Return(nil).Times(2)
	cleanups[0]()
}

// TestWatchDeviceFieldGroups_SkipsModelWithNothingToWatch: a model that
// supports none of the requested fields gets no DCGM group at all.
func TestWatchDeviceFieldGroups_SkipsModelWithNothingToWatch(t *testing.T) {
	ctrl, mockDCGM := withMockDCGM(t)
	deviceInfo := mockGPUDeviceInfo(ctrl, a30Model, rtx2000Model)

	a30Group := dcgm.GroupHandle{}
	a30Group.SetHandle(1)
	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(a30Group, nil).Times(1)
	mockDCGM.EXPECT().AddEntityToGroup(a30Group, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return(nil, nil)

	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(10)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fp64Active}).Return(fieldGroup, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(fieldGroup, a30Group, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	groups, _, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{{Name: "profiling", Fields: []dcgm.Short{fp64Active}, IntervalMSec: 1000}},
		deviceInfo,
	)

	require.NoError(t, err)
	assert.Equal(t, []dcgm.GroupHandle{a30Group}, groups)
	require.Len(t, cleanups, 1)
	expectCleanup(mockDCGM)
	cleanups[0]()
}

// TestWatchDeviceFieldGroups_UnidentifiedGPUsGetSeparateGroups: GPUs whose
// model name DCGM could not report may be different models, so each gets its
// own group and its own support query instead of sharing one.
func TestWatchDeviceFieldGroups_UnidentifiedGPUsGetSeparateGroups(t *testing.T) {
	ctrl, mockDCGM := withMockDCGM(t)
	deviceInfo := mockGPUDeviceInfo(ctrl, "", "")

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(dcgm.GroupHandle{}, nil).Times(2)
	mockDCGM.EXPECT().AddEntityToGroup(gomock.Any(), dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().AddEntityToGroup(gomock.Any(), dcgm.FE_GPU, uint(1)).Return(nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return(nil, nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed, fp64Active}).Return(dcgm.FieldHandle{}, nil)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed}).Return(dcgm.FieldHandle{}, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)

	_, _, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{fbUsed, fp64Active}, IntervalMSec: 1000}},
		deviceInfo,
	)

	require.NoError(t, err)
	all := []dcgm.Short{fbUsed, fp64Active}
	assert.Equal(t, all, FilterUnwatchedDCPFields(dcgm.Device{GPU: 0}, all))
	assert.Equal(t, []dcgm.Short{fbUsed}, FilterUnwatchedDCPFields(dcgm.Device{GPU: 1}, all))
	expectCleanup(mockDCGM)
	for _, cleanup := range cleanups {
		cleanup()
	}
}

func TestGPUModelKey(t *testing.T) {
	assert.Equal(t, a30Model, gpuModelKey(dcgm.Device{GPU: 3, Identifiers: dcgm.DeviceIdentifiers{Model: a30Model}}))
	assert.NotEqual(t, gpuModelKey(dcgm.Device{GPU: 0}), gpuModelKey(dcgm.Device{GPU: 1}))
}

// TestWatchDeviceFieldGroups_QueryErrorFailsClosed: when a model's support
// query fails, both the watch and the scrape drop its DCP fields, and a rearm
// does not requery DCGM.
func TestWatchDeviceFieldGroups_QueryErrorFailsClosed(t *testing.T) {
	ctrl, mockDCGM := withMockDCGM(t)
	deviceInfo := mockGPUDeviceInfo(ctrl, a30Model, rtx2000Model)

	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(dcgm.GroupHandle{}, nil).Times(4)
	mockDCGM.EXPECT().AddEntityToGroup(gomock.Any(), dcgm.FE_GPU, gomock.Any()).Return(nil).Times(4)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil).Times(1)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return(nil, errors.New("dcgm busy")).Times(1)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed, fp64Active}).Return(dcgm.FieldHandle{}, nil).Times(2)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed}).Return(dcgm.FieldHandle{}, nil).Times(2)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(4)
	expectCleanup(mockDCGM)

	watchGroups := []FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{fbUsed, fp64Active}, IntervalMSec: 1000}}
	for range 2 {
		_, _, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(watchGroups, deviceInfo)
		require.NoError(t, err)
		for _, cleanup := range cleanups {
			cleanup()
		}
	}

	assert.Equal(t, []dcgm.Short{fbUsed}, FilterUnwatchedDCPFields(modelDevice(rtx2000Model), []dcgm.Short{fbUsed, fp64Active}))
}

// TestWatchDeviceFieldGroups_SingleModelUsesOneGroup: a single-model node
// still gets one shared group.
func TestWatchDeviceFieldGroups_SingleModelUsesOneGroup(t *testing.T) {
	ctrl, mockDCGM := withMockDCGM(t)
	deviceInfo := mockGPUDeviceInfo(ctrl, a30Model, a30Model)

	group := dcgm.GroupHandle{}
	group.SetHandle(1)
	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(group, nil).Times(1)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(1)).Return(nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return(metricGroups(fp64Active), nil)

	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(10)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed, fp64Active}).Return(fieldGroup, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(fieldGroup, group, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	groups, fieldGroups, cleanups, err := NewDeviceWatcher().WatchDeviceFieldGroups(
		[]FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{fbUsed, fp64Active}, IntervalMSec: 1000}},
		deviceInfo,
	)

	require.NoError(t, err)
	assert.Equal(t, []dcgm.GroupHandle{group}, groups)
	assert.Equal(t, []dcgm.FieldHandle{fieldGroup}, fieldGroups)
	require.Len(t, cleanups, 1)
	expectCleanup(mockDCGM)
	cleanups[0]()
}
