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

package cmd

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgmprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mocknvmlprovider "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/nvmlprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
)

// migFieldLevels is the entity level DCGM declares for each test field, as
// listed in DCGM's field metadata, so no test fakes a level for a real field.
var migFieldLevels = map[dcgm.Short]dcgm.Field_Entity_Group{
	dcgm.DCGM_FI_DEV_FB_USED:              dcgm.FE_GPU_CI,
	dcgm.DCGM_FI_DEV_XID_ERRORS:           dcgm.FE_GPU_CI,
	dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE:    dcgm.FE_GPU_CI,
	dcgm.DCGM_FI_PROF_DRAM_ACTIVE:         dcgm.FE_GPU_I,
	dcgm.DCGM_FI_DEV_POWER_USAGE:          dcgm.FE_GPU,
	dcgm.DCGM_FI_PROF_PCIE_TX_BYTES:       dcgm.FE_GPU,
	dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS: dcgm.FE_GPU,
	dcgm.DCGM_FI_DEV_BOARD_SERIAL:         dcgm.FE_GPU,
	dcgm.DCGM_FI_DRIVER_VERSION:           dcgm.FE_NONE,
}

// migLayout is a node where each GPU has the given number of GPU instances,
// each with one compute instance. A GPU with no instances is not in MIG mode.
type migLayout []int

// gpuInstanceID returns the DCGM entity ID of the n-th GPU instance of gpu.
// IDs count up across GPUs in order.
func (l migLayout) gpuInstanceID(gpu, n int) uint {
	id := n
	for _, instances := range l[:gpu] {
		id += instances
	}
	return uint(id) //nolint:gosec // test layouts are small
}

// computeInstanceID returns the DCGM entity ID of the compute instance in the
// n-th GPU instance of gpu. It is offset so it cannot be mistaken for a GPU
// instance ID.
func (l migLayout) computeInstanceID(gpu, n int) uint {
	return l.gpuInstanceID(gpu, n) + 100
}

// hierarchy returns the DCGM MIG hierarchy of the layout.
func (l migLayout) hierarchy() dcgm.MigHierarchy_v2 {
	var hierarchy dcgm.MigHierarchy_v2
	add := func(entity, parent dcgm.GroupEntityPair, info dcgm.MigEntityInfo) {
		hierarchy.EntityList[hierarchy.Count] = dcgm.MigHierarchyInfo_v2{Entity: entity, Parent: parent, Info: info}
		hierarchy.Count++
	}

	for gpu, instances := range l {
		for n := range instances {
			gpuEntity := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: uint(gpu)} //nolint:gosec // test layouts are small
			instance := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: l.gpuInstanceID(gpu, n)}
			compute := dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: l.computeInstanceID(gpu, n)}
			add(instance, gpuEntity, dcgm.MigEntityInfo{NvmlInstanceId: uint(n)}) //nolint:gosec // test layouts are small
			add(compute, instance, dcgm.MigEntityInfo{})
		}
	}
	return hierarchy
}

// migNode is a fake DCGM host engine for one layout. It records the entities
// and fields of every watch and every latest-value read.
type migNode struct {
	mu          sync.Mutex
	groups      map[uintptr][]dcgm.GroupEntityPair
	fieldGroups map[uintptr][]dcgm.Short
	watches     []string
	reads       []migRead
}

// migRead is one latest-value read of one entity.
type migRead struct {
	entity dcgm.GroupEntityPair
	fields []dcgm.Short
}

