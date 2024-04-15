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

package dcgmexporter

import (
	"sync"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"golang.org/x/sync/errgroup"
)

// groupCollectorTuple represents a composite key, that consists Group and Collector.
// The groupCollectorTuple is necessary to maintain uniqueness of Group and Collector pairs.
type groupCollectorTuple struct {
	Group     dcgm.Field_Entity_Group
	Collector Collector
}

// groupCounterTuple represents a composite key, that consists Group and Counter.
// The groupCounterTuple is necessary to maintain uniqueness of Group and Counter pairs.
type groupCounterTuple struct {
	Group   dcgm.Field_Entity_Group
	Counter Counter
}

type Registry struct {
	collectorGroups     map[dcgm.Field_Entity_Group][]Collector
	collectorGroupsSeen map[groupCollectorTuple]struct{}
	mtx                 sync.RWMutex
}

// NewRegistry creates a new registry
func NewRegistry() *Registry {
	return &Registry{
		collectorGroups:     map[dcgm.Field_Entity_Group][]Collector{},
		collectorGroupsSeen: map[groupCollectorTuple]struct{}{},
	}
}

// Register registers a collector with the registry.
func (r *Registry) Register(group dcgm.Field_Entity_Group, c Collector) {
	if _, exists := r.collectorGroupsSeen[groupCollectorTuple{Group: group, Collector: c}]; exists {
		return
	}
	r.collectorGroups[group] = append(r.collectorGroups[group], c)
	r.collectorGroupsSeen[groupCollectorTuple{Group: group, Collector: c}] = struct{}{}
}

// Gather gathers metrics from all registered collectors.
func (r *Registry) Gather() (MetricsByCounterGroup, error) {
	r.mtx.Lock()
	defer r.mtx.Unlock()

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
					val, _ := sm.LoadOrStore(groupCounterTuple{Group: group, Counter: counter}, []Metric{})
					out := val.([]Metric)
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
			output[tuple.Group] = map[Counter][]Metric{}
		}
		output[tuple.Group][tuple.Counter] = value.([]Metric)
		return true // continue iteration
	})

	return output, nil
}

// Cleanup resources of registered collectors
func (r *Registry) Cleanup() {
	for _, collectors := range r.collectorGroups {
		for _, c := range collectors {
			c.Cleanup()
		}
	}
}
