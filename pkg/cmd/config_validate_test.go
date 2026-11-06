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

package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// withInlineMetrics adds inline metric definitions and a Prometheus output to
// a version 2 body so a test does not depend on a metric file on the host.
func withInlineMetrics(body string) string {
	return "version: 2\nmetrics:\n  - name: gpu\n    fields:\n      - {name: DCGM_FI_DEV_GPU_TEMP, prometheus_type: gauge, help: Temperature.}\n" +
		body + "outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n"
}

// wantDiagnostic is what a test expects of one diagnostic.
type wantDiagnostic struct {
	severity string
	source   settingSource // File is replaced by the test file path when it is "FILE"
	summary  string        // must be contained in the summary
	fix      string        // must be contained in the fix when set
}

// validateDiagnosticCase is one config validate scenario: the file, the flags
// and the environment it runs with, and the diagnostics it must produce.
type validateDiagnosticCase struct {
	name string
	yaml string
	args []string
	env  map[string]string
	want []wantDiagnostic
}

// TestValidateDiagnostics checks each kind of problem is reported with its
// severity, its source and a message, and that the exit status follows the errors.
func TestValidateDiagnostics(t *testing.T) {
	for _, tc := range validateDiagnosticCases() {
		t.Run(tc.name, func(t *testing.T) {
			assertValidateDiagnostics(t, tc.yaml, tc.args, tc.env, tc.want)
		})
	}
}

