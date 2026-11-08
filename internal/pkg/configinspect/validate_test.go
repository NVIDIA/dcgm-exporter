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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestValidateAcceptsAValidConfigAndListsWhatItChecked checks that validate accepts a valid config and lists
// what it checked.
func TestValidateAcceptsAValidConfigAndListsWhatItChecked(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+outputsBlock)
	result, err := Validate(r, nil, "text")
	require.NoError(t, err)
	assert.NoError(t, result.Failure)
	assert.Empty(t, result.Stderr)
	text := string(result.Stdout)
	assert.Contains(t, text, "exporter.yaml: valid (0 errors, 0 warnings)\n")
	assert.Contains(t, text, "Checked: configuration settings\n")
	assert.Contains(t, text, "Checked: inline metric definitions\n")
	assert.Contains(t, text, "Not checked (needs the running exporter): GPUs and MIG instances, DCGM connection")
}

// TestValidateWritesWarningsToStderrAndTheResultToStdout checks that validate writes warnings to stderr and
// the result to stdout.
func TestValidateWritesWarningsToStderrAndTheResultToStdout(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+outputsBlock)
	r.Warnings = []appconfig.ResolutionWarning{{Message: "a notice"}}
	result, err := Validate(r, nil, "text")
	require.NoError(t, err)
	assert.NoError(t, result.Failure)
	assert.Equal(t, "warning  a notice\n", string(result.Stderr))
	assert.Contains(t, string(result.Stdout), "valid (0 errors, 1 warning)")
}

// TestValidateWritesNothingToStdoutWhenTheConfigIsInvalid checks that validate writes nothing to stdout when
// the config is invalid.
func TestValidateWritesNothingToStdoutWhenTheConfigIsInvalid(t *testing.T) {
	r := resolvedDefaults()
	r.Config.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceInline, Fields: []appconfig.MetricField{
		{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "gauge", Help: "t"},
	}}

	r.Config.WatchGroups = []appconfig.WatchGroup{{Name: "none", Interval: 1000, Fields: []string{"DCGM_FI_DEV_NOT_THERE"}}}
	result, err := Validate(r, nil, "text")
	require.NoError(t, err)
	assert.Empty(t, result.Stdout)
	require.EqualError(t, result.Failure, "configuration is invalid")
	stderr := string(result.Stderr)
	assert.Contains(t, stderr, "no config file: invalid (1 error, 0 warnings)\n")
	assert.Contains(t, stderr, "error    watch group \"none\" matched no configured fields")
	assert.Contains(t, stderr, "Checked: configuration settings\n")
}

// TestValidateReportsAWindowOverrideOnANormalFieldBeforeGroupProblems checks that validate reports a window
// override on a normal field before group problems.
func TestValidateReportsAWindowOverrideOnANormalFieldBeforeGroupProblems(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+`    overrides:
      - match: [DCGM_FI_DEV_GPU_TEMP]
        window: 5m
`+outputsBlock)

	result, err := Validate(r, nil, "text")
	require.NoError(t, err)
	assert.Contains(t, string(result.Stderr), "validate overrides: metrics[0].overrides[0].match[0]: selects DCGM_FI_DEV_GPU_TEMP, which is not a window counter")
}

// TestValidateLocatesAnOverrideThatMatchesNothingAtItsEntry checks that validate locates an override that
// matches nothing at its entry.
func TestValidateLocatesAnOverrideThatMatchesNothingAtItsEntry(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+`    overrides:
      - {name: temp, match: [DCGM_FI_DEV_GPU_TEMP], every: 1s}
      - {name: clock, match: [DCGM_FI_DEV_SM_CLOCK], every: 2s}
      - {name: none, match: [DCGM_FI_DEV_NOT_THERE], every: 3s}
`+outputsBlock)

	result, err := Validate(r, nil, "json")
	require.NoError(t, err)
	var envelope struct {
		Data validateData `json:"data"`
	}

	require.NoError(t, json.Unmarshal(result.Stdout, &envelope))
	require.Len(t, envelope.Data.Diagnostics, 1)
	problem := envelope.Data.Diagnostics[0]
	assert.Equal(t, `override "none" (metrics[0].overrides[2]) matched no configured fields`, problem.Summary)
	assert.Equal(t, "metrics[0].overrides[2]", problem.Source.Path)
	assert.Positive(t, problem.Source.Line)
}

