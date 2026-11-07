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
	"fmt"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// ExporterCounter identifies a metric the exporter computes itself rather than
// reading directly from one DCGM field.
type ExporterCounter uint16

// Exporter counters run from 9001 upward, above the DCGM field IDs.
// DCGMFIUnknown is the zero value.
const (
	DCGMFIUnknown                              ExporterCounter = 0
	DCGMXIDErrorsCount                         ExporterCounter = iota + 9000
	DCGMClockEventsCount                       ExporterCounter = iota + 9000
	DCGMGPUHealthStatus                        ExporterCounter = iota + 9000
	DCGMP2PStatus                              ExporterCounter = iota + 9000
	DCGMXIDErrorsTotal                         ExporterCounter = iota + 9000
	DCGMClockEventsTotal                       ExporterCounter = iota + 9000
	DCGMFieldPolicyEnabled                     ExporterCounter = iota + 9000
	DCGMFieldPolicyCurrentValue                ExporterCounter = iota + 9000
	DCGMFieldPolicyThreshold                   ExporterCounter = iota + 9000
	DCGMFieldPolicyViolationCount              ExporterCounter = iota + 9000
	DCGMFieldPolicyLastTriggerTimestampSeconds ExporterCounter = iota + 9000
	DCGMSystemHealthStatus                     ExporterCounter = iota + 9000
)

// String returns the configuration field name for an exporter counter.
func (enm ExporterCounter) String() string {
	switch enm {
	case DCGMXIDErrorsCount:
		return DCGMExpXIDErrorsCount
	case DCGMClockEventsCount:
		return DCGMExpClockEventsCount
	case DCGMGPUHealthStatus:
		return DCGMExpGPUHealthStatus
	case DCGMP2PStatus:
		return DCGMExpP2PStatus
	case DCGMXIDErrorsTotal:
		return DCGMExpXIDErrorsTotal
	case DCGMClockEventsTotal:
		return DCGMExpClockEventsTotal
	case DCGMFieldPolicyEnabled:
		return DCGMExpFieldPolicyEnabled
	case DCGMFieldPolicyCurrentValue:
		return DCGMExpFieldPolicyCurrentValue
	case DCGMFieldPolicyThreshold:
		return DCGMExpFieldPolicyThreshold
	case DCGMFieldPolicyViolationCount:
		return DCGMExpFieldPolicyViolationCount
	case DCGMFieldPolicyLastTriggerTimestampSeconds:
		return DCGMExpFieldPolicyLastTriggerTimestampSeconds
	case DCGMSystemHealthStatus:
		return DCGMExpSystemHealthStatus
	default:
		return "DCGM_FI_UNKNOWN"
	}
}

// DCGMFields maps each exporter counter's configuration name to its value.
var DCGMFields = map[string]ExporterCounter{
	DCGMXIDErrorsCount.String():                         DCGMXIDErrorsCount,
	DCGMClockEventsCount.String():                       DCGMClockEventsCount,
	DCGMGPUHealthStatus.String():                        DCGMGPUHealthStatus,
	DCGMP2PStatus.String():                              DCGMP2PStatus,
	DCGMXIDErrorsTotal.String():                         DCGMXIDErrorsTotal,
	DCGMClockEventsTotal.String():                       DCGMClockEventsTotal,
	DCGMFieldPolicyEnabled.String():                     DCGMFieldPolicyEnabled,
	DCGMFieldPolicyCurrentValue.String():                DCGMFieldPolicyCurrentValue,
	DCGMFieldPolicyThreshold.String():                   DCGMFieldPolicyThreshold,
	DCGMFieldPolicyViolationCount.String():              DCGMFieldPolicyViolationCount,
	DCGMFieldPolicyLastTriggerTimestampSeconds.String(): DCGMFieldPolicyLastTriggerTimestampSeconds,
	DCGMSystemHealthStatus.String():                     DCGMSystemHealthStatus,
	DCGMFIUnknown.String():                              DCGMFIUnknown,
}

// exporterWatchDependency names a DCGM field that an exporter-owned family
// needs to read. Registry assembly asks CounterSet for these fields before it
// creates watches, keeping family internals out of the command package.
type exporterWatchDependency struct {
	families []ExporterCounter
	field    dcgm.Short
	name     string
	// eventDriven is true when DCGM adds a sample to the field only when an
	// event happens, not on the watch interval.
	eventDriven bool
}

// exporterWatchDependencies is the complete mapping from exporter families to
// their backing DCGM fields, in the order WatchCounters appends them. Families
// that need no DCGM watch are omitted.
var exporterWatchDependencies = []exporterWatchDependency{
	{
		families: []ExporterCounter{DCGMXIDErrorsCount, DCGMXIDErrorsTotal},
		field:    dcgm.DCGM_FI_DEV_XID_ERROR,
		name:     "DCGM_FI_DEV_XID_ERROR",
		// DCGM records an XID sample only when NVML reports an XID event.
		eventDriven: true,
	},
	{
		families: []ExporterCounter{DCGMClockEventsCount, DCGMClockEventsTotal},
		field:    dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS,
		name:     "DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
	},
}

// CountSource is the DCGM field that an exporter count family counts samples of.
type CountSource struct {
	// FieldID is the DCGM field the family reads.
	FieldID dcgm.Short
	// Name is the field name the watch groups match against.
	Name string
	// EventDriven is true when DCGM samples the field only when an event
	// happens, so its history holds events, not one sample per interval.
	EventDriven bool
}

// CountSourceOf returns the DCGM field behind the exporter count family named
// name, and false when name is not a count family.
func CountSourceOf(name string) (CountSource, bool) {
	family, ok := DCGMFields[name]
	if !ok {
		return CountSource{}, false
	}

	for _, dependency := range exporterWatchDependencies {
		if len(dependency.families) > 0 && dependency.families[0] == family {
			return CountSource{FieldID: dependency.field, Name: dependency.name, EventDriven: dependency.eventDriven}, true
		}
	}

	return CountSource{}, false
}

// WatchCounters returns configured DCGM counters plus the deduplicated backing
// fields required by enabled exporter families. The watch-list manager uses
// this complete list, while collector construction remains family-specific.
func (cs *CounterSet) WatchCounters() CounterList {
	if cs == nil {
		return nil
	}

	result := append(CounterList(nil), cs.DCGMCounters...)
	for _, dependency := range exporterWatchDependencies {
		if !hasEnabledExporterFamily(cs.ExporterCounters, dependency.families) || containsCounterField(result, dependency.field) {
			continue
		}
		result = append(result, Counter{FieldID: dependency.field, FieldName: dependency.name})
	}
	return result
}

// hasEnabledExporterFamily reports whether counters includes any family in the
// supplied dependency rule.
func hasEnabledExporterFamily(counters CounterList, families []ExporterCounter) bool {
	for _, counter := range counters {
		for _, family := range families {
			if counter.FieldID == dcgm.Short(family) {
				return true
			}
		}
	}
	return false
}

// containsCounterField reports whether counters already carries fieldID.
func containsCounterField(counters CounterList, fieldID dcgm.Short) bool {
	for _, counter := range counters {
		if counter.FieldID == fieldID {
			return true
		}
	}
	return false
}

// IdentifyMetricType resolves a configuration field name to its exporter counter value.
func IdentifyMetricType(s string) (ExporterCounter, error) {
	mv, ok := DCGMFields[s]
	if !ok {
		return mv, fmt.Errorf("unknown ExporterCounter field '%s'", s)
	}
	return mv, nil
}
