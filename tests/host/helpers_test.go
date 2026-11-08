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
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
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
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

var randomPortMutex sync.Mutex

var usedPorts = map[int]struct{}{}

const (
	// hostExporterCleanupTimeout is the --dcgm-cleanup-timeout every host
	// exporter starts with, so a stop that waits on DCGM ends well inside the
	// test's graceful stop budget instead of the 5m product default.
	hostExporterCleanupTimeout         = 30 * time.Second
	exporterShutdownMargin             = 15 * time.Second
	exporterKillTimeout                = 5 * time.Second
	hostTerminationTestGracefulTimeout = 25 * time.Millisecond
	hostTerminationTestKillTimeout     = time.Second
)

// hostProcessTerminationPolicy bounds test cleanup. The graceful part allows
// the exporter's wait for DCGM to release its watches, which
// hostExporterCleanupTimeout bounds, plus a margin for draining scrapes and
// closing DCGM.
type hostProcessTerminationPolicy struct {
	GracefulTimeout time.Duration
	KillTimeout     time.Duration
}

var exporterTerminationPolicy = hostProcessTerminationPolicy{
	GracefulTimeout: hostExporterCleanupTimeout + exporterShutdownMargin,
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

// defaultCollectorsFile derives the host runner's proven metric set from the
// product-owned defaults. The non-root GPU runner cannot watch the expanded
// default preset, so live host tests keep a fixed subset of the defaults.
func defaultCollectorsFile(t testing.TB) string {
	t.Helper()
	canonical := canonicalDefaultCollectorsFile(t)
	contents, err := os.ReadFile(canonical)
	require.NoError(t, err)

	// These newer default fields are outside the non-root host runner's baseline.
	excluded := map[string]struct{}{
		"DCGM_FI_DEV_PCIE_REPLAY_TOTAL": {},
		"DCGM_FI_DEV_FB_RESERVED_BYTES": {},
		"DCGM_FI_DEV_FB_CAPACITY_BYTES": {},
		// The non-root host runner keeps its pinned field set; NVLink throughput
		// needs NVLink hardware, which that runner does not provide. No in-repo
		// automated test covers this row on the default path.
		"DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND": {},
		"DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO":              {},
		"DCGM_FI_PROF_TENSOR_UTIL_RATIO":                 {},
		"DCGM_FI_PROF_DRAM_UTIL_RATIO":                   {},
		"DCGM_FI_PROF_PCIE_TX_BYTES":                     {},
		"DCGM_FI_PROF_PCIE_RX_BYTES":                     {},
	}
	var hostCounters strings.Builder
	for _, line := range strings.SplitAfter(string(contents), "\n") {
		field, _, _ := strings.Cut(line, ",")
		if _, skip := excluded[strings.TrimSpace(field)]; !skip {
			hostCounters.WriteString(line)
		}
	}
	path := filepath.Join(t.TempDir(), "host-counters.csv")
	require.NoError(t, os.WriteFile(path, []byte(hostCounters.String()), 0o600))
	return path
}

// canonicalDefaultCollectorsFile resolves the product CSV from Bazel runfiles
// or the package working directory used by direct Go tests.
func canonicalDefaultCollectorsFile(t testing.TB) string {
	t.Helper()

	var candidates []string
	if testSrcDir, workspace := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); testSrcDir != "" && workspace != "" {
		candidates = append(candidates, filepath.Join(testSrcDir, workspace, "etc", "default-counters.csv"))
	}
	candidates = append(candidates, filepath.Join("..", "..", "etc", "default-counters.csv"))

	for _, candidate := range candidates {
		// #nosec G703 -- candidates are this test's fixed relative path or Bazel-provided runfiles path; they are read only.
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		path, err := filepath.Abs(candidate)
		require.NoError(t, err)
		return path
	}

	t.Fatalf("canonical default collectors file not found; checked %s", strings.Join(candidates, ", "))
	return ""
}

