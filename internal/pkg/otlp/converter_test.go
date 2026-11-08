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

package otlp

import (
	"bytes"
	"context"
	"maps"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/rendermetrics"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
)

const converterSampleTime int64 = 1_700_000_000_123_456

// Two GPUs and two workloads: two native fields, three health states, four event families with two subseries, and P2P.
const converterMixedPoints = 2 * 2 * (2 + 3 + 4*2 + 1)

var converterResource = ResourceIdentity{ServiceName: "dcgm-exporter", ServiceVersion: "test", ServiceInstanceID: "process-id", Hostname: "node"}

// converterPoints flattens gauge and sum points for shared output assertions.
func converterPoints(result *metricspb.ResourceMetrics) []*metricspb.NumberDataPoint {
	var points []*metricspb.NumberDataPoint
	for _, scope := range result.ScopeMetrics {
		for _, metric := range scope.Metrics {
			points = append(points, metric.GetGauge().GetDataPoints()...)
			points = append(points, metric.GetSum().GetDataPoints()...)
		}
	}
	return points
}

type converterMappingCase struct {
	id                       signalcatalog.SignalID
	field                    dcgm.Short
	name, unit, value        string
	integer                  int64
	double                   float64
	isDouble, sum, monotonic bool
	key, attribute           string
}

// TestConverterDefaultMappings catches wrong sources, units, wire types, and missing default mappings.
func TestConverterDefaultMappings(t *testing.T) {
	tests := []converterMappingCase{
		{id: "gpu.driver.version", field: dcgm.DCGM_FI_DRIVER_VERSION, name: "hw.driver_version", value: "550.54"},
		{id: "gpu.clock.sm", field: dcgm.DCGM_FI_DEV_SM_CLOCK, name: "com.nvidia.gpu.sm.clock", unit: "Hz", value: "1234", integer: 1_234_000_000},
		{id: "gpu.clock.memory", field: dcgm.DCGM_FI_DEV_MEM_CLOCK, name: "com.nvidia.gpu.memory.clock", unit: "Hz", value: "2000", integer: 2_000_000_000},
		{id: "gpu.temperature", field: dcgm.DCGM_FI_DEV_GPU_TEMP, name: "hw.temperature", unit: "Cel", value: "42", integer: 42, key: "hw.sensor_location", attribute: "gpu"},
		{id: "gpu.memory.temperature", field: dcgm.DCGM_FI_DEV_MEMORY_TEMP, name: "hw.temperature", unit: "Cel", value: "51", integer: 51, key: "hw.sensor_location", attribute: "memory"},
		{id: "gpu.power.usage", field: dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS, name: "hw.power", unit: "W", value: "123.456000", double: 123.456, isDouble: true},
		{id: "gpu.energy.total", field: dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, name: "hw.energy", unit: "J", value: "12345", double: 12.345, isDouble: true, sum: true, monotonic: true},
		{id: "gpu.utilization", field: dcgm.DCGM_FI_DEV_GPU_UTIL, name: "hw.gpu.utilization", unit: "1", value: "75", double: .75, isDouble: true, key: "hw.gpu.task", attribute: "general"},
		{id: "gpu.memory_copy.utilization", field: dcgm.DCGM_FI_DEV_MEM_COPY_UTIL, name: "com.nvidia.gpu.memory_copy.utilization", unit: "1", value: "25", double: .25, isDouble: true},
		{id: "gpu.encoder.utilization", field: dcgm.DCGM_FI_DEV_ENC_UTIL, name: "hw.gpu.utilization", unit: "1", value: "50", double: .5, isDouble: true, key: "hw.gpu.task", attribute: "encoder"},
		{id: "gpu.decoder.utilization", field: dcgm.DCGM_FI_DEV_DEC_UTIL, name: "hw.gpu.utilization", unit: "1", value: "10", double: .1, isDouble: true, key: "hw.gpu.task", attribute: "decoder"},
		{id: "gpu.memory.usage", field: dcgm.DCGM_FI_DEV_FB_USED, name: "hw.gpu.memory.usage", unit: "By", value: "8", integer: 8_388_608, sum: true},
		{id: "gpu.memory.free", field: dcgm.DCGM_FI_DEV_FB_FREE, name: "com.nvidia.gpu.memory.free", unit: "By", value: "16", integer: 16_777_216},
		{id: "gpu.memory.reserved", field: dcgm.DCGM_FI_DEV_FB_RESERVED, name: "com.nvidia.gpu.memory.reserved", unit: "By", value: "2", integer: 2_097_152},
		{id: "gpu.memory.remapped_rows.corrected", field: dcgm.DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS, name: "com.nvidia.gpu.memory.remapped_rows", unit: "{row}", value: "11", integer: 11, sum: true, monotonic: true, key: "error.type", attribute: "corrected"},
		{id: "gpu.memory.remapped_rows.uncorrected", field: dcgm.DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS, name: "com.nvidia.gpu.memory.remapped_rows", unit: "{row}", value: "3", integer: 3, sum: true, monotonic: true, key: "error.type", attribute: "uncorrected"},
		{id: "gpu.memory.row_remap.failure", field: dcgm.DCGM_FI_DEV_ROW_REMAP_FAILURE, name: "com.nvidia.gpu.memory.row_remap.failure", unit: "1", value: "1", integer: 1},
		{id: "gpu.graphics_engine.utilization", field: dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE, name: "com.nvidia.gpu.graphics_engine.utilization", unit: "1", value: "0.125000", double: .125, isDouble: true},
		{id: "gpu.memory_interface.utilization", field: dcgm.DCGM_FI_PROF_DRAM_ACTIVE, name: "com.nvidia.gpu.memory_interface.utilization", unit: "1", value: "0.250000", double: .25, isDouble: true},
		{id: "gpu.tensor_pipe.utilization", field: dcgm.DCGM_FI_PROF_PIPE_TENSOR_ACTIVE, name: "com.nvidia.gpu.tensor_pipe.utilization", unit: "1", value: "0.500000", double: .5, isDouble: true},
		{id: "gpu.pcie.io.receive", field: dcgm.DCGM_FI_PROF_PCIE_RX_BYTES, name: "com.nvidia.gpu.pcie.io.rate", unit: "By/s", value: "1234", double: 1234, isDouble: true, key: "network.io.direction", attribute: "receive"},
		{id: "gpu.pcie.io.transmit", field: dcgm.DCGM_FI_PROF_PCIE_TX_BYTES, name: "com.nvidia.gpu.pcie.io.rate", unit: "By/s", value: "5678", double: 5678, isDouble: true, key: "network.io.direction", attribute: "transmit"},
		{id: "gpu.pcie.replay", field: dcgm.DCGM_FI_DEV_PCIE_REPLAY_COUNTER, name: "com.nvidia.gpu.pcie.replay", unit: "{retry}", value: "9007199254740993", integer: 9_007_199_254_740_993, sum: true, monotonic: true},
		{id: "gpu.xid.last", field: dcgm.DCGM_FI_DEV_XID_ERRORS, name: "com.nvidia.gpu.xid.last", unit: "1", value: "31", integer: 31},
		{id: "gpu.nvlink.throughput", field: dcgm.DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND, name: "com.nvidia.gpu.nvlink.throughput", unit: "By/s", value: "123456", integer: 123456},
	}
	covered := make([]signalcatalog.SignalID, 0, len(tests))
	for _, tt := range tests {
		covered = append(covered, tt.id)
		t.Run(string(tt.id), func(t *testing.T) {
			testConverterMapping(t, tt)
		})
	}
	defaults, err := signalcatalog.ResolveSet(signalcatalog.SetDefault)
	require.NoError(t, err)
	assert.ElementsMatch(t, defaults, covered)
}

