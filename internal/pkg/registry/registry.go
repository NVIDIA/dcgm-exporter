/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package registry

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// ErrRegistryShuttingDown is returned when Gather() is called on a registry that is shutting down
var ErrRegistryShuttingDown = errors.New("registry is shutting down")

// groupCounterTuple represents a composite key, that consists Group and Counter.
// The groupCounterTuple is necessary to maintain uniqueness of Group and Counter pairs.
type groupCounterTuple struct {
	Group   dcgm.Field_Entity_Group
	Counter counters.Counter
}

type Registry struct {
	collectorGroups     map[dcgm.Field_Entity_Group][]collector.Collector
	collectorGroupsSeen map[collector.EntityCollectorTuple]struct{}
	mtx                 sync.RWMutex
	activeGathers       atomic.Int32 // Tracks in-flight Gather() calls for safe cleanup
	shuttingDown        atomic.Bool  // Signals that cleanup is imminent
}

// NewRegistry creates a new registry
func NewRegistry() *Registry {
	return &Registry{
		collectorGroups:     map[dcgm.Field_Entity_Group][]collector.Collector{},
		collectorGroupsSeen: map[collector.EntityCollectorTuple]struct{}{},
	}
}

// Register registers a collector with the registry.
func (r *Registry) Register(entityCollectorTuples collector.EntityCollectorTuple) {
	if _, exists := r.collectorGroupsSeen[entityCollectorTuples]; exists {
		return
	}
	r.collectorGroups[entityCollectorTuples.Entity()] = append(r.collectorGroups[entityCollectorTuples.Entity()],
		entityCollectorTuples.Collector())
	r.collectorGroupsSeen[entityCollectorTuples] = struct{}{}
}

// Gather gathers metrics from all registered collectors.
func (r *Registry) Gather() (MetricsByCounterGroup, error) {
	// Check if registry is shutting down
	if r.shuttingDown.Load() {
		return nil, ErrRegistryShuttingDown
	}

	// Track this gather operation for safe cleanup
	r.activeGathers.Add(1)
	defer r.activeGathers.Add(-1)

	// Use RLock instead of Lock to allow concurrent gathers
	// This is safe because we don't modify collectorGroups during gather
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	// Double-check shutdown flag after acquiring lock
	if r.shuttingDown.Load() {
		return nil, ErrRegistryShuttingDown
	}

	var wg sync.WaitGroup

	g := new(errgroup.Group)

	var sm sync.Map

	for group, collectors := range r.collectorGroups {
		for _, c := range collectors {
			c := c // creates new c, see https://golang.org/doc/faq#closures_and_goroutines
			group := group
			wg.Add(1)
			g.Go(func() error {
				metrics, err := c.GetMetrics()
				if err != nil {
					return err
				}

				for counter, metricVals := range metrics {
					val, _ := sm.LoadOrStore(groupCounterTuple{Group: group, Counter: counter}, []collector.Metric{})
					out := val.([]collector.Metric)
					out = append(out, metricVals...)
					sm.Store(groupCounterTuple{Group: group, Counter: counter}, out)
				}

				return nil
			})
		}
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	output := MetricsByCounterGroup{}

	sm.Range(func(key, value interface{}) bool {
		tuple := key.(groupCounterTuple)
		if _, exists := output[tuple.Group]; !exists {
			output[tuple.Group] = map[counters.Counter][]collector.Metric{}
		}
		output[tuple.Group][tuple.Counter] = value.([]collector.Metric)
		return true // continue iteration
	})

	return output, nil
}

// Cleanup resources of registered collectors
// This method uses reference counting to wait for in-flight Gather() calls
// to complete before cleaning up DCGM resources, avoiding use-after-free.
func (r *Registry) Cleanup() {
	// Signal that we're shutting down - prevents new Gather() calls
	r.shuttingDown.Store(true)

	// Wait for all active Gather() calls to complete
	// Poll with exponential backoff for efficiency
	maxWaitTime := 2 * time.Second
	startTime := time.Now()
	sleepTime := 1 * time.Millisecond

	for {
		active := r.activeGathers.Load()
		if active == 0 {
			break
		}

		elapsed := time.Since(startTime)
		if elapsed >= maxWaitTime {
			// This shouldn't happen in normal operation, but log it if it does
			slog.Warn("Registry cleanup timed out waiting for active gathers",
				slog.Int("active_gathers", int(active)),
				slog.Duration("elapsed", elapsed))
			break
		}

		slog.Debug("Waiting for active gathers to complete before cleanup",
			slog.Int("active_gathers", int(active)),
			slog.Duration("elapsed", elapsed))

		time.Sleep(sleepTime)

		// Exponential backoff: 1ms, 2ms, 4ms, 8ms, 16ms, ...
		sleepTime *= 2
		if sleepTime > 100*time.Millisecond {
			sleepTime = 100 * time.Millisecond // Cap at 100ms
		}
	}

	// Now safe to cleanup - all Gather() calls have completed
	r.mtx.Lock()
	defer r.mtx.Unlock()

	for _, collectors := range r.collectorGroups {
		for _, c := range collectors {
			c.Cleanup()
		}
	}
}
