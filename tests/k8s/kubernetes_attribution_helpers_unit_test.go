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
)

// TestRegularDRAAttributionContract checks that every expected label must occur
// on the same DCGM metric sample.
func TestRegularDRAAttributionContract(t *testing.T) {
	want := regularDRAMetricExpectation{
		Pod:       "cuda-workload",
		Namespace: "dcgm-exporter",
		Container: "cuda-workload",
		regularDRAAllocation: regularDRAAllocation{
			ClaimName:      "cuda-workload-gpu-abcde",
			ClaimNamespace: "dcgm-exporter",
			Driver:         "gpu.nvidia.com",
			Pool:           "node-a",
			Device:         "gpu-0",
		},
	}
	valid := `DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload",dra_claim_name="cuda-workload-gpu-abcde",dra_claim_namespace="dcgm-exporter",dra_driver_name="gpu.nvidia.com",dra_pool_name="node-a",dra_device_name="gpu-0"} 42
`
	// Cases keep each malformed identity and its expected rejection visible.
	tests := []struct {
		name    string
		metrics string
		wantErr bool
	}{
		{name: "complete allocation identity", metrics: valid},
		{
			name:    "DRA sample also carries virtual GPU identity",
			metrics: strings.Replace(valid, `gpu="0",`, `gpu="0",vgpu="7",`, 1),
			wantErr: true,
		},
		{
			name:    "valid sample does not hide contaminated sample",
			metrics: valid + strings.Replace(valid, `gpu="0",`, `gpu="0",vgpu="7",`, 1),
			wantErr: true,
		},
		{
			name:    "missing claim namespace",
			metrics: strings.Replace(valid, `dra_claim_namespace="dcgm-exporter",`, "", 1),
			wantErr: true,
		},
		{
			name: "identity split across samples",
			metrics: `DCGM_FI_DEV_GPU_TEMP_CELSIUS{gpu="0",pod="cuda-workload",namespace="dcgm-exporter",container="cuda-workload",dra_claim_name="cuda-workload-gpu-abcde",dra_claim_namespace="dcgm-exporter",dra_driver_name="gpu.nvidia.com"} 42
DCGM_FI_DEV_BOARD_POWER_WATTS{gpu="0",dra_pool_name="node-a",dra_device_name="gpu-0"} 42
`,
			wantErr: true,
		},
		{
			name:    "different allocation device",
			metrics: strings.Replace(valid, `dra_device_name="gpu-0"`, `dra_device_name="gpu-1"`, 1),
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := regularDRAAttributionContract([]byte(test.metrics), want)
			if test.wantErr && err == nil {
				t.Fatal("regularDRAAttributionContract() error = nil, want error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("regularDRAAttributionContract() error = %v", err)
			}
		})
	}
}

// TestRegularDRAAllocationFromClaim checks valid and malformed live claim allocations.
func TestRegularDRAAllocationFromClaim(t *testing.T) {
	valid := []byte(`{"status":{"allocation":{"devices":{"results":[{"request":"gpu","driver":"gpu.nvidia.com","pool":"node-a","device":"gpu-0"}]}}}}`)
	tests := []struct {
		name    string
		claim   []byte
		wantErr bool
	}{
		{name: "one gpu allocation", claim: valid},
		{
			name:    "multiple allocations",
			claim:   []byte(`{"status":{"allocation":{"devices":{"results":[{"request":"gpu","driver":"gpu.nvidia.com","pool":"node-a","device":"gpu-0"},{"request":"gpu","driver":"gpu.nvidia.com","pool":"node-a","device":"gpu-1"}]}}}}`),
			wantErr: true,
		},
		{
			name:    "wrong request",
			claim:   []byte(strings.Replace(string(valid), `"request":"gpu"`, `"request":"other"`, 1)),
			wantErr: true,
		},
		{
			name:    "missing device",
			claim:   []byte(strings.Replace(string(valid), `"device":"gpu-0"`, `"device":""`, 1)),
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := regularDRAAllocationFromClaim(test.claim, "dcgm-exporter", "cuda-workload-gpu-abcde")
			if test.wantErr {
				if err == nil {
					t.Fatal("regularDRAAllocationFromClaim() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("regularDRAAllocationFromClaim() error = %v", err)
			}
			if got != (regularDRAAllocation{
				ClaimName:      "cuda-workload-gpu-abcde",
				ClaimNamespace: "dcgm-exporter",
				Driver:         "gpu.nvidia.com",
				Pool:           "node-a",
				Device:         "gpu-0",
			}) {
				t.Fatalf("regularDRAAllocationFromClaim() = %#v", got)
			}
		})
	}
}

// TestRegularDRAWorkloadClaimAndNode checks pod scheduling and claim resolution data.
func TestRegularDRAWorkloadClaimAndNode(t *testing.T) {
	valid := []byte(`{"spec":{"nodeName":"gpu-node-a"},"status":{"resourceClaimStatuses":[{"name":"gpu","resourceClaimName":"cuda-workload-gpu-abcde"}]}}`)
	claimName, nodeName, err := regularDRAWorkloadClaimAndNode(valid)
	if err != nil {
		t.Fatalf("regularDRAWorkloadClaimAndNode() error = %v", err)
	}
	if claimName != "cuda-workload-gpu-abcde" || nodeName != "gpu-node-a" {
		t.Fatalf("regularDRAWorkloadClaimAndNode() = (%q, %q), want claim and workload node", claimName, nodeName)
	}

	for _, input := range [][]byte{
		[]byte(`{"spec":{},"status":{"resourceClaimStatuses":[{"name":"gpu","resourceClaimName":"cuda-workload-gpu-abcde"}]}}`),
		[]byte(`{"spec":{"nodeName":"gpu-node-a"},"status":{"resourceClaimStatuses":[]}}`),
		[]byte(`not-json`),
	} {
		if _, _, err := regularDRAWorkloadClaimAndNode(input); err == nil {
			t.Fatalf("regularDRAWorkloadClaimAndNode(%s) error = nil, want error", input)
		}
	}
}
