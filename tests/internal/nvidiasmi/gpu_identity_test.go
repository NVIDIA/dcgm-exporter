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
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// TestParseGPUIdentity covers identity values, placeholders and malformed rows.
func TestParseGPUIdentity(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []nvidiasmi.GPU
		wantErr string
	}{
		{
			name: "two GPUs",
			output: "0, GPU-aaa, NVIDIA B200, 00000000:1B:00.0, 580.82.07, [N/A]\n" +
				"1, GPU-bbb, NVIDIA B200, 00000000:43:00.0, 580.82.07, Enabled\n",
			want: []nvidiasmi.GPU{
				{Index: 0, UUID: "GPU-aaa", Name: "NVIDIA B200", PCIBusID: "00000000:1B:00.0", DriverVersion: "580.82.07"},
				{Index: 1, UUID: "GPU-bbb", Name: "NVIDIA B200", PCIBusID: "00000000:43:00.0", DriverVersion: "580.82.07", MIGEnabled: true},
			},
		},
		{
			name:   "text placeholder becomes empty",
			output: "0, GPU-aaa, [N/A], [N/A], 580.82.07, N/A\n",
			want:   []nvidiasmi.GPU{{Index: 0, UUID: "GPU-aaa", DriverVersion: "580.82.07"}},
		},
		{name: "empty output", output: "", wantErr: "listed no GPUs"},
		{name: "wrong column count", output: "0, GPU-aaa, NVIDIA B200\n", wantErr: "wrong number of fields"},
		{name: "index is not a number", output: "x, GPU-aaa, B200, 0:1:0.0, 1, Disabled\n", wantErr: "index \"x\""},
		{name: "missing UUID", output: "0, [N/A], B200, 0:1:0.0, 1, Disabled\n", wantErr: "has no UUID"},
		{name: "unknown MIG mode", output: "0, GPU-aaa, B200, 0:1:0.0, 1, Pending\n", wantErr: "mig.mode.current"},
		{
			name:    "duplicate UUID",
			output:  "0, GPU-aaa, B200, 0:1:0.0, 1, Disabled\n1, GPU-aaa, B200, 0:2:0.0, 1, Disabled\n",
			wantErr: "listed twice",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nvidiasmi.ParseGPUIdentity([]byte(tc.output))

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseGPUIdentity() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGPUIdentity() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseGPUIdentity() =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// TestQueryGPUIdentityUsesRunner passes the query and a deadline to the runner and wraps its errors.
func TestQueryGPUIdentityUsesRunner(t *testing.T) {
	var gotArgs []string
	hadDeadline := false
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		_, hadDeadline = ctx.Deadline()
		return []byte("0, GPU-aaa, B200, 0:1:0.0, 1, Disabled\n"), nil
	}

	gpus, err := nvidiasmi.QueryGPUIdentity(context.Background(), run)
	if err != nil || len(gpus) != 1 || gpus[0].UUID != "GPU-aaa" {
		t.Fatalf("QueryGPUIdentity() = %+v, %v", gpus, err)
	}
	wantArgs := []string{
		"--query-gpu=index,uuid,name,pci.bus_id,driver_version,mig.mode.current",
		"--format=csv,noheader,nounits",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("runner args = %q, want %q", gotArgs, wantArgs)
	}
	if !hadDeadline {
		t.Fatal("QueryGPUIdentity() should give the runner a deadline")
	}

	failing := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("not found") }
	if _, err := nvidiasmi.QueryGPUIdentity(context.Background(), failing); err == nil ||
		!strings.Contains(err.Error(), "query GPU identity with nvidia-smi: not found") {
		t.Fatalf("QueryGPUIdentity() error = %v, want wrapped runner error", err)
	}
}
