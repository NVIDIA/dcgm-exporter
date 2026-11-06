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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// explainCSVRows is a metric file with a spread of DCGM fields and an exporter counter.
const explainCSVRows = `DCGM_FI_DEV_SM_CLOCK,gauge,SM clock.
DCGM_FI_DEV_MEM_CLOCK,gauge,Memory clock.
DCGM_FI_DEV_GPU_TEMP,gauge,GPU temperature.
DCGM_FI_DEV_POWER_USAGE,gauge,Power.
DCGM_FI_DEV_POWER_MGMT_LIMIT,gauge,Power limit.
DCGM_FI_DEV_GPU_UTIL,gauge,GPU utilization.
DCGM_FI_DEV_FB_FREE,gauge,Free memory.
DCGM_FI_DEV_FB_USED,gauge,Used memory.
DCGM_EXP_XID_ERRORS_COUNT,gauge,XID count.
`

// explainFile writes a version 2 file that reads explainCSVRows, with extra
// lines added to its metrics entry.
func explainFile(t *testing.T, metricsExtra string) string {
	t.Helper()
	csv := inspectionCSV(t, explainCSVRows)
	return writeConfigFile(t, fmt.Sprintf("version: 2\nmetrics:\n  - name: gpu\n    file: %s\n%soutputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n",
		csv, metricsExtra))
}

