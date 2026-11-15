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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicemonitoring"
)

// createStatsBlob builds a binary stats blob holding the struct version word
// of a 16 byte version 1 struct followed by its three exported members.
func createStatsBlob(sessionCount, averageFps, averageLatency uint32) [4096]byte {
	blob := [4096]byte{}
	binary.LittleEndian.PutUint32(blob[0:], 16|(1<<24))
	binary.LittleEndian.PutUint32(blob[4:], sessionCount)
	binary.LittleEndian.PutUint32(blob[8:], averageFps)
	binary.LittleEndian.PutUint32(blob[12:], averageLatency)
	return blob
}

// createStatsBlobSlice returns a stats blob as a slice for decoder tests.
func createStatsBlobSlice(sessionCount, averageFps, averageLatency uint32) []byte {
	blob := createStatsBlob(sessionCount, averageFps, averageLatency)
	return blob[:]
}

// TestDecodeBinaryFieldValue decodes encoder and frame buffer capture stats
// blobs and checks the member suffixes and values, and checks that a short
// blob and an unregistered field ID are rejected.
func TestDecodeBinaryFieldValue(t *testing.T) {
	tests := []struct {
		name    string
		fieldID dcgm.Short
		blob    []byte
		want    []binaryMemberValue
		wantErr bool
	}{
		{
			name:    "encoder stats",
			fieldID: dcgm.DCGM_FI_DEV_ENC_STATS,
			blob:    createStatsBlobSlice(2, 30, 5),
			want: []binaryMemberValue{
				{suffix: "SESSION_COUNT", value: 2},
				{suffix: "AVG_FPS", value: 30},
				{suffix: "AVG_LATENCY", value: 5},
			},
		},
		{
			name:    "frame buffer capture stats",
			fieldID: dcgm.DCGM_FI_DEV_FBC_STATS,
			blob:    createStatsBlobSlice(1, 60, 900),
			want: []binaryMemberValue{
				{suffix: "SESSION_COUNT", value: 1},
				{suffix: "AVG_FPS", value: 60},
				{suffix: "AVG_LATENCY", value: 900},
			},
		},
		{
			name:    "blob shorter than the struct",
			fieldID: dcgm.DCGM_FI_DEV_ENC_STATS,
			blob:    createStatsBlobSlice(2, 30, 5)[:8],
			wantErr: true,
		},
		{
			name:    "field without a registered layout",
			fieldID: dcgm.DCGM_FI_DEV_GPU_TEMP,
			blob:    createStatsBlobSlice(2, 30, 5),
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values, err := decodeBinaryFieldValue(test.fieldID, test.blob)

			if test.wantErr {
				require.Error(t, err)
				assert.Nil(t, values)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, values)
		})
	}
}

// TestToMetricWithBinaryStatsFields converts encoder and frame buffer
// capture stats samples and checks that each sample becomes one metric per
// struct member, named after the field and member, with the member value
// and the GPU identity of the scalar path.
func TestToMetricWithBinaryStatsFields(t *testing.T) {
	mi := devicemonitoring.Info{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		DeviceInfo: dcgm.Device{
			GPU:  0,
			UUID: "fake0",
			Identifiers: dcgm.DeviceIdentifiers{
				Model: "NVIDIA T400 4GB",
			},
			PCI: dcgm.PCIInfo{
				BusID: "00000000:0000:0000.0",
			},
		},
	}
	c := []counters.Counter{
		{
			FieldID:   dcgm.DCGM_FI_DEV_ENC_STATS,
			FieldName: "DCGM_FI_DEV_ENC_STATS",
			PromType:  "gauge",
			Help:      "Encoder stats help info",
		},
		{
			FieldID:   dcgm.DCGM_FI_DEV_FBC_STATS,
			FieldName: "DCGM_FI_DEV_FBC_STATS",
			PromType:  "gauge",
			Help:      "Frame buffer capture stats help info",
		},
	}
	values := []dcgm.FieldValue_v1{
		{
			FieldID:   dcgm.DCGM_FI_DEV_ENC_STATS,
			FieldType: dcgm.DCGM_FT_BINARY,
			Status:    dcgm.DCGM_ST_OK,
			Value:     createStatsBlob(2, 30, 5),
		},
		{
			FieldID:   dcgm.DCGM_FI_DEV_FBC_STATS,
			FieldType: dcgm.DCGM_FT_BINARY,
			Status:    dcgm.DCGM_ST_OK,
			Value:     createStatsBlob(1, 60, 900),
		},
	}

	metrics := MetricsByCounter{}
	collector := &DCGMCollector{counters: c, hostname: "host-a"}
	collector.toMetric(metrics, values, mi, 0, nil)

	want := map[string]string{
		"DCGM_FI_DEV_ENC_STATS_SESSION_COUNT": "2",
		"DCGM_FI_DEV_ENC_STATS_AVG_FPS":       "30",
		"DCGM_FI_DEV_ENC_STATS_AVG_LATENCY":   "5",
		"DCGM_FI_DEV_FBC_STATS_SESSION_COUNT": "1",
		"DCGM_FI_DEV_FBC_STATS_AVG_FPS":       "60",
		"DCGM_FI_DEV_FBC_STATS_AVG_LATENCY":   "900",
	}

	require.Len(t, metrics, len(want))
	for counter, memberMetrics := range metrics {
		require.Len(t, memberMetrics, 1)
		assert.Equal(t, want[counter.FieldName], memberMetrics[0].Value)
		assert.Equal(t, mi.DeviceInfo.UUID, memberMetrics[0].GPUUUID)
		assert.Equal(t, "gauge", counter.PromType)
	}
}
