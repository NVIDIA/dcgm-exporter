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

package devicewatchlistmanager

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
)

// countWindowSet is a metric source that enables the counters named by names.
func countWindowSet(names ...string) *counters.CounterSet {
	set := &counters.CounterSet{
		DCGMCounters: counters.CounterList{{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP"}},
	}

	for _, name := range names {
		set.ExporterCounters = append(set.ExporterCounters, counters.Counter{
			FieldID: dcgm.Short(counters.DCGMFields[name]), FieldName: name,
		})
	}

	return set
}

// countWindowResolved is a default configuration with the XID and clock events
// windows and the global retention age set, and the origins the test names.
func countWindowResolved(window, maxAge time.Duration, origins map[string]appconfig.SettingOrigin) *appconfig.Resolved {
	config := appconfig.DefaultConfig()
	config.XIDCountWindowSize = int(window.Milliseconds())
	config.ClockEventsCountWindowSize = int(window.Milliseconds())
	config.WatchRetention.MaxAge = maxAge

	return &appconfig.Resolved{Config: config, Origins: origins}
}

// withSamples sets the global retention sample limit of r.
func withSamples(r *appconfig.Resolved, samples int64) *appconfig.Resolved {
	r.Config.WatchRetention.MaxSamples = samples

	return r
}

// TestCountWindowWarnings checks which windows get a warning and how it names
// the settings involved.
func TestCountWindowWarnings(t *testing.T) {
	xid := appconfig.ExporterXIDErrorsCountField
	clock := appconfig.ExporterClockEventsCountField
	fileWindow := map[string]appconfig.SettingOrigin{
		appconfig.FieldXIDCountWindowSize:         {Kind: appconfig.OriginFile, Key: "metrics[0].overrides[1].window"},
		appconfig.FieldClockEventsCountWindowSize: {Kind: appconfig.OriginFile, Key: "metrics[0].overrides[1].window"},
	}
	flagWindow := map[string]appconfig.SettingOrigin{
		appconfig.FieldXIDCountWindowSize: {Kind: appconfig.OriginFlag, Name: "--xid-count-window-size"},
		appconfig.FieldWatchMaxAge: {
			Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_WATCH_MAX_KEEP_AGE",
		},
	}

	tests := map[string]struct {
		set     *counters.CounterSet
		counter string
		setup   func() *appconfig.Resolved
		want    string // empty means no warning
	}{
		"window shorter than the age": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved { return countWindowResolved(5*time.Minute, 10*time.Minute, fileWindow) },
		},
		"window equal to the age": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved { return countWindowResolved(10*time.Minute, 10*time.Minute, fileWindow) },
		},
		"window longer than the age, window from the file": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved { return countWindowResolved(20*time.Minute, 10*time.Minute, fileWindow) },
			want: "metrics[0].overrides[1].window: the 20m window is longer than " +
				"--watch-max-keep-age (10m), so DCGM_EXP_XID_ERRORS_COUNT can count only the last 10m",
		},
		"window and age from a flag and the environment": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved { return countWindowResolved(30*time.Minute, 15*time.Minute, flagWindow) },
			want: "--xid-count-window-size: the 30m window is longer than " +
				"DCGM_EXPORTER_WATCH_MAX_KEEP_AGE (15m), so DCGM_EXP_XID_ERRORS_COUNT can count only the last 15m",
		},
		"no age limit": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved { return withSamples(countWindowResolved(time.Hour, 0, fileWindow), 1000) },
		},
		"the sample limit does not limit the time of XID events": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved { return withSamples(countWindowResolved(20*time.Minute, 0, fileWindow), 39) },
		},
		"the age limit is named when the sample limit is tighter for XID events": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved {
				return withSamples(countWindowResolved(20*time.Minute, 15*time.Minute, fileWindow), 10)
			},
			want: "metrics[0].overrides[1].window: the 20m window is longer than " +
				"--watch-max-keep-age (15m), so DCGM_EXP_XID_ERRORS_COUNT can count only the last 15m",
		},
		"clock events sample limit shorter than the window": {
			set: countWindowSet(clock), counter: clock,
			setup: func() *appconfig.Resolved { return withSamples(countWindowResolved(20*time.Minute, 0, fileWindow), 39) },
			want: "metrics[0].overrides[1].window: the 20m window is longer than the 39 samples that " +
				"--watch-max-keep-samples keeps at one sample every 30s, " +
				"so DCGM_EXP_CLOCK_EVENTS_COUNT can count only the last 19m",
		},
		"clock events sample limit covers the window (41 samples span 40 intervals)": {
			set: countWindowSet(clock), counter: clock,
			setup: func() *appconfig.Resolved { return withSamples(countWindowResolved(20*time.Minute, 0, fileWindow), 41) },
		},
		"the tighter of the age and sample limits is named for clock events": {
			set: countWindowSet(clock), counter: clock,
			setup: func() *appconfig.Resolved {
				return withSamples(countWindowResolved(20*time.Minute, 15*time.Minute, fileWindow), 10)
			},
			want: "metrics[0].overrides[1].window: the 20m window is longer than the 10 samples that " +
				"--watch-max-keep-samples keeps at one sample every 30s, " +
				"so DCGM_EXP_CLOCK_EVENTS_COUNT can count only the last 4m30s",
		},
		"clock events window on the global path is named by its flag": {
			set: countWindowSet(clock), counter: clock,
			setup: func() *appconfig.Resolved { return countWindowResolved(5*time.Minute, time.Minute, nil) },
			want: "--clock-events-count-window-size: the 5m window is longer than " +
				"--watch-max-keep-age (1m), so DCGM_EXP_CLOCK_EVENTS_COUNT can count only the last 1m",
		},
		"a sample limit and interval too large to multiply do not warn": {
			set: countWindowSet(clock), counter: clock,
			setup: func() *appconfig.Resolved {
				r := withSamples(countWindowResolved(20*time.Minute, 0, fileWindow), math.MaxInt32)
				r.Config.CollectInterval = appconfig.MaxMilliseconds

				return r
			},
		},
		"metric not enabled": {
			set: countWindowSet(), counter: xid,
			setup: func() *appconfig.Resolved { return countWindowResolved(20*time.Minute, 10*time.Minute, fileWindow) },
		},
		"other count metric enabled": {
			set: countWindowSet(clock), counter: xid,
			setup: func() *appconfig.Resolved {
				r := countWindowResolved(20*time.Minute, 10*time.Minute, fileWindow)
				r.Config.ClockEventsCountWindowSize = int(time.Minute.Milliseconds())

				return r
			},
		},
		"default window with a short age is named by its flag": {
			set: countWindowSet(xid), counter: xid,
			setup: func() *appconfig.Resolved { return countWindowResolved(5*time.Minute, time.Minute, nil) },
			want: "--xid-count-window-size: the 5m window is longer than " +
				"--watch-max-keep-age (1m), so DCGM_EXP_XID_ERRORS_COUNT can count only the last 1m",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			warnings := CountWindowWarnings(tc.setup(), tc.set)
			if tc.want == "" {
				assert.Empty(t, warnings)
				return
			}

			require.Len(t, warnings, 1)
			assert.Equal(t, tc.want, warnings[0].Message)
			assert.Equal(t, []any{slog.String("metric", tc.counter)}, warnings[0].Attrs)
		})
	}
}

