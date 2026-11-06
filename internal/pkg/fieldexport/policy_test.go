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

package fieldexport

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// TestRuleFor checks the representative policy decisions used by the parser and probe.
func TestRuleFor(t *testing.T) {
	tests := []struct {
		name        string
		fieldID     dcgm.Short
		disposition Disposition
		alternative string
	}{
		{
			name:        "packed compute capability",
			fieldID:     dcgm.DCGM_FI_CUDA_GPU_COMPUTE_CAPABILITY,
			disposition: Unsupported,
		},
		{
			name:        "P2P bitmap",
			fieldID:     dcgm.DCGM_FI_DEV_NVLINK_P2P_STATUS,
			disposition: RequiresAlternative,
			alternative: "DCGM_EXP_P2P_STATUS",
		},
		{
			name:        "failure-to-zero MIG mode",
			fieldID:     dcgm.DCGM_FI_DEV_MIG_MODE,
			disposition: Unsupported,
		},
		{
			name:        "failure-to-zero MIG slice limit",
			fieldID:     dcgm.DCGM_FI_DEV_MIG_MAX_SLICES,
			disposition: Unsupported,
		},
		{
			name:        "CPU affinity bitmap",
			fieldID:     dcgm.DCGM_FI_DEV_CPU_AFFINITY_0,
			disposition: Unsupported,
		},
		{
			name:        "failure-to-zero confidential compute mode",
			fieldID:     dcgm.DCGM_FI_DEV_CC_MODE,
			disposition: Unsupported,
		},
		{
			name:        "memory affinity bitmap",
			fieldID:     dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_0,
			disposition: Unsupported,
		},
		{
			name:        "failure-to-zero GPM support",
			fieldID:     dcgm.DCGM_FI_DEV_GPM_SUPPORT,
			disposition: Unsupported,
		},
		{
			name:        "clock event bitmap",
			fieldID:     dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
			disposition: RequiresAlternative,
			alternative: "DCGM_EXP_CLOCK_EVENTS_COUNT or DCGM_EXP_CLOCK_EVENTS_TOTAL",
		},
		{
			name:        "supported clock list",
			fieldID:     dcgm.DCGM_FI_DEV_CLOCKS_SUPPORTED,
			disposition: Unsupported,
		},
		{
			name:        "power profile bitmap",
			fieldID:     dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_REQUESTED_MASK,
			disposition: Unsupported,
		},
		{
			name:        "fabric health mask",
			fieldID:     dcgm.DCGM_FI_DEV_FABRIC_HEALTH_MASK,
			disposition: RequiresAlternative,
			alternative: "DCGM_FI_DEV_FABRIC_HEALTH_SUMMARY",
		},
		{
			name:        "ConnectX correctable error mask",
			fieldID:     dcgm.DCGM_FI_DEV_CONNECTX_CORRECTABLE_ERROR_MASK,
			disposition: Unsupported,
		},
		{
			name:        "ConnectX uncorrectable error mask",
			fieldID:     dcgm.DCGM_FI_DEV_CONNECTX_UNCORRECTABLE_ERROR_MASK,
			disposition: Unsupported,
		},
		{
			name:        "ordinary scalar",
			fieldID:     dcgm.DCGM_FI_DEV_GPU_TEMP,
			disposition: GenericScalar,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := RuleFor(tt.fieldID)
			if rule.Disposition != tt.disposition {
				t.Fatalf("RuleFor(%d).Disposition = %v, want %v", tt.fieldID, rule.Disposition, tt.disposition)
			}
			if rule.Alternative != tt.alternative {
				t.Fatalf("RuleFor(%d).Alternative = %q, want %q", tt.fieldID, rule.Alternative, tt.alternative)
			}
			if rule.AllowsGenericScalar() != (tt.disposition == GenericScalar) {
				t.Fatalf("RuleFor(%d).AllowsGenericScalar() does not match disposition %v", tt.fieldID, tt.disposition)
			}
		})
	}
}

// TestRuleForClassifiesEveryFieldInEachSharedRule checks every field in grouped rules.
func TestRuleForClassifiesEveryFieldInEachSharedRule(t *testing.T) {
	tests := []struct {
		name     string
		fieldIDs []dcgm.Short
	}{
		{
			name: "CPU affinity",
			fieldIDs: []dcgm.Short{
				dcgm.DCGM_FI_DEV_CPU_AFFINITY_0,
				dcgm.DCGM_FI_DEV_CPU_AFFINITY_1,
				dcgm.DCGM_FI_DEV_CPU_AFFINITY_2,
				dcgm.DCGM_FI_DEV_CPU_AFFINITY_3,
			},
		},
		{
			name: "memory affinity",
			fieldIDs: []dcgm.Short{
				dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_0,
				dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_1,
				dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_2,
				dcgm.DCGM_FI_DEV_MEMORY_AFFINITY_3,
			},
		},
		{
			name: "power profile",
			fieldIDs: []dcgm.Short{
				dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_REQUESTED_MASK,
				dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_ENFORCED_MASK,
				dcgm.DCGM_FI_DEV_BOARD_POWER_PROFILE_SUPPORTED_MASK,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, fieldID := range tt.fieldIDs {
				if RuleFor(fieldID).AllowsGenericScalar() {
					t.Errorf("RuleFor(%d) allows generic scalar export", fieldID)
				}
			}
		})
	}
}

// TestRulesExplainEveryRejectedField makes sure rejected config entries have useful errors.
func TestRulesExplainEveryRejectedField(t *testing.T) {
	for fieldID, rule := range rules {
		if rule.Reason == "" {
			t.Errorf("rules[%d] has no reason", fieldID)
		}

		switch rule.Disposition {
		case RequiresAlternative:
			if rule.Alternative == "" {
				t.Errorf("rules[%d] requires an alternative but does not name one", fieldID)
			}
		case Unsupported:
			if rule.Alternative != "" {
				t.Errorf("rules[%d] is unsupported but names an alternative", fieldID)
			}
		default:
			t.Errorf("rules[%d] has invalid disposition %v", fieldID, rule.Disposition)
		}
	}
}

// TestDCGM470BaseUnitFieldsAreReviewedGenericScalars keeps the 4.7 review record current.
func TestDCGM470BaseUnitFieldsAreReviewedGenericScalars(t *testing.T) {
	for fieldID := 1600; fieldID <= 1650; fieldID++ {
		if !IsReviewed(fieldID) {
			t.Errorf("IsReviewed(%d) = false", fieldID)
		}
		if !RuleFor(dcgm.Short(fieldID)).AllowsGenericScalar() {
			t.Errorf("RuleFor(%d) rejects a reviewed scalar field", fieldID)
		}
	}
}
