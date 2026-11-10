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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The tests in this file carry over every version 1 case from the 4.8.4
// yaml_config_test.go. Inputs are unchanged. Expectations changed only where
// noted with "CHANGED".

// v1Base is the Config the 4.8.4 tests started from, without the fields that no
// longer exist on Config (CollectorsFile, ConfigMapData).
func v1Base() *Config {
	return &Config{
		CollectInterval: 30000,
		WatchRetention:  DefaultWatchRetention(),
		MetricSource:    MetricSource{Kind: MetricSourceFile, File: DefaultCollectorsFile},
	}
}

// TestV1ParseCases ports TestParseYAMLConfig and the retention parse tests.
// Success cases are checked through ApplyTo because the version 1 structs are no longer exported.
func TestV1ParseCases(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError string
		check     func(t *testing.T, c *Config)
	}{
		{
			name:  "file source with collection interval",
			input: "version: 1\nmetrics:\n  file: /etc/dcgm-exporter/default-counters.csv\ncollection:\n  interval: 45s\n",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, MetricSource{Kind: MetricSourceFile, File: "/etc/dcgm-exporter/default-counters.csv"}, c.MetricSource)
				assert.Equal(t, 45000, c.CollectInterval)
			},
		},
		{
			name:  "exporter metrics without custom metric source",
			input: "version: 1\nmetrics:\n  enableExporterMetrics: true\n",
			check: func(t *testing.T, c *Config) {
				assert.True(t, c.EnableExporterMetrics)
				assert.Equal(t, v1Base().MetricSource, c.MetricSource)
			},
		},
		{
			name:  "inline fields",
			input: "version: 1\nmetrics:\n  fields:\n    - name: DCGM_FI_DEV_GPU_TEMP\n      prometheusType: gauge\n      help: GPU temperature.\n",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, MetricSource{Kind: MetricSourceInline, Fields: []MetricField{
					{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "gauge", Help: "GPU temperature."},
				}}, c.MetricSource)
			},
		},
		{
			// CHANGED: the message names the YAML path and line instead of the Go type.
			name:      "configmap source is not a YAML metric source",
			input:     "version: 1\nmetrics:\n  configMap:\n    namespace: default\n    name: exporter-metrics-config-map\n    key: metrics\n",
			wantError: "metrics.configMap: unknown key at line 3",
		},
		{
			// CHANGED: same path form.
			name:      "unknown field fails",
			input:     "version: 1\nunknown: true\n",
			wantError: "unknown: unknown key at line 2",
		},
		{
			// CHANGED: a missing version is reported as required, and the message lists both versions.
			name:      "version is required",
			input:     "metrics:\n  file: /tmp/counters.csv\n",
			wantError: "version: required; must be 1 or 2",
		},
		{
			name:      "ambiguous metric source fails",
			input:     "version: 1\nmetrics:\n  file: /tmp/counters.csv\n  fields:\n    - name: DCGM_FI_DEV_GPU_TEMP\n      prometheusType: gauge\n      help: GPU temperature.\n",
			wantError: "exactly one",
		},
		{
			name:      "duplicate inline metric name fails",
			input:     "version: 1\nmetrics:\n  fields:\n    - {name: DCGM_FI_DEV_GPU_TEMP, prometheusType: gauge, help: GPU temperature.}\n    - {name: DCGM_FI_DEV_GPU_TEMP, prometheusType: counter, help: Duplicate.}\n",
			wantError: "duplicates metrics.fields",
		},
		{
			name:      "duplicate trimmed inline metric name fails",
			input:     "version: 1\nmetrics:\n  fields:\n    - {name: DCGM_FI_DEV_GPU_TEMP, prometheusType: gauge, help: GPU temperature.}\n    - {name: '  DCGM_FI_DEV_GPU_TEMP  ', prometheusType: counter, help: Duplicate.}\n",
			wantError: "duplicates metrics.fields",
		},
		{
			name:      "duplicate metric source key fails",
			input:     "version: 1\nmetrics:\n  file: /tmp/counters.csv\n  file: /tmp/other-counters.csv\n",
			wantError: "duplicate YAML key",
		},
		{
			name:      "non scalar mapping key fails",
			input:     "version: 1\n? [metrics]\n: value\n",
			wantError: "YAML mapping keys must be scalar",
		},
		{
			// CHANGED: the error no longer carries Go's "missing unit in duration" text.
			name:      "invalid duration unit fails",
			input:     "version: 1\ncollection:\n  interval: 30000\n",
			wantError: "invalid duration",
		},
		{
			name:      "sub-millisecond duration fails",
			input:     "version: 1\ncollection:\n  interval: 500us\n",
			wantError: "whole milliseconds",
		},
		{
			name:  "watch groups",
			input: "version: 1\ncollection:\n  watchGroups:\n    - name: slow\n      interval: 10m\n      fields:\n        - DCGM_FI_DEV_NVLINK_PPCNT_*\n",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, []WatchGroup{{Name: "slow", Interval: 600000, Fields: []string{"DCGM_FI_DEV_NVLINK_PPCNT_*"}}}, c.WatchGroups)
			},
		},
		{
			name:      "watch group missing fields fails",
			input:     "version: 1\ncollection:\n  watchGroups:\n    - name: slow\n      interval: 10m\n",
			wantError: "fields must not be empty",
		},
		{
			name:      "watch group duplicate name fails",
			input:     "version: 1\ncollection:\n  watchGroups:\n    - {name: slow, interval: 10m, fields: [DCGM_FI_DEV_NVLINK_PPCNT_*]}\n    - {name: slow, interval: 20m, fields: [DCGM_FI_DEV_ECC_*]}\n",
			wantError: "duplicated",
		},
		{
			name:      "watch group duplicate name with a bad interval reports the interval",
			input:     "version: 1\ncollection:\n  watchGroups:\n    - {name: slow, interval: 10m, fields: [DCGM_FI_DEV_NVLINK_PPCNT_*]}\n    - {name: slow, interval: bogus, fields: [DCGM_FI_DEV_ECC_*]}\n",
			wantError: "collection.watchGroups[1].interval",
		},
		{
			name:      "malformed retention age",
			input:     "version: 1\ncollection:\n  retention:\n    maxAge: invalid\n",
			wantError: "collection.retention.maxAge",
		},
		{
			name:      "negative retention age",
			input:     "version: 1\ncollection:\n  retention:\n    maxAge: -1s\n",
			wantError: "maxAge must not be negative",
		},
		{
			name:      "negative retention samples",
			input:     "version: 1\ncollection:\n  retention:\n    maxSamples: -1\n",
			wantError: "maxSamples must not be negative",
		},
		{
			name:      "retention samples exceed DCGM range",
			input:     "version: 1\ncollection:\n  retention:\n    maxSamples: 2147483648\n",
			wantError: "must not exceed 2147483647",
		},
		{
			name:      "group bounds both disabled",
			input:     "version: 1\ncollection:\n  watchGroups:\n    - name: invalid\n      interval: 1s\n      retention:\n        maxAge: 0s\n        maxSamples: 0\n      fields:\n        - DCGM_FI_DEV_GPU_TEMP\n",
			wantError: "collection.watchGroups[0].retention",
		},
		{
			// CHANGED: path form instead of "field samples not found".
			name:      "unknown retention property",
			input:     "version: 1\ncollection:\n  retention:\n    samples: 2\n",
			wantError: "collection.retention.samples: unknown key at line 4",
		},
		{
			// TestParseYAMLConfigAllowsGlobalBoundsToBeCompletedByLaterOverrides:
			// root bounds that are both zero parse, so a later flag or env value can complete them.
			name:  "global bounds both zero are allowed at parse time",
			input: "version: 1\ncollection:\n  retention:\n    maxAge: 0s\n    maxSamples: 0\n",
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, WatchRetention{MaxAge: 0, MaxSamples: 0}, c.WatchRetention)
				assert.Error(t, c.WatchRetention.Validate(), "final validation still rejects it")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseYAMLConfig([]byte(tt.input))
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Nil(t, parsed)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, parsed)
			assert.Equal(t, 1, parsed.Version)
			config := v1Base()
			require.NoError(t, parsed.ApplyTo(config))
			tt.check(t, config)
		})
	}
}

