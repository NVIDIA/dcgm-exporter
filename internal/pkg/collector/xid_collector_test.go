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

package collector

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdevicewatcher "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestIsDCGMExpXIDErrorsCountEnabled(t *testing.T) {
	tests := []struct {
		name string
		arg  counters.CounterList
		want bool
	}{
		{
			name: "empty",
			arg:  counters.CounterList{},
			want: false,
		},
		{
			name: "counter disabled",
			arg: counters.CounterList{
				counters.Counter{
					FieldID:   1,
					FieldName: "random1",
				},
				counters.Counter{
					FieldID:   2,
					FieldName: "random2",
				},
			},
			want: false,
		},
		{
			name: "counter enabled",
			arg: counters.CounterList{
				counters.Counter{
					FieldID:   1,
					FieldName: counters.DCGMExpXIDErrorsCount,
				},
				counters.Counter{
					FieldID:   2,
					FieldName: "random2",
				},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, IsDCGMExpXIDErrorsCountEnabled(tt.arg), "unexpected response")
		})
	}
}

func TestNewXIDCollector(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

	sampleDeviceInfo := &deviceinfo.Info{}
	sampleDeviceFields := []dcgm.Short{42}
	sampleCollectorInterval := int64(1)
	sampleConfig := appconfig.Config{}
	sampleHostname := "localhost"
	var sampleCleanups []func()

	sampleDCGMExpXIDCounter := counters.Counter{
		FieldID:   1,
		FieldName: counters.DCGMExpXIDErrorsCount,
	}

	sampleOtherCounter := counters.Counter{
		FieldID:   2,
		FieldName: "random2",
	}

	sampleLabelCounter := counters.Counter{
		FieldID:   3,
		FieldName: "random2",
		PromType:  "label",
	}

	type args struct {
		counterList     counters.CounterList
		hostname        string
		config          *appconfig.Config
		deviceWatchList *devicewatchlistmanager.WatchList
	}
	tests := []struct {
		name       string
		args       args
		conditions func(watcher *mockdevicewatcher.MockWatcher)
		want       func(string, *appconfig.Config, devicewatchlistmanager.WatchList) Collector
		wantErr    bool
	}{
		{
			name: "counter is disabled ",
			args: args{
				counterList:     counters.CounterList{},
				hostname:        sampleHostname,
				config:          nil,
				deviceWatchList: &devicewatchlistmanager.WatchList{},
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher) {},
			want: func(
				_ string, _ *appconfig.Config,
				_ devicewatchlistmanager.WatchList,
			) Collector {
				return nil
			},
			wantErr: true,
		},
		{
			name: "new XID collector watcher fails",
			args: args{
				counterList: counters.CounterList{
					sampleDCGMExpXIDCounter,
					sampleOtherCounter,
					sampleLabelCounter,
				},
				hostname: sampleHostname,
				config:   &sampleConfig,
				deviceWatchList: devicewatchlistmanager.NewWatchList(sampleDeviceInfo, sampleDeviceFields, nil,
					mockDeviceWatcher, sampleCollectorInterval),
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher) {
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return(nil,
					nil,
					sampleCleanups, fmt.Errorf("some error"))
			},
			want: func(
				_ string, _ *appconfig.Config,
				_ devicewatchlistmanager.WatchList,
			) Collector {
				return nil
			},
			wantErr: true,
		},
		{
			name: "new XID collector ",
			args: args{
				counterList: counters.CounterList{
					sampleDCGMExpXIDCounter,
					sampleOtherCounter,
					sampleLabelCounter,
				},
				hostname: sampleHostname,
				config:   &sampleConfig,
				deviceWatchList: devicewatchlistmanager.NewWatchList(sampleDeviceInfo, sampleDeviceFields, nil,
					mockDeviceWatcher, sampleCollectorInterval),
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher) {
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return(nil,
					nil,
					sampleCleanups, nil)
			},
			want: func(
				hostname string, config *appconfig.Config,
				deviceWatchList devicewatchlistmanager.WatchList,
			) Collector {
				deviceWatchList.SetDeviceFields([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})
				return &xidCollector{
					expCollector{
						baseExpCollector: baseExpCollector{
							deviceWatchList: deviceWatchList,
							counter:         sampleDCGMExpXIDCounter,
							labelsCounters:  []counters.Counter{sampleLabelCounter},
							hostname:        hostname,
							config:          config,
							cleanups:        sampleCleanups,
						},
						sourceFields: map[dcgm.Short]string{
							dcgm.DCGM_FI_DEV_XID_ERRORS: "DCGM_FI_DEV_XID_ERRORS",
						},
						windowSize: config.XIDCountWindowSize,
					},
				}
			},
			wantErr: false,
		},
		{
			name: "new XID collector with no label counters",
			args: args{
				counterList: counters.CounterList{
					sampleDCGMExpXIDCounter,
					sampleOtherCounter,
				},
				hostname: sampleHostname,
				config:   &sampleConfig,
				deviceWatchList: devicewatchlistmanager.NewWatchList(sampleDeviceInfo, sampleDeviceFields, nil,
					mockDeviceWatcher, sampleCollectorInterval),
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher) {
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return(nil,
					nil,
					sampleCleanups, nil)
			},
			want: func(
				hostname string, config *appconfig.Config,
				deviceWatchList devicewatchlistmanager.WatchList,
			) Collector {
				deviceWatchList.SetDeviceFields([]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS})
				return &xidCollector{
					expCollector{
						baseExpCollector: baseExpCollector{
							deviceWatchList: deviceWatchList,
							counter:         sampleDCGMExpXIDCounter,
							labelsCounters:  nil,
							hostname:        hostname,
							config:          config,
							cleanups:        sampleCleanups,
						},
						sourceFields: map[dcgm.Short]string{
							dcgm.DCGM_FI_DEV_XID_ERRORS: "DCGM_FI_DEV_XID_ERRORS",
						},
						windowSize: config.XIDCountWindowSize,
					},
				}
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.conditions(mockDeviceWatcher)

			got, err := NewXIDCollector(tt.args.counterList, tt.args.hostname, tt.args.config,
				*tt.args.deviceWatchList)
			want := tt.want(tt.args.hostname, tt.args.config, *tt.args.deviceWatchList)

			if !tt.wantErr {
				assert.NoError(t, err, "unexpected error")

				wantAttrs := testutils.GetFields(&want.(*xidCollector).expCollector, testutils.Fields)
				gotAttrs := testutils.GetFields(&got.(*xidCollector).expCollector, testutils.Fields)
				assert.Equal(t, wantAttrs, gotAttrs, "unexpected result")

				gotFuncAttrs := testutils.GetFields(&got.(*xidCollector).expCollector, testutils.Functions)
				for functionName, value := range gotFuncAttrs {
					assert.NotNilf(t, value, "unexpected %s to be not nil", functionName)
				}
			} else {
				assert.Error(t, err, "expected error")
				assert.Equal(t, want, got, "unexpected result")
			}
		})
	}
}

