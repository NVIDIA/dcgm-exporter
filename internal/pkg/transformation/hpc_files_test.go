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
package transformation

import (
	"bytes"
	"fmt"
	"io"
	"net"
	stdos "os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestHPCMappingFileTypes checks only a single-link regular file is read as a mapping.
func TestHPCMappingFileTypes(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "hardlink", "socket", "directory", "device"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "0")
			outside := filepath.Join(t.TempDir(), "marker")
			require.NoError(t, stdos.WriteFile(outside, []byte("outside-marker"), 0o600))

			switch kind {
			case "regular":
				require.NoError(t, stdos.WriteFile(name, []byte("job"), 0o600))
			case "symlink":
				require.NoError(t, stdos.Symlink(outside, name))
			case "hardlink":
				require.NoError(t, stdos.Link(outside, name))
			case "socket":
				listener, err := net.Listen("unix", name)
				require.NoError(t, err)
				defer listener.Close()
			case "directory":
				require.NoError(t, stdos.Mkdir(name, 0o700))
			case "device":
				// Linux /dev/null has major 1 and minor 3.
				const nullDevice = 1<<8 | 3
				if err := unix.Mknod(name, unix.S_IFCHR|0o600, nullDevice); err != nil {
					t.Skipf("device creation unavailable: %v", err)
				}
			}

			directory, err := openMappingDirectory(dir)
			require.NoError(t, err)
			defer directory.Close()

			jobs, err := readMappingAt(directory, "0", hpcTestConfig(dir))
			if kind == "regular" {
				require.NoError(t, err)
				require.Equal(t, []string{"job"}, jobs)
			} else {
				require.Error(t, err)
				require.Nil(t, jobs)
			}
		})
	}
}

// TestHPCDirectoryPinnedAndSymlinkRejected checks reads stay in the directory that was opened, a symlinked directory is refused, and names that are not GPU IDs are refused.
func TestHPCDirectoryPinnedAndSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "jobs")
	require.NoError(t, stdos.Mkdir(dir, 0o700))
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte("pinned"), 0o600))

	directory, err := openMappingDirectory(dir)
	require.NoError(t, err)
	defer directory.Close()

	require.NoError(t, stdos.Rename(dir, filepath.Join(root, "old")))
	require.NoError(t, stdos.Mkdir(dir, 0o700))
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte("replacement"), 0o600))

	jobs, err := readMappingAt(directory, "0", hpcTestConfig(dir))
	require.NoError(t, err)
	require.Equal(t, []string{"pinned"}, jobs)

	link := filepath.Join(root, "link")
	require.NoError(t, stdos.Symlink(dir, link))
	for _, path := range []string{link, link + "/", link + "/."} {
		_, err = openMappingDirectory(path)
		require.Error(t, err)
	}

	for _, name := range []string{"../0", "0/1", "-1", "0.1.2"} {
		_, err = readMappingAt(directory, name, hpcTestConfig(dir))
		require.Error(t, err)
	}
}

// TestHPCReadBoundaries checks each limit at and just past its boundary, and that a reader takes at most one byte past the file limit.
func TestHPCReadBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input       string
		bytes, line, jobs int64
		want              []string
		invalid           bool
	}{
		{"empty", "", 1, 1, 1, nil, false},
		{"blank and zero", "\n 0 \n  7 \n", 10, 1, 1, []string{"7"}, false},
		{"one", "x", 1, 1, 1, []string{"x"}, false},
		{"exact bytes", "ab\nc", 4, 2, 2, []string{"ab", "c"}, false},
		{"excess bytes", "ab\ncx", 4, 3, 2, nil, true},
		{"excess line", "abc", 4, 2, 2, nil, true},
		{"dedup order", "b\na\nb\n", 6, 1, 2, []string{"b", "a"}, false},
		{"excess jobs", "a\nb\nc", 5, 1, 2, nil, true},
		{"CRLF", "a\r\nb\r\n", 6, 1, 2, []string{"a", "b"}, false},
		{"blank lines", "\na\n", 3, 1, 2, []string{"a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := hpcTestConfig("")
			config.HPCJobMappingMaxFileBytes = tc.bytes
			config.HPCJobMappingMaxJobIDBytes = tc.line
			config.HPCJobMappingMaxJobsPerFile = tc.jobs

			reader := bytes.NewReader([]byte(tc.input))
			jobs, err := readMappingJobs(reader, config)
			if tc.invalid {
				require.Error(t, err)
				require.Nil(t, jobs)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, jobs)
			}

			require.LessOrEqual(t, int64(len(tc.input)-reader.Len()), tc.bytes+1)
		})
	}

	// This simulates growth after an accepted size check; only cap+1 is consumed.
	config := hpcTestConfig("")
	config.HPCJobMappingMaxFileBytes = 2

	_, err := readMappingJobs(io.MultiReader(strings.NewReader("ab"), strings.NewReader("growing")), config)
	require.Error(t, err)
}

// swapMappingThroughSymlink atomically replaces name with a symlink to
// outside, then with a regular file again, as a scheduler's rename would.
func swapMappingThroughSymlink(dir, name, outside string) error {
	temp := filepath.Join(dir, "next")
	if err := stdos.Symlink(outside, temp); err != nil {
		return err
	}

	if err := stdos.Rename(temp, name); err != nil {
		return err
	}

	if err := stdos.WriteFile(temp, []byte("job"), 0o600); err != nil {
		return err
	}

	return stdos.Rename(temp, name)
}

