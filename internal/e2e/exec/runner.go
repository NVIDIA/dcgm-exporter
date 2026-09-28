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
	"io"
	"os"
	stdexec "os/exec"
)

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

// OSRunner runs commands on the local operating system.
type OSRunner struct{}

// Run executes one command and captures stdout, stderr, exit code, and error.
func (OSRunner) Run(ctx context.Context, command Command) Result {
	// #nosec G204 -- e2e deliberately executes configured tools and suite binaries.
	cmd := stdexec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	if len(command.Env) != 0 {
		cmd.Env = append(os.Environ(), command.Env...)
	}
	if len(command.Stdin) != 0 {
		cmd.Stdin = bytes.NewReader(command.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = captureWriter(&stdout, command.Stdout)
	cmd.Stderr = captureWriter(&stderr, command.Stderr)

	err := cmd.Run()
	return Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: exitCode(err),
		Err:      err,
	}
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
	if exitErr, ok := err.(*stdexec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}
