/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import (
	"fmt"
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
	"testing"
)

const (
	fakeProfileName string = "2fake.4gb"
)

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

	return sysInfo
}

func SpoofSystemInfo() SystemInfo {
	var sysInfo SystemInfo
	sysInfo.GPUCount = 2
	sysInfo.GPUs[0].DeviceInfo.GPU = 0
	gi := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{"fake", 0, 0, 0, 0, 3},
		ProfileName: fakeProfileName,
		EntityId:    0,
	}
	sysInfo.GPUs[0].GPUInstances = append(sysInfo.GPUs[0].GPUInstances, gi)
	gi2 := GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{"fake", 0, 1, 0, 0, 3},
		ProfileName: fakeProfileName,
		EntityId:    14,
	}
	sysInfo.GPUs[1].GPUInstances = append(sysInfo.GPUs[1].GPUInstances, gi2)
	sysInfo.GPUs[1].DeviceInfo.GPU = 1

	return sysInfo
}

func TestMonitoredEntities(t *testing.T) {
	sysInfo := SpoofSystemInfo()
	sysInfo.gOpt.Flex = true

	monitoring := GetMonitoredEntities(sysInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored entities but found %d", len(monitoring)))
	instanceCount := 0
	gpuCount := 0
	for _, mi := range monitoring {
		if mi.Entity.EntityGroupId == dcgm.FE_GPU_I {
			instanceCount = instanceCount + 1
			require.NotEqual(t, mi.InstanceInfo, nil, "Expected InstanceInfo to be populated but it wasn't")
			require.Equal(t, mi.InstanceInfo.ProfileName, fakeProfileName, "Expected profile named '%s' but found '%s'", fakeProfileName, mi.InstanceInfo.ProfileName)
			if mi.Entity.EntityId != uint(0) {
				// One of these should be 0, the other should be 14
				require.Equal(t, mi.Entity.EntityId, uint(14), "Expected 14 as EntityId but found %s", monitoring[1].Entity.EntityId)
			}
		} else {
			gpuCount = gpuCount + 1
			require.Equal(t, mi.InstanceInfo, (*GPUInstanceInfo)(nil), "Expected InstanceInfo to be nil but it wasn't")
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
		require.Equal(t, (*GPUInstanceInfo)(nil), mi.InstanceInfo, "Expected InstanceInfo not to be populated but it was")
	}
}

func TestVerifyDevicePresence(t *testing.T) {
	sysInfo := SpoofSystemInfo()
	var dOpt DeviceOptions
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

//func TestMigProfileNames(t *testing.T) {
//	sysInfo := SpoofSystemInfo()
//    SetMigProfileNames(sysInfo, values)
//}

func TestMonitoredSwitches(t *testing.T) {
	sysInfo := SpoofSwitchSystemInfo()

	/* test that only switches are returned */
	monitoring := GetMonitoredEntities(sysInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored switches but found %d", len(monitoring)))
	for _, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_SWITCH, fmt.Sprintf("Should have only returned switches but returned %d", mi.Entity.EntityGroupId))
	}

	/* test that only "up" links are monitored and 1 from each switch */
	sysInfo.InfoType = dcgm.FE_LINK
	monitoring = GetMonitoredEntities(sysInfo)
	require.Equal(t, len(monitoring), 2, fmt.Sprintf("Should have 2 monitored links but found %d", len(monitoring)))
	for i, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_LINK, fmt.Sprintf("Should have only returned links but returned %d", mi.Entity.EntityGroupId))
		require.Equal(t, mi.ParentId, uint(i), fmt.Sprint("Link should reference switch parent"))
	}
}
