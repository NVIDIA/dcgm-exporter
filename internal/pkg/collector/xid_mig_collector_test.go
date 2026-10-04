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
	"fmt"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

func TestXIDCollectorCollectsMIGScopes(t *testing.T) {
	for _, flex := range []bool{true, false} {
		for _, hasEvents := range []bool{true, false} {
			t.Run(fmt.Sprintf("flex=%t/events=%t", flex, hasEvents), func(t *testing.T) {
				fixture := newXIDScopeFixture(t, flex)
				counter := counters.Counter{
					FieldID:   dcgm.Short(counters.DCGMXIDErrorsCount),
					FieldName: counters.DCGMExpXIDErrorsCount,
					PromType:  "gauge",
					Help:      "xid count",
				}
				collector, err := NewXIDCollector(
					counters.CounterList{counter, xidScopeLabelCounter()}, "localhost",
					&appconfig.Config{XIDCountWindowSize: 1000}, *fixture.watchList,
				)
				require.NoError(t, err)
				t.Cleanup(collector.Cleanup)
				fixture.assertWatchedEntities(t)

				now := time.Unix(100, 0)
				collector.(*xidCollector).now = func() time.Time { return now }
				var events []dcgm.FieldValue_v2
				if hasEvents {
					events = xidScopeEvents()
				}
				fixture.client.EXPECT().UpdateAllFields().Return(nil)
				fixture.client.EXPECT().GetValuesSince(fixture.group, fixture.fieldGroup, now.Add(-time.Second)).
					Return(fixture.watchedEvents(events), time.Time{}, nil)

				got, err := collector.GetMetrics()
				require.NoError(t, err)
				want := map[xidScopeMetricKey]string{
					{gpu: "0", xid: "45"}:                   "1",
					{gpu: "1", xid: "45"}:                   "1",
					{gpu: "0", gi: "3", xid: "45"}:          "1",
					{gpu: "0", gi: "3", ci: "0", xid: "45"}: "2",
					{gpu: "0", gi: "3", ci: "1", xid: "45"}: "1",
				}
				if !hasEvents {
					want = map[xidScopeMetricKey]string{
						{gpu: "0"}: "0", {gpu: "1"}: "0", {gpu: "0", gi: "3"}: "0",
						{gpu: "0", gi: "3", ci: "0"}: "0", {gpu: "0", gi: "3", ci: "1"}: "0",
					}
				}
				assertXIDScopeMetrics(t, got[counter], want)
				for _, metric := range got[counter] {
					assert.Equal(t, "1000", metric.Labels[windowSizeInMSLabel])
				}
			})
		}
	}
}

func TestXIDTotalCollectorCollectsMIGScopesOnceAcrossPolls(t *testing.T) {
	for _, flex := range []bool{true, false} {
		t.Run(fmt.Sprintf("flex=%t", flex), func(t *testing.T) {
			fixture := newXIDScopeFixture(t, flex)
			collector, err := NewXIDTotalCollector(
				counters.CounterList{xidTotalCounter(), xidScopeLabelCounter()}, "localhost",
				&appconfig.Config{CollectInterval: int(time.Hour / time.Millisecond)}, *fixture.watchList,
			)
			require.NoError(t, err)
			t.Cleanup(collector.Cleanup)
			fixture.assertWatchedEntities(t)

			totalCollector := collector.(*xidTotalCollector)
			firstCursor := totalCollector.initialSince.Add(time.Second)
			secondCursor := firstCursor.Add(time.Second)
			secondEvents := []dcgm.FieldValue_v2{
				xidScopeEvent(dcgm.FE_GPU_CI, 21, 45),
				xidScopeEvent(dcgm.FE_GPU_CI, 22, 46),
				xidScopeEvent(dcgm.FE_GPU, 0, 45),
			}
			gomock.InOrder(
				fixture.client.EXPECT().UpdateAllFields().Return(nil),
				fixture.client.EXPECT().GetValuesSince(fixture.group, fixture.fieldGroup, totalCollector.initialSince).
					Return(fixture.watchedEvents(xidScopeEvents()), firstCursor, nil),
				fixture.client.EXPECT().UpdateAllFields().Return(nil),
				fixture.client.EXPECT().GetValuesSince(fixture.group, fixture.fieldGroup, firstCursor).
					Return(fixture.watchedEvents(secondEvents), secondCursor, nil),
			)
			require.NoError(t, totalCollector.collectNewEvents())
			require.NoError(t, totalCollector.collectNewEvents())

			got, err := collector.GetMetrics()
			require.NoError(t, err)
			assertXIDScopeMetrics(t, got[xidTotalCounter()], map[xidScopeMetricKey]string{
				{gpu: "0", xid: "45"}:                   "2",
				{gpu: "1", xid: "45"}:                   "1",
				{gpu: "0", gi: "3", xid: "45"}:          "1",
				{gpu: "0", gi: "3", ci: "0", xid: "45"}: "3",
				{gpu: "0", gi: "3", ci: "1", xid: "45"}: "1",
				{gpu: "0", gi: "3", ci: "1", xid: "46"}: "1",
			})
			for _, metric := range got[xidTotalCounter()] {
				assert.NotContains(t, metric.Labels, windowSizeInMSLabel)
			}
		})
	}
}

