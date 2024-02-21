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

package dcgmexporter

import (
	"fmt"
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/sirupsen/logrus"
)

// IsDCGMExpClockThrottleReasonsEnabledCount checks if the DCGM_FI_EXP_CLOCK_THROTTLE_REASONS_COUNT counter exists
func IsDCGMExpClockThrottleReasonsEnabledCount(counters []Counter) bool {
	return slices.ContainsFunc(counters,
		func(c Counter) bool {
			return c.FieldName == dcgmExpClockThrottleReasonsCount
		})
}

type clocksThrottleReasonsCollector struct {
	expCollector
}

type clocksThrottleReasonBitmask int64

const (
	// DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE Nothing is running on the GPU and the clocks are dropping to Idle state
	DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE clocksThrottleReasonBitmask = 0x0000000000000001
	// DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING GPU clocks are limited by current setting of applications clocks
	DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING clocksThrottleReasonBitmask = 0x0000000000000002
	// DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP SW Power Scaling algorithm is reducing the clocks below requested clocks
	DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP clocksThrottleReasonBitmask = 0x0000000000000004
	// DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN HW Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN clocksThrottleReasonBitmask = 0x0000000000000008
	// DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST Sync Boost
	DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST clocksThrottleReasonBitmask = 0x0000000000000010
	//SW Thermal Slowdown
	DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL clocksThrottleReasonBitmask = 0x0000000000000020
	// DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL HW Thermal Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL clocksThrottleReasonBitmask = 0x0000000000000040
	// DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE HW Power Brake Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE clocksThrottleReasonBitmask = 0x0000000000000080
	// DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS GPU clocks are limited by current setting of Display clocks
	DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS clocksThrottleReasonBitmask = 0x0000000000000100
)

var clocksThrottleReasonToString = map[clocksThrottleReasonBitmask]string{
	// See: https://github.com/NVIDIA/DCGM/blob/6792b70c65b938d17ac9d791f59ceaadc0c7ef8a/dcgmi/CommandLineParser.cpp#L63
	DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE:       "gpu_idle",
	DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING: "clocks_setting",
	DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP:   "power_cap",
	DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN:    "hw_slowdown",
	DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST:     "sync_boost",
	DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL:     "sw_thermal",
	DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL:     "hw_thermal",
	DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE: "hw_power_brake",
	DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS: "display_clocks",
}

// String method to convert the enum value to a string
func (enm clocksThrottleReasonBitmask) String() string {
	return clocksThrottleReasonToString[enm]
}

func (c *clocksThrottleReasonsCollector) GetMetrics() (MetricsByCounter, error) {
	return c.expCollector.getMetrics()
}

func NewClocksThrottleReasonsCollector(counters []Counter,
	hostname string,
	config *Config,
	fieldEntityGroupTypeSystemInfo FieldEntityGroupTypeSystemInfoItem) (Collector, error) {
	if !IsDCGMExpClockThrottleReasonsEnabledCount(counters) {
		logrus.Error(dcgmExpClockThrottleReasonsCount + " collector is disabled")
		return nil, fmt.Errorf(dcgmExpClockThrottleReasonsCount + " collector is disabled")
	}

	collector := clocksThrottleReasonsCollector{}
	collector.expCollector = newExpCollector(
		counters,
		hostname,
		[]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCK_THROTTLE_REASONS},
		config,
		fieldEntityGroupTypeSystemInfo,
	)

	collector.counter = counters[slices.IndexFunc(counters, func(c Counter) bool {
		return c.FieldName == dcgmExpClockThrottleReasonsCount
	})]

	collector.labelFiller = func(metricValueLabels map[string]string, entityValue int64) {
		metricValueLabels["throttle_reason"] = clocksThrottleReasonBitmask(entityValue).String()
	}

	collector.windowSize = config.ClockThrottleReasonsCountWindowSize

	collector.fieldValueParser = func(value int64) []int64 {
		var reasons []int64

		// The int64 value may represent multiple reasons.
		// To extract a specific reason, we need to perform an XOR operation with a bitmask.
		reasonBitmask := clocksThrottleReasonBitmask(value)

		for tr := range clocksThrottleReasonToString {
			if reasonBitmask&tr != 0 {
				reasons = append(reasons, int64(tr))
			}
		}

		return reasons
	}

	return &collector, nil
}
