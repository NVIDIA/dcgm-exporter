/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher monitors a file for changes using fsnotify.
type FileWatcher struct {
	filePath      string
	debounceDelay time.Duration
	eventMask     fsnotify.Op
}

// FileWatcherOption configures a FileWatcher.
type FileWatcherOption func(*FileWatcher)

// WithDebounceDelay sets the debounce delay for file change events.
// Default is 200ms.
func WithDebounceDelay(delay time.Duration) FileWatcherOption {
	return func(fw *FileWatcher) {
		fw.debounceDelay = delay
	}
}

// WithEventMask sets which filesystem events to watch for.
// Default is Create|Write|Remove|Rename.
func WithEventMask(mask fsnotify.Op) FileWatcherOption {
	return func(fw *FileWatcher) {
		fw.eventMask = mask
	}
}

// NewFileWatcher creates a new file watcher for the specified file path.
// Accepts optional configuration via FileWatcherOption functions.
func NewFileWatcher(filePath string, opts ...FileWatcherOption) *FileWatcher {
	fw := &FileWatcher{
		filePath:      filePath,
		debounceDelay: 200 * time.Millisecond,
		eventMask:     fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename,
	}

	for _, opt := range opts {
		opt(fw)
	}

	return fw
}

// Watch starts monitoring the file and calls onChange when the file is modified.
// It blocks until the context is cancelled and returns nil on clean shutdown.
func (fw *FileWatcher) Watch(ctx context.Context, onChange func()) error {
	slog.Info("Watching for changes in file",
		slog.String("file", fw.filePath),
		slog.Duration("debounce", fw.debounceDelay))

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer watcher.Close()

	dir := filepath.Dir(fw.filePath)
	file := filepath.Base(fw.filePath)

	err = watcher.Add(dir)
	if err != nil {
		return fmt.Errorf("failed to watch directory %s: %w", dir, err)
	}

	// Initialize lastModTime with current file timestamp to avoid spurious reload on startup
	// We want to detect file CHANGES, not the initial state
	filePath := filepath.Join(dir, file)
	var lastModTime time.Time
	if info, err := os.Stat(filePath); err == nil {
		lastModTime = info.ModTime()
		slog.Debug("Initialized file watcher with current timestamp",
			slog.String("file", fw.filePath),
			slog.Time("initial_modtime", lastModTime))
	}

	var (
		debounceTimer *time.Timer
		timerCh       <-chan time.Time
	)

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			slog.Debug("File watcher stopping", slog.String("file", fw.filePath))
			return ctx.Err()

		case <-timerCh:
			// Debounce timer expired, check if file was actually modified
			info, err := os.Stat(filepath.Join(dir, file))
			if err == nil {
				modTime := info.ModTime()
				if modTime != lastModTime {
					lastModTime = modTime
					onChange()
				}
			}
			timerCh = nil

		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("watcher events channel closed")
			}

			if event.Op&fw.eventMask != 0 && filepath.Base(event.Name) == file {
				// Reset or create debounce timer
				if debounceTimer == nil {
					debounceTimer = time.NewTimer(fw.debounceDelay)
					timerCh = debounceTimer.C
				} else {
					if !debounceTimer.Stop() {
						select {
						case <-debounceTimer.C:
						default:
						}
					}
					debounceTimer.Reset(fw.debounceDelay)
					timerCh = debounceTimer.C
				}
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("watcher errors channel closed")
			}
			slog.Warn("File watcher error",
				slog.String("file", fw.filePath),
				slog.String("error", err.Error()))
		}
	}
}