// TestExplainShowsTheWinningSourceAndWhatItReplaced checks a flag that beats
// the file is the origin, the file value is listed as replaced, and nothing warns.
func TestExplainShowsTheWinningSourceAndWhatItReplaced(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    every: 10s\n")

	data := explainJSON(t, []string{"-c", "30000"}, path)
	every := settingByPath(t, data, "metrics[0].every")
	assert.Equal(t, "30s", every.Value)
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--collect-interval"}, every.Source)
	require.Len(t, every.Replaced, 1, "the file value that lost is listed")
	assert.Equal(t, appconfig.OriginFile, every.Replaced[0].Kind)
	assert.Equal(t, path, every.Replaced[0].File)
	assert.Equal(t, 5, every.Replaced[0].Line)
	assert.Equal(t, "metrics[0].every", every.Replaced[0].Path)
	assert.Equal(t, "10s", every.Replaced[0].Value)
	assert.Empty(t, data.Warnings, "a flag replacing a file value is not a conflict")

	_, stderr, err := inspectionRun(t, "-c", "30000", "config", "explain", path)
	require.NoError(t, err)
	assert.Empty(t, stderr)

	t.Setenv("DCGM_EXPORTER_INTERVAL", "20000")
	data = explainJSON(t, nil, path)
	every = settingByPath(t, data, "metrics[0].every")
	assert.Equal(t, settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"}, every.Source)
	assert.Equal(t, "20s", every.Value)
	require.Len(t, every.Replaced, 1)

	data = explainJSON(t, nil, path, "--", "--collect-interval", "5000")
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--collect-interval"}, settingByPath(t, data, "metrics[0].every").Source)
}

// TestExplainListsOnlySettingsThatDiffer checks the settings list holds only the settings away
// from the built-in default, with the file position of each, and that a flag
// naming the default value still shows when it replaced a file value.
func TestExplainListsOnlySettingsThatDiffer(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    every: 30s\n")
	data := explainJSON(t, nil, path)
	paths := []string{}
	for _, origin := range data.Settings {
		paths = append(paths, origin.Path)
	}

	assert.Equal(t, []string{
		"metrics[0].file", "metrics[0].enrichment.kubernetes", "metrics[0].enrichment.container",
		"metrics[0].enrichment.hpc_jobs",
	}, paths, "30s is the default; the file differs and a version 2 file turns the label sources on as auto")

	file := settingByPath(t, data, "metrics[0].file")
	assert.Equal(t, appconfig.OriginFile, file.Source.Kind)
	assert.Equal(t, 4, file.Source.Line)
	assert.Equal(t, "metrics[0].file", file.Source.Path)

	data = explainJSON(t, []string{"--collect-interval", "20000"}, explainFile(t, "    every: 30s\n"))
	assert.Equal(t, "20s", settingByPath(t, data, "metrics[0].every").Value)
	data = explainJSON(t, []string{"--collect-interval", "30000"}, explainFile(t, "    every: 10s\n"))
	every := settingByPath(t, data, "metrics[0].every")
	assert.Equal(t, "30s", every.Value, "equal to the default, but it replaced the file's 10s")
	assert.Len(t, every.Replaced, 1)
}

// TestExplainDefaultTextIsShort checks the default text names only the
// non-default settings and summarizes the fields, and --verbose adds the rest.
func TestExplainDefaultTextIsShort(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    every: 30s\n    overrides:\n      - {name: fast-power, match: [\"DCGM_FI_DEV_POWER_*\"], every: 1s}\n")

	short, _, err := inspectionRun(t, "config", "explain", path)
	require.NoError(t, err)
	assert.Contains(t, short, "Config file   "+path+"\n")
	assert.Contains(t, short, "Settings that differ from defaults\n")
	assert.Contains(t, short, "Fields   10 from ")
	assert.Contains(t, short, ", every 30s, retention 10m, no sample limit\n")
	assert.Contains(t, short, "  override fast-power  (2 fields)  every 1s, retention 10m, no sample limit\n")
	assert.NotContains(t, short, "overrides[0]", "the keys of an override are summarized under Fields, not listed")
	assert.NotContains(t, short, "match DCGM_FI_DEV_POWER_*", "match items are shown with --verbose")
	assert.Contains(t, short, "Not checked (needs the running exporter): GPUs and MIG instances")
	assert.Contains(t, short, "Use --verbose for all settings and all fields.\n")
	assert.NotContains(t, short, "DCGM_FI_DEV_SM_CLOCK", "fields are listed only with --verbose")
	assert.NotContains(t, short, "server.read_timeout", "default settings are listed only with --verbose")
	assert.Less(t, strings.Count(short, "\n"), 20)

	full, _, err := inspectionRun(t, "config", "explain", path, "--verbose")
	require.NoError(t, err)
	assert.Contains(t, full, "All settings\n")
	assert.Contains(t, full, "read_timeout")
	assert.Contains(t, full, "match DCGM_FI_DEV_POWER_* -> DCGM_FI_DEV_POWER_USAGE, DCGM_FI_DEV_POWER_MGMT_LIMIT\n")
	for _, field := range []string{"DCGM_FI_DEV_SM_CLOCK", "DCGM_FI_DEV_FB_USED", "DCGM_EXP_XID_ERRORS_COUNT"} {
		assert.Contains(t, full, field)
	}

	// Field rows are aligned in columns, so compare them with runs of spaces collapsed.
	squeezed := strings.Join(strings.Fields(full), " ")
	assert.Contains(t, squeezed, "DCGM_FI_DEV_POWER_USAGE gauge id 155 every 1s, retention 10m, no sample limit override fast-power")
	assert.Contains(t, squeezed, "DCGM_EXP_XID_ERRORS_COUNT gauge window 5m")
	assert.Contains(t, full, "    DCGM_FI_DEV_SM_CLOCK          gauge  id 100", "the type and id columns line up")
	assert.Contains(t, full, "  overrides[0].every", "verbose lists the keys of an override with their sources")
	assert.NotContains(t, full, "Use --verbose")
	assert.Greater(t, strings.Count(full, "\n"), 3*strings.Count(short, "\n"))
}

// TestExplainResolvesRegexMatchItems checks a /regex/ match item lists the
// fields it selects, the same way the exporter would assign them.
func TestExplainResolvesRegexMatchItems(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    overrides:\n      - {name: clocks, match: [\"/DCGM_FI_DEV_(SM|MEM)_CLOCK/\"], every: 1s}\n")

	full, _, err := inspectionRun(t, "config", "explain", path, "--verbose")
	require.NoError(t, err)
	assert.Contains(t, full, "match /DCGM_FI_DEV_(SM|MEM)_CLOCK/ -> DCGM_FI_DEV_SM_CLOCK, DCGM_FI_DEV_MEM_CLOCK\n")
}

// TestExplainShowsTheHPCSwitchAndItsFlagOverride checks the file's HPC job
// switch has its own row, and that a mapping directory flag takes it over.
func TestExplainShowsTheHPCSwitchAndItsFlagOverride(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    enrichment: {hpc_jobs: false}\n")

	text, _, err := inspectionRun(t, "config", "explain", path, "--verbose")
	require.NoError(t, err)
	assert.Regexp(t, `enrichment\.hpc_jobs +false +file:5\n`, text)
	assert.Regexp(t, `hpc_jobs\.mapping_dir +\S+ +default\n +inactive: HPC job labels are disabled\n`, text)

	data := explainJSON(t, nil, path, "--", "--hpc-job-mapping-dir", "/jobs")
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--hpc-job-mapping-dir"}, settingByPath(t, data, "metrics[0].enrichment.hpc_jobs").Source)
}

// TestExplainListsEveryCollectedField checks the fields with their timing and retention.
func TestExplainListsEveryCollectedField(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, `    every: 20s
    overrides:
      - name: fast-power
        match: ["DCGM_FI_DEV_POWER_*"]
        every: 1s
        sources: {dcgm: {watch: {max_keep_age: 1m}}}
      - {match: [DCGM_EXP_XID_ERRORS_COUNT], window: 2m}
`)

	data := explainJSON(t, []string{"--watch-max-keep-samples", "50"}, path)
	byName := map[string]inspectionField{}
	for _, field := range data.Fields {
		byName[field.Name] = field
	}

	require.Len(t, data.Fields, 10, "nine rows and the DCGM field behind the XID counter")

	temp := byName["DCGM_FI_DEV_GPU_TEMP"]
	assert.Equal(t, 150, temp.DCGMFieldID)
	assert.Equal(t, "gauge", temp.PrometheusType)
	assert.Equal(t, "20s", temp.Every)
	assert.Equal(t, &fieldRetention{MaxKeepAge: "10m", MaxKeepSamples: 50}, temp.Retention)
	assert.Empty(t, temp.Override)

	power := byName["DCGM_FI_DEV_POWER_USAGE"]
	assert.Equal(t, "1s", power.Every)
	assert.Equal(t, &fieldRetention{MaxKeepAge: "1m", MaxKeepSamples: 50}, power.Retention, "unset values inherit")
	assert.Equal(t, "fast-power", power.Override)
	assert.Equal(t, "fast-power", byName["DCGM_FI_DEV_POWER_MGMT_LIMIT"].Override)

	exporter := byName["DCGM_EXP_XID_ERRORS_COUNT"]
	assert.Zero(t, exporter.DCGMFieldID, "exporter counters have no DCGM field id")
	assert.Equal(t, "2m", exporter.Window)
	assert.Nil(t, exporter.Retention)
	assert.Contains(t, byName, "DCGM_FI_DEV_XID_ERROR", "the field the XID counter reads is collected")
}

// TestExplainWithoutAPathResolvesStartupDefaults checks explain picks its file
// like startup: --config-file, the environment, or no file at all.
func TestExplainWithoutAPathResolvesStartupDefaults(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, explainCSVRows)

	data := explainJSON(t, []string{"--collectors", csv})
	assert.Empty(t, data.Path)
	assert.Zero(t, data.InputVersion)
	assert.Equal(t, "metrics[0].file", settingByPath(t, data, "metrics[0].file").Path)
	text, _, err := inspectionRun(t, "--collectors", csv, "config", "explain")
	require.NoError(t, err)
	assert.Contains(t, text, "Config file   none (startup defaults)\n")

	selected := explainFile(t, "    every: 7s\n")
	data = explainJSON(t, []string{"--config-file", selected})
	assert.Equal(t, selected, data.Path)
	assert.Equal(t, "7s", settingByPath(t, data, "metrics[0].every").Value)

	t.Setenv("DCGM_EXPORTER_CONFIG_FILE", selected)
	data = explainJSON(t, nil)
	assert.Equal(t, selected, data.Path)

	missing := filepath.Join(t.TempDir(), "missing.csv")
	_, stderr, err := inspectionRun(t, "config", "explain", "--", "--collectors", missing)
	require.Error(t, err, "explain reads the metric file named by --collectors, so it must exist")
	assert.Contains(t, stderr, missing+": cannot read metric file: file does not exist")
}

// TestExplainReportsAVersion1File checks a version 1 input is explained with its notice and its own key paths.
func TestExplainReportsAVersion1File(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, version1Config)
	data := explainJSON(t, nil, path)
	assert.Equal(t, 1, data.InputVersion)
	require.Len(t, data.Warnings, 1)
	assert.Equal(t, appconfig.LegacyConfigVersionWarning, data.Warnings[0].Summary)
	assert.Equal(t, settingSource{Kind: appconfig.OriginFile, File: path, Line: 1, Column: 10, Path: "version"}, data.Warnings[0].Source)
	fields := settingByPath(t, data, "metrics[0].fields")
	assert.Equal(t, settingSource{Kind: appconfig.OriginFile, File: path, Line: 3, Column: 3, Path: "metrics.fields"}, fields.Source,
		"the source is the key in the file; the path is the version 2 key")

	assert.Equal(t, 2, data.Config.Version, "the config is the version 2 form")
}

