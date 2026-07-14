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
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/stretchr/testify/require"
)

// runSystemdSocketActivation verifies --web-systemd-socket serves on an inherited fd 3 listener.
func runSystemdSocketActivation(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	tcpListener, ok := listener.(*net.TCPListener)
	require.True(t, ok, "expected TCP listener for systemd socket helper")
	listenerFile, err := tcpListener.File()
	require.NoError(t, err)
	defer listenerFile.Close()

	//nolint:gosec // The test intentionally re-executes its own binary as a helper.
	helper := exec.Command(os.Args[0])
	helper.Env = append(
		os.Environ(),
		"HOST_SYSTEMD_SOCKET_HELPER=1",
		fmt.Sprintf("HOST_SYSTEMD_SOCKET_EXPORTER_BINARY=%s", requireExporterBinary(t)),
		"LISTEN_FDS=1",
	)
	helper.ExtraFiles = []*os.File{listenerFile}
	helper.Stdout = os.Stdout
	helper.Stderr = os.Stderr
	fmt.Fprintln(os.Stdout)
	require.NoError(t, helper.Start())
	defer func() {
		if helper.ProcessState != nil && helper.ProcessState.Exited() {
			return
		}
		fmt.Fprintln(os.Stdout)
		_ = helper.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() {
			done <- helper.Wait()
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = helper.Process.Kill()
			<-done
		}
		fmt.Fprintln(os.Stdout)
	}()

	url := fmt.Sprintf("http://%s/metrics", listener.Addr().String())
	metricsResp, err := retry.DoWithData(
		func() (string, error) {
			resp, _, err := httpGet(t, url, &http.Client{Timeout: 5 * time.Second})
			if err != nil {
				return "", err
			}
			if len(resp) == 0 {
				return "", errors.New("empty response")
			}
			return resp, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)
	require.NoError(t, err)
	fmt.Fprintln(os.Stdout)
	validateHostDefaultMetrics(t, metricsResp)
}

// runSystemdSocketHelper execs dcgm-exporter in place with systemd activation env.
func runSystemdSocketHelper() int {
	binary := os.Getenv("HOST_SYSTEMD_SOCKET_EXPORTER_BINARY")
	if binary == "" {
		fmt.Fprintln(os.Stderr, "HOST_SYSTEMD_SOCKET_EXPORTER_BINARY is required")
		return 1
	}
	if err := os.Setenv("LISTEN_PID", fmt.Sprintf("%d", os.Getpid())); err != nil {
		fmt.Fprintf(os.Stderr, "set LISTEN_PID: %v\n", err)
		return 1
	}

	argv := []string{
		binary,
		"--collectors", "./testdata/default-counters.csv",
		"--address", "127.0.0.1:1",
		"--web-systemd-socket",
	}
	// #nosec G702 -- this test-only helper must exec the explicit product binary in place so LISTEN_PID matches the systemd socket consumer.
	if err := syscall.Exec(binary, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "exec dcgm-exporter: %v\n", err)
		return 1
	}
	return 0
}
