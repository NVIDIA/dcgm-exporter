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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// refusedLinkError is the error the exporter logs for a mapping file that is a
// hard link or not a regular file.
const refusedLinkError = "mapping must be a regular file with one link"

// mappingWarning is the start of the rate-limited warning the exporter logs
// for a mapping directory or file it will not read.
func mappingWarning(location string) string {
	return fmt.Sprintf("HPC job mapping '%s' is not usable", location)
}

// gpuTemperatureCollectors writes a counters file with only the GPU
// temperature field, so each scrape is small.
func gpuTemperatureCollectors(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "collectors.csv")
	require.NoError(t, os.WriteFile(path, []byte(enrichmentFamily+",gauge,GPU temperature.\n"), 0o600))

	return path
}

// startHPCMappingExporter starts the exporter on the mapping directory with a
// one-second interval and any extra flags. It returns the process, its metrics
// URL and the first scrape that held the GPU temperature family.
func startHPCMappingExporter(t testing.TB, mappingDir string, extraArgs ...string) (process *hostExporterProcess, metricsURL, firstScrape string) {
	t.Helper()
	port := getRandomAvailablePort(t)
	metricsURL = fmt.Sprintf(metricsURLFormat, port)

	args := append([]string{
		"--collect-interval", "1000",
		"--collectors", gpuTemperatureCollectors(t),
		"--address", fmt.Sprintf(":%d", port),
		"--hpc-job-mapping-dir", mappingDir,
	}, extraArgs...)
	process, firstScrape = startExporterAndWaitForFamilies(t, metricsURL, nil, []string{enrichmentFamily}, args...)

	return process, metricsURL, firstScrape
}

// requireUnlabeledGPUZero shows that the scrape still serves the GPU 0
// temperature sample and that no series carries an hpc_job label.
func requireUnlabeledGPUZero(t testing.TB, body string) {
	t.Helper()
	families, err := parseMetricFamilies(body)
	require.NoError(t, err)
	require.Contains(t, families, enrichmentFamily)

	gpuZero := 0
	for _, metric := range families[enrichmentFamily].GetMetric() {
		for _, pair := range metric.GetLabel() {
			if pair.GetName() == "gpu" && pair.GetValue() == "0" {
				gpuZero++
			}
		}
	}
	require.Positive(t, gpuZero, "the GPU 0 sample must still be served")

	requireNoSeriesWithLabel(t, body, hpcJobLabel)
}

// requireMappingRefused shows that the exporter refused the entry at location:
// the first scrape and a later one, taken after several collection intervals
// with the entry still unsafe, serve GPU 0 without job labels, and the warning
// with errorText is logged once despite repeated scrapes.
func requireMappingRefused(t testing.TB, process *hostExporterProcess, metricsURL, firstScrape, location, errorText string) {
	t.Helper()
	requireUnlabeledGPUZero(t, firstScrape)

	warning := mappingWarning(location)
	require.Eventually(t, func() bool {
		return strings.Contains(process.output.String(), warning)
	}, startupWaitTimeout, startupPollInterval, "the exporter must warn about %s", location)

	requireLoggedOnce(t, process, warning)
	require.Contains(t, process.output.String(), errorText)

	laterScrape, status, err := fetchRaw(metricsURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	requireUnlabeledGPUZero(t, laterScrape)
}

// gpuZeroHasJob reports whether a GPU 0 temperature series carries the job.
// It returns an error instead of failing the test, so polled conditions can use it.
func gpuZeroHasJob(body, job string) (bool, error) {
	families, err := parseMetricFamilies(body)
	if err != nil {
		return false, err
	}

	family, found := families[enrichmentFamily]
	if !found {
		return false, nil
	}

	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, pair := range metric.GetLabel() {
			labels[pair.GetName()] = pair.GetValue()
		}

		if labels["gpu"] == "0" && labels[hpcJobLabel] == job {
			return true, nil
		}
	}

	return false, nil
}

// requireEventuallyGPUZeroJob polls the metrics URL until a GPU 0 series
// carries the job, or fails the test with the given message.
func requireEventuallyGPUZeroJob(t testing.TB, metricsURL, job, msg string) {
	t.Helper()
	require.Eventually(t, func() bool {
		body, status, err := fetchRaw(metricsURL)
		if err != nil || status != http.StatusOK {
			return false
		}

		found, err := gpuZeroHasJob(body, job)

		return err == nil && found
	}, startupWaitTimeout, startupPollInterval, msg)
}

