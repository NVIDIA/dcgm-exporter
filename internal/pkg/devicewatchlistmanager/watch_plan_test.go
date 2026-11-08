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

package devicewatchlistmanager

import (
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
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
)

// TestHistoricalWatchPolicies catches physical policies leaking into instance watches.
func TestHistoricalWatchPolicies(t *testing.T) {
	age := 900 * time.Second
	type policy struct {
		interval int64
		age      float64
		samples  int32
	}
	gpu := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}
	gi := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7}
	ci := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21}
	for _, tt := range []struct {
		name      string
		enabled   bool
		setFields func(*WatchList, []dcgm.Short)
		want      map[dcgm.GroupEntityPair]policy
	}{
		{name: "Prometheus all scopes", setFields: (*WatchList).SetDeviceFieldsForAllGPUScopes, want: map[dcgm.GroupEntityPair]policy{gpu: {30000000, 600, 0}, gi: {30000000, 600, 0}, ci: {30000000, 600, 0}}},
		{name: "OTLP preserves every Prometheus scope policy", enabled: true, setFields: (*WatchList).SetDeviceFieldsForAllGPUScopes, want: map[dcgm.GroupEntityPair]policy{gpu: {30000000, 600, 0}, gi: {30000000, 600, 0}, ci: {30000000, 600, 0}}},
		{name: "Prometheus physical scope", setFields: (*WatchList).SetDeviceFieldsForPhysicalGPUs, want: map[dcgm.GroupEntityPair]policy{gpu: {30000000, 600, 0}}},
		{name: "OTLP physical scope excludes instances", enabled: true, setFields: (*WatchList).SetDeviceFieldsForPhysicalGPUs, want: map[dcgm.GroupEntityPair]policy{gpu: {30000000, 600, 0}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			native := mockdcgm.NewMockDCGM(ctrl)
			field := dcgm.Short(dcgm.DCGM_FI_DEV_XID_ERRORS)
			native.EXPECT().FieldGetByID(gomock.Any()).DoAndReturn(func(id dcgm.Short) (dcgm.FieldMeta, error) {
				level := dcgm.FE_GPU
				if id == field {
					level = dcgm.FE_GPU_CI
				}
				return dcgm.FieldMeta{EntityLevel: level}, nil
			}).AnyTimes()
			native.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
			info := testutils.MockGPUDeviceInfo(ctrl, 1, map[int][]deviceinfo.GPUInstanceInfo{0: {{EntityId: 7, ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 21}}}}})
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			watcher := devicewatcher.NewDeviceWatcherWithClient(native)
			fields := watcher.GetDeviceFields(counters.CounterList{{FieldID: field}}, dcgm.FE_GPU)
			list := newWatchListWithGroups(info, fields.Fields, nil, fields.ComputeInstanceFields, fields.FieldsAtMultipleScopes, fields.GPULevelFields, defaultFieldWatchGroups(fields.Fields, 30000), watcher, 30000, 600, 0)
			sources, err := signalcatalog.WatchSourcesFor("gpu.xid.last", signalcatalog.ProfileNVIDIA)
			require.NoError(t, err)
			config := &appconfig.Config{EnableOTLP: tt.enabled, OTLPWatchCollections: []appconfig.OTLPWatchCollection{{Name: "physical", Sources: sources, Interval: 5000, Retention: appconfig.WatchRetentionOverride{MaxAge: &age}}}}
			require.NoError(t, NewWatchListManager(nil, config).addOTLPWatchPlan(list, watcher))
			originalReads, err := list.Reads()
			require.NoError(t, err)
			historical := *list
			tt.setFields(&historical, []dcgm.Short{field})
			groups := make(map[uintptr][]dcgm.GroupEntityPair)
			var next uintptr
			native.EXPECT().CreateGroup(gomock.Any()).DoAndReturn(func(string) (dcgm.GroupHandle, error) {
				next++
				handle := dcgm.GroupHandle{}
				handle.SetHandle(next)
				return handle, nil
			}).AnyTimes()
			native.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(group dcgm.GroupHandle, kind dcgm.Field_Entity_Group, id uint) error {
				groups[group.GetHandle()] = append(groups[group.GetHandle()], dcgm.GroupEntityPair{EntityGroupId: kind, EntityId: id})
				return nil
			}).AnyTimes()
			native.EXPECT().FieldGroupCreate(gomock.Any(), []dcgm.Short{field}).DoAndReturn(func(string, []dcgm.Short) (dcgm.FieldHandle, error) {
				next++
				handle := dcgm.FieldHandle{}
				handle.SetHandle(next)
				return handle, nil
			}).AnyTimes()
			got := make(map[dcgm.GroupEntityPair]policy)
			native.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ dcgm.FieldHandle, group dcgm.GroupHandle, interval int64, age float64, samples int32) error {
				for _, entity := range groups[group.GetHandle()] {
					require.NotContains(t, got, entity)
					got[entity] = policy{interval, age, samples}
				}
				return nil
			}).AnyTimes()
			native.EXPECT().UnwatchFields(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			native.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
			native.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
			result, err := historical.Watch()
			require.NoError(t, err)
			t.Cleanup(result.Close)
			require.Equal(t, tt.want, got)
			reads, err := list.Reads()
			require.NoError(t, err)
			require.Equal(t, originalReads, reads, "historical narrowing must not change the shared plan")
		})
	}
}

