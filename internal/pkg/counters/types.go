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

package counters

import (
	"strings"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// Counter stores one metric's DCGM or exporter field identity and render metadata.
type Counter struct {
	FieldID   dcgm.Short `json:"field_id"`
	FieldName string     `json:"field_name"`
	PromType  string     `json:"prom_type"`
	Help      string     `json:"help"`
}

// IsLabel reports whether PromType marks this counter as a label-only field.
func (c Counter) IsLabel() bool {
	return c.PromType == "label"
}

// IsProfilingMetric reports whether this counter names a DCGM profiling field.
func (c Counter) IsProfilingMetric() bool {
	return strings.HasPrefix(c.FieldName, "DCGM_FI_PROF_")
}

// CounterList is the ordered set of counters selected for a metric source.
type CounterList []Counter

// LabelCounters returns only label fields, preserving source order.
func (c CounterList) LabelCounters() CounterList {
	var labelsCounters CounterList
	for _, counter := range c {
		if counter.IsLabel() {
			labelsCounters = append(labelsCounters, counter)
		}
	}

	return labelsCounters
}

// NonLabelCounters returns metric counters, preserving source order.
func (c CounterList) NonLabelCounters() CounterList {
	var nonLabelCounters CounterList
	for _, counter := range c {
		if !counter.IsLabel() {
			nonLabelCounters = append(nonLabelCounters, counter)
		}
	}

	return nonLabelCounters
}

// HasProfilingMetrics reports whether any counter is a DCGM profiling field.
func (c CounterList) HasProfilingMetrics() bool {
	for _, counter := range c {
		if counter.IsProfilingMetric() {
			return true
		}
	}
	return false
}

// CounterSet keeps configured DCGM fields separate from exporter-owned counters.
type CounterSet struct {
	DCGMCounters     CounterList
	ExporterCounters CounterList
}

// HasProfilingMetrics reports whether either counter list contains a profiling field.
func (cs *CounterSet) HasProfilingMetrics() bool {
	return cs.DCGMCounters.HasProfilingMetrics() || cs.ExporterCounters.HasProfilingMetrics()
}
