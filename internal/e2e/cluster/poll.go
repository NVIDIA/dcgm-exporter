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
	"time"
)

const (
	// defaultPollInterval is the wait between checks while the cluster converges.
	defaultPollInterval = 5 * time.Second
	// defaultPollTimeout bounds a wait whose timeout option is empty or invalid.
	defaultPollTimeout = 5 * time.Minute
)

// poll runs check until it reports true and then returns nil. When the timeout option passes first, it
// returns timedOut's error; when ctx ends, ctx.Err(). Config.pollInterval, when set, replaces the default
// wait between checks.
func (c Config) poll(ctx context.Context, timeout string, check func() bool, timedOut func() error) error {
	interval := c.pollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	return pollUntil(ctx, interval, parseDuration(timeout, defaultPollTimeout), check, timedOut)
}

// pollUntil runs check every interval until it reports done, the timeout passes, or ctx ends. Check always
// runs at least once.
func pollUntil(ctx context.Context, interval, timeout time.Duration, check func() bool, timedOut func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		if check() {
			return nil
		}
		if time.Now().After(deadline) {
			return timedOut()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
