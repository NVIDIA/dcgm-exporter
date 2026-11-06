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

// switchScaleNVLinkCount distinguishes switch-scale bonded topology from direct GPU links.
const switchScaleNVLinkCount = 8

// fabricSignals separates independent platform expectations from observed DCGM support.
type fabricSignals struct {
	fabricReported     bool
	switchConnected    bool
	switchScaleNVLinks int
	activeNVLink       string
	gb200              bool
	arm64              bool
	nvlink5            bool
}

// FabricHealth reports whether nvidia-smi exposes Fabric state and whether
// every exposed section has reached Completed/Success.
func FabricHealth(output string) (reported, healthy bool, evidence string) {
	healthy, evidence = healthyFabricEvidence(output)
	return evidence != "", healthy, evidence
}

// probeFabricSignals derives platform and protocol facts from lightweight
// nvidia-smi and CPU inventory output.
func probeFabricSignals(query, nvidiaQ, topo, nvlinkStatus, lscpu string) fabricSignals {
	_, fabricEvidence := healthyFabricEvidence(nvidiaQ)
	switchConnected, _ := switchConnectedEvidence(nvidiaQ)
	architecture := strings.ToLower(lscpu)
	return fabricSignals{
		fabricReported:     fabricEvidence != "",
		switchConnected:    switchConnected,
		switchScaleNVLinks: largestTopologyNVLinkCount(topo),
		activeNVLink:       activeNVLinkEvidence(nvlinkStatus),
		gb200:              strings.Contains(strings.ToUpper(query), "GB200"),
		arm64:              strings.Contains(architecture, "aarch64") || strings.Contains(architecture, "arm64"),
		nvlink5:            newestComputeCapabilityMajor(query) >= 10,
	}
}

// nvlinkPresent reports topology evidence without requiring healthy links.
func (s fabricSignals) nvlinkPresent() bool {
	return s.gb200 || s.fabricReported || s.switchConnected || s.switchScaleNVLinks > 0 || s.activeNVLink != ""
}

// nvswitchPresent reports switch-backed topology without requiring local telemetry.
func (s fabricSignals) nvswitchPresent() bool {
	return s.gb200 || s.switchConnected || s.switchScaleNVLinks >= switchScaleNVLinkCount
}

// providerEvidence identifies the local switch telemetry protocol contract.
func (s fabricSignals) providerEvidence() string {
	provider := "NSCQ"
	if s.nvlink5 {
		provider = "NVSDM"
	}
	architecture := "x86_64"
	if s.arm64 {
		architecture = "ARM64"
	}
	return "architecture=" + architecture + ", provider=" + provider
}

// evidence combines the host signals used to classify the fabric.
func (s fabricSignals) evidence(nvidiaQ, topo string) string {
	var evidence []string
	if _, item := switchConnectedEvidence(nvidiaQ); item != "" {
		evidence = append(evidence, item)
	}
	if _, item := healthyFabricEvidence(nvidiaQ); item != "" {
		evidence = append(evidence, item)
	}
	if s.gb200 {
		evidence = append(evidence, "GPU model: GB200")
	}
	if s.arm64 {
		evidence = append(evidence, "architecture: ARM64")
	}
	if s.switchScaleNVLinks > 0 {
		evidence = append(evidence, fmt.Sprintf("topology NV%d", s.switchScaleNVLinks))
	}
	if s.activeNVLink != "" {
		evidence = append(evidence, s.activeNVLink)
	}
	if len(evidence) != 0 {
		return strings.Join(evidence, "; ")
	}
	return firstLine(topo)
}

// newestComputeCapabilityMajor returns the highest compute-capability major in the GPU inventory.
func newestComputeCapabilityMajor(query string) int {
	newest := 0
	for _, line := range strings.Split(query, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) < 5 {
			continue
		}
		capability, err := strconv.ParseFloat(strings.TrimSpace(fields[4]), 64)
		if err == nil && int(capability) > newest {
			newest = int(capability)
		}
	}
	return newest
}

// healthyFabricEvidence requires every meaningful nvidia-smi Fabric section to be healthy.
func healthyFabricEvidence(output string) (bool, string) {
	inFabric := false
	fabricIndent := 0
	state := ""
	status := ""
	sections := 0
	healthySections := 0
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

// fabricSectionIsPresent reports whether a Fabric section carries non-placeholder state.
func fabricSectionIsPresent(state, status string) bool {
	return fabricValueMeaningful(state) || fabricValueMeaningful(status)
}

// fabricValueMeaningful ignores empty and N/A nvidia-smi Fabric values.
func fabricValueMeaningful(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return !strings.EqualFold(value, "N/A")
}

// switchConnectedEvidence detects a switch-connected peer type in GPU query output.
func switchConnectedEvidence(output string) (bool, string) {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Peer Type") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(value), "Switch Connected") {
			return true, "Peer Type: Switch Connected"
		}
	}
	return false, ""
}

// largestTopologyNVLinkCount returns the widest NVLink bond reported by topology output.
func largestTopologyNVLinkCount(topo string) int {
	largest := 0
	for _, token := range topologyNVLinkRE.FindAllString(topo, -1) {
		count, err := strconv.Atoi(strings.TrimPrefix(token, "NV"))
		if err == nil && count > largest {
			largest = count
		}
	}
	return largest
}

// probeOutputFailed reports whether inventory output contains a command failure instead of data.
func probeOutputFailed(outputs ...string) bool {
	for _, output := range outputs {
		lower := strings.ToLower(output)
		for _, marker := range []string{
			"command failed:",
			"nvidia-smi has failed",
			"failed to initialize",
			"couldn't communicate",
			"command not found",
			"executable file not found",
		} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}
