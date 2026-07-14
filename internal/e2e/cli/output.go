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
	"context"
	"fmt"
	"io"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// outputRunner hides successful setup command chatter unless verbose output is enabled.
type outputRunner struct {
	base    e2eexec.Runner
	stdout  io.Writer
	verbose bool
}

// newOutputRunner adds e2e output policy around a lower-level command runner.
func newOutputRunner(stdout io.Writer, runner e2eexec.Runner, verbose bool) e2eexec.Runner {
	return outputRunner{base: runner, stdout: stdout, verbose: verbose}
}

// Run executes a command with normal or verbose e2e output policy.
func (r outputRunner) Run(ctx context.Context, command e2eexec.Command) e2eexec.Result {
	label := commandLabel(command)
	if r.verbose {
		fmt.Fprintf(r.stdout, "[e2e] verbose: command: %s\n", commandString(command))
		fmt.Fprintf(r.stdout, "[e2e] ----- command output: %s -----\n", label)
		if command.Stdout == nil {
			command.Stdout = r.stdout
		}
		if command.Stderr == nil {
			command.Stderr = r.stdout
		}
		result := r.base.Run(ctx, command)
		fmt.Fprintf(r.stdout, "[e2e] ----- end command output: %s -----\n", label)
		return result
	}

	if command.QuietOnSuccess {
		command.Stdout = nil
		command.Stderr = nil
		result := r.base.Run(ctx, command)
		if result.ExitCode != 0 || result.Err != nil {
			writeCommandFailure(r.stdout, label, command, result)
		}
		return result
	}
	return r.base.Run(ctx, command)
}

// writeSection prints a high-level e2e output boundary.
func writeSection(w io.Writer, name string) {
	fmt.Fprintf(w, "[e2e] === %s ===\n", name)
}

// writeStep prints one live milestone in normal output.
func writeStep(w io.Writer, name, detail string) {
	fmt.Fprintf(w, "[e2e] %s: %s\n", name, detail)
}

// writeVerbose prints a line only when verbose output is enabled.
func writeVerbose(w io.Writer, enabled bool, format string, args ...any) {
	if enabled {
		fmt.Fprintf(w, "[e2e] verbose: "+format+"\n", args...)
	}
}

// writeCommandFailure prints captured output for the command that actually failed.
func writeCommandFailure(w io.Writer, label string, command e2eexec.Command, result e2eexec.Result) {
	fmt.Fprintf(w, "[e2e] FAIL %s\n", label)
	fmt.Fprintf(w, "[e2e] command: %s\n", commandString(command))
	fmt.Fprintf(w, "[e2e] exit code: %d\n", result.ExitCode)
	if result.Err != nil {
		fmt.Fprintf(w, "[e2e] error: %v\n", result.Err)
	}
	fmt.Fprintln(w, "[e2e] stdout:")
	writeIndentedOutput(w, result.Stdout)
	fmt.Fprintln(w, "[e2e] stderr:")
	writeIndentedOutput(w, result.Stderr)
}

// writeIndentedOutput makes captured command output easy to scan under a failure header.
func writeIndentedOutput(w io.Writer, data []byte) {
	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		fmt.Fprintln(w, "  <empty>")
		return
	}
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(w, "  %s\n", strings.TrimRight(line, "\r"))
	}
}

// commandLabel returns a short stable label for command output sections.
func commandLabel(command e2eexec.Command) string {
	if command.LogName != "" {
		return command.LogName
	}
	return command.Name
}

// commandString renders a command without environment or stdin values.
func commandString(command e2eexec.Command) string {
	parts := append([]string{command.Name}, command.Args...)
	return strings.Join(parts, " ")
}

// labelFilterOrNone formats Ginkgo labels for verbose plan output.
func labelFilterOrNone(labels []string) string {
	if len(labels) == 0 {
		return "none"
	}
	return strings.Join(labels, " || ")
}
