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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// The types below decode the JSON that the config commands print. They are
// kept apart from the code that writes it, so the tests check the output
// contract itself.

// explainData is the data of a config explain result.
type explainData struct {
	Path         string                `json:"path"`
	InputVersion int                   `json:"input_version"`
	Config       *appconfig.YAMLConfig `json:"config"`
	Settings     []settingRow          `json:"settings"`
	FlagOnly     []settingRow          `json:"flag_only"`
	Overrides    []inspectionOverride  `json:"overrides"`
	Fields       []inspectionField     `json:"fields"`
	Warnings     []diagnostic          `json:"warnings"`
	NotChecked   []string              `json:"not_checked"`
}

// settingRow is where one setting's value came from and which lower
// layers it replaced.
type settingRow struct {
	Path     string          `json:"path"`
	Value    any             `json:"value"`
	Source   settingSource   `json:"source"`
	Replaced []replacedValue `json:"replaced"`
	Inactive string          `json:"inactive"`
	Note     string          `json:"note"`
}

// replacedValue is a value that lost to a higher layer.
type replacedValue struct {
	settingSource
	Value any    `json:"value"`
	Raw   string `json:"raw"`
}

// settingSource is where a setting's value, or a problem with it, came from.
type settingSource struct {
	Kind   appconfig.OriginKind `json:"kind"`
	Name   string               `json:"name"`
	File   string               `json:"file"`
	Line   int                  `json:"line"`
	Column int                  `json:"column"`
	Path   string               `json:"path"`
}

// inspectionOverride is one metrics override with what each match item selected.
type inspectionOverride struct {
	Name      string          `json:"name"`
	Every     string          `json:"every"`
	Retention *fieldRetention `json:"retention"`
	Match     []matchResolved `json:"match"`
}

// matchResolved is one match item of an override and the fields it selected.
type matchResolved struct {
	Item   string   `json:"item"`
	Fields []string `json:"fields"`
}

// inspectionField is one field the exporter would collect.
type inspectionField struct {
	Name           string          `json:"name"`
	DCGMFieldID    int             `json:"dcgm_field_id"`
	PrometheusType string          `json:"prometheus_type"`
	Every          string          `json:"every"`
	Retention      *fieldRetention `json:"retention"`
	Window         string          `json:"window"`
	Override       string          `json:"override"`
}

// fieldRetention is how much history DCGM keeps for a field.
type fieldRetention struct {
	MaxKeepAge     string `json:"max_keep_age"`
	MaxKeepSamples int64  `json:"max_keep_samples"`
}

// diagnostic is one problem or warning found in the configuration.
type diagnostic struct {
	Severity string        `json:"severity"`
	Source   settingSource `json:"source"`
	Summary  string        `json:"summary"`
	Fix      string        `json:"fix"`
}

// validateData is the data of a config validate result.
type validateData struct {
	Path        string       `json:"path"`
	Valid       bool         `json:"valid"`
	Diagnostics []diagnostic `json:"diagnostics"`
	Checked     []string     `json:"checked"`
	NotChecked  []string     `json:"not_checked"`
}

// inspectionRun runs the real CLI parser with a daemon-action tripwire.
func inspectionRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := NewApp("test")
	app.Writer, app.ErrWriter = &stdout, &stderr
	app.Action = func(*cli.Context) error {
		t.Fatal("inspection reached daemon action")
		return nil
	}
	err := app.Run(append([]string{"dcgm-exporter"}, args...))
	return stdout.String(), stderr.String(), err
}

