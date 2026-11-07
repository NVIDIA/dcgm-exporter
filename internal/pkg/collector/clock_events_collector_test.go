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
	"cmp"
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

const invalidClockEventValue = 10000

// TestIsDCGMExpClockEventsCountEnabled checks that the helper returns true when the clock events count
// counter is in the list, and false for an empty list or a list of unrelated counters.
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

// TestNewClockEventsCollector checks that construction fails without the clock events count counter or when
// the field watch fails. Otherwise the collector watches the clock event reasons field, keeps the label
// counters, the DCGM client and the configured window, and has its parser, label filler and clock set.
func TestNewClockEventsCollector(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	previousClient := dcgmprovider.Client()
	dcgmprovider.SetClient(mockDCGM)
	t.Cleanup(func() { dcgmprovider.SetClient(previousClient) })

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

	// args supplies constructor inputs for each clock-events collector case.
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
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return(nil,
					nil,
					sampleCleanups, nil)
			},
			want: func(
				hostname string, config *appconfig.Config,
				deviceWatchList devicewatchlistmanager.WatchList,
			) Collector {
				deviceWatchList.SetDeviceFieldsForPhysicalGPUs([]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})
				return &clockEventsCollector{
					expCollector{
						baseExpCollector: baseExpCollector{
							deviceWatchList: deviceWatchList,
							counter:         sampleDCGMExpClockEventsCounter,
							labelsCounters:  []counters.Counter{sampleLabelCounter},
							hostname:        hostname,
							config:          config,
							watchResult:     watchResultFromCleanups(sampleCleanups),
						},
						sourceFields: map[dcgm.Short]string{
							dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
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
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return(nil,
					nil,
					sampleCleanups, nil)
			},
			want: func(
				hostname string, config *appconfig.Config,
				deviceWatchList devicewatchlistmanager.WatchList,
			) Collector {
				deviceWatchList.SetDeviceFieldsForPhysicalGPUs([]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})
				return &clockEventsCollector{
					expCollector{
						baseExpCollector: baseExpCollector{
							deviceWatchList: deviceWatchList,
							counter:         sampleDCGMExpClockEventsCounter,
							labelsCounters:  nil,
							hostname:        hostname,
							config:          config,
							watchResult:     watchResultFromCleanups(sampleCleanups),
						},
						sourceFields: map[dcgm.Short]string{
							dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
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
				require.NoError(t, err, "unexpected error")

				wantCollector := want.(*clockEventsCollector)
				gotCollector := got.(*clockEventsCollector)
				assert.Same(t, mockDCGM, gotCollector.client)
				assert.Equal(t, wantCollector.deviceWatchList, gotCollector.deviceWatchList)
				assert.Equal(t, wantCollector.counter, gotCollector.counter)
				assert.Equal(t, wantCollector.labelsCounters, gotCollector.labelsCounters)
				assert.Equal(t, wantCollector.hostname, gotCollector.hostname)
				assert.Equal(t, wantCollector.config, gotCollector.config)
				require.NotNil(t, gotCollector.watchResult)
				assert.Equal(t, wantCollector.watchResult.Bindings(), gotCollector.watchResult.Bindings())
				assert.Equal(t, wantCollector.sourceFields, gotCollector.sourceFields)
				assert.Equal(t, wantCollector.windowSize, gotCollector.windowSize)
				assert.NotNil(t, gotCollector.fieldValueParser)
				assert.NotNil(t, gotCollector.labelFiller)
			} else {
				assert.Error(t, err, "expected error")
				assert.Equal(t, want, got, "unexpected result")
			}
		})
	}
}

// clockEventMetric builds one expected clock-event metric from the shared fixture values.
func (fixture *clockEventsGetMetricsFixture) clockEventMetric(
	gpuID uint, value string, event clockEventBitmask, useOldNamespace bool,
) Metric {
	uuid := "UUID"
	if useOldNamespace {
		uuid = "uuid"
	}

	labels := map[string]string{
		WindowSizeInMSLabel:   "0",
		fixture.mockFieldName: fixture.mockLabelValue,
	}

	if event != invalidClockEventValue {
		labels["clock_event"] = event.String()
	}

	return Metric{
		Counter:       fixture.mockDCGMExpClockEventsCounter,
		Value:         value,
		GPU:           fmt.Sprintf("%d", gpuID),
		GPUUUID:       "",
		GPUDevice:     fmt.Sprintf("nvidia%d", gpuID),
		GPUModelName:  "",
		UUID:          uuid,
		MigProfile:    "",
		GPUInstanceID: "",
		Hostname:      fixture.mockHostname,
		Labels:        labels,
		Attributes:    map[string]string{},
	}
}

