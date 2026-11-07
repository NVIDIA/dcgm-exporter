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

package devicewatchlistmanager

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatcher "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testfixtures"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

var (
	deviceOptionFalse = appconfig.DeviceOptions{
		Flex:       false,
		MajorRange: nil,
		MinorRange: nil,
	}

	deviceOptionTrue = appconfig.DeviceOptions{
		Flex:       true,
		MajorRange: nil,
		MinorRange: nil,
	}

	deviceOptionOther = appconfig.DeviceOptions{
		Flex:       false,
		MajorRange: []int{1},
		MinorRange: []int{-1},
	}

	mockDeviceInfoFunc = func(ctrl *gomock.Controller) *mockdeviceinfo.MockProvider {
		gOpts := appconfig.DeviceOptions{
			Flex: true,
		}

		mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
		mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

		return mockGPUDeviceInfo
	}
)

const noComputeInstancesWarning = "MIG GPU instance has no compute instances; FB metrics will be unavailable if this instance is monitored"

// captureWarnLogs redirects the default logger to a buffer for warning assertions.
func captureWarnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	return &logs
}

// warningRecords returns structured warning records with the requested message.
func warningRecords(t *testing.T, logs *bytes.Buffer, message string) []map[string]any {
	t.Helper()

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}

		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		if record["msg"] == message {
			records = append(records, record)
		}
	}

	return records
}

// TestNewWatchList checks that a new list watches its device fields followed by its label fields, is empty
// without device fields, and passes a watcher error back from Watch. SetDeviceFields replaces the device
// fields and the watched fields.
func TestNewWatchList(t *testing.T) {
	ctrl := gomock.NewController(t)

	// args supplies device metadata, requested fields, and collection interval for watch-list construction.
	type args struct {
		deviceInfo        deviceinfo.Provider
		deviceFields      []dcgm.Short
		labelDeviceFields []dcgm.Short
		newDeviceFields   []dcgm.Short
		collectInterval   int64
	}
	tests := []struct {
		name         string
		args         args
		wantWatch    []dcgm.Short
		wantEmpty    bool
		wantWatchErr bool
	}{
		{
			name: "New Watch List",
			args: args{
				deviceInfo:        mockDeviceInfoFunc(ctrl),
				deviceFields:      []dcgm.Short{1, 2, 3, 4},
				labelDeviceFields: []dcgm.Short{100, 101},
				collectInterval:   int64(1),
			},
			wantWatch:    []dcgm.Short{1, 2, 3, 4, 100, 101},
			wantEmpty:    false,
			wantWatchErr: false,
		},
		{
			name: "Empty Device Fields",
			args: args{
				deviceInfo:        mockDeviceInfoFunc(ctrl),
				deviceFields:      nil,
				labelDeviceFields: []dcgm.Short{100, 101},
				collectInterval:   int64(1),
			},
			wantWatch:    nil,
			wantEmpty:    true,
			wantWatchErr: false,
		},
		{
			name: "SetDevice Fields",
			args: args{
				deviceInfo:        mockDeviceInfoFunc(ctrl),
				deviceFields:      []dcgm.Short{1, 2, 3, 4},
				labelDeviceFields: []dcgm.Short{100, 101},
				newDeviceFields:   []dcgm.Short{1000},
				collectInterval:   int64(1),
			},
			wantWatch:    []dcgm.Short{1, 2, 3, 4, 100, 101},
			wantEmpty:    false,
			wantWatchErr: false,
		},
		{
			name: "Watch Error",
			args: args{
				deviceInfo:        mockDeviceInfoFunc(ctrl),
				deviceFields:      nil,
				labelDeviceFields: []dcgm.Short{100, 101},
				collectInterval:   int64(1),
			},
			wantWatch:    nil,
			wantEmpty:    true,
			wantWatchErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

			var err error
			if tt.wantWatchErr {
				err = fmt.Errorf("some error")
			}

			mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(
				defaultFieldWatchGroups(tt.wantWatch, tt.args.collectInterval),
				tt.args.deviceInfo,
			).Return([]dcgm.GroupHandle{}, []dcgm.FieldHandle{}, []func(){}, err)

			got := NewWatchList(tt.args.deviceInfo, tt.args.deviceFields, tt.args.labelDeviceFields, mockDeviceWatcher,
				tt.args.collectInterval)

			assert.Equal(t, tt.args.deviceInfo, got.DeviceInfo(), "Unexpected DeviceInfo() output.")
			assert.Equal(t, tt.args.deviceFields, got.DeviceFields(), "Unexpected DeviceFields() output.")
			assert.Equal(t, tt.args.labelDeviceFields, got.LabelDeviceFields(),
				"Unexpected LabelDeviceFields() output.")
			assert.Equal(t, tt.wantWatch, got.watchFields, "Unexpected watchFields output.")
			assert.Equal(t, tt.wantEmpty, got.IsEmpty(), "Unexpected IsEmpty() output.")

			result, err := got.Watch()
			if !tt.wantWatchErr {
				assert.Nil(t, err, "expected no error")
			} else {
				assert.NotNil(t, err, "expected error")
			}
			assert.Empty(t, result.Bindings(), "Unexpected watch bindings.")

			if tt.args.newDeviceFields != nil {
				got.SetDeviceFields(tt.args.newDeviceFields)
				assert.Equal(t, tt.args.newDeviceFields, got.DeviceFields(),
					"Unexpected DeviceFields() output after SetDeviceFields().")
				assert.NotEqual(t, tt.args.deviceFields, got.DeviceFields(),
					"Unexpected DeviceFields() output after SetDeviceFields().")
				assert.Equal(t, []dcgm.Short{1000, 100, 101}, got.watchFields,
					"Unexpected watchFields output after SetDeviceFields().")
			}
		})
	}
}

// TestWatchList_SetDeviceFieldsClearsWatchFieldsForLabelOnlyList checks that setting no device fields keeps the label
// fields but leaves nothing to watch, so the list counts as empty.
func TestWatchList_SetDeviceFieldsClearsWatchFieldsForLabelOnlyList(t *testing.T) {
	ctrl := gomock.NewController(t)
	deviceInfo := mockDeviceInfoFunc(ctrl)
	got := NewWatchList(
		deviceInfo,
		[]dcgm.Short{1},
		[]dcgm.Short{100, 101},
		mockdevicewatcher.NewMockWatcher(ctrl),
		1,
	)

	got.SetDeviceFields(nil)

	assert.Empty(t, got.DeviceFields())
	assert.Equal(t, []dcgm.Short{100, 101}, got.LabelDeviceFields())
	assert.Empty(t, got.watchFields)
	assert.True(t, got.IsEmpty())
}

// TestWatchListKeepsFieldsAndEntitiesAtMatchingScopes checks that Watch sends GPU-level fields to the GPU watch,
// compute instance fields to the compute instance watch and fields at both scopes to the
// parent GPU watch, returns all three bindings, and drops the extra watches once the fields are reset.
func TestWatchListKeepsFieldsAndEntitiesAtMatchingScopes(t *testing.T) {
	groupName := t.Name()
	ctrl := gomock.NewController(t)
	deviceInfo := mockDeviceInfoFunc(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	deviceFields := []dcgm.Short{
		dcgm.DCGM_FI_DEV_GPU_TEMP,
		dcgm.DCGM_FI_DEV_FB_USED,
		dcgm.DCGM_FI_DEV_XID_ERRORS,
	}
	labelFields := []dcgm.Short{dcgm.DCGM_FI_DEV_UUID}
	computeInstanceFields := []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED, dcgm.DCGM_FI_DEV_XID_ERRORS}
	watchGroups := []devicewatcher.FieldWatchGroup{{
		Name:         groupName,
		Fields:       append(append([]dcgm.Short{}, deviceFields...), labelFields...),
		IntervalMSec: 1,
	}}
	baseGroup := dcgm.GroupHandle{}
	baseGroup.SetHandle(uintptr(1))
	computeGroup := dcgm.GroupHandle{}
	computeGroup.SetHandle(uintptr(2))
	parentGroup := dcgm.GroupHandle{}
	parentGroup.SetHandle(uintptr(3))
	baseFieldGroup := dcgm.FieldHandle{}
	baseFieldGroup.SetHandle(uintptr(4))
	computeFieldGroup := dcgm.FieldHandle{}
	computeFieldGroup.SetHandle(uintptr(5))
	parentFieldGroup := dcgm.FieldHandle{}
	parentFieldGroup.SetHandle(uintptr(6))
	watcher.EXPECT().WatchDeviceFieldGroups([]devicewatcher.FieldWatchGroup{{
		Name: groupName, Fields: []dcgm.Short{
			dcgm.DCGM_FI_DEV_GPU_TEMP,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FI_DEV_UUID,
		}, IntervalMSec: 1,
	}}, deviceInfo).Return([]dcgm.GroupHandle{baseGroup}, []dcgm.FieldHandle{baseFieldGroup}, nil, nil)
	watcher.EXPECT().WatchDeviceFieldGroupsForComputeInstanceFields(
		[]devicewatcher.FieldWatchGroup{{Name: groupName, Fields: []dcgm.Short{
			dcgm.DCGM_FI_DEV_FB_USED,
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FI_DEV_UUID,
		}, IntervalMSec: 1}},
		deviceInfo,
	).Return([]dcgm.GroupHandle{computeGroup}, []dcgm.FieldHandle{computeFieldGroup}, nil, nil)
	watcher.EXPECT().WatchDeviceFieldGroupsForParentGPUs([]devicewatcher.FieldWatchGroup{{
		Name:         groupName,
		Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS, dcgm.DCGM_FI_DEV_UUID},
		IntervalMSec: 1,
	}}, deviceInfo).Return([]dcgm.GroupHandle{parentGroup}, []dcgm.FieldHandle{parentFieldGroup}, nil, nil)

	watchList := newWatchListWithGroups(
		deviceInfo,
		deviceFields,
		labelFields,
		computeInstanceFields,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		nil,
		watchGroups,
		watcher,
		1,
		appconfig.DefaultWatchMaxKeepAge.Seconds(),
		int32(appconfig.DefaultWatchMaxSamples),
	)

	returnedFields := watchList.ComputeInstanceFields()
	returnedFields[0] = dcgm.DCGM_FI_DEV_GPU_TEMP
	assert.Equal(t, computeInstanceFields, watchList.ComputeInstanceFields())
	result, err := watchList.Watch()
	assert.NoError(t, err)
	assert.Equal(t, []devicewatcher.WatchBinding{
		{Group: baseGroup, FieldGroup: baseFieldGroup},
		{Group: computeGroup, FieldGroup: computeFieldGroup},
		{Group: parentGroup, FieldGroup: parentFieldGroup},
	}, result.Bindings())
}

