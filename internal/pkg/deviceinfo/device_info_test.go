/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package deviceinfo

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

var fakeProfileName = "2fake.4gb"

func SpoofDeviceInfo() Info {
	var deviceInfo Info
	deviceInfo.gpuCount = 2
	deviceInfo.gpus[0].DeviceInfo.GPU = 0
	gi := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    0,
	}
	deviceInfo.gpus[0].GPUInstances = append(deviceInfo.gpus[0].GPUInstances, gi)
	gi2 := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlInstanceId: 1, NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    14,
	}
	deviceInfo.gpus[1].GPUInstances = append(deviceInfo.gpus[1].GPUInstances, gi2)
	deviceInfo.gpus[1].DeviceInfo.GPU = 1

	return deviceInfo
}

func TestVerifyDevicePresence(t *testing.T) {
	deviceInfo := SpoofDeviceInfo()
	var dOpt appconfig.DeviceOptions
	dOpt.Flex = true
	err := deviceInfo.VerifyDevicePresence(dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	dOpt.Flex = false
	dOpt.MajorRange = append(dOpt.MajorRange, -1)
	dOpt.MinorRange = append(dOpt.MinorRange, -1)
	err = deviceInfo.VerifyDevicePresence(dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	dOpt.MinorRange[0] = 10 // this GPU instance doesn't exist
	err = deviceInfo.VerifyDevicePresence(dOpt)
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU instance, but none found")

	dOpt.MajorRange[0] = 10 // this GPU doesn't exist
	dOpt.MinorRange[0] = -1
	err = deviceInfo.VerifyDevicePresence(dOpt)
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU, but none found")

	// Add gpus and instances that exist
	dOpt.MajorRange[0] = 0
	dOpt.MajorRange = append(dOpt.MajorRange, 1)
	dOpt.MinorRange[0] = 0
	dOpt.MinorRange = append(dOpt.MinorRange, 14)
	err = deviceInfo.VerifyDevicePresence(dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)
}

func TestIsSwitchWatched(t *testing.T) {
	tests := []struct {
		name       string
		switchID   uint
		deviceInfo Info
		want       bool
	}{
		{
			name:     "Monitor all devices",
			switchID: 1,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					Flex: true,
				},
			},
			want: true,
		},
		{
			name:     "MajorRange empty",
			switchID: 2,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{},
				},
			},
			want: false,
		},
		{
			name:     "MajorRange contains -1 to watch all devices",
			switchID: 3,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
				},
			},
			want: true,
		},
		{
			name:     "SwitchID in MajorRange",
			switchID: 4,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{3, 4, 5},
				},
			},
			want: true,
		},
		{
			name:     "SwitchID not in MajorRange",
			switchID: 5,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{3, 4, 6},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.deviceInfo.IsSwitchWatched(tt.switchID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsLinkWatched(t *testing.T) {
	tests := []struct {
		name       string
		linkIndex  uint
		switchID   uint
		deviceInfo Info
		want       bool
	}{
		{
			name:       "Monitor all devices",
			linkIndex:  1,
			deviceInfo: Info{sOpt: appconfig.DeviceOptions{Flex: true}},
			want:       true,
		},
		{
			name:       "No watched devices",
			linkIndex:  1,
			deviceInfo: Info{},
			want:       false,
		},
		{
			name:      "Watched link with empty MinorRange",
			linkIndex: 2,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
				},
				switches: []SwitchInfo{
					{
						EntityId: 1,
						NvLinks: []dcgm.NvLinkStatus{
							{Index: 2},
						},
					},
				},
			},
			want: false,
		},
		{
			name:      "MinorRange contains -1 to watch all links",
			switchID:  1,
			linkIndex: 3,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
					MinorRange: []int{-1},
				},
				switches: []SwitchInfo{
					{
						EntityId: 1,
						NvLinks: []dcgm.NvLinkStatus{
							{Index: 3},
						},
					},
				},
			},
			want: true,
		},
		{
			name:      "The link not in the watched switch",
			switchID:  1,
			linkIndex: 4,
			deviceInfo: Info{
				sOpt: appconfig.DeviceOptions{
					MajorRange: []int{-1},
					MinorRange: []int{1, 2, 3},
				},
				switches: []SwitchInfo{
					{
						EntityId: 1,
						NvLinks: []dcgm.NvLinkStatus{
							{Index: 4},
						},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.deviceInfo.IsLinkWatched(tt.linkIndex, tt.switchID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCPUWatched(t *testing.T) {
	tests := []struct {
		name       string
		cpuID      uint
		deviceInfo Info
		want       bool
	}{
		{
			name:  "Monitor all devices",
			cpuID: 1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{Flex: true},
				cpus: []CPUInfo{
					{
						EntityId: 1,
					},
				},
			},
			want: true,
		},
		{
			name:  "MajorRange Contains -1",
			cpuID: 2,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{-1}},
				cpus: []CPUInfo{
					{
						EntityId: 2,
					},
				},
			},
			want: true,
		},
		{
			name:  "CPU ID in MajorRange",
			cpuID: 3,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{1, 2, 3}},
				cpus: []CPUInfo{
					{
						EntityId: 3,
					},
				},
			},
			want: true,
		},
		{
			name:  "CPU ID Not in MajorRange",
			cpuID: 4,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{1, 2, 3}},
				cpus: []CPUInfo{
					{
						EntityId: 4,
					},
				},
			},
			want: false,
		},
		{
			name:  "MajorRange Empty",
			cpuID: 5,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{}},
				cpus: []CPUInfo{
					{
						EntityId: 5,
					},
				},
			},
			want: false,
		},
		{
			name:  "CPU not found",
			cpuID: 6,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{MajorRange: []int{}},
				cpus: []CPUInfo{
					{
						EntityId: 5,
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.deviceInfo.IsCPUWatched(tt.cpuID))
		})
	}
}

