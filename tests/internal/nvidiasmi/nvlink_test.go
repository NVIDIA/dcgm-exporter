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

package nvidiasmi_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// TestActiveNVLinks parses nvidia-smi nvlink -s output into the active links
// of each GPU, and rejects output that cannot name a link's GPU.
func TestActiveNVLinks(t *testing.T) {
	const (
		uuid0 = "GPU-0b3c8a5e-1d2f-4a6b-9c7d-8e9f0a1b2c3d"
		uuid1 = "GPU-1c4d9b6f-2e3a-4b7c-8d9e-9f0a1b2c3d4e"
	)
	tests := []struct {
		name    string
		output  string
		want    []nvidiasmi.NVLink
		wantErr string
	}{
		{
			name: "two GPUs with active links",
			output: "GPU 0: NVIDIA H100 80GB HBM3 (UUID: " + uuid0 + ")\n" +
				"\t Link 0: 26.562 GB/s\n" +
				"\t Link 1: 26.562 GB/s\n" +
				"GPU 1: NVIDIA H100 80GB HBM3 (UUID: " + uuid1 + ")\n" +
				"\t Link 0: 26.562 GB/s\n",
			want: []nvidiasmi.NVLink{
				{GPU: 0, GPUUUID: uuid0, Link: 0},
				{GPU: 0, GPUUUID: uuid0, Link: 1},
				{GPU: 1, GPUUUID: uuid1, Link: 0},
			},
		},
		{
			name: "whole-number speed",
			output: "GPU 3: NVIDIA B200 (UUID: " + uuid0 + ")\n" +
				"\t Link 17: 50 GB/s\n",
			want: []nvidiasmi.NVLink{{GPU: 3, GPUUUID: uuid0, Link: 17}},
		},
		{
			name: "inactive and zero-speed links are left out",
			output: "GPU 0: NVIDIA A100-SXM4-80GB (UUID: " + uuid0 + ")\n" +
				"\t Link 0: <inactive>\n" +
				"\t Link 1: 0 GB/s\n" +
				"\t Link 2: 0.000 GB/s\n" +
				"\t Link 3: 25 GB/s\n",
			want: []nvidiasmi.NVLink{{GPU: 0, GPUUUID: uuid0, Link: 3}},
		},
		{
			name: "GPU with no active links",
			output: "GPU 0: NVIDIA GeForce RTX 4090 (UUID: " + uuid0 + ")\n" +
				"GPU 1: NVIDIA H100 80GB HBM3 (UUID: " + uuid1 + ")\n" +
				"\t Link 4: 26.562 GB/s\n",
			want: []nvidiasmi.NVLink{{GPU: 1, GPUUUID: uuid1, Link: 4}},
		},
		{
			name:   "no GPU lines",
			output: "NVML: Unable to retrieve NVLink information as all links are inActive\n",
		},
		{
			name:    "link before GPU line",
			output:  "\t Link 0: 26.562 GB/s\n",
			wantErr: "before any GPU line",
		},
		{
			name:    "GPU line without UUID",
			output:  "GPU 0: NVIDIA H100 80GB HBM3\n\t Link 0: 26.562 GB/s\n",
			wantErr: "no index and UUID",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := nvidiasmi.ActiveNVLinks(test.output)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ActiveNVLinks() error = %v, want it to contain %q", err, test.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("ActiveNVLinks() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ActiveNVLinks() = %#v, want %#v", got, test.want)
			}
		})
	}
}