// TestOTLPWatchRetention checks inheritance and finite bounds through collection planning.
func TestOTLPWatchRetention(t *testing.T) {
	age := 900 * time.Second
	zeroAge, minute := time.Duration(0), time.Minute
	zeroSamples, tenSamples := int64(0), int64(10)
	fastSamples, slowSamples := int64(2), int64(6)
	for _, tt := range []struct {
		name        string
		collections []appconfig.OTLPWatchCollection
		wantSamples int32
		wantErr     string
	}{
		{
			name: "conflicting PCIe metadata retention",
			collections: []appconfig.OTLPWatchCollection{
				{Name: "samples", Interval: 5000, Retention: appconfig.WatchRetentionOverride{MaxAge: &zeroAge, MaxSamples: &tenSamples}},
				{Name: "age", Interval: 5000, Retention: appconfig.WatchRetentionOverride{MaxAge: &minute, MaxSamples: &zeroSamples}},
			},
			wantErr: "field 235 has invalid merged watch policy from \"samples and age\"",
		},
		{name: "inherit global policy", collections: []appconfig.OTLPWatchCollection{{Name: "fast", Interval: 5000}}, wantSamples: 4},
		{name: "merge finite collection bounds", collections: []appconfig.OTLPWatchCollection{
			{Name: "fast", Interval: 5000, Retention: appconfig.WatchRetentionOverride{MaxAge: &age, MaxSamples: &fastSamples}},
			{Name: "slow", Interval: 30000, Retention: appconfig.WatchRetentionOverride{MaxSamples: &slowSamples}},
		}, wantSamples: 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			native := mockdcgm.NewMockDCGM(ctrl)
			native.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU}, nil).AnyTimes()
			watcher := devicewatcher.NewDeviceWatcherWithClient(native)
			info := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			sources, err := signalcatalog.WatchSourcesFor("gpu.temperature", signalcatalog.ProfileNVIDIA)
			require.NoError(t, err)
			for i := range tt.collections {
				tt.collections[i].Sources = sources
				if tt.wantErr != "" && i == 1 {
					other, err := signalcatalog.WatchSourcesFor("gpu.power.usage", signalcatalog.ProfileNVIDIA)
					require.NoError(t, err)
					tt.collections[i].Sources = other
				}
			}
			config := &appconfig.Config{EnableOTLP: true, WatchRetention: appconfig.WatchRetention{MaxAge: 1200 * time.Second, MaxSamples: 4}, OTLPWatchCollections: tt.collections}
			before := config.Clone()
			list := NewWatchList(info, nil, nil, watcher, 30000)
			err = NewWatchListManager(nil, config).addOTLPWatchPlan(list, watcher)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorContains(t, err, "at least one history limit must be finite")
				require.Equal(t, before, config)
				return
			}

			require.NoError(t, err)
			want := devicewatcher.FieldWatchGroup{Name: "fast", Fields: []dcgm.Short{150, 235, 236, 237, 238}, IntervalMSec: 5000, MaxKeepAge: 1200, MaxKeepSamples: tt.wantSamples}
			require.Equal(t, []devicewatcher.FieldWatchGroup{want, want}, list.FieldWatchGroups())
			reads, err := list.Reads()
			require.NoError(t, err)
			require.Len(t, reads, 1)
			require.Equal(t, []dcgm.Short{150, 235, 236, 237, 238}, reads[0].Fields)
			require.Equal(t, before, config)
		})
	}
}

