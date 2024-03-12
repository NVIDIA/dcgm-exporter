/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package dcgm_client

import (
	"fmt"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/common"
)

var fakeProfileName = "2fake.4gb"

func SpoofSwitchSystemInfo() SystemInfo {
	var sysInfo SystemInfo
	sysInfo.InfoType = dcgm.FE_SWITCH
	sw1 := SwitchInfo{
		EntityId: 0,
	}
	sw2 := SwitchInfo{
		EntityId: 1,
	}

	l1 := dcgm.NvLinkStatus{
		ParentId:   0,
		ParentType: dcgm.FE_SWITCH,
		State:      2,
		Index:      0,
	}

	l2 := dcgm.NvLinkStatus{
		ParentId:   0,
		ParentType: dcgm.FE_SWITCH,
		State:      3,
		Index:      1,
	}

	l3 := dcgm.NvLinkStatus{
		ParentId:   1,
		ParentType: dcgm.FE_SWITCH,
		State:      2,
		Index:      0,
	}

	l4 := dcgm.NvLinkStatus{
		ParentId:   1,
		ParentType: dcgm.FE_SWITCH,
		State:      3,
		Index:      1,
	}

	sw1.NvLinks = append(sw1.NvLinks, l1)
	sw1.NvLinks = append(sw1.NvLinks, l2)
	sw2.NvLinks = append(sw2.NvLinks, l3)
	sw2.NvLinks = append(sw2.NvLinks, l4)

	sysInfo.Switches = append(sysInfo.Switches, sw1)
	sysInfo.Switches = append(sysInfo.Switches, sw2)

	sysInfo.SOpt.MajorRange = []int{-1}
	sysInfo.SOpt.MinorRange = []int{-1}

	return sysInfo
}

func SpoofSystemInfo() SystemInfo {
	var sysInfo SystemInfo
	sysInfo.GPUCount = 2
	sysInfo.GPUs[0].DeviceInfo.GPU = 0
	gi := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    0,
	}
	sysInfo.GPUs[0].GPUInstances = append(sysInfo.GPUs[0].GPUInstances, gi)
	gi2 := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlInstanceId: 1, NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    14,
	}
	sysInfo.GPUs[1].GPUInstances = append(sysInfo.GPUs[1].GPUInstances, gi2)
	sysInfo.GPUs[1].DeviceInfo.GPU = 1

	return sysInfo
}

func TestMonitoredEntities(t *testing.T) {
	sysInfo := SpoofSystemInfo()
	sysInfo.GOpt.Flex = true

	monitoring := GetMonitoredEntities(sysInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored entities but found %d", len(monitoring)))
	instanceCount := 0
	gpuCount := 0
	for _, mi := range monitoring {
		if mi.Entity.EntityGroupId == dcgm.FE_GPU_I {
			instanceCount = instanceCount + 1
			require.NotEqual(t, mi.InstanceInfo, nil, "Expected InstanceInfo to be populated but it wasn't")
			require.Equal(t, mi.InstanceInfo.ProfileName, fakeProfileName, "Expected profile named '%s' but found '%s'",
				fakeProfileName, mi.InstanceInfo.ProfileName)
			if mi.Entity.EntityId != uint(0) {
				// One of these should be 0, the other should be 14
				require.Equal(t, mi.Entity.EntityId, uint(14), "Expected 14 as EntityId but found %s",
					monitoring[1].Entity.EntityId)
			}
		} else {
			gpuCount = gpuCount + 1
			require.Equal(t, mi.InstanceInfo, (*GPUInstanceInfo)(nil),
				"Expected InstanceInfo to be nil but it wasn't")
		}
	}
	require.Equal(t, instanceCount, 2, "Expected 2 GPU instances but found %d", instanceCount)
	require.Equal(t, gpuCount, 0, "Expected 0 GPUs but found %d", gpuCount)

	sysInfo.GPUs[0].GPUInstances = sysInfo.GPUs[0].GPUInstances[:0]
	sysInfo.GPUs[1].GPUInstances = sysInfo.GPUs[1].GPUInstances[:0]
	monitoring = GetMonitoredEntities(sysInfo)
	require.Equal(t, 2, len(monitoring), fmt.Sprintf("Should have 2 monitored entities but found %d", len(monitoring)))
	for i, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_GPU, "Expected FE_GPU but found %d", mi.Entity.EntityGroupId)
		require.Equal(t, uint(i), mi.DeviceInfo.GPU, "Expected GPU %d but found %d", i, mi.DeviceInfo.GPU)
		require.Equal(t, (*GPUInstanceInfo)(nil), mi.InstanceInfo,
			"Expected InstanceInfo not to be populated but it was")
	}
}

