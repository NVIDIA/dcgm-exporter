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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	configPrecedenceYAMLMetric = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"
	configPrecedenceEnvMetric  = "DCGM_FI_DEV_BOARD_POWER_WATTS"
)

// runYAMLCollectorsEnvironmentPrecedence proves an explicitly set collectors
// environment variable takes precedence over an inline YAML metric source.
func runYAMLCollectorsEnvironmentPrecedence(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	tempDir := t.TempDir()
	collectorsPath := filepath.Join(tempDir, "environment-collectors.csv")
	configPath := filepath.Join(tempDir, "dcgm-exporter.yaml")

	require.NoError(t, os.WriteFile(collectorsPath, []byte(
		configPrecedenceEnvMetric+",gauge,Power draw.\n",
	), 0o600))
	require.NoError(t, os.WriteFile(configPath, []byte(fmt.Sprintf(`
version: 2
metrics:
  - name: gpu
    enrichment: {kubernetes: false, container: false, hpc_jobs: false}
    fields:
      - name: %s
        prometheus_type: gauge
        help: GPU temperature.
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
`, configPrecedenceYAMLMetric)), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWaitForFamilies(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		[]string{"DCGM_EXPORTER_COLLECTORS=" + collectorsPath},
		nil,
		"--config-file", configPath,
		"--address", fmt.Sprintf(":%d", port),
		// This spec checks which metric file wins, not cadence, so collect every
		// second instead of waiting for the 30s default.
		"--collect-interval", "1000",
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	require.Contains(t, families, configPrecedenceEnvMetric)
	require.NotContains(t, families, configPrecedenceYAMLMetric)
}

// explainedSetting is one setting row of config explain -o json.
type explainedSetting struct {
	Path   string          `json:"path"`
	Value  json.RawMessage `json:"value"`
	Source struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"source"`
	Replaced []struct {
		Kind  string          `json:"kind"`
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"replaced"`
}

// explainSettings returns the setting rows of config explain -o json for a
// fixture, keyed by YAML path.
func explainSettings(t testing.TB, fixture string, env []string, args ...string) map[string]explainedSetting {
	t.Helper()
	run := runConfigCommand(t, env, "explain", append([]string{configFixture(fixture), "-o", "json"}, args...)...)
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)

	var data struct {
		Settings []explainedSetting `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(decodeEnvelope(t, run.Stdout, "config.explain"), &data))

	settings := make(map[string]explainedSetting, len(data.Settings))
	for _, setting := range data.Settings {
		settings[setting.Path] = setting
	}

	return settings
}

// explainInterval returns the setting row of metrics[0].every from explain output.
func explainInterval(t testing.TB, env []string, args ...string) explainedSetting {
	t.Helper()
	settings := explainSettings(t, "version2-file-every.yaml", env, args...)
	setting, found := settings["metrics[0].every"]
	require.True(t, found, "explain has no setting row for metrics[0].every")

	return setting
}

// runExplainShowsWinningInterval shows that explain names the source that won
// and the values it replaced, for the same inputs the cadence check uses.
func runExplainShowsWinningInterval(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	file := explainInterval(t, nil)
	require.JSONEq(t, `"2s"`, string(file.Value))
	require.Equal(t, "file", file.Source.Kind)
	require.Empty(t, file.Replaced)

	env := explainInterval(t, []string{"DCGM_EXPORTER_INTERVAL=4000"})
	require.JSONEq(t, `"4s"`, string(env.Value))
	require.Equal(t, "env", env.Source.Kind)
	require.Equal(t, "DCGM_EXPORTER_INTERVAL", env.Source.Name)
	require.Len(t, env.Replaced, 1)
	require.Equal(t, "file", env.Replaced[0].Kind)
	require.JSONEq(t, `"2s"`, string(env.Replaced[0].Value))

	fromFlag := explainInterval(t, []string{"DCGM_EXPORTER_INTERVAL=4000"}, "--", "--collect-interval", "1000")
	require.JSONEq(t, `"1s"`, string(fromFlag.Value))
	require.Equal(t, "flag", fromFlag.Source.Kind)
	require.Equal(t, "--collect-interval", fromFlag.Source.Name)
	require.Len(t, fromFlag.Replaced, 2)
	require.Equal(t, "env", fromFlag.Replaced[0].Kind)
	require.JSONEq(t, `"4s"`, string(fromFlag.Replaced[0].Value))
	require.Equal(t, "file", fromFlag.Replaced[1].Kind)
	require.JSONEq(t, `"2s"`, string(fromFlag.Replaced[1].Value))
}
