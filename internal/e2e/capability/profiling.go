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

// profilingCapability reports whether DCGM exposes profiling metric groups.
func profilingCapability(output string) Capability {
	switch {
	case profilingEvidence(output):
		return hostCapability("profiling", StatusSupported, "DCGM container profile --list", "DCGM reported profiling metric groups", firstLine(output))
	case dcgmProbeUnavailable(output):
		return hostCapability("profiling", StatusUnknown, "DCGM container profile --list", dcgmProbeUnavailableReason("profiling", output), firstLine(output))
	default:
		return hostCapability("profiling", StatusUnsupported, "DCGM container profile --list", "DCGM did not report profiling metric groups", firstLine(output))
	}
}

// profilingEvidence finds positive profiling evidence while ignoring unsupported wording.
func profilingEvidence(output string) bool {
	return positiveEvidence(output, profilingRE, "no metric group", "not metric group", "not supported", "unsupported")
}
