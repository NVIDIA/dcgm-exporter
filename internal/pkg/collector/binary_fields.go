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
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
)

// binaryFieldMember describes one numeric member of a binary DCGM struct:
// the suffix appended to the field name to form the member metric name, and
// the member offset within the struct.
type binaryFieldMember struct {
	suffix string
	offset int
}

// binaryFieldLayout describes a binary DCGM struct whose exported members
// are all unsigned 32-bit integers: the minimum blob size in bytes and the
// members to export. The struct version member is never exported.
type binaryFieldLayout struct {
	size    int
	members []binaryFieldMember
}

// binaryFieldLayouts maps binary field IDs to the layout of the struct the
// field carries. Binary fields without a layout here are not exported.
var binaryFieldLayouts = map[dcgm.Short]binaryFieldLayout{
	// dcgmDeviceEncStats_v1: version, sessionCount, averageFps, averageLatency.
	dcgm.DCGM_FI_DEV_ENC_STATS: {
		size: 16,
		members: []binaryFieldMember{
			{suffix: "SESSION_COUNT", offset: 4},
			{suffix: "AVG_FPS", offset: 8},
			{suffix: "AVG_LATENCY", offset: 12},
		},
	},
	// dcgmDeviceFbcStats_v1: version, sessionCount, averageFps, averageLatency.
	dcgm.DCGM_FI_DEV_FBC_STATS: {
		size: 16,
		members: []binaryFieldMember{
			{suffix: "SESSION_COUNT", offset: 4},
			{suffix: "AVG_FPS", offset: 8},
			{suffix: "AVG_LATENCY", offset: 12},
		},
	},
}

// binaryMemberValue is one decoded struct member: the metric name suffix
// and the member value.
type binaryMemberValue struct {
	suffix string
	value  uint32
}

// decodeBinaryFieldValue decodes the blob of a binary field value into one
// value per exported struct member, in layout order. It returns an error
// when the field has no registered layout or the blob is shorter than the
// struct it should contain.
func decodeBinaryFieldValue(fieldID dcgm.Short, blob []byte) ([]binaryMemberValue, error) {
	layout, ok := binaryFieldLayouts[fieldID]
	if !ok {
		return nil, fmt.Errorf("no binary field layout registered for field ID %d", fieldID)
	}

	if len(blob) < layout.size {
		return nil, fmt.Errorf(
			"binary value for field ID %d is %d bytes, shorter than the %d byte struct",
			fieldID, len(blob), layout.size,
		)
	}

	values := make([]binaryMemberValue, 0, len(layout.members))
	for _, member := range layout.members {
		values = append(values, binaryMemberValue{
			suffix: member.suffix,
			value:  binary.LittleEndian.Uint32(blob[member.offset:]),
		})
	}

	return values, nil
}

// appendBinaryFieldMetrics expands one binary field value into one metric
// per struct member and appends the metrics to metrics. It reports whether
// the value was handled; when it returns false the caller falls back to the
// scalar conversion path, which skips the value and logs why.
func (c *DCGMCollector) appendBinaryFieldMetrics(
	metrics MetricsByCounter,
	val dcgm.FieldValue_v1,
	mi devicemonitoring.Info,
	readTimestamp int64,
	source *SourceIdentity,
	selected []counters.Counter,
	labels map[string]string,
) bool {
	if val.Status != dcgm.DCGM_ST_OK {
		return false
	}

	counter, err := findCounterField(selected, val.FieldID)
	if err != nil || counter.IsLabel() {
		return false
	}

	members, err := decodeBinaryFieldValue(val.FieldID, val.Value[:])
	if err != nil {
		return false
	}

	uuid := "UUID"
	if c.useOldNamespace {
		uuid = "uuid"
	}
	gpu := strconv.FormatUint(uint64(mi.DeviceInfo.GPU), 10)
	migProfile := ""
	gpuInstanceID := ""
	computeInstanceID := ""
	if mi.InstanceInfo != nil {
		migProfile = mi.InstanceInfo.ProfileName
		gpuInstanceID = strconv.FormatUint(uint64(mi.InstanceInfo.Info.NvmlInstanceId), 10)
	}
	if mi.ComputeInstanceInfo != nil {
		computeInstanceID = strconv.FormatUint(uint64(mi.ComputeInstanceInfo.InstanceInfo.NvmlComputeInstanceId), 10)
	}

	for _, member := range members {
		memberCounter := counter
		memberCounter.FieldName = counter.FieldName + "_" + member.suffix

		m := Metric{
			Source:    source,
			Counter:   memberCounter,
			Value:     strconv.FormatUint(uint64(member.value), 10),
			Timestamp: metricTimestamp(val.TS, readTimestamp),

			UUID:         uuid,
			GPU:          gpu,
			GPUUUID:      mi.DeviceInfo.UUID,
			GPUDevice:    "nvidia" + gpu,
			GPUModelName: getGPUModel(mi.DeviceInfo, c.replaceBlanksInModelName),
			GPUPCIBusID:  mi.DeviceInfo.PCI.BusID,
			Hostname:     c.hostname,

			Labels:               labels,
			Attributes:           map[string]string{},
			ParentType:           mi.ParentType,
			MigProfile:           migProfile,
			GPUInstanceID:        gpuInstanceID,
			GPUComputeInstanceID: computeInstanceID,
		}

		metrics[m.Counter] = append(metrics[m.Counter], m)
	}

	return true
}
