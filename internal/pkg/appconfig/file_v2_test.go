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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A version 2 file with nothing else keeps the 4.8.4 defaults.
func TestV2OnlyVersionKeepsDefaults(t *testing.T) {
	y, config := parseAndApply(t, "version: 2\n")
	assert.Equal(t, baseConfig(), config)
	assert.Empty(t, y.Warnings())
	assert.Empty(t, y.SettingOrigins())
}

// The default CSV with the default interval changes nothing, once every
// enrichment switch is written as false. A metric entry that leaves the
// switches out means auto; the enrichment tests cover that.
func TestV2DefaultMetricEntryKeepsDefaults(t *testing.T) {
	_, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /etc/dcgm-exporter/default-counters.csv
    every: 30s
    enrichment: {kubernetes: false, container: false, dra: false, hpc_jobs: false}
`+v2Outputs)
	assert.Equal(t, baseConfig(), config)
}

// Inline rows.
func TestV2InlineFields(t *testing.T) {
	y, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    fields:
      - {name: DCGM_FI_DEV_GPU_TEMP, prometheus_type: gauge, help: "GPU temperature (in C)."}
`+v2Outputs)
	assert.Equal(t, MetricSource{Kind: MetricSourceInline, Fields: []MetricField{
		{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "gauge", Help: "GPU temperature (in C)."},
	}}, config.MetricSource)
	assert.Equal(t, "metrics[0].fields", y.SettingOrigins()["MetricSource"])
	_, ok := config.MetricFileWatcherPath()
	assert.False(t, ok)
}

// An override becomes a watch group with its own retention age and inherits the exporter-wide samples.
func TestV2OverrideBecomesWatchGroup(t *testing.T) {
	_, config := parseAndApply(t, powerOverrideV2)
	assert.Equal(t, 30000, config.CollectInterval)
	require.Len(t, config.WatchGroups, 1)
	group := config.WatchGroups[0]
	assert.Equal(t, "fast-power", group.Name)
	assert.Equal(t, 1000, group.Interval)
	assert.Equal(t, []string{"DCGM_FI_DEV_BOARD_POWER_*"}, group.Fields)
	assert.True(t, group.ExtendedMatch)
	assert.Equal(t, WatchRetention{MaxAge: time.Minute, MaxSamples: 0}, group.Retention.Resolve(config.WatchRetention))
}

// A missing retention value is inherited and an explicit zero is kept.
func TestV2OverrideInheritsMissingRetentionAndKeepsExplicitZero(t *testing.T) {
	_, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:
      - match: [DCGM_FI_DEV_GPU_TEMP]
        every: 1s
        sources: {dcgm: {watch: {max_keep_samples: 0}}}
sources:
  dcgm:
    watch: {max_keep_age: 10m, max_keep_samples: 5}
`+v2Outputs)
	require.Len(t, config.WatchGroups, 1)
	resolved := config.WatchGroups[0].Retention.Resolve(config.WatchRetention)
	assert.Equal(t, WatchRetention{MaxAge: 10 * time.Minute, MaxSamples: 0}, resolved)
	assert.Equal(t, "metrics[0].overrides[0]", config.WatchGroups[0].Name)
}

// Exporter-wide retention (sources.dcgm.watch) and an override's retention are
// both rejected when they set both retention values to zero.
func TestV2RetentionRules(t *testing.T) {
	requireParseError(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:
      - match: [DCGM_FI_DEV_GPU_TEMP]
        every: 1s
        sources: {dcgm: {watch: {max_keep_age: 0s, max_keep_samples: 0}}}
`+v2Outputs, "metrics[0].overrides[0].sources.dcgm.watch: max_keep_age and max_keep_samples cannot both be zero")

	for name, tc := range map[string]struct{ yaml, wantErr string }{
		"malformed age":    {"max_keep_age: invalid", "sources.dcgm.watch.max_keep_age"},
		"negative age":     {"max_keep_age: -1s", "sources.dcgm.watch.max_keep_age must not be negative"},
		"negative count":   {"max_keep_samples: -1", "sources.dcgm.watch.max_keep_samples must not be negative"},
		"too many samples": {"max_keep_samples: 2147483648", "must not exceed 2147483647"},
	} {
		t.Run(name, func(t *testing.T) {
			requireParseError(t, "version: 2\nsources:\n  dcgm:\n    watch:\n      "+tc.yaml+"\n", tc.wantErr)
		})
	}

	requireParseError(t, "version: 2\nsources:\n  dcgm:\n    watch: {max_keep_age: 0s, max_keep_samples: 0}\n",
		"sources.dcgm.watch: max_keep_age and max_keep_samples cannot both be zero")

	// One zero value alone is allowed.
	_, err := ParseYAMLConfig([]byte("version: 2\nsources:\n  dcgm:\n    watch: {max_keep_age: 0s, max_keep_samples: 5}\n"))
	require.NoError(t, err)
}