func TestVerifyDevicePresence(t *testing.T) {
	sysInfo := SpoofSystemInfo()
	var dOpt common.DeviceOptions
	dOpt.Flex = true
	err := VerifyDevicePresence(&sysInfo, dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	dOpt.Flex = false
	dOpt.MajorRange = append(dOpt.MajorRange, -1)
	dOpt.MinorRange = append(dOpt.MinorRange, -1)
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	dOpt.MinorRange[0] = 10 // this GPU instance doesn't exist
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU instance, but none found")

	dOpt.MajorRange[0] = 10 // this GPU doesn't exist
	dOpt.MinorRange[0] = -1
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU, but none found")

	// Add GPUs and instances that exist
	dOpt.MajorRange[0] = 0
	dOpt.MajorRange = append(dOpt.MajorRange, 1)
	dOpt.MinorRange[0] = 0
	dOpt.MinorRange = append(dOpt.MinorRange, 14)
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)
}

func TestMonitoredSwitches(t *testing.T) {
	sysInfo := SpoofSwitchSystemInfo()

	/* test that only switches are returned */
	monitoring := GetMonitoredEntities(sysInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored switches but found %d", len(monitoring)))
	for _, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_SWITCH,
			fmt.Sprintf("Should have only returned switches but returned %d", mi.Entity.EntityGroupId))
	}

	/* test that only "up" links are monitored and 1 from each switch */
	sysInfo.InfoType = dcgm.FE_LINK
	monitoring = GetMonitoredEntities(sysInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored links but found %d", len(monitoring)))
	for i, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_LINK,
			fmt.Sprintf("Should have only returned links but returned %d", mi.Entity.EntityGroupId))
		require.Equal(t, mi.ParentId, uint(i), "Link should reference switch parent")
	}
}

