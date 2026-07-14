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

package collector

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdevicewatcher "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestIsDCGMExpClockEventsTotalEnabled(t *testing.T) {
	tests := []struct {
		name string
		arg  counters.CounterList
		want bool
	}{
		{name: "empty", arg: counters.CounterList{}, want: false},
		{
			name: "unrelated exporter counter",
			arg:  counters.CounterList{{FieldName: counters.DCGMExpXIDErrorsCount}},
			want: false,
		},
		{
			name: "enabled",
			arg:  counters.CounterList{{FieldName: counters.DCGMExpClockEventsTotal}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsDCGMExpClockEventsTotalEnabled(tt.arg))
		})
	}
}

func TestClockEventsTotalCollectorCountsActivationEdges(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE|DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING), 0),
		clockTotalValue(0, 0, 0),
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
	}, time.Unix(10, 0), nil)

	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[clockEventsTotalCounter()]

	assert.Equal(t, "1", metricValueByLabel(t, metrics, "clock_event", "gpu_idle"))
	assert.Equal(t, "1", metricValueByLabel(t, metrics, "clock_event", "clocks_setting"))
	for _, metric := range metrics {
		assert.NotContains(t, metric.Labels, windowSizeInMSLabel)
	}
}

func TestClockEventsTotalCollectorCountsTransitionMatrix(t *testing.T) {
	idle := int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE)
	clocksSetting := int64(DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING)

	tests := []struct {
		name    string
		samples []dcgm.FieldValue_v2
		want    map[string]string
	}{
		{
			name: "0 to A counts A",
			samples: []dcgm.FieldValue_v2{
				clockTotalValue(0, 0, 0),
				clockTotalValue(0, idle, 0),
			},
			want: map[string]string{"gpu_idle": "1"},
		},
		{
			name: "A to A does not count again",
			samples: []dcgm.FieldValue_v2{
				clockTotalValue(0, idle, 0),
				clockTotalValue(0, idle, 0),
			},
			want: map[string]string{},
		},
		{
			name: "A to A plus B counts only B",
			samples: []dcgm.FieldValue_v2{
				clockTotalValue(0, idle, 0),
				clockTotalValue(0, idle|clocksSetting, 0),
			},
			want: map[string]string{"clocks_setting": "1"},
		},
		{
			name: "A plus B to B does not count again",
			samples: []dcgm.FieldValue_v2{
				clockTotalValue(0, idle|clocksSetting, 0),
				clockTotalValue(0, clocksSetting, 0),
			},
			want: map[string]string{},
		},
		{
			name: "B to A plus B counts only A",
			samples: []dcgm.FieldValue_v2{
				clockTotalValue(0, clocksSetting, 0),
				clockTotalValue(0, idle|clocksSetting, 0),
			},
			want: map[string]string{"gpu_idle": "1"},
		},
		{
			name: "blank and failed samples do not clear previous state",
			samples: []dcgm.FieldValue_v2{
				clockTotalValue(0, idle, 0),
				clockTotalValue(0, 0, -1),
				clockTotalValue(0, dcgm.DCGM_FT_INT64_BLANK, 0),
				clockTotalValue(0, idle, 0),
				clockTotalValue(0, 0, 0),
				clockTotalValue(0, idle, 0),
			},
			want: map[string]string{"gpu_idle": "1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockDCGM := setMockDCGMClient(t, ctrl)
			collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
			defer collector.Cleanup()

			mockDCGM.EXPECT().UpdateAllFields().Return(nil)
			mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return(tt.samples, time.Unix(10, 0), nil)

			require.NoError(t, collector.collectNewEvents())

			got, err := collector.GetMetrics()
			require.NoError(t, err)
			metrics := got[clockEventsTotalCounter()]

			require.Len(t, metrics, len(tt.want))
			for label, value := range tt.want {
				assert.Equal(t, value, metricValueByLabel(t, metrics, "clock_event", label))
			}
		})
	}
}

func TestClockEventsTotalCollectorPrimesMultiBitFirstObservationWithoutCounting(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE|DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING), 0),
	}, time.Unix(10, 0), nil)

	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[clockEventsTotalCounter()]

	assert.Len(t, metrics, 0)
}

