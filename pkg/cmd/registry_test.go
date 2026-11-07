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

package cmd

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/rendermetrics"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils"
	testdcgm "github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils/dcgm"
)

// TestSharedWatchPrometheusText checks exact GPU/MIG text and counter ownership after watch expansion.
func TestSharedWatchPrometheusText(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mig    bool
		scoped bool
	}{
		{name: "whole GPUs"},
		{name: "MIG parents", mig: true},
		{name: "MIG instance and compute-instance fields", mig: true, scoped: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			baseline := renderSharedMetrics(t, tt.mig, false, tt.scoped)
			expanded := renderSharedMetrics(t, tt.mig, true, tt.scoped)
			require.NotEmpty(t, baseline)
			require.Contains(t, baseline, "DCGM_FI_DEV_POWER_USAGE{")
			require.Contains(t, baseline, "DCGM_FI_DEV_FAN_SPEED_RATIO{")
			require.Contains(t, baseline, `DCGM_FI_DRIVER_VERSION="test-driver"`)
			require.Equal(t, baseline, expanded)
		})
	}
}

// renderSharedMetrics exercises planning, collection, rendering, and cleanup against fixed native values.
func renderSharedMetrics(t *testing.T, mig, otlp, scoped bool) string {
	t.Helper()
	restoreStartupSeams(t)
	native := mockdcgmprovider.NewMockDCGM(gomock.NewController(t))
	previous := dcgmprovider.Client()
	dcgmprovider.SetClient(native)
	defer dcgmprovider.SetClient(previous)
	cs := &counters.CounterSet{DCGMCounters: counters.CounterList{
		testutils.SampleGPUTempCounter,
		testutils.SampleDriverVersionCounter,
		{FieldID: dcgm.DCGM_FI_DEV_POWER_USAGE, FieldName: "DCGM_FI_DEV_POWER_USAGE", PromType: "gauge", Help: "Power"},
		{FieldID: dcgm.DCGM_FI_DEV_FAN_SPEED_RATIO, FieldName: "DCGM_FI_DEV_FAN_SPEED_RATIO", PromType: "gauge", Help: "Fan"},
	}}
	if scoped {
		cs.DCGMCounters = append(cs.DCGMCounters,
			counters.Counter{FieldID: dcgm.DCGM_FI_PROF_DRAM_ACTIVE, FieldName: "DCGM_FI_PROF_DRAM_ACTIVE", PromType: "gauge"},
			counters.Counter{FieldID: dcgm.DCGM_FI_DEV_FB_USED, FieldName: "DCGM_FI_DEV_FB_USED", PromType: "gauge"},
			counters.Counter{FieldID: dcgm.DCGM_FI_DEV_XID_ERROR, FieldName: "DCGM_FI_DEV_XID_ERROR", PromType: "gauge"},
		)
		native.EXPECT().FieldGetByID(dcgm.DCGM_FI_PROF_DRAM_ACTIVE).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU_I}, nil).AnyTimes()
		native.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_FB_USED).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU_CI}, nil).AnyTimes()
		native.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_XID_ERROR).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU_CI}, nil).AnyTimes()
	}
	original := append(counters.CounterList(nil), cs.DCGMCounters...)
	getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
		return cs, nil
	}
	config := &appconfig.Config{NoHostname: true, GPUDeviceOptions: appconfig.DeviceOptions{Flex: true}, CollectInterval: 30000, WatchRetention: appconfig.DefaultWatchRetention()}
	config.WatchGroups = []appconfig.WatchGroup{{Name: "scrape", Interval: 30000, Fields: []string{"*"}}}
	if otlp {
		config.EnableOTLP = true
		var sources []signalcatalog.WatchSource
		for _, id := range []signalcatalog.SignalID{"gpu.temperature", "gpu.power.usage", "gpu.memory_interface.utilization"} {
			selected, err := signalcatalog.WatchSourcesFor(id, signalcatalog.ProfileNVIDIA)
			require.NoError(t, err)
			sources = append(sources, selected...)
		}
		config.OTLPWatchCollections = []appconfig.OTLPWatchCollection{{Name: "telemetry", Sources: sources, Interval: 5000}}
	}
	expectGPUDiscovery(native, mig)
	assertCleanup := expectSharedWatches(t, native, otlp)
	assertReads := expectSharedReads(t, native, otlp, scoped)
	capabilities := counters.DCPCapabilities{}
	if scoped {
		capabilities = counters.NewDCPCapabilities([]dcgm.MetricGroup{{FieldIds: []uint{uint(dcgm.DCGM_FI_PROF_DRAM_ACTIVE)}}})
	}
	reg, _, err := buildRegistry(context.Background(), config, capabilities)
	require.NoError(t, err)
	defer reg.Cleanup()
	metrics, err := reg.Gather()
	require.NoError(t, err)
	if mig && otlp {
		assertSharedPCIe(t, metrics[dcgm.FE_GPU])
	}
	assertReads()
	var output bytes.Buffer
	require.NoError(t, rendermetrics.Render(&output, metrics))
	require.Equal(t, original, cs.DCGMCounters)
	reg.Cleanup()
	assertCleanup()
	return output.String()
}

// expectSharedReads checks that each expected entity is read once with its scoped fields.
func expectSharedReads(t *testing.T, native *mockdcgmprovider.MockDCGM, otlp, scoped bool) func() {
	t.Helper()
	wantEntities := []dcgm.GroupEntityPair{{EntityGroupId: dcgm.FE_GPU, EntityId: 0}, {EntityGroupId: dcgm.FE_GPU, EntityId: 1}}

	if scoped {
		for id := uint(0); id < 2; id++ {
			wantEntities = append(wantEntities,
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: id + 7},
				dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: id + 21},
			)
		}
	}
	reads := make(map[dcgm.GroupEntityPair]int)
	native.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(group dcgm.Field_Entity_Group, id uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
		wantFields := sharedReadFields(group, otlp, scoped)
		assert.ElementsMatch(t, wantFields, fields, "read fields for %v:%d", group, id)
		reads[dcgm.GroupEntityPair{EntityGroupId: group, EntityId: id}]++
		return sharedPCIeValues(id, fields), nil
	}).AnyTimes()
	return func() {
		require.Len(t, reads, len(wantEntities))
		for _, entity := range wantEntities {
			require.Equal(t, 1, reads[entity], "each selected entity is read once: %v", entity)
		}
	}
}

