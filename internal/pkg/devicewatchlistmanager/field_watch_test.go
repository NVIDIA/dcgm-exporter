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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestWatchOfField checks that the first matching group takes a field and that
// any other field gets the collection interval and the global retention.
func TestWatchOfField(t *testing.T) {
	age := time.Minute
	config := appconfig.DefaultConfig()
	config.WatchGroups = []appconfig.WatchGroup{
		{
			Name: "first", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_*"}, ExtendedMatch: true,
			Retention: appconfig.WatchRetentionOverride{MaxAge: &age},
		},
		{Name: "second", Interval: 2000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}, ExtendedMatch: true},
	}

	tests := map[string]struct {
		field string
		want  FieldWatch
	}{
		"first matching group wins": {
			field: "DCGM_FI_DEV_GPU_TEMP",
			want: FieldWatch{Group: 0, Interval: time.Second, Retention: appconfig.WatchRetention{
				MaxAge: age, MaxSamples: appconfig.DefaultWatchMaxSamples,
			}},
		},
		"unmatched field gets the defaults": {
			field: "DCGM_FI_DEV_SM_CLOCK",
			want:  FieldWatch{Group: -1, Interval: 30 * time.Second, Retention: appconfig.DefaultWatchRetention()},
		},
		"unnamed field gets the defaults": {
			field: "",
			want:  FieldWatch{Group: -1, Interval: 30 * time.Second, Retention: appconfig.DefaultWatchRetention()},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := WatchOfField(config, tc.field)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestWatchOfFieldUsesTheDefaultRetentionForAZeroConfig checks that a config
// with no global retention gets the default retention, as the installed watch does.
func TestWatchOfFieldUsesTheDefaultRetentionForAZeroConfig(t *testing.T) {
	got, err := WatchOfField(&appconfig.Config{CollectInterval: 5000}, "DCGM_FI_DEV_GPU_TEMP")

	require.NoError(t, err)
	assert.Equal(t, FieldWatch{Group: -1, Interval: 5 * time.Second, Retention: appconfig.DefaultWatchRetention()}, got)
}

// TestWatchOfFieldReportsUnreadablePatterns checks that a bad pattern returns
// the default watch together with the error.
func TestWatchOfFieldReportsUnreadablePatterns(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.WatchGroups = []appconfig.WatchGroup{{Name: "bad", Interval: 1000, Fields: []string{"["}, ExtendedMatch: true}}

	got, err := WatchOfField(config, "DCGM_FI_DEV_GPU_TEMP")

	require.Error(t, err)
	assert.Equal(t, -1, got.Group)
	assert.Equal(t, 30*time.Second, got.Interval)
}

// TestWatchFieldAliases checks exact aliases and XID alias patterns in both config formats.
func TestWatchFieldAliases(t *testing.T) {
	for _, extended := range []bool{false, true} {
		for _, tt := range []struct {
			item string
			name string
			want bool
		}{
			{item: "DCGM_FI_DEV_XID_ERROR", name: "DCGM_FI_DEV_XID_ERRORS", want: true},
			{item: "DCGM_FI_DEV_XID_ERRORS", name: "DCGM_FI_DEV_XID_ERROR", want: true},
			{item: "DCGM_FI_DEV_XID_ERRORS*", name: "DCGM_FI_DEV_XID_ERROR", want: true},
			{item: "/DCGM_FI_DEV_XID_ERRORS/", name: "DCGM_FI_DEV_XID_ERROR", want: extended},
			{item: "/DCGM_FI_DEV_XID_ERROR/", name: "DCGM_FI_DEV_XID_ERRORS", want: extended},
			{item: "DCGM_FI_DEV_XID_ERRORS*", name: "DCGM_FI_DEV_GPU_TEMP"},
		} {
			config := appconfig.DefaultConfig()
			config.WatchGroups = []appconfig.WatchGroup{{Fields: []string{tt.item}, Interval: 1000, ExtendedMatch: extended}}
			watch, err := WatchOfField(config, tt.name)
			require.NoError(t, err)
			assert.Equal(t, tt.want, watch.Group == 0, "item %q, extended %t", tt.item, extended)
		}
	}
}
