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
	"fmt"
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

const (
	CatalogVersion uint      = 1
	ProfileNVIDIA  ProfileID = "nvidia/v1"
)

const (
	SetDefault     SetID = "@default"
	SetTemperature SetID = "@temperature"
	SetPower       SetID = "@power"
	SetUtilization SetID = "@utilization"
)

const (
	signalDriverVersion                 SignalID = "gpu.driver.version"
	signalClockSM                       SignalID = "gpu.clock.sm"
	signalClockMemory                   SignalID = "gpu.clock.memory"
	signalTemperature                   SignalID = "gpu.temperature"
	signalMemoryTemperature             SignalID = "gpu.memory.temperature"
	signalPowerUsage                    SignalID = "gpu.power.usage"
	signalEnergyTotal                   SignalID = "gpu.energy.total"
	signalUtilization                   SignalID = "gpu.utilization"
	signalMemoryCopyUtilization         SignalID = "gpu.memory_copy.utilization"
	signalEncoderUtilization            SignalID = "gpu.encoder.utilization"
	signalDecoderUtilization            SignalID = "gpu.decoder.utilization"
	signalMemoryUsage                   SignalID = "gpu.memory.usage"
	signalMemoryFree                    SignalID = "gpu.memory.free"
	signalMemoryReserved                SignalID = "gpu.memory.reserved"
	signalMemoryRemappedRowsCorrected   SignalID = "gpu.memory.remapped_rows.corrected"
	signalMemoryRemappedRowsUncorrected SignalID = "gpu.memory.remapped_rows.uncorrected"
	signalMemoryRowRemapFailure         SignalID = "gpu.memory.row_remap.failure"
	signalGraphicsEngineUtilization     SignalID = "gpu.graphics_engine.utilization"
	signalMemoryInterfaceUtilization    SignalID = "gpu.memory_interface.utilization"
	signalTensorPipeUtilization         SignalID = "gpu.tensor_pipe.utilization"
	signalPCIeIOReceive                 SignalID = "gpu.pcie.io.receive"
	signalPCIeIOTransmit                SignalID = "gpu.pcie.io.transmit"
	signalPCIeReplay                    SignalID = "gpu.pcie.replay"
	signalXIDLast                       SignalID = "gpu.xid.last"
	signalNVLinkThroughput              SignalID = "gpu.nvlink.throughput"
	signalVGPULicenseStatus             SignalID = "gpu.vgpu.license.status"
	signalVGPUInstanceLicenseDetail     SignalID = "gpu.vgpu.instance.license.detail"
	signalHealthStatus                  SignalID = "gpu.health.status"
	signalClockEventObservations        SignalID = "gpu.clock_event.observations"
	signalClockEventTransitions         SignalID = "gpu.clock_event.transitions"
	signalP2PStatus                     SignalID = "gpu.p2p.status"
	signalXIDObservations               SignalID = "gpu.xid.observations"
	signalXIDRecords                    SignalID = "gpu.xid.records"
	signalOTLPMetricSendFailures        SignalID = "exporter.otlp.metric.send.failures"
	signalOTLPMetricIntervalsSkipped    SignalID = "exporter.otlp.metric.intervals.skipped"
	signalOTLPLogRecordsDropped         SignalID = "exporter.otlp.log.records.dropped"
)