func TestClockEventsTotalCollectorUsesReturnedCursor(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	nextSince := time.Unix(123, 0)
	gomock.InOrder(
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
			clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
		}, nextSince, nil),
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, nextSince).Return([]dcgm.FieldValue_v2{
			clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE|DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING), 0),
		}, time.Unix(456, 0), nil),
	)

	require.NoError(t, collector.collectNewEvents())
	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[clockEventsTotalCounter()]

	require.Len(t, metrics, 1)
	assert.Equal(t, "1", metricValueByLabel(t, metrics, "clock_event", "clocks_setting"))
}

func TestClockEventsTotalCollectorScrapeDoesNotCallGetValuesSince(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP), 0),
	}, time.Unix(10, 0), nil)

	require.NoError(t, collector.collectNewEvents())

	first, err := collector.GetMetrics()
	require.NoError(t, err)
	second, err := collector.GetMetrics()
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestClockEventsTotalCollectorGetValuesSinceErrorIncludesPollContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	since := time.Unix(123, 0)
	collector.stateMu.Lock()
	collector.cursors[group] = since
	collector.stateMu.Unlock()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, since).Return(nil, time.Time{}, errors.New("dcgm unavailable"))

	err := collector.collectNewEvents()

	require.Error(t, err)
	assert.ErrorContains(t, err, "group_handle=1")
	assert.ErrorContains(t, err, "field_group_handle=1")
	assert.ErrorContains(t, err, "since_timestamp=1970-01-01T00:02:03Z")
}

func TestClockEventsTotalCollectorPollErrorsDoNotMutateState(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	stableCursor := time.Unix(10, 0)
	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, 0, 0),
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
	}, stableCursor, nil)
	require.NoError(t, collector.collectNewEvents())

	wantTotals := collector.snapshotTotals()
	wantPrevious := clockPreviousSnapshot(collector)
	require.True(t, stableCursor.Equal(collector.cursorForGroup(group)))

	mockDCGM.EXPECT().UpdateAllFields().Return(errors.New("update failed"))
	err := collector.collectNewEvents()
	require.Error(t, err)
	assert.Equal(t, wantTotals, collector.snapshotTotals())
	assert.Equal(t, wantPrevious, clockPreviousSnapshot(collector))
	assert.True(t, stableCursor.Equal(collector.cursorForGroup(group)))

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, stableCursor).Return(nil, time.Time{}, errors.New("get failed"))
	err = collector.collectNewEvents()
	require.Error(t, err)
	assert.Equal(t, wantTotals, collector.snapshotTotals())
	assert.Equal(t, wantPrevious, clockPreviousSnapshot(collector))
	assert.True(t, stableCursor.Equal(collector.cursorForGroup(group)))
}

func TestClockEventsTotalCollectorConcurrentCollectAndScrape(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil).AnyTimes()
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, gomock.Any()).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, 0, 0),
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
	}, time.Unix(10, 0), nil).AnyTimes()

	var wg sync.WaitGroup
	errCh := make(chan error, 100)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 50 {
			if err := collector.collectNewEvents(); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			if _, err := collector.GetMetrics(); err != nil {
				errCh <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}

func TestClockEventsTotalCollectorCollectsMultipleGroupsAndGPUs(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	group1 := testGroupHandle(1)
	group2 := testGroupHandle(2)
	collector, fieldGroup := newTestClockEventsTotalCollectorWithGroups(t, ctrl, 2, []dcgm.GroupHandle{group1, group2}, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group1, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, 0, 0),
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
	}, time.Unix(10, 0), nil)
	mockDCGM.EXPECT().GetValuesSince(group2, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(1, 0, 0),
		clockTotalValue(1, int64(DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING), 0),
	}, time.Unix(20, 0), nil)

	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[clockEventsTotalCounter()]
	require.Len(t, metrics, 2)
	assert.Equal(t, "1", metricValueByGPUAndLabel(t, metrics, "0", "clock_event", "gpu_idle"))
	assert.Equal(t, "1", metricValueByGPUAndLabel(t, metrics, "1", "clock_event", "clocks_setting"))
}

func TestClockEventsTotalCollectorRestartStartsWithEmptyState(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	first, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, first.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, 0, 0),
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
	}, time.Unix(10, 0), nil)
	require.NoError(t, first.collectNewEvents())
	got, err := first.GetMetrics()
	require.NoError(t, err)
	require.Len(t, got[clockEventsTotalCounter()], 1)
	first.Cleanup()

	second, group, fieldGroup := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer second.Cleanup()
	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, second.initialSince).Return([]dcgm.FieldValue_v2{
		clockTotalValue(0, int64(DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE), 0),
	}, time.Unix(20, 0), nil)
	require.NoError(t, second.collectNewEvents())
	got, err = second.GetMetrics()
	require.NoError(t, err)
	assert.Empty(t, got[clockEventsTotalCounter()])
}