// TestWatchListReturnsAllCleanupsWhenParentGPUWatchFails checks that when the parent GPU watch fails, Watch
// returns the error and no result, and runs the cleanups of the GPU, compute instance and parent watches.
func TestWatchListReturnsAllCleanupsWhenParentGPUWatchFails(t *testing.T) {
	groupName := t.Name()
	ctrl := gomock.NewController(t)
	deviceInfo := mockDeviceInfoFunc(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	watchGroups := []devicewatcher.FieldWatchGroup{{
		Name:         groupName,
		Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		IntervalMSec: 1,
	}}
	baseCleaned := false
	computeCleaned := false
	parentCleaned := false
	watcher.EXPECT().WatchDeviceFieldGroups(watchGroups, deviceInfo).Return(
		nil,
		nil,
		[]func(){func() { baseCleaned = true }},
		nil,
	)
	watcher.EXPECT().WatchDeviceFieldGroupsForComputeInstanceFields(watchGroups, deviceInfo).Return(
		nil,
		nil,
		[]func(){func() { computeCleaned = true }},
		nil,
	)
	watcher.EXPECT().WatchDeviceFieldGroupsForParentGPUs(gomock.Any(), deviceInfo).Return(
		nil,
		nil,
		[]func(){func() { parentCleaned = true }},
		assert.AnError,
	)

	watchList := newWatchListWithGroups(
		deviceInfo,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		nil,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		[]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		nil,
		watchGroups,
		watcher,
		1,
		appconfig.DefaultWatchMaxKeepAge.Seconds(),
		int32(appconfig.DefaultWatchMaxSamples),
	)

	result, err := watchList.Watch()

	require.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, result)
	assert.True(t, baseCleaned)
	assert.True(t, computeCleaned)
	assert.True(t, parentCleaned)
}

// TestWatchListWithOnlyComputeInstanceFieldsDoesNotCreateBaseWatch checks that a list whose only field is a
// compute instance field starts just the compute instance watch and no plain GPU watch.
func TestWatchListWithOnlyComputeInstanceFieldsDoesNotCreateBaseWatch(t *testing.T) {
	groupName := t.Name()
	ctrl := gomock.NewController(t)
	deviceInfo := mockDeviceInfoFunc(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	fields := []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED}
	watchGroups := []devicewatcher.FieldWatchGroup{{
		Name:           groupName,
		Fields:         fields,
		IntervalMSec:   5000,
		MaxKeepAge:     30,
		MaxKeepSamples: 2,
	}}
	watcher.EXPECT().WatchDeviceFieldGroupsForComputeInstanceFields(watchGroups, deviceInfo).Return(nil, nil, nil, nil)

	watchList := newWatchListWithGroups(
		deviceInfo,
		fields,
		nil,
		fields,
		nil,
		nil,
		watchGroups,
		watcher,
		1,
		appconfig.DefaultWatchMaxKeepAge.Seconds(),
		int32(appconfig.DefaultWatchMaxSamples),
	)

	_, err := watchList.Watch()

	require.NoError(t, err)
}

// TestWatchListReturnsAllCleanupsWhenComputeInstanceWatchFails checks that when the compute instance watch
// fails, Watch returns the error and no result, and runs the cleanups of both watches already started.
func TestWatchListReturnsAllCleanupsWhenComputeInstanceWatchFails(t *testing.T) {
	groupName := t.Name()
	ctrl := gomock.NewController(t)
	deviceInfo := mockDeviceInfoFunc(ctrl)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	deviceFields := []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DEV_FB_USED}
	watchGroups := []devicewatcher.FieldWatchGroup{{Name: groupName, Fields: deviceFields, IntervalMSec: 1}}
	baseCleaned := false
	computeCleaned := false
	watcher.EXPECT().WatchDeviceFieldGroups([]devicewatcher.FieldWatchGroup{{
		Name: groupName, Fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP}, IntervalMSec: 1,
	}}, deviceInfo).Return(nil, nil, []func(){func() { baseCleaned = true }}, nil)
	watcher.EXPECT().WatchDeviceFieldGroupsForComputeInstanceFields([]devicewatcher.FieldWatchGroup{{
		Name: groupName, Fields: []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED}, IntervalMSec: 1,
	}}, deviceInfo).Return(nil, nil, []func(){func() { computeCleaned = true }}, assert.AnError)

	watchList := newWatchListWithGroups(
		deviceInfo,
		deviceFields,
		nil,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED},
		nil,
		nil,
		watchGroups,
		watcher,
		1,
		appconfig.DefaultWatchMaxKeepAge.Seconds(),
		int32(appconfig.DefaultWatchMaxSamples),
	)

	result, err := watchList.Watch()

	require.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, result)
	assert.True(t, baseCleaned)
	assert.True(t, computeCleaned)
}

// TestWatchListExplainsRefusedFieldsAcrossScopes checks that a refusal in the
// first scope names refused fields from every scope, including compute-instance
// fields that DCGM never got to watch.
func TestWatchListExplainsRefusedFieldsAcrossScopes(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("OTLP scopes %t", scoped), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			deviceInfo := mockDeviceInfoFunc(ctrl)
			const smClockHertz, memoryClockHertz = dcgm.Short(1603), dcgm.Short(1604)
			deviceFields := []dcgm.Short{smClockHertz, memoryClockHertz}
			watchGroups := []devicewatcher.FieldWatchGroup{{Name: "default", Fields: deviceFields, IntervalMSec: 1}}
			refusal := errors.New("error creating DCGM fields group: Bad parameter passed to function")

			client := mockdcgm.NewMockDCGM(ctrl)
			group := dcgm.GroupHandle{}
			group.SetHandle(uintptr(1))
			client.EXPECT().CreateGroup(gomock.Any()).Return(group, nil)
			client.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, gomock.Any()).Return(nil).AnyTimes()
			client.EXPECT().DestroyGroup(group).Return(nil)
			client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{smClockHertz}).Return(dcgm.FieldHandle{}, refusal).Times(2)
			client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{memoryClockHertz}).Return(dcgm.FieldHandle{}, refusal)
			client.EXPECT().FieldGetByID(smClockHertz).Return(dcgm.FieldMeta{Tag: "sm_clock_hertz"}, nil)
			client.EXPECT().FieldGetByID(memoryClockHertz).Return(dcgm.FieldMeta{Tag: "memory_clock_hertz"}, nil)

			watchList := newWatchListWithGroups(
				deviceInfo,
				deviceFields,
				nil,
				[]dcgm.Short{memoryClockHertz},
				nil,
				nil,
				watchGroups,
				devicewatcher.NewDeviceWatcherWithClient(client),
				1,
				appconfig.DefaultWatchMaxKeepAge.Seconds(),
				int32(appconfig.DefaultWatchMaxSamples),
			)

			if scoped {
				watchList.scopedWatchGroups = []scopedWatchGroups{
					{scope: devicewatcher.WatchSelectedGPUs, groups: []devicewatcher.FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{smClockHertz}, IntervalMSec: 1}}},
					{scope: devicewatcher.WatchOnlyComputeInstances, groups: []devicewatcher.FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{memoryClockHertz}, IntervalMSec: 1}}},
				}
			}

			result, err := watchList.Watch()

			require.ErrorIs(t, err, refusal)
			assert.Nil(t, result)
			assert.ErrorContains(t, err, `refused these fields: watch group "default": `+
				"sm_clock_hertz (field ID 1603), memory_clock_hertz (field ID 1604)")
		})
	}
}

// TestWatchResult_BindingsReturnsCopy checks that changing the slice returned by Bindings does not change the
// bindings stored in the result.
func TestWatchResult_BindingsReturnsCopy(t *testing.T) {
	original := dcgm.FieldHandle{}
	original.SetHandle(uintptr(1))
	replacement := dcgm.FieldHandle{}
	replacement.SetHandle(uintptr(2))
	result := devicewatcher.ResultFromBindings([]devicewatcher.WatchBinding{{
		Group:      dcgm.GroupHandle{},
		FieldGroup: original,
	}}, nil)

	bindings := result.Bindings()
	bindings[0].FieldGroup = replacement

	assert.Equal(t, []devicewatcher.WatchBinding{{FieldGroup: original}}, result.Bindings())
}

func TestWatchList_FieldReplacementRestoresConfiguredScope(t *testing.T) {
	for _, tt := range []struct {
		name        string
		setFields   func(*WatchList, []dcgm.Short)
		watchFields []dcgm.Short
	}{
		{"with labels", (*WatchList).SetDeviceFields, []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DEV_UUID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			info := testutils.MockGPUDeviceInfo(ctrl, 1, map[int][]deviceinfo.GPUInstanceInfo{0: {
				{EntityId: 7, ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 21}}},
			}})
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			client := mockdcgm.NewMockDCGM(ctrl)
			watcher := devicewatcher.NewDeviceWatcherWithClient(client)
			list := NewWatchList(info, nil, []dcgm.Short{dcgm.DCGM_FI_DEV_UUID}, watcher, 1)
			list.SetDeviceFieldsForAllGPUScopes([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})
			require.Len(t, list.MonitoredEntities(), 3)

			tt.setFields(list, []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP})

			entities := list.MonitoredEntities()
			require.Len(t, entities, 1)
			assert.Equal(t, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7}, entities[0].Entity)
			assert.Equal(t, []dcgm.Short{dcgm.DCGM_FI_DEV_UUID}, list.LabelDeviceFields())
			assert.Equal(t, tt.watchFields, list.watchFields)
			group, fieldGroup := dcgm.GroupHandle{}, dcgm.FieldHandle{}
			group.SetHandle(uintptr(1))
			fieldGroup.SetHandle(uintptr(2))
			client.EXPECT().CreateGroup(gomock.Any()).Return(group, nil)
			client.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU_I, uint(7)).Return(nil)
			client.EXPECT().FieldGroupCreate(gomock.Any(), tt.watchFields).Return(fieldGroup, nil)
			client.EXPECT().WatchFieldsWithGroupExNoRefresh(fieldGroup, group, int64(1000), float64(600), int32(0)).Return(nil)
			client.EXPECT().UnwatchFields(fieldGroup, group).Return(nil)
			client.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil)
			client.EXPECT().DestroyGroup(group).Return(nil)
			result, err := list.Watch()
			require.NoError(t, err)
			t.Cleanup(result.Close)
		})
	}
}

