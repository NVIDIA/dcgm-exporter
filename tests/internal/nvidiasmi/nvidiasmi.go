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

// Package nvidiasmi runs nvidia-smi and parses its text for test suites
// without importing internal/e2e/capability (which pulls e2e exec probing).
package nvidiasmi

import (
	"fmt"
	"strings"
)

// FabricHealth reports whether nvidia-smi exposes Fabric state and whether
// every exposed section has reached Completed/Success.
func FabricHealth(output string) (reported, healthy bool, evidence string) {
	healthy, evidence = healthyFabricEvidence(output)
	return evidence != "", healthy, evidence
}

// healthyFabricEvidence summarizes meaningful Fabric sections and requires Completed/Success in every section.
func healthyFabricEvidence(output string) (bool, string) {
	inFabric := false
	fabricIndent := 0
	state := ""
	status := ""
	sections := 0
	healthySections := 0
	// Count each completed parse section before entering its sibling or leaving Fabric.
	finishSection := func() {
		if !inFabric || !fabricSectionIsPresent(state, status) {
			return
		}
		sections++
		if strings.EqualFold(state, "Completed") && strings.EqualFold(status, "Success") {
			healthySections++
		}
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if trimmed == "Fabric" {
			finishSection()
			inFabric = true
			fabricIndent = indent
			state = ""
			status = ""
			continue
		}
		if !inFabric || trimmed == "" {
			continue
		}
		if indent <= fabricIndent {
			finishSection()
			inFabric = false
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(key), "State"):
			state = strings.TrimSpace(value)
		case strings.EqualFold(strings.TrimSpace(key), "Status"):
			status = strings.TrimSpace(value)
		}
	}
	finishSection()
	if sections == 0 {
		return false, ""
	}
	if healthySections != sections {
		return false, fmt.Sprintf("Fabric health: %d/%d sections completed with Success", healthySections, sections)
	}
	return true, fmt.Sprintf("Fabric health: %d/%d sections completed with Success", healthySections, sections)
}

// fabricSectionIsPresent ignores sections whose state and status are both absent or N/A.
func fabricSectionIsPresent(state, status string) bool {
	return fabricValueMeaningful(state) || fabricValueMeaningful(status)
}

// fabricValueMeaningful distinguishes reported Fabric values from empty or placeholder values.
func fabricValueMeaningful(value string) bool {
	return !notReported(value)
}
