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

// Package main provides the e2e validation CLI entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/testutils/childcover"
)

// TestMain reuses this executable as the CLI and a controlled native suite.
func TestMain(m *testing.M) {
	switch os.Getenv("E2E_PROCESS_TEST_ROLE") {
	case "cli":
		if err := os.Setenv("E2E_PROCESS_TEST_ROLE", "suite"); err != nil {
			panic(err)
		}
		code := run()
		if err := childcover.Flush(m, os.Getenv("E2E_PROCESS_TEST_COVERDIR")); err != nil {
			panic(err)
		}
		os.Exit(code)
	case "suite":
		os.Exit(controlledSuiteExitCode())
	}
	os.Exit(m.Run())
}

// controlledSuiteExitCode supplies a predictable downstream suite result.
func controlledSuiteExitCode() int {
	if len(os.Args) > 1 && os.Args[1] == "-test.list" {
		listControlledStaticTests()
		return 0
	}

	if os.Getenv("E2E_PROCESS_TEST_FAILURE") == "true" {
		fmt.Fprintln(os.Stderr, "controlled native assertion failed")
		return 1
	}
	fmt.Println("controlled native assertion passed")
	return 0
}

// listControlledStaticTests lets the fixture pass the CLI's catalog check.
func listControlledStaticTests() {
	for _, entry := range scenario.Catalog {
		if entry.Suite != scenario.SuiteStatic {
			continue
		}
		for _, name := range strings.Split(entry.ResultName, ",") {
			fmt.Println(strings.TrimSpace(name))
		}
	}
}

// processExitCase describes expected process status, output and marker behavior.
type processExitCase struct {
	name    string
	args    []string
	failure bool
	code    int
	want    []string
	markers bool
}

// TestProcessExitCodes checks CLI process exits and markers using controlled suite statuses.
func TestProcessExitCodes(t *testing.T) {
	executable, root, bin := prepareProcessFixture(t)

	for _, tc := range []processExitCase{
		{
			name: "success",
			args: []string{"--scenario", "static/paths"},
			want: []string{"controlled native assertion passed", "Run passed"},
		},
		{
			name: "success markers",
			args: []string{"--scenario", "static/paths", "--result-markers"},
			want: []string{
				"&&&& RUNNING dcgm_exporter_e2e_static",
				"&&&& PASSED dcgm_exporter_e2e_static",
			},
			markers: true,
		},
		{
			name: "marker opt out",
			args: []string{"--scenario", "static/paths", "--result-markers", "--no-result-markers"},
			want: []string{"controlled native assertion passed", "Run passed"},
		},
		{
			name: "setup failure",
			args: []string{"--suite", "static", "--scenario", "host/startupMetrics"},
			code: 10,
			want: []string{"no runnable catalog scenarios"},
		},
		{
			name:    "scenario failure",
			args:    []string{"--scenario", "static/paths", "--result-markers"},
			failure: true,
			code:    20,
			want: []string{
				"controlled native assertion failed",
				"&&&& FAILED dcgm_exporter_e2e_static",
			},
			markers: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runProcessExitCase(t, executable, root, bin, tc)
		})
	}
}

// prepareProcessFixture copies this executable into an isolated prebuilt suite directory.
func prepareProcessFixture(t *testing.T) (string, string, string) {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}

	fixtureDir, err := os.OpenRoot(bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixtureDir.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := fixtureDir.WriteFile("dcgm-exporter-static.test", contents, 0o700); err != nil {
		t.Fatal(err)
	}

	return executable, root, bin
}

// runProcessExitCase invokes the real CLI entrypoint with isolated fixture controls.
func runProcessExitCase(t *testing.T, executable, root, bin string, tc processExitCase) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := append([]string{"tests", "--bootstrap=skip", "--prebuilt-binaries", "--tools-dir", bin}, tc.args...)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = root
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "E2E_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "E2E_PROCESS_TEST_ROLE=cli", fmt.Sprintf("E2E_PROCESS_TEST_FAILURE=%t", tc.failure))

	// The child leaves through the CLI's exit path, so it saves its coverage
	// data into the parent's folder itself.
	coverDir, err := childcover.Dir()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, "E2E_PROCESS_TEST_COVERDIR="+coverDir)

	output, code := captureProcessResult(t, cmd)
	assertProcessResult(t, tc, output, code)
}

// captureProcessResult preserves combined output and the operating system's exit status.
func captureProcessResult(t *testing.T, cmd *exec.Cmd) (string, int) {
	t.Helper()

	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("start CLI: %v\n%s", err, output)
	}
	return string(output), exit.ExitCode()
}

// assertProcessResult checks completion, original failure output and marker controls.
func assertProcessResult(t *testing.T, tc processExitCase, output string, code int) {
	t.Helper()

	if tc.code != 0 && strings.Contains(output, "Run passed") {
		t.Errorf("failed run reported success\n%s", output)
	}
	if code != tc.code {
		t.Fatalf("exit = %d, want %d\n%s", code, tc.code, output)
	}

	for _, want := range tc.want {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q\n%s", want, output)
		}
	}
	if strings.Contains(output, "&&&& ") != tc.markers {
		t.Errorf("unexpected marker mode\n%s", output)
	}
}