// TestExplainConfigIsTheShowDocument checks explain and show describe the same document.
func TestExplainConfigIsTheShowDocument(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, explainCSVRows)
	text := strings.Replace(fullVersion2Config, "fields:\n      - {name: DCGM_FI_DEV_GPU_TEMP, prometheus_type: gauge, help: Temperature.}\n      - {name: DCGM_FI_DEV_POWER_USAGE, prometheus_type: gauge, help: Power.}",
		"file: "+csv, 1)

	path := writeConfigFile(t, text)
	// The full config turns profiling on, which needs basic auth or TLS.
	webPolicy := inspectionYAML(t, "basic_auth_users:\n  alice: $2y$10$CWCEbs7mt8QFrToJR9OcG.tduEKpx9SROhWnaDHQgQX4Q6hqWNASa\n")
	args := []string{"--web-config-file", webPolicy}
	shown, _, err := inspectionRun(t, append(args, "config", "show", path, "--output", "json")...)
	require.NoError(t, err)
	stdout, _, err := inspectionRun(t, append(args, "config", "explain", path, "--output", "json")...)
	require.NoError(t, err)
	var envelope struct {
		Data struct{ Config json.RawMessage }
	}

	require.NoError(t, json.Unmarshal([]byte(stdout), &envelope))
	assert.JSONEq(t, shown, string(envelope.Data.Config))
}

