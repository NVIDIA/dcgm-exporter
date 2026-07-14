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

	"github.com/stretchr/testify/assert"
)

func TestCounterPredicates(t *testing.T) {
	assert.True(t, Counter{PromType: "label"}.IsLabel())
	assert.False(t, Counter{PromType: "gauge"}.IsLabel())

	assert.True(t, Counter{FieldName: "DCGM_FI_PROF_SM_ACTIVE"}.IsProfilingMetric())
	assert.False(t, Counter{FieldName: "DCGM_FI_DEV_GPU_TEMP"}.IsProfilingMetric())
}

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
