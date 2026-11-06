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

package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// goFieldNames are names of the Go struct fields behind the settings. The
// commands print YAML paths and flag names, never these.
var goFieldNames = []string{
	"CollectIntervalMilliseconds", "CollectInterval", "XIDCountWindowSize", "ClockEventsCountWindowSize",
	"WatchGroups", "MetricSource", "FieldsFile", "KubernetesEnablePodLabels", "HPCJobMappingDir",
}

// locatedDiagnostic matches a diagnostic that starts with file:line:column.
var locatedDiagnostic = regexp.MustCompile(`\.yaml:\d+:\d+ `)

// requireNoGoFieldNames fails when output names an internal Go struct field.
func requireNoGoFieldNames(t testing.TB, output string) {
	t.Helper()
	for _, name := range goFieldNames {
		require.NotContainsf(t, output, name, "output leaks the internal name %s:\n%s", name, output)
	}
}

// runConfigValidateSucceeds checks validate on a good file in both formats.
func runConfigValidateSucceeds(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	run := runConfigCommand(t, nil, "validate", inspectionFixture)
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	require.Contains(t, run.Stdout, "valid")
	require.Contains(t, run.Stdout, "Not checked", "validate must say what it did not check")
	requireNoGoFieldNames(t, run.Stdout+run.Stderr)

	run = runConfigCommand(t, nil, "validate", inspectionFixture, "-o", "json")
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	var result validateResult
	require.NoError(t, json.Unmarshal(decodeEnvelope(t, run.Stdout, "config.validate"), &result))
	require.True(t, result.Valid)
	require.Empty(t, result.Diagnostics)
	require.NotEmpty(t, result.NotChecked, "the not-checked list must be present")
}

// runConfigValidateFailures checks that every invalid file exits 1, writes
// nothing to stdout, and names the file, line, column and YAML path.
func runConfigValidateFailures(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	cases := []struct {
		fixture  string
		yamlPath string
		summary  string
	}{
		{"unknown-top-level-key.yaml", "unknownSetting", "unknown key"},
		{"unsupported-version.yaml", "version", "must be 1 or 2"},
		{"version2-override-overlap.yaml", "metrics[0].overrides", "matches multiple overrides"},
		{"version2-override-matches-nothing.yaml", "metrics[0].overrides[0]", "matched no configured fields"},
		{"version2-match-invalid-regex.yaml", "metrics[0].overrides[0].match[0]", "invalid regular expression"},
		{"version2-match-group-item.yaml", "metrics[0].overrides[0].match[0]", "reserved for groups"},
		{"version2-override-invalid-name.yaml", "metrics[0].overrides[0].name", "may contain only letters"},
		{"version2-enrichment-conflict.yaml", "metrics[0].enrichment", "cannot both be true"},
	}
	for _, tc := range cases {
		t.Logf("validate %s", tc.fixture)
		path := configFixture(tc.fixture)

		run := runConfigCommand(t, nil, "validate", path)
		require.Equal(t, exitInvalid, run.ExitCode, "%s: stderr:\n%s", tc.fixture, run.Stderr)
		require.Empty(t, run.Stdout, "%s: stdout must stay empty when the file is invalid", tc.fixture)
		require.Regexp(t, locatedDiagnostic, run.Stderr, "%s: diagnostics must carry file:line:column", tc.fixture)
		require.Contains(t, run.Stderr, tc.fixture)
		require.Contains(t, run.Stderr, tc.yamlPath)
		require.Contains(t, run.Stderr, tc.summary)
		requireNoGoFieldNames(t, run.Stderr)

		run = runConfigCommand(t, nil, "validate", path, "-o", "json")
		require.Equal(t, exitInvalid, run.ExitCode, "%s: stderr:\n%s", tc.fixture, run.Stderr)
		var result validateResult
		require.NoError(t, json.Unmarshal(decodeEnvelope(t, run.Stdout, "config.validate"), &result))
		require.False(t, result.Valid)
		require.Len(t, result.Diagnostics, 1)
		diagnostic := result.Diagnostics[0]
		require.Equal(t, "error", diagnostic.Severity)
		require.Equal(t, tc.yamlPath, diagnostic.Source.Path)
		require.Contains(t, diagnostic.Source.File, tc.fixture)
		require.Positive(t, diagnostic.Source.Line)
		require.Positive(t, diagnostic.Source.Column)
		require.Contains(t, diagnostic.Summary, tc.summary)
		require.NotEmpty(t, result.NotChecked)
	}
}

