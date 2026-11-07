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

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2/types"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// gomegaMessage is the summary of the Equal failure in the fixtures. The
// backslash-n is literal text in the fixture's compared string, not a line break.
const gomegaMessage = `Expected <string>: /dev/vsock not present\nsecond line to equal <string>: ok`

const (
	reportFixtureDir = "testdata/ginkgo"
	reportSpecBase   = "host/vsock when VSOCK is present "
	reportSourceFile = "/work/tests/host/vsock_test.go"
	reportFlagPrefix = "--ginkgo.json-report="
	noReportText     = "no Ginkgo report"
)

// TestReadGinkgoOutcome checks which specs a report lists as failed and as
// skipped, including that a spec filtered out by labels is not listed.
func TestReadGinkgoOutcome(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.json")
	require.NoError(t, os.WriteFile(garbage, []byte("{not json"), 0o600))

	tests := []struct {
		name        string
		path        string
		wantFailed  []specOutcome
		wantSkipped []specOutcome
		wantErr     string
	}{
		{
			name: "one failure",
			path: filepath.Join(reportFixtureDir, "one_failure.json"),
			wantFailed: []specOutcome{
				{Name: reportSpecBase + "other group", Location: reportSourceFile + ":19", Message: "Expected <int>: 1 to equal <int>: 2"},
			},
		},
		{
			name: "several failures",
			path: filepath.Join(reportFixtureDir, "several_failures.json"),
			wantFailed: []specOutcome{
				{Name: reportSpecBase + "reaches the hostengine", Location: reportSourceFile + ":16", Message: gomegaMessage},
				{Name: reportSpecBase + "reads the model name", Location: reportSourceFile + ":17", Message: "model name is blank extra detail"},
			},
		},
		{
			name: "timeout",
			path: filepath.Join(reportFixtureDir, "timeout.json"),
			wantFailed: []specOutcome{
				{Name: reportSpecBase + "times out", Location: reportSourceFile + ":20", Message: "A node timeout occurred"},
			},
		},
		{
			name: "skipped specs in a passing group, filtered specs left out",
			path: filepath.Join(reportFixtureDir, "skipped_in_passing_group.json"),
			wantSkipped: []specOutcome{
				{Name: reportSpecBase + "skips without image", Location: reportSourceFile + ":18", Message: "distroless image not configured"},
			},
		},
		{
			name: "failed and skipped specs in one report",
			path: filepath.Join(reportFixtureDir, "failure_and_skip.json"),
			wantFailed: []specOutcome{
				{Name: reportSpecBase + "reaches the hostengine", Location: reportSourceFile + ":22", Message: "vsock is not available"},
			},
			wantSkipped: []specOutcome{
				{Name: reportSpecBase + "skips without image", Location: reportSourceFile + ":23", Message: "distroless image not configured"},
			},
		},
		{
			name: "BeforeSuite failure takes its name from the node type",
			path: filepath.Join(reportFixtureDir, "before_suite_failure.json"),
			wantFailed: []specOutcome{
				{Name: "BeforeSuite", Location: reportSourceFile + ":16", Message: "Expected <string>: /dev/vsock to equal <string>: present"},
			},
		},
		{
			name: "panic adds the forwarded panic and the cleanup failure",
			path: filepath.Join(reportFixtureDir, "panic.json"),
			wantFailed: []specOutcome{
				{
					Name:     reportSpecBase + "panics",
					Location: reportSourceFile + ":26",
					Message:  "Test Panicked; panic: nil pointer in scraper; also: cleanup also failed",
				},
			},
		},
		{
			name: "spec skipped after an ordered-container failure is not listed",
			path: filepath.Join(reportFixtureDir, "ordered_skip.json"),
			wantFailed: []specOutcome{
				{Name: "host/vsock ordered first breaks", Location: reportSourceFile + ":31", Message: "first spec broke"},
			},
		},
		{
			name: "suite timeout",
			path: filepath.Join(reportFixtureDir, "suite_timeout.json"),
			wantFailed: []specOutcome{
				{Name: reportSpecBase + "waits", Location: reportSourceFile + ":28", Message: "A suite timeout occurred"},
			},
		},
		{
			name:    "missing report",
			path:    filepath.Join(t.TempDir(), "absent.json"),
			wantErr: "read Ginkgo report",
		},
		{
			name:    "unreadable report",
			path:    garbage,
			wantErr: "parse Ginkgo report",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := readGinkgoOutcome(tt.path)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantFailed, outcome.Failed)
			require.Equal(t, tt.wantSkipped, outcome.Skipped)
		})
	}
}

