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

type xidMIGCollectorCase struct {
	name      string
	options   appconfig.DeviceOptions
	entities  []dcgm.GroupEntityPair
	values    []dcgm.FieldValue_v2
	want      []string
	wantZeros []string
}

func TestXIDCollectorsCollectMIGScopes(t *testing.T) {
	gpu0 := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	gpu1 := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1}
	gi7 := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7}
	gi8 := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 8}
	ci21 := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21}
	ci22 := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 22}
	ci23 := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 23}
	labels := map[dcgm.GroupEntityPair]string{
		gpu0: "gpu0", gpu1: "gpu1", gi7: "gi7", gi8: "gi8",
		ci21: "ci21", ci22: "ci22", ci23: "ci23",
	}
	instances := map[int][]deviceinfo.GPUInstanceInfo{0: {
		{
			EntityId: 7, Info: dcgm.MigEntityInfo{NvmlInstanceId: 3}, ProfileName: "2g.20gb",
			ComputeInstances: []deviceinfo.ComputeInstanceInfo{
				{EntityId: 21, InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 0}},
				{EntityId: 22, InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 1}},
			},
		},
		{
			EntityId: 8, Info: dcgm.MigEntityInfo{NvmlInstanceId: 4}, ProfileName: "1g.10gb",
			ComputeInstances: []deviceinfo.ComputeInstanceInfo{
				{EntityId: 23, InstanceInfo: dcgm.MigEntityInfo{NvmlComputeInstanceId: 0}},
			},
		},
	}}
	value := func(entity dcgm.GroupEntityPair, xid int64) dcgm.FieldValue_v2 {
		return dcgm.FieldValue_v2{
			EntityGroupId: entity.EntityGroupId, EntityID: entity.EntityId,
			FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, FieldType: dcgm.DCGM_FT_INT64,
			Value: createInt64ByteArray(xid),
		}
	}
	// Literal summaries keep expected NVML identities independent of topology selection.
	tests := []xidMIGCollectorCase{
		{
			name: "whole GPU", options: appconfig.DeviceOptions{MajorRange: []int{1}},
			entities: []dcgm.GroupEntityPair{gpu1}, values: []dcgm.FieldValue_v2{value(gpu1, 45)},
			want: []string{"1///45=1@gpu1"},
		},
		{
			name: "flex multiple scopes", options: appconfig.DeviceOptions{Flex: true},
			entities: []dcgm.GroupEntityPair{gpu0, gpu1, gi7, gi8, ci21, ci22, ci23},
			values: []dcgm.FieldValue_v2{
				value(ci21, 45), value(ci21, 45), value(ci22, 45),
				value(gi7, 31), value(gpu0, 63), value(gpu1, 79),
			},
			want: []string{
				"0/3/0/45=2@ci21", "0/3/1/45=1@ci22", "0/3//31=1@gi7",
				"0///63=1@gpu0", "1///79=1@gpu1",
			},
			wantZeros: []string{"0/4//=0@gi8", "0/4/0/=0@ci23"},
		},
		{
			name: "explicit instance", options: appconfig.DeviceOptions{MinorRange: []int{7}},
			entities:  []dcgm.GroupEntityPair{gpu0, gi7, ci21, ci22},
			values:    []dcgm.FieldValue_v2{value(ci21, 45), value(ci22, 45)},
			want:      []string{"0/3/0/45=1@ci21", "0/3/1/45=1@ci22"},
			wantZeros: []string{"0///=0@gpu0", "0/3//=0@gi7"},
		},
		{
			name: "combined selection", options: appconfig.DeviceOptions{MajorRange: []int{0}, MinorRange: []int{7}},
			entities: []dcgm.GroupEntityPair{gpu0, gi7, ci21, ci22},
			values:   []dcgm.FieldValue_v2{value(gpu0, 63)}, want: []string{"0///63=1@gpu0"},
			wantZeros: []string{"0/3//=0@gi7", "0/3/0/=0@ci21", "0/3/1/=0@ci22"},
		},
		{
			name: "empty history", options: appconfig.DeviceOptions{MinorRange: []int{7}},
			entities:  []dcgm.GroupEntityPair{gpu0, gi7, ci21, ci22},
			wantZeros: []string{"0///=0@gpu0", "0/3//=0@gi7", "0/3/0/=0@ci21", "0/3/1/=0@ci22"},
		},
	}

	for _, tt := range tests {
		for _, family := range []string{counters.DCGMExpXIDErrorsCount, counters.DCGMExpXIDErrorsTotal} {
			t.Run(tt.name+"/"+family, func(t *testing.T) {
				runXIDMIGCollectorCase(t, family, tt, instances, labels)
			})
		}
	}
}

