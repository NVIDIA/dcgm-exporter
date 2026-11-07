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
	"testing/synctest"
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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

// TestIsDCGMExpXIDErrorsTotalEnabled checks that the helper returns true when the XID errors total counter is
// in the list, and false for an empty list or one holding only the clock events count counter.
func TestIsDCGMExpXIDErrorsTotalEnabled(t *testing.T) {
	tests := []struct {
		name string
		arg  counters.CounterList
		want bool
	}{
		{name: "empty", arg: counters.CounterList{}, want: false},
		{
			name: "unrelated exporter counter",
			arg:  counters.CounterList{{FieldName: counters.DCGMExpClockEventsCount}},
			want: false,
		},
		{
			name: "enabled",
			arg:  counters.CounterList{{FieldName: counters.DCGMExpXIDErrorsTotal}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsDCGMExpXIDErrorsTotalEnabled(tt.arg))
		})
	}
}

// TestXIDTotalCollectorCollectsBetweenScrapes checks that XID samples from two background polls add up per XID
// code before a scrape, and the total series carry no window label.
func TestXIDTotalCollectorCollectsBetweenScrapes(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	gomock.InOrder(
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
			xidTotalValue(0, 42, 0),
		}, time.Unix(10, 0), nil),
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, time.Unix(10, 0)).Return([]dcgm.FieldValue_v2{
			xidTotalValue(0, 42, 0),
			xidTotalValue(0, 46, 0),
		}, time.Unix(20, 0), nil),
	)

	require.NoError(t, collector.collectNewEvents())
	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[xidTotalCounter()]

	assert.Equal(t, "2", metricValueByLabel(t, metrics, "xid", "42"))
	assert.Equal(t, "1", metricValueByLabel(t, metrics, "xid", "46"))
	for _, metric := range metrics {
		assert.NotContains(t, metric.Labels, WindowSizeInMSLabel)
		assert.Positive(t, metric.Timestamp)
		assert.Equal(t, collector.initialSince.UnixMicro(), metric.StartTimestamp)
	}
}

// TestXIDTotalCollectorUsesReturnedCursor checks that the second poll asks DCGM for values since the timestamp
// the first poll returned, and its samples add to the earlier totals.
func TestXIDTotalCollectorUsesReturnedCursor(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	nextSince := time.Unix(123, 0)
	gomock.InOrder(
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
			xidTotalValue(0, 42, 0),
		}, nextSince, nil),
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, nextSince).Return([]dcgm.FieldValue_v2{
			xidTotalValue(0, 46, 0),
		}, time.Unix(456, 0), nil),
	)

	require.NoError(t, collector.collectNewEvents())
	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[xidTotalCounter()]

	assert.Equal(t, "1", metricValueByLabel(t, metrics, "xid", "42"))
	assert.Equal(t, "1", metricValueByLabel(t, metrics, "xid", "46"))
}

// TestXIDTotalCollectorSkipsBlankStatusFailedAndNoErrorSamples checks that blank values, failed samples, and XID
// 0 are not counted; only real XID codes produce series.
func TestXIDTotalCollectorSkipsBlankStatusFailedAndNoErrorSamples(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		xidTotalValue(0, dcgm.DCGM_FT_INT64_BLANK, 0),
		xidTotalValue(0, 42, -1),
		xidTotalValue(0, 0, 0),
		xidTotalValue(0, 46, 0),
	}, time.Unix(10, 0), nil)

	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[xidTotalCounter()]

	assert.Len(t, metrics, 1)
	assert.Equal(t, "1", metricValueByLabel(t, metrics, "xid", "46"))
}

