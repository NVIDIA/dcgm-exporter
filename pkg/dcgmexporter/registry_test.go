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

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	m "github.com/NVIDIA/dcgm-exporter/mocks/pkg/dcgmexporter"
)

func TestRegistry_Gather(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	collector := m.NewMockCollector(ctrl)
	reg := NewRegistry()

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
		collectorState func() *gomock.Call
		assert         func(MetricsByCounter, error)
	}

	tests := []test{
		{
			name: "When collector return no errors",
			collectorState: func() *gomock.Call {
				return collector.EXPECT().GetMetrics().Return(metrics, nil)
			},
			assert: func(mbc MetricsByCounter, err error) {
				require.NoError(t, err)
				require.Len(t, mbc, 2)
			},
		},
		{
			name: "When collector return errors",
			collectorState: func() *gomock.Call {
				return collector.EXPECT().GetMetrics().Return(MetricsByCounter{}, errors.New("Boom!"))
			},
			assert: func(mbc MetricsByCounter, err error) {
				require.Error(t, err)
				require.Len(t, mbc, 0)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg.collectors = nil
			reg.Register(collector)
			tc.collectorState()
			got, err := reg.Gather()
			tc.assert(got, err)
		})

	}
}