// reportingRunner returns a fake runner that answers each test binary with the
// given exit code and, when fixture is set, copies it to the --ginkgo.json-report path.
func reportingRunner(t *testing.T, exitCode int, fixture string) *fakeRunner {
	t.Helper()

	return &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if !strings.HasSuffix(command.Name, ".test") {
			return e2eexec.Result{}, false
		}

		path := reportPathArg(command.Args)
		require.NotEmpty(t, path, "test binary command has no Ginkgo report flag: %v", command.Args)
		if fixture != "" {
			data, err := os.ReadFile(fixture)
			require.NoError(t, err)
			//nolint:gosec // The path is the report file the code under test asked the fake binary to write.
			require.NoError(t, os.WriteFile(path, data, 0o600))
		}
		return e2eexec.Result{ExitCode: exitCode, Stderr: []byte("unrelated exporter log\n")}, true
	}}
}

// reportPathArg returns the path in the --ginkgo.json-report argument, or "".
func reportPathArg(args []string) string {
	for _, arg := range args {
		if path, ok := strings.CutPrefix(arg, reportFlagPrefix); ok {
			return path
		}
	}
	return ""
}

// TestMessageSummary checks the joining, whitespace collapsing and cap of messages.
func TestMessageSummary(t *testing.T) {
	long := strings.Repeat("x", maxMessageChars+10)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "gomega style", in: "Expected\n    <string>: a\nto equal\n    <string>: b\n", want: "Expected <string>: a to equal <string>: b"},
		{name: "blank lines and tabs", in: "\n one\t two \n\n three", want: "one two three"},
		{name: "empty", in: " \n", want: ""},
		{name: "cut", in: long, want: strings.Repeat("x", maxMessageChars) + "…"},
		{name: "exactly at the cap", in: long[:maxMessageChars], want: long[:maxMessageChars]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, messageSummary(tt.in))
		})
	}
}

// TestNewSpecOutcomeFallsBackToNodeLocation checks that a failure without its
// own location reports the location of the node that failed.
func TestNewSpecOutcomeFallsBackToNodeLocation(t *testing.T) {
	spec := types.SpecReport{
		LeafNodeType: types.NodeTypeBeforeSuite,
		State:        types.SpecStateFailed,
		Failure: types.Failure{
			Message:             "setup broke",
			FailureNodeLocation: types.CodeLocation{FileName: reportSourceFile, LineNumber: 9},
		},
	}

	require.Equal(t,
		specOutcome{Name: "BeforeSuite", Location: reportSourceFile + ":9", Message: "setup broke"},
		newSpecOutcome(spec))
}