// sortClockEventMetrics orders metrics by GPU, then by clock_event label. Cases
// must not repeat a GPU and reason pair, since the order of equal metrics is
// unspecified.
func sortClockEventMetrics(metrics []Metric) {
	slices.SortFunc(metrics, func(a, b Metric) int {
		return cmp.Or(cmp.Compare(a.GPU, b.GPU), cmp.Compare(a.Labels["clock_event"], b.Labels["clock_event"]))
	})
}

// clockEventsGetMetricsFixture holds the mocks shared by the clock-event result cases.
type clockEventsGetMetricsFixture struct {
	// ctrl owns the mocks for one subtest and backs any additional device-info mock.
	ctrl                          *gomock.Controller
	mockDCGM                      *mockdcgm.MockDCGM
	mockDeviceWatcher             *mockdevicewatcher.MockWatcher
	mockDCGMExpClockEventsCounter counters.Counter
	mockOtherCounter              counters.Counter
	mockLabelDeviceField          dcgm.Short
	mockFieldName                 string
	mockLabelValue                string
	mockLabelCounter              counters.Counter
	gOpts                         appconfig.DeviceOptions
	mockGPUDeviceInfo             deviceinfo.Provider
	gpuID1                        uint
	gpuID2                        uint
	mockDeviceFields              []dcgm.Short
	mockCollectorInterval         int64
	mockConfig                    appconfig.Config
	mockHostname                  string
	cleanupCalled                 int
	mockCleanups                  []func()
	mockGroupHandle1              dcgm.GroupHandle
	mockFieldGroupHandle          dcgm.FieldHandle
	mockLatestValues              []dcgm.FieldValue_v1
}

// clockEventsGetMetricsCase states one input result, its mock calls, and its expected metrics.
type clockEventsGetMetricsCase struct {
	name string
	// collector builds the collector with the counters and device topology for this case.
	collector func(*clockEventsGetMetricsFixture) (Collector, error)
	// conditions installs the watcher and DCGM calls. Its sampled entities report
	// the two samples values; any other entity's value is written in conditions.
	conditions func(*clockEventsGetMetricsFixture, *mockdevicewatcher.MockWatcher, byte, byte)
	// samples are the reason bits passed to conditions for the case's first two
	// sampled entities.
	samples [2]byte
	// want returns the expected metrics.
	want func(*clockEventsGetMetricsFixture) MetricsByCounter
	// wantErr marks cases where GetMetrics must fail without returning metrics.
	wantErr bool
}

// Test_clockEventsCollector_GetMetrics checks windowed metrics for ordinary, MIG, and error cases.
func Test_clockEventsCollector_GetMetrics(t *testing.T) {
	t.Run("GPU entity event counts", func(t *testing.T) {
		runClockEventsGetMetricsCases(t, clockEventGPUCases())
	})
	t.Run("MIG and entity-group results", func(t *testing.T) {
		runClockEventsGetMetricsCases(t, clockEventMIGAndEntityCases())
	})
	t.Run("DCGM query errors", func(t *testing.T) {
		runClockEventsGetMetricsCases(t, clockEventQueryErrorCases())
	})
}

