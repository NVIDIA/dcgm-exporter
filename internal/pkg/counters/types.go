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

type Counter struct {
	FieldID   dcgm.Short `json:"field_id"`
	FieldName string     `json:"field_name"`
	PromType  string     `json:"prom_type"`
	Help      string     `json:"help"`
}

func (c Counter) IsLabel() bool {
	return c.PromType == "label"
}

func (c Counter) IsProfilingMetric() bool {
	return strings.HasPrefix(c.FieldName, "DCGM_FI_PROF_")
}

type CounterList []Counter

func (c CounterList) LabelCounters() CounterList {
	var labelsCounters CounterList
	for _, counter := range c {
		if counter.IsLabel() {
			labelsCounters = append(labelsCounters, counter)
		}
	}

	return labelsCounters
}

func (c CounterList) HasProfilingMetrics() bool {
	for _, counter := range c {
		if counter.IsProfilingMetric() {
			return true
		}
	}
	return false
}

type CounterSet struct {
	DCGMCounters     CounterList
	ExporterCounters CounterList
}

func (cs *CounterSet) HasProfilingMetrics() bool {
	return cs.DCGMCounters.HasProfilingMetrics() || cs.ExporterCounters.HasProfilingMetrics()
}