// TestXIDTotalCollectorScrapeDoesNotCallGetValuesSince checks that repeated GetMetrics calls return the same
// totals without polling DCGM again.
func TestXIDTotalCollectorScrapeDoesNotCallGetValuesSince(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		xidTotalValue(0, 42, 0),
	}, time.Unix(10, 0), nil)

	require.NoError(t, collector.collectNewEvents())

	observed := collector.initialSince.Add(time.Minute)
	collector.now = func() time.Time { return observed }

	first, err := collector.GetMetrics()
	require.NoError(t, err)
	require.Len(t, first[collector.counter], 1)

	assert.Equal(t, observed.UnixMicro(), first[collector.counter][0].Timestamp)
	observed = observed.Add(time.Second)
	second, err := collector.GetMetrics()
	require.NoError(t, err)

	require.Len(t, second[collector.counter], len(first[collector.counter]))
	for i := range first[collector.counter] {
		assert.Equal(t, observed.UnixMicro(), second[collector.counter][i].Timestamp)
		first[collector.counter][i].Timestamp = second[collector.counter][i].Timestamp
	}
	assert.Equal(t, first, second)
}

// TestXIDTotalCollectorGetValuesSinceErrorIncludesPollContext checks that a GetValuesSince failure returns an
// error that names the group handle, field group handle, and since timestamp.
func TestXIDTotalCollectorGetValuesSinceErrorIncludesPollContext(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	since := time.Unix(123, 0)
	collector.stateMu.Lock()
	collector.cursors[newCumulativeWatchCursorKey(group, fieldGroup)] = since
	collector.stateMu.Unlock()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, since).Return(nil, time.Time{}, errors.New("dcgm unavailable"))

	err := collector.collectNewEvents()

	require.Error(t, err)
	assert.ErrorContains(t, err, "group_handle=1")
	assert.ErrorContains(t, err, "field_group_handle=1")
	assert.ErrorContains(t, err, "since_timestamp=1970-01-01T00:02:03Z")
}

// TestXIDTotalCollectorPollErrorsDoNotMutateState checks that an UpdateAllFields failure or a GetValuesSince
// failure leaves the totals and the cursor unchanged.
func TestXIDTotalCollectorPollErrorsDoNotMutateState(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	stableCursor := time.Unix(10, 0)
	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		xidTotalValue(0, 42, 0),
	}, stableCursor, nil)
	require.NoError(t, collector.collectNewEvents())

	wantTotals := collector.snapshotTotals()
	require.True(t, stableCursor.Equal(xidCursor(collector, group, fieldGroup)))

	mockDCGM.EXPECT().UpdateAllFields().Return(errors.New("update failed"))
	err := collector.collectNewEvents()
	require.Error(t, err)
	assert.Equal(t, wantTotals, collector.snapshotTotals())
	assert.True(t, stableCursor.Equal(xidCursor(collector, group, fieldGroup)))

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, stableCursor).Return(nil, time.Time{}, errors.New("get failed"))
	err = collector.collectNewEvents()
	require.Error(t, err)
	assert.Equal(t, wantTotals, collector.snapshotTotals())
	assert.True(t, stableCursor.Equal(xidCursor(collector, group, fieldGroup)))
}

// TestXIDTotalCollectorSplitPollErrorDoesNotPartiallyMutateState checks that when the second field group's poll
// fails, values from the first field group in the same poll are dropped: totals and cursors stay empty.
func TestXIDTotalCollectorSplitPollErrorDoesNotPartiallyMutateState(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	group := testGroupHandle(1)
	firstFieldGroup := testFieldHandle(1)
	secondFieldGroup := testFieldHandle(2)
	collector := newTestXIDTotalCollectorWithFieldGroups(
		t,
		ctrl,
		1,
		[]dcgm.GroupHandle{group},
		[]dcgm.FieldHandle{firstFieldGroup, secondFieldGroup},
		nil,
	)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, firstFieldGroup, collector.initialSince).Return(
		[]dcgm.FieldValue_v2{xidTotalValue(0, 42, 0)},
		time.Unix(10, 0),
		nil,
	)
	mockDCGM.EXPECT().GetValuesSince(group, secondFieldGroup, collector.initialSince).Return(
		nil,
		time.Time{},
		errors.New("second field group failed"),
	)

	err := collector.collectNewEvents()

	require.Error(t, err)
	assert.Empty(t, collector.snapshotTotals())
	collector.stateMu.RLock()
	defer collector.stateMu.RUnlock()
	assert.Empty(t, collector.cursors)
}