// inspectionYAML writes a temporary configuration for command tests.
func inspectionYAML(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "exporter.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// inspectionCSV writes a metric definition file with the given rows.
func inspectionCSV(t *testing.T, rows string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metrics.csv")
	require.NoError(t, os.WriteFile(path, []byte(rows), 0o600))
	return path
}

// inspectionEnvelopeOf decodes the versioned result and fails on a wrong envelope.
func inspectionEnvelopeOf(t *testing.T, stdout, command string) json.RawMessage {
	t.Helper()
	var envelope struct {
		FormatVersion int             `json:"format_version"`
		Command       string          `json:"command"`
		Data          json.RawMessage `json:"data"`
	}

	require.NoError(t, json.Unmarshal([]byte(stdout), &envelope), stdout)
	require.Equal(t, 1, envelope.FormatVersion)
	require.Equal(t, command, envelope.Command)
	return envelope.Data
}

// explainJSON runs config explain with JSON output and decodes its data. The
// root arguments are the global options that go before the command.
func explainJSON(t *testing.T, rootArgs []string, args ...string) explainData {
	t.Helper()
	full := append(append([]string{}, rootArgs...), "config", "explain", "--output", "json")
	stdout, stderr, err := inspectionRun(t, append(full, args...)...)
	require.NoError(t, err, stderr)
	var data explainData
	require.NoError(t, json.Unmarshal(inspectionEnvelopeOf(t, stdout, "config.explain"), &data))
	return data
}

// validateJSON runs config validate with JSON output and decodes its data. The
// run may fail; the result is printed either way. The root arguments are the
// global options that go before the command.
func validateJSON(t *testing.T, rootArgs []string, args ...string) (validateData, error) {
	t.Helper()
	full := append(append([]string{}, rootArgs...), "config", "validate", "--output", "json")
	stdout, _, err := inspectionRun(t, append(full, args...)...)
	var data validateData
	require.NoError(t, json.Unmarshal(inspectionEnvelopeOf(t, stdout, "config.validate"), &data))
	return data, err
}

// flagOnlyByPath finds a setting that only a flag can set, by its flag name.
func flagOnlyByPath(t *testing.T, data explainData, path string) settingRow {
	t.Helper()
	for _, origin := range data.FlagOnly {
		if origin.Path == path {
			return origin
		}
	}

	t.Fatalf("no flag-only setting %s in %+v", path, data.FlagOnly)
	return settingRow{}
}

// settingByPath finds the setting row for a key and fails if the view omits it.
func settingByPath(t *testing.T, data explainData, path string) settingRow {
	t.Helper()
	for _, origin := range data.Settings {
		if origin.Path == path {
			return origin
		}
	}

	t.Fatalf("no setting %s in %+v", path, data.Settings)
	return settingRow{}
}

// TestInspectionCommandGrammar checks operand, output and override-tail admission.
func TestInspectionCommandGrammar(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, "DCGM_FI_DEV_GPU_TEMP,gauge,Temperature\n")
	path := inspectionYAML(t, "version: 2\nmetrics:\n  - name: gpu\n    file: "+csv+"\noutputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n")
	for _, args := range [][]string{
		{"config", "show"},
		{"config", "show", "--output", "json"},
		{"config", "show", "-o", "yaml"},
		{"config", "show", path, "--output", "json"},
		{"config", "show", "--output=json", path},
		{"config", "show", "--", "--debug=false"},
		{"config", "show", "--help=false", "--", "--debug=false"},
		{"config", "show", "-h=false", "--", "--debug=false"},
		{"config", "show", "-output", "json", "--", "--debug=false"},
		{"config", "show", "-output=json", "--", "--debug=false"},
		{"--address", ":9501", "config", "show", path, "--", "--address", ":9502"},
		{"config", "validate", path},
		{"--config-file", path, "config", "validate"},
		{"config", "validate", "-o", "json", path},
		{"config", "explain", path, "--verbose"},
		{"config", "explain", "--verbose", path},
		{"config", "explain", "--", "--collectors", csv},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, err := inspectionRun(t, args...)
			require.NoError(t, err, stderr)
			require.NotEmpty(t, stdout)
		})
	}

	for _, args := range [][]string{
		{"config", "unknown"},
		{"config", "show", path, "extra"},
		{"config", "show", "--", path},
		{"config", "show", "--help=false", "--", path},
		{"config", "show", "-h=false", "--", path},
		{"config", "show", "-output", "json", "--", path},
		{"config", "show", "-output=json", "--", path},
		{"config", "validate", "--help=false", "--", path},
		{"config", "explain", "--help=false", "--", path},
		{"config", "show", "--output", "text"},
		{"config", "validate", "--output", "yaml", path},
		{"config", "explain", "--output", "yaml", path},
		{"config", "show", "--output", "json", "--output", "yaml"},
		{"config", "show", path, "--output", "json", "--output", "yaml"},
		{"config", "show", "--output", "json", path, "--output", "json"},
		{"config", "show", "--verbose"},
		{"config", "validate", "--verbose", path},
		{"config", "explain", "--verbose=bad", path},
		{"config", "explain", path, "--", "--verbose"},
		{"config", "show", path, "--", "--config-file", path},
		{"config", "show", path, "--", "--help"},
		{"config", "show", path, "--", "--version"},
		{"config", "show", path, "--", "config"},
		{"config", "show", path, "--", "--output", "json"},
		{"--config-file", "different.yaml", "config", "show", path},
		{"--address", ":9501", "config", "show", path, "--", "-a", ":9502"},
		{"-a", ":9501", "config", "show", path, "--", "--address", ":9502"},
		{"config", "--verbose"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, _, err := inspectionRun(t, args...)
			require.Error(t, err)
			require.Empty(t, stdout)
			require.Equal(t, exitUsage, ExitStatus(err), "wrong command usage ends with status 2")
		})
	}

	implicit, _, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err)
	explicit, _, err := inspectionRun(t, "config", "show", path, "--output", "yaml")
	require.NoError(t, err)
	require.Equal(t, implicit, explicit)
}