// TestMergeWatchGroups checks shared-field cadence, retention conflicts, stable order, and disabled behavior.
func TestMergeWatchGroups(t *testing.T) {
	prometheus := []devicewatcher.FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{150, 155}, IntervalMSec: 30000, MaxKeepAge: 600}}
	tests := []struct {
		name       string
		prometheus []devicewatcher.FieldWatchGroup
		selected   []devicewatcher.FieldWatchGroup
		want       []devicewatcher.FieldWatchGroup
		wantErr    string
	}{
		{name: "disabled leaves Prometheus unchanged", want: prometheus},
		{
			name:     "identical policies share one group",
			selected: []devicewatcher.FieldWatchGroup{{Name: "otel", Fields: []dcgm.Short{157}, IntervalMSec: 30000, MaxKeepAge: 600}},
			want:     []devicewatcher.FieldWatchGroup{{Name: "default", Fields: []dcgm.Short{150, 155, 157}, IntervalMSec: 30000, MaxKeepAge: 600}},
		},
		{
			name:     "shared fields retain Prometheus interval and age",
			selected: []devicewatcher.FieldWatchGroup{{Name: "otel", Fields: []dcgm.Short{150, 157, 157}, IntervalMSec: 5000, MaxKeepAge: 900, MaxKeepSamples: 10}},
			want: []devicewatcher.FieldWatchGroup{
				{Name: "default", Fields: []dcgm.Short{150, 155}, IntervalMSec: 30000, MaxKeepAge: 600},
				{Name: "otel", Fields: []dcgm.Short{157}, IntervalMSec: 5000, MaxKeepAge: 900, MaxKeepSamples: 10},
			},
		},
		{
			name: "shared fields retain individual Prometheus policies",
			prometheus: []devicewatcher.FieldWatchGroup{
				{Name: "temperature", Fields: []dcgm.Short{150}, IntervalMSec: 30000, MaxKeepAge: 600, MaxKeepSamples: 4},
				{Name: "power", Fields: []dcgm.Short{155}, IntervalMSec: 60000, MaxKeepAge: 1200, MaxKeepSamples: 8},
			},
			selected: []devicewatcher.FieldWatchGroup{{Name: "otel", Fields: []dcgm.Short{150, 155, 157}, IntervalMSec: 5000, MaxKeepAge: 900}},
			want: []devicewatcher.FieldWatchGroup{
				{Name: "temperature", Fields: []dcgm.Short{150}, IntervalMSec: 30000, MaxKeepAge: 600, MaxKeepSamples: 4},
				{Name: "power", Fields: []dcgm.Short{155}, IntervalMSec: 60000, MaxKeepAge: 1200, MaxKeepSamples: 8},
				{Name: "otel", Fields: []dcgm.Short{157}, IntervalMSec: 5000, MaxKeepAge: 900},
			},
		},
		{
			name:     "zero age stays unbounded",
			selected: []devicewatcher.FieldWatchGroup{{Name: "otel", Fields: []dcgm.Short{157}, IntervalMSec: 5000, MaxKeepSamples: 20}},
			want: []devicewatcher.FieldWatchGroup{
				{Name: "default", Fields: []dcgm.Short{150, 155}, IntervalMSec: 30000, MaxKeepAge: 600},
				{Name: "otel", Fields: []dcgm.Short{157}, IntervalMSec: 5000, MaxKeepSamples: 20},
			},
		},
		{
			name: "conflicting OTLP-only unbounded dimensions rejected",
			selected: []devicewatcher.FieldWatchGroup{
				{Name: "age", Fields: []dcgm.Short{157}, IntervalMSec: 30000, MaxKeepAge: 600},
				{Name: "samples", Fields: []dcgm.Short{157}, IntervalMSec: 5000, MaxKeepSamples: 20},
			},
			wantErr: "age and samples",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prometheus == nil {
				tt.prometheus = prometheus
			}
			got, err := mergeWatchGroups(tt.prometheus, tt.selected)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, []dcgm.Short{150, 155}, prometheus[0].Fields)
		})
	}
}

