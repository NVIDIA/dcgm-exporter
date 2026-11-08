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

package mig

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestPrepareHostBeforeClusterPlatformPreflight keeps no-op selections platform independent.
func TestPrepareHostBeforeClusterPlatformPreflight(t *testing.T) {
	mutationError := errMIGRequiresLinux
	var mutationCommands []string
	if runtime.GOOS == "linux" {
		mutationError = nil
		mutationCommands = []string{"nvidia-smi mig -i 0 -lgip"}
	}
	for _, tc := range []struct {
		name         string
		opts         config.TestOptions
		wantError    error
		wantCommands []string
	}{
		{name: "selected host mutation", opts: config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}}, wantError: mutationError, wantCommands: mutationCommands},
		{name: "automatic layout validation", opts: config.TestOptions{MIGConfigure: "auto", Scenarios: []string{"k8s/mig"}}},
		{name: "MIG scenario skipped", opts: config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}, SkipScenarios: []string{"k8s/mig"}}},
		{name: "unrelated scenario", opts: config.TestOptions{MIGConfigure: "19", Scenarios: []string{"static/chartRbacRendering"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Linux probes unsupported hardware; other platforms reject mutation before probing.
			runner := &fakeRunner{results: map[string]e2eexec.Result{
				"nvidia-smi mig -i 0 -lgip": {ExitCode: 6, Stderr: []byte("Not Supported")},
			}}
			cleanup, err := PrepareHostBeforeCluster(context.Background(), io.Discard, runner, tc.opts)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("platform preflight error = %v, want %v", err, tc.wantError)
			}
			if err := cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, command := range runner.commands {
				got = append(got, commandKey(command))
			}
			if strings.Join(got, "\n") != strings.Join(tc.wantCommands, "\n") {
				t.Fatalf("platform preflight commands = %v, want %v", got, tc.wantCommands)
			}
		})
	}
}

func TestPrepareHostBeforeClusterMutatesAndRestores(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
		"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte("0, Disabled\n"),
		"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(""),
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
	}}
	var stdout bytes.Buffer

	cleanup, err := prepareMIGForTest(t, context.Background(), &stdout, runner, config.TestOptions{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("-cgi") || !runner.hasCommandArg("19") {
		t.Fatalf("MIG create command missing; commands = %#v", runner.commands)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("MIG cleanup error = %v", err)
	}
	for _, want := range []string{"-dci", "-dgi", "-mig"} {
		if !runner.hasCommandArg(want) {
			t.Fatalf("MIG cleanup command missing %q; commands = %#v", want, runner.commands)
		}
	}
	if !strings.Contains(stdout.String(), "Configuring MIG on GPU 0 with profile 19") {
		t.Fatalf("MIG log missing:\n%s", stdout.String())
	}
}

func TestPrepareHostBeforeClusterSkipsUnsupportedProfiles(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"nvidia-smi mig -i 0 -lgip": {ExitCode: 6, Stderr: []byte("No MIG-supported devices found\n")},
	}}
	var stdout bytes.Buffer

	cleanup, err := prepareMIGForTest(t, context.Background(), &stdout, runner, config.TestOptions{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("MIG cleanup error = %v", err)
	}
	if runner.hasCommandArg("-mig") {
		t.Fatalf("MIG mutation ran on unsupported host: %#v", runner.commands)
	}
	if !strings.Contains(stdout.String(), "no MIG profiles were detected") {
		t.Fatalf("MIG unsupported log missing:\n%s", stdout.String())
	}
}

