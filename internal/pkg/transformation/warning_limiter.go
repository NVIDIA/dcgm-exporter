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

package transformation

import (
	"sync"
	"time"
)

// sourceWarningInterval is the shortest time between two warnings that an
// enrichment source is not usable while it stays that way.
const sourceWarningInterval = 5 * time.Minute

// warningLimiter lets a repeating failure log once per interval, tracked per
// failure location so one broken location cannot hide another. The zero value
// uses sourceWarningInterval and the real clock.
type warningLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	now      func() time.Time
	last     map[string]time.Time
}

// allow reports whether a warning for the single failure of a limiter may be
// logged now, and counts it if so.
func (l *warningLimiter) allow() bool { return l.allowFor("") }

// allowFor reports whether a warning for one failure location may be logged
// now, and counts it if so.
func (l *warningLimiter) allowFor(location string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// The zero value uses the default interval and the real clock.
	interval := l.interval
	if interval <= 0 {
		interval = sourceWarningInterval
	}

	now := time.Now
	if l.now != nil {
		now = l.now
	}

	if last, warned := l.last[location]; warned && now().Sub(last) < interval {
		return false
	}

	if l.last == nil {
		l.last = make(map[string]time.Time)
	}

	l.last[location] = now()

	return true
}