// sharedReadFields lists the independently expected fields for the collection fixtures.
func sharedReadFields(group dcgm.Field_Entity_Group, otlp, scoped bool) []dcgm.Short {
	if scoped {
		switch group {
		case dcgm.FE_GPU_I:
			return []dcgm.Short{dcgm.DCGM_FI_PROF_DRAM_ACTIVE, dcgm.DCGM_FI_DEV_XID_ERROR}
		case dcgm.FE_GPU_CI:
			return []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED, dcgm.DCGM_FI_DEV_XID_ERROR}
		}
	}

	fields := []dcgm.Short{150, 155, 1612, 1}
	if otlp {
		fields = append(fields, 157, 235, 236, 237, 238)
	}
	if scoped {
		fields = append(fields, dcgm.DCGM_FI_DEV_XID_ERROR)
	}
	return fields
}

// assertSharedPCIe checks each metric carries the PCIe attributes of its physical GPU.
func assertSharedPCIe(t *testing.T, metrics collector.MetricsByCounter) {
	t.Helper()
	want := map[string][4]int64{"0": {4, 16, 3, 8}, "1": {5, 8, 4, 4}}
	require.NotEmpty(t, metrics)
	for _, samples := range metrics {
		for _, sample := range samples {
			require.NotNil(t, sample.Source)
			expected, ok := want[sample.GPU]
			require.True(t, ok, "unexpected parent GPU %s", sample.GPU)
			for i, value := range []*int64{sample.Source.PCIeMaxGeneration, sample.Source.PCIeMaxWidth, sample.Source.PCIeGeneration, sample.Source.PCIeWidth} {
				require.NotNil(t, value)
				require.Equal(t, expected[i], *value, "GPU %s PCIe attribute %d", sample.GPU, i)
			}
		}
	}
}

// sharedPCIeValues gives the two physical parents distinct PCIe values for collection tests.
func sharedPCIeValues(gpu uint, fields []dcgm.Short) []dcgm.FieldValue_v1 {
	pcie := map[dcgm.Short][2]uint64{
		dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_GEN:   {4, 5},
		dcgm.DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH: {16, 8},
		dcgm.DCGM_FI_DEV_PCIE_LINK_GEN:       {3, 4},
		dcgm.DCGM_FI_DEV_PCIE_LINK_WIDTH:     {8, 4},
	}
	values := sharedWatchValues(fields)
	for i := range values {
		if link, ok := pcie[values[i].FieldID]; ok {
			binary.LittleEndian.PutUint64(values[i].Value[:], link[gpu])
		}
	}
	return values
}

// expectGPUDiscovery supplies two physical GPUs and the requested MIG hierarchy.
func expectGPUDiscovery(native *mockdcgmprovider.MockDCGM, mig bool) {
	native.EXPECT().GetAllDeviceCount().Return(uint(2), nil)
	native.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
	native.EXPECT().GetDeviceInfo(gomock.Any()).DoAndReturn(func(id uint) (dcgm.Device, error) {
		return dcgm.Device{GPU: id, UUID: fmt.Sprintf("GPU-%d", id)}, nil
	}).AnyTimes()
	native.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
	native.EXPECT().FieldGetByID(gomock.Any()).Return(dcgm.FieldMeta{EntityLevel: dcgm.FE_GPU}, nil).AnyTimes()
	hierarchy := dcgm.MigHierarchy_v2{}
	if mig {
		hierarchy.Count = 4
		for id := uint(0); id < 2; id++ {
			hierarchy.EntityList[id].Parent = dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: id}
			hierarchy.EntityList[id].Entity = dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: id + 7}
			hierarchy.EntityList[id+2].Parent = hierarchy.EntityList[id].Entity
			hierarchy.EntityList[id+2].Entity = dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: id + 21}
		}
		profiles := []dcgm.FieldValue_v2{{EntityID: 7}, {EntityID: 8}}
		native.EXPECT().EntitiesGetLatestValues(gomock.Any(), []dcgm.Short{dcgm.DCGM_FI_DEV_NAME}, uint(dcgm.DCGM_FV_FLAG_LIVE_DATA)).Return(profiles, nil)
		native.EXPECT().Fv2_String(gomock.Any()).Return("1g.10gb").Times(2)
	}
	native.EXPECT().GetGPUInstanceHierarchy().Return(hierarchy, nil)
}

// expectSharedWatches checks Prometheus settings, unique watches, and complete cleanup.
func expectSharedWatches(t *testing.T, native *mockdcgmprovider.MockDCGM, otlp bool) func() {
	t.Helper()
	groups := make(map[uintptr][]dcgm.GroupEntityPair)
	fieldGroups := make(map[uintptr][]dcgm.Short)
	watched := make(map[dcgm.GroupEntityPair]map[dcgm.Short]bool)
	var next uintptr
	native.EXPECT().CreateGroup(gomock.Any()).DoAndReturn(func(string) (dcgm.GroupHandle, error) {
		next++
		handle := dcgm.GroupHandle{}
		handle.SetHandle(next)
		groups[next] = nil
		return handle, nil
	}).AnyTimes()
	native.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(group dcgm.GroupHandle, kind dcgm.Field_Entity_Group, id uint) error {
		groups[group.GetHandle()] = append(groups[group.GetHandle()], dcgm.GroupEntityPair{EntityGroupId: kind, EntityId: id})
		return nil
	}).AnyTimes()
	native.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(func(_ string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
		next++
		handle := dcgm.FieldHandle{}
		handle.SetHandle(next)
		fieldGroups[next] = append([]dcgm.Short(nil), fields...)
		return handle, nil
	}).AnyTimes()
	native.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(fields dcgm.FieldHandle, group dcgm.GroupHandle, interval int64, age float64, samples int32) error {
		for _, entity := range groups[group.GetHandle()] {
			if watched[entity] == nil {
				watched[entity] = make(map[dcgm.Short]bool)
			}
			assertPrometheusWatch(t, fieldGroups[fields.GetHandle()], interval, age, samples)
			for _, field := range fieldGroups[fields.GetHandle()] {
				require.False(t, watched[entity][field], "duplicate watch for %v field %d", entity, field)
				watched[entity][field] = true
			}
		}
		return nil
	}).AnyTimes()
	native.EXPECT().UnwatchFields(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	native.EXPECT().DestroyGroup(gomock.Any()).DoAndReturn(func(group dcgm.GroupHandle) error { delete(groups, group.GetHandle()); return nil }).AnyTimes()
	native.EXPECT().FieldGroupDestroy(gomock.Any()).DoAndReturn(func(fields dcgm.FieldHandle) error { delete(fieldGroups, fields.GetHandle()); return nil }).AnyTimes()
	return func() {
		require.Empty(t, groups)
		require.Empty(t, fieldGroups)
		require.Equal(t, otlp, watched[dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0}][dcgm.DCGM_FI_DEV_POWER_USAGE_INSTANT])
	}
}

