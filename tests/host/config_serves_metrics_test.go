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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// configServesMetricsListedField is the one field in version1-inline-fields.yaml.
const configServesMetricsListedField = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"

// runConfigServesMetrics starts the exporter from a version 1 config file that
// lists one inline field and checks that the field is served and that no other
// DCGM field family is. It runs against injected NVML data, so it
// needs no GPU, but the run must set NVML_INJECTION_MODE and NVML_YAML_FILE.
func runConfigServesMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	require.Equal(t, "True", os.Getenv("NVML_INJECTION_MODE"), "NVML injection is required")
	require.NotEmpty(t, os.Getenv("NVML_YAML_FILE"), "NVML_YAML_FILE is required")

	port := getRandomAvailablePort(t)
	_, metricsResp := startFastExporterAndWait(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		"--config-file", configFixture("version1-inline-fields.yaml"),
		"--address", fmt.Sprintf(":%d", port),
	)

	families, err := parseMetricFamilies(metricsResp)
	require.NoError(t, err)
	require.Contains(t, families, configServesMetricsListedField)
	for name := range families {
		if strings.HasPrefix(name, "DCGM_FI_") {
			require.Equal(t, configServesMetricsListedField, name, "the config file lists only %s", configServesMetricsListedField)
		}
	}
}