// validateDiagnosticCases lists the scenarios TestValidateDiagnostics runs.
func validateDiagnosticCases() []validateDiagnosticCase {
	const web = "version: 2\nserver: {profiling: true}\n"

	return []validateDiagnosticCase{
		{
			name: "bad duration at a known line and a bad flag value",
			yaml: withInlineMetrics("    every: 0s\n"),
			args: []string{"--xid-count-window-size", "0"},
			want: []wantDiagnostic{
				{
					severity: "error", source: settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 6, Column: 12, Path: "metrics[0].every"},
					summary: "must be greater than 0",
				},
				{
					severity: "error", source: settingSource{Kind: appconfig.OriginFlag, Name: "--xid-count-window-size"},
					summary: "must be greater than 0, got 0",
				},
			},
		},
		{
			name: "window beyond a duration is named by its flag",
			yaml: "version: 2\n",
			args: []string{"--xid-count-window-size", "9223372036855"},
			want: []wantDiagnostic{{
				severity: "error", source: settingSource{Kind: appconfig.OriginFlag, Name: "--xid-count-window-size"},
				summary: "9223372036855 is more than 9223372036854 milliseconds",
			}},
		},
		{
			name: "value from the environment is named by its variable",
			yaml: "version: 2\n",
			env:  map[string]string{"DCGM_EXPORTER_CLOCK_EVENTS_COUNT_WINDOW_SIZE": "-1"},
			want: []wantDiagnostic{{
				severity: "error", source: settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_CLOCK_EVENTS_COUNT_WINDOW_SIZE"},
				summary: "must be greater than 0, got -1",
			}},
		},
		{
			name: "unknown key is reported at the key",
			yaml: "version: 2\nserver:\n  max_scrapes: 3\n",
			want: []wantDiagnostic{{
				severity: "error",
				source:   settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 3, Column: 3, Path: "server.max_scrapes"},
				summary:  "unknown key",
			}},
		},
		{
			name: "wrong type",
			yaml: "version: 2\nserver:\n  max_concurrent_scrapes: many\n",
			want: []wantDiagnostic{{
				severity: "error",
				source:   settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 3, Column: 27, Path: "server.max_concurrent_scrapes"},
				summary:  "cannot unmarshal",
			}},
		},
		{
			name: "unsupported version",
			yaml: "version: 99\n",
			want: []wantDiagnostic{{
				severity: "error",
				source:   settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 1, Column: 10, Path: "version"},
				summary:  "must be 1 or 2",
			}},
		},
		{
			name: "YAML syntax error",
			yaml: "version: 2\nmetrics: [\n",
			want: []wantDiagnostic{{severity: "error", source: settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 2}, summary: "yaml"}},
		},
		{
			name: "duplicate key",
			yaml: "version: 2\nserver: {read_timeout: 1s}\nserver: {write_timeout: 1s}\n",
			want: []wantDiagnostic{{severity: "error", source: settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 3, Column: 1}, summary: "duplicate YAML key"}},
		},
		{
			name: "output of an unsupported type",
			yaml: "version: 2\nmetrics:\n  - name: t\n    file: /a.csv\noutputs:\n  t: {type: otlp, schema: dcgm/v1}\n",
			want: []wantDiagnostic{{
				severity: "error",
				source:   settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 6, Column: 13, Path: "outputs.t.type"},
				summary:  "must be prometheus",
			}},
		},
		{
			name: "invalid watch group pattern",
			yaml: withInlineMetrics("    overrides:\n      - {match: ['['], every: 1s}\n"),
			want: []wantDiagnostic{{
				severity: "error", source: settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 7, Column: 18, Path: "metrics[0].overrides[0].match[0]"},
				summary: "invalid pattern",
			}},
		},
		{
			name: "version 1 file is a warning",
			yaml: version1Config,
			want: []wantDiagnostic{{
				severity: "warning",
				source:   settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 1, Column: 10, Path: "version"},
				summary:  "version 1 is deprecated",
			}},
		},
		{
			name: "count window longer than the retained history is a warning",
			yaml: "version: 2\nmetrics:\n  - name: gpu\n    fields:\n" +
				"      - {name: DCGM_EXP_XID_ERRORS_COUNT, prometheus_type: gauge, help: XID errors.}\n" +
				"    overrides:\n      - {match: [DCGM_EXP_XID_ERRORS_COUNT], window: 20m}\n" +
				"outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n",
			want: []wantDiagnostic{{
				severity: "warning",
				summary: "metrics[0].overrides[0].window: the 20m window is longer than --watch-max-keep-age (10m), " +
					"so DCGM_EXP_XID_ERRORS_COUNT can count only the last 10m",
			}},
		},
		{
			name: "deprecated flag is a warning that names the replacement, and a flag with no config key is not",
			yaml: "version: 2\n",
			args: []string{"--no-hostname", "--configmap-data", configMapSelector},
			want: []wantDiagnostic{
				{severity: "warning", source: settingSource{Kind: appconfig.OriginFlag, Name: "--configmap-data"}, summary: "mount the ConfigMap as a file"},
			},
		},
		{
			name: "deprecated flag from the environment",
			yaml: withInlineMetrics(""),
			env:  map[string]string{"DCGM_EXPORTER_CONFIGMAP_DATA": configMapSelector},
			want: []wantDiagnostic{{
				severity: "warning",
				source:   settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_CONFIGMAP_DATA"},
				summary:  "--configmap-data is deprecated",
			}},
		},
		{
			name: "profiling without a web config file",
			yaml: web,
			want: []wantDiagnostic{{
				severity: "error",
				source:   settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 2, Column: 21, Path: "server.profiling"},
				summary:  "profiling requires a web config file", fix: "server.web_config_file",
			}},
		},
		{
			name: "embedded log with a remote hostengine",
			yaml: "version: 2\nsources: {dcgm: {hostengine: 'h:1', embedded_log: {level: info}}}\n",
			want: []wantDiagnostic{{
				severity: "error", source: settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 2, Column: 37, Path: "sources.dcgm.embedded_log"},
				summary: "cannot be used with a remote hostengine",
			}},
		},
		{
			name: "invalid log format is reported without its value",
			yaml: "version: 2\n",
			args: []string{"--log-format", "bad-format-value"},
			want: []wantDiagnostic{{
				severity: "error", source: settingSource{Kind: appconfig.OriginFlag, Name: "--log-format"},
				summary: "invalid log-format parameter value",
			}},
		},
		{
			name: "invalid device selector and timeout together",
			yaml: "version: 2\n",
			args: []string{"--devices", "g:", "--web-read-timeout", "soon"},
			want: []wantDiagnostic{
				{severity: "error", source: settingSource{Kind: appconfig.OriginFlag, Name: "--devices"}},
				{severity: "error", source: settingSource{Kind: appconfig.OriginFlag, Name: "--web-read-timeout"}, summary: "parse web-read-timeout"},
			},
		},
		{
			name: "problems in the file and in a flag are both reported",
			yaml: "version: 2\nserver: {max_concurrent_scrapes: 0}\n",
			args: []string{"--max-concurrent-scrapes", "0"},
			want: []wantDiagnostic{
				{
					severity: "error", source: settingSource{Kind: appconfig.OriginFile, File: "FILE", Line: 2, Column: 34, Path: "server.max_concurrent_scrapes"},
					summary: "must be greater than 0",
				},
				{severity: "error", source: settingSource{Kind: appconfig.OriginFlag, Name: "--max-concurrent-scrapes"}, summary: "must be greater than 0"},
			},
		},
	}
}