type converterValidationCase struct {
	name  string
	id    signalcatalog.SignalID
	field dcgm.Short
	value string
	edit  func(*collector.Metric)
	ctx   func() (context.Context, context.CancelFunc)
	want  string
	mixed bool

	wantTime    uint64
	wantInteger int64
}

// TestConverterValidation catches malformed inputs before a payload or source period is published.
func TestConverterValidation(t *testing.T) {
	tests := []converterValidationCase{
		{name: "invalid integer", id: "gpu.clock.sm", field: dcgm.DCGM_FI_DEV_SM_CLOCK, value: "1.5", want: "parse"},
		{name: "MHz overflow", id: "gpu.clock.sm", field: dcgm.DCGM_FI_DEV_SM_CLOCK, value: "9223372036854775807", want: "overflow"},
		{name: "MiB overflow", id: "gpu.memory.usage", field: dcgm.DCGM_FI_DEV_FB_USED, value: "9223372036854775807", want: "overflow"},
		{name: "NaN", id: "gpu.power.usage", field: dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS, value: "NaN", want: "finite"},
		{name: "infinity", id: "gpu.power.usage", field: dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS, value: "+Inf", want: "finite"},
		{name: "negative GPU index", value: "42", edit: func(m *collector.Metric) { m.GPU = "-1" }, want: "com.nvidia.gpu.index"},
		{name: "overflowing GPU index", value: "42", edit: func(m *collector.Metric) { m.GPU = "9223372036854775808" }, want: "com.nvidia.gpu.index"},
		{name: "malformed GI index", id: "gpu.memory.usage", field: dcgm.DCGM_FI_DEV_FB_USED, value: "1", edit: func(m *collector.Metric) {
			m.Source.EntityGroup = dcgm.FE_GPU_I
			m.GPUInstanceID = "invalid"
		}, want: "com.nvidia.mig.gpu_instance.id"},
		{name: "negative total", id: "gpu.energy.total", field: dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, value: "-1", want: "nonnegative"},
		{name: "zero time", value: "42", edit: func(m *collector.Metric) { m.Timestamp = 0 }, want: "timestamp"},
		{name: "negative time", value: "42", edit: func(m *collector.Metric) { m.Timestamp = -1 }, want: "timestamp"},
		{name: "timestamp overflow", value: "42", edit: func(m *collector.Metric) { m.Timestamp = int64(uint64(math.MaxUint64)/1000) + 1 }, want: "timestamp"},
		{name: "maximum timestamp", value: "42", edit: func(m *collector.Metric) { m.Timestamp = int64(uint64(math.MaxUint64) / 1000) }, wantTime: 18_446_744_073_709_551_000, wantInteger: 42},
		{name: "negative temperature", value: "-10", wantTime: 1_700_000_000_123_456_000, wantInteger: -10},
		{name: "canceled mixed snapshot", value: "42", mixed: true, ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, cancel
		}, want: "canceled"},
		{name: "deadline mixed snapshot", value: "42", mixed: true, ctx: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Time{})
		}, want: "deadline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testConverterValidation(t, tt)
		})
	}
}

