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

// Package exec defines the command-runner seam used by e2e orchestration.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	stdexec "os/exec"
	"strings"
	"time"
)

const processTerminationGrace = 2 * time.Second

// Command is an external command invocation.
type Command struct {
	Name           string
	Args           []string
	Env            []string
	Dir            string
	Stdin          []byte
	Stdout         io.Writer
	Stderr         io.Writer
	LogName        string
	QuietOnSuccess bool
	// SensitiveOutput asks wrappers to keep command output out of logs and error text.
	SensitiveOutput bool
}

// Result is the captured result of a command invocation.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

// Runner runs external commands for e2e workflows.
type Runner interface {
	Run(context.Context, Command) Result
}

// EnvironmentRunner adds authoritative environment values to every nested command.
// Values configured here replace command-local entries with the same name.
type EnvironmentRunner struct {
	Runner Runner
	Env    []string
}

// Run decorates a command before forwarding it to the underlying runner.
func (r EnvironmentRunner) Run(ctx context.Context, command Command) Result {
	command.Env = mergeEnvironment(command.Env, r.Env)
	return r.Runner.Run(ctx, command)
}

// mergeEnvironment appends overrides after removing duplicate names.
func mergeEnvironment(current, overrides []string) []string {
	overridden := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		name, _, _ := strings.Cut(entry, "=")
		overridden[name] = struct{}{}
	}
	merged := make([]string, 0, len(current)+len(overrides))
	for _, entry := range current {
		name, _, _ := strings.Cut(entry, "=")
		if _, ok := overridden[name]; !ok {
			merged = append(merged, entry)
		}
	}
	return append(merged, overrides...)
}

// OSRunner runs commands on the local operating system.
type OSRunner struct{}

// Run executes one command and captures stdout, stderr, exit code, and error.
func (OSRunner) Run(ctx context.Context, command Command) Result {
	// #nosec G204 -- e2e deliberately executes configured tools and suite binaries.
	cmd := stdexec.Command(command.Name, command.Args...)
	configureCommandProcessGroup(cmd)
	cmd.WaitDelay = processTerminationGrace
	cmd.Dir = command.Dir
	if len(command.Env) != 0 {
		cmd.Env = mergeEnvironment(os.Environ(), command.Env)
	}
	if len(command.Stdin) != 0 {
		cmd.Stdin = bytes.NewReader(command.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = captureWriter(&stdout, command.Stdout)
	cmd.Stderr = captureWriter(&stderr, command.Stderr)

	err := runCommand(ctx, cmd)
	// Stderr is the failure reason when a test binary wrote no Ginkgo report, and for the static
	// suite, so name the reason a command was stopped there.
	if reason := commandStopReason(err); reason != "" {
		fmt.Fprintf(cmd.Stderr, "command stopped: %s\n", reason)
	}
	return Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: exitCode(err),
		Err:      err,
	}
}

// commandStopReason reports why a command stopped from its returned error.
func commandStopReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded.Error()
	case errors.Is(err, stdexec.ErrWaitDelay):
		return fmt.Sprintf("a child process kept its output open for %s after it exited", processTerminationGrace)
	default:
		return ""
	}
}

// runCommand terminates the complete command process group on cancellation.
// Suite binaries can start helper processes that retain inherited output pipes;
// killing only the immediate parent would otherwise leave Cmd.Wait blocked.
func runCommand(ctx context.Context, cmd *stdexec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return finishCommandWait(cmd, err)
	case <-ctx.Done():
		return waitForCommandAfterCancellation(ctx, cmd, done)
	}
}

// waitForCommandAfterCancellation uses an already-ready wait result before signaling the process group.
func waitForCommandAfterCancellation(ctx context.Context, cmd *stdexec.Cmd, done <-chan error) error {
	select {
	case err := <-done:
		return finishCommandWait(cmd, err)
	default:
	}

	_ = terminateCommandProcessGroup(cmd)
	select {
	case err := <-done:
		return errors.Join(ctx.Err(), finishCommandWait(cmd, err))
	case <-time.After(processTerminationGrace):
		_ = killCommandProcessGroup(cmd)
		return errors.Join(ctx.Err(), finishCommandWait(cmd, <-done))
	}
}

// finishCommandWait kills remaining descendants when waiting on output pipes times out.
func finishCommandWait(cmd *stdexec.Cmd, err error) error {
	if errors.Is(err, stdexec.ErrWaitDelay) {
		_ = killCommandProcessGroup(cmd)
	}
	return err
}

// captureWriter mirrors process output to a caller stream while retaining it in memory.
func captureWriter(buffer *bytes.Buffer, stream io.Writer) io.Writer {
	if stream == nil {
		return buffer
	}
	return io.MultiWriter(buffer, stream)
}

// exitCode normalizes os/exec errors into harness exit codes.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *stdexec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
