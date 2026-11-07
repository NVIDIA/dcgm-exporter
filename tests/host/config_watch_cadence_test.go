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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

// DCGM field IDs behind the metric names the cadence fixtures use.
const (
	fieldGPUTemperature = 150  // DCGM_FI_DEV_GPU_TEMP_CELSIUS
	fieldSMClock        = 1603 // DCGM_FI_DEV_SM_CLOCK_HERTZ
	fieldBoardPower     = 155  // DCGM_FI_DEV_BOARD_POWER_WATTS
)

// cadenceWindow is max_keep_age in the interval fixtures. DCGM keeps the samples
// of that age, so after the window is full the cached sample count of a field
// is the window divided by its update interval. The retention fixture sets
// max_keep_age to 0s and caps by sample count instead, so there the window is
// only how long the test waits for the caps to be reached.
const cadenceWindow = 12 * time.Second

// cadenceMargin is how long past the window the count is read, which lets the
// oldest samples expire.
const cadenceMargin = 3 * time.Second

// sampleRange is the inclusive range a cached sample count must fall in. The
// range is wider than the ideal count because DCGM schedules updates with some
// slack, and still far from the count of the next slower or faster interval.
type sampleRange struct{ min, max int }

// everySecond, everyTwoSeconds and everyFourSeconds are the sample counts a
// window of cadenceWindow gives at each update interval.
var (
	everySecond      = sampleRange{10, 14}
	everyTwoSeconds  = sampleRange{5, 8}
	everyFourSeconds = sampleRange{2, 4}
)

// cacheCounts asks the DCGM probe for the cached sample counts of fieldIDs on
// every monitored entity. The exporter must still be connected, because DCGM
// drops the watches of a client that disconnects.
func (h privateHostEngine) cacheCounts(t testing.TB, fieldIDs ...int) dcgmprobe.CacheCounts {
	t.Helper()
	require.NotEmpty(t, *dcgmProbeBinaryFlag, "-dcgm-probe-binary is required")

	output := filepath.Join(t.TempDir(), "cache-counts.json")
	args := []string{"cache-counts", "--hostengine", h.address, "--output", output}
	for _, id := range fieldIDs {
		args = append(args, "--field-id", strconv.Itoa(id))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- the probe binary is an explicit E2E harness input.
	cmd := exec.CommandContext(ctx, *dcgmProbeBinaryFlag, args...)
	cmd.Env = hostExporterEnv()
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	require.NoErrorf(t, cmd.Run(), "cache counts: %s", safeProbeFailureSummary(stderr.String()))

	data, err := os.ReadFile(output)
	require.NoError(t, err)
	var report dcgmprobe.CacheCounts
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, dcgmprobe.CurrentCacheCountsVersion, report.Version)
	require.NotEmpty(t, report.Entities, "the report holds no monitored entities")

	return report
}

// requireCounts checks that every entity cached a sample count inside the
// range given for each field.
func requireCounts(t testing.TB, report dcgmprobe.CacheCounts, want map[int]sampleRange) {
	t.Helper()
	for entity, fields := range report.Entities {
		for id, bounds := range want {
			count, ok := fields[strconv.Itoa(id)]
			require.Truef(t, ok, "entity %s has no count for field %d", entity, id)
			require.GreaterOrEqualf(t, count, bounds.min, "entity %s field %d cached too few samples", entity, id)
			require.LessOrEqualf(t, count, bounds.max, "entity %s field %d cached too many samples", entity, id)
		}
	}
}

// startExporterOnHostEngine starts the exporter against the private hostengine
// with exactly the given arguments and returns when its first scrape holds the
// named families, with the time that happened. The watches exist by then, so
// the cache window is measured from that time and not from the process start.
func startExporterOnHostEngine(
	t testing.TB, engine privateHostEngine, envOverrides []string, families []string, args ...string,
) (*hostExporterProcess, time.Time) {
	t.Helper()
	port := getRandomAvailablePort(t)
	process, _ := startExporterAndWaitForFamilies(
		t,
		fmt.Sprintf("http://localhost:%d/metrics", port),
		envOverrides,
		families,
		append([]string{"--remote-hostengine-info", engine.uri(), "--address", fmt.Sprintf(":%d", port)}, args...)...,
	)
	return process, time.Now()
}