type xidScopeFixture struct {
	client     *mockdcgm.MockDCGM
	watchList  *devicewatchlistmanager.WatchList
	group      dcgm.GroupHandle
	fieldGroup dcgm.FieldHandle
	watched    []dcgm.GroupEntityPair
}

// Use the real watcher and watch list: only the DCGM boundary is mocked. The
// initial list contains no direct XID metric, so constructors must install the
// source watch even when the CSV enables only exporter counters and labels.
func newXIDScopeFixture(t *testing.T, flex bool) *xidScopeFixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	fixture := &xidScopeFixture{
		client:     setMockDCGMClient(t, ctrl),
		group:      testGroupHandle(1),
		fieldGroup: testFieldHandle(1),
	}
	instance := deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{NvmlInstanceId: 3},
		EntityId:    7,
		ProfileName: "2g.20gb",
		ComputeInstances: []deviceinfo.ComputeInstanceInfo{
			{InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 0}, EntityId: 21},
			{InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 1}, EntityId: 22},
		},
	}
	info := testutils.MockGPUDeviceInfo(ctrl, 2, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
	options := appconfig.DeviceOptions{Flex: true}
	if !flex {
		// Select both a whole GPU and its instance to check that the parent is
		// not watched again when the required scopes are combined.
		options = appconfig.DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{-1}}
	}
	info.EXPECT().GOpts().Return(options).AnyTimes()
	fixture.watchList = devicewatchlistmanager.NewWatchList(
		info, nil, []dcgm.Short{xidScopeLabelCounter().FieldID}, devicewatcher.NewDeviceWatcher(), 1000,
	)
	fixture.client.EXPECT().CreateGroup(gomock.Any()).Return(fixture.group, nil)
	fixture.client.EXPECT().AddEntityToGroup(fixture.group, gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ dcgm.GroupHandle, group dcgm.Field_Entity_Group, id uint) error {
			fixture.watched = append(fixture.watched, dcgm.GroupEntityPair{EntityGroupId: group, EntityId: id})
			return nil
		}).AnyTimes()
	fixture.client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}).
		Return(fixture.fieldGroup, nil)
	fixture.client.EXPECT().WatchFieldsWithGroupEx(fixture.fieldGroup, fixture.group, int64(1000000),
		appconfig.DefaultWatchMaxKeepAge.Seconds(), int32(appconfig.DefaultWatchMaxSamples)).Return(nil)
	fixture.client.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), []dcgm.Short{xidScopeLabelCounter().FieldID}).
		DoAndReturn(func(group dcgm.Field_Entity_Group, id uint, _ []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
			return []dcgm.FieldValue_v1{{
				FieldID:   xidScopeLabelCounter().FieldID,
				FieldType: dcgm.DCGM_FT_STRING,
				Value:     testutils.StrToByteArray(fmt.Sprintf("%d/%d", group, id)),
			}}, nil
		}).AnyTimes()
	fixture.client.EXPECT().UnwatchFields(fixture.fieldGroup, fixture.group).Return(nil)
	fixture.client.EXPECT().FieldGroupDestroy(fixture.fieldGroup).Return(nil)
	fixture.client.EXPECT().DestroyGroup(fixture.group).Return(nil)
	return fixture
}

