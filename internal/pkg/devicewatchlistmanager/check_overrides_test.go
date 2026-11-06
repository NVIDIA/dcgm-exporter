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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

// overrideTestSet has two DCGM fields and the XID exporter counter, which reads
// a DCGM field that the set does not list.
func overrideTestSet() *counters.CounterSet {
	return &counters.CounterSet{
		DCGMCounters: counters.CounterList{
			{FieldID: dcgm.DCGM_FI_DEV_GPU_TEMP, FieldName: "DCGM_FI_DEV_GPU_TEMP"},
			{FieldID: dcgm.DCGM_FI_DEV_SM_CLOCK, FieldName: "DCGM_FI_DEV_SM_CLOCK"},
		},
		ExporterCounters: counters.CounterList{
			{FieldID: dcgm.Short(counters.DCGMXIDErrorsCount), FieldName: appconfig.ExporterXIDErrorsCountField},
		},
	}
}

// windowOverride builds a counter window override at path that matches items.
func windowOverride(t *testing.T, path string, items ...string) appconfig.WindowOverride {
	t.Helper()
	matcher, err := appconfig.ParseFieldMatcher(items, true)
	require.NoError(t, err)
	return appconfig.WindowOverride{Path: path, Match: matcher}
}

// watchGroup builds a watch group with a one second interval that selects the
// given fields with extended matching.
func watchGroup(name string, fields ...string) appconfig.WatchGroup {
	return appconfig.WatchGroup{Name: name, Interval: 1000, Fields: fields, ExtendedMatch: true}
}

