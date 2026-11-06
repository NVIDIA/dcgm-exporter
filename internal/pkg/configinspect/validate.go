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
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// validateData is the data of a config validate result.
type validateData struct {
	Path        string       `json:"path"`
	Valid       bool         `json:"valid"`
	Diagnostics []diagnostic `json:"diagnostics"`
	// Checked lists the local inputs that were read and found valid.
	Checked    []string `json:"checked"`
	NotChecked []string `json:"not_checked"`
}

// Validate checks the configuration and the local files it names, and reports
// every problem with where it comes from, as text or json. Result.Failure is
// set when there is any error.
func Validate(r *appconfig.Resolved, resolveErr error, format OutputFormat) (Result, error) {
	problems := []error{resolveErr}
	// The lists start non-nil so JSON writes [] rather than null.
	data := validateData{Path: r.Config.ConfigFile, Checked: []string{}}
	if resolveErr == nil {
		// The local files only mean something once the settings that name them resolved.
		inputs := readLocalInputs(r.Config, true)
		problems = append(problems, inputs.problems...)
		addCountWindowWarnings(r, inputs)
		data.Checked = append([]string{"configuration settings"}, inputs.checked...)
	}

	notCheckedList := notCheckedFor(r.Config)
	if hidden := hiddenLocalInputs(r.Config); resolveErr != nil && hidden != "" {
		// Any setting problem hides the local inputs; say so, so the report does
		// not imply that everything but the startup checks was verified.
		notCheckedList = append(notCheckedList, notChecked{"local_files", hidden})
	}

	data.Diagnostics = append([]diagnostic{}, errorDiagnostics(r, problems...)...)
	data.Diagnostics = append(data.Diagnostics, warningDiagnostics(r)...)
	data.Valid = countDiagnostics(data.Diagnostics, severityError) == 0
	data.NotChecked = notCheckedIDs(notCheckedList)

	var result Result
	if !data.Valid {
		result.Failure = errors.New("configuration is invalid")
	}

	switch format {
	case FormatJSON:
		var err error
		result.Stdout, err = jsonText(inspectionEnvelope{FormatVersion: 1, Command: "config.validate", Data: data})
		return result, err
	case FormatText:
	default:
		return result, unsupportedFormat("validate", format)
	}

	report := validateText(data, notCheckedList, resolveErr != nil)
	if data.Valid {
		// Only warnings can be left; they go to stderr and the result to stdout.
		result.Stderr = diagnosticsText(data.Diagnostics)
		result.Stdout = []byte(report.summary + report.detail)
		return result, nil
	}

	// A run with errors writes nothing to stdout.
	result.Stderr = append(append([]byte(report.summary), diagnosticsText(data.Diagnostics)...), report.detail...)
	return result, nil
}

// validateReport is the text of a validate result in the parts that go before
// and after the diagnostics.
type validateReport struct{ summary, detail string }

// validateText writes the header line, the checked inputs and the list of
// what was not checked. fromReader says the errors come from reading the
// settings; the local files are only read when they all resolved.
func validateText(data validateData, notCheckedList []notChecked, fromReader bool) validateReport {
	errorCount := countDiagnostics(data.Diagnostics, severityError)
	warningCount := countDiagnostics(data.Diagnostics, severityWarning)
	name := "no config file"
	if data.Path != "" {
		name = EscapeText(data.Path)
	}

	state := "valid"
	if !data.Valid {
		state = "invalid"
	}

	var summary, detail bytes.Buffer
	counts := plural(errorCount, "error") + ", " + plural(warningCount, "warning")
	fmt.Fprintf(&summary, "%s: %s (%s)\n", name, state, counts)
	if fromReader && singleFileError(data.Diagnostics) {
		detail.WriteString("Only the first problem in the config file is reported; fix it and run again.\n")
	}

	for _, item := range data.Checked {
		fmt.Fprintf(&detail, "Checked: %s\n", EscapeText(item))
	}

	// Local files need no running exporter; they are listed apart when a settings problem hid them.
	var runtime, hidden []notChecked
	for _, item := range notCheckedList {
		if item.id == "local_files" {
			hidden = append(hidden, item)
		} else {
			runtime = append(runtime, item)
		}
	}

	writeWrappedList(&detail, "Not checked (needs the running exporter): ", notCheckedLabels(runtime))
	if len(hidden) > 0 {
		writeWrappedList(&detail, "Not checked until the errors above are fixed: ", notCheckedLabels(hidden))
	}

	return validateReport{summary: summary.String(), detail: detail.String()}
}

// plural writes a count with its noun, for example "1 error" or "2 errors".
func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", noun)
	}

	return fmt.Sprintf("%d %ss", count, noun)
}

// singleFileError says the only error is one the config file reader found. The
// reader stops at its first problem, so other problems in the file may remain.
func singleFileError(diagnostics []diagnostic) bool {
	errorCount := 0
	for _, d := range diagnostics {
		if d.Severity == severityError {
			errorCount++
			if d.Source.Kind != appconfig.OriginFile || d.Source.Line == 0 {
				return false
			}
		}
	}

	return errorCount == 1
}

// hiddenLocalInputs names the local inputs the config uses that a settings
// problem kept from being checked: the metric file or the inline metric
// definitions, and the web config file when one is set. It is empty when the
// config uses none of them, for example with a ConfigMap metric source.
func hiddenLocalInputs(config *appconfig.Config) string {
	var inputs []string
	switch config.MetricSource.Kind {
	case appconfig.MetricSourceFile:
		inputs = append(inputs, "metric file")
	case appconfig.MetricSourceInline:
		inputs = append(inputs, "inline metric definitions")
	}

	if config.WebConfigFile != "" {
		inputs = append(inputs, "web config file")
	}

	return strings.Join(inputs, " and ")
}
