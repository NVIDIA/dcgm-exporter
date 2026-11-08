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

package exec

import (
	"context"
	"errors"
	"os"
	stdexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOSRunnerCancellationTerminatesChildHoldingOutputPipe verifies a timeout
// stops background children that would otherwise keep the command output open.
func TestOSRunnerCancellationTerminatesChildHoldingOutputPipe(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancelled := cancelWhenPIDWritten(t, pidFile)

	result := OSRunner{}.Run(ctx, Command{
		Name: "sh",
		Args: []string{"-c", "trap '' TERM; (trap '' TERM; while :; do sleep 1; done) & child=$!; printf %s \"$child\" > \"$1\"; wait", "sh", pidFile},
	})
	finished := time.Now()
	if result.ExitCode == 0 {
		t.Fatal("cancelled command exited successfully")
	}
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Err = %v, want context.Canceled", result.Err)
	}
	if !strings.Contains(string(result.Stderr), "command stopped: context canceled") {
		t.Fatalf("Stderr = %q, want the cancellation cause", result.Stderr)
	}
	// Shutdown is timed from cancellation, so a slow start does not count.
	if elapsed := finished.Sub(<-cancelled); elapsed > processTerminationGrace+time.Second {
		t.Fatalf("cancellation took %s, want bounded process-group shutdown", elapsed)
	}

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse child PID: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		status, statusErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if errors.Is(statusErr, os.ErrNotExist) || processIsZombie(status) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child PID %d remains after cancellation: kill=%v status=%q", pid, err, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestOSRunnerCancellationKillsChildAfterWaitDelay verifies the child is gone after the runner escalates beyond TERM.
func TestOSRunnerCancellationKillsChildAfterWaitDelay(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, _ := cancelWhenPIDWritten(t, pidFile)

	result := OSRunner{}.Run(ctx, Command{
		Name: "sh",
		Args: []string{"-c", "(trap '' TERM; while :; do sleep 1; done) & child=$!; printf %s \"$child\" > \"$1\"; trap 'exit 0' TERM; wait", "sh", pidFile},
	})
	if result.ExitCode == 0 {
		t.Fatal("cancelled command exited successfully")
	}

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse child PID: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		status, statusErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if errors.Is(statusErr, os.ErrNotExist) || processIsZombie(status) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child PID %d remains after WaitDelay: kill=%v status=%q", pid, err, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cancelWhenPIDWritten returns a context that is cancelled once the command
// has written its child PID to path, and a channel that receives the time just
// before that cancellation. A fixed timeout can fire before a slow shell writes
// the file; this way the child always exists when cancellation starts. The
// context is also cancelled after 10 seconds or at test cleanup.
func cancelWhenPIDWritten(t *testing.T, path string) (context.Context, <-chan time.Time) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cancelled := make(chan time.Time, 1)

	go func() {
		defer func() {
			cancelled <- time.Now()
			cancel()
		}()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.After(10 * time.Second)
		for {
			if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-deadline:
				return
			case <-ticker.C:
			}
		}
	}()

	return ctx, cancelled
}

// processIsZombie reports whether Linux /proc process status describes an
// exited process that has not yet been reaped by its parent.
func processIsZombie(stat []byte) bool {
	fields := strings.Fields(string(stat))
	return len(fields) > 2 && fields[2] == "Z"
}

// TestOSRunnerCancellationBoundsEscapedOutputPipe checks cancellation returns promptly when a detached child keeps the pipe open.
func TestOSRunnerCancellationBoundsEscapedOutputPipe(t *testing.T) {
	if _, err := stdexec.LookPath("setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}
	pidFile := filepath.Join(t.TempDir(), "escaped.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	started := time.Now()
	result := OSRunner{}.Run(ctx, Command{
		Name: "sh",
		Args: []string{"-c", "setsid sleep 30 & child=$!; printf %s \"$child\" > \"$1\"; wait", "sh", pidFile},
	})
	elapsed := time.Since(started)

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read escaped child PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse escaped child PID: %v", err)
	}
	t.Cleanup(func() {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("stop escaped child: %v", err)
		}
	})
	if result.ExitCode == 0 {
		t.Fatal("cancelled command exited successfully")
	}
	if elapsed > processTerminationGrace+time.Second {
		t.Fatalf("escaped output pipe kept cancellation waiting for %s", elapsed)
	}
}

// TestOSRunnerNamesHeldOutputPipeStop reports a distinct wait-delay error when a detached child holds output open.
func TestOSRunnerNamesHeldOutputPipeStop(t *testing.T) {
	if _, err := stdexec.LookPath("setsid"); err != nil {
		t.Skip("setsid is unavailable")
	}
	pidFile := filepath.Join(t.TempDir(), "escaped.pid")
	result := OSRunner{}.Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-c", "setsid sleep 30 & printf %s \"$!\" > \"$1\"", "sh", pidFile},
	})
	if pidBytes, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err == nil {
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		}
	}
	if !errors.Is(result.Err, stdexec.ErrWaitDelay) {
		t.Fatalf("Err = %v, want ErrWaitDelay", result.Err)
	}
	if !strings.Contains(string(result.Stderr), "kept its output open") {
		t.Fatalf("Stderr = %q, want the held output pipe named", result.Stderr)
	}
}