// runConfigValidateSettingErrors checks failures that come from flags and
// environment variables and not from the file.
func runConfigValidateSettingErrors(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	run := runConfigCommand(t, nil, "validate", inspectionFixture, "--", "-x", "0")
	require.Equal(t, exitInvalid, run.ExitCode, "stderr:\n%s", run.Stderr)
	require.Empty(t, run.Stdout)
	require.Contains(t, run.Stderr, "--xid-count-window-size", "the message must name the flag")

	run = runConfigCommand(t, []string{"DCGM_EXPORTER_INTERVAL=fast"}, "validate", inspectionFixture)
	require.Equal(t, exitInvalid, run.ExitCode, "stderr:\n%s", run.Stderr)
	require.Empty(t, run.Stdout)
	require.Contains(t, run.Stderr, "DCGM_EXPORTER_INTERVAL", "the message must name the environment variable")

	run = runConfigCommand(t, nil, "validate", configFixture("does-not-exist.yaml"))
	require.Equal(t, exitInvalid, run.ExitCode, "stderr:\n%s", run.Stderr)
	require.Empty(t, run.Stdout)
	require.Contains(t, run.Stderr, "does-not-exist.yaml")
}

// runConfigUsageErrors checks that wrong command usage exits 2 with nothing on stdout.
func runConfigUsageErrors(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	for _, args := range [][]string{
		{"config", "bogus"},
		{"config", "validate", inspectionFixture, "-o", "xml"},
		{"config", "show", inspectionFixture, "-o", "text"},
		{"config", "explain", inspectionFixture, "-o", "yaml"},
		{"config", "validate", "-o", "json", inspectionFixture, "-o", "text"},
	} {
		run := runExporterCommand(t, configInspectionTimeout, nil, args...)
		require.Equal(t, exitUsageErr, run.ExitCode, "%v: stderr:\n%s", args, run.Stderr)
		require.Empty(t, run.Stdout, "%v: stdout must stay empty on a usage error", args)
		require.NotEmpty(t, run.Stderr, "%v: a usage error must say what is wrong", args)
	}
}

// runConfigVersion1Warns checks that a version 1 file is accepted with a deprecation warning.
func runConfigVersion1Warns(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	path := configFixture("version1-inline-fields.yaml")

	run := runConfigCommand(t, nil, "validate", path, "-o", "json")
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	var result validateResult
	require.NoError(t, json.Unmarshal(decodeEnvelope(t, run.Stdout, "config.validate"), &result))
	require.True(t, result.Valid)
	require.Len(t, result.Diagnostics, 1)
	require.Equal(t, "warning", result.Diagnostics[0].Severity)
	require.Equal(t, "version", result.Diagnostics[0].Source.Path)
	require.Contains(t, result.Diagnostics[0].Summary, "deprecated")

	run = runConfigCommand(t, nil, "show", path)
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	require.True(t, strings.HasPrefix(run.Stdout, "version: 2\n"), "show prints version 2 whatever the input version:\n%s", run.Stdout)
	require.Contains(t, run.Stdout, "DCGM_FI_DEV_GPU_TEMP_CELSIUS")
}

// runConfigShowRoundTrip checks that show output fed back to show prints the same text.
func runConfigShowRoundTrip(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	for _, format := range []string{"yaml", "json"} {
		first := runConfigCommand(t, nil, "show", inspectionFixture, "-o", format, "--", "-x", "120000")
		require.Equal(t, exitOK, first.ExitCode, "stderr:\n%s", first.Stderr)
		require.NotEmpty(t, first.Stdout)
		requireNoGoFieldNames(t, first.Stdout)
		require.Contains(t, first.Stdout, "2m", "the -x value must reach the output")
		if format == "json" {
			require.True(t, json.Valid([]byte(first.Stdout)), "show -o json is not JSON:\n%s", first.Stdout)
		}

		shown := filepath.Join(t.TempDir(), "shown."+format)
		require.NoError(t, os.WriteFile(shown, []byte(first.Stdout), 0o600))
		second := runConfigCommand(t, nil, "show", shown, "-o", format)
		require.Equal(t, exitOK, second.ExitCode, "stderr:\n%s", second.Stderr)
		require.Equal(t, first.Stdout, second.Stdout, "show output must round trip as %s", format)
	}
}