// assertValidateDiagnostics runs config validate on yaml and checks the
// diagnostics, the valid flag and the exit status against want.
func assertValidateDiagnostics(t *testing.T, yaml string, args []string, env map[string]string, want []wantDiagnostic) {
	t.Helper()

	unsetFlagEnvVars(t, NewApp().Flags)
	for key, value := range env {
		t.Setenv(key, value)
	}

	path := writeConfigFile(t, yaml)
	data, err := validateJSON(t, args, path)

	require.Len(t, data.Diagnostics, len(want), "%+v", data.Diagnostics)
	wantErrors := 0
	for i, w := range want {
		got := data.Diagnostics[i]
		assert.Equal(t, w.severity, got.Severity)
		if w.severity == "error" {
			wantErrors++
		}

		wantSource := w.source
		if wantSource.File == "FILE" {
			wantSource.File = path
		}

		assert.Equal(t, wantSource, got.Source, "diagnostic %d: %+v", i, got)
		assert.Contains(t, got.Summary, w.summary)
		assert.Contains(t, got.Fix, w.fix)
	}

	assert.Equal(t, wantErrors == 0, data.Valid)
	if wantErrors == 0 {
		require.NoError(t, err)
		return
	}

	require.Error(t, err)
	assert.Equal(t, exitFailure, ExitStatus(err))
	assert.True(t, AlreadyReported(err))
}

// TestValidateTextOutputAndStreams checks the text report, and that a run with
// errors writes nothing to stdout while a valid run keeps warnings on stderr.
func TestValidateTextOutputAndStreams(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	bad := writeConfigFile(t, withInlineMetrics("    every: 0s\n"))
	stdout, stderr, err := inspectionRun(t, "--xid-count-window-size", "0", "config", "validate", bad)
	require.Error(t, err)
	assert.Equal(t, exitFailure, ExitStatus(err))
	assert.Empty(t, stdout)
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 5, stderr)
	assert.Equal(t, bad+": invalid (2 errors, 0 warnings)", lines[0])
	assert.Equal(t, "error    "+bad+":6:12 metrics[0].every: must be greater than 0", lines[1])
	assert.Equal(t, "error    --xid-count-window-size: must be greater than 0, got 0", lines[2])
	assert.True(t, strings.HasPrefix(lines[3], "Not checked (needs the running exporter): GPUs and MIG instances, DCGM connection"))
	assert.True(t, strings.HasPrefix(lines[4], "  "), "a long list wraps with an indent")

	valid := writeConfigFile(t, version1Config)
	stdout, stderr, err = inspectionRun(t, "config", "validate", valid)
	require.NoError(t, err)
	assert.Equal(t, "warning  "+valid+":1:10 version: "+appconfig.LegacyConfigVersionWarning+"\n", stderr)
	assert.Contains(t, stdout, valid+": valid (0 errors, 1 warning)\n")
	assert.Contains(t, stdout, "Checked: inline metric definitions\n")
	assert.Contains(t, stdout, "Not checked (needs the running exporter): ")
	assert.NotContains(t, stdout, "warning  ", "warning lines stay on stderr")
}

