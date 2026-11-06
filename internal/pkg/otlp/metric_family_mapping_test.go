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
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	mockdeviceinfo "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/deviceinfo"
	mockdevicewatcher "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
)

const exporterStartTime = converterSampleTime - 1_000_000

type exporterMappingCase struct {
	id                        signalcatalog.SignalID
	family, value, name, unit string
	labels                    map[string]string
	attrs                     map[string]any
	values                    []int64
	states                    []string
	sum, monotonic            bool
}

// TestConverterExporterMappings checks each family's shape, value, timestamps, and typed attributes.
func TestConverterExporterMappings(t *testing.T) {
	for _, tt := range exporterMappingCases() {
		t.Run(string(tt.id), func(t *testing.T) { testExporterMapping(t, tt) })
	}
}

// exporterMappingCases supplies literal wire expectations for the supported exporter families.
func exporterMappingCases() []exporterMappingCase {
	return []exporterMappingCase{
		{
			id: "gpu.health.status", family: "DCGM_EXP_GPU_HEALTH_STATUS", value: "10", name: "hw.status", unit: "1", sum: true,
			labels: map[string]string{"health_watch": "PCIE", "health_error_code": "DCGM_FR_PCI_REPLAY_RATE", "health_error_severity": "MONITOR", "health_error_category": "HARDWARE_PCIE"},
			attrs:  map[string]any{"com.nvidia.dcgm.health.watch": "PCIE", "com.nvidia.dcgm.health.error.code": "DCGM_FR_PCI_REPLAY_RATE", "com.nvidia.dcgm.health.error.severity": "MONITOR", "com.nvidia.dcgm.health.error.category": "HARDWARE_PCIE"},
			values: []int64{0, 1, 0}, states: []string{"ok", "degraded", "failed"},
		},
		{
			id: "gpu.clock_event.observations", family: "DCGM_EXP_CLOCK_EVENTS_COUNT", value: "7", name: "com.nvidia.gpu.clock_event.observations", unit: "{observation}",
			labels: map[string]string{"clock_event": "gpu_idle", "window_size_in_ms": "2750"}, attrs: map[string]any{"com.nvidia.gpu.clock_event.type": "gpu_idle", "com.nvidia.dcgm.exporter.sample.window": int64(2750)}, values: []int64{7},
		},
		{
			id: "gpu.clock_event.transitions", family: "DCGM_EXP_CLOCK_EVENTS_TOTAL", value: "13", name: "com.nvidia.gpu.clock_event.transitions", unit: "{event}", sum: true, monotonic: true,
			labels: map[string]string{"clock_event": "sw_power_cap"}, attrs: map[string]any{"com.nvidia.gpu.clock_event.type": "sw_power_cap"}, values: []int64{13},
		},
		{
			id: "gpu.p2p.status", family: "DCGM_EXP_P2P_STATUS", value: "255", name: "com.nvidia.gpu.p2p.status", unit: "1",
			labels: map[string]string{"link_status": "Unknown", "peer_gpu": "1"}, attrs: map[string]any{"com.nvidia.gpu.p2p.link.status": "Unknown", "com.nvidia.gpu.peer.uuid": "GPU-peer"}, values: []int64{255},
		},
		{
			id: "gpu.xid.observations", family: "DCGM_EXP_XID_ERRORS_COUNT", value: "4", name: "com.nvidia.gpu.xid.observations", unit: "{observation}",
			labels: map[string]string{"xid": "42", "window_size_in_ms": "1400"}, attrs: map[string]any{"com.nvidia.gpu.xid.code": int64(42), "com.nvidia.dcgm.exporter.sample.window": int64(1400)}, values: []int64{4},
		},
		{
			id: "gpu.xid.records", family: "DCGM_EXP_XID_ERRORS_TOTAL", value: "19", name: "com.nvidia.gpu.xid.records", unit: "{record}", sum: true, monotonic: true,
			labels: map[string]string{"xid": "46"}, attrs: map[string]any{"com.nvidia.gpu.xid.code": int64(46)}, values: []int64{19},
		},
	}
}