func (f *xidScopeFixture) assertWatchedEntities(t *testing.T) {
	t.Helper()
	assert.ElementsMatch(t, []dcgm.GroupEntityPair{
		{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		{EntityGroupId: dcgm.FE_GPU, EntityId: 1},
		{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7},
		{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21},
		{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 22},
	}, f.watched)
}

func (f *xidScopeFixture) watchedEvents(events []dcgm.FieldValue_v2) []dcgm.FieldValue_v2 {
	var values []dcgm.FieldValue_v2
	for _, event := range events {
		for _, entity := range f.watched {
			if entity.EntityGroupId == event.EntityGroupId && entity.EntityId == event.EntityID {
				values = append(values, event)
			}
		}
	}
	return values
}

func xidScopeEvents() []dcgm.FieldValue_v2 {
	return []dcgm.FieldValue_v2{
		xidScopeEvent(dcgm.FE_GPU, 0, 45), xidScopeEvent(dcgm.FE_GPU, 1, 45),
		xidScopeEvent(dcgm.FE_GPU_I, 7, 45), xidScopeEvent(dcgm.FE_GPU_CI, 21, 45),
		xidScopeEvent(dcgm.FE_GPU_CI, 21, 45), xidScopeEvent(dcgm.FE_GPU_CI, 22, 45),
	}
}

func xidScopeEvent(group dcgm.Field_Entity_Group, id uint, xid int64) dcgm.FieldValue_v2 {
	value := xidTotalValue(id, xid, 0)
	value.EntityGroupId = group
	return value
}

func xidScopeLabelCounter() counters.Counter {
	return counters.Counter{FieldID: dcgm.DCGM_FI_DEV_SERIAL, FieldName: "entity_label", PromType: "label"}
}

type xidScopeMetricKey struct {
	gpu, gi, ci, xid string
}

func assertXIDScopeMetrics(t *testing.T, metrics []Metric, want map[xidScopeMetricKey]string) {
	t.Helper()
	got := make(map[xidScopeMetricKey]string)
	labelEntities := map[xidScopeMetricKey]dcgm.GroupEntityPair{
		{gpu: "0"}:                   {EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		{gpu: "1"}:                   {EntityGroupId: dcgm.FE_GPU, EntityId: 1},
		{gpu: "0", gi: "3"}:          {EntityGroupId: dcgm.FE_GPU_I, EntityId: 7},
		{gpu: "0", gi: "3", ci: "0"}: {EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21},
		{gpu: "0", gi: "3", ci: "1"}: {EntityGroupId: dcgm.FE_GPU_CI, EntityId: 22},
	}
	for _, metric := range metrics {
		key := xidScopeMetricKey{gpu: metric.GPU, gi: metric.GPUInstanceID, ci: metric.GPUComputeInstanceID}
		entity, exists := labelEntities[key]
		assert.True(t, exists, "unexpected entity identity: %v", key)
		assert.Equal(t, fmt.Sprintf("%d/%d", entity.EntityGroupId, entity.EntityId), metric.Labels["entity_label"])
		if key.gi != "" {
			assert.Equal(t, "2g.20gb", metric.MigProfile)
		}
		key.xid = metric.Labels["xid"]
		assert.NotContains(t, got, key, "duplicate metric identity")
		got[key] = metric.Value
	}
	assert.Equal(t, want, got)
}
