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

package exec

import (
	"bytes"
	"context"
	"errors"
	"os"
	stdexec "os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buffer.String()
}

type fakeRunner struct {
	commands []Command
	results  []Result
}

func (f *fakeRunner) Run(_ context.Context, command Command) Result {
	f.commands = append(f.commands, command)
	if len(f.results) == 0 {
		return Result{ExitCode: -1}
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result
}

func TestFakeRunner(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: []byte("ok\n")}}}
	got := runner.Run(context.Background(), Command{Name: "echo", Args: []string{"ok"}})

	if string(got.Stdout) != "ok\n" {
		t.Fatalf("Stdout = %q, want ok", got.Stdout)
	}
	if len(runner.commands) != 1 || runner.commands[0].Name != "echo" {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

func TestEnvironmentRunnerReplacesCommandEnvironment(t *testing.T) {
	base := &fakeRunner{results: []Result{{ExitCode: 0}}}
	runner := EnvironmentRunner{Runner: base, Env: []string{"DOCKER_CONFIG=/run/e2e-docker", "E2E_RUN=1"}}
	runner.Run(context.Background(), Command{Env: []string{"UNCHANGED=yes", "DOCKER_CONFIG=/home/user/.docker"}})

	want := []string{"UNCHANGED=yes", "DOCKER_CONFIG=/run/e2e-docker", "E2E_RUN=1"}
	if len(base.commands) != 1 || strings.Join(base.commands[0].Env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("command environment = %#v, want %#v", base.commands[0].Env, want)
	}
}

func TestOSRunnerStreamsAndCapturesOutput(t *testing.T) {
	var streamed synchronizedBuffer
	done := make(chan Result, 1)
	go func() {
		done <- OSRunner{}.Run(context.Background(), Command{
			Name:   "sh",
			Args:   []string{"-c", "printf start; sleep 0.2; printf end"},
			Stdout: &streamed,
		})
	}()

	deadline := time.After(time.Second)
	for !strings.Contains(streamed.String(), "start") {
		select {
		case <-deadline:
			t.Fatalf("output did not stream before command completed; streamed=%q", streamed.String())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	result := <-done
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", result.ExitCode, result.Stderr)
	}
	if string(result.Stdout) != "startend" || streamed.String() != "startend" {
		t.Fatalf("captured=%q streamed=%q, want startend", result.Stdout, streamed.String())
	}
}

// TestOSRunnerDoesNotStartAfterCancellation ensures an already-canceled context cannot start the child process.
func TestOSRunnerDoesNotStartAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := t.TempDir() + "/started"

	result := OSRunner{}.Run(ctx, Command{Name: "sh", Args: []string{"-c", "touch " + started}})

	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", result.Err)
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatalf("command started after cancellation: stat error = %v", err)
	}
	if !strings.Contains(string(result.Stderr), "command stopped: context canceled") {
		t.Fatalf("Stderr = %q, want cancellation cause", result.Stderr)
	}
}

// TestOSRunnerStreamsCancellationReason checks child stderr and the stop reason reach both captured and streaming output.
func TestOSRunnerStreamsCancellationReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var streamed synchronizedBuffer
	done := make(chan Result, 1)

	go func() {
		done <- OSRunner{}.Run(ctx, Command{
			Name:   "sh",
			Args:   []string{"-c", "printf 'child diagnostic\\n' >&2; exec sleep 30"},
			Stderr: &streamed,
		})
	}()
	deadline := time.After(5 * time.Second)
	for !strings.Contains(streamed.String(), "child diagnostic\n") {
		select {
		case result := <-done:
			t.Fatalf("command exited before cancellation: %#v", result)
		case <-deadline:
			cancel()
			<-done
			t.Fatal("command diagnostic did not stream before timeout")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	result := <-done
	if result.ExitCode == 0 {
		t.Fatal("cancelled command exited successfully")
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", result.Err)
	}

	const want = "child diagnostic\ncommand stopped: context canceled\n"
	if got := string(result.Stderr); got != want {
		t.Fatalf("captured stderr = %q, want %q", got, want)
	}
	if got := streamed.String(); got != want {
		t.Fatalf("streamed stderr = %q, want %q", got, want)
	}
}

// TestCommandStopReasonUsesReturnedError keeps an independent process failure from being reported as a context stop.
func TestCommandStopReasonUsesReturnedError(t *testing.T) {
	processErr := stdexec.Command("sh", "-c", "exit 17").Run()
	if processErr == nil {
		t.Fatal("process error = nil, want non-zero exit")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want context.Canceled", ctx.Err())
	}

	if got := commandStopReason(processErr); got != "" {
		t.Fatalf("commandStopReason() = %q after independent exit, want empty", got)
	}
}

// TestWaitForCommandPrefersCompletedResultOnCancellation preserves a finished process error without joining cancellation.
func TestWaitForCommandPrefersCompletedResultOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processErr := stdexec.Command("sh", "-c", "exit 23").Run()
	if processErr == nil {
		t.Fatal("process error = nil, want non-zero exit")
	}
	done := make(chan error, 1)
	done <- processErr

	got := waitForCommandAfterCancellation(ctx, nil, done)
	if got != processErr {
		t.Fatalf("wait result = %v, want completed process error %v", got, processErr)
	}
	if errors.Is(got, context.Canceled) {
		t.Fatalf("completed process error was joined with cancellation: %v", got)
	}
}

func TestOSRunnerOverridesInheritedEnvironment(t *testing.T) {
	t.Setenv("E2E_ENVIRONMENT_RUNNER_TEST", "ambient")
	result := OSRunner{}.Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-c", "printf %s \"$E2E_ENVIRONMENT_RUNNER_TEST\""},
		Env:  []string{"E2E_ENVIRONMENT_RUNNER_TEST=isolated"},
	})

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", result.ExitCode, result.Stderr)
	}
	if string(result.Stdout) != "isolated" {
		t.Fatalf("environment value = %q, want isolated", result.Stdout)
	}
}

// TestExitCodeUnwrapsJoinedExitError preserves the command status when
// cancellation adds a context error to the process failure.
func TestExitCodeUnwrapsJoinedExitError(t *testing.T) {
	err := stdexec.Command("sh", "-c", "exit 42").Run()
	if err == nil {
		t.Fatal("command error = nil, want non-zero exit")
	}

	if got := exitCode(errors.Join(context.DeadlineExceeded, err)); got != 42 {
		t.Fatalf("exitCode() = %d, want 42", got)
	}
}