// TestHostDefaultCollectorsFile verifies the host field set has 18 rows and excludes profiling counters.
func TestHostDefaultCollectorsFile(t *testing.T) {
	contents, err := os.ReadFile(defaultCollectorsFile(t))
	require.NoError(t, err)
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(contents))
	require.NoError(t, err)
	require.Len(t, rows, 18, "host tests must use the established non-root runner field set (18 rows)")
	for _, row := range rows {
		require.NotContains(t, row.Name, "DCGM_FI_PROF_")
	}
}

// writeCounters writes a counters file at path with one CSV row per entry
// of rows, each "field, type, help".
func writeCounters(t testing.TB, path string, rows ...string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o600))
}

// gaugeRows returns one gauge counters row for each field name.
func gaugeRows(fields ...string) []string {
	rows := make([]string, 0, len(fields))
	for _, field := range fields {
		rows = append(rows, field+", gauge, Test fixture field.")
	}
	return rows
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

// hostCleanupTimeoutArgs sets the DCGM cleanup timeout of a host exporter to
// hostExporterCleanupTimeout. Callers put it before their own arguments.
var hostCleanupTimeoutArgs = []string{"--dcgm-cleanup-timeout", hostExporterCleanupTimeout.String()}

// startExporterProcessWithEnv starts the product exporter with explicit
// environment overrides and the host DCGM cleanup timeout.
func startExporterProcessWithEnv(t testing.TB, envOverrides []string, args ...string) *hostExporterProcess {
	t.Helper()
	args = append(slices.Clone(hostCleanupTimeoutArgs), args...)

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

// mergeEnv replaces each overridden key once and appends its requested value.
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

// hostExporterEnv passes only the host variables required by exporter integration tests.
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

// TestHostExporterEnvFiltersUnrelatedDCGMVars keeps unrelated DCGM field variables out of the child environment.
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

// TestMergeEnvOverridesExistingValues replaces prior values without dropping unrelated entries.
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

// TestTerminateRunningHostProcessKillsHungChild proves a hung child is force-killed within the configured bound.
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

// TestTerminateRunningHostProcessAllowsGracefulChild accepts a child that exits after SIGTERM.
func TestTerminateRunningHostProcessAllowsGracefulChild(t *testing.T) {
	cmd, done := startHostTerminationHelper(t, "graceful")
	policy := hostProcessTerminationPolicy{GracefulTimeout: time.Second, KillTimeout: time.Second}
	require.NoError(t, terminateRunningHostProcess("graceful test helper", cmd.Process, done, policy))
}

// TestWaitForForcedHostProcessExitAllowsAlreadyReapedGracefulChild accepts a completed child when kill finds it gone.
func TestWaitForForcedHostProcessExitAllowsAlreadyReapedGracefulChild(t *testing.T) {
	done := make(chan error, 1)
	done <- nil
	policy := hostProcessTerminationPolicy{GracefulTimeout: time.Second, KillTimeout: time.Second}

	require.NoError(t, waitForForcedHostProcessExit("graceful test helper", done, policy, os.ErrProcessDone))
}

// startHostTerminationHelper starts a test subprocess and waits for its readiness line.
func startHostTerminationHelper(t testing.TB, mode string) (*exec.Cmd, <-chan error) {
	t.Helper()
	// #nosec G204,G702 -- this test re-executes the current test binary with a fixed test selector.
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminateRunningHostProcessKillsHungChild$")
	cmd.Env = terminationHelperEnvironment(mode)
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

// terminationHelperEnvironment removes the race detector's default exit delay
// from helper children so the test's graceful-shutdown deadline measures the
// signal handler rather than race-runtime reporting time.
func terminationHelperEnvironment(mode string) []string {
	env := mergeEnv(os.Environ(), []string{"HOST_TERMINATION_HELPER=" + mode})
	options := strings.Fields(os.Getenv("GORACE"))
	filtered := options[:0]
	for _, option := range options {
		if !strings.HasPrefix(option, "atexit_sleep_ms=") {
			filtered = append(filtered, option)
		}
	}
	filtered = append(filtered, "atexit_sleep_ms=0")
	return mergeEnv(env, []string{"GORACE=" + strings.Join(filtered, " ")})
}

// running reports whether the exporter process has not exited yet.
func (p *hostExporterProcess) running() error {
	select {
	case err := <-p.done:
		// Put the exit value back so terminate still sees that the process exited.
		p.done <- err

		if err == nil {
			return fmt.Errorf("dcgm-exporter exited before serving metrics; output:\n%s", p.output.String())
		}
		return fmt.Errorf("dcgm-exporter exited before serving metrics: %w\n%s", err, p.output.String())
	default:
		return nil
	}
}

// startExporterAndWaitForFamilies starts the product exporter with exactly the
// given arguments and environment overrides and returns its first scrape that
// holds every named family, or any DCGM family when none is named. It adds no
// collection interval, so the interval comes from the config file, the
// environment, or the product default.
func startExporterAndWaitForFamilies(
	t testing.TB, metricsURL string, envOverrides []string, families []string, args ...string,
) (*hostExporterProcess, string) {
	t.Helper()
	process := startExporterProcessWithEnv(t, envOverrides, args...)
	metricsResp, err := retryMetricsWithFamilies(metricsURL, process, families)
	if err != nil {
		t.Fatalf("read metrics from dcgm-exporter: %v\n%s", err, process.output.String())
	}
	return process, metricsResp
}

// startFastExporterAndWaitForHealth starts the exporter with a one-second
// collection interval and returns the first non-empty response of its health URL.
func startFastExporterAndWaitForHealth(t testing.TB, healthURL string, args ...string) (*hostExporterProcess, string) {
	t.Helper()
	process := startExporterProcess(t, append(slices.Clone(fastCollectionArgs), args...)...)
	healthResp, err := retryNonEmptyResponse(healthURL, process)
	if err != nil {
		t.Fatalf("read health from dcgm-exporter: %v\n%s", err, process.output.String())
	}
	return process, healthResp
}

// fastCollectionArgs asks for a one-second collection interval. Tests that need
// quick samples and do not check the interval pass it explicitly.
var fastCollectionArgs = []string{"--collect-interval", "1000"}

// startFastExporterAndWait is startExporterAndWaitForFamilies with a one-second
// collection interval and any DCGM family accepted.
func startFastExporterAndWait(t testing.TB, metricsURL string, args ...string) (*hostExporterProcess, string) {
	t.Helper()
	return startExporterAndWaitForFamilies(t, metricsURL, nil, nil, append(slices.Clone(fastCollectionArgs), args...)...)
}

// startFastCSVExporterAndWait starts the exporter with a one-second collection
// interval and collectorsPath as its CSV, and returns its first scrape that
// holds every row of that CSV the DCGM field support evidence lists as
// supported. Rows such as profiling fields have no value in their first DCGM
// sample, so an earlier scrape can lack them. Without evidence it waits for
// any DCGM family.
func startFastCSVExporterAndWait(t testing.TB, metricsURL, collectorsPath string, args ...string) (*hostExporterProcess, string) {
	t.Helper()

	args = append([]string{"--collectors", collectorsPath}, args...)
	return startExporterAndWaitForFamilies(t, metricsURL, nil, hostSupportedFamilies(t, collectorsPath),
		append(slices.Clone(fastCollectionArgs), args...)...)
}

// hostSupportedFamilies returns the metric rows of the CSV at collectorsPath
// that the DCGM field support evidence lists as supported. Label rows are
// left out because they are labels, not families.
func hostSupportedFamilies(t testing.TB, collectorsPath string) []string {
	t.Helper()

	contents, err := os.ReadFile(collectorsPath)
	require.NoError(t, err)
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(contents))
	require.NoError(t, err)

	support := hostFieldSupport(t)
	var families []string
	for _, row := range rows {
		if metriccontract.IsLabelRow(row) {
			continue
		}
		if status, _ := support.Classify(row.Name); status == metriccontract.FieldSupported {
			families = append(families, row.Name)
		}
	}
	return families
}

// startFastExporterAndWaitWithEnv is startFastExporterAndWait with environment overrides.
func startFastExporterAndWaitWithEnv(t testing.TB, metricsURL string, envOverrides []string, args ...string) (*hostExporterProcess, string) {
	t.Helper()
	return startExporterAndWaitForFamilies(t, metricsURL, envOverrides, nil, append(slices.Clone(fastCollectionArgs), args...)...)
}

// exporterRun is what one run of the product binary left behind.
type exporterRun struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runExporterCommand runs the product binary with args and the given extra
// environment and waits for it to exit within timeout. It returns the
// separately captured stdout and stderr and the exit status; any exit status
// is accepted, so callers assert the one they expect.
func runExporterCommand(t testing.TB, timeout time.Duration, envOverrides []string, args ...string) exporterRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stdout, stderr lockedBuffer
	// #nosec G204 -- requireExporterBinary requires an explicit flag/env path and validates absolute path, existence, non-directory, and executable bit.
	cmd := exec.CommandContext(ctx, requireExporterBinary(t), args...)
	cmd.Env = mergeEnv(hostExporterEnv(), envOverrides)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = exporterKillTimeout

	err := cmd.Run()
	require.NoErrorf(t, ctx.Err(), "dcgm-exporter %v was still running after %s; stderr:\n%s", args, timeout, stderr.String())
	run := exporterRun{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return run
	}
	var exitErr *exec.ExitError
	require.ErrorAsf(t, err, &exitErr, "dcgm-exporter %v did not run to an exit status; stderr:\n%s", args, stderr.String())
	run.ExitCode = exitErr.ExitCode()
	return run
}

// runExporterExpectingStartFailure runs the product exporter with args and the
// given extra environment and requires it to exit with a non-zero status within
// timeout. It returns the run so callers can check the error message, the exit
// status and that nothing was written to stdout.
func runExporterExpectingStartFailure(t testing.TB, timeout time.Duration, envOverrides []string, args ...string) exporterRun {
	t.Helper()
	run := runExporterCommand(t, timeout, envOverrides, args...)
	require.NotZerof(t, run.ExitCode, "dcgm-exporter %v exited with status 0; stdout:\n%s", args, run.Stdout)
	return run
}

// retryNonEmptyResponse waits until the endpoint serves a non-empty response. Use it
// for endpoints such as /health; for /metrics wait for a family with retryMetricsWithFamilies.
func retryNonEmptyResponse(metricsURL string, process *hostExporterProcess) (string, error) {
	return retryResponse(metricsURL, process, func(body string) bool { return len(body) > 0 })
}

// retryMetricsWithFamilies waits until a scrape parses and holds every named
// family, or any DCGM_ family when none is named.
func retryMetricsWithFamilies(metricsURL string, process *hostExporterProcess, families []string) (string, error) {
	return retryResponse(metricsURL, process, func(body string) bool {
		parsed, err := parseMetricFamilies(body)
		if err != nil {
			return false
		}
		if len(families) == 0 {
			for name := range parsed {
				if strings.HasPrefix(name, "DCGM_") {
					return true
				}
			}
			return false
		}
		return includesAllMetricFamilies(parsed, families)
	})
}

// retryResponse polls a URL until accept approves a 200 response body or the
// startup deadline passes. It stops early when the exporter exits.
func retryResponse(url string, process *hostExporterProcess, accept func(body string) bool) (string, error) {
	return retryResponseWith(url, func() (string, int, error) { return fetchRaw(url) }, process, accept)
}

// retryResponseWith is retryResponse for any fetch, such as an HTTPS request
// with credentials. It stops early when the exporter exits and names the URL
// and the last fetch error and status when the deadline passes.
func retryResponseWith(url string, fetch func() (string, int, error), process *hostExporterProcess, accept func(body string) bool) (string, error) {
	deadline := time.Now().Add(startupWaitTimeout)
	var lastErr error
	lastStatus := -1
	for time.Now().Before(deadline) {
		if err := process.running(); err != nil {
			return "", err
		}

		resp, statusCode, err := fetch()
		if err == nil && statusCode == http.StatusOK && accept(resp) {
			return resp, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastStatus = statusCode
		}
		time.Sleep(startupPollInterval)
	}

	if err := process.running(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("timed out after %s waiting for an accepted response from %s (last status: %d, last error: %v)", startupWaitTimeout, url, lastStatus, lastErr)
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

// validateHostDefaultMetrics checks the non-root host preset against a host scrape.
func validateHostDefaultMetrics(t testing.TB, metricsResp string) {
	t.Helper()
	validateHostMetricsFromCollectors(t, metricsResp, defaultCollectorsFile(t))
}

// validateHostMetricsFromCollectors checks a scrape against the CSV used to
// start the exporter: exactly its rows, with its types and HELP text. A
// missing row fails when the DCGM field support evidence says DCGM returns a
// value for it.
func validateHostMetricsFromCollectors(t testing.TB, metricsResp, collectorsPath string) {
	t.Helper()

	contents, err := os.ReadFile(collectorsPath)
	require.NoError(t, err)
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(contents))
	require.NoError(t, err)

	families, err := metriccontract.ParseText([]byte(metricsResp))
	require.NoError(t, err)
	require.NoError(t, metriccontract.ValidateAtLeastOneDCGMGPUFamily(families))
	require.NoError(t, metriccontract.ValidateDCGMSamples(families))
	require.NoError(t, metriccontract.ValidateDefaultCounterRows(
		families,
		rows,
		metriccontract.DefaultCounterOptions{
			FieldSupport: hostFieldSupport(t),
			ExactCSV:     true,
			SkipWriter:   testLogWriter{t: t},
		},
	))
}

// hostFieldSupport parses the E2E_DCGM_FIELD_SUPPORT evidence. The evidence
// covers the shipped default CSV, so its names are checked against that file;
// the host runner's CSV is a subset of it.
func hostFieldSupport(t testing.TB) metriccontract.FieldSupport {
	t.Helper()

	contents, err := os.ReadFile(canonicalDefaultCollectorsFile(t))
	require.NoError(t, err)
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(contents))
	require.NoError(t, err)

	support, err := metriccontract.ParseFieldSupport(os.Getenv(metriccontract.FieldSupportEnv), rows)
	require.NoError(t, err, "DCGM field support evidence should name enabled shipped default counters")
	return support
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
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", -1, fmt.Errorf("build request for %s: %w", url, err)
	}
	return fetchWith(&http.Client{Timeout: 10 * time.Second}, req)
}