// testExporterMapping verifies one family against its expected OTLP representation.
func testExporterMapping(t *testing.T, tt exporterMappingCase) {
	t.Helper()
	m := exporterMetric(tt.family, tt.value, tt.labels)
	if tt.id == "gpu.p2p.status" {
		m.Source.PeerGPUUUID = "GPU-peer"
	}
	m.Attributes = map[string]string{"pod": "workload", "namespace": "default"}
	c, err := NewConverter([]signalcatalog.SignalID{tt.id}, converterResource)
	require.NoError(t, err)
	result, err := c.Convert(context.Background(), exporterSnapshot(m))
	require.NoError(t, err)
	require.Len(t, result.ScopeMetrics[0].Metrics, 1)
	family := result.ScopeMetrics[0].Metrics[0]
	assert.Equal(t, tt.name, family.Name)
	assert.Equal(t, tt.unit, family.Unit)
	var start uint64
	if tt.sum {
		require.NotNil(t, family.GetSum())
		assert.Equal(t, tt.monotonic, family.GetSum().IsMonotonic)
		assert.Equal(t, metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, family.GetSum().AggregationTemporality)
		start = uint64(exporterStartTime) * 1000
	} else {
		require.NotNil(t, family.GetGauge())
	}
	points := converterPoints(result)
	require.Len(t, points, len(tt.values))
	for i, point := range points {
		assert.IsType(t, &metricspb.NumberDataPoint_AsInt{}, point.Value)
		assert.Equal(t, tt.values[i], point.GetAsInt())
		assert.Equal(t, uint64(converterSampleTime)*1000, point.TimeUnixNano)
		assert.Equal(t, start, point.StartTimeUnixNano)
		attrs := converterAttributes(point.Attributes)
		assert.Equal(t, "gpu:GPU-reported", attrs["hw.id"].GetStringValue())
		assert.Equal(t, "gpu", attrs["hw.type"].GetStringValue())
		assert.Equal(t, "workload", attrs["k8s.pod.name"].GetStringValue())
		for key, value := range tt.attrs {
			assertExporterAttribute(t, attrs[key], value)
		}
		if len(tt.states) != 0 {
			assert.Equal(t, tt.states[i], attrs["hw.state"].GetStringValue())
		}
		assert.NotContains(t, attrs, "peer_gpu")
	}
	assert.Empty(t, c.periods, "exporter periods belong to collectors")
}

// assertExporterAttribute checks the value and its OTLP string or integer type.
func assertExporterAttribute(t *testing.T, actual *commonpb.AnyValue, expected any) {
	t.Helper()
	require.NotNil(t, actual)
	switch value := expected.(type) {
	case string:
		assert.IsType(t, &commonpb.AnyValue_StringValue{}, actual.Value)
		assert.Equal(t, value, actual.GetStringValue())
	case int64:
		assert.IsType(t, &commonpb.AnyValue_IntValue{}, actual.Value)
		assert.Equal(t, value, actual.GetIntValue())
	default:
		t.Fatalf("unsupported expected attribute type %T", expected)
	}
}

// exporterMetric supplies a collector start and an intentional native-field-ID collision.
func exporterMetric(family, value string, labels map[string]string) collector.Metric {
	m := gpuFixture()
	m.UUIDLabelName = "UUID"
	// Deliberately share a native field ID: exporter lookup must use the family name.
	m.Counter = counters.Counter{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: family, PromType: "gauge", Help: "exporter fixture"}
	if family == "DCGM_EXP_CLOCK_EVENTS_TOTAL" || family == "DCGM_EXP_XID_ERRORS_TOTAL" {
		m.Counter.PromType = "counter"
	}
	m.Timestamp, m.StartTimestamp = converterSampleTime, exporterStartTime
	m.Value, m.Labels = value, labels
	return m
}

// exporterSnapshot groups supplied samples by counter while preserving their identities.
func exporterSnapshot(metrics ...collector.Metric) registry.MetricsByCounterGroup {
	snapshot := registry.MetricsByCounterGroup{dcgm.FE_GPU: collector.MetricsByCounter{}}
	for _, m := range metrics {
		snapshot[dcgm.FE_GPU][m.Counter] = append(snapshot[dcgm.FE_GPU][m.Counter], m)
	}
	return snapshot
}

