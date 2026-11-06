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
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	v2Outputs = `
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
`
	// powerOverrideV2 and powerGroupV1 describe the same watch assignment in version 2 and version 1.
	powerOverrideV2 = `
version: 2
metrics:
  - name: gpu
    file: /etc/dcgm-exporter/default-counters.csv
    every: 30s
    overrides:
      - name: fast-power
        match: ["DCGM_FI_DEV_BOARD_POWER_*"]
        every: 1s
        sources: {dcgm: {watch: {max_keep_age: 1m}}}
    enrichment: {kubernetes: false, container: false, dra: false, hpc_jobs: false}
` + v2Outputs
	powerGroupV1 = `
version: 1
collection:
  interval: 30s
  watchGroups:
    - name: fast-power
      interval: 1s
      fields: ["DCGM_FI_DEV_BOARD_POWER_*"]
      retention: {maxAge: 1m}
`
)

// baseConfig is the 4.8.4 default the YAML is applied onto.
func baseConfig() *Config {
	return &Config{
		CollectInterval:            30000,
		WatchRetention:             DefaultWatchRetention(),
		MaxConcurrentScrapes:       DefaultMaxConcurrentScrapes,
		XIDCountWindowSize:         300000,
		ClockEventsCountWindowSize: 300000,
		GPUBindUnbindPollInterval:  time.Second,
		MetricSource:               MetricSource{Kind: MetricSourceFile, File: DefaultCollectorsFile},
	}
}

// parseAndApply parses input and applies it to a base config, failing the test on any error.
func parseAndApply(t *testing.T, input string) (*YAMLConfig, *Config) {
	t.Helper()
	y, err := ParseYAMLConfig([]byte(input))
	require.NoError(t, err)
	config := baseConfig()
	require.NoError(t, y.ApplyTo(config))
	return y, config
}

// requireParseError fails the test unless parsing input fails with an error containing wantErr.
func requireParseError(t *testing.T, input, wantErr string) {
	t.Helper()
	config, err := ParseYAMLConfig([]byte(input))
	require.Error(t, err)
	assert.Nil(t, config)
	assert.Contains(t, err.Error(), wantErr)
}

// A nil YAMLConfig is a no-op, and a nil target Config is an error.
func TestNilYAMLConfig(t *testing.T) {
	var y *YAMLConfig
	assert.NoError(t, y.ApplyTo(baseConfig()))
	assert.Empty(t, y.SettingOrigins())
	assert.Empty(t, y.Warnings())
	require.Error(t, (&YAMLConfig{Version: 2}).ApplyTo(nil))
}

// The version 1 watch group and the version 2 override apply to the same Config,
// except that only the version 2 group reads regular expressions.
func TestV1AndV2PairApplyToTheSameConfig(t *testing.T) {
	_, fromV1 := parseAndApply(t, powerGroupV1)
	_, fromV2 := parseAndApply(t, powerOverrideV2)
	require.Len(t, fromV1.WatchGroups, 1)
	require.Len(t, fromV2.WatchGroups, 1)
	assert.False(t, fromV1.WatchGroups[0].ExtendedMatch)
	assert.True(t, fromV2.WatchGroups[0].ExtendedMatch)
	assert.Equal(t, "metrics[0].overrides[0]", fromV2.WatchGroups[0].Path)
	fromV2.WatchGroups[0].ExtendedMatch = false
	fromV2.WatchGroups[0].Path = ""
	assert.Equal(t, fromV1, fromV2)
}

