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

package dcgmexporter

import (
	"fmt"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// FieldEntityGroupTypeToMonitor supported entity group types
var FieldEntityGroupTypeToMonitor = []dcgm.Field_Entity_Group{
	dcgm.FE_GPU,
	dcgm.FE_SWITCH,
	dcgm.FE_LINK,
	dcgm.FE_CPU,
	dcgm.FE_CPU_CORE,
}

type FieldEntityGroupTypeSystemInfoItem struct {
	SystemInfo   SystemInfo
	DeviceFields []dcgm.Short
}

func (f FieldEntityGroupTypeSystemInfoItem) isEmpty() bool {
	return len(f.DeviceFields) == 0
}

// FieldEntityGroupTypeSystemInfo represents a mapping between FieldEntityGroupType and SystemInfo
type FieldEntityGroupTypeSystemInfo struct {
	items         map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem
	counters      []Counter
	gpuDevices    appconfig.DeviceOptions
	switchDevices appconfig.DeviceOptions
	cpuDevices    appconfig.DeviceOptions
	useFakeGPUs   bool
}

// NewEntityGroupTypeSystemInfo creates a new instance of the FieldEntityGroupTypeSystemInfo
func NewEntityGroupTypeSystemInfo(c []Counter, config *appconfig.Config) *FieldEntityGroupTypeSystemInfo {
	return &FieldEntityGroupTypeSystemInfo{
		items:         make(map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem),
		counters:      c,
		gpuDevices:    config.GPUDevices,
		switchDevices: config.SwitchDevices,
		cpuDevices:    config.CPUDevices,
		useFakeGPUs:   config.UseFakeGPUs,
	}
}

// Load loads SystemInfo for a provided Field_Entity_Group
func (e *FieldEntityGroupTypeSystemInfo) Load(entityType dcgm.Field_Entity_Group) error {
	var deviceFields = NewDeviceFields(e.counters, entityType)

	if !ShouldMonitorDeviceType(deviceFields, entityType) {
		return fmt.Errorf("no fields to watch for device type: %d", entityType)
	}

	sysInfo, err := GetSystemInfo(&appconfig.Config{
		GPUDevices:    e.gpuDevices,
		SwitchDevices: e.switchDevices,
		CPUDevices:    e.cpuDevices,
		UseFakeGPUs:   e.useFakeGPUs,
	}, entityType)
	if err != nil {
		return err
	}

	e.items[entityType] = FieldEntityGroupTypeSystemInfoItem{
		SystemInfo:   *sysInfo,
		DeviceFields: deviceFields,
	}

	return err
}

// Get returns FieldEntityGroupTypeSystemInfoItem, bool by dcgm.Field_Entity_Group
func (e *FieldEntityGroupTypeSystemInfo) Get(key dcgm.Field_Entity_Group) (FieldEntityGroupTypeSystemInfoItem, bool) {
	val, exists := e.items[key]
	return val, exists
}
