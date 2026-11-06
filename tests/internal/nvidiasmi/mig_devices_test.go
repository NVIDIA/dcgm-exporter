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

// migPodReport is a trimmed `nvidia-smi -q -x` report from inside a pod that
// holds one MIG device.
const migPodReport = `<?xml version="1.0" ?>
<!DOCTYPE nvidia_smi_log SYSTEM "nvsmi_device_v12.dtd">
<nvidia_smi_log>
	<driver_version>580.82.07</driver_version>
	<attached_gpus>1</attached_gpus>
	<gpu id="00000008:01:00.0">
		<product_name>NVIDIA GB200</product_name>
		<uuid>GPU-aaa</uuid>
		<mig_mode>
			<current_mig>Enabled</current_mig>
			<pending_mig>Enabled</pending_mig>
		</mig_mode>
		<mig_devices>
			<mig_device>
				<index>0</index>
				<gpu_instance_id>3</gpu_instance_id>
				<compute_instance_id>0</compute_instance_id>
			</mig_device>
		</mig_devices>
	</gpu>
</nvidia_smi_log>
`

// TestParseMIGDevices covers MIG devices, GPUs without MIG devices and
// malformed reports.
func TestParseMIGDevices(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []nvidiasmi.MIGDevice
		wantErr string
	}{
		{
			name:   "one MIG device",
			output: migPodReport,
			want:   []nvidiasmi.MIGDevice{{GPUUUID: "GPU-aaa", GPUInstanceID: "3"}},
		},
		{
			name:   "a GPU with MIG off",
			output: `<nvidia_smi_log><gpu><uuid>GPU-bbb</uuid><mig_devices>None</mig_devices></gpu></nvidia_smi_log>`,
		},
		{
			name:    "a MIG device without a GPU instance ID",
			output:  `<nvidia_smi_log><gpu><uuid>GPU-aaa</uuid><mig_devices><mig_device><index>0</index></mig_device></mig_devices></gpu></nvidia_smi_log>`,
			wantErr: "MIG device without a GPU UUID or GPU instance ID",
		},
		{
			name:    "not XML",
			output:  "No devices were found",
			wantErr: "read XML report",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nvidiasmi.ParseMIGDevices([]byte(tc.output))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseMIGDevices() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseMIGDevices() error = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseMIGDevices() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestQueryMIGDevices checks the arguments the query passes and that a runner
// error is returned with context.
func TestQueryMIGDevices(t *testing.T) {
	var gotArgs []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(migPodReport), nil
	}

	devices, err := nvidiasmi.QueryMIGDevices(context.Background(), run)
	if err != nil {
		t.Fatalf("QueryMIGDevices() error = %v", err)
	}
	if !reflect.DeepEqual(gotArgs, []string{"-q", "-x"}) {
		t.Fatalf("QueryMIGDevices() args = %v, want [-q -x]", gotArgs)
	}
	if len(devices) != 1 {
		t.Fatalf("QueryMIGDevices() devices = %#v, want one", devices)
	}

	failing := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("exec failed") }
	_, err = nvidiasmi.QueryMIGDevices(context.Background(), failing)
	if err == nil || !strings.Contains(err.Error(), "query MIG devices with nvidia-smi: exec failed") {
		t.Fatalf("QueryMIGDevices() error = %v", err)
	}
}