// newClockEventsGetMetricsFixture creates the DCGM, watcher, counter, and device-info mocks for this test.
func newClockEventsGetMetricsFixture(t *testing.T) *clockEventsGetMetricsFixture {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)

	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() {
		dcgmprovider.SetClient(realDCGM)
	})
	dcgmprovider.SetClient(mockDCGM)

	fixture := &clockEventsGetMetricsFixture{
		ctrl:              ctrl,
		mockDCGM:          mockDCGM,
		mockDeviceWatcher: mockDeviceWatcher,
		mockDCGMExpClockEventsCounter: counters.Counter{
			FieldID:   1,
			FieldName: counters.DCGMExpClockEventsCount,
		},
		mockOtherCounter: counters.Counter{
			FieldID:   2,
			FieldName: "random2",
		},
		mockLabelDeviceField:  dcgm.Short(3),
		mockFieldName:         "random3",
		mockLabelValue:        "this is mock label",
		gOpts:                 appconfig.DeviceOptions{Flex: true},
		gpuID1:                uint(0),
		gpuID2:                uint(1),
		mockDeviceFields:      []dcgm.Short{42},
		mockCollectorInterval: int64(1),
		mockConfig:            appconfig.Config{},
		mockHostname:          "localhost",
	}
	fixture.mockLabelCounter = counters.Counter{
		FieldID:   fixture.mockLabelDeviceField,
		FieldName: fixture.mockFieldName,
		PromType:  "label",
	}
	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, 2, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(fixture.gOpts).AnyTimes()
	fixture.mockGPUDeviceInfo = mockGPUDeviceInfo
	mockGroupHandle1 := dcgm.GroupHandle{}
	mockGroupHandle1.SetHandle(uintptr(1))
	fixture.mockGroupHandle1 = mockGroupHandle1

	mockFieldGroupHandle := dcgm.FieldHandle{}
	mockFieldGroupHandle.SetHandle(uintptr(1))
	fixture.mockFieldGroupHandle = mockFieldGroupHandle

	fixture.mockLatestValues = []dcgm.FieldValue_v1{
		{
			FieldID:   150,
			FieldType: dcgm.DCGM_FT_INT64,
			Value:     [4096]byte{42},
		},
		{
			FieldID:   fixture.mockLabelDeviceField,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray(fixture.mockLabelValue),
		},
		{
			FieldID:   fixture.mockLabelDeviceField,
			FieldType: dcgm.DCGM_FT_STRING,
			Value:     testutils.StrToByteArray(dcgm.DCGM_FT_STR_NOT_FOUND),
		},
	}
	// Cleanup increments this counter so every successful case proves collector teardown ran.
	fixture.mockCleanups = []func(){
		func() {
			fixture.cleanupCalled++
		},
	}
	return fixture
}

// clockEventGPUCases defines the GPU entity event counts cases for GetMetrics.
func clockEventGPUCases() []clockEventsGetMetricsCase {
	tests := []clockEventsGetMetricsCase{
		{
			name: "clock events collector with single clock events",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}
				sampleConfig := appconfig.Config{UseOldNamespace: true}
				deviceWatchList := devicewatchlistmanager.NewWatchList(fixture.mockGPUDeviceInfo, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &sampleConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID2, Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID1,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID2,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
			},
			samples: [2]byte{
				byte(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP | DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL),
				byte(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE | DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				return MetricsByCounter{
					fixture.mockDCGMExpClockEventsCounter: []Metric{
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP, true),
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL, true),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE, true),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL, true),
					},
				}
			},
			wantErr: false,
		},
		{
			name: "extra values from GPUs that are not monitored",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(fixture.mockGPUDeviceInfo, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID2, Value: [4096]byte{gpu2Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: uint(2), Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID1,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID2,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
			},
			samples: [2]byte{
				byte(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP | DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL),
				byte(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE | DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				return MetricsByCounter{
					fixture.mockDCGMExpClockEventsCounter: []Metric{
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP, false),
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL, false),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE, false),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL, false),
					},
				}
			},
			wantErr: false,
		},
		{
			name: "clock events collector with multiple clock events",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(fixture.mockGPUDeviceInfo, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID2, Value: [4096]byte{gpu2Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID2, Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID1,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID2,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
			},
			samples: [2]byte{
				byte(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP | DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL),
				byte(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE | DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				return MetricsByCounter{
					fixture.mockDCGMExpClockEventsCounter: []Metric{
						fixture.clockEventMetric(fixture.gpuID1, "3", DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP, false),
						fixture.clockEventMetric(fixture.gpuID1, "3", DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL, false),
						fixture.clockEventMetric(fixture.gpuID2, "2", DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE, false),
						fixture.clockEventMetric(fixture.gpuID2, "2", DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL, false),
					},
				}
			},
			wantErr: false,
		},
	}
	return tests
}