// assertPrometheusWatch checks native policies for the fixture's configured Prometheus fields.
func assertPrometheusWatch(t *testing.T, fields []dcgm.Short, interval int64, age float64, samples int32) {
	t.Helper()
	for _, field := range fields {
		switch field {
		case dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DEV_POWER_USAGE, dcgm.DCGM_FI_DEV_FAN_SPEED_RATIO, dcgm.DCGM_FI_DRIVER_VERSION:
			require.Equal(t, int64(30000000), interval, "Prometheus interval for field %d", field)
			require.Equal(t, float64(600), age, "Prometheus retention age for field %d", field)
			require.Zero(t, samples, "Prometheus sample bound for field %d", field)
		}
	}
}

// sharedWatchValues returns fixed numeric and driver values for text comparisons.
func sharedWatchValues(fields []dcgm.Short) []dcgm.FieldValue_v1 {
	values := make([]dcgm.FieldValue_v1, 0, len(fields))
	for _, field := range fields {
		value := dcgm.FieldValue_v1{FieldID: field, FieldType: dcgm.DCGM_FT_INT64, TS: 12345}
		binary.LittleEndian.PutUint64(value.Value[:], 42)
		if field == dcgm.DCGM_FI_DRIVER_VERSION {
			value.FieldType = dcgm.DCGM_FT_STRING
			copy(value.Value[:], "test-driver")
		}
		if field == dcgm.DCGM_FI_DEV_POWER_USAGE || field == dcgm.DCGM_FI_DEV_POWER_USAGE_INSTANT || field == dcgm.DCGM_FI_DEV_FAN_SPEED_RATIO {
			value.FieldType = dcgm.DCGM_FT_DOUBLE
			binary.LittleEndian.PutUint64(value.Value[:], math.Float64bits(float64(field)))
		}
		values = append(values, value)
	}
	return values
}

// TestBuildRegistryWithDetachedGPU exercises discovery and collection without
// allowing any native GPU metadata query or watch installation.
func TestBuildRegistryWithDetachedGPU(t *testing.T) {
	for _, exportersEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("exporters_enabled_%t", exportersEnabled), func(t *testing.T) {
			restoreStartupSeams(t)
			client := mockdcgmprovider.NewMockDCGM(gomock.NewController(t))
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(client)
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			config := &appconfig.Config{NoHostname: true, GPUDeviceOptions: appconfig.DeviceOptions{Flex: true}, CollectInterval: 1000}
			getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
				return detachedGPUCounters(exportersEnabled), nil
			}
			client.EXPECT().FieldGetByID(gomock.Any()).DoAndReturn(func(id dcgm.Short) (dcgm.FieldMeta, error) {
				return dcgm.FieldMeta{FieldID: id, EntityLevel: dcgm.FE_GPU}, nil
			}).AnyTimes()
			client.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
			client.EXPECT().GetGPUStatus(uint(0)).Return(dcgm.EntityStatusDetached)
			client.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
			client.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, &dcgm.Error{Code: dcgm.DCGM_ST_NVML_NOT_LOADED})
			if exportersEnabled {
				client.EXPECT().GetAllFieldPolicies().Return([]dcgm.FieldPolicy{{PolicyID: 1, Enabled: true}}, nil)
			}
			reg, manager, err := buildRegistry(context.Background(), config, counters.DCPCapabilities{})
			require.NoError(t, err)
			t.Cleanup(reg.Cleanup)
			watchList, exists := manager.EntityWatchList(dcgm.FE_GPU)
			require.True(t, exists)
			require.Equal(t, uint(1), watchList.DeviceInfo().GPUCount())
			metrics, err := reg.Gather()
			require.NoError(t, err)
			for _, family := range metrics[dcgm.FE_GPU] {
				require.Empty(t, family)
			}
			if exportersEnabled {
				require.Len(t, metrics[dcgm.FE_NONE], 1, "global field-policy metrics must remain available")
				for _, samples := range metrics[dcgm.FE_NONE] {
					require.Len(t, samples, 1)
					require.Equal(t, "1", samples[0].Value)
				}
			}
		})
	}
}

func detachedGPUCounters(exportersEnabled bool) *counters.CounterSet {
	cs := &counters.CounterSet{DCGMCounters: counters.CounterList{testutils.SampleGPUTempCounter}}
	if !exportersEnabled {
		return cs
	}
	for _, name := range []string{
		counters.DCGMExpClockEventsCount, counters.DCGMExpClockEventsTotal,
		counters.DCGMExpXIDErrorsCount, counters.DCGMExpXIDErrorsTotal,
		counters.DCGMExpGPUHealthStatus, counters.DCGMExpP2PStatus,
		counters.DCGMExpFieldPolicyEnabled,
	} {
		promType := "gauge"
		if name == counters.DCGMExpClockEventsTotal || name == counters.DCGMExpXIDErrorsTotal {
			promType = "counter"
		}
		cs.ExporterCounters = append(cs.ExporterCounters, counters.Counter{
			FieldID: dcgm.Short(counters.DCGMFields[name]), FieldName: name, PromType: promType,
		})
	}
	return cs
}

