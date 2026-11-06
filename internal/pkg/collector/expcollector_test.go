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

package collector

import (
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// TestCountSource checks that each count family resolves to the DCGM field its
// collectors watch and the field name they log, and that a name that is not a
// count family returns an error.
func TestCountSource(t *testing.T) {
	for _, tt := range []struct {
		family    string
		wantField dcgm.Short
		wantName  string
	}{
		{counters.DCGMExpXIDErrorsCount, dcgm.DCGM_FI_DEV_XID_ERRORS, "DCGM_FI_DEV_XID_ERROR"},
		{counters.DCGMExpClockEventsCount, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, "DCGM_FI_DEV_CLOCKS_EVENT_REASONS"},
	} {
		t.Run(tt.family, func(t *testing.T) {
			source, err := countSource(tt.family)

			require.NoError(t, err)
			assert.Equal(t, tt.wantField, source.FieldID)
			assert.Equal(t, tt.wantName, source.Name)
		})
	}

	_, err := countSource(counters.DCGMExpGPUHealthStatus)
	assert.ErrorContains(t, err, counters.DCGMExpGPUHealthStatus+" has no DCGM source field")
}