// TestFinishGroup checks, for each suite kind, the failure text built from a
// report or from stderr, and which skipped specs are recorded.
func TestFinishGroup(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.json")
	require.NoError(t, os.WriteFile(garbage, []byte("[]x"), 0o600))
	stderrLines := make([]string, 25)
	for i := range stderrLines {
		stderrLines[i] = fmt.Sprintf("log line %d", i+1)
	}
	noisy := e2eexec.Result{ExitCode: 3, Stderr: []byte(strings.Join(stderrLines, "\n") + "\n")}
	passed := e2eexec.Result{}
	fixture := func(name string) string { return filepath.Join(reportFixtureDir, name) }

	tests := []struct {
		name       string
		path       string
		result     e2eexec.Result
		want       []string
		wantAbsent []string
		wantSkips  int
	}{
		{
			name:   "failed specs",
			path:   fixture("several_failures.json"),
			result: noisy,
			want: []string{
				"GROUP failed:",
				"FAILED " + reportSpecBase + "reaches the hostengine (" + reportSourceFile + ":16): " + gomegaMessage,
				"FAILED " + reportSpecBase + "reads the model name (" + reportSourceFile + ":17): model name is blank extra detail",
			},
			wantAbsent: []string{"log line", "stderr"},
		},
		{
			name:       "a failed group still lists its skipped specs",
			path:       fixture("failure_and_skip.json"),
			result:     noisy,
			want:       []string{"FAILED " + reportSpecBase + "reaches the hostengine", "vsock is not available"},
			wantAbsent: []string{"distroless image not configured"},
			wantSkips:  1,
		},
		{
			name:      "a passing group lists its skipped specs",
			path:      fixture("skipped_in_passing_group.json"),
			result:    passed,
			wantSkips: 1,
		},
		{
			name:       "ordered-container skip is not listed",
			path:       fixture("ordered_skip.json"),
			result:     noisy,
			want:       []string{"first breaks"},
			wantAbsent: []string{"second never runs"},
		},
		{
			name:   "before suite failure",
			path:   fixture("before_suite_failure.json"),
			result: noisy,
			want:   []string{"FAILED BeforeSuite (" + reportSourceFile + ":16)"},
		},
		{
			name:   "suite failure reason with a failed spec",
			path:   fixture("suite_timeout.json"),
			result: noisy,
			want:   []string{"A suite timeout occurred", "suite: Suite Timeout Elapsed"},
		},
		{
			name:   "suite failure reason with no failed spec",
			path:   fixture("no_specs_ran.json"),
			result: noisy,
			want: []string{
				"suite: Detected no specs ran", "lists no failed spec", "exit status 3", "stderr: log line 25",
			},
		},
		{
			name:   "panic",
			path:   fixture("panic.json"),
			result: noisy,
			want:   []string{"panic: nil pointer in scraper", "also: cleanup also failed"},
		},
		{
			name:   "missing report shows exit status and the last 20 stderr lines",
			path:   filepath.Join(t.TempDir(), "absent.json"),
			result: noisy,
			want: []string{
				noReportText, "exit status 3", "last stderr lines (20):",
				"stderr: log line 6\n", "stderr: log line 25",
			},
			wantAbsent: []string{"log line 5\n"},
		},
		{
			name:   "unreadable report",
			path:   garbage,
			result: e2eexec.Result{ExitCode: 2},
			want:   []string{noReportText, "exit status 2", "stderr was empty"},
		},
		{
			name:   "report without a failed spec",
			path:   fixture("skipped_in_passing_group.json"),
			result: noisy,
			want:   []string{"lists no failed spec", "exit status 3", "stderr: log line 25"},
			// The skip is still recorded for the end-of-run listing.
			wantSkips: 1,
		},
		{
			name:   "passing group without a report",
			path:   filepath.Join(t.TempDir(), "absent.json"),
			result: passed,
		},
	}
	for _, kind := range []string{"host", "container", "k8s"} {
		for _, tt := range tests {
			t.Run(kind+"/"+tt.name, func(t *testing.T) {
				var skips skippedSpecs

				err := finishGroup(kind, "GROUP", tt.path, tt.result, &skips)

				requireFailureText(t, err, tt.want, tt.wantAbsent)
				require.Len(t, skips, tt.wantSkips)
			})
		}
	}
}

// requireFailureText checks that err is nil when want is empty, and otherwise
// contains every want string and none of the absent strings.
func requireFailureText(t *testing.T, err error, want, absent []string) {
	t.Helper()

	if len(want) == 0 {
		require.NoError(t, err)
		return
	}

	require.Error(t, err)
	for _, text := range want {
		require.Contains(t, err.Error(), text)
	}
	for _, text := range absent {
		require.NotContains(t, err.Error(), text)
	}
}

// TestSkippedSpecsWrite checks the end-of-run listing of skipped specs per group.
func TestSkippedSpecsWrite(t *testing.T) {
	var skips skippedSpecs
	var out bytes.Buffer

	skips.write(&out)
	require.Empty(t, out.String(), "no skipped specs prints nothing")

	require.NoError(t, finishGroup("host", "no-skips", filepath.Join(reportFixtureDir, "one_failure.json"), e2eexec.Result{}, &skips))
	require.NoError(t, finishGroup("host", "absent", filepath.Join(t.TempDir(), "absent.json"), e2eexec.Result{}, &skips))
	require.NoError(t, finishGroup("container", "container-baseline", filepath.Join(reportFixtureDir, "skipped_in_passing_group.json"), e2eexec.Result{}, &skips))
	skips.write(&out)

	require.Equal(t, "[e2e] skipped specs:\n"+
		"[e2e]   group container-baseline:\n"+
		"[e2e]     SKIPPED "+reportSpecBase+"skips without image: distroless image not configured\n", out.String())
}

// TestGinkgoGroupArgs checks the arguments every group run passes to its test binary.
func TestGinkgoGroupArgs(t *testing.T) {
	args := ginkgoGroupArgs("TestSuite", []string{"a", "b"}, "/tmp/report.json")

	require.Equal(t, []string{
		"-test.v", "-test.run", "^TestSuite$",
		"--ginkgo.v", "--ginkgo.no-color", "--ginkgo.fail-on-empty",
		"--ginkgo.label-filter=a || b",
		"--ginkgo.json-report=/tmp/report.json",
	}, args)
}