const (
	metricGPUClockSM                         = "com.nvidia.gpu.sm.clock"
	metricGPUClockMemory                     = "com.nvidia.gpu.memory.clock"
	metricTemperature                        = "hw.temperature"
	metricPower                              = "hw.power"
	metricEnergy                             = "hw.energy"
	metricGPUUtilization                     = "hw.gpu.utilization"
	metricGPUMemoryCopyUtilization           = "com.nvidia.gpu.memory_copy.utilization"
	metricGPUMemoryUsage                     = "hw.gpu.memory.usage"
	metricGPUMemoryFree                      = "com.nvidia.gpu.memory.free"
	metricGPUMemoryReserved                  = "com.nvidia.gpu.memory.reserved"
	metricGPUMemoryRemappedRows              = "com.nvidia.gpu.memory.remapped_rows"
	metricGPUMemoryRowRemapFailure           = "com.nvidia.gpu.memory.row_remap.failure"
	metricGPUGraphicsEngineUtilization       = "com.nvidia.gpu.graphics_engine.utilization"
	metricGPUMemoryInterfaceUtilization      = "com.nvidia.gpu.memory_interface.utilization"
	metricGPUTensorPipeUtilization           = "com.nvidia.gpu.tensor_pipe.utilization"
	metricGPUPCIeIORate                      = "com.nvidia.gpu.pcie.io.rate"
	metricGPUPCIeReplay                      = "com.nvidia.gpu.pcie.replay"
	metricGPUXIDLast                         = "com.nvidia.gpu.xid.last"
	metricGPUNVLinkThroughput                = "com.nvidia.gpu.nvlink.throughput"
	metricStatus                             = "hw.status"
	metricGPUClockEventObservations          = "com.nvidia.gpu.clock_event.observations"
	metricGPUClockEventTransitions           = "com.nvidia.gpu.clock_event.transitions"
	metricGPUP2PStatus                       = "com.nvidia.gpu.p2p.status"
	metricGPUXIDObservations                 = "com.nvidia.gpu.xid.observations"
	metricGPUXIDRecords                      = "com.nvidia.gpu.xid.records"
	metricExporterOTLPMetricSendFailures     = "com.nvidia.dcgm.exporter.otlp.metric.send.failures"
	metricExporterOTLPMetricIntervalsSkipped = "com.nvidia.dcgm.exporter.otlp.metric.intervals.skipped"
	metricExporterOTLPLogRecordsDropped      = "com.nvidia.dcgm.exporter.otlp.log.records.dropped"
)

const (
	attributeDriverVersion      = "hw.driver_version"
	attributeSensorLocation     = "hw.sensor_location"
	attributeHWType             = "hw.type"
	attributeGPUTask            = "hw.gpu.task"
	attributeErrorType          = "error.type"
	attributeNetworkIODirection = "network.io.direction"
	attributeServiceInstanceID  = "service.instance.id"
	attributeServiceName        = "service.name"
)

const (
	ExporterFieldClockEventsCount = "DCGM_EXP_CLOCK_EVENTS_COUNT"
	ExporterFieldClockEventsTotal = "DCGM_EXP_CLOCK_EVENTS_TOTAL"
	ExporterFieldXIDErrorsCount   = "DCGM_EXP_XID_ERRORS_COUNT"
	ExporterFieldXIDErrorsTotal   = "DCGM_EXP_XID_ERRORS_TOTAL"
	ExporterFieldGPUHealthStatus  = "DCGM_EXP_GPU_HEALTH_STATUS"
	ExporterFieldP2PStatus        = "DCGM_EXP_P2P_STATUS"
)

