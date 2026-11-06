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

package debug

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// TestFileDumper_DumpToFile checks that FileDumper writes readable plain and compressed JSON.
func TestFileDumper_DumpToFile(t *testing.T) {
	tests := []struct {
		name        string
		compression bool
		suffix      string
		read        func(*testing.T, string) map[string]string
	}{
		{
			name:        "plain JSON",
			compression: false,
			suffix:      ".json",
			read: func(t *testing.T, path string) map[string]string {
				t.Helper()
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				var got map[string]string
				require.NoError(t, json.Unmarshal(data, &got))
				return got
			},
		},
		{
			name:        "gzip JSON",
			compression: true,
			suffix:      ".json.gz",
			read: func(t *testing.T, path string) map[string]string {
				t.Helper()
				f, err := os.Open(path)
				require.NoError(t, err)
				defer f.Close()

				gz, err := gzip.NewReader(f)
				require.NoError(t, err)
				defer gz.Close()

				var got map[string]string
				require.NoError(t, json.NewDecoder(gz).Decode(&got))
				return got
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{
				Enabled:     true,
				Directory:   t.TempDir(),
				Compression: tt.compression,
			}))

			path, err := fd.DumpToFile(map[string]string{"gpu": "0"}, "metrics", "GPU")

			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(path, tt.suffix))
			assert.Equal(t, map[string]string{"gpu": "0"}, tt.read(t, path))
			info, statErr := os.Stat(path)
			require.NoError(t, statErr)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		})
	}
}

// TestFileDumper_DumpToFileDisabled checks that disabled dumping does not create a file.
func TestFileDumper_DumpToFileDisabled(t *testing.T) {
	fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{Enabled: false, Directory: t.TempDir()}))

	path, err := fd.DumpToFile(map[string]string{"ignored": "true"}, "metrics", "GPU")

	require.NoError(t, err)
	assert.Empty(t, path)
}

// TestFileDumper_DumpToFileErrors checks that a directory creation failure is returned.
func TestFileDumper_DumpToFileErrors(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-directory")
	require.NoError(t, os.WriteFile(blockingFile, []byte("x"), 0o600))

	fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{
		Enabled:   true,
		Directory: filepath.Join(blockingFile, "child"),
	}))

	_, err := fd.DumpToFile(map[string]string{"gpu": "0"}, "metrics", "GPU")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create debug directory")
}

// TestFileDumper_DumpToFileRemovesFailedDump checks that a failed write never publishes a dump file.
func TestFileDumper_DumpToFileRemovesFailedDump(t *testing.T) {
	dir := t.TempDir()
	fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{Enabled: true, Directory: dir}))

	_, err := fd.DumpToFile(make(chan int), "metrics", "GPU")

	require.Error(t, err)
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

// TestFileDumper_DumpToFileRejectsUnrecognizedNames checks that dumps use the cleanup name contract.
func TestFileDumper_DumpToFileRejectsUnrecognizedNames(t *testing.T) {
	dir := t.TempDir()
	fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{Enabled: true, Directory: dir}))

	_, err := fd.DumpToFile(map[string]string{"gpu": "0"}, "unrecognized", "GPU")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported debug dump name")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

