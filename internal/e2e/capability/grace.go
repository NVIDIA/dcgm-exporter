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

// graceCapability reports Grace CPU support from DCGM hierarchy evidence with lscpu as a hint.
func graceCapability(lscpu, discovery string) Capability {
	switch {
	case graceEvidence(discovery):
		return hostCapability("grace_cpu", StatusSupported, "DCGM container discovery -l", "DCGM reported Grace CPU hierarchy evidence", firstLine(discovery))
	case strings.Contains(strings.ToLower(lscpu), "grace"):
		return hostCapability("grace_cpu", StatusUnknown, "DCGM container discovery -l/lscpu", "Grace CPU detected, but "+dcgmProbeUnavailableReason("cpu-hierarchy", discovery), firstLine(lscpu))
	default:
		return hostCapability("grace_cpu", StatusUnsupported, "DCGM container discovery -l/lscpu", "Grace CPU or DCGM CPU hierarchy evidence was not detected", firstLine(lscpu))
	}
}

// graceEvidence finds positive Grace CPU hierarchy evidence while ignoring failure text.
func graceEvidence(output string) bool {
	return positiveEvidence(output, graceRE, "not found", "connection refused", "could not", "failed", "error", "no cpu", "not cpu", "unsupported")
}