// fetchWith sends req with client and returns the body and status code. The
// status code is -1 when no response arrived.
func fetchWith(client *http.Client, req *http.Request) (string, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return "", -1, fmt.Errorf("get %s: %w", req.URL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("read %s: %w", req.URL, err)
	}
	return string(body), resp.StatusCode, nil
}

// The basic auth user and password that testdata/web-config.yml accepts.
const (
	webConfigUser     = "alice"
	webConfigPassword = "password"
)

// newTLSTestClient returns a client for the exporter's self-signed test
// certificate in testdata/tlsCertificate.crt.
func newTLSTestClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				//nolint:gosec // The test connects to a temporary self-signed local server.
				InsecureSkipVerify: true,
			},
		},
	}
}

// familiesShape is scrapeShape for a scrape that is already parsed.
func familiesShape(families map[string]*io_prometheus_client.MetricFamily) map[string][]string {
	shape := map[string][]string{}
	for name, family := range families {
		if !strings.HasPrefix(name, "DCGM_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make([]string, 0, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels = append(labels, label.GetName())
			}
			slices.Sort(labels)
			shape[name] = append(shape[name], strings.Join(labels, ","))
		}
		slices.Sort(shape[name])
	}
	return shape
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

// settledScrapes is how many scrapes in a row, startupPollInterval apart,
// must agree before a run is compared. Five span two seconds, which covers
// two one-second collection intervals.
const settledScrapes = 5

