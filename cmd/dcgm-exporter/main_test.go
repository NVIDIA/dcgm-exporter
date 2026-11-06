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

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// argsVariable carries the arguments for the child process that runs main.
const argsVariable = "DCGM_EXPORTER_TEST_MAIN_ARGS"

// TestMain lets a test run the real main function in a child process, because
// main ends the process with its exit status.
func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv(argsVariable); ok {
		os.Args = append([]string{"dcgm-exporter"}, strings.Split(args, "\x1f")...)
		main()
		os.Exit(0)
	}

	os.Exit(m.Run())
}

// runMain runs main with args and returns its exit status, stdout and stderr.
func runMain(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	// #nosec G204 G702 -- the test re-executes its own binary.
	command := exec.Command(os.Args[0])
	command.Env = []string{argsVariable + "=" + strings.Join(args, "\x1f"), "PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), stdout.String(), stderr.String()
	}

	require.NoError(t, err)
	return 0, stdout.String(), stderr.String()
}

func TestMainExitStatuses(t *testing.T) {
	valid := filepath.Join(t.TempDir(), "valid.yaml")
	require.NoError(t, os.WriteFile(valid, []byte("version: 2\n"), 0o600))
	invalid := filepath.Join(t.TempDir(), "invalid.yaml")
	require.NoError(t, os.WriteFile(invalid, []byte("version: 2\nserver: {read_timeout: 0s}\n"), 0o600))
	csv := filepath.Join(t.TempDir(), "metrics.csv")
	require.NoError(t, os.WriteFile(csv, []byte("DCGM_FI_DEV_GPU_TEMP,gauge,Temperature\n"), 0o600))

	status, stdout, stderr := runMain(t, "--collectors", csv, "config", "validate", valid)
	assert.Equal(t, 0, status, stderr)
	assert.Contains(t, stdout, "valid")

	status, stdout, stderr = runMain(t, "config", "validate", invalid)
	assert.Equal(t, 1, status, "invalid configuration")
	assert.Empty(t, stdout, "a run with errors writes nothing to stdout")
	assert.Contains(t, stderr, "server.read_timeout")
	assert.Equal(t, 1, strings.Count(stderr, "error    "), "the problem is written once, not again by main")
	assert.NotContains(t, stderr, "configuration is invalid", "main does not repeat the reported failure")

	status, stdout, stderr = runMain(t, "config", "validate", "--bogus")
	assert.Equal(t, 2, status, "wrong usage of a config command")
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "bogus")

	status, _, _ = runMain(t, "--bogus-flag")
	assert.Equal(t, 1, status, "startup usage errors keep their status")
}

func TestMainEscapesStartupErrors(t *testing.T) {
	// The pattern fails to compile; the compiler's message repeats it.
	status, stdout, stderr := runMain(t, "--kubernetes", "--kubernetes-pod-label-allowlist-regex", "[\x1b[2J",
		"--collect-interval", "0")
	assert.Equal(t, 1, status)
	assert.Empty(t, stdout)
	assert.NotContains(t, stderr, "\x1b", "control characters are written as escapes")
	assert.Contains(t, stderr, `\x1b[2J`)
	assert.Equal(t, 2, strings.Count(stderr, "ERROR"), "each problem is its own record")
}
