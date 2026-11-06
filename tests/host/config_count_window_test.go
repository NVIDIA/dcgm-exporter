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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runCountWindowWarnsAtStartup shows that the running exporter, not only
// `config validate`, logs one warning when a count window in the file is longer
// than the history DCGM keeps, names the override and the flag that set the
// retention, and still starts.
func runCountWindowWarnsAtStartup(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	port := getRandomAvailablePort(t)
	process, _ := startFastExporterAndWaitForHealth(t,
		fmt.Sprintf("http://localhost:%d/health", port),
		"--config-file", configFixture("version2-window-on-xid-count.yaml"),
		"--watch-max-keep-age", "30s",
		"--address", fmt.Sprintf(":%d", port),
	)

	log := process.output.String()
	const override = "metrics[0].overrides[0].window"
	var warnings []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, override) {
			warnings = append(warnings, line)
		}
	}
	require.Len(t, warnings, 1, "one count window warning expected:\n%s", log)
	require.Contains(t, warnings[0], "DCGM_EXP_XID_ERRORS_COUNT")
	require.Contains(t, warnings[0], "--watch-max-keep-age (30s)", "the warning names the flag that set the retention")
}
