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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// acquireMIGLock excludes other cooperating runs until restoration completes.
// The fixed host directory is root-owned; non-root callers can lock its file read-only.
func acquireMIGLock(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, path string, owner int) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquire MIG lock: %w", err)
	}
	root, err := openMIGLockDirectory(ctx, stdout, runner, filepath.Dir(path), owner)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := openMIGLockFile(ctx, stdout, runner, root, path, owner)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("MIG configuration on GPU %s is already in use by another E2E run", defaultMIGGPUIndex)
		}
		return nil, fmt.Errorf("acquire MIG lock: %w", err)
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			// Closing the descriptor releases flock even if explicit unlock fails.
			releaseErr = errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
			if releaseErr != nil {
				releaseErr = fmt.Errorf("release MIG lock: %w", releaseErr)
			}
		})
		return releaseErr
	}, nil
}

// openMIGLockDirectory anchors inspection and opening in a trusted parent.
func openMIGLockDirectory(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, dir string, owner int) (*os.Root, error) {
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("open MIG lock parent directory: %w", err)
	}
	defer parent.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil || !trustedMIGLockOwner(parentInfo, owner) {
		return nil, fmt.Errorf("MIG lock parent directory must be owned by uid %d and not writable by group or others", owner)
	}
	dirName := filepath.Base(dir)
	if _, err := parent.Lstat(dirName); errors.Is(err, os.ErrNotExist) {
		if err := runInstallCommand(ctx, stdout, runner, "prepare_mig_lock_directory", sudoCommand("mkdir", "-p", "-m", "0755", "--", dir)); err != nil {
			return nil, fmt.Errorf("prepare MIG lock directory: %w", err)
		}
	}
	info, err := parent.Lstat(dirName)
	if err != nil {
		return nil, fmt.Errorf("inspect MIG lock directory: %w", err)
	}
	if !info.IsDir() || !trustedMIGLockOwner(info, owner) {
		return nil, fmt.Errorf("MIG lock directory %s must be owned by uid %d and not writable by group or others", dir, owner)
	}
	root, err := parent.OpenRoot(dirName)
	if err != nil {
		return nil, fmt.Errorf("open MIG lock directory: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("MIG lock directory changed while opening: %s", dir)
	}
	return root, nil
}

// openMIGLockFile verifies the opened descriptor still names the inspected inode.
func openMIGLockFile(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, root *os.Root, path string, owner int) (*os.File, error) {
	name := filepath.Base(path)
	if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
		// Noclobber creates only a missing file, including when another run races us.
		// Never replace or remove the inode: held locks belong to that inode.
		script := `umask 022; (set -C; : > "$1") || test -e "$1"`
		if err := runInstallCommand(ctx, stdout, runner, "prepare_mig_lock_file", sudoCommand("sh", "-c", script, "sh", path)); err != nil {
			return nil, fmt.Errorf("prepare MIG lock file: %w", err)
		}
	}
	checked, err := root.Lstat(name)
	if err != nil || !checked.Mode().IsRegular() || !trustedMIGLockOwner(checked, owner) {
		return nil, fmt.Errorf("MIG lock file %s must be a regular file owned by uid %d and not writable by group or others", path, owner)
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open MIG lock file: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(checked, info) || !info.Mode().IsRegular() || !trustedMIGLockOwner(info, owner) {
		file.Close()
		return nil, fmt.Errorf("MIG lock file %s must be a regular file owned by uid %d and not writable by group or others", path, owner)
	}
	return file, nil
}

// trustedMIGLockOwner rejects artifacts that another user could replace or modify.
func trustedMIGLockOwner(info os.FileInfo, owner int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner >= 0 && uint64(stat.Uid) == uint64(owner) && info.Mode().Perm()&0o022 == 0
}
