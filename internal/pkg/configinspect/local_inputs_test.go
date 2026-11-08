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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// writeTestFile writes contents to a new file called name in a temporary
// directory and returns its path.
func writeTestFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestNotCheckedForListsTheConfigMapOnlyForAConfigMapSource(t *testing.T) {
	config := appconfig.DefaultConfig()
	ids := notCheckedIDs(notCheckedFor(config))
	assert.Contains(t, ids, "gpus")
	assert.Contains(t, ids, "listener")
	assert.NotContains(t, ids, "configmap_content")

	config.MetricSource.Kind = appconfig.MetricSourceConfigMap
	assert.Contains(t, notCheckedIDs(notCheckedFor(config)), "configmap_content")
	assert.NotContains(t, notCheckedIDs(notCheckedFor(appconfig.DefaultConfig())), "configmap_content", "the list is not shared between calls")
}

func TestReadLocalInputsReadsTheMetricFile(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource.File = writeTestFile(t, "metrics.csv", "DCGM_FI_DEV_GPU_TEMP, gauge, Temperature.\n")
	inputs := readLocalInputs(config, false)
	assert.Empty(t, inputs.problems)
	require.NotNil(t, inputs.counters)
	assert.Len(t, inputs.counters.DCGMCounters, 1)
	assert.Equal(t, []string{"metric file: " + config.MetricSource.File}, inputs.checked)
}

func TestReadLocalInputsReportsMetricFileProblemsWithoutSystemDetails(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource.File = filepath.Join(t.TempDir(), "missing.csv")
	inputs := readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	assert.Nil(t, inputs.counters)
	problem := inputs.problems[0].(*appconfig.SettingError)
	assert.Equal(t, "MetricSource", problem.Field)
	assert.Equal(t, config.MetricSource.File+": cannot read metric file: file does not exist", problem.Summary)

	config.MetricSource.File = writeTestFile(t, "quote.csv", "DCGM_FI_DEV_GPU_TEMP, \"gauge, Help.\n")
	inputs = readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	assert.Contains(t, inputs.problems[0].(*appconfig.SettingError).Summary, "quote.csv: line 1: ")

	config.MetricSource.File = writeTestFile(t, "bad.csv", "NOT_A_FIELD, gauge, Help.\n")
	inputs = readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	assert.Contains(t, inputs.problems[0].Error(), "validate metric definitions")

	// The problem names the line of the bad row, counting comments and blank lines.
	config.MetricSource.File = writeTestFile(t, "typed.csv",
		"# comment\n\nDCGM_FI_DEV_GPU_TEMP, gauge, Help.\n# comment\nDCGM_FI_DEV_FB_USED, histogram, Help.\n")
	inputs = readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	assert.Contains(t, inputs.problems[0].Error(), "validate metric definitions: line 5: unsupported Prometheus metric type")
	// The summary names the file, so the line is not read as a line of the config file.
	assert.True(t, strings.HasPrefix(inputs.problems[0].(*appconfig.SettingError).Summary,
		config.MetricSource.File+": validate metric definitions: line 5: unsupported Prometheus metric type"))

	// Inline definitions have no file to name.
	config.MetricSource = appconfig.MetricSource{
		Kind:   appconfig.MetricSourceInline,
		Fields: []appconfig.MetricField{{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "histogram", Help: "Help."}},
	}
	inputs = readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	assert.Empty(t, inputs.problems[0].(*appconfig.SettingError).Summary)
}

func TestReadLocalInputsChecksOverridesOnlyWhenAsked(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource.File = writeTestFile(t, "metrics.csv", "DCGM_FI_DEV_GPU_TEMP, gauge, Temperature.\n")
	config.WatchGroups = []appconfig.WatchGroup{{Name: "none", Interval: 1000, Fields: []string{"DCGM_FI_DEV_NOT_THERE"}}}

	assert.Empty(t, readLocalInputs(config, false).problems)

	inputs := readLocalInputs(config, true)
	require.Len(t, inputs.problems, 1)
	assert.Contains(t, inputs.problems[0].Error(), "watch group \"none\" matched no configured fields")
}

func TestReadLocalInputsReadsInlineDefinitionsAndSkipsAConfigMap(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceInline, Fields: []appconfig.MetricField{
		{Name: "DCGM_FI_DEV_GPU_TEMP", PrometheusType: "gauge", Help: "Temperature."},
	}}

	inputs := readLocalInputs(config, true)
	assert.Empty(t, inputs.problems)
	assert.Equal(t, []string{"inline metric definitions"}, inputs.checked)

	config.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceConfigMap}
	inputs = readLocalInputs(config, true)
	assert.Empty(t, inputs.problems)
	assert.Empty(t, inputs.checked)
	assert.Nil(t, inputs.counters)
}

func TestReadLocalInputsChecksTheWebPolicyWithoutRepeatingItsContents(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceInline}

	config.WebConfigFile = writeTestFile(t, "web.yml", "")
	inputs := readLocalInputs(config, false)
	assert.Empty(t, inputs.problems)
	assert.Equal(t, []string{"web config file: " + config.WebConfigFile}, inputs.checked[len(inputs.checked)-1:])

	config.WebConfigFile = filepath.Join(t.TempDir(), "missing.yml")
	inputs = readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	problem := inputs.problems[0].(*appconfig.SettingError)
	assert.Equal(t, "WebConfigFile", problem.Field)
	assert.Equal(t, config.WebConfigFile+": cannot read web config file: file does not exist", problem.Summary)

	config.WebConfigFile = writeTestFile(t, "secret.yml", "basic_auth_users: [not-a-map-secret-marker\n")
	inputs = readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	assert.NotContains(t, inputs.problems[0].Error(), "secret-marker")
	assert.Contains(t, inputs.problems[0].Error(), "details suppressed to protect credentials")
}

func TestReadLocalInputsRejectsProfilingWithoutTLSOrBasicAuth(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource = appconfig.MetricSource{Kind: appconfig.MetricSourceInline}
	config.EnablePprof = true
	config.WebConfigFile = writeTestFile(t, "web.yml", "http_server_config:\n  http2: true\n")

	inputs := readLocalInputs(config, false)
	require.Len(t, inputs.problems, 1)
	problem := inputs.problems[0].(*appconfig.SettingError)
	assert.Equal(t, "EnablePprof", problem.Field)
	assert.NotContains(t, inputs.checked, "web config file: "+config.WebConfigFile)

	config.EnablePprof = false
	assert.Empty(t, readLocalInputs(config, false).problems)
}

func TestMetricReadReasonDoesNotRepeatTheSystemError(t *testing.T) {
	assert.Equal(t, "cannot read metric file: file does not exist", metricReadReason(fs.ErrNotExist))
	assert.Equal(t, "cannot read metric file: permission denied", metricReadReason(fs.ErrPermission))
	assert.Equal(t, "cannot read metric file", metricReadReason(os.ErrInvalid))
}
