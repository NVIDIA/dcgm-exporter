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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	configPrecedenceYAMLMetric = "DCGM_FI_DEV_GPU_TEMP"
	configPrecedenceEnvMetric  = "DCGM_FI_DEV_POWER_USAGE"
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
  fields:
    - name: %s
      prometheusType: gauge
      help: GPU temperature.
`, configPrecedenceYAMLMetric)), 0o600))

	port := getRandomAvailablePort(t)
	_, metricsResp := startExporterAndWaitWithEnv(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		[]string{"DCGM_EXPORTER_COLLECTORS=" + collectorsPath},
		"--config-file", configPath,
		"--address", fmt.Sprintf(":%d", port),
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	require.Contains(t, families, configPrecedenceEnvMetric)
	require.NotContains(t, families, configPrecedenceYAMLMetric)
}
