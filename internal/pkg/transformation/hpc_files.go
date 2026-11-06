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

package transformation

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	sysOS "os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// errMappingIsDirectory marks a directory named like a mapping file. It is
// not a mapping, so the mapper skips it without a warning.
var errMappingIsDirectory = errors.New("mapping is a directory")

// openMappingDirectory pins the operator-owned directory for one mapping pass.
func openMappingDirectory(path string) (*sysOS.File, error) {
	if path == "" {
		return nil, errors.New("mapping directory is empty")
	}

	// Remove only final slash/dot forms; interior components retain kernel resolution.
	for {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}

		if !strings.HasSuffix(path, "/.") {
			break
		}

		path = strings.TrimSuffix(path, "/.")
		if path == "" {
			path = "/"
		}
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open HPC mapping directory %q: %w", path, err)
	}

	return sysOS.NewFile(uintptr(fd), path), nil
}

// readMappingAt rejects links and special files before consuming job input.
func readMappingAt(directory *sysOS.File, name string, config *appconfig.Config) ([]string, error) {
	if !validGPUFile.MatchString(name) {
		return nil, errors.New("invalid mapping filename")
	}

	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}

	file := sysOS.NewFile(uintptr(fd), name)
	defer file.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}

	if err := validateMappingFile(stat, config); err != nil {
		return nil, err
	}

	return readMappingJobs(file, config)
}

// validateMappingFile accepts only a regular file with one link that is
// within the file byte limit.
func validateMappingFile(stat unix.Stat_t, config *appconfig.Config) error {
	if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
		return errMappingIsDirectory
	}

	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return errors.New("mapping must be a regular file with one link")
	}

	if stat.Size > config.HPCJobMappingMaxFileBytes {
		return errors.New("mapping exceeds file byte limit")
	}

	return nil
}

// readMappingJobs reads job IDs in order. It trims each line and omits blank,
// zero and repeated lines. It reads one byte past the file limit, so a file
// that grew after Fstat is still refused.
func readMappingJobs(reader io.Reader, config *appconfig.Config) ([]string, error) {
	if err := appconfig.ValidateHPCJobMappingLimits(config); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(reader, config.HPCJobMappingMaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read mapping: %w", err)
	}

	if int64(len(data)) > config.HPCJobMappingMaxFileBytes {
		return nil, errors.New("mapping exceeds file byte limit")
	}

	var jobs []string
	seen := make(map[string]struct{})
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		job := string(bytes.TrimSpace(line))

		// Slurm epilog scripts write "0" when a job ends; Slurm job IDs start at 1.
		if job == "" || job == "0" {
			continue
		}

		if int64(len(job)) > config.HPCJobMappingMaxJobIDBytes {
			return nil, errors.New("mapping exceeds job ID byte limit")
		}

		if _, duplicate := seen[job]; duplicate {
			continue
		}

		if int64(len(jobs)) >= config.HPCJobMappingMaxJobsPerFile {
			return nil, errors.New("mapping exceeds unique job limit")
		}

		seen[job] = struct{}{}
		jobs = append(jobs, job)
	}

	return jobs, nil
}