// TestV1MaxConcurrentScrapesRejectsNonPositive ports TestParseYAMLConfigRejectsInvalidMaxConcurrentScrapes.
func TestV1MaxConcurrentScrapesRejectsNonPositive(t *testing.T) {
	for _, value := range []int{0, -1} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			_, err := ParseYAMLConfig([]byte(fmt.Sprintf("version: 1\nserver:\n  maxConcurrentScrapes: %d\n", value)))
			require.Error(t, err)
			assert.ErrorContains(t, err, "server.maxConcurrentScrapes must be greater than 0")
		})
	}
}

// TestV1ApplyToCases ports TestYAMLConfigApplyTo, ...MaxConcurrentScrapes,
// ...EnableExporterMetrics, ...WatchGroups and ...WatchRetention.
func TestV1ApplyToCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		base  *Config
		check func(t *testing.T, c *Config)
	}{
		{
			name:  "inline fields with interval",
			input: "version: 1\nmetrics:\n  fields:\n    - name: DCGM_FI_DEV_GPU_TEMP\n      prometheusType: gauge\n      help: GPU temperature.\ncollection:\n  interval: 10s\n",
			base:  v1Base(),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, 10000, c.CollectInterval)
				assert.Equal(t, MetricSourceInline, c.MetricSource.Kind)
				require.Len(t, c.MetricSource.Fields, 1)
				assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", c.MetricSource.Fields[0].Name)
				_, watched := c.MetricFileWatcherPath()
				assert.False(t, watched)
			},
		},
		{
			name:  "max concurrent scrapes",
			input: "version: 1\nserver:\n  maxConcurrentScrapes: 8\n",
			base:  &Config{MaxConcurrentScrapes: DefaultMaxConcurrentScrapes},
			check: func(t *testing.T, c *Config) { assert.Equal(t, 8, c.MaxConcurrentScrapes) },
		},
		{
			name:  "exporter metrics keep the metric file",
			input: "version: 1\nmetrics:\n  enableExporterMetrics: true\n",
			base:  v1Base(),
			check: func(t *testing.T, c *Config) {
				assert.True(t, c.EnableExporterMetrics)
				assert.Equal(t, MetricSourceFile, c.MetricSource.Kind)
				assert.Equal(t, DefaultCollectorsFile, c.MetricSource.File)
			},
		},
		{
			name:  "explicit false turns exporter metrics off",
			input: "version: 1\nmetrics:\n  enableExporterMetrics: false\n",
			base:  &Config{EnableExporterMetrics: true},
			check: func(t *testing.T, c *Config) { assert.False(t, c.EnableExporterMetrics) },
		},
		{
			name:  "watch groups",
			input: "version: 1\ncollection:\n  interval: 5s\n  watchGroups:\n    - name: slow-nvlink\n      interval: 10m\n      fields:\n        - DCGM_FI_DEV_NVLINK_PPCNT_*\n",
			base:  &Config{CollectInterval: 30000},
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, 5000, c.CollectInterval)
				require.Len(t, c.WatchGroups, 1)
				assert.Equal(t, WatchGroup{Name: "slow-nvlink", Interval: 600000, Fields: []string{"DCGM_FI_DEV_NVLINK_PPCNT_*"}}, c.WatchGroups[0])
			},
		},
		{
			name: "watch retention keeps omission and zero",
			input: `version: 1
collection:
  retention:
    maxAge: 2m
    maxSamples: 5
  watchGroups:
    - name: latest-values
      interval: 1s
      retention:
        maxAge: 0s
        maxSamples: 2
      fields:
        - DCGM_FI_DEV_GPU_TEMP
    - name: inherited
      interval: 30s
      fields:
        - DCGM_FI_DEV_POWER_USAGE
`,
			base: v1Base(),
			check: func(t *testing.T, c *Config) {
				assert.Equal(t, WatchRetention{MaxAge: 2 * time.Minute, MaxSamples: 5}, c.WatchRetention)
				require.Len(t, c.WatchGroups, 2)
				assert.Equal(t, WatchRetention{MaxAge: 0, MaxSamples: 2}, c.WatchGroups[0].Retention.Resolve(c.WatchRetention))
				assert.Equal(t, c.WatchRetention, c.WatchGroups[1].Retention.Resolve(c.WatchRetention))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := ParseYAMLConfig([]byte(tt.input))
			require.NoError(t, err)
			require.NoError(t, parsed.ApplyTo(tt.base))
			tt.check(t, tt.base)
		})
	}
}

