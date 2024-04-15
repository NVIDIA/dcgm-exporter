/*
 * Copyright (c) 2021, NVIDIA CORPORATION.  All rights reserved.
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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockCollector struct {
	mock.Mock
}

func (m *mockCollector) GetMetrics() (MetricsByCounter, error) {
	args := m.Called()
	return args.Get(0).(MetricsByCounter), args.Error(1)
}

func (m *mockCollector) Cleanup() {
	m.Called()
}

func TestRegistry_Gather(t *testing.T) {
	collector := new(mockCollector)

	metrics := MetricsByCounter{}
	counterA := Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}

	metrics[counterA] = append(metrics[counterA], Metric{
		GPU:        "0",
		Counter:    counterA,
		Attributes: map[string]string{},
	})

	counterB := Counter{
		FieldName: "DCGM_FI_EXP_CLOCK_THROTTLE_REASONS_COUNT",
		PromType:  "gauge",
	}

	metrics[counterB] = append(metrics[counterB], Metric{
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
				cs := collector.On("GetMetrics").Return(MetricsByCounter{}, errors.New("Boom!"))
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
			reg.Register(dcgm.FE_GPU, collector)
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
	reg.Register(dcgm.FE_GPU, collector)
	reg.Register(dcgm.FE_GPU, collector)
	assert.Len(t, reg.collectorGroups, 1)
	assert.Len(t, reg.collectorGroupsSeen, 1)
}