func TestBuildRegistryDetectsGPUDetachedAfterDiscovery(t *testing.T) {
	for _, status := range []dcgm.EntityStatus{dcgm.EntityStatusDetached, dcgm.EntityStatusOk, dcgm.EntityStatusUnknown} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			restoreStartupSeams(t)
			client := mockdcgmprovider.NewMockDCGM(gomock.NewController(t))
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(client)
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
				return &counters.CounterSet{DCGMCounters: counters.CounterList{testutils.SampleGPUTempCounter}}, nil
			}
			client.EXPECT().FieldGetByID(dcgm.DCGM_FI_DEV_GPU_TEMP).
				Return(testutils.SampleFieldIDToFieldMeta[dcgm.DCGM_FI_DEV_GPU_TEMP], nil).AnyTimes()
			client.EXPECT().GetAllDeviceCount().Return(uint(1), nil)
			gomock.InOrder(
				client.EXPECT().GetGPUStatus(uint(0)).Return(dcgm.EntityStatusOk),
				client.EXPECT().GetGPUStatus(uint(0)).Return(status),
			)
			client.EXPECT().GetDeviceInfo(uint(0)).Return(dcgm.Device{GPU: 0, UUID: "GPU-0"}, nil)
			client.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
			client.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
			group := dcgm.GroupHandle{}
			group.SetHandle(42)
			nativeErr := &dcgm.Error{Code: dcgm.DCGM_ST_BADPARAM}
			gomock.InOrder(
				client.EXPECT().CreateGroup(gomock.Any()).Return(group, nil),
				client.EXPECT().AddEntityToGroup(group, dcgm.FE_GPU, uint(0)).Return(nativeErr),
				client.EXPECT().DestroyGroup(group).Return(nil),
			)
			config := &appconfig.Config{NoHostname: true, GPUDeviceOptions: appconfig.DeviceOptions{Flex: true}, CollectInterval: 1000}
			reg, _, err := buildRegistry(context.Background(), config, counters.DCPCapabilities{})
			require.Nil(t, reg)
			require.ErrorIs(t, err, nativeErr)
			require.Equal(t, status == dcgm.EntityStatusDetached, errors.Is(err, errGPUDetachedDuringBuild))
		})
	}
}

// Test_getDeviceWatchListManager checks device watch groups chosen for registry construction.
// scriptedWatchListManager returns configured creation errors so registry
// tests can prove which topology failures are optional without using DCGM.
type scriptedWatchListManager struct {
	errs  map[dcgm.Field_Entity_Group]error
	calls []dcgm.Field_Entity_Group
}

// CreateEntityWatchList records each registry watch request and returns its scripted error.
func (m *scriptedWatchListManager) CreateEntityWatchList(
	entityType dcgm.Field_Entity_Group, _ devicewatcher.WatchInstaller, _ int64,
) error {
	m.calls = append(m.calls, entityType)
	return m.errs[entityType]
}

// EntityWatchList returns an empty watch list in optional-topology registry tests.
func (*scriptedWatchListManager) EntityWatchList(dcgm.Field_Entity_Group) (devicewatchlistmanager.WatchList, bool) {
	return devicewatchlistmanager.WatchList{}, false
}

// TestCreateEntityWatchListsSkipsOnlyMissingOptionalTopologies distinguishes
// an absent CPU/NVSwitch from invalid selected IDs, which must fail startup.
func TestCreateEntityWatchListsSkipsOnlyMissingOptionalTopologies(t *testing.T) {
	t.Run("missing optional topology", func(t *testing.T) {
		manager := &scriptedWatchListManager{errs: map[dcgm.Field_Entity_Group]error{
			dcgm.FE_SWITCH:   deviceinfo.ErrNoSwitchesToMonitor,
			dcgm.FE_CPU:      deviceinfo.ErrNoCPUsToMonitor,
			dcgm.FE_CPU_CORE: deviceinfo.ErrNoCPUsToMonitor,
		}}

		require.NoError(t, createEntityWatchLists(manager, nil, 1000))
		assert.Equal(t, devicewatchlistmanager.DeviceTypesToWatch, manager.calls)
	})

	t.Run("invalid selector", func(t *testing.T) {
		selectorErr := errors.New("selected GPU 99 does not exist")
		manager := &scriptedWatchListManager{errs: map[dcgm.Field_Entity_Group]error{
			dcgm.FE_GPU: selectorErr,
		}}

		err := createEntityWatchLists(manager, nil, 1000)
		require.ErrorIs(t, err, selectorErr)
		assert.Equal(t, []dcgm.Field_Entity_Group{dcgm.FE_GPU}, manager.calls)
	})
}

func Test_getDeviceWatchListManager(t *testing.T) {
	config := &appconfig.Config{
		UseRemoteHE:         false,
		EnableDCGMLog:       true,
		DCGMLogLevel:        "DEBUG",
		GPUDeviceOptions:    appconfig.DeviceOptions{},
		SwitchDeviceOptions: appconfig.DeviceOptions{},
		CPUDeviceOptions:    appconfig.DeviceOptions{},
		UseFakeGPUs:         true,
	}

	tests := []struct {
		name       string
		counterSet *counters.CounterSet
		assertion  func(*testing.T, devicewatchlistmanager.Manager)
	}{
		{
			name: "When DCGM_FI_DEV_XID_ERRORS and DCGM_EXP_XID_ERRORS_COUNT enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   230,
						FieldName: "DCGM_FI_DEV_XID_ERRORS",
						PromType:  "gauge",
						Help:      "Value of the last XID error encountered.",
					},
				},
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9001,
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
						PromType:  "gauge",
						Help:      "Count of XID Errors within user-specified time window (see xid-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(230), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_FI_DEV_XID_ERRORS enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   230,
						FieldName: "DCGM_FI_DEV_XID_ERRORS",
						PromType:  "gauge",
						Help:      "Value of the last XID error encountered.",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(230), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_XID_ERRORS_COUNT enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9001,
						FieldName: "DCGM_EXP_XID_ERRORS_COUNT",
						PromType:  "gauge",
						Help:      "Count of XID Errors within user-specified time window (see xid-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(230), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_XID_ERRORS_TOTAL enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   dcgm.Short(counters.DCGMXIDErrorsTotal),
						FieldName: counters.DCGMExpXIDErrorsTotal,
						PromType:  "counter",
						Help:      "cumulative XID errors observed since exporter start",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.DCGM_FI_DEV_XID_ERRORS, values[0].FieldID)
			},
		},
		{
			name:       "When no counters",
			counterSet: &counters.CounterSet{},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 0)
			},
		},
		{
			name: "When DCGM_FI_DEV_CLOCK_THROTTLE_REASON and DCGM_EXP_CLOCK_EVENTS_COUNT enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   112,
						FieldName: "DCGM_FI_DEV_CLOCK_THROTTLE_REASON",
						PromType:  "gauge",
					},
				},
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9002,
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
						PromType:  "gauge",
						Help:      "Count of clock events within the user-specified time window (see clock-events-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(112), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_FI_DEV_CLOCK_THROTTLE_REASON enabled",
			counterSet: &counters.CounterSet{
				DCGMCounters: []counters.Counter{
					{
						FieldID:   112,
						FieldName: "DCGM_FI_DEV_CLOCK_THROTTLE_REASON",
						PromType:  "gauge",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(112), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_CLOCK_EVENTS_COUNT enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   9002,
						FieldName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
						PromType:  "gauge",
						Help:      "Count of clock events within the user-specified time window (see clock-events-count-window-size param).",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.Short(112), values[0].FieldID)
			},
		},
		{
			name: "When DCGM_EXP_CLOCK_EVENTS_TOTAL enabled",
			counterSet: &counters.CounterSet{
				ExporterCounters: []counters.Counter{
					{
						FieldID:   dcgm.Short(counters.DCGMClockEventsTotal),
						FieldName: counters.DCGMExpClockEventsTotal,
						PromType:  "counter",
						Help:      "cumulative clock events observed since exporter start (edge-counted)",
					},
				},
			},
			assertion: func(t *testing.T, got devicewatchlistmanager.Manager) {
				require.NotNil(t, got)
				values := testutils.GetStructPrivateFieldValue[[]counters.Counter](t, got, "counters")
				require.Len(t, values, 1)
				assert.Equal(t, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, values[0].FieldID)
			},
		},
	}

	testdcgm.Initialize(t, config)
	defer dcgmprovider.Client().Cleanup()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := startDeviceWatchListManager(tt.counterSet, config)
			require.NoError(t, err)
			if tt.assertion == nil {
				t.Skip(tt.name)
			}
			tt.assertion(t, got)
		})
	}
}