// exporterFixture creates a fresh mapping sample and adds the required peer for P2P.
func exporterFixture(id signalcatalog.SignalID) collector.Metric {
	for _, row := range exporterMappingCases() {
		if row.id == id {
			m := exporterMetric(row.family, row.value, row.labels)
			if id == "gpu.p2p.status" {
				m.Source.PeerGPUUUID = "GPU-peer"
			}
			return m
		}
	}
	panic("unknown exporter test signal")
}

// TestConverterExporterBoundaries checks invalid metadata, required identities, and zero window rows.
func TestConverterExporterBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name   string
		id     signalcatalog.SignalID
		edit   func(*collector.Metric)
		want   string
		points int
	}{
		{name: "negative observation count", id: "gpu.clock_event.observations", edit: func(m *collector.Metric) { m.Value = "-1" }, want: "nonnegative"},
		{name: "clock zero placeholder", id: "gpu.clock_event.observations", edit: func(m *collector.Metric) { m.Value = "0"; delete(m.Labels, "clock_event") }},
		{name: "XID zero placeholder", id: "gpu.xid.observations", edit: func(m *collector.Metric) { m.Value = "0"; delete(m.Labels, "xid") }},
		{name: "classified clock zero", id: "gpu.clock_event.observations", edit: func(m *collector.Metric) { m.Value = "0" }, points: 1},
		{name: "classified XID zero and zero window", id: "gpu.xid.observations", edit: func(m *collector.Metric) { m.Value = "0"; m.Labels["xid"] = "0"; m.Labels["window_size_in_ms"] = "0" }, points: 1},
		{name: "nonzero clock missing discriminator", id: "gpu.clock_event.observations", edit: func(m *collector.Metric) { delete(m.Labels, "clock_event") }, want: "required identity"},
		{name: "nonzero XID missing discriminator", id: "gpu.xid.observations", edit: func(m *collector.Metric) { delete(m.Labels, "xid") }, want: "required identity"},
		{name: "clock total missing discriminator", id: "gpu.clock_event.transitions", edit: func(m *collector.Metric) { delete(m.Labels, "clock_event") }, want: "required identity"},
		{name: "XID total missing discriminator", id: "gpu.xid.records", edit: func(m *collector.Metric) { delete(m.Labels, "xid") }, want: "required identity"},
		{name: "missing window", id: "gpu.clock_event.observations", edit: func(m *collector.Metric) { delete(m.Labels, "window_size_in_ms") }, want: "sample.window"},
		{name: "negative window", id: "gpu.clock_event.observations", edit: func(m *collector.Metric) { m.Labels["window_size_in_ms"] = "-1" }, want: "nonnegative"},
		{name: "overflowing window", id: "gpu.clock_event.observations", edit: func(m *collector.Metric) { m.Labels["window_size_in_ms"] = "9223372036854775808" }, want: "sample.window"},
		{name: "malformed XID", id: "gpu.xid.records", edit: func(m *collector.Metric) { m.Labels["xid"] = "4.2" }, want: "xid.code"},
		{name: "negative XID", id: "gpu.xid.records", edit: func(m *collector.Metric) { m.Labels["xid"] = "-1" }, want: "nonnegative"},
		{name: "overflowing XID", id: "gpu.xid.records", edit: func(m *collector.Metric) { m.Labels["xid"] = "9223372036854775808" }, want: "xid.code"},
		{name: "missing collector start", id: "gpu.clock_event.transitions", edit: func(m *collector.Metric) { m.StartTimestamp = 0 }, want: "collector start"},
		{name: "negative collector start", id: "gpu.xid.records", edit: func(m *collector.Metric) { m.StartTimestamp = -1 }, want: "collector start"},

		{name: "overflowing collector start", id: "gpu.xid.records", edit: func(m *collector.Metric) { m.StartTimestamp = int64(uint64(math.MaxUint64)/1000) + 1 }, want: "nanosecond range"},
		{name: "unsupported health value", id: "gpu.health.status", edit: func(m *collector.Metric) { m.Value = "1" }, want: "health value"},
		{name: "missing health identity", id: "gpu.health.status", edit: func(m *collector.Metric) { delete(m.Labels, "health_watch") }, want: "health.watch"},
		{name: "P2P index cannot replace peer UUID", id: "gpu.p2p.status", edit: func(m *collector.Metric) { m.Source.PeerGPUUUID = "" }},
		{name: "P2P raw negative status preserved", id: "gpu.p2p.status", edit: func(m *collector.Metric) { m.Value = "-1" }, points: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := exporterFixture(tt.id)
			tt.edit(&m)
			c, err := NewConverter([]signalcatalog.SignalID{tt.id}, converterResource)
			require.NoError(t, err)
			result, err := c.Convert(context.Background(), exporterSnapshot(m))
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			points := converterPoints(result)
			require.Len(t, points, tt.points)
			if len(points) > 0 {
				assert.Equal(t, m.Value, strconv.FormatInt(points[0].GetAsInt(), 10))
			}
		})
	}
}

