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

package testutils

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

var fakeProfileName = "2fake.4gb"

var (
	MockGPUInstanceInfo1 = deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    0,
	}

	MockGPUInstanceInfo2 = deviceinfo.GPUInstanceInfo{
		Info:        dcgm.MigEntityInfo{GpuUuid: "fake", NvmlInstanceId: 1, NvmlProfileSlices: 3},
		ProfileName: fakeProfileName,
		EntityId:    14,
	}

	MockNVLinkVal1 = dcgm.NvLinkStatus{
		State: 2,
		Index: 0,
	}

	MockNVLinkVal2 = dcgm.NvLinkStatus{
		State: 3,
		Index: 1,
	}
)
