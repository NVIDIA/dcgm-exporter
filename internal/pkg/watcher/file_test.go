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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

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

func TestFileWatcher_WatchReportsDebouncedFileChange(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("before"), 0o600); err != nil {
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

	time.Sleep(50 * time.Millisecond)
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