// TestNewWatchListManager checks that a new manager copies the counters, the GPU, switch and CPU device
// options and the fake GPU setting from the config, and starts with no entity watch lists.
func TestNewWatchListManager(t *testing.T) {
	// args provides the counters and device options used to initialize a watch-list manager.
	type args struct {
		counters counters.CounterList
		config   *appconfig.Config
	}
	tests := []struct {
		name string
		args args
		want *WatchListManager
	}{
		{
			name: "New Watch List Manager",
			args: args{
				counters: testutils.SampleCounters,
				config: &appconfig.Config{
					GPUDeviceOptions:    deviceOptionFalse,
					SwitchDeviceOptions: deviceOptionTrue,
					CPUDeviceOptions:    deviceOptionOther,
					UseFakeGPUs:         false,
				},
			},
			want: &WatchListManager{
				entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
				counters:         testutils.SampleCounters,
				gOpts:            deviceOptionFalse,
				sOpts:            deviceOptionTrue,
				cOpts:            deviceOptionOther,
				useFakeGPUs:      false,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, NewWatchListManager(tt.args.counters, tt.args.config),
				"Unexpected NewWatchListManager output")
		})
	}
}

// TestValidateWatchGroups checks which watch group patterns are accepted: a pattern must match at least one
// configured field, may not match a field another group matches, and must be a valid wildcard or regex;
// errors name the group or override path.
func TestValidateWatchGroups(t *testing.T) {
	tests := []struct {
		name        string
		counters    counters.CounterList
		watchGroups []appconfig.WatchGroup
		wantErr     string
		exactErr    bool
	}{
		{
			name: "current XID field matches previous name",
			counters: counters.CounterList{
				{FieldID: dcgm.DCGM_FI_DEV_XID_ERROR, FieldName: "DCGM_FI_DEV_XID_ERROR"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "events", Interval: 1000, Fields: []string{"DCGM_FI_DEV_XID_ERRORS"}},
			},
		},
		{
			name: "previous XID field matches current name",
			counters: counters.CounterList{
				{FieldID: dcgm.DCGM_FI_DEV_XID_ERROR, FieldName: "DCGM_FI_DEV_XID_ERRORS"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "events", Interval: 1000, Fields: []string{"DCGM_FI_DEV_XID_ERROR"}},
			},
		},
		{
			name: "XID wildcard matches the canonical backing field",
			counters: counters.CounterList{
				{FieldID: dcgm.DCGM_FI_DEV_XID_ERROR, FieldName: "DCGM_FI_DEV_XID_ERROR"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "events", Interval: 1000, Fields: []string{"DCGM_FI_DEV_XID_ERRORS*"}},
			},
		},
		{
			name: "XID aliases cannot select the same field in separate groups",
			counters: counters.CounterList{
				{FieldID: dcgm.DCGM_FI_DEV_XID_ERROR, FieldName: "DCGM_FI_DEV_XID_ERROR"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "first", Interval: 1000, Fields: []string{"DCGM_FI_DEV_XID_ERROR"}},
				{Name: "second", Interval: 1000, Fields: []string{"DCGM_FI_DEV_XID_ERRORS"}},
			},
			wantErr: "matches multiple watch groups",
		},
		{
			name: "XID wildcard and canonical name cannot select the same field in separate groups",
			counters: counters.CounterList{
				{FieldID: dcgm.DCGM_FI_DEV_XID_ERROR, FieldName: "DCGM_FI_DEV_XID_ERROR"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "first", Interval: 1000, Fields: []string{"DCGM_FI_DEV_XID_ERROR"}},
				{Name: "second", Interval: 1000, Fields: []string{"DCGM_FI_DEV_XID_ERRORS*"}},
			},
			wantErr: "matches multiple watch groups",
		},
		{
			name: "wildcard matches configured field",
			watchGroups: []appconfig.WatchGroup{
				{Name: "power", Interval: 60000, Fields: []string{"DCGM_FI_DEV_POWER_*"}},
			},
		},
		{
			name: "overlap rejected",
			watchGroups: []appconfig.WatchGroup{
				{Name: "gpu", Interval: 60000, Fields: []string{"DCGM_FI_DEV_GPU_*"}},
				{Name: "temp", Interval: 120000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
			},
			wantErr: "matches multiple watch groups",
		},
		{
			name: "empty match rejected",
			watchGroups: []appconfig.WatchGroup{
				{Name: "missing", Interval: 60000, Fields: []string{"DCGM_FI_DEV_NOT_CONFIGURED"}},
			},
			wantErr: "matched no configured fields",
		},
		{
			name: "invalid wildcard rejected",
			watchGroups: []appconfig.WatchGroup{
				{Name: "bad", Interval: 60000, Fields: []string{"DCGM_FI_DEV_GPU_["}},
			},
			wantErr: "invalid field pattern",
		},
		{
			name: "invalid wildcard after match rejected",
			watchGroups: []appconfig.WatchGroup{
				{Name: "bad", Interval: 60000, Fields: []string{"DCGM_FI_DEV_*", "DCGM_FI_DEV_GPU_["}},
			},
			wantErr: "invalid field pattern",
		},
		{
			name: "extended regex matches configured field",
			watchGroups: []appconfig.WatchGroup{
				{Name: "power", Interval: 60000, Fields: []string{"/DCGM_FI_DEV_POWER_(USAGE|LIMIT)/"}, ExtendedMatch: true},
			},
		},
		{
			name: "extended regex matches nothing",
			watchGroups: []appconfig.WatchGroup{
				{Name: "missing", Interval: 60000, Fields: []string{"/DCGM_FI_DEV_NOT_.*/"}, ExtendedMatch: true},
			},
			wantErr: `watch group "missing" matched no configured fields`,
		},
		{
			name: "extended regex overlaps another group",
			counters: counters.CounterList{
				{FieldID: dcgm.Short(1), FieldName: "FIELD_A"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "first", Interval: 60000, Fields: []string{"FIELD_A"}, ExtendedMatch: true},
				{Name: "second", Interval: 120000, Fields: []string{"/FIELD_.*/"}, ExtendedMatch: true},
			},
			wantErr:  "field FIELD_A matches multiple watch groups: first and second",
			exactErr: true,
		},
		{
			name: "extended regex must match the whole name",
			counters: counters.CounterList{
				{FieldID: dcgm.Short(1), FieldName: "FIELD_A"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "part", Interval: 60000, Fields: []string{"/FIELD/"}, ExtendedMatch: true},
			},
			wantErr: `watch group "part" matched no configured fields`,
		},
		{
			name: "invalid regex rejected with the item index",
			watchGroups: []appconfig.WatchGroup{
				{Name: "bad", Interval: 60000, Fields: []string{"FIELD_A", "/(/"}, ExtendedMatch: true},
			},
			wantErr: "invalid field pattern at index 1: invalid regular expression",
		},
		{
			name: "version 1 group keeps slashes as ordinary characters",
			watchGroups: []appconfig.WatchGroup{
				{Name: "v1", Interval: 60000, Fields: []string{"/DCGM_FI_DEV_.*/"}},
			},
			wantErr: `watch group "v1" matched no configured fields`,
		},
		{
			name: "overlap names the YAML path of a named override",
			counters: counters.CounterList{
				{FieldID: dcgm.Short(1), FieldName: "FIELD_A"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "first", Path: "metrics[0].overrides[0]", Interval: 60000, Fields: []string{"FIELD_A"}, ExtendedMatch: true},
				{Name: "metrics[0].overrides[1]", Path: "metrics[0].overrides[1]", Interval: 120000, Fields: []string{"/FIELD_.*/"}, ExtendedMatch: true},
			},
			wantErr:  "field FIELD_A matches multiple overrides: first (metrics[0].overrides[0]) and metrics[0].overrides[1]",
			exactErr: true,
		},
		{
			name: "matches nothing names the YAML path",
			watchGroups: []appconfig.WatchGroup{
				{Name: "none", Path: "metrics[0].overrides[2]", Interval: 60000, Fields: []string{"/NOT_.*/"}, ExtendedMatch: true},
			},
			wantErr:  `override "none" (metrics[0].overrides[2]) matched no configured fields`,
			exactErr: true,
		},
		{
			name: "overlap error is deterministic",
			counters: counters.CounterList{
				{FieldID: dcgm.Short(2), FieldName: "FIELD_B"},
				{FieldID: dcgm.Short(1), FieldName: "FIELD_A"},
			},
			watchGroups: []appconfig.WatchGroup{
				{Name: "first", Interval: 60000, Fields: []string{"FIELD_*"}},
				{Name: "second", Interval: 120000, Fields: []string{"FIELD_*"}},
			},
			wantErr:  "field FIELD_A matches multiple watch groups: first and second",
			exactErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counterList := testutils.SampleCounters
			if tt.counters != nil {
				counterList = tt.counters
			}
			err := ValidateWatchGroups(counterList, tt.watchGroups)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tt.exactErr {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// FuzzValidateWatchGroups checks with random field names and patterns that validation gives the same result
// twice, and that groups it accepts split the fields the same way each time with every field in one group.
func FuzzValidateWatchGroups(f *testing.F) {
	seeds := [][4]string{
		{"FIELD_A", "FIELD_B", "FIELD_A", "FIELD_B"},
		{"DCGM_FI_DEV_GPU_TEMP", "DCGM_FI_DEV_POWER_USAGE", "DCGM_FI_DEV_GPU_*", "DCGM_FI_DEV_POWER_*"},
		{"FIELD_A", "FIELD_B", "FIELD_*", "FIELD_*"},
		{"FIELD_A", "FIELD_B", "FIELD_A", "["},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1], seed[2], seed[3])
	}

	f.Fuzz(func(t *testing.T, fieldA, fieldB, patternA, patternB string) {
		fieldIDs := []dcgm.Short{1, 2}
		counterList := counters.CounterList{
			{FieldID: fieldIDs[0], FieldName: fieldA},
			{FieldID: fieldIDs[1], FieldName: fieldB},
		}
		watchGroups := []appconfig.WatchGroup{
			{Name: "first", Interval: 60000, Fields: []string{patternA}},
			{Name: "second", Interval: 120000, Fields: []string{patternB}},
		}

		firstErr := ValidateWatchGroups(counterList, watchGroups)
		secondErr := ValidateWatchGroups(counterList, watchGroups)
		if (firstErr == nil) != (secondErr == nil) || firstErr != nil && firstErr.Error() != secondErr.Error() {
			t.Fatalf("validation is not deterministic: first=%v second=%v", firstErr, secondErr)
		}
		if firstErr != nil {
			return
		}

		manager := &WatchListManager{counters: counterList, watchGroups: watchGroups}
		first, err := manager.partitionFieldWatchGroups(fieldIDs, 30000)
		if err != nil {
			t.Fatalf("validated watch groups could not be partitioned: %v", err)
		}
		second, err := manager.partitionFieldWatchGroups(fieldIDs, 30000)
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatalf("partitioning is not deterministic: first=%#v second=%#v err=%v", first, second, err)
		}

		assignments := make(map[dcgm.Short]int, len(fieldIDs))
		for _, group := range first {
			for _, fieldID := range group.Fields {
				assignments[fieldID]++
			}
		}
		for _, fieldID := range fieldIDs {
			if assignments[fieldID] != 1 {
				t.Fatalf("field %d was assigned %d times", fieldID, assignments[fieldID])
			}
		}
	})
}

// TestWatchListManagerPartitionFieldWatchGroups checks that each field goes to the watch group whose pattern
// matches it, with that group's interval, and that unmatched fields go to a default group at the default interval.
func TestWatchListManagerPartitionFieldWatchGroups(t *testing.T) {
	manager := &WatchListManager{
		counters: testutils.SampleCounters,
		watchGroups: []appconfig.WatchGroup{
			{Name: "power", Interval: 60000, Fields: []string{"DCGM_FI_DEV_POWER_*"}},
			{Name: "temperature", Interval: 120000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
		},
	}

	got, err := manager.partitionFieldWatchGroups(testutils.SampleGPUFieldIDs, 30000)

	require.NoError(t, err)
	assert.Equal(t, []devicewatcher.FieldWatchGroup{
		{
			Name:           "power",
			Fields:         []dcgm.Short{testutils.SampleGPUPowerUsageCounter.FieldID},
			IntervalMSec:   60000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
		{
			Name:           "temperature",
			Fields:         []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
			IntervalMSec:   120000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
		{
			Name: "default",
			Fields: []dcgm.Short{
				testutils.SampleGPUTotalEnergyCounter.FieldID,
				testutils.SampleVGPULicenseStatusCounter.FieldID,
			},
			IntervalMSec:   30000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
	}, got)
}

// TestWatchListManagerPartitionFieldWatchGroupsKeepsLabelFieldsInDefaultGroup checks that a label field no
// group names is watched in the default group at the default interval.
func TestWatchListManagerPartitionFieldWatchGroupsKeepsLabelFieldsInDefaultGroup(t *testing.T) {
	manager := &WatchListManager{
		counters: testutils.SampleCounters,
		watchGroups: []appconfig.WatchGroup{
			{Name: "temperature", Interval: 120000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
		},
	}

	got, err := manager.partitionFieldWatchGroups(
		[]dcgm.Short{
			testutils.SampleGPUTempCounter.FieldID,
			testutils.SampleDriverVersionCounter.FieldID,
		},
		30000,
	)

	require.NoError(t, err)
	assert.Equal(t, []devicewatcher.FieldWatchGroup{
		{
			Name:           "temperature",
			Fields:         []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
			IntervalMSec:   120000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
		{
			Name:           "default",
			Fields:         []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			IntervalMSec:   30000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
	}, got)
}

// TestWatchListManagerPartitionsExporterBackingFieldsByName checks that fields the exporter reads to build its
// own metrics, such as XID errors and clock events, are matched to watch groups by field name.
func TestWatchListManagerPartitionsExporterBackingFieldsByName(t *testing.T) {
	manager := &WatchListManager{
		counters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_XID_ERROR, FieldName: "DCGM_FI_DEV_XID_ERROR"},
			{FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, FieldName: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS"},
		},
		watchGroups: []appconfig.WatchGroup{
			{Name: "xid", Interval: 60000, Fields: []string{"DCGM_FI_DEV_XID_ERRORS"}},
			{Name: "clock-events", Interval: 120000, Fields: []string{"DCGM_FI_DEV_CLOCKS_EVENT_REASONS"}},
		},
	}

	got, err := manager.partitionFieldWatchGroups(
		[]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
		30000,
	)

	require.NoError(t, err)
	assert.Equal(t, []devicewatcher.FieldWatchGroup{
		{
			Name:           "xid",
			Fields:         []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
			IntervalMSec:   60000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
		{
			Name:           "clock-events",
			Fields:         []dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
			IntervalMSec:   120000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
	}, got)
}

// TestWatchListManagerResolvesPerGroupRetention verifies property-level inheritance reaches concrete field groups.
func TestWatchListManagerResolvesPerGroupRetention(t *testing.T) {
	maxSamples := int64(2)
	manager := &WatchListManager{
		counters:       testutils.SampleCounters,
		watchRetention: appconfig.WatchRetention{MaxAge: 5 * time.Minute, MaxSamples: 9},
		watchGroups: []appconfig.WatchGroup{
			{
				Name:     "latest-values",
				Interval: 1000,
				Fields:   []string{"DCGM_FI_DEV_GPU_TEMP"},
				Retention: appconfig.WatchRetentionOverride{
					MaxSamples: &maxSamples,
				},
			},
		},
	}

	got, err := manager.partitionFieldWatchGroups(
		[]dcgm.Short{
			testutils.SampleGPUTempCounter.FieldID,
			testutils.SampleGPUPowerUsageCounter.FieldID,
		},
		30000,
	)

	require.NoError(t, err)
	assert.Equal(t, []devicewatcher.FieldWatchGroup{
		{
			Name:           "latest-values",
			Fields:         []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
			IntervalMSec:   1000,
			MaxKeepAge:     300,
			MaxKeepSamples: 2,
		},
		{
			Name:           "default",
			Fields:         []dcgm.Short{testutils.SampleGPUPowerUsageCounter.FieldID},
			IntervalMSec:   30000,
			MaxKeepAge:     300,
			MaxKeepSamples: 9,
		},
	}, got)
}

// TestWatchListManagerPartitionFieldWatchGroupsRejectsInvalidPattern checks that an invalid wildcard pattern
// returns an "invalid field pattern" error and no groups.
func TestWatchListManagerPartitionFieldWatchGroupsRejectsInvalidPattern(t *testing.T) {
	manager := &WatchListManager{
		counters: testutils.SampleCounters,
		watchGroups: []appconfig.WatchGroup{
			{Name: "bad", Interval: 60000, Fields: []string{"DCGM_FI_DEV_GPU_["}},
		},
	}

	got, err := manager.partitionFieldWatchGroups(testutils.SampleGPUFieldIDs, 30000)

	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "invalid field pattern")
}

// TestWatchListSetDeviceFieldsResetsToDefaultCadence checks that new device fields no configured group
// covers are put in the default group at the default interval, replacing the old groups.
func TestWatchListSetDeviceFieldsResetsToDefaultCadence(t *testing.T) {
	watchList := NewWatchListWithGroups(
		mockDeviceInfoFunc(gomock.NewController(t)),
		[]dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
		nil,
		[]devicewatcher.FieldWatchGroup{
			{
				Name:         "temperature",
				Fields:       []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
				IntervalMSec: 120000,
			},
		},
		mockdevicewatcher.NewMockWatcher(gomock.NewController(t)),
		30000,
	)

	watchList.SetDeviceFields([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})

	assert.Equal(t, []devicewatcher.FieldWatchGroup{
		{
			Name:           "default",
			Fields:         []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
			IntervalMSec:   30000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
	}, watchList.FieldWatchGroups())
}

// TestWatchListSetDeviceFieldsPreservesConfiguredCadence checks that new device fields keep the interval and
// keep limits of the configured groups they belong to, and that a group left with no fields is dropped.
func TestWatchListSetDeviceFieldsPreservesConfiguredCadence(t *testing.T) {
	watchList := NewWatchListWithGroups(
		mockDeviceInfoFunc(gomock.NewController(t)),
		[]dcgm.Short{
			dcgm.DCGM_FI_DEV_XID_ERRORS,
			dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
			testutils.SampleGPUTempCounter.FieldID,
		},
		nil,
		[]devicewatcher.FieldWatchGroup{
			{
				Name:           "xid",
				Fields:         []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
				IntervalMSec:   60000,
				MaxKeepAge:     600,
				MaxKeepSamples: 0,
			},
			{
				Name:           "clock-events",
				Fields:         []dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
				IntervalMSec:   120000,
				MaxKeepAge:     0,
				MaxKeepSamples: 2,
			},
			{
				Name:         "default",
				Fields:       []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
				IntervalMSec: 30000,
			},
		},
		mockdevicewatcher.NewMockWatcher(gomock.NewController(t)),
		30000,
	)

	watchList.SetDeviceFields([]dcgm.Short{
		dcgm.DCGM_FI_DEV_XID_ERRORS,
		dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
	})

	assert.Equal(t, []devicewatcher.FieldWatchGroup{
		{
			Name:           "xid",
			Fields:         []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
			IntervalMSec:   60000,
			MaxKeepAge:     600,
			MaxKeepSamples: 0,
		},
		{
			Name:           "clock-events",
			Fields:         []dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
			IntervalMSec:   120000,
			MaxKeepAge:     0,
			MaxKeepSamples: 2,
		},
	}, watchList.FieldWatchGroups())
}

// TestDeviceTypesToWatchDoesNotIncludeGPUCI checks that compute instances are not in the list of entity types
// that get their own watch list.
func TestDeviceTypesToWatchDoesNotIncludeGPUCI(t *testing.T) {
	assert.NotContains(t, DeviceTypesToWatch, dcgm.FE_GPU_CI)
}

// TestWatchListManagerCreateEntityWatchListSkipsCPUDiscoveryWithoutMetrics checks that when no CPU
// metric is configured, no CPU watch list is created and CPUs are not looked up.
func TestWatchListManagerCreateEntityWatchListSkipsCPUDiscoveryWithoutMetrics(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGMProvider)

	counterList := counters.CounterList{testutils.SampleDriverVersionCounter}
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	watcher.EXPECT().GetDeviceFields(counterList.NonLabelCounters(), dcgm.FE_CPU).Return(devicewatcher.ResolvedFields{})

	manager := NewWatchListManager(counterList, &appconfig.Config{})
	require.NoError(t, manager.CreateEntityWatchList(dcgm.FE_CPU, watcher, 1))

	_, exists := manager.EntityWatchList(dcgm.FE_CPU)
	assert.False(t, exists)
}

// createEntityWatchListFields describes the manager state expected before creating an entity watch list.
type createEntityWatchListFields struct {
	entityWatchLists      map[dcgm.Field_Entity_Group]WatchList
	entityWatchListsCount int
	counters              counters.CounterList
	gOpts                 appconfig.DeviceOptions
	sOpts                 appconfig.DeviceOptions
	cOpts                 appconfig.DeviceOptions
	useFakeGPUs           bool
}

// createEntityWatchListArgs provides the entity, watcher, and interval used to create one entity watch list.
type createEntityWatchListArgs struct {
	entityType      dcgm.Field_Entity_Group
	watcher         *mockdevicewatcher.MockWatcher
	collectInterval int64
}

// createEntityWatchListCase describes one manager state, its mocked field and device discovery, and the
// watch lists CreateEntityWatchList should leave behind.
type createEntityWatchListCase struct {
	name         string
	fields       createEntityWatchListFields
	args         createEntityWatchListArgs
	deviceFields []dcgm.Short
	labelFields  []dcgm.Short
	// mockFunc declares the watcher and DCGM calls expected for this case. A successful case expects
	// device discovery twice, because wantFunc discovers devices before CreateEntityWatchList does.
	mockFunc func(
		*mockdevicewatcher.MockWatcher, counters.CounterList, counters.CounterList,
		dcgm.Field_Entity_Group, []dcgm.Short, []dcgm.Short,
	)
	// wantFunc builds the watch lists expected after the call; it runs before CreateEntityWatchList.
	wantFunc func(
		*WatchListManager, dcgm.Field_Entity_Group, []dcgm.Short, []dcgm.Short,
		*mockdevicewatcher.MockWatcher, int64,
	) map[dcgm.Field_Entity_Group]WatchList
	wantErr bool
}

// TestWatchListManager_CreateEntityWatchList checks how CreateEntityWatchList adds, replaces, and refuses
// GPU watch lists.
func TestWatchListManager_CreateEntityWatchList(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
	mockDCGMProvider.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	t.Run("new watch lists", func(t *testing.T) {
		runCreateEntityWatchListCases(t, createEntityWatchListNewCases(ctrl, mockDCGMProvider))
	})
	t.Run("existing watch lists", func(t *testing.T) {
		runCreateEntityWatchListCases(t, createEntityWatchListExistingCases(ctrl, mockDCGMProvider))
	})
	t.Run("device discovery errors", func(t *testing.T) {
		runCreateEntityWatchListCases(t, createEntityWatchListErrorCases(ctrl, mockDCGMProvider))
	})
}

// createEntityWatchListNewCases defines GPU watch lists created on an empty manager with different field sets.
func createEntityWatchListNewCases(
	ctrl *gomock.Controller, mockDCGMProvider *mockdcgm.MockDCGM,
) []createEntityWatchListCase {
	return []createEntityWatchListCase{
		{
			name: "Create GPU WatchList",
			fields: createEntityWatchListFields{
				entityWatchLists:      make(map[dcgm.Field_Entity_Group]WatchList),
				entityWatchListsCount: 1,
				counters:              testutils.SampleCounters,
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: createEntityWatchListArgs{
				entityType:      dcgm.FE_GPU,
				watcher:         mockdevicewatcher.NewMockWatcher(ctrl),
				collectInterval: 1,
			},
			deviceFields: testutils.SampleGPUFieldIDs,
			labelFields:  []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher, counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group, deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(devicewatcher.ResolvedFields{Fields: deviceFields})
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(devicewatcher.ResolvedFields{Fields: labelDeviceFields})

				fakeDevices := testfixtures.SampleGPUDevices()
				_, fakeGPUs, _, _ := testfixtures.SampleMIGHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).Times(2)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil).Times(2)
			},
			wantFunc: func(
				e *WatchListManager, entityType dcgm.Field_Entity_Group, deviceFields,
				labelDeviceFields []dcgm.Short, watcher *mockdevicewatcher.MockWatcher, collectInterval int64,
			) map[dcgm.Field_Entity_Group]WatchList {
				watchList := make(map[dcgm.Field_Entity_Group]WatchList)

				mockDeviceInfo, _ := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
				watchList[entityType] = *NewWatchList(mockDeviceInfo, deviceFields, labelDeviceFields, watcher,
					collectInterval)

				return watchList
			},
		},
		{
			name: "Create GPU WatchList Without DCGM Fields",
			fields: createEntityWatchListFields{
				entityWatchLists:      make(map[dcgm.Field_Entity_Group]WatchList),
				entityWatchListsCount: 1,
				counters:              []counters.Counter{},
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: createEntityWatchListArgs{
				entityType:      dcgm.FE_GPU,
				watcher:         mockdevicewatcher.NewMockWatcher(ctrl),
				collectInterval: 1,
			},
			deviceFields: []dcgm.Short{},
			labelFields:  []dcgm.Short{},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher, counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group, deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(devicewatcher.ResolvedFields{Fields: deviceFields}).Times(1)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(devicewatcher.ResolvedFields{Fields: labelDeviceFields}).Times(1)

				fakeDevices := testfixtures.SampleGPUDevices()
				_, fakeGPUs, _, _ := testfixtures.SampleMIGHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method.
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).Times(2)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil).Times(2)
			},
			wantFunc: func(
				e *WatchListManager,
				entityType dcgm.Field_Entity_Group,
				deviceFields,
				labelDeviceFields []dcgm.Short,
				watcher *mockdevicewatcher.MockWatcher,
				collectInterval int64,
			) map[dcgm.Field_Entity_Group]WatchList {
				watchList := make(map[dcgm.Field_Entity_Group]WatchList)

				mockDeviceInfo, _ := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
				watchList[entityType] = *NewWatchList(mockDeviceInfo, deviceFields, []dcgm.Short{}, watcher,
					collectInterval)

				return watchList
			},
			wantErr: false,
		},
		{
			name: "Create GPU WatchList With Only Driver-Version Label",
			fields: createEntityWatchListFields{
				entityWatchLists:      make(map[dcgm.Field_Entity_Group]WatchList),
				entityWatchListsCount: 1,
				counters:              counters.CounterList{testutils.SampleDriverVersionCounter},
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: createEntityWatchListArgs{
				entityType:      dcgm.FE_GPU,
				watcher:         mockdevicewatcher.NewMockWatcher(ctrl),
				collectInterval: 1,
			},
			deviceFields: []dcgm.Short{},
			labelFields:  []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher,
				counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group,
				deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(devicewatcher.ResolvedFields{Fields: deviceFields}).Times(1)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(devicewatcher.ResolvedFields{Fields: labelDeviceFields}).Times(1)

				fakeDevices := testfixtures.SampleGPUDevices()
				_, fakeGPUs, _, _ := testfixtures.SampleMIGHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method.
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).Times(2)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil).Times(2)
			},
			wantFunc: func(
				e *WatchListManager,
				entityType dcgm.Field_Entity_Group,
				deviceFields,
				labelDeviceFields []dcgm.Short,
				watcher *mockdevicewatcher.MockWatcher,
				collectInterval int64,
			) map[dcgm.Field_Entity_Group]WatchList {
				watchList := make(map[dcgm.Field_Entity_Group]WatchList)

				mockDeviceInfo, _ := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
				watchList[entityType] = *NewWatchList(mockDeviceInfo, deviceFields, labelDeviceFields, watcher,
					collectInterval)

				return watchList
			},
			wantErr: false,
		},
	}
}

// createEntityWatchListExistingCases defines GPU watch lists created when the manager already holds watch lists,
// either for the GPU itself or for other entity types.
func createEntityWatchListExistingCases(
	ctrl *gomock.Controller, mockDCGMProvider *mockdcgm.MockDCGM,
) []createEntityWatchListCase {
	return []createEntityWatchListCase{
		{
			name: "Override existing GPU WatchList",
			fields: createEntityWatchListFields{
				entityWatchLists: map[dcgm.Field_Entity_Group]WatchList{
					dcgm.FE_GPU: {
						deviceInfo:        &deviceinfo.Info{},
						deviceFields:      []dcgm.Short{10, 20, 30},
						labelDeviceFields: []dcgm.Short{100, 200, 300},
						watcher:           nil,
						collectInterval:   10000,
					},
				},
				entityWatchListsCount: 1,
				counters:              testutils.SampleCounters,
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: createEntityWatchListArgs{
				entityType:      dcgm.FE_GPU,
				watcher:         mockdevicewatcher.NewMockWatcher(ctrl),
				collectInterval: 1,
			},
			deviceFields: testutils.SampleGPUFieldIDs,
			labelFields:  []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher, counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group, deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(devicewatcher.ResolvedFields{Fields: deviceFields})
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(devicewatcher.ResolvedFields{Fields: labelDeviceFields})

				fakeDevices := testfixtures.SampleGPUDevices()
				_, fakeGPUs, _, _ := testfixtures.SampleMIGHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).Times(2)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil).Times(2)
			},
			wantFunc: func(
				e *WatchListManager, entityType dcgm.Field_Entity_Group, deviceFields,
				labelDeviceFields []dcgm.Short, watcher *mockdevicewatcher.MockWatcher, collectInterval int64,
			) map[dcgm.Field_Entity_Group]WatchList {
				watchList := make(map[dcgm.Field_Entity_Group]WatchList)

				mockDeviceInfo, _ := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
				watchList[entityType] = *NewWatchList(mockDeviceInfo, deviceFields, labelDeviceFields, watcher,
					collectInterval)

				return watchList
			},
		},
		{
			name: "Multiple Type WatchList",
			fields: createEntityWatchListFields{
				entityWatchLists: map[dcgm.Field_Entity_Group]WatchList{
					dcgm.FE_GPU: {
						deviceInfo:        &deviceinfo.Info{},
						deviceFields:      []dcgm.Short{10, 20, 30},
						labelDeviceFields: []dcgm.Short{100, 200, 300},
						watcher:           nil,
						collectInterval:   10000,
					},
					dcgm.FE_CPU: {
						deviceInfo:        &deviceinfo.Info{},
						deviceFields:      []dcgm.Short{11, 21, 31},
						labelDeviceFields: []dcgm.Short{110, 210, 310},
						watcher:           nil,
						collectInterval:   10000,
					},
				},
				entityWatchListsCount: 2,
				counters:              testutils.SampleCounters,
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: createEntityWatchListArgs{
				entityType:      dcgm.FE_GPU,
				watcher:         mockdevicewatcher.NewMockWatcher(ctrl),
				collectInterval: 1,
			},
			deviceFields: testutils.SampleGPUFieldIDs,
			labelFields:  []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher, counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group, deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(devicewatcher.ResolvedFields{Fields: deviceFields})
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(devicewatcher.ResolvedFields{Fields: labelDeviceFields})

				fakeDevices := testfixtures.SampleGPUDevices()
				_, fakeGPUs, _, _ := testfixtures.SampleMIGHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).Times(2)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil).Times(2)
			},
			wantFunc: func(
				e *WatchListManager, entityType dcgm.Field_Entity_Group, deviceFields,
				labelDeviceFields []dcgm.Short, watcher *mockdevicewatcher.MockWatcher, collectInterval int64,
			) map[dcgm.Field_Entity_Group]WatchList {
				watchList := make(map[dcgm.Field_Entity_Group]WatchList)
				for entity, existingWatchList := range e.entityWatchLists {
					watchList[entity] = existingWatchList
				}

				mockDeviceInfo, _ := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
				watchList[entityType] = *NewWatchList(mockDeviceInfo, deviceFields, labelDeviceFields, watcher,
					collectInterval)

				return watchList
			},
		},
		{
			name: "Multiple Type WatchList and different type",
			fields: createEntityWatchListFields{
				entityWatchLists: map[dcgm.Field_Entity_Group]WatchList{
					dcgm.FE_SWITCH: {
						deviceInfo:        &deviceinfo.Info{},
						deviceFields:      []dcgm.Short{10, 20, 30},
						labelDeviceFields: []dcgm.Short{100, 200, 300},
						watcher:           nil,
						collectInterval:   10000,
					},
					dcgm.FE_CPU: {
						deviceInfo:        &deviceinfo.Info{},
						deviceFields:      []dcgm.Short{11, 21, 31},
						labelDeviceFields: []dcgm.Short{110, 210, 310},
						watcher:           nil,
						collectInterval:   10000,
					},
				},
				entityWatchListsCount: 3,
				counters:              testutils.SampleCounters,
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: createEntityWatchListArgs{
				entityType:      dcgm.FE_GPU,
				watcher:         mockdevicewatcher.NewMockWatcher(ctrl),
				collectInterval: 1,
			},
			deviceFields: testutils.SampleGPUFieldIDs,
			labelFields:  []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher, counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group, deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(devicewatcher.ResolvedFields{Fields: deviceFields})
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(devicewatcher.ResolvedFields{Fields: labelDeviceFields})

				fakeDevices := testfixtures.SampleGPUDevices()
				_, fakeGPUs, _, _ := testfixtures.SampleMIGHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
				mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil).Times(2)
				mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(mockHierarchy, nil).Times(2)
			},
			wantFunc: func(
				e *WatchListManager, entityType dcgm.Field_Entity_Group, deviceFields,
				labelDeviceFields []dcgm.Short, watcher *mockdevicewatcher.MockWatcher, collectInterval int64,
			) map[dcgm.Field_Entity_Group]WatchList {
				watchList := make(map[dcgm.Field_Entity_Group]WatchList)
				for entity, existingWatchList := range e.entityWatchLists {
					watchList[entity] = existingWatchList
				}

				mockDeviceInfo, _ := deviceinfo.Initialize(e.gOpts, e.sOpts, e.cOpts, e.useFakeGPUs, entityType)
				watchList[entityType] = *NewWatchList(mockDeviceInfo, deviceFields, labelDeviceFields, watcher,
					collectInterval)

				return watchList
			},
		},
	}
}