// TestConverterExporterMIGEligibility checks which families emit points for MIG GPU instances.
func TestConverterExporterMIGEligibility(t *testing.T) {
	for _, tt := range []struct {
		id     signalcatalog.SignalID
		points int
	}{
		{"gpu.health.status", 0},
		{"gpu.p2p.status", 0},
		{"gpu.clock_event.observations", 1},
		{"gpu.clock_event.transitions", 1},
		{"gpu.xid.observations", 1},
		{"gpu.xid.records", 1},
	} {
		t.Run(string(tt.id), func(t *testing.T) {
			m := exporterFixture(tt.id)
			m.Source.EntityGroup = dcgm.FE_GPU_I
			m.GPUInstanceID = "0"
			c, err := NewConverter([]signalcatalog.SignalID{tt.id}, converterResource)
			require.NoError(t, err)
			result, err := c.Convert(context.Background(), exporterSnapshot(m))
			require.NoError(t, err)
			points := converterPoints(result)
			require.Len(t, points, tt.points)
			if len(points) > 0 {
				assert.Equal(t, "mig-gi:GPU-reported:0", converterAttributes(points[0].Attributes)["hw.id"].GetStringValue())
			}
		})
	}
}

// TestConverterExporterPeriods checks collector starts across new series, workload changes, and restarts.
func TestConverterExporterPeriods(t *testing.T) {
	for _, tt := range []struct {
		id                   signalcatalog.SignalID
		label, first, second string
	}{
		{"gpu.clock_event.transitions", "clock_event", "gpu_idle", "sw_power_cap"},
		{"gpu.xid.records", "xid", "42", "46"},
	} {
		t.Run(string(tt.id), func(t *testing.T) {
			c, err := NewConverter([]signalcatalog.SignalID{tt.id}, converterResource)
			require.NoError(t, err)
			for _, step := range []struct {
				name, pod      string
				start, elapsed int64
				series         int
			}{
				{"late first conversion", "first", exporterStartTime, 0, 1},
				{"new series in later snapshot", "first", exporterStartTime, 1, 2},
				{"workload change", "next", exporterStartTime, 2, 2},
				{"collector restart", "next", converterSampleTime + 3, 3, 2},
			} {
				t.Run(step.name, func(t *testing.T) {
					first, second := exporterFixture(tt.id), exporterFixture(tt.id)
					first.Labels[tt.label], second.Labels[tt.label] = tt.first, tt.second
					first.Value, second.Value = "7", "19"
					first.StartTimestamp, second.StartTimestamp = step.start, step.start
					first.Timestamp, second.Timestamp = converterSampleTime+step.elapsed, converterSampleTime+step.elapsed
					first.Attributes, second.Attributes = map[string]string{"pod": step.pod}, map[string]string{"pod": step.pod}
					samples := []collector.Metric{first, second}
					result, err := c.Convert(context.Background(), exporterSnapshot(samples[:step.series]...))
					require.NoError(t, err)
					points := converterPoints(result)
					require.Len(t, points, step.series)
					values := []int64{7, 19}
					for i, p := range points {
						assert.Equal(t, values[i], p.GetAsInt())
						assert.EqualValues(t, step.start*1000, p.StartTimeUnixNano)
						assert.EqualValues(t, (converterSampleTime+step.elapsed)*1000, p.TimeUnixNano)
						assert.Equal(t, step.pod, converterAttributes(p.Attributes)["k8s.pod.name"].GetStringValue())
					}
					assert.Empty(t, c.periods)
				})
			}
		})
	}
}

