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
	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

type Counter struct {
	FieldID   dcgm.Short
	FieldName string
	PromType  string
	Help      string
}

func (c Counter) IsLabel() bool {
	return c.PromType == "label"
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

type CounterSet struct {
	DCGMCounters     CounterList
	ExporterCounters CounterList
}
