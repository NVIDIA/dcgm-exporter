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
	"slices"
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// deferRestores opens a scope in which the DCGM client restores surviving
// watches once, at its end. Tests replace it.
var deferRestores = dcgmprovider.DeferRestores

// ErrRegistryShuttingDown is returned when work tries to use a registry after
// retirement has stopped new collector registrations and scrapes.
var ErrRegistryShuttingDown = errors.New("registry is shutting down")

// Registry owns the collectors published for one exporter runtime. Gather
// admits a scrape while retirement is not under way; Retire then waits for all
// admitted scrapes and collector cleanup before it closes Done.
type Registry struct {
	collectorGroups     map[dcgm.Field_Entity_Group][]collector.Collector
	collectorGroupsSeen map[collector.EntityCollectorTuple]struct{}
	mtx                 sync.Mutex
	noActiveGathers     *sync.Cond
	activeGathers       int
	shuttingDown        bool

	retireOnce sync.Once
	done       chan struct{}
	retireErr  error
}

// NewRegistry creates an unpublished registry. Callers register every
// collector before handing the registry to the server, then retire it once
// that published runtime is replaced or stops.
func NewRegistry() *Registry {
	r := &Registry{
		collectorGroups:     map[dcgm.Field_Entity_Group][]collector.Collector{},
		collectorGroupsSeen: map[collector.EntityCollectorTuple]struct{}{},
		done:                make(chan struct{}),
	}
	r.noActiveGathers = sync.NewCond(&r.mtx)
	return r
}

// Register adds one collector before retirement begins. It returns
// ErrRegistryShuttingDown instead of silently accepting a late collector that
// would never be safely cleaned up.
func (r *Registry) Register(entityCollectorTuples collector.EntityCollectorTuple) error {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	if r.shuttingDown {
		return ErrRegistryShuttingDown
	}
	if _, exists := r.collectorGroupsSeen[entityCollectorTuples]; exists {
		return nil
	}
	r.collectorGroups[entityCollectorTuples.Entity()] = append(r.collectorGroups[entityCollectorTuples.Entity()],
		entityCollectorTuples.Collector())
	r.collectorGroupsSeen[entityCollectorTuples] = struct{}{}
	return nil
}

// Gather admits one standalone scrape, gathers its metrics, and releases the
// admission when it returns. Server requests that also render metrics use
// GatherAdmitted so their one admission stays open through rendering.
func (r *Registry) Gather() (MetricsByCounterGroup, error) {
	if err := r.BeginUse(); err != nil {
		return nil, err
	}
	defer r.EndUse()
	return r.GatherAdmitted()
}

// GatherAdmitted gathers metrics for an operation that BeginUse already
// accepted. The caller must call EndUse after it has finished every operation
// that can refer to this registry, including rendering a scrape response.
func (r *Registry) GatherAdmitted() (MetricsByCounterGroup, error) {
	r.mtx.Lock()
	collectorGroups := make(map[dcgm.Field_Entity_Group][]collector.Collector, len(r.collectorGroups))
	for group, collectors := range r.collectorGroups {
		collectorGroups[group] = slices.Clone(collectors)
	}
	r.mtx.Unlock()

	output := MetricsByCounterGroup{}
	var metricsByCounterGroupMtx sync.Mutex
	var collectorErrors []error
	var collectorsWG sync.WaitGroup

	for group, collectors := range collectorGroups {
		for _, c := range collectors {
			c := c // creates new c, see https://golang.org/doc/faq#closures_and_goroutines
			group := group
			collectorsWG.Add(1)
			go func() {
				defer collectorsWG.Done()
				metrics, err := c.GetMetrics()
				if err != nil {
					metricsByCounterGroupMtx.Lock()
					collectorErrors = append(collectorErrors, err)
					metricsByCounterGroupMtx.Unlock()
					return
				}

				metricsByCounterGroupMtx.Lock()
				defer metricsByCounterGroupMtx.Unlock()
				mergeCollectorMetrics(output, group, metrics)
			}()
		}
	}

	collectorsWG.Wait()
	if len(collectorErrors) > 0 {
		return nil, errors.Join(collectorErrors...)
	}

	return output, nil
}