// TestStartDeviceWatchListManagerRejectsInvalidWatchGroups checks invalid watch groups stop manager startup.
func TestStartDeviceWatchListManagerRejectsInvalidWatchGroups(t *testing.T) {
	tests := []struct {
		name        string
		watchGroups []appconfig.WatchGroup
		wantErr     string
	}{
		{
			name: "overlap",
			watchGroups: []appconfig.WatchGroup{
				{Name: "temperature", Interval: 10000, Fields: []string{"DCGM_FI_DEV_GPU_*"}},
				{Name: "gpu-temp", Interval: 30000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
			},
			wantErr: "matches multiple watch groups",
		},
		{
			name: "empty match",
			watchGroups: []appconfig.WatchGroup{
				{Name: "no-match", Interval: 10000, Fields: []string{"DCGM_FI_DEV_DOES_NOT_EXIST"}},
			},
			wantErr: "matched no configured fields",
		},
		{
			name: "overlap names the YAML paths",
			watchGroups: []appconfig.WatchGroup{
				{Name: "a", Path: "metrics[0].overrides[0]", Interval: 10000, Fields: []string{"DCGM_FI_DEV_GPU_*"}},
				{Name: "b", Path: "metrics[0].overrides[1]", Interval: 30000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
			},
			wantErr: "a (metrics[0].overrides[0]) and b (metrics[0].overrides[1])",
		},
	}

	counterSet := &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := startDeviceWatchListManager(counterSet, &appconfig.Config{WatchGroups: tt.watchGroups})

			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// countingFactory is a minimal collector.Factory that records how many times
// NewCollectors is invoked and returns fixed tuples or its configured error.
type countingFactory struct {
	calls      atomic.Int32
	collectors []collector.EntityCollectorTuple
	err        error
}

type callbackFactory func() ([]collector.EntityCollectorTuple, error)

// NewCollectors invokes the test hook for a controlled candidate-construction phase.
func (f callbackFactory) NewCollectors() ([]collector.EntityCollectorTuple, error) {
	return f()
}

// TestBuildRegistryCancellation checks that cancellation stops later phases,
// and that a cancel after the collectors exist returns the candidate for the
// caller to retire with its own cleanup timeout.
func TestBuildRegistryCancellation(t *testing.T) {
	tests := []struct {
		phase string
		want  []string
		built bool
	}{
		{phase: "before"},
		{phase: "counters", want: []string{"counters"}},
		{phase: "watches", want: []string{"counters", "watches"}},
		{phase: "hostname", want: []string{"counters", "watches", "hostname"}},
		{phase: "collectors", want: []string{"counters", "watches", "hostname", "collectors"}, built: true},
	}
	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) { checkBuildCancellation(t, tt.phase, tt.want, tt.built) })
	}
}

// TestStartupDiscardsCanceledRegistry checks startup rejects and cleans a cancelled candidate.
func TestStartupDiscardsCanceledRegistry(t *testing.T) {
	restoreStartupSeams(t)
	coord := newTestCoordinator(t)
	var cleanups atomic.Int32
	candidate := registryWithCleanup(t, func() { cleanups.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buildRegistryFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*registry.Registry, devicewatchlistmanager.Manager, error) {
		cancel()
		return candidate, topologyManager(), nil
	}
	got, _, err := buildInitialRegistry(ctx, &appconfig.Config{}, coord)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, got)
	require.Equal(t, int32(1), cleanups.Load())
}

// checkBuildCancellation blocks the chosen phase to check cancellation without
// timing races. built says the build returns its candidate, unretired, instead
// of the cancellation.
func checkBuildCancellation(t *testing.T, phase string, want []string, built bool) {
	t.Helper()
	restoreStartupSeams(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls []string
	visit := func(current string) {
		calls = append(calls, current)
		if current == phase {
			close(entered)
			<-release
		}
	}
	getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
		visit("counters")
		return &counters.CounterSet{}, nil
	}
	startWatchListManagerFunc = func(*counters.CounterSet, *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		visit("watches")
		return topologyManager(), nil
	}
	getHostnameFunc = func(*appconfig.Config) (string, error) { visit("hostname"); return "host", nil }
	tracked := &synchronousTrackingCollector{}
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(tracked)
	initCollectorFactoryFunc = func(*counters.CounterSet, devicewatchlistmanager.Manager, string, *appconfig.Config) collector.Factory {
		return callbackFactory(func() ([]collector.EntityCollectorTuple, error) {
			visit("collectors")
			return []collector.EntityCollectorTuple{tuple}, nil
		})
	}
	if phase == "before" {
		cancel()
	}
	type result struct {
		registry *registry.Registry
		err      error
	}
	done := make(chan result, 1)
	go func() {
		reg, _, err := buildRegistry(ctx, &appconfig.Config{}, counters.DCPCapabilities{})
		done <- result{reg, err}
	}()
	if phase != "before" {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("build did not reach controlled phase")
		}
		cancel()
		close(release)
	}
	select {
	case got := <-done:
		if got.registry != nil {
			t.Cleanup(got.registry.Cleanup)
		}
		assertCanceledBuild(t, got.registry, got.err, built)
		assert.Equal(t, want, calls)
		assert.Zero(t, tracked.cleanupCalls.Load())
	case <-time.After(2 * time.Second):
		t.Fatal("build did not return after cancellation")
	}
}