// TestInspectionUsageErrors checks startup usage errors keep status 1 and
// usage errors inside the config command tree, including a global flag that
// does not parse before show, validate or explain, end with status 2.
func TestInspectionUsageErrors(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for _, args := range [][]string{
		{"--unknown"},
		{"--collect-interval", "bad"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, err := inspectionRun(t, args...)
			require.Error(t, err)
			require.Equal(t, exitFailure, ExitStatus(err), "daemon usage errors keep the 4.8.4 status")
			require.False(t, AlreadyReported(err))
			require.Contains(t, stdout, "Incorrect Usage:")
			require.Contains(t, stdout, "USAGE\n")
			require.Empty(t, stderr, "main owns the returned error diagnostic")
		})
	}

	path := inspectionYAML(t, "version: 2\n")
	for _, args := range [][]string{
		{"config", "--unknown"},
		{"config", "show", "--unknown"},
		{"config", "show", path, "--unknown"},
		{"config", "show", "--", "--unknown"},
		{"config", "show", "--", "--debug=bad"},
		{"--collect-interval", "bad", "config", "show"},
		{"--unknown", "config", "validate"},
		{"--address", ":9501", "-a", ":9502", "config", "show"},
		{"--unknown", "--address", ":9501", "config", "explain"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, err := inspectionRun(t, args...)
			require.Error(t, err)
			require.Equal(t, exitUsage, ExitStatus(err))
			require.Empty(t, stdout)
			require.Empty(t, stderr, "main owns the returned error diagnostic")
		})
	}

	// A bad environment value outside the config tree is a startup usage error.
	t.Setenv("DCGM_EXPORTER_DEBUG", "bad")
	stdout, _, err := inspectionRun(t)
	require.ErrorContains(t, err, "DCGM_EXPORTER_DEBUG")
	require.Equal(t, exitFailure, ExitStatus(err))
	require.Empty(t, stdout, "the error is for stderr; no usage text goes to stdout")
	stdout, _, err = inspectionRun(t, "help")
	require.ErrorContains(t, err, "DCGM_EXPORTER_DEBUG")
	require.Empty(t, stdout)
}

// TestInspectionRejectsAnOutputSelectedTwice checks a repeated output option is
// a usage error in either spelling and on either side of PATH.
func TestInspectionRejectsAnOutputSelectedTwice(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for _, args := range [][]string{
		{"config", "validate", "-o", "json", "-o", "text", "testdata/inspection.yaml"},
		{"config", "validate", "--output", "json", "--output", "text", "testdata/inspection.yaml"},
		{"config", "validate", "-o", "json", "testdata/inspection.yaml", "-o", "text"},
		{"config", "validate", "testdata/inspection.yaml", "-o", "json", "-o", "text"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, _, err := inspectionRun(t, args...)
			require.ErrorContains(t, err, "duplicate --output")
			require.Equal(t, exitUsage, ExitStatus(err))
			require.Empty(t, stdout)
		})
	}

	_, _, err := inspectionRun(t, "config", "validate", "-o", "json", "--output", "text", "testdata/inspection.yaml")
	require.Error(t, err, "the parser refuses to mix the two spellings")
}

