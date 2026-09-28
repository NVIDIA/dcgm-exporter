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

// remoteDcgmCapability validates that the configured DCGM image can run nv-hostengine and discover GPUs.
func remoteDcgmCapability(opts ProbeOptions, versionOutput, discovery string) Capability {
	name := "dcgm:remote_dcgm"
	switch {
	case opts.DCGMImage == "":
		return Capability{Name: name, Status: StatusUnsupported, Source: "DCGM image configuration", Reason: "remote DCGM requires a configured DCGM image"}
	case opts.DryRun:
		return Capability{Name: name, Status: StatusUnknown, Source: "DCGM image availability check", Reason: "dry-run verified the DCGM image is accessible but did not run nv-hostengine for version verification", Evidence: opts.DCGMImage}
	case dcgmProbeUnavailable(versionOutput):
		return Capability{Name: name, Status: StatusUnsupported, Source: "nv-hostengine --version", Reason: "DCGM image is unavailable or does not contain matching /usr/bin/nv-hostengine", Evidence: firstLine(versionOutput)}
	}

	version := dcgmVersionFromOutput(versionOutput)
	if opts.ExpectedDCGMVersion != "" && version == "" {
		return Capability{Name: name, Status: StatusUnsupported, Source: "nv-hostengine --version", Reason: "remote DCGM version could not be parsed from nv-hostengine output", Evidence: firstLine(versionOutput)}
	}
	if opts.ExpectedDCGMVersion != "" && version != opts.ExpectedDCGMVersion {
		return Capability{Name: name, Status: StatusUnsupported, Source: "nv-hostengine --version", Reason: "remote DCGM version " + version + " does not match required " + opts.ExpectedDCGMVersion, Evidence: firstLine(versionOutput)}
	}
	if dcgmProbeUnavailable(discovery) {
		return Capability{Name: name, Status: StatusUnsupported, Source: "dcgmi discovery -l", Reason: "remote DCGM image matched " + expectedVersionText(opts.ExpectedDCGMVersion) + ", but did not discover GPUs through nv-hostengine", Evidence: firstLine(discovery)}
	}
	return Capability{Name: name, Status: StatusSupported, Source: "dcgmi discovery -l", Reason: "remote DCGM image contains matching " + expectedVersionText(opts.ExpectedDCGMVersion) + " and discovers GPUs through nv-hostengine", Evidence: firstLine(discovery)}
}

// failureInjectionCapability gates injection scenarios on a verified remote DCGM image.
func failureInjectionCapability(opts ProbeOptions, remote Capability) Capability {
	name := "dcgm:failure_injection"
	if !opts.FailureInjection {
		return Capability{Name: name, Status: StatusUnsupported, Source: "feature flag", Reason: "DCGM failure injection is disabled"}
	}
	if remote.Status != StatusSupported {
		return Capability{Name: name, Status: StatusUnsupported, Source: "remote DCGM", Reason: "failure injection requires a verified remote DCGM image", Evidence: remote.Reason}
	}
	return Capability{Name: name, Status: StatusSupported, Source: "dcgmi test --inject", Reason: "DCGM image provides dcgmi field injection", Evidence: remote.Reason}
}

// dcgmVersionFromOutput extracts the first version-looking token from nv-hostengine output.
func dcgmVersionFromOutput(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(strings.ToLower(line), "version") {
			if match := versionRE.FindString(line); match != "" {
				return match
			}
		}
	}
	return versionRE.FindString(output)
}

// expectedVersionText formats the required version phrase used in capability reasons.
func expectedVersionText(version string) string {
	if version == "" {
		return "the configured version"
	}
	return version
}
