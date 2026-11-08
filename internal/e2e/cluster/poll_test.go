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

package cluster

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPollUntil checks the result for a check that passes, a timeout, and a canceled context, and how often
// each one runs the check.
func TestPollUntil(t *testing.T) {
	timedOut := errors.New("timed out")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name       string
		ctx        context.Context
		passAt     int
		timeout    time.Duration
		wantErr    error
		wantChecks int
	}{
		{name: "passes at once", ctx: context.Background(), passAt: 1, timeout: time.Minute, wantChecks: 1},
		{name: "passes after retries", ctx: context.Background(), passAt: 3, timeout: time.Minute, wantChecks: 3},
		{name: "times out after one check", ctx: context.Background(), timeout: -time.Second, wantErr: timedOut, wantChecks: 1},
		{name: "stops when the context ends", ctx: canceled, timeout: time.Minute, wantErr: context.Canceled, wantChecks: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			checks := 0
			check := func() bool {
				checks++
				return checks == test.passAt
			}

			err := pollUntil(test.ctx, time.Millisecond, test.timeout, check, func() error { return timedOut })

			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("pollUntil() error = %v, want %v", err, test.wantErr)
			}
			if checks != test.wantChecks {
				t.Fatalf("pollUntil() ran the check %d times, want %d", checks, test.wantChecks)
			}
		})
	}
}

// TestConfigPollDefaults checks that an empty or invalid timeout option and an unset interval fall back to
// the defaults rather than ending the wait at once.
func TestConfigPollDefaults(t *testing.T) {
	for _, timeout := range []string{"", "not-a-duration"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		checks := 0
		err := Config{}.poll(ctx, timeout, func() bool {
			checks++
			return false
		}, func() error { return errors.New("timed out") })
		cancel()

		if !errors.Is(err, context.DeadlineExceeded) || checks != 1 {
			t.Fatalf("poll(timeout=%q) error = %v after %d checks, want the context deadline after one check", timeout, err, checks)
		}
	}
}
