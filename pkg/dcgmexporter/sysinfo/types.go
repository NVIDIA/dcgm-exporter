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

package sysinfo

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

type SystemInfoInterface interface {
}

type GPUInfo struct {
	DeviceInfo   dcgm.Device
	GPUInstances []GPUInstanceInfo
	MigEnabled   bool
}

type GPUInstanceInfo struct {
	Info             dcgm.MigEntityInfo
	ProfileName      string
	EntityId         uint
	ComputeInstances []ComputeInstanceInfo
}

type ComputeInstanceInfo struct {
	InstanceInfo dcgm.MigEntityInfo
	ProfileName  string
	EntityId     uint
}

type CPUInfo struct {
	EntityId uint
	Cores    []uint
}

type SwitchInfo struct {
	EntityId uint
	NvLinks  []dcgm.NvLinkStatus
}

type MonitoringInfo struct {
	Entity       dcgm.GroupEntityPair
	DeviceInfo   dcgm.Device
	InstanceInfo *GPUInstanceInfo
	ParentId     uint
}