// newMIGNode installs a fake DCGM and NVML client that serve layout.
func newMIGNode(t *testing.T, layout migLayout) *migNode {
	t.Helper()
	ctrl := gomock.NewController(t)
	client := mockdcgmprovider.NewMockDCGM(ctrl)
	previous := dcgmprovider.Client()
	dcgmprovider.SetClient(client)
	t.Cleanup(func() { dcgmprovider.SetClient(previous) })

	nvml := mocknvmlprovider.NewMockNVML(ctrl)
	previousNVML := nvmlprovider.Client()
	nvmlprovider.SetClient(nvml)
	t.Cleanup(func() { nvmlprovider.SetClient(previousNVML) })
	nvml.EXPECT().GetGPUInstanceProfileName(gomock.Any(), gomock.Any()).Return("3g.test", nil).AnyTimes()

	node := &migNode{groups: map[uintptr][]dcgm.GroupEntityPair{}, fieldGroups: map[uintptr][]dcgm.Short{}}
	client.EXPECT().FieldGetByID(gomock.Any()).DoAndReturn(func(field dcgm.Short) (dcgm.FieldMeta, error) {
		level, ok := migFieldLevels[field]
		if !ok {
			return dcgm.FieldMeta{}, fmt.Errorf("unexpected metadata field %d", field)
		}
		return dcgm.FieldMeta{FieldID: field, EntityLevel: level}, nil
	}).AnyTimes()
	client.EXPECT().GetAllDeviceCount().Return(uint(len(layout)), nil).AnyTimes()
	client.EXPECT().GetGPUStatus(gomock.Any()).Return(dcgm.EntityStatusOk).AnyTimes()
	client.EXPECT().GetDeviceInfo(gomock.Any()).DoAndReturn(func(id uint) (dcgm.Device, error) {
		return dcgm.Device{GPU: id, UUID: fmt.Sprintf("GPU-%d", id)}, nil
	}).AnyTimes()
	client.EXPECT().GetNvLinkLinkStatus().Return(nil, nil).AnyTimes()
	client.EXPECT().GetGPUInstanceHierarchy().Return(layout.hierarchy(), nil).AnyTimes()
	client.EXPECT().EntitiesGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(entities []dcgm.GroupEntityPair, _ []dcgm.Short, _ uint) ([]dcgm.FieldValue_v2, error) {
			values := make([]dcgm.FieldValue_v2, 0, len(entities))
			for _, entity := range entities {
				values = append(values, dcgm.FieldValue_v2{EntityGroupId: entity.EntityGroupId, EntityID: entity.EntityId})
			}
			return values, nil
		}).AnyTimes()
	client.EXPECT().Fv2_String(gomock.Any()).Return("3g.test").AnyTimes()
	client.EXPECT().UpdateAllFields().Return(nil).AnyTimes()
	client.EXPECT().GetValuesSince(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, time.Time{}, nil).AnyTimes()
	client.EXPECT().EntityGetLatestValues(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(node.latestValues).AnyTimes()
	node.recordWatches(client)
	return node
}

// recordWatches makes client hand out group and field-group handles and
// record each watch as "entities: fields", for example "GPU x8: [155]".
func (n *migNode) recordWatches(client *mockdcgmprovider.MockDCGM) {
	client.EXPECT().CreateGroup(gomock.Any()).DoAndReturn(func(string) (dcgm.GroupHandle, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		var group dcgm.GroupHandle
		group.SetHandle(uintptr(len(n.groups) + 1))
		n.groups[group.GetHandle()] = nil
		return group, nil
	}).AnyTimes()
	client.EXPECT().AddEntityToGroup(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(group dcgm.GroupHandle, kind dcgm.Field_Entity_Group, id uint) error {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.groups[group.GetHandle()] = append(n.groups[group.GetHandle()], dcgm.GroupEntityPair{EntityGroupId: kind, EntityId: id})
			return nil
		}).AnyTimes()
	client.EXPECT().FieldGroupCreate(gomock.Any(), gomock.Any()).DoAndReturn(func(_ string, fields []dcgm.Short) (dcgm.FieldHandle, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		var handle dcgm.FieldHandle
		handle.SetHandle(uintptr(len(n.fieldGroups) + 1))
		n.fieldGroups[handle.GetHandle()] = slices.Sorted(slices.Values(fields))
		return handle, nil
	}).AnyTimes()
	client.EXPECT().WatchFieldsWithGroupExNoRefresh(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(fields dcgm.FieldHandle, group dcgm.GroupHandle, _ int64, _ float64, _ int32) error {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.watches = append(n.watches, fmt.Sprintf("%s: %v", entityCounts(n.groups[group.GetHandle()]), n.fieldGroups[fields.GetHandle()]))
			return nil
		}).AnyTimes()
	client.EXPECT().UnwatchFields(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	client.EXPECT().FieldGroupDestroy(gomock.Any()).Return(nil).AnyTimes()
	client.EXPECT().DestroyGroup(gomock.Any()).Return(nil).AnyTimes()
}

// entityCounts describes a group by how many entities of each kind it holds,
// for example "GPU x8, GPU Instance x16".
func entityCounts(entities []dcgm.GroupEntityPair) string {
	var parts []string
	for _, kind := range []dcgm.Field_Entity_Group{dcgm.FE_GPU, dcgm.FE_GPU_I, dcgm.FE_GPU_CI} {
		count := 0
		for _, entity := range entities {
			if entity.EntityGroupId == kind {
				count++
			}
		}
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%s x%d", kind, count))
		}
	}
	return strings.Join(parts, ", ")
}