// xidCursor reads the saved cursor for one XID watch pair.
func xidCursor(c *xidTotalCollector, group dcgm.GroupHandle, fieldGroup dcgm.FieldHandle) time.Time {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	return c.cursors[newCumulativeWatchCursorKey(group, fieldGroup)]
}

// TestXIDTotalCollectorConcurrentCollectAndScrape checks that polling and GetMetrics running at the same time on
// two goroutines return no errors.
func TestXIDTotalCollectorConcurrentCollectAndScrape(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	collector, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil).AnyTimes()
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, gomock.Any()).Return([]dcgm.FieldValue_v2{
		xidTotalValue(0, 42, 0),
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

// TestXIDTotalCollectorCollectsMultipleGroupsAndGPUs checks that one poll reads every watch group and keeps the
// counts for each GPU in separate series.
func TestXIDTotalCollectorCollectsMultipleGroupsAndGPUs(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	group1 := testGroupHandle(1)
	group2 := testGroupHandle(2)
	collector, fieldGroup := newTestXIDTotalCollectorWithGroups(t, ctrl, 2, []dcgm.GroupHandle{group1, group2}, nil)
	defer collector.Cleanup()

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group1, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		xidTotalValue(0, 42, 0),
	}, time.Unix(10, 0), nil)
	mockDCGM.EXPECT().GetValuesSince(group2, fieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
		xidTotalValue(1, 46, 0),
	}, time.Unix(20, 0), nil)

	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[xidTotalCounter()]
	require.Len(t, metrics, 2)
	assert.Equal(t, "1", metricValueByGPUAndLabel(t, metrics, "0", "xid", "42"))
	assert.Equal(t, "1", metricValueByGPUAndLabel(t, metrics, "1", "xid", "46"))
}

// TestXIDTotalCollectorRestartStartsWithEmptyTotals checks that a new collector starts with no XID series even
// after an earlier collector counted some.
func TestXIDTotalCollectorRestartStartsWithEmptyTotals(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)
	first, group, fieldGroup := newTestXIDTotalCollector(t, ctrl, nil)

	mockDCGM.EXPECT().UpdateAllFields().Return(nil)
	mockDCGM.EXPECT().GetValuesSince(group, fieldGroup, first.initialSince).Return([]dcgm.FieldValue_v2{
		xidTotalValue(0, 42, 0),
	}, time.Unix(10, 0), nil)
	require.NoError(t, first.collectNewEvents())
	got, err := first.GetMetrics()
	require.NoError(t, err)
	require.Len(t, got[xidTotalCounter()], 1)
	first.Cleanup()

	second, _, _ := newTestXIDTotalCollector(t, ctrl, nil)
	defer second.Cleanup()
	got, err = second.GetMetrics()
	require.NoError(t, err)
	assert.Empty(t, got[xidTotalCounter()])
}

// TestXIDTotalCollectorInitialCursorStartsAtCollectorCreation checks that the first poll cursor is set to the
// time the collector was created, not the zero time.
func TestXIDTotalCollectorInitialCursorStartsAtCollectorCreation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctrl := gomock.NewController(t)
		setMockDCGMClient(t, ctrl)
		expected := time.Now()

		collector, _, _ := newTestXIDTotalCollector(t, ctrl, nil)
		defer collector.Cleanup()

		assert.Equal(t, expected, collector.initialSince)
	})
}

