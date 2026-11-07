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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDebugDumpLimitsFromFile checks that the file sets each dump limit and
// records where it came from.
func TestDebugDumpLimitsFromFile(t *testing.T) {
	y, config := parseAndApply(t, `version: 2
debug_dump:
  min_interval: 5s
  max_file_bytes: 10
  max_total_bytes: 20
  max_files: 2
`)

	assert.Equal(t, 5*time.Second, config.DumpConfig.MinInterval)
	assert.Equal(t, int64(10), config.DumpConfig.MaxFileBytes)
	assert.Equal(t, int64(20), config.DumpConfig.MaxTotalBytes)
	assert.Equal(t, int64(2), config.DumpConfig.MaxFiles)
	origins := y.SettingOrigins()
	for field, key := range map[string]string{
		"DumpConfig.MinInterval":   "debug_dump.min_interval",
		"DumpConfig.MaxFileBytes":  "debug_dump.max_file_bytes",
		"DumpConfig.MaxTotalBytes": "debug_dump.max_total_bytes",
		"DumpConfig.MaxFiles":      "debug_dump.max_files",
	} {
		assert.Contains(t, origins[field], key, field)
	}
}

// TestDebugDumpMinIntervalMustBePositive checks that the file cannot set a
// zero or malformed dump interval.
func TestDebugDumpMinIntervalMustBePositive(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "soon"} {
		t.Run(value, func(t *testing.T) {
			y, err := ParseYAMLConfig([]byte("version: 2\ndebug_dump: {min_interval: " + value + "}\n"))
			if err == nil {
				err = y.ApplyTo(DefaultConfig())
			}
			require.ErrorContains(t, err, "debug_dump.min_interval")
		})
	}
}

// TestDumpConfigValidate checks each dump setting the file dumper cannot use,
// and that the defaults are usable.
func TestDumpConfigValidate(t *testing.T) {
	require.NoError(t, DefaultDumpConfig().Validate())

	tests := []struct {
		name   string
		change func(*DumpConfig)
		field  string
	}{
		{"empty directory", func(c *DumpConfig) { c.Enabled, c.Directory = true, " " }, "DumpConfig.Directory"},
		{"zero interval", func(c *DumpConfig) { c.MinInterval = 0 }, "DumpConfig.MinInterval"},
		{"zero file bytes", func(c *DumpConfig) { c.MaxFileBytes = 0 }, "DumpConfig.MaxFileBytes"},
		{"negative total bytes", func(c *DumpConfig) { c.MaxTotalBytes = -1 }, "DumpConfig.MaxTotalBytes"},
		{"zero files", func(c *DumpConfig) { c.MaxFiles = 0 }, "DumpConfig.MaxFiles"},
		{"file over total", func(c *DumpConfig) { c.MaxFileBytes = c.MaxTotalBytes + 1 }, "DumpConfig.MaxFileBytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dump := DefaultDumpConfig()
			tt.change(&dump)

			err := dump.Validate()

			var settingErr *SettingError
			require.True(t, errors.As(err, &settingErr), "error %v", err)
			assert.Equal(t, tt.field, settingErr.Field)
		})
	}
}

// TestDebugDumpRetentionKey checks that debug_dump.retention takes any
// duration of zero or more and rejects the rest at the key.
func TestDebugDumpRetentionKey(t *testing.T) {
	cases := []struct {
		name, value string
		want        time.Duration
		wantErr     string
	}{
		{name: "hours", value: "12h", want: 12 * time.Hour},
		{name: "minutes", value: "90m", want: 90 * time.Minute},
		{name: "mixed", value: "1h30m15s", want: time.Hour + 30*time.Minute + 15*time.Second},
		{name: "zero", value: "0", want: 0},
		{name: "zero with unit", value: "0s", want: 0},
		{name: "negative", value: "-1h", wantErr: "debug_dump.retention: must be 0 or more"},
		{name: "not a duration", value: "soon", wantErr: "debug_dump.retention"},
		{name: "bare number", value: "24", wantErr: "debug_dump.retention"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := "version: 2\ndebug_dump: {retention: '" + tc.value + "'}\n"
			if tc.wantErr != "" {
				requireParseError(t, input, tc.wantErr)
				return
			}

			y, config := parseAndApply(t, input)

			assert.Equal(t, tc.want, config.DumpConfig.Retention)
			assert.Equal(t, "debug_dump.retention", y.SettingOrigins()["DumpConfig.Retention"])
		})
	}
}

// TestDumpRetentionFromHours checks how a --dump-retention value in hours maps
// to a retention: negative values mean no age cleanup, and a value too large
// for a duration is an error naming the flag.
func TestDumpRetentionFromHours(t *testing.T) {
	cases := []struct {
		name    string
		hours   int
		want    time.Duration
		wantErr bool
	}{
		{name: "default", hours: 24, want: 24 * time.Hour},
		{name: "zero", hours: 0, want: 0},
		{name: "negative", hours: -1, want: 0},
		{name: "very negative", hours: -maxDumpRetentionHours - 10, want: 0},
		{name: "largest", hours: maxDumpRetentionHours, want: time.Duration(maxDumpRetentionHours) * time.Hour},
		{name: "too large", hours: maxDumpRetentionHours + 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DumpRetentionFromHours(tc.hours)
			if tc.wantErr {
				require.ErrorContains(t, err, FlagDumpRetention)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestFullConfigRetentionLoadsBack checks that config show writes a retention
// the file key accepts, including 0.
func TestFullConfigRetentionLoadsBack(t *testing.T) {
	for _, retention := range []time.Duration{0, 90 * time.Minute, 24 * time.Hour} {
		config := DefaultConfig()
		config.DumpConfig.Retention = retention

		dump := NewFullConfig(config, ConfigNames{}).DebugDump
		y, reloaded := parseAndApply(t, "version: 2\ndebug_dump: {retention: '"+dump.Retention+"'}\n")

		assert.Equal(t, retention, reloaded.DumpConfig.Retention, dump.Retention)
		assert.Contains(t, y.SettingOrigins(), "DumpConfig.Retention")
	}
}
