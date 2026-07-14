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
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
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
	exporterTerminateTimeout = 10 * time.Second
	exporterKillTimeout      = 5 * time.Second
)

var exporterBinaryFlag = flag.String(
	"exporter-binary",
	os.Getenv("E2E_EXPORTER_BINARY"),
	"path to the dcgm-exporter product binary under test",
)

var dcgmProbeBinaryFlag = flag.String(
	"dcgm-probe-binary",
	"",
	"path to the direct-DCGM probe used by NVML-injection tests",
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
	t.Helper()
	processOutput := &lockedBuffer{}
	// #nosec G204 -- requireExporterBinary requires an explicit flag/env path and validates absolute path, existence, non-directory, and executable bit.
	cmd := exec.Command(requireExporterBinary(t), args...)
	cmd.Env = hostExporterEnv()
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
	env := hostExporterEnv()
	require.Contains(t, env, "GOCOVERDIR=/tmp/cover")
	require.Contains(t, env, "E2E_REQUIRE_VSOCK=1")
	require.Contains(t, env, "NVML_INJECTION_MODE=True")
	require.Contains(t, env, "NVML_YAML_FILE=/tmp/injection.yaml")
	for _, item := range env {
		require.NotContains(t, item, "DCGM_FI_DEV_GPU_TEMP")
	}
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

		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case err := <-p.done:
			require.NoErrorf(t, err, "dcgm-exporter shutdown failed; output:\n%s", p.output.String())
		case <-time.After(exporterTerminateTimeout):
			if p.processExitedWithoutWait() {
				t.Log("dcgm-exporter process exited before cleanup observed Wait completion")
				return
			}
			if p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			select {
			case err := <-p.done:
				t.Fatalf("dcgm-exporter did not shut down within timeout; forced kill returned %v; output:\n%s", err, p.output.String())
			case <-time.After(exporterKillTimeout):
				if p.processExitedWithoutWait() {
					t.Log("dcgm-exporter process exited after SIGKILL before cleanup observed Wait completion")
					return
				}
				t.Fatalf("dcgm-exporter did not exit after SIGKILL; output:\n%s", p.output.String())
			}
		}
	})
}

// processExitedWithoutWait reports whether /proc says the child is already gone.
func (p *hostExporterProcess) processExitedWithoutWait() bool {
	if p.cmd.Process == nil || p.cmd.Process.Pid <= 0 {
		return false
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", p.cmd.Process.Pid))
	return os.IsNotExist(err)
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
	process := startExporterProcess(t, args...)
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