// settingBaseline is the run every case is compared with: the host counter
// file, one-second collection and nothing else, plus what nvidia-smi reports.
type settingBaseline struct {
	collectors string
	families   map[string]*io_prometheus_client.MetricFamily
	// gpus are the DCGM GPU IDs the baseline reports, in numeric order.
	gpus      []string
	inventory []nvidiasmi.GPU
}

// settingRun is one settled scrape of one exporter run, the port it listened
// on and where the scrape came from.
type settingRun struct {
	families map[string]*io_prometheus_client.MetricFamily
	target   scrapeTarget
	port     int
	// log is what the exporter had written to its output once the scrape settled.
	log string
}

// settingForm is one way to give a setting: its flag or its config key.
// args returns every exporter argument of the run, including the listen
// address port; check compares the run with the baseline. target is the
// scrape URL for a port, or nil for plain HTTP on localhost; ipv6 makes the
// run listen on an IPv6 loopback port and skips when the host has none.
type settingForm struct {
	name   string
	args   func(t testing.TB, base *settingBaseline, port int) []string
	check  func(t testing.TB, base *settingBaseline, run settingRun)
	target func(port int) scrapeTarget
	ipv6   bool
}

// startSettingBaseline asks nvidia-smi for the host's GPUs, runs the
// exporter with the host counter file and a one-second interval, and keeps
// its settled scrape.
func startSettingBaseline(t testing.TB) *settingBaseline {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	inventory, err := hostInventory.Identity(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, inventory, "nvidia-smi reports no GPU")

	base := &settingBaseline{
		collectors: defaultCollectorsFile(t),
		inventory:  inventory,
	}
	port := getRandomAvailablePort(t)
	process := startExporterProcess(t, baselineArgs(base.collectors, port)...)
	base.families = waitForSettledScrape(t, plainTarget(port), process).families
	process.terminate(t)

	// The baseline must show the exporter's own families are off by default.
	require.Empty(t, exporterFamilies(base.families), "the baseline must not serve the exporter's own families")

	base.gpus = labelValues(base.families, "gpu")
	require.NotEmpty(t, base.gpus, "the baseline reports no GPU")
	return base
}

