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
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// These tests mutate package/process globals; hold each lock until t.Cleanup
// restores the original value so future parallel tests cannot interleave them.
var (
	cleanupTimeoutMu  sync.Mutex
	slogDefaultTestMu sync.Mutex
)

func TestXIDTotalCollectorCleanupTimesOutAndDefersWatchCleanup(t *testing.T) {
	withCumulativeCollectorCleanupTimeout(t, 50*time.Millisecond)
	logs := captureSlogOutput(t)
	cleanupCalled := make(chan struct{}, 2)
	collector := &xidTotalCollector{
		expCollector: expCollector{baseExpCollector: baseExpCollector{
			counter: counters.Counter{FieldName: counters.DCGMExpXIDErrorsTotal},
			cleanups: []func(){
				func() {
					cleanupCalled <- struct{}{}
				},
			},
		}},
	}
	collector.poller = newCumulativeCollectorPoller(
		counters.DCGMExpXIDErrorsTotal,
		time.Hour,
		collector.collectNewEvents,
		collector.expCollector.Cleanup,
	)
	markPollerStarted(&collector.poller)

	done := make(chan struct{})
	go func() {
		collector.Cleanup()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cleanup did not return after poller timeout")
	}

	select {
	case <-cleanupCalled:
		t.Fatal("watch cleanup ran before poller stopped")
	default:
	}

	close(collector.poller.doneCh)
	select {
	case <-cleanupCalled:
	case <-time.After(time.Second):
		t.Fatal("watch cleanup was not called after poller stopped")
	}

	select {
	case <-cleanupCalled:
		t.Fatal("watch cleanup ran more than once")
	default:
	}
	assert.Contains(t, logs.String(), "cumulative event poller did not stop within timeout")
	assert.Contains(t, logs.String(), counters.DCGMExpXIDErrorsTotal)
}

func TestClockEventsTotalCollectorCleanupTimesOutAndDefersWatchCleanup(t *testing.T) {
	withCumulativeCollectorCleanupTimeout(t, 50*time.Millisecond)
	logs := captureSlogOutput(t)
	cleanupCalled := make(chan struct{}, 2)
	collector := &clockEventsTotalCollector{
		expCollector: expCollector{baseExpCollector: baseExpCollector{
			counter: counters.Counter{FieldName: counters.DCGMExpClockEventsTotal},
			cleanups: []func(){
				func() {
					cleanupCalled <- struct{}{}
				},
			},
		}},
	}
	collector.poller = newCumulativeCollectorPoller(
		counters.DCGMExpClockEventsTotal,
		time.Hour,
		collector.collectNewEvents,
		collector.expCollector.Cleanup,
	)
	markPollerStarted(&collector.poller)

	done := make(chan struct{})
	go func() {
		collector.Cleanup()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cleanup did not return after poller timeout")
	}

	select {
	case <-cleanupCalled:
		t.Fatal("watch cleanup ran before poller stopped")
	default:
	}

	close(collector.poller.doneCh)
	select {
	case <-cleanupCalled:
	case <-time.After(time.Second):
		t.Fatal("watch cleanup was not called after poller stopped")
	}

	select {
	case <-cleanupCalled:
		t.Fatal("watch cleanup ran more than once")
	default:
	}
	assert.Contains(t, logs.String(), "cumulative event poller did not stop within timeout")
	assert.Contains(t, logs.String(), counters.DCGMExpClockEventsTotal)
}

func TestCumulativeCollectorPollIntervalFallbackWarns(t *testing.T) {
	logs := captureSlogOutput(t)

	got := cumulativeCollectorPollInterval(counters.DCGMExpXIDErrorsTotal, 0)

	require.Equal(t, 30*time.Second, got)
	assert.Contains(t, logs.String(), "invalid cumulative collector poll interval; using default")
	assert.Contains(t, logs.String(), counters.DCGMExpXIDErrorsTotal)
	assert.Contains(t, logs.String(), "collect_interval_ms=0")
	assert.Contains(t, logs.String(), "default_interval=30s")
}

func TestCumulativeEventPollFailureLogIncludesPollContext(t *testing.T) {
	logs := captureSlogJSONOutput(t)
	group := testGroupHandle(7)
	fieldGroup := testFieldHandle(9)
	since := time.Unix(123, 0)
	err := newCumulativePollContextError(
		"get test values since cursor",
		group,
		fieldGroup,
		since,
		errors.New("dcgm unavailable"),
	)

	logCumulativeEventPollFailure("DCGM_EXP_TEST_TOTAL", err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &got))
	assert.Equal(t, "WARN", got["level"])
	assert.Equal(t, "cumulative event poll failed", got["msg"])
	assert.Equal(t, "DCGM_EXP_TEST_TOTAL", got["collector"])
	assert.Equal(t, float64(7), got["group_handle"])
	assert.Equal(t, float64(9), got["field_group_handle"])
	assert.Equal(t, "1970-01-01T00:02:03Z", got["since_timestamp"])
	assert.Contains(t, got["error"], "group_handle=7")
	assert.Contains(t, got["error"], "field_group_handle=9")
	assert.Contains(t, got["error"], "since_timestamp=1970-01-01T00:02:03Z")
}

func TestPollerSkipsCollectAfterStop(t *testing.T) {
	collectCalled := false
	poller := newCumulativeCollectorPoller(
		counters.DCGMExpXIDErrorsTotal,
		time.Hour,
		func() error {
			collectCalled = true
			return nil
		},
		func() {},
	)
	close(poller.stopCh)

	require.False(t, poller.collectIfRunning())
	assert.False(t, collectCalled)
}

func TestPollerCleanupBeforeStart(t *testing.T) {
	withCumulativeCollectorCleanupTimeout(t, 20*time.Millisecond)
	cleanupCalled := make(chan struct{}, 2)
	poller := newCumulativeCollectorPoller(
		counters.DCGMExpXIDErrorsTotal,
		time.Hour,
		func() error {
			t.Fatal("collect should not run")
			return nil
		},
		func() {
			cleanupCalled <- struct{}{}
		},
	)

	poller.Cleanup()

	select {
	case <-poller.doneCh:
	case <-time.After(time.Second):
		t.Fatal("doneCh was not closed")
	}
	select {
	case <-cleanupCalled:
	case <-time.After(time.Second):
		t.Fatal("cleanup was not called")
	}

	poller.Cleanup()
	poller.start()
	select {
	case <-cleanupCalled:
		t.Fatal("cleanup ran more than once")
	default:
	}
}

func markPollerStarted(poller *cumulativeCollectorPoller) {
	poller.lifecycleMu.Lock()
	defer poller.lifecycleMu.Unlock()
	poller.started = true
}

func withCumulativeCollectorCleanupTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()

	cleanupTimeoutMu.Lock()
	previous := cumulativeCollectorCleanupTimeout
	cumulativeCollectorCleanupTimeout = timeout
	t.Cleanup(func() {
		cumulativeCollectorCleanupTimeout = previous
		cleanupTimeoutMu.Unlock()
	})
}

func captureSlogOutput(t *testing.T) *bytes.Buffer {
	t.Helper()

	slogDefaultTestMu.Lock()
	var logs bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		slogDefaultTestMu.Unlock()
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	return &logs
}

func captureSlogJSONOutput(t *testing.T) *bytes.Buffer {
	t.Helper()

	slogDefaultTestMu.Lock()
	var logs bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(previous)
		slogDefaultTestMu.Unlock()
	})
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	return &logs
}