func TestClockEventsTotalCollectorInitialCursorStartsAtCollectorCreation(t *testing.T) {
	ctrl := gomock.NewController(t)
	setMockDCGMClient(t, ctrl)

	before := time.Now()
	collector, _, _ := newTestClockEventsTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()
	after := time.Now()

	require.False(t, collector.initialSince.IsZero())
	assert.False(t, collector.initialSince.Before(before))
	assert.False(t, collector.initialSince.After(after))
}

func TestClockEventsTotalCollectorCleanupStopsPollerBeforeWatchCleanup(t *testing.T) {
	ctrl := gomock.NewController(t)
	setMockDCGMClient(t, ctrl)

	cleanupCalled := make(chan struct{}, 1)
	var collector *clockEventsTotalCollector
	collector, _, _ = newTestClockEventsTotalCollector(t, ctrl, []func(){
		func() {
			select {
			case <-collector.poller.doneCh:
			default:
				t.Fatal("watch cleanup ran before poller stopped")
			}
			cleanupCalled <- struct{}{}
		},
	})

	collector.Cleanup()

	select {
	case <-cleanupCalled:
	case <-time.After(time.Second):
		t.Fatal("watch cleanup was not called")
	}
}

func TestClockEventsTotalCollectorCleanupIsIdempotent(t *testing.T) {
	ctrl := gomock.NewController(t)
	setMockDCGMClient(t, ctrl)

	cleanupCalls := 0
	collector, _, _ := newTestClockEventsTotalCollector(t, ctrl, []func(){
		func() {
			cleanupCalls++
		},
	})

	collector.Cleanup()
	collector.Cleanup()

	assert.Equal(t, 1, cleanupCalls)
}

func newTestClockEventsTotalCollector(
	t *testing.T, ctrl *gomock.Controller, cleanups []func(),
) (*clockEventsTotalCollector, dcgm.GroupHandle, dcgm.FieldHandle) {
	t.Helper()

	group := testGroupHandle(1)
	collector, fieldGroup := newTestClockEventsTotalCollectorWithGroups(t, ctrl, 1, []dcgm.GroupHandle{group}, cleanups)
	return collector, group, fieldGroup
}

func newTestClockEventsTotalCollectorWithGroups(
	t *testing.T, ctrl *gomock.Controller, gpuCount int, groups []dcgm.GroupHandle, cleanups []func(),
) (*clockEventsTotalCollector, dcgm.FieldHandle) {
	t.Helper()

	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)
	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, gpuCount, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	fieldGroup := testFieldHandle(1)
	mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).
		Return(groups, []dcgm.FieldHandle{fieldGroup}, cleanups, nil)

	counterList := counters.CounterList{clockEventsTotalCounter()}
	deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, nil, nil, mockDeviceWatcher, int64(time.Hour/time.Second))

	collector, err := NewClockEventsTotalCollector(counterList, "localhost", &appconfig.Config{CollectInterval: int(time.Hour / time.Millisecond)}, *deviceWatchList)
	require.NoError(t, err)

	return collector.(*clockEventsTotalCollector), fieldGroup
}

func clockEventsTotalCounter() counters.Counter {
	return counters.Counter{
		FieldID:   dcgm.Short(counters.DCGMClockEventsTotal),
		FieldName: counters.DCGMExpClockEventsTotal,
		PromType:  "counter",
		Help:      "clock total",
	}
}

func clockTotalValue(gpuID uint, value int64, status int) dcgm.FieldValue_v2 {
	return dcgm.FieldValue_v2{
		EntityGroupId: dcgm.FE_GPU,
		EntityID:      gpuID,
		FieldID:       dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		FieldType:     dcgm.DCGM_FT_INT64,
		Status:        status,
		Value:         createInt64ByteArray(value),
	}
}

func clockPreviousSnapshot(c *clockEventsTotalCollector) map[dcgm.GroupEntityPair]clockEventBitmask {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	previous := make(map[dcgm.GroupEntityPair]clockEventBitmask, len(c.previous))
	for entity, bitmask := range c.previous {
		previous[entity] = bitmask
	}
	return previous
}
