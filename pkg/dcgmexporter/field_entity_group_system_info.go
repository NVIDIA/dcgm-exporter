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
	Config       *Config
}

// FieldEntityGroupTypeSystemInfo represents a mapping between FieldEntityGroupType and SystemInfo
type FieldEntityGroupTypeSystemInfo struct {
	items    map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem
	counters []Counter
	config   *Config
}

// NewEntityGroupTypeSystemInfo creates a new instance of the FieldEntityGroupTypeSystemInfo
func NewEntityGroupTypeSystemInfo(c []Counter, config *Config) *FieldEntityGroupTypeSystemInfo {
	return &FieldEntityGroupTypeSystemInfo{
		items:    make(map[dcgm.Field_Entity_Group]FieldEntityGroupTypeSystemInfoItem),
		counters: c,
		config:   config,
	}
}

// Load loads SystemInfo for a provided Field_Entity_Group
func (e *FieldEntityGroupTypeSystemInfo) Load(entityType dcgm.Field_Entity_Group) error {
	var deviceFields = NewDeviceFields(e.counters, entityType)

	if !ShouldMonitorDeviceType(deviceFields, entityType) {
		return fmt.Errorf("No fields to watch for device type: %d", entityType)
	}

	sysInfo, err := GetSystemInfo(e.config, entityType)
	if err == nil {
		e.items[entityType] = FieldEntityGroupTypeSystemInfoItem{
			SystemInfo:   *sysInfo,
			DeviceFields: deviceFields,
			Config:       e.config,
		}
	}

	return err
}

// Get returns FieldEntityGroupTypeSystemInfoItem, bool by dcgm.Field_Entity_Group
func (e *FieldEntityGroupTypeSystemInfo) Get(key dcgm.Field_Entity_Group) (FieldEntityGroupTypeSystemInfoItem, bool) {
	val, exists := e.items[key]
	return val, exists
}