func TestParseYAMLConfigStructureErrors(t *testing.T) {
	requireParseError(t, "version: 2\nserver: {}\nserver: {}\n", "duplicate YAML key")
	requireParseError(t, "version: 2\n---\nversion: 2\n", "multiple YAML documents")
	requireParseError(t, "version: 2\n? [a]\n: b\n", "YAML mapping keys must be scalar")
	requireParseError(t, "version: 2\nmetrics:\n  - name: gpu\n    file: /a\n    file: /b\n", "duplicate YAML key")
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
				MetricSource: MetricSource{Kind: MetricSourceFile, File: DefaultCollectorsFile},
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

// loadAndApply loads a config file from disk and applies it onto the 4.8.4 defaults.
func loadAndApply(t *testing.T, path string) (*YAMLConfig, *Config) {
	t.Helper()
	y, err := LoadYAMLConfigFile(path)
	require.NoError(t, err)
	config := baseConfig()
	require.NoError(t, y.ApplyTo(config))
	return y, config
}

// The default config file installed in images and packages loads and keeps the 4.8.4 defaults.
func TestShippedDefaultConfigFileKeepsDefaults(t *testing.T) {
	y, config := loadAndApply(t, "../../../etc/config.yaml")
	want := baseConfig()
	want.Address = ":9400"
	assert.Equal(t, want, config)
	assert.Empty(t, y.Warnings())
}

// chartDefaultConfig reads the default `config` document from the chart's values.yaml.
func chartDefaultConfig(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile("../../../deployment/values.yaml")
	require.NoError(t, err)
	var values struct {
		Config map[string]interface{} `yaml:"config"`
	}
	require.NoError(t, yaml.Unmarshal(data, &values))
	require.NotEmpty(t, values.Config)
	return values.Config
}

// The chart's default config is read by the real reader without warnings and gives
// the 4.8.4 defaults apart from Kubernetes labels, the listen address it writes out
// and the metrics list, which it reads from the file the chart mounts.
func TestChartDefaultConfigParsesAndKeepsDefaults(t *testing.T) {
	document, err := yaml.Marshal(chartDefaultConfig(t))
	require.NoError(t, err)
	y, err := ParseYAMLConfig(document)
	require.NoError(t, err)
	assert.Empty(t, y.Warnings())
	config := baseConfig()
	require.NoError(t, y.ApplyTo(config))

	// `auto` asks for Kubernetes labels where the cluster is reachable.
	assert.True(t, config.Kubernetes)
	config.Kubernetes = false
	assert.Equal(t, EnrichmentAuto, config.EnrichmentModes.Kubernetes)
	config.EnrichmentModes.Kubernetes = baseConfig().EnrichmentModes.Kubernetes

	// The chart mounts its metrics list, by default the image's list, at its own path.
	assert.Equal(t, "/etc/dcgm-exporter/chart/metrics/default-counters.csv", config.MetricSource.File)
	config.MetricSource.File = DefaultCollectorsFile

	want := baseConfig()
	want.Address = ":9400"
	assert.Equal(t, want, config)
}

// Every config example in the chart README parses with the real reader. An example is
// the exporter document under `config`, applied over the chart's default config as Helm
// does, or the document text in `config.data` of the upgrade section's 4.8.4 values,
// which is still a document the reader must accept.
func TestChartReadmeConfigExamplesParse(t *testing.T) {
	data, err := os.ReadFile("../../../deployment/README.md")
	require.NoError(t, err)
	fence := regexp.MustCompile("(?s)```yaml\n(.*?)```")
	parsed := 0
	for _, block := range fence.FindAllStringSubmatch(string(data), -1) {
		document := readmeConfigDocument(t, block[1])
		if document == nil {
			continue
		}

		_, err := ParseYAMLConfig(document)
		require.NoError(t, err, "README example:\n%s", document)
		parsed++
	}
	require.GreaterOrEqual(t, parsed, 5, "the README must contain config examples")
}

// readmeConfigDocument returns the exporter config document that one README YAML
// block holds, or nil when the block is not an exporter document.
func readmeConfigDocument(t *testing.T, block string) []byte {
	t.Helper()
	var values struct {
		Config map[string]interface{} `yaml:"config"`
	}
	if !strings.HasPrefix(block, "config:") && !strings.Contains(block, "\nconfig:") {
		return nil
	}
	require.NoError(t, yaml.Unmarshal([]byte(block), &values), "README block:\n%s", block)

	// A block with neither a version nor the 4.8.4 data text is not an exporter document.
	_, hasVersion := values.Config["version"]
	_, hasData := values.Config["data"]
	if !hasVersion && !hasData {
		return nil
	}

	if text, ok := values.Config["data"].(string); ok {
		return []byte(text)
	}

	document, err := yaml.Marshal(mergeHelmValues(chartDefaultConfig(t), values.Config))
	require.NoError(t, err)

	return document
}

// mergeHelmValues applies overrides to defaults the way Helm merges values: maps merge
// key by key, any other value (lists included) replaces the default, and null removes
// the key. It changes and returns defaults.
func mergeHelmValues(defaults, overrides map[string]interface{}) map[string]interface{} {
	for key, value := range overrides {
		if value == nil {
			delete(defaults, key)
			continue
		}

		override, isMap := value.(map[string]interface{})
		current, wasMap := defaults[key].(map[string]interface{})
		if isMap && wasMap {
			defaults[key] = mergeHelmValues(current, override)
			continue
		}

		defaults[key] = value
	}

	return defaults
}

// A relative metrics file in a version 2 file starts from the config file's
// directory. A version 1 file keeps the path as written.
func TestRelativeMetricFilePaths(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	v2 := write("v2.yaml", "version: 2\nmetrics:\n  - name: gpu\n    file: counters.csv\n"+v2Outputs)
	_, config := loadAndApply(t, v2)
	assert.Equal(t, filepath.Join(dir, "counters.csv"), config.MetricSource.File)

	absolute := write("v2-absolute.yaml", "version: 2\nmetrics:\n  - name: gpu\n    file: /etc/counters.csv\n"+v2Outputs)
	_, config = loadAndApply(t, absolute)
	assert.Equal(t, "/etc/counters.csv", config.MetricSource.File)

	v1 := write("v1.yaml", "version: 1\nmetrics:\n  file: counters.csv\n")
	_, config = loadAndApply(t, v1)
	assert.Equal(t, "counters.csv", config.MetricSource.File)
}

// An empty or comment-only file gets a clear error that names the file.
func TestEmptyConfigFileError(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "comment only": "# nothing here\n", "blank": "\n\n"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "empty.yaml")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			_, err := LoadYAMLConfigFile(path)
			require.ErrorContains(t, err, "empty.yaml")
			require.ErrorContains(t, err, "contains no YAML document")
			assert.NotContains(t, err.Error(), "EOF")
		})
	}
}