// TestInspectionReportsABadEnvironmentValueAsADiagnostic checks that inside the
// config tree an unparsable environment value is an ordinary problem with an
// environment source: no usage text, exit status 1, and JSON on stdout.
func TestInspectionReportsABadEnvironmentValueAsADiagnostic(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	t.Setenv("DCGM_EXPORTER_INTERVAL", "abc")
	t.Setenv("DCGM_EXPORTER_DEBUG", "maybe")
	path := inspectionYAML(t, "version: 2\n")

	data, err := validateJSON(t, nil, path)
	require.Error(t, err)
	assert.Equal(t, exitFailure, ExitStatus(err))
	require.Len(t, data.Diagnostics, 2, "%+v", data.Diagnostics)
	assert.Equal(t, settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"}, data.Diagnostics[0].Source)
	assert.Equal(t, `"abc" is not a whole number of milliseconds`, data.Diagnostics[0].Summary)
	assert.Equal(t, settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_DEBUG"}, data.Diagnostics[1].Source)
	assert.Equal(t, `"maybe" is not true or false`, data.Diagnostics[1].Summary)

	for _, command := range []string{"show", "explain"} {
		stdout, stderr, err := inspectionRun(t, "config", command, path)
		require.Error(t, err, command)
		assert.Equal(t, exitFailure, ExitStatus(err))
		assert.Empty(t, stdout, command)
		assert.Contains(t, stderr, "error    DCGM_EXPORTER_INTERVAL: ")
		assert.NotContains(t, stderr, "Incorrect Usage")
	}

	stdout, _, err := inspectionRun(t, "config", "validate", "-o", "json", path)
	require.Error(t, err)
	assert.True(t, json.Valid([]byte(stdout)), "the JSON result is printed on stdout: %q", stdout)

	// A flag on the command line still replaces the unusable variable.
	stdout, stderr, err := inspectionRun(t, "--collect-interval", "5000", "--debug=false", "config", "show", path)
	require.Error(t, err, "the variable is still reported while another input sets the flag: %s", stdout)
	assert.Contains(t, stderr, "DCGM_EXPORTER_INTERVAL")
}

// TestInspectionTailReplacesOnlyTheVariablesItSets checks that a flag after --
// hides an unparsable environment value only for the flag it sets.
func TestInspectionTailReplacesOnlyTheVariablesItSets(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	t.Setenv("DCGM_EXPORTER_INTERVAL", "abc")

	// Name a metric file in the test directory so the run does not depend on the host's default file.
	csv := inspectionCSV(t, "DCGM_FI_DEV_GPU_TEMP,gauge,Temperature\n")
	path := inspectionYAML(t, "version: 2\nmetrics:\n  - name: gpu\n    file: "+csv+"\noutputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n")

	_, stderr, err := inspectionRun(t, "config", "validate", path, "--", "--web-read-timeout=9s")
	require.Error(t, err)
	assert.Contains(t, stderr, "DCGM_EXPORTER_INTERVAL")

	_, stderr, err = inspectionRun(t, "config", "validate", path, "--", "--collect-interval", "5")
	require.NoError(t, err, stderr)
}

// TestInspectionHelp checks contextual help, metadata inventory and readable width.
func TestInspectionHelp(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	// Every help spelling prints usage that fits the width.
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"help"},
		{"h"},
		{"config"},
		{"config", "--help"},
		{"help", "config", "explain"},
		{"config", "show", "--help"},
		{"config", "validate", "--help"},
		{"config", "explain", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, err := inspectionRun(t, args...)
			require.NoError(t, err)
			require.Empty(t, stderr)
			require.Contains(t, stdout, "USAGE")
			require.NotContains(t, stdout, "\t")
			for _, line := range strings.Split(stdout, "\n") {
				if utf8.RuneCountInString(line) > 80 {
					// Only a single indivisible token may exceed the available width.
					words := strings.Fields(line)
					require.Len(t, words, 1, "wide prose: %s", line)
				}
			}
		})
	}

	// Help for a topic that does not exist is an error with nothing on stdout.
	for _, args := range [][]string{
		{"help", "unknown"},
		{"help", "config", "unknown"},
		{"help", "config", "show", "extra"},
	} {
		stdout, _, err := inspectionRun(t, args...)
		require.Error(t, err)
		require.Empty(t, stdout)
	}

	// Root help lists every flag exactly once, with the selector guide.
	stdout, _, err := inspectionRun(t, "--help")
	require.NoError(t, err)
	for _, flag := range NewApp().Flags {
		name := "--" + flag.Names()[0]
		occurrences := strings.Count(stdout, "  "+name+", ") +
			strings.Count(stdout, "  "+name+" ") + strings.Count(stdout, "  "+name+"\n")
		require.Equal(t, 1, occurrences, "flag missing or repeated: %s", name)
	}

	require.Contains(t, stdout, "CPU: g selects CPUs; i selects CPU cores; f selects both.")
	require.Contains(t, stdout, "Switch: g selects switches; i selects links; f selects both.")
	require.Contains(t, stdout, "Default: true")

	// The help command and the --help flag print the same text.
	root, _, err := inspectionRun(t, "help")
	require.NoError(t, err)
	require.Equal(t, stdout, root)

	contextual, _, err := inspectionRun(t, "help", "config", "explain")
	require.NoError(t, err)
	direct, _, err := inspectionRun(t, "config", "explain", "--help")
	require.NoError(t, err)
	require.Equal(t, contextual, direct)

	// No application-global help hooks affect another urfave app.
	other := cli.NewApp()
	var buffer bytes.Buffer
	other.Writer = &buffer
	require.NoError(t, other.Run([]string{"other", "--help"}))
	require.NotContains(t, buffer.String(), "DEVICE SELECTORS")
}

