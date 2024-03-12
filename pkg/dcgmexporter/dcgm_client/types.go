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

//go:generate mockgen -destination=mocks/pkg/dcgmexporter/mock_expcollector.go github.com/NVIDIA/dcgm_client-exporter/pkg/dcgmexporter Collector

package dcgm_client

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/common"
)

type GPUInfo struct {
	DeviceInfo   dcgm.Device
	GPUInstances []GPUInstanceInfo
	MigEnabled   bool
}

type CPUInfo struct {
	EntityId uint
	Cores    []uint
}

type SwitchInfo struct {
	EntityId uint
	NvLinks  []dcgm.NvLinkStatus
}

type ComputeInstanceInfo struct {
	InstanceInfo dcgm.MigEntityInfo
	ProfileName  string
	EntityId     uint
}

type GPUInstanceInfo struct {
	Info             dcgm.MigEntityInfo
	ProfileName      string
	EntityId         uint
	ComputeInstances []ComputeInstanceInfo
}

type SystemInfo struct {
	GPUCount uint
	GPUs     [dcgm.MAX_NUM_DEVICES]GPUInfo
	GOpt     common.DeviceOptions
	SOpt     common.DeviceOptions
	COpt     common.DeviceOptions
	InfoType dcgm.Field_Entity_Group
	Switches []SwitchInfo
	CPUs     []CPUInfo
}