// TestConverterHealthRecovery checks state recovery and independently owned health points.
func TestConverterHealthRecovery(t *testing.T) {
	c, err := NewConverter([]signalcatalog.SignalID{"gpu.health.status"}, converterResource)
	require.NoError(t, err)
	for i, tt := range []struct{ value, active, code string }{
		{"20", "failed", "DCGM_FR_PCI_REPLAY_RATE"},
		{"0", "ok", "DCGM_FR_OK"},
	} {
		t.Run(tt.active, func(t *testing.T) {
			m := exporterFixture("gpu.health.status")
			m.Value, m.Labels["health_error_code"] = tt.value, tt.code
			m.Timestamp += int64(i)
			result, err := c.Convert(context.Background(), exporterSnapshot(m))
			require.NoError(t, err)
			points := converterPoints(result)
			require.Len(t, points, 3)
			for _, point := range points {
				attrs := converterAttributes(point.Attributes)
				var value int64
				if attrs["hw.state"].GetStringValue() == tt.active {
					value = 1
				}
				assert.Equal(t, value, point.GetAsInt())
				assert.Equal(t, tt.code, attrs["com.nvidia.dcgm.health.error.code"].GetStringValue())
				assert.Equal(t, uint64(exporterStartTime)*1000, point.StartTimeUnixNano)
			}
			points[0].Value.(*metricspb.NumberDataPoint_AsInt).AsInt = -1
			converterAttributes(points[0].Attributes)["hw.state"].Value = &commonpb.AnyValue_StringValue{StringValue: "mutated"}
			assert.NotEqual(t, int64(-1), points[1].GetAsInt())
			assert.Equal(t, "degraded", converterAttributes(points[1].Attributes)["hw.state"].GetStringValue())
			assert.Empty(t, c.periods)
		})
	}
}

// TestConverterDirectedP2P keeps the source and peer identities distinct in each direction.
func TestConverterDirectedP2P(t *testing.T) {
	first, second := exporterFixture("gpu.p2p.status"), exporterFixture("gpu.p2p.status")
	second.GPUUUID, second.Source.PeerGPUUUID = "GPU-peer", "GPU-reported"
	c, err := NewConverter([]signalcatalog.SignalID{"gpu.p2p.status"}, converterResource)
	require.NoError(t, err)
	result, err := c.Convert(context.Background(), exporterSnapshot(first, second))
	require.NoError(t, err)
	points := converterPoints(result)
	require.Len(t, points, 2)
	for i, tt := range []struct{ gpu, peer string }{{"GPU-reported", "GPU-peer"}, {"GPU-peer", "GPU-reported"}} {
		attrs := converterAttributes(points[i].Attributes)
		assert.Equal(t, tt.gpu, attrs["com.nvidia.gpu.uuid"].GetStringValue())
		assert.Equal(t, tt.peer, attrs["com.nvidia.gpu.peer.uuid"].GetStringValue())
	}
}