func sortXIDMetrics(metrics []Metric) {
	slices.SortFunc(metrics, func(a, b Metric) int {
		if a.GPU < b.GPU {
			return -1
		} else if a.GPU == b.GPU {
			if a.Labels["xid"] < b.Labels["xid"] {
				return -1
			}
		}
		return 1
	})
}

func xidMetricsCreator(
	counter counters.Counter, gpuID uint, value, hostname, mockFieldName,
	mockFieldLabelValue string, mockXID uint64,
) Metric {
	return Metric{
		Counter:       counter,
		Value:         value,
		GPU:           fmt.Sprintf("%d", gpuID),
		GPUUUID:       "",
		GPUDevice:     fmt.Sprintf("nvidia%d", gpuID),
		GPUModelName:  "",
		UUID:          "UUID",
		MigProfile:    "",
		GPUInstanceID: "",
		Hostname:      hostname,
		Labels: map[string]string{
			windowSizeInMSLabel: "0",
			mockFieldName:       mockFieldLabelValue,
			"xid":               fmt.Sprint(mockXID),
		},
		Attributes: map[string]string{},
	}
}

func Test_xidCollector_GetMetrics(t *testing.T) {
	/******* Mock DCGM *************/
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

	realDCGM := dcgmprovider.Client()
	defer func() {
		dcgmprovider.SetClient(realDCGM)
	}()
	dcgmprovider.SetClient(mockDCGM)

	/******** Mock Counters ************/
	mockDCGMXIDCounter := counters.Counter{
		FieldID:   1,
		FieldName: counters.DCGMExpXIDErrorsCount,
	}

	mockOtherCounter := counters.Counter{
		FieldID:   2,
		FieldName: "random2",
	}

	mockLabelDeviceField := dcgm.Short(3)
	mockFieldName := "random3"
	mockLabelValue := "this is mock label"
	mockLabelCounter := counters.Counter{
		FieldID:   mockLabelDeviceField,
		FieldName: mockFieldName,
		PromType:  "label",
	}

	/******** Mock Device Info *********/
	gOpts := appconfig.DeviceOptions{
		Flex: true,
	}

	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(gOpts).AnyTimes()

	/******** Other Mock Inputs ************/
	gpuID1 := uint(0)
	gpuID2 := uint(1)

	mockDeviceFields := []dcgm.Short{42}
	mockCollectorInterval := int64(1)
	mockConfig := appconfig.Config{}
	mockHostname := "localhost"
	var mockCleanups []func()

	mockGroupHandle1 := dcgm.GroupHandle{}
	mockGroupHandle1.SetHandle(uintptr(1))

	mockGroupHandle2 := dcgm.GroupHandle{}
	mockGroupHandle2.SetHandle(uintptr(2))

	mockFieldGroupHandle := dcgm.FieldHandle{}
	mockFieldGroupHandle.SetHandle(uintptr(1))

	mockLatestValues := []dcgm.FieldValue_v1{
		{
			FieldID:   150,
			FieldType: dcgm.DCGM_FT_INT64,
			Value:     [4096]byte{42},
		},
		{
			FieldID:   mockLabelDeviceField,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray(mockLabelValue),
		},
		{
			FieldID:   mockLabelDeviceField,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray(dcgm.DCGM_FT_STR_NOT_FOUND),
		},
	}

	tests := []struct {
		name       string
		collector  func() Collector
		conditions func(*mockdevicewatcher.MockWatcher, byte, byte)
		want       func() (MetricsByCounter, byte, byte)
		wantErr    bool
	}{
		{
			name: "XID collector with single XID event",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMXIDCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewXIDCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID2, Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					[]dcgm.FieldHandle{mockFieldGroupHandle},
					mockCleanups, nil)

				mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				mockDCGM.EXPECT().GetValuesSince(mockGroupHandle1, mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID1,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID2,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
			},
			want: func() (MetricsByCounter, byte, byte) {
				mockXIDErr1 := uint64(42)
				mockXIDErr2 := uint64(46)

				return MetricsByCounter{
					mockDCGMXIDCounter: []Metric{
						xidMetricsCreator(mockDCGMXIDCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr1),
						xidMetricsCreator(mockDCGMXIDCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr2),
					},
				}, byte(mockXIDErr1), byte(mockXIDErr2)
			},
			wantErr: false,
		},
		{
			name: "xid collector with multiple events",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMXIDCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewXIDCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, xidErr1, xidErr2 byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID1, Value: [4096]byte{xidErr1}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID1, Value: [4096]byte{xidErr1}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID1, Value: [4096]byte{xidErr2}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID2, Value: [4096]byte{xidErr1}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID2, Value: [4096]byte{xidErr2}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID2, Value: [4096]byte{xidErr2}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID2, Value: [4096]byte{xidErr2}},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					[]dcgm.FieldHandle{mockFieldGroupHandle},
					mockCleanups, nil)

				mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				mockDCGM.EXPECT().GetValuesSince(mockGroupHandle1, mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID1,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID2,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
			},
			want: func() (MetricsByCounter, byte, byte) {
				mockXIDErr1 := uint64(42)
				mockXIDErr2 := uint64(46)

				return MetricsByCounter{
					mockDCGMXIDCounter: []Metric{
						xidMetricsCreator(mockDCGMXIDCounter, gpuID1, "2", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr1),
						xidMetricsCreator(mockDCGMXIDCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr2),
						xidMetricsCreator(mockDCGMXIDCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr1),
						xidMetricsCreator(mockDCGMXIDCounter, gpuID2, "3", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr2),
					},
				}, byte(mockXIDErr1), byte(mockXIDErr2)
			},
			wantErr: false,
		},
		{
			name: "XID collector with DCGM_INT64_BLANK values should be filtered out",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMXIDCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewXIDCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, xidErr1, xidErr2 byte) {
				blankValue := createInt64ByteArray(dcgm.DCGM_FT_INT64_BLANK)

				mockEntitiesResult := []dcgm.FieldValue_v2{
					// Valid XID error from GPU 0
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID1, FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, FieldType: dcgm.DCGM_FT_INT64, Value: [4096]byte{xidErr1}},
					// DCGM_INT64_BLANK value from GPU 0 - should be filtered out
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID1, FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, FieldType: dcgm.DCGM_FT_INT64, Value: blankValue},
					// Another valid XID error from GPU 1
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID2, FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, FieldType: dcgm.DCGM_FT_INT64, Value: [4096]byte{xidErr2}},
					// Another DCGM_INT64_BLANK value from GPU 1 - should be filtered out
					{EntityGroupId: dcgm.FE_GPU, EntityID: gpuID2, FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, FieldType: dcgm.DCGM_FT_INT64, Value: blankValue},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					[]dcgm.FieldHandle{mockFieldGroupHandle},
					mockCleanups, nil)

				mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				mockDCGM.EXPECT().GetValuesSince(mockGroupHandle1, mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID1,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID2,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
			},
			want: func() (MetricsByCounter, byte, byte) {
				mockXIDErr1 := uint64(42)
				mockXIDErr2 := uint64(46)

				// We should only get the valid XID errors, not the BLANK values
				return MetricsByCounter{
					mockDCGMXIDCounter: []Metric{
						xidMetricsCreator(mockDCGMXIDCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr1),
						xidMetricsCreator(mockDCGMXIDCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockXIDErr2),
					},
				}, byte(mockXIDErr1), byte(mockXIDErr2)
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, gpu1Value, gpu2Value := tt.want()
			tt.conditions(mockDeviceWatcher, gpu1Value, gpu2Value)
			c := tt.collector()

			got, err := c.GetMetrics()

			if !tt.wantErr {
				assert.NoError(t, err, "GetMetrics() failed")
				assert.NotEmpty(t, got)

				wantMetrics := want[mockDCGMXIDCounter]
				gotMetrics := got[mockDCGMXIDCounter]

				assert.Len(t, gotMetrics, len(wantMetrics), "GetMetrics() returned wrong number of metrics")

				sortXIDMetrics(wantMetrics)
				sortXIDMetrics(gotMetrics)

				assert.Equalf(t, wantMetrics, gotMetrics, "GetMetrics()")
			}
		})
	}
}

