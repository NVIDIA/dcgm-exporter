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

package k8s

import (
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// TestNVLinkFabricContract matches nvidia-smi active links with exporter
// per-link samples by GPU UUID and link index.
func TestNVLinkFabricContract(t *testing.T) {
	const (
		uuid0 = "GPU-0b3c8a5e-1d2f-4a6b-9c7d-8e9f0a1b2c3d"
		uuid1 = "GPU-1c4d9b6f-2e3a-4b7c-8d9e-9f0a1b2c3d4e"
	)
	active := []nvidiasmi.NVLink{
		{GPU: 0, GPUUUID: uuid0, Link: 0},
		{GPU: 0, GPUUUID: uuid0, Link: 1},
		{GPU: 1, GPUUUID: uuid1, Link: 0},
	}
	header := "# TYPE DCGM_FI_PROF_NVLINK_TX_BYTES gauge\n"
	allLinks := header +
		`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",gpu_uuid="` + uuid0 + `",nvlink="0"} 12` + "\n" +
		`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",gpu_uuid="` + uuid0 + `",nvlink="1"} 12` + "\n" +
		`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="1",gpu_uuid="` + uuid1 + `",nvlink="0"} 12` + "\n"

	tests := []struct {
		name    string
		metrics string
		active  []nvidiasmi.NVLink
		wantErr []string
	}{
		{name: "every active link reported", metrics: allLinks, active: active},
		{
			name: "links reported by different families",
			metrics: header +
				`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",gpu_uuid="` + uuid0 + `",nvlink="0"} 12` + "\n" +
				"# TYPE DCGM_FI_DEV_NVLINK_RX_BYTES_TOTAL counter\n" +
				`DCGM_FI_DEV_NVLINK_RX_BYTES_TOTAL{gpu="0",gpu_uuid="` + uuid0 + `",nvlink="1"} 5` + "\n" +
				`DCGM_FI_DEV_NVLINK_RX_BYTES_TOTAL{gpu="1",gpu_uuid="` + uuid1 + `",nvlink="0"} 5` + "\n",
			active: active,
		},
		{
			name: "missing link names the GPU and link",
			metrics: header +
				`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",gpu_uuid="` + uuid0 + `",nvlink="0"} 12` + "\n" +
				`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="1",gpu_uuid="` + uuid1 + `",nvlink="0"} 12` + "\n",
			active:  active,
			wantErr: []string{"GPU 0 (" + uuid0 + ") link 1", `nvlink="1"`},
		},
		{
			name: "same link index on another GPU does not count",
			metrics: header +
				`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",gpu_uuid="` + uuid0 + `",nvlink="0"} 12` + "\n" +
				`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",gpu_uuid="` + uuid0 + `",nvlink="1"} 12` + "\n",
			active:  active,
			wantErr: []string{"GPU 1 (" + uuid1 + ") link 0"},
		},
		{
			name: "GPU-level sample without link label does not count",
			metrics: header +
				`DCGM_FI_PROF_NVLINK_TX_BYTES{gpu="0",UUID="` + uuid0 + `"} 12` + "\n",
			active:  active[:1],
			wantErr: []string{"GPU 0 (" + uuid0 + ") link 0"},
		},
		{
			name:    "no active links",
			metrics: allLinks,
			wantErr: []string{"reported no active NVLink"},
		},
		{
			name: "sample checks still apply",
			metrics: allLinks +
				"# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge\n" +
				`DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0"} 40` + "\n",
			active:  active,
			wantErr: []string{"without GPU UUID"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireContractError(t, nvlinkFabricContract([]byte(test.metrics), test.active), test.wantErr)
		})
	}
}

// TestNVLinkFabricCustomMetricsCollectEveryLinkFamily keeps the deployment's
// CSV rows and the families the contract reads in step.
func TestNVLinkFabricCustomMetricsCollectEveryLinkFamily(t *testing.T) {
	collected := map[string]bool{}
	for _, row := range nvlinkFabricCustomMetrics() {
		field, _, _ := strings.Cut(row, ",")
		collected[strings.TrimSpace(field)] = true
	}

	for _, family := range nvlinkFabricLinkFamilies {
		if !collected[family] {
			t.Errorf("nvlinkFabricCustomMetrics() has no row for %s", family)
		}
	}
}
