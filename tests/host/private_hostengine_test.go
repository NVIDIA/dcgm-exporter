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
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// privateHostEngine is a standalone DCGM hostengine the test owns. The
// exporter connects to it with --remote-hostengine-info, and the DCGM probe or
// dcgmi reads or writes its cache. It runs as the test user and inherits any
// NVML injection variables, so it works on a GPU-free host; without injection
// it monitors the host's real GPUs and shares no state with a system
// hostengine.
type privateHostEngine struct {
	// network is "tcp" or "unix", and address the host:port or socket path.
	network string
	address string
	logFile string
}

// startTCPHostEngine starts a private hostengine on a free loopback port.
func startTCPHostEngine(t testing.TB) privateHostEngine {
	t.Helper()
	port := strconv.Itoa(getRandomAvailablePort(t))
	dir := privateHostEngineDir(t)
	return startPrivateHostEngine(t, dir, "tcp", net.JoinHostPort("127.0.0.1", port), "-b", "127.0.0.1", "-p", port)
}

// startUnixHostEngine starts a private hostengine on a Unix socket in a
// private directory.
func startUnixHostEngine(t testing.TB) privateHostEngine {
	t.Helper()
	dir := privateHostEngineDir(t)
	socket := filepath.Join(dir, "dcgm.sock")
	return startPrivateHostEngine(t, dir, "unix", socket, "-d", socket)
}

// startPrivateHostEngine starts nv-hostengine from PATH in the foreground with
// listenArgs, keeping its pid and log files in dir, waits until network/address
// accepts connections, and stops it when the test ends. It fails the test when nv-hostengine is missing, exits
// early or does not listen within 30 seconds.
func startPrivateHostEngine(t testing.TB, dir, network, address string, listenArgs ...string) privateHostEngine {
	t.Helper()
	hostengine, err := exec.LookPath("nv-hostengine")
	require.NoError(t, err, "a private hostengine needs nv-hostengine on PATH; datacenter-gpu-manager-4-core installs it")

	engine := privateHostEngine{network: network, address: address, logFile: filepath.Join(dir, "nv-hostengine.log")}
	args := append([]string{"-n", "--pid", filepath.Join(dir, "nv-hostengine.pid"), "-f", engine.logFile}, listenArgs...)
	// #nosec G204 -- the binary is found on PATH and the arguments are fixed by the callers in this package.
	cmd := exec.Command(hostengine, args...)
	cmd.Env = hostExporterEnv()
	output := &lockedBuffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	require.NoError(t, cmd.Start(), "start nv-hostengine")

	// Reap the process in the background so the wait below can see an early exit.
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Logf("stop nv-hostengine: %v", err)
		}
		<-exited
	})

	// The exit error is read only after the process exited, so it is never read while being written.
	waitForHostEngine(t, engine, exited, func() string {
		select {
		case <-exited:
			return fmt.Sprintf("exit: %v\n%s\n%s", exitErr, output.String(), engine.logTail())
		default:
			return output.String() + "\n" + engine.logTail()
		}
	})
	return engine
}

// waitForHostEngine waits until engine accepts connections. It fails the test
// when exited closes first or after 30 seconds, with the text describe returns.
func waitForHostEngine(t testing.TB, engine privateHostEngine, exited <-chan struct{}, describe func() string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-exited:
			t.Fatalf("nv-hostengine exited early\n%s", describe())
		default:
		}

		conn, err := net.DialTimeout(engine.network, engine.address, time.Second)
		if err == nil {
			if err := conn.Close(); err != nil {
				t.Logf("close the nv-hostengine readiness connection: %v", err)
			}
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("nv-hostengine did not listen on %s %s\n%s", engine.network, engine.address, describe())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// maxSocketPathLength is the longest Unix socket path the hostengine is given.
// Linux limits a socket path to 107 bytes.
const maxSocketPathLength = 100

// hostEngineTempRoot returns tmp, or /tmp when a socket in a private directory
// under tmp would have a path longer than maxSocketPathLength. The name
// stands for the directory name MkdirTemp creates, whose random part is up to
// ten digits.
func hostEngineTempRoot(tmp string) string {
	if len(filepath.Join(tmp, "dcgm-he-0123456789", "dcgm.sock")) > maxSocketPathLength {
		return "/tmp"
	}
	return tmp
}

// privateHostEngineDir creates a private directory with a short path, so a
// socket in it stays under the Unix limit even when TMPDIR is long, and removes
// it when the test ends. The directory mode is 0700.
func privateHostEngineDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp(hostEngineTempRoot(os.TempDir()), "dcgm-he-")
	require.NoError(t, err)

	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("remove the private hostengine directory: %v", err)
		}
	})
	return dir
}

// startFastExporterOnHostEngine starts the exporter against engine with a
// one-second collection interval and a counters file of rows, plus args, and
// returns it with its metrics URL once a scrape holds every named family.
func startFastExporterOnHostEngine(
	t testing.TB, engine privateHostEngine, rows, families []string, args ...string,
) (*hostExporterProcess, string) {
	t.Helper()
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	process, _ := startExporterAndWaitForFamilies(
		t,
		metricsURL,
		nil,
		families,
		append([]string{
			"--collectors", writeScenarioCounters(t, rows...),
			"--address", fmt.Sprintf("127.0.0.1:%d", port),
			"--remote-hostengine-info", engine.uri(),
			"--collect-interval", "1000",
		}, args...)...,
	)
	return process, metricsURL
}

// uri returns the DCGM connection string the exporter and dcgmi use for this
// hostengine: unix://<socket> or host:port.
func (h privateHostEngine) uri() string {
	if h.network == "unix" {
		return "unix://" + h.address
	}
	return h.address
}

// dcgmi runs dcgmi from PATH with subcommand against this hostengine and
// returns its combined output and whether it exited with status 0. A missing
// dcgmi or a command that does not finish within 60 seconds fails the test.
func (h privateHostEngine) dcgmi(t testing.TB, subcommand string, args ...string) (string, bool) {
	t.Helper()
	dcgmi, err := exec.LookPath("dcgmi")
	require.NoError(t, err, "this scenario needs dcgmi on PATH; datacenter-gpu-manager-4-core installs it")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// dcgmi takes the connection string after the subcommand.
	cmdArgs := append([]string{subcommand, "--host", h.uri()}, args...)
	// #nosec G204 -- dcgmi is found on PATH and the arguments are built by this test.
	cmd := exec.CommandContext(ctx, dcgmi, cmdArgs...)
	cmd.Env = hostExporterEnv()
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, ctx.Err(), "dcgmi %v did not finish within 60s: %s", cmdArgs, out)

	return string(out), err == nil
}

// logTail returns the end of the hostengine log for failure messages.
func (h privateHostEngine) logTail() string {
	data, err := os.ReadFile(h.logFile)
	if err != nil {
		return "nv-hostengine log unavailable: " + err.Error()
	}

	const limit = 4096
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return string(data)
}

// TestHostEngineTempRoot checks that a long temporary directory is replaced
// by /tmp and a short one is kept.
func TestHostEngineTempRoot(t *testing.T) {
	long := "/" + strings.Repeat("a", maxSocketPathLength)
	require.Equal(t, "/tmp", hostEngineTempRoot(long))
	require.Equal(t, "/var/tmp", hostEngineTempRoot("/var/tmp"))
}
