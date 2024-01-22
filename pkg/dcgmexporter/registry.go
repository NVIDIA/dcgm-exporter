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

import "sync"

type Registry struct {
	collectors []Collector
	mtx        sync.RWMutex
}

func NewRegistry() *Registry {
	return &Registry{
		collectors: make([]Collector, 0),
	}
}

func (r *Registry) Register(c Collector) {
	r.collectors = append(r.collectors, c)
}

func (r *Registry) Gather() (map[Counter][]Metric, error) {
	r.mtx.Lock()
	defer r.mtx.Unlock()

	output := map[Counter][]Metric{}

	for _, c := range r.collectors {
		metrics, err := c.GetMetrics()

		if err != nil {
			return nil, err
		}

		for counter, metricVals := range metrics {
			output[counter] = append(output[counter], metricVals...)
		}
	}

	return output, nil
}