// assertCanceledBuild checks the result of a canceled build: the candidate
// with no error when built, otherwise the cancellation and no registry.
func assertCanceledBuild(t *testing.T, reg *registry.Registry, err error, built bool) {
	t.Helper()

	if built {
		require.NoError(t, err)
		assert.NotNil(t, reg)
		return
	}

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, reg)
}

// NewCollectors records one factory build and returns the fixed candidate
// collectors used by TestPopulateRegistry_CallsNewCollectorsOnce.
func (f *countingFactory) NewCollectors() ([]collector.EntityCollectorTuple, error) {
	f.calls.Add(1)
	return f.collectors, f.err
}

// fakeMetricCollector is the minimal successful collector used to test cmd
// registry registration without DCGM watches.
type fakeMetricCollector struct{}

// GetMetrics returns an empty successful sample for registry assembly tests.
func (fakeMetricCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return collector.MetricsByCounter{}, nil
}

// Cleanup satisfies Collector; fakeMetricCollector acquires no resources.
func (fakeMetricCollector) Cleanup() {}

// completionTrackingCollector delays native cleanup until its test closes done.
// It verifies that registry registration failures preserve cleanup completion.
type completionTrackingCollector struct {
	done         chan struct{}
	cleanupCalls atomic.Int32
	cleanupOrder *[]int
	cleanupPanic any
	id           int
}

// GetMetrics returns an empty successful sample for registry assembly tests.
func (*completionTrackingCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return collector.MetricsByCounter{}, nil
}

// Cleanup records the rejected collector's requested cleanup.
func (c *completionTrackingCollector) Cleanup() {
	c.cleanupCalls.Add(1)
	if c.cleanupOrder != nil {
		*c.cleanupOrder = append(*c.cleanupOrder, c.id)
	}
	if c.cleanupPanic != nil {
		panic(c.cleanupPanic)
	}
}

// CleanupComplete exposes the test-controlled native cleanup completion signal.
func (c *completionTrackingCollector) CleanupComplete() <-chan struct{} {
	return c.done
}

// TestPopulateRegistry_CallsNewCollectorsOnce ensures each registry calls
// NewCollectors once. Extra calls install field watches on collectors that are
// never registered or cleaned up.
func TestPopulateRegistry_CallsNewCollectorsOnce(t *testing.T) {
	tuples := []collector.EntityCollectorTuple{{}, {}, {}}
	tuples[0].SetEntity(dcgm.FE_GPU)
	tuples[1].SetEntity(dcgm.FE_SWITCH)
	tuples[2].SetEntity(dcgm.FE_CPU)

	f := &countingFactory{collectors: tuples}
	r := registry.NewRegistry()

	got, err := populateRegistry(f, r)

	require.NoError(t, err)
	assert.Equal(t, int32(1), f.calls.Load(),
		"NewCollectors must be invoked exactly once per registry lifecycle")
	assert.Equal(t, len(tuples), got,
		"returned count equals the number of collectors registered")
}

// TestPopulateRegistryCleansUnregisteredCollectorsInReverseOrder verifies
// startup cleans every rejected candidate before closing the DCGM client.
func TestPopulateRegistryCleansUnregisteredCollectorsInReverseOrder(t *testing.T) {
	r := registry.NewRegistry()
	select {
	case <-r.Retire():
	case <-time.After(time.Second):
		t.Fatal("registry did not finish empty retirement")
	}

	cleanupOrder := make([]int, 0, 3)
	collectors := []*completionTrackingCollector{
		{done: make(chan struct{}), cleanupOrder: &cleanupOrder, id: 1},
		{done: make(chan struct{}), cleanupOrder: &cleanupOrder, id: 2},
		{done: make(chan struct{}), cleanupOrder: &cleanupOrder, id: 3},
	}
	entityGroups := []dcgm.Field_Entity_Group{dcgm.FE_GPU, dcgm.FE_SWITCH, dcgm.FE_CPU}
	tuples := make([]collector.EntityCollectorTuple, len(collectors))
	for index, candidate := range collectors {
		tuples[index].SetEntity(entityGroups[index])
		tuples[index].SetCollector(candidate)
	}

	_, err := populateRegistry(&countingFactory{collectors: tuples}, r)
	require.ErrorIs(t, err, registry.ErrRegistryShuttingDown)
	for _, candidate := range collectors {
		require.Equal(t, int32(1), candidate.cleanupCalls.Load())
	}
	assert.Equal(t, []int{3, 2, 1}, cleanupOrder)

	require.ErrorIs(t, waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: 10 * time.Millisecond}), context.DeadlineExceeded)

	close(collectors[2].done)
	close(collectors[1].done)
	require.ErrorIs(t, waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: 10 * time.Millisecond}), context.DeadlineExceeded,
		"cleanup must wait for every collector")
	close(collectors[0].done)
	require.NoError(t, waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: time.Second}))
}

// TestPopulateRegistryContinuesRollbackAfterCleanupPanic proves a panic from
// one rejected collector is surfaced while cleanup continues in reverse order.
func TestPopulateRegistryContinuesRollbackAfterCleanupPanic(t *testing.T) {
	r := registry.NewRegistry()
	select {
	case <-r.Retire():
	case <-time.After(time.Second):
		t.Fatal("registry did not finish empty retirement")
	}

	cleanupOrder := make([]int, 0, 3)
	cleanupPanic := errors.New("collector cleanup panicked")
	collectors := []*completionTrackingCollector{
		{done: make(chan struct{}), cleanupOrder: &cleanupOrder, id: 1},
		{done: make(chan struct{}), cleanupOrder: &cleanupOrder, cleanupPanic: cleanupPanic, id: 2},
		{done: make(chan struct{}), cleanupOrder: &cleanupOrder, id: 3},
	}
	entityGroups := []dcgm.Field_Entity_Group{dcgm.FE_GPU, dcgm.FE_SWITCH, dcgm.FE_CPU}
	tuples := make([]collector.EntityCollectorTuple, len(collectors))
	for index, candidate := range collectors {
		tuples[index].SetEntity(entityGroups[index])
		tuples[index].SetCollector(candidate)
	}

	_, err := populateRegistry(&countingFactory{collectors: tuples}, r)
	require.ErrorIs(t, err, registry.ErrRegistryShuttingDown)
	require.ErrorIs(t, err, cleanupPanic)
	assert.Equal(t, []int{3, 2, 1}, cleanupOrder)
	for _, candidate := range collectors {
		require.Equal(t, int32(1), candidate.cleanupCalls.Load())
	}

	var registrationErr collectorRegistrationError
	require.ErrorAs(t, err, &registrationErr)
	require.Len(t, registrationErr.completionSignals, 2)
	require.ErrorIs(t, waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: time.Second}), cleanupPanic)
	for _, index := range []int{2, 0} {
		close(collectors[index].done)
	}
	require.ErrorIs(t, waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: time.Second}), cleanupPanic,
		"successful cleanup signals must not hide another collector's cleanup panic")
}

