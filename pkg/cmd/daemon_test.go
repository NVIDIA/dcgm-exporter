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

package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The script prints the daemon marker, the session ID and PID from /proc/<pid>/stat, and the target of stdin.
const daemonTestScript = `echo "marker=$` + daemonChildEnv + `"; read -r stat < /proc/$$/stat; echo "stat=$stat"; echo "stdin=$(readlink /proc/$$/fd/0)"`

func waitForLines(t *testing.T, path string, lines int) []string {
	t.Helper()

	var got []string
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		got = strings.Split(strings.TrimSpace(string(data)), "\n")
		return len(got) >= lines
	}, 10*time.Second, 10*time.Millisecond)

	return got
}

func TestStartDetached_RunsInNewSessionWithLogFile(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "daemon.log")
	require.NoError(t, os.WriteFile(logFile, []byte("previous run\n"), 0o600))

	pid, err := startDetached("/bin/sh", []string{"-c", daemonTestScript}, logFile)
	require.NoError(t, err)

	lines := waitForLines(t, logFile, 4)
	assert.Equal(t, "previous run", lines[0], "the log file must be appended to, not truncated")
	assert.Equal(t, "marker=1", lines[1])

	// /proc/<pid>/stat: pid (comm) state ppid pgrp session ...
	fields := strings.Fields(strings.TrimPrefix(lines[2], "stat="))
	require.GreaterOrEqual(t, len(fields), 6)
	assert.Equal(t, strconv.Itoa(pid), fields[0])
	assert.Equal(t, fields[0], fields[5], "the process must lead its own session")

	assert.Equal(t, "stdin="+os.DevNull, lines[3])
}

func TestStartDetached_ReturnsErrorForMissingExecutable(t *testing.T) {
	_, err := startDetached(filepath.Join(t.TempDir(), "missing"), nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start dcgm-exporter in the background")
}

func TestStartDetached_ReturnsErrorForUnwritableLogFile(t *testing.T) {
	_, err := startDetached("/bin/sh", []string{"-c", "true"}, filepath.Join(t.TempDir(), "missing", "daemon.log"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open daemon log file")
}