// latestValues records the read and returns the value 7 for every field. The
// board serial is "serial-<entity group>-<entity ID>", so a label read on the
// wrong entity shows in the rendered labels.
func (n *migNode) latestValues(group dcgm.Field_Entity_Group, id uint, fields []dcgm.Short) ([]dcgm.FieldValue_v1, error) {
	n.mu.Lock()
	n.reads = append(n.reads, migRead{dcgm.GroupEntityPair{EntityGroupId: group, EntityId: id}, slices.Clone(fields)})
	n.mu.Unlock()

	values := make([]dcgm.FieldValue_v1, 0, len(fields))
	for _, field := range fields {
		value := dcgm.FieldValue_v1{FieldID: field, FieldType: dcgm.DCGM_FT_INT64}
		binary.LittleEndian.PutUint64(value.Value[:], 7)
		if field == dcgm.DCGM_FI_DEV_BOARD_SERIAL {
			value.FieldType = dcgm.DCGM_FT_STRING
			copy(value.Value[:], fmt.Sprintf("serial-%d-%d", group, id))
		}
		values = append(values, value)
	}
	return values, nil
}

// watchList returns every recorded watch, sorted so the order of creation
// does not matter.
func (n *migNode) watchList() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Sorted(slices.Values(n.watches))
}

// gpuReadsOf counts, for each physical GPU, the reads that included field.
func (n *migNode) gpuReadsOf(field dcgm.Short) map[uint]int {
	n.mu.Lock()
	defer n.mu.Unlock()
	counts := map[uint]int{}
	for _, read := range n.reads {
		if read.entity.EntityGroupId == dcgm.FE_GPU && slices.Contains(read.fields, field) {
			counts[read.entity.EntityId]++
		}
	}
	return counts
}

// gpuLevelReadsOnInstances lists reads of GPU-level fields on entities that
// are not physical GPUs.
func (n *migNode) gpuLevelReadsOnInstances() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var wrong []string
	for _, read := range n.reads {
		if read.entity.EntityGroupId == dcgm.FE_GPU {
			continue
		}
		for _, field := range read.fields {
			if migFieldLevels[field] == dcgm.FE_GPU {
				wrong = append(wrong, fmt.Sprintf("field %d on %s %d", field, read.entity.EntityGroupId, read.entity.EntityId))
			}
		}
	}
	return wrong
}

// migCounters returns a counter set with the named DCGM gauges, the named
// label fields and, when clockEvents is set, the clock events count family.
func migCounters(gauges, labels []string, clockEvents bool) *counters.CounterSet {
	set := &counters.CounterSet{}
	for _, name := range gauges {
		set.DCGMCounters = append(set.DCGMCounters, counters.Counter{FieldID: dcgm.GetFieldIDOrPanic(name), FieldName: name, PromType: "gauge"})
	}
	for _, name := range labels {
		set.DCGMCounters = append(set.DCGMCounters, counters.Counter{FieldID: dcgm.GetFieldIDOrPanic(name), FieldName: name, PromType: "label"})
	}
	if clockEvents {
		name := counters.DCGMExpClockEventsCount
		set.ExporterCounters = append(set.ExporterCounters, counters.Counter{
			FieldID: dcgm.Short(counters.DCGMFields[name]), FieldName: name, PromType: "gauge",
		})
	}
	return set
}

// startMIGNode runs the real registry build for layout with the device
// options and counters.
func startMIGNode(
	t *testing.T, layout migLayout, options appconfig.DeviceOptions, set *counters.CounterSet,
) (*migNode, *registry.Registry) {
	t.Helper()
	restoreStartupSeams(t)
	node := newMIGNode(t, layout)
	getCountersFunc = func(context.Context, *appconfig.Config, counters.DCPCapabilities) (*counters.CounterSet, error) {
		return set, nil
	}

	config := &appconfig.Config{
		NoHostname:                 true,
		GPUDeviceOptions:           options,
		CollectInterval:            1000,
		ClockEventsCountWindowSize: 1000,
	}
	reg, _, err := buildRegistry(context.Background(), config, counters.DCPCapabilities{})
	require.NoError(t, err)
	t.Cleanup(func() { reg.Cleanup(); <-reg.Retire() })
	return node, reg
}