// TestPopulateRegistryRejectsNilCleanupCompletion proves an absent signal does
// not count as evidence that a rejected collector released native resources.
func TestPopulateRegistryRejectsNilCleanupCompletion(t *testing.T) {
	r := registry.NewRegistry()
	select {
	case <-r.Retire():
	case <-time.After(time.Second):
		t.Fatal("registry did not finish empty retirement")
	}

	candidate := &completionTrackingCollector{id: 1}
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(candidate)

	_, err := populateRegistry(&countingFactory{collectors: []collector.EntityCollectorTuple{tuple}}, r)
	require.ErrorIs(t, err, registry.ErrRegistryShuttingDown)
	assert.Equal(t, int32(1), candidate.cleanupCalls.Load())
	assert.ErrorIs(t,
		waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: 10 * time.Millisecond}),
		context.DeadlineExceeded,
		"nil completion must remain unproven until the bounded deadline",
	)
}

// TestBuildRegistrySuccessAndHostnameFailure checks registry construction and hostname error handling.
func TestBuildRegistrySuccessAndHostnameFailure(t *testing.T) {
	restoreStartupSeams(t)

	counterSet := &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		},
	}
	manager := topologyManager()
	tuples := []collector.EntityCollectorTuple{{}}
	tuples[0].SetEntity(dcgm.FE_GPU)
	tuples[0].SetCollector(fakeMetricCollector{})

	getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
		return counterSet, nil
	}
	startWatchListManagerFunc = func(got *counters.CounterSet, _ *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		assert.Same(t, counterSet, got)
		return manager, nil
	}
	getHostnameFunc = func(*appconfig.Config) (string, error) {
		return "host-a", nil
	}
	factory := &countingFactory{collectors: tuples}
	initCollectorFactoryFunc = func(
		gotCounters *counters.CounterSet,
		gotManager devicewatchlistmanager.Manager,
		hostname string,
		_ *appconfig.Config,
	) collector.Factory {
		assert.Same(t, counterSet, gotCounters)
		assert.Equal(t, manager, gotManager)
		assert.Equal(t, "host-a", hostname)
		return factory
	}

	reg, gotManager, err := buildRegistry(context.Background(), &appconfig.Config{}, counters.DCPCapabilities{})
	require.NoError(t, err)
	require.NotNil(t, reg)
	t.Cleanup(reg.Cleanup)
	assert.Equal(t, manager, gotManager)
	assert.Equal(t, int32(1), factory.calls.Load())

	getHostnameFunc = func(*appconfig.Config) (string, error) {
		return "", errors.New("no hostname")
	}
	initCollectorFactoryFunc = func(
		*counters.CounterSet,
		devicewatchlistmanager.Manager,
		string,
		*appconfig.Config,
	) collector.Factory {
		t.Fatal("collector factory should not be initialized when hostname lookup fails")
		return nil
	}

	reg, gotManager, err = buildRegistry(context.Background(), &appconfig.Config{}, counters.DCPCapabilities{})
	require.Error(t, err)
	assert.Nil(t, reg)
	assert.Nil(t, gotManager)
	assert.Contains(t, err.Error(), "failed to get hostname")

	getHostnameFunc = func(*appconfig.Config) (string, error) {
		t.Fatal("hostname lookup should be skipped when NoHostname is enabled")
		return "", nil
	}
	factory = &countingFactory{collectors: tuples}
	initCollectorFactoryFunc = func(
		gotCounters *counters.CounterSet,
		gotManager devicewatchlistmanager.Manager,
		hostname string,
		_ *appconfig.Config,
	) collector.Factory {
		assert.Same(t, counterSet, gotCounters)
		assert.Equal(t, manager, gotManager)
		assert.Empty(t, hostname)
		return factory
	}

	reg, gotManager, err = buildRegistry(context.Background(), &appconfig.Config{NoHostname: true}, counters.DCPCapabilities{})
	require.NoError(t, err)
	require.NotNil(t, reg)
	t.Cleanup(reg.Cleanup)
	assert.Equal(t, manager, gotManager)
	assert.Equal(t, int32(1), factory.calls.Load())
}

// TestBuildRegistryReturnsFactoryError verifies that a failed candidate is
// never returned for startup or swapped into the live runtime during reload.
func TestBuildRegistryReturnsFactoryError(t *testing.T) {
	restoreStartupSeams(t)
	wantErr := errors.New("collector creation failed")
	counterSet := &counters.CounterSet{}
	manager := topologyManager()
	factory := &countingFactory{err: wantErr}
	getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
		return counterSet, nil
	}
	startWatchListManagerFunc = func(*counters.CounterSet, *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		return manager, nil
	}
	initCollectorFactoryFunc = func(
		*counters.CounterSet,
		devicewatchlistmanager.Manager,
		string,
		*appconfig.Config,
	) collector.Factory {
		return factory
	}

	reg, gotManager, err := buildRegistry(context.Background(), &appconfig.Config{NoHostname: true}, counters.DCPCapabilities{})

	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, reg)
	assert.Nil(t, gotManager)
	assert.Equal(t, int32(1), factory.calls.Load())
}

// TestBuildRegistryReturnsCounterLoadError checks counter loading errors leave registry construction.
func TestBuildRegistryReturnsCounterLoadError(t *testing.T) {
	restoreStartupSeams(t)

	getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
		return nil, errors.New("bad collectors")
	}
	startWatchListManagerFunc = func(*counters.CounterSet, *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		t.Fatal("watch list manager should not start when counter loading fails")
		return nil, nil
	}

	reg, gotManager, err := buildRegistry(context.Background(), &appconfig.Config{}, counters.DCPCapabilities{})

	require.Error(t, err)
	assert.Nil(t, reg)
	assert.Nil(t, gotManager)
	assert.Contains(t, err.Error(), "failed to get counters")
	assert.Contains(t, err.Error(), "bad collectors")
}