// TestInspectionWriteFailures checks errors from failed and incomplete output writes.
func TestInspectionWriteFailures(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)

	app := NewApp()
	app.Writer = failingInspectionWriter{}
	require.ErrorIs(t, app.Run([]string{"dcgm-exporter", "config", "show"}), io.ErrClosedPipe)

	app = NewApp()
	app.Writer = shortInspectionWriter{}
	require.ErrorIs(t, app.Run([]string{"dcgm-exporter", "config", "show", "--output", "json"}), io.ErrShortWrite)
}

// TestInspectionHelpRepeatedRun checks command paths and version remain correct across runs.
func TestInspectionHelpRepeatedRun(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)

	app := NewApp("test-version")
	var stdout bytes.Buffer
	app.Writer = &stdout

	// Running the same app twice prints the same command path both times.
	for i := 0; i < 2; i++ {
		stdout.Reset()
		require.NoError(t, app.Run([]string{"dcgm-exporter", "help", "config", "explain"}))
		require.Equal(t, 1, strings.Count(stdout.String(), "NAME\n  dcgm-exporter config explain\n"))
		require.NotContains(t, stdout.String(), "dcgm-exporter dcgm-exporter")
	}

	stdout.Reset()
	require.NoError(t, app.Run([]string{"dcgm-exporter", "--help"}))
	require.Contains(t, stdout.String(), "VERSION\n  test-version")
}

// TestInspectionReusedAppClearsParserPresence checks old argv spellings do not affect the next run.
func TestInspectionReusedAppClearsParserPresence(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	app := NewApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	require.NoError(t, app.Run([]string{"dcgm-exporter", "--address", ":9500", "config", "show"}))
	require.NoError(t, app.Run([]string{"dcgm-exporter", "config", "show", "--", "-a", ":9501"}))
}

