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

	"golang.org/x/sync/errgroup"
)

type Registry struct {
	collectors []Collector
	mtx        sync.RWMutex
}

func NewRegistry() *Registry {
	return &Registry{
		collectors: make([]Collector, 0),
	}
}

// Register registers a collector with the registry.
func (r *Registry) Register(c Collector) {
	r.collectors = append(r.collectors, c)
}

// Gather gathers metrics from all registered collectors.
func (r *Registry) Gather() (MetricsByCounter, error) {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	var wg sync.WaitGroup
	wg.Add(len(r.collectors))

	g := new(errgroup.Group)

	var sm sync.Map

	for _, c := range r.collectors {
		c := c // creates new c, see https://golang.org/doc/faq#closures_and_goroutines
		g.Go(func() error {
			metrics, err := c.GetMetrics()

			if err != nil {
				return err
			}

			for counter, metricVals := range metrics {
				val, _ := sm.LoadOrStore(counter, []Metric{})
				out := val.([]Metric)
				out = append(out, metricVals...)
				sm.Store(counter, out)
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	output := MetricsByCounter{}

	sm.Range(func(key, value interface{}) bool {
		output[key.(Counter)] = value.([]Metric)
		return true // continue iteration
	})

	return output, nil
}

// Cleanup resources of registered collectors
func (r *Registry) Cleanup() {
	for _, c := range r.collectors {
		c.Cleanup()
	}
}