// Field IDs in the expected watches: 1 driver version, 53 board serial, 112
// clock events, 155 power, 230 XID, 252 FB used, 1001 GR engine, 1005 DRAM, 1009
// PCIe TX. DCGM declares 53, 112, 155 and 1009 at GPU level, so they are watched
// on physical GPUs only whenever MIG instances are selected. The GPU collector
// watches 112 as the clock events source, and the clock events collector adds
// its own watch of it.
func TestGPULevelFieldsAreWatchedOnPhysicalGPUs(t *testing.T) {
	eightMIGGPUs := migLayout{2, 2, 2, 2, 2, 2, 2, 2}
	gauges := []string{
		"DCGM_FI_DEV_FB_USED", "DCGM_FI_PROF_GR_ENGINE_ACTIVE", "DCGM_FI_PROF_DRAM_ACTIVE",
		"DCGM_FI_DEV_POWER_USAGE", "DCGM_FI_PROF_PCIE_TX_BYTES", "DCGM_FI_DEV_XID_ERRORS",
	}
	flex := appconfig.DeviceOptions{Flex: true}
	for _, tt := range []struct {
		name    string
		layout  migLayout
		options appconfig.DeviceOptions
		labels  []string
		want    []string
	}{
		{
			name: "flex on eight MIG GPUs", layout: eightMIGGPUs, options: flex,
			want: []string{
				"GPU Compute Instance x16: [230 252 1001]",
				"GPU Instance x16: [230 1005]",
				"GPU x8: [112 155 1009]",
				"GPU x8: [112]",
				"GPU x8: [230]",
			},
		},
		{
			// The board serial is read on physical GPUs only; the driver
			// version has no entity level and stays on every entity.
			name: "GPU-level label", layout: eightMIGGPUs, options: flex,
			labels: []string{"DCGM_FI_DEV_BOARD_SERIAL", "DCGM_FI_DRIVER_VERSION"},
			want: []string{
				"GPU Compute Instance x16: [1 230 252 1001]",
				"GPU Instance x16: [1 230 1005]",
				"GPU x8: [1 230]",
				"GPU x8: [1 53 112 155 1009]",
				"GPU x8: [112]",
			},
		},
		{
			name: "whole GPU and MIG GPU on one node", layout: migLayout{0, 2}, options: flex,
			want: []string{
				"GPU x1, GPU Compute Instance x2: [230 252 1001]",
				"GPU x1, GPU Instance x2: [230 1005]",
				"GPU x1: [230]",
				"GPU x2: [112 155 1009]",
				"GPU x2: [112]",
			},
		},
		{
			name: "instances of one GPU", layout: eightMIGGPUs,
			options: appconfig.DeviceOptions{MinorRange: []int{int(eightMIGGPUs.gpuInstanceID(5, 0)), int(eightMIGGPUs.gpuInstanceID(5, 1))}},
			want: []string{
				"GPU Compute Instance x2: [230 252 1001]",
				"GPU Instance x2: [230 1005]",
				"GPU x1: [112 155 1009]",
				"GPU x1: [112]",
				"GPU x1: [230]",
			},
		},
		{
			// Without selected MIG instances the watches are the same as
			// before GPU-level fields were placed on physical GPUs.
			name: "MIG GPUs selected as whole GPUs", layout: eightMIGGPUs, options: appconfig.DeviceOptions{MajorRange: []int{-1}},
			want: []string{
				"GPU x8: [112 155 230 1005 1009]",
				"GPU x8: [112]",
				"GPU x8: [230 252 1001]",
			},
		},
		{
			name: "GPUs without MIG", layout: migLayout{0, 0, 0, 0}, options: flex,
			want: []string{
				"GPU x4: [112 155 230 1005 1009]",
				"GPU x4: [112]",
				"GPU x4: [230 252 1001]",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node, reg := startMIGNode(t, tt.layout, tt.options, migCounters(gauges, tt.labels, true))

			_, err := reg.Gather()
			require.NoError(t, err)

			assert.Equal(t, tt.want, node.watchList())
			assert.Empty(t, node.gpuLevelReadsOnInstances())
		})
	}
}