func Test_xidCollector_GetMetricsReadsAllDeviceFieldGroups(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)

	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	fastFieldGroup := dcgm.FieldHandle{}
	fastFieldGroup.SetHandle(uintptr(2))
	slowFieldGroup := dcgm.FieldHandle{}
	slowFieldGroup.SetHandle(uintptr(3))

	mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).
		Return(
			[]dcgm.GroupHandle{groupHandle},
			[]dcgm.FieldHandle{fastFieldGroup, slowFieldGroup},
			nil,
			nil,
		)

	counter := counters.Counter{
		FieldID:   dcgm.Short(counters.DCGMXIDErrorsCount),
		FieldName: counters.DCGMExpXIDErrorsCount,
	}
	deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, nil, nil, mockDeviceWatcher, 1)
	collector, err := NewXIDCollector(counters.CounterList{counter}, "localhost", &appconfig.Config{}, *deviceWatchList)
	require.NoError(t, err)

	xidValue := createInt64ByteArray(42)
	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(groupHandle, fastFieldGroup, gomock.AssignableToTypeOf(time.Time{})).
		Return([]dcgm.FieldValue_v2{
			{
				EntityGroupId: dcgm.FE_GPU,
				EntityID:      0,
				FieldID:       dcgm.DCGM_FI_DEV_XID_ERRORS,
				FieldType:     dcgm.DCGM_FT_INT64,
				Status:        0,
				Value:         xidValue,
			},
		}, time.Time{}, nil)
	mockDCGM.EXPECT().GetValuesSince(groupHandle, slowFieldGroup, gomock.AssignableToTypeOf(time.Time{})).
		Return([]dcgm.FieldValue_v2{
			{
				EntityGroupId: dcgm.FE_GPU,
				EntityID:      0,
				FieldID:       dcgm.DCGM_FI_DEV_XID_ERRORS,
				FieldType:     dcgm.DCGM_FT_INT64,
				Status:        0,
				Value:         xidValue,
			},
		}, time.Time{}, nil)

	got, err := collector.GetMetrics()

	require.NoError(t, err)
	require.Len(t, got[counter], 1)
	assert.Equal(t, "2", got[counter][0].Value)
	assert.Equal(t, "42", got[counter][0].Labels["xid"])
}

