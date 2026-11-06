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

package appconfig

import (
	"fmt"
)

// maxEntityIndices caps one index list. It matches the cap the device selector
// flags apply to an expanded range.
const maxEntityIndices = 1024

// YAMLEntities selects which GPUs, switches and CPUs are monitored. It is the
// YAML form of the --devices, --switch-devices and --cpu-devices flags.
type YAMLEntities struct {
	GPU    *YAMLGPUEntities    `yaml:"gpu,omitempty" json:"gpu,omitempty"`
	Switch *YAMLSwitchEntities `yaml:"switch,omitempty" json:"switch,omitempty"`
	CPU    *YAMLCPUEntities    `yaml:"cpu,omitempty" json:"cpu,omitempty"`
}

// YAMLGPUEntities selects GPUs and GPU instances. Policy is flex or explicit;
// GPUs and Instances are "all", "none" or a list of indices. Policy flex takes
// no lists; policy explicit needs at least one, and a list left out means
// none, unlike the switch and cpu selectors where it means all.
type YAMLGPUEntities struct {
	Policy    string `yaml:"policy,omitempty" json:"policy,omitempty"`
	GPUs      any    `yaml:"gpus,omitempty" json:"gpus,omitempty"`
	Instances any    `yaml:"instances,omitempty" json:"instances,omitempty"`
}

// YAMLSwitchEntities selects NVSwitches and their links. A missing list means all.
type YAMLSwitchEntities struct {
	Indices any `yaml:"indices,omitempty" json:"indices,omitempty"`
	Links   any `yaml:"links,omitempty" json:"links,omitempty"`
}

// YAMLCPUEntities selects CPUs and their cores. A missing list means all.
type YAMLCPUEntities struct {
	Indices any `yaml:"indices,omitempty" json:"indices,omitempty"`
	Cores   any `yaml:"cores,omitempty" json:"cores,omitempty"`
}

// apply writes each selector that the file names into the device options.
func (e *YAMLEntities) apply(config *Config, entitiesPath string, rec originRecorder) error {
	if e == nil {
		return nil
	}

	// GPUs.
	if e.GPU != nil {
		options, err := e.GPU.deviceOptions(entitiesPath + ".gpu")
		if err != nil {
			return err
		}

		config.GPUDeviceOptions = options
		rec.record("GPUDeviceOptions", entitiesPath+".gpu")
	}

	// Switches and their links.
	if e.Switch != nil {
		options, err := majorMinorOptions(e.Switch.Indices, e.Switch.Links, entitiesPath+".switch", "indices", "links")
		if err != nil {
			return err
		}

		config.SwitchDeviceOptions = options
		rec.record("SwitchDeviceOptions", entitiesPath+".switch")
	}

	// CPUs and their cores.
	if e.CPU != nil {
		options, err := majorMinorOptions(e.CPU.Indices, e.CPU.Cores, entitiesPath+".cpu", "indices", "cores")
		if err != nil {
			return err
		}

		config.CPUDeviceOptions = options
		rec.record("CPUDeviceOptions", entitiesPath+".cpu")
	}

	return nil
}

// deviceOptions converts the GPU selector to the runtime options the -d flag builds.
func (g *YAMLGPUEntities) deviceOptions(gpuPath string) (DeviceOptions, error) {
	switch g.Policy {
	case "", "flex":
		if g.GPUs != nil || g.Instances != nil {
			return DeviceOptions{}, keyErrorf(gpuPath, "policy flex cannot set gpus or instances")
		}

		return DeviceOptions{Flex: true}, nil
	case "explicit":
		if g.GPUs == nil && g.Instances == nil {
			return DeviceOptions{}, keyErrorf(gpuPath, "policy explicit requires gpus and/or instances")
		}

		major, err := selectionRange(g.GPUs, gpuPath+".gpus", false)
		if err != nil {
			return DeviceOptions{}, err
		}

		minor, err := selectionRange(g.Instances, gpuPath+".instances", false)
		if err != nil {
			return DeviceOptions{}, err
		}

		if len(major) == 0 && len(minor) == 0 {
			return DeviceOptions{}, keyErrorf(gpuPath, "selects nothing; set gpus or instances to all or a list of indices")
		}

		return DeviceOptions{MajorRange: major, MinorRange: minor}, nil
	default:
		return DeviceOptions{}, keyErrorf(gpuPath+".policy", "%q is not supported; use flex or explicit", g.Policy)
	}
}

