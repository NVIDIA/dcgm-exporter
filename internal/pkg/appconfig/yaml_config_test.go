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
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseYAMLConfig(t *testing.T) {
	enableExporterMetricsDisabled := false
	detectBindUnbindEnabled := true
	tests := []struct {
		name      string
		input     string
		want      *YAMLConfig
		wantError string
	}{
		{
			name: "GPU bind/unbind detection",
			input: `
version: 2
sources:
  dcgm:
    detectBindUnbind:
      enabled: true
      pollInterval: 250ms
`,
			want: &YAMLConfig{
				Version: 2,
				Sources: &YAMLSources{
					DCGM: &YAMLDCGMSource{
						DetectBindUnbind: &YAMLDetectBindUnbind{
							Enabled:      &detectBindUnbindEnabled,
							PollInterval: "250ms",
						},
					},
				},
			},
		},
		{
			name: "file source with collections",
			input: `
version: 2
metrics:
  file: /etc/dcgm-exporter/default-counters.csv
collections:
  - name: scrape
    every: 45s
    metrics:
      include: ["*"]
`,
			want: &YAMLConfig{
				Version: 2,
				Metrics: &YAMLMetrics{
					File: "/etc/dcgm-exporter/default-counters.csv",
				},
				Collections: []CollectionConfig{
					{
						Name:    "scrape",
						Every:   "45s",
						Metrics: CollectionMetricsConfig{Include: []string{"*"}},
					},
				},
			},
		},
		{
			name: "exporter metrics without custom metric source",
			input: `
version: 2
metrics:
  enableExporterMetrics: false
`,
			want: &YAMLConfig{
				Version: 2,
				Metrics: &YAMLMetrics{
					EnableExporterMetrics: &enableExporterMetricsDisabled,
				},
			},
		},
		{
			name: "inline fields",
			input: `
version: 2
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
`,
			want: &YAMLConfig{
				Version: 2,
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
version: 2
metrics:
  configMap:
    namespace: default
    name: exporter-metrics-config-map
    key: metrics
`,
			wantError: "field configMap not found",
		},
		{
			name: "unknown DCGM source setting fails",
			input: `
version: 1
sources:
  dcgm:
    enableBindUnbindWatch: true
`,
			wantError: "field enableBindUnbindWatch not found",
		},
		{
			name: "server GPU key is not accepted",
			input: `
version: 1
server:
  gpu:
    detectBindUnbind: true
`,
			wantError: "field gpu not found",
		},
		{
			name: "non-positive bind unbind interval fails",
			input: `
version: 1
sources:
  dcgm:
    detectBindUnbind:
      pollInterval: 0s
`,
			wantError: "sources.dcgm.detectBindUnbind.pollInterval must be greater than 0",
		},
		{
			name: "unknown field fails",
			input: `
version: 2
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
			wantError: "version must be 1 or 2",
		},
		{
			name: "ambiguous metric source fails",
			input: `
version: 2
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
version: 2
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
version: 2
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
version: 2
metrics:
  file: /tmp/counters.csv
  file: /tmp/other-counters.csv
`,
			wantError: "duplicate YAML key",
		},
		{
			name: "non scalar mapping key fails",
			input: `
version: 2
? [metrics]
: value
`,
			wantError: "YAML mapping keys must be scalar",
		},
		{
			name: "invalid collection duration unit fails",
			input: `
version: 2
collections:
  - name: scrape
    every: 30000
    metrics:
      include: ["*"]
`,
			wantError: "missing unit",
		},
		{
			name: "sub-millisecond collection duration fails",
			input: `
version: 2
collections:
  - name: scrape
    every: 500us
    metrics:
      include: ["*"]
`,
			wantError: "whole milliseconds",
		},
		{
			name: "collection requires metric patterns",
			input: `
version: 2
collections:
  - name: scrape
    every: 30s
    metrics: {}
`,
			wantError: "collections[0].metrics.include must not be empty",
		},
		{
			name: "collection rejects empty metric patterns",
			input: `
version: 2
collections:
  - name: scrape
    every: 30s
    metrics:
      include: []
`,
			wantError: "collections[0].metrics.include must not be empty",
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

func TestParseYAMLConfigRejectsInvalidMaxConcurrentScrapes(t *testing.T) {
	tests := []struct {
		name  string
		value int
	}{
		{name: "zero", value: 0},
		{name: "negative", value: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseYAMLConfig([]byte(fmt.Sprintf(`
version: 2
server:
  maxConcurrentScrapes: %d
`, tt.value)))

			require.Error(t, err)
			assert.ErrorContains(t, err, "server.maxConcurrentScrapes must be greater than 0")
		})
	}
}

func TestParseYAMLConfigCollections(t *testing.T) {
	config, err := ParseYAMLConfig([]byte(`
version: 2
metrics:
  file: /etc/dcgm-exporter/default-counters.csv
collections:
  - name: scrape
    every: 45s
    metrics:
      include: ["*"]
  - name: fast-thermals
    every: 5s
    metrics:
      include:
        - DCGM_FI_DEV_GPU_TEMP
        - DCGM_FI_DEV_POWER_USAGE
`))

	require.NoError(t, err)
	assert.Equal(t, []CollectionConfig{
		{
			Name:    "scrape",
			Every:   "45s",
			Metrics: CollectionMetricsConfig{Include: []string{"*"}},
		},
		{
			Name:  "fast-thermals",
			Every: "5s",
			Metrics: CollectionMetricsConfig{Include: []string{
				"DCGM_FI_DEV_GPU_TEMP",
				"DCGM_FI_DEV_POWER_USAGE",
			}},
		},
	}, config.Collections)
}

func TestParseYAMLConfigSupportsV1Collection(t *testing.T) {
	config, err := ParseYAMLConfig([]byte(`
version: 1
collection:
  interval: 30s
  retention:
    maxAge: 10m
    maxSamples: 0
  watchGroups:
    - name: fast-thermals
      interval: 5s
      retention:
        maxAge: 0s
        maxSamples: 2
      fields:
        - DCGM_FI_DEV_GPU_TEMP
`))
	require.NoError(t, err)
	assert.Equal(t, 1, config.Version)

	roundTripped, err := yaml.Marshal(config)
	require.NoError(t, err)
	assert.Contains(t, string(roundTripped), "version: 1")
	assert.Contains(t, string(roundTripped), "collection:")
	assert.NotContains(t, string(roundTripped), "collections:")

	runtime := &Config{CollectInterval: 30000, WatchRetention: DefaultWatchRetention()}
	require.NoError(t, config.ApplyTo(runtime))
	assert.Equal(t, 30000, runtime.CollectInterval)
	assert.Equal(t, WatchRetention{MaxAge: 10 * time.Minute, MaxSamples: 0}, runtime.WatchRetention)
	require.Len(t, runtime.WatchGroups, 1)
	zero := time.Duration(0)
	maxSamples := int64(2)
	assert.Equal(t, WatchGroup{
		Name:     "fast-thermals",
		Interval: 5000,
		Fields:   []string{"DCGM_FI_DEV_GPU_TEMP"},
		Retention: WatchRetentionOverride{
			MaxAge:     &zero,
			MaxSamples: &maxSamples,
		},
	}, runtime.WatchGroups[0])
}

func TestParseYAMLConfigRejectsV1CollectionInV2(t *testing.T) {
	_, err := ParseYAMLConfig([]byte(`
version: 2
collection:
  interval: 30s
`))

	require.Error(t, err)
	assert.ErrorContains(t, err, "field collection not found")
}

func TestParseYAMLConfigRejectsV2CollectionsInV1(t *testing.T) {
	_, err := ParseYAMLConfig([]byte(`
version: 1
collections:
  - name: scrape
    every: 30s
    metrics:
      include: ["*"]
`))

	require.Error(t, err)
	assert.ErrorContains(t, err, "field collections not found")
}

func TestParseYAMLConfigRejectsV2WatchSettingsInV1(t *testing.T) {
	_, err := ParseYAMLConfig([]byte(`
version: 1
sources:
  dcgm:
    watch:
      maxKeepAge: 10m
`))

	require.Error(t, err)
	assert.ErrorContains(t, err, "field watch not found")
}

func FuzzParseYAMLConfig(f *testing.F) {
	seeds := []string{
		"version: 2\ncollections:\n  - name: scrape\n    every: 45s\n    metrics:\n      include: ['*']\n",
		"version: 2\nmetrics:\n  fields:\n    - name: DCGM_FI_DEV_GPU_TEMP\n      prometheusType: gauge\n      help: GPU temperature.\n",
		"version: 2\nmetrics:\n  file: /tmp/first.csv\n  file: /tmp/second.csv\n",
		"version: 2\n---\nversion: 2\n",
		"version: 1\ncollection:\n  interval: 30s\n",
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
version: 2
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature.
collections:
  - name: scrape
    every: 10s
    metrics:
      include: ["*"]
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

func TestYAMLConfigApplyToGPUBindUnbindDetection(t *testing.T) {
	for _, tt := range []struct {
		name        string
		initial     bool
		detectValue bool
	}{
		{name: "enables detection", initial: false, detectValue: true},
		{name: "disables detection", initial: true, detectValue: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := &Config{
				EnableGPUBindUnbindWatch:  tt.initial,
				GPUBindUnbindPollInterval: time.Second,
			}
			yamlConfig, err := ParseYAMLConfig([]byte(fmt.Sprintf(`
version: 2
sources:
  dcgm:
    detectBindUnbind:
      enabled: %t
      pollInterval: 250ms
`, tt.detectValue)))
			require.NoError(t, err)

			require.NoError(t, yamlConfig.ApplyTo(config))

			assert.Equal(t, tt.detectValue, config.EnableGPUBindUnbindWatch)
			assert.Equal(t, 250*time.Millisecond, config.GPUBindUnbindPollInterval)
		})
	}
}

func TestYAMLConfigApplyToGPUBindUnbindDetectionUsesDefaultsWhenOmitted(t *testing.T) {
	config := &Config{
		EnableGPUBindUnbindWatch:  false,
		GPUBindUnbindPollInterval: time.Second,
	}
	yamlConfig, err := ParseYAMLConfig([]byte(`
version: 1
sources:
  dcgm:
    detectBindUnbind: {}
`))
	require.NoError(t, err)

	require.NoError(t, yamlConfig.ApplyTo(config))

	assert.False(t, config.EnableGPUBindUnbindWatch)
	assert.Equal(t, time.Second, config.GPUBindUnbindPollInterval)
}

func TestYAMLConfigApplyToMaxConcurrentScrapes(t *testing.T) {
	config := &Config{MaxConcurrentScrapes: DefaultMaxConcurrentScrapes}
	yamlConfig, err := ParseYAMLConfig([]byte(`
version: 2
server:
  maxConcurrentScrapes: 8
`))
	require.NoError(t, err)

	require.NoError(t, yamlConfig.ApplyTo(config))

	assert.Equal(t, 8, config.MaxConcurrentScrapes)
}

// TestYAMLConfigApplyToEnableExporterMetrics verifies that YAML can opt into exporter metrics without replacing DCGM metrics.
func TestYAMLConfigApplyToEnableExporterMetrics(t *testing.T) {
	config := &Config{
		CollectorsFile: DefaultCollectorsFile,
		MetricSource: MetricSource{
			Kind: MetricSourceFile,
			File: DefaultCollectorsFile,
		},
	}
	yamlConfig, err := ParseYAMLConfig([]byte(`
version: 2
metrics:
  enableExporterMetrics: true
`))
	require.NoError(t, err)

	require.NoError(t, yamlConfig.ApplyTo(config))

	assert.True(t, config.EnableExporterMetrics)
	assert.Equal(t, DefaultCollectorsFile, config.CollectorsFile)
	assert.Equal(t, MetricSourceFile, config.MetricSource.Kind)
	assert.Equal(t, DefaultCollectorsFile, config.MetricSource.File)
}

func TestYAMLConfigApplyToCollections(t *testing.T) {
	config := &Config{
		CollectInterval: 30000,
		WatchRetention:  DefaultWatchRetention(),
	}
	yamlConfig, err := ParseYAMLConfig([]byte(`
version: 2
collections:
  - name: scrape
    every: 45s
    metrics:
      include: ["*"]
  - name: fast-thermals
    every: 5s
    sources:
      dcgm:
        watch:
          maxKeepAge: 0s
          maxKeepSamples: 2
    metrics:
      include:
        - DCGM_FI_DEV_GPU_TEMP
        - DCGM_FI_DEV_POWER_USAGE
`))
	require.NoError(t, err)

	require.NoError(t, yamlConfig.ApplyTo(config))

	assert.Equal(t, 45000, config.CollectInterval)
	require.Len(t, config.WatchGroups, 1)
	zero := time.Duration(0)
	maxSamples := int64(2)
	assert.Equal(t, WatchGroup{
		Name:     "fast-thermals",
		Interval: 5000,
		Fields: []string{
			"DCGM_FI_DEV_GPU_TEMP",
			"DCGM_FI_DEV_POWER_USAGE",
		},
		Retention: WatchRetentionOverride{
			MaxAge:     &zero,
			MaxSamples: &maxSamples,
		},
	}, config.WatchGroups[0])
}

// TestYAMLConfigApplyToWatchRetention verifies global, default-collection, and
// per-collection YAML values retain omission and zero semantics.
func TestYAMLConfigApplyToWatchRetention(t *testing.T) {
	config := &Config{
		CollectInterval: 30000,
		WatchRetention:  DefaultWatchRetention(),
	}
	yamlConfig, err := ParseYAMLConfig([]byte(`
version: 2
sources:
  dcgm:
    watch:
      maxKeepAge: 2m
      maxKeepSamples: 5
collections:
  - name: scrape
    every: 30s
    sources:
      dcgm:
        watch:
          maxKeepSamples: 4
    metrics:
      include: ["*"]
  - name: latest-values
    every: 1s
    sources:
      dcgm:
        watch:
          maxKeepAge: 0s
    metrics:
      include:
        - DCGM_FI_DEV_GPU_TEMP
  - name: inherited
    every: 30s
    metrics:
      include:
        - DCGM_FI_DEV_POWER_USAGE
`))
	require.NoError(t, err)

	require.NoError(t, yamlConfig.ApplyTo(config))

	assert.Equal(t, WatchRetention{MaxAge: 2 * time.Minute, MaxSamples: 4}, config.WatchRetention)
	require.Len(t, config.WatchGroups, 2)
	assert.Equal(t, WatchRetention{MaxAge: 0, MaxSamples: 4},
		config.WatchGroups[0].Retention.Resolve(config.WatchRetention))
	assert.Equal(t, config.WatchRetention,
		config.WatchGroups[1].Retention.Resolve(config.WatchRetention))
}

// TestParseYAMLConfigRejectsInvalidWatchRetention verifies malformed or impossible YAML bounds fail with field context.
func TestParseYAMLConfigRejectsInvalidWatchRetention(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "malformed age",
			yaml: `
version: 2
sources:
  dcgm:
    watch:
      maxKeepAge: invalid
`,
			wantErr: "sources.dcgm.watch.maxKeepAge",
		},
		{
			name: "negative age",
			yaml: `
version: 2
sources:
  dcgm:
    watch:
      maxKeepAge: -1s
`,
			wantErr: "maxKeepAge must not be negative",
		},
		{
			name: "negative samples",
			yaml: `
version: 2
sources:
  dcgm:
    watch:
      maxKeepSamples: -1
`,
			wantErr: "maxKeepSamples must not be negative",
		},
		{
			name: "samples exceed DCGM range",
			yaml: `
version: 2
sources:
  dcgm:
    watch:
      maxKeepSamples: 2147483648
`,
			wantErr: "must not exceed 2147483647",
		},
		{
			name: "group bounds both disabled",
			yaml: `
version: 2
collections:
  - name: invalid
    every: 1s
    sources:
      dcgm:
        watch:
          maxKeepAge: 0s
          maxKeepSamples: 0
    metrics:
      include:
        - DCGM_FI_DEV_GPU_TEMP
`,
			wantErr: "collections[0].sources.dcgm.watch",
		},
		{
			name: "unknown retention property",
			yaml: `
version: 2
sources:
  dcgm:
    watch:
      samples: 2
`,
			wantErr: "field samples not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseYAMLConfig([]byte(tt.yaml))

			require.Error(t, err)
			assert.Nil(t, config)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestParseYAMLConfigAllowsGlobalBoundsToBeCompletedByLaterOverrides guards deferred validation before CLI precedence.
func TestParseYAMLConfigAllowsGlobalBoundsToBeCompletedByLaterOverrides(t *testing.T) {
	config, err := ParseYAMLConfig([]byte(`
version: 2
sources:
  dcgm:
    watch:
      maxKeepAge: 0s
      maxKeepSamples: 0
`))

	require.NoError(t, err)
	require.NotNil(t, config)
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
