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

// Package testfixtures provides static values that tests can share without
// adding test-only APIs to production packages.
package testfixtures

import (
	"math"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// SampleGPUDevices returns two stable GPU descriptions for tests that need a
// predictable multi-GPU inventory without querying DCGM or NVML.
func SampleGPUDevices() []dcgm.Device {
	return []dcgm.Device{
		{
			GPU:  0,
			UUID: "000000000000",
			Identifiers: dcgm.DeviceIdentifiers{
				Model: "NVIDIA T400 4GB",
			},
		},
		{
			GPU:  1,
			UUID: "11111111111",
			Identifiers: dcgm.DeviceIdentifiers{
				Model: "NVIDIA A100 40GB",
			},
		},
	}
}

// SampleMIGHierarchy returns a stable two-GPU MIG topology and its GPU,
// GPU-instance, and compute-instance slices for device-info tests.
func SampleMIGHierarchy() (dcgm.MigHierarchy_v2, []dcgm.MigHierarchyInfo_v2, []dcgm.MigHierarchyInfo_v2,
	[]dcgm.MigHierarchyInfo_v2,
) {
	sampleMigHierarchy := dcgm.MigHierarchy_v2{
		Version: 2,
		Count:   9,
	}

	sampleGPU1 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_NONE, EntityId: math.MaxUint},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU1",
			NvmlGpuIndex:          0,
			NvmlInstanceId:        math.MaxUint,
			NvmlComputeInstanceId: math.MaxUint,
			NvmlMigProfileId:      math.MaxUint,
			NvmlProfileSlices:     0,
		},
	}

	sampleGPU2 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_NONE, EntityId: math.MaxUint},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU2",
			NvmlGpuIndex:          1,
			NvmlInstanceId:        math.MaxUint,
			NvmlComputeInstanceId: math.MaxUint,
			NvmlMigProfileId:      math.MaxUint,
			NvmlProfileSlices:     0,
		},
	}

	sampleGPU1Instance1 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 1},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU1_I1",
			NvmlGpuIndex:          0,
			NvmlInstanceId:        0,
			NvmlComputeInstanceId: math.MaxUint,
			NvmlMigProfileId:      1,
			NvmlProfileSlices:     4,
		},
	}

	sampleGPU1Instance2 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 0},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU1_I2",
			NvmlGpuIndex:          0,
			NvmlInstanceId:        1,
			NvmlComputeInstanceId: math.MaxUint,
			NvmlMigProfileId:      2,
			NvmlProfileSlices:     2,
		},
	}

	sampleGPU1Instance1CI1 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 1},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 1},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU1_I1_CI1",
			NvmlGpuIndex:          0,
			NvmlInstanceId:        0,
			NvmlComputeInstanceId: 0,
			NvmlMigProfileId:      3,
			NvmlProfileSlices:     1,
		},
	}

	sampleGPU1Instance1CI2 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 2},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 1},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU1_I1_CI2",
			NvmlGpuIndex:          0,
			NvmlInstanceId:        0,
			NvmlComputeInstanceId: 1,
			NvmlMigProfileId:      4,
			NvmlProfileSlices:     1,
		},
	}

	sampleGPU1Instance2CI1 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 3},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 2},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU1_I2_CI1",
			NvmlGpuIndex:          0,
			NvmlInstanceId:        1,
			NvmlComputeInstanceId: 2,
			NvmlMigProfileId:      5,
			NvmlProfileSlices:     1,
		},
	}

	sampleGPU2Instance1 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 3},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU, EntityId: 1},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU2_I1",
			NvmlGpuIndex:          1,
			NvmlInstanceId:        0,
			NvmlComputeInstanceId: math.MaxUint,
			NvmlMigProfileId:      6,
			NvmlProfileSlices:     4,
		},
	}

	sampleGPU2Instance1CI1 := dcgm.MigHierarchyInfo_v2{
		Entity: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_CI, EntityId: 4},
		Parent: dcgm.GroupEntityPair{EntityGroupId: dcgm.FE_GPU_I, EntityId: 3},
		Info: dcgm.MigEntityInfo{
			GpuUuid:               "FAKE_GPU2_I1_CI1",
			NvmlGpuIndex:          1,
			NvmlInstanceId:        0,
			NvmlComputeInstanceId: 0,
			NvmlMigProfileId:      7,
			NvmlProfileSlices:     1,
		},
	}

	sampleMigHierarchy.EntityList[0] = sampleGPU1
	sampleMigHierarchy.EntityList[1] = sampleGPU1Instance1
	sampleMigHierarchy.EntityList[2] = sampleGPU1Instance1CI1
	sampleMigHierarchy.EntityList[3] = sampleGPU1Instance1CI2
	sampleMigHierarchy.EntityList[4] = sampleGPU1Instance2
	sampleMigHierarchy.EntityList[5] = sampleGPU1Instance2CI1
	sampleMigHierarchy.EntityList[6] = sampleGPU2
	sampleMigHierarchy.EntityList[7] = sampleGPU2Instance1
	sampleMigHierarchy.EntityList[8] = sampleGPU2Instance1CI1

	return sampleMigHierarchy, []dcgm.MigHierarchyInfo_v2{sampleGPU1, sampleGPU2},
		[]dcgm.MigHierarchyInfo_v2{sampleGPU1Instance1, sampleGPU1Instance2, sampleGPU2Instance1},
		[]dcgm.MigHierarchyInfo_v2{
			sampleGPU1Instance1CI1, sampleGPU1Instance1CI2, sampleGPU1Instance2CI1,
			sampleGPU2Instance1CI1,
		}
}