// TestHPCAtomicReplacementRace checks a file swapped between a symlink and a regular file is never read through the symlink.
func TestHPCAtomicReplacementRace(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, stdos.WriteFile(outside, []byte("outside-marker"), 0o600))
	name := filepath.Join(dir, "0")
	require.NoError(t, stdos.WriteFile(name, []byte("job"), 0o600))

	directory, err := openMappingDirectory(dir)
	require.NoError(t, err)
	defer directory.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()

	writeError := make(chan error, 1)
	go func() {
		defer wg.Done()
		for range 300 {
			if err := swapMappingThroughSymlink(dir, name, outside); err != nil {
				writeError <- err
				return
			}
		}

		writeError <- nil
	}()

	for range 300 {
		jobs, err := readMappingAt(directory, "0", hpcTestConfig(dir))
		if err == nil {
			require.Equal(t, []string{"job"}, jobs)
		} else {
			require.Nil(t, jobs)
		}
	}

	wg.Wait()
	require.NoError(t, <-writeError)
}

// TestHPCFIFOOpenDoesNotWaitForWriter checks a FIFO named like a mapping is refused without blocking the scrape.
func TestHPCFIFOOpenDoesNotWaitForWriter(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "0")
	require.NoError(t, unix.Mkfifo(name, 0o600))

	directory, err := openMappingDirectory(dir)
	require.NoError(t, err)
	defer directory.Close()

	done := make(chan error, 1)
	go func() { _, err := readMappingAt(directory, "0", hpcTestConfig(dir)); done <- err }()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		// Release a regressed blocking open so this test cannot leave work behind.
		fd, err := unix.Open(name, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if err == nil {
			_ = unix.Close(fd)
		}

		select {
		case blockedErr := <-done:
			t.Logf("blocked read ended after release: %v", blockedErr)
		case <-time.After(time.Second):
		}

		t.Fatal("FIFO mapping waited for a writer")
	}
}

// TestHPCGrowthAndReadError checks a file that grows past the limit after it was opened, and a failed read, add no jobs.
func TestHPCGrowthAndReadError(t *testing.T) {
	name := filepath.Join(t.TempDir(), "0")
	require.NoError(t, stdos.WriteFile(name, []byte("a"), 0o600))

	file, err := stdos.Open(name)
	require.NoError(t, err)
	defer file.Close()

	info, err := file.Stat()
	require.NoError(t, err)
	require.EqualValues(t, 1, info.Size())

	writer, err := stdos.OpenFile(name, stdos.O_APPEND|stdos.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = writer.WriteString("bc")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	config := hpcTestConfig("")
	config.HPCJobMappingMaxFileBytes = 2

	jobs, err := readMappingJobs(file, config)
	require.Error(t, err)
	require.Nil(t, jobs)

	require.NoError(t, file.Close())
	jobs, err = readMappingJobs(file, config)
	require.Error(t, err)
	require.Nil(t, jobs)
}

// TestHPCDeviceDescriptorRejected checks a character device is refused as a mapping.
func TestHPCDeviceDescriptorRejected(t *testing.T) {
	fd, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(fd)

	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &stat))
	require.ErrorContains(t, validateMappingFile(stat, hpcTestConfig("")), "regular file")
}

// TestHPCDefaultBoundaries checks the default file, job ID and job count limits.
func TestHPCDefaultBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		jobs        int
		invalid     bool
	}{
		{"file exact", strings.Repeat("x\n", 32768), 1, false},
		{"file excess", strings.Repeat("x\n", 32768) + "x", 0, true},
		{"job ID exact", strings.Repeat("x", 256), 1, false},
		{"job ID excess", strings.Repeat("x", 257), 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte(tc.input), 0o600))

			directory, err := openMappingDirectory(dir)
			require.NoError(t, err)
			defer directory.Close()
			jobs, err := readMappingAt(directory, "0", hpcTestConfig(dir))
			if tc.invalid {
				require.Error(t, err)
				require.Nil(t, jobs)
			} else {
				require.NoError(t, err)
				require.Len(t, jobs, tc.jobs)
			}
		})
	}

	var lines []string
	for i := range 65 {
		lines = append(lines, fmt.Sprintf("job-%d", i))
	}

	jobs, err := readMappingJobs(strings.NewReader(strings.Join(lines[:64], "\n")), hpcTestConfig(""))
	require.NoError(t, err)
	require.Equal(t, lines[:64], jobs)

	jobs, err = readMappingJobs(strings.NewReader(strings.Join(lines, "\n")), hpcTestConfig(""))
	require.Error(t, err)
	require.Nil(t, jobs)
}

// TestHPCDirectoryPreservesParentSymlinkResolution checks symlinks before the final directory component still resolve as the kernel resolves them.
func TestHPCDirectoryPreservesParentSymlinkResolution(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	require.NoError(t, stdos.MkdirAll(filepath.Join(outside, "child"), 0o700))
	actual := filepath.Join(outside, "jobs")
	lexical := filepath.Join(root, "jobs")

	for dir, job := range map[string]string{actual: "resolved", lexical: "lexical"} {
		require.NoError(t, stdos.Mkdir(dir, 0o700))
		require.NoError(t, stdos.WriteFile(filepath.Join(dir, "0"), []byte(job), 0o600))
	}

	link := filepath.Join(root, "link")
	require.NoError(t, stdos.Symlink(filepath.Join(outside, "child"), link))

	// filepath.Join would clean away the very '..' component this fixture proves.
	path := link + "/../jobs/./"
	directory, err := openMappingDirectory(path)
	require.NoError(t, err)
	defer directory.Close()

	jobs, err := readMappingAt(directory, "0", hpcTestConfig(path))
	require.NoError(t, err)
	require.Equal(t, []string{"resolved"}, jobs)
}