// clockGroupResolved is a configuration whose clock events window is five
// minutes and whose watch groups are the temperature group and a clock events
// group that sets the retention values given.
func clockGroupResolved(clocks appconfig.WatchRetentionOverride, origins map[string]appconfig.SettingOrigin) *appconfig.Resolved {
	age := 2 * time.Minute
	r := &appconfig.Resolved{Config: appconfig.DefaultConfig(), Origins: origins}
	r.Config.ClockEventsCountWindowSize = int((5 * time.Minute).Milliseconds())
	r.Config.WatchGroups = []appconfig.WatchGroup{
		{
			Name: "temps", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}, ExtendedMatch: true,
			Retention: appconfig.WatchRetentionOverride{MaxAge: &age},
		},
		{
			Name: "clocks", Interval: 5000, Fields: []string{"DCGM_FI_DEV_CLOCKS_EVENT_REASONS"}, ExtendedMatch: true,
			Retention: clocks,
		},
	}

	return r
}

// TestCountWindowWarningsUseTheWatchGroupThatMatchesTheSourceField checks that
// a watch group matching the source field sets its interval and retention, that
// a group matching other fields does not, and that each limit is named by the
// setting that supplied it.
func TestCountWindowWarningsUseTheWatchGroupThatMatchesTheSourceField(t *testing.T) {
	age := 2 * time.Minute
	samples := int64(10)
	const (
		clockWindow = "metrics[0].overrides[2].window"
		clockKey    = "metrics[0].overrides[1].sources.dcgm.watch."
	)
	origins := map[string]appconfig.SettingOrigin{
		appconfig.FieldClockEventsCountWindowSize: {Kind: appconfig.OriginFile, Key: clockWindow},
		appconfig.WatchGroupMaxAgeField(0): {
			Kind: appconfig.OriginFile, Key: "metrics[0].overrides[0].sources.dcgm.watch.max_keep_age",
		},
		appconfig.WatchGroupMaxAgeField(1):     {Kind: appconfig.OriginFile, Key: clockKey + "max_keep_age"},
		appconfig.WatchGroupMaxSamplesField(1): {Kind: appconfig.OriginFile, Key: clockKey + "max_keep_samples"},
		appconfig.FieldWatchMaxAge:             {Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_WATCH_MAX_KEEP_AGE"},
	}

	tests := map[string]struct {
		retention appconfig.WatchRetentionOverride
		want      string
	}{
		"the group sets age and samples": {
			retention: appconfig.WatchRetentionOverride{MaxAge: &age, MaxSamples: &samples},
			want: clockWindow + ": the 5m window is longer than the 10 samples that " + clockKey +
				"max_keep_samples keeps at one sample every 5s, " +
				"so DCGM_EXP_CLOCK_EVENTS_COUNT can count only the last 45s",
		},
		"the group sets only the age": {
			retention: appconfig.WatchRetentionOverride{MaxAge: &age},
			want: clockWindow + ": the 5m window is longer than " + clockKey +
				"max_keep_age (2m), so DCGM_EXP_CLOCK_EVENTS_COUNT can count only the last 2m",
		},
		"the group sets only the samples and the global age is tighter": {
			retention: appconfig.WatchRetentionOverride{MaxSamples: ptr(int64(1000))},
			want: clockWindow + ": the 5m window is longer than DCGM_EXPORTER_WATCH_MAX_KEEP_AGE (3m), " +
				"so DCGM_EXP_CLOCK_EVENTS_COUNT can count only the last 3m",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := clockGroupResolved(tc.retention, origins)
			if tc.retention.MaxAge == nil {
				r.Config.WatchRetention.MaxAge = 3 * time.Minute
			}

			warnings := CountWindowWarnings(r, countWindowSet(appconfig.ExporterClockEventsCountField))

			require.Len(t, warnings, 1)
			assert.Equal(t, tc.want, warnings[0].Message)
		})
	}
}

