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

package server

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/logging"
)

// scrapeFlight holds one immutable scrape result shared by overlapping requests.
type scrapeFlight struct {
	done     chan struct{}
	response []byte
	err      error
}

// scrapeCoordinator limits admitted requests and coalesces active scrape work.
type scrapeCoordinator struct {
	slots chan struct{}

	mu       sync.Mutex
	inFlight *scrapeFlight
}

// newScrapeCoordinator creates a coordinator with a fixed admission capacity.
func newScrapeCoordinator(maxConcurrent int) *scrapeCoordinator {
	return &scrapeCoordinator{
		slots: make(chan struct{}, maxConcurrent),
	}
}

// tryAcquire reserves an admission slot without blocking.
func (c *scrapeCoordinator) tryAcquire() bool {
	select {
	case c.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// release returns one previously acquired admission slot.
func (c *scrapeCoordinator) release() {
	<-c.slots
}

// wait blocks until the current producer finishes or ctx expires. Call it only
// after stopping new scrape requests so another flight cannot start.
func (c *scrapeCoordinator) wait(ctx context.Context) error {
	c.mu.Lock()
	flight := c.inFlight
	c.mu.Unlock()
	if flight == nil {
		return nil
	}

	select {
	case <-flight.done:
		return nil
	case <-ctx.Done():
		select {
		case <-flight.done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

// do joins the current flight or starts one producer. A canceled caller stops
// waiting without canceling the producer.
func (c *scrapeCoordinator) do(
	ctx context.Context,
	produce func() ([]byte, error),
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	flight := c.inFlight
	if flight == nil {
		flight = &scrapeFlight{
			done: make(chan struct{}),
		}
		c.inFlight = flight

		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error(
						"Scrape producer panicked.",
						slog.Any("panic", recovered),
						slog.String(logging.StackTrace, string(debug.Stack())),
					)
					flight.response = nil
					flight.err = fmt.Errorf("scrape producer panic: %v", recovered)
				}

				c.mu.Lock()
				c.inFlight = nil
				close(flight.done)
				c.mu.Unlock()
			}()

			flight.response, flight.err = produce()
		}()
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.response, flight.err
	}
}
