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

	"github.com/NVIDIA/dcgm-exporter/pkg/common"
)

//go:generate mockgen -destination=mocks/pkg/dcgmexporter/sysinfo/mock_system_info.go github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/sysinfo SystemInfoInterface

type SystemInfoInterface interface {
	GPUCount() uint
	GPUs() []GPUInfo
	GPU(uint) GPUInfo
	Switches() []SwitchInfo
	Switch(uint) SwitchInfo
	CPUs() []CPUInfo
	CPU(uint) CPUInfo
	GOpts() common.DeviceOptions
	SOpts() common.DeviceOptions
	COpts() common.DeviceOptions
	InfoType() dcgm.Field_Entity_Group
	InitializeNvSwitchInfo(common.DeviceOptions) error
	InitializeGPUInfo(common.DeviceOptions, bool) error
	InitializeCPUInfo(common.DeviceOptions) error
	SetGPUInstanceProfileName(uint, string) bool
	VerifyCPUDevicePresence(common.DeviceOptions) error
	VerifySwitchDevicePresence(common.DeviceOptions) error
	VerifyDevicePresence(common.DeviceOptions) error
	PopulateMigProfileNames([]dcgm.GroupEntityPair) error
	SetMigProfileNames([]dcgm.FieldValue_v2) error
	GPUIdExists(int) bool
	SwitchIdExists(int) bool
	CPUIdExists(int) bool
	GPUInstanceIdExists(int) bool
	LinkIdExists(int) bool
	CPUCoreIdExists(int) bool
	IsSwitchWatched(uint) bool
	IsLinkWatched(uint, uint) bool
	IsCPUWatched(uint) bool
	IsCoreWatched(uint, uint) bool
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