// TestBuildRegistryReturnsWatchListManagerError checks watch list setup errors leave registry construction.
func TestBuildRegistryReturnsWatchListManagerError(t *testing.T) {
	restoreStartupSeams(t)

	counterSet := &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		},
	}

	getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
		return counterSet, nil
	}
	startWatchListManagerFunc = func(got *counters.CounterSet, _ *appconfig.Config) (devicewatchlistmanager.Manager, error) {
		assert.Same(t, counterSet, got)
		return nil, errors.New("bad watch groups")
	}
	getHostnameFunc = func(*appconfig.Config) (string, error) {
		t.Fatal("hostname should not be read when watch list manager startup fails")
		return "", nil
	}

	reg, gotManager, err := buildRegistry(context.Background(), &appconfig.Config{}, counters.DCPCapabilities{})

	require.Error(t, err)
	assert.Nil(t, reg)
	assert.Nil(t, gotManager)
	assert.Contains(t, err.Error(), "bad watch groups")
}

// blockingShutdownCollector holds a metrics request open until the test releases it.
// It verifies that exporter shutdown returns after the configured drain deadline.
type blockingShutdownCollector struct {
	started      chan<- struct{}
	release      <-chan struct{}
	cleanupCalls *atomic.Int32
}

// GetMetrics blocks until the test releases the in-flight scrape.
func (c blockingShutdownCollector) GetMetrics() (collector.MetricsByCounter, error) {
	close(c.started)
	<-c.release

	return collector.MetricsByCounter{}, nil
}

// Cleanup records that the registry cleaned up the collector after the scrape finishes.
func (c blockingShutdownCollector) Cleanup() {
	c.cleanupCalls.Add(1)
}

// TestBuildRegistryRejectsDiscoveryFailureBeforeAllocatingWatches verifies that
// configured entity discovery must succeed before a candidate can own native watches.
func TestBuildRegistryRejectsDiscoveryFailureBeforeAllocatingWatches(t *testing.T) {
	cases := []struct {
		entity   dcgm.Field_Entity_Group
		counter  counters.Counter
		discover func(*mockdcgmprovider.MockDCGM, error)
	}{
		{dcgm.FE_GPU, testutils.SampleGPUTempCounter, func(native *mockdcgmprovider.MockDCGM, failure error) {
			native.EXPECT().GetAllDeviceCount().Return(uint(0), failure)
		}},
		{dcgm.FE_CPU, testutils.SampleCPUUtilTotalCounter, func(native *mockdcgmprovider.MockDCGM, failure error) {
			native.EXPECT().GetAllDeviceCount().Return(uint(0), nil)
			native.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
			native.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
			native.EXPECT().GetCPUHierarchy().Return(dcgm.CPUHierarchy_v2{}, failure)
		}},
		{dcgm.FE_SWITCH, testutils.SampleSwitchCurrentTempCounter, func(native *mockdcgmprovider.MockDCGM, failure error) {
			native.EXPECT().GetAllDeviceCount().Return(uint(0), nil)
			native.EXPECT().GetNvLinkLinkStatus().Return(nil, nil)
			native.EXPECT().GetGPUInstanceHierarchy().Return(dcgm.MigHierarchy_v2{}, nil)
			native.EXPECT().GetEntityGroupEntities(dcgm.FE_SWITCH).Return(nil, failure)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.entity.String(), func(t *testing.T) {
			restoreStartupSeams(t)
			native := mockdcgmprovider.NewMockDCGM(gomock.NewController(t))
			native.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusUnknown).AnyTimes()
			previous := dcgmprovider.Client()
			dcgmprovider.SetClient(native)
			t.Cleanup(func() { dcgmprovider.SetClient(previous) })
			failure := errors.New("native discovery unavailable")
			config := &appconfig.Config{NoHostname: true, GPUDeviceOptions: appconfig.DeviceOptions{Flex: true}}
			counter := tc.counter
			cs := &counters.CounterSet{DCGMCounters: counters.CounterList{counter}}
			getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
				return cs, nil
			}
			// Metadata selects the failing entity. Permanent watches
			// must not start even though GPU discovery completed before CPU or switch.
			native.EXPECT().FieldGetByID(counter.FieldID).Return(testutils.SampleFieldIDToFieldMeta[counter.FieldID], nil).AnyTimes()
			tc.discover(native, failure)
			candidate, manager, err := buildRegistry(context.Background(), config, counters.DCPCapabilities{})
			require.ErrorIs(t, err, failure)
			require.Nil(t, candidate)
			require.Nil(t, manager)
		})
	}
}

// TestPopulateRegistryRollbackTimeoutLeavesNoWaiter keeps a cleanup channel
// open so TestMain's goroutine check detects any unbounded background waiter.
func TestPopulateRegistryRollbackTimeoutLeavesNoWaiter(t *testing.T) {
	r := registry.NewRegistry()
	<-r.Retire()
	candidate := &completionTrackingCollector{done: make(chan struct{})}
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(candidate)

	_, err := populateRegistry(&countingFactory{collectors: []collector.EntityCollectorTuple{tuple}}, r)
	require.ErrorIs(t, err, registry.ErrRegistryShuttingDown)
	require.ErrorIs(t, waitForCollectorRollback(context.Background(), err, cleanupWait{timeout: 10 * time.Millisecond}), context.DeadlineExceeded)
	require.Equal(t, int32(1), candidate.cleanupCalls.Load())
}

// TestStartDeviceWatchListManagerRejectsWindowOverrideOnNormalField checks the
// startup check, which covers a ConfigMap source read at startup, names the YAML path.
func TestStartDeviceWatchListManagerRejectsWindowOverrideOnNormalField(t *testing.T) {
	counterSet := &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		},
	}
	matcher, err := appconfig.ParseFieldMatcher([]string{"DCGM_FI_DEV_GPU_TEMP"}, true)
	require.NoError(t, err)
	config := &appconfig.Config{
		MetricSource:    appconfig.MetricSource{Kind: appconfig.MetricSourceConfigMap},
		WindowOverrides: []appconfig.WindowOverride{{Path: "metrics[0].overrides[0]", Match: matcher}},
	}

	got, err := startDeviceWatchListManager(counterSet, config)

	assert.Nil(t, got)
	require.ErrorContains(t, err, "metrics[0].overrides[0].match[0]: selects DCGM_FI_DEV_GPU_TEMP, which is not a window counter")
}