// TestValidateJSONListsWhatWasNotChecked checks a valid result still says what
// it did not check, and that an invalid run prints its JSON result on stdout.
func TestValidateJSONListsWhatWasNotChecked(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	data, err := validateJSON(t, nil, writeConfigFile(t, withInlineMetrics("")))
	require.NoError(t, err)
	assert.True(t, data.Valid)
	assert.Empty(t, data.Diagnostics)
	assert.Equal(t, []string{"gpus", "dcgm", "field_support", "kubelet_socket", "workload_labels", "listener", "directories"}, data.NotChecked)
	assert.Equal(t, []string{"configuration settings", "inline metric definitions"}, data.Checked)

	data, err = validateJSON(t, []string{"--configmap-data", configMapSelector}, writeConfigFile(t, "version: 2\n"))
	require.NoError(t, err)
	assert.Contains(t, data.NotChecked, "configmap_content", "a ConfigMap cannot be read offline")
	assert.NotContains(t, data.Checked, "inline metric definitions")

	_, err = validateJSON(t, []string{"--web-read-timeout", "0s"}, writeConfigFile(t, "version: 2\n"))
	require.Error(t, err)
}

// TestValidateWithoutAPathUsesTheStartupFileSelection checks validate picks
// the file like startup does: --config-file, then the environment, then none.
func TestValidateWithoutAPathUsesTheStartupFileSelection(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, "DCGM_FI_DEV_GPU_TEMP,gauge,Temperature\n")
	broken := writeConfigFile(t, "version: 2\nserver: {read_timeout: 0s}\n")

	data, err := validateJSON(t, []string{"--collectors", csv})
	require.NoError(t, err)
	assert.Empty(t, data.Path, "no file is selected without a flag or variable")

	data, err = validateJSON(t, []string{"--config-file", broken})
	require.Error(t, err)
	assert.Equal(t, broken, data.Path)

	t.Setenv("DCGM_EXPORTER_CONFIG_FILE", broken)
	data, err = validateJSON(t, nil)
	require.Error(t, err)
	assert.Equal(t, broken, data.Path)
	assert.Equal(t, "server.read_timeout", data.Diagnostics[0].Source.Path)

	t.Setenv("DCGM_EXPORTER_CONFIG_FILE", "")
	data, err = validateJSON(t, nil)
	require.Error(t, err, "an explicit empty selector is an error, not no file")
	assert.Contains(t, data.Diagnostics[0].Summary, "empty")
}