// runSettingForms starts the exporter once per form, checks each run with the
// form's own check, then checks that every form served the same families,
// series and label names. A setting given as a flag and as a config key
// passes both forms.
func runSettingForms(t testing.TB, base *settingBaseline, forms []settingForm) {
	t.Helper()
	runs := make([]settingRun, 0, len(forms))
	for _, form := range forms {
		port := getRandomAvailablePort(t)
		if form.ipv6 {
			port = getRandomAvailableIPv6Port(t)
		}
		process := startExporterProcess(t, form.args(t, base, port)...)

		target := plainTarget(port)
		if form.target != nil {
			target = form.target(port)
		}

		run := waitForSettledScrape(t, target, process)
		run.port = port
		run.log = process.output.String()
		t.Logf("%s: %d families", form.name, len(run.families))
		form.check(t, base, run)
		process.terminate(t)
		runs = append(runs, run)
	}

	requireFormsServeSameOutput(t, forms, runs)
}

// requireFormsServeSameOutput checks every run served the same families,
// series and label names as the first run.
func requireFormsServeSameOutput(t testing.TB, forms []settingForm, runs []settingRun) {
	t.Helper()
	for i := 1; i < len(runs); i++ {
		require.Equalf(t, familiesShape(runs[0].families), familiesShape(runs[i].families),
			"%s and %s must serve the same DCGM families, series and label names", forms[0].name, forms[i].name)
		require.Equalf(t, sortedFamilyNames(runs[0].families), sortedFamilyNames(runs[i].families),
			"%s and %s must serve the same families", forms[0].name, forms[i].name)
	}
}

