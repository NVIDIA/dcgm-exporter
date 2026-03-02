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

package registry

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	collectorpkg "github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

type mockCollector struct {
	mock.Mock
}

func (m *mockCollector) GetMetrics() (collectorpkg.MetricsByCounter, error) {
	args := m.Called()
	return args.Get(0).(collectorpkg.MetricsByCounter), args.Error(1)
}

func (m *mockCollector) Cleanup() {
	m.Called()
}

func TestRegistry_Gather(t *testing.T) {
	collector := new(mockCollector)

	metrics := collectorpkg.MetricsByCounter{}
	counterA := counters.Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}

	metrics[counterA] = append(metrics[counterA], collectorpkg.Metric{
		GPU:        "0",
		Counter:    counterA,
		Attributes: map[string]string{},
	})

	counterB := counters.Counter{
		FieldName: "DCGM_FI_EXP_CLOCK_THROTTLE_REASONS_COUNT",
		PromType:  "gauge",
	}

	metrics[counterB] = append(metrics[counterB], collectorpkg.Metric{
		GPU:        "0",
		Counter:    counterB,
		Value:      "42",
		Attributes: map[string]string{},
	})

	type test struct {
		name           string
		collectorState func() *mock.Call
		assert         func(MetricsByCounterGroup, error)
	}

	tests := []test{
		{
			name: "When collector return no errors",
			collectorState: func() *mock.Call {
				cs := collector.On("GetMetrics").Return(metrics, nil)
				cs.On("Cleanup").Return()
				return cs
			},
			assert: func(mbcg MetricsByCounterGroup, err error) {
				require.NoError(t, err)
				require.Contains(t, mbcg, dcgm.FE_GPU)
				require.Len(t, mbcg, 1)
				require.Len(t, mbcg[dcgm.FE_GPU], 2)
			},
		},
		{
			name: "When collector return errors",
			collectorState: func() *mock.Call {
				cs := collector.On("GetMetrics").Return(collectorpkg.MetricsByCounter{}, errors.New("Boom!"))
				cs.On("Cleanup").Return()
				return cs
			},
			assert: func(mbcg MetricsByCounterGroup, err error) {
				require.Error(t, err)
				require.Len(t, mbcg, 0)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := NewRegistry()
			newEntityCollectorTuple := collectorpkg.EntityCollectorTuple{}
			newEntityCollectorTuple.SetEntity(dcgm.FE_GPU)
			newEntityCollectorTuple.SetCollector(collector)
			reg.Register(newEntityCollectorTuple)
			mockCall := tc.collectorState()
			got, err := reg.Gather()
			tc.assert(got, err)
			mockCall.Unset()
			reg.Cleanup()
		})
	}
}

func TestRegistry_Register_Accepts_Duplicates_(t *testing.T) {
	reg := NewRegistry()
	collector := new(mockCollector)

	newEntityCollectorTuple1 := collectorpkg.EntityCollectorTuple{}
	newEntityCollectorTuple1.SetEntity(dcgm.FE_GPU)
	newEntityCollectorTuple1.SetCollector(collector)

	newEntityCollectorTuple2 := collectorpkg.EntityCollectorTuple{}
	newEntityCollectorTuple2.SetEntity(dcgm.FE_GPU)
	newEntityCollectorTuple2.SetCollector(collector)

	reg.Register(newEntityCollectorTuple1)
	reg.Register(newEntityCollectorTuple2)
	assert.Len(t, reg.collectorGroups, 1)
	assert.Len(t, reg.collectorGroupsSeen, 1)
}