// TestFileDumper_CleanupOldFiles checks that retention removes only expired exporter-owned files.
func TestFileDumper_CleanupOldFiles(t *testing.T) {
	dir := t.TempDir()
	oldDump := filepath.Join(dir, "metrics-GPU-20200101-000000-deadbeef.json")
	oldCompressedDump := filepath.Join(dir, "deviceinfo-CPU Core-20200101-000000-deadbeef.json.gz")
	oldTemporaryDump := filepath.Join(dir, "metrics-GPU-20200101-000000-01234567.json.tmp")
	newDump := filepath.Join(dir, "metrics-GPU-20200101-000000-cafebabe.json")
	newTemporaryDump := filepath.Join(dir, "metrics-GPU-20200101-000000-89abcdef.json.tmp")
	oldUnrelatedFile := filepath.Join(dir, "old.json")
	oldMalformedFile := filepath.Join(dir, "metrics-GPU-not-a-time-deadbeef.json")
	oldDumpDirectory := filepath.Join(dir, "metrics-GPU-20200101-000000-feedface.json")
	oldDumpSymlink := filepath.Join(dir, "metrics-GPU-20200101-000000-01234567.json")
	target := filepath.Join(dir, "target.json")

	for _, path := range []string{
		oldDump,
		oldCompressedDump,
		oldTemporaryDump,
		newDump,
		newTemporaryDump,
		oldUnrelatedFile,
		oldMalformedFile,
		target,
	} {
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
	}
	require.NoError(t, os.Mkdir(oldDumpDirectory, 0o755))
	require.NoError(t, os.Symlink(target, oldDumpSymlink))

	oldTime := time.Now().Add(-3 * time.Hour)
	for _, path := range []string{
		oldDump,
		oldCompressedDump,
		oldTemporaryDump,
		oldUnrelatedFile,
		oldMalformedFile,
		oldDumpDirectory,
		target,
	} {
		require.NoError(t, os.Chtimes(path, oldTime, oldTime))
	}

	fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{Enabled: true, Directory: dir, Retention: time.Hour}))
	require.NoError(t, fd.CleanupOldFiles())

	assert.NoFileExists(t, oldDump)
	assert.NoFileExists(t, oldCompressedDump)
	assert.NoFileExists(t, oldTemporaryDump)
	assert.FileExists(t, newDump)
	assert.FileExists(t, newTemporaryDump)
	assert.FileExists(t, oldUnrelatedFile)
	assert.FileExists(t, oldMalformedFile)
	assert.DirExists(t, oldDumpDirectory)
	assert.FileExists(t, oldDumpSymlink)
}

// TestFileDumperRemovalFailureDoesNotBlockOtherExpiredFiles checks that one
// file that cannot be removed does not stop removal of the other expired files.
func TestFileDumperRemovalFailureDoesNotBlockOtherExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "metrics-GPU-20200101-000000-deadbeef.json")
	expired := filepath.Join(dir, "metrics-GPU-20200101-000000-cafebabe.json")
	vanished := filepath.Join(dir, "metrics-GPU-20200101-000000-01234567.json")
	now := time.Now()
	oldTime := now.Add(-2 * time.Hour)
	var files []ownedDump
	for _, path := range []string{blocked, expired, vanished} {
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
		require.NoError(t, os.Chtimes(path, oldTime, oldTime))
		info, err := os.Stat(path)
		require.NoError(t, err)
		files = append(files, ownedDump{path, info})
	}
	// Replace an observed file with a nonempty directory to make removal fail.
	require.NoError(t, os.Remove(blocked))
	require.NoError(t, os.Mkdir(blocked, 0o700))
	protected := filepath.Join(blocked, "unrelated")
	require.NoError(t, os.WriteFile(protected, []byte("keep"), 0o600))
	require.NoError(t, os.Remove(vanished))

	fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{Enabled: true, Directory: dir, Retention: time.Hour}))
	err := fd.removeExpired(files, now)
	require.ErrorContains(t, err, blocked)
	require.NotErrorIs(t, err, os.ErrNotExist)
	require.NotContains(t, err.Error(), vanished)
	require.NoFileExists(t, expired)
	require.FileExists(t, protected)
	require.NoError(t, fd.removeExpired(files[1:], now))
}

// TestIsDumpFilename checks that cleanup accepts only valid exporter-owned final filenames.
func TestIsDumpFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     bool
	}{
		{name: "plain dump", filename: "metrics-GPU-20260101-000000-deadbeef.json", want: true},
		{name: "compressed dump", filename: "deviceinfo-CPU Core-20260101-000000-deadbeef.json.gz", want: true},
		{name: "host metrics dump", filename: "metrics-Host-20260101-000000-deadbeef.json", want: true},
		{name: "invalid timestamp", filename: "metrics-GPU-20261301-000000-deadbeef.json"},
		{name: "unsupported prefix", filename: "unknown-GPU-20260101-000000-deadbeef.json"},
		{name: "temporary dump", filename: "metrics-GPU-20260101-000000-deadbeef.json.tmp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isDumpFilename(tt.filename))
		})
	}
}

