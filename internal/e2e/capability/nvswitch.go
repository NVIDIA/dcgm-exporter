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

// nvswitchCapability reports physical switch-backed topology without requiring DCGM telemetry.
func nvswitchCapability(fabric fabricSignals, nvidiaQ, topo string) Capability {
	if fabric.nvswitchPresent() {
		return hostCapability(
			"nvswitch",
			StatusSupported,
			"nvidia-smi topology and fabric inventory",
			"host advertises NVSwitch-backed topology",
			fabric.evidence(nvidiaQ, topo),
		)
	}
	if probeOutputFailed(nvidiaQ, topo) {
		return hostCapability(
			"nvswitch",
			StatusUnknown,
			"nvidia-smi topology and fabric inventory",
			"NVSwitch hardware inventory could not be determined",
			firstLine(nvidiaQ+"\n"+topo),
		)
	}
	return hostCapability(
		"nvswitch",
		StatusUnsupported,
		"nvidia-smi topology and fabric inventory",
		"host does not advertise NVSwitch-backed topology",
		firstLine(topo),
	)
}

// nvswitchApplicability reports the local provider contracts available to switch scenarios.
func nvswitchApplicability(
	inventory Capability,
	fabric fabricSignals,
) (Capability, Capability, Capability) {
	local := inheritedApplicability("nvswitch_local_telemetry", inventory)
	if inventory.Status == StatusSupported {
		if fabric.nvlink5 && fabric.arm64 {
			local = hostCapability(
				"nvswitch_local_telemetry",
				StatusUnsupported,
				"CPU architecture and NVLink generation",
				"local NVSDM switch telemetry is not published for ARM/SBSA compute nodes",
				"architecture=ARM64, provider=NVSDM",
			)
		} else {
			local = hostCapability(
				"nvswitch_local_telemetry",
				StatusSupported,
				"CPU architecture and NVLink generation",
				"platform supports a local NVSwitch telemetry provider",
				fabric.providerEvidence(),
			)
		}
	}

	nscq := providerApplicability(
		"nvswitch_nscq",
		local,
		!fabric.nvlink5,
		"legacy NSCQ NVSwitch field contract",
	)
	nvsdm := providerApplicability(
		"nvswitch_nvsdm",
		local,
		fabric.nvlink5,
		"NVSDM NVSwitch field contract",
	)
	return local, nscq, nvsdm
}

// inheritedApplicability carries a coarse feature result into a dependent applicability gate.
func inheritedApplicability(name string, feature Capability) Capability {
	return Capability{
		Name:     "host:" + name,
		Status:   feature.Status,
		Source:   feature.Source,
		Reason:   feature.Reason,
		Evidence: feature.Evidence,
	}
}

// providerApplicability identifies which switch field contract applies to the host.
func providerApplicability(name string, local Capability, applies bool, contract string) Capability {
	if local.Status != StatusSupported {
		return inheritedApplicability(name, local)
	}
	if !applies {
		return hostCapability(
			name,
			StatusUnsupported,
			"NVLink protocol generation",
			"scenario requires the "+contract,
			local.Evidence,
		)
	}
	return hostCapability(
		name,
		StatusSupported,
		"NVLink protocol generation",
		"platform uses the "+contract,
		local.Evidence,
	)
}
