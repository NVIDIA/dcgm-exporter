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
	"log/slog"
	"sync"
	"time"
)

// cumulativeCollectorCleanupTimeout bounds hot reload and shutdown if the poller
// is stuck in a DCGM cgo call.
var cumulativeCollectorCleanupTimeout = 2 * time.Second

const cumulativeCollectorDefaultPollInterval = 30 * time.Second

// cumulativeCollectorPoller owns a periodic native event read and separates a
// stop request from final watch cleanup when that read cannot return promptly.
type cumulativeCollectorPoller struct {
	collector    string
	pollInterval time.Duration
	collect      func() error
	cleanup      func()

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool

	stopOnce          sync.Once
	cleanupOnce       sync.Once
	stopCh            chan struct{}
	doneCh            chan struct{}
	cleanupCompleteCh chan struct{}
}

// newCumulativeCollectorPoller creates an idle poller. Its owning cumulative
// collector starts it after all watches have been installed.
func newCumulativeCollectorPoller(
	collector string,
	pollInterval time.Duration,
	collect func() error,
	cleanup func(),
) cumulativeCollectorPoller {
	return cumulativeCollectorPoller{
		collector:         collector,
		pollInterval:      pollInterval,
		collect:           collect,
		cleanup:           cleanup,
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
		cleanupCompleteCh: make(chan struct{}),
	}
}

// cumulativeCollectorPollInterval uses the configured interval or warns and returns the default.
func cumulativeCollectorPollInterval(collector string, collectIntervalMS int) time.Duration {
	pollInterval := time.Duration(collectIntervalMS) * time.Millisecond
	if pollInterval > 0 {
		return pollInterval
	}

	slog.Warn("invalid cumulative collector poll interval; using default",
		slog.String("collector", collector),
		slog.Int("collect_interval_ms", collectIntervalMS),
		slog.Duration("default_interval", cumulativeCollectorDefaultPollInterval))
	return cumulativeCollectorDefaultPollInterval
}

// start launches the polling loop once unless cleanup has already stopped the poller.
func (p *cumulativeCollectorPoller) start() {
	p.lifecycleMu.Lock()
	if p.started || p.stopped {
		p.lifecycleMu.Unlock()
		return
	}
	p.started = true
	p.lifecycleMu.Unlock()

	go p.run()
}

// run polls on each tick and exits when cleanup requests a stop.
func (p *cumulativeCollectorPoller) run() {
	defer p.completeCleanup()
	defer close(p.doneCh)

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			if !p.collectIfRunning() {
				return
			}
		}
	}
}

// collectIfRunning skips work after a stop request and otherwise performs one poll.
func (p *cumulativeCollectorPoller) collectIfRunning() bool {
	select {
	case <-p.stopCh:
		return false
	default:
	}

	if err := p.collect(); err != nil {
		logCumulativeEventPollFailure(p.collector, err)
	}
	return true
}

// Cleanup requests the poller to stop and returns after the bounded request
// wait. Callers that must release DCGM use CleanupComplete before doing so.
func (p *cumulativeCollectorPoller) Cleanup() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
		if !p.markStopped() {
			close(p.doneCh)
			p.completeCleanup()
			return
		}

		select {
		case <-p.doneCh:
		case <-time.After(cumulativeCollectorCleanupTimeout):
			// The poller may still be inside a DCGM call; defer watch teardown
			// to its exit path instead of destroying handles that may still be in use.
			slog.Error("cumulative event poller did not stop within timeout; deferring watch teardown until poller exits",
				slog.String("collector", p.collector),
				slog.Duration("timeout", cumulativeCollectorCleanupTimeout))
		}
	})
}

// CleanupComplete closes after a stopped poller has released its field watches.
// Registry retirement uses it after Cleanup returns so a delayed native poll
// cannot outlive the DCGM provider that created its handles.
func (p *cumulativeCollectorPoller) CleanupComplete() <-chan struct{} {
	return p.cleanupCompleteCh
}

// completeCleanup runs the poller's resource cleanup once and then signals
// owners that both the polling call and its native field watches are finished.
func (p *cumulativeCollectorPoller) completeCleanup() {
	p.cleanupOnce.Do(func() {
		p.cleanup()
		close(p.cleanupCompleteCh)
	})
}

// markStopped prevents a later start and reports whether a run loop was launched.
func (p *cumulativeCollectorPoller) markStopped() bool {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()

	p.stopped = true
	return p.started
}
