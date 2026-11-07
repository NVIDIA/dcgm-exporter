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

package metriccontract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseFieldSupport checks the JSON document format and the rules that
// tie its names to the CSV rows.
func TestParseFieldSupport(t *testing.T) {
	rows := []DefaultCounterRow{
		{Name: "DCGM_FI_DEV_GPU_UTIL_RATIO"},
		{Name: "DCGM_FI_DEV_MEMORY_TEMP_CELSIUS"},
		{Name: "DCGM_FI_SYSTEM_DRIVER_VERSION"},
	}
	type classification struct {
		status FieldStatus
		reason string
	}
	tests := []struct {
		name    string
		raw     string
		present bool
		want    map[string]classification
		wantErr string
	}{
		{
			name: "empty means no evidence",
			raw:  "  ",
			want: map[string]classification{
				"DCGM_FI_DEV_GPU_UTIL_RATIO": {FieldUnknown, "no DCGM field support evidence for this run (E2E_DCGM_FIELD_SUPPORT is not set)"},
			},
		},
		{
			name:    "supported, unsupported and unlisted rows",
			raw:     `{"supported":["DCGM_FI_DEV_GPU_UTIL_RATIO","DCGM_FI_SYSTEM_DRIVER_VERSION"],"unsupported":{"DCGM_FI_DEV_MEMORY_TEMP_CELSIUS":"N/A on GPU 0"}}`,
			present: true,
			want: map[string]classification{
				"DCGM_FI_DEV_GPU_UTIL_RATIO":      {FieldSupported, ""},
				"DCGM_FI_SYSTEM_DRIVER_VERSION":   {FieldSupported, ""},
				"DCGM_FI_DEV_MEMORY_TEMP_CELSIUS": {FieldUnsupported, "N/A on GPU 0"},
			},
		},
		{
			name:    "present evidence without a row",
			raw:     `{"supported":["DCGM_FI_DEV_GPU_UTIL_RATIO"]}`,
			present: true,
			want: map[string]classification{
				"DCGM_FI_DEV_MEMORY_TEMP_CELSIUS": {FieldUnknown, "the DCGM field support evidence does not classify this row"},
			},
		},
		{
			name:    "name not in the CSV",
			raw:     `{"supported":["DCGM_FI_DEV_POWER_USAGE"]}`,
			wantErr: `E2E_DCGM_FIELD_SUPPORT supported field "DCGM_FI_DEV_POWER_USAGE" is not an enabled row of the CSV`,
		},
		{
			name:    "unsupported name not in the CSV",
			raw:     `{"unsupported":{"DCGM_FI_DEV_POWER_USAGE":"N/A"}}`,
			wantErr: `unsupported field "DCGM_FI_DEV_POWER_USAGE" is not an enabled row of the CSV`,
		},
		{
			name:    "row in both lists",
			raw:     `{"supported":["DCGM_FI_DEV_GPU_UTIL_RATIO"],"unsupported":{"DCGM_FI_DEV_GPU_UTIL_RATIO":"N/A"}}`,
			wantErr: `lists field "DCGM_FI_DEV_GPU_UTIL_RATIO" as both supported and unsupported`,
		},
		{
			name:    "supported row twice",
			raw:     `{"supported":["DCGM_FI_DEV_GPU_UTIL_RATIO","DCGM_FI_DEV_GPU_UTIL_RATIO"]}`,
			wantErr: `lists supported field "DCGM_FI_DEV_GPU_UTIL_RATIO" twice`,
		},
		{
			name:    "unsupported row without a reason",
			raw:     `{"unsupported":{"DCGM_FI_DEV_GPU_UTIL_RATIO":" "}}`,
			wantErr: `unsupported field "DCGM_FI_DEV_GPU_UTIL_RATIO" has no reason`,
		},
		{
			name:    "old comma list",
			raw:     "DCGM_FI_DEV_GPU_UTIL_RATIO,DCGM_FI_DEV_MEMORY_TEMP_CELSIUS",
			wantErr: "E2E_DCGM_FIELD_SUPPORT is not a field support JSON document",
		},
		{
			name:    "unknown key",
			raw:     `{"supported":[],"skipped":{}}`,
			wantErr: "E2E_DCGM_FIELD_SUPPORT is not a field support JSON document",
		},
		{
			name:    "two documents",
			raw:     `{"supported":[]} {"supported":[]}`,
			wantErr: "E2E_DCGM_FIELD_SUPPORT holds more than one JSON value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseFieldSupport(test.raw, rows)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.present, got.present)
			for name, want := range test.want {
				status, reason := got.Classify(name)
				require.Equal(t, want, classification{status, reason}, name)
			}
		})
	}
}

// TestParseFieldSupportReadsTheProducerDocument parses the document the e2e
// CLI's producer test writes, so a format change on either side fails.
func TestParseFieldSupportReadsTheProducerDocument(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "field-support-document.json"))
	require.NoError(t, err)
	rows := []DefaultCounterRow{
		{Name: "DCGM_FI_DEV_GPU_UTIL_RATIO"},
		{Name: "DCGM_FI_SYSTEM_DRIVER_VERSION"},
		{Name: "DCGM_FI_DEV_XID_ERROR"},
	}

	support, err := ParseFieldSupport(string(raw), rows)

	require.NoError(t, err)
	status, _ := support.Classify("DCGM_FI_DEV_GPU_UTIL_RATIO")
	require.Equal(t, FieldSupported, status)
	status, _ = support.Classify("DCGM_FI_SYSTEM_DRIVER_VERSION")
	require.Equal(t, FieldSupported, status)
	status, reason := support.Classify("DCGM_FI_DEV_XID_ERROR")
	require.Equal(t, FieldUnsupported, status)
	require.Contains(t, reason, "DCGM returned no value (N/A) on every GPU during the probe")
}