// inspectionTripwire replaces an existing typed runtime seam. It fails if inspection ever
// crosses the native, registry, watcher, hostname or listener boundary.
func inspectionTripwire[T any](t *testing.T, seam *T) {
	t.Helper()
	previous := *seam
	t.Cleanup(func() { *seam = previous })
	function := reflect.MakeFunc(reflect.TypeFor[T](), func([]reflect.Value) []reflect.Value {
		t.Fatal("inspection called runtime seam")
		panic("unreachable")
	})
	*seam = function.Interface().(T)
}

// TestInspectionAllRuntimeSeamsRemainUnused checks every runtime boundary and dump creation remain unused.
func TestInspectionAllRuntimeSeamsRemainUnused(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	inspectionTripwire(t, &initializeDCGMProviderFunc)
	inspectionTripwire(t, &initializeNVMLProviderFunc)
	inspectionTripwire(t, &cleanupNVMLProviderFunc)
	inspectionTripwire(t, &buildRegistryFunc)
	inspectionTripwire(t, &getCountersFunc)
	inspectionTripwire(t, &startWatchListManagerFunc)
	inspectionTripwire(t, &getHostnameFunc)
	inspectionTripwire(t, &initCollectorFactoryFunc)
	inspectionTripwire(t, &newMetricsServerFunc)
	inspectionTripwire(t, &newFileWatcherFunc)
	inspectionTripwire(t, &newGPUBindUnbindWatcherFunc)
	dump := filepath.Join(t.TempDir(), "uncreated-dump")
	for _, args := range [][]string{
		{"config", "show", "--", "--dump-enabled", "--dump-directory", dump},
		{"--kubernetes", "--configmap-data", "namespace:name", "config", "validate", "testdata/inspection.yaml"},
		{"config", "explain", "testdata/inspection.yaml"},
		{"config", "explain", "--verbose", "testdata/inspection.yaml"},
		{"config", "validate", "testdata/inspection.yaml", "--", "--dump-enabled", "--dump-directory", dump},
		{"help", "config", "explain"},
		{"config", "show", "--help"},
		{"--help"},
	} {
		app := NewApp()
		app.Writer, app.ErrWriter = io.Discard, io.Discard
		require.NoError(t, app.Run(append([]string{"dcgm-exporter"}, args...)))
	}

	_, err := os.Stat(dump)
	require.True(t, os.IsNotExist(err))
}

// failingInspectionWriter is an output device that fails every write.
type failingInspectionWriter struct{}

// Write simulates an output device that rejects the result.
func (failingInspectionWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

// shortInspectionWriter is an output device that writes less than it was given.
type shortInspectionWriter struct{}

// Write simulates an output device that silently writes only part of the result.
func (shortInspectionWriter) Write(p []byte) (int, error) {
	return len(p) - 1, nil
}

// TestInspectionGoldenViews checks deterministic help and inspection output byte for byte.
func TestInspectionGoldenViews(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for name, args := range map[string][]string{
		"help-root.txt":        {"--help"},
		"help-config.txt":      {"help", "config"},
		"help-explain.txt":     {"help", "config", "explain"},
		"help-show.txt":        {"help", "config", "show"},
		"help-validate.txt":    {"help", "config", "validate"},
		"show-defaults.yaml":   {"config", "show"},
		"show-defaults.json":   {"config", "show", "--output", "json"},
		"show-inspection.yaml": {"config", "show", "testdata/inspection.yaml"},
		"validate.txt":         {"config", "validate", "testdata/inspection.yaml"},
		"validate.json":        {"config", "validate", "testdata/inspection.yaml", "--output", "json"},
		"explain.txt":          {"config", "explain", "testdata/inspection.yaml"},
		"explain-verbose.txt":  {"config", "explain", "testdata/inspection.yaml", "--verbose"},
		"explain.json":         {"config", "explain", "testdata/inspection.yaml", "--output", "json"},
		"explain-verbose.json": {"config", "explain", "testdata/inspection.yaml", "--output", "json", "--verbose"},

		"show-overrides.yaml":           {"config", "show", "testdata/overrides.yaml"},
		"explain-overrides.txt":         {"config", "explain", "testdata/overrides.yaml"},
		"explain-overrides-verbose.txt": {"config", "explain", "testdata/overrides.yaml", "--verbose"},

		// A required container source turns off Kubernetes, also when a flag turned Kubernetes on.
		"explain-container-required-kubernetes-flag.txt": {
			"config", "explain", "testdata/container-required.yaml", "--", "--kubernetes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, err := inspectionRun(t, args...)
			require.NoError(t, err)
			require.Empty(t, stderr)
			filename := filepath.Join("testdata", name)
			if os.Getenv("UPDATE_INSPECTION_GOLDEN") == "1" {
				require.NoError(t, os.WriteFile(filename, []byte(stdout), 0o600))
			}

			expected, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, string(expected), stdout)
		})
	}
}