// ptr returns a pointer to a copy of value.
func ptr[T any](value T) *T {
	return &value
}

// TestCountWindowWarningMatchesTheInstalledWatch checks that the warning is
// worded from the watch group that the exporter installs for the source field,
// by partitioning the same configuration into the groups the watcher gets.
func TestCountWindowWarningMatchesTheInstalledWatch(t *testing.T) {
	age := 2 * time.Minute
	samples := int64(10)
	r := clockGroupResolved(appconfig.WatchRetentionOverride{MaxAge: &age, MaxSamples: &samples}, nil)
	set := countWindowSet(appconfig.ExporterClockEventsCountField)
	watched := set.WatchCounters()

	fieldIDs := make([]dcgm.Short, 0, len(watched))
	for _, counter := range watched {
		fieldIDs = append(fieldIDs, counter.FieldID)
	}

	installed, err := NewWatchListManager(watched, r.Config).partitionFieldWatchGroups(fieldIDs, int64(r.Config.CollectInterval))
	require.NoError(t, err)

	for _, counter := range watched {
		watch, err := WatchOfField(r.Config, counter.FieldName)
		require.NoError(t, err)

		group, ok := installedGroupOf(installed, counter.FieldID)
		require.True(t, ok, counter.FieldName)
		assert.Equal(t, watch.Interval.Milliseconds(), group.IntervalMSec, counter.FieldName)
		assert.Equal(t, watch.Retention.MaxAge.Seconds(), group.MaxKeepAge, counter.FieldName)
		assert.Equal(t, watch.Retention.MaxSamples, int64(group.MaxKeepSamples), counter.FieldName)
	}

	clocks, _ := installedGroupOf(installed, dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS)
	warnings := CountWindowWarnings(r, set)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].Message, fmt.Sprintf("the %d samples that", clocks.MaxKeepSamples))
	assert.Contains(t, warnings[0].Message, fmt.Sprintf("one sample every %s", appconfig.FormatDuration(
		time.Duration(clocks.IntervalMSec)*time.Millisecond)))
}

// installedGroupOf returns the installed watch group that holds fieldID.
func installedGroupOf(groups []devicewatcher.FieldWatchGroup, fieldID dcgm.Short) (devicewatcher.FieldWatchGroup, bool) {
	for _, group := range groups {
		if slices.Contains(group.Fields, fieldID) {
			return group, true
		}
	}

	return devicewatcher.FieldWatchGroup{}, false
}