// TestExplainNamesSettingsOnlyAFlagCanSet checks a setting with no version 2
// key is listed under its flag.
func TestExplainNamesSettingsOnlyAFlagCanSet(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	data := explainJSON(t, []string{"--kubernetes", "--no-hostname"}, explainFile(t, ""))
	kubernetes := settingByPath(t, data, "metrics[0].enrichment.kubernetes")
	assert.Equal(t, "auto", kubernetes.Value)
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--kubernetes"}, kubernetes.Source)
	assert.Contains(t, flagOnlyByPath(t, data, "--no-hostname").Source.Name, "no-hostname")
	assert.NotContains(t, data.Settings, flagOnlyByPath(t, data, "--no-hostname"), "flags are listed apart from config file keys")
}

// TestExplainReportsAConfigMapAsNotRead checks a ConfigMap source lists no fields and says so.
func TestExplainReportsAConfigMapAsNotRead(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	root := []string{"--configmap-data", "metrics:definitions"}
	data := explainJSON(t, root, writeConfigFile(t, "version: 2\n"))
	assert.Empty(t, data.Fields)
	assert.Contains(t, data.NotChecked, "configmap_content")
	source := flagOnlyByPath(t, data, "--configmap-data")
	assert.Equal(t, "metrics:definitions", source.Value)
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--configmap-data"}, source.Source)

	text, _, err := inspectionRun(t, append(root, "config", "explain")...)
	require.NoError(t, err)
	assert.Contains(t, text, "Fields   not read: ConfigMap metrics:definitions\n")
	assert.Contains(t, text, "ConfigMap metric definitions")
}

// TestExplainListsOverridesWhenTheMetricsAreNotRead checks a ConfigMap source,
// whose fields are not read, still lists the overrides the file defines.
func TestExplainListsOverridesWhenTheMetricsAreNotRead(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, "version: 2\nmetrics:\n  - name: gpu\n    file: /etc/m.csv\n    overrides:\n"+
		"      - {name: fast, match: [\"DCGM_FI_DEV_POWER_*\"], every: 1s}\n"+
		"outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n")

	for _, extra := range [][]string{nil, {"--verbose"}} {
		text, _, err := inspectionRun(t, append([]string{"--configmap-data", "ns:name", "config", "explain", path}, extra...)...)
		require.NoError(t, err)
		assert.Contains(t, text, "Fields   not read: ConfigMap ns:name\n  override fast  every 1s, retention 10m, no sample limit\n")
		assert.NotContains(t, text, "no fields", "match items cannot resolve without the definitions")
	}
}

// TestExplainVerboseJSONListsEverySetting checks --verbose adds the settings
// that are at their defaults to the JSON settings, as it does to the text.
func TestExplainVerboseJSONListsEverySetting(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "")
	changed := explainJSON(t, nil, path)
	all := explainJSON(t, nil, path, "--verbose")
	assert.Greater(t, len(all.Settings), len(changed.Settings))
	assert.Equal(t, settingSource{Kind: appconfig.OriginDefault}, settingByPath(t, all, "server.read_timeout").Source)
	assert.Greater(t, len(all.FlagOnly), len(changed.FlagOnly))
}

// TestExplainNamesSettingsThatAreIgnored checks a setting the selected mode
// ignores says why, in the verbose view that lists every setting.
func TestExplainNamesSettingsThatAreIgnored(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for _, tc := range []struct {
		name string
		args []string
		path string
		want string
	}{
		{"socket without Kubernetes", nil, "sources.kubernetes.pod_resources_socket", "inactive: Kubernetes mode is disabled"},
		{
			"allowlist without pod labels",
			[]string{"--kubernetes", "--kubernetes-pod-label-allowlist-regex", "^a$"},
			"sources.kubernetes.pod_labels.allow", "inactive: Kubernetes pod labels are disabled",
		},
		{
			"dump directory while dumps are off",
			[]string{"--dump-directory", "/x"},
			"debug_dump.directory",
			"inactive: debug dumps are disabled",
		},
		{
			"listener under systemd",
			[]string{"--web-systemd-socket", "--address", ":9500"},
			"outputs.prometheus.listen",
			"inactive: systemd socket activation supplies listeners",
		},
		{
			"container labels under Kubernetes",
			[]string{"--kubernetes", "--container-labels"},
			"metrics[0].enrichment.container",
			"used if Kubernetes is unavailable at startup",
		},
		{
			"poll interval while bind watching is off", nil, "sources.dcgm.detect_bind_unbind.every",
			"inactive: GPU bind/unbind watching is disabled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.args) > 0 && tc.args[0] == "--"+CLIWebSystemdSocket && runtime.GOOS != "linux" {
				t.Skip("--web-systemd-socket is registered on Linux only")
			}

			path := writeConfigFile(t, "version: 2\n")
			args := []string{"--collectors", inspectionCSV(t, explainCSVRows), "config", "explain", path, "--verbose"}
			if len(tc.args) > 0 {
				args = append(append(args, "--"), tc.args...)
			}

			text, _, err := inspectionRun(t, args...)
			require.NoError(t, err)
			row := explainRowWithReason(text, tc.path)
			assert.Contains(t, row, tc.want, text)
		})
	}
}

