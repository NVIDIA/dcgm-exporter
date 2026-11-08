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

package counters

import (
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// DCPCapabilities is an immutable snapshot of profiling fields supported by
// the current DCGM topology. The lifecycle coordinator creates it after DCGM
// starts, then passes it to counter parsing without putting runtime discovery
// data into application configuration.
type DCPCapabilities struct {
	metricGroups []dcgm.MetricGroup
}

// NewDCPCapabilities copies DCGM profiling groups into an independent runtime
// snapshot. Call it immediately after querying DCGM so later provider resets
// or callers cannot mutate the groups used by a registry build.
func NewDCPCapabilities(groups []dcgm.MetricGroup) DCPCapabilities {
	return DCPCapabilities{metricGroups: cloneMetricGroups(groups)}
}

// Supports reports whether a profiling field belongs to this capability
// snapshot. Counter parsing calls it for DCGM_FI_PROF fields; all other fields
// do not need a profiling capability check.
func (c DCPCapabilities) Supports(fieldID uint) bool {
	for _, group := range c.metricGroups {
		if slices.Contains(group.FieldIds, fieldID) {
			return true
		}
	}
	return false
}

// cloneMetricGroups copies DCGM metric groups and their nested field lists.
// The capability constructor uses it to keep its snapshot independent from the
// slice returned by DCGM.
func cloneMetricGroups(groups []dcgm.MetricGroup) []dcgm.MetricGroup {
	if groups == nil {
		return nil
	}
	clone := make([]dcgm.MetricGroup, len(groups))
	for i, group := range groups {
		clone[i] = group
		clone[i].FieldIds = slices.Clone(group.FieldIds)
	}
	return clone
}
