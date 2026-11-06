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
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestEscapeTextWritesControlCharactersAsEscapes(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain text is unchanged", "metrics[0].every: 10s é", "metrics[0].every: 10s é"},
		{"escape sequence", "a\x1b[31mred", `a\x1b[31mred`},
		{"line breaks and tabs", "a\nb\r\tc", `a\nb\r\tc`},
		{"carriage return cannot overwrite a line", "ok\rbad", `ok\rbad`},
		{"delete and null", "a\x7fb\x00", `a\x7fb\x00`},
		{"C1 control", "a\u009bb", `a\x9bb`},
		{"text direction override", "a\u202eb", "a\\u202eb"},
		{"zero width space", "a\u200bb", "a\\u200bb"},
		{"line separator", "a\u2028b", "a\\u2028b"},
		{"invalid UTF-8 byte", "a\xffb", `a\xffb`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, EscapeText(tc.in))
		})
	}
}

func TestDiagnosticTextLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   diagnostic
		want string
	}{
		{
			"file position and path",
			diagnostic{Severity: "error", Source: settingSource{Kind: appconfig.OriginFile, File: "c.yaml", Line: 14, Column: 12, Path: "metrics[0].every"}, Summary: "bad"},
			"error    c.yaml:14:12 metrics[0].every: bad",
		},
		{
			"file line only",
			diagnostic{Severity: "error", Source: settingSource{Kind: appconfig.OriginFile, File: "c.yaml", Line: 3}, Summary: "bad"},
			"error    c.yaml:3: bad",
		},
		{
			"file only",
			diagnostic{Severity: "warning", Source: settingSource{Kind: appconfig.OriginFile, File: "c.yaml"}, Summary: "bad"},
			"warning  c.yaml: bad",
		},
		{"flag", diagnostic{Severity: "error", Source: settingSource{Kind: appconfig.OriginFlag, Name: "--x"}, Summary: "bad"}, "error    --x: bad"},
		{"environment variable", diagnostic{Severity: "error", Source: settingSource{Kind: appconfig.OriginEnv, Name: "X_Y"}, Summary: "bad"}, "error    X_Y: bad"},
		{"no source", diagnostic{Severity: "error", Summary: "bad"}, "error    bad"},
		{"fix", diagnostic{Severity: "error", Summary: "bad", Fix: "do this"}, "error    bad (fix: do this)"},
		{"escaped", diagnostic{Severity: "error", Source: settingSource{Kind: appconfig.OriginFile, File: "a\x1b.yaml"}, Summary: "b\nc"}, `error    a\x1b.yaml: b\nc`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.in.text())
		})
	}
}

func TestFlattenErrorsSplitsJoinedErrors(t *testing.T) {
	first, second, third := errors.New("first"), errors.New("second"), errors.New("third")
	assert.Nil(t, flattenErrors(nil))
	assert.Equal(t, []error{first}, flattenErrors(first))
	assert.Equal(t, []error{first, second, third},
		flattenErrors(errors.Join(first, errors.Join(second, nil, third))))
}

func TestSafeReadReasonDoesNotRepeatTheSystemError(t *testing.T) {
	const secret = "secret-path-marker"
	for _, tc := range []struct {
		name  string
		cause error
		want  string
	}{
		{"not found", fs.ErrNotExist, "cannot read configuration file: file does not exist"},
		{"permission", fs.ErrPermission, "cannot read configuration file: permission denied"},
		{"other", errors.New(secret), "cannot read configuration file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("read config file %q: %w", secret, &fs.PathError{Op: "open", Path: secret, Err: tc.cause})
			got := safeReadReason(err)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, secret)
		})
	}
}

