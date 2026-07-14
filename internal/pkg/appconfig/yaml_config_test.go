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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseYAMLConfig(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      *YAMLConfig
		wantError string
	}{
		{
			name: "file source with collection interval",
			input: `
version: 1
metrics:
  file: /etc/dcgm-exporter/default-counters.csv
collection:
  interval: 45s
`,
			want: &YAMLConfig{
				Version: 1,
				Metrics: &YAMLMetrics{
					File: "/etc/dcgm-exporter/default-counters.csv",
				},
				Collection: &YAMLCollection{
					Interval: "45s",
				},
			},
		},
		{
			name: "inline fields",
			input: `
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
`,
			want: &YAMLConfig{
				Version: 1,
				Metrics: &YAMLMetrics{
					Fields: []YAMLMetricField{
						{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "gauge", Help: "GPU temperature."},
					},
				},
			},
		},
		{
			name: "configmap source is not a YAML metric source",
			input: `
version: 1
metrics:
  configMap:
    namespace: default
    name: exporter-metrics-config-map
    key: metrics
`,
			wantError: "field configMap not found",
		},
		{
			name: "unknown field fails",
			input: `
version: 1
unknown: true
`,
			wantError: "field unknown not found",
		},
		{
			name: "version is required",
			input: `
metrics:
  file: /tmp/counters.csv
`,
			wantError: "version must be 1",
		},
		{
			name: "ambiguous metric source fails",
			input: `
version: 1
metrics:
  file: /tmp/counters.csv
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
`,
			wantError: "exactly one",
		},
		{
			name: "duplicate inline metric name fails",
			input: `
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: counter
      help: Duplicate GPU temperature.
`,
			wantError: "duplicates metrics.fields",
		},
		{
			name: "duplicate trimmed inline metric name fails",
			input: `
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
    - name: "  DCGM_FI_DEV_GPU_TEMP  "
      prometheusType: counter
      help: Duplicate GPU temperature with surrounding whitespace.
`,
			wantError: "duplicates metrics.fields",
		},
		{
			name: "duplicate metric source key fails",
			input: `
version: 1
metrics:
  file: /tmp/counters.csv
  file: /tmp/other-counters.csv
`,
			wantError: "duplicate YAML key",
		},
		{
			name: "non scalar mapping key fails",
			input: `
version: 1
? [metrics]
: value
`,
			wantError: "YAML mapping keys must be scalar",
		},
		{
			name: "invalid duration unit fails",
			input: `
version: 1
collection:
  interval: 30000
`,
			wantError: "missing unit",
		},
		{
			name: "sub-millisecond duration fails",
			input: `
version: 1
collection:
  interval: 500us
`,
			wantError: "whole milliseconds",
		},
		{
			name: "watch groups",
			input: `
version: 1
collection:
  watchGroups:
    - name: slow
      interval: 10m
      fields:
        - DCGM_FI_DEV_NVLINK_PPCNT_*
`,
			want: &YAMLConfig{
				Version: 1,
				Collection: &YAMLCollection{
					WatchGroups: []YAMLWatchGroup{
						{
							Name:     "slow",
							Interval: "10m",
							Fields:   []string{"DCGM_FI_DEV_NVLINK_PPCNT_*"},
						},
					},
					parsedWatchGroups: []WatchGroup{
						{
							Name:     "slow",
							Interval: 600000,
							Fields:   []string{"DCGM_FI_DEV_NVLINK_PPCNT_*"},
						},
					},
				},
			},
		},
		{
			name: "watch group missing fields fails",
			input: `
version: 1
collection:
  watchGroups:
    - name: slow
      interval: 10m
`,
			wantError: "fields must not be empty",
		},
		{
			name: "watch group duplicate name fails",
			input: `
version: 1
collection:
  watchGroups:
    - name: slow
      interval: 10m
      fields:
        - DCGM_FI_DEV_NVLINK_PPCNT_*
    - name: slow
      interval: 20m
      fields:
        - DCGM_FI_DEV_ECC_*
`,
			wantError: "duplicated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseYAMLConfig([]byte(tt.input))
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func FuzzParseYAMLConfig(f *testing.F) {
	seeds := []string{
		"version: 1\nmetrics:\n  file: /etc/dcgm-exporter/default-counters.csv\ncollection:\n  interval: 45s\n",
		"version: 1\nmetrics:\n  fields:\n    - name: DCGM_FI_DEV_GPU_TEMP\n      prometheusType: gauge\n      help: GPU temperature.\n",
		"version: 1\ncollection:\n  watchGroups:\n    - name: slow\n      interval: 10m\n      fields: [DCGM_FI_DEV_NVLINK_PPCNT_*]\n",
		"version: 1\nmetrics:\n  file: /tmp/first.csv\n  file: /tmp/second.csv\n",
		"version: 1\n---\nversion: 1\n",
		"version: 1\ncollection:\n  watchGroups:\n    -",
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseYAMLConfig(data)
		if err != nil {
			return
		}

		base := Config{
			CollectorsFile:  DefaultCollectorsFile,
			ConfigMapData:   UndefinedConfigMapData,
			CollectInterval: 30000,
			MetricSource: MetricSource{
				Kind: MetricSourceFile,
				File: DefaultCollectorsFile,
			},
		}
		config := base
		if err := parsed.ApplyTo(&config); err != nil {
			t.Fatalf("parsed configuration could not be applied: %v", err)
		}

		normalized, err := yaml.Marshal(parsed)
		if err != nil {
			t.Fatalf("marshal parsed configuration: %v", err)
		}
		reparsed, err := ParseYAMLConfig(normalized)
		if err != nil {
			t.Fatalf("parse normalized configuration: %v\n%s", err, normalized)
		}
		reparsedConfig := base
		if err := reparsed.ApplyTo(&reparsedConfig); err != nil {
			t.Fatalf("normalized configuration could not be applied: %v", err)
		}
		if !reflect.DeepEqual(config, reparsedConfig) {
			t.Fatalf("configuration behavior changed after normalization:\nfirst:  %#v\nsecond: %#v", config, reparsedConfig)
		}
	})
}

func TestYAMLConfigApplyTo(t *testing.T) {
	config := &Config{
		CollectorsFile:  DefaultCollectorsFile,
		ConfigMapData:   UndefinedConfigMapData,
		CollectInterval: 30000,
		MetricSource: MetricSource{
			Kind: MetricSourceFile,
			File: DefaultCollectorsFile,
		},
	}
	yamlConfig, err := ParseYAMLConfig([]byte(`
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collection:
  interval: 10s
`))
	require.NoError(t, err)

	require.NoError(t, yamlConfig.ApplyTo(config))

	assert.Equal(t, 10000, config.CollectInterval)
	assert.Equal(t, UndefinedConfigMapData, config.ConfigMapData)
	assert.Equal(t, MetricSourceInline, config.MetricSource.Kind)
	require.Len(t, config.MetricSource.Fields, 1)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP", config.MetricSource.Fields[0].Name)
	assert.False(t, mustMetricFileWatcherPath(config))
}

func TestYAMLConfigApplyToWatchGroups(t *testing.T) {
	config := &Config{
		CollectInterval: 30000,
	}
	yamlConfig, err := ParseYAMLConfig([]byte(`
version: 1
collection:
  interval: 5s
  watchGroups:
    - name: slow-nvlink
      interval: 10m
      fields:
        - DCGM_FI_DEV_NVLINK_PPCNT_*
`))
	require.NoError(t, err)

	require.NoError(t, yamlConfig.ApplyTo(config))

	assert.Equal(t, 5000, config.CollectInterval)
	require.Len(t, config.WatchGroups, 1)
	assert.Equal(t, WatchGroup{
		Name:     "slow-nvlink",
		Interval: 600000,
		Fields:   []string{"DCGM_FI_DEV_NVLINK_PPCNT_*"},
	}, config.WatchGroups[0])
}

func TestMetricFileWatcherPath(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		wantPath string
		wantOK   bool
	}{
		{
			name: "default file source is watched",
			config: &Config{
				CollectorsFile: DefaultCollectorsFile,
				ConfigMapData:  UndefinedConfigMapData,
			},
			wantPath: DefaultCollectorsFile,
			wantOK:   true,
		},
		{
			name: "inline source is not watched",
			config: &Config{
				MetricSource: MetricSource{Kind: MetricSourceInline},
			},
		},
		{
			name: "configmap source is not watched",
			config: &Config{
				MetricSource: MetricSource{Kind: MetricSourceConfigMap},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotOK := tt.config.MetricFileWatcherPath()
			assert.Equal(t, tt.wantPath, gotPath)
			assert.Equal(t, tt.wantOK, gotOK)
		})
	}
}

func mustMetricFileWatcherPath(config *Config) bool {
	_, ok := config.MetricFileWatcherPath()
	return ok
}
