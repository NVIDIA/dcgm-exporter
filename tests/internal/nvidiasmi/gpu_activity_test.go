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
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// TestParseGPUUtilization covers reported values, placeholders and malformed rows.
func TestParseGPUUtilization(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []nvidiasmi.GPUUtilization
		wantErr string
	}{
		{
			name:   "two GPUs",
			output: "0, GPU-aaa, 0\n1, GPU-bbb, 99\n",
			want: []nvidiasmi.GPUUtilization{
				{Index: 0, UUID: "GPU-aaa", UtilizationPercent: reading(0, "0")},
				{Index: 1, UUID: "GPU-bbb", UtilizationPercent: reading(99, "99")},
			},
		},
		{
			name:   "placeholder utilization",
			output: "0, GPU-aaa, [N/A]\n",
			want:   []nvidiasmi.GPUUtilization{{Index: 0, UUID: "GPU-aaa", UtilizationPercent: nvidiasmi.Reading{Raw: "[N/A]"}}},
		},
		{name: "no rows", output: "", wantErr: "listed no GPUs"},
		{name: "index not a number", output: "x, GPU-aaa, 0\n", wantErr: `index "x" is not an integer`},
		{name: "missing UUID", output: "0, [N/A], 0\n", wantErr: "has no UUID"},
		{name: "utilization not a number", output: "0, GPU-aaa, busy\n", wantErr: `value "busy" is not a number`},
		{name: "wrong column count", output: "0, GPU-aaa\n", wantErr: "wrong number of fields"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nvidiasmi.ParseGPUUtilization([]byte(tc.output))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestParseComputeAppUUIDs checks that UUIDs are read once each and other lines are ignored.
func TestParseComputeAppUUIDs(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{name: "no processes", output: "", want: nil},
		{name: "one GPU twice", output: "GPU-a\nGPU-a\n GPU-b \n", want: []string{"GPU-a", "GPU-b"}},
		{name: "message", output: "No running processes found\n", want: nil},
		{name: "MIG device", output: "MIG-1111\nGPU-a\nMIG-1111\n", want: []string{"MIG-1111", "GPU-a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nvidiasmi.ParseComputeAppUUIDs([]byte(tc.output)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestParseCUDAVersion reads the version from the summary header and rejects a summary without one.
func TestParseCUDAVersion(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantMajor int
		wantMinor int
		wantErr   bool
	}{
		{name: "CUDA 13", output: "| NVIDIA-SMI 620.64   Driver Version: 620.64   CUDA Version: 13.1     |", wantMajor: 13, wantMinor: 1},
		{name: "CUDA 12", output: "Driver Version: 550.54.15      CUDA Version: 12.4", wantMajor: 12, wantMinor: 4},
		{
			name:      "UMD version from driver 620.64",
			output:    "| NVIDIA-SMI 620.64                 KMD Version: 620.64        CUDA UMD Version: 13.5     |",
			wantMajor: 13, wantMinor: 5,
		},
		{name: "no version", output: "No devices were found", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			major, minor, err := nvidiasmi.ParseCUDAVersion([]byte(tc.output))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %d.%d, want an error", major, minor)
				}
				return
			}

			if err != nil || major != tc.wantMajor || minor != tc.wantMinor {
				t.Fatalf("got %d.%d, %v; want %d.%d", major, minor, err, tc.wantMajor, tc.wantMinor)
			}
		})
	}
}

// TestQueriesPassTheirArguments checks the nvidia-smi arguments each query uses.
func TestQueriesPassTheirArguments(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte("0, GPU-a, 0\nCUDA Version: 13.0\n"), nil
	}

	_, err := nvidiasmi.QueryGPUUtilization(context.Background(), func(ctx context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte("0, GPU-a, 0\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nvidiasmi.QueryComputeAppUUIDs(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, _, err := nvidiasmi.QueryCUDAVersion(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"--query-gpu=index,uuid,utilization.gpu", "--format=csv,noheader,nounits"},
		{"--query-compute-apps=gpu_uuid", "--format=csv,noheader"},
		nil,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}
