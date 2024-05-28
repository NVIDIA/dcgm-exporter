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

	"github.com/stretchr/testify/assert"
)

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
