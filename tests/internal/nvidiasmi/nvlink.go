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

package nvidiasmi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	nvlinkGPUHeaderRE = regexp.MustCompile(`^GPU ([0-9]+): .*\(UUID: (GPU-[^)]+)\)\s*$`)
	nvlinkLinkLineRE  = regexp.MustCompile(`^Link ([0-9]+):\s*(.*)$`)
	nvlinkSpeedRE     = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*GB/s$`)
)

// NVLink is one GPU link that nvidia-smi reports as active.
type NVLink struct {
	GPU     int
	GPUUUID string
	Link    int
}

// ActiveNVLinks parses `nvidia-smi nvlink -s` output and returns every link
// with a positive speed, in output order. Links shown as inactive or with a
// zero speed are left out. A link line before any GPU header, or a GPU header
// without a UUID, is an error because the link could not be named.
func ActiveNVLinks(output string) ([]NVLink, error) {
	var links []NVLink
	var current *NVLink

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "GPU ") {
			gpu, err := parseNVLinkGPUHeader(trimmed)
			if err != nil {
				return nil, err
			}

			current = &gpu
			continue
		}

		match := nvlinkLinkLineRE.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}

		if current == nil {
			return nil, fmt.Errorf("nvidia-smi nvlink -s link line comes before any GPU line: %q", trimmed)
		}

		if !nvlinkSpeedIsPositive(match[2]) {
			continue
		}

		link, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("nvidia-smi nvlink -s link index %q on GPU %d: %w", match[1], current.GPU, err)
		}

		links = append(links, NVLink{GPU: current.GPU, GPUUUID: current.GPUUUID, Link: link})
	}

	return links, nil
}

// parseNVLinkGPUHeader reads the GPU index and UUID from one GPU header line
// of `nvidia-smi nvlink -s`.
func parseNVLinkGPUHeader(line string) (NVLink, error) {
	header := nvlinkGPUHeaderRE.FindStringSubmatch(line)
	if header == nil {
		return NVLink{}, fmt.Errorf("nvidia-smi nvlink -s GPU line has no index and UUID: %q", line)
	}

	index, err := strconv.Atoi(header[1])
	if err != nil {
		return NVLink{}, fmt.Errorf("nvidia-smi nvlink -s GPU index %q: %w", header[1], err)
	}

	return NVLink{GPU: index, GPUUUID: header[2]}, nil
}

// nvlinkSpeedIsPositive reports whether a link line's value is a speed above
// zero, such as "26.562 GB/s". Any other text, such as "<inactive>", is false.
func nvlinkSpeedIsPositive(value string) bool {
	match := nvlinkSpeedRE.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return false
	}

	speed, err := strconv.ParseFloat(match[1], 64)
	return err == nil && speed > 0
}
