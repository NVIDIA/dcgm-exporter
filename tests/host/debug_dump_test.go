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
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// dumpLimits are the storage limits written into version2-debug-dump-limits.yaml.
type dumpLimits struct {
	files, fileBytes, totalBytes int64
}

// dumpTestFamily is the one field the debug dump fixtures collect.
const dumpTestFamily = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"

// runDebugDumpWritesFiles starts the exporter from a version 2 file that turns the
// debug dump on and points it at a directory that does not exist yet. It checks
// that a scrape leaves a metrics dump and a device information dump, both valid
// JSON files readable only by the owner, in a directory the exporter created
// with owner-only access.
func runDebugDumpWritesFiles(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	parentDir := filepath.Join(t.TempDir(), "parent")
	dumpDir := filepath.Join(parentDir, "dumps")
	startDebugDumpExporter(t, "version2-debug-dump.yaml", dumpDir, dumpLimits{})

	require.Eventually(t, func() bool {
		metrics, _ := filepath.Glob(filepath.Join(dumpDir, "metrics-*.json"))
		devices, _ := filepath.Glob(filepath.Join(dumpDir, "deviceinfo-*.json"))
		return len(metrics) > 0 && len(devices) > 0
	}, startupWaitTimeout, startupPollInterval, "the dump directory holds no dump files")

	for _, dir := range []string{parentDir, dumpDir} {
		info, err := os.Lstat(dir)
		require.NoError(t, err)
		require.Equalf(t, os.FileMode(0o700), info.Mode().Perm(), "%s permissions", dir)
	}

	files, err := filepath.Glob(filepath.Join(dumpDir, "*.json"))
	require.NoError(t, err)
	for _, file := range files {
		info, err := os.Lstat(file)
		require.NoError(t, err)
		require.Equalf(t, os.FileMode(0o600), info.Mode().Perm(), "%s permissions", filepath.Base(file))

		content, err := os.ReadFile(file)
		require.NoError(t, err)
		require.Truef(t, json.Valid(content), "%s is not valid JSON", filepath.Base(file))
	}
}

// runDebugDumpRefusesSymlinkDirectory points the dump directory at a symbolic
// link to another directory. The exporter must log that it cannot write the
// dump, keep serving metrics, and write nothing through the link.
func runDebugDumpRefusesSymlinkDirectory(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "dumps")
	require.NoError(t, os.Symlink(target, link))
	process, metricsURL := startDebugDumpExporter(t, "version2-debug-dump.yaml", link, dumpLimits{})

	require.Eventually(t, func() bool {
		_, _, _ = fetchRaw(metricsURL)
		output := process.output.String()
		return strings.Contains(output, "Failed to write metrics debug file") &&
			strings.Contains(output, "must be a directory")
	}, startupWaitTimeout, startupPollInterval, "no refused dump was logged")

	body, status, err := fetchRaw(metricsURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, dumpTestFamily)

	entries, err := os.ReadDir(target)
	require.NoError(t, err)
	require.Empty(t, entries, "the exporter wrote through the symbolic link")

	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "the exporter replaced the symbolic link")
}

// runDebugDumpStaysWithinLimits checks each storage limit on its own. First
// a small max_files with roomy byte limits; then roomy max_files with byte
// limits sized from the largest dump seen, so only the byte limit can remove
// files.
func runDebugDumpStaysWithinLimits(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	fileLimits := dumpLimits{files: 3, fileBytes: 1 << 20, totalBytes: 2 << 20}
	largest, _, _ := scrapeDumpsWithinLimits(t, fileLimits, 2*int(fileLimits.files)+1)

	fileBytes := 2 * largest
	byteLimits := dumpLimits{files: 1000, fileBytes: fileBytes, totalBytes: 3 * fileBytes}
	_, seen, present := scrapeDumpsWithinLimits(t, byteLimits, 20)
	require.Lessf(t, present, seen, "max_total_bytes %d removed no dump", byteLimits.totalBytes)
}

// scrapeDumpsWithinLimits starts an exporter with the given dump limits and
// scrapes it until it has written wantDumps distinct dump files. After every
// scrape the directory must hold no more files or bytes than the limits allow,
// and no dump may fail to write. It returns the largest dump file seen, the
// number of distinct dumps written and the number still present at the end.
func scrapeDumpsWithinLimits(t testing.TB, limits dumpLimits, wantDumps int) (largest int64, seen, present int) {
	t.Helper()
	dumpDir := t.TempDir()
	process, metricsURL := startDebugDumpExporter(t, "version2-debug-dump-limits.yaml", dumpDir, limits)

	written := map[string]struct{}{}
	deadline := time.Now().Add(startupWaitTimeout)
	for len(written) < wantDumps && time.Now().Before(deadline) {
		_, status, err := fetchRaw(metricsURL)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)

		listing := listDumpDirectory(t, dumpDir)
		require.LessOrEqualf(t, int64(len(listing.names)), limits.files, "dump files after a scrape: %v", listing.names)
		require.LessOrEqual(t, listing.totalBytes, limits.totalBytes, "dump bytes after a scrape")
		for _, name := range listing.names {
			written[name] = struct{}{}
		}
		largest = max(largest, listing.largestBytes)
		present = len(listing.names)

		// The fixture admits one dump of each kind per second.
		time.Sleep(startupPollInterval)
	}

	output := process.output.String()
	require.GreaterOrEqualf(t, len(written), wantDumps, "the exporter stopped writing dumps; output:\n%s", output)
	for _, warning := range []string{"Failed to write metrics debug file", "Failed to write device info debug file"} {
		require.NotContains(t, output, warning)
	}
	return largest, len(written), present
}

// startDebugDumpExporter fills the dump directory and limits into a config
// fixture, starts the exporter from it and waits for the first scrape that
// holds the fixture's family. It returns the process and its metrics URL.
func startDebugDumpExporter(t testing.TB, fixture, dumpDir string, limits dumpLimits) (*hostExporterProcess, string) {
	t.Helper()
	template, err := os.ReadFile(configFixture(fixture))
	require.NoError(t, err)
	config := strings.NewReplacer(
		"${DUMP_DIRECTORY}", dumpDir,
		"${MAX_FILES}", strconv.FormatInt(limits.files, 10),
		"${MAX_FILE_BYTES}", strconv.FormatInt(limits.fileBytes, 10),
		"${MAX_TOTAL_BYTES}", strconv.FormatInt(limits.totalBytes, 10),
	).Replace(string(template))
	configPath := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600)) //nolint:gosec // G703: the path is inside the test temp directory

	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)
	process, _ := startExporterAndWaitForFamilies(t, metricsURL, nil, []string{dumpTestFamily},
		"--config-file", configPath, "--address", fmt.Sprintf(":%d", port))
	return process, metricsURL
}

// dumpListing is what one look at a dump directory found.
type dumpListing struct {
	names        []string
	totalBytes   int64
	largestBytes int64
}

// listDumpDirectory returns the names of the entries in a dump directory and
// the total and largest size of its regular files.
func listDumpDirectory(t testing.TB, dumpDir string) dumpListing {
	t.Helper()
	entries, err := os.ReadDir(dumpDir)
	require.NoError(t, err)

	var listing dumpListing
	for _, entry := range entries {
		info, err := entry.Info()
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)

		listing.names = append(listing.names, entry.Name())
		if info.Mode().IsRegular() {
			listing.totalBytes += info.Size()
			listing.largestBytes = max(listing.largestBytes, info.Size())
		}
	}
	return listing
}