// PATH and --config-file may name the same file in different spellings, and
// only different files conflict.
func TestSameConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	path := filepath.Join(dir, "exporter.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 2\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.yaml"), []byte("version: 2\n"), 0o600))
	require.NoError(t, os.Symlink(path, filepath.Join(dir, "link.yaml")))

	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", path, path, true},
		{"relative and absolute", "exporter.yaml", path, true},
		{"dot slash form", "./exporter.yaml", "exporter.yaml", true},
		{"symlink to the same file", "link.yaml", path, true},
		{"different files", "other.yaml", path, false},
		{"missing files with different names", "missing-a.yaml", "missing-b.yaml", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sameConfigFile(tc.a, tc.b))
		})
	}
}

// TestInspectionNamesWhatAnUnparsableEnvironmentValueShouldBe checks the
// diagnostic says in plain words what the value should be, for each kind of flag.
func TestInspectionNamesWhatAnUnparsableEnvironmentValueShouldBe(t *testing.T) {
	path := inspectionYAML(t, "version: 2\n")
	for env, want := range map[string]string{
		"DCGM_EXPORTER_INTERVAL":                `"x" is not a whole number of milliseconds`,
		"DCGM_EXPORTER_MAX_CONCURRENT_SCRAPES":  `"x" is not a whole number`,
		"DCGM_EXPORTER_WATCH_MAX_KEEP_SAMPLES":  `"x" is not a whole number`,
		"DCGM_EXPORTER_WATCH_MAX_KEEP_AGE":      `"x" is not a duration`,
		"DCGM_EXPORTER_ENABLE_EXPORTER_METRICS": `"x" is not true or false`,
	} {
		t.Run(env, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			t.Setenv(env, "x")
			data, err := validateJSON(t, nil, path)
			require.Error(t, err)
			require.NotEmpty(t, data.Diagnostics)
			assert.Equal(t, want, data.Diagnostics[0].Summary)
		})
	}
}

// TestBadEnvironmentValueOutsideALeafStillFails checks the config group and its
// help do not hide an unparsable environment value, because nothing else would report it.
func TestBadEnvironmentValueOutsideALeafStillFails(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	t.Setenv("DCGM_EXPORTER_INTERVAL", "abc")
	for _, args := range [][]string{{"config"}, {"config", "--help"}} {
		stdout, _, err := inspectionRun(t, args...)
		require.Error(t, err, strings.Join(args, " "))
		assert.Equal(t, exitFailure, ExitStatus(err))
		assert.Empty(t, stdout, "the error is for stderr; no usage text goes to stdout")
	}

	for _, command := range []string{"show", "validate", "explain"} {
		stdout, _, err := inspectionRun(t, "config", command, "--help")
		require.NoError(t, err, "help for a leaf stays reachable: %s", command)
		assert.Contains(t, stdout, "USAGE")
	}
}