var catalogV1 = definition{
	Version: CatalogVersion,
	Signals: []Signal{
		{
			ID:               signalDriverVersion,
			Source:           dcgmSource("DCGM_FI_DRIVER_VERSION", dcgm.DCGM_FI_DRIVER_VERSION),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueString,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           attributeBinding(attributeDriverVersion),
		},
		{
			ID:               signalClockSM,
			Source:           dcgmSource("DCGM_FI_DEV_SM_CLOCK", dcgm.DCGM_FI_DEV_SM_CLOCK),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUClockSM, "Hz", ConversionMegahertzToHertz),
		},
		{
			ID:               signalClockMemory,
			Source:           dcgmSource("DCGM_FI_DEV_MEM_CLOCK", dcgm.DCGM_FI_DEV_MEM_CLOCK),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUClockMemory, "Hz", ConversionMegahertzToHertz),
		},
		{
			ID:               signalTemperature,
			Source:           dcgmSource("DCGM_FI_DEV_GPU_TEMP", dcgm.DCGM_FI_DEV_GPU_TEMP),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: []string{"hw.id", "hw.parent"},
			Output: gaugeBinding(metricTemperature, "Cel", ConversionIdentity,
				Attribute{Name: attributeSensorLocation, Value: "gpu"}),
		},
		{
			ID:               signalMemoryTemperature,
			Source:           dcgmSource("DCGM_FI_DEV_MEMORY_TEMP", dcgm.DCGM_FI_DEV_MEMORY_TEMP),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: []string{"hw.id", "hw.parent"},
			Output: gaugeBinding(metricTemperature, "Cel", ConversionIdentity,
				Attribute{Name: attributeSensorLocation, Value: "memory"}),
		},
		{
			ID:               signalPowerUsage,
			Source:           dcgmSource("DCGM_FI_DEV_POWER_USAGE", dcgm.DCGM_FI_DEV_POWER_USAGE),
			Entity:           EntityGPU,
			ValueType:        ValueFloat64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: []string{"hw.id", attributeHWType},
			Output: withSource(
				gaugeBinding(metricPower, "W", ConversionIdentity),
				dcgmSource("DCGM_FI_DEV_BOARD_POWER_RAW_WATTS", dcgm.DCGM_FI_DEV_BOARD_POWER_RAW_WATTS),
			),
		},
		{
			ID:               signalEnergyTotal,
			Source:           dcgmSource("DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", dcgm.DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: []string{"hw.id", attributeHWType},
			Output:           sumBinding(metricEnergy, "J", ResetSource, ConversionMillijoulesToJoules),
		},
		{
			ID:               signalUtilization,
			Source:           dcgmSource("DCGM_FI_DEV_GPU_UTIL", dcgm.DCGM_FI_DEV_GPU_UTIL),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output: gaugeBinding(metricGPUUtilization, "1", ConversionPercentToRatio,
				Attribute{Name: attributeGPUTask, Value: "general"}),
		},
		{
			ID:               signalMemoryCopyUtilization,
			Source:           dcgmSource("DCGM_FI_DEV_MEM_COPY_UTIL", dcgm.DCGM_FI_DEV_MEM_COPY_UTIL),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUMemoryCopyUtilization, "1", ConversionPercentToRatio),
		},
		{
			ID:               signalEncoderUtilization,
			Source:           dcgmSource("DCGM_FI_DEV_ENC_UTIL", dcgm.DCGM_FI_DEV_ENC_UTIL),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output: gaugeBinding(metricGPUUtilization, "1", ConversionPercentToRatio,
				Attribute{Name: attributeGPUTask, Value: "encoder"}),
		},
		{
			ID:               signalDecoderUtilization,
			Source:           dcgmSource("DCGM_FI_DEV_DEC_UTIL", dcgm.DCGM_FI_DEV_DEC_UTIL),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output: gaugeBinding(metricGPUUtilization, "1", ConversionPercentToRatio,
				Attribute{Name: attributeGPUTask, Value: "decoder"}),
		},
		{
			ID:               signalMemoryUsage,
			Source:           dcgmSource("DCGM_FI_DEV_FB_USED", dcgm.DCGM_FI_DEV_FB_USED),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           upDownBinding(metricGPUMemoryUsage, "By", ResetExporterRestart, ConversionMebibytesToBytes),
		},
		{
			ID:               signalMemoryFree,
			Source:           dcgmSource("DCGM_FI_DEV_FB_FREE", dcgm.DCGM_FI_DEV_FB_FREE),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUMemoryFree, "By", ConversionMebibytesToBytes),
		},
		{
			ID:               signalMemoryReserved,
			Source:           dcgmSource("DCGM_FI_DEV_FB_RESERVED", dcgm.DCGM_FI_DEV_FB_RESERVED),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUMemoryReserved, "By", ConversionMebibytesToBytes),
		},
		{
			ID:               signalMemoryRemappedRowsCorrected,
			Source:           dcgmSource("DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS", dcgm.DCGM_FI_DEV_CORRECTABLE_REMAPPED_ROWS),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output: sumBinding(metricGPUMemoryRemappedRows, "{row}", ResetSource, ConversionIdentity,
				Attribute{Name: attributeErrorType, Value: "corrected"}),
		},
		{
			ID:               signalMemoryRemappedRowsUncorrected,
			Source:           dcgmSource("DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS", dcgm.DCGM_FI_DEV_UNCORRECTABLE_REMAPPED_ROWS),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output: sumBinding(metricGPUMemoryRemappedRows, "{row}", ResetSource, ConversionIdentity,
				Attribute{Name: attributeErrorType, Value: "uncorrected"}),
		},
		{
			ID:               signalMemoryRowRemapFailure,
			Source:           dcgmSource("DCGM_FI_DEV_ROW_REMAP_FAILURE", dcgm.DCGM_FI_DEV_ROW_REMAP_FAILURE),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output:           gaugeBinding(metricGPUMemoryRowRemapFailure, "1", ConversionIdentity),
		},
		{
			ID:               signalGraphicsEngineUtilization,
			Source:           dcgmSource("DCGM_FI_PROF_GR_ENGINE_ACTIVE", dcgm.DCGM_FI_PROF_GR_ENGINE_ACTIVE),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueFloat64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUGraphicsEngineUtilization, "1", ConversionIdentity),
		},
		{
			ID:               signalMemoryInterfaceUtilization,
			Source:           dcgmSource("DCGM_FI_PROF_DRAM_ACTIVE", dcgm.DCGM_FI_PROF_DRAM_ACTIVE),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueFloat64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUMemoryInterfaceUtilization, "1", ConversionIdentity),
		},
		{
			ID:               signalTensorPipeUtilization,
			Source:           dcgmSource("DCGM_FI_PROF_PIPE_TENSOR_ACTIVE", dcgm.DCGM_FI_PROF_PIPE_TENSOR_ACTIVE),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueFloat64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuOrMIGIdentity(),
			Output:           gaugeBinding(metricGPUTensorPipeUtilization, "1", ConversionIdentity),
		},
		{
			ID:               signalPCIeIOReceive,
			Source:           dcgmSource("DCGM_FI_PROF_PCIE_RX_BYTES", dcgm.DCGM_FI_PROF_PCIE_RX_BYTES),
			Entity:           EntityGPU,
			ValueType:        ValueFloat64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output: gaugeBinding(metricGPUPCIeIORate, "By/s", ConversionIdentity,
				Attribute{Name: attributeNetworkIODirection, Value: "receive"}),
		},
		{
			ID:               signalPCIeIOTransmit,
			Source:           dcgmSource("DCGM_FI_PROF_PCIE_TX_BYTES", dcgm.DCGM_FI_PROF_PCIE_TX_BYTES),
			Entity:           EntityGPU,
			ValueType:        ValueFloat64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output: gaugeBinding(metricGPUPCIeIORate, "By/s", ConversionIdentity,
				Attribute{Name: attributeNetworkIODirection, Value: "transmit"}),
		},
		{
			ID:               signalPCIeReplay,
			Source:           dcgmSource("DCGM_FI_DEV_PCIE_REPLAY_COUNTER", dcgm.DCGM_FI_DEV_PCIE_REPLAY_COUNTER),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output:           sumBinding(metricGPUPCIeReplay, "{retry}", ResetSource, ConversionIdentity),
		},
		{
			ID:               signalXIDLast,
			Source:           dcgmSource("DCGM_FI_DEV_XID_ERRORS", dcgm.DCGM_FI_DEV_XID_ERRORS),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output:           gaugeBinding(metricGPUXIDLast, "1", ConversionIdentity),
		},
		{
			ID:               signalNVLinkThroughput,
			Source:           dcgmSource("DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL", dcgm.DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output: withSource(
				gaugeBinding(metricGPUNVLinkThroughput, "By/s", ConversionIdentity),
				dcgmSource("DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND", dcgm.DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND),
			),
		},
		{
			ID:               signalVGPULicenseStatus,
			Source:           dcgmSource("DCGM_FI_DEV_VGPU_LICENSE_STATUS", dcgm.DCGM_FI_DEV_VGPU_LICENSE_STATUS),
			Entity:           EntityGPU,
			ValueType:        ValueInt64,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: gpuIdentity(),
			Output:           omitted("field 526 entity scope is unresolved for nvidia/v1"),
		},
		{
			ID:               signalVGPUInstanceLicenseDetail,
			Source:           dcgmSource("DCGM_FI_DEV_VGPU_INSTANCE_LICENSE_STATUS", dcgm.DCGM_FI_DEV_VGPU_INSTANCE_LICENSE_STATUS),
			Entity:           EntityVGPU,
			ValueType:        ValueString,
			Timestamp:        TimestampDCGMOrReadTime,
			RequiredIdentity: []string{"hw.id"},
			Output:           omitted("vGPU entity collection is outside nvidia/v1"),
		},
		{
			ID:        signalHealthStatus,
			Source:    exporterSource(ExporterFieldGPUHealthStatus),
			Entity:    EntityGPU,
			ValueType: ValueInt64,
			Timestamp: TimestampExporterEventTime,
			RequiredIdentity: []string{
				"hw.id", attributeHWType, "hw.state",
				"com.nvidia.dcgm.health.watch", "com.nvidia.dcgm.health.error.code",
				"com.nvidia.dcgm.health.error.severity", "com.nvidia.dcgm.health.error.category",
			},
			Output: upDownBinding(metricStatus, "1", ResetExporterRestart, ConversionHealthState),
		},
		{
			ID:        signalClockEventObservations,
			Source:    exporterSource(ExporterFieldClockEventsCount),
			Entity:    EntityGPUOrMIGGI,
			ValueType: ValueInt64,
			Timestamp: TimestampExporterEventTime,
			RequiredIdentity: []string{
				"hw.id", attributeHWType, "com.nvidia.gpu.clock_event.type", "com.nvidia.dcgm.exporter.sample.window",
			},
			Output: windowGaugeBinding(metricGPUClockEventObservations, "{observation}", ConversionIdentity),
		},
		{
			ID:               signalClockEventTransitions,
			Source:           exporterSource(ExporterFieldClockEventsTotal),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampExporterEventTime,
			RequiredIdentity: []string{"hw.id", attributeHWType, "com.nvidia.gpu.clock_event.type"},
			Output:           sumBinding(metricGPUClockEventTransitions, "{event}", ResetExporterRestart, ConversionIdentity),
		},
		{
			ID:        signalP2PStatus,
			Source:    exporterSource(ExporterFieldP2PStatus),
			Entity:    EntityGPU,
			ValueType: ValueInt64,
			Timestamp: TimestampExporterEventTime,
			RequiredIdentity: []string{
				"com.nvidia.gpu.uuid", "com.nvidia.gpu.peer.uuid", "com.nvidia.gpu.p2p.link.status",
			},
			Output: gaugeBinding(metricGPUP2PStatus, "1", ConversionIdentity),
		},
		{
			ID:        signalXIDObservations,
			Source:    exporterSource(ExporterFieldXIDErrorsCount),
			Entity:    EntityGPUOrMIGGI,
			ValueType: ValueInt64,
			Timestamp: TimestampExporterEventTime,
			RequiredIdentity: []string{
				"hw.id", attributeHWType, "com.nvidia.gpu.xid.code", "com.nvidia.dcgm.exporter.sample.window",
			},
			Output: windowGaugeBinding(metricGPUXIDObservations, "{observation}", ConversionIdentity),
		},
		{
			ID:               signalXIDRecords,
			Source:           exporterSource(ExporterFieldXIDErrorsTotal),
			Entity:           EntityGPUOrMIGGI,
			ValueType:        ValueInt64,
			Timestamp:        TimestampExporterEventTime,
			RequiredIdentity: []string{"hw.id", attributeHWType, "com.nvidia.gpu.xid.code"},
			Output:           sumBinding(metricGPUXIDRecords, "{record}", ResetExporterRestart, ConversionIdentity),
		},
		{
			ID:               signalOTLPMetricSendFailures,
			Source:           exporterSource(string(signalOTLPMetricSendFailures)),
			Entity:           EntityExporter,
			ValueType:        ValueInt64,
			Timestamp:        TimestampExporterEventTime,
			RequiredIdentity: []string{attributeServiceName, attributeServiceInstanceID},
			Output:           sumBinding(metricExporterOTLPMetricSendFailures, "{failure}", ResetExporterRestart, ConversionIdentity),
		},
		{
			ID:               signalOTLPMetricIntervalsSkipped,
			Source:           exporterSource(string(signalOTLPMetricIntervalsSkipped)),
			Entity:           EntityExporter,
			ValueType:        ValueInt64,
			Timestamp:        TimestampExporterEventTime,
			RequiredIdentity: []string{attributeServiceName, attributeServiceInstanceID},
			Output:           sumBinding(metricExporterOTLPMetricIntervalsSkipped, "{interval}", ResetExporterRestart, ConversionIdentity),
		},
		{
			ID:        signalOTLPLogRecordsDropped,
			Source:    exporterSource(string(signalOTLPLogRecordsDropped)),
			Entity:    EntityExporter,
			ValueType: ValueInt64,
			Timestamp: TimestampExporterEventTime,
			RequiredIdentity: []string{
				attributeServiceName, attributeServiceInstanceID, "com.nvidia.dcgm.exporter.log.source",
			},
			Output: sumBinding(metricExporterOTLPLogRecordsDropped, "{record}", ResetExporterRestart, ConversionIdentity),
		},
	},
	Sets: []namedSet{
		{ID: SetDefault, Signals: []SignalID{
			signalDriverVersion,
			signalClockSM,
			signalClockMemory,
			signalTemperature,
			signalMemoryTemperature,
			signalPowerUsage,
			signalEnergyTotal,
			signalUtilization,
			signalMemoryCopyUtilization,
			signalEncoderUtilization,
			signalDecoderUtilization,
			signalMemoryUsage,
			signalMemoryFree,
			signalMemoryReserved,
			signalMemoryRemappedRowsCorrected,
			signalMemoryRemappedRowsUncorrected,
			signalMemoryRowRemapFailure,
			signalGraphicsEngineUtilization,
			signalMemoryInterfaceUtilization,
			signalTensorPipeUtilization,
			signalPCIeIOReceive,
			signalPCIeIOTransmit,
			signalPCIeReplay,
			signalXIDLast,
			signalNVLinkThroughput,
		}},
		{ID: SetTemperature, Signals: []SignalID{signalTemperature, signalMemoryTemperature}},
		{ID: SetPower, Signals: []SignalID{signalPowerUsage, signalEnergyTotal}},
		{ID: SetUtilization, Signals: []SignalID{
			signalUtilization,
			signalMemoryCopyUtilization,
			signalEncoderUtilization,
			signalDecoderUtilization,
		}},
	},
}

