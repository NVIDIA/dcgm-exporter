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
	"context"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// migCapability reports whether MIG-backed scenarios can run or be configured.
func migCapability(migPresent, migProfilesAvailable, migCanConfigure bool, migProfiles, migInstance string) Capability {
	return hostCapability(
		"mig",
		migStatus(migPresent, migProfilesAvailable, migCanConfigure),
		migSource(migPresent),
		migReason(migPresent, migProfilesAvailable, migCanConfigure),
		migEvidence(migInstance, migProfiles),
	)
}

// mixedMIGCapability reports whether the host can offer both MIG and full-GPU devices.
func mixedMIGCapability(migPresent, migProfilesAvailable, migCanConfigure bool, fullGPUCount int, migProfiles, migInstance string) Capability {
	return hostCapability(
		"mixed_mig",
		mixedMIGStatus(migPresent, migProfilesAvailable, migCanConfigure, fullGPUCount),
		migSource(migPresent),
		mixedMIGReason(migPresent, migProfilesAvailable, migCanConfigure, fullGPUCount),
		migEvidence(migInstance, migProfiles),
	)
}

// migInstanceCapability records whether DCGM exposed a selectable MIG instance entity.
func migInstanceCapability(migAvailable bool, hierarchy, entityID, nvmlID string) Capability {
	switch {
	case entityID != "" && nvmlID != "":
		return hostCapability("mig_instance_entity", StatusSupported, "DCGM container discovery --compute-hierarchy", "DCGM reported a selectable MIG GPU instance entity", "entity_id="+entityID+", nvml_instance_id="+nvmlID)
	case migAvailable && dcgmProbeUnavailable(hierarchy):
		return hostCapability("mig_instance_entity", StatusUnknown, "DCGM container discovery --compute-hierarchy", dcgmProbeUnavailableReason("mig-hierarchy", hierarchy), firstLine(hierarchy))
	case migAvailable:
		return hostCapability("mig_instance_entity", StatusUnsupported, "DCGM container discovery --compute-hierarchy", "DCGM did not report a selectable MIG GPU instance entity", firstLine(hierarchy))
	default:
		return hostCapability("mig_instance_entity", StatusUnsupported, "DCGM container discovery --compute-hierarchy", "MIG instances are not available", firstLine(hierarchy))
	}
}

// migInstanceSelectionPair extracts DCGM entity ID and NVML instance ID from compute hierarchy output.
func migInstanceSelectionPair(output string) (string, string) {
	for _, line := range strings.Split(output, "\n") {
		match := migInstanceRE.FindStringSubmatch(line)
		if len(match) == 3 {
			return match[2], match[1]
		}
	}
	return "", ""
}

// migInstanceLine returns the first actual MIG device line from nvidia-smi -L output.
func migInstanceLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if migDeviceLineRE.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// hasMIGProfiles reports whether nvidia-smi listed any usable MIG profile.
func hasMIGProfiles(output string) bool {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	return !strings.Contains(lower, "no mig-supported") &&
		!strings.Contains(lower, "not supported") &&
		!strings.Contains(lower, "failed") &&
		!strings.Contains(lower, "couldn't communicate") &&
		!strings.Contains(lower, "error")
}

// runMIGProfileProbe checks per-GPU MIG profiles before falling back to the legacy all-GPU query.
func runMIGProfileProbe(ctx context.Context, runner e2eexec.Runner, query string) string {
	var firstOutput string
	for _, index := range gpuIndexes(query) {
		output := runText(ctx, runner, "nvidia-smi", "mig", "-i", index, "-lgip")
		if hasMIGProfiles(output) {
			return output
		}
		if firstOutput == "" && strings.TrimSpace(output) != "" {
			firstOutput = output
		}
	}

	output := runText(ctx, runner, "nvidia-smi", "mig", "-lgip")
	if strings.TrimSpace(output) != "" {
		return output
	}
	return firstOutput
}

// migStatus maps observed or configurable MIG state to a capability status.
func migStatus(migPresent, migProfilesAvailable, migCanConfigure bool) Status {
	if migPresent || (migProfilesAvailable && migCanConfigure) {
		return StatusSupported
	}
	return StatusUnsupported
}

// migReason explains how MIG availability was determined.
func migReason(migPresent, migProfilesAvailable, migCanConfigure bool) string {
	switch {
	case migPresent:
		return "MIG instances are present"
	case migProfilesAvailable && migCanConfigure:
		return "MIG can be configured by explicit request"
	case migProfilesAvailable:
		return "MIG profiles are available, but MIG instances are not present; use --mig-configure true or a profile id to allow host mutation"
	default:
		return "MIG instances or profiles were not detected"
	}
}

// mixedMIGStatus requires MIG availability plus at least one additional full GPU.
func mixedMIGStatus(migPresent, migProfilesAvailable, migCanConfigure bool, fullGPUCount int) Status {
	if (migPresent && fullGPUCount >= 1) || (!migPresent && migProfilesAvailable && migCanConfigure && fullGPUCount >= 2) {
		return StatusSupported
	}
	return StatusUnsupported
}

// mixedMIGReason explains why mixed MIG/full-GPU selection is or is not possible.
func mixedMIGReason(migPresent, migProfilesAvailable, migCanConfigure bool, fullGPUCount int) string {
	if migPresent && fullGPUCount >= 1 {
		return "MIG instances are present with at least one full GPU for full-GPU selection"
	}
	if !migPresent && migProfilesAvailable && migCanConfigure && fullGPUCount >= 2 {
		return "MIG can be configured on one GPU while another GPU remains full"
	}
	if migPresent {
		return "MIG is available, but no full GPU was detected for mixed MIG device selection"
	}
	if migProfilesAvailable && migCanConfigure {
		return "MIG can be configured, but mixed MIG selection requires at least two full GPUs before configuration"
	}
	if migProfilesAvailable {
		return "MIG profiles are available, but mixed MIG selection requires existing MIG instances or --mig-configure true/profile"
	}
	return "Mixed MIG device selection requires MIG profiles and at least two GPUs"
}

// migSource describes which nvidia-smi probe supplied MIG capability evidence.
func migSource(migPresent bool) string {
	if migPresent {
		return "nvidia-smi -L"
	}
	return "nvidia-smi mig -i <gpu> -lgip"
}

// migEvidence prefers an observed MIG instance over profile-only evidence.
func migEvidence(migInstance, migProfiles string) string {
	if migInstance != "" {
		return migInstance
	}
	return firstLineOr(migProfiles, "No MIG-supported devices found.")
}

// boolModeRequired treats explicit true-like feature modes as permission to mutate host state.
func boolModeRequired(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto", "false", "0", "no", "off":
		return false
	default:
		return true
	}
}
