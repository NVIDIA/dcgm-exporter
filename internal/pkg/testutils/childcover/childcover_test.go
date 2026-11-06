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

package childcover

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// childDirEnv names the coverage folder when this test binary runs as the
// child process of TestFlushSavesChildCoverage.
const childDirEnv = "CHILDCOVER_TEST_CHILD_DIR"

// childOutput is what the child process prints before it saves its coverage.
const childOutput = "child output\n"

// TestMain runs the tests, or, when childDirEnv is set, acts as a child
// process that prints childOutput and saves its coverage with Flush.
func TestMain(m *testing.M) {
	dir, isChild := os.LookupEnv(childDirEnv)
	if !isChild {
		os.Exit(m.Run())
	}

	fmt.Print(childOutput)
	if err := Flush(m, dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestDir checks that a coverage build always gets a folder and that the test
// framework is told to read it.
func TestDir(t *testing.T) {
	got, err := Dir()
	require.NoError(t, err)

	if testing.CoverMode() == "" {
		assert.Empty(t, got)
		return
	}

	require.NotEmpty(t, got)
	assert.DirExists(t, got)
	assert.Equal(t, got, flag.Lookup("test.gocoverdir").Value.String())
}

// TestFlushWithoutFolder checks that a binary without a coverage folder does
// not run the test framework a second time.
func TestFlushWithoutFolder(t *testing.T) {
	require.NoError(t, Flush(nil, ""))
}

// TestFlushSavesChildCoverage checks that a child process saves coverage data
// into the folder it is given and that its own output is not mixed with the
// test framework's.
func TestFlushSavesChildCoverage(t *testing.T) {
	if testing.CoverMode() == "" {
		t.Skip("the binary was built without coverage")
	}
	dir := t.TempDir()
	testBinary, err := os.Executable()
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), testBinary)
	cmd.Env = append(os.Environ(), childDirEnv+"="+dir)
	out, err := cmd.Output()
	require.NoError(t, err)

	assert.Equal(t, childOutput, string(out))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "child saved no coverage data")
}
