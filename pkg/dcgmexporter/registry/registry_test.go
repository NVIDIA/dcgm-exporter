/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	m "github.com/NVIDIA/dcgm-exporter/mocks/pkg/dcgmexporter/collector"
	"github.com/NVIDIA/dcgm-exporter/pkg/common"
	"github.com/NVIDIA/dcgm-exporter/pkg/dcgmexporter/collector"
)

func TestRegistry_Gather(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	newCollector := m.NewMockCollector(ctrl)
	reg := NewRegistry(&common.Config{})

	metrics := collector.MetricsByCounter{}
	counterA := common.Counter{
		FieldID:   155,
		FieldName: "DCGM_FI_DEV_POWER_USAGE",
		PromType:  "gauge",
	}
	metrics[counterA] = append(metrics[counterA], collector.Metric{
		GPU:        "0",
		Counter:    counterA,
		Attributes: map[string]string{},
	})

	counterB := common.Counter{
		FieldName: "DCGM_FI_EXP_CLOCK_THROTTLE_REASONS_COUNT",
		PromType:  "gauge",
	}

	metrics[counterB] = append(metrics[counterB], collector.Metric{
		GPU:        "0",
		Counter:    counterB,
		Value:      "42",
		Attributes: map[string]string{},
	})

	type test struct {
		name           string
		collectorState func() *gomock.Call
		assert         func(collector.MetricsByCounter, error)
	}

	tests := []test{
		{
			name: "When collector return no errors",
			collectorState: func() *gomock.Call {
				return newCollector.EXPECT().GetMetrics().Return(metrics, nil)
			},
			assert: func(mbc collector.MetricsByCounter, err error) {
				require.NoError(t, err)
				require.Len(t, mbc, 2)
			},
		},
		{
			name: "When collector return errors",
			collectorState: func() *gomock.Call {
				return newCollector.EXPECT().GetMetrics().Return(collector.MetricsByCounter{}, errors.New("Boom!"))
			},
			assert: func(mbc collector.MetricsByCounter, err error) {
				require.Error(t, err)
				require.Len(t, mbc, 0)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg.collectors = nil
			reg.Register(newCollector)
			tc.collectorState()
			got, err := reg.Gather()
			tc.assert(got, err)
		})

	}
}
