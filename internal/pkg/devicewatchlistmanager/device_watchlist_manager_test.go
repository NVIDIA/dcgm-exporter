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
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatcher "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
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

			mockDeviceWatcher.EXPECT().WatchDeviceFields(tt.args.deviceFields, tt.args.deviceInfo,
				tt.args.collectInterval*1000).Return([]dcgm.GroupHandle{}, dcgm.FieldHandle{}, []func(){}, err)

			got := NewWatchList(tt.args.deviceInfo, tt.args.deviceFields, tt.args.labelDeviceFields, mockDeviceWatcher,
				tt.args.collectInterval)

			assert.Equal(t, tt.args.deviceInfo, got.DeviceInfo(), "Unexpected DeviceInfo() output.")
			assert.Equal(t, tt.args.deviceFields, got.DeviceFields(), "Unexpected DeviceFields() output.")
			assert.Equal(t, tt.args.labelDeviceFields, got.LabelDeviceFields(),
				"Unexpected LabelDeviceFields() output.")
			assert.Equal(t, tt.wantEmpty, got.IsEmpty(), "Unexpected IsEmpty() output.")

			_, err = got.Watch()
			if !tt.wantWatchErr {
				assert.Nil(t, err, "expected no error")
			} else {
				assert.NotNil(t, err, "expected error")
			}

			if tt.args.newDeviceFields != nil {
				got.SetDeviceFields(tt.args.newDeviceFields)
				assert.Equal(t, tt.args.newDeviceFields, got.DeviceFields(),
					"Unexpected DeviceFields() output after SetDeviceFields().")
				assert.NotEqual(t, tt.args.deviceFields, got.DeviceFields(),
					"Unexpected DeviceFields() output after SetDeviceFields().")
			}
		})
	}
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
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher, counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group, deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields).Times(1)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(deviceFields).Times(1)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
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
			deviceFields: []dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			mockFunc: func(
				watcher *mockdevicewatcher.MockWatcher,
				counters, labelCounters counters.CounterList,
				entityType dcgm.Field_Entity_Group,
				deviceFields, labelDeviceFields []dcgm.Short,
			) {
				watcher.EXPECT().GetDeviceFields(counters, entityType).Return(deviceFields).Times(1)
				watcher.EXPECT().GetDeviceFields(labelCounters, entityType).Return(deviceFields).Times(1)

				fakeDevices := deviceinfo.SpoofGPUDevices()
				_, fakeGPUs, _, _ := deviceinfo.SpoofMigHierarchy()

				mockHierarchy := dcgm.MigHierarchy_v2{
					Count: 1,
				}
				mockHierarchy.EntityList[0] = fakeGPUs[0]

				// Times 2 because the wantFunc is also calling the same method
				mockDCGMProvider.EXPECT().GetAllDeviceCount().Return(uint(1), nil).Times(2)
				mockDCGMProvider.EXPECT().GetDeviceInfo(gomock.Any()).Return(fakeDevices[0], nil).Times(2)
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
				tt.fields.counters,
				tt.fields.counters.LabelCounters(),
				tt.args.entityType,
				tt.deviceFields,
				[]dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
			)

			want := tt.wantFunc(
				e,
				tt.args.entityType,
				tt.deviceFields,
				[]dcgm.Short{testutils.SampleDriverVersionCounter.FieldID},
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