// createEntityWatchListErrorCases defines device discovery failures that must leave no watch list behind.
func createEntityWatchListErrorCases(
	ctrl *gomock.Controller, mockDCGMProvider *mockdcgm.MockDCGM,
) []createEntityWatchListCase {
	return []createEntityWatchListCase{
		{
			name: "Device Info initialize error",
			fields: createEntityWatchListFields{
				entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
				counters:         testutils.SampleCounters,
				gOpts:            deviceOptionFalse,
				sOpts:            deviceOptionTrue,
				cOpts:            deviceOptionOther,
				useFakeGPUs:      false,
			},
			args: createEntityWatchListArgs{
				entityType:      dcgm.FE_GPU,
				watcher:         mockdevicewatcher.NewMockWatcher(ctrl),
				collectInterval: 1,
			},
			deviceFields: testutils.SampleGPUFieldIDs,
			labelFields:  []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher, counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group, deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(devicewatcher.ResolvedFields{Fields: deviceFields})
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(devicewatcher.ResolvedFields{Fields: labelDeviceFields})

				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(0), fmt.Errorf("some error"))
			},
			wantFunc: func(
				e *WatchListManager, entityType dcgm.Field_Entity_Group, deviceFields,
				labelDeviceFields []dcgm.Short, watcher *mockdevicewatcher.MockWatcher, collectInterval int64,
			) map[dcgm.Field_Entity_Group]WatchList {
				return nil
			},
			wantErr: true,
		},
	}
}

