/*
 * Copyright (c) 2020, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"fmt"
	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/dcgm"
	"github.com/stretchr/testify/require"
	"testing"
)

const (
	fakeProfileName string = "2fake.4gb"
)

func SpoofSystemInfo() SystemInfo {
	var sysInfo SystemInfo
	sysInfo.GpuCount = 2
	sysInfo.MigEnabled = true
	sysInfo.Gpus[0].DeviceInfo.GPU = 0
	gi := GpuInstanceInfo{
		Info:        dcgm.MigEntityInfo{"fake", 0, 0, 0, 0, 3},
		ProfileName: fakeProfileName,
		EntityId:    0,
	}
	sysInfo.Gpus[0].GpuInstances = append(sysInfo.Gpus[0].GpuInstances, gi)
	gi2 := GpuInstanceInfo{
		Info:        dcgm.MigEntityInfo{"fake", 0, 1, 0, 0, 3},
		ProfileName: fakeProfileName,
		EntityId:    14,
	}
	sysInfo.Gpus[1].GpuInstances = append(sysInfo.Gpus[1].GpuInstances, gi2)
	sysInfo.Gpus[1].DeviceInfo.GPU = 1

	return sysInfo
}

func TestMonitoredEntities(t *testing.T) {
	sysInfo := SpoofSystemInfo()
	sysInfo.dOpt.Flex = true

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
			require.Equal(t, mi.InstanceInfo, (*GpuInstanceInfo)(nil), "Expected InstanceInfo to be nil but it wasn't")
		}
	}
	require.Equal(t, instanceCount, 2, "Expected 2 GPU instances but found %d", instanceCount)
	require.Equal(t, gpuCount, 0, "Expected 0 GPUs but found %d", gpuCount)

	sysInfo.MigEnabled = false // we are now monitoring the GPUs
	monitoring = GetMonitoredEntities(sysInfo)
	require.Equal(t, 2, len(monitoring), fmt.Sprintf("Should have 2 monitored entities but found %d", len(monitoring)))
	for i, mi := range monitoring {
		require.Equal(t, mi.Entity.EntityGroupId, dcgm.FE_GPU, "Expected FE_GPU but found %d", mi.Entity.EntityGroupId)
		require.Equal(t, uint(i), mi.DeviceInfo.GPU, "Expected GPU %d but found %d", i, mi.DeviceInfo.GPU)
		require.Equal(t, (*GpuInstanceInfo)(nil), mi.InstanceInfo, "Expected InstanceInfo not to be populated but it was")
	}
}

func TestVerifyDevicePresence(t *testing.T) {
	sysInfo := SpoofSystemInfo()
	var dOpt DeviceOptions
	dOpt.Flex = true
	err := VerifyDevicePresence(&sysInfo, dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	dOpt.Flex = false
	dOpt.GpuRange = append(dOpt.GpuRange, -1)
	dOpt.GpuInstanceRange = append(dOpt.GpuInstanceRange, -1)
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)

	dOpt.GpuInstanceRange[0] = 10 // this GPU instance doesn't exist
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU instance, but none found")

	dOpt.GpuRange[0] = 10 // this GPU doesn't exist
	dOpt.GpuInstanceRange[0] = -1
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.NotEqual(t, err, nil, "Expected to have an error for a non-existent GPU, but none found")

	// Add GPUs and instances that exist
	dOpt.GpuRange[0] = 0
	dOpt.GpuRange = append(dOpt.GpuRange, 1)
	dOpt.GpuInstanceRange[0] = 0
	dOpt.GpuInstanceRange = append(dOpt.GpuInstanceRange, 14)
	err = VerifyDevicePresence(&sysInfo, dOpt)
	require.Equal(t, err, nil, "Expected to have no error, but found %s", err)
}

//func TestMigProfileNames(t *testing.T) {
//	sysInfo := SpoofSystemInfo()
//    SetMigProfileNames(sysInfo, values)
//}
