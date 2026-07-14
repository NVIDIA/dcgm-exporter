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
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

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

func TestNewWatchList(t *testing.T) {
	ctrl := gomock.NewController(t)

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

			_, err = got.Watch()
			if !tt.wantWatchErr {
				assert.Nil(t, err, "expected no error")
			} else {
				assert.NotNil(t, err, "expected error")
			}
			assert.Empty(t, got.DeviceGroups(), "Unexpected DeviceGroups() output.")
			assert.Equal(t, dcgm.FieldHandle{}, got.DeviceFieldGroup(), "Unexpected DeviceFieldGroup() output.")

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

func TestWatchList_SetDeviceFieldsWithoutLabelWatchesPreservesLabelFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	got := NewWatchList(
		mockDeviceInfoFunc(ctrl),
		[]dcgm.Short{1},
		[]dcgm.Short{100, 101},
		mockdevicewatcher.NewMockWatcher(ctrl),
		1,
	)

	got.SetDeviceFieldsWithoutLabelWatches([]dcgm.Short{200, 200})

	assert.Equal(t, []dcgm.Short{200, 200}, got.DeviceFields())
	assert.Equal(t, []dcgm.Short{100, 101}, got.LabelDeviceFields())
	assert.Equal(t, []dcgm.Short{200}, got.watchFields)
	assert.False(t, got.IsEmpty())
}

func TestNewWatchListManager(t *testing.T) {
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

func TestValidateWatchGroups(t *testing.T) {
	tests := []struct {
		name        string
		counters    counters.CounterList
		watchGroups []appconfig.WatchGroup
		wantErr     string
		exactErr    bool
	}{
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
			Name:         "power",
			Fields:       []dcgm.Short{testutils.SampleGPUPowerUsageCounter.FieldID},
			IntervalMSec: 60000,
		},
		{
			Name:         "temperature",
			Fields:       []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
			IntervalMSec: 120000,
		},
		{
			Name: "default",
			Fields: []dcgm.Short{
				testutils.SampleGPUTotalEnergyCounter.FieldID,
				testutils.SampleVGPULicenseStatusCounter.FieldID,
			},
			IntervalMSec: 30000,
		},
	}, got)
}

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
			Name:         "temperature",
			Fields:       []dcgm.Short{testutils.SampleGPUTempCounter.FieldID},
			IntervalMSec: 120000,
		},
		{
			Name:         "default",
			Fields:       []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			IntervalMSec: 30000,
		},
	}, got)
}

func TestWatchListManagerPartitionsExporterBackingFieldsByName(t *testing.T) {
	manager := &WatchListManager{
		counters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, FieldName: "DCGM_FI_DEV_XID_ERRORS"},
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
			Name:         "xid",
			Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
			IntervalMSec: 60000,
		},
		{
			Name:         "clock-events",
			Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
			IntervalMSec: 120000,
		},
	}, got)
}

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
			Name:         "default",
			Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
			IntervalMSec: 30000,
		},
	}, watchList.FieldWatchGroups())
}

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
				Name:         "xid",
				Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
				IntervalMSec: 60000,
			},
			{
				Name:         "clock-events",
				Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
				IntervalMSec: 120000,
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
			Name:         "xid",
			Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
			IntervalMSec: 60000,
		},
		{
			Name:         "clock-events",
			Fields:       []dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
			IntervalMSec: 120000,
		},
	}, watchList.FieldWatchGroups())
}

func TestDeviceTypesToWatchDoesNotIncludeGPUCI(t *testing.T) {
	assert.NotContains(t, DeviceTypesToWatch, dcgm.FE_GPU_CI)
}