// clockEventMIGAndEntityCases defines the MIG and entity-group results cases for GetMetrics.
func clockEventMIGAndEntityCases() []clockEventsGetMetricsCase {
	tests := []clockEventsGetMetricsCase{
		{
			// The clock event reasons field is GPU-level, so the parent GPU of
			// the MIG instance reports its series, without instance labels.
			name: "parent GPU of a MIG instance with no events in window returns 0",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}

				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[3] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				mockGPUDeviceInfoTemp := testutils.MockGPUDeviceInfo(fixture.ctrl, 4, gpuInstanceInfos)
				mockGPUDeviceInfoTemp.EXPECT().GOpts().Return(fixture.gOpts).AnyTimes()

				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfoTemp, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID2, Value: [4096]byte{gpu2Value}},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID1,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID2,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(2),
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(3),
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
			},
			samples: [2]byte{
				byte(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP | DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL),
				byte(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE | DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				migClockEvent := fixture.clockEventMetric(uint(3), "0", invalidClockEventValue, false)

				return MetricsByCounter{
					fixture.mockDCGMExpClockEventsCounter: []Metric{
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP, false),
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL, false),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE, false),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL, false),
						fixture.clockEventMetric(uint(2), "0", invalidClockEventValue, false),
						migClockEvent,
					},
				}
			},
			wantErr: false,
		},
		{
			name: "parent GPU of a MIG instance with events in window returns count",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}

				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[3] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo2}

				mockGPUDeviceInfoTemp := testutils.MockGPUDeviceInfo(fixture.ctrl, 4, gpuInstanceInfos)
				mockGPUDeviceInfoTemp.EXPECT().GOpts().Return(fixture.gOpts).AnyTimes()

				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfoTemp, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, gpu1Value, gpu2Value byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpu1Value}},
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID2, Value: [4096]byte{gpu2Value}},
					{
						EntityGroupId: dcgm.FE_GPU,
						EntityID:      uint(3),
						Value:         [4096]byte{byte(DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL)},
					},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID1,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID2,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(2),
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, uint(3),
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
			},
			samples: [2]byte{
				byte(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP | DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL),
				byte(DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE | DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL),
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				mockMIGClockOutput := DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL
				migClockEvent := fixture.clockEventMetric(uint(3), "1", mockMIGClockOutput, false)

				return MetricsByCounter{
					fixture.mockDCGMExpClockEventsCounter: []Metric{
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP, false),
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL, false),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE, false),
						fixture.clockEventMetric(fixture.gpuID2, "1", DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL, false),
						fixture.clockEventMetric(uint(2), "0", invalidClockEventValue, false),
						migClockEvent,
					},
				}
			},
			wantErr: false,
		},
		{
			// A (FE_GPU_I, 0) sample must not be counted for (FE_GPU, 0). GPU
			// instances are not monitored for this GPU-level field, so their
			// samples are ignored and GPU 1 reports as the parent GPU.
			name: "GPU instance sample with a GPU's entity ID is not conflated",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}

				gpuInstanceInfos := make(map[int][]deviceinfo.GPUInstanceInfo)
				gpuInstanceInfos[1] = []deviceinfo.GPUInstanceInfo{testutils.MockGPUInstanceInfo1}

				mockGPUDeviceInfoTemp := testutils.MockGPUDeviceInfo(fixture.ctrl, 2, gpuInstanceInfos)
				mockGPUDeviceInfoTemp.EXPECT().GOpts().Return(fixture.gOpts).AnyTimes()

				deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfoTemp, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, gpuValue, giValue byte) {
				mockEntitiesResult := []dcgm.FieldValue_v2{
					{EntityGroupId: dcgm.FE_GPU, EntityID: fixture.gpuID1, Value: [4096]byte{gpuValue}},
					{
						EntityGroupId: dcgm.FE_GPU_I,
						EntityID:      testutils.MockGPUInstanceInfo1.EntityId,
						Value:         [4096]byte{giValue},
					},
				}

				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return(mockEntitiesResult, time.Time{}, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID1,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID2,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, nil)
			},
			samples: [2]byte{byte(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP), byte(DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL)},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				return MetricsByCounter{
					fixture.mockDCGMExpClockEventsCounter: []Metric{
						fixture.clockEventMetric(fixture.gpuID1, "1", DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP, false),
						fixture.clockEventMetric(fixture.gpuID2, "0", invalidClockEventValue, false),
					},
				}
			},
			wantErr: false,
		},
	}
	return tests
}