// waitForWindow waits until the cache window of the exporter that became ready at
// ready is full. The window is a measurement period and cannot be polled
// for: a count in range early would also be in range for a wrong interval.
func waitForWindow(ready time.Time) {
	time.Sleep(time.Until(ready.Add(cadenceWindow + cadenceMargin)))
}

// runConfigFileEveryCadence shows that the file-level every value sets how
// often DCGM updates every field. It fails if the interval is lost, because
// the default interval of 30 seconds caches one sample in the window.
func runConfigFileEveryCadence(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	engine := startTCPHostEngine(t)
	process, ready := startExporterOnHostEngine(t, engine, nil,
		[]string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_BOARD_POWER_WATTS"},
		"--config-file", configFixture("version2-file-every.yaml"))
	defer process.terminate(t)

	waitForWindow(ready)
	requireCounts(t, engine.cacheCounts(t, fieldGPUTemperature, fieldBoardPower), map[int]sampleRange{
		fieldGPUTemperature: everyTwoSeconds,
		fieldBoardPower:     everyTwoSeconds,
	})
}

// runWatchGroupCadence shows that each override interval applies to the fields
// it matches and the file-level interval to the rest.
func runWatchGroupCadence(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	engine := startTCPHostEngine(t)
	process, ready := startExporterOnHostEngine(t, engine, nil,
		[]string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_SM_CLOCK_HERTZ", "DCGM_FI_DEV_BOARD_POWER_WATTS"},
		"--config-file", configFixture("version2-watch-cadence.yaml"))
	defer process.terminate(t)

	waitForWindow(ready)
	requireCounts(t, engine.cacheCounts(t, fieldGPUTemperature, fieldSMClock, fieldBoardPower), map[int]sampleRange{
		fieldGPUTemperature: everySecond,
		fieldSMClock:        everyTwoSeconds,
		fieldBoardPower:     everyFourSeconds,
	})
}

// runWatchGroupRetention shows that an override can cap the cached samples of
// its fields below the file-level cap. DCGM turns a sample cap into an age
// window with some slack, so a count may exceed the cap by one.
func runWatchGroupRetention(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	const globalCap, overrideCap = 6, 2
	engine := startTCPHostEngine(t)
	process, ready := startExporterOnHostEngine(t, engine, nil,
		[]string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_BOARD_POWER_WATTS"},
		"--config-file", configFixture("version2-watch-retention.yaml"))
	defer process.terminate(t)

	// Both caps are reached within a few intervals; the extra seconds show
	// the counts stay at the cap instead of growing.
	waitForWindow(ready)
	requireCounts(t, engine.cacheCounts(t, fieldGPUTemperature, fieldBoardPower), map[int]sampleRange{
		fieldGPUTemperature: {overrideCap, overrideCap + 1},
		fieldBoardPower:     {globalCap, globalCap + 1},
	})
}

// runIntervalPrecedenceCadence shows the order file, environment, flags: each
// later source replaces the interval of the earlier ones. The file asks for
// two seconds, the environment for four and flags for one.
func runIntervalPrecedenceCadence(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	families := []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_BOARD_POWER_WATTS"}
	fields := []int{fieldGPUTemperature, fieldBoardPower}
	env := []string{"DCGM_EXPORTER_INTERVAL=4000"}

	t.Log("environment replaces the file interval")
	engine := startTCPHostEngine(t)
	process, ready := startExporterOnHostEngine(t, engine, env, families,
		"--config-file", configFixture("version2-file-every.yaml"))
	waitForWindow(ready)
	requireCounts(t, engine.cacheCounts(t, fields...), map[int]sampleRange{
		fieldGPUTemperature: everyFourSeconds,
		fieldBoardPower:     everyFourSeconds,
	})
	process.terminate(t)

	t.Log("the flag replaces the environment and the file interval")
	engine = startTCPHostEngine(t)
	process, ready = startExporterOnHostEngine(t, engine, env, families,
		"--config-file", configFixture("version2-file-every.yaml"), "--collect-interval", "1000")
	defer process.terminate(t)
	waitForWindow(ready)
	requireCounts(t, engine.cacheCounts(t, fields...), map[int]sampleRange{
		fieldGPUTemperature: everySecond,
		fieldBoardPower:     everySecond,
	})
}
