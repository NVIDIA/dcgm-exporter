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

// Package fieldexport decides whether a raw DCGM field can be emitted by the
// exporter's generic scalar collector.
package fieldexport

import "github.com/NVIDIA/go-dcgm/pkg/dcgm"

// Disposition tells the config parser and E2E probe whether a raw DCGM field
// can be sent to the generic scalar collector.
type Disposition uint8

const (
	// GenericScalar means the generic collector can emit the raw field as one scalar.
	GenericScalar Disposition = iota
	// RequiresAlternative means another metric provides a safe, documented representation.
	RequiresAlternative
	// Unsupported means no generic scalar metric can represent the raw field truthfully.
	Unsupported
)

// Rule records how one raw DCGM field can be exported. The config parser uses
// it to reject unsafe selections, and the E2E probe uses it to skip them.
// Alternative names a safer metric when one is available.
type Rule struct {
	Disposition Disposition
	Reason      string
	Alternative string
}

// fieldIDRange records a run of field IDs that were reviewed together. The
// go-dcgm bump helper checks these ranges before accepting new field IDs.
type fieldIDRange struct {
	first int
	last  int
}

var reviewedGenericScalarRanges = []fieldIDRange{
	// DCGM 4.7.0 base-unit fields are individual quantities, totals, or per-link values.
	{first: 1600, last: 1650},
}

// AllowsGenericScalar tells the config parser and E2E probe whether they may
// send this field to the generic scalar collector.
func (r Rule) AllowsGenericScalar() bool {
	return r.Disposition == GenericScalar
}

// RuleFor returns the decision used by the config parser and E2E probe for a
// raw field. Existing unknown fields default to GenericScalar because DCGM
// metadata cannot describe their meaning. The go-dcgm bump helper refuses new
// field IDs until they are reviewed.
func RuleFor(fieldID dcgm.Short) Rule {
	if rule, ok := rules[fieldID]; ok {
		return rule
	}

	return Rule{Disposition: GenericScalar}
}

// IsReviewed reports whether a numeric field ID has an explicit export choice.
// The go-dcgm bump helper calls it for every newly added field ID.
func IsReviewed(fieldID int) bool {
	for reviewedID := range rules {
		if int(reviewedID) == fieldID {
			return true
		}
	}

	for _, fieldRange := range reviewedGenericScalarRanges {
		if fieldID >= fieldRange.first && fieldID <= fieldRange.last {
			return true
		}
	}

	return false
}

var rules = map[dcgm.Short]Rule{
	dcgm.DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY: {
		Disposition: Unsupported,
		Reason:      "it packs the major and minor compute-capability versions into one integer",
	},
	dcgm.DCGM_FI_DEV_NVLINK_P2P_STATUS: {
		Disposition: RequiresAlternative,
		Reason:      "it is a P2P bitmap without peer GPU identity",
		Alternative: "DCGM_EXP_P2P_STATUS",
	},
	dcgm.DCGM_FI_DEV_MIG_MODE: {
		Disposition: Unsupported,
		Reason:      "DCGM can return an OK status with a zero value after an internal failure",
	},
	dcgm.DCGM_FI_DEV_MIG_MAX_SLICES: {
		Disposition: Unsupported,
		Reason:      "DCGM can return an OK status with a zero value after an internal failure",
	},
	dcgm.DCGM_FI_DEV_CPU_AFFINITY_0: {
		Disposition: Unsupported,
		Reason:      "it is a CPU affinity bitmap without CPU identity",
	},
	dcgm.DCGM_FI_DEV_CPU_AFFINITY_1: {
		Disposition: Unsupported,
		Reason:      "it is a CPU affinity bitmap without CPU identity",
	},
	dcgm.DCGM_FI_DEV_CPU_AFFINITY_2: {
		Disposition: Unsupported,
		Reason:      "it is a CPU affinity bitmap without CPU identity",
	},
	dcgm.DCGM_FI_DEV_CPU_AFFINITY_3: {
		Disposition: Unsupported,
		Reason:      "it is a CPU affinity bitmap without CPU identity",
	},
	dcgm.DCGM_FI_DEV_CC_MODE: {
		Disposition: Unsupported,
		Reason:      "DCGM can return an OK status with a zero value after an internal failure",
	},
	dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_0: {
		Disposition: Unsupported,
		Reason:      "it is a memory affinity bitmap without memory-node identity",
	},
	dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_1: {
		Disposition: Unsupported,
		Reason:      "it is a memory affinity bitmap without memory-node identity",
	},
	dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_2: {
		Disposition: Unsupported,
		Reason:      "it is a memory affinity bitmap without memory-node identity",
	},
	dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_3: {
		Disposition: Unsupported,
		Reason:      "it is a memory affinity bitmap without memory-node identity",
	},
	dcgm.DCGM_FI_DEV_GPM_SUPPORT: {
		Disposition: Unsupported,
		Reason:      "DCGM can return an OK status with a zero value after an internal failure",
	},
	dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS: {
		Disposition: RequiresAlternative,
		Reason:      "it is a clock-event bitmap without event identity",
		Alternative: "DCGM_EXP_CLOCK_EVENTS_COUNT or DCGM_EXP_CLOCK_EVENTS_TOTAL",
	},
	dcgm.DCGM_FI_DEV_CLOCKS_SUPPORTED: {
		Disposition: Unsupported,
		Reason:      "it contains a supported-clock list rather than one scalar value",
	},
	dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_REQUESTED_MASK: {
		Disposition: Unsupported,
		Reason:      "it is a power-profile bitmask without profile identity",
	},
	dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_ENFORCED_MASK: {
		Disposition: Unsupported,
		Reason:      "it is a power-profile bitmask without profile identity",
	},
	dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_SUPPORTED_MASK: {
		Disposition: Unsupported,
		Reason:      "it is a power-profile bitmask without profile identity",
	},
	dcgm.DCGM_FI_DEV_FABRIC_HEALTH_MASK: {
		Disposition: RequiresAlternative,
		Reason:      "it is a fabric-health mask without health-condition identity",
		Alternative: "DCGM_FI_DEV_FABRIC_HEALTH_SUMMARY",
	},
	dcgm.DCGM_FI_DEV_CONNECTX_CORRECTABLE_ERROR_MASK: {
		Disposition: Unsupported,
		Reason:      "it is a ConnectX error bitmask without error identity",
	},
	dcgm.DCGM_FI_DEV_CONNECTX_UNCORRECTABLE_ERROR_MASK: {
		Disposition: Unsupported,
		Reason:      "it is a ConnectX error bitmask without error identity",
	},
}
