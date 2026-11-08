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

package collector

import (
	"errors"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockdcgm "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/dcgmprovider"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

// TestIsDCGMExpFieldPolicyEnabled checks that the helper returns false for a nil list and true when the list
// holds a field policy counter.
func TestIsDCGMExpFieldPolicyEnabled(t *testing.T) {
	assert.False(t, IsDCGMExpFieldPolicyEnabled(nil))
	assert.True(t, IsDCGMExpFieldPolicyEnabled(counters.CounterList{{FieldName: counters.DCGMExpFieldPolicyThreshold}}))
}

// TestFieldPolicyCollectorGetMetrics exports policy values with the trigger timestamp, hostname, and policy labels.
func TestFieldPolicyCollectorGetMetrics(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)

	triggered := time.Date(2026, time.March, 20, 12, 30, 0, 250000000, time.UTC)
	mockDCGM.EXPECT().GetAllFieldPolicies().Return([]dcgm.FieldPolicy{{
		PolicyID: 73, FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, Enabled: true,
		CurrentValue: 82.5, Threshold: 80, ViolationCount: 4, LastTriggeredTimestamp: triggered,
	}}, nil)
	collector, err := NewFieldPolicyCollector(fieldPolicyCounters(), "host-a")
	require.NoError(t, err)

	metrics, err := collector.GetMetrics()
	require.NoError(t, err)
	assert.Equal(t, "1", metrics[fieldPolicyCounter(counters.DCGMExpFieldPolicyEnabled)][0].Value)
	assert.Equal(t, "82.5", metrics[fieldPolicyCounter(counters.DCGMExpFieldPolicyCurrentValue)][0].Value)
	assert.Equal(t, "80", metrics[fieldPolicyCounter(counters.DCGMExpFieldPolicyThreshold)][0].Value)
	assert.Equal(t, "4", metrics[fieldPolicyCounter(counters.DCGMExpFieldPolicyViolationCount)][0].Value)
	timestamp := metrics[fieldPolicyCounter(counters.DCGMExpFieldPolicyLastTriggerTimestampSeconds)][0]
	assert.Equal(t, "1.77400980025e+09", timestamp.Value)
	assert.Equal(t, "host-a", timestamp.Hostname)
	assert.Equal(t, map[string]string{"policy_id": "73", "field_id": "150"}, timestamp.Labels)
}

// TestFieldPolicyCollectorReturnsQueryError checks that a GetAllFieldPolicies failure makes GetMetrics return
// that error wrapped with "get field policies".
func TestFieldPolicyCollectorReturnsQueryError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockDCGM := mockdcgm.NewMockDCGM(ctrl)
	realDCGM := dcgmprovider.Client()
	t.Cleanup(func() { dcgmprovider.SetClient(realDCGM) })
	dcgmprovider.SetClient(mockDCGM)
	wantErr := errors.New("field policy query failed")
	mockDCGM.EXPECT().GetAllFieldPolicies().Return(nil, wantErr)
	collector, err := NewFieldPolicyCollector(fieldPolicyCounters(), "host-a")
	require.NoError(t, err)
	_, err = collector.GetMetrics()
	assert.ErrorIs(t, err, wantErr)
	assert.ErrorContains(t, err, "get field policies")
}

// fieldPolicyCounters returns the complete policy-metric set used by the tests.
func fieldPolicyCounters() counters.CounterList {
	return counters.CounterList{
		fieldPolicyCounter(counters.DCGMExpFieldPolicyEnabled),
		fieldPolicyCounter(counters.DCGMExpFieldPolicyCurrentValue),
		fieldPolicyCounter(counters.DCGMExpFieldPolicyThreshold),
		fieldPolicyCounter(counters.DCGMExpFieldPolicyViolationCount),
		fieldPolicyCounter(counters.DCGMExpFieldPolicyLastTriggerTimestampSeconds),
	}
}

// fieldPolicyCounter creates metadata for one field-policy counter name.
func fieldPolicyCounter(name string) counters.Counter {
	metricType, err := counters.IdentifyMetricType(name)
	if err != nil {
		panic(err)
	}
	return counters.Counter{FieldID: dcgm.Short(metricType), FieldName: name, PromType: "gauge"}
}
