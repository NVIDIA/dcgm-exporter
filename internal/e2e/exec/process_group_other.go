//go:build !unix

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

import stdexec "os/exec"

// configureCommandProcessGroup leaves commands unchanged where process groups
// are unavailable. The cancellation path then stops only the direct process.
func configureCommandProcessGroup(_ *stdexec.Cmd) {
	// Process-group options are unavailable on this target, so nothing is configured here.
}

// terminateCommandProcessGroup stops the direct command where process groups
// are unavailable. It is used when the e2e context expires.
func terminateCommandProcessGroup(cmd *stdexec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// killCommandProcessGroup force-stops the direct command after graceful
// termination is unavailable or has not completed.
func killCommandProcessGroup(cmd *stdexec.Cmd) error {
	// Forced termination is the same direct-process operation on this target.
	return terminateCommandProcessGroup(cmd)
}