// TestHostRunListsSkippedSpecsOnceAtTheEnd checks the run-end listing for a
// passing and for a failing host group, and that a failing group reports its
// failed specs instead of stderr.
func TestHostRunListsSkippedSpecsOnceAtTheEnd(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		fixture  string
		wantErr  string
	}{
		{name: "passing group", fixture: "skipped_in_passing_group.json"},
		{
			name:     "failing group",
			exitCode: 1,
			fixture:  "failure_and_skip.json",
			wantErr:  "reaches the hostengine (" + reportSourceFile + ":22): vsock is not available",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "tests", "host", "testdata")} {
				require.NoError(t, os.MkdirAll(dir, 0o755))
			}
			configurePackageTestEnvironment(t)
			for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter"} {
				require.NoError(t, writeExecutableFile(filepath.Join(root, "bin", name)))
			}
			var stdout, stderr bytes.Buffer
			runner := reportingRunner(t, tt.exitCode, filepath.Join(reportFixtureDir, tt.fixture))

			err := runWithRootRunner(
				[]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--bootstrap=skip"},
				&stdout, &stderr, root, runner,
			)

			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), "unrelated exporter log")
			}
			require.Equal(t, 1, strings.Count(stdout.String(), "[e2e] skipped specs:"))
			require.Contains(t, stdout.String(), "SKIPPED "+reportSpecBase+"skips without image: distroless image not configured")
			hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
			require.True(t, ok)
			require.NotEmpty(t, reportPathArg(hostCommand.Args))
		})
	}
}

// TestContainerGroupRun checks the container group's report flag, its failure
// reasons, and that its report directory is removed after a passing and a failing run.
func TestContainerGroupRun(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		fixture  string
		wantErr  []string
	}{
		{name: "passing", fixture: "skipped_in_passing_group.json"},
		{name: "failing", exitCode: 1, fixture: "one_failure.json", wantErr: []string{"container group container-baseline failed:", "other group"}},
		{
			name: "failing without a report", exitCode: 7,
			wantErr: []string{noReportText, "exit status 7", "stderr: unrelated exporter log"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := ""
			if tt.fixture != "" {
				fixture = filepath.Join(reportFixtureDir, tt.fixture)
			}
			runner := reportingRunner(t, tt.exitCode, fixture)
			var skips skippedSpecs
			run := containerGroupRun{
				stdout: io.Discard, runner: runner, binary: "/tmp/container.test",
				testName: "TestContainer", dir: t.TempDir(), skips: &skips,
			}

			err := run.run(context.Background(), scenario.PlanGroup{Name: "container-baseline", Labels: []string{"imageStartup"}}, nil)

			if len(tt.wantErr) == 0 {
				require.NoError(t, err)
			}
			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
			require.Len(t, runner.commands, 1)
			reportPath := reportPathArg(runner.commands[0].Args)
			require.NotEmpty(t, reportPath)
			_, statErr := os.Stat(filepath.Dir(reportPath))
			require.ErrorIs(t, statErr, os.ErrNotExist, "the report directory must be removed after the group")
		})
	}
}

// TestK8sGroupReportsFailedSpecs checks that a failed Kubernetes group passes
// the report flag and reports the report's failed specs, and that a group's
// skipped specs are collected whether it passed or failed.
func TestK8sGroupReportsFailedSpecs(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		fixture  string
		wantErr  string
	}{
		{name: "failed", exitCode: 1, fixture: "failure_and_skip.json", wantErr: "FAILED " + reportSpecBase + "reaches the hostengine (" + reportSourceFile + ":22): vsock is not available"},
		{name: "passed with skips", fixture: "skipped_in_passing_group.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestVersions(t, root)
			opts, err := parseTests(nil, io.Discard)
			require.NoError(t, err)
			runner := reportingRunner(t, tt.exitCode, filepath.Join(reportFixtureDir, tt.fixture))
			run := newK8sSuiteRun(
				context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
				"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
			)

			err = run.runK8sGroup(&opts, scenario.PlanGroup{Name: "default", Labels: []string{"default"}})

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, *run.skips, 1)
		})
	}
}

// TestExitAndStderrNamesTheExitStatusOnce verifies a plain non-zero exit, which
// the runner reports with an *exec.ExitError, is not described twice, while an
// error that says more is kept.
func TestExitAndStderrNamesTheExitStatusOnce(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 3").Run()
	if exitErr == nil {
		t.Fatal("sh -c 'exit 3' succeeded")
	}

	if got := exitAndStderr(e2eexec.Result{ExitCode: 3, Err: exitErr}); got != "exit status 3; stderr was empty" {
		t.Errorf("plain exit = %q, want the exit status once", got)
	}

	startErr := errors.New(`exec: "ginkgo": executable file not found in $PATH`)
	if got := exitAndStderr(e2eexec.Result{ExitCode: -1, Err: startErr}); !strings.Contains(got, "executable file not found") {
		t.Errorf("start failure = %q, want the error text", got)
	}
}