func TestErrorDiagnosticFallsBackToTheMessage(t *testing.T) {
	r := &appconfig.Resolved{Config: appconfig.DefaultConfig(), Origins: map[string]appconfig.SettingOrigin{}}
	got := errorDiagnostic(r, errors.New("something else"))
	assert.Equal(t, diagnostic{Severity: severityError, Summary: "something else"}, got)

	r.Origins["WebReadTimeout"] = appconfig.SettingOrigin{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_WEB_READ_TIMEOUT"}
	got = errorDiagnostic(r, &appconfig.SettingError{Err: errors.New("long text"), Field: "WebReadTimeout", Summary: "short", Fix: "fix it"})
	assert.Equal(t, diagnostic{
		Severity: severityError, Source: settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_WEB_READ_TIMEOUT"}, Summary: "short", Fix: "fix it",
	}, got)

	got = errorDiagnostic(r, &appconfig.SettingError{Err: errors.New("no origin recorded"), Field: "Nothing"})
	assert.Equal(t, settingSource{}, got.Source)
}

func TestErrorDiagnosticLocatesEachKindOfProblem(t *testing.T) {
	r := resolvedDefaults()
	r.Config.ConfigFile = "exporter.yaml"

	located := &appconfig.ConfigError{
		Path: "metrics[0].every", Line: 6, Column: 14, Msg: "must be greater than 0", Err: errors.New("metrics[0].every: must be greater than 0"),
	}

	assert.Equal(t, diagnostic{
		Severity: severityError,
		Source:   settingSource{Kind: appconfig.OriginFile, File: "exporter.yaml", Line: 6, Column: 14, Path: "metrics[0].every"},
		Summary:  "must be greater than 0",
	}, errorDiagnostic(r, located))

	read := &appconfig.ReadError{Path: "exporter.yaml", Err: &fs.PathError{Op: "open", Path: "exporter.yaml", Err: fs.ErrPermission}}
	assert.Equal(t, diagnostic{
		Severity: severityError, Source: settingSource{Kind: appconfig.OriginFile, File: "exporter.yaml"},
		Summary: "cannot read configuration file: permission denied",
	}, errorDiagnostic(r, read))

	env := &appconfig.EnvValueError{Env: "DCGM_EXPORTER_INTERVAL", Flag: "collect-interval", Summary: `parsing "abc": invalid syntax`, Err: errors.New("x")}
	assert.Equal(t, diagnostic{
		Severity: severityError, Source: settingSource{Kind: appconfig.OriginEnv, Name: "DCGM_EXPORTER_INTERVAL"},
		Summary: `parsing "abc": invalid syntax`,
	}, errorDiagnostic(r, env))
}

func TestErrorDiagnosticsFlattenJoinedProblemsInOrder(t *testing.T) {
	r := resolvedDefaults()
	joined := errors.Join(errors.New("first"), nil, errors.Join(errors.New("second"), errors.New("third")))
	diagnostics := errorDiagnostics(r, joined, errors.New("fourth"))
	summaries := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		summaries = append(summaries, d.Summary)
	}

	assert.Equal(t, []string{"first", "second", "third", "fourth"}, summaries)
}

func TestFlattenErrorsSplitsAWrapperAroundJoinedErrors(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	wrapped := fmt.Errorf("loading: %w", errors.Join(first, second))
	assert.Equal(t, []error{first, second}, flattenErrors(wrapped))
	single := fmt.Errorf("loading: %w", first)
	assert.Equal(t, []error{single}, flattenErrors(single))
}

// TestFlattenErrorsSplitsJoinedErrorsUnderSeveralWrappers covers the chain a
// config file selected by an environment variable produces.
func TestFlattenErrorsSplitsJoinedErrorsUnderSeveralWrappers(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	wrapped := fmt.Errorf("selected by DCGM_EXPORTER_CONFIG_FILE: %w",
		fmt.Errorf("parse config file: %w", errors.Join(first, second)))
	assert.Equal(t, []error{first, second}, flattenErrors(wrapped))

	deep := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", fmt.Errorf("inner: %w", first)))
	assert.Equal(t, []error{deep}, flattenErrors(deep))
}

func TestWarningDiagnosticsKeepOnlyKnownSources(t *testing.T) {
	r := resolvedDefaults()
	r.Warnings = []appconfig.ResolutionWarning{
		{Message: "from a flag", Origin: appconfig.SettingOrigin{Kind: appconfig.OriginFlag, Name: "--x"}},
		{Message: "from nowhere", Origin: appconfig.SettingOrigin{Kind: appconfig.OriginDefault}},
	}

	assert.Equal(t, []diagnostic{
		{Severity: severityWarning, Source: settingSource{Kind: appconfig.OriginFlag, Name: "--x"}, Summary: "from a flag"},
		{Severity: severityWarning, Summary: "from nowhere"},
	}, warningDiagnostics(r))
}

func TestCountDiagnosticsCountsOneSeverity(t *testing.T) {
	list := []diagnostic{{Severity: severityError}, {Severity: severityWarning}, {Severity: severityError}}
	assert.Equal(t, 2, countDiagnostics(list, severityError))
	assert.Equal(t, 1, countDiagnostics(list, severityWarning))
	assert.Zero(t, countDiagnostics(nil, severityError))
}