// explainRowWithReason finds the text row of a setting, "  relative-path  value
// source", together with the line below it, which holds its inactive reason.
func explainRowWithReason(text, path string) string {
	// The row names the key without its section, such as "every" for metrics[0].every.
	relative := path
	if !strings.HasPrefix(path, "--") {
		if end := strings.Index(path, "]."); strings.HasPrefix(path, "metrics[0]") && end >= 0 {
			relative = path[end+2:]
		} else if dot := strings.IndexByte(path, '.'); dot >= 0 {
			relative = path[dot+1:]
		}
	}

	lines := strings.Split(text, "\n")
	var row string
	for i, candidate := range lines {
		if strings.HasPrefix(strings.TrimSpace(candidate), relative+" ") && i+1 < len(lines) {
			row = candidate + "\n" + lines[i+1]
		}
	}

	return row
}

// TestExplainNamesListenerInactiveUnderSystemd checks the JSON setting of the
// listener when systemd supplies the sockets.
func TestExplainNamesListenerInactiveUnderSystemd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("--web-systemd-socket is registered on Linux only")
	}

	unsetFlagEnvVars(t, NewApp().Flags)
	data := explainJSON(t, []string{"--collectors", inspectionCSV(t, explainCSVRows), "--web-systemd-socket", "--address", ":9500"},
		writeConfigFile(t, "version: 2\n"))

	assert.Equal(t, "systemd socket activation supplies listeners", settingByPath(t, data, "outputs.prometheus.listen").Inactive)
}

// TestExplainFailsWithoutOutputWhenTheSettingsAreInvalid checks problems go to
// stderr, stdout stays empty and the status is the invalid-configuration one.
func TestExplainFailsWithoutOutputWhenTheSettingsAreInvalid(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	missing := filepath.Join(t.TempDir(), "missing.csv")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"bad setting", []string{"--web-write-timeout", "0s", "config", "explain", explainFile(t, "")}, "--web-write-timeout"},
		{"missing metric file", []string{"config", "explain", "--", "--collectors", missing}, "cannot read metric file: file does not exist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := inspectionRun(t, tc.args...)
			require.Error(t, err)
			assert.Equal(t, exitFailure, ExitStatus(err))
			assert.True(t, AlreadyReported(err))
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.want)
		})
	}
}

// TestShowAndExplainSkipTheOverrideChecksAgainstTheMetricFile checks show and
// explain display an override that matches no field, while validate and startup
// report it.
func TestShowAndExplainSkipTheOverrideChecksAgainstTheMetricFile(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    overrides:\n      - {name: none, match: [DCGM_FI_NOPE], every: 1s}\n")
	for _, command := range []string{"show", "explain"} {
		_, stderr, err := inspectionRun(t, "config", command, path)
		require.NoError(t, err, "%s: %s", command, stderr)
	}

	_, _, err := inspectionRun(t, "config", "validate", path)
	require.Error(t, err)
	assert.Equal(t, exitFailure, ExitStatus(err))

	_, err = resolveFileAt(t, path)
	require.ErrorContains(t, err, `override "none" (metrics[0].overrides[0]) matched no configured fields`)
}

// TestExplainEscapesControlCharactersInValues checks a value typed by the user cannot move the cursor.
func TestExplainEscapesControlCharactersInValues(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "")
	text, _, err := inspectionRun(t, "config", "explain", path, "--", "--hpc-job-mapping-dir", "/jobs\x1b[2J\rx")
	require.NoError(t, err)
	assert.Contains(t, text, `/jobs\x1b[2J\rx`)
	assert.NotContains(t, text, "\x1b")
	assert.NotContains(t, text, "\r")
}

// TestExplainHandlesUnnamedWatchGroups checks an interval override with no name,
// which the reader names by its path, does not break show or explain and is
// summarized with that name.
func TestExplainHandlesUnnamedWatchGroups(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    overrides:\n      - {match: [\"DCGM_FI_DEV_POWER_*\"], every: 1s}\n      - {name: temps, match: [DCGM_FI_DEV_GPU_TEMP], every: 2s}\n")
	_, stderr, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err, stderr)
	text, _, err := inspectionRun(t, "config", "explain", path)
	require.NoError(t, err)
	assert.Contains(t, text, "  override metrics[0].overrides[0] (unnamed)  (2 fields)  every 1s")
	assert.Contains(t, text, "  override temps  (1 field)  every 2s")
	data := explainJSON(t, nil, path)
	assert.Equal(t, "metrics[0].overrides[0]", data.Fields[3].Override)

	// A user-chosen name that equals the generated form is a config error.
	clash := explainFile(t, "    overrides:\n      - {match: [\"DCGM_FI_DEV_POWER_*\"], every: 1s}\n      - {name: \"metrics[0].overrides[0]\", match: [DCGM_FI_DEV_GPU_TEMP], every: 2s}\n")
	_, _, err = inspectionRun(t, "config", "show", clash)
	require.ErrorContains(t, err, "may contain only letters, digits")
}

