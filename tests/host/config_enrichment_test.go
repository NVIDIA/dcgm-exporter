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
	"time"

	"github.com/stretchr/testify/require"
)

// Names the enrichment checks repeat: the labels the sources add, the family
// every fixture serves, the flag that loads the file and the metrics URL form.
const (
	containerLabel   = "container"
	hpcJobLabel      = "hpc_job"
	enrichmentFamily = "DCGM_FI_DEV_GPU_TEMP_CELSIUS"
	configFileFlag   = "--config-file"
	metricsURLFormat = "http://localhost:%d/metrics"
	containerYAMLKey = "metrics[0].enrichment.container"
)

// renderConfigFixture reads a config fixture, replaces each ${NAME} placeholder
// given as a name and value pair, and writes the result to a temporary file.
func renderConfigFixture(t testing.TB, fixture string, replacements ...string) string {
	t.Helper()
	template, err := os.ReadFile(configFixture(fixture))
	require.NoError(t, err)

	rendered := strings.NewReplacer(replacements...).Replace(string(template))
	path := filepath.Join(t.TempDir(), fixture)
	require.NoError(t, os.WriteFile(path, []byte(rendered), 0o600)) //nolint:gosec // G703: the path is inside the test temp directory

	return path
}

// containerEnrichmentConfig writes the container enrichment fixture with the
// given mode (true, false or auto) and runtime socket path.
func containerEnrichmentConfig(t testing.TB, mode, socket string) string {
	t.Helper()
	return renderConfigFixture(t, "version2-container-enrichment.yaml", "${CONTAINER_MODE}", mode, "${RUNTIME_SOCKET}", socket)
}

// kubernetesEnrichmentConfig writes the Kubernetes enrichment fixture with the
// given mode (true, false or auto) and kubelet pod-resources socket path.
func kubernetesEnrichmentConfig(t testing.TB, mode, socket string) string {
	t.Helper()
	return renderConfigFixture(t, "version2-kubernetes-enrichment.yaml", "${KUBERNETES_MODE}", mode, "${KUBELET_SOCKET}", socket)
}

// hpcJobsEnrichmentConfig writes the HPC job enrichment fixture with the given
// mode (true or false) and job mapping directory.
func hpcJobsEnrichmentConfig(t testing.TB, mode, mappingDir string) string {
	t.Helper()
	return renderConfigFixture(t, "version2-hpc-jobs-enrichment.yaml", "${HPC_JOBS_MODE}", mode, "${MAPPING_DIR}", mappingDir)
}