// cancelWhenContext cancels when conversion has produced a named output condition.
type cancelWhenContext struct {
	context.Context
	cancel context.CancelFunc
	ready  func() bool
}

// Err observes completed conversion work instead of counting context checks.
func (c *cancelWhenContext) Err() error {
	if c.ready() {
		c.cancel()
	}

	return c.Context.Err()
}

// TestConverterCancellationDuringConversion checks rollback and retry at named progress boundaries.
func TestConverterCancellationDuringConversion(t *testing.T) {
	for _, tt := range []struct {
		name  string
		ready func(map[pointIdentity]bool, map[sourceIdentity]sourcePeriod) bool
	}{
		{"after a native counter update", func(_ map[pointIdentity]bool, pending map[sourceIdentity]sourcePeriod) bool { return len(pending) != 0 }},
		{"after all health points before commit", func(seen map[pointIdentity]bool, _ map[sourceIdentity]sourcePeriod) bool {
			healthPoints := 0
			for key := range seen {
				if key.name == "hw.status" {
					healthPoints++
				}
			}
			return healthPoints == 6
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewConverter([]signalcatalog.SignalID{"gpu.energy.total", "gpu.health.status"}, converterResource)
			require.NoError(t, err)
			_, err = c.Convert(context.Background(), converterSnapshot(dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, "100"))
			require.NoError(t, err)
			periods := maps.Clone(c.periods)
			snapshot, saved := converterCancellationSnapshot(), converterCancellationSnapshot()
			seen := make(map[pointIdentity]bool)
			pending := make(map[sourceIdentity]sourcePeriod)

			base, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			ctx := &cancelWhenContext{Context: base, cancel: cancel, ready: func() bool { return tt.ready(seen, pending) }}
			result, err := c.convert(ctx, snapshot, seen, pending)
			require.ErrorIs(t, err, context.Canceled)
			assert.Nil(t, result)
			assert.Equal(t, periods, c.periods)
			assert.Equal(t, saved, snapshot)

			retry, err := c.Convert(context.Background(), snapshot)
			require.NoError(t, err)
			assert.Len(t, converterPoints(retry), 8)
			assert.Equal(t, saved, snapshot)
		})
	}
}

// converterCancellationSnapshot supplies a newer native total and health data across two workloads.
func converterCancellationSnapshot() registry.MetricsByCounterGroup {
	snapshot := converterSnapshot(dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, "110")
	for _, metrics := range snapshot[dcgm.FE_GPU] {
		metrics[0].Timestamp++
	}
	health := exporterFixture("gpu.health.status")
	health.GPUUUID = "GPU-test"
	snapshot[dcgm.FE_GPU][health.Counter] = []collector.Metric{health}
	addConverterContainers(snapshot)
	return snapshot
}

func TestNewConverterValidation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		ids      []signalcatalog.SignalID
		identity ResourceIdentity
	}{
		{"unknown signal", []signalcatalog.SignalID{"unknown"}, converterResource},
		{"duplicate signal", []signalcatalog.SignalID{"gpu.temperature", "gpu.temperature"}, converterResource},
		{"unsupported exporter signal", []signalcatalog.SignalID{"exporter.otlp.metric.send.failures"}, converterResource},
		{"missing resource identity", []signalcatalog.SignalID{"gpu.temperature"}, ResourceIdentity{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewConverter(tt.ids, tt.identity)
			require.Error(t, err)
			assert.Nil(t, c)
		})
	}
}

type converterLifecycleCase struct {
	name  string
	id    signalcatalog.SignalID
	field dcgm.Short
	steps []converterLifecycleStep
}

type converterLifecycleStep struct {
	value                               string
	time                                int64
	start                               uint64
	uuid, pod                           string
	gi                                  bool
	missing, malformed, canceled, fresh bool
	malformedExporter                   bool
	want                                string
}