func TestPrepareHostBeforeClusterSkipsUnsupportedExplicitProfile(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"nvidia-smi mig -i 0 -lgip": {
			ExitCode: 6,
			Stderr:   []byte("No MIG-supported devices found\n"),
		},
	}}

	_, err := prepareMIGForTest(t, context.Background(), io.Discard, runner, config.TestOptions{
		MIGConfigure: "19",
		Scenarios:    []string{"k8s/mig"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandArg("-mig") {
		t.Fatalf("MIG mutation ran on unsupported host: %#v", runner.commands)
	}
}

func TestPrepareHostBeforeClusterHonorsSkippedMIGScenarios(t *testing.T) {
	runner := &fakeRunner{}

	_, err := prepareMIGForTest(t, context.Background(), io.Discard, runner, config.TestOptions{
		MIGConfigure: "true",
		SkipScenarios: []string{
			"k8s/mig",
			"k8s/migFullGpuSelection",
			"k8s/migInstanceSelection",
			"k8s/migSpecificInstanceSelection",
			"k8s/migCombinedDeviceSelection",
			"k8s/migPodAttribution",
			"k8s/gpuOperatorMig",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("MIG probe ran with every MIG scenario skipped: %#v", runner.commands)
	}
}

func TestPrepareHostBeforeClusterFailsWhenSafetyProbeFails(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string][]byte{
			"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
			"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader": []byte("0, Disabled\n"),
		},
		results: map[string]e2eexec.Result{
			"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": {
				ExitCode: 1,
				Stderr:   []byte("nvidia-smi failed"),
			},
		},
	}
	_, err := prepareMIGForTest(t, context.Background(), io.Discard, runner, config.TestOptions{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err == nil || !strings.Contains(err.Error(), "query active GPU compute processes") {
		t.Fatalf("PrepareHostBeforeCluster() error = %v, want safety probe failure", err)
	}
	if runner.hasCommandArg("-mig") {
		t.Fatalf("MIG mutation ran after failed safety probe: %#v", runner.commands)
	}
}

func TestPrepareHostBeforeClusterFailsWhenCurrentModeUnreadable(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string][]byte{
			"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
		},
		results: map[string]e2eexec.Result{
			"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader": {
				ExitCode: 1,
				Stderr:   []byte("nvidia-smi failed"),
			},
		},
	}
	_, err := prepareMIGForTest(t, context.Background(), io.Discard, runner, config.TestOptions{
		MIGConfigure: "true",
		Scenarios:    []string{"k8s/mig"},
	})
	if err == nil || !strings.Contains(err.Error(), "query current MIG mode") {
		t.Fatalf("PrepareHostBeforeCluster() error = %v, want mode query failure", err)
	}
	if runner.hasCommandArg("-mig") {
		t.Fatalf("MIG mutation ran after unreadable current mode: %#v", runner.commands)
	}
}

func TestPrepareHostBeforeClusterPreservesUnownedState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		processes string
		layout    string
		wantError string
	}{
		{name: "active clients", processes: "123, GPU-test, workload\n", layout: "GPU 0: NVIDIA Test GPU\n", wantError: "compute processes are active"},
		{name: "existing instances", layout: "GPU 0: NVIDIA Test GPU\n  MIG 1g.10gb Device 0\n", wantError: "existing MIG instances"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{outputs: map[string][]byte{
				"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
				"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte("0, Enabled\n"),
				"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(tc.processes),
				"nvidia-smi -L": []byte(tc.layout),
			}}
			_, err := prepareMIGForTest(t, context.Background(), io.Discard, runner, config.TestOptions{
				MIGConfigure: "19",
				Scenarios:    []string{"k8s/mig"},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("configuration error = %v, want %q", err, tc.wantError)
			}
			for _, arg := range []string{"-mig", "-cgi", "-dci", "-dgi"} {
				if runner.hasCommandArg(arg) {
					t.Fatalf("host mutation %s attempted after ownership refusal: %v", arg, runner.commands)
				}
			}
		})
	}
}

func TestRestoreMIGInstancesReportsFailureAndContinues(t *testing.T) {
	dgi := sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-dgi")
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		commandKey(dgi): {ExitCode: 1, Stderr: []byte("in use\n")},
	}}

	err := restoreMIGInstances(context.Background(), io.Discard, runner, "Disabled", cdiSpecState{dirExisted: true})
	if err == nil || !strings.Contains(err.Error(), "-dgi") {
		t.Fatalf("restoreMIGInstances() error = %v, want GPU instance cleanup failure", err)
	}
	if !runner.hasCommandArg("-mig") || !runner.hasCommandArg("0") {
		t.Fatalf("MIG disable was not attempted after cleanup failure: %#v", runner.commands)
	}
}

