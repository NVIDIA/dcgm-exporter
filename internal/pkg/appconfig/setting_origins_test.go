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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV2SettingOrigins(t *testing.T) {
	y, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    every: 10s
    overrides:
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 1m
      - name: temperature
        match: [DCGM_FI_DEV_GPU_TEMP]
        every: 3s
        sources: {dcgm: {watch: {max_keep_samples: 4}}}
      - match: [DCGM_FI_DEV_BOARD_POWER_WATTS]
        every: 4s
`+v2Outputs)
	origins := y.SettingOrigins()
	require.Len(t, config.WatchGroups, 2)
	assert.Equal(t, map[string]string{
		"MetricSource":                        "metrics[0].file",
		"CollectInterval":                     "metrics[0].every",
		"Kubernetes":                          "metrics[0].enrichment",
		"ContainerLabels":                     "metrics[0].enrichment",
		"KubernetesEnableDRA":                 "metrics[0].enrichment",
		"ContainerRuntimeSocket":              "metrics[0].enrichment",
		"HPCJobMappingDir":                    "metrics[0].enrichment",
		"HPCJobs":                             "metrics[0].enrichment",
		"XIDCountWindowSize":                  "metrics[0].overrides[0].window",
		"WatchGroups":                         "metrics[0].overrides",
		"WatchGroups[0].Name":                 "metrics[0].overrides[1].name",
		"WatchGroups[0].Interval":             "metrics[0].overrides[1].every",
		"WatchGroups[0].Fields":               "metrics[0].overrides[1].match",
		"WatchGroups[0].Retention.MaxSamples": "metrics[0].overrides[1].sources.dcgm.watch.max_keep_samples",
		"WatchGroups[1].Interval":             "metrics[0].overrides[2].every",
		"WatchGroups[1].Fields":               "metrics[0].overrides[2].match",
	}, origins)
	assert.Equal(t, "metrics[0].overrides[2]", config.WatchGroups[1].Name)
}

// Every override is either a watch group or a counter window, and each watch
// group gets its own origins in the same order the groups were built.
func TestV2MixedOverridesOrigins(t *testing.T) {
	y, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:
      - name: first
        match: [DCGM_FI_DEV_GPU_TEMP]
        every: 1s
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 1m
      - name: second
        match: [DCGM_FI_DEV_BOARD_POWER_WATTS]
        every: 2s
      - match: [DCGM_EXP_CLOCK_EVENTS_COUNT]
        window: 2m
      - name: third
        match: [DCGM_FI_DEV_SM_CLOCK_HERTZ]
        every: 3s
`+v2Outputs)
	origins := y.SettingOrigins()
	require.Len(t, config.WatchGroups, 3)
	for i, wantOverride := range []int{0, 2, 4} {
		field := fmt.Sprintf("WatchGroups[%d]", i)
		path := fmt.Sprintf("metrics[0].overrides[%d]", wantOverride)
		assert.Equal(t, path+".name", origins[field+".Name"], field)
		assert.Equal(t, path+".every", origins[field+".Interval"], field)
		assert.Equal(t, path+".match", origins[field+".Fields"], field)
	}
	assert.NotContains(t, origins, "WatchGroups[3].Interval")
	assert.Equal(t, "metrics[0].overrides[1].window", origins["XIDCountWindowSize"])
	assert.Equal(t, "metrics[0].overrides[3].window", origins["ClockEventsCountWindowSize"])
	assert.Equal(t, []string{"first", "second", "third"}, []string{config.WatchGroups[0].Name, config.WatchGroups[1].Name, config.WatchGroups[2].Name})
}

// TestLegacyWatchGroupOriginsFollowAssignment covers preservation and explicit or implicit clears.
func TestLegacyWatchGroupOriginsFollowAssignment(t *testing.T) {
	for _, tc := range []struct {
		name, collection, wantOrigin string
		wantName                     string
	}{
		{name: "absent", wantName: "previous"},
		{name: "null", collection: "collection: null\n", wantName: "previous"},
		{name: "empty", collection: "collection: {}\n", wantOrigin: "collection"},
		{name: "interval only", collection: "collection: {interval: 1s}\n", wantOrigin: "collection"},
		{name: "null groups", collection: "collection: {watchGroups: null}\n", wantOrigin: "collection"},
		{name: "empty groups", collection: "collection: {watchGroups: []}\n", wantOrigin: "collection"},
		{name: "populated", collection: "collection:\n  watchGroups:\n    - name: temperature\n      interval: 1s\n      fields: [DCGM_FI_DEV_GPU_TEMP]\n", wantOrigin: "collection.watchGroups", wantName: "temperature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y, err := ParseYAMLConfig([]byte("version: 1\n" + tc.collection))
			require.NoError(t, err)
			cfg := &Config{WatchRetention: DefaultWatchRetention(), WatchGroups: []WatchGroup{{Name: "previous"}}}
			require.NoError(t, y.ApplyTo(cfg))
			origins := y.SettingOrigins()
			if tc.wantOrigin == "" {
				assert.NotContains(t, origins, "WatchGroups")
			} else {
				assert.Equal(t, tc.wantOrigin, origins["WatchGroups"])
			}
			if tc.wantName == "" {
				assert.Empty(t, cfg.WatchGroups)
			} else {
				require.Len(t, cfg.WatchGroups, 1)
				assert.Equal(t, tc.wantName, cfg.WatchGroups[0].Name)
			}
			if tc.wantName == "temperature" {
				assert.Equal(t, "collection.watchGroups[0].name", origins["WatchGroups[0].Name"])
				assert.Equal(t, "collection.watchGroups[0].interval", origins["WatchGroups[0].Interval"])
				assert.Equal(t, "collection.watchGroups[0].fields", origins["WatchGroups[0].Fields"])
			} else {
				assert.NotContains(t, origins, "WatchGroups[0].Name")
			}
		})
	}
}