// TestSharedWatchSplitting checks native chunks at the combined 127/128-field boundary.
func TestSharedWatchSplitting(t *testing.T) {
	for _, tt := range []struct {
		name            string
		prometheusCount int
		wantChunks      []int
		otlpInterval    int
	}{
		{name: "127 union fields", prometheusCount: 122, wantChunks: []int{127}, otlpInterval: 30000},
		{name: "128 union fields", prometheusCount: 123, wantChunks: []int{127, 1}, otlpInterval: 30000},
		{name: "split retains separate policies", prometheusCount: 128, wantChunks: []int{127, 1, 5}, otlpInterval: 5000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			native := mockdcgm.NewMockDCGM(ctrl)
			native.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU}, nil).AnyTimes()
			native.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
			info := testutils.MockGPUDeviceInfo(ctrl, 1, nil)
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			watcher := devicewatcher.NewDeviceWatcherWithClient(native)
			prometheus := make([]dcgm.Short, tt.prometheusCount)
			for i := range prometheus {
				prometheus[i] = dcgm.Short(i + 1)
			}
			list := NewWatchList(info, prometheus, nil, watcher, 30000)
			sources, err := signalcatalog.WatchSourcesFor("gpu.power.usage", signalcatalog.ProfileNVIDIA)
			require.NoError(t, err)
			manager := NewWatchListManager(nil, &appconfig.Config{EnableOTLP: true, OTLPWatchCollections: []appconfig.OTLPWatchCollection{{Name: "telemetry", Sources: sources, Interval: tt.otlpInterval}}})
			require.NoError(t, manager.addOTLPWatchPlan(list, watcher))
			group := dcgm.GroupHandle{}
			group.SetHandle(1)
			native.EXPECT().CreateGroup(gomock.Any()).Return(group, nil)
			native.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(0)).Return(nil)
			var chunks []int
			var watched []dcgm.Short
			fieldGroups := make(map[uintptr][]dcgm.Short)
			native.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(func(_ string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
				chunks = append(chunks, len(fields))
				watched = append(watched, fields...)
				handle := dcgm.FieldHandle{}
				handle.SetHandle(uintptr(len(chunks) + 10))
				fieldGroups[handle.GetHandle()] = append([]dcgm.Short(nil), fields...)
				return handle, nil
			}).Times(len(tt.wantChunks))
			native.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), group, gomock.Any(), float64(600), int32(0)).DoAndReturn(
				func(handle dcgm.FieldHandle, _ dcgm.GroupHandle, interval int64, _ float64, _ int32) error {
					for _, field := range fieldGroups[handle.GetHandle()] {
						wantInterval := int64(30000000)
						if int(field) > tt.prometheusCount {
							wantInterval = int64(tt.otlpInterval) * 1000
						}
						require.Equal(t, wantInterval, interval, "field %d", field)
					}
					return nil
				},
			).Times(len(tt.wantChunks))
			native.EXPECT().UnwatchFields(gomock.Any(), group).Return(nil).Times(len(tt.wantChunks))
			native.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).Times(len(tt.wantChunks))
			native.EXPECT().DestroyGroup(group).Return(nil)
			result, err := list.Watch()
			require.NoError(t, err)
			defer result.Close()
			require.Equal(t, tt.wantChunks, chunks)
			require.Equal(t, append(prometheus, 157, 235, 236, 237, 238), watched)
		})
	}
}

