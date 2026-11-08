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

// Package childcover lets a test that starts copies of its own binary count
// the code those child processes run in the parent's coverage profile.
package childcover

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"testing"
)

var (
	dirOnce sync.Once
	dir     string
	dirErr  error
)

// Dir returns the folder the running test binary reads coverage data from
// when it writes its profile at exit. A child process that saves its data
// there is counted in the parent's profile. go test passes this folder with
// -test.gocoverdir; when a binary has coverage but no folder, as under bazel
// coverage, Dir creates one and sets the flag. It returns "" when the binary
// was built without coverage, and must be called after flags are parsed.
func Dir() (string, error) {
	dirOnce.Do(func() {
		if testing.CoverMode() == "" {
			return
		}

		coverDir := flag.Lookup("test.gocoverdir")
		if coverDir == nil {
			dirErr = fmt.Errorf("test binary has no -test.gocoverdir flag")
			return
		}
		if dir = coverDir.Value.String(); dir != "" {
			return
		}

		// The test framework merges this folder into the profile after the
		// tests finish, so it must outlive every test's temporary directory.
		if dir, dirErr = os.MkdirTemp("", "gocoverdir"); dirErr != nil {
			dirErr = fmt.Errorf("create coverage folder: %w", dirErr)
			return
		}
		if err := coverDir.Value.Set(dir); err != nil {
			dir, dirErr = "", fmt.Errorf("set -test.gocoverdir: %w", err)
		}
	})

	return dir, dirErr
}

// Flush saves this process's coverage data into dir by running the test
// framework with no tests selected. A child process calls it from TestMain
// when it ends without returning from testing.M.Run, such as one that runs a
// program's main. The framework's own stdout output is discarded so it does
// not mix with the program's. It does nothing when dir is empty.
func Flush(m *testing.M, dir string) error {
	if dir == "" {
		return nil
	}

	for name, value := range map[string]string{"test.run": "^$", "test.gocoverdir": dir} {
		if err := flag.Set(name, value); err != nil {
			return fmt.Errorf("set -%s: %w", name, err)
		}
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	stdout := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = stdout }()

	if code := m.Run(); code != 0 {
		return fmt.Errorf("save coverage data: test framework exited with %d", code)
	}

	return nil
}