// TestXIDTotalCollectorCleanupStopsPollerBeforeWatchCleanup checks that Cleanup waits for the poller to stop
// before it runs the watch cleanup.
func TestXIDTotalCollectorCleanupStopsPollerBeforeWatchCleanup(t *testing.T) {
	ctrl := gomock.NewController(t)
	setMockDCGMClient(t, ctrl)

	cleanupCalled := make(chan struct{}, 1)
	var collector *xidTotalCollector
	collector, _, _ = newTestXIDTotalCollector(t, ctrl, []func(){
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

// TestXIDTotalCollectorCleanupIsIdempotent checks that calling Cleanup twice runs the watch cleanup once and
// closes CleanupComplete.
func TestXIDTotalCollectorCleanupIsIdempotent(t *testing.T) {
	ctrl := gomock.NewController(t)
	setMockDCGMClient(t, ctrl)

	cleanupCalls := 0
	collector, _, _ := newTestXIDTotalCollector(t, ctrl, []func(){
		func() {
			cleanupCalls++
		},
	})

	collector.Cleanup()
	collector.Cleanup()

	select {
	case <-collector.CleanupComplete():
	case <-time.After(time.Second):
		t.Fatal("watch cleanup did not complete")
	}

	assert.Equal(t, 1, cleanupCalls)
}

// setMockDCGMClient installs a mock process client and restores the previous client after the test.
func setMockDCGMClient(t *testing.T, ctrl *gomock.Controller) *mockdcgm.MockDCGM {
	t.Helper()

	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)

	return mockDCGM
}

// newTestXIDTotalCollector creates a one-GPU XID total collector fixture.
func newTestXIDTotalCollector(
	t *testing.T, ctrl *gomock.Controller, cleanups []func(),
) (*xidTotalCollector, dcgm.GroupHandle, dcgm.FieldHandle) {
	t.Helper()

	group := testGroupHandle(1)
	collector, fieldGroup := newTestXIDTotalCollectorWithGroups(t, ctrl, 1, []dcgm.GroupHandle{group}, cleanups)
	return collector, group, fieldGroup
}

// TestXIDTotalCollectorPollsAllFieldGroups checks that each field group in a watch is polled from its own saved
// cursor, and XIDs from both polls add to one total.
func TestXIDTotalCollectorPollsAllFieldGroups(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := setMockDCGMClient(t, ctrl)

	group := testGroupHandle(1)
	firstFieldGroup := testFieldHandle(1)
	secondFieldGroup := testFieldHandle(2)
	collector := newTestXIDTotalCollectorWithFieldGroups(
		t, ctrl, 1, []dcgm.GroupHandle{group}, []dcgm.FieldHandle{firstFieldGroup, secondFieldGroup}, nil,
	)
	defer collector.Cleanup()

	firstCursor := time.Unix(10, 0)
	secondCursor := time.Unix(11, 0)
	gomock.InOrder(
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, firstFieldGroup, collector.initialSince).Return(nil, firstCursor, nil),
		mockDCGM.EXPECT().GetValuesSince(group, secondFieldGroup, collector.initialSince).Return([]dcgm.FieldValue_v2{
			xidTotalValue(0, 42, 0),
		}, secondCursor, nil),
		mockDCGM.EXPECT().UpdateAllFields().Return(nil),
		mockDCGM.EXPECT().GetValuesSince(group, firstFieldGroup, firstCursor).Return(nil, time.Unix(20, 0), nil),
		mockDCGM.EXPECT().GetValuesSince(group, secondFieldGroup, secondCursor).Return([]dcgm.FieldValue_v2{
			xidTotalValue(0, 42, 0),
		}, time.Unix(21, 0), nil),
	)

	require.NoError(t, collector.collectNewEvents())
	require.NoError(t, collector.collectNewEvents())

	got, err := collector.GetMetrics()
	require.NoError(t, err)
	metrics := got[xidTotalCounter()]
	assert.Equal(t, "2", metricValueByLabel(t, metrics, "xid", "42"))
}

// newTestXIDTotalCollectorWithFieldGroups creates a collector fixture with one watch per field group.
func newTestXIDTotalCollectorWithFieldGroups(
	t *testing.T,
	ctrl *gomock.Controller,
	gpuCount int,
	groups []dcgm.GroupHandle,
	fieldGroups []dcgm.FieldHandle,
	cleanups []func(),
) *xidTotalCollector {
	t.Helper()

	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)
	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, gpuCount, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).
		Return(groups, fieldGroups, cleanups, nil)

	counterList := counters.CounterList{xidTotalCounter()}
	deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, nil, nil, mockDeviceWatcher, int64(time.Hour/time.Second))

	collector, err := NewXIDTotalCollector(counterList, "localhost", &appconfig.Config{CollectInterval: int(time.Hour / time.Millisecond)}, *deviceWatchList)
	require.NoError(t, err)

	return collector.(*xidTotalCollector)
}

