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
	"fmt"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// migCapability reports whether the GPU advertises any configurable MIG profile.
func migCapability(migProfiles string) Capability {
	switch {
	case hasMIGProfiles(migProfiles):
		return hostCapability(
			"mig",
			StatusSupported,
			"nvidia-smi mig -lgip",
			"GPU inventory advertises configurable MIG profiles",
			firstLine(migProfiles),
		)
	case migProfilesUnsupported(migProfiles):
		return hostCapability(
			"mig",
			StatusUnsupported,
			"nvidia-smi mig -lgip",
			"GPU inventory does not advertise MIG profiles",
			firstLine(migProfiles),
		)
	default:
		return hostCapability(
			"mig",
			StatusUnknown,
			"nvidia-smi mig -lgip",
			"MIG profile inventory could not be determined",
			firstLine(migProfiles),
		)
	}
}

// migModeCounts counts GPUs with MIG mode on and GPUs without it, from the
// mig.mode.current column of the nvidia-smi query. A GPU that does not report
// MIG as enabled counts as a full-sized GPU.
func migModeCounts(query string) (on, off int) {
	for _, line := range strings.Split(query, "\n") {
		fields := strings.Split(line, ",")
		if len(fields) < 6 || len(gpuIndexes(line)) == 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(fields[5]), "Enabled") {
			on++
		} else {
			off++
		}
	}
	return on, off
}

// migStateCapabilities reports the current MIG state for planning from the
// GPU counts with MIG on and off. mig_active is supported when a GPU has MIG on
// and DCGM lists a GPU instance. mixed_mig is supported when, in addition,
// another GPU has MIG off. The hierarchy text is the output of dcgmi discovery
// --compute-hierarchy and is read only when a GPU has MIG on.
func migStateCapabilities(query string, on, off int, hierarchy string) (active, mixed Capability) {
	const source = "nvidia-smi query and dcgmi discovery --compute-hierarchy"
	both := func(status Status, reason, evidence string) (Capability, Capability) {
		return hostCapability("mig_active", status, source, reason, evidence),
			hostCapability("mixed_mig", status, source, reason, evidence)
	}
	if probeOutputFailed(query) {
		return both(StatusUnknown, "GPU MIG mode could not be determined", firstLine(query))
	}
	evidence := fmt.Sprintf("%d GPU(s) with MIG on, %d with MIG off", on, off)
	if on == 0 {
		return both(StatusUnsupported, "MIG is off on every GPU", evidence)
	}
	if dcgmProbeUnavailable(hierarchy) {
		return both(StatusUnknown, dcgmProbeUnavailableReason("compute-hierarchy", hierarchy), firstLine(hierarchy))
	}
	if _, _, found := migInstanceSelectionPair(hierarchy); !found {
		return both(StatusUnsupported, "MIG is on but DCGM lists no GPU instance", evidence)
	}
	active = hostCapability("mig_active", StatusSupported, source, "MIG is on and DCGM lists a GPU instance", evidence)
	if off == 0 {
		mixed = hostCapability("mixed_mig", StatusUnsupported, source, "MIG is on for every GPU; mixed MIG needs a GPU with MIG off", evidence)
		return active, mixed
	}
	mixed = hostCapability("mixed_mig", StatusSupported, source, "one GPU has MIG on with an instance while another has MIG off", evidence)
	return active, mixed
}

// migInstanceSelectionPair extracts the DCGM entity ID and NVML instance ID of
// the first GPU instance in compute hierarchy output. migInstanceRE captures
// both IDs as digits, so found means both are present.
func migInstanceSelectionPair(output string) (entityID, nvmlID string, found bool) {
	for _, line := range strings.Split(output, "\n") {
		match := migInstanceRE.FindStringSubmatch(line)
		if len(match) == 3 {
			return match[2], match[1], true
		}
	}
	return "", "", false
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

// migProfilesUnsupported recognizes an authoritative no-MIG response.
func migProfilesUnsupported(output string) bool {
	lower := strings.ToLower(strings.TrimSpace(output))
	return strings.Contains(lower, "no mig-supported") ||
		strings.Contains(lower, "not supported")
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