// metricKey describes where one sample is reported: its GPU and, for an
// instance, its GPU instance and compute instance IDs.
func metricKey(m collector.Metric) string {
	return fmt.Sprintf("gpu=%s GPU_I_ID=%s GPU_CI_ID=%s", m.GPU, m.GPUInstanceID, m.GPUComputeInstanceID)
}

// On a MIG node in flex mode, a GPU-level gauge has one series per physical
// GPU without instance labels, while instance-level gauges keep one series per
// instance. Every DCGM field series shows the board serial of its physical GPU,
// and one scrape reads each physical GPU once.
func TestGPULevelFieldsReportOneSeriesPerGPU(t *testing.T) {
	layout := migLayout{2, 2}
	gauges := []string{"DCGM_FI_DEV_FB_USED", "DCGM_FI_PROF_DRAM_ACTIVE", "DCGM_FI_DEV_POWER_USAGE"}
	set := migCounters(gauges, []string{"DCGM_FI_DEV_BOARD_SERIAL"}, true)
	node, reg := startMIGNode(t, layout, appconfig.DeviceOptions{Flex: true}, set)

	metrics, err := reg.Gather()
	require.NoError(t, err)

	want := map[string][]string{
		"DCGM_FI_DEV_FB_USED": {
			"gpu=0 GPU_I_ID=0 GPU_CI_ID=0", "gpu=0 GPU_I_ID=1 GPU_CI_ID=0",
			"gpu=1 GPU_I_ID=0 GPU_CI_ID=0", "gpu=1 GPU_I_ID=1 GPU_CI_ID=0",
		},
		"DCGM_FI_PROF_DRAM_ACTIVE": {
			"gpu=0 GPU_I_ID=0 GPU_CI_ID=", "gpu=0 GPU_I_ID=1 GPU_CI_ID=",
			"gpu=1 GPU_I_ID=0 GPU_CI_ID=", "gpu=1 GPU_I_ID=1 GPU_CI_ID=",
		},
		"DCGM_FI_DEV_POWER_USAGE":        {"gpu=0 GPU_I_ID= GPU_CI_ID=", "gpu=1 GPU_I_ID= GPU_CI_ID="},
		counters.DCGMExpClockEventsCount: {"gpu=0 GPU_I_ID= GPU_CI_ID=", "gpu=1 GPU_I_ID= GPU_CI_ID="},
	}
	got := map[string][]string{}
	for _, families := range metrics {
		for counter, samples := range families {
			for _, sample := range samples {
				got[counter.FieldName] = append(got[counter.FieldName], metricKey(sample))
				if counter.FieldName == counters.DCGMExpClockEventsCount {
					continue
				}
				assert.Equal(t, fmt.Sprintf("serial-%d-%s", dcgm.FE_GPU, sample.GPU), sample.Labels["DCGM_FI_DEV_BOARD_SERIAL"],
					"%s %s", counter.FieldName, metricKey(sample))
			}
		}
	}
	for name := range got {
		slices.Sort(got[name])
	}
	assert.Equal(t, want, got)
	assert.Empty(t, node.gpuLevelReadsOnInstances())
	assert.Equal(t, map[uint]int{0: 1, 1: 1}, node.gpuReadsOf(dcgm.DCGM_FI_DEV_BOARD_SERIAL))
}

// With only instance-level gauges, the instance series still show the board
// serial of their physical GPU, read once per GPU and scrape.
func TestGPULevelLabelOnInstanceSeries(t *testing.T) {
	set := migCounters([]string{"DCGM_FI_PROF_DRAM_ACTIVE"}, []string{"DCGM_FI_DEV_BOARD_SERIAL"}, false)
	node, reg := startMIGNode(t, migLayout{2, 2}, appconfig.DeviceOptions{Flex: true}, set)

	metrics, err := reg.Gather()
	require.NoError(t, err)

	var serials []string
	for _, samples := range metrics[dcgm.FE_GPU] {
		for _, sample := range samples {
			serials = append(serials, sample.GPU+" "+sample.Labels["DCGM_FI_DEV_BOARD_SERIAL"])
		}
	}
	slices.Sort(serials)
	assert.Equal(t, []string{"0 serial-1-0", "0 serial-1-0", "1 serial-1-1", "1 serial-1-1"}, serials)
	assert.Empty(t, node.gpuLevelReadsOnInstances())
	assert.Equal(t, map[uint]int{0: 1, 1: 1}, node.gpuReadsOf(dcgm.DCGM_FI_DEV_BOARD_SERIAL))
}
