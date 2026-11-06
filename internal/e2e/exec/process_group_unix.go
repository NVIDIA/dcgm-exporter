//go:build unix

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
	"errors"
	stdexec "os/exec"
	"syscall"
)

// configureCommandProcessGroup places each command in its own process group so
// timeout handling can stop the command and every helper it starts.
func configureCommandProcessGroup(cmd *stdexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateCommandProcessGroup asks every process in a command group to stop
// gracefully when its e2e context expires.
func terminateCommandProcessGroup(cmd *stdexec.Cmd) error {
	return signalCommandProcessGroup(cmd, syscall.SIGTERM)
}

// killCommandProcessGroup force-stops every process in a command group after
// graceful termination has exceeded its grace period.
func killCommandProcessGroup(cmd *stdexec.Cmd) error {
	return signalCommandProcessGroup(cmd, syscall.SIGKILL)
}

// signalCommandProcessGroup sends one Unix signal to a command and all of its
// child processes, treating an already-exited group as successfully stopped.
func signalCommandProcessGroup(cmd *stdexec.Cmd, signal syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
