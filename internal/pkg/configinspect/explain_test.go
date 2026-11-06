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
package configinspect

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// explainJSON explains a config as JSON and decodes the result's data.
func explainJSON(t *testing.T, r *appconfig.Resolved) (explainData, Result) {
	t.Helper()
	result, err := Explain(r, nil, "json", false)
	require.NoError(t, err)
	var envelope struct {
		FormatVersion int             `json:"format_version"`
		Command       string          `json:"command"`
		Data          json.RawMessage `json:"data"`
	}

	require.NoError(t, json.Unmarshal(result.Stdout, &envelope))
	assert.Equal(t, 1, envelope.FormatVersion)
	assert.Equal(t, "config.explain", envelope.Command)
	var data explainData
	require.NoError(t, json.Unmarshal(envelope.Data, &data))
	return data, result
}

func TestExplainListsFieldsWithTheirOverrides(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+`    overrides:
      - name: temperatures
        match: [DCGM_FI_DEV_GPU_TEMP]
        every: 2s
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 10m
`+outputsBlock)

	data, result := explainJSON(t, r)
	assert.Empty(t, result.Stderr)
	assert.NoError(t, result.Failure)

	fields := map[string]inspectionField{}
	for _, field := range data.Fields {
		fields[field.Name] = field
	}

	assert.Equal(t, "2s", fields["DCGM_FI_DEV_GPU_TEMP"].Every)
	assert.Equal(t, "temperatures", fields["DCGM_FI_DEV_GPU_TEMP"].Override)
	assert.Equal(t, "30s", fields["DCGM_FI_DEV_SM_CLOCK"].Every)
	assert.Empty(t, fields["DCGM_FI_DEV_SM_CLOCK"].Override)
	assert.Equal(t, "10m", fields["DCGM_EXP_XID_ERRORS_COUNT"].Window)
	assert.Equal(t, "5m", fields["DCGM_EXP_CLOCK_EVENTS_COUNT"].Window)
	assert.Zero(t, fields["DCGM_EXP_XID_ERRORS_COUNT"].DCGMFieldID, "an exporter counter has no DCGM field")
	assert.Contains(t, fields, "DCGM_FI_DEV_XID_ERROR", "the field the XID counter reads is collected too")
	assert.Contains(t, fields, "DCGM_FI_DEV_CLOCKS_EVENT_REASONS", "the field the clock events counter reads is collected too")

	require.Len(t, data.Overrides, 1)
	assert.Equal(t, "temperatures", data.Overrides[0].Name)
	assert.Equal(t, []matchResolved{{Item: "DCGM_FI_DEV_GPU_TEMP", Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}}}, data.Overrides[0].Match)
	assert.Contains(t, data.NotChecked, "dcgm")
}

// TestExplainListsAliasMatches checks that each selector lists the field receiving its override.
func TestExplainListsAliasMatches(t *testing.T) {
	for _, selector := range []string{
		"DCGM_FI_DEV_XID_ERRORS",
		"DCGM_FI_DEV_XID_ERRORS*",
		"/DCGM_FI_DEV_XID_ERRORS/",
	} {
		t.Run(selector, func(t *testing.T) {
			r := resolvedFrom(t, inlineMetrics+`    overrides:
      - name: events
        match: [`+selector+`]
        every: 2s
`+outputsBlock)

			data, result := explainJSON(t, r)
			require.NoError(t, result.Failure)
			require.Len(t, data.Overrides, 1)
			assert.Equal(t, []matchResolved{{Item: selector, Fields: []string{"DCGM_FI_DEV_XID_ERROR"}}}, data.Overrides[0].Match)
			for _, field := range data.Fields {
				if field.Name == "DCGM_FI_DEV_XID_ERROR" {
					assert.Equal(t, "events", field.Override)
					assert.Equal(t, "2s", field.Every)
				}
			}
		})
	}
}

func TestExplainTextSummarizesTheFields(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+`    overrides:
      - name: temperatures
        match: [DCGM_FI_DEV_GPU_TEMP]
        every: 2s
`+outputsBlock)

	result, err := Explain(r, nil, "text", false)
	require.NoError(t, err)
	text := string(result.Stdout)
	assert.Contains(t, text, "Config file   exporter.yaml")
	assert.Contains(t, text, "Settings that differ from defaults")
	assert.Contains(t, text, "  override temperatures  (1 field)  every 2s, retention 10m, no sample limit")
	assert.NotContains(t, text, "match DCGM_FI_DEV_GPU_TEMP ->", "match lines are for verbose")
	assert.Contains(t, text, "Use --verbose for all settings and all fields.")

	result, err = Explain(r, nil, "text", true)
	require.NoError(t, err)
	verbose := string(result.Stdout)
	assert.Contains(t, verbose, "All settings")
	assert.Contains(t, verbose, "match DCGM_FI_DEV_GPU_TEMP -> DCGM_FI_DEV_GPU_TEMP")
	assert.Regexp(t, `(?m)^ +DCGM_FI_DEV_GPU_TEMP +gauge +id 150 +every 2s, retention \S+, no sample limit +override temperatures$`, verbose)
	assert.NotContains(t, verbose, "Use --verbose")
}

func TestExplainReportsAMissingMetricFileAsAFailure(t *testing.T) {
	r := resolvedDefaults()
	r.Config.MetricSource.File = filepath.Join(t.TempDir(), "missing.csv")
	result, err := Explain(r, nil, "text", false)
	require.NoError(t, err)
	assert.Empty(t, result.Stdout, "a failed run writes nothing to stdout")
	require.Error(t, result.Failure)
	assert.Contains(t, string(result.Stderr), "missing.csv: cannot read metric file: file does not exist")
}

