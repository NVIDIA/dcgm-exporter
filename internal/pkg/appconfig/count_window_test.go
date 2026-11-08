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

package appconfig

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCountWindowsReachTheirSettings checks that each table row reads and
// writes the setting of its own counter.
func TestCountWindowsReachTheirSettings(t *testing.T) {
	config := DefaultConfig()
	config.XIDCountWindowSize = 1000
	config.ClockEventsCountWindowSize = 2000

	xid, ok := CountWindowOf(ExporterXIDErrorsCountField)
	assert.True(t, ok)
	assert.Equal(t, FieldXIDCountWindowSize, xid.Field)
	assert.Equal(t, FlagXIDCountWindowSize, xid.Flag)
	assert.Equal(t, time.Second, xid.Duration(config))

	clock, ok := CountWindowOf(ExporterClockEventsCountField)
	assert.True(t, ok)
	assert.Equal(t, 2*time.Second, clock.Duration(config))

	_, ok = CountWindowOf("DCGM_FI_DEV_GPU_TEMP")
	assert.False(t, ok)
	assert.Equal(t, []string{ExporterXIDErrorsCountField, ExporterClockEventsCountField}, CountWindowCounters())
}

// TestCountWindowDurationDoesNotWrap checks that a value above the largest
// duration is cut instead of wrapping to a negative duration.
func TestCountWindowDurationDoesNotWrap(t *testing.T) {
	config := DefaultConfig()
	config.XIDCountWindowSize = int(^uint(0) >> 1)

	assert.Positive(t, CountWindows[0].Duration(config))
}