// majorMinorOptions converts a switch or CPU selector. A missing list means all.
// All of both lists gives the flexible selector, the same as the default. It
// does not reproduce the check the -s and -p flags make for the value g+i (all
// switches or CPUs plus all links or cores) that each listed entity exists.
func majorMinorOptions(major, minor any, entityPath, majorKey, minorKey string) (DeviceOptions, error) {
	majorRange, err := selectionRange(major, entityPath+"."+majorKey, true)
	if err != nil {
		return DeviceOptions{}, err
	}

	minorRange, err := selectionRange(minor, entityPath+"."+minorKey, true)
	if err != nil {
		return DeviceOptions{}, err
	}

	// Two empty lists select nothing, which is an error. Two lists of "all"
	// are the same as the default selector.
	switch {
	case len(majorRange) == 0 && len(minorRange) == 0:
		return DeviceOptions{}, keyErrorf(entityPath, "selects nothing; set %s or %s to all or a list of indices",
			majorKey, minorKey)
	case isAllSelection(majorRange) && isAllSelection(minorRange):
		return DeviceOptions{Flex: true}, nil
	}

	return DeviceOptions{MajorRange: majorRange, MinorRange: minorRange}, nil
}

// isAllSelection reports whether a range means all, which is stored as [-1].
func isAllSelection(indices []int) bool {
	return len(indices) == 1 && indices[0] == -1
}

// selectionRange converts "all", "none" or an index list to the range form the
// runtime uses: [-1] for all, nil for none. A missing value is none, or all
// when missingMeansAll is set.
func selectionRange(value any, selectionPath string, missingMeansAll bool) ([]int, error) {
	switch v := value.(type) {
	case nil:
		if missingMeansAll {
			return []int{-1}, nil
		}

		return nil, nil
	case string:
		// A string is valid only as all or none; anything else reaches the error below.
		switch v {
		case "all":
			return []int{-1}, nil
		case "none":
			return nil, nil
		}
	case []any:
		return indexList(v, selectionPath)
	}

	return nil, keyErrorf(selectionPath, "must be all, none or a list of indices")
}

// UniqueIndices drops repeated indices and keeps the first-seen order. The
// device flags and the entities section both use it, so a selector such as
// g:0,0 selects GPU 0 once and the document that show prints loads back.
func UniqueIndices(indices []int) []int {
	if len(indices) < 2 {
		return indices
	}

	seen := make(map[int]struct{}, len(indices))
	unique := make([]int, 0, len(indices))

	for _, index := range indices {
		if _, ok := seen[index]; ok {
			continue
		}

		seen[index] = struct{}{}
		unique = append(unique, index)
	}

	return unique
}

// indexList checks a list of non-negative indices and drops repeated ones.
func indexList(values []any, listPath string) ([]int, error) {
	if len(values) == 0 {
		return nil, keyErrorf(listPath, "must not be an empty list; use none")
	}

	if len(values) > maxEntityIndices {
		return nil, keyErrorf(listPath, "may contain at most %d indices", maxEntityIndices)
	}

	indices := make([]int, 0, len(values))
	for i, value := range values {
		index, ok := value.(int)
		if !ok || index < 0 {
			return nil, keyErrorf(fmt.Sprintf("%s[%d]", listPath, i), "must be a non-negative integer")
		}
		indices = append(indices, index)
	}

	return UniqueIndices(indices), nil
}