var signalIndex = func() map[SignalID]int {
	index := make(map[SignalID]int, len(catalogV1.Signals))
	for i := range catalogV1.Signals {
		index[catalogV1.Signals[i].ID] = i
	}
	return index
}()

// Signals returns independent copies of all catalog signals.
func Signals() []Signal {
	signals := make([]Signal, len(catalogV1.Signals))
	for i, signal := range catalogV1.Signals {
		signals[i] = cloneSignal(signal)
	}
	return signals
}

// Lookup returns an independent copy of the signal identified by id.
func Lookup(id SignalID) (Signal, bool) {
	i, ok := signalIndex[id]
	if !ok {
		return Signal{}, false
	}
	return cloneSignal(catalogV1.Signals[i]), true
}

// BindingFor returns an independent copy of a signal's profile binding.
func BindingFor(id SignalID, profile ProfileID) (Binding, error) {
	signal, ok := Lookup(id)
	if !ok {
		return Binding{}, fmt.Errorf("unknown signal %q", id)
	}
	if profile != ProfileNVIDIA {
		return Binding{}, fmt.Errorf("unknown output profile %q", profile)
	}
	return signal.Output, nil
}

// ResolveSet returns an independent copy of a named set's signal IDs.
func ResolveSet(id SetID) ([]SignalID, error) {
	for _, set := range catalogV1.Sets {
		if set.ID == id {
			return slices.Clone(set.Signals), nil
		}
	}
	return nil, fmt.Errorf("unknown named set %q", id)
}