// TestValidateReadsTheLocalFilesTheConfigNames checks the metric file, the
// collections and the web policy are read and that errors name them.
func TestValidateReadsTheLocalFilesTheConfigNames(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	missing := filepath.Join(t.TempDir(), "missing.csv")
	policy := filepath.Join(t.TempDir(), "web.yaml")
	require.NoError(t, os.WriteFile(policy, []byte("http_server_config:\n  headers:\n    X-Frame-Options: deny\n"), 0o600))
	authPolicy := filepath.Join(t.TempDir(), "auth.yaml")
	authUsers := "basic_auth_users:\n  alice: $2y$10$CWCEbs7mt8QFrToJR9OcG.tduEKpx9SROhWnaDHQgQX4Q6hqWNASa\n"
	require.NoError(t, os.WriteFile(authPolicy, []byte(authUsers), 0o600))

	for _, tc := range []struct {
		name    string
		csv     string // empty means the file is missing
		extra   string // appended to the metrics entry
		args    []string
		want    string
		wantErr bool
		checked string // a prefix that one entry of the checked list must have
	}{
		{name: "valid rows", csv: "DCGM_FI_PROF_GR_ENGINE_ACTIVE,gauge,Activity\n", checked: "metric file: "},
		{name: "comment only", csv: "# comment only\n"},
		{name: "missing file", wantErr: true, want: missing + ": cannot read metric file: file does not exist"},
		{name: "unknown field", csv: "UNKNOWN,gauge,help\n", wantErr: true, want: "UNKNOWN"},
		{name: "bad type", csv: "DCGM_FI_DEV_GPU_TEMP,histogram,help\n", wantErr: true, want: "histogram"},
		{
			name:    "bad type after comments and blank lines",
			csv:     "# comment\n\nDCGM_FI_DEV_GPU_TEMP,gauge,T\n# comment\nDCGM_FI_DEV_FB_USED,histogram,help\n",
			wantErr: true, want: "line 5: unsupported Prometheus metric type",
		},
		{name: "short row", csv: "DCGM_FI_DEV_GPU_TEMP,gauge\n", wantErr: true, want: "malformed CSV record"},
		{name: "retired field", csv: "DCGM_FI_DEV_PCIE_TX_THROUGHPUT,gauge,help\n", wantErr: true, want: "DCGM_FI_PROF_PCIE_TX_BYTES"},
		{
			name: "override matches no field", csv: "DCGM_FI_DEV_POWER_USAGE,gauge,Power\n",
			extra:   "    overrides:\n      - {name: missing, match: [DCGM_FI_NOT_IN_CSV], every: 1s}\n",
			wantErr: true, want: `override "missing" (metrics[0].overrides[0]) matched no configured fields`,
		},
		{
			name: "unnamed override is named by its path", csv: "DCGM_FI_DEV_POWER_USAGE,gauge,Power\n",
			extra:   "    overrides:\n      - {match: [DCGM_FI_NOT_IN_CSV], every: 1s}\n",
			wantErr: true, want: `override "metrics[0].overrides[0]" matched no configured fields`,
		},
		{
			name: "two overrides match one field", csv: "DCGM_FI_DEV_POWER_USAGE,gauge,Power\n",
			extra: "    overrides:\n      - {name: fast-a, match: [\"DCGM_FI_DEV_POWER_*\"], every: 1s}\n" +
				"      - {name: fast-b, match: [DCGM_FI_DEV_POWER_USAGE], every: 2s}\n",
			wantErr: true, want: "matches multiple overrides: fast-a (metrics[0].overrides[0]) and fast-b (metrics[0].overrides[1])",
		},
		{
			name: "a window override needs no matching row", csv: "DCGM_FI_DEV_POWER_USAGE,gauge,Power\n",
			extra: "    overrides:\n      - {match: [DCGM_EXP_XID_ERRORS_COUNT], window: 1m}\n",
		},
		{
			name: "exporter counter selects the field it reads", csv: "DCGM_EXP_XID_ERRORS_COUNT,gauge,XID count\n",
			extra: "    overrides:\n      - {name: xid, match: [DCGM_FI_DEV_XID_ERROR], every: 1s}\n",
		},
		{
			name: "valid web config file", csv: "DCGM_FI_DEV_GPU_TEMP,gauge,T\n", args: []string{"--web-config-file", policy},
			checked: "web config file: ",
		},
		{
			name: "profiling needs TLS or basic auth", csv: "DCGM_FI_DEV_GPU_TEMP,gauge,T\n",
			args: []string{"--enable-pprof", "--web-config-file", policy}, wantErr: true,
			want: "profiling requires a web config file with TLS or basic auth",
		},
		{
			name: "profiling with basic auth", csv: "DCGM_FI_DEV_GPU_TEMP,gauge,T\n",
			args: []string{"--enable-pprof", "--web-config-file", authPolicy}, checked: "web config file: ",
		},
		{name: "wrong number of fields", csv: "DCGM_FI_DEV_GPU_TEMP,gauge,T\nDCGM_FI_DEV_FB_USED,gauge\n", wantErr: true, want: "line 2: wrong number of fields"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := missing
			if tc.csv != "" {
				file = inspectionCSV(t, tc.csv)
			}

			path := writeConfigFile(t, "version: 2\nmetrics:\n  - name: gpu\n    file: "+file+"\n"+tc.extra+
				"outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n")

			data, err := validateJSON(t, tc.args, path)
			if !tc.wantErr {
				require.NoError(t, err, "%+v", data.Diagnostics)
				if tc.checked != "" {
					assert.True(t, slices.ContainsFunc(data.Checked, func(item string) bool { return strings.HasPrefix(item, tc.checked) }),
						"%v lacks %q", data.Checked, tc.checked)
				}

				return
			}

			require.Error(t, err)
			require.NotEmpty(t, data.Diagnostics)
			assert.Contains(t, data.Diagnostics[0].Summary, tc.want)
			assert.Equal(t, exitFailure, ExitStatus(err))
		})
	}
}

