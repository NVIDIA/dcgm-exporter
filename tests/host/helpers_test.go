/*
 * Copyright (c) 2023, NVIDIA CORPORATION.  All rights reserved.
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
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

var randomPortMutex sync.Mutex

var usedPorts = map[int]struct{}{}

const (
	// exporterWebWriteTimeout tracks the current product default without making
	// host black-box tests import exporter internals.
	exporterWebWriteTimeout            = 30 * time.Second
	exporterShutdownMargin             = 15 * time.Second
	exporterKillTimeout                = 5 * time.Second
	hostTerminationTestGracefulTimeout = 25 * time.Millisecond
	hostTerminationTestKillTimeout     = time.Second
)

// hostProcessTerminationPolicy bounds test cleanup while allowing the product's
// documented HTTP shutdown window and DCGM cleanup to finish.
type hostProcessTerminationPolicy struct {
	GracefulTimeout time.Duration
	KillTimeout     time.Duration
}

var exporterTerminationPolicy = hostProcessTerminationPolicy{
	GracefulTimeout: exporterWebWriteTimeout + exporterShutdownMargin,
	KillTimeout:     exporterKillTimeout,
}

var exporterBinaryFlag = flag.String(
	"exporter-binary",
	os.Getenv("E2E_EXPORTER_BINARY"),
	"path to the dcgm-exporter product binary under test",
)

var dcgmProbeBinaryFlag = flag.String(
	"dcgm-probe-binary",
	"",
	"path to the direct-DCGM probe used by DCGM mock tests",
)

var dcgmFieldsFileFlag = flag.String(
	"dcgm-fields-file",
	"",
	"path to the pinned go-dcgm const_fields.go used by the probe",
)

// requireExporterBinary returns the product binary used by black-box host tests.
func requireExporterBinary(t testing.TB) string {
	t.Helper()
	binary := strings.TrimSpace(*exporterBinaryFlag)
	if binary == "" {
		t.Fatal("host integration tests require -exporter-binary or E2E_EXPORTER_BINARY")
	}
	abs, err := filepath.Abs(binary)
	require.NoError(t, err)
	info, err := os.Stat(abs)
	require.NoErrorf(t, err, "dcgm-exporter product binary not found: %s", abs)
	require.Falsef(t, info.IsDir(), "dcgm-exporter product binary is a directory: %s", abs)
	require.NotZerof(t, info.Mode()&0o111, "dcgm-exporter product binary is not executable: %s", abs)
	return abs
}

// lockedBuffer safely captures concurrent stdout/stderr writes from a child process.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write records process output while satisfying io.Writer.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns all captured process output.
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// hostExporterProcess tracks a running product exporter child process.
type hostExporterProcess struct {
	cmd           *exec.Cmd
	done          chan error
	output        *lockedBuffer
	terminateOnce sync.Once
}

// startExporterProcess starts the product exporter with the supplied CLI args.
func startExporterProcess(t testing.TB, args ...string) *hostExporterProcess {
	return startExporterProcessWithEnv(t, nil, args...)
}

// startExporterProcessWithEnv starts the product exporter with explicit environment overrides.
func startExporterProcessWithEnv(t testing.TB, envOverrides []string, args ...string) *hostExporterProcess {
	t.Helper()
	processOutput := &lockedBuffer{}
	// #nosec G204 -- requireExporterBinary requires an explicit flag/env path and validates absolute path, existence, non-directory, and executable bit.
	cmd := exec.Command(requireExporterBinary(t), args...)
	cmd.Env = mergeEnv(hostExporterEnv(), envOverrides)
	cmd.Stdout = processOutput
	cmd.Stderr = processOutput
	require.NoErrorf(t, cmd.Start(), "start dcgm-exporter %v", args)

	process := &hostExporterProcess{
		cmd:    cmd,
		done:   make(chan error, 1),
		output: processOutput,
	}
	go func() {
		process.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		process.terminate(t)
	})
	return process
}

func mergeEnv(base, overrides []string) []string {
	merged := append([]string{}, base...)
	for _, override := range overrides {
		key, _, _ := strings.Cut(override, "=")
		for i := len(merged) - 1; i >= 0; i-- {
			if existingKey, _, _ := strings.Cut(merged[i], "="); existingKey == key {
				merged = append(merged[:i], merged[i+1:]...)
			}
		}
		merged = append(merged, override)
	}
	return merged
}

func hostExporterEnv() []string {
	allowed := []string{
		"PATH",
		"LD_LIBRARY_PATH",
		"HOME",
		"TMPDIR",
		"GOCOVERDIR",
		"E2E_REQUIRE_VSOCK",
		"E2E_REQUIRE_DCGM",
		"NVML_INJECTION_MODE",
		"NVML_YAML_FILE",
		"DCGM_NVSDM_MOCK_YAML",
	}
	env := make([]string, 0, len(allowed))
	for _, key := range allowed {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func TestHostExporterEnvFiltersUnrelatedDCGMVars(t *testing.T) {
	t.Setenv("DCGM_FI_DEV_GPU_TEMP", "1")
	t.Setenv("GOCOVERDIR", "/tmp/cover")
	t.Setenv("E2E_REQUIRE_VSOCK", "1")
	t.Setenv("NVML_INJECTION_MODE", "True")
	t.Setenv("NVML_YAML_FILE", "/tmp/injection.yaml")
	t.Setenv("DCGM_NVSDM_MOCK_YAML", "/tmp/mock.yaml")
	env := hostExporterEnv()
	require.Contains(t, env, "GOCOVERDIR=/tmp/cover")
	require.Contains(t, env, "E2E_REQUIRE_VSOCK=1")
	require.Contains(t, env, "NVML_INJECTION_MODE=True")
	require.Contains(t, env, "NVML_YAML_FILE=/tmp/injection.yaml")
	require.Contains(t, env, "DCGM_NVSDM_MOCK_YAML=/tmp/mock.yaml")
	for _, item := range env {
		require.NotContains(t, item, "DCGM_FI_DEV_GPU_TEMP")
	}
}

func TestMergeEnvOverridesExistingValues(t *testing.T) {
	merged := mergeEnv(
		[]string{"PATH=/usr/bin", "LD_LIBRARY_PATH=/system", "HOME=/tmp"},
		[]string{"LD_LIBRARY_PATH=/custom", "LD_DEBUG=libs"},
	)
	require.ElementsMatch(t, []string{
		"PATH=/usr/bin", "LD_LIBRARY_PATH=/custom", "HOME=/tmp", "LD_DEBUG=libs",
	}, merged)
}

// signal sends an OS signal to the exporter child process.
func (p *hostExporterProcess) signal(t testing.TB, sig os.Signal) {
	t.Helper()
	if p.cmd.Process == nil {
		t.Fatalf("dcgm-exporter process was not started; output:\n%s", p.output.String())
	}
	require.NoErrorf(t, p.cmd.Process.Signal(sig), "send %s to dcgm-exporter; output:\n%s", sig, p.output.String())
}

// terminate stops the exporter process and waits for it to exit.
func (p *hostExporterProcess) terminate(t testing.TB) {
	t.Helper()
	p.terminateOnce.Do(func() {
		select {
		case err := <-p.done:
			if err != nil {
				t.Logf("dcgm-exporter exited before cleanup: %v\n%s", err, p.output.String())
			}
			return
		default:
		}

		if err := terminateRunningHostProcess("dcgm-exporter", p.cmd.Process, p.done, exporterTerminationPolicy); err != nil {
			t.Fatalf("%v; output:\n%s", err, p.output.String())
		}
	})
}

// terminateRunningHostProcess requests a graceful exit, then bounds the forced-kill path.
// Callers first check whether their child exited before cleanup begins.
func terminateRunningHostProcess(name string, process *os.Process, done <-chan error, policy hostProcessTerminationPolicy) error {
	if process == nil {
		return fmt.Errorf("%s process was not started", name)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		select {
		case exitErr := <-done:
			if exitErr == nil {
				return nil
			}
			return fmt.Errorf("%s exited before SIGTERM could be sent: %w", name, exitErr)
		case <-time.After(policy.KillTimeout):
			return fmt.Errorf("send SIGTERM to %s: %w", name, err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s shutdown failed: %w", name, err)
		}
		return nil
	case <-time.After(policy.GracefulTimeout):
		if processExitedWithoutWait(process) {
			return nil
		}
	}
	killErr := process.Kill()
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return fmt.Errorf("kill %s after graceful shutdown timeout: %w", name, killErr)
	}
	return waitForForcedHostProcessExit(name, done, policy, killErr)
}

// waitForForcedHostProcessExit distinguishes a child that exited gracefully
// while cleanup was checking it from one that needed a forced exit.
func waitForForcedHostProcessExit(name string, done <-chan error, policy hostProcessTerminationPolicy, killErr error) error {
	select {
	case exitErr := <-done:
		if errors.Is(killErr, os.ErrProcessDone) && exitErr == nil {
			return nil
		}
		return fmt.Errorf("%s did not shut down within %s; forced kill returned %v", name, policy.GracefulTimeout, exitErr)
	case <-time.After(policy.KillTimeout):
		return fmt.Errorf("%s did not exit within %s after SIGKILL", name, policy.KillTimeout)
	}
}

// processExitedWithoutWait reports whether /proc says the child is already gone.
func processExitedWithoutWait(process *os.Process) bool {
	if process == nil || process.Pid <= 0 {
		return false
	}
	// #nosec G703 -- process is a child started by this test harness, so its PID is not user input.
	_, err := os.Stat(fmt.Sprintf("/proc/%d", process.Pid))
	return os.IsNotExist(err)
}

func TestTerminateRunningHostProcessKillsHungChild(t *testing.T) {
	if mode := os.Getenv("HOST_TERMINATION_HELPER"); mode != "" {
		switch mode {
		case "graceful":
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, syscall.SIGTERM)
			fmt.Fprintln(os.Stdout, "ready")
			<-signals
			return
		case "hung":
			signal.Ignore(syscall.SIGTERM)
			fmt.Fprintln(os.Stdout, "ready")
			select {}
		default:
			t.Fatalf("unknown host termination helper mode %q", mode)
		}
	}

	cmd, done := startHostTerminationHelper(t, "hung")
	policy := hostProcessTerminationPolicy{GracefulTimeout: hostTerminationTestGracefulTimeout, KillTimeout: hostTerminationTestKillTimeout}
	started := time.Now()
	terminationErr := terminateRunningHostProcess("hung test helper", cmd.Process, done, policy)
	require.ErrorContains(t, terminationErr, "forced kill returned")
	require.Less(t, time.Since(started), policy.GracefulTimeout+policy.KillTimeout)
}

func TestTerminateRunningHostProcessAllowsGracefulChild(t *testing.T) {
	cmd, done := startHostTerminationHelper(t, "graceful")
	policy := hostProcessTerminationPolicy{GracefulTimeout: time.Second, KillTimeout: time.Second}
	require.NoError(t, terminateRunningHostProcess("graceful test helper", cmd.Process, done, policy))
}

func TestWaitForForcedHostProcessExitAllowsAlreadyReapedGracefulChild(t *testing.T) {
	done := make(chan error, 1)
	done <- nil
	policy := hostProcessTerminationPolicy{GracefulTimeout: time.Second, KillTimeout: time.Second}

	require.NoError(t, waitForForcedHostProcessExit("graceful test helper", done, policy, os.ErrProcessDone))
}

func startHostTerminationHelper(t testing.TB, mode string) (*exec.Cmd, <-chan error) {
	t.Helper()
	// #nosec G204,G702 -- this test re-executes the current test binary with a fixed test selector.
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminateRunningHostProcessKillsHungChild$")
	cmd.Env = append(os.Environ(), "HOST_TERMINATION_HELPER="+mode)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", ready)
	return cmd, done
}

// running reports whether the exporter process has not exited yet.
func (p *hostExporterProcess) running() error {
	select {
	case err := <-p.done:
		if err == nil {
			return fmt.Errorf("dcgm-exporter exited before serving metrics; output:\n%s", p.output.String())
		}
		return fmt.Errorf("dcgm-exporter exited before serving metrics: %w\n%s", err, p.output.String())
	default:
		return nil
	}
}

// startExporterAndWait starts the product exporter and returns its first metrics scrape.
func startExporterAndWait(t testing.TB, metricsURL string, args ...string) (*hostExporterProcess, string) {
	return startExporterAndWaitWithEnv(t, metricsURL, nil, args...)
}

func startExporterAndWaitWithEnv(t testing.TB, metricsURL string, envOverrides []string, args ...string) (*hostExporterProcess, string) {
	t.Helper()
	hasCollectInterval := false
	for _, arg := range args {
		if arg == "-c" || arg == "--collect-interval" {
			hasCollectInterval = true
			break
		}
	}
	if !hasCollectInterval {
		args = append([]string{"-c", "1000"}, args...)
	}
	process := startExporterProcessWithEnv(t, envOverrides, args...)
	metricsResp, err := retryMetrics(metricsURL, process)
	if err != nil {
		t.Fatalf("read metrics from dcgm-exporter: %v\n%s", err, process.output.String())
	}
	return process, metricsResp
}

// retryMetrics waits until the exporter serves a non-empty metrics response.
func retryMetrics(metricsURL string, process *hostExporterProcess) (string, error) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if err := process.running(); err != nil {
			return "", err
		}
		resp, statusCode, err := fetchRaw(metricsURL)
		if err == nil && statusCode == http.StatusOK && len(resp) > 0 {
			return resp, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := process.running(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("timed out waiting for metrics from %s", metricsURL)
}

// testLogWriter forwards reusable contract skip messages into the active test log.
type testLogWriter struct {
	t testing.TB
}

// Write records a shared contract diagnostic without failing the active test.
func (w testLogWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// getRandomAvailablePort reserves a unique local TCP port for a host exporter instance.
func getRandomAvailablePort(t testing.TB) int {
	randomPortMutex.Lock()
	defer randomPortMutex.Unlock()
	t.Helper()
retry:
	addr, err := net.ResolveTCPAddr("tcp", ":0")
	require.NoError(t, err)
	l, err := net.ListenTCP("tcp", addr)
	require.NoError(t, err)
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if _, exist := usedPorts[port]; exist {
		goto retry
	}
	usedPorts[port] = struct{}{}
	return port
}

// validateHostDefaultMetrics parses a host scrape and checks default-counter rows when emitted.
func validateHostDefaultMetrics(t testing.TB, metricsResp string) {
	t.Helper()
	require.NotEmpty(t, metricsResp)

	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(metricsResp))
	require.NoError(t, err)
	require.Greater(t, len(families), 0, "expected number of metrics more than 0")

	defaultCounters, err := os.ReadFile("./testdata/default-counters.csv")
	require.NoError(t, err)
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(defaultCounters))
	require.NoError(t, err)
	require.NoError(t, metriccontract.ValidateDefaultCounterRows(
		families,
		rows,
		metriccontract.DefaultCounterOptions{SkipWriter: testLogWriter{t: t}},
	))
}

// httpGet performs a GET request and returns the body, status code, and transport error.
func httpGet(t testing.TB, url string, customClient ...*http.Client) (string, int, error) {
	t.Helper()

	client := http.DefaultClient

	if len(customClient) > 0 {
		client = customClient[0]
	}

	resp, err := client.Get(url)
	if err != nil {
		return "", -1, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", -1, err
	}
	return string(body), resp.StatusCode, nil
}

// fetchRaw performs a GET request without requiring a testing.TB.
func fetchRaw(url string) (string, int, error) {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", -1, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(body), resp.StatusCode, nil
}

// newRequestWithBasicAuth creates an HTTP request with a Basic Authorization header.
func newRequestWithBasicAuth(t testing.TB, username, password, method string, url string, body io.Reader) *http.Request {
	t.Helper()
	auth := username + ":" + password
	authorizationValue := base64.StdEncoding.EncodeToString([]byte(auth))
	req, err := http.NewRequest(method, url, body)
	require.NoError(t, err)
	req.Header.Add("Authorization", "Basic "+authorizationValue)
	return req
}
