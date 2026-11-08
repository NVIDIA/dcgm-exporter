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

package signalcatalog

import (
	"slices"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWatchSourcesFor checks effective bindings, hardware scopes, metadata dependencies, and omitted signals.
func TestWatchSourcesFor(t *testing.T) {
	metadata := []WatchSource{
		{Source: Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_PCIE_MAX_LINK_GEN", FieldID: 235}, Entity: EntityGPU, Metadata: true},
		{Source: Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH", FieldID: 236}, Entity: EntityGPU, Metadata: true},
		{Source: Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_PCIE_LINK_GEN", FieldID: 237}, Entity: EntityGPU, Metadata: true},
		{Source: Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_PCIE_LINK_WIDTH", FieldID: 238}, Entity: EntityGPU, Metadata: true},
	}
	tests := []struct {
		name    string
		id      SignalID
		profile ProfileID
		primary WatchSource
		wantErr string
	}{
		{
			name: "temperature and physical metadata", id: "gpu.temperature", profile: ProfileNVIDIA,
			primary: WatchSource{Source: Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_GPU_TEMP", FieldID: 150}, Entity: EntityGPU},
		},
		{
			name: "utilization retains instance scope", id: "gpu.utilization", profile: ProfileNVIDIA,
			primary: WatchSource{Source: Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_GPU_UTIL", FieldID: 203}, Entity: EntityGPUOrMIGGI},
		},
		{
			name: "raw power source selection", id: "gpu.power.usage", profile: ProfileNVIDIA,
			primary: WatchSource{Source: Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_BOARD_POWER_RAW_WATTS", FieldID: 157}, Entity: EntityGPU},
		},
		{
			name: "exporter dependency stays exporter owned", id: "gpu.xid.records", profile: ProfileNVIDIA,
			primary: WatchSource{Source: Source{Kind: SourceExporter, Name: "DCGM_EXP_XID_ERRORS_TOTAL"}, Entity: EntityGPUOrMIGGI},
		},
		{name: "driver uses discovered identity", id: "gpu.driver.version", profile: ProfileNVIDIA},
		{name: "omitted binding", id: "gpu.vgpu.license.status", profile: ProfileNVIDIA},
		{name: "process signal has no native fields", id: "exporter.otlp.metric.send.failures", profile: ProfileNVIDIA},
		{name: "unknown signal", id: "gpu.unknown", profile: ProfileNVIDIA, wantErr: "unknown signal"},
		{name: "unknown profile", id: "gpu.temperature", profile: "unknown", wantErr: "unknown output profile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WatchSourcesFor(tt.id, tt.profile)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.primary.Source.Kind == "" {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 5)
			assert.Equal(t, tt.primary, got[0])
			assert.Equal(t, metadata, got[1:])
		})
	}
}

func TestValidateDefinition(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*definition)
		wantErr string
	}{
		{name: "valid"},
		{
			name: "duplicate signal ID",
			mutate: func(def *definition) {
				def.Signals[1].ID = def.Signals[0].ID
			},
			wantErr: `duplicate signal ID "gpu.temperature"`,
		},
		{
			name: "binding has neither output nor omission",
			mutate: func(def *definition) {
				def.Signals[0].Output.Name = ""
			},
			wantErr: "must define exactly one of output name or omission",
		},
		{
			name: "binding has output and omission",
			mutate: func(def *definition) {
				def.Signals[0].Output.OmitReason = "not supported"
			},
			wantErr: "must define exactly one of output name or omission",
		},
		{
			name: "unknown entity scope",
			mutate: func(def *definition) {
				def.Signals[0].Entity = "unknown"
			},
			wantErr: `unknown entity scope "unknown"`,
		},
		{
			name: "unknown value type",
			mutate: func(def *definition) {
				def.Signals[0].ValueType = "unknown"
			},
			wantErr: `unknown value type "unknown"`,
		},
		{
			name: "unknown timestamp rule",
			mutate: func(def *definition) {
				def.Signals[0].Timestamp = "unknown"
			},
			wantErr: `unknown timestamp rule "unknown"`,
		},
		{
			name: "empty required identity",
			mutate: func(def *definition) {
				def.Signals[0].RequiredIdentity = nil
			},
			wantErr: "required identity is empty",
		},
		{
			name: "unknown conversion",
			mutate: func(def *definition) {
				def.Signals[0].Output.Conversion = "unknown"
			},
			wantErr: `unknown conversion "unknown"`,
		},
		{
			name: "unknown reset rule",
			mutate: func(def *definition) {
				def.Signals[0].Output.Instrument = InstrumentSum
				def.Signals[0].Output.Temporality = TemporalityCumulative
				def.Signals[0].Output.Monotonic = true
				def.Signals[0].Output.Reset = "unknown"
			},
			wantErr: `unknown reset rule "unknown"`,
		},
		{
			name: "same output has incompatible instrument",
			mutate: func(def *definition) {
				def.Signals[1].Output.Instrument = InstrumentSum
				def.Signals[1].Output.Temporality = TemporalityCumulative
				def.Signals[1].Output.Monotonic = true
				def.Signals[1].Output.Reset = ResetSource
			},
			wantErr: "conflicting instrument",
		},
		{
			name: "same output has duplicate static identity",
			mutate: func(def *definition) {
				def.Signals[1].Output.Attributes = slices.Clone(def.Signals[0].Output.Attributes)
			},
			wantErr: "duplicate output identity",
		},
		{
			name: "duplicate named set",
			mutate: func(def *definition) {
				def.Sets = append(def.Sets, namedSet{ID: SetID("@default")})
			},
			wantErr: `duplicate named set "@default"`,
		},
		{
			name: "unknown named-set member",
			mutate: func(def *definition) {
				def.Sets[0].Signals = append(def.Sets[0].Signals, SignalID("gpu.unknown"))
			},
			wantErr: `unknown signal "gpu.unknown"`,
		},
		{
			name: "duplicate named-set member",
			mutate: func(def *definition) {
				def.Sets[0].Signals = append(def.Sets[0].Signals, SignalID("gpu.temperature"))
			},
			wantErr: `duplicate member "gpu.temperature"`,
		},
		{
			name: "named set contains omitted NVIDIA signal",
			mutate: func(def *definition) {
				def.Sets[0].Signals = append(def.Sets[0].Signals, SignalID("gpu.vgpu.license.status"))
			},
			wantErr: "is omitted from nvidia/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := validTestDefinition()
			if tt.mutate != nil {
				tt.mutate(&def)
			}

			err := validate(def)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// validTestDefinition returns a fresh catalog for validation tests.
func validTestDefinition() definition {
	nvidiaTemperature := func(location string) Binding {
		return Binding{
			Name:        "hw.temperature",
			OutputKind:  OutputMetric,
			Instrument:  InstrumentGauge,
			Unit:        "Cel",
			Temporality: TemporalityInstant,
			Reset:       ResetNone,
			Conversion:  ConversionIdentity,
			Attributes:  []Attribute{{Name: "hw.sensor_location", Value: location}},
		}
	}

	return definition{
		Version: 1,
		Signals: []Signal{
			{
				ID:               SignalID("gpu.temperature"),
				Source:           Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_GPU_TEMP", FieldID: uint16(dcgm.DCGM_FI_DEV_GPU_TEMP)},
				Entity:           EntityGPU,
				ValueType:        ValueInt64,
				Timestamp:        TimestampDCGMOrReadTime,
				RequiredIdentity: []string{"hw.id"},
				Output:           nvidiaTemperature("gpu"),
			},
			{
				ID:               SignalID("gpu.memory.temperature"),
				Source:           Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_MEMORY_TEMP", FieldID: uint16(dcgm.DCGM_FI_DEV_MEMORY_TEMP)},
				Entity:           EntityGPU,
				ValueType:        ValueInt64,
				Timestamp:        TimestampDCGMOrReadTime,
				RequiredIdentity: []string{"hw.id"},
				Output:           nvidiaTemperature("memory"),
			},
			{
				ID:               SignalID("gpu.vgpu.license.status"),
				Source:           Source{Kind: SourceDCGM, Name: "DCGM_FI_DEV_VGPU_LICENSE_STATUS", FieldID: uint16(dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS)},
				Entity:           EntityVGPU,
				ValueType:        ValueInt64,
				Timestamp:        TimestampDCGMOrReadTime,
				RequiredIdentity: []string{"hw.id"},
				Output:           Binding{OmitReason: "vGPU entity collection is outside nvidia/v1"},
			},
		},
		Sets: []namedSet{{ID: SetID("@default"), Signals: []SignalID{SignalID("gpu.temperature")}}},
	}
}

type contractCase struct {
	id               SignalID
	sourceKind       SourceKind
	sourceName       string
	sourceID         dcgm.Short
	entity           EntityScope
	valueType        ValueType
	timestamp        TimestampRule
	nvidiaSourceName string
	nvidiaSourceID   dcgm.Short
	nvidiaName       string
	nvidiaOmission   string
	outputKind       OutputKind
	instrument       InstrumentKind
	unit             string
	temporality      Temporality
	monotonic        bool
	reset            ResetRule
	conversion       Conversion
	attributes       []Attribute
}

func TestCatalogContract(t *testing.T) {
	tests := []contractCase{
		{
			id: "gpu.driver.version", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DRIVER_VERSION",
			sourceID: dcgm.DCGM_FI_DRIVER_VERSION, entity: EntityGPUOrMIGGI, valueType: ValueString,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.driver_version", outputKind: OutputAttribute,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.clock.sm", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_SM_CLOCK",
			sourceID: dcgm.DCGM_FI_DEV_SM_CLOCK, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.sm.clock", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "Hz", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionMegahertzToHertz,
		},
		{
			id: "gpu.clock.memory", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_MEM_CLOCK",
			sourceID: dcgm.DCGM_FI_DEV_MEM_CLOCK, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory.clock", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "Hz", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionMegahertzToHertz,
		},
		{
			id: "gpu.temperature", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_GPU_TEMP",
			sourceID: dcgm.DCGM_FI_DEV_GPU_TEMP, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.temperature", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "Cel", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity, attributes: []Attribute{{Name: "hw.sensor_location", Value: "gpu"}},
		},
		{
			id: "gpu.memory.temperature", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_MEMORY_TEMP",
			sourceID: dcgm.DCGM_FI_DEV_MEMORY_TEMP, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.temperature", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "Cel", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity, attributes: []Attribute{{Name: "hw.sensor_location", Value: "memory"}},
		},
		{
			id: "gpu.power.usage", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_POWER_USAGE",
			sourceID: dcgm.DCGM_FI_DEV_POWER_USAGE, entity: EntityGPU, valueType: ValueFloat64,
			timestamp: TimestampDCGMOrReadTime, nvidiaSourceName: "DCGM_FI_DEV_BOARD_POWER_RAW_WATTS",
			nvidiaSourceID: dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS, nvidiaName: "hw.power", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "W", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.energy.total", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION",
			sourceID: dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.energy", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "J", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetSource, conversion: ConversionMillijoulesToJoules,
		},
		{
			id: "gpu.utilization", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_GPU_UTIL",
			sourceID: dcgm.DCGM_FI_DEV_GPU_UTIL, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.gpu.utilization", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionPercentToRatio, attributes: []Attribute{{Name: "hw.gpu.task", Value: "general"}},
		},
		{
			id: "gpu.memory_copy.utilization", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_MEM_COPY_UTIL",
			sourceID: dcgm.DCGM_FI_DEV_MEM_COPY_UTIL, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory_copy.utilization", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionPercentToRatio,
		},
		{
			id: "gpu.encoder.utilization", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_ENC_UTIL",
			sourceID: dcgm.DCGM_FI_DEV_ENC_UTIL, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.gpu.utilization", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionPercentToRatio, attributes: []Attribute{{Name: "hw.gpu.task", Value: "encoder"}},
		},
		{
			id: "gpu.decoder.utilization", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_DEC_UTIL",
			sourceID: dcgm.DCGM_FI_DEV_DEC_UTIL, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.gpu.utilization", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionPercentToRatio, attributes: []Attribute{{Name: "hw.gpu.task", Value: "decoder"}},
		},
		{
			id: "gpu.memory.usage", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_FB_USED",
			sourceID: dcgm.DCGM_FI_DEV_FB_USED, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "hw.gpu.memory.usage", outputKind: OutputMetric,
			instrument: InstrumentUpDownCounter, unit: "By", temporality: TemporalityCumulative,
			reset: ResetExporterRestart, conversion: ConversionMebibytesToBytes,
		},
		{
			id: "gpu.memory.free", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_FB_FREE",
			sourceID: dcgm.DCGM_FI_DEV_FB_FREE, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory.free", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "By", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionMebibytesToBytes,
		},
		{
			id: "gpu.memory.reserved", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_FB_RESERVED",
			sourceID: dcgm.DCGM_FI_DEV_FB_RESERVED, entity: EntityGPUOrMIGGI, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory.reserved", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "By", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionMebibytesToBytes,
		},
		{
			id: "gpu.memory.remapped_rows.corrected", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS",
			sourceID: dcgm.DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory.remapped_rows", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{row}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetSource, conversion: ConversionIdentity, attributes: []Attribute{{Name: "error.type", Value: "corrected"}},
		},
		{
			id: "gpu.memory.remapped_rows.uncorrected", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS",
			sourceID: dcgm.DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory.remapped_rows", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{row}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetSource, conversion: ConversionIdentity, attributes: []Attribute{{Name: "error.type", Value: "uncorrected"}},
		},
		{
			id: "gpu.memory.row_remap.failure", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_ROW_REMAP_FAILURE",
			sourceID: dcgm.DCGM_FI_DEV_ROW_REMAP_FAILURE, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory.row_remap.failure", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.graphics_engine.utilization", sourceKind: SourceDCGM, sourceName: "DCGM_FI_PROF_GR_ENGINE_ACTIVE",
			sourceID: dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE, entity: EntityGPUOrMIGGI, valueType: ValueFloat64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.graphics_engine.utilization", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.memory_interface.utilization", sourceKind: SourceDCGM, sourceName: "DCGM_FI_PROF_DRAM_ACTIVE",
			sourceID: dcgm.DCGM_FI_PROF_DRAM_ACTIVE, entity: EntityGPUOrMIGGI, valueType: ValueFloat64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.memory_interface.utilization", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.tensor_pipe.utilization", sourceKind: SourceDCGM, sourceName: "DCGM_FI_PROF_PIPE_TENSOR_ACTIVE",
			sourceID: dcgm.DCGM_FI_PROF_PIPE_TENSOR_ACTIVE, entity: EntityGPUOrMIGGI, valueType: ValueFloat64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.tensor_pipe.utilization", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.pcie.io.receive", sourceKind: SourceDCGM, sourceName: "DCGM_FI_PROF_PCIE_RX_BYTES",
			sourceID: dcgm.DCGM_FI_PROF_PCIE_RX_BYTES, entity: EntityGPU, valueType: ValueFloat64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.pcie.io.rate", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "By/s", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity, attributes: []Attribute{{Name: "network.io.direction", Value: "receive"}},
		},
		{
			id: "gpu.pcie.io.transmit", sourceKind: SourceDCGM, sourceName: "DCGM_FI_PROF_PCIE_TX_BYTES",
			sourceID: dcgm.DCGM_FI_PROF_PCIE_TX_BYTES, entity: EntityGPU, valueType: ValueFloat64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.pcie.io.rate", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "By/s", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity, attributes: []Attribute{{Name: "network.io.direction", Value: "transmit"}},
		},
		{
			id: "gpu.pcie.replay", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
			sourceID: dcgm.DCGM_FI_DEV_PCIE_REPLAY_COUNTER, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.pcie.replay", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{retry}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetSource, conversion: ConversionIdentity,
		},
		{
			id: "gpu.xid.last", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_XID_ERRORS",
			sourceID: dcgm.DCGM_FI_DEV_XID_ERRORS, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaName: "com.nvidia.gpu.xid.last", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.nvlink.throughput", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL",
			sourceID: dcgm.DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL, entity: EntityGPU, valueType: ValueInt64,
			timestamp: TimestampDCGMOrReadTime, nvidiaSourceName: "DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND",
			nvidiaSourceID: dcgm.DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND,
			nvidiaName:     "com.nvidia.gpu.nvlink.throughput", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "By/s", temporality: TemporalityInstant, reset: ResetNone,
			conversion: ConversionIdentity,
		},
		{
			id: "gpu.vgpu.license.status", sourceKind: SourceDCGM, sourceName: "DCGM_FI_DEV_VGPU_LICENSE_STATUS",
			sourceID: dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS, entity: EntityGPU, valueType: ValueInt64,
			timestamp:      TimestampDCGMOrReadTime,
			nvidiaOmission: "field 526 entity scope is unresolved for nvidia/v1",
		},
		{
			id: "gpu.vgpu.instance.license.detail", sourceKind: SourceDCGM,
			sourceName: "DCGM_FI_DEV_VGPU_INSTANCE_LICENSE_STATUS",
			sourceID:   dcgm.DCGM_FI_DEV_VGPU_INSTANCE_LICENSE_STATUS, entity: EntityVGPU, valueType: ValueString,
			timestamp:      TimestampDCGMOrReadTime,
			nvidiaOmission: "vGPU entity collection is outside nvidia/v1",
		},
		{
			id: "gpu.health.status", sourceKind: SourceExporter, sourceName: "DCGM_EXP_GPU_HEALTH_STATUS",
			entity: EntityGPU, valueType: ValueInt64, timestamp: TimestampExporterEventTime,
			nvidiaName: "hw.status", outputKind: OutputMetric, instrument: InstrumentUpDownCounter,
			unit: "1", temporality: TemporalityCumulative, reset: ResetExporterRestart,
			conversion: ConversionHealthState,
		},
		{
			id: "gpu.clock_event.observations", sourceKind: SourceExporter, sourceName: "DCGM_EXP_CLOCK_EVENTS_COUNT",
			entity: EntityGPUOrMIGGI, valueType: ValueInt64, timestamp: TimestampExporterEventTime,
			nvidiaName: "com.nvidia.gpu.clock_event.observations", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "{observation}", temporality: TemporalityWindow,
			reset: ResetNone, conversion: ConversionIdentity,
		},
		{
			id: "gpu.clock_event.transitions", sourceKind: SourceExporter, sourceName: "DCGM_EXP_CLOCK_EVENTS_TOTAL",
			entity: EntityGPUOrMIGGI, valueType: ValueInt64, timestamp: TimestampExporterEventTime,
			nvidiaName: "com.nvidia.gpu.clock_event.transitions", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{event}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetExporterRestart, conversion: ConversionIdentity,
		},
		{
			id: "gpu.p2p.status", sourceKind: SourceExporter, sourceName: "DCGM_EXP_P2P_STATUS",
			entity: EntityGPU, valueType: ValueInt64, timestamp: TimestampExporterEventTime,
			nvidiaName: "com.nvidia.gpu.p2p.status", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "1", temporality: TemporalityInstant,
			reset: ResetNone, conversion: ConversionIdentity,
		},
		{
			id: "gpu.xid.observations", sourceKind: SourceExporter, sourceName: "DCGM_EXP_XID_ERRORS_COUNT",
			entity: EntityGPUOrMIGGI, valueType: ValueInt64, timestamp: TimestampExporterEventTime,
			nvidiaName: "com.nvidia.gpu.xid.observations", outputKind: OutputMetric,
			instrument: InstrumentGauge, unit: "{observation}", temporality: TemporalityWindow,
			reset: ResetNone, conversion: ConversionIdentity,
		},
		{
			id: "gpu.xid.records", sourceKind: SourceExporter, sourceName: "DCGM_EXP_XID_ERRORS_TOTAL",
			entity: EntityGPUOrMIGGI, valueType: ValueInt64, timestamp: TimestampExporterEventTime,
			nvidiaName: "com.nvidia.gpu.xid.records", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{record}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetExporterRestart, conversion: ConversionIdentity,
		},
		{
			id: "exporter.otlp.metric.send.failures", sourceKind: SourceExporter,
			sourceName: "exporter.otlp.metric.send.failures", entity: EntityExporter, valueType: ValueInt64,
			timestamp:  TimestampExporterEventTime,
			nvidiaName: "com.nvidia.dcgm.exporter.otlp.metric.send.failures", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{failure}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetExporterRestart, conversion: ConversionIdentity,
		},
		{
			id: "exporter.otlp.metric.intervals.skipped", sourceKind: SourceExporter,
			sourceName: "exporter.otlp.metric.intervals.skipped", entity: EntityExporter, valueType: ValueInt64,
			timestamp:  TimestampExporterEventTime,
			nvidiaName: "com.nvidia.dcgm.exporter.otlp.metric.intervals.skipped", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{interval}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetExporterRestart, conversion: ConversionIdentity,
		},
		{
			id: "exporter.otlp.log.records.dropped", sourceKind: SourceExporter,
			sourceName: "exporter.otlp.log.records.dropped", entity: EntityExporter, valueType: ValueInt64,
			timestamp:  TimestampExporterEventTime,
			nvidiaName: "com.nvidia.dcgm.exporter.otlp.log.records.dropped", outputKind: OutputMetric,
			instrument: InstrumentSum, unit: "{record}", temporality: TemporalityCumulative, monotonic: true,
			reset: ResetExporterRestart, conversion: ConversionIdentity,
		},
	}

	require.Len(t, tests, 36)
	require.Len(t, Signals(), 36)
	require.NoError(t, Validate())

	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			signal, ok := Lookup(tt.id)
			require.True(t, ok)
			binding, err := BindingFor(tt.id, ProfileNVIDIA)
			require.NoError(t, err)
			assert.Equal(t, signal.Output, binding)
			assert.Equal(t, Source{Kind: tt.sourceKind, Name: tt.sourceName, FieldID: uint16(tt.sourceID)}, signal.Source)
			assert.Equal(t, tt.entity, signal.Entity)
			assert.Equal(t, tt.valueType, signal.ValueType)
			assert.Equal(t, tt.timestamp, signal.Timestamp)
			assert.Equal(t, requiredIdentityForContract(tt.id, tt.entity), signal.RequiredIdentity)

			wantBinding := Binding{
				Name: tt.nvidiaName, OmitReason: tt.nvidiaOmission,
				OutputKind: tt.outputKind, Instrument: tt.instrument, Unit: tt.unit,
				Temporality: tt.temporality, Monotonic: tt.monotonic, Reset: tt.reset,
				Conversion: tt.conversion, Attributes: tt.attributes,
			}
			if tt.nvidiaSourceName != "" {
				wantBinding.SourceOverride = Source{
					Kind: SourceDCGM, Name: tt.nvidiaSourceName, FieldID: uint16(tt.nvidiaSourceID),
				}
			}
			assert.Equal(t, wantBinding, signal.Output)

			if tt.sourceKind == SourceDCGM {
				assertDCGMSource(t, tt.sourceName, tt.sourceID, false)
			}
			if tt.nvidiaSourceName != "" {
				assertDCGMSource(t, tt.nvidiaSourceName, tt.nvidiaSourceID, true)
			}
		})
	}

	profiles := []struct {
		name           string
		id             ProfileID
		wantBindingErr string
	}{
		{name: "legacy", id: ProfileID("legacy"), wantBindingErr: `unknown output profile "legacy"`},
		{name: "nvidia/v1", id: ProfileID("nvidia/v1")},
		{name: "unversioned nvidia", id: ProfileID("nvidia"), wantBindingErr: `unknown output profile "nvidia"`},
		{name: "unknown", id: ProfileID("missing"), wantBindingErr: `unknown output profile "missing"`},
	}
	for _, tt := range profiles {
		t.Run("profile "+tt.name, func(t *testing.T) {
			_, err := BindingFor("gpu.temperature", tt.id)
			if tt.wantBindingErr != "" {
				require.EqualError(t, err, tt.wantBindingErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// assertDCGMSource checks the field ID and, when requested, whether DCGM can
// read the field's current value.
func assertDCGMSource(t *testing.T, name string, want dcgm.Short, requireCurrent bool) {
	t.Helper()
	got, ok := dcgm.GetFieldID(name)
	require.True(t, ok, name)
	assert.Equal(t, want, got)
	if requireCurrent {
		assert.True(t, dcgm.IsCurrentField(name), name)
	}
}

// requiredIdentityForContract returns the expected identifying attributes
// for a signal and entity type.
func requiredIdentityForContract(id SignalID, entity EntityScope) []string {
	switch id {
	case "gpu.temperature", "gpu.memory.temperature":
		return []string{"hw.id", "hw.parent"}
	case "gpu.power.usage", "gpu.energy.total":
		return []string{"hw.id", "hw.type"}
	case "gpu.health.status":
		return []string{
			"hw.id", "hw.type", "hw.state",
			"com.nvidia.dcgm.health.watch", "com.nvidia.dcgm.health.error.code",
			"com.nvidia.dcgm.health.error.severity", "com.nvidia.dcgm.health.error.category",
		}
	case "gpu.clock_event.observations":
		return []string{
			"hw.id", "hw.type", "com.nvidia.gpu.clock_event.type", "com.nvidia.dcgm.exporter.sample.window",
		}
	case "gpu.clock_event.transitions":
		return []string{"hw.id", "hw.type", "com.nvidia.gpu.clock_event.type"}
	case "gpu.p2p.status":
		return []string{
			"com.nvidia.gpu.uuid", "com.nvidia.gpu.peer.uuid", "com.nvidia.gpu.p2p.link.status",
		}
	case "gpu.xid.observations":
		return []string{
			"hw.id", "hw.type", "com.nvidia.gpu.xid.code", "com.nvidia.dcgm.exporter.sample.window",
		}
	case "gpu.xid.records":
		return []string{"hw.id", "hw.type", "com.nvidia.gpu.xid.code"}
	case "exporter.otlp.log.records.dropped":
		return []string{"service.name", "service.instance.id", "com.nvidia.dcgm.exporter.log.source"}
	}

	switch entity {
	case EntityGPU, EntityVGPU:
		return []string{"hw.id"}
	case EntityGPUOrMIGGI:
		return []string{"hw.id", "hw.type"}
	case EntityExporter:
		return []string{"service.name", "service.instance.id"}
	default:
		return nil
	}
}

var defaultSignalIDs = []SignalID{
	"gpu.driver.version",
	"gpu.clock.sm",
	"gpu.clock.memory",
	"gpu.temperature",
	"gpu.memory.temperature",
	"gpu.power.usage",
	"gpu.energy.total",
	"gpu.utilization",
	"gpu.memory_copy.utilization",
	"gpu.encoder.utilization",
	"gpu.decoder.utilization",
	"gpu.memory.usage",
	"gpu.memory.free",
	"gpu.memory.reserved",
	"gpu.memory.remapped_rows.corrected",
	"gpu.memory.remapped_rows.uncorrected",
	"gpu.memory.row_remap.failure",
	"gpu.graphics_engine.utilization",
	"gpu.memory_interface.utilization",
	"gpu.tensor_pipe.utilization",
	"gpu.pcie.io.receive",
	"gpu.pcie.io.transmit",
	"gpu.pcie.replay",
	"gpu.xid.last",
	"gpu.nvlink.throughput",
}

func TestResolveSet(t *testing.T) {
	tests := []struct {
		name    string
		set     SetID
		want    []SignalID
		wantErr string
	}{
		{name: "default", set: SetDefault, want: defaultSignalIDs},
		{name: "temperature", set: SetTemperature, want: []SignalID{"gpu.temperature", "gpu.memory.temperature"}},
		{name: "power", set: SetPower, want: []SignalID{"gpu.power.usage", "gpu.energy.total"}},
		{
			name: "utilization", set: SetUtilization,
			want: []SignalID{
				"gpu.utilization",
				"gpu.memory_copy.utilization",
				"gpu.encoder.utilization",
				"gpu.decoder.utilization",
			},
		},
		{name: "unknown", set: SetID("@missing"), wantErr: "unknown named set \"@missing\""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveSet(tt.set)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T)
		check  func(t *testing.T)
	}{
		{
			name: "watch sources",
			mutate: func(t *testing.T) {
				sources, err := WatchSourcesFor("gpu.power.usage", ProfileNVIDIA)
				require.NoError(t, err)
				sources[0].Source.FieldID = 1
				sources[1].Entity = EntityExporter
			},
			check: func(t *testing.T) {
				sources, err := WatchSourcesFor("gpu.power.usage", ProfileNVIDIA)
				require.NoError(t, err)
				assert.Equal(t, uint16(dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS), sources[0].Source.FieldID)
				assert.Equal(t, EntityGPU, sources[1].Entity)
			},
		},
		{
			name: "signals",
			mutate: func(t *testing.T) {
				signals := Signals()
				signals[3].RequiredIdentity[0] = "changed"
				signals[3].Output.Attributes[0].Value = "changed"
			},
			check: func(t *testing.T) {
				signals := Signals()
				assert.Equal(t, "hw.id", signals[3].RequiredIdentity[0])
				assert.Equal(t, "gpu", signals[3].Output.Attributes[0].Value)
			},
		},
		{
			name: "lookup",
			mutate: func(t *testing.T) {
				signal, ok := Lookup("gpu.temperature")
				require.True(t, ok)
				signal.RequiredIdentity[0] = "changed"
				signal.Output.Attributes[0].Value = "changed"
			},
			check: func(t *testing.T) {
				signal, ok := Lookup("gpu.temperature")
				require.True(t, ok)
				assert.Equal(t, "hw.id", signal.RequiredIdentity[0])
				assert.Equal(t, "gpu", signal.Output.Attributes[0].Value)
			},
		},
		{
			name: "binding",
			mutate: func(t *testing.T) {
				binding, err := BindingFor("gpu.temperature", ProfileNVIDIA)
				require.NoError(t, err)
				binding.Attributes[0].Value = "changed"
			},
			check: func(t *testing.T) {
				binding, err := BindingFor("gpu.temperature", ProfileNVIDIA)
				require.NoError(t, err)
				assert.Equal(t, "gpu", binding.Attributes[0].Value)
			},
		},
		{
			name: "named set",
			mutate: func(t *testing.T) {
				signals, err := ResolveSet(SetDefault)
				require.NoError(t, err)
				signals[0] = "changed"
			},
			check: func(t *testing.T) {
				signals, err := ResolveSet(SetDefault)
				require.NoError(t, err)
				assert.Equal(t, SignalID("gpu.driver.version"), signals[0])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.mutate(t)
			tt.check(t)
		})
	}
}