// newTestXIDTotalCollectorWithGroups creates a collector fixture for caller-supplied GPU groups.
func newTestXIDTotalCollectorWithGroups(
	t *testing.T, ctrl *gomock.Controller, gpuCount int, groups []dcgm.GroupHandle, cleanups []func(),
) (*xidTotalCollector, dcgm.FieldHandle) {
	t.Helper()

	mockDeviceWatcher := mockdevicewatcher.NewMockWatcher(ctrl)
	mockGPUDeviceInfo := testutils.MockGPUDeviceInfo(ctrl, gpuCount, nil)
	mockGPUDeviceInfo.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()

	fieldGroup := testFieldHandle(1)
	mockDeviceWatcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).
		Return(groups, []dcgm.FieldHandle{fieldGroup}, cleanups, nil)

	counterList := counters.CounterList{xidTotalCounter()}
	deviceWatchList := devicewatchlistmanager.NewWatchList(mockGPUDeviceInfo, nil, nil, mockDeviceWatcher, int64(time.Hour/time.Second))

	collector, err := NewXIDTotalCollector(counterList, "localhost", &appconfig.Config{CollectInterval: int(time.Hour / time.Millisecond)}, *deviceWatchList)
	require.NoError(t, err)

	return collector.(*xidTotalCollector), fieldGroup
}

// testGroupHandle creates a group handle with a stable test value.
func testGroupHandle(handle uintptr) dcgm.GroupHandle {
	group := dcgm.GroupHandle{}
	group.SetHandle(handle)
	return group
}

// testFieldHandle creates a field-group handle with a stable test value.
func testFieldHandle(handle uintptr) dcgm.FieldHandle {
	fieldGroup := dcgm.FieldHandle{}
	fieldGroup.SetHandle(handle)
	return fieldGroup
}

// xidTotalCounter returns the exporter counter used by cumulative XID tests.
func xidTotalCounter() counters.Counter {
	return counters.Counter{
		FieldID:   dcgm.Short(counters.DCGMXIDErrorsTotal),
		FieldName: counters.DCGMExpXIDErrorsTotal,
		PromType:  "counter",
		Help:      "xid total",
	}
}

// xidTotalValue builds one XID sample with the requested value and status.
func xidTotalValue(gpuID uint, xid int64, status int) dcgm.FieldValue_v2 {
	return dcgm.FieldValue_v2{
		EntityGroupId: dcgm.FE_GPU,
		EntityID:      gpuID,
		FieldID:       dcgm.DCGM_FI_DEV_XID_ERRORS,
		FieldType:     dcgm.DCGM_FT_INT64,
		Status:        status,
		Value:         createInt64ByteArray(xid),
	}
}

// metricValueByLabel returns the value of the first metric with label=value and
// fails the test when none matches.
func metricValueByLabel(t *testing.T, metrics []Metric, label, value string) string {
	t.Helper()

	for _, metric := range metrics {
		if metric.Labels[label] == value {
			return metric.Value
		}
	}

	t.Fatalf("metric with %s=%q not found in %#v", label, value, metrics)
	return ""
}

// metricValueByGPUAndLabel returns the value of the first metric for gpu with
// label=value and fails the test when none matches.
func metricValueByGPUAndLabel(t *testing.T, metrics []Metric, gpu, label, value string) string {
	t.Helper()

	for _, metric := range metrics {
		if metric.GPU == gpu && metric.Labels[label] == value {
			return metric.Value
		}
	}

	t.Fatalf("metric with gpu=%q %s=%q not found in %#v", gpu, label, value, metrics)
	return ""
}