func Test_xidCollector_GetMetricsLogsBlankSourceFieldName(t *testing.T) {
	buf := setupDebugLogCapture(t)

	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)

	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	mockGroupHandle := dcgm.GroupHandle{}
	mockGroupHandle.SetHandle(uintptr(1))
	mockFieldGroupHandle := dcgm.FieldHandle{}
	mockFieldGroupHandle.SetHandle(uintptr(1))

	mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).
		Return([]dcgm.GroupHandle{mockGroupHandle}, []dcgm.FieldHandle{mockFieldGroupHandle}, nil, nil)

	counterList := counters.CounterList{
		{
			FieldID:   dcgm.Short(counters.DCGMXIDErrorsCount),
			FieldName: counters.DCGMExpXIDErrorsCount,
		},
	}
	deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, nil, nil, mockDeviceWatcher, 1)

	collector, err := NewXIDCollector(counterList, "localhost", &appconfig.Config{}, *deviceWatchList)
	require.NoError(t, err)

	blankValue := createInt64ByteArray(dcgm.DCGM_FT_INT64_BLANK)
	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(mockGroupHandle, mockFieldGroupHandle, gomock.AssignableToTypeOf(time.Time{})).
		Return([]dcgm.FieldValue_v2{
			{
				EntityGroupId: dcgm.FE_GPU,
				EntityID:      0,
				FieldID:       dcgm.DCGM_FI_DEV_XID_ERRORS,
				FieldType:     dcgm.DCGM_FT_INT64,
				Status:        0,
				Value:         blankValue,
			},
		}, time.Time{}, nil)

	_, err = collector.GetMetrics()
	require.NoError(t, err)

	got := findLogRecord(t, buf, blankValueSkippedMessage)
	assert.Equal(t, blankValueSkippedMessage, got["msg"])
	assert.Equal(t, float64(dcgm.DCGM_FI_DEV_XID_ERRORS), got["fieldID"])
	assert.Equal(t, "DCGM_FI_DEV_XID_ERRORS", got["fieldName"])
	assert.Equal(t, dcgm.FE_GPU.String(), got["entityType"])
	assert.Equal(t, float64(0), got["entityID"])
}

