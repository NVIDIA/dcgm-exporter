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

// dcgmFieldSupport records the resolved DCGM field name and numeric field ID.
type dcgmFieldSupport struct {
	Name string
	ID   string
}

// unsupportedFieldCapability reports whether a DCGM field can exercise unsupported-field handling.
func unsupportedFieldCapability(fields, candidate, evidence string) Capability {
	switch {
	case candidate != "":
		return hostCapability("unsupported_field", StatusSupported, "DCGM container dmon", "DCGM reported an unsupported field sentinel for "+candidate, evidence)
	case dcgmProbeUnavailable(fields):
		return hostCapability("unsupported_field", StatusUnknown, "DCGM container dmon -l", dcgmProbeUnavailableReason("unsupported-field", fields), firstLine(fields))
	default:
		return hostCapability("unsupported_field", StatusUnsupported, "DCGM container dmon", "DCGM did not report an unsupported sentinel for the candidate fields", strings.Join(unsupportedFieldCandidates(), ","))
	}
}

// selectUnsupportedFieldCandidate probes likely retired/ECC fields until DCGM returns an unsupported sentinel.
func selectUnsupportedFieldCandidate(ctx context.Context, runner e2eexec.Runner, opts ProbeOptions, fields string) (string, string) {
	if opts.DryRun || opts.DCGMImage == "" {
		return "", ""
	}
	for _, candidate := range unsupportedFieldCandidates() {
		field := resolveDCGMFieldSupport(fields, candidate, "")
		if field.ID == "" {
			continue
		}
		output := dcgmContainerProbe(ctx, runner, opts, "dmon", "-e", field.ID, "-c", "1")
		if dcgmFieldProbeReportsUnsupported(output) {
			return candidate, "field_id=" + field.ID + "; " + firstLine(output)
		}
	}
	return "", ""
}

// resolveDCGMFieldSupport finds a field ID from dcgmi output or falls back to a known ID.
func resolveDCGMFieldSupport(fields, name, fallbackID string) dcgmFieldSupport {
	if id := dcgmFieldIDFromList(fields, name); id != "" {
		return dcgmFieldSupport{Name: name, ID: id}
	}
	if fallbackID != "" {
		return dcgmFieldSupport{Name: name, ID: fallbackID}
	}
	return dcgmFieldSupport{Name: name}
}

// dmonSource describes the exact dmon probe used for result evidence.
func (field dcgmFieldSupport) dmonSource() string {
	if field.ID == "" {
		return "DCGM container dmon"
	}
	return "DCGM container dmon -e " + field.ID
}

// label formats a field name with its numeric ID when available.
func (field dcgmFieldSupport) label() string {
	if field.ID == "" {
		return field.Name
	}
	return field.Name + " (" + field.ID + ")"
}

// unsupportedFieldCandidates lists DCGM fields that commonly expose sentinel values on newer GPUs.
func unsupportedFieldCandidates() []string {
	return []string{
		"DCGM_FI_DEV_ECC_SBE_VOL_TOTAL",
		"DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
		"DCGM_FI_DEV_RETIRED_SBE",
		"DCGM_FI_DEV_RETIRED_DBE",
		"DCGM_FI_DEV_RETIRED_PENDING",
		"DCGM_FI_DEV_POWER_VIOLATION",
		"DCGM_FI_DEV_THERMAL_VIOLATION",
	}
}

// dcgmFieldIDFromList extracts a numeric field ID from dcgmi dmon -l output.
func dcgmFieldIDFromList(output, field string) string {
	for _, line := range strings.Split(output, "\n") {
		if !dcgmFieldLineMatches(line, field) {
			continue
		}
		return fieldIDRE.FindString(line)
	}
	return ""
}

// dcgmFieldLineMatches recognizes a field line using canonical and display-name tags.
func dcgmFieldLineMatches(line, field string) bool {
	lower := strings.ToLower(line)
	for _, tag := range dcgmFieldTags(field) {
		if strings.Contains(lower, tag) {
			return true
		}
	}
	return false
}

// dcgmFieldTag normalizes a DCGM field constant into a lowercase search tag.
func dcgmFieldTag(field string) string {
	field = strings.TrimPrefix(field, "DCGM_FI_DEV_")
	field = strings.TrimPrefix(field, "DCGM_FI_")
	return strings.ToLower(field)
}

// dcgmFieldTags returns alternate tags seen across DCGM field-list output.
func dcgmFieldTags(field string) []string {
	switch field {
	case "DCGM_FI_DEV_ECC_SBE_VOL_TOTAL":
		return []string{dcgmFieldTag(field), "ecc_sbe_volatile_total"}
	case "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL":
		return []string{dcgmFieldTag(field), "ecc_dbe_volatile_total"}
	case "DCGM_FI_DEV_RETIRED_SBE":
		return []string{dcgmFieldTag(field), "retired_pages_sbe"}
	case "DCGM_FI_DEV_RETIRED_DBE":
		return []string{dcgmFieldTag(field), "retired_pages_dbe"}
	case "DCGM_FI_DEV_RETIRED_PENDING":
		return []string{dcgmFieldTag(field), "retired_pages_pending"}
	default:
		return []string{dcgmFieldTag(field)}
	}
}

// dcgmFieldProbeReportsUnsupported detects sentinel and textual unsupported-field responses.
func dcgmFieldProbeReportsUnsupported(output string) bool {
	lower := strings.ToLower(output)
	return dcgmSentinelEvidence(output) != "" ||
		strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "not_supported") ||
		strings.Contains(lower, "not-supported") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "not permissioned") ||
		strings.Contains(lower, "blank") ||
		strings.Contains(lower, "n/a")
}

// dcgmSentinelEvidence names known DCGM integer sentinel values found in probe output.
func dcgmSentinelEvidence(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "dcgm_ft_int64_not_supported") || strings.Contains(output, "9223372036854775794"):
		return "DCGM_FT_INT64_NOT_SUPPORTED"
	case strings.Contains(lower, "dcgm_ft_int64_not_found") || strings.Contains(output, "9223372036854775793"):
		return "DCGM_FT_INT64_NOT_FOUND"
	case strings.Contains(lower, "dcgm_ft_int64_not_permissioned") || strings.Contains(output, "9223372036854775795"):
		return "DCGM_FT_INT64_NOT_PERMISSIONED"
	case strings.Contains(lower, "dcgm_ft_int64_blank") || strings.Contains(output, "9223372036854775792"):
		return "DCGM_FT_INT64_BLANK"
	default:
		return ""
	}
}

// dcgmDmonHasRealSample reports whether dmon returned a concrete sample instead of a sentinel or n/a.
func dcgmDmonHasRealSample(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "GPU" {
			continue
		}
		value := strings.ToLower(fields[len(fields)-1])
		if value != "n/a" && dcgmSentinelEvidence(value) == "" {
			return true
		}
	}
	return false
}
