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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// configFixture is the path of a config file fixture.
func configFixture(name string) string {
	return filepath.Join("testdata", "config", name)
}

// Exit statuses of the config commands: 0 for success, 1 for a configuration
// that is not valid, 2 for wrong command usage.
const (
	exitOK       = 0
	exitInvalid  = 1
	exitUsageErr = 2
)

// configInspectionTimeout bounds one config command. The commands never start
// DCGM, so they return at once.
const configInspectionTimeout = 30 * time.Second

// inspectionFixture is the version 2 file the inspection checks read.
var inspectionFixture = configFixture("version2-collections.yaml")

// inspectionEnvelope is the JSON every config command writes with -o json.
type inspectionEnvelope struct {
	FormatVersion int             `json:"format_version"`
	Command       string          `json:"command"`
	Data          json.RawMessage `json:"data"`
}

// inspectionDiagnostic is one entry of the validate result.
type inspectionDiagnostic struct {
	Severity string `json:"severity"`
	Source   struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
		Path   string `json:"path"`
	} `json:"source"`
	Summary string `json:"summary"`
}

// validateResult is the data of config validate -o json.
type validateResult struct {
	Path        string                 `json:"path"`
	Valid       bool                   `json:"valid"`
	Diagnostics []inspectionDiagnostic `json:"diagnostics"`
	NotChecked  []string               `json:"not_checked"`
}

// runConfigCommand runs `dcgm-exporter config <subcommand> args...`.
func runConfigCommand(t testing.TB, envOverrides []string, subcommand string, args ...string) exporterRun {
	t.Helper()
	return runExporterCommand(t, configInspectionTimeout, envOverrides, append([]string{"config", subcommand}, args...)...)
}

// decodeEnvelope parses JSON output and checks its command and format version.
func decodeEnvelope(t testing.TB, stdout, command string) json.RawMessage {
	t.Helper()
	var envelope inspectionEnvelope
	require.NoErrorf(t, json.Unmarshal([]byte(stdout), &envelope), "stdout is not JSON:\n%s", stdout)
	require.Equal(t, 1, envelope.FormatVersion)
	require.Equal(t, command, envelope.Command)
	return envelope.Data
}
