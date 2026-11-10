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
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// scrapeShape reduces a scrape to what two runs with the same settings share:
// for each DCGM family, the sorted label names of every series. A dropped or
// added label shows as a difference, and so does a different series count.
func scrapeShape(t testing.TB, body string) map[string][]string {
	t.Helper()
	families, err := parseMetricFamilies(body)
	require.NoError(t, err)
	return familiesShape(families)
}

// runVersion2FileMatchesFlags starts the exporter once from a version 2 file
// and once from the equivalent flags and checks that both serve the same
// families with the same series and label names.
func runVersion2FileMatchesFlags(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	families := []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_BOARD_POWER_WATTS"}

	filePort := getRandomAvailablePort(t)
	_, fromFile := startExporterAndWaitForFamilies(t, fmt.Sprintf("http://localhost:%d/metrics", filePort), nil, families,
		"--config-file", configFixture("version2-flag-equivalent.yaml"),
		"--address", fmt.Sprintf(":%d", filePort),
	)

	flagPort := getRandomAvailablePort(t)
	_, fromFlags := startExporterAndWaitForFamilies(t, fmt.Sprintf("http://localhost:%d/metrics", flagPort), nil, families,
		"--collectors", "testdata/flag-equivalent-counters.csv",
		"--collect-interval", "1000",
		"--watch-max-keep-age", "30s",
		"--address", fmt.Sprintf(":%d", flagPort),
	)

	fileShape, flagShape := scrapeShape(t, fromFile), scrapeShape(t, fromFlags)
	names := make([]string, 0, len(fileShape))
	for name := range fileShape {
		names = append(names, name)
	}
	sort.Strings(names)
	require.Equal(t, []string{"DCGM_FI_DEV_BOARD_POWER_WATTS", "DCGM_FI_DEV_GPU_TEMP_CELSIUS"}, names, "the file serves exactly its two fields")
	require.Equal(t, flagShape, fileShape, "the file and the equivalent flags must serve the same families, series and label names")
}