func runXIDMIGCollectorCase(
	t *testing.T,
	family string,
	tt xidMIGCollectorCase,
	instances map[int][]deviceinfo.GPUInstanceInfo,
	labels map[dcgm.GroupEntityPair]string,
) {
	t.Helper()

	ctrl := gomock.NewController(t)
	client := mockdcgm.NewMockDCGM(ctrl)
	info := testutils.MockGPUDeviceInfo(ctrl, 2, instances)
	info.EXPECT().GOpts().Return(tt.options).AnyTimes()
	group, fieldGroup := testGroupHandle(1), testFieldHandle(2)
	client.EXPECT().CreateGroup(gomock.Any()).Return(group, nil)
	for _, entity := range tt.entities {
		client.EXPECT().AddEntityToGroup(group, entity.EntityGroupId, entity.EntityId).Return(nil)
	}
	client.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS}).Return(fieldGroup, nil)
	client.EXPECT().WatchFieldsWithGroupExNoRefresh(fieldGroup, group, int64(1000), float64(600), int32(0)).Return(nil)
	client.EXPECT().UnwatchFields(fieldGroup, group).Return(nil)
	client.EXPECT().FieldGroupDestroy(fieldGroup).Return(nil)
	client.EXPECT().DestroyGroup(group).Return(nil)

	labelCounter := counters.Counter{FieldID: dcgm.DCGM_FI_DRIVER_VERSION, FieldName: "DCGM_FI_DRIVER_VERSION", PromType: "label"}
	for _, entity := range tt.entities {
		client.EXPECT().EntityGetLatestValues(entity.EntityGroupId, entity.EntityId, []dcgm.Short{labelCounter.FieldID}).Return(
			[]dcgm.FieldValue_v1{{FieldID: labelCounter.FieldID, FieldType: dcgm.DCGM_FT_STRING, Value: testutils.StrToByteArray(labels[entity])}}, nil)
	}
	watchList := devicewatchlistmanager.NewWatchList(info, nil, []dcgm.Short{labelCounter.FieldID}, devicewatcher.NewDeviceWatcherWithClient(client), 1)
	config := &appconfig.Config{CollectInterval: int(time.Hour / time.Millisecond), XIDCountWindowSize: 300000}
	counter := xidTotalCounter()
	counterList := counters.CounterList{counter, labelCounter}
	var c Collector
	var err error
	if family == counters.DCGMExpXIDErrorsCount {
		counter = counters.Counter{FieldID: dcgm.Short(counters.DCGMXIDErrorsCount), FieldName: family, PromType: "gauge"}
		counterList[0] = counter
		c, err = newXIDCollectorWithClient(client, counterList, "localhost", config, *watchList)
		require.NoError(t, err)
		t.Cleanup(c.Cleanup)
		client.EXPECT().UpdateAllFields().Return(nil)
		client.EXPECT().GetValuesSince(group, fieldGroup, gomock.AssignableToTypeOf(time.Time{})).Return(tt.values, time.Unix(10, 0), nil)
	} else {
		c, err = newXIDTotalCollectorWithClient(client, counterList, "localhost", config, *watchList)
		require.NoError(t, err)
		total := c.(*xidTotalCollector)
		t.Cleanup(func() {
			total.Cleanup()
			select {
			case <-total.CleanupComplete():
			case <-time.After(time.Second):
				t.Error("XID poller cleanup did not complete")
			}
		})
		client.EXPECT().UpdateAllFields().Return(nil)
		client.EXPECT().GetValuesSince(group, fieldGroup, total.initialSince).Return(tt.values, time.Unix(10, 0), nil)
		require.NoError(t, total.collectNewEvents())
		if tt.name == "explicit instance" {
			client.EXPECT().UpdateAllFields().Return(nil)
			client.EXPECT().GetValuesSince(group, fieldGroup, time.Unix(10, 0)).Return(nil, time.Unix(20, 0), nil)
			require.NoError(t, total.collectNewEvents())
		}
	}

	got, err := c.GetMetrics()
	require.NoError(t, err)
	summaries := make([]string, 0, len(got[counter]))
	for _, metric := range got[counter] {
		summaries = append(summaries, summarizeXIDMIGMetric(t, metric, family, labelCounter.FieldName))
	}
	want := append([]string(nil), tt.want...)
	if family == counters.DCGMExpXIDErrorsCount {
		want = append(want, tt.wantZeros...)
	}
	assert.ElementsMatch(t, want, summaries)
}

// summarizeXIDMIGMetric checks one sample's scope and labels before describing it for comparison.
func summarizeXIDMIGMetric(t *testing.T, metric Metric, family, labelName string) string {
	t.Helper()

	if metric.GPUInstanceID != "" {
		assert.NotEmpty(t, metric.MigProfile)
	}

	if family == counters.DCGMExpXIDErrorsCount {
		assert.Equal(t, "300000", metric.Labels[WindowSizeInMSLabel])
	} else {
		assert.NotContains(t, metric.Labels, WindowSizeInMSLabel)
	}

	if metric.Value == "0" {
		assert.NotContains(t, metric.Labels, "xid")
	}

	return fmt.Sprintf("%s/%s/%s/%s=%s@%s", metric.GPU, metric.GPUInstanceID, metric.GPUComputeInstanceID,
		metric.Labels["xid"], metric.Value, metric.Labels[labelName])
}