// TestConverterCumulativeLifecycle proves source continuity and all-or-error updates.
func TestConverterCumulativeLifecycle(t *testing.T) {
	tests := []converterLifecycleCase{
		{"native reset and repeats", "gpu.energy.total", dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, []converterLifecycleStep{
			{value: "100", time: 1, start: 1}, {value: "120", time: 2, start: 1}, {value: "120", time: 2, start: 1}, {value: "5", time: 3, start: 3}, {missing: true, time: 4}, {value: "6", time: 5, start: 3},
		}},
		{"exact raw energy reset", "gpu.energy.total", dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, []converterLifecycleStep{
			{value: "1001", time: 1, start: 1}, {value: "1000", time: 2, start: 2}, {value: "9007199254740993", time: 3, start: 2}, {value: "9007199254740992", time: 4, start: 4},
		}},
		{"memory decrease keeps period", "gpu.memory.usage", dcgm.DCGM_FI_DEV_FB_USED, []converterLifecycleStep{
			{value: "8", time: 1, start: 1}, {value: "4", time: 2, start: 1}, {value: "3", time: 2, start: 1},
		}},
		{"independent GPU and GI", "gpu.memory.usage", dcgm.DCGM_FI_DEV_FB_USED, []converterLifecycleStep{
			{value: "8", time: 1, start: 1}, {value: "8", time: 2, start: 2, gi: true}, {value: "8", time: 3, start: 3, uuid: "GPU-other"}, {value: "4", time: 4, start: 1}, {value: "3", time: 5, start: 2, gi: true},
		}},
		{"metadata churn and new generation", "gpu.energy.total", dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, []converterLifecycleStep{
			{value: "100", time: 1, start: 1, pod: "old"}, {value: "110", time: 2, start: 1, pod: "new"}, {value: "120", time: 3, start: 3, fresh: true},
		}},
		{"rollback malformed and canceled snapshots", "gpu.energy.total", dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, []converterLifecycleStep{
			{value: "100", time: 1, start: 1}, {value: "5", time: 2, malformed: true, want: "parse"}, {value: "105", time: 3, start: 1}, {value: "2", time: 4, canceled: true, want: "canceled"}, {value: "106", time: 5, start: 1},
		}},
		{"rollback exporter after native cumulative", "gpu.energy.total", dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, []converterLifecycleStep{
			{value: "100", time: 1, malformedExporter: true, want: "health value"},
			{value: "105", time: 2, start: 2},
			{value: "5", time: 3, malformedExporter: true, want: "health value"},
			{value: "110", time: 4, start: 2},
		}},
		{"old and conflicting timestamps", "gpu.pcie.replay", dcgm.DCGM_FI_DEV_PCIE_REPLAY_COUNTER, []converterLifecycleStep{
			{value: "100", time: 2, start: 2}, {value: "110", time: 1, want: "timestamp"}, {value: "101", time: 2, want: "conflicting"}, {value: "120", time: 3, start: 2},
		}},
		{"independent corrected and uncorrected periods", "gpu.memory.remapped_rows.corrected", dcgm.DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS, []converterLifecycleStep{
			{value: "100", time: 1, start: 1}, {value: "5", time: 2, start: 2}, {value: "10", time: 3, start: 2},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testConverterLifecycle(t, tt)
		})
	}
}

type converterCompatibilityCase struct {
	name             string
	ids              []signalcatalog.SignalID
	fields           []dcgm.Short
	edit             func(registry.MetricsByCounterGroup)
	families, points int
	render           bool
	want             string
}

// TestConverterCompatibility checks composition and ownership at the renderer boundary.
func TestConverterCompatibility(t *testing.T) {
	defaults, err := signalcatalog.ResolveSet(signalcatalog.SetDefault)
	require.NoError(t, err)
	tests := []converterCompatibilityCase{
		{name: "mixed native and exporter sources", ids: converterMixedIDs(), fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION}, edit: addConverterMixedSources, families: 8, points: converterMixedPoints, render: true},
		{name: "duplicate health state identity", ids: []signalcatalog.SignalID{"gpu.health.status"}, want: "duplicate", edit: func(s registry.MetricsByCounterGroup) {
			m := exporterFixture("gpu.health.status")
			s[dcgm.FE_GPU][m.Counter] = []collector.Metric{m, m}
		}},
		{name: "merged families", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DEV_MEMORY_TEMP, dcgm.DCGM_FI_DEV_GPU_UTIL, dcgm.DCGM_FI_DEV_ENC_UTIL, dcgm.DCGM_FI_DEV_DEC_UTIL, dcgm.DCGM_FI_PROF_PCIE_RX_BYTES, dcgm.DCGM_FI_PROF_PCIE_TX_BYTES, dcgm.DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS, dcgm.DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS}, families: 4, points: 9, render: true},
		{name: "preserved entity scope", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, dcgm.DCGM_FI_DEV_FB_USED}, families: 3, points: 4, edit: addConverterEntityScopes},
		{name: "standalone container fanout", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED}, families: 1, points: 2, render: true, edit: addConverterContainers},
		{name: "Kubernetes projections without extra fanout", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED}, families: 1, points: 2, render: true, edit: addConverterKubernetesProjections},
		{name: "legacy and override coexistence", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_POWER_USAGE, dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS, dcgm.DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL, dcgm.DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND, dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS, dcgm.DCGM_FI_DEV_VGPU_INSTANCE_LICENSE_STATUS}, ids: append(append([]signalcatalog.SignalID{}, defaults...), "gpu.vgpu.license.status", "gpu.vgpu.instance.license.detail"), families: 2, points: 2, render: true, edit: setConverterLegacyValues},
		{name: "missing overrides and DCP readings", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_POWER_USAGE, dcgm.DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL}},
		{name: "empty snapshot"},
		{name: "duplicate final identity", fields: []dcgm.Short{dcgm.DCGM_FI_DEV_FB_USED}, want: "duplicate", edit: func(s registry.MetricsByCounterGroup) {
			for counter, metrics := range s[dcgm.FE_GPU] {
				s[dcgm.FE_GPU][counter] = append(metrics, metrics[0])
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testConverterCompatibility(t, tt, defaults)
		})
	}
}

