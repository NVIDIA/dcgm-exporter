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

import "strings"

// c2cCapability reports whether host and DCGM evidence can support C2C scenarios.
func c2cCapability(nvidiaQ, discovery, profile, fields string) Capability {
	if c2cEnabled(nvidiaQ) && c2cFieldEvidence(discovery, profile, fields) {
		return hostCapability("c2c", StatusSupported, "nvidia-smi -q/DCGM container discovery/profile/fields", "GPU C2C mode is enabled and DCGM reported C2C field evidence", firstLine(discovery+"\n"+profile+"\n"+fields))
	}
	if c2cEnabled(nvidiaQ) {
		return hostCapability("c2c", StatusUnknown, "nvidia-smi -q/DCGM container probe", "GPU C2C mode is enabled, but "+dcgmProbeUnavailableReason("c2c-field", fields), firstLine(nvidiaQ))
	}
	return hostCapability("c2c", StatusUnsupported, "nvidia-smi -q", "GPU C2C mode is not enabled", "")
}

// c2cEnabled detects enabled GPU C2C mode in nvidia-smi -q output.
func c2cEnabled(nvidiaQ string) bool {
	for _, line := range strings.Split(nvidiaQ, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "gpu c2c mode") && strings.Contains(lower, "enabled") {
			return true
		}
	}
	return false
}

// c2cFieldEvidence searches DCGM outputs for C2C field or metric evidence.
func c2cFieldEvidence(values ...string) bool {
	return positiveEvidence(strings.Join(values, "\n"), c2cFieldRE, "not found", "connection refused", "could not", "failed", "error", "no c2c", "not c2c", "unsupported")
}