// writeMappingFile writes one job ID to path as a regular file.
func writeMappingFile(t testing.TB, path, job string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(job+"\n"), 0o600))
}

// runHPCMappingSymlinkedDirectoryRefused shows that a mapping directory given
// as a symlink to a real directory is not followed: the exporter starts,
// keeps the source on, serves GPU samples without job labels and warns once.
// Replacing the symlink with a real directory makes the labels appear, so the
// refusal is the cause.
func runHPCMappingSymlinkedDirectoryRefused(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	const jobName = "symlinked-dir-job"
	realDir := t.TempDir()
	writeMappingFile(t, filepath.Join(realDir, "0"), jobName)

	mappingDir := filepath.Join(t.TempDir(), "jobs")
	require.NoError(t, os.Symlink(realDir, mappingDir))

	process, metricsURL, body := startHPCMappingExporter(t, mappingDir)
	// Kernels differ on the errno for a final symlink opened as a directory
	// without following it (ENOTDIR or ELOOP), so the check names the
	// exporter's own wording.
	requireMappingRefused(t, process, metricsURL, body, mappingDir, "open HPC mapping directory")
	require.Contains(t, process.output.String(), staysOnWarning, "startup must keep the source on")

	require.NoError(t, os.Remove(mappingDir))
	require.NoError(t, os.Mkdir(mappingDir, 0o750))
	writeMappingFile(t, filepath.Join(mappingDir, "0"), jobName)
	requireEventuallyGPUZeroJob(t, metricsURL, jobName, "a real mapping directory must be read")
}

// runHPCMappingSymlinkedFileRefused shows that a mapping file that is a
// symlink to a regular file elsewhere is not read: GPU 0 keeps its sample
// without the job from the target, and the exporter warns once. Replacing the
// symlink with a regular file makes the label appear.
func runHPCMappingSymlinkedFileRefused(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	const jobName = "symlinked-file-job"
	target := filepath.Join(t.TempDir(), "outside-job")
	writeMappingFile(t, target, jobName)

	mappingDir := t.TempDir()
	mappingFile := filepath.Join(mappingDir, "0")
	require.NoError(t, os.Symlink(target, mappingFile))

	process, metricsURL, body := startHPCMappingExporter(t, mappingDir)
	requireMappingRefused(t, process, metricsURL, body, mappingFile, "too many levels of symbolic links")

	require.NoError(t, os.Remove(mappingFile))
	writeMappingFile(t, mappingFile, jobName)
	requireEventuallyGPUZeroJob(t, metricsURL, jobName, "a regular mapping file must be read")
}

// runHPCMappingHardLinkRefused shows that a mapping file with a second hard
// link elsewhere is refused: GPU 0 keeps its sample without the job, and the
// exporter warns once. Removing the other link leaves one link, and the label
// appears.
func runHPCMappingHardLinkRefused(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	const jobName = "hard-link-job"
	mappingDir := t.TempDir()
	mappingFile := filepath.Join(mappingDir, "0")
	writeMappingFile(t, mappingFile, jobName)

	// Test temporary directories share one file system, so the link can be made.
	otherLink := filepath.Join(t.TempDir(), "other-link")
	require.NoError(t, os.Link(mappingFile, otherLink))

	process, metricsURL, body := startHPCMappingExporter(t, mappingDir)
	requireMappingRefused(t, process, metricsURL, body, mappingFile, refusedLinkError)

	require.NoError(t, os.Remove(otherLink))
	requireEventuallyGPUZeroJob(t, metricsURL, jobName, "a mapping file with one link must be read")
}

// runHPCMappingOversizedFileRefused shows that a mapping file larger than
// --hpc-job-mapping-max-file-bytes adds no job label while the GPU 0 sample is
// still served, and that the exporter warns once. Shrinking the file under the
// limit makes the label appear.
func runHPCMappingOversizedFileRefused(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	const (
		maxFileBytes = 32
		jobName      = "small-job"
	)
	mappingDir := t.TempDir()
	mappingFile := filepath.Join(mappingDir, "0")
	writeMappingFile(t, mappingFile, strings.Repeat("9", maxFileBytes+1))

	process, metricsURL, body := startHPCMappingExporter(t, mappingDir,
		"--hpc-job-mapping-max-file-bytes", fmt.Sprint(maxFileBytes))
	requireMappingRefused(t, process, metricsURL, body, mappingFile, "mapping exceeds file byte limit")

	writeMappingFile(t, mappingFile, jobName)
	requireEventuallyGPUZeroJob(t, metricsURL, jobName, "a mapping file within the limit must be read")
}