func testConverterMapping(t *testing.T, tt converterMappingCase) {
	t.Helper()
	ids := []signalcatalog.SignalID{tt.id}
	snapshot := converterSnapshot(tt.field, tt.value)
	if tt.id == "gpu.driver.version" {
		ids = append(ids, "gpu.clock.sm")
		snapshot = converterSnapshot(dcgm.DCGM_FI_DEV_SM_CLOCK, "1")
	}
	c, err := NewConverter(ids, converterResource)
	require.NoError(t, err)
	result, err := c.Convert(context.Background(), snapshot)
	require.NoError(t, err)
	assert.Equal(t, "dcgm-exporter", converterAttributes(result.Resource.Attributes)["service.name"].GetStringValue())
	assert.Equal(t, "process-id", converterAttributes(result.Resource.Attributes)["service.instance.id"].GetStringValue())
	require.Len(t, result.ScopeMetrics, 1)
	require.Len(t, result.ScopeMetrics[0].Metrics, 1)
	metric := result.ScopeMetrics[0].Metrics[0]
	if tt.id == "gpu.driver.version" {
		assert.Equal(t, "com.nvidia.gpu.sm.clock", metric.Name)
		assert.Equal(t, "550.54", converterAttributes(metric.GetGauge().DataPoints[0].Attributes)["hw.driver_version"].GetStringValue())
		return
	}
	assert.Equal(t, tt.name, metric.Name)
	assert.Equal(t, tt.unit, metric.Unit)
	var points []*metricspb.NumberDataPoint
	if tt.sum {
		require.NotNil(t, metric.GetSum())
		assert.Equal(t, metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, metric.GetSum().AggregationTemporality)
		assert.Equal(t, tt.monotonic, metric.GetSum().IsMonotonic)
		points = metric.GetSum().DataPoints
	} else {
		require.NotNil(t, metric.GetGauge())
		points = metric.GetGauge().DataPoints
	}
	require.Len(t, points, 1)
	point := points[0]
	assert.Equal(t, uint64(1_700_000_000_123_456_000), point.TimeUnixNano)
	if tt.sum {
		assert.Equal(t, point.TimeUnixNano, point.StartTimeUnixNano)
	} else {
		assert.Zero(t, point.StartTimeUnixNano)
	}
	if tt.isDouble {
		require.IsType(t, &metricspb.NumberDataPoint_AsDouble{}, point.Value)
		assert.Equal(t, tt.double, point.GetAsDouble())
	} else {
		require.IsType(t, &metricspb.NumberDataPoint_AsInt{}, point.Value)
		assert.Equal(t, tt.integer, point.GetAsInt())
	}
	attrs := converterAttributes(point.Attributes)
	assert.Equal(t, "550.54", attrs["hw.driver_version"].GetStringValue())
	assert.Equal(t, "gpu", attrs["hw.type"].GetStringValue())
	if tt.key != "" {
		assert.Equal(t, tt.attribute, attrs[tt.key].GetStringValue())
	}
	if tt.key == "hw.sensor_location" {
		assert.Equal(t, "gpu:GPU-test:temperature:"+tt.attribute, attrs["hw.id"].GetStringValue())
		assert.Equal(t, "gpu:GPU-test", attrs["hw.parent"].GetStringValue())
	} else {
		assert.Equal(t, "gpu:GPU-test", attrs["hw.id"].GetStringValue())
	}
}

func testConverterValidation(t *testing.T, tt converterValidationCase) {
	t.Helper()
	if tt.id == "" {
		tt.id = "gpu.temperature"
		tt.field = dcgm.DCGM_FI_DEV_GPU_TEMP
	}
	ids := []signalcatalog.SignalID{tt.id}
	if tt.mixed {
		ids = converterMixedIDs()
	}
	c, err := NewConverter(ids, converterResource)
	require.NoError(t, err)
	snapshot := converterValidationSnapshot(tt)
	ctx := context.Background()
	if tt.ctx != nil {
		var cancel context.CancelFunc
		ctx, cancel = tt.ctx()
		defer cancel()
	}
	result, err := c.Convert(ctx, snapshot)
	if tt.want == "" {
		require.NoError(t, err)
		point := result.ScopeMetrics[0].Metrics[0].GetGauge().DataPoints[0]
		assert.Equal(t, tt.wantTime, point.TimeUnixNano)
		assert.Equal(t, tt.wantInteger, point.GetAsInt())
		return
	}
	require.ErrorContains(t, err, tt.want)
	if ctx.Err() != nil {
		assert.ErrorIs(t, err, ctx.Err())
		assert.Empty(t, c.periods)
		assert.Equal(t, converterValidationSnapshot(tt), snapshot)
		retry, retryErr := c.Convert(context.Background(), snapshot)
		require.NoError(t, retryErr)
		assert.NotEmpty(t, converterPoints(retry))
	}
	assert.Nil(t, result)
}

// converterValidationSnapshot creates fresh input for validation and mutation checks.
func converterValidationSnapshot(tt converterValidationCase) registry.MetricsByCounterGroup {
	if tt.mixed {
		return converterMixedSnapshot()
	}
	snapshot := converterSnapshot(tt.field, tt.value)
	if tt.edit != nil {
		for _, points := range snapshot[dcgm.FE_GPU] {
			tt.edit(&points[0])
		}
	}
	return snapshot
}