// TestConverterIncompleteIdentityKeepsOtherSignals checks that one unidentified row cannot hide valid samples.
func TestConverterIncompleteIdentityKeepsOtherSignals(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*collector.Metric)
	}{
		{"missing source", func(m *collector.Metric) { m.Source = nil }},
		{"missing GPU UUID", func(m *collector.Metric) { m.GPUUUID = "" }},
		{"missing GPU index", func(m *collector.Metric) { m.GPU = "" }},
		{"synthetic GPU UUID", func(m *collector.Metric) { m.Source.SyntheticGPUUUID = true }},
		{"missing peer UUID", func(m *collector.Metric) { m.Source.PeerGPUUUID = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			bad := exporterFixture("gpu.p2p.status")
			tt.edit(&bad)
			good := exporterFixture("gpu.xid.records")
			healthyPeer := exporterFixture("gpu.p2p.status")
			healthyPeer.Source.PeerGPUUUID = "GPU-healthy-peer"
			snapshot := exporterSnapshot(healthyPeer, bad, good)
			c, err := NewConverter([]signalcatalog.SignalID{"gpu.p2p.status", "gpu.xid.records"}, converterResource)
			require.NoError(t, err)

			result, err := c.Convert(context.Background(), snapshot)
			require.NoError(t, err)
			require.Len(t, result.ScopeMetrics[0].Metrics, 2)
			assert.Equal(t, "com.nvidia.gpu.p2p.status", result.ScopeMetrics[0].Metrics[0].Name)
			assert.Equal(t, "com.nvidia.gpu.xid.records", result.ScopeMetrics[0].Metrics[1].Name)
			points := converterPoints(result)
			require.Len(t, points, 2)
			assert.Equal(t, "GPU-healthy-peer", converterAttributes(points[0].Attributes)["com.nvidia.gpu.peer.uuid"].GetStringValue())
			assert.Equal(t, good.Value, strconv.FormatInt(points[1].GetAsInt(), 10))

			var warning map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &warning))
			assert.Equal(t, "WARN", warning["level"])
			assert.Equal(t, "gpu.p2p.status", warning["signal"])
			assert.Equal(t, float64(1), warning["samples"])
		})
	}
}

// TestConverterClockRollbackKeepsSnapshot checks exporter start clamping alongside a native sample.
func TestConverterClockRollbackKeepsSnapshot(t *testing.T) {
	for _, id := range []signalcatalog.SignalID{"gpu.health.status", "gpu.clock_event.transitions", "gpu.xid.records"} {
		t.Run(string(id), func(t *testing.T) {
			m := exporterFixture(id)
			m.StartTimestamp = m.Timestamp + 1
			snapshot := converterSnapshot(dcgm.DCGM_FI_DEV_GPU_TEMP, "42")
			snapshot[dcgm.FE_GPU][m.Counter] = []collector.Metric{m}
			c, err := NewConverter([]signalcatalog.SignalID{"gpu.temperature", id}, converterResource)
			require.NoError(t, err)

			result, err := c.Convert(context.Background(), snapshot)
			require.NoError(t, err)
			require.Len(t, result.ScopeMetrics[0].Metrics, 2)
			for _, p := range result.ScopeMetrics[0].Metrics[1].GetSum().DataPoints {
				assert.Equal(t, p.TimeUnixNano, p.StartTimeUnixNano)
			}
		})
	}
}

// TestExporterRulesMatchCatalog checks support and window behavior against every exporter metric in the catalog.
func TestExporterRulesMatchCatalog(t *testing.T) {
	catalogFamilies := make(map[string]bool)
	for _, signal := range signalcatalog.Signals() {
		if signal.Source.Kind != signalcatalog.SourceExporter || signal.Output.OutputKind != signalcatalog.OutputMetric {
			continue
		}
		t.Run(string(signal.ID), func(t *testing.T) {
			if signal.Entity == signalcatalog.EntityExporter {
				assert.False(t, supportsExporterMetric(signal.Source.Name))
				return
			}
			catalogFamilies[signal.Source.Name] = true
			rule, ok := exporterRules[signal.Source.Name]
			require.True(t, ok, "exporter metric has no family rule")
			assert.Equal(t, signal.Output.Temporality == signalcatalog.TemporalityWindow, rule.windowed)

			m := exporterFixture(signal.ID)
			raw, err := strconv.ParseInt(m.Value, 10, 64)
			require.NoError(t, err)
			_, err = exporterAttributes(signal.Source.Name, m, raw)
			require.NoError(t, err)
			delete(m.Labels, rule.label)
			assert.Equal(t, rule.windowed, isWindowPlaceholder(signal.Source.Name, m, 0))
		})
	}

	assert.Len(t, catalogFamilies, len(exporterRules), "family rules and catalog diverged")
}

