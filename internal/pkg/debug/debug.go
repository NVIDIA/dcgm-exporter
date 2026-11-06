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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

const dumpTimestampFormat = "20060102-150405"

var dumpFilenamePattern = regexp.MustCompile(
	`^(metrics|deviceinfo)-(GPU|NvSwitch|NvLink|CPU|CPU Core|Host)-(\d{8}-\d{6})-([a-f0-9]{8})\.json(?:\.gz)?$`,
)

// FileDumper writes optional JSON snapshots and removes only its own expired dump files.
// MetricsServer creates one when debug dumping is enabled and calls it while serving metrics.
type FileDumper struct {
	config       appconfig.DumpConfig
	mu           sync.Mutex
	lastAttempts map[string]time.Time
	now          func() time.Time
}

// NewFileDumper builds the dumper MetricsServer uses when debug dumping is enabled.
func NewFileDumper(config appconfig.DumpConfig) *FileDumper {
	config.Directory = trimDirectorySuffix(config.Directory)
	return &FileDumper{
		config:       config,
		lastAttempts: make(map[string]time.Time),
		now:          time.Now,
	}
}

// trimDirectorySuffix exposes a final symlink to Lstat without changing interior
// parent components, whose resolution can depend on earlier directory symlinks.
func trimDirectorySuffix(directory string) string {
	for {
		trimmed := strings.TrimRight(directory, "/")
		if trimmed == "" && directory != "" {
			return "/"
		}
		if !strings.HasSuffix(trimmed, "/.") {
			return trimmed
		}
		directory = strings.TrimSuffix(trimmed, ".")
	}
}

// DumpToFile stores a metrics or device-information snapshot in the configured directory.
// MetricsServer passes a supported prefix and entity group; the completed dump is published only after finalization.
func (fd *FileDumper) DumpToFile(data any, prefix, suffix string) (string, error) {
	if !fd.config.Enabled {
		return "", nil
	}

	fd.mu.Lock()
	defer fd.mu.Unlock()
	now := fd.now()
	admitted, err := fd.admit(prefix, suffix, now)
	if !admitted || err != nil {
		return "", err
	}
	filename, err := fd.prepareWrite(prefix, suffix, now)
	if err != nil {
		return "", err
	}

	fullPath := strings.TrimSuffix(fd.config.Directory, "/") + "/" + filename

	temporaryPath := fullPath + ".tmp"
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary debug file; provide a writable dump mount: %w", err)
	}
	removeTemporaryFile := true
	defer func() {
		if removeTemporaryFile {
			if err := os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) {
				slog.Warn("Failed to remove incomplete debug file",
					slog.String("file", temporaryPath),
					slog.String("error", err.Error()))
			}
		}
	}()

	if err := writeDump(&limitedDumpWriter{WriteCloser: file, remaining: fd.config.MaxFileBytes}, data, fd.config.Compression); err != nil {
		return "", fmt.Errorf("failed to write debug file: %w", err)
	}
	if err := os.Rename(temporaryPath, fullPath); err != nil {
		return "", fmt.Errorf("failed to publish debug file: %w", err)
	}
	removeTemporaryFile = false

	slog.Debug("Debug file written",
		slog.String("file", fullPath),
		slog.String("prefix", prefix),
		slog.String("suffix", suffix))

	return fullPath, nil
}

// admit records an attempt for the prefix and suffix pair and reports whether
// it may run: a pair is admitted at most once per minimum interval, whether or
// not the attempt then succeeds. The caller holds fd.mu.
func (fd *FileDumper) admit(prefix, suffix string, now time.Time) (bool, error) {
	// Validate the finite pair before recording admission; skipped attempts need no entropy.
	candidate := fmt.Sprintf("%s-%s-%s-00000000.json", prefix, suffix, now.Format(dumpTimestampFormat))
	if !isDumpFilename(candidate) {
		return false, fmt.Errorf("unsupported debug dump name: prefix=%q suffix=%q", prefix, suffix)
	}

	key := prefix + "/" + suffix
	if previous, ok := fd.lastAttempts[key]; ok && now.Sub(previous) < fd.config.MinInterval {
		return false, nil
	}
	fd.lastAttempts[key] = now
	return true, nil
}

// prepareWrite picks a new file name, checks the directory and frees enough
// space for one full-size file. The caller holds fd.mu.
func (fd *FileDumper) prepareWrite(prefix, suffix string, now time.Time) (string, error) {
	filename, err := newDumpFilename(prefix, suffix, now, fd.config.Compression)
	if err != nil {
		return "", err
	}
	if err := fd.config.Validate(); err != nil {
		return "", err
	}
	if err := fd.prepareDirectory(); err != nil {
		return "", err
	}
	if err := fd.reserve(now); err != nil {
		return "", fmt.Errorf("debug dump directory %q: cannot reserve storage: %w", fd.config.Directory, err)
	}
	return filename, nil
}