func testConverterLifecycle(t *testing.T, tt converterLifecycleCase) {
	t.Helper()
	ids := []signalcatalog.SignalID{tt.id, "gpu.temperature", "gpu.health.status"}
	if tt.id == "gpu.memory.remapped_rows.corrected" {
		ids = append(ids, "gpu.memory.remapped_rows.uncorrected")
	}
	c, err := NewConverter(ids, converterResource)
	require.NoError(t, err)
	for i, st := range tt.steps {
		if st.fresh {
			c, err = NewConverter(ids, converterResource)
			require.NoError(t, err)
		}
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			testConverterLifecycleStep(t, c, tt, st)
		})
	}
}

func converterLifecycleSnapshot(tt converterLifecycleCase, st converterLifecycleStep) registry.MetricsByCounterGroup {
	if st.missing {
		return nil
	}
	snapshot := converterSnapshot(tt.field, st.value)
	for _, metrics := range snapshot[dcgm.FE_GPU] {
		metrics[0].Timestamp = converterSampleTime + st.time
		if st.uuid != "" {
			metrics[0].GPUUUID = st.uuid
		}
		if st.gi {
			metrics[0].Source.EntityGroup = dcgm.FE_GPU_I
			metrics[0].GPUInstanceID = "0"
		}
		if st.pod != "" {
			metrics[0].Attributes = map[string]string{"pod": st.pod}
		}
	}
	addConverterLifecycleSamples(snapshot, tt, st)
	return snapshot
}

// addConverterLifecycleSamples adds failure inputs and companion fields for cumulative-state cases.
func addConverterLifecycleSamples(snapshot registry.MetricsByCounterGroup, tt converterLifecycleCase, st converterLifecycleStep) {
	if st.malformed {
		for counter, metrics := range converterSnapshot(dcgm.DCGM_FI_DEV_GPU_TEMP, "bad")[dcgm.FE_GPU] {
			snapshot[dcgm.FE_GPU][counter] = metrics
		}
	}
	if st.malformedExporter {
		m := exporterFixture("gpu.health.status")
		m.Value = "1"
		snapshot[dcgm.FE_GPU][m.Counter] = []collector.Metric{m}
	}
	if tt.id == "gpu.memory.remapped_rows.corrected" {
		for counter, metrics := range converterSnapshot(dcgm.DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS, "100")[dcgm.FE_GPU] {
			metrics[0].Timestamp = converterSampleTime + st.time
			snapshot[dcgm.FE_GPU][counter] = metrics
		}
	}
}

func testConverterLifecycleStep(t *testing.T, c *Converter, tt converterLifecycleCase, st converterLifecycleStep) {
	t.Helper()
	snapshot := converterLifecycleSnapshot(tt, st)
	ctx := context.Background()
	if st.canceled {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	}
	periods := maps.Clone(c.periods)
	result, err := c.Convert(ctx, snapshot)
	if st.want != "" {
		require.ErrorContains(t, err, st.want)
		require.Nil(t, result)
		assert.Equal(t, periods, c.periods)
		assert.Equal(t, converterLifecycleSnapshot(tt, st), snapshot)
		return
	}
	require.NoError(t, err)
	if st.missing {
		require.Empty(t, result.ScopeMetrics[0].Metrics)
		return
	}
	require.Len(t, result.ScopeMetrics[0].Metrics, 1)
	point := result.ScopeMetrics[0].Metrics[0].GetSum().DataPoints[0]
	if tt.id == "gpu.memory.remapped_rows.corrected" {
		require.Len(t, result.ScopeMetrics[0].Metrics[0].GetSum().DataPoints, 2)
		assert.Equal(t, uint64(converterSampleTime+1)*1000, result.ScopeMetrics[0].Metrics[0].GetSum().DataPoints[1].StartTimeUnixNano)
	}
	assert.Equal(t, (uint64(converterSampleTime)+st.start)*1000, point.StartTimeUnixNano)
	if tt.id == "gpu.memory.usage" {
		value, err := strconv.ParseInt(st.value, 10, 64)
		require.NoError(t, err)
		assert.Equal(t, value*1_048_576, point.GetAsInt())
	}
	if tt.id == "gpu.energy.total" {
		raw, err := strconv.ParseInt(st.value, 10, 64)
		require.NoError(t, err)
		assert.Equal(t, float64(raw)/1000, point.GetAsDouble(), "emit the observed total without accumulating or subtracting it")
		assert.Len(t, c.periods, 1, "optional workload churn must not create source periods")
	}
}

func addConverterEntityScopes(s registry.MetricsByCounterGroup) {
	for counter, metrics := range s[dcgm.FE_GPU] {
		for _, group := range []dcgm.Field_Entity_Group{dcgm.FE_GPU_I, dcgm.FE_GPU_CI, dcgm.FE_VGPU, dcgm.FE_CPU, dcgm.FE_SWITCH, dcgm.FE_LINK} {
			m := metrics[0]
			source := *m.Source
			source.EntityGroup = group
			m.Source = &source
			m.GPUInstanceID = "0"
			metrics = append(metrics, m)
		}
		s[dcgm.FE_GPU][counter] = metrics
	}
}

func addConverterContainers(s registry.MetricsByCounterGroup) {
	for counter, metrics := range s[dcgm.FE_GPU] {
		metrics[0].Source.Containers = []collector.ContainerIdentity{{Name: "first", ID: "a"}, {Name: "second", ID: "b"}}
		s[dcgm.FE_GPU][counter] = metrics
	}
}

