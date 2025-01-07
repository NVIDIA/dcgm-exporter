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

//go:generate go run -v go.uber.org/mock/mockgen  -destination=../../mocks/pkg/deviceinfo/mock_device_info.go -package=deviceinfo -copyright_file=../../../hack/header.txt . Provider

package deviceinfo

import (
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

type Provider interface {
	GPUCount() uint
	GPUs() []GPUInfo
	GPU(i uint) GPUInfo
	Switches() []SwitchInfo
	Switch(i uint) SwitchInfo
	CPUs() []CPUInfo
	CPU(i uint) CPUInfo
	GOpts() appconfig.DeviceOptions
	SOpts() appconfig.DeviceOptions
	COpts() appconfig.DeviceOptions
	InfoType() dcgm.Field_Entity_Group
	IsCPUWatched(cpuID uint) bool
	IsCoreWatched(coreID uint, cpuID uint) bool
	IsSwitchWatched(switchID uint) bool
	IsLinkWatched(linkIndex uint, switchID uint) bool
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
