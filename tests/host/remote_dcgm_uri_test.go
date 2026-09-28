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
// of the default `make test` / `make test-integration-host` runs.
//
// Run locally (host with an NVIDIA GPU + DCGM >= 4.5.0, vsock_loopback loaded):
//
//	sudo modprobe vsock_loopback
//	go test -count=1 -v -tags dcgm_uri_integration ./tests/host/ \
//	    -args --ginkgo.label-filter=dcgmUri
//
// The tests start dedicated hostengine processes on random ports/sockets and
// unique pid files; they do not stop nvidia-dcgm.service. On developer hosts a
// service-managed nv-hostengine may already be running. If starting a dedicated
// hostengine fails, stop nvidia-dcgm.service before running this opt-in suite and
// restart it afterward.
package host

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/stretchr/testify/require"
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

// runExporterServesMetricsOverRemoteDCGMURISchemes verifies that every
// supported remote DCGM URI scheme can back an exporter scrape.
func runExporterServesMetricsOverRemoteDCGMURISchemes(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping live integration test in short mode")
	}
	requireHostEngineEnv(t)

	tcpPort := getRandomAvailablePort(t)
	vsockPort := getRandomAvailablePort(t)
	unixSocket := filepath.Join("/tmp", fmt.Sprintf("dcgm-he-%d.sock", getRandomAvailablePort(t)))

	tests := []struct {
		name       string
		engineArgs []string
		uri        string
		preflight  func(t testing.TB)
		postStart  func(t testing.TB)
	}{
		{
			name:       "tcp",
			engineArgs: []string{"-b", "127.0.0.1", "-p", fmt.Sprintf("%d", tcpPort)},
			uri:        fmt.Sprintf("tcp://127.0.0.1:%d", tcpPort),
		},
		{
			name:       "unix",
			engineArgs: []string{"-d", unixSocket},
			uri:        "unix://" + unixSocket,
			postStart: func(t testing.TB) {
				t.Cleanup(func() { _ = rootCommand("rm", "-f", unixSocket).Run() })
			},
		},
		{
			name:       "vsock",
			engineArgs: []string{"-c", "1", "-p", fmt.Sprintf("%d", vsockPort)},
			uri:        fmt.Sprintf("vsock://1:%d", vsockPort),
			preflight:  requireVsock,
		},
	}

	for _, tt := range tests {
		t.Logf("checking %s remote DCGM URI", tt.name)
		if tt.preflight != nil {
			tt.preflight(t)
		}
		startHostEngine(t, tt.engineArgs...)
		if tt.postStart != nil {
			tt.postStart(t)
		}

		metricsResp := scrapeExporterAgainstRemoteDCGM(t, tt.uri)
		assertRemoteDCGMMetrics(t, metricsResp, tt.uri)
	}
}

// scrapeExporterAgainstRemoteDCGM runs dcgm-exporter against a remote DCGM URI.
func scrapeExporterAgainstRemoteDCGM(t testing.TB, uri string) string {
	t.Helper()
	httpPort := getRandomAvailablePort(t)
	process := startExporterProcess(
		t,
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf("127.0.0.1:%d", httpPort),
		"--remote-hostengine-info", uri,
	)
	defer process.terminate(t)

	metricsResp, err := retry.DoWithData(
		func() (string, error) {
			if err := process.running(); err != nil {
				return "", err
			}
			body, _, err := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/metrics", httpPort))
			if err != nil {
				return "", err
			}
			if len(body) == 0 {
				return "", errors.New("empty response")
			}
			return body, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)

	require.NoErrorf(t, err, "scrape exporter via %s\n%s", uri, process.output.String())
	return metricsResp
}

// assertRemoteDCGMMetrics verifies a remote DCGM scrape has GPU metrics and sane labels.
func assertRemoteDCGMMetrics(t testing.TB, metricsResp string, uri string) {
	t.Helper()
	require.NotEmpty(t, metricsResp)
	require.Contains(t, metricsResp, "DCGM_FI_DEV_", "expected GPU device metrics over vsock")
	localHostname, err := os.Hostname()
	require.NoError(t, err)
	require.Contains(t, metricsResp, fmt.Sprintf(`hostname="%s"`, localHostname), "expected local hostname label over %s", uri)
	require.NotContains(t, metricsResp, `hostname="vsock://`, "raw remote URI leaked into hostname label")
	require.NotContains(t, metricsResp, `hostname="tcp://`, "raw remote URI leaked into hostname label")
	require.NotContains(t, metricsResp, `hostname="unix://`, "raw remote URI leaked into hostname label")
}

// runExporterServesMetricsOverVSOCKRemoteDCGM drives the exporter against a vsock DCGM endpoint.
func runExporterServesMetricsOverVSOCKRemoteDCGM(t testing.TB) {
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
