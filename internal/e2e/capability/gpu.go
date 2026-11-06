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
	if probeOutputFailed(query) {
		return hostCapability(
			"gpu",
			StatusUnknown,
			"nvidia-smi query",
			"GPU inventory probe failed",
			firstLine(query),
		)
	}
	return hostCapability(
		"gpu",
		statusIf(gpuCount > 0),
		"nvidia-smi query",
		reasonIf(gpuCount > 0, "nvidia-smi returned GPU inventory", "nvidia-smi could not return GPU inventory"),
		firstLine(query),
	)
}

// multiGPUApplicability reports whether scenarios that compare GPU peers apply.
func multiGPUApplicability(gpuCount int) Capability {
	if gpuCount < 2 {
		return hostCapability(
			"multi_gpu",
			StatusUnsupported,
			"nvidia-smi query",
			"scenario requires at least two GPUs",
			fmt.Sprintf("%d GPU(s)", gpuCount),
		)
	}
	return hostCapability(
		"multi_gpu",
		StatusSupported,
		"nvidia-smi query",
		"scenario can compare at least two GPUs",
		fmt.Sprintf("%d GPUs", gpuCount),
	)
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