// TestConverterCollectedWindowMetrics verifies the label contract using production collector output.
func TestConverterCollectedWindowMetrics(t *testing.T) {
	for _, tt := range []struct {
		name         string
		id           signalcatalog.SignalID
		field        dcgm.Short
		family       string
		privateID    dcgm.Short
		raw          uint64
		attribute    string
		want         any
		newCollector func(counters.CounterList, string, *appconfig.Config, devicewatchlistmanager.WatchList) (collector.Collector, error)
	}{
		{"clock", "gpu.clock_event.observations", dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, counters.DCGMExpClockEventsCount, dcgm.Short(counters.DCGMClockEventsCount), 4, "com.nvidia.gpu.clock_event.type", "power_cap", collector.NewClockEventsCollector},
		{"XID", "gpu.xid.observations", dcgm.DCGM_FI_DEV_XID_ERRORS, counters.DCGMExpXIDErrorsCount, dcgm.Short(counters.DCGMXIDErrorsCount), 42, "com.nvidia.gpu.xid.code", int64(42), collector.NewXIDCollector},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := mockdcgm.NewMockDCGM(ctrl)
			oldClient := dcgmprovider.Client()
			dcgmprovider.SetClient(client)
			t.Cleanup(func() { dcgmprovider.SetClient(oldClient) })
			devices := mockdeviceinfo.NewMockProvider(ctrl)
			gpu := deviceinfo.GPUInfo{DeviceInfo: dcgm.Device{GPU: 0, UUID: "GPU-collected"}}
			devices.EXPECT().InfoType().Return(dcgm.FE_GPU).AnyTimes()
			devices.EXPECT().GOpts().Return(appconfig.DeviceOptions{Flex: true}).AnyTimes()
			devices.EXPECT().GPUCount().Return(uint(1)).AnyTimes()
			devices.EXPECT().GPU(uint(0)).Return(gpu).AnyTimes()
			devices.EXPECT().GPUs().Return([]deviceinfo.GPUInfo{gpu}).AnyTimes()
			watcher := mockdevicewatcher.NewMockWatcher(ctrl)
			group, fields := dcgm.GroupHandle{}, dcgm.FieldHandle{}
			group.SetHandle(1)
			fields.SetHandle(1)
			watcher.EXPECT().WatchDeviceFieldGroups(gomock.Any(), gomock.Any()).Return([]dcgm.GroupHandle{group}, []dcgm.FieldHandle{fields}, nil, nil)
			watchList := devicewatchlistmanager.NewWatchList(devices, nil, nil, watcher, 1)
			counter := counters.Counter{FieldID: tt.privateID, FieldName: tt.family, PromType: "gauge", Help: "window observations"}
			config := &appconfig.Config{EnableOTLP: true, ClockEventsCountWindowSize: 2750, XIDCountWindowSize: 2750}
			producer, err := tt.newCollector(counters.CounterList{counter}, "host", config, *watchList)
			require.NoError(t, err)
			defer producer.Cleanup()
			value := dcgm.FieldValue_v2{EntityGroupId: dcgm.FE_GPU, EntityID: 0, FieldID: tt.field, FieldType: dcgm.DCGM_FT_INT64}
			binary.NativeEndian.PutUint64(value.Value[:], tt.raw)
			client.EXPECT().UpdateAllFields().Return(nil)
			client.EXPECT().GetValuesSince(group, fields, gomock.Any()).Return([]dcgm.FieldValue_v2{value}, time.Time{}, nil)

			samples, err := producer.GetMetrics()
			require.NoError(t, err)
			require.Len(t, samples[counter], 1)

			c, err := NewConverter([]signalcatalog.SignalID{tt.id}, converterResource)
			require.NoError(t, err)

			result, err := c.Convert(context.Background(), registry.MetricsByCounterGroup{dcgm.FE_GPU: samples})
			require.NoError(t, err)

			points := converterPoints(result)
			require.Len(t, points, 1)
			assert.EqualValues(t, 1, points[0].GetAsInt())
			attrs := converterAttributes(points[0].Attributes)
			assert.EqualValues(t, 2750, attrs["com.nvidia.dcgm.exporter.sample.window"].GetIntValue())
			if number, ok := tt.want.(int64); ok {
				assert.Equal(t, number, attrs[tt.attribute].GetIntValue())
			} else {
				assert.Equal(t, tt.want, attrs[tt.attribute].GetStringValue())
			}
			assert.Equal(t, "gpu:GPU-collected", attrs["hw.id"].GetStringValue())
		})
	}
}