// TestValidateHidesWebPolicyContents checks that a bad web policy's contents and
// parser errors never appear in the output, while the policy file's name does,
// and that read failures name only a category and not the system error text.
func TestValidateHidesWebPolicyContents(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := inspectionYAML(t, withInlineMetrics(""))
	policy := filepath.Join(t.TempDir(), "web.yaml")
	for _, text := range []string{
		"basic_auth_users:\n  user: [super-secret-value]\n",
		"tls_server_config:\n  cipher_suites: [super-secret-value]\n",
		"http_server_config:\n  headers:\n    X-Frame-Options: super-secret-value\n",
	} {
		require.NoError(t, os.WriteFile(policy, []byte(text), 0o600))
		stdout, stderr, err := inspectionRun(t, "--web-config-file", policy, "config", "validate", path)
		require.Error(t, err)
		assert.Empty(t, stdout)
		assert.NotContains(t, stderr+err.Error(), "super-secret-value")
		assert.Contains(t, stderr, policy)
		assert.Contains(t, stderr, "details suppressed to protect credentials")

		data, _ := validateJSON(t, []string{"--web-config-file", policy}, path)
		require.Len(t, data.Diagnostics, 1)
		assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--web-config-file"}, data.Diagnostics[0].Source)
		assert.NotContains(t, data.Diagnostics[0].Summary, "super-secret-value")
	}

	missing := filepath.Join(t.TempDir(), "missing.yaml")
	_, stderr, err := inspectionRun(t, "config", "validate", missing)
	require.Error(t, err)
	assert.Contains(t, stderr, "cannot read configuration file: file does not exist")
	assert.NotContains(t, stderr, "no such file", "the system error text is not repeated")
}

// TestValidateEscapesControlCharacters checks text output cannot carry raw
// control characters from the file name, a key or a value.
func TestValidateEscapesControlCharacters(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, "version: 2\n\"bad\\e[31mkey\\u202e\": 1\n")
	stdout, stderr, err := inspectionRun(t, "config", "validate", path)
	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "bad\\x1b[31mkey\\u202e")
	for _, r := range stderr {
		invisible := unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && r != '\n')
		assert.False(t, invisible, "raw control character %U in %q", r, stderr)
	}

	dir := filepath.Join(t.TempDir(), "dir\x1b[2J")
	require.NoError(t, os.Mkdir(dir, 0o700))
	file := filepath.Join(dir, "exporter.yaml")
	require.NoError(t, os.WriteFile(file, []byte("version: 2\nserver: {read_timeout: 0s}\n"), 0o600))
	_, stderr, err = inspectionRun(t, "config", "validate", file)
	require.Error(t, err)
	assert.Contains(t, stderr, `dir\x1b[2J`)
	assert.NotContains(t, stderr, "\x1b")
}

// TestValidateSaysWhenSettingProblemsHideTheLocalFiles checks the report does
// not imply the metric and web policy files were checked when they were not.
func TestValidateSaysWhenSettingProblemsHideTheLocalFiles(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	data, err := validateJSON(t, []string{"--web-read-timeout", "0s"}, writeConfigFile(t, withInlineMetrics("")))
	require.Error(t, err)
	assert.Contains(t, data.NotChecked, "local_files")
	assert.Empty(t, data.Checked)

	data, err = validateJSON(t, nil, writeConfigFile(t, withInlineMetrics("")))
	require.NoError(t, err)
	assert.NotContains(t, data.NotChecked, "local_files")
}

// TestValidateListsAnInputAsCheckedOnlyWhenItIsValid checks a metric file that
// is read but rejected is not in the checked list.
func TestValidateListsAnInputAsCheckedOnlyWhenItIsValid(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, "UNKNOWN,gauge,help\n")
	data, err := validateJSON(t, []string{"--collectors", csv}, writeConfigFile(t, "version: 2\n"))
	require.Error(t, err)
	assert.Equal(t, []string{"configuration settings"}, data.Checked)

	data, err = validateJSON(t, nil, writeConfigFile(t, withInlineMetrics("")+""))
	require.NoError(t, err)
	assert.Contains(t, data.Checked, "inline metric definitions")
}

// TestValidateBlamesTheWatchRetentionKeyThatIsWrong checks a bad sample count
// is attributed to the sample setting, not the age setting.
func TestValidateBlamesTheWatchRetentionKeyThatIsWrong(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	data, err := validateJSON(t, []string{"--watch-max-keep-samples", "-1"}, writeConfigFile(t, withInlineMetrics("")))
	require.Error(t, err)
	require.NotEmpty(t, data.Diagnostics)
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--watch-max-keep-samples"}, data.Diagnostics[0].Source)
}