// TestFileDumper_CleanupOldFilesDisabled checks that disabled dumping leaves old files in place.
func TestFileDumper_CleanupOldFilesDisabled(t *testing.T) {
	dir := t.TempDir()
	oldDump := filepath.Join(dir, "metrics-GPU-20200101-000000-deadbeef.json")
	require.NoError(t, os.WriteFile(oldDump, []byte("{}"), 0o600))
	oldTime := time.Now().Add(-3 * time.Hour)
	require.NoError(t, os.Chtimes(oldDump, oldTime, oldTime))

	fd := NewFileDumper(testDumpConfig(appconfig.DumpConfig{Enabled: false, Directory: dir, Retention: time.Hour}))
	require.NoError(t, fd.CleanupOldFiles())

	assert.FileExists(t, oldDump)
}

// TestFileDumper_CleanupOldFilesNoopAndErrors checks no-op and unreadable-directory behavior.
func TestFileDumper_CleanupOldFilesNoopAndErrors(t *testing.T) {
	assert.NoError(t, NewFileDumper(testDumpConfig(appconfig.DumpConfig{Retention: 0})).CleanupOldFiles())
	assert.NoError(t, NewFileDumper(testDumpConfig(appconfig.DumpConfig{
		Enabled:   true,
		Directory: filepath.Join(t.TempDir(), "missing"),
		Retention: time.Hour,
	})).CleanupOldFiles())

	dir := t.TempDir()
	notDirectory := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(notDirectory, []byte("x"), 0o600))

	err := NewFileDumper(testDumpConfig(appconfig.DumpConfig{
		Enabled:   true,
		Directory: notDirectory,
		Retention: time.Hour,
	})).CleanupOldFiles()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writable dump mount")
}

// TestWriteDumpReportsCloseErrors checks that final file and gzip writes are not silently ignored.
func TestWriteDumpReportsCloseErrors(t *testing.T) {
	fileCloseErr := errors.New("file close failed")
	gzipCloseErr := errors.New("gzip close failed")

	t.Run("file", func(t *testing.T) {
		err := writeDump(&closeErrorWriter{closeErr: fileCloseErr}, map[string]string{"gpu": "0"}, false)

		require.ErrorIs(t, err, fileCloseErr)
	})

	t.Run("gzip", func(t *testing.T) {
		file := &failAfterFirstWriteCloser{writeErr: gzipCloseErr}
		err := writeDump(file, nil, true)

		require.ErrorIs(t, err, gzipCloseErr)
		assert.Contains(t, err.Error(), "failed to finalize debug dump")
		assert.GreaterOrEqual(t, file.writeCount, 2)
	})
}

// closeErrorWriter accepts writes and returns a configured error when writeDump closes it.
// The close-error test uses it to verify that file finalization errors reach the caller.
type closeErrorWriter struct {
	bytes.Buffer
	closeErr error
}

// Close returns the configured error so the test can check writeDump reports it.
func (w *closeErrorWriter) Close() error {
	return w.closeErr
}

// failAfterFirstWriteCloser accepts the initial gzip write and rejects later writes.
// The gzip-close test uses it to make gzip.Writer.Close return a write error.
type failAfterFirstWriteCloser struct {
	bytes.Buffer
	writeCount int
	writeErr   error
}

// Write records calls and fails after the first one to simulate gzip finalization failure.
func (w *failAfterFirstWriteCloser) Write(data []byte) (int, error) {
	w.writeCount++
	if w.writeCount > 1 {
		return 0, w.writeErr
	}

	return w.Buffer.Write(data)
}

// Close succeeds so the gzip writer is the only source of the tested finalization error.
func (*failAfterFirstWriteCloser) Close() error {
	return nil
}

// testDumpConfig fills the quotas that bare DumpConfig literals leave at zero;
// a zero MaxFileBytes makes every dump write fail the per-file limit.
func testDumpConfig(c appconfig.DumpConfig) appconfig.DumpConfig {
	c.MinInterval = time.Minute
	c.MaxFileBytes = 16 << 20
	c.MaxTotalBytes = 256 << 20
	c.MaxFiles = 128
	return c
}

