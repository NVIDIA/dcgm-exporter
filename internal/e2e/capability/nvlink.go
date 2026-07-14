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

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const (
	nvlinkCRCFieldName       = "DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL"
	nvlinkCRCFieldFallbackID = "409"
)

// nvlinkCapability reports whether host topology output shows active NVLink.
func nvlinkCapability(active bool, topo string) Capability {
	return hostCapability(
		"nvlink",
		statusIf(active),
		"nvidia-smi nvlink/topo",
		reasonIf(active, "active NVLink evidence was detected", "active NVLink evidence was not detected"),
		firstLine(topo),
	)
}

// HasActiveNVLink reports whether nvlink/topology output shows active NVLink.
func HasActiveNVLink(nvlink, topo string) bool {
	return nvlinkBandwidthRE.MatchString(nvlink) || topologyNVLinkRE.MatchString(topo)
}

// nvlinkFailureInjectionCapability verifies the DCGM NVLink CRC field needed by injection scenarios.
func nvlinkFailureInjectionCapability(ctx context.Context, runner e2eexec.Runner, opts ProbeOptions, injection Capability, fields string) Capability {
	name := "dcgm:failure_injection_nvlink_crc"
	if injection.Status != StatusSupported {
		return Capability{Name: name, Status: injection.Status, Source: injection.Source, Reason: injection.Reason, Evidence: injection.Evidence, DCGMNotSupported: injection.DCGMNotSupported}
	}
	field := nvlinkCRCFieldSupport(fields)
	output := dcgmContainerProbe(ctx, runner, opts, "dmon", "-e", field.ID, "-c", "1")
	if sentinel := dcgmSentinelEvidence(output); sentinel == "DCGM_FT_INT64_NOT_SUPPORTED" {
		return Capability{
			Name:             name,
			Status:           StatusUnsupported,
			Source:           field.dmonSource(),
			Reason:           fmt.Sprintf("DCGM field %s returned %s", field.label(), sentinel),
			Evidence:         firstLine(output),
			DCGMNotSupported: true,
		}
	}
	if dcgmProbeUnavailable(output) {
		return Capability{Name: name, Status: StatusUnknown, Source: field.dmonSource(), Reason: dcgmProbeUnavailableReason("nvlink-crc", output), Evidence: firstLine(output)}
	}
	if dcgmDmonHasRealSample(output) {
		return Capability{Name: name, Status: StatusSupported, Source: field.dmonSource(), Reason: fmt.Sprintf("DCGM field %s returned readable samples", field.label()), Evidence: firstLine(output)}
	}
	return Capability{Name: name, Status: StatusUnsupported, Source: field.dmonSource(), Reason: fmt.Sprintf("DCGM field %s did not return readable samples", field.label()), Evidence: firstLine(output), DCGMNotSupported: true}
}

// nvlinkCRCFieldSupport resolves the field read back by NVLink failure injection.
// NVLink CRC flit errors are the scenario signal because they are integer DCGM
// device fields on NVLink-capable GPUs; a readable dmon sample proves DCGM can
// resolve and collect the same field family that the failure-injection suite uses.
func nvlinkCRCFieldSupport(fields string) dcgmFieldSupport {
	return resolveDCGMFieldSupport(fields, nvlinkCRCFieldName, nvlinkCRCFieldFallbackID)
}