func TestLoadYAMLConfigFile(t *testing.T) {
	_, err := LoadYAMLConfigFile("  ")
	require.ErrorContains(t, err, "config file path is empty")
	_, err = LoadYAMLConfigFile(t.TempDir() + "/missing.yaml")
	require.ErrorContains(t, err, "read config file")
}

// indentedBlockScalarItem finds a sequence item that yaml.v3 wrote as a block
// scalar with an indentation indicator, such as "- |4-". yaml.v3 adds the
// indicator only for a string that starts with a space or newline. The header
// line holds nothing else, so a content line that only starts the same way does
// not match.
var indentedBlockScalarItem = regexp.MustCompile(`(?m)^\s*- \|[1-9][+-]?$`)

// TestIndentedBlockScalarItem checks that the pattern matches block scalar
// header lines of sequence items and not block scalar content lines.
func TestIndentedBlockScalarItem(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "header", yaml: "items:\n  - |4\n     a\n", want: true},
		{name: "header with strip chomping", yaml: "items:\n  - |4-\n     a\n", want: true},
		{name: "header with keep chomping", yaml: "- |2+\n   a\n", want: true},
		{name: "plain block scalar", yaml: "items:\n  - |\n    a\n", want: false},
		{name: "content line that starts like a header", yaml: "help: |\n  see\n  - |4 header example\n", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, indentedBlockScalarItem.MatchString(tt.yaml))
		})
	}
}

// failNormalized fails a fuzz case after the parsed configuration was written
// back as YAML. yaml.v3 cannot always read back a multi-line string that starts
// with a space or newline when it writes it as a block scalar in a sequence: it
// either fails, or reads an item such as "\n#" as an empty string because the
// "#" line looks like a comment. A failure on output that holds such an item is
// that library bug and skips the case instead, so output that reads back
// correctly is always checked.
func failNormalized(t *testing.T, normalized []byte, format string, args ...any) {
	t.Helper()

	if indentedBlockScalarItem.Match(normalized) {
		t.Skip("yaml.v3 cannot read back its own block scalar in a sequence")
	}
	t.Fatalf(format, args...)
}

// FuzzParseYAMLConfig checks that any input that parses can also be applied and
// survives a marshal and parse round trip.
func FuzzParseYAMLConfig(f *testing.F) {
	seeds := []string{
		powerOverrideV2,
		"version: 2\nmetrics:\n  - name: gpu\n    fields:\n      - {name: DCGM_FI_DEV_GPU_TEMP, prometheus_type: gauge, help: GPU temperature.}\n" + v2Outputs,
		"version: 2\nmetrics:\n  - name: gpu\n    file: /tmp/first.csv\n    file: /tmp/second.csv\n",
		"version: 2\nmetrics:\n  - name: gpu\n    file: /tmp/a.csv\n    overrides:\n      - match: [DCGM_EXP_XID_ERRORS_COUNT]\n        window: 2m\n" + v2Outputs,
		"version: 2\nmetrics:\n  - name: gpu\n    file: /tmp/a.csv\n    overrides:\n      - match: ['/DCGM_FI_DEV_(?i:gpu_temp).*/', 'DCGM_FI_DEV_BOARD_POWER_*', '@group']\n        every: 1s\n" + v2Outputs,
		"version: 2\nmetrics:\n  - name: gpu\n    file: /tmp/a.csv\n    overrides:\n      - match: ['/DCGM_EXP_(XID|CLOCK_EVENTS)_.*/']\n        window: 2m\n" + v2Outputs,
		"version: 2\n---\nversion: 2\n",
		"version: 1\ncollection:\n  interval: 30s\n",
		powerGroupV1,
		mergeKeyV1,
		mergeKeyV2,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseYAMLConfig(data)
		if err != nil {
			return
		}

		config := *baseConfig()
		if err := parsed.ApplyTo(&config); err != nil {
			t.Fatalf("parsed configuration could not be applied: %v", err)
		}

		normalized, err := yaml.Marshal(parsed)
		if err != nil {
			t.Fatalf("marshal parsed configuration: %v", err)
		}
		var generic any
		if err := yaml.Unmarshal(normalized, &generic); err != nil {
			failNormalized(t, normalized, "marshalled configuration is not valid YAML: %v\n%s", err, normalized)
		}
		reparsed, err := ParseYAMLConfig(normalized)
		if err != nil {
			failNormalized(t, normalized, "parse normalized configuration: %v\n%s", err, normalized)
		}
		reparsedConfig := *baseConfig()
		if err := reparsed.ApplyTo(&reparsedConfig); err != nil {
			failNormalized(t, normalized, "normalized configuration could not be applied: %v", err)
		}
		if !reflect.DeepEqual(config, reparsedConfig) {
			failNormalized(t, normalized, "configuration behavior changed after normalization:\nfirst:  %#v\nsecond: %#v", config, reparsedConfig)
		}
	})
}