// seriesHaveLabel reports whether any series in a scrape carries the label
// (with the value, when one is given). It returns an error instead of failing
// the test, so polled conditions, which run off the test goroutine, can use it.
func seriesHaveLabel(body, label, value string) (bool, error) {
	families, err := parseMetricFamilies(body)
	if err != nil {
		return false, err
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == label && (value == "" || pair.GetValue() == value) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// requireNoSeriesWithLabel fails the test when any series carries the label.
func requireNoSeriesWithLabel(t testing.TB, body, label string) {
	t.Helper()
	found, err := seriesHaveLabel(body, label, "")
	require.NoError(t, err)
	require.False(t, found, "no series may carry the %s label", label)
}

// requireEventuallyLabeled polls the metrics URL until a series carries the
// label with the value, or fails the test with the given message.
func requireEventuallyLabeled(t testing.TB, metricsURL, label, value string, msgAndArgs ...any) {
	t.Helper()
	require.Eventually(t, func() bool {
		body, status, err := fetchRaw(metricsURL)
		if err != nil || status != http.StatusOK {
			return false
		}

		found, err := seriesHaveLabel(body, label, value)

		return err == nil && found
	}, startupWaitTimeout, startupPollInterval, msgAndArgs...)
}

// warnOnceWatch is how long the warn-once checks keep counting after the first
// scrape. It spans several one-second collection intervals.
const warnOnceWatch = 3 * time.Second

// requireLoggedOnce shows that the exporter log holds the text exactly once,
// and that it still does after several more collection intervals.
func requireLoggedOnce(t testing.TB, process *hostExporterProcess, text string) {
	t.Helper()
	require.Never(t, func() bool {
		return strings.Count(process.output.String(), text) > 1
	}, warnOnceWatch, 250*time.Millisecond, "%q must not be logged again", text)
	require.Equal(t, 1, strings.Count(process.output.String(), text), "%q is logged once", text)
}

// staysOnWarning is the startup warning for an auto source that is not usable.
const staysOnWarning = "is not usable at startup and stays on"

// requiredSourceWaitLog is the startup line logged while a required source is
// not usable yet.
const requiredSourceWaitLog = "Waiting for a required enrichment source"

// requireStartupWaited shows that startup waited for a required source: the
// log has the waiting line and not the warning an auto source would give.
func requireStartupWaited(t testing.TB, process *hostExporterProcess) {
	t.Helper()
	log := process.output.String()
	require.Contains(t, log, requiredSourceWaitLog, "startup must wait for a required source")
	require.NotContains(t, log, staysOnWarning, "a required source must not be treated as auto")
}

// startEnrichmentExporter starts the exporter with a container enrichment file
// and returns the process, its metrics URL and the first scrape that held the
// GPU temperature family.
func startEnrichmentExporter(t testing.TB, mode, socket string) (process *hostExporterProcess, metricsURL, firstScrape string) {
	t.Helper()
	port := getRandomAvailablePort(t)
	metricsURL = fmt.Sprintf(metricsURLFormat, port)
	process, firstScrape = startExporterAndWaitForFamilies(t, metricsURL, nil, []string{enrichmentFamily},
		configFileFlag, containerEnrichmentConfig(t, mode, socket),
		"--address", fmt.Sprintf(":%d", port),
	)
	return process, metricsURL, firstScrape
}

// runEnrichmentAutoMissingSocketStarts shows that a `container: auto` setting with
// no runtime socket still starts and serves GPU metrics without container
// labels. A missing socket may appear later, so the source stays on, warns
// once and waits.
func runEnrichmentAutoMissingSocketStarts(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	missing := filepath.Join(t.TempDir(), "no-such-runtime.sock")
	process, _, body := startEnrichmentExporter(t, "auto", missing)
	requireNoSeriesWithLabel(t, body, containerLabel)

	requireLoggedOnce(t, process, staysOnWarning)
	log := process.output.String()
	require.Contains(t, log, containerYAMLKey)
	require.Contains(t, log, missing)
}

// runEnrichmentAutoUnusableSocketWarns shows that a `container: auto` setting whose
// socket path exists but is not a socket starts, warns once that the source is
// not usable and stays on, names the YAML path of the switch, and adds no
// container labels.
func runEnrichmentAutoUnusableSocketWarns(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	notASocket := filepath.Join(t.TempDir(), "not-a-socket")
	require.NoError(t, os.WriteFile(notASocket, nil, 0o600))
	process, _, body := startEnrichmentExporter(t, "auto", notASocket)
	requireNoSeriesWithLabel(t, body, containerLabel)

	requireLoggedOnce(t, process, staysOnWarning)
	log := process.output.String()
	require.Contains(t, log, "must be a Unix socket")
	require.Contains(t, log, containerYAMLKey)
	require.Contains(t, log, notASocket)
}

// requiredSourceFailureTimeout bounds a run that waits the full 60 seconds for
// a required source and then fails, with room for the exporter to start.
const requiredSourceFailureTimeout = 90 * time.Second

// requiredSourceWaitMessage is how a required source that never became usable
// reports the fixed 60 second wait.
const requiredSourceWaitMessage = "still unavailable after waiting 1m0s"

// runEnrichmentRequiredMissingSocketFails shows that a `container: true` setting
// with no runtime socket waits 60 seconds, then stops startup with exit status 1
// and names the YAML path, the socket and the wait. It is the one case that
// waits the full time for a required source.
func runEnrichmentRequiredMissingSocketFails(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	missing := filepath.Join(t.TempDir(), "no-such-runtime.sock")
	failure := runExporterExpectingStartFailure(t, requiredSourceFailureTimeout, nil,
		configFileFlag, containerEnrichmentConfig(t, "true", missing), "--address", "127.0.0.1:0")
	require.Equal(t, exitInvalid, failure.ExitCode, "stderr:\n%s", failure.Stderr)
	require.Contains(t, failure.Stderr, containerYAMLKey)
	require.Contains(t, failure.Stderr, missing)
	require.Contains(t, failure.Stderr, requiredSourceWaitMessage)
}

// runLater runs action on its own goroutine after the delay and, when the test
// ends, stops a pending run or waits for a running one to finish.
func runLater(t testing.TB, delay time.Duration, action func()) {
	t.Helper()
	finished := make(chan struct{})
	timer := time.AfterFunc(delay, func() {
		defer close(finished)
		action()
	})

	t.Cleanup(func() {
		if timer.Stop() {
			return
		}
		<-finished
	})
}

// runEnrichmentRequiredWaitsForLateSocket shows that a `container: true` setting
// whose runtime socket appears a few seconds after the exporter starts does not
// fail: startup waits for the socket, then serves metrics with container labels.
// The log proves the wait, as the exporter logs it only for a required source
// that is not usable yet.
func runEnrichmentRequiredWaitsForLateSocket(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	socket := filepath.Join(t.TempDir(), "docker.sock")

	// Registered before runLater, so it runs after the late start has finished.
	var stop func() error
	var serveErr error
	t.Cleanup(func() {
		if serveErr == nil && stop != nil {
			require.NoError(t, stop())
		}
	})
	runLater(t, 6*time.Second, func() {
		_, stop, serveErr = serveFakeDockerRuntime(socket)
	})

	process, metricsURL, _ := startEnrichmentExporter(t, "true", socket)
	requireStartupWaited(t, process)
	requireEventuallyLabeled(t, metricsURL, containerLabel, runtimeLabelContainerName,
		"the container labels must appear once the socket does")
}

// runEnrichmentFalseNeverTouchesSocket shows that a `container: false` setting does
// not connect to the runtime socket even when one is listening, while an auto
// and a true switch do and add the labels.
func runEnrichmentFalseNeverTouchesSocket(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	for _, mode := range []string{"auto", "true"} {
		socket, requests := startFakeDockerRuntime(t)
		_, metricsURL, _ := startEnrichmentExporter(t, mode, socket)
		requireEventuallyLabeled(t, metricsURL, containerLabel, runtimeLabelContainerName,
			"container=%s must switch the labels on", mode)
		require.Positive(t, requests.Load(), "container=%s must read the runtime socket", mode)
	}

	socket, requests := startFakeDockerRuntime(t)
	_, metricsURL, body := startEnrichmentExporter(t, "false", socket)
	requireNoSeriesWithLabel(t, body, containerLabel)

	require.Never(t, func() bool {
		if requests.Load() > 0 {
			return true
		}

		scrape, _, err := fetchRaw(metricsURL)
		if err != nil {
			// A failed scrape says nothing about the socket; check again on the next tick.
			return false
		}

		found, err := seriesHaveLabel(scrape, containerLabel, "")
		if err != nil {
			// A scrape that cannot be read must fail the check, not hide a label.
			t.Errorf("read scrape: %v", err)
			return true
		}

		return found
	}, 4*time.Second, 250*time.Millisecond, "container=false must not touch the socket")
}

// runEnrichmentKubernetesMissingSocket shows that a `kubernetes: auto` setting with
// no kubelet socket starts, warns with the YAML path that the source is not
// usable and stays on, and adds no pod labels. It is skipped under NVML injection,
// where the exporter has no NVML client for Kubernetes labels. A fake kubelet
// pod-resources server is not used: it needs a gRPC server on the kubelet API,
// which the Kubernetes suite covers.
func runEnrichmentKubernetesMissingSocket(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	// An enabled Kubernetes source starts the exporter's own NVML client, so the
	// auto case needs the real NVML library. Under NVML injection there is none.
	if os.Getenv("NVML_INJECTION_MODE") == "True" {
		t.Skip("skipping: Kubernetes labels need the real NVML library, not NVML injection")
	}

	missing := filepath.Join(t.TempDir(), "no-such-kubelet.sock")
	port := getRandomAvailablePort(t)
	process, body := startExporterAndWaitForFamilies(t, fmt.Sprintf(metricsURLFormat, port), nil,
		[]string{enrichmentFamily},
		configFileFlag, kubernetesEnrichmentConfig(t, "auto", missing), "--address", fmt.Sprintf(":%d", port))
	requireNoSeriesWithLabel(t, body, "pod")

	requireLoggedOnce(t, process, "Kubernetes "+staysOnWarning)

	log := process.output.String()
	require.Contains(t, log, "metrics[0].enrichment.kubernetes")
	require.Contains(t, log, missing)
}

// startHPCJobsExporter starts the exporter with an HPC job enrichment file and
// returns the process and its metrics URL once a GPU temperature scrape works.
func startHPCJobsExporter(t testing.TB, mode, mappingDir string) (process *hostExporterProcess, metricsURL, firstScrape string) {
	t.Helper()
	port := getRandomAvailablePort(t)
	metricsURL = fmt.Sprintf(metricsURLFormat, port)
	process, firstScrape = startExporterAndWaitForFamilies(t, metricsURL, nil, []string{enrichmentFamily},
		configFileFlag, hpcJobsEnrichmentConfig(t, mode, mappingDir),
		"--address", fmt.Sprintf(":%d", port),
	)

	return process, metricsURL, firstScrape
}

// runEnrichmentHPCJobsTrueWaitsForLateDirectory shows that an `hpc_jobs: true`
// setting whose mapping directory is created a few seconds after the exporter
// starts does not fail: startup waits for the directory, then the series carry
// the hpc_job label from the mapping file.
func runEnrichmentHPCJobsTrueWaitsForLateDirectory(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	const jobName = "late-job-0"
	mappingDir := filepath.Join(t.TempDir(), "jobs")
	runLater(t, 6*time.Second, func() {
		if err := os.MkdirAll(mappingDir, 0o750); err != nil {
			t.Errorf("create the late HPC mapping directory: %v", err)
			return
		}

		if err := os.WriteFile(filepath.Join(mappingDir, "0"), []byte(jobName+"\n"), 0o600); err != nil {
			t.Errorf("write the late HPC mapping file: %v", err)
		}
	})

	process, metricsURL, _ := startHPCJobsExporter(t, "true", mappingDir)
	requireStartupWaited(t, process)
	requireEventuallyLabeled(t, metricsURL, hpcJobLabel, jobName, "the hpc_job label must appear once the directory does")
}

// runEnrichmentHPCJobsFalseAddsNoLabels shows that an `hpc_jobs: false` setting
// adds no hpc_job label even when the mapping directory holds a job for GPU 0.
func runEnrichmentHPCJobsFalseAddsNoLabels(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	mappingDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(mappingDir, "0"), []byte("unused-job\n"), 0o600))
	_, metricsURL, body := startHPCJobsExporter(t, "false", mappingDir)
	requireNoSeriesWithLabel(t, body, hpcJobLabel)

	require.Never(t, func() bool {
		scrape, _, err := fetchRaw(metricsURL)
		if err != nil {
			// A failed scrape says nothing about the labels; check again on the next tick.
			return false
		}

		found, err := seriesHaveLabel(scrape, hpcJobLabel, "")
		if err != nil {
			// A scrape that cannot be read must fail the check, not hide a label.
			t.Errorf("read scrape: %v", err)
			return true
		}

		return found
	}, warnOnceWatch, 250*time.Millisecond, "hpc_jobs=false must not add the hpc_job label")
}