func TestExplainPassesOnAResolutionFailure(t *testing.T) {
	problem := errors.New("settings did not resolve")
	result, err := Explain(resolvedDefaults(), problem, "text", false)
	require.NoError(t, err)
	assert.Empty(t, result.Stdout)
	assert.Equal(t, problem, result.Failure)
	assert.Equal(t, "error    settings did not resolve\n", string(result.Stderr))
}

func TestExplainWithoutFieldsReadsNothingFromAConfigMap(t *testing.T) {
	r := resolvedDefaults()
	r.Config.MetricSource = appconfig.MetricSource{
		Kind: appconfig.MetricSourceConfigMap, ConfigMap: appconfig.ConfigMapMetricSource{Namespace: "ns", Name: "metrics"},
	}

	r.Config.WatchGroups = []appconfig.WatchGroup{{Name: "temps", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}}}
	r.Origins["MetricSource"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--configmap-data"}
	data, _ := explainJSON(t, r)
	assert.Empty(t, data.Fields)
	assert.Contains(t, data.NotChecked, "configmap_content")
	require.Len(t, data.Overrides, 1)
	assert.Equal(t, []matchResolved{{Item: "DCGM_FI_DEV_GPU_TEMP", Fields: []string{}}}, data.Overrides[0].Match)

	result, err := Explain(r, nil, "text", false)
	require.NoError(t, err)
	assert.Contains(t, string(result.Stdout), "Fields   not read: ConfigMap ns:metrics")
	assert.Contains(t, string(result.Stdout), "  override temps  every ")
	assert.NotContains(t, string(result.Stdout), "(0 fields)")
}

func TestExplainListsWhatAFlagReplaced(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+"    every: 2s\n"+outputsBlock)
	r.ReplacedEnv["CollectInterval"] = []appconfig.EnvValue{{Name: "DCGM_EXPORTER_INTERVAL", Value: "1500"}}
	r.Config.CollectInterval = 5000
	r.Origins["CollectInterval"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--collect-interval"}

	data, _ := explainJSON(t, r)
	var every settingRow
	for _, origin := range data.Settings {
		if origin.Path == "metrics[0].every" {
			every = origin
		}
	}

	assert.Equal(t, "5s", every.Value)
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--collect-interval"}, every.Source)
	require.Len(t, every.Replaced, 2)
	assert.Equal(t, settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"}, every.Replaced[0].settingSource)
	assert.Equal(t, "1.5s", every.Replaced[0].Value)
	assert.Equal(t, "1500", every.Replaced[0].Raw)
	assert.Equal(t, appconfig.OriginFile, every.Replaced[1].Kind)
	assert.Equal(t, "2s", every.Replaced[1].Value)
}

func TestReplacedEnvironmentValueConvertsMilliseconds(t *testing.T) {
	for _, tc := range []struct {
		name, flag, raw string
		wantValue       any
		wantRaw         string
	}{
		{"milliseconds shown as a duration", appconfig.FlagCollectInterval, "1500", "1.5s", "1500"},
		{"unparsable stays as written", appconfig.FlagCollectInterval, "soon", "soon", ""},
		{"zero stays as written", appconfig.FlagXIDCountWindowSize, "0", "0", ""},
		{"largest duration is converted", appconfig.FlagXIDCountWindowSize, "9223372036854", "2562047h47m16.854s", "9223372036854"},
		{"above the largest duration stays as written", appconfig.FlagXIDCountWindowSize, "9223372036855", "9223372036855", ""},
		{"other flags keep their text", appconfig.FlagAddress, ":9500", ":9500", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, raw := replacedEnvironmentValue(tc.flag, tc.raw)
			assert.Equal(t, tc.wantValue, value)
			assert.Equal(t, tc.wantRaw, raw)
		})
	}
}

func TestExplainTextListsEachReplacedValueUnderItsRow(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+"    every: 2s\n"+outputsBlock)
	r.ReplacedEnv["CollectInterval"] = []appconfig.EnvValue{{Name: "DCGM_EXPORTER_INTERVAL", Value: "1500"}}
	r.Config.CollectInterval = 5000
	r.Origins["CollectInterval"] = appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--collect-interval"}

	result, err := Explain(r, nil, "text", false)
	require.NoError(t, err)
	text := string(result.Stdout)
	assert.Contains(t, text, "flag --collect-interval\n")
	assert.Contains(t, text, "    replaces env DCGM_EXPORTER_INTERVAL value: 1.5s (1500)\n")
	assert.Contains(t, text, "    replaces file:")
}

// TestExplainWarnsWhenACountWindowOutlastsTheHistory checks that explain
// reports the warning in its JSON warnings and on stderr.
func TestExplainWarnsWhenACountWindowOutlastsTheHistory(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+`    overrides:
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 20m
`+outputsBlock)

	result, err := Explain(r, nil, "json", false)
	require.NoError(t, err)

	var envelope struct {
		Data struct {
			Warnings []diagnostic `json:"warnings"`
		} `json:"data"`
	}

	require.NoError(t, json.Unmarshal(result.Stdout, &envelope))
	require.Len(t, envelope.Data.Warnings, 1)
	const want = "metrics[0].overrides[0].window: the 20m window is longer than " +
		"--watch-max-keep-age (10m), so DCGM_EXP_XID_ERRORS_COUNT can count only the last 10m"
	assert.Equal(t, want, envelope.Data.Warnings[0].Summary)
	assert.Contains(t, string(result.Stderr), want)
}