// runCreateEntityWatchListCases runs each case on a fresh manager; cases share the caller's DCGM mock,
// so every case must consume the DCGM calls it declares.
func runCreateEntityWatchListCases(t *testing.T, tests []createEntityWatchListCase) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &WatchListManager{
				entityWatchLists: tt.fields.entityWatchLists,
				counters:         tt.fields.counters,
				gOpts:            tt.fields.gOpts,
				sOpts:            tt.fields.sOpts,
				cOpts:            tt.fields.cOpts,
				useFakeGPUs:      tt.fields.useFakeGPUs,
			}

			tt.mockFunc(
				tt.args.watcher,
				tt.fields.counters.NonLabelCounters(),
				tt.fields.counters.LabelCounters(),
				tt.args.entityType,
				tt.deviceFields,
				tt.labelFields,
			)

			want := tt.wantFunc(
				e,
				tt.args.entityType,
				tt.deviceFields,
				tt.labelFields,
				tt.args.watcher,
				tt.args.collectInterval,
			)

			err := e.CreateEntityWatchList(tt.args.entityType, tt.args.watcher, tt.args.collectInterval)
			got := e.entityWatchLists
			gotEntityWatchList, exist := e.EntityWatchList(tt.args.entityType)

			if !tt.wantErr {
				assert.Nil(t, err, "expected no error")
				wantEntityWatchList := want[tt.args.entityType]

				assert.True(t, exist, "expected entity to exist")
				assert.Equal(t, want, got, "expected output to be equal")
				assert.Equal(t, tt.fields.entityWatchListsCount, len(got),
					"expected entityWatchLists count to be equal")
				assert.Equal(t, wantEntityWatchList, gotEntityWatchList, "expected entity results to be equal")
			} else {
				assert.NotNil(t, err, "expected an error.")
				assert.Equal(t, 0, len(got), "expected output to be zero")
				assert.False(t, exist, "expected entity to not exist")
			}
		})
	}
}