func addConverterKubernetesProjections(s registry.MetricsByCounterGroup) {
	for counter, metrics := range s[dcgm.FE_GPU] {
		m := metrics[0]
		m.Attributes = map[string]string{"pod_name": "first", "pod_namespace": "ns"}
		m.Source.Containers = []collector.ContainerIdentity{{Name: "a", ID: "a"}, {Name: "b", ID: "b"}}
		other := m
		other.Value = "50"
		other.Attributes = map[string]string{"pod": "second", "namespace": "ns"}
		s[dcgm.FE_GPU][counter] = []collector.Metric{m, other}
	}
}

func setConverterLegacyValues(s registry.MetricsByCounterGroup) {
	for counter, metrics := range s[dcgm.FE_GPU] {
		if counter.FieldID == dcgm.DCGM_FI_DEV_POWER_USAGE || counter.FieldID == dcgm.DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL || counter.FieldID == dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS || counter.FieldID == dcgm.DCGM_FI_DEV_VGPU_INSTANCE_LICENSE_STATUS {
			metrics[0].Value = "999"
			s[dcgm.FE_GPU][counter] = metrics
		}
	}
}

func testConverterCompatibility(t *testing.T, tt converterCompatibilityCase, defaults []signalcatalog.SignalID) {
	t.Helper()
	ids := tt.ids
	if ids == nil {
		ids = defaults
	}
	c, err := NewConverter(ids, converterResource)
	require.NoError(t, err)
	snapshot := converterFields(tt.fields...)
	if tt.edit != nil {
		tt.edit(snapshot)
	}
	// Keep an independent full snapshot, including Source which JSON omits.
	saved := converterFields(tt.fields...)
	if tt.edit != nil {
		tt.edit(saved)
	}
	var before, after bytes.Buffer
	if tt.render {
		require.NoError(t, rendermetrics.Render(&before, snapshot))
	}
	result, err := c.Convert(context.Background(), snapshot)
	if tt.want != "" {
		require.ErrorContains(t, err, tt.want)
		require.Nil(t, result)
		return
	}
	require.NoError(t, err)
	require.Len(t, result.ScopeMetrics[0].Metrics, tt.families)
	points := converterPoints(result)
	require.Len(t, points, tt.points)
	assert.Equal(t, saved, snapshot)
	if tt.render {
		require.NoError(t, rendermetrics.Render(&after, snapshot))
		assert.Equal(t, before.String(), after.String())
	}
	if tt.name == "legacy and override coexistence" {
		assert.Equal(t, float64(100), points[0].GetAsDouble())
		assert.Equal(t, int64(100), points[1].GetAsInt())
	}
	if tt.name == "Kubernetes projections without extra fanout" {
		assert.Equal(t, []int64{104_857_600, 52_428_800}, []int64{points[0].GetAsInt(), points[1].GetAsInt()})
		assert.Equal(t, points[0].StartTimeUnixNano, points[1].StartTimeUnixNano)
		assert.Len(t, c.periods, 1)
	}
	if tt.name == "mixed native and exporter sources" {
		assert.Len(t, c.periods, 2, "only native energy on two GPUs retains periods")
		wantNames := []string{"hw.temperature", "hw.energy"}
		for _, row := range exporterMappingCases() {
			wantNames = append(wantNames, row.name)
		}
		var gotNames []string
		for _, family := range result.ScopeMetrics[0].Metrics {
			gotNames = append(gotNames, family.Name)
		}
		assert.ElementsMatch(t, wantNames, gotNames)
		assertMixedHealthPoints(t, result)
	}
	testConverterOwnership(t, c, tt, snapshot, saved, result, points)
}

func testConverterOwnership(t *testing.T, c *Converter, tt converterCompatibilityCase, snapshot, saved registry.MetricsByCounterGroup, result *metricspb.ResourceMetrics, points []*metricspb.NumberDataPoint) {
	t.Helper()
	expected := proto.Clone(result)
	mutateConverterResult(result)
	if tt.name == "standalone container fanout" {
		assert.Equal(t, int64(104_857_600), points[1].GetAsInt())
	}
	next, err := c.Convert(context.Background(), snapshot)
	require.NoError(t, err)
	assert.True(t, proto.Equal(expected, next), "returned protobuf mutation affected later conversion")
	assert.Equal(t, saved, snapshot)
}

func mutateConverterResult(result *metricspb.ResourceMetrics) {
	result.Resource.Attributes[0].Value.Value = &commonpb.AnyValue_StringValue{StringValue: "mutated"}
	metrics := result.ScopeMetrics[0].Metrics
	if len(metrics) == 0 {
		return
	}
	points := metrics[0].GetGauge().GetDataPoints()
	if sum := metrics[0].GetSum(); sum != nil {
		points = sum.DataPoints
	}
	if len(points) > 0 {
		mutateConverterPoint(points[0])
	}
	for _, metric := range metrics {
		if metric.Name != "hw.status" {
			continue
		}
		for _, point := range metric.GetSum().DataPoints {
			mutateConverterPoint(point)
		}
	}
}

// mutateConverterPoint changes a value and attribute to expose shared output storage.
func mutateConverterPoint(point *metricspb.NumberDataPoint) {
	point.Attributes[0].Value.Value = &commonpb.AnyValue_StringValue{StringValue: "mutated"}
	if value, ok := point.Value.(*metricspb.NumberDataPoint_AsInt); ok {
		value.AsInt = -99
	} else {
		point.Value.(*metricspb.NumberDataPoint_AsDouble).AsDouble = -99
	}
}

