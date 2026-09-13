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

// Regression coverage for https://github.com/NVIDIA/dcgm-exporter/issues/657:
// a node mixing GPU models (e.g. A30 + RTX2000) failed to start entirely
// because one shared DCGM watch group covered every GPU, and DCP/profiling
// fields fail the whole group's registration if any member GPU doesn't
// support them. These tests cover the per-model partition that replaces
// that shared group for exactly this situation.

import (
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
	// fbUsed is an ordinary field: DCGM reports it as a per-entity
	// NOT_SUPPORTED value rather than failing the watch, so it should never
	// be filtered out.
	fbUsed = dcgm.DCGM_FI_DEV_FB_USED // 252, < dcpFieldsStart

	// fp64Active is the DCP field from the original bug report. It's the one
	// that needs per-model filtering.
	fp64Active = dcgm.DCGM_FI_PROF_FP64_UTIL_RATIO // 1006, DCP range
)

func TestAnyDCPField(t *testing.T) {
	tests := []struct {
		name   string
		groups []FieldWatchGroup
		want   bool
	}{
		{"no groups", nil, false},
		{"only ordinary fields", []FieldWatchGroup{{Fields: []dcgm.Short{fbUsed}}}, false},
		{"one DCP field among ordinary ones", []FieldWatchGroup{{Fields: []dcgm.Short{fbUsed, fp64Active}}}, true},
		{"DCP field in a later group", []FieldWatchGroup{{Fields: []dcgm.Short{fbUsed}}, {Fields: []dcgm.Short{fp64Active}}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, anyDCPField(tt.groups))
		})
	}
}

func TestPartitionMonitoredEntitiesByModel(t *testing.T) {
	a30 := devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		DeviceInfo: dcgm.Device{GPU: 0, Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA A30"}},
	}
	rtx1 := devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1},
		DeviceInfo: dcgm.Device{GPU: 1, Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA RTX2000"}},
	}
	rtx2 := devicemonitoring.Info{
		Entity:     dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 2},
		DeviceInfo: dcgm.Device{GPU: 2, Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA RTX2000"}},
	}

	models, byModel := partitionMonitoredEntitiesByModel([]devicemonitoring.Info{a30, rtx1, rtx2})

	require.Equal(t, []string{"NVIDIA A30", "NVIDIA RTX2000"}, models, "models should be sorted for deterministic group creation order")
	assert.Equal(t, []devicemonitoring.Info{a30}, byModel["NVIDIA A30"])
	assert.Equal(t, []devicemonitoring.Info{rtx1, rtx2}, byModel["NVIDIA RTX2000"])
}

func TestFilterFieldsForModel(t *testing.T) {
	tests := []struct {
		name      string
		fields    []dcgm.Short
		supported map[dcgm.Short]bool
		want      []dcgm.Short
	}{
		{
			name:      "ordinary field always kept even with no DCP support",
			fields:    []dcgm.Short{fbUsed},
			supported: map[dcgm.Short]bool{},
			want:      []dcgm.Short{fbUsed},
		},
		{
			name:      "DCP field dropped when model doesn't support it",
			fields:    []dcgm.Short{fbUsed, fp64Active},
			supported: map[dcgm.Short]bool{},
			want:      []dcgm.Short{fbUsed},
		},
		{
			name:      "DCP field kept when model supports it",
			fields:    []dcgm.Short{fbUsed, fp64Active},
			supported: map[dcgm.Short]bool{fp64Active: true},
			want:      []dcgm.Short{fbUsed, fp64Active},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, filterFieldsForModel(tt.fields, tt.supported))
		})
	}
}

func TestSupportedDCPFields(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return([]dcgm.MetricGroup{
		{FieldIds: []uint{uint(fp64Active)}},
	}, nil)

	supported, err := supportedDCPFields(0)
	require.NoError(t, err)
	assert.Equal(t, map[dcgm.Short]bool{fp64Active: true}, supported)
}