// CleanupOldFiles removes expired regular files that match the exporter dump name format.
// MetricsServer calls it periodically only while debug dumping is enabled.
func (fd *FileDumper) CleanupOldFiles() error {
	if !fd.config.Enabled || fd.config.Retention <= 0 {
		return nil
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	files, err := fd.ownedFiles()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fd.removeExpired(files, fd.now())
}

// prepareDirectory creates private directories and leaves existing mounts untouched.
func (fd *FileDumper) prepareDirectory() error {
	if err := os.MkdirAll(fd.config.Directory, 0o700); err != nil {
		return fmt.Errorf("failed to create debug directory %q; provide a writable dump mount: %w", fd.config.Directory, err)
	}
	return fd.validateDirectory()
}

// validateDirectory never creates or changes an existing mount, including during cleanup.
func (fd *FileDumper) validateDirectory() error {
	info, err := os.Lstat(fd.config.Directory)
	if err != nil {
		return fmt.Errorf("inspect debug directory %q: %w", fd.config.Directory, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("debug directory %q must be a directory; provide a writable dump mount", fd.config.Directory)
	}
	return nil
}

// ownedDump is one exporter dump file and the metadata read when it was listed.
type ownedDump struct {
	path string
	info os.FileInfo
}

// ownedFiles accounts only regular files in the existing exporter namespace.
func (fd *FileDumper) ownedFiles() ([]ownedDump, error) {
	if err := fd.validateDirectory(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(fd.config.Directory)
	if err != nil {
		return nil, fmt.Errorf("failed to read debug directory %q; provide a readable dump mount: %w", fd.config.Directory, err)
	}
	var files []ownedDump
	for _, entry := range entries {
		if !isDumpFilename(strings.TrimSuffix(entry.Name(), ".tmp")) {
			continue
		}
		path := strings.TrimSuffix(fd.config.Directory, "/") + "/" + entry.Name()
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			// Removed by another process after ReadDir.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect owned debug file %q: %w", path, err)
		}
		if info.Mode().IsRegular() {
			files = append(files, ownedDump{path, info})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].info.ModTime().Equal(files[j].info.ModTime()) {
			return files[i].path < files[j].path
		}
		return files[i].info.ModTime().Before(files[j].info.ModTime())
	})
	return files, nil
}

// removeExpired deletes owned files older than the retention period, keeps
// going past files it cannot remove, and returns those failures together.
func (fd *FileDumper) removeExpired(files []ownedDump, now time.Time) error {
	if fd.config.Retention <= 0 {
		return nil
	}
	cutoff := now.Add(-fd.config.Retention)
	var errs []error
	for _, file := range files {
		if file.info.ModTime().Before(cutoff) {
			if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove expired debug file %q; provide a writable dump mount: %w", file.path, err))
			}
		}
	}
	return errors.Join(errs...)
}

// reserve leaves room for one complete maximum-sized file, including temporary output.
func (fd *FileDumper) reserve(now time.Time) error {
	files, err := fd.ownedFiles()
	if err != nil {
		return err
	}
	if err := fd.removeExpired(files, now); err != nil {
		return err
	}
	var retained []ownedDump
	var total int64
	for _, file := range files {
		if fd.config.Retention > 0 && file.info.ModTime().Before(now.Add(-fd.config.Retention)) {
			continue
		}
		if file.info.Size() > int64(1<<63-1)-total {
			return fmt.Errorf("owned debug file sizes overflow storage accounting")
		}
		total += file.info.Size()
		retained = append(retained, file)
	}
	for len(retained) > 0 && (total > fd.config.MaxTotalBytes-fd.config.MaxFileBytes || int64(len(retained)) >= fd.config.MaxFiles) {
		file := retained[0]
		// A file removed by another process after listing already freed its space.
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove debug file for quota %q: %w", file.path, err)
		}
		total -= file.info.Size()
		retained = retained[1:]
	}

	return nil
}

var errDumpTooLarge = errors.New("debug dump exceeds the maximum dump file size")

// limitedDumpWriter bounds bytes at the filesystem sink, below gzip finalization.
type limitedDumpWriter struct {
	io.WriteCloser
	remaining int64
}

// Write passes at most the remaining byte budget to the file and fails once
// the dump would exceed it.
func (w *limitedDumpWriter) Write(data []byte) (int, error) {
	exceeded := int64(len(data)) > w.remaining
	if exceeded {
		data = data[:int(w.remaining)]
	}
	n, err := w.WriteCloser.Write(data)
	w.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	if exceeded {
		return n, errDumpTooLarge
	}
	if n != len(data) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

// newDumpFilename creates a valid exporter dump name with a fresh random token.
// DumpToFile calls it before creating a file so cleanup recognizes only exporter-owned dumps.
func newDumpFilename(prefix, suffix string, timestamp time.Time, compressed bool) (string, error) {
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	filename := fmt.Sprintf(
		"%s-%s-%s-%s.json",
		prefix,
		suffix,
		timestamp.Format(dumpTimestampFormat),
		hex.EncodeToString(randomBytes),
	)
	if compressed {
		filename += ".gz"
	}
	if !isDumpFilename(filename) {
		return "", fmt.Errorf("unsupported debug dump name: prefix=%q suffix=%q", prefix, suffix)
	}

	return filename, nil
}

// isDumpFilename reports whether a name is an exporter dump with a valid timestamp.
// CleanupOldFiles uses it to leave unrelated or malformed directory entries untouched.
func isDumpFilename(filename string) bool {
	matches := dumpFilenamePattern.FindStringSubmatch(filename)
	if matches == nil {
		return false
	}

	_, err := time.Parse(dumpTimestampFormat, matches[3])
	return err == nil
}

// writeDump encodes a snapshot and closes its compressor and file before reporting success.
// DumpToFile uses its error to avoid returning a path for an incomplete dump.
func writeDump(file io.WriteCloser, data any, compressed bool) error {
	var writer io.Writer = file
	var gzipWriter *gzip.Writer
	if compressed {
		gzipWriter = gzip.NewWriter(file)
		writer = gzipWriter
	}

	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encodeErr := encoder.Encode(data)

	var closeErr error
	if gzipWriter != nil {
		closeErr = errors.Join(closeErr, gzipWriter.Close())
	}
	closeErr = errors.Join(closeErr, file.Close())

	if encodeErr != nil {
		return fmt.Errorf("failed to encode data: %w", encodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to finalize debug dump: %w", closeErr)
	}

	return nil
}