// TestInspectionValidateGoldens checks the result of validate for a valid file
// with a deprecation warning and for each kind of error: an error in the file
// (with its position and key), from a flag, from an environment variable, and
// wrong usage. Each golden holds the exit status and both output streams.
func TestInspectionValidateGoldens(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"validate-file-error.txt", []string{"config", "validate", "testdata/invalid.yaml"}, nil},
		{"validate-file-error-json.txt", []string{"config", "validate", "testdata/invalid.yaml", "--output", "json"}, nil},
		{"validate-flag-error.txt", []string{"--xid-count-window-size", "0", "config", "validate", "testdata/inspection.yaml"}, nil},
		{
			"validate-flag-error-json.txt",
			[]string{"--xid-count-window-size", "0", "config", "validate", "testdata/inspection.yaml", "-o", "json"},
			nil,
		},
		{
			"validate-env-error.txt",
			[]string{"config", "validate", "testdata/inspection.yaml"},
			map[string]string{"DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE": "0"},
		},
		{
			"validate-env-error-json.txt",
			[]string{"config", "validate", "testdata/inspection.yaml", "-o", "json"},
			map[string]string{"DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE": "0"},
		},
		{
			"validate-env-unparsable.txt",
			[]string{"config", "validate", "testdata/inspection.yaml"},
			map[string]string{"DCGM_EXPORTER_INTERVAL": "fast"},
		},
		{"validate-version1-warning.txt", []string{"config", "validate", "testdata/version1.yaml"}, nil},
		{"validate-version1-warning-json.txt", []string{"config", "validate", "testdata/version1.yaml", "-o", "json"}, nil},
		{"validate-usage-error.txt", []string{"config", "validate", "--output", "xml", "testdata/inspection.yaml"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			stdout, stderr, err := inspectionRun(t, tc.args...)
			got := fmt.Sprintf("exit %d\n--- stdout ---\n%s--- stderr ---\n%s", exitStatusOf(err), stdout, stderr)
			if err != nil && !AlreadyReported(err) {
				got += "--- error ---\n" + err.Error() + "\n"
			}

			filename := filepath.Join("testdata", tc.name)
			if os.Getenv("UPDATE_INSPECTION_GOLDEN") == "1" {
				require.NoError(t, os.WriteFile(filename, []byte(got), 0o600))
			}

			expected, readErr := os.ReadFile(filename)
			require.NoError(t, readErr)
			require.Equal(t, string(expected), got)
		})
	}
}

// exitStatusOf is the exit status for the error a run returned; nil is 0.
func exitStatusOf(err error) int {
	if err == nil {
		return 0
	}

	return ExitStatus(err)
}

// TestInspectionUsageErrorsAreSpecific checks the wording of the usage errors
// that people are likely to run into.
func TestInspectionUsageErrorsAreSpecific(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := inspectionYAML(t, "version: 2\n")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"config file after the delimiter":    {[]string{"config", "validate", path, "--", "--config-file", "x"}, "--config-file is not accepted here"},
		"config file before the path":        {[]string{"config", "explain", "--config-file", "x"}, "--config-file is not accepted here"},
		"standard input":                     {[]string{"config", "validate", "-"}, "standard input) is not supported"},
		"a value that looks like the option": {[]string{"config", "show", path, "--", "--collectors", "--config-file"}, ""},
		"unknown option":                     {[]string{"config", "validate", "--bogus"}, "unknown option --bogus"},
		"empty path":                         {[]string{"config", "validate", ""}, "PATH must not be empty"},
		"operand after the delimiter":        {[]string{"config", "validate", path, "--", "stray"}, `only exporter options may follow --; unexpected "stray"`},
		"two spellings of one flag":          {[]string{"--no-hostname", "config", "validate", path, "--", "-n"}, "-n and --no-hostname are the same flag; use one spelling"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, _, err := inspectionRun(t, tc.args...)
			if tc.want == "" {
				require.NoError(t, err, "a flag value is not an option")
				return
			}

			require.ErrorContains(t, err, tc.want)
			require.True(t, IsUsageError(err))
			require.Empty(t, stdout)
			require.NotContains(t, err.Error(), "flag provided but not defined")
		})
	}
}

// TestInspectRefusesACommandItDoesNotKnow checks that a command that is not
// show, validate or explain is an error rather than running explain.
func TestInspectRefusesACommandItDoesNotKnow(t *testing.T) {
	c := cli.NewContext(cli.NewApp(), nil, nil)
	c.Command = &cli.Command{Name: "other"}

	err := inspect(c, inspectionOptions{}, &appconfig.Resolved{}, nil)
	assert.ErrorContains(t, err, `"other" is not a config inspection command`)
}