// A window override sets the counter window in milliseconds.
// Rejected windows are covered in TestV2OverrideRejections.
func TestV2WindowOverrides(t *testing.T) {
	_, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 2m
      - match: [DCGM_EXP_CLOCK_EVENTS_COUNT]
        window: 90s
`+v2Outputs)
	assert.Equal(t, 120000, config.XIDCountWindowSize)
	assert.Equal(t, 90000, config.ClockEventsCountWindowSize)
	assert.Empty(t, config.WatchGroups, "window overrides are not watch groups")
}

// A window override picks the counters its items select and may select none.
func TestV2WindowOverrideItemForms(t *testing.T) {
	tests := []struct {
		name         string
		match        string
		wantXID      int
		wantClockEvt int
	}{
		{"exact name", "[DCGM_EXP_XID_ERRORS_COUNT]", 120000, 300000},
		{"wildcard", "['DCGM_EXP_*']", 120000, 120000},
		{"regex", "['/DCGM_EXP_(?i:clock)_EVENTS_COUNT/']", 300000, 120000},
		{"regex that matches part of a name", "['/DCGM_EXP_XID/']", 300000, 300000},
		{"gauge field", "[DCGM_FI_DEV_GPU_TEMP_CELSIUS]", 300000, 300000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, config := parseAndApply(t, "version: 2\nmetrics:\n  - name: gpu\n    file: /tmp/a.csv\n    overrides:\n      - match: "+tt.match+"\n        window: 2m\n"+v2Outputs)
			assert.Equal(t, tt.wantXID, config.XIDCountWindowSize)
			assert.Equal(t, tt.wantClockEvt, config.ClockEventsCountWindowSize)
		})
	}
}

// A window override is kept with its YAML path and match items so the metric
// source can be checked once it is read. Parsing reads no metric file, so an
// item that selects a normal field is not an error here.
func TestV2WindowOverridesAreRecordedWithoutReadingFiles(t *testing.T) {
	_, config := parseAndApply(t, "version: 2\nmetrics:\n  - name: gpu\n    file: /nonexistent/a.csv\n    overrides:\n"+
		"      - {match: [DCGM_EXP_XID_ERRORS_COUNT, 'DCGM_FI_DEV_*'], window: 5m}\n"+
		"      - {match: ['/NOTHING_.*/'], window: 1m}\n"+v2Outputs)
	require.Len(t, config.WindowOverrides, 2)
	assert.Equal(t, "metrics[0].overrides[0]", config.WindowOverrides[0].Path)
	assert.Len(t, config.WindowOverrides[0].Match, 2)
	assert.Equal(t, "metrics[0].overrides[1]", config.WindowOverrides[1].Path)
	assert.Empty(t, config.WatchGroups)
}

func TestV2OverrideRejections(t *testing.T) {
	const head = `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:`
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "invalid regular expression",
			yaml: `
      - match: [DCGM_FI_DEV_GPU_TEMP_CELSIUS, '/DCGM_(/']
        every: 1s
`,
			wantErr: "metrics[0].overrides[0].match[1]: invalid regular expression",
		},
		{
			name: "reserved group item",
			yaml: `
      - match: ['@thermals']
        every: 1s
`,
			wantErr: `metrics[0].overrides[0].match[0]: "@thermals": reserved for groups; not supported`,
		},
		{
			name: "zero window",
			yaml: `
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 0s
`,
			wantErr: "metrics[0].overrides[0].window: must be greater than 0",
		},
		{
			name: "fractional window",
			yaml: `
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 1500us
`,
			wantErr: "overrides[0].window: must be expressed in whole milliseconds",
		},
		{
			name: "same counter in two windows",
			yaml: `
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 1m
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 2m
`,
			wantErr: "metrics[0].overrides[1].window: DCGM_EXP_XID_ERRORS_COUNT already has a window in overrides[0]",
		},
		{
			name: "every and window",
			yaml: `
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        every: 1s
        window: 1m
`,
			wantErr: "overrides[0]: set either every or window, not both",
		},
		{
			name: "blank window beside every is treated as absent",
			yaml: `
      - match: [DCGM_FI_DEV_GPU_TEMP]
        every: 0s
        window: " "
`,
			wantErr: "overrides[0].every: must be greater than 0",
		},
		{
			name: "neither every nor window",
			yaml: `
      - match: [DCGM_FI_DEV_GPU_TEMP]
`,
			wantErr: "overrides[0]: one of every or window is required",
		},
		{
			name: "sources without every",
			yaml: `
      - match: [DCGM_FI_DEV_GPU_TEMP]
        sources: {dcgm: {watch: {max_keep_samples: 1}}}
`,
			wantErr: "overrides[0]: one of every or window is required",
		},
		{
			name: "sources with window",
			yaml: `
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 1m
        sources: {dcgm: {watch: {max_keep_samples: 1}}}
`,
			wantErr: "overrides[0].sources: not allowed together with window",
		},
		{
			name: "empty match",
			yaml: `
      - match: []
        every: 1s
`,
			wantErr: "overrides[0].match: must not be empty",
		},
		{
			name: "blank signal",
			yaml: `
      - match: ['DCGM_FI_DEV_GPU_TEMP', ' ']
        every: 1s
`,
			wantErr: "overrides[0].match[1]: must not be empty",
		},
		{
			name: "bad pattern",
			yaml: `
      - match: ['DCGM_[']
        every: 1s
`,
			wantErr: "overrides[0].match[0]: invalid pattern",
		},
		{
			name: "zero every",
			yaml: `
      - match: [DCGM_FI_DEV_GPU_TEMP]
        every: 0s
`,
			wantErr: "overrides[0].every: must be greater than 0",
		},
		{
			name: "sub-millisecond every",
			yaml: `
      - match: [DCGM_FI_DEV_GPU_TEMP]
        every: 1500us
`,
			wantErr: "overrides[0].every: must be expressed in whole milliseconds",
		},
		{
			name: "duplicate names",
			yaml: `
      - {name: a, match: [DCGM_FI_DEV_GPU_TEMP], every: 1s}
      - {name: a, match: [DCGM_FI_DEV_BOARD_POWER_WATTS], every: 1s}
`,
			wantErr: `overrides[1].name: "a" is already used by metrics[0].overrides[0]`,
		},
		{
			name: "name shared with a window override",
			yaml: `
      - {name: a, match: [DCGM_EXP_XID_ERRORS_COUNT], window: 1m}
      - {name: a, match: [DCGM_FI_DEV_BOARD_POWER_WATTS], every: 1s}
`,
			wantErr: `overrides[1].name: "a" is already used by metrics[0].overrides[0]`,
		},
		{
			name: "name with a space",
			yaml: `
      - {name: "fast power", match: [DCGM_FI_DEV_GPU_TEMP], every: 1s}
`,
			wantErr: `overrides[0].name: "fast power" may contain only letters, digits, _, - and . (dot)`,
		},
		{
			name: "name with brackets",
			yaml: `
      - {name: "metrics[0].overrides[0]", match: [DCGM_FI_DEV_GPU_TEMP], every: 1s}
      - {match: [DCGM_FI_DEV_BOARD_POWER_WATTS], every: 1s}
`,
			wantErr: `overrides[0].name: "metrics[0].overrides[0]" may contain only letters, digits, _, - and . (dot)`,
		},
		{
			name: "window override name with a slash",
			yaml: `
      - {name: a/b, match: [DCGM_EXP_XID_ERRORS_COUNT], window: 1m}
`,
			wantErr: `overrides[0].name: "a/b" may contain only`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, head+tt.yaml+v2Outputs, tt.wantErr)
		})
	}
}

// Two metric entries are reported as such even when the outputs block is missing.
func TestV2TwoEntriesWithoutOutputs(t *testing.T) {
	requireParseError(t, "version: 2\nmetrics:\n  - {name: a, file: /tmp/a.csv}\n  - {name: b, file: /tmp/b.csv}\n",
		"metrics: only one entry is supported, got 2")
}

func TestV2MetricEntryRejections(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			"file and fields", "  - name: gpu\n    file: /tmp/a.csv\n    fields: [{name: A, prometheus_type: gauge, help: h}]\n",
			"metrics[0]: exactly one of file or fields must be set",
		},
		{"neither file nor fields", "  - name: gpu\n", "metrics[0]: exactly one of file or fields must be set"},
		{"missing name", "  - file: /tmp/a.csv\n", "metrics[0].name: is required"},
		{"two entries", "  - {name: a, file: /tmp/a.csv}\n  - {name: b, file: /tmp/b.csv}\n", "metrics: only one entry is supported, got 2"},
		{"missing help", "  - name: gpu\n    fields: [{name: A, prometheus_type: gauge}]\n", "metrics[0].fields[0].help is required"},
		{"missing type", "  - name: gpu\n    fields: [{name: A, help: h}]\n", "metrics[0].fields[0].prometheus_type is required"},
		{"missing field name", "  - name: gpu\n    fields: [{prometheus_type: gauge, help: h}]\n", "metrics[0].fields[0].name is required"},
		{
			"duplicate field", "  - name: gpu\n    fields: [{name: A, prometheus_type: gauge, help: h}, {name: ' A ', prometheus_type: gauge, help: h}]\n",
			"metrics[0].fields[1].name duplicates metrics[0].fields[0].name",
		},
		{"bad every", "  - name: gpu\n    file: /tmp/a.csv\n    every: 30\n", "metrics[0].every: invalid duration \"30\""},
		{"sub-millisecond every", "  - name: gpu\n    file: /tmp/a.csv\n    every: 1500us\n", "metrics[0].every: must be expressed in whole milliseconds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, "version: 2\nmetrics:\n"+tt.yaml+v2Outputs, tt.wantErr)
		})
	}
}

func TestV2ServerSourcesAndObservability(t *testing.T) {
	y, config := parseAndApply(t, `
version: 2
server:
  max_concurrent_scrapes: 8
observability:
  metrics: {enabled: true}
sources:
  dcgm:
    watch: {max_keep_age: 2m, max_keep_samples: 4}
    detect_bind_unbind: {enabled: true, every: 250ms}
`)
	assert.Equal(t, 8, config.MaxConcurrentScrapes)
	assert.True(t, config.EnableExporterMetrics)
	assert.Equal(t, WatchRetention{MaxAge: 2 * time.Minute, MaxSamples: 4}, config.WatchRetention)
	assert.True(t, config.EnableGPUBindUnbindWatch)
	assert.Equal(t, 250*time.Millisecond, config.GPUBindUnbindPollInterval)
	assert.Equal(t, map[string]string{
		"MaxConcurrentScrapes":      "server.max_concurrent_scrapes",
		"EnableExporterMetrics":     "observability.metrics.enabled",
		"WatchRetention.MaxAge":     "sources.dcgm.watch.max_keep_age",
		"WatchRetention.MaxSamples": "sources.dcgm.watch.max_keep_samples",
		"EnableGPUBindUnbindWatch":  "sources.dcgm.detect_bind_unbind.enabled",
		"GPUBindUnbindPollInterval": "sources.dcgm.detect_bind_unbind.every",
	}, y.SettingOrigins())
}

func TestV2ExplicitFalseAndKeptDefaults(t *testing.T) {
	config := baseConfig()
	config.EnableExporterMetrics = true
	config.EnableGPUBindUnbindWatch = true
	y, err := ParseYAMLConfig([]byte("version: 2\nobservability: {metrics: {enabled: false}}\nsources: {dcgm: {detect_bind_unbind: {enabled: false}}}\n"))
	require.NoError(t, err)
	require.NoError(t, y.ApplyTo(config))
	assert.False(t, config.EnableExporterMetrics)
	assert.False(t, config.EnableGPUBindUnbindWatch)
	assert.Equal(t, time.Second, config.GPUBindUnbindPollInterval)
}

func TestV2ValueRejections(t *testing.T) {
	requireParseError(t, "version: 2\nserver:\n  max_concurrent_scrapes: 0\n", "server.max_concurrent_scrapes: must be greater than 0")
	requireParseError(t, "version: 2\nserver:\n  max_concurrent_scrapes: -1\n", "server.max_concurrent_scrapes: must be greater than 0")
	requireParseError(t, "version: 2\nsources:\n  dcgm:\n    detect_bind_unbind: {every: 0s}\n",
		"sources.dcgm.detect_bind_unbind.every: must be greater than 0")
	requireParseError(t, "version: 2\nsources:\n  dcgm:\n    detect_bind_unbind: {every: soon}\n",
		"sources.dcgm.detect_bind_unbind.every")
}

// A rejected file must not leave a half-applied Config behind.
func TestV2ApplyToLeavesConfigAloneOnError(t *testing.T) {
	scrapes := 8
	y := &YAMLConfig{
		Version: 2,
		// The server limit and the metric file are written before the bad interval is read.
		Server:  &YAMLServer{MaxConcurrentScrapes: &scrapes},
		Metrics: []YAMLMetricEntry{{Name: "gpu", File: "/tmp/a.csv", Every: "bad"}},
		Outputs: map[string]YAMLOutput{"prometheus": {Type: "prometheus", Schema: "dcgm/v1"}},
	}
	config := baseConfig()
	err := y.ApplyTo(config)
	require.ErrorContains(t, err, "metrics[0].every")
	assert.Equal(t, baseConfig(), config)
}

// The complete example file loads, limited to the keys this build accepts.
func TestV2SharedExampleSubset(t *testing.T) {
	y, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /etc/dcgm-exporter/default-counters.csv
    every: 30s
    overrides:
      - name: fast-power
        match: ["DCGM_FI_DEV_BOARD_POWER_*"]
        every: 1s
        sources:
          dcgm:
            watch: {max_keep_age: 1m, max_keep_samples: 0}
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 5m
      - match: [DCGM_EXP_CLOCK_EVENTS_COUNT]
        window: 5m
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
sources:
  dcgm:
    watch: {max_keep_age: 10m, max_keep_samples: 0}
    detect_bind_unbind: {enabled: false, every: 1s}
server:
  max_concurrent_scrapes: 16
observability:
  metrics: {enabled: false}
`)
	require.Len(t, config.WatchGroups, 1)
	assert.Equal(t, "fast-power", config.WatchGroups[0].Name)
	assert.Equal(t, 300000, config.XIDCountWindowSize)
	assert.Equal(t, 300000, config.ClockEventsCountWindowSize)
	assert.Equal(t, "metrics[0].overrides[2].window", y.SettingOrigins()["ClockEventsCountWindowSize"])
}

// Unnamed interval overrides are named by their YAML path, so each has a distinct name,
// and user names with the allowed characters are kept.
func TestV2UnnamedOverridesAreNamedByPath(t *testing.T) {
	_, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /tmp/a.csv
    overrides:
      - {match: [DCGM_FI_DEV_GPU_TEMP], every: 1s}
      - {name: fast_power-1.a, match: [DCGM_FI_DEV_BOARD_POWER_WATTS], every: 2s}
      - {match: [DCGM_FI_DEV_MEMORY_TEMP_CELSIUS], every: 3s}
`+v2Outputs)
	require.Len(t, config.WatchGroups, 3)
	assert.Equal(t, "metrics[0].overrides[0]", config.WatchGroups[0].Name)
	assert.Equal(t, "fast_power-1.a", config.WatchGroups[1].Name)
	assert.Equal(t, "metrics[0].overrides[2]", config.WatchGroups[2].Name)
}
