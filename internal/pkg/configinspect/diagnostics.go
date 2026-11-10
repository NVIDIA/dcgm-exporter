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
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
)

// diagnostic severities.
const (
	severityError   = "error"
	severityWarning = "warning"
)

// diagnostic is one problem or warning found in the configuration.
type diagnostic struct {
	Severity string        `json:"severity"`
	Source   settingSource `json:"source"`
	Summary  string        `json:"summary"`
	Fix      string        `json:"fix,omitempty"`
}

// flattenErrors splits joined errors into the individual problems. A joined
// error that sits under any number of single wrappers, such as the file loader's
// "parse config file" and the "selected by" context around it, is split too;
// the wrappers' own text is left out, because each problem says where it is.
// An error with no joined error under it comes back as it is.
func flattenErrors(err error) []error {
	if err == nil {
		return nil
	}

	// Walk down the single wrappers to the first joined error, if there is one.
	for inner := err; inner != nil; inner = errors.Unwrap(inner) {
		if joined, ok := inner.(interface{ Unwrap() []error }); ok {
			var all []error
			for _, problem := range joined.Unwrap() {
				all = append(all, flattenErrors(problem)...)
			}

			return all
		}
	}

	return []error{err}
}

// errorDiagnostics turns the problems found while resolving or checking the
// configuration into diagnostics that name where each one comes from.
func errorDiagnostics(r *appconfig.Resolved, problems ...error) []diagnostic {
	var diagnostics []diagnostic
	for _, problem := range problems {
		for _, leaf := range flattenErrors(problem) {
			diagnostics = append(diagnostics, errorDiagnostic(r, leaf))
		}
	}

	return diagnostics
}

// errorDiagnostic describes one problem.
func errorDiagnostic(r *appconfig.Resolved, err error) diagnostic {
	var located *appconfig.ConfigError
	var setting *appconfig.SettingError
	var read *appconfig.ReadError
	var env *appconfig.EnvValueError
	switch {
	case errors.As(err, &located):
		return diagnostic{
			Severity: severityError,
			Source: settingSource{
				Kind: appconfig.OriginFile, File: r.Config.ConfigFile, Line: located.Line, Column: located.Column, Path: located.Path,
			},
			Summary: located.Message(),
		}
	case errors.As(err, &setting):
		summary := setting.Summary
		if summary == "" {
			summary = setting.Error()
		}

		source := sourceOf(r, r.OriginOf(setting.Field)).forProblem()
		if setting.Path != "" {
			source = settingSource{Kind: appconfig.OriginFile, File: r.Config.ConfigFile, Path: setting.Path}
			source.Line, source.Column, _ = r.YAML.Locate(setting.Path)
		}

		return diagnostic{Severity: severityError, Source: source, Summary: summary, Fix: setting.Fix}
	case errors.As(err, &env):
		return diagnostic{
			Severity: severityError, Source: settingSource{Kind: appconfig.OriginEnv, Name: env.Env}, Summary: env.Summary,
		}
	case errors.As(err, &read):
		return diagnostic{
			Severity: severityError, Source: settingSource{Kind: appconfig.OriginFile, File: r.Config.ConfigFile},
			Summary: safeReadReason(err),
		}
	default:
		return diagnostic{Severity: severityError, Summary: err.Error()}
	}
}

// warningDiagnostics describes the warnings found while resolving.
func warningDiagnostics(r *appconfig.Resolved) []diagnostic {
	diagnostics := make([]diagnostic, 0, len(r.Warnings))
	for _, warning := range r.Warnings {
		diagnostics = append(diagnostics, diagnostic{
			Severity: severityWarning, Source: sourceOf(r, warning.Origin).forProblem(), Summary: warning.Message,
		})
	}

	return diagnostics
}

// addCountWindowWarnings adds the warnings about count windows that DCGM's
// history cannot fill. They need the metric definitions, so they are added when
// the metric source was read.
func addCountWindowWarnings(r *appconfig.Resolved, inputs localInputs) {
	if inputs.counters != nil {
		r.Warnings = append(r.Warnings, devicewatchlistmanager.CountWindowWarnings(r, inputs.counters)...)
	}
}

// safeReadReason says why a file could not be read without repeating the
// system error, which can carry paths and other details.
func safeReadReason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "cannot read configuration file: file does not exist"
	case errors.Is(err, fs.ErrPermission):
		return "cannot read configuration file: permission denied"
	default:
		return "cannot read configuration file"
	}
}

// countDiagnostics counts the diagnostics of one severity.
func countDiagnostics(diagnostics []diagnostic, severity string) int {
	count := 0
	for _, d := range diagnostics {
		if d.Severity == severity {
			count++
		}
	}

	return count
}

// text writes a diagnostic as one line made of its severity, where it comes
// from and the problem, such as: error    config.yaml:14:12 metrics[0].every: "0s"
// must be greater than 0
func (d diagnostic) text() string {
	var line strings.Builder
	fmt.Fprintf(&line, "%-9s", d.Severity)
	if source := d.Source.text(); source != "" {
		line.WriteString(source + ": ")
	}

	line.WriteString(EscapeText(d.Summary))
	if d.Fix != "" {
		line.WriteString(" (fix: " + EscapeText(d.Fix) + ")")
	}

	return line.String()
}

// text writes the source the way compilers do: file:line:column and the YAML path.
func (s settingSource) text() string {
	switch {
	case s.File != "":
		text := EscapeText(s.File)
		if s.Line > 0 {
			text += fmt.Sprintf(":%d", s.Line)
			if s.Column > 0 {
				text += fmt.Sprintf(":%d", s.Column)
			}
		}

		if s.Path != "" {
			text += " " + EscapeText(s.Path)
		}

		return text
	case s.Name != "":
		return EscapeText(s.Name)
	default:
		return ""
	}
}

// EscapeText makes user-supplied text safe to print. Control characters, line
// breaks, invisible formatting characters (such as text direction overrides)
// and bytes that are not valid UTF-8 are written as escapes, so the text cannot
// move the cursor, hide other text or change the terminal.
func EscapeText(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&out, `\x%02x`, text[i])
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029:
			if r < 0x100 {
				fmt.Fprintf(&out, `\x%02x`, r)
			} else {
				fmt.Fprintf(&out, `\u%04x`, r)
			}
		default:
			out.WriteString(text[i : i+size])
		}

		i += size
	}

	return out.String()
}