// clockEventQueryErrorCases defines the DCGM query errors cases for GetMetrics.
func clockEventQueryErrorCases() []clockEventsGetMetricsCase {
	tests := []clockEventsGetMetricsCase{
		{
			name: "clock events collector with UpdateAllFields() error",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(fixture.mockGPUDeviceInfo, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, _, _ byte) {
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(fmt.Errorf("some error"))
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				return nil
			},
			wantErr: true,
		},
		{
			name: "clock events collector with GetValuesSince() error",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(fixture.mockGPUDeviceInfo, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, _, _ byte) {
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return([]dcgm.FieldValue_v2{}, time.Time{},
					fmt.Errorf("some error"))
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				return nil
			},
			wantErr: true,
		},
		{
			name: "clock events collector with EntityGetLatestValues() error",
			collector: func(fixture *clockEventsGetMetricsFixture) (Collector, error) {
				counterList := counters.CounterList{
					fixture.mockDCGMExpClockEventsCounter,
					fixture.mockOtherCounter,
					fixture.mockLabelCounter,
				}
				deviceWatchList := devicewatchlistmanager.NewWatchList(fixture.mockGPUDeviceInfo, fixture.mockDeviceFields,
					[]dcgm.Short{fixture.mockLabelDeviceField}, fixture.mockDeviceWatcher, fixture.mockCollectorInterval)

				return NewClockEventsCollector(counterList, fixture.mockHostname, &fixture.mockConfig, *deviceWatchList)
			},
			conditions: func(fixture *clockEventsGetMetricsFixture, watcher *mockdevicewatcher.MockWatcher, _, _ byte) {
				watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{fixture.mockGroupHandle1},
					[]dcgm.FieldHandle{fixture.mockFieldGroupHandle},
					fixture.mockCleanups, nil)

				fixture.mockDCGM.EXPECT().UpdateAllFields().Return(nil)
				fixture.mockDCGM.EXPECT().GetValuesSince(fixture.mockGroupHandle1, fixture.mockFieldGroupHandle,
					gomock.AssignableToTypeOf(time.Time{})).Return([]dcgm.FieldValue_v2{}, time.Time{}, nil)
				fixture.mockDCGM.EXPECT().EntityGetLatestValues(dcgm.FE_GPU, fixture.gpuID1,
					[]dcgm.Short{fixture.mockLabelDeviceField}).Return(fixture.mockLatestValues, fmt.Errorf("some error"))
			},
			want: func(fixture *clockEventsGetMetricsFixture) MetricsByCounter {
				return nil
			},
			wantErr: true,
		},
	}
	return tests
}