// TestDumpReservationsAndRestart checks that the size and count limits count
// files from an earlier dumper on the same directory.
func TestDumpReservationsAndRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = dir
	cfg.Retention = 0
	cfg.MaxFileBytes = 64
	cfg.MaxTotalBytes = 128
	cfg.MaxFiles = 2
	cfg.Compression = false
	now := time.Now()
	for i := 0; i < 4; i++ {
		name := filepath.Join(dir, fmt.Sprintf("metrics-GPU-20200101-000000-%08x.json.tmp", i))
		require.NoError(t, os.WriteFile(name, make([]byte, 50), 0o600))
		require.NoError(t, os.Chtimes(name, now.Add(-time.Duration(4-i)*time.Minute), now.Add(-time.Duration(4-i)*time.Minute)))
	}
	unrelated := filepath.Join(dir, "keep.txt")
	require.NoError(t, os.WriteFile(unrelated, make([]byte, 512), 0o600))
	target := filepath.Join(dir, "metrics-GPU-20200101-000000-ffffffff.json")
	require.NoError(t, os.Symlink(unrelated, target))
	dumper := NewFileDumper(cfg)
	path, err := dumper.DumpToFile(map[string]string{"a": "b"}, "metrics", "GPU")
	require.NoError(t, err)
	require.FileExists(t, path)
	files, err := dumper.ownedFiles()
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.FileExists(t, unrelated)
	require.FileExists(t, target)
	// A new owner reconstructs accounting from files rather than resetting quotas.
	restarted := NewFileDumper(cfg)
	_, err = restarted.DumpToFile(map[string]string{"a": "b"}, "metrics", "GPU")
	require.NoError(t, err)
	files, err = restarted.ownedFiles()
	require.NoError(t, err)
	require.LessOrEqual(t, len(files), 2)
	var total int64
	for _, file := range files {
		total += file.info.Size()
	}
	require.LessOrEqual(t, total, cfg.MaxTotalBytes)
}

// TestDumpOnDiskCapAndFailedAttemptInterval checks that an oversized dump is
// not kept and that failed attempts still wait for the minimum interval.
func TestDumpOnDiskCapAndFailedAttemptInterval(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			cfg := appconfig.DefaultDumpConfig()
			cfg.Enabled = true
			cfg.Directory = t.TempDir()
			cfg.Compression = compressed
			cfg.MaxFileBytes = 8
			cfg.MaxTotalBytes = 16
			fd := NewFileDumper(cfg)
			now := time.Now()
			fd.now = func() time.Time { return now }
			_, err := fd.DumpToFile(map[string]string{"a": "b"}, "metrics", "GPU")
			require.ErrorIs(t, err, errDumpTooLarge)
			entries, err := os.ReadDir(cfg.Directory)
			require.NoError(t, err)
			require.Empty(t, entries)
			path, err := fd.DumpToFile(make(chan int), "metrics", "GPU")
			require.NoError(t, err)
			require.Empty(t, path)
			// Different pairs have independent admission, and failure still consumes admission.
			_, err = fd.DumpToFile(make(chan int), "deviceinfo", "GPU")
			require.Error(t, err)
			now = now.Add(cfg.MinInterval)
			_, err = fd.DumpToFile(make(chan int), "metrics", "GPU")
			require.Error(t, err)
		})
	}
}

// TestDumpExactPlainBoundary checks that a dump exactly at the file limit is
// kept and one byte over is rejected, and that new directories are private.
func TestDumpExactPlainBoundary(t *testing.T) {
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = filepath.Join(t.TempDir(), "private")
	cfg.Compression = false
	cfg.MaxFileBytes = 3
	cfg.MaxTotalBytes = 3
	cfg.MaxFiles = 1
	path, err := NewFileDumper(cfg).DumpToFile(struct{}{}, "metrics", "GPU")
	require.NoError(t, err)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "{}\n", string(contents))
	info, err := os.Stat(cfg.Directory)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	cfg.MaxFileBytes = 2
	_, err = NewFileDumper(cfg).DumpToFile(struct{}{}, "metrics", "GPU")
	require.ErrorIs(t, err, errDumpTooLarge)
}

