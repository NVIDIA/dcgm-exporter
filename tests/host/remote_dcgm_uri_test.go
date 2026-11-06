//go:build dcgm_uri_integration

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

// Live integration tests for DCGM connection-string remote DCGM support:
// vsock://, tcp://, and unix://.
//
// These are gated behind the dcgm_uri_integration build tag because they drive
// a real nv-hostengine per transport (which requires root or passwordless sudo
// to start) and, for vsock, the vsock_loopback kernel module. They are NOT part
// of the default `task test` / `task test:host` runs.
//
// Run TCP or Unix locally (host with an NVIDIA GPU + DCGM >= 4.5.0).
// The harness exposes host/dcgmUri for TCP and host/dcgmUriUnix for Unix:
//
//	go test -count=1 -v -tags dcgm_uri_integration ./tests/host/ \
//	    -args --ginkgo.label-filter='dcgmUri || dcgmUriUnix'
//
// VSOCK additionally needs the vsock_loopback kernel module:
//
//	sudo modprobe vsock_loopback
//
//	go test -count=1 -v -tags dcgm_uri_integration ./tests/host/ \
//	    -args --ginkgo.label-filter=dcgmUriVSOCK
//
// The tests start dedicated hostengine processes on random ports/sockets and
// unique pid files; they do not stop nvidia-dcgm.service. On developer hosts a
// service-managed nv-hostengine may already be running. If starting a dedicated
// hostengine fails, stop nvidia-dcgm.service before running this opt-in suite and
// restart it afterward.
package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// rootCommand returns a command that runs directly as root or through sudo.
func rootCommand(name string, args ...string) *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command(name, args...)
	}
	sudoArgs := append([]string{name}, args...)
	return exec.Command("sudo", sudoArgs...)
}

// requireHostEngineEnv skips unless nv-hostengine is on PATH and root command
// execution is available for dedicated hostengine lifecycle operations.
func requireHostEngineEnv(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("nv-hostengine"); err != nil {
		requireOrSkipHostengine(t, "nv-hostengine not found in PATH")
	}
	if os.Geteuid() == 0 {
		return
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		requireOrSkipHostengine(t, "sudo not found in PATH")
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		requireOrSkipHostengine(t, "passwordless sudo required to start nv-hostengine")
	}
}

// requireOrSkipHostengine turns missing hostengine prerequisites into skips unless CI requires DCGM.
func requireOrSkipHostengine(t testing.TB, msg string) {
	t.Helper()
	if os.Getenv("E2E_REQUIRE_DCGM") == "1" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

// requireVsock skips unless the vsock device is present (vsock_loopback loaded),
// or fails when E2E_REQUIRE_VSOCK=1 so CI probes cannot pass by skipping VSOCK.
func requireVsock(t testing.TB) {
	t.Helper()
	if _, err := os.Stat("/dev/vsock"); err != nil {
		msg := "/dev/vsock not present; run: sudo modprobe vsock_loopback"
		if os.Getenv("E2E_REQUIRE_VSOCK") == "1" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
}

// startHostEngine starts a dedicated nv-hostengine with the given transport args,
// returning once it has daemonized, and registers cleanup to terminate it.
func startHostEngine(t testing.TB, args ...string) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "nv-hostengine.pid")

	startArgs := append([]string{"nv-hostengine", "--pid", pidFile}, args...)
	out, err := rootCommand(startArgs[0], startArgs[1:]...).CombinedOutput()
	require.NoErrorf(
		t,
		err,
		"start nv-hostengine %v: %s\nIf a service-managed nv-hostengine is already running, stop nvidia-dcgm.service before this opt-in suite and restart it afterward.",
		args,
		out,
	)

	t.Cleanup(func() {
		if termOut, termErr := rootCommand("nv-hostengine", "--term", "--pid", pidFile).CombinedOutput(); termErr != nil {
			t.Logf("terminate nv-hostengine (%s): %v: %s", pidFile, termErr, termOut)
		}
		// The pid file is written by root; remove it before TempDir cleanup.
		_ = rootCommand("rm", "-f", pidFile).Run()
	})
}

