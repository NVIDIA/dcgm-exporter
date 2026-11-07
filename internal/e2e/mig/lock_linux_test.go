//go:build linux

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
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestMIGLockRejectsAnotherProcessAndRetainsInode(t *testing.T) {
	path := testMIGLockPath(t)
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireMIGLock(context.Background(), io.Discard, &fakeRunner{}, path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestMIGLockProcessContender$")
	command.Env = append(os.Environ(), "DCGM_E2E_MIG_LOCK_TEST_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("process contender: %v\n%s", err, output)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode changed after release: %v", err)
	}
	next, err := acquireMIGLock(context.Background(), io.Discard, &fakeRunner{}, path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if err := next(); err != nil {
		t.Fatal(err)
	}
}

func TestMIGLockProcessContender(t *testing.T) {
	path := os.Getenv("DCGM_E2E_MIG_LOCK_TEST_PATH")
	if path == "" {
		t.Skip("invoked by the real process contention test")
	}
	runner := &fakeRunner{}
	_, err := prepareHostBeforeCluster(context.Background(), io.Discard, runner,
		config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}}, path, os.Geteuid())
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("contender error = %v", err)
	}
	if len(runner.commands) != 1 || commandKey(runner.commands[0]) != "nvidia-smi mig -i 0 -lgip" {
		t.Fatalf("contender reached state checks or mutation commands: %v", runner.commands)
	}
}

func TestPrepareHostBeforeClusterHoldsMIGLockUntilCleanup(t *testing.T) {
	path := testMIGLockPath(t)
	runner := &fakeRunner{outputs: map[string][]byte{
		"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
		"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte("0, Disabled\n"),
		"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(""),
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU\n"),
	}}
	runner.beforeRun = func(commandCtx context.Context, command e2eexec.Command) {
		assertLiveMIGContext(t, commandCtx)
		if strings.Contains(command.LogName, "delete_mig_") || command.LogName == "nvidia_smi_disable_mig" {
			assertBoundedMIGCleanup(t, commandCtx)
		}
		if commandKey(command) != "nvidia-smi mig -i 0 -lgip" && (command.Name == "nvidia-smi" || command.Name == "sudo") {
			assertMIGRunBlocked(t, commandCtx, path)
		}
	}
	cleanup, err := prepareHostBeforeCluster(context.Background(), io.Discard, runner,
		config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}}, path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	runner.results = map[string]e2eexec.Result{
		commandKey(sudoCommand("nvidia-smi", "mig", "-i", "0", "-dci")): {ExitCode: 1, Stderr: []byte("restore compute failure")},
	}
	t.Cleanup(func() { assertMIGRestorationFailure(t, cleanup(context.Background())) })
	assertMIGRunBlocked(t, context.Background(), path)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls sync.WaitGroup
	for range 4 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			assertMIGRestorationFailure(t, cleanup(canceledCtx))
		}()
	}
	calls.Wait()
	commandCount := len(runner.commands)
	next, err := acquireMIGLock(context.Background(), io.Discard, &fakeRunner{}, path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := next(); err != nil {
			t.Error(err)
		}
	}()
	assertMIGRestorationFailure(t, cleanup(context.Background()))
	if len(runner.commands) != commandCount {
		t.Fatal("repeated cleanup changed a later owner's fixture")
	}
}

func TestPrepareHostBeforeClusterReleasesMIGLockOnFailureOrRefusal(t *testing.T) {
	t.Run("partial setup failure", testMIGLockReleasedAfterSetupFailure)
	t.Run("preexisting layout refusal", testMIGLockReleasedAfterRefusal)
}

func testMIGLockReleasedAfterSetupFailure(t *testing.T) {
	t.Helper()
	path := testMIGLockPath(t)
	runner := migLockFailureRunner(t, path, "GPU 0: NVIDIA Test GPU\n")
	create := commandKey(sudoCommand("nvidia-smi", "mig", "-i", "0", "-cgi", "19", "-C"))
	runner.results = map[string]e2eexec.Result{create: {ExitCode: 1, Stderr: []byte("partial create failure")}}
	_, err := prepareHostBeforeCluster(context.Background(), io.Discard, runner,
		config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}}, path, os.Geteuid())
	if err == nil || !strings.Contains(err.Error(), "partial create failure") || !runner.hasCommandArg("-dgi") {
		t.Fatalf("rollback missing: %v, %v", err, runner.commands)
	}
	assertMIGLockAvailable(t, path)
}

func testMIGLockReleasedAfterRefusal(t *testing.T) {
	t.Helper()
	path := testMIGLockPath(t)
	runner := migLockFailureRunner(t, path, "MIG 1g.10gb Device 0\n")
	_, err := prepareHostBeforeCluster(context.Background(), io.Discard, runner,
		config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}}, path, os.Geteuid())
	if err == nil {
		t.Fatal("setup unexpectedly succeeded")
	}
	if runner.hasCommandArg("-dgi") || runner.hasCommandArg("-mig") {
		t.Fatal("ownership refusal mutated host")
	}
	assertMIGLockAvailable(t, path)
}

