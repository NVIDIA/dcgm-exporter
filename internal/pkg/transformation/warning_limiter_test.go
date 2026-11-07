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
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// syncBuffer collects log output that background goroutines may write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends to the buffer under the lock.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns what was logged so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// count reports how many log lines contain text.
func (b *syncBuffer) count(text string) int {
	return strings.Count(b.String(), text)
}

// captureLogs sends the default logger to a buffer at Info level or above until
// the test ends. Tests that use it must not run in parallel.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	logs := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// fakeClock moves only when a test says so.
type fakeClock struct{ now time.Time }

// Now returns the time the test set.
func (c *fakeClock) Now() time.Time { return c.now }

func TestWarningLimiterAllowsOneWarningPerInterval(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	limiter := &warningLimiter{interval: 5 * time.Minute, now: clock.Now}

	steps := []struct {
		advance time.Duration
		want    bool
	}{
		{0, true},
		{0, false},
		{4*time.Minute + 59*time.Second, false},
		{time.Second, true},
		{time.Minute, false},
		{4 * time.Minute, true},
	}
	for i, step := range steps {
		clock.now = clock.now.Add(step.advance)
		assert.Equal(t, step.want, limiter.allow(), "step %d", i)
	}
}

// A limiter with no interval set waits the default sourceWarningInterval between warnings.
func TestWarningLimiterWithoutAnIntervalUsesTheDefault(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	limiter := &warningLimiter{now: clock.Now}

	assert.True(t, limiter.allow())
	clock.now = clock.now.Add(sourceWarningInterval - time.Second)
	assert.False(t, limiter.allow(), "just inside the default interval")
	clock.now = clock.now.Add(time.Second)
	assert.True(t, limiter.allow(), "the default interval has passed")
	assert.Equal(t, 5*time.Minute, sourceWarningInterval)
}

// The zero value of the limiter allows the first warning and then refuses the next.
func TestWarningLimiterZeroValueWarnsOnceThenWaits(t *testing.T) {
	var limiter warningLimiter
	assert.True(t, limiter.allow())
	assert.False(t, limiter.allow())
}

func TestWarningLimiterTracksEachLocationSeparately(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	limiter := &warningLimiter{interval: 5 * time.Minute, now: clock.Now}

	assert.True(t, limiter.allowFor("/jobs/0"))
	assert.True(t, limiter.allowFor("/jobs/1"), "a second location is not hidden by the first")
	assert.False(t, limiter.allowFor("/jobs/0"))
	assert.False(t, limiter.allowFor("/jobs/1"))
}
