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

package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

// daemonChildEnv marks the process started by --daemon, so that it runs in the foreground of its new session
// instead of starting another copy.
const daemonChildEnv = "DCGM_EXPORTER_DAEMON_CHILD"

// startDaemon starts args[0] with args[1:] in a new session, detached from the calling terminal, and returns once
// the process has started. Go cannot fork a running process, so the executable is started again instead.
func startDaemon(args []string, logFile string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find the dcgm-exporter executable: %w", err)
	}

	pid, err := startDetached(executable, args[1:], logFile)
	if err != nil {
		return err
	}

	slog.Info("Started dcgm-exporter in the background", slog.Int("pid", pid))

	return nil
}

func startDetached(executable string, args []string, logFile string) (int, error) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return 0, fmt.Errorf("failed to open %s: %w", os.DevNull, err)
	}
	defer stdin.Close()

	// The output must not stay on the caller's terminal or pipe: a write to a closed pipe on stdout or stderr
	// stops a Go program.
	outputPath := os.DevNull
	if logFile != "" {
		outputPath = logFile
	}

	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("failed to open daemon log file %s: %w", outputPath, err)
	}
	defer output.Close()

	// #nosec G702 -- the executable is this process's own binary and the arguments are its own command line.
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1")
	cmd.Stdin = stdin
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("failed to start dcgm-exporter in the background: %w", err)
	}

	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, fmt.Errorf("failed to release the background process %d: %w", pid, err)
	}

	return pid, nil
}