// migLockFailureRunner checks ownership during state checks, setup and rollback.
func migLockFailureRunner(t *testing.T, path, layout string) *fakeRunner {
	t.Helper()
	return &fakeRunner{
		outputs: map[string][]byte{
			"nvidia-smi mig -i 0 -lgip": []byte("MIG 1g.10gb 19\n"),
			"nvidia-smi --query-gpu=index,mig.mode.current --format=csv,noheader":             []byte("0, Disabled\n"),
			"nvidia-smi --query-compute-apps=pid,gpu_uuid,process_name --format=csv,noheader": []byte(""),
			"nvidia-smi -L": []byte(layout),
		},
		beforeRun: func(ctx context.Context, command e2eexec.Command) {
			if commandKey(command) != "nvidia-smi mig -i 0 -lgip" {
				assertMIGLockHeld(t, context.WithoutCancel(ctx), path)
			}
		},
	}
}

func assertMIGLockAvailable(t *testing.T, path string) {
	t.Helper()
	release, err := acquireMIGLock(context.Background(), io.Discard, &fakeRunner{}, path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func assertMIGRestorationFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "restore compute failure") {
		t.Errorf("cleanup must retain restoration error: %v", err)
	}
}

func TestMIGLockRejectsUntrustedArtifactsBeforeNativeCommands(t *testing.T) {
	for _, kind := range []string{"directory symlink", "writable directory", "file symlink", "writable file", "wrong owner", "file directory"} {
		t.Run(kind, func(t *testing.T) {
			path, owner := untrustedMIGLockPath(t, kind)
			runner := &fakeRunner{}
			_, err := prepareHostBeforeCluster(context.Background(), io.Discard, runner,
				config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}}, path, owner)
			if err == nil {
				t.Fatal("untrusted lock accepted")
			}
			if len(runner.commands) != 1 || commandKey(runner.commands[0]) != "nvidia-smi mig -i 0 -lgip" {
				t.Fatalf("untrusted lock reached state checks or mutation commands: %v", runner.commands)
			}
		})
	}
}

// untrustedMIGLockPath changes one trust condition of a private fixture.
func untrustedMIGLockPath(t *testing.T, kind string) (string, int) {
	t.Helper()
	path := testMIGLockPath(t)
	owner := os.Geteuid()
	switch kind {
	case "directory symlink":
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(filepath.Dir(path), link); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(link, filepath.Base(path))
	case "writable directory":
		if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
	case "file symlink":
		link := filepath.Join(filepath.Dir(path), "link.lock")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		path = link
	case "writable file":
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
	case "wrong owner":
		owner++
	case "file directory":
		path = filepath.Dir(path)
	}
	return path, owner
}

// TestMIGLockCreatesStableArtifacts runs the setup commands only inside a private fixture.
func TestMIGLockCreatesStableArtifacts(t *testing.T) {
	for _, existingDirectory := range []bool{false, true} {
		name := "missing directory"
		if existingDirectory {
			name = "missing file"
		}
		t.Run(name, func(t *testing.T) {
			testMIGLockCreatesStableArtifacts(t, existingDirectory)
		})
	}
}

func testMIGLockCreatesStableArtifacts(t *testing.T, existingDirectory bool) {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "locks")
	if existingDirectory {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "mig.lock")
	runner := &fakeRunner{beforeRun: func(ctx context.Context, command e2eexec.Command) {
		runMIGLockFixtureCommand(t, ctx, command)
	}}
	release, err := acquireMIGLock(context.Background(), io.Discard, runner, path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Error(err)
		}
	})
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || !trustedMIGLockOwner(before, os.Geteuid()) {
		t.Fatalf("created untrusted lock file: %v", err)
	}
	if _, err := acquireMIGLock(context.Background(), io.Discard, runner, path, os.Geteuid()); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("created lock did not exclude contender: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("created lock inode changed after release: %v", err)
	}
}

// runMIGLockFixtureCommand executes setup only inside the test user's private directory.
func runMIGLockFixtureCommand(t *testing.T, ctx context.Context, command e2eexec.Command) {
	t.Helper()
	// The fixture belongs to this user, so execute setup without privilege escalation.
	if command.Name == "sudo" {
		command.Name, command.Args = command.Args[0], command.Args[1:]
	}
	result := (e2eexec.OSRunner{}).Run(ctx, command)
	if result.Err != nil || result.ExitCode != 0 {
		t.Fatalf("fixture setup: %v, %s", result.Err, result.Stderr)
	}
}

func assertMIGLockHeld(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	release, err := acquireMIGLock(ctx, io.Discard, &fakeRunner{}, path, os.Geteuid())
	if err == nil {
		if err := release(); err != nil {
			t.Error(err)
		}
		t.Fatal("MIG ownership released before rollback finished")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("unexpected contention error: %v", err)
	}
}

func assertMIGRunBlocked(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	other := &fakeRunner{}
	_, err := prepareHostBeforeCluster(ctx, io.Discard, other,
		config.TestOptions{MIGConfigure: "19", Scenarios: []string{"k8s/mig"}}, path, os.Geteuid())
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Errorf("second run error = %v", err)
	}
	if len(other.commands) != 1 || commandKey(other.commands[0]) != "nvidia-smi mig -i 0 -lgip" {
		t.Errorf("second run reached state checks or mutation commands: %v", other.commands)
	}
}
