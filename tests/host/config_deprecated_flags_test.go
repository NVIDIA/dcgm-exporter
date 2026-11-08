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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// deprecatedConfigMapDataWarning is the start of the warning for --configmap-data.
const deprecatedConfigMapDataWarning = "flag --configmap-data is deprecated"

// runDeprecatedFlagWarnsOnceInValidate sets the deprecated --configmap-data flag
// on the command line and in the environment, and --no-hostname, which is not
// deprecated, the same way. It checks that validate reports one warning for the first and none
// for the second, and still exits 0.
func runDeprecatedFlagWarnsOnceInValidate(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	env := []string{"DCGM_EXPORTER_CONFIGMAP_DATA=ns:name", "DCGM_EXPORTER_NO_HOSTNAME=true"}
	run := runConfigCommand(t, env, "validate", inspectionFixture, "-o", "json", "--", "-m", "ns:name", "-n")
	require.Equal(t, exitOK, run.ExitCode, "stderr:\n%s", run.Stderr)
	var result validateResult
	require.NoError(t, json.Unmarshal(decodeEnvelope(t, run.Stdout, "config.validate"), &result))
	require.True(t, result.Valid)
	warnings := 0
	for _, diagnostic := range result.Diagnostics {
		require.NotContains(t, diagnostic.Summary, "--no-hostname", "a flag with no config key is not deprecated:\n%s", run.Stdout)
		if diagnostic.Severity == "warning" && strings.Contains(diagnostic.Summary, deprecatedConfigMapDataWarning) {
			warnings++
		}
	}
	require.Equal(t, 1, warnings, "one warning per deprecated flag:\n%s", run.Stdout)
}

// runFlagWithoutConfigKeyDoesNotWarnAtStartup starts the exporter with --no-hostname
// set on the command line and in the environment. The flag has no config key
// but is not deprecated, so the log holds no deprecation warning.
func runFlagWithoutConfigKeyDoesNotWarnAtStartup(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	port := getRandomAvailablePort(t)
	process, _ := startExporterAndWaitForFamilies(t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		[]string{"DCGM_EXPORTER_NO_HOSTNAME=true"},
		[]string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS"},
		"--config-file", configFixture("version2-flag-equivalent.yaml"),
		"--address", fmt.Sprintf(":%d", port),
		"-n",
	)
	require.NotContains(t, process.output.String(), "deprecated", "no flag warning expected:\n%s", process.output.String())
}