// runClockEventsGetMetricsCases executes one behavior group and checks results and cleanup.
func runClockEventsGetMetricsCases(t *testing.T, tests []clockEventsGetMetricsCase) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newClockEventsGetMetricsFixture(t)
			want := tt.want(fixture)
			tt.conditions(fixture, fixture.mockDeviceWatcher, tt.samples[0], tt.samples[1])
			c, err := tt.collector(fixture)
			require.NoError(t, err, "construct collector for %s", tt.name)
			require.NotNil(t, c, "collector for %s", tt.name)
			c.(*clockEventsCollector).now = func() time.Time { return time.UnixMicro(123) }
			defer func() {
				c.Cleanup()
				assert.Equal(t, 1, fixture.cleanupCalled, "clean up function was not called")
			}()

			got, err := c.GetMetrics()

			if !tt.wantErr {
				assert.NoError(t, err, "GetMetrics() failed")
				assert.NotEmpty(t, got, "GetMetrics() returned no metrics")

				wantMetrics := want[fixture.mockDCGMExpClockEventsCounter]
				gotMetrics := got[fixture.mockDCGMExpClockEventsCounter]
				for i := range wantMetrics {
					wantMetrics[i].Timestamp = 123
				}

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

// Test_clockEventsCollector_GetMetricsUsesConfiguredWindow checks that GetMetrics asks DCGM for values since now
// minus the configured window, labels the series with that window, and reports 1 with a clock_event label for
// one event or 0 without that label when none were seen.
func Test_clockEventsCollector_GetMetricsUsesConfiguredWindow(t *testing.T) {
	const windowSize = 2750
	now := time.Date(2026, time.September, 11, 12, 0, 0, 0, time.UTC)
	counter := counters.Counter{
		FieldID:   dcgm.Short(counters.DCGMClockEventsCount),
		FieldName: counters.DCGMExpClockEventsCount,
	}

	tests := []struct {
		name           string
		values         []dcgm.FieldValue_v2
		wantCount      string
		wantClockEvent string
		wantEventLabel bool
	}{
		{
			name: "counts an event in the configured window",
			values: []dcgm.FieldValue_v2{{
				EntityGroupId: dcgm.FE_GPU,
				EntityID:      0,
				FieldID:       dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
				FieldType:     dcgm.DCGM_FT_INT64,
				Value:         createInt64ByteArray(int64(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP)),
			}},
			wantCount:      "1",
			wantClockEvent: "power_cap",
			wantEventLabel: true,
		},
		{
			name:      "reports zero when there are no events in the configured window",
			wantCount: "0",
		},
		{
			name: "reports zero when sampled clock reasons are empty",
			values: []dcgm.FieldValue_v2{{
				EntityGroupId: dcgm.FE_GPU,
				EntityID:      0,
				FieldID:       dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
				FieldType:     dcgm.DCGM_FT_INT64,
				Value:         createInt64ByteArray(0),
			}},
			wantCount: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			fieldGroupHandle := dcgm.FieldHandle{}
			fieldGroupHandle.SetHandle(uintptr(1))

			mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).
				Return([]dcgm.GroupHandle{groupHandle}, []dcgm.FieldHandle{fieldGroupHandle}, nil, nil)

			deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, nil, nil, mockDeviceWatcher, 1)
			collector, err := NewClockEventsCollector(
				counters.CounterList{counter},
				"localhost",
				&appconfig.Config{ClockEventsCountWindowSize: windowSize},
				*deviceWatchList,
			)
			require.NoError(t, err)
			collector.(*clockEventsCollector).now = func() time.Time { return now }

			mockDCGM.EXPECT().UpdateAllFields().Return(nil)
			mockDCGM.EXPECT().GetValuesSince(
				groupHandle,
				fieldGroupHandle,
				now.Add(-windowSize*time.Millisecond),
			).Return(tt.values, time.Time{}, nil)

			got, err := collector.GetMetrics()

			require.NoError(t, err)
			require.Len(t, got[counter], 1)
			assert.Equal(t, tt.wantCount, got[counter][0].Value)
			assert.Equal(t, "2750", got[counter][0].Labels[WindowSizeInMSLabel])
			assert.Equal(t, now.UnixMicro(), got[counter][0].Timestamp)
			if tt.wantEventLabel {
				assert.Equal(t, tt.wantClockEvent, got[counter][0].Labels["clock_event"])
			} else {
				assert.NotContains(t, got[counter][0].Labels, "clock_event")
			}
		})
	}
}

// Test_clockEventsCollector_GetMetricsLogsBlankSourceFieldName checks that a blank clock event reasons sample is
// skipped with a debug log naming the source DCGM field, its ID, the entity type, and the entity ID.
func Test_clockEventsCollector_GetMetricsLogsBlankSourceFieldName(t *testing.T) {
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
			FieldID:   dcgm.Short(counters.DCGMClockEventsCount),
			FieldName: counters.DCGMExpClockEventsCount,
		},
	}
	deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, nil, nil, mockDeviceWatcher, 1)

	collector, err := NewClockEventsCollector(counterList, "localhost", &appconfig.Config{}, *deviceWatchList)
	require.NoError(t, err)

	blankValue := createInt64ByteArray(dcgm.DCGM_FT_INT64_BLANK)
	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(mockGroupHandle, mockFieldGroupHandle, gomock.AssignableToTypeOf(time.Time{})).
		Return([]dcgm.FieldValue_v2{
			{
				EntityGroupId: dcgm.FE_GPU,
				EntityID:      0,
				FieldID:       dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
				FieldType:     dcgm.DCGM_FT_INT64,
				Status:        0,
				Value:         blankValue,
			},
		}, time.Time{}, nil)

	_, err = collector.GetMetrics()
	require.NoError(t, err)

	got := findLogRecord(t, buf, blankValueSkippedMessage)
	assert.Equal(t, blankValueSkippedMessage, got["msg"])
	assert.Equal(t, float64(dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS), got["fieldID"])
	assert.Equal(t, "DCGM_FI_DEV_CLOCKS_EVENT_REASONS", got["fieldName"])
	assert.Equal(t, dcgm.FE_GPU.String(), got["entityType"])
	assert.Equal(t, float64(0), got["entityID"])
}
