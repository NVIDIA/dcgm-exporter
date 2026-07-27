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

package nvmlprovider

import (
	"testing"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitializeWithRetry_SucceedsAfterTransientFailures verifies that a couple
// of ERROR_LIBRARY_NOT_FOUND failures (e.g. the GPU driver installer is still
// running) are retried and initialization succeeds once the library becomes
// available.
func TestInitializeWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	defer func() { nvmlInitFunc = nvml.Init }()
	defer reset()

	callCount := 0
	nvmlInitFunc = func() nvml.Return {
		callCount++
		if callCount <= 2 {
			return nvml.ERROR_LIBRARY_NOT_FOUND
		}
		return nvml.SUCCESS
	}

	err := InitializeWithRetry(5, time.Millisecond, 5*time.Millisecond)

	require.NoError(t, err)
	assert.Equal(t, 3, callCount, "expected exactly 3 calls: 2 failures + 1 success")
	assert.True(t, Client().(nvmlProvider).initialized)
}

// TestInitializeWithRetry_GivesUpAfterExhaustingAttempts verifies that a
// persistent transient error is retried up to the attempt limit and then
// returns a wrapped error naming the last failure.
func TestInitializeWithRetry_GivesUpAfterExhaustingAttempts(t *testing.T) {
	defer func() { nvmlInitFunc = nvml.Init }()
	defer reset()

	callCount := 0
	nvmlInitFunc = func() nvml.Return {
		callCount++
		return nvml.ERROR_LIBRARY_NOT_FOUND
	}

	err := InitializeWithRetry(4, time.Millisecond, 5*time.Millisecond)

	require.Error(t, err)
	assert.Equal(t, 4, callCount, "expected exactly 4 attempts")
	assert.Contains(t, err.Error(), "4 attempts")
	assert.Contains(t, err.Error(), nvml.ErrorString(nvml.ERROR_LIBRARY_NOT_FOUND))
}

// TestInitializeWithRetry_FailsFastOnNonTransientError verifies that an error
// other than ERROR_LIBRARY_NOT_FOUND is not retried, since retrying a
// permanent error only delays an unavoidable failure.
func TestInitializeWithRetry_FailsFastOnNonTransientError(t *testing.T) {
	defer func() { nvmlInitFunc = nvml.Init }()
	defer reset()

	callCount := 0
	nvmlInitFunc = func() nvml.Return {
		callCount++
		return nvml.ERROR_INSUFFICIENT_POWER
	}

	err := InitializeWithRetry(5, time.Millisecond, 5*time.Millisecond)

	require.Error(t, err)
	assert.Equal(t, 1, callCount, "non-transient error should fail after a single attempt")
	assert.Contains(t, err.Error(), nvml.ErrorString(nvml.ERROR_INSUFFICIENT_POWER))
}

// TestBackoffDuration_RespectsMaxWaitCap verifies the exponential backoff is
// capped at maxWait (plus jitter on top), rather than growing unbounded.
func TestBackoffDuration_RespectsMaxWaitCap(t *testing.T) {
	baseWait := 2 * time.Second
	maxWait := 20 * time.Second

	// A large attempt number would overflow the naive exponential without the cap.
	d := backoffDuration(10, baseWait, maxWait)

	assert.GreaterOrEqual(t, d, maxWait, "backoff should never go below the capped wait")
	assert.LessOrEqual(t, d, maxWait+time.Duration(float64(maxWait)*maxJitterFraction),
		"backoff should not exceed maxWait plus the maximum jitter fraction")
}

// TestBackoffDuration_JitterVaries proves jitter is actually applied by
// asserting repeated calls with identical inputs don't all return the same
// duration.
func TestBackoffDuration_JitterVaries(t *testing.T) {
	baseWait := 2 * time.Second
	maxWait := 20 * time.Second

	seen := make(map[time.Duration]bool)
	for i := 0; i < 20; i++ {
		d := backoffDuration(10, baseWait, maxWait)
		assert.GreaterOrEqual(t, d, maxWait)
		seen[d] = true
	}

	assert.Greater(t, len(seen), 1, "expected varying durations across repeated calls due to jitter")
}

// TestBackoffDuration_ExponentialGrowthBeforeCap verifies the backoff doubles
// on each attempt while still below maxWait, allowing for up to
// maxJitterFraction of jitter added on top of each scaled value.
func TestBackoffDuration_ExponentialGrowthBeforeCap(t *testing.T) {
	baseWait := 100 * time.Millisecond
	maxWait := 10 * time.Second

	for attempt, scaled := range map[int]time.Duration{
		0: baseWait,
		1: baseWait * 2,
		2: baseWait * 4,
	} {
		d := backoffDuration(attempt, baseWait, maxWait)
		maxWithJitter := scaled + time.Duration(float64(scaled)*maxJitterFraction)

		assert.GreaterOrEqualf(t, d, scaled, "attempt %d: backoff should be at least the scaled base", attempt)
		assert.LessOrEqualf(t, d, maxWithJitter, "attempt %d: backoff should not exceed scaled base plus max jitter", attempt)
	}
}
