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
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// The host's NVIDIA CDI spec, which the MIG step regenerates while MIG is changed.
const (
	cdiSpecDir  = "/etc/cdi"
	cdiSpecPath = cdiSpecDir + "/nvidia.yaml"
)

// cdiSpecStatRE matches "stat -c '%a %u:%g'" output: an octal mode and a numeric owner and group.
var cdiSpecStatRE = regexp.MustCompile(`^([0-7]{3,4}) ([0-9]+):([0-9]+)$`)

// cdiSpecState is the host's NVIDIA CDI spec as it was before the run changed MIG.
type cdiSpecState struct {
	dirExisted bool
	exists     bool
	content    []byte
	mode       string
	uid, gid   string
}

// saveCDISpec records whether the CDI directory and spec exist, and the spec's bytes, mode and
// owner, so restoreCDISpec can put the host back as it found it. A spec that is a symbolic link
// is refused before MIG changes, because regenerating it would write through the link.
func saveCDISpec(ctx context.Context, runner e2eexec.Runner) (cdiSpecState, error) {
	var state cdiSpecState
	var err error
	if state.dirExisted, err = pathExists(ctx, runner, "-d", cdiSpecDir); err != nil {
		return cdiSpecState{}, err
	}

	link, err := pathExists(ctx, runner, "-L", cdiSpecPath)
	if err != nil {
		return cdiSpecState{}, err
	}
	if link {
		return cdiSpecState{}, fmt.Errorf("refusing to configure MIG: %s is a symbolic link, which E2E cannot restore after regenerating it", cdiSpecPath)
	}

	if state.exists, err = pathExists(ctx, runner, "-e", cdiSpecPath); err != nil || !state.exists {
		return state, err
	}

	stat := runner.Run(ctx, sudoCommand("stat", "-c", "%a %u:%g", cdiSpecPath))
	if stat.ExitCode != 0 || stat.Err != nil {
		return cdiSpecState{}, fmt.Errorf("read the mode and owner of %s before MIG configuration: %w", cdiSpecPath, runResultError("stat", stat))
	}
	match := cdiSpecStatRE.FindStringSubmatch(strings.TrimSpace(string(stat.Stdout)))
	if match == nil {
		return cdiSpecState{}, fmt.Errorf("read the mode and owner of %s before MIG configuration: unexpected stat output %q", cdiSpecPath, stat.Stdout)
	}
	state.mode, state.uid, state.gid = match[1], match[2], match[3]

	content := runner.Run(ctx, sudoCommand("cat", cdiSpecPath))
	if content.ExitCode != 0 || content.Err != nil {
		return cdiSpecState{}, fmt.Errorf("read %s before MIG configuration: %w", cdiSpecPath, runResultError("cat", content))
	}
	state.content = content.Stdout
	return state, nil
}

// pathExists runs "test flag path" and reports its answer. Any result other than true or false
// is an error, so an unreadable path is never mistaken for a missing one.
func pathExists(ctx context.Context, runner e2eexec.Runner, flag, path string) (bool, error) {
	result := runner.Run(ctx, sudoCommand("test", flag, path))
	switch {
	case result.ExitCode == 0 && result.Err == nil:
		return true, nil
	case result.ExitCode == 1:
		return false, nil
	default:
		return false, fmt.Errorf("check %s: %w", path, runResultError("test", result))
	}
}

// restoreCDISpec puts the saved spec back with its mode and owner, or removes the spec, and the
// CDI directory if the run created it, when they did not exist before.
func restoreCDISpec(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, state cdiSpecState) error {
	if state.exists {
		install := sudoCommand("install", "-m", state.mode, "-o", state.uid, "-g", state.gid, "/dev/stdin", cdiSpecPath)
		install.Stdin = state.content
		return runInstallCommand(ctx, stdout, runner, "restore_cdi_spec", install)
	}

	err := runInstallCommand(ctx, stdout, runner, "remove_cdi_spec", sudoCommand("rm", "-f", cdiSpecPath))
	if state.dirExisted {
		return err
	}

	// The run may never have created the directory, for example without nvidia-ctk.
	created, checkErr := pathExists(ctx, runner, "-d", cdiSpecDir)
	if checkErr != nil || !created {
		return errors.Join(err, checkErr)
	}
	return errors.Join(err, runInstallCommand(ctx, stdout, runner, "remove_cdi_dir",
		sudoCommand("rmdir", "--ignore-fail-on-non-empty", cdiSpecDir)))
}
