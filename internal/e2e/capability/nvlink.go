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

// HasActiveNVLink reports whether nvidia-smi exposes at least one active link.
func HasActiveNVLink(output string) bool {
	return nvlinkBandwidthRE.MatchString(output)
}

// nvlinkFabricCapability reports physical NVLink hardware without checking health or counters.
func nvlinkFabricCapability(fabric fabricSignals, nvidiaQ, topo, nvlinkStatus string) Capability {
	if probeOutputFailed(nvidiaQ, topo) {
		return hostCapability(
			"nvlink_fabric",
			StatusUnknown,
			"nvidia-smi topology and fabric inventory",
			"NVLink hardware inventory could not be determined",
			firstLine(nvidiaQ+"\n"+topo+"\n"+nvlinkStatus),
		)
	}
	if fabric.nvlinkPresent() {
		return hostCapability(
			"nvlink_fabric",
			StatusSupported,
			"nvidia-smi topology and fabric inventory",
			"host advertises NVLink fabric hardware",
			fabric.evidence(nvidiaQ, topo),
		)
	}
	return hostCapability(
		"nvlink_fabric",
		StatusUnsupported,
		"nvidia-smi topology and fabric inventory",
		"host does not advertise NVLink fabric hardware",
		firstLine(topo),
	)
}

// activeNVLinkEvidence returns the first active link reported by nvidia-smi.
func activeNVLinkEvidence(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if nvlinkBandwidthRE.MatchString(line) {
			return "active NVLink: " + strings.TrimSpace(line)
		}
	}
	return ""
}

// nvlink5Applicability reports whether scenarios for the NVLink 5 field contract apply.
func nvlink5Applicability(query string) Capability {
	major := newestComputeCapabilityMajor(query)
	if major == 0 {
		return hostCapability(
			"nvlink5",
			StatusUnknown,
			"nvidia-smi compute capability",
			"GPU protocol generation could not be determined",
			firstLine(query),
		)
	}
	if major < 10 {
		return hostCapability(
			"nvlink5",
			StatusUnsupported,
			"nvidia-smi compute capability",
			"scenario requires an NVLink 5 generation GPU",
			firstLine(query),
		)
	}
	return hostCapability(
		"nvlink5",
		StatusSupported,
		"nvidia-smi compute capability",
		"GPU generation uses the NVLink 5 field contract",
		firstLine(query),
	)
}
