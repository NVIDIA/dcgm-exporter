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

package devicemonitoring

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/deviceinfo"
)

// Info describes one monitored DCGM entity. DeviceInfo is set for GPUs, GPU
// instances, compute instances and GPU NVLinks, which carry their parent GPU's
// device; switches, switch links, CPUs and CPU cores leave it zero.
// InstanceInfo is set only for GPU instances and compute instances, and
// ComputeInstanceInfo only for compute instances. ParentId and ParentType name
// the entity's parent: the GPU instance of a compute instance, the GPU or
// switch of a link, or the CPU of a core. A GPU instance has ParentType FE_GPU
// but ParentId PARENT_ID_IGNORED, so its GPU comes from DeviceInfo. Top-level
// entities use PARENT_ID_IGNORED and FE_NONE.
type Info struct {
	Entity       dcgm.GroupEntityPair
	DeviceInfo   dcgm.Device
	InstanceInfo *deviceinfo.GPUInstanceInfo
	// ComputeInstanceInfo identifies the child compute instance when Entity is a GPU compute instance.
	ComputeInstanceInfo *deviceinfo.ComputeInstanceInfo
	ParentId            uint
	ParentType          dcgm.Field_Entity_Group
}