// TestExplainReportsAnInactivePollIntervalOfZero checks the setting the user
// gave is shown, not the loadable value the document writes instead.
func TestExplainReportsAnInactivePollIntervalOfZero(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	root := []string{"--gpu-bind-unbind-poll-interval", "0s"}
	data := explainJSON(t, root, explainFile(t, ""))
	assert.Equal(t, "0s", settingByPath(t, data, "sources.dcgm.detect_bind_unbind.every").Value)
	_, stderr, err := inspectionRun(t, append(root, "config", "show")...)
	require.NoError(t, err)
	assert.Contains(t, stderr, "every is 0s, but written as 1s")
	assert.NotContains(t, stderr, "left out of the output")
}

// TestExplainKeepsGroupsWithTheSameNameApart checks two groups that end up with
// the same name are summarized separately, each with its own fields and retention.
func TestExplainKeepsGroupsWithTheSameNameApart(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    overrides:\n"+
		"      - {match: [\"DCGM_FI_DEV_POWER_*\"], every: 1s, sources: {dcgm: {watch: {max_keep_age: 1m}}}}\n"+
		"      - {match: [DCGM_FI_DEV_GPU_TEMP], every: 2s}\n")

	text, _, err := inspectionRun(t, "config", "explain", path)
	require.NoError(t, err)
	assert.Contains(t, text, "(2 fields)  every 1s, retention 1m, no sample limit")
	assert.Contains(t, text, "(1 field)  every 2s, retention 10m, no sample limit")
}

// TestExplainShortensReplacedInlineDefinitions checks inline metric definitions
// that a flag replaced are counted, not printed in full.
func TestExplainShortensReplacedInlineDefinitions(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, explainCSVRows)
	text, _, err := inspectionRun(t, "--collectors", csv, "config", "explain", writeConfigFile(t, withInlineMetrics("")))
	require.NoError(t, err)
	assert.Contains(t, text, "replaces file:4 (metrics[0].fields) value: 1 inline definition")
	assert.NotContains(t, text, "Temperature.")
}