// TestPhysicalFieldScopes checks physical source placement and unchanged Prometheus fields.
func TestPhysicalFieldScopes(t *testing.T) {
	tests := []struct {
		name             string
		prometheusFields []dcgm.Short
		labelFields      []dcgm.Short
		enabled          bool
		gpuLevel         bool
	}{
		{name: "GPU-level Prometheus fields stay on physical GPUs", prometheusFields: []dcgm.Short{150, 155}, enabled: true, gpuLevel: true},
		{
			name:             "GPU-instance label alone does not add a watch",
			prometheusFields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP},
			labelFields:      []dcgm.Short{dcgm.DCGM_FI_PROF_DRAM_ACTIVE},
			enabled:          true,
			gpuLevel:         true,
		},
		{name: "Prometheus numeric fields", prometheusFields: []dcgm.Short{150, 155}, enabled: true},
		{name: "Prometheus label does not suppress numeric source", labelFields: []dcgm.Short{157}, enabled: true},
		{name: "OTLP only", enabled: true},
		{name: "disabled OTLP keeps Prometheus watches", prometheusFields: []dcgm.Short{150, 155}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			native := mockdcgm.NewMockDCGM(ctrl)
			native.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU}, nil).AnyTimes()
			watcher := devicewatcher.NewDeviceWatcherWithClient(native)
			instance := deviceinfo.GPUInstanceInfo{EntityId: 7, ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 21}}}
			info := testutils.MockGPUDeviceInfo(ctrl, 2, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			sources, err := signalcatalog.WatchSourcesFor("gpu.power.usage", signalcatalog.ProfileNVIDIA)
			require.NoError(t, err)
			config := &appconfig.Config{EnableOTLP: tt.enabled, OTLPWatchCollections: []appconfig.OTLPWatchCollection{{Name: "otel", Sources: sources, Interval: 5000}}, WatchRetention: appconfig.DefaultWatchRetention()}
			manager := NewWatchListManager(counters.CounterList{}, config)
			list := NewWatchList(info, tt.prometheusFields, tt.labelFields, watcher, 30000)
			if tt.gpuLevel {
				list.gpuLevelFields = append([]dcgm.Short(nil), tt.prometheusFields...)
			}
			before := append([]dcgm.Short(nil), tt.prometheusFields...)
			require.NoError(t, manager.addOTLPWatchPlan(list, watcher))
			if !tt.enabled {
				assert.Equal(t, before, list.DeviceFields())
				assert.Empty(t, list.scopedWatchGroups)
				return
			}
			assert.Contains(t, list.DeviceFields(), dcgm.Short(157))
			reads, err := list.Reads()
			require.NoError(t, err)
			got := make(map[dcgm.GroupEntityPair][]dcgm.Short)
			for _, read := range reads {
				got[read.Entity.Entity] = read.Fields
			}
			wantParent := []dcgm.Short{157, 235, 236, 237, 238}
			if tt.gpuLevel {
				prometheus := append(append([]dcgm.Short(nil), before...), tt.labelFields...)
				wantParent = append(prometheus, wantParent...)
				assert.NotContains(t, got, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7})
			}
			assert.Equal(t, wantParent, got[dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}])
			assert.NotContains(t, got[dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 7}], dcgm.Short(235))
			assert.NotContains(t, got, dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21})
			assert.Contains(t, got[dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1}], dcgm.Short(157))
			assert.Equal(t, before, tt.prometheusFields)
		})
	}
}

