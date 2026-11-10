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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestAbsolutePath checks a relative path is made absolute and other values are left alone.
func TestAbsolutePath(t *testing.T) {
	assert.Empty(t, absolutePath(""))
	assert.Equal(t, "/etc/x.csv", absolutePath("/etc/x.csv"))
	assert.Equal(t, "unix:///run/containerd.sock", absolutePath("unix:///run/containerd.sock"), "a value with a scheme is not a path")
	relative := absolutePath("metrics.csv")
	assert.True(t, filepath.IsAbs(relative))
	assert.Equal(t, "metrics.csv", filepath.Base(relative))
}

// TestEffectiveDocumentMakesPathsAbsolute checks the effective config names its files by absolute path.
func TestEffectiveDocumentMakesPathsAbsolute(t *testing.T) {
	config := appconfig.DefaultConfig()
	config.MetricSource.File = "metrics.csv"
	config.WebConfigFile = "web.yml"
	config.DumpConfig.Directory = "dumps"
	document := effectiveDocument(config, appconfig.ConfigNames{})
	for _, path := range []string{document.Metrics[0].File, document.Server.WebConfigFile, document.DebugDump.Directory} {
		assert.True(t, filepath.IsAbs(path), path)
	}

	assert.Equal(t, "/var/run/docker.sock", document.Sources.Container.RuntimeSocket)
}

// TestDefaultSettingValuesHoldEverySettingOnce checks that default setting values hold every setting once.
func TestDefaultSettingValuesHoldEverySettingOnce(t *testing.T) {
	defaults := defaultSettingValues()
	assert.Equal(t, "30s", defaults["CollectInterval"])
	assert.Equal(t, ":9400", defaults["Address"])
	assert.Contains(t, defaults, "UseOldNamespace", "flag-only settings have defaults too")
}

// TestFailureReportsProblemsBeforeWarningsAndLeavesStdoutEmpty checks that failure reports problems before
// warnings and leaves stdout empty.
func TestFailureReportsProblemsBeforeWarningsAndLeavesStdoutEmpty(t *testing.T) {
	r := resolvedDefaults()
	r.Warnings = []appconfig.ResolutionWarning{{Message: "a notice"}}
	problem := errors.Join(errors.New("first"), errors.New("second"))
	result := failure(r, problem)
	assert.Empty(t, result.Stdout)
	assert.Equal(t, problem, result.Failure)
	assert.Equal(t, "error    first\nerror    second\nwarning  a notice\n", string(result.Stderr))
}

// TestJSONTextIsIndentedAndEndsWithANewline checks that JSON text is indented and ends with a newline.
func TestJSONTextIsIndentedAndEndsWithANewline(t *testing.T) {
	out, err := jsonText(map[string]any{"a": 1})
	require.NoError(t, err)
	assert.Equal(t, "{\n  \"a\": 1\n}\n", string(out))

	_, err = jsonText(make(chan int))
	assert.Error(t, err)
}

// TestDiagnosticsTextHasOneLinePerDiagnostic checks that diagnostics text has one line per diagnostic.
func TestDiagnosticsTextHasOneLinePerDiagnostic(t *testing.T) {
	assert.Empty(t, diagnosticsText(nil))
	text := diagnosticsText([]diagnostic{{Severity: severityError, Summary: "a"}, {Severity: severityWarning, Summary: "b"}})
	assert.Equal(t, "error    a\nwarning  b\n", string(text))
}

// TestCommandsRefuseAFormatTheyDoNotWrite checks that a format a command does
// not write is an error, not another format.
func TestCommandsRefuseAFormatTheyDoNotWrite(t *testing.T) {
	r := resolvedFrom(t, inlineMetrics+outputsBlock)

	_, err := Show(r, nil, FormatText)
	assert.ErrorContains(t, err, `config show cannot write the format "text"`)

	_, err = Validate(r, nil, FormatYAML)
	assert.ErrorContains(t, err, `config validate cannot write the format "yaml"`)

	_, err = Explain(r, nil, FormatYAML, false)
	assert.ErrorContains(t, err, `config explain cannot write the format "yaml"`)
}
