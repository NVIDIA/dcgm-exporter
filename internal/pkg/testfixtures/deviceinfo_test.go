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

package testfixtures

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
)

// TestSampleGPUDevices keeps the shared GPU fixture stable for tests that
// select devices by their index, UUID, or model name.
func TestSampleGPUDevices(t *testing.T) {
	assert.Equal(t, []dcgm.Device{
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
	}, SampleGPUDevices())
}

// TestSampleMIGHierarchy keeps the fixture's two GPUs, three GPU instances,
// and four compute instances aligned with the hierarchy returned to callers.
func TestSampleMIGHierarchy(t *testing.T) {
	hierarchy, gpus, gpuInstances, computeInstances := SampleMIGHierarchy()

	assert.Equal(t, uint(2), hierarchy.Version)
	assert.Equal(t, uint(9), hierarchy.Count)
	assert.Len(t, gpus, 2)
	assert.Len(t, gpuInstances, 3)
	assert.Len(t, computeInstances, 4)
	assert.Equal(t, []dcgm.MigHierarchyInfo_v2{hierarchy.EntityList[0], hierarchy.EntityList[6]}, gpus)
	assert.Equal(t, []dcgm.MigHierarchyInfo_v2{hierarchy.EntityList[1], hierarchy.EntityList[4], hierarchy.EntityList[7]}, gpuInstances)
	assert.Equal(t, []dcgm.MigHierarchyInfo_v2{hierarchy.EntityList[2], hierarchy.EntityList[3], hierarchy.EntityList[5], hierarchy.EntityList[8]}, computeInstances)
}