// TestComputeFieldScopes checks memory and exporter-backed XID fields retain native scopes.
func TestComputeFieldScopes(t *testing.T) {
	tests := []struct {
		name       string
		signal     signalcatalog.SignalID
		field      dcgm.Short
		multiScope bool
	}{
		{name: "memory", signal: "gpu.memory.usage", field: dcgm.DCGM_FI_DEV_FB_USED},
		{name: "exporter XID dependency", signal: "gpu.xid.records", field: dcgm.DCGM_FI_DEV_XID_ERRORS, multiScope: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			native := mockdcgm.NewMockDCGM(ctrl)
			native.EXPECT().FieldGetByID(gomock.Any()).DoAndReturn(func(field dcgm.Short) (dcgm.FieldMeta, error) {
				level := dcgm.FE_GPU
				if field == tt.field {
					level = dcgm.FE_GPU_CI
				}
				return dcgm.FieldMeta{EntityLevel: level}, nil
			}).AnyTimes()
			watcher := devicewatcher.NewDeviceWatcherWithClient(native)
			instance := deviceinfo.GPUInstanceInfo{EntityId: 7, ComputeInstances: []deviceinfo.ComputeInstanceInfo{{EntityId: 21}}}
			info := testutils.MockGPUDeviceInfo(ctrl, 1, map[int][]deviceinfo.GPUInstanceInfo{0: {instance}})
			info.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			sources, err := signalcatalog.WatchSourcesFor(tt.signal, signalcatalog.ProfileNVIDIA)
			require.NoError(t, err)
			manager := NewWatchListManager(nil, &appconfig.Config{EnableOTLP: true, OTLPWatchCollections: []appconfig.OTLPWatchCollection{{Name: "otel", Sources: sources, Interval: 5000}}})
			list := NewWatchList(info, nil, nil, watcher, 30000)
			require.NoError(t, manager.addOTLPWatchPlan(list, watcher))
			assert.Equal(t, []dcgm.Short{tt.field}, list.ComputeInstanceFields())
			assert.Equal(t, tt.multiScope, len(list.FieldsAtMultipleScopes()) > 0)
			reads, err := list.Reads()
			require.NoError(t, err)
			got := make(map[dcgm.GroupEntityPair][]dcgm.Short)
			for _, read := range reads {
				got[read.Entity.Entity] = read.Fields
			}
			assert.Equal(t, []dcgm.Short{tt.field}, got[dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21}])
			assert.NotContains(t, got[dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 21}], dcgm.Short(235))
		})
	}
}

// TestSplitInstanceFieldsKeepsLabelsWithComputeInstanceMetrics checks that label fields and fields valid
// at both scopes go in both lists, while compute-instance-only fields go only in the compute instance list.
func TestSplitInstanceFieldsKeepsLabelsWithComputeInstanceMetrics(t *testing.T) {
	metricField := dcgm.DCGM_FI_DEV_GPU_TEMP
	computeInstanceField := dcgm.DCGM_FI_DEV_FB_USED
	multiScopeField := dcgm.DCGM_FI_DEV_XID_ERRORS
	labelField := dcgm.DCGM_FI_DRIVER_VERSION

	nonComputeInstanceFields, computeInstanceFields := SplitInstanceFields(
		[]dcgm.Short{metricField, computeInstanceField, multiScopeField, labelField},
		[]dcgm.Short{computeInstanceField, multiScopeField},
		[]dcgm.Short{multiScopeField},
		[]dcgm.Short{labelField},
		nil,
	)

	assert.Equal(t, []dcgm.Short{metricField, multiScopeField, labelField}, nonComputeInstanceFields)
	assert.Equal(t, []dcgm.Short{computeInstanceField, multiScopeField, labelField}, computeInstanceFields)

	instanceFields, _ := SplitInstanceFields([]dcgm.Short{metricField}, nil, nil, nil, []dcgm.Short{metricField})
	require.NotNil(t, instanceFields)
	require.Empty(t, instanceFields)
}