// checkSameDCGMShapeAsBaseline checks the run serves the baseline's DCGM
// families, series and label names; the setting changes only how they are served.
func checkSameDCGMShapeAsBaseline(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	require.Equal(t, familiesShape(base.families), familiesShape(run.families),
		"the run must serve the baseline's DCGM families, series and label names")
}

// baselineArgs are the arguments of a baseline run with collectors on port.
func baselineArgs(collectors string, port int) []string {
	return baselineArgsAt(collectors, fmt.Sprintf(":%d", port))
}

// baselineArgsAt are the arguments of a baseline run with collectors that
// listens on address.
func baselineArgsAt(collectors, address string) []string {
	return []string{
		"--collectors", collectors,
		"--collect-interval", "1000",
		"--address", address,
	}
}

// writeSettingConfig writes document as a config file and returns its path.
// JSON is valid YAML, so the document is written as JSON.
func writeSettingConfig(t testing.TB, document map[string]any) string {
	t.Helper()
	data, err := json.Marshal(document)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// prometheusOutput is the Prometheus output block listening on listen.
func prometheusOutput(listen string) map[string]any {
	return map[string]any{"type": "prometheus", "schema": "dcgm/v1", "listen": listen}
}

// scrapeTarget is a metrics URL with the client and credentials to reach it.
type scrapeTarget struct {
	url       string
	client    *http.Client
	basicAuth bool
}

// plainTarget is the plain HTTP metrics URL on port.
func plainTarget(port int) scrapeTarget {
	return scrapeTarget{url: fmt.Sprintf("http://localhost:%d/metrics", port)}
}

// tlsTarget is the HTTPS metrics URL on port with the web config's user.
func tlsTarget(port int) scrapeTarget {
	return scrapeTarget{url: fmt.Sprintf("https://localhost:%d/metrics", port), client: newTLSTestClient(), basicAuth: true}
}

// fetch returns a function that fetches the target once.
func (s scrapeTarget) fetch(t testing.TB) func() (string, int, error) {
	return func() (string, int, error) {
		if !s.basicAuth {
			return fetchRaw(s.url)
		}
		return fetchWith(s.client, newRequestWithBasicAuth(t, webConfigUser, webConfigPassword, http.MethodGet, s.url, nil))
	}
}

// waitForSettledScrape polls target until settledScrapes scrapes in a row
// parse, hold a DCGM family, and have the same families, series and label
// names, so a run is compared only once every field has its first sample.
func waitForSettledScrape(t testing.TB, target scrapeTarget, process *hostExporterProcess) settingRun {
	t.Helper()
	var previous map[string][]string
	var settled map[string]*io_prometheus_client.MetricFamily
	streak := 0
	_, err := retryResponseWith(target.url, target.fetch(t), process, func(body string) bool {
		families, err := parseMetricFamilies(body)
		if err != nil {
			streak = 0
			return false
		}

		shape := familiesShape(families)
		if len(shape) == 0 || !reflect.DeepEqual(shape, previous) {
			previous, streak = shape, 1
			return false
		}

		streak++
		settled = families
		return streak >= settledScrapes
	})
	require.NoErrorf(t, err, "wait for %d matching scrapes from %s:\n%s", settledScrapes, target.url, process.output.String())
	return settingRun{families: settled, target: target}
}

// labelValues lists the distinct values of label across every family, in
// numeric order when they are numbers.
func labelValues(families map[string]*io_prometheus_client.MetricFamily, label string) []string {
	var values []string
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			if value, ok := labelMap(metric)[label]; ok && !slices.Contains(values, value) {
				values = append(values, value)
			}
		}
	}

	slices.SortFunc(values, func(left, right string) int {
		leftNumber, leftErr := strconv.Atoi(left)
		rightNumber, rightErr := strconv.Atoi(right)
		if leftErr == nil && rightErr == nil {
			return leftNumber - rightNumber
		}
		return strings.Compare(left, right)
	})
	return values
}

// labelMap maps the label names of metric to their values.
func labelMap(metric *io_prometheus_client.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}

// firstLabel returns the value of the first of names that labels has, or "".
func firstLabel(labels map[string]string, names []string) string {
	for _, name := range names {
		if value, ok := labels[name]; ok {
			return value
		}
	}
	return ""
}

// sortedFamilyNames lists the family names of families in order.
func sortedFamilyNames(families map[string]*io_prometheus_client.MetricFamily) []string {
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// exporterFamilyPrefixes match every family of the exporter's own metrics.
var exporterFamilyPrefixes = []string{"go_", "process_", "promhttp_"}

// exporterFamilies lists the families of the exporter's own metrics.
func exporterFamilies(families map[string]*io_prometheus_client.MetricFamily) []string {
	var names []string
	for _, name := range sortedFamilyNames(families) {
		if slices.ContainsFunc(exporterFamilyPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) }) {
			names = append(names, name)
		}
	}
	return names
}
