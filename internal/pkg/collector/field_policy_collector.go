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
	"fmt"
	"math"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/dcgmprovider"
)

var fieldPolicyCounterNames = map[string]struct{}{
	counters.DCGMExpFieldPolicyEnabled:                     {},
	counters.DCGMExpFieldPolicyCurrentValue:                {},
	counters.DCGMExpFieldPolicyThreshold:                   {},
	counters.DCGMExpFieldPolicyViolationCount:              {},
	counters.DCGMExpFieldPolicyLastTriggerTimestampSeconds: {},
}

// fieldPolicyCollector reads selected field-policy gauges without changing DCGM policies.
type fieldPolicyCollector struct {
	hostname string
	counters map[string]counters.Counter
}

// IsDCGMExpFieldPolicyEnabled reports whether any supported policy counter is selected.
func IsDCGMExpFieldPolicyEnabled(counterList counters.CounterList) bool {
	for _, counter := range counterList {
		if _, ok := fieldPolicyCounterNames[counter.FieldName]; ok {
			return true
		}
	}
	return false
}

// NewFieldPolicyCollector keeps only supported field-policy counters for one hostname.
func NewFieldPolicyCollector(counterList counters.CounterList, hostname string) (*fieldPolicyCollector, error) {
	selected := make(map[string]counters.Counter, len(fieldPolicyCounterNames))
	for _, counter := range counterList {
		if _, ok := fieldPolicyCounterNames[counter.FieldName]; ok {
			selected[counter.FieldName] = counter
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no field-policy counters are selected")
	}
	return &fieldPolicyCollector{hostname: hostname, counters: selected}, nil
}

// GetMetrics returns policy state with hostname, policy_id, and field_id labels.
func (c *fieldPolicyCollector) GetMetrics() (MetricsByCounter, error) {
	policies, err := dcgmprovider.Client().GetAllFieldPolicies()
	if err != nil {
		return nil, fmt.Errorf("get field policies: %w", err)
	}
	metrics := make(MetricsByCounter, len(c.counters))
	for _, policy := range policies {
		labels := map[string]string{
			"policy_id": strconv.FormatUint(policy.PolicyID, 10),
			"field_id":  strconv.FormatUint(uint64(policy.FieldID), 10),
		}
		c.addMetric(metrics, counters.DCGMExpFieldPolicyEnabled, labels, boolGauge(policy.Enabled))
		c.addMetric(metrics, counters.DCGMExpFieldPolicyCurrentValue, labels, policy.CurrentValue)
		c.addMetric(metrics, counters.DCGMExpFieldPolicyThreshold, labels, policy.Threshold)
		c.addMetric(metrics, counters.DCGMExpFieldPolicyViolationCount, labels, float64(policy.ViolationCount))
		c.addMetric(metrics, counters.DCGMExpFieldPolicyLastTriggerTimestampSeconds, labels, policyTimestampSeconds(policy))
	}
	return metrics, nil
}

// Cleanup satisfies Collector and releases nothing.
func (c *fieldPolicyCollector) Cleanup() {
	// Policy queries allocate no watches or groups for this collector to release.
}

// addMetric appends a finite value only when the requested policy counter is enabled.
func (c *fieldPolicyCollector) addMetric(metrics MetricsByCounter, name string, labels map[string]string, value float64) {
	counter, ok := c.counters[name]
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	metrics[counter] = append(metrics[counter], Metric{
		Counter: counter, Value: strconv.FormatFloat(value, 'g', -1, 64), Hostname: c.hostname, Labels: labels,
	})
}

// boolGauge maps false and true to the Prometheus gauge values zero and one.
func boolGauge(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// policyTimestampSeconds converts a policy's trigger time to Unix seconds, or zero when absent.
func policyTimestampSeconds(policy dcgm.FieldPolicy) float64 {
	if policy.LastTriggeredTimestamp.IsZero() {
		return 0
	}
	return float64(policy.LastTriggeredTimestamp.UnixMicro()) / 1_000_000
}