// The 4.8.3 shape: an interval and a watch group.
func TestV1WatchGroupShape(t *testing.T) {
	y, config := parseAndApply(t, `
version: 1
collection:
  interval: 45s
  watchGroups:
    - name: fast
      interval: 1s
      fields: ["DCGM_FI_DEV_BOARD_POWER_*"]
`)
	require.Len(t, y.Warnings(), 1)
	assert.Equal(t, LegacyConfigVersionWarning, y.Warnings()[0].Message)
	assert.Equal(t, 45000, config.CollectInterval)
	require.Len(t, config.WatchGroups, 1)
	assert.Equal(t, WatchGroup{Name: "fast", Interval: 1000, Fields: []string{"DCGM_FI_DEV_BOARD_POWER_*"}}, config.WatchGroups[0])
}

// Inline rows in a version 1 file.
func TestV1InlineRows(t *testing.T) {
	y, config := parseAndApply(t, `
version: 1
metrics:
  fields:
    - {name: DCGM_FI_DEV_GPU_TEMP, prometheusType: gauge, help: GPU temperature.}
`)
	assert.Equal(t, MetricSource{Kind: MetricSourceInline, Fields: []MetricField{
		{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "gauge", Help: "GPU temperature."},
	}}, config.MetricSource)
	assert.Equal(t, "metrics.fields", y.SettingOrigins()["MetricSource"])
}

