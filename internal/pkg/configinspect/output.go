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
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// OutputFormat is the form in which a config command writes its result.
type OutputFormat string

// The output formats. config show writes yaml or json; config validate and
// config explain write text or json.
const (
	FormatText OutputFormat = "text"
	FormatYAML OutputFormat = "yaml"
	FormatJSON OutputFormat = "json"
)

// unsupportedFormat is the error for a format a command does not write.
func unsupportedFormat(command string, format OutputFormat) error {
	return fmt.Errorf("config %s cannot write the format %q", command, format)
}

// inspectionEnvelope is the versioned result of validate and explain. Within
// format version 1 a change only adds keys.
type inspectionEnvelope struct {
	FormatVersion int    `json:"format_version"`
	Command       string `json:"command"`
	Data          any    `json:"data"`
}

// effectiveDocument is the full config that the config commands print, with
// relative file paths made absolute so it means the same wherever it is saved.
func effectiveDocument(config *appconfig.Config, names appconfig.ConfigNames) *appconfig.YAMLConfig {
	document := appconfig.NewFullConfig(config, names)
	document.Metrics[0].File = absolutePath(document.Metrics[0].File)
	document.Server.WebConfigFile = absolutePath(document.Server.WebConfigFile)
	document.DebugDump.Directory = absolutePath(document.DebugDump.Directory)
	document.Sources.HPCJobs.MappingDir = absolutePath(document.Sources.HPCJobs.MappingDir)
	document.Sources.Container.RuntimeSocket = absolutePath(document.Sources.Container.RuntimeSocket)
	document.Sources.Kubernetes.PodResourcesSocket = absolutePath(document.Sources.Kubernetes.PodResourcesSocket)
	return document
}

// absolutePath makes a relative path absolute; an empty or unresolvable path is unchanged.
func absolutePath(path string) string {
	// A value with a scheme, such as unix:///run/containerd.sock, is not a relative path.
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "://") {
		return path
	}

	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}

	return path
}

// defaultSettingValues maps each setting to its built-in default value.
func defaultSettingValues() map[string]any {
	defaults := map[string]any{}
	for _, view := range settingViews(appconfig.DefaultConfig(), appconfig.ConfigNames{}) {
		defaults[view.field] = view.value
	}

	return defaults
}

// Result is what a config command writes and how the run ends. The caller
// writes Stderr and then Stdout, and turns a non-nil Failure into the exit
// status for an invalid configuration. Nothing here writes to a terminal.
type Result struct {
	Stdout, Stderr []byte
	// Failure is set when the configuration is invalid.
	Failure error
}

// failure reports problems on stderr, leaves stdout empty and marks the run as
// failed.
func failure(r *appconfig.Resolved, problems error) Result {
	diagnostics := append(errorDiagnostics(r, problems), warningDiagnostics(r)...)
	return Result{Stderr: diagnosticsText(diagnostics), Failure: problems}
}

// diagnosticsText writes one line per diagnostic.
func diagnosticsText(diagnostics []diagnostic) []byte {
	var buffer bytes.Buffer
	for _, d := range diagnostics {
		buffer.WriteString(d.text() + "\n")
	}

	return buffer.Bytes()
}

// jsonText encodes value as indented JSON followed by a newline. Invisible
// formatting characters are written as \u escapes.
func jsonText(value any) ([]byte, error) {
	output, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}

	escaped := escapeInvisible(string(output), func(r rune) string {
		high, low := utf16.EncodeRune(r)
		return fmt.Sprintf(`\u%04x\u%04x`, high, low)
	}, func(r rune) string { return fmt.Sprintf(`\u%04x`, r) })

	return append([]byte(escaped), '\n'), nil
}

// isInvisible says r is a control character above ASCII, an invisible
// formatting character such as a text direction override, or a line or
// paragraph separator. Printed raw, these can reorder or hide the text around
// them, and the encoders do not escape them all.
func isInvisible(r rune) bool {
	return r >= 0x7f && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029)
}

// escapeInvisible writes each invisible character of text as an escape. A
// character outside the basic plane uses the wide format.
func escapeInvisible(text string, wide, narrow func(rune) string) string {
	var out strings.Builder
	for _, r := range text {
		switch {
		case !isInvisible(r):
			out.WriteRune(r)
		case r > 0xffff:
			out.WriteString(wide(r))
		default:
			out.WriteString(narrow(r))
		}
	}

	return out.String()
}
