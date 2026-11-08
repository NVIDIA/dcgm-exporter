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

// Provider supplies discovered GPU, CPU, and switch topology to collectors and
// renderers. Info is the production implementation; callers can provide a
// small fake in tests when they only need the read-only topology contract.
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

// GPUInfo describes one GPU and the MIG instances and NVLink ports discovered
// beneath it. Info returns these values to code that builds GPU metric labels.
type GPUInfo struct {
	// Status distinguishes a known detached GPU from a missing GPU.
	Status           dcgm.EntityStatus
	SyntheticGPUUUID bool `json:"-"`
	DeviceInfo       dcgm.Device
	GPUInstances     []GPUInstanceInfo
	MigEnabled       bool
	NvLinks          []dcgm.NvLinkStatus
}

// GPUInstanceInfo describes one MIG GPU instance and its compute instances.
// It is nested in GPUInfo after device discovery has built the MIG hierarchy.
type GPUInstanceInfo struct {
	Info             dcgm.MigEntityInfo
	ProfileName      string
	EntityId         uint
	ComputeInstances []ComputeInstanceInfo
}

// ComputeInstanceInfo describes one MIG compute instance. It is nested in its
// parent GPUInstanceInfo for collectors that need a compute-instance entity.
type ComputeInstanceInfo struct {
	InstanceInfo dcgm.MigEntityInfo
	ProfileName  string
	EntityId     uint
}

// CPUInfo describes a CPU entity and the cores selected beneath it. Info
// returns these values when CPU or CPU-core metrics are configured.
type CPUInfo struct {
	EntityId uint
	Cores    []uint
	Serial   string
}

// SwitchInfo describes an NVSwitch entity and its selected NVLink ports. Info
// returns these values when switch or link metrics are configured.
type SwitchInfo struct {
	EntityId uint
	NvLinks  []dcgm.NvLinkStatus
}