func TestPrepareHostBeforeClusterRestoresFailedConfiguration(t *testing.T) {
	enable := commandKey(sudoCommand("nvidia-smi", "-i", defaultMIGGPUIndex, "-mig", "1"))
	create := commandKey(sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-cgi", "19", "-C"))
	dci := commandKey(sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-dci"))
	dgi := commandKey(sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-dgi"))
	disable := commandKey(sudoCommand("nvidia-smi", "-i", defaultMIGGPUIndex, "-mig", "0"))
	for _, tc := range []struct {
		name         string
		mode         string
		failedStep   string
		cancel       bool
		cleanupFails bool
	}{
		{name: "enable failure", mode: "Disabled", failedStep: enable},
		{name: "partial creation failure", mode: "Disabled", failedStep: create},
		{name: "preserve enabled mode", mode: "Enabled", failedStep: create},
		{name: "cancelled creation", mode: "Disabled", failedStep: create, cancel: true},
		{name: "creation and cleanup failures", mode: "Disabled", failedStep: create, cleanupFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &fakeRunner{
				outputs: map[string][]byte{
					"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
					"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte("0, " + tc.mode + "\n"),
					"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(""),
					"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
				},
				results: map[string]e2eexec.Result{
					tc.failedStep: {ExitCode: 1, Stdout: []byte("GPU instance created before failure\n"), Stderr: []byte("setup failure\n")},
				},
			}
			if tc.cleanupFails {
				runner.results[dgi] = e2eexec.Result{ExitCode: 1, Stderr: []byte("cleanup failure\n")}
			}
			runner.beforeRun = func(commandCtx context.Context, command e2eexec.Command) {
				key := commandKey(command)
				if key == tc.failedStep && tc.cancel {
					cancel()
				}
				if key == dci || key == dgi || key == disable {
					assertLiveMIGContext(t, commandCtx)
					assertBoundedMIGCleanup(t, commandCtx)
				}
			}
			_, err := prepareMIGForTest(t, ctx, io.Discard, runner, config.TestOptions{
				MIGConfigure: "19",
				Scenarios:    []string{"k8s/mig"},
			})
			assertMIGConfigurationFailure(t, err, tc.cleanupFails)
			assertMIGRollbackCommands(t, runner, tc.mode)
		})
	}
}

type fakeRunner struct {
	outputs   map[string][]byte
	results   map[string]e2eexec.Result
	commands  []e2eexec.Command
	beforeRun func(context.Context, e2eexec.Command)
}

func (f *fakeRunner) Run(ctx context.Context, command e2eexec.Command) e2eexec.Result {
	f.commands = append(f.commands, command)
	if f.beforeRun != nil {
		f.beforeRun(ctx, command)
	}
	key := commandKey(command)
	if f.results != nil {
		if result, ok := f.results[key]; ok {
			return streamFakeResult(command, result)
		}
	}
	if output, ok := f.outputs[key]; ok {
		return streamFakeResult(command, e2eexec.Result{Stdout: output})
	}
	// A host CDI spec is present as a regular file, owned by root with mode 644, unless a test
	// says otherwise.
	if key == commandKey(sudoCommand("stat", "-c", "%a %u:%g", cdiSpecPath)) {
		return streamFakeResult(command, e2eexec.Result{Stdout: []byte("644 0:0\n")})
	}
	if key == commandKey(sudoCommand("test", "-L", cdiSpecPath)) {
		return streamFakeResult(command, e2eexec.Result{ExitCode: 1})
	}
	if f.outputs != nil {
		return streamFakeResult(command, e2eexec.Result{})
	}
	return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

func commandKey(command e2eexec.Command) string {
	return strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
}

func streamFakeResult(command e2eexec.Command, result e2eexec.Result) e2eexec.Result {
	if command.Stdout != nil && len(result.Stdout) != 0 {
		_, _ = command.Stdout.Write(result.Stdout)
	}
	if command.Stderr != nil && len(result.Stderr) != 0 {
		_, _ = command.Stderr.Write(result.Stderr)
	}
	return result
}

func (f *fakeRunner) hasCommandArg(value string) bool {
	for _, command := range f.commands {
		for _, arg := range command.Args {
			if arg == value {
				return true
			}
		}
	}
	return false
}

// prepareMIGForTest uses a real lock in an isolated directory instead of the host lock.
func prepareMIGForTest(t *testing.T, ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions) (func(context.Context) error, error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("MIG host configuration requires Linux")
	}
	path := testMIGLockPath(t)
	cleanup, err := prepareHostBeforeCluster(ctx, stdout, runner, opts, path, os.Geteuid())
	if err == nil {
		t.Cleanup(func() {
			if err := cleanup(context.WithoutCancel(ctx)); err != nil {
				t.Errorf("test fixture cleanup: %v", err)
			}
		})
	}
	return cleanup, err
}

// testMIGLockPath creates a private lock fixture owned by the test process.
func testMIGLockPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mig.lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertLiveMIGContext(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := ctx.Err(); err != nil {
		t.Errorf("MIG command received a canceled context: %v", err)
	}
}

func assertBoundedMIGCleanup(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > migRollbackTimeout {
		t.Error("MIG cleanup must have a live bounded deadline")
	}
}

func assertMIGRollbackCommands(t *testing.T, runner *fakeRunner, mode string) {
	t.Helper()
	dci := commandKey(sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-dci"))
	dgi := commandKey(sudoCommand("nvidia-smi", "mig", "-i", defaultMIGGPUIndex, "-dgi"))
	disable := commandKey(sudoCommand("nvidia-smi", "-i", defaultMIGGPUIndex, "-mig", "0"))
	want := []string{dci, dgi}
	if mode == "Disabled" {
		want = append(want, disable)
	}
	var got []string
	for _, command := range runner.commands {
		key := commandKey(command)
		if key == dci || key == dgi || key == disable {
			got = append(got, key)
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rollback commands = %v, want %v", got, want)
	}
}

func assertMIGConfigurationFailure(t *testing.T, err error, cleanupFails bool) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "setup failure") {
		t.Fatalf("configuration error = %v, want original setup failure", err)
	}
	if cleanupFails && !strings.Contains(err.Error(), "cleanup failure") {
		t.Fatalf("configuration error = %v, want cleanup failure too", err)
	}
}