// TestDumpConcurrentAdmissionAndCleanup checks that concurrent dumps and
// cleanups admit one dump per interval.
func TestDumpConcurrentAdmissionAndCleanup(t *testing.T) {
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = t.TempDir()
	cfg.MaxFiles = 2
	fd := NewFileDumper(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fd.DumpToFile(map[string]string{"x": "y"}, "metrics", "GPU")
			assert.NoError(t, err)
			assert.NoError(t, fd.CleanupOldFiles())
		}()
	}
	wg.Wait()
	files, err := fd.ownedFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
}

// TestDumpDirectoryFailureIsThrottledAndNamesMount checks that an unwritable
// directory is named in the error and that retries wait for the interval.
func TestDumpDirectoryFailureIsThrottledAndNamesMount(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root process to exercise filesystem permission denial")
	}
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.Mkdir(cfg.Directory, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(cfg.Directory, 0o700)) })
	fd := NewFileDumper(cfg)
	_, err := fd.DumpToFile(struct{}{}, "metrics", "GPU")
	require.ErrorIs(t, err, os.ErrPermission)
	require.ErrorContains(t, err, cfg.Directory)
	require.ErrorContains(t, err, "writable dump mount")
	path, err := fd.DumpToFile(struct{}{}, "metrics", "GPU")
	require.NoError(t, err)
	require.Empty(t, path)
}

// TestLimitedDumpWriterNeverPassesExcessBytes checks that the writer never
// passes bytes past its budget and reports short writes.
func TestLimitedDumpWriterNeverPassesExcessBytes(t *testing.T) {
	var sink closeErrorWriter
	writer := &limitedDumpWriter{WriteCloser: &sink, remaining: 3}
	n, err := writer.Write([]byte("abcdef"))
	require.Equal(t, 3, n)
	require.ErrorIs(t, err, errDumpTooLarge)
	require.Equal(t, "abc", sink.String())
	n, err = writer.Write([]byte("x"))
	require.Zero(t, n)
	require.ErrorIs(t, err, errDumpTooLarge)
	require.NoError(t, writer.Close())
	short := &limitedDumpWriter{WriteCloser: &shortDumpWriter{}, remaining: 4}
	_, err = short.Write([]byte("abc"))
	require.ErrorIs(t, err, io.ErrShortWrite)
}

// shortDumpWriter accepts no bytes, to exercise short-write handling.
type shortDumpWriter struct{}

func (*shortDumpWriter) Write([]byte) (int, error) { return 0, nil }
func (*shortDumpWriter) Close() error              { return nil }

// TestDumpGzipFinalizationBoundary checks that the gzip trailer counts toward
// the file limit.
func TestDumpGzipFinalizationBoundary(t *testing.T) {
	var sink closeErrorWriter
	require.NoError(t, writeDump(&sink, nil, true))
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = t.TempDir()
	cfg.MaxFileBytes = int64(sink.Len())
	cfg.MaxTotalBytes = cfg.MaxFileBytes
	cfg.MaxFiles = 1
	path, err := NewFileDumper(cfg).DumpToFile(nil, "metrics", "GPU")
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, cfg.MaxFileBytes, info.Size())
	cfg.MaxFileBytes--
	_, err = NewFileDumper(cfg).DumpToFile(nil, "metrics", "GPU")
	require.ErrorIs(t, err, errDumpTooLarge)
	require.ErrorContains(t, err, "finalize")
	entries, err := os.ReadDir(cfg.Directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestDumpStorageFailurePreventsCleanupAndNewWrite checks that a read-only
// directory fails cleanup and writes without removing files.
func TestDumpStorageFailurePreventsCleanupAndNewWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root process to exercise filesystem permission denial")
	}
	dir := t.TempDir()
	old := filepath.Join(dir, "metrics-GPU-20200101-000000-deadbeef.json")
	require.NoError(t, os.WriteFile(old, []byte("{}"), 0o600))
	past := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(old, past, past))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = dir
	fd := NewFileDumper(cfg)
	err := fd.CleanupOldFiles()
	require.ErrorIs(t, err, os.ErrPermission)
	require.ErrorContains(t, err, "writable dump mount")
	_, err = fd.DumpToFile(struct{}{}, "metrics", "GPU")
	require.ErrorIs(t, err, os.ErrPermission)
	require.FileExists(t, old)
}