// The 4.8.4 Helm example shape, with non-default values so that ignoring any key fails the test.
func TestV1HelmExampleShape(t *testing.T) {
	y, config := parseAndApply(t, `
version: 1
metrics:
  file: /tmp/custom.csv
  enableExporterMetrics: true
collection:
  interval: 45s
  retention: {maxAge: 2m, maxSamples: 5}
server:
  maxConcurrentScrapes: 8
`)
	assert.Equal(t, MetricSource{Kind: MetricSourceFile, File: "/tmp/custom.csv"}, config.MetricSource)
	assert.True(t, config.EnableExporterMetrics)
	assert.Equal(t, 45000, config.CollectInterval)
	assert.Equal(t, WatchRetention{MaxAge: 2 * time.Minute, MaxSamples: 5}, config.WatchRetention)
	assert.Equal(t, 8, config.MaxConcurrentScrapes)
	assert.Equal(t, map[string]string{
		"EnableExporterMetrics":     "metrics.enableExporterMetrics",
		"MetricSource":              "metrics.file",
		"MaxConcurrentScrapes":      "server.maxConcurrentScrapes",
		"CollectInterval":           "collection.interval",
		"WatchRetention.MaxAge":     "collection.retention.maxAge",
		"WatchRetention.MaxSamples": "collection.retention.maxSamples",
		"WatchGroups":               "collection",
	}, y.SettingOrigins())
}

// Version 1 validation messages keep their released wording.
func TestV1RejectionsKeepTheirMessages(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "empty metrics block",
			yaml: `
version: 1
metrics: {}
`,
			wantErr: "metrics must specify file, fields, or enableExporterMetrics",
		},
		{
			name: "file and fields",
			yaml: `
version: 1
metrics:
  file: a
  fields: [{name: A, prometheusType: gauge, help: h}]
`,
			wantErr: "metrics must specify exactly one of file or fields",
		},
		{
			name: "missing type",
			yaml: `
version: 1
metrics:
  fields: [{name: A, help: h}]
`,
			wantErr: "metrics.fields[0].prometheusType is required",
		},
		{
			name: "duplicate field",
			yaml: `
version: 1
metrics:
  fields: [{name: A, prometheusType: gauge, help: h}, {name: A, prometheusType: gauge, help: h}]
`,
			wantErr: "metrics.fields[1].name duplicates metrics.fields[0].name",
		},
		{
			name: "zero scrape limit",
			yaml: `
version: 1
server:
  maxConcurrentScrapes: 0
`,
			wantErr: "server.maxConcurrentScrapes must be greater than 0",
		},
		{
			name: "sub-millisecond interval",
			yaml: `
version: 1
collection:
  interval: 1500us
`,
			wantErr: "collection.interval: must be expressed in whole milliseconds",
		},
		{
			name: "group with both bounds zero",
			yaml: `
version: 1
collection:
  watchGroups:
    - {name: a, interval: 1s, fields: [X], retention: {maxAge: 0s, maxSamples: 0}}
`,
			wantErr: "collection.watchGroups[0].retention: maxAge and maxSamples cannot both be zero",
		},
		{
			name: "duplicate group name",
			yaml: `
version: 1
collection:
  watchGroups:
    - {name: a, interval: 1s, fields: [X]}
    - {name: a, interval: 1s, fields: [Y]}
`,
			wantErr: "collection.watchGroups[1].name",
		},
		{
			name: "negative retention age",
			yaml: `
version: 1
collection:
  retention: {maxAge: -1s}
`,
			wantErr: "collection.retention.maxAge must not be negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, strings.TrimPrefix(tt.yaml, "\n"), tt.wantErr)
		})
	}
}

func TestV1KeepsItsShapeWhenMarshalled(t *testing.T) {
	y, err := ParseYAMLConfig([]byte(powerGroupV1))
	require.NoError(t, err)
	out, err := yaml.Marshal(y)
	require.NoError(t, err)
	assert.Contains(t, string(out), "version: 1")
	assert.Contains(t, string(out), "watchGroups:")
	assert.NotContains(t, string(out), "overrides:")
}

// The deprecation notice appears for every version 1 file and never for version 2.
func TestWarningsOnlyForVersion1(t *testing.T) {
	v1, err := ParseYAMLConfig([]byte("version: 1\n"))
	require.NoError(t, err)
	assert.Equal(t, []ResolutionWarning{{
		Message: "config version 1 is deprecated and will be removed in 4.10; see the version 2 migration table in the 4.9.0 documentation",
		Origin:  SettingOrigin{Kind: OriginFile, Key: "version"},
	}}, v1.Warnings())

	v2, err := ParseYAMLConfig([]byte("version: 2\n"))
	require.NoError(t, err)
	assert.Empty(t, v2.Warnings())
}

func TestV1AppliesNothingWhenOmitted(t *testing.T) {
	y, config := parseAndApply(t, "version: 1\n")
	assert.Equal(t, baseConfig(), config)
	assert.Empty(t, y.SettingOrigins())
}