// runExporterServesMetricsOverRemoteDCGM checks one remote DCGM endpoint.
func runExporterServesMetricsOverRemoteDCGM(t testing.TB, engineArgs []string, uri string) {
	if testing.Short() {
		t.Skip("skipping live integration test in short mode")
	}
	requireHostEngineEnv(t)
	startHostEngine(t, engineArgs...)
	assertRemoteDCGMMetrics(t, scrapeExporterAgainstRemoteDCGM(t, uri), uri)
}

// runExporterServesMetricsOverRemoteDCGMTCP checks exporter metrics through a TCP host-engine URI.
func runExporterServesMetricsOverRemoteDCGMTCP(t testing.TB) {
	port := getRandomAvailablePort(t)
	runExporterServesMetricsOverRemoteDCGM(
		t,
		[]string{"-b", "127.0.0.1", "-p", fmt.Sprintf("%d", port)},
		fmt.Sprintf("tcp://127.0.0.1:%d", port),
	)
}

// runExporterServesMetricsOverRemoteDCGMUnix checks exporter metrics through a Unix socket URI.
func runExporterServesMetricsOverRemoteDCGMUnix(t testing.TB) {
	socket := filepath.Join(t.TempDir(), "nv-hostengine.sock")
	runExporterServesMetricsOverRemoteDCGM(t, []string{"-d", socket}, "unix://"+socket)
}

// scrapeExporterAgainstRemoteDCGM runs dcgm-exporter against a remote DCGM URI
// and returns its first scrape that holds every default CSV row DCGM supports.
func scrapeExporterAgainstRemoteDCGM(t testing.TB, uri string) string {
	t.Helper()
	httpPort := getRandomAvailablePort(t)
	process, metricsResp := startFastCSVExporterAndWait(
		t,
		fmt.Sprintf("http://127.0.0.1:%d/metrics", httpPort),
		defaultCollectorsFile(t),
		"--address", fmt.Sprintf("127.0.0.1:%d", httpPort),
		"--remote-hostengine-info", uri,
	)
	defer process.terminate(t)

	return metricsResp
}

// assertRemoteDCGMMetrics checks a remote DCGM scrape against the same metric
// contract as a direct host exporter, and checks that every hostname label is
// the local host rather than the remote URI.
func assertRemoteDCGMMetrics(t testing.TB, metricsResp string, uri string) {
	t.Helper()
	validateHostDefaultMetrics(t, metricsResp)

	localHostname, err := os.Hostname()
	require.NoError(t, err)
	families, err := metriccontract.ParseText([]byte(metricsResp))
	require.NoError(t, err)

	// Every sample that carries a hostname must name this host.
	labeled := 0
	for name, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if !slices.Contains(metriccontract.GPUHostnameLabels, label.GetName()) {
					continue
				}

				labeled++
				require.Equalf(t, localHostname, label.GetValue(),
					"metric family %s hostname label over %s, want the local hostname", name, uri)
			}
		}
	}
	require.NotZerof(t, labeled, "no sample has a hostname label over %s", uri)
}

// runExporterServesMetricsOverRemoteDCGMVSOCK drives the exporter against a VSOCK DCGM endpoint.
func runExporterServesMetricsOverRemoteDCGMVSOCK(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping live integration test in short mode")
	}
	requireHostEngineEnv(t)
	requireVsock(t)

	vsockPort := getRandomAvailablePort(t)
	uri := fmt.Sprintf("vsock://1:%d", vsockPort)

	startHostEngine(t, "-c", "1", "-p", fmt.Sprintf("%d", vsockPort))
	assertRemoteDCGMMetrics(t, scrapeExporterAgainstRemoteDCGM(t, uri), uri)
}
