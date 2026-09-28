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
	"regexp"
	"strings"
)

var (
	ansiRE            = regexp.MustCompile(`\x1b\[[0-9;]*[[:alpha:]]`)
	versionRE         = regexp.MustCompile(`[0-9]+[.][0-9]+[.][0-9]+([.][0-9]+)?`)
	nvlinkBandwidthRE = regexp.MustCompile(`Link [0-9]+:[[:space:]]*[0-9]+(\.[0-9]+)?[[:space:]]*GB/s`)
	topologyNVLinkRE  = regexp.MustCompile(`\bNV[0-9]+\b`)
	migInstanceRE     = regexp.MustCompile(`I[[:space:]]+[0-9]+/([0-9]+).*GPU Instance \(EntityID:[[:space:]]*([0-9]+)\)`)
	migDeviceLineRE   = regexp.MustCompile(`(?i)^[[:space:]]*MIG[[:space:]].*[[:space:]]Device[[:space:]]+[0-9]+:[[:space:]]*\(UUID:[[:space:]]*MIG-`)
	fieldIDRE         = regexp.MustCompile(`\b[0-9]+\b`)
	profilingRE       = regexp.MustCompile(`(?i)DCGM_FI_PROF_[A-Z0-9_]+|Profiling metrics|metric group|gr_engine_active|sm_active|sm_occupancy|tensor_active|dram_active|nvlink(_l[0-9]+)?_(tx|rx)_bytes|c2c_(tx|rx)_[[:alnum:]_]+`)
	nvswitchRE        = regexp.MustCompile(`(?i)NVSwitch|NvSwitch`)
	graceRE           = regexp.MustCompile(`(?i)Grace|FE_CPU|CPU Core|CPU hierarchy`)
	c2cFieldRE        = regexp.MustCompile(`(?i)DCGM_FI_(DEV|PROF)_C2C|C2C.*field|field.*C2C|(^|[[:space:]|])c2c_[[:alnum:]_]+`)
)

// hostCapability prefixes a host-scoped capability name and preserves probe evidence.
func hostCapability(name string, status Status, source, reason, evidence string) Capability {
	return Capability{Name: "host:" + name, Status: status, Source: source, Reason: reason, Evidence: evidence}
}

// statusIf maps a boolean probe result to supported or unsupported.
func statusIf(ok bool) Status {
	if ok {
		return StatusSupported
	}
	return StatusUnsupported
}

// reasonIf chooses the supported or unsupported reason text.
func reasonIf(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

// firstLine returns the first non-empty output line for compact evidence.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// firstLineOr returns fallback when output has no non-empty lines.
func firstLineOr(text, fallback string) string {
	if line := firstLine(text); line != "" {
		return line
	}
	return fallback
}

// positiveEvidence finds a matching evidence line while ignoring known negative phrasing.
func positiveEvidence(output string, pattern *regexp.Regexp, absence ...string) bool {
	for _, line := range strings.Split(output, "\n") {
		lower := strings.ToLower(line)
		skip := false
		for _, marker := range absence {
			if strings.Contains(lower, marker) {
				skip = true
				break
			}
		}
		if !skip && pattern.MatchString(line) {
			return true
		}
	}
	return false
}

// stripANSI removes terminal color/control escapes from command output.
func stripANSI(text string) string {
	return ansiRE.ReplaceAllString(text, "")
}