func Test_xidCollector_GetMetricsIsolatesLabelsPerEntity(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)

	labelFieldID := dcgm.Short(3)
	labelName := "label_field"
	xidCounter := counters.Counter{FieldID: 1, FieldName: counters.DCGMExpXIDErrorsCount}
	labelCounter := counters.Counter{FieldID: labelFieldID, FieldName: labelName, PromType: "label"}

	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	groupHandle := dcgm.GroupHandle{}
	groupHandle.SetHandle(uintptr(1))
	fieldGroupHandle := dcgm.FieldHandle{}
	fieldGroupHandle.SetHandle(uintptr(1))
	mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).
		Return([]dcgm.GroupHandle{groupHandle}, []dcgm.FieldHandle{fieldGroupHandle}, nil, nil)

	deviceWatchList := devicewatchlistmanager.NewWatchList(
		mockGPUDeviceInfo,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		[]dcgm.Short{labelFieldID},
		mockDeviceWatcher,
		1,
	)
	collector, err := NewXIDCollector(
		counters.CounterList{xidCounter, labelCounter},
		"localhost",
		&appconfig.Config{},
		*deviceWatchList,
	)
	require.NoError(t, err)

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(groupHandle, fieldGroupHandle, gomock.AssignableToTypeOf(time.Time{})).
		Return([]dcgm.FieldValue_v2{{
			EntityGroupId: dcgm.FE_GPU,
			EntityID:      1,
			FieldID:       dcgm.DCGM_FI_DEV_XID_ERRORS,
			FieldType:     dcgm.DCGM_FT_INT64,
			Status:        0,
			Value:         createInt64ByteArray(42),
		}}, time.Time{}, nil)
	mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(0), []dcgm.Short{labelFieldID}).
		Return([]dcgm.FieldValue_v1{{
			FieldID:   labelFieldID,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray("gpu0-label"),
		}}, nil)
	mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(1), []dcgm.Short{labelFieldID}).
		Return([]dcgm.FieldValue_v1{{
			FieldID:   labelFieldID,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray("gpu1-label"),
		}}, nil)

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	require.Len(t, got[xidCounter], 2)

	byGPU := map[string]Metric{}
	for _, metric := range got[xidCounter] {
		byGPU[metric.GPU] = metric
	}
	assert.Equal(t, "gpu0-label", byGPU["0"].Labels[labelName])
	assert.Equal(t, "gpu1-label", byGPU["1"].Labels[labelName])
}
