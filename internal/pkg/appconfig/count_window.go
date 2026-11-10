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

package appconfig

import (
	"fmt"
	"time"
)

// Setting field names that origin tracking uses for the settings the count
// window and retention checks name.
const (
	FieldXIDCountWindowSize         = "XIDCountWindowSize"
	FieldClockEventsCountWindowSize = "ClockEventsCountWindowSize"
	FieldWatchMaxAge                = "WatchRetention.MaxAge"
	FieldWatchMaxSamples            = "WatchRetention.MaxSamples"

	// watchGroupFieldFormat names the setting fields of one watch group.
	watchGroupFieldFormat    = "WatchGroups[%d]"
	fieldRetentionMaxAge     = ".Retention.MaxAge"
	fieldRetentionMaxSamples = ".Retention.MaxSamples"
)

// WatchGroupField is the setting field name of the watch group at index.
func WatchGroupField(index int) string {
	return fmt.Sprintf(watchGroupFieldFormat, index)
}

// WatchGroupMaxAgeField is the setting field name of the retention age of the
// watch group at index.
func WatchGroupMaxAgeField(index int) string {
	return WatchGroupField(index) + fieldRetentionMaxAge
}

// WatchGroupMaxSamplesField is the setting field name of the retention sample
// limit of the watch group at index.
func WatchGroupMaxSamplesField(index int) string {
	return WatchGroupField(index) + fieldRetentionMaxSamples
}

// CountWindow describes one windowed count metric: the exporter counter, the
// setting that holds its sampling window and the flag that sets it.
type CountWindow struct {
	// Counter is the exporter counter the window belongs to.
	Counter string
	// Field is the setting field name that origin tracking uses.
	Field string
	// Flag is the flag that sets the window, without the leading dashes.
	Flag string
	// Setting returns the window in milliseconds, where Config keeps it.
	Setting func(*Config) *int
}

// CountWindows lists the windowed count metrics in the order they are reported.
var CountWindows = []CountWindow{
	{
		Counter: ExporterXIDErrorsCountField,
		Field:   FieldXIDCountWindowSize,
		Flag:    FlagXIDCountWindowSize,
		Setting: func(c *Config) *int { return &c.XIDCountWindowSize },
	},
	{
		Counter: ExporterClockEventsCountField,
		Field:   FieldClockEventsCountWindowSize,
		Flag:    FlagClockEventsCountWindowSize,
		Setting: func(c *Config) *int { return &c.ClockEventsCountWindowSize },
	},
}

// CountWindowOf returns the window of the counter named counter, and false when
// the counter has none.
func CountWindowOf(counter string) (CountWindow, bool) {
	for _, window := range CountWindows {
		if window.Counter == counter {
			return window, true
		}
	}

	return CountWindow{}, false
}

// CountWindowCounters lists the counter names in CountWindows.
func CountWindowCounters() []string {
	names := make([]string, 0, len(CountWindows))
	for _, window := range CountWindows {
		names = append(names, window.Counter)
	}

	return names
}

// Duration returns the window the config sets. A value above MaxMilliseconds,
// which validation rejects, is cut to the longest duration so it cannot wrap.
func (w CountWindow) Duration(c *Config) time.Duration {
	return time.Duration(min(*w.Setting(c), MaxMilliseconds)) * time.Millisecond
}