func TestWatchListManager_CreateEntityWatchList(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGMProvider)

	type fields struct {
		entityWatchLists      map[dcgm.Field_Entity_Group]WatchList
		entityWatchListsCount int
		counters              counters.CounterList
		gOpts                 appconfig.DeviceOptions
		sOpts                 appconfig.DeviceOptions
		cOpts                 appconfig.DeviceOptions
		useFakeGPUs           bool
	}
	type args struct {
		entityType      dcgm.Field_Entity_Group
		watcher         *mockdevicewatcher.MockWatcher
		collectInterval int64
	}
	tests := []struct {
		name         string
		fields       fields
		args         args
		deviceFields []dcgm.Short
		labelFields  []dcgm.Short
		mockFunc     func(
			*mockdevicewatcher.MockWatcher, counters.CounterList, counters.CounterList,
			dcgm.Field_Entity_Group, []dcgm.Short, []dcgm.Short,
		)
		wantFunc func(
			*WatchListManager, dcgm.Field_Entity_Group, []dcgm.Short, []dcgm.Short,
			*mockdevicewatcher.MockWatcher, int64,
		) map[dcgm.Field_Entity_Group]WatchList
		wantErr bool
	}{
		{
			name: "Create GPU WatchList",
			fields: fields{
				entityWatchLists:      make(map[dcgm.Field_Entity_Group]WatchList),
				entityWatchListsCount: 1,
				counters:              testutils.SampleCounters,
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: args{
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
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(labelDeviceFields)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

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
			name: "Override existing GPU WatchList",
			fields: fields{
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
			args: args{
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
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(labelDeviceFields)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

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
			fields: fields{
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
			args: args{
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
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(labelDeviceFields)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

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
			fields: fields{
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
			args: args{
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
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(labelDeviceFields)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

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
			name: "Device Info initialize error",
			fields: fields{
				entityWatchLists: make(map[dcgm.Field_Entity_Group]WatchList),
				counters:         testutils.SampleCounters,
				gOpts:            deviceOptionFalse,
				sOpts:            deviceOptionTrue,
				cOpts:            deviceOptionOther,
				useFakeGPUs:      false,
			},
			args: args{
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
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(labelDeviceFields)

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
		{
			name: "No GPU WatchList",
			fields: fields{
				entityWatchLists:      make(map[dcgm.Field_Entity_Group]WatchList),
				entityWatchListsCount: 1,
				counters:              []counters.Counter{},
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: args{
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
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields).Times(1)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(labelDeviceFields).Times(1)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

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
			name: "Only Driver Version to Watch",
			fields: fields{
				entityWatchLists:      make(map[dcgm.Field_Entity_Group]WatchList),
				entityWatchListsCount: 1,
				counters:              counters.CounterList{testutils.SampleDriverVersionCounter},
				gOpts:                 deviceOptionFalse,
				sOpts:                 deviceOptionTrue,
				cOpts:                 deviceOptionOther,
				useFakeGPUs:           false,
			},
			args: args{
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
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields).Times(1)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(labelDeviceFields).Times(1)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

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

func TestWatchListManager_CreateEntityWatchListWarnsOnceForGPUInstanceWithoutComputeInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

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

	fakeDevices := deviceinfo.SpoofGPUDevices()
	_, fakeGPUs, fakeGPUInstances, _ := deviceinfo.SpoofMigHierarchy()

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
		watcher.EXPECT().GetDeviceFields(manager.counters.NonLabelCounters(), entityType).Return([]dcgm.Short{})
		watcher.EXPECT().GetDeviceFields(manager.counters.LabelCounters(), entityType).Return([]dcgm.Short{})
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
	mockDCGMProvider.EXPECT().GetEntityGroupEntities(dcgm.FE_SWITCH).Return(nil, fmt.Errorf("no switches"))
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

func TestWatchListManager_CreateEntityWatchListDoesNotWarnWhenGPUInstancesHaveComputeInstancesByParentID(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGMProvider := mockdcgm.NewMockDCGM(ctrl)

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

	fakeDevices := deviceinfo.SpoofGPUDevices()
	_, fakeGPUs, fakeGPUInstances, fakeGPUComputeInstances := deviceinfo.SpoofMigHierarchy()

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

	watcher.EXPECT().GetDeviceFields(manager.counters.NonLabelCounters(), dcgm.FE_GPU).Return([]dcgm.Short{})
	watcher.EXPECT().GetDeviceFields(manager.counters.LabelCounters(), dcgm.FE_GPU).Return([]dcgm.Short{})
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