// TestWatchDeviceFieldGroups_PartitionsMixedGPUModels reproduces issue #657
// end to end: a node with an A30 (supports fp64Active) and an RTX2000
// (doesn't) used to register one shared group with fp64Active in it, and
// DCGM rejected the whole watch. This asserts the fix instead: one group
// per model, with fp64Active only watched against the A30's group.
func TestWatchDeviceFieldGroups_PartitionsMixedGPUModels(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()

	a30 := deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 0, Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA A30"}}}
	rtx2000 := deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 1, Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA RTX2000"}}}
	mockDeviceInfo.EXPECT().GPU(uint(0)).Return(a30).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(1)).Return(rtx2000).AnyTimes()
	mockDeviceInfo.EXPECT().GPUs().Return([]deviceinfo.GPUInfo{a30, rtx2000}).AnyTimes()

	a30Group := dcgm.GroupHandle{}
	a30Group.SetHandle(1)
	rtxGroup := dcgm.GroupHandle{}
	rtxGroup.SetHandle(2)
	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(a30Group, nil)
	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(rtxGroup, nil)
	mockDCGM.EXPECT().AddEntityToGroup(a30Group, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().AddEntityToGroup(rtxGroup, dcgm.FE_GPU, uint(1)).Return(nil)

	// A30 supports the DCP field; RTX2000 doesn't report it as supported.
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(0)).Return([]dcgm.MetricGroup{
		{FieldIds: []uint{uint(fp64Active)}},
	}, nil)
	mockDCGM.EXPECT().GetSupportedMetricGroups(uint(1)).Return([]dcgm.MetricGroup{}, nil)

	a30FieldGroup := dcgm.FieldHandle{}
	a30FieldGroup.SetHandle(10)
	rtxFieldGroup := dcgm.FieldHandle{}
	rtxFieldGroup.SetHandle(20)

	// The A30 group watches both fields; the RTX2000 group watches only the
	// ordinary one. If this were still one shared group, the RTX2000's lack
	// of fp64Active support would fail the single WatchFieldsWithGroupEx
	// call for both GPUs - that's the bug.
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed, fp64Active}).Return(a30FieldGroup, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(a30FieldGroup, a30Group, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed}).Return(rtxFieldGroup, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(rtxFieldGroup, rtxGroup, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	watcher := NewDeviceWatcher()
	groups, fieldGroups, cleanups, err := watcher.WatchDeviceFieldGroups(
		[]FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{fbUsed, fp64Active}, IntervalMSec: 1000}},
		mockDeviceInfo,
	)

	require.NoError(t, err)
	assert.ElementsMatch(t, []dcgm.GroupHandle{a30Group, rtxGroup}, groups)
	assert.ElementsMatch(t, []dcgm.FieldHandle{a30FieldGroup, rtxFieldGroup}, fieldGroups)
	require.Len(t, cleanups, 1)

	// Cleanup should tear down both groups without error.
	mockDCGM.EXPECT().UnwatchFields(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockDCGM.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	mockDCGM.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
	cleanups[0]()
}

// TestWatchDeviceFieldGroups_SingleModelUnchanged makes sure the common case
// - one GPU model on the node - still goes through the original single
// shared-group path, not the partitioned one, so there's no behavior change
// for the overwhelming majority of real deployments.
func TestWatchDeviceFieldGroups_SingleModelUnchanged(t *testing.T) {
	realDCGM := dcgmprovider.Client()
	defer dcgmprovider.SetClient(realDCGM)

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	dcgmprovider.SetClient(mockDCGM)

	mockDeviceInfo := mockdeviceinfo.NewMockProvider(ctrl)
	mockDeviceInfo.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
	mockDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	mockDeviceInfo.EXPECT().GPUCount().Return(uint(2)).AnyTimes()

	gpu0 := deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 0, Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA A30"}}}
	gpu1 := deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 1, Identifiers: dcgm.DeviceIdentifiers{Model: "NVIDIA A30"}}}
	mockDeviceInfo.EXPECT().GPU(uint(0)).Return(gpu0).AnyTimes()
	mockDeviceInfo.EXPECT().GPU(uint(1)).Return(gpu1).AnyTimes()
	mockDeviceInfo.EXPECT().GPUs().Return([]deviceinfo.GPUInfo{gpu0, gpu1}).AnyTimes()

	group := dcgm.GroupHandle{}
	group.SetHandle(1)
	mockDCGM.EXPECT().CreateGroup(gomock.Any()).Return(group, nil)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(0)).Return(nil)
	mockDCGM.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(1)).Return(nil)

	// No GetSupportedMetricGroups call expected: a single-model node never
	// enters the partitioned path at all.
	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(10)
	mockDCGM.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{fbUsed, fp64Active}).Return(fieldGroup, nil)
	mockDCGM.EXPECT().WatchFieldsWithGroupEx(fieldGroup, group, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)

	watcher := NewDeviceWatcher()
	groups, fieldGroups, cleanups, err := watcher.WatchDeviceFieldGroups(
		[]FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{fbUsed, fp64Active}, IntervalMSec: 1000}},
		mockDeviceInfo,
	)

	require.NoError(t, err)
	assert.Equal(t, []dcgm.GroupHandle{group}, groups)
	assert.Equal(t, []dcgm.FieldHandle{fieldGroup}, fieldGroups)
	require.Len(t, cleanups, 1)

	mockDCGM.EXPECT().UnwatchFields(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mockDCGM.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	mockDCGM.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
	cleanups[0]()
}