func TestCheckOverrides(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config func(t *testing.T) *appconfig.Config
		want   string
	}{
		{"no overrides", func(*testing.T) *appconfig.Config { return &appconfig.Config{} }, ""},
		{"valid group and window", func(t *testing.T) *appconfig.Config {
			return &appconfig.Config{
				WindowOverrides: []appconfig.WindowOverride{windowOverride(t, "metrics[0].overrides[0]", appconfig.ExporterXIDErrorsCountField)},
				WatchGroups:     []appconfig.WatchGroup{watchGroup("temps", "DCGM_FI_DEV_GPU_TEMP")},
			}
		}, ""},
		{"a group may select the field the XID counter reads", func(*testing.T) *appconfig.Config {
			return &appconfig.Config{WatchGroups: []appconfig.WatchGroup{watchGroup("xid", "DCGM_FI_DEV_XID_ERROR")}}
		}, ""},
		{"window on a normal field", func(t *testing.T) *appconfig.Config {
			return &appconfig.Config{
				WindowOverrides: []appconfig.WindowOverride{windowOverride(t, "metrics[0].overrides[0]", "DCGM_FI_DEV_GPU_TEMP")},
			}
		}, "which is not a window counter"},
		{"window on a normal field alias", func(t *testing.T) *appconfig.Config {
			return &appconfig.Config{
				WindowOverrides: []appconfig.WindowOverride{windowOverride(t, "metrics[0].overrides[0]", "DCGM_FI_DEV_GPU_TEMP_CELSIUS")},
			}
		}, "selects DCGM_FI_DEV_GPU_TEMP, which is not a window counter"},
		{"group that matches nothing", func(*testing.T) *appconfig.Config {
			return &appconfig.Config{WatchGroups: []appconfig.WatchGroup{watchGroup("none", "DCGM_FI_DEV_NOT_THERE")}}
		}, "matched no configured fields"},
		{"groups that overlap", func(*testing.T) *appconfig.Config {
			return &appconfig.Config{WatchGroups: []appconfig.WatchGroup{
				watchGroup("a", "DCGM_FI_DEV_GPU_TEMP"), watchGroup("b", "/^DCGM_FI_DEV_.*TEMP$/"),
			}}
		}, "matches multiple watch groups"},
		{"group with a bad pattern", func(*testing.T) *appconfig.Config {
			return &appconfig.Config{WatchGroups: []appconfig.WatchGroup{watchGroup("bad", "/(/")}}
		}, "invalid field pattern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckOverrides(tc.config(t), overrideTestSet())
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestCheckOverridesReportsTheWindowProblemFirst(t *testing.T) {
	config := &appconfig.Config{
		WindowOverrides: []appconfig.WindowOverride{windowOverride(t, "metrics[0].overrides[0]", "DCGM_FI_DEV_SM_CLOCK")},
		WatchGroups:     []appconfig.WatchGroup{watchGroup("none", "DCGM_FI_DEV_NOT_THERE")},
	}

	err := CheckOverrides(config, overrideTestSet())
	require.Error(t, err)
	var window *WindowOverrideError
	assert.ErrorAs(t, err, &window)
	assert.Equal(t, window.Err.Error(), err.Error(), "the window problem keeps its own text")
	assert.Equal(t, window.Err, errors.Unwrap(err))
	assert.Contains(t, err.Error(), "metrics[0].overrides[0].match[0]")
}

func TestCheckOverridesReportsAGroupProblemAsIs(t *testing.T) {
	config := &appconfig.Config{WatchGroups: []appconfig.WatchGroup{
		{Name: "none", Path: "metrics[0].overrides[1]", Interval: 1000, Fields: []string{"DCGM_FI_DEV_NOT_THERE"}, ExtendedMatch: true},
	}}

	err := CheckOverrides(config, overrideTestSet())
	var window *WindowOverrideError
	var unmatched *UnmatchedWatchGroupError
	assert.False(t, errors.As(err, &window))
	require.ErrorAs(t, err, &unmatched)
	assert.Equal(t, "metrics[0].overrides[1]", unmatched.Group.Path)
}

func TestCheckWindowOverridesLeavesGroupsAlone(t *testing.T) {
	config := &appconfig.Config{WatchGroups: []appconfig.WatchGroup{watchGroup("none", "DCGM_FI_DEV_NOT_THERE")}}
	assert.NoError(t, checkWindowOverrides(config, overrideTestSet()))
}

// inlineSource is a metric source with two DCGM fields written in the config.
func inlineSource() appconfig.MetricSource {
	return appconfig.MetricSource{Kind: appconfig.MetricSourceInline, Fields: []appconfig.MetricField{
		{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "gauge", Help: "Temperature."},
		{Name: "DCGM_FI_DEV_SM_CLOCK", PrometheusType: "gauge", Help: "Clock."},
	}}
}

func TestCheckOverridesInLocalSource(t *testing.T) {
	file := filepath.Join(t.TempDir(), "metrics.csv")
	require.NoError(t, os.WriteFile(file, []byte("DCGM_FI_DEV_GPU_TEMP, gauge, Temperature.\n"), 0o600))
	unmatched := appconfig.WatchGroup{Name: "none", Path: "metrics[0].overrides[0]", Interval: 1000, Fields: []string{"DCGM_FI_DEV_NOT_THERE"}, ExtendedMatch: true}
	v1Unmatched := appconfig.WatchGroup{Name: "none", Interval: 1000, Fields: []string{"DCGM_FI_DEV_NOT_THERE"}, ExtendedMatch: true}

	for _, tc := range []struct {
		name    string
		source  appconfig.MetricSource
		groups  []appconfig.WatchGroup
		wantErr string
	}{
		{"override matching nothing in an inline source", inlineSource(), []appconfig.WatchGroup{unmatched}, "matched no configured fields"},
		{"override matching nothing in a file", appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: file}, []appconfig.WatchGroup{unmatched}, "matched no configured fields"},
		{"version 1 group is left to the registry", inlineSource(), []appconfig.WatchGroup{v1Unmatched}, ""},
		{"unreadable file is left to the later load", appconfig.MetricSource{Kind: appconfig.MetricSourceFile, File: file + ".missing"}, []appconfig.WatchGroup{unmatched}, ""},
		{"ConfigMap is not read", appconfig.MetricSource{Kind: appconfig.MetricSourceConfigMap}, []appconfig.WatchGroup{unmatched}, ""},
		{"invalid definitions are left to the later load", appconfig.MetricSource{Kind: appconfig.MetricSourceInline, Fields: []appconfig.MetricField{{Name: "X", PrometheusType: "bad"}}}, []appconfig.WatchGroup{unmatched}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &appconfig.Config{MetricSource: tc.source, WatchGroups: tc.groups}
			err := CheckOverridesInLocalSource(config, ReadLocalCounterSet(tc.source))
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestRejectNonWindowFields checks allowed counters and errors that identify a disallowed field.
func TestRejectNonWindowFields(t *testing.T) {
	names := []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_POWER_USAGE", "DCGM_EXP_XID_ERRORS_COUNT"}
	tests := []struct {
		name    string
		match   []string
		wantErr string
	}{
		{name: "counter by name", match: []string{"DCGM_EXP_XID_ERRORS_COUNT"}},
		{name: "counter by wildcard", match: []string{"DCGM_EXP_*_COUNT"}},
		{name: "counter by regex", match: []string{"/DCGM_EXP_XID_.*/"}},
		{name: "name that is not in the source", match: []string{"DCGM_EXP_CLOCK_EVENTS_COUNT"}},
		{name: "wildcard that matches nothing", match: []string{"NOTHING_*"}},
		{name: "regex that matches nothing", match: []string{"/NOTHING_.*/"}},
		{
			name:    "normal field by name",
			match:   []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"},
			wantErr: "metrics[0].overrides[2].match[0]: selects DCGM_FI_DEV_GPU_TEMP_CELSIUS, which is not a window counter",
		},
		{
			name:    "normal field by wildcard",
			match:   []string{"DCGM_EXP_XID_ERRORS_COUNT", "DCGM_FI_DEV_POWER_*"},
			wantErr: "metrics[0].overrides[2].match[1]: selects DCGM_FI_DEV_POWER_USAGE, which is not a window counter",
		},
		{
			name:    "normal field by regex",
			match:   []string{"/DCGM_FI_DEV_GPU_.*/"},
			wantErr: "metrics[0].overrides[2].match[0]: selects DCGM_FI_DEV_GPU_TEMP_CELSIUS, which is not a window counter",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := appconfig.ParseFieldMatcher(tt.match, true)
			require.NoError(t, err)
			err = rejectNonWindowFields([]appconfig.WindowOverride{{Path: "metrics[0].overrides[2]", Match: matcher}}, names)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