// Validate checks the built-in catalog definition.
func Validate() error {
	return validate(catalogV1)
}

func dcgmSource(name string, id dcgm.Short) Source {
	return Source{Kind: SourceDCGM, Name: name, FieldID: uint16(id)}
}

func exporterSource(name string) Source {
	return Source{Kind: SourceExporter, Name: name}
}

func attributeBinding(name string) Binding {
	return Binding{Name: name, OutputKind: OutputAttribute, Conversion: ConversionIdentity}
}

// gaugeBinding maps a signal to a gauge reporting its current value.
func gaugeBinding(name, unit string, conversion Conversion, attributes ...Attribute) Binding {
	return Binding{
		Name: name, OutputKind: OutputMetric, Instrument: InstrumentGauge, Unit: unit,
		Temporality: TemporalityInstant, Reset: ResetNone, Conversion: conversion,
		Attributes: attributes,
	}
}

// windowGaugeBinding creates a gauge for sampled observations within a sampling window.
func windowGaugeBinding(name, unit string, conversion Conversion, attributes ...Attribute) Binding {
	binding := gaugeBinding(name, unit, conversion, attributes...)
	binding.Temporality = TemporalityWindow
	return binding
}

// sumBinding creates a cumulative sum marked as never decreasing between resets.
func sumBinding(name, unit string, reset ResetRule, conversion Conversion, attributes ...Attribute) Binding {
	return Binding{
		Name: name, OutputKind: OutputMetric, Instrument: InstrumentSum, Unit: unit,
		Temporality: TemporalityCumulative, Monotonic: true, Reset: reset, Conversion: conversion,
		Attributes: attributes,
	}
}

// upDownBinding creates a cumulative counter whose value may increase or decrease.
func upDownBinding(name, unit string, reset ResetRule, conversion Conversion, attributes ...Attribute) Binding {
	return Binding{
		Name: name, OutputKind: OutputMetric, Instrument: InstrumentUpDownCounter, Unit: unit,
		Temporality: TemporalityCumulative, Reset: reset, Conversion: conversion, Attributes: attributes,
	}
}

func omitted(reason string) Binding {
	return Binding{OmitReason: reason}
}

func withSource(binding Binding, source Source) Binding {
	binding.SourceOverride = source
	return binding
}

func gpuIdentity() []string {
	return []string{"hw.id"}
}

func gpuOrMIGIdentity() []string {
	return []string{"hw.id", attributeHWType}
}

// cloneSignal copies the signal's slices, including those in its output binding.
func cloneSignal(signal Signal) Signal {
	signal.RequiredIdentity = slices.Clone(signal.RequiredIdentity)
	signal.Output = cloneBinding(signal.Output)
	return signal
}

// cloneBinding copies the binding's attribute slice.
func cloneBinding(binding Binding) Binding {
	binding.Attributes = slices.Clone(binding.Attributes)
	return binding
}