func TestIsCoreWatched(t *testing.T) {
	tests := []struct {
		name       string
		coreID     uint
		cpuID      uint
		deviceInfo Info
		want       bool
	}{
		{
			name:   "Monitor all devices",
			coreID: 1,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{Flex: true},
			},
			want: true,
		},
		{
			name:   "Core in MinorRange",
			coreID: 2,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: true,
		},
		{
			name:   "Core Not in MinorRange",
			coreID: 4,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: false,
		},
		{
			name:   "MinorRange Contains -1",
			coreID: 5,
			cpuID:  1,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{-1},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: true,
		},
		{
			name:   "CPU Not Found",
			coreID: 1,
			cpuID:  2,
			deviceInfo: Info{
				cOpt: appconfig.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				cpus: []CPUInfo{{EntityId: 1}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.deviceInfo.IsCoreWatched(tt.coreID, tt.cpuID))
		})
	}
}

func TestSetMigProfileNames(t *testing.T) {
	config := &appconfig.Config{
		UseRemoteHE: false,
	}
	dcgmprovider.Initialize(config)
	defer dcgmprovider.Client().Cleanup()

	tests := []struct {
		name       string
		deviceInfo Info
		values     []dcgm.FieldValue_v2
		valid      bool
	}{
		{
			name: "MIG profile found",
			deviceInfo: Info{
				gpuCount: 1,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityId:    1,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: true,
		},
		{
			name: "Multiple MIG gpus",
			deviceInfo: Info{
				gpuCount: 3,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 2},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 3},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityId:    2,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: true,
		},
		{
			name: "Multiple MIG gpus and Values",
			deviceInfo: Info{
				gpuCount: 3,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 2},
						},
					},
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 3},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityId:    2,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
				{
					EntityId:    3,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: true,
		},
		{
			name: "MIG profile not found",
			deviceInfo: Info{
				gpuCount: 1,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityId:    2,
					FieldType:   dcgm.DCGM_FT_STRING,
					StringValue: &fakeProfileName,
				},
			},
			valid: false,
		},
		{
			name: "MIG profile not string type",
			deviceInfo: Info{
				gpuCount: 1,
				gpus: [dcgm.MAX_NUM_DEVICES]GPUInfo{
					{
						GPUInstances: []GPUInstanceInfo{
							{EntityId: 1},
						},
					},
				},
			},
			values: []dcgm.FieldValue_v2{
				{
					EntityId:    1,
					FieldType:   dcgm.DCGM_FT_BINARY,
					StringValue: &fakeProfileName,
					Value:       [4096]byte{'1', '2', '3'},
				},
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.valid {
				assert.NoError(t, tt.deviceInfo.SetMigProfileNames(tt.values), "Expected no error.")
			} else {
				assert.Error(t, tt.deviceInfo.SetMigProfileNames(tt.values), "Expected an error.")
			}
		})
	}
}