// TestWatchListManagerCreateEntityWatchListUsesResolvedComputeInstanceFields checks that the GPU watch
// list keeps the compute instance fields and the fields at both scopes that the watcher reports.
func TestWatchListManagerCreateEntityWatchListUsesResolvedComputeInstanceFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDCGM.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)

	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	deviceFields := []dcgm.Short{
		dcgm.DCGM_FI_DEV_GPU_TEMP,
		dcgm.DCGM_FI_DEV_FB_USED,
		dcgm.DCGM_FI_DEV_XID_ERRORS,
	}
	labelFields := []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID}
	sampleCounters := counters.CounterList(testutils.SampleCounters)
	watcher.EXPECT().GetDeviceFields(sampleCounters.NonLabelCounters(), dcgm.FE_GPU).Return(devicewatcher.ResolvedFields{
		Fields:                 deviceFields,
		ComputeInstanceFields:  []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED},
		FieldsAtMultipleScopes: []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
	})
	watcher.EXPECT().GetDeviceFields(sampleCounters.LabelCounters(), dcgm.FE_GPU).Return(devicewatcher.ResolvedFields{
		Fields: labelFields,
	})

	fakeDevices := testfixtures.SampleGPUDevices()
	_, fakeGPUs, _, _ := testfixtures.SampleMIGHierarchy()
	hierarchy := dcgm.MigHierarchy_v2{Count: 1}
	hierarchy.EntityList[0] = fakeGPUs[0]
	mockDCGM.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
	mockDCGM.EXPECT().GetDeviceInfo(uint(0)).Return(fakeDevices[0], nil)
	mockDCGM.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
	mockDCGM.EXPECT().GetGPUInstanceHierarchy().Return(hierarchy, nil)

	manager := &WatchListManager{
		entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
		counters:         sampleCounters,
		gOpts:            deviceOptionFalse,
		sOpts:            deviceOptionTrue,
		cOpts:            deviceOptionOther,
	}

	require.NoError(t, manager.CreateEntityWatchList(dcgm.FE_GPU, watcher, 1))
	watchList, ok := manager.EntityWatchList(dcgm.FE_GPU)
	require.True(t, ok)
	assert.Equal(t, []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED}, watchList.ComputeInstanceFields())
	assert.Equal(t, []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}, watchList.FieldsAtMultipleScopes())
}

