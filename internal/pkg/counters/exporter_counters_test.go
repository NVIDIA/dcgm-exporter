/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
)

// TestIdentifyMetricType checks that the exporter metric names in its table, and DCGM_FI_UNKNOWN, map to
// their counter types, and that a name with extra characters after a known name is an error.
func TestIdentifyMetricType(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		output ExporterCounter
		valid  bool
	}{
		{
			name:   "Valid Input DCGM_EXP_XID_ERRORS_COUNT",
			field:  "DCGM_EXP_XID_ERRORS_COUNT",
			output: DCGMXIDErrorsCount,
			valid:  true,
		},
		{
			name:   "Valid Input DCGM_EXP_XID_ERRORS_TOTAL",
			field:  "DCGM_EXP_XID_ERRORS_TOTAL",
			output: DCGMXIDErrorsTotal,
			valid:  true,
		},
		{
			name:   "Valid Input DCGM_EXP_CLOCK_EVENTS_TOTAL",
			field:  "DCGM_EXP_CLOCK_EVENTS_TOTAL",
			output: DCGMClockEventsTotal,
			valid:  true,
		},
		{
			name:   "Valid Input DCGM_EXP_CLOCK_EVENTS_COUNT",
			field:  DCGMExpClockEventsCount,
			output: DCGMClockEventsCount,
			valid:  true,
		},
		{
			name:   "Valid Input DCGM_EXP_GPU_HEALTH_STATUS",
			field:  DCGMExpGPUHealthStatus,
			output: DCGMGPUHealthStatus,
			valid:  true,
		},
		{
			name:   "Valid Input DCGM_EXP_P2P_STATUS",
			field:  DCGMExpP2PStatus,
			output: DCGMP2PStatus,
			valid:  true,
		},
		{
			name:   "Valid Input DCGM_EXP_SYSTEM_HEALTH_STATUS",
			field:  DCGMExpSystemHealthStatus,
			output: DCGMSystemHealthStatus,
			valid:  true,
		},
		{
			name:   "Valid Input DCGM_FI_UNKNOWN",
			field:  "DCGM_FI_UNKNOWN",
			output: DCGMFIUnknown,
			valid:  true,
		},
		{
			name:   "Invalid Input DCGM_EXP_XID_ERRORS_COUNTXXX",
			field:  "DCGM_EXP_XID_ERRORS_COUNTXXX",
			output: DCGMFIUnknown,
			valid:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := IdentifyMetricType(tt.field)
			if tt.valid {
				assert.NoError(t, err, "Expected metrics to be found.")
				assert.Equal(t, output, tt.output, "Invalid output")
			} else {
				assert.Errorf(t, err, "Expected metrics to be not found.")
			}
		})
	}
}

// TestExporterCounterValuesAndStrings pins the numeric value and metric name of
// exporter counters, so adding a counter cannot renumber or rename another.
func TestExporterCounterValuesAndStrings(t *testing.T) {
	tests := []struct {
		counter ExporterCounter
		value   ExporterCounter
		name    string
	}{
		{counter: DCGMFIUnknown, value: 0, name: "DCGM_FI_UNKNOWN"},
		{counter: DCGMXIDErrorsCount, value: 9001, name: "DCGM_EXP_XID_ERRORS_COUNT"},
		{counter: DCGMClockEventsCount, value: 9002, name: "DCGM_EXP_CLOCK_EVENTS_COUNT"},
		{counter: DCGMGPUHealthStatus, value: 9003, name: "DCGM_EXP_GPU_HEALTH_STATUS"},
		{counter: DCGMP2PStatus, value: 9004, name: "DCGM_EXP_P2P_STATUS"},
		{counter: DCGMXIDErrorsTotal, value: 9005, name: "DCGM_EXP_XID_ERRORS_TOTAL"},
		{counter: DCGMClockEventsTotal, value: 9006, name: "DCGM_EXP_CLOCK_EVENTS_TOTAL"},
		{counter: DCGMFieldPolicyEnabled, value: 9007, name: "DCGM_EXP_FIELD_POLICY_ENABLED"},
		{counter: DCGMFieldPolicyCurrentValue, value: 9008, name: "DCGM_EXP_FIELD_POLICY_CURRENT_VALUE"},
		{counter: DCGMFieldPolicyThreshold, value: 9009, name: "DCGM_EXP_FIELD_POLICY_THRESHOLD"},
		{counter: DCGMFieldPolicyViolationCount, value: 9010, name: "DCGM_EXP_FIELD_POLICY_VIOLATION_COUNT"},
		{counter: DCGMFieldPolicyLastTriggerTimestampSeconds, value: 9011, name: "DCGM_EXP_FIELD_POLICY_LAST_TRIGGER_TIMESTAMP_SECONDS"},
		{counter: DCGMSystemHealthStatus, value: 9012, name: "DCGM_EXP_SYSTEM_HEALTH_STATUS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.value, tt.counter)
			assert.Equal(t, tt.name, tt.counter.String())
		})
	}
}

// TestCountSourceOf checks that each count family reports the DCGM field it
// counts, and that other names report none.
func TestCountSourceOf(t *testing.T) {
	tests := map[string]struct {
		name string
		want CountSource
		ok   bool
	}{
		"XID count": {
			name: DCGMXIDErrorsCount.String(),
			want: CountSource{FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS, Name: "DCGM_FI_DEV_XID_ERROR", EventDriven: true},
			ok:   true,
		},
		"clock events count": {
			name: DCGMClockEventsCount.String(),
			want: CountSource{FieldID: dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, Name: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS"},
			ok:   true,
		},
		"family without a count": {name: DCGMGPUHealthStatus.String()},
		"unknown name":           {name: "DCGM_FI_DEV_GPU_TEMP"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := CountSourceOf(tc.name)

			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}
