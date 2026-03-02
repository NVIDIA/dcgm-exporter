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

package collector

import (
	"fmt"
	"log/slog"
	"slices"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// IsDCGMExpClockEventsCountEnabled checks if the DCGM_EXP_CLOCK_EVENTS_COUNT counter exists
func IsDCGMExpClockEventsCountEnabled(counterList counters.CounterList) bool {
	return slices.ContainsFunc(counterList,
		func(c counters.Counter) bool {
			return c.FieldName == counters.DCGMExpClockEventsCount
		})
}

type clockEventsCollector struct {
	expCollector
}

type clockEventBitmask int64

// Source of the const values: https://github.com/NVIDIA/DCGM/blob/master/dcgmlib/dcgm_fields.h
const (
	// DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE Nothing is running on the GPU and the clocks are dropping to Idle state
	DCGM_CLOCKS_THROTTLE_REASON_GPU_IDLE clockEventBitmask = 0x0000000000000001
	// DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING GPU clocks are limited by current setting of applications clocks
	DCGM_CLOCKS_THROTTLE_REASON_CLOCKS_SETTING clockEventBitmask = 0x0000000000000002
	// DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP SW Power Scaling algorithm is reducing the clocks below requested clocks
	DCGM_CLOCKS_THROTTLE_REASON_SW_POWER_CAP clockEventBitmask = 0x0000000000000004
	// DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN HW Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_SLOWDOWN clockEventBitmask = 0x0000000000000008
	// DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST Sync Boost
	DCGM_CLOCKS_THROTTLE_REASON_SYNC_BOOST clockEventBitmask = 0x0000000000000010
	// SW Thermal Slowdown
	DCGM_CLOCKS_THROTTLE_REASON_SW_THERMAL clockEventBitmask = 0x0000000000000020
	// DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL HW Thermal Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_THERMAL clockEventBitmask = 0x0000000000000040
	// DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE HW Power Brake Slowdown (reducing the core clocks by a factor of 2 or more) is engaged
	DCGM_CLOCKS_THROTTLE_REASON_HW_POWER_BRAKE clockEventBitmask = 0x0000000000000080
	// DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS GPU clocks are limited by current setting of Display clocks
	DCGM_CLOCKS_THROTTLE_REASON_DISPLAY_CLOCKS clockEventBitmask = 0x0000000000000100
)

var clockEventToString = map[clockEventBitmask]string{
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
func (enm clockEventBitmask) String() string {
	return clockEventToString[enm]
}

func (c *clockEventsCollector) GetMetrics() (MetricsByCounter, error) {
	return c.expCollector.getMetrics()
}

func NewClockEventsCollector(
	counterList counters.CounterList,
	hostname string,
	config *appconfig.Config,
	deviceWatchList devicewatchlistmanager.WatchList,
) (Collector, error) {
	if !IsDCGMExpClockEventsCountEnabled(counterList) {
		slog.Error(counters.DCGMExpClockEventsCount + " collector is disabled")
		return nil, fmt.Errorf(counters.DCGMExpClockEventsCount + " collector is disabled")
	}

	collector := clockEventsCollector{}
	var err error
	deviceWatchList.SetDeviceFields([]dcgm.Short{dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS})

	collector.expCollector, err = newExpCollector(
		counterList.LabelCounters(),
		hostname,
		config,
		deviceWatchList,
	)
	if err != nil {
		return nil, err
	}

	collector.counter = counterList[slices.IndexFunc(counterList, func(c counters.Counter) bool {
		return c.FieldName == counters.DCGMExpClockEventsCount
	})]

	collector.labelFiller = func(metricValueLabels map[string]string, entityValue int64) {
		metricValueLabels["clock_event"] = clockEventBitmask(entityValue).String()
	}

	collector.windowSize = config.ClockEventsCountWindowSize

	collector.fieldValueParser = func(value int64) []int64 {
		var reasons []int64

		// The int64 value may represent multiple events.
		// To extract a specific event, we need to perform an XOR operation with a bitmask.
		reasonBitmask := clockEventBitmask(value)

		for tr := range clockEventToString {
			if reasonBitmask&tr != 0 {
				reasons = append(reasons, int64(tr))
			}
		}

		return reasons
	}

	return &collector, nil
}
