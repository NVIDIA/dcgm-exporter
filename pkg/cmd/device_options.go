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

package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

const deviceSelectorExpansionLimitErrorFormat = "device selector expands to more than %d indices"

// parseDeviceOptions parses the flex, major, minor, and combined selector grammar used by device flags.
func parseDeviceOptions(devices string) (appconfig.DeviceOptions, error) {
	var dOpt appconfig.DeviceOptions

	parts := strings.Split(devices, "+")
	for _, part := range parts {
		if part == "" {
			return dOpt, fmt.Errorf("invalid device option '%s': empty selector", devices)
		}

		if err := parseDeviceOption(devices, part, len(parts) > 1, &dOpt); err != nil {
			return dOpt, err
		}
	}

	return dOpt, nil
}

// parseDeviceOption validates one selector from a device flag and adds its
// indices to the GPU, switch, or CPU options being built by parseDeviceOptions.
func parseDeviceOption(devices, part string, combined bool, options *appconfig.DeviceOptions) error {
	letter, rangeSpec, hasRange := strings.Cut(part, ":")
	if strings.Contains(rangeSpec, ":") {
		return fmt.Errorf("invalid ranged device option '%s': there can only be one specified range", devices)
	}

	switch letter {
	case FlexKey:
		return parseFlexDeviceOption(combined, hasRange, options)
	case MajorKey, MinorKey:
		var currentRange []int
		if letter == MajorKey {
			currentRange = options.MajorRange
		} else {
			currentRange = options.MinorRange
		}

		if currentRange != nil {
			return fmt.Errorf("duplicate device option '%s'", letter)
		}

		indices, err := parseDeviceRange(rangeSpec, hasRange, int(dcgm.MAX_NUM_CPU_CORES))
		if err != nil {
			return err
		}

		if letter == MajorKey {
			options.MajorRange = indices
		} else {
			options.MinorRange = indices
		}
	default:
		return fmt.Errorf("the only valid options are 'f', 'g', or 'i', but found '%s'", letter)
	}

	return nil
}

// parseFlexDeviceOption accepts the flex selector only when used on its own
// without a range. parseDeviceOption calls it before setting flexible selection.
func parseFlexDeviceOption(combined, hasRange bool, options *appconfig.DeviceOptions) error {
	if combined {
		return fmt.Errorf("the flex option 'f' cannot be combined with other device options")
	}

	if hasRange {
		return fmt.Errorf("no range can be specified with the flex option 'f'")
	}

	options.Flex = true
	return nil
}

// parseDeviceRange expands an optional comma-separated selector range into device indices.
func parseDeviceRange(rangeSpec string, hasRange bool, limit int) ([]int, error) {
	if !hasRange {
		// No range means all present devices of the type.
		return []int{-1}, nil
	}

	var indices []int
	for _, numberOrRange := range strings.Split(rangeSpec, ",") {
		var err error
		indices, err = appendDeviceRange(indices, numberOrRange, limit)
		if err != nil {
			return nil, err
		}
	}

	// The expansion limit counts repeats; the selection keeps each index once.
	return appconfig.UniqueIndices(indices), nil
}

// appendDeviceRange adds one index or inclusive range to a device selection.
// parseDeviceRange calls it for each comma-separated part and enforces the limit.
func appendDeviceRange(indices []int, numberOrRange string, limit int) ([]int, error) {
	rangeTokens := strings.Split(numberOrRange, "-")
	switch len(rangeTokens) {
	case 1:
		number, err := strconv.Atoi(rangeTokens[0])
		if err != nil {
			return nil, err
		}

		if len(indices) >= limit {
			return nil, fmt.Errorf(deviceSelectorExpansionLimitErrorFormat, dcgm.MAX_NUM_CPU_CORES)
		}

		return append(indices, number), nil
	case 2:
		start, end, err := parseDeviceRangeBounds(rangeTokens, numberOrRange)
		if err != nil {
			return nil, err
		}

		return appendDeviceIndexRange(indices, start, end, limit)
	default:
		return nil, fmt.Errorf("range can only be '<number>-<number>', but found '%s'", numberOrRange)
	}
}

// parseDeviceRangeBounds reads and checks the bounds of a device range.
func parseDeviceRangeBounds(rangeTokens []string, numberOrRange string) (int, int, error) {
	start, err := strconv.Atoi(rangeTokens[0])
	if err != nil {
		return 0, 0, err
	}

	end, err := strconv.Atoi(rangeTokens[1])
	if err != nil {
		return 0, 0, err
	}

	if start > end {
		return 0, 0, fmt.Errorf("invalid range '%s': start (%d) must not exceed end (%d)", numberOrRange, start, end)
	}

	return start, end, nil
}

// appendDeviceIndexRange adds each index from a parsed range to device options.
func appendDeviceIndexRange(indices []int, start, end, limit int) ([]int, error) {
	for i := start; i <= end; i++ {
		if len(indices) >= limit {
			return nil, fmt.Errorf(deviceSelectorExpansionLimitErrorFormat, dcgm.MAX_NUM_CPU_CORES)
		}

		indices = append(indices, i)
	}

	return indices, nil
}

// deviceOptionsFlagValue returns the CLI selector that recreates options.
// NewApp uses it for help defaults, keeping displayed device choices aligned
// with the resolved configuration that startup will actually use.
func deviceOptionsFlagValue(options appconfig.DeviceOptions) string {
	if options.Flex {
		return FlexKey
	}

	selectors := make([]string, 0, 2)
	appendSelector := func(key string, indices []int) {
		if len(indices) == 0 {
			return
		}

		if len(indices) == 1 && indices[0] == -1 {
			selectors = append(selectors, key)
			return
		}

		values := make([]string, len(indices))
		for i, index := range indices {
			values[i] = strconv.Itoa(index)
		}

		selectors = append(selectors, key+":"+strings.Join(values, ","))
	}

	appendSelector(MajorKey, options.MajorRange)
	appendSelector(MinorKey, options.MinorRange)
	return strings.Join(selectors, "+")
}