// mergeCollectorMetrics runs under the gather's output mutex and detaches newly
// encountered metric slices from collector-owned storage.
func mergeCollectorMetrics(output MetricsByCounterGroup, group dcgm.Field_Entity_Group, metrics collector.MetricsByCounter) {
	for counter, metricVals := range metrics {
		metricsByCounter, exists := output[group]
		if !exists {
			metricsByCounter = collector.MetricsByCounter{}
			output[group] = metricsByCounter
		}
		if existing, exists := metricsByCounter[counter]; exists {
			metricsByCounter[counter] = append(existing, metricVals...)
			continue
		}
		metricsByCounter[counter] = slices.Clone(metricVals)
	}
}

// BeginUse admits one runtime operation while the registry is published. The
// server keeps this admission from its registry snapshot through rendering, so
// retirement waits for the full old-runtime scrape path rather than only the
// native collector calls inside Gather.
func (r *Registry) BeginUse() error {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if r.shuttingDown {
		return ErrRegistryShuttingDown
	}
	r.activeGathers++
	return nil
}

// EndUse releases one successful BeginUse admission and wakes retirement when
// no gather or renderer still refers to the registry's collector generation.
func (r *Registry) EndUse() {
	r.finishGather()
}

// Retire starts the registry's one cleanup operation and returns a channel
// that closes only after all admitted gathers, collector stop work, and
// completion-aware collector cleanup have finished. Repeated calls return the
// same channel, so another owner can wait for an already-started retirement.
func (r *Registry) Retire() <-chan struct{} {
	r.retireOnce.Do(func() {
		go r.retire()
	})
	return r.done
}

// RetirementError returns a panic converted to an error when a collector
// cleanup failed. Owners must treat that result as unsafe for native provider
// teardown because the collector may still hold native resources.
func (r *Registry) RetirementError() error {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return r.retireErr
}

// Cleanup preserves the legacy synchronous cleanup API for callers that do
// not have a deadline. Runtime owners should prefer Retire and wait on its
// returned channel so a stuck native call can be handled without closing its
// provider underneath it.
func (r *Registry) Cleanup() {
	<-r.Retire()
}

// finishGather records that one admitted scrape has stopped using the
// collector set and wakes retirement when it was the last active gather.
func (r *Registry) finishGather() {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.activeGathers--
	if r.activeGathers == 0 {
		r.noActiveGathers.Broadcast()
	}
}

// retire freezes admissions, waits for already admitted scrapes, then releases
// collectors. Its one goroutine is shared by all Retire callers so repeated
// reloads cannot create unbounded cleanup workers.
func (r *Registry) retire() {
	defer close(r.done)
	defer r.recordCleanupPanic()

	r.mtx.Lock()
	r.shuttingDown = true
	for r.activeGathers > 0 {
		r.noActiveGathers.Wait()
	}
	collectors := make([]collector.Collector, 0)
	for _, groupCollectors := range r.collectorGroups {
		collectors = append(collectors, groupCollectors...)
	}
	r.mtx.Unlock()

	// Background pollers stop first and finish their DCGM calls, so none of
	// them reads while the other collectors' restore is deferred.
	var completers []collector.CleanupCompleter
	var others []collector.Collector
	for _, c := range collectors {
		if completionAware, ok := c.(collector.CleanupCompleter); ok {
			c.Cleanup()
			completers = append(completers, completionAware)
			continue
		}

		others = append(others, c)
	}
	for _, completionAware := range completers {
		<-completionAware.CleanupComplete()
	}

	cleanupWithOneRestore(others)
}

// cleanupWithOneRestore cleans up collectors inside one DeferRestores scope,
// so DCGM restores the surviving watches once for all of them instead of once
// per collector. The scope ends even if a cleanup panics.
func cleanupWithOneRestore(collectors []collector.Collector) {
	end := deferRestores()
	defer end()

	for _, c := range collectors {
		c.Cleanup()
	}
}

// recordCleanupPanic converts a collector cleanup panic into retirement state
// so application code can stop safely instead of tearing down native clients.
func (r *Registry) recordCleanupPanic() {
	if recover() != nil {
		r.mtx.Lock()
		r.retireErr = errors.New("registry collector cleanup panicked")
		r.mtx.Unlock()
	}
}