// TestValidateHidesLocalFilesWhenTheSettingsDidNotResolve checks that validate hides local files when the
// settings did not resolve.
func TestValidateHidesLocalFilesWhenTheSettingsDidNotResolve(t *testing.T) {
	const prefix = "Not checked until the errors above are fixed: "
	tests := map[string]struct {
		setup func(*appconfig.Config)
		want  string // empty means no such line
	}{
		"metric file":                {setup: func(*appconfig.Config) {}, want: prefix + "metric file\n"},
		"metric and web config file": {setup: func(c *appconfig.Config) { c.WebConfigFile = "web.yml" }, want: prefix + "metric file and web config file\n"},
		"inline metric definitions":  {setup: func(c *appconfig.Config) { c.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceInline} }, want: prefix + "inline metric definitions\n"},
		"ConfigMap reads nothing local": {setup: func(c *appconfig.Config) {
			c.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceConfigMap}
		}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := resolvedDefaults()
			tc.setup(r.Config)

			result, err := Validate(r, errors.New("settings did not resolve"), "text")
			require.NoError(t, err)
			assert.Empty(t, result.Stdout)
			assert.Error(t, result.Failure)

			stderr := string(result.Stderr)
			assert.NotContains(t, stderr, "Checked: configuration settings")
			if tc.want == "" {
				assert.NotContains(t, stderr, prefix)
				return
			}

			assert.Contains(t, stderr, tc.want)
		})
	}
}

// TestValidateJSON checks the JSON result of validate.
func TestValidateJSON(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+outputsBlock)
	result, err := Validate(r, nil, "json")
	require.NoError(t, err)
	var envelope struct {
		FormatVersion int          `json:"format_version"`
		Command       string       `json:"command"`
		Data          validateData `json:"data"`
	}

	require.NoError(t, json.Unmarshal(result.Stdout, &envelope))
	assert.Equal(t, "config.validate", envelope.Command)
	assert.True(t, envelope.Data.Valid)
	assert.Equal(t, []string{"configuration settings", "inline metric definitions"}, envelope.Data.Checked)
	assert.NotEmpty(t, envelope.Data.NotChecked)
	assert.Empty(t, envelope.Data.Diagnostics)

	// An invalid config still prints its JSON, and the run fails.
	result, err = Validate(resolvedDefaults(), errors.New("bad"), "json")
	require.NoError(t, err)
	assert.Error(t, result.Failure)
	require.NoError(t, json.Unmarshal(result.Stdout, &envelope))
	assert.False(t, envelope.Data.Valid)
	assert.Contains(t, envelope.Data.NotChecked, "local_files")
	assert.Empty(t, result.Stderr)
}

// TestPlural checks a count is written with its noun.
func TestPlural(t *testing.T) {
	assert.Equal(t, "0 errors", plural(0, "error"))
	assert.Equal(t, "1 error", plural(1, "error"))
	assert.Equal(t, "2 errors", plural(2, "error"))
}

// TestSingleFileError checks when the only error is one the config file reader found.
func TestSingleFileError(t *testing.T) {
	inFile := diagnostic{Severity: severityError, Source: settingSource{Kind: appconfig.OriginFile, Line: 3}}
	assert.True(t, singleFileError([]diagnostic{inFile, {Severity: severityWarning}}))
	assert.False(t, singleFileError([]diagnostic{inFile, inFile}), "several errors may come from several places")
	assert.False(t, singleFileError([]diagnostic{{Severity: severityError, Source: settingSource{Kind: appconfig.OriginFlag}}}))
	assert.False(t, singleFileError([]diagnostic{{Severity: severityError, Source: settingSource{Kind: appconfig.OriginFile}}}), "no line")
	assert.False(t, singleFileError(nil))
}

// TestValidateSaysTheFileReaderStopsAtItsFirstProblem checks that validate says the file reader stops at its
// first problem.
func TestValidateSaysTheFileReaderStopsAtItsFirstProblem(t *testing.T) {
	r := resolvedDefaults()
	r.Config.ConfigFile = "exporter.yaml"
	problem := &appconfig.ConfigError{
		Path: "metrics[0].every", Line: 3, Column: 5, Msg: "must be greater than 0", Err: errors.New("metrics[0].every: must be greater than 0"),
	}

	result, err := Validate(r, problem, "text")
	require.NoError(t, err)
	assert.Contains(t, string(result.Stderr), "Only the first problem in the config file is reported")
	assert.Contains(t, string(result.Stderr), "error    exporter.yaml:3:5 metrics[0].every: must be greater than 0")

	// Two problems may not both come from the file, so the note does not apply.
	result, err = Validate(r, errors.Join(problem, errors.New("another")), "text")
	require.NoError(t, err)
	assert.NotContains(t, string(result.Stderr), "Only the first problem")
}
