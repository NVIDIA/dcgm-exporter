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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
)

// TestCounterSetWatchCounters verifies registry assembly receives each exporter
// backing field exactly once, whether or not the metric source already lists it.
func TestCounterSetWatchCounters(t *testing.T) {
	tests := []struct {
		name string
		set  *CounterSet
		want []dcgm.Short
	}{
		{
			name: "adds both backing fields",
			set: &CounterSet{ExporterCounters: CounterList{
				{FieldID: dcgm.Short(DCGMClockEventsCount)},
				{FieldID: dcgm.Short(DCGMXIDErrorsTotal)},
			}},
			want: []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS},
		},
		{
			name: "does not duplicate configured backing field",
			set: &CounterSet{
				DCGMCounters: CounterList{{FieldID: dcgm.DCGM_FI_DEV_XID_ERRORS}},
				ExporterCounters: CounterList{{
					FieldID: dcgm.Short(DCGMXIDErrorsCount),
				}},
			},
			want: []dcgm.Short{dcgm.DCGM_FI_DEV_XID_ERRORS},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCounters := tt.set.WatchCounters()
			gotFields := make([]dcgm.Short, 0, len(gotCounters))
			for _, counter := range gotCounters {
				gotFields = append(gotFields, counter.FieldID)
			}

			assert.Equal(t, tt.want, gotFields)
		})
	}
}

// TestCounterPredicates expects IsLabel to be true only for the label type and
// IsProfilingMetric to be true only for DCGM_FI_PROF_ fields.
func TestCounterPredicates(t *testing.T) {
	assert.True(t, Counter{PromType: "label"}.IsLabel())
	assert.False(t, Counter{PromType: "gauge"}.IsLabel())

	assert.True(t, Counter{FieldName: "DCGM_FI_PROF_SM_ACTIVE"}.IsProfilingMetric())
	assert.False(t, Counter{FieldName: "DCGM_FI_DEV_GPU_TEMP"}.IsProfilingMetric())
}

// TestCounterListHelpers expects LabelCounters and NonLabelCounters to split a list by
// type, keeping order, and HasProfilingMetrics to be true only when a list holds a PROF field.
func TestCounterListHelpers(t *testing.T) {
	labels := CounterList{
		{FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		{FieldName: "DCGM_FI_DRIVER_VERSION", PromType: "label"},
		{FieldName: "DCGM_FI_PROF_SM_ACTIVE", PromType: "gauge"},
	}

	assert.Equal(t, CounterList{{FieldName: "DCGM_FI_DRIVER_VERSION", PromType: "label"}}, labels.LabelCounters())
	assert.Equal(t, CounterList{
		{FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"},
		{FieldName: "DCGM_FI_PROF_SM_ACTIVE", PromType: "gauge"},
	}, labels.NonLabelCounters())
	assert.True(t, labels.HasProfilingMetrics())
	assert.False(t, CounterList{{FieldName: "DCGM_FI_DEV_GPU_TEMP", PromType: "gauge"}}.HasProfilingMetrics())
}

// TestCounterSetHasProfilingMetrics expects a set to report profiling metrics when either
// its DCGM or its exporter counters hold a PROF field, and not otherwise.
func TestCounterSetHasProfilingMetrics(t *testing.T) {
	assert.True(t, (&CounterSet{
		DCGMCounters: CounterList{{FieldName: "DCGM_FI_PROF_SM_ACTIVE"}},
	}).HasProfilingMetrics())

	assert.True(t, (&CounterSet{
		ExporterCounters: CounterList{{FieldName: "DCGM_FI_PROF_GR_ENGINE_ACTIVE"}},
	}).HasProfilingMetrics())

	assert.False(t, (&CounterSet{
		DCGMCounters:     CounterList{{FieldName: "DCGM_FI_DEV_GPU_TEMP"}},
		ExporterCounters: CounterList{{FieldName: "DCGM_EXP_XID_ERRORS_COUNT"}},
	}).HasProfilingMetrics())
}

// TestCounterSetWatchCountersNamesTheFieldsAsDCGMDoes checks the added fields
// carry the names DCGM gives them, because override patterns match these names.
func TestCounterSetWatchCountersNamesTheFieldsAsDCGMDoes(t *testing.T) {
	set := &CounterSet{ExporterCounters: CounterList{
		{FieldID: dcgm.Short(DCGMClockEventsTotal)},
		{FieldID: dcgm.Short(DCGMXIDErrorsCount)},
	}}

	names := map[dcgm.Short]string{}
	for _, counter := range set.WatchCounters() {
		names[counter.FieldID] = counter.FieldName
	}

	assert.Equal(t, map[dcgm.Short]string{
		dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS: "DCGM_FI_DEV_CLOCKS_EVENT_REASONS",
		dcgm.DCGM_FI_DEV_XID_ERROR:            "DCGM_FI_DEV_XID_ERROR",
	}, names)

	assert.Nil(t, (*CounterSet)(nil).WatchCounters())
}
