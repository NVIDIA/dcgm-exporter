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

const invalidClockEventValue = 10000

func TestIsDCGMExpClockEventsCountEnabled(t *testing.T) {
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
			name: "counter event count disabled",
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
			name: "counter event count enabled",
			arg: counters.CounterList{
				counters.Counter{
					FieldID:   1,
					FieldName: counters.DCGMExpClockEventsCount,
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
			assert.Equalf(t, tt.want, IsDCGMExpClockEventsCountEnabled(tt.arg), "unexpected response")
		})
	}
}

func TestNewClockEventsCollector(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

	sampleDeviceInfo := &deviceinfo.Info{}
	sampleDeviceFields := []dcgm.Short{42}
	sampleCollectorInterval := int64(1)
	sampleConfig := appconfig.Config{}
	sampleHostname := "localhost"
	var sampleCleanups []func()

	sampleDCGMExpClockEventsCounter := counters.Counter{
		FieldID:   1,
		FieldName: counters.DCGMExpClockEventsCount,
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
			name: "new clock events collector watcher fails",
			args: args{
				counterList: counters.CounterList{
					sampleDCGMExpClockEventsCounter,
					sampleOtherCounter,
					sampleLabelCounter,
				},
				hostname: sampleHostname,
				config:   &sampleConfig,
				deviceWatchList: devicewatchlistmanager.NewWatchList(sampleDeviceInfo, sampleDeviceFields, nil,
					mockDeviceWatcher, sampleCollectorInterval),
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher) {
				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return(nil,
					dcgm.FieldHandle{},
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
			name: "new clock events collector ",
			args: args{
				counterList: counters.CounterList{
					sampleDCGMExpClockEventsCounter,
					sampleOtherCounter,
					sampleLabelCounter,
				},
				hostname: sampleHostname,
				config:   &sampleConfig,
				deviceWatchList: devicewatchlistmanager.NewWatchList(sampleDeviceInfo, sampleDeviceFields, nil,
					mockDeviceWatcher, sampleCollectorInterval),
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher) {
				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return(nil,
					dcgm.FieldHandle{},
					sampleCleanups, nil)
			},
			want: func(
				hostname string, config *appconfig.Config,
				deviceWatchList devicewatchlistmanager.WatchList,
			) Collector {
				deviceWatchList.SetDeviceFields([]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})
				return &clockEventsCollector{
					expCollector{
						baseExpCollector: baseExpCollector{
							deviceWatchList: deviceWatchList,
							counter:         sampleDCGMExpClockEventsCounter,
							labelsCounters:  []counters.Counter{sampleLabelCounter},
							hostname:        hostname,
							config:          config,
							cleanups:        sampleCleanups,
						},
						windowSize: config.ClockEventsCountWindowSize,
					},
				}
			},
			wantErr: false,
		},
		{
			name: "new clock events collector with no label counters",
			args: args{
				counterList: counters.CounterList{
					sampleDCGMExpClockEventsCounter,
					sampleOtherCounter,
				},
				hostname: sampleHostname,
				config:   &sampleConfig,
				deviceWatchList: devicewatchlistmanager.NewWatchList(sampleDeviceInfo, sampleDeviceFields, nil,
					mockDeviceWatcher, sampleCollectorInterval),
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher) {
				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return(nil,
					dcgm.FieldHandle{},
					sampleCleanups, nil)
			},
			want: func(
				hostname string, config *appconfig.Config,
				deviceWatchList devicewatchlistmanager.WatchList,
			) Collector {
				deviceWatchList.SetDeviceFields([]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})
				return &clockEventsCollector{
					expCollector{
						baseExpCollector: baseExpCollector{
							deviceWatchList: deviceWatchList,
							counter:         sampleDCGMExpClockEventsCounter,
							labelsCounters:  nil,
							hostname:        hostname,
							config:          config,
							cleanups:        sampleCleanups,
						},
						windowSize: config.ClockEventsCountWindowSize,
					},
				}
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.conditions(mockDeviceWatcher)

			got, err := NewClockEventsCollector(tt.args.counterList, tt.args.hostname, tt.args.config,
				*tt.args.deviceWatchList)
			want := tt.want(tt.args.hostname, tt.args.config, *tt.args.deviceWatchList)

			if !tt.wantErr {
				assert.NoError(t, err, "unexpected error")

				wantAttrs := testutils.GetFields(&want.(*clockEventsCollector).expCollector, testutils.Fields)
				gotAttrs := testutils.GetFields(&got.(*clockEventsCollector).expCollector, testutils.Fields)
				assert.Equal(t, wantAttrs, gotAttrs, "unexpected result")

				gotFuncAttrs := testutils.GetFields(&got.(*clockEventsCollector).expCollector, testutils.Functions)
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

func clockEventMetricsCreator(
	counter counters.Counter, gpuID uint, value, hostname, mockFieldName,
	mockFieldLabelValue string, mockClockEvent uint64, useOldNamespace bool,
) Metric {
	uuid := "UUID"
	if useOldNamespace {
		uuid = "uuid"
	}

	labels := map[string]string{
		windowSizeInMSLabel: "0",
		mockFieldName:       mockFieldLabelValue,
	}

	if mockClockEvent != invalidClockEventValue {
		labels["clock_event"] = clockEventBitmask(mockClockEvent).String()
	}

	return Metric{
		Counter:       counter,
		Value:         value,
		GPU:           fmt.Sprintf("%d", gpuID),
		GPUUUID:       "",
		GPUDevice:     fmt.Sprintf("nvidia%d", gpuID),
		GPUModelName:  "",
		UUID:          uuid,
		MigProfile:    "",
		GPUInstanceID: "",
		Hostname:      hostname,
		Labels:        labels,
		Attributes:    map[string]string{},
	}
}

func sortClockEventMetrics(metrics []Metric) {
	slices.SortFunc(metrics, func(a, b Metric) int {
		if a.GPU < b.GPU {
			return -1
		} else if a.GPU == b.GPU {
			if a.Labels["clock_event"] < b.Labels["clock_event"] {
				return -1
			}
		}
		return 1
	})
}

func Test_clockEventsCollector_GetMetrics(t *testing.T) {
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
	mockDCGMExpClockEventsCounter := counters.Counter{
		FieldID:   1,
		FieldName: counters.DCGMExpClockEventsCount,
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
	cleanupCalled := 0
	mockCleanups := []func(){
		func() {
			cleanupCalled++
		},
	}

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
			name: "clock events collector with single clock events",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMExpClockEventsCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				sampleConfig := appconfig.Config{UseOldNamespace: true}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewClockEventsCollector(counterList, mockHostname, &sampleConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityID: gpuID2, Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					mockFieldGroupHandle,
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
				mockClockOutput11 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP)
				mockClockOutput12 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL)

				mockClockOutput21 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE)
				mockClockOutput22 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL)

				return MetricsByCounter{
					mockDCGMExpClockEventsCounter: []Metric{
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput11, true),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput12, true),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput21, true),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput22, true),
					},
				}, byte(mockClockOutput11 + mockClockOutput12), byte(mockClockOutput21 + mockClockOutput22)
			},
			wantErr: false,
		},
		{
			name: "extra values from GPUs that are not monitored",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMExpClockEventsCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewClockEventsCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityID: gpuID2, Value: [4096]byte{gpu2Value}},
					{EntityID: uint(2), Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					mockFieldGroupHandle,
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
				mockClockOutput11 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP)
				mockClockOutput12 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL)

				mockClockOutput21 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE)
				mockClockOutput22 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL)

				return MetricsByCounter{
					mockDCGMExpClockEventsCounter: []Metric{
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput11, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput12, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput21, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput22, false),
					},
				}, byte(mockClockOutput11 + mockClockOutput12), byte(mockClockOutput21 + mockClockOutput22)
			},
			wantErr: false,
		},
		{
			name: "missing values for a GPU that is monitored",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMExpClockEventsCounter,
					mockOtherCounter,
					mockLabelCounter,
				}

				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[3] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				mockGPUDeviceInfoTemp := testutils.MockGPUDeviceInfo(ctrl, 4, gpuInstanceInfos)
				mockGPUDeviceInfoTemp.EXPECT().GOpts().Return(gOpts).AnyTimes()

				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfoTemp, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewClockEventsCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityID: gpuID2, Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					mockFieldGroupHandle,
					mockCleanups, nil)

				mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				mockDCGM.EXPECT().GetValuesSince(mockGroupHandle1, mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID1,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID2,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(2),
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU_I, uint(14),
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, nil)
			},
			want: func() (MetricsByCounter, byte, byte) {
				mockClockOutput11 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP)
				mockClockOutput12 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL)

				mockClockOutput21 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE)
				mockClockOutput22 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL)

				migClockEvent := clockEventMetricsCreator(mockDCGMExpClockEventsCounter, uint(3), "0", mockHostname,
					mockFieldName,
					mockLabelValue, invalidClockEventValue, false)
				migClockEvent.MigProfile = testutils.MockGPUInstanceInfo2.ProfileName
				migClockEvent.GPUInstanceID = fmt.Sprintf("%d", testutils.MockGPUInstanceInfo2.Info.NvmlInstanceId)

				return MetricsByCounter{
					mockDCGMExpClockEventsCounter: []Metric{
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput11, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput12, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput21, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "1", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput22, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, uint(2), "0", mockHostname,
							mockFieldName,
							mockLabelValue, invalidClockEventValue, false),
						migClockEvent,
					},
				}, byte(mockClockOutput11 + mockClockOutput12), byte(mockClockOutput21 + mockClockOutput22)
			},
			wantErr: false,
		},
		{
			name: "clock events collector with multiple clock events",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMExpClockEventsCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewClockEventsCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityID: gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityID: gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityID: gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityID: gpuID2, Value: [4096]byte{gpu2Value}},
					{EntityID: gpuID2, Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					mockFieldGroupHandle,
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
				mockClockOutput11 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP)
				mockClockOutput12 := uint64(DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL)

				mockClockOutput21 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE)
				mockClockOutput22 := uint64(DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL)

				return MetricsByCounter{
					mockDCGMExpClockEventsCounter: []Metric{
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "3", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput11, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID1, "3", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput12, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "2", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput21, false),
						clockEventMetricsCreator(mockDCGMExpClockEventsCounter, gpuID2, "2", mockHostname,
							mockFieldName,
							mockLabelValue, mockClockOutput22, false),
					},
				}, byte(mockClockOutput11 + mockClockOutput12), byte(mockClockOutput21 + mockClockOutput22)
			},
			wantErr: false,
		},
		{
			name: "clock events collector with UpdateAllFields() error",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMExpClockEventsCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewClockEventsCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, _, _ byte) {
				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					mockFieldGroupHandle,
					mockCleanups, nil)

				mockDCGM.EXPECT().UpdateAllFields().Return(fmt.Errorf("some error"))
			},
			want: func() (MetricsByCounter, byte, byte) {
				return nil, 0, 0
			},
			wantErr: true,
		},
		{
			name: "clock events collector with GetValuesSince() error",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMExpClockEventsCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewClockEventsCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, _, _ byte) {
				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					mockFieldGroupHandle,
					mockCleanups, nil)

				mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				mockDCGM.EXPECT().GetValuesSince(mockGroupHandle1, mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return([]dcgm.FieldValue_v2{}, time.Time{},
					fmt.Errorf("some error"))
			},
			want: func() (MetricsByCounter, byte, byte) {
				return nil, 0, 0
			},
			wantErr: true,
		},
		{
			name: "clock events collector with EntityGetLatestValues() error",
			collector: func() Collector {
				counterList := counters.CounterList{
					mockDCGMExpClockEventsCounter,
					mockOtherCounter,
					mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, mockDeviceFields,
					[]dcgm.Short{mockLabelDeviceField}, mockDeviceWatcher, mockCollectorInterval)

				collector, _ := NewClockEventsCollector(counterList, mockHostname, &mockConfig, *deviceWatchList)
				return collector
			},
			conditions: func(watcher *mockdevicewatcher.MockWatcher, _, _ byte) {
				watcher.EXPECT().WatchDeviceFields(gomock.Any(), gomock.Any(),
					gomock.Any()).Return([]dcgm.GroupHandle{mockGroupHandle1},
					mockFieldGroupHandle,
					mockCleanups, nil)

				mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				mockDCGM.EXPECT().GetValuesSince(mockGroupHandle1, mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return([]dcgm.FieldValue_v2{}, time.Time{}, nil)
				mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, gpuID1,
					[]dcgm.Short{mockLabelDeviceField}).Return(mockLatestValues, fmt.Errorf("some error"))
			},
			want: func() (MetricsByCounter, byte, byte) {
				return nil, 0, 0
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, gpu1Value, gpu2Value := tt.want()
			tt.conditions(mockDeviceWatcher, gpu1Value, gpu2Value)
			c := tt.collector()
			defer func() {
				c.Cleanup()
				assert.Equal(t, 1, cleanupCalled, "clean up function was not called")
				cleanupCalled = 0 // reset to zero
			}()

			got, err := c.GetMetrics()

			if !tt.wantErr {
				assert.NoError(t, err, "GetMetrics() failed")
				assert.NotEmpty(t, got, "GetMetrics() returned no metrics")

				wantMetrics := want[mockDCGMExpClockEventsCounter]
				gotMetrics := got[mockDCGMExpClockEventsCounter]

				assert.Len(t, gotMetrics, len(wantMetrics), "GetMetrics() returned wrong number of metrics")

				sortClockEventMetrics(wantMetrics)
				sortClockEventMetrics(gotMetrics)

				assert.Equalf(t, wantMetrics, gotMetrics, "GetMetrics()")
			} else {
				assert.Errorf(t, err, "GetMetrics() did not return expected error")
				assert.Empty(t, got, "GetMetrics() returned unexpected metrics")
			}
		})
	}
}
