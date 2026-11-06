/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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

package watcher

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

type watcherReadyWriter chan struct{}

// Write signals readiness when the file watcher logs its initialized state.
func (ready watcherReadyWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "Initialized file watcher with current timestamp") {
		select {
		case ready <- struct{}{}:
		default:
		}
	}
	return len(data), nil
}

// waitForFileWatcher installs a test log handler and returns a function that
// waits for watcher readiness.
func waitForFileWatcher(t *testing.T) func() {
	t.Helper()
	ready := make(watcherReadyWriter, 1)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(ready, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() {
		t.Helper()
		select {
		case <-ready:
		case <-time.After(2 * time.Second):
			t.Fatal("file watcher did not initialize")
		}
	}
}

// TestNewFileWatcher_DefaultOptions checks that a watcher made without options keeps the file path, waits
// 200ms before reporting, and reacts to create, write, remove and rename events.
func TestNewFileWatcher_DefaultOptions(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	fw := NewFileWatcher(testFile)

	if fw.filePath != testFile {
		t.Errorf("expected filePath=%s, got %s", testFile, fw.filePath)
	}

	if fw.debounceDelay != 200*time.Millisecond {
		t.Errorf("expected default debounceDelay=200ms, got %v", fw.debounceDelay)
	}

	expectedMask := fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	if fw.eventMask != expectedMask {
		t.Errorf("expected default eventMask=%v, got %v", expectedMask, fw.eventMask)
	}
}

// TestNewFileWatcher_WithOptions checks that the debounce delay and event mask options replace the defaults.
func TestNewFileWatcher_WithOptions(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	customDelay := 500 * time.Millisecond
	customMask := fsnotify.Write | fsnotify.Create

	fw := NewFileWatcher(
		testFile,
		WithDebounceDelay(customDelay),
		WithEventMask(customMask),
	)

	if fw.debounceDelay != customDelay {
		t.Errorf("expected debounceDelay=%v, got %v", customDelay, fw.debounceDelay)
	}

	if fw.eventMask != customMask {
		t.Errorf("expected eventMask=%v, got %v", customMask, fw.eventMask)
	}
}

// TestNewFileWatcher_MultipleOptions passes the delay and event mask options as one slice and checks that
// both are applied.
func TestNewFileWatcher_MultipleOptions(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	// Demonstrates functional options pattern - options can be composed
	opts := []FileWatcherOption{
		WithDebounceDelay(1 * time.Second),
		WithEventMask(fsnotify.Write),
	}

	fw := NewFileWatcher(testFile, opts...)

	if fw.debounceDelay != 1*time.Second {
		t.Errorf("expected debounceDelay=1s, got %v", fw.debounceDelay)
	}

	if fw.eventMask != fsnotify.Write {
		t.Errorf("expected eventMask=Write, got %v", fw.eventMask)
	}
}

// TestFileWatcher_WatchReportsDebouncedFileChange writes to a watched file and expects
// the callback within two seconds, then expects Watch to return context.Canceled once cancelled.
func TestFileWatcher_WatchReportsDebouncedFileChange(t *testing.T) {
	waitUntilReady := waitForFileWatcher(t)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Ensure the next write changes the timestamp even on coarse filesystems.
	initialModTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(testFile, initialModTime, initialModTime); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fw := NewFileWatcher(testFile, WithDebounceDelay(10*time.Millisecond))
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- fw.Watch(ctx, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()

	waitUntilReady()
	if err := os.WriteFile(testFile, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("file watcher did not report the write")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("file watcher did not stop")
	}
}

func TestFileWatcher_WatchReportsReplacementWithSameModTime(t *testing.T) {
	waitUntilReady := waitForFileWatcher(t)
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(testFile, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(testFile)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- NewFileWatcher(testFile, WithDebounceDelay(10*time.Millisecond)).Watch(ctx, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()
	waitUntilReady()
	replacement := filepath.Join(tmpDir, "replacement.yaml")
	if err := os.WriteFile(replacement, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, original.ModTime(), original.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, testFile); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("file watcher missed replacement with unchanged modification time")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("file watcher did not stop")
	}
}

func TestFileWatcher_WatchReportsProjectedVolumeUpdate(t *testing.T) {
	waitUntilReady := waitForFileWatcher(t)
	dir := t.TempDir()
	filePath := filepath.Join(dir, "config.yaml")
	for _, version := range []string{"..first", "..second"} {
		versionDir := filepath.Join(dir, version)
		if err := os.Mkdir(versionDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(versionDir, "config.yaml"), []byte(version), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("..first", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/config.yaml", filePath); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- NewFileWatcher(filePath, WithDebounceDelay(10*time.Millisecond)).Watch(ctx, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()
	waitUntilReady()
	if err := os.Symlink("..second", filepath.Join(dir, "..data_tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("file watcher missed projected volume update")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("file watcher did not stop")
	}
}

// TestFileWatcher_WatchReturnsDirectoryError expects a "failed to watch directory" error when
// the watched file's directory does not exist.
func TestFileWatcher_WatchReturnsDirectoryError(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "missing")
	fw := NewFileWatcher(filepath.Join(missingDir, "test.txt"))

	err := fw.Watch(context.Background(), func() {})

	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); !strings.Contains(got, "failed to watch directory") {
		t.Fatalf("expected directory watch error, got %q", got)
	}
}
