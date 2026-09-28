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

package capability

import (
	"fmt"
	"strconv"
	"strings"
)

// gpuCapability reports whether nvidia-smi returned any usable GPU inventory.
func gpuCapability(query string, gpuCount int) Capability {
	return hostCapability(
		"gpu",
		statusIf(gpuCount > 0),
		"nvidia-smi query",
		reasonIf(gpuCount > 0, "nvidia-smi returned GPU inventory", "nvidia-smi could not return GPU inventory"),
		firstLine(query),
	)
}

// p2pCapability reports whether the host has enough NVLink-backed GPUs for P2P status checks.
func p2pCapability(gpuCount int, activeNVLink bool) Capability {
	return hostCapability("p2p", p2pStatus(gpuCount, activeNVLink), "nvidia-smi query", p2pReason(gpuCount, activeNVLink), fmt.Sprintf("%d GPU(s)", gpuCount))
}

// GPUCount counts valid GPU rows in nvidia-smi CSV query output.
func GPUCount(query string) int {
	return len(gpuIndexes(query))
}

// gpuIndexes extracts GPU indexes from nvidia-smi CSV query output.
func gpuIndexes(query string) []string {
	var indexes []string
	for _, line := range strings.Split(query, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if line == "" || strings.Contains(lower, "failed") || strings.Contains(lower, "error") || !strings.Contains(line, ",") {
			continue
		}
		fields := strings.Split(line, ",")
		if _, err := strconv.Atoi(strings.TrimSpace(fields[0])); err == nil {
			indexes = append(indexes, strings.TrimSpace(fields[0]))
		}
	}
	return indexes
}

// fullGPUCount counts GPUs whose current MIG mode still exposes a full GPU.
func fullGPUCount(query string) int {
	count := 0
	for _, line := range strings.Split(query, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) < 6 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(fields[0])); err != nil {
			continue
		}
		migMode := strings.Trim(strings.TrimSpace(fields[5]), "[]")
		if !strings.EqualFold(migMode, "enabled") {
			count++
		}
	}
	return count
}

// p2pStatus requires at least two GPUs and active NVLink evidence.
func p2pStatus(gpuCount int, activeNVLink bool) Status {
	if gpuCount < 2 || !activeNVLink {
		return StatusUnsupported
	}
	return StatusSupported
}

// p2pReason explains the P2P capability decision.
func p2pReason(gpuCount int, activeNVLink bool) string {
	if gpuCount < 2 {
		return "P2P status validation requires at least two GPUs"
	}
	if !activeNVLink {
		return "P2P status validation requires active NVLink evidence"
	}
	return "multi-GPU active NVLink evidence was detected for P2P status validation"
}