// TestWatchListManager_CreateEntityWatchListWarnsOnceForGPUInstanceWithoutComputeInstances checks that a
// GPU instance with no compute instances logs one warning with its GPU, IDs, profile and a hint,
// even when a second watch list is created.
func TestWatchListManager_CreateEntityWatchListWarnsOnceForGPUInstanceWithoutComputeInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
	mockDCGMProvider.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	logs := captureWarnLogs(t)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	manager := &WatchListManager{
		entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
		counters:         testutils.SampleCounters,
		gOpts:            appconfig.DeviceOptions{Flex: true},
		sOpts:            appconfig.DeviceOptions{Flex: true},
		cOpts:            appconfig.DeviceOptions{Flex: true},
		useFakeGPUs:      false,
	}

	fakeDevices := testfixtures.SampleGPUDevices()
	_, fakeGPUs, fakeGPUInstances, _ := testfixtures.SampleMIGHierarchy()

	hierarchy := dcgm.MigHierarchy_v2{Count: 2}
	hierarchy.EntityList[0] = fakeGPUs[0]
	hierarchy.EntityList[1] = fakeGPUInstances[0]

	entities := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
	}
	values := []dcgm.FieldValue_v2{
		{EntityID: fakeGPUInstances[0].Entity.EntityId},
	}

	expectWatchFields := func(entityType dcgm.Field_Entity_Group) {
		watcher.EXPECT().GetDeviceFields(manager.counters.NonLabelCounters(), entityType).Return(devicewatcher.ResolvedFields{
			Fields: []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
		})
		watcher.EXPECT().GetDeviceFields(manager.counters.LabelCounters(), entityType).Return(devicewatcher.ResolvedFields{})
	}
	expectGPUInfoInit := func() {
		mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
		mockDCGMProvider.EXPECT().GetDeviceInfo(uint(0)).Return(fakeDevices[0], nil)
		mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
		mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(hierarchy, nil)
		mockDCGMProvider.EXPECT().EntitiesGetLatestValues(entities, gomock.Any(), gomock.Any()).Return(values, nil)
		mockDCGMProvider.EXPECT().Fv2_String(values[0]).Return("1g.10gb")
	}

	expectWatchFields(dcgm.FE_GPU)
	expectGPUInfoInit()
	require.NoError(t, manager.CreateEntityWatchList(dcgm.FE_GPU, watcher, 1))
	records := warningRecords(t, logs, noComputeInstancesWarning)
	require.Len(t, records, 1)

	expectWatchFields(dcgm.FE_LINK)
	mockDCGMProvider.EXPECT().GetEntityGroupEntities(dcgm.FE_SWITCH).Return(nil, nil)
	expectGPUInfoInit()
	require.NoError(t, manager.CreateEntityWatchList(dcgm.FE_LINK, watcher, 1))

	records = warningRecords(t, logs, noComputeInstancesWarning)
	require.Len(t, records, 1)
	assert.EqualValues(t, 0, records[0]["gpu_id"])
	assert.EqualValues(t, fakeGPUInstances[0].Entity.EntityId, records[0]["gpu_instance_entity_id"])
	assert.EqualValues(t, fakeGPUInstances[0].Info.NvmlInstanceId, records[0]["nvml_instance_id"])
	assert.Equal(t, "1g.10gb", records[0]["mig_profile"])
	assert.Equal(t, "create at least one MIG compute instance for this GPU instance", records[0]["hint"])

	expectWatchFields(dcgm.FE_GPU)
	expectGPUInfoInit()
	require.NoError(t, manager.CreateEntityWatchList(dcgm.FE_GPU, watcher, 1))
	records = warningRecords(t, logs, noComputeInstancesWarning)
	require.Len(t, records, 2)
}

// TestWatchListManager_CreateEntityWatchListDoesNotWarnWhenGPUInstancesHaveComputeInstancesByParentID checks
// that no missing-compute-instance warning is logged when each GPU instance has a compute instance
// linked to it by parent ID.
func TestWatchListManager_CreateEntityWatchListDoesNotWarnWhenGPUInstancesHaveComputeInstancesByParentID(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)
	mockDCGMProvider.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	logs := captureWarnLogs(t)
	watcher := mockdevicewatcher.NewMockWatcher(ctrl)
	manager := &WatchListManager{
		entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
		counters:         testutils.SampleCounters,
		gOpts:            appconfig.DeviceOptions{Flex: true},
		sOpts:            appconfig.DeviceOptions{Flex: true},
		cOpts:            appconfig.DeviceOptions{Flex: true},
		useFakeGPUs:      false,
	}

	fakeDevices := testfixtures.SampleGPUDevices()
	_, fakeGPUs, fakeGPUInstances, fakeGPUComputeInstances := testfixtures.SampleMIGHierarchy()

	hierarchy := dcgm.MigHierarchy_v2{Count: 5}
	hierarchy.EntityList[0] = fakeGPUs[0]
	hierarchy.EntityList[1] = fakeGPUInstances[0]
	hierarchy.EntityList[2] = fakeGPUInstances[1]
	hierarchy.EntityList[3] = fakeGPUComputeInstances[0]
	hierarchy.EntityList[4] = fakeGPUComputeInstances[2]

	entities := []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[0].Entity.EntityId},
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: fakeGPUInstances[1].Entity.EntityId},
	}
	values := []dcgm.FieldValue_v2{
		{EntityID: fakeGPUInstances[0].Entity.EntityId},
		{EntityID: fakeGPUInstances[1].Entity.EntityId},
	}

	watcher.EXPECT().GetDeviceFields(manager.counters.NonLabelCounters(), dcgm.FE_GPU).Return(devicewatcher.ResolvedFields{})
	watcher.EXPECT().GetDeviceFields(manager.counters.LabelCounters(), dcgm.FE_GPU).Return(devicewatcher.ResolvedFields{})
	mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
	mockDCGMProvider.EXPECT().GetDeviceInfo(uint(0)).Return(fakeDevices[0], nil)
	mockDCGMProvider.EXPECT().GetNvLinkLinkStatus().Return([]dcgm.NvLinkStatus{}, nil)
	mockDCGMProvider.EXPECT().GetGPUInstanceHierarchy().Return(hierarchy, nil)
	mockDCGMProvider.EXPECT().EntitiesGetLatestValues(entities, gomock.Any(), gomock.Any()).Return(values, nil)
	mockDCGMProvider.EXPECT().Fv2_String(values[0]).Return("1g.10gb")
	mockDCGMProvider.EXPECT().Fv2_String(values[1]).Return("2g.20gb")

	require.NoError(t, manager.CreateEntityWatchList(dcgm.FE_GPU, watcher, 1))
	assert.Empty(t, warningRecords(t, logs, noComputeInstancesWarning))
}

// TestWatchListManager_EntityWatchList returns the requested entity watch list, applies an override when present, and reports missing entity types.
func TestWatchListManager_EntityWatchList(t *testing.T) {
	tests := []struct {
		name             string
		deviceType       dcgm.Field_Entity_Group
		entityWatchLists map[dcgm.Field_Entity_Group]WatchList
		wantWatchList    WatchList
		wantExist        bool
		override         bool
	}{
		{
			name:       "Get GPU WatchList",
			deviceType: dcgm.FE_GPU,
			entityWatchLists: map[dcgm.Field_Entity_Group]WatchList{
				dcgm.FE_GPU: {
					deviceInfo:        &deviceinfo.Info{},
					deviceFields:      []dcgm.Short{10, 20, 30},
					labelDeviceFields: []dcgm.Short{100, 200, 300},
					watcher:           nil,
					collectInterval:   10000,
				},
			},
			wantWatchList: WatchList{
				deviceInfo:        &deviceinfo.Info{},
				deviceFields:      []dcgm.Short{10, 20, 30},
				labelDeviceFields: []dcgm.Short{100, 200, 300},
				watcher:           nil,
				collectInterval:   10000,
			},
			wantExist: true,
		},
		{
			name:       "Get latest GPU WatchList",
			deviceType: dcgm.FE_GPU,
			entityWatchLists: map[dcgm.Field_Entity_Group]WatchList{
				dcgm.FE_GPU: {
					deviceInfo:        &deviceinfo.Info{},
					deviceFields:      []dcgm.Short{10, 20, 30},
					labelDeviceFields: []dcgm.Short{100, 200, 300},
					watcher:           nil,
					collectInterval:   10000,
				},
			},
			wantWatchList: WatchList{
				deviceInfo:        &deviceinfo.Info{},
				deviceFields:      []dcgm.Short{101, 201, 301},
				labelDeviceFields: []dcgm.Short{1001, 2001, 3001},
				watcher:           nil,
				collectInterval:   10000,
			},
			wantExist: true,
			override:  true,
		},
		{
			name:             "Empty WatchList",
			deviceType:       dcgm.FE_GPU,
			entityWatchLists: map[dcgm.Field_Entity_Group]WatchList{},
			wantWatchList:    WatchList{},
			wantExist:        false,
		},
		{
			name:       "Get GPU WatchList when only CPU Entity exist",
			deviceType: dcgm.FE_GPU,
			entityWatchLists: map[dcgm.Field_Entity_Group]WatchList{
				dcgm.FE_CPU: {
					deviceInfo:        &deviceinfo.Info{},
					deviceFields:      []dcgm.Short{10, 20, 30},
					labelDeviceFields: []dcgm.Short{100, 200, 300},
					watcher:           nil,
					collectInterval:   10000,
				},
			},
			wantWatchList: WatchList{},
			wantExist:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &WatchListManager{
				entityWatchLists: tt.entityWatchLists,
			}

			if tt.override {
				e.entityWatchLists[tt.deviceType] = tt.wantWatchList
			}

			gotEntityWatchList, exist := e.EntityWatchList(tt.deviceType)
			assert.Equal(t, tt.wantExist, exist, "expected entity exist value to be equal")
			assert.Equal(t, tt.wantWatchList, gotEntityWatchList, "expected output to be equal")
		})
	}
}