// TestExplainListsEveryReplacedValueInPrecedenceOrder checks the lower inputs a
// winning value replaced: an environment variable (raw text) and the file.
func TestExplainListsEveryReplacedValueInPrecedenceOrder(t *testing.T) {
	file := explainFile(t, "    every: 10s\n")
	noFile := explainFile(t, "")
	flag := []string{"--collect-interval", "5000"}
	env := map[string]string{"DCGM_EXPORTER_INTERVAL": "20000"}
	for _, tc := range []struct {
		name     string
		root     []string
		env      map[string]string
		path     string
		want     settingSource
		replaced []replacedValue
	}{
		{
			name: "flag over environment", root: flag, env: env, path: noFile,
			want:     settingSource{Kind: appconfig.OriginFlag, Name: "--collect-interval"},
			replaced: []replacedValue{{settingSource: settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"}, Value: "20s", Raw: "20000"}},
		},
		{
			name: "flag with no environment variable", root: flag, path: noFile,
			want: settingSource{Kind: appconfig.OriginFlag, Name: "--collect-interval"},
		},
		{
			name: "environment alone", env: env, path: noFile,
			want: settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"},
		},
		{
			name: "flag over environment over file", root: flag, env: env, path: file,
			want: settingSource{Kind: appconfig.OriginFlag, Name: "--collect-interval"},
			replaced: []replacedValue{
				{settingSource: settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"}, Value: "20s", Raw: "20000"},
				{settingSource: settingSource{Kind: appconfig.OriginFile, File: file, Line: 5, Column: 12, Path: "metrics[0].every"}, Value: "10s"},
			},
		},
		{
			name: "environment over file", env: env, path: file,
			want:     settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"},
			replaced: []replacedValue{{settingSource: settingSource{Kind: appconfig.OriginFile, File: file, Line: 5, Column: 12, Path: "metrics[0].every"}, Value: "10s"}},
		},
		{
			name: "empty environment variable is not a value", root: flag, env: map[string]string{"DCGM_EXPORTER_INTERVAL": ""}, path: noFile,
			want: settingSource{Kind: appconfig.OriginFlag, Name: "--collect-interval"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			data := explainJSON(t, tc.root, tc.path)
			every := settingByPath(t, data, "metrics[0].every")
			assert.Equal(t, tc.want, every.Source)
			assert.Equal(t, tc.replaced, every.Replaced)
		})
	}
}

// TestExplainOriginsPointAtTheValueThatSetThem checks a path no input set is a
// default, even though a switch in the file cleared it, and that a key set
// through another key is named.
func TestExplainOriginsPointAtTheValueThatSetThem(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	data := explainJSON(t, []string{"--collectors", inspectionCSV(t, explainCSVRows)},
		writeConfigFile(t, "version: 2\nmetrics:\n  - {name: gpu, file: /a.csv, enrichment: {hpc_jobs: false}}\noutputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n"))

	for _, origin := range data.Settings {
		assert.NotEqual(t, "sources.hpc_jobs.mapping_dir", origin.Path, "no input set the directory, so it is at its default")
	}

	text, _, err := inspectionRun(t, "config", "explain", "--verbose", writeConfigFile(t, version1Config), "--", "--collectors", inspectionCSV(t, explainCSVRows))
	require.NoError(t, err)
	assert.Contains(t, text, "replaces file:3 (metrics.fields)", "the replaced value is from the key the version 1 file used")
}

// TestExplainShowsEmbeddedLogKeysOnlyWhileTheLogIsOn checks there is no row for
// the embedded log while its block is left out, and lowercase level and file
// rows when it is on.
func TestExplainShowsEmbeddedLogKeysOnlyWhileTheLogIsOn(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, explainCSVRows)
	off, _, err := inspectionRun(t, "--collectors", csv, "config", "explain", "--verbose")
	require.NoError(t, err)
	assert.NotContains(t, off, "embedded_log")
	assert.NotContains(t, off, "NONE")
	on, _, err := inspectionRun(t, "--collectors", csv, "--enable-dcgm-log", "--dcgm-log-level", "WARN", "config", "explain", "--verbose")
	require.NoError(t, err)
	assert.Regexp(t, `dcgm\.embedded_log\.file\s+stdout\s+flag --enable-dcgm-log`, on)
	assert.Regexp(t, `dcgm\.embedded_log\.level\s+warning\s+flag --dcgm-log-level`, on)
}

// TestExplainListsOneRowPerKey checks an object setting is listed by its keys,
// not as inline JSON, and that flags are listed apart from config file keys.
func TestExplainListsOneRowPerKey(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	text, _, err := inspectionRun(t, "--collectors", inspectionCSV(t, explainCSVRows), "--no-hostname", "config", "explain", "--verbose")
	require.NoError(t, err)
	assert.NotContains(t, text, `{"`, "no object is printed as inline JSON")
	assert.Regexp(t, `entities\.gpu\.policy\s+flex\s+default`, text)
	assert.Regexp(t, `entities\.switch\.indices\s+all\s+default`, text)
	keyed, flagSection, found := strings.Cut(text, "Flag-only settings")
	require.True(t, found)
	assert.Contains(t, flagSection, "--no-hostname")
	assert.NotContains(t, keyed, "--no-hostname")
	// The field table below the settings is not wrapped: its columns follow the field names.
	settings, _, _ := strings.Cut(text, "\nFields ")
	for _, line := range strings.Split(settings, "\n") {
		if !strings.Contains(line, os.TempDir()) { // a temporary file name is long
			assert.LessOrEqual(t, len([]rune(line)), 100, line)
		}
	}
}

// TestExplainShowsTheCountWindowsEvenAtTheirDefault checks explain lists both
// effective counter windows although show leaves them out.
func TestExplainShowsTheCountWindowsEvenAtTheirDefault(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	text, _, err := inspectionRun(t, "--collectors", inspectionCSV(t, explainCSVRows), "config", "explain", "--verbose")
	require.NoError(t, err)
	assert.Regexp(t, `overrides\[DCGM_EXP_XID_ERRORS_COUNT\]\.window\s+5m\s+default`, text)
	assert.Regexp(t, `overrides\[DCGM_EXP_CLOCK_EVENTS_COUNT\]\.window\s+5m\s+default`, text)
}

// TestExplainResolvesEveryMatchItem checks each match item of an override is
// listed with the fields it selected, in text and in JSON.
func TestExplainResolvesEveryMatchItem(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    overrides:\n      - {name: hot, match: [\"DCGM_FI_DEV_POWER_*\", DCGM_FI_DEV_GPU_TEMP, DCGM_FI_NOPE], every: 1s}\n")
	data := explainJSON(t, nil, path)
	require.Len(t, data.Overrides, 1)
	assert.Equal(t, "hot", data.Overrides[0].Name)
	assert.Equal(t, []matchResolved{
		{Item: "DCGM_FI_DEV_POWER_*", Fields: []string{"DCGM_FI_DEV_POWER_USAGE", "DCGM_FI_DEV_POWER_MGMT_LIMIT"}},
		{Item: "DCGM_FI_DEV_GPU_TEMP", Fields: []string{"DCGM_FI_DEV_GPU_TEMP"}},
		{Item: "DCGM_FI_NOPE", Fields: []string{}},
	}, data.Overrides[0].Match)
}

// TestExplainKeepsTheCountWindowsApart checks that when only the clock event
// window is changed, the XID window is still reported as its own default.
func TestExplainKeepsTheCountWindowsApart(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	text, _, err := inspectionRun(t, "--collectors", inspectionCSV(t, explainCSVRows), "--clock-events-count-window-size", "60000", "config", "explain", "--verbose")
	require.NoError(t, err)
	assert.Regexp(t, `overrides\[DCGM_EXP_XID_ERRORS_COUNT\]\.window\s+5m\s+default`, text)
	assert.Regexp(t, `overrides\[DCGM_EXP_CLOCK_EVENTS_COUNT\]\.window\s+1m\s+flag --clock-events-count-window-size`, text)
	assert.NotContains(t, text, "overrides[0].window", "a window has one name whether or not it is at its default")
}

// TestExplainKeepsAnEmptyDirectoryFlag checks a flag that clears the HPC mapping
// directory is shown with the file value it replaced.
func TestExplainKeepsAnEmptyDirectoryFlag(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    enrichment: {hpc_jobs: auto}\n")
	data := explainJSON(t, []string{"--hpc-job-mapping-dir", ""}, writeConfigFile(t, mustRead(t, path)+"sources: {hpc_jobs: {mapping_dir: /jobs}}\n"))
	origin := settingByPath(t, data, "sources.hpc_jobs.mapping_dir")
	assert.Equal(t, settingSource{Kind: appconfig.OriginFlag, Name: "--hpc-job-mapping-dir"}, origin.Source)
	require.Len(t, origin.Replaced, 1)
	assert.Equal(t, "/jobs", origin.Replaced[0].Value)
}

// mustRead returns the text of a file.
func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// TestExplainNotesHowEachSwitchIsTreated checks a required source says the exporter
// waits at startup and an auto source says it is checked by the running exporter.
// container switch matches what startup does with Kubernetes and container labels.
func TestExplainNotesHowEachSwitchIsTreated(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    enrichment: {kubernetes: false, container: true, dra: false, hpc_jobs: auto}\n")
	text, _, err := inspectionRun(t, "config", "explain", path, "--verbose")
	require.NoError(t, err)
	assert.Contains(t, text, "note: required: the exporter waits up to 1m at startup")
	assert.Contains(t, text, "note: auto: used when available (checked by the running exporter)")
}

// TestExplainContainerLabelsFollowTheRuntimeRules checks the reason given for the
// container switch matches what startup does with Kubernetes and container labels.
func TestExplainContainerLabelsFollowTheRuntimeRules(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for name, tc := range map[string]struct {
		enrichment string
		want       string
		notWant    string
	}{
		"required container turns auto Kubernetes off": {
			"{container: true}", "Kubernetes mode is off because metrics[0].enrichment.container is true", "ignored in Kubernetes mode",
		},
		"both auto": {
			"{}", "used if Kubernetes is unavailable at startup", "inactive: container labels",
		},
		"required Kubernetes": {
			"{kubernetes: true, container: auto}", "container labels are ignored in Kubernetes mode", "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := explainFile(t, "    enrichment: "+tc.enrichment+"\n")
			text, _, err := inspectionRun(t, "config", "explain", path, "--verbose")
			require.NoError(t, err)
			assert.Contains(t, text, tc.want)
			if tc.notWant != "" {
				assert.NotContains(t, text, tc.notWant)
			}
		})
	}
}

// TestExplainShowsKubernetesInactiveWhenFlagOrEnvironmentTurnedItOn checks that
// a Kubernetes source turned on by a flag or an environment variable is shown as
// inactive when the file requires container labels, because startup turns it
// off then.
func TestExplainShowsKubernetesInactiveWhenFlagOrEnvironmentTurnedItOn(t *testing.T) {
	const wantInactive = "Kubernetes mode is off because metrics[0].enrichment.container is true"
	path := explainFile(t, "    enrichment: {kubernetes: auto, container: true, dra: false, hpc_jobs: false}\n")
	for name, setup := range map[string]struct {
		env  string
		args []string
	}{
		"flag":        {args: []string{"--", "--kubernetes"}},
		"environment": {env: "true"},
		"file only":   {},
	} {
		t.Run(name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			if setup.env != "" {
				t.Setenv("DCGM_EXPORTER_KUBERNETES", setup.env)
			}

			data := explainJSON(t, nil, append([]string{path}, setup.args...)...)
			assert.Equal(t, wantInactive, settingByPath(t, data, "metrics[0].enrichment.kubernetes").Inactive)
		})
	}
}

// TestExplainShowsTheWatchGroupNameTheFileGaveAndTheNameShowWrites checks
// that a version 1 watch group name an override cannot carry is listed as the
// file wrote it, with a note naming the name config show writes, in both the
// default and the verbose text.
func TestExplainShowsTheWatchGroupNameTheFileGaveAndTheNameShowWrites(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, `version: 1
metrics:
  fields:
    - {name: DCGM_FI_DEV_POWER_USAGE, prometheusType: gauge, help: Power.}
collection:
  interval: 30s
  watchGroups:
    - {name: "fast power", interval: 2s, fields: [DCGM_FI_DEV_POWER_USAGE]}
`)
	for _, args := range [][]string{{"config", "explain", path}, {"config", "explain", "--verbose", path}} {
		text, _, err := inspectionRun(t, args...)
		require.NoError(t, err)
		assert.Regexp(t, `overrides\[0\]\.name\s+fast power\s+file:8`, text, args)
		assert.Contains(t, text, `note: written as "fast-power"`, args)
		assert.NotRegexp(t, `overrides\[0\]\.name\s+fast-power`, text, args)
	}
}