// TestValidateReportsEveryTypeErrorOfAFile checks several type errors in one
// file each become a diagnostic, through the loader's wrapper.
func TestValidateReportsEveryTypeErrorOfAFile(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	data, err := validateJSON(t, nil, writeConfigFile(t, "version: 2\nserver:\n  max_concurrent_scrapes: many\n  read_timeout: [1]\n"))
	require.Error(t, err)
	require.Len(t, data.Diagnostics, 2, "%+v", data.Diagnostics)
	assert.Equal(t, "server.max_concurrent_scrapes", data.Diagnostics[0].Source.Path)
	assert.Equal(t, "server.read_timeout", data.Diagnostics[1].Source.Path)
}

// TestValidateReportsFileProblemsInPlainWords checks the wording and location of
// problems the config file reader finds.
func TestValidateReportsFileProblemsInPlainWords(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for name, tc := range map[string]struct {
		yaml, want, notWant string
	}{
		"unknown key without a repeated line": {
			withInlineMetrics("    bogus: 1\n"), ":6:5 metrics[0].bogus: unknown key\n", "at line",
		},
		"duration text": {
			withInlineMetrics("    every: fast\n"), `invalid duration "fast" (use a number with a unit, such as 30s)`, "time:",
		},
		"duration without a unit": {
			withInlineMetrics("    every: 5\n"), `invalid duration "5" (use a number with a unit, such as 30s)`, "time:",
		},
		"missing version":    {"metrics: []\n", "testdata-free:1:1 version: required; must be 1 or 2", ""},
		"bad listen address": {"version: 2\nmetrics:\n  - name: gpu\n    file: /x.csv\noutputs:\n  p: {type: prometheus, schema: dcgm/v1, listen: notanaddress}\n", "outputs.p.listen: invalid address parameter value", ""},
	} {
		t.Run(name, func(t *testing.T) {
			path := inspectionYAML(t, tc.yaml)
			_, stderr, err := inspectionRun(t, "config", "validate", path)
			require.Error(t, err)
			assert.Equal(t, exitFailure, ExitStatus(err))
			want := strings.Replace(tc.want, "testdata-free", path, 1)
			assert.Contains(t, stderr, want)
			if tc.notWant != "" {
				assert.NotContains(t, stderr, tc.notWant)
			}

			assert.Contains(t, stderr, "Only the first problem in the config file is reported")
		})
	}
}

// TestValidateRejectsKubernetesValuesTheReaderRejects checks a flag value that
// a saved config could not load back is a problem for validate and show.
func TestValidateRejectsKubernetesValuesTheReaderRejects(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for _, args := range [][]string{
		{"--kubernetes-gpu-id-type", "bogus"},
		{"--kubernetes-pod-label-allowlist-regex", "["},
	} {
		for _, command := range []string{"validate", "show"} {
			_, _, err := inspectionRun(t, append(append([]string{}, args...), "config", command)...)
			require.Error(t, err, "%s %v", command, args)
			assert.Equal(t, exitFailure, ExitStatus(err), "%s %v", command, args)
		}
	}
}

// TestValidateAcceptsTheShippedDefaultFiles checks that the shipped config
// file, with its metric file pointed at the shipped default-counters.csv,
// passes config validate with no errors or warnings and reads the metric file.
// The chart's default config is the same document (tests/static), so this also
// covers what a default chart install loads.
func TestValidateAcceptsTheShippedDefaultFiles(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	shipped, err := os.ReadFile(shippedDefaultConfig)
	require.NoError(t, err)
	counters, err := filepath.Abs("../../etc/default-counters.csv")
	require.NoError(t, err)

	const installedCounters = "/etc/dcgm-exporter/default-counters.csv"
	require.Equal(t, 1, strings.Count(string(shipped), installedCounters), "the shipped file names the installed metric file once")
	config := writeConfigFile(t, strings.Replace(string(shipped), installedCounters, counters, 1))

	data, err := validateJSON(t, nil, config)
	require.NoError(t, err, "diagnostics: %+v", data.Diagnostics)
	assert.True(t, data.Valid)
	assert.Empty(t, data.Diagnostics)
	assert.Contains(t, data.Checked, "metric file: "+counters)
}