// TestOptionalDiscoveryAbsenceAndFailure distinguishes successful optional
// entity absence from an API failure without allocating an entity watch list.
func TestOptionalDiscoveryAbsenceAndFailure(t *testing.T) {
	cases := []struct {
		entity   dcgm.Field_Entity_Group
		counter  counters.Counter
		discover func(*mockdcgm.MockDCGM, error)
	}{
		{dcgm.FE_CPU, testutils.SampleCPUUtilTotalCounter, func(native *mockdcgm.MockDCGM, err error) {
			native.EXPECT().GetCPUHierarchy().Return(dcgm.CPUHierarchy_v2{}, err)
		}},
		{dcgm.FE_CPU_CORE, testutils.SampleCPUUtilTotalCounter, func(native *mockdcgm.MockDCGM, err error) {
			native.EXPECT().GetCPUHierarchy().Return(dcgm.CPUHierarchy_v2{}, err)
		}},
		{dcgm.FE_SWITCH, testutils.SampleSwitchCurrentTempCounter, func(native *mockdcgm.MockDCGM, err error) {
			native.EXPECT().GetEntityGroupEntities(dcgm.FE_SWITCH).Return(nil, err)
		}},
	}
	failure := errors.New("discovery API failed")
	outcomes := []struct {
		name string
		err  error
	}{{"absent", nil}, {"failure", failure}}
	for _, tc := range cases {
		for _, outcome := range outcomes {
			t.Run(tc.entity.String()+"/"+outcome.name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				native := mockdcgm.NewMockDCGM(ctrl)
				previous := dcgmprovider.Client()
				dcgmprovider.SetClient(native)
				t.Cleanup(func() { dcgmprovider.SetClient(previous) })
				tc.discover(native, outcome.err)
				watcher := mockdevicewatcher.NewMockWatcher(ctrl)
				counterList := counters.CounterList{tc.counter}
				watcher.EXPECT().GetDeviceFields(counterList.NonLabelCounters(), tc.entity).Return(devicewatcher.ResolvedFields{Fields: []dcgm.Short{tc.counter.FieldID}})
				watcher.EXPECT().GetDeviceFields(counterList.LabelCounters(), tc.entity).Return(devicewatcher.ResolvedFields{})
				manager := NewWatchListManager(counterList, &appconfig.Config{})
				err := manager.CreateEntityWatchList(tc.entity, watcher, 1000)
				if outcome.err != nil {
					require.ErrorIs(t, err, failure)
				} else {
					require.NoError(t, err)
				}
				_, exists := manager.EntityWatchList(tc.entity)
				require.False(t, exists)
			})
		}
	}
}

// scopeRecorder is a watch installer that records the fields of each watched
// scope and fails the scope named in failScope.
type scopeRecorder struct {
	watched   map[devicewatcher.WatchScope][]dcgm.Short
	failScope devicewatcher.WatchScope
	fail      bool
	closed    int
}

// GetDeviceFields is unused by WatchList.Watch and resolves nothing.
func (r *scopeRecorder) GetDeviceFields([]counters.Counter, dcgm.Field_Entity_Group) devicewatcher.ResolvedFields {
	return devicewatcher.ResolvedFields{}
}

// Watch records the fields of groups under scope, or fails when scope is the
// failing scope. Closing a returned result counts in closed.
func (r *scopeRecorder) Watch(
	groups []devicewatcher.FieldWatchGroup, _ deviceinfo.Provider, scope devicewatcher.WatchScope,
) (*devicewatcher.WatchResult, error) {
	if r.fail && scope == r.failScope {
		return nil, errors.New("watch failed")
	}

	for _, group := range groups {
		r.watched[scope] = append(r.watched[scope], group.Fields...)
	}
	return devicewatcher.ResultFromBindings(nil, func() { r.closed++ }), nil
}

// ExplainWatchFailure returns err unchanged.
func (r *scopeRecorder) ExplainWatchFailure(_ []devicewatcher.FieldWatchGroup, err error) error {
	return err
}

// TestWatchListPlacesGPULevelFieldsOnPhysicalGPUs checks that, when MIG
// instances are selected, fields DCGM declares at GPU level are watched on
// physical GPUs and never in a scope that holds instances, and that GPU-level
// labels become inherited labels. Without selected instances nothing changes.
func TestWatchListPlacesGPULevelFieldsOnPhysicalGPUs(t *testing.T) {
	const (
		power  = dcgm.DCGM_FI_DEV_POWER_USAGE
		fbUsed = dcgm.DCGM_FI_DEV_FB_USED
		dram   = dcgm.DCGM_FI_PROF_DRAM_ACTIVE
		serial = dcgm.DCGM_FI_DEV_BOARD_SERIAL
		driver = dcgm.DCGM_FI_DRIVER_VERSION
	)
	for _, tt := range []struct {
		name          string
		options       appconfig.DeviceOptions
		fields        []dcgm.Short
		labels        []dcgm.Short
		ciFields      []dcgm.Short
		gpuLevel      []dcgm.Short
		want          map[devicewatcher.WatchScope][]dcgm.Short
		wantGPULevel  []dcgm.Short
		wantInherited []dcgm.Short
	}{
		{
			name: "flex on a MIG GPU", options: appconfig.DeviceOptions{Flex: true},
			fields: []dcgm.Short{power, dram}, labels: []dcgm.Short{serial, driver},
			gpuLevel: []dcgm.Short{power, serial},
			want: map[devicewatcher.WatchScope][]dcgm.Short{
				devicewatcher.WatchPhysicalGPUs:       {power, serial, driver},
				devicewatcher.WatchConfiguredEntities: {dram, driver},
			},
			wantGPULevel: []dcgm.Short{power, serial}, wantInherited: []dcgm.Short{serial},
		},
		{
			name: "with compute-instance fields", options: appconfig.DeviceOptions{Flex: true},
			fields: []dcgm.Short{power, fbUsed}, labels: []dcgm.Short{driver},
			ciFields: []dcgm.Short{fbUsed}, gpuLevel: []dcgm.Short{power},
			want: map[devicewatcher.WatchScope][]dcgm.Short{
				devicewatcher.WatchPhysicalGPUs:            {power, driver},
				devicewatcher.WatchComputeInstanceEntities: {fbUsed, driver},
			},
			wantGPULevel: []dcgm.Short{power},
		},
		{
			name: "only a GPU-level label", options: appconfig.DeviceOptions{Flex: true},
			fields: []dcgm.Short{dram}, labels: []dcgm.Short{serial},
			gpuLevel: []dcgm.Short{serial},
			want: map[devicewatcher.WatchScope][]dcgm.Short{
				devicewatcher.WatchPhysicalGPUs:       {serial},
				devicewatcher.WatchConfiguredEntities: {dram},
			},
			wantGPULevel: []dcgm.Short{serial}, wantInherited: []dcgm.Short{serial},
		},
		{
			name: "whole GPUs", options: appconfig.DeviceOptions{MajorRange: []int{-1}},
			fields: []dcgm.Short{power, dram}, labels: []dcgm.Short{serial},
			gpuLevel: []dcgm.Short{power, serial},
			want: map[devicewatcher.WatchScope][]dcgm.Short{
				devicewatcher.WatchConfiguredEntities: {power, dram, serial},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instances := []deviceinfo.GPUInstanceInfo{{EntityId: 7, ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 9}}}}
			deviceInfo := testutils.MockGPUDeviceInfo(gomock.NewController(t), 1, map[int][]deviceinfo.GPUInstanceInfo{0: instances})
			deviceInfo.EXPECT().GOpts().Return(tt.options).AnyTimes()
			recorder := &scopeRecorder{watched: map[devicewatcher.WatchScope][]dcgm.Short{}}
			watchList := newWatchListWithGroups(
				deviceInfo, tt.fields, tt.labels, tt.ciFields, nil, tt.gpuLevel, nil, recorder, 1,
				appconfig.DefaultWatchMaxKeepAge.Seconds(), int32(appconfig.DefaultWatchMaxSamples),
			)

			result, err := watchList.Watch()
			require.NoError(t, err)
			result.Close()

			assert.Equal(t, tt.want, recorder.watched)
			assert.ElementsMatch(t, tt.wantGPULevel, watchList.GPULevelFields())
			assert.ElementsMatch(t, tt.wantInherited, watchList.InheritedLabelFields())
		})
	}
}

// TestWatchListClosesPhysicalGPUWatchWhenInstanceWatchFails checks that a
// failed instance-scope watch releases the physical GPU watch already made.
func TestWatchListClosesPhysicalGPUWatchWhenInstanceWatchFails(t *testing.T) {
	instances := []deviceinfo.GPUInstanceInfo{{EntityId: 7}}
	deviceInfo := testutils.MockGPUDeviceInfo(gomock.NewController(t), 1, map[int][]deviceinfo.GPUInstanceInfo{0: instances})
	deviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
	recorder := &scopeRecorder{
		watched:   map[devicewatcher.WatchScope][]dcgm.Short{},
		failScope: devicewatcher.WatchConfiguredEntities,
		fail:      true,
	}
	watchList := newWatchListWithGroups(
		deviceInfo,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_POWER_USAGE, dcgm.DCGM_FI_PROF_DRAM_ACTIVE}, nil, nil, nil,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_POWER_USAGE}, nil, recorder, 1,
		appconfig.DefaultWatchMaxKeepAge.Seconds(), int32(appconfig.DefaultWatchMaxSamples),
	)

	result, err := watchList.Watch()

	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 1, recorder.closed)
}
