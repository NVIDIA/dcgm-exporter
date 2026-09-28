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

// nvswitchCapability reports whether DCGM discovery found NVSwitch entities.
func nvswitchCapability(discovery string) Capability {
	switch {
	case nvswitchEvidence(discovery):
		return hostCapability("nvswitch", StatusSupported, "DCGM container discovery -l", "DCGM reported NVSwitch entities", firstLine(discovery))
	case dcgmProbeUnavailable(discovery):
		return hostCapability("nvswitch", StatusUnknown, "DCGM container discovery -l", dcgmProbeUnavailableReason("switch-discovery", discovery), firstLine(discovery))
	default:
		return hostCapability("nvswitch", StatusUnsupported, "DCGM container discovery -l", "DCGM did not report NVSwitch entities", firstLine(discovery))
	}
}

// nvswitchEvidence finds positive NVSwitch evidence while ignoring negative wording.
func nvswitchEvidence(output string) bool {
	return positiveEvidence(output, nvswitchRE, "no nvswitch", "not nvswitch", "0 nvswitch")
}
