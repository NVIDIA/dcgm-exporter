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

package counters

import (
	"fmt"
	stdos "os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/require"
)

// deprecatedFieldsWithOwnID lists DCGM 4.7 fields that dcgm_fields.h marks as
// deprecated but that keep their own field ID, so dcgm.IsLegacyField does not
// report them. Shipped files must use the replacement names instead.
var deprecatedFieldsWithOwnID = []string{
	"DCGM_FI_DEV_BAR1_TOTAL",
	"DCGM_FI_DEV_BAR1_USED",
	"DCGM_FI_DEV_BAR1_FREE",
	"DCGM_FI_SYSTEM_GPU_SYNC_BOOST",
	"DCGM_FI_DEV_SM_CLOCK",
	"DCGM_FI_DEV_MEM_CLOCK",
	"DCGM_FI_DEV_VIDEO_CLOCK",
	"DCGM_FI_DEV_APP_SM_CLOCK",
	"DCGM_FI_DEV_APP_MEM_CLOCK",
	"DCGM_FI_DEV_MAX_SM_CLOCK",
	"DCGM_FI_DEV_MAX_MEM_CLOCK",
	"DCGM_FI_DEV_MAX_VIDEO_CLOCK",
	"DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION",
	"DCGM_FI_DEV_FAN_SPEED",
	"DCGM_FI_DEV_PCIE_TX_THROUGHPUT",
	"DCGM_FI_DEV_PCIE_RX_THROUGHPUT",
	"DCGM_FI_DEV_MEM_COPY_UTIL",
	"DCGM_FI_DEV_ENC_UTIL",
	"DCGM_FI_DEV_DEC_UTIL",
	"DCGM_FI_DEV_POWER_VIOLATION",
	"DCGM_FI_DEV_THERMAL_VIOLATION",
	"DCGM_FI_DEV_SYNC_BOOST_VIOLATION",
	"DCGM_FI_DEV_BOARD_LIMIT_VIOLATION",
	"DCGM_FI_DEV_LOW_UTIL_VIOLATION",
	"DCGM_FI_DEV_RELIABILITY_VIOLATION",
	"DCGM_FI_DEV_TOTAL_APP_CLOCKS_VIOLATION",
	"DCGM_FI_DEV_TOTAL_BASE_CLOCKS_VIOLATION",
	"DCGM_FI_DEV_FB_TOTAL",
	"DCGM_FI_DEV_FB_FREE",
	"DCGM_FI_DEV_FB_USED",
	"DCGM_FI_DEV_FB_RESERVED",
	"DCGM_FI_DEV_C2C_MAX_BANDWIDTH",
	"DCGM_FI_DEV_NVLINK_THROUGHPUT_TOTAL",
	"DCGM_FI_DEV_VGPU_MEMORY_USAGE",
	"DCGM_FI_DEV_NVLINK_PPCNT_RECOVERY_TIME_BETWEEN_LAST_TWO",
	"DCGM_FI_PROF_NVLINK_TX_BYTES_PER_LINK",
	"DCGM_FI_PROF_NVLINK_RX_BYTES_PER_LINK",
}

// deprecatedPerLinkFields returns the deprecated per-link NVLink field names
// that only differ by link number.
func deprecatedPerLinkFields() []string {
	var names []string
	for link := 0; link <= 17; link++ {
		names = append(names,
			fmt.Sprintf("DCGM_FI_DEV_NVLINK_THROUGHPUT_L%d", link),
			fmt.Sprintf("DCGM_FI_PROF_NVLINK_L%d_TX_BYTES", link),
			fmt.Sprintf("DCGM_FI_PROF_NVLINK_L%d_RX_BYTES", link),
		)
	}

	return names
}

var shippedFieldNamePattern = regexp.MustCompile(`^#?\s*(DCGM_(?:FI|EXP)_[A-Z0-9_]+)\s*,`)

// shippedFieldNames returns every field name in the file, active or commented.
func shippedFieldNames(t *testing.T, path string) []string {
	t.Helper()

	data, err := stdos.ReadFile(path)
	require.NoError(t, err)

	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if match := shippedFieldNamePattern.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			names = append(names, match[1])
		}
	}
	require.NotEmpty(t, names, "%s: no field names found", path)

	return names
}

func TestDeprecatedFieldListIsAccurate(t *testing.T) {
	names := append(append([]string{}, deprecatedFieldsWithOwnID...), deprecatedPerLinkFields()...)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			_, known := dcgm.GetFieldID(name)
			require.True(t, known, "%s is not a go-dcgm field; remove it from the deprecated list", name)
			require.False(t, dcgm.IsLegacyField(name),
				"%s is a legacy alias already rejected by IsLegacyField; remove it from the deprecated list", name)
		})
	}
}

func TestShippedProfilesUseOnlyCurrentFieldNames(t *testing.T) {
	deprecated := make(map[string]bool)
	for _, name := range deprecatedFieldsWithOwnID {
		deprecated[name] = true
	}
	for _, name := range deprecatedPerLinkFields() {
		deprecated[name] = true
	}

	for _, source := range []string{"etc/default-counters.csv", "etc/dcp-metrics-included.csv"} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(repoRootFromThisTest(t), source)
			for _, name := range shippedFieldNames(t, path) {
				switch {
				case strings.HasPrefix(name, "DCGM_EXP_"):
					continue // Exporter-owned names are not DCGM fields.
				case dcgm.IsLegacyField(name):
					t.Errorf("%s uses legacy field name %s", source, name)
				case deprecated[name]:
					t.Errorf("%s uses deprecated field name %s", source, name)
				default:
					if _, known := dcgm.GetFieldID(name); !known {
						t.Errorf("%s uses %s, which go-dcgm does not know", source, name)
					}
				}
			}
		})
	}
}

func TestShippedProfilesAreIdentical(t *testing.T) {
	root := repoRootFromThisTest(t)

	defaults, err := stdos.ReadFile(filepath.Join(root, "etc/default-counters.csv"))
	require.NoError(t, err)
	dcp, err := stdos.ReadFile(filepath.Join(root, "etc/dcp-metrics-included.csv"))
	require.NoError(t, err)

	require.Equal(t, string(defaults), string(dcp),
		"etc/dcp-metrics-included.csv must stay byte-identical to etc/default-counters.csv")
}