func TestIsSwitchWatched(t *testing.T) {
	tests := []struct {
		name     string
		switchID uint
		sysInfo  SystemInfo
		want     bool
	}{
		{
			name:     "Monitor all devices",
			switchID: 1,
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					Flex: true,
				},
			},
			want: true,
		},
		{
			name:     "MajorRange empty",
			switchID: 2,
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					MajorRange: []int{},
				},
			},
			want: false,
		},
		{
			name:     "MajorRange contains -1 to watch all devices",
			switchID: 3,
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					MajorRange: []int{-1},
				},
			},
			want: true,
		},
		{
			name:     "SwitchID in MajorRange",
			switchID: 4,
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					MajorRange: []int{3, 4, 5},
				},
			},
			want: true,
		},
		{
			name:     "SwitchID not in MajorRange",
			switchID: 5,
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					MajorRange: []int{3, 4, 6},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSwitchWatched(tt.switchID, tt.sysInfo)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsLinkWatched(t *testing.T) {
	tests := []struct {
		name      string
		linkIndex uint
		switchID  uint
		sysInfo   SystemInfo
		want      bool
	}{
		{
			name:      "Monitor all devices",
			linkIndex: 1,
			sysInfo:   SystemInfo{SOpt: common.DeviceOptions{Flex: true}},
			want:      true,
		},
		{
			name:      "No watched devices",
			linkIndex: 1,
			sysInfo:   SystemInfo{},
			want:      false,
		},
		{
			name:      "Watched link with empty MinorRange",
			linkIndex: 2,
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					MajorRange: []int{-1},
				},
				Switches: []SwitchInfo{
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
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					MajorRange: []int{-1},
					MinorRange: []int{-1},
				},
				Switches: []SwitchInfo{
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
			sysInfo: SystemInfo{
				SOpt: common.DeviceOptions{
					MajorRange: []int{-1},
					MinorRange: []int{1, 2, 3},
				},
				Switches: []SwitchInfo{
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
			got := IsLinkWatched(tt.linkIndex, tt.switchID, tt.sysInfo)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCPUWatched(t *testing.T) {
	tests := []struct {
		name    string
		cpuID   uint
		sysInfo SystemInfo
		want    bool
	}{
		{
			name:  "Monitor all devices",
			cpuID: 1,
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{Flex: true},
				CPUs: []CPUInfo{
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
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{MajorRange: []int{-1}},
				CPUs: []CPUInfo{
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
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{MajorRange: []int{1, 2, 3}},
				CPUs: []CPUInfo{
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
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{MajorRange: []int{1, 2, 3}},
				CPUs: []CPUInfo{
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
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{MajorRange: []int{}},
				CPUs: []CPUInfo{
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
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{MajorRange: []int{}},
				CPUs: []CPUInfo{
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
			assert.Equal(t, tt.want, IsCPUWatched(tt.cpuID, tt.sysInfo))
		})
	}
}

func TestIsCoreWatched(t *testing.T) {
	tests := []struct {
		name    string
		coreID  uint
		cpuID   uint
		sysInfo SystemInfo
		want    bool
	}{
		{
			name:   "Monitor all devices",
			coreID: 1,
			cpuID:  1,
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{Flex: true},
			},
			want: true,
		},
		{
			name:   "Core in MinorRange",
			coreID: 2,
			cpuID:  1,
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				CPUs: []CPUInfo{{EntityId: 1}},
			},
			want: true,
		},
		{
			name:   "Core Not in MinorRange",
			coreID: 4,
			cpuID:  1,
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				CPUs: []CPUInfo{{EntityId: 1}},
			},
			want: false,
		},
		{
			name:   "MinorRange Contains -1",
			coreID: 5,
			cpuID:  1,
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{
					MinorRange: []int{-1},
					MajorRange: []int{-1},
				},
				CPUs: []CPUInfo{{EntityId: 1}},
			},
			want: true,
		},
		{
			name:   "CPU Not Found",
			coreID: 1,
			cpuID:  2,
			sysInfo: SystemInfo{
				COpt: common.DeviceOptions{
					MinorRange: []int{1, 2, 3},
					MajorRange: []int{-1},
				},
				CPUs: []CPUInfo{{EntityId: 1}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsCoreWatched(tt.coreID, tt.cpuID, tt.sysInfo))
		})
	}
}

func TestSetMigProfileNames(t *testing.T) {
	tests := []struct {
		name    string
		sysInfo SystemInfo
		values  []dcgm.FieldValue_v2
		valid   bool
	}{
		{
			name: "MIG profile found",
			sysInfo: SystemInfo{
				GPUCount: 1,
				GPUs: [dcgm.MAX_NUM_DEVICES]GPUInfo{
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
			name: "Multiple MIG GPUs",
			sysInfo: SystemInfo{
				GPUCount: 3,
				GPUs: [dcgm.MAX_NUM_DEVICES]GPUInfo{
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
			name: "Multiple MIG GPUs and Values",
			sysInfo: SystemInfo{
				GPUCount: 3,
				GPUs: [dcgm.MAX_NUM_DEVICES]GPUInfo{
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
			sysInfo: SystemInfo{
				GPUCount: 1,
				GPUs: [dcgm.MAX_NUM_DEVICES]GPUInfo{
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
			sysInfo: SystemInfo{
				GPUCount: 1,
				GPUs: [dcgm.MAX_NUM_DEVICES]GPUInfo{
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
				assert.NoError(t, SetMigProfileNames(&tt.sysInfo, tt.values), "Expected no error.")
			} else {
				assert.Error(t, SetMigProfileNames(&tt.sysInfo, tt.values), "Expected an error.")
			}
		})
	}
}