// converterMixedIDs selects two native signals and all supported exporter metric families.
func converterMixedIDs() []signalcatalog.SignalID {
	ids := []signalcatalog.SignalID{"gpu.temperature", "gpu.energy.total"}
	for _, row := range exporterMappingCases() {
		ids = append(ids, row.id)
	}
	return ids
}

// converterMixedSnapshot supplies mixed native and exporter input for validation tests.
func converterMixedSnapshot() registry.MetricsByCounterGroup {
	snapshot := converterFields(dcgm.DCGM_FI_DEV_GPU_TEMP, dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION)
	addConverterMixedSources(snapshot)
	return snapshot
}

// addConverterMixedSources adds exporter subseries, GPU/workload projections, and unselected metrics.
func addConverterMixedSources(snapshot registry.MetricsByCounterGroup) {
	for _, row := range exporterMappingCases() {
		m := exporterFixture(row.id)
		metrics := []collector.Metric{m}
		for _, label := range []string{"clock_event", "xid"} {
			if m.Labels[label] == "" {
				continue
			}
			next := exporterFixture(row.id)
			next.Labels[label], next.Value = "31", "29"
			if label == "clock_event" {
				next.Labels[label] = "clocks_setting"
			}
			metrics = append(metrics, next)
		}
		snapshot[dcgm.FE_GPU][m.Counter] = metrics
	}
	for counter, metrics := range snapshot[dcgm.FE_GPU] {
		var expanded []collector.Metric
		for _, m := range metrics {
			expanded = append(expanded, mixedHardwareMetrics(m)...)
		}
		snapshot[dcgm.FE_GPU][counter] = expanded
	}
	for _, name := range []string{"unapproved_custom_metric", "go_goroutines", "process_cpu_seconds_total", "http_requests_total"} {
		counter := counters.Counter{FieldName: name, PromType: "gauge", Help: "unselected"}
		snapshot[dcgm.FE_GPU][counter] = []collector.Metric{{Counter: counter, Value: "123", UUIDLabelName: "UUID"}}
	}
}

// mixedHardwareMetrics models two GPUs and their containers, reversing peer direction when present.
func mixedHardwareMetrics(m collector.Metric) []collector.Metric {
	m.Source.Containers = []collector.ContainerIdentity{{Name: "first", ID: "a"}, {Name: "second", ID: "b"}}
	other := m
	source := *m.Source
	other.Source = &source
	other.GPUUUID, other.GPU = "GPU-peer", "1"
	if m.Source.PeerGPUUUID != "" {
		other.Source.PeerGPUUUID = m.GPUUUID
	}
	return []collector.Metric{m, other}
}

func converterFields(fields ...dcgm.Short) registry.MetricsByCounterGroup {
	result := registry.MetricsByCounterGroup{dcgm.FE_GPU: collector.MetricsByCounter{}}
	for _, field := range fields {
		for counter, metrics := range converterSnapshot(field, "100")[dcgm.FE_GPU] {
			result[dcgm.FE_GPU][counter] = metrics
		}
	}
	return result
}

// converterSnapshot keeps numeric samples separate from preserved native identity.
func converterSnapshot(field dcgm.Short, value string) registry.MetricsByCounterGroup {
	metric := gpuFixture()
	metric.GPUUUID = "GPU-test"
	metric.UUIDLabelName = "UUID"
	metric.Source.GPUDriverVersion = "550.54"
	metric.Timestamp = converterSampleTime
	metric.Value = value
	metric.Counter = counters.Counter{FieldID: field, FieldName: "sample_" + strconv.Itoa(int(field)), PromType: "gauge", Help: "sample"}
	return registry.MetricsByCounterGroup{dcgm.FE_GPU: {metric.Counter: {metric}}}
}

func converterAttributes(attrs []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	result := make(map[string]*commonpb.AnyValue, len(attrs))
	for _, attr := range attrs {
		result[attr.Key] = attr.Value
	}
	return result
}

// assertMixedHealthPoints checks every GPU, workload, and state value in the mixed fixture.
func assertMixedHealthPoints(t *testing.T, result *metricspb.ResourceMetrics) {
	t.Helper()
	want := make(map[[3]string]int64)
	for _, gpu := range []string{"GPU-reported", "GPU-peer"} {
		for _, container := range []string{"a", "b"} {
			for _, state := range []struct {
				name  string
				value int64
			}{{"ok", 0}, {"degraded", 1}, {"failed", 0}} {
				want[[3]string{"gpu:" + gpu, container, state.name}] = state.value
			}
		}
	}

	got := make(map[[3]string]int64)
	for _, family := range result.ScopeMetrics[0].Metrics {
		if family.Name != "hw.status" {
			continue
		}
		for _, point := range family.GetSum().DataPoints {
			attrs := converterAttributes(point.Attributes)
			key := [3]string{attrs["hw.id"].GetStringValue(), attrs["container.id"].GetStringValue(), attrs["hw.state"].GetStringValue()}
			_, duplicate := got[key]
			assert.False(t, duplicate, "duplicate health point %v", key)
			got[key] = point.GetAsInt()
		}
	}

	assert.Equal(t, want, got)
}
