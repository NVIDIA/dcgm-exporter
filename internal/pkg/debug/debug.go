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

package debug

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// FileDumper handles file-based debugging output
type FileDumper struct {
	config appconfig.DumpConfig
}

// NewFileDumper creates a new file dumper with the given configuration
func NewFileDumper(config appconfig.DumpConfig) *FileDumper {
	return &FileDumper{
		config: config,
	}
}

// DumpToFile writes any JSON-serializable data to a file and returns the filename
func (fd *FileDumper) DumpToFile(data any, prefix, suffix string) (string, error) {
	if !fd.config.Enabled {
		return "", nil
	}

	// Generate a random 8-character hex string to prevent collisions
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	randomStr := hex.EncodeToString(randomBytes)

	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s-%s-%s", prefix, suffix, timestamp, randomStr)

	if fd.config.Compression {
		filename += ".json.gz"
	} else {
		filename += ".json"
	}

	fullPath := filepath.Join(fd.config.Directory, filename)

	// Ensure directory exists
	if err := os.MkdirAll(fd.config.Directory, 0o755); err != nil {
		return "", fmt.Errorf("failed to create debug directory: %w", err)
	}

	// Create file
	file, err := os.Create(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to create debug file: %w", err)
	}
	defer file.Close()

	// Write with or without compression
	if fd.config.Compression {
		gz := gzip.NewWriter(file)
		defer gz.Close()

		encoder := json.NewEncoder(gz)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(data); err != nil {
			return "", fmt.Errorf("failed to encode data: %w", err)
		}
	} else {
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(data); err != nil {
			return "", fmt.Errorf("failed to encode data: %w", err)
		}
	}

	slog.Debug("Debug file written",
		slog.String("file", fullPath),
		slog.String("prefix", prefix),
		slog.String("suffix", suffix))

	return fullPath, nil
}

// CleanupOldFiles removes debug files older than the retention period
func (fd *FileDumper) CleanupOldFiles() error {
	if fd.config.Retention <= 0 {
		return nil
	}

	cutoff := time.Now().Add(-time.Duration(fd.config.Retention) * time.Hour)

	files, err := os.ReadDir(fd.config.Directory)
	if err != nil {
		// If the directory doesn't exist, treat it as a no-op (no files to clean up)
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read debug directory: %w", err)
	}

	removed := 0
	for _, file := range files {
		if info, err := file.Info(); err == nil && info.ModTime().Before(cutoff) {
			fullPath := filepath.Join(fd.config.Directory, file.Name())
			if err := os.Remove(fullPath); err != nil {
				slog.Warn("Failed to remove old debug file",
					slog.String("file", fullPath),
					slog.String("error", err.Error()))
			} else {
				removed++
			}
		}
	}

	if removed > 0 {
		slog.Debug("Cleaned up old debug files",
			slog.Int("removed_count", removed),
			slog.String("directory", fd.config.Directory))
	}

	return nil
}