// runConfigExplainOutput checks explain in both formats.
func runConfigExplainOutput(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	run := runConfigCommand(t, nil, "explain", inspectionFixture)
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	require.Contains(t, run.Stdout, "Not checked")
	require.Contains(t, run.Stdout, "metrics[0]")
	requireNoGoFieldNames(t, run.Stdout+run.Stderr)

	run = runConfigCommand(t, nil, "explain", inspectionFixture, "-o", "json")
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	var data struct {
		Path       string   `json:"path"`
		NotChecked []string `json:"not_checked"`
	}
	require.NoError(t, json.Unmarshal(decodeEnvelope(t, run.Stdout, "config.explain"), &data))
	require.Equal(t, inspectionFixture, data.Path)
	require.NotEmpty(t, data.NotChecked)
	requireNoGoFieldNames(t, run.Stdout)
}

// runConfigWindowOverride checks that a window on an XID count family is accepted and shown.
func runConfigWindowOverride(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	path := configFixture("version2-window-on-xid-count.yaml")

	run := runConfigCommand(t, nil, "validate", path)
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)

	run = runConfigCommand(t, nil, "show", path)
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	require.Contains(t, run.Stdout, "window: 1m30s")
}

// precedenceSettingCase is one setting that a file, an environment variable
// and a flag can all set, with a different value from each.
type precedenceSettingCase struct {
	name string
	// path is the YAML path of the setting row explain prints.
	path string
	// envName and envValue are the environment variable and its value; flag and
	// flagValue are the flag and its value.
	envName, envValue string
	flag, flagValue   string
	// fileValue, envJSON and flagJSON are the values explain shows, as JSON.
	// envReplaced is how a lower flag shows the environment value it replaced:
	// the text as set, which for devices differs from the parsed list.
	fileValue, envJSON, flagJSON, envReplaced string
}

var precedenceSettingCases = []precedenceSettingCase{
	{
		name: "listen address", path: "outputs.prometheus.listen",
		envName: "DCGM_EXPORTER_LISTEN", envValue: ":9402", flag: "--address", flagValue: ":9403",
		fileValue: `":9401"`, envJSON: `":9402"`, flagJSON: `":9403"`, envReplaced: `":9402"`,
	},
	{
		name: "log format", path: "observability.logs.format",
		envName: "DCGM_EXPORTER_LOG_FORMAT", envValue: "text", flag: "--log-format", flagValue: "json",
		fileValue: `"json"`, envJSON: `"text"`, flagJSON: `"json"`, envReplaced: `"text"`,
	},
	{
		name: "device selection", path: "metrics[0].entities.gpu.gpus",
		envName: "DCGM_EXPORTER_DEVICES_STR", envValue: "g:1", flag: "--devices", flagValue: "g:2",
		fileValue: `[0]`, envJSON: `[1]`, flagJSON: `[2]`, envReplaced: `"g:1"`,
	},
}

// runExplainShowsPrecedenceForSettings shows that explain reports a flag over
// an environment variable over the file for the listen address, the log format
// and the device selection, with the replaced values in that order. It never
// starts DCGM.
func runExplainShowsPrecedenceForSettings(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	const fixture = "version2-precedence-settings.yaml"
	for _, tc := range precedenceSettingCases {
		t.Log(tc.name)
		env := []string{tc.envName + "=" + tc.envValue}

		file := explainSettings(t, fixture, nil)[tc.path]
		require.JSONEq(t, tc.fileValue, string(file.Value), "%s: the file value", tc.name)
		require.Equal(t, "file", file.Source.Kind)
		require.Empty(t, file.Replaced)

		fromEnv := explainSettings(t, fixture, env)[tc.path]
		require.JSONEq(t, tc.envJSON, string(fromEnv.Value), "%s: the environment value", tc.name)
		require.Equal(t, "env", fromEnv.Source.Kind)
		require.Equal(t, tc.envName, fromEnv.Source.Name)
		require.Len(t, fromEnv.Replaced, 1)
		require.Equal(t, "file", fromEnv.Replaced[0].Kind)
		require.JSONEq(t, tc.fileValue, string(fromEnv.Replaced[0].Value))

		fromFlag := explainSettings(t, fixture, env, "--", tc.flag, tc.flagValue)[tc.path]
		require.JSONEq(t, tc.flagJSON, string(fromFlag.Value), "%s: the flag value", tc.name)
		require.Equal(t, "flag", fromFlag.Source.Kind)
		require.Equal(t, tc.flag, fromFlag.Source.Name)
		require.Len(t, fromFlag.Replaced, 2)
		require.Equal(t, "env", fromFlag.Replaced[0].Kind)
		require.JSONEq(t, tc.envReplaced, string(fromFlag.Replaced[0].Value))
		require.Equal(t, "file", fromFlag.Replaced[1].Kind)
		require.JSONEq(t, tc.fileValue, string(fromFlag.Replaced[1].Value))
	}
}