// Run with a root-owned, group-writable mount and a non-root process to prove
// the same filesystem access contract used by Kubernetes fsGroup volumes.
func TestDumpGroupWritableMount(t *testing.T) {
	directory := os.Getenv("DCGM_EXPORTER_TEST_GROUP_WRITABLE_DUMP_DIRECTORY")
	if directory == "" {
		t.Skip("requires a root-owned, group-writable mount and non-root process")
	}
	require.NotZero(t, os.Geteuid())
	before, err := os.Lstat(directory) // #nosec G703 -- The test runner supplies the mount whose metadata this fixture verifies.
	require.NoError(t, err)
	require.True(t, before.IsDir())
	owner, ok := before.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.Zero(t, owner.Uid)
	groups, err := os.Getgroups()
	require.NoError(t, err)
	require.Contains(t, groups, int(owner.Gid))
	require.Equal(t, os.FileMode(0o770), before.Mode().Perm())
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = directory
	cfg.Compression = false
	fd := NewFileDumper(cfg)
	path, err := fd.DumpToFile(map[string]string{"mount": "group-writable"}, "metrics", "GPU")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{"mount":"group-writable"}`, string(data))
	file, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), file.Mode().Perm())
	fd.now = func() time.Time { return time.Now().Add(cfg.Retention + time.Hour) }
	require.NoError(t, fd.CleanupOldFiles())
	require.NoFileExists(t, path)
	after, err := os.Lstat(directory) // #nosec G703 -- Verify that the same test-runner mount remains unchanged.
	require.NoError(t, err)
	afterOwner, ok := after.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.Equal(t, owner.Uid, afterOwner.Uid)
	require.Equal(t, owner.Gid, afterOwner.Gid)
	require.Equal(t, before.Mode(), after.Mode())
}

// TestDumpCleanupRejectsDirectorySymlink checks that a dump directory that is a
// symlink, with or without trailing slashes, is neither cleaned nor written.
func TestDumpCleanupRejectsDirectorySymlink(t *testing.T) {
	external := t.TempDir()
	old := filepath.Join(external, "metrics-GPU-20200101-000000-deadbeef.json")
	require.NoError(t, os.WriteFile(old, []byte("external"), 0o600))
	past := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(old, past, past))
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = filepath.Join(t.TempDir(), "dump-link")
	require.NoError(t, os.Symlink(external, cfg.Directory))
	link := cfg.Directory
	for _, suffix := range []string{"", "/", "/.", "//././", "/.//.///"} {
		t.Run(suffix, func(t *testing.T) {
			cfg.Directory = link + suffix
			fd := NewFileDumper(cfg)
			require.Equal(t, link, fd.config.Directory)
			require.ErrorContains(t, fd.CleanupOldFiles(), "writable dump mount")
			_, err := fd.DumpToFile(struct{}{}, "metrics", "GPU")
			require.ErrorContains(t, err, "writable dump mount")
		})
	}
	data, err := os.ReadFile(old)
	require.NoError(t, err)
	require.Equal(t, "external", string(data))
	entries, err := os.ReadDir(external)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	missing := filepath.Join(t.TempDir(), "missing")
	cfg.Directory = missing
	require.NoError(t, NewFileDumper(cfg).CleanupOldFiles())
	require.NoDirExists(t, missing)
}

// TestDumpPreservesInteriorParentSymlinkResolution checks that symlinks before
// a ".." in the directory resolve as the filesystem resolves them.
func TestDumpPreservesInteriorParentSymlinkResolution(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "target")
	require.NoError(t, os.MkdirAll(filepath.Join(physical, "child"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(physical, "dumps"), 0o700))
	decoy := filepath.Join(root, "dumps")
	require.NoError(t, os.Mkdir(decoy, 0o700))
	link := filepath.Join(root, "parent-link")
	require.NoError(t, os.Symlink(filepath.Join(physical, "child"), link))
	old := filepath.Join(physical, "dumps", "metrics-GPU-20200101-000000-deadbeef.json")
	decoyOld := filepath.Join(decoy, filepath.Base(old))
	for _, path := range []string{old, decoyOld} {
		require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
		past := time.Now().Add(-48 * time.Hour)
		require.NoError(t, os.Chtimes(path, past, past))
	}
	cfg := appconfig.DefaultDumpConfig()
	cfg.Enabled = true
	cfg.Directory = link + "/../dumps/./"
	fd := NewFileDumper(cfg)
	require.Equal(t, link+"/../dumps", fd.config.Directory)
	require.Equal(t, link+"/../dumps/./", cfg.Directory)
	require.NoError(t, fd.CleanupOldFiles())
	require.NoFileExists(t, old)
	require.FileExists(t, decoyOld)
	path, err := fd.DumpToFile(struct{}{}, "metrics", "GPU")
	require.NoError(t, err)
	require.FileExists(t, path)
	entries, err := os.ReadDir(filepath.Join(physical, "dumps"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	decoyEntries, err := os.ReadDir(decoy)
	require.NoError(t, err)
	require.Len(t, decoyEntries, 1)
	require.Equal(t, filepath.Base(decoyOld), decoyEntries[0].Name())
}

// TestDumpDirectorySuffixBoundaries checks which trailing slashes and dots the
// dumper trims from the directory.
func TestDumpDirectorySuffixBoundaries(t *testing.T) {
	for directory, want := range map[string]string{
		"": "", "/": "/", "///./.": "/", ".": ".", "././": ".",
		"parent/../dumps/./": "parent/../dumps", "parent//../dumps": "parent//../dumps",
	} {
		fd := NewFileDumper(appconfig.DumpConfig{Directory: directory})
		require.Equal(t, want, fd.config.Directory)
	}
}

// TestDumpFullStorageReturnsError checks that a full device returns an error
// and closes the file.
func TestDumpFullStorageReturnsError(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			file, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
			if os.IsNotExist(err) {
				t.Skip("requires /dev/full, which Linux provides")
			}
			require.NoError(t, err)
			err = writeDump(&limitedDumpWriter{WriteCloser: file, remaining: 1024}, struct{}{}, compressed)
			require.ErrorIs(t, err, syscall.ENOSPC)
			_, err = file.Write([]byte("x"))
			require.ErrorIs(t, err, os.ErrClosed)
		})
	}
}

// TestDumpBoundedSlowWriter checks that a slow filesystem write still ends at
// the byte budget.
func TestDumpBoundedSlowWriter(t *testing.T) {
	sink := &slowDumpWriter{entered: make(chan struct{}), release: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		result <- writeDump(&limitedDumpWriter{WriteCloser: sink, remaining: 2}, struct{}{}, false)
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dump did not reach the filesystem sink")
	}
	// Storage quotas cannot preempt a blocked filesystem call; let this finite write finish.
	close(sink.release)
	select {
	case err := <-result:
		require.ErrorIs(t, err, errDumpTooLarge)
	case <-time.After(5 * time.Second):
		t.Fatal("released filesystem sink did not finish")
	}
	require.Equal(t, "{}", sink.String())
	require.True(t, sink.closed)
}

// slowDumpWriter blocks its first write until released, to model slow storage.
type slowDumpWriter struct {
	bytes.Buffer
	entered chan struct{}
	release chan struct{}
	closed  bool
}

// Write signals entry, waits for release, then buffers the data.
func (w *slowDumpWriter) Write(data []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.Buffer.Write(data)
}

// Close records that the writer was closed.
func (w *slowDumpWriter) Close() error {
	w.closed = true
	return nil
}
