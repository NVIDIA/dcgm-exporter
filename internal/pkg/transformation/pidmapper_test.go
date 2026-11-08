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

package transformation

import (
	"fmt"
	stdos "os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testPodUID    = "a9c80282-3f6b-4d5b-84d5-a137a6668011"
	testPodCgroup = "0::/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-poda9c80282_3f6b_4d5b_84d5_a137a6668011.slice/cri-containerd-abc.scope\n"
)

// writeProcStat writes a /proc/<pid>/stat file whose command name contains
// spaces and parentheses, with the given start time in field 22.
func writeProcStat(t *testing.T, procRoot string, pid uint32, startTime uint64) {
	t.Helper()
	stat := fmt.Sprintf("%d (my (odd) cmd) S 1 1 1 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 %d 1000 10\n", pid, startTime)
	writeProcFile(t, procRoot, pid, "stat", stat)
}

// writeProcFile writes one file under a temporary /proc/<pid> directory.
func writeProcFile(t *testing.T, procRoot string, pid uint32, name, content string) {
	t.Helper()
	dir := filepath.Join(procRoot, strconv.FormatUint(uint64(pid), 10))
	require.NoError(t, stdos.MkdirAll(dir, 0o755))
	require.NoError(t, stdos.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

// newTestPIDToPodMapper returns a mapper that reads process files from procRoot.
func newTestPIDToPodMapper(procRoot string) *pidToPodMapper {
	mapper := newPIDToPodMapper()
	mapper.procRoot = procRoot
	return mapper
}

// TestExtractPodUID checks cgroup layouts, UID normalization, and absent or short identities.
func TestExtractPodUID(t *testing.T) {
	t.Parallel()
	// Path cases cover supported cgroup layouts and invalid UID candidates.
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "cgroups v1 besteffort",
			path:     "/kubepods/besteffort/poda9c80282-3f6b-4d5b-84d5-a137a6668011/container123",
			expected: "a9c80282-3f6b-4d5b-84d5-a137a6668011",
		},
		{
			name:     "cgroups v1 burstable",
			path:     "/kubepods/burstable/pod12345678-1234-1234-1234-123456789012/abc",
			expected: "12345678-1234-1234-1234-123456789012",
		},
		{
			name:     "cgroups v2 with underscores",
			path:     "/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-poda9c80282_3f6b_4d5b_84d5_a137a6668011.slice",
			expected: "a9c80282-3f6b-4d5b-84d5-a137a6668011",
		},
		{
			name:     "no pod UID",
			path:     "/system.slice/docker.service",
			expected: "",
		},
		{
			name:     "short UID (invalid)",
			path:     "/kubepods/pod123/container",
			expected: "",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := extractPodUID(tc.path)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestExtractPodUIDFromPaths checks controller paths and unified-cgroup fallback.
func TestExtractPodUIDFromPaths(t *testing.T) {
	t.Parallel()
	// Controller and unified paths exercise the same UID resolver.
	tests := []struct {
		name       string
		subsystems map[string]string
		unified    string
		expected   string
	}{
		{
			name: "found in subsystems",
			subsystems: map[string]string{
				"memory": "/kubepods/besteffort/poda9c80282-3f6b-4d5b-84d5-a137a6668011/container",
				"cpu":    "/kubepods/besteffort/poda9c80282-3f6b-4d5b-84d5-a137a6668011/container",
			},
			unified:  "",
			expected: "a9c80282-3f6b-4d5b-84d5-a137a6668011",
		},
		{
			name:       "found in unified only",
			subsystems: map[string]string{},
			unified:    "/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod12345678_1234_1234_1234_123456789012.slice",
			expected:   "12345678-1234-1234-1234-123456789012",
		},
		{
			name: "not found",
			subsystems: map[string]string{
				"memory": "/system.slice/docker.service",
			},
			unified:  "/user.slice",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := extractPodUIDFromPaths(tc.subsystems, tc.unified)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestBuildPIDToPodMap joins cached process UIDs to known pods and omits unmatched processes.
func TestBuildPIDToPodMap(t *testing.T) {
	t.Parallel()
	procRoot := t.TempDir()
	mapper := newTestPIDToPodMapper(procRoot)

	pods := []PodInfo{
		{Name: "pod1", Namespace: "default", UID: "uid-1"},
		{Name: "pod2", Namespace: "default", UID: "uid-2"},
		{Name: "pod3", Namespace: "kube-system", UID: ""},
	}

	for _, pid := range []uint32{1001, 1002, 1003} {
		writeProcStat(t, procRoot, pid, 10)
	}
	mapper.pidToUID[1001] = cachedPodUID{uid: "uid-1", startTime: 10}
	mapper.pidToUID[1002] = cachedPodUID{uid: "uid-2", startTime: 10}
	mapper.pidToUID[1003] = cachedPodUID{uid: "uid-unknown", startTime: 10}

	result := mapper.buildPIDToPodMap([]uint32{1001, 1002, 1003, 1004}, pods)

	assert.Len(t, result, 2)
	assert.Equal(t, "pod1", result[1001].Name)
	assert.Equal(t, "pod2", result[1002].Name)
	assert.Nil(t, result[1003])
	assert.Nil(t, result[1004])
}

// TestBuildPIDToPodMapIgnoresStaleCache re-reads the cgroup when a cached PID
// now has a different start time, so the reused PID maps to its current pod.
func TestBuildPIDToPodMapIgnoresStaleCache(t *testing.T) {
	t.Parallel()
	procRoot := t.TempDir()
	mapper := newTestPIDToPodMapper(procRoot)
	pods := []PodInfo{
		{Name: "old", Namespace: "default", UID: "uid-old"},
		{Name: "new", Namespace: "default", UID: testPodUID},
	}

	writeProcStat(t, procRoot, 1001, 11)
	writeProcFile(t, procRoot, 1001, "cgroup", testPodCgroup)
	mapper.pidToUID[1001] = cachedPodUID{uid: "uid-old", startTime: 10}

	result := mapper.buildPIDToPodMap([]uint32{1001}, pods)

	require.Len(t, result, 1)
	assert.Equal(t, "new", result[1001].Name)
}

// TestBuildPIDToPodMapFromProc maps a process with a stable start time and
// skips processes whose stat file is missing or unreadable.
func TestBuildPIDToPodMapFromProc(t *testing.T) {
	t.Parallel()
	procRoot := t.TempDir()
	pods := []PodInfo{{Name: "pod1", Namespace: "default", UID: testPodUID}}

	writeProcStat(t, procRoot, 1001, 4242)
	writeProcFile(t, procRoot, 1001, "cgroup", testPodCgroup)

	writeProcFile(t, procRoot, 1002, "cgroup", testPodCgroup)

	writeProcFile(t, procRoot, 1003, "stat", "1003 (cmd) S 1 1\n")
	writeProcFile(t, procRoot, 1003, "cgroup", testPodCgroup)

	result := newTestPIDToPodMapper(procRoot).buildPIDToPodMap([]uint32{1001, 1002, 1003}, pods)

	require.Len(t, result, 1)
	assert.Equal(t, "pod1", result[1001].Name)
}

// TestBuildPIDToPodMapFromProcRequiresAssignedPod skips a process whose pod is
// not assigned to the GPU being mapped.
func TestBuildPIDToPodMapFromProcRequiresAssignedPod(t *testing.T) {
	t.Parallel()
	procRoot := t.TempDir()
	pods := []PodInfo{{Name: "other", Namespace: "default", UID: "12345678-1234-1234-1234-123456789012"}}

	writeProcStat(t, procRoot, 1001, 4242)
	writeProcFile(t, procRoot, 1001, "cgroup", testPodCgroup)

	result := newTestPIDToPodMapper(procRoot).buildPIDToPodMap([]uint32{1001}, pods)

	assert.Empty(t, result)
}

// TestCheckStartTime accepts an unchanged start time and rejects a changed or
// missing one.
func TestCheckStartTime(t *testing.T) {
	t.Parallel()
	procRoot := t.TempDir()
	writeProcStat(t, procRoot, 1001, 4242)
	mapper := newTestPIDToPodMapper(procRoot)

	require.NoError(t, mapper.checkStartTime(1001, 4242))
	require.ErrorContains(t, mapper.checkStartTime(1001, 4241), "was reused")
	require.ErrorContains(t, mapper.checkStartTime(1002, 4242), "failed to read stat file")
}

// TestReadStartTime parses field 22 after the last closing parenthesis.
func TestReadStartTime(t *testing.T) {
	t.Parallel()
	procRoot := t.TempDir()
	writeProcStat(t, procRoot, 1001, 987654321)
	writeProcFile(t, procRoot, 1002, "stat", "1002 no command name\n")
	writeProcFile(t, procRoot, 1003, "stat", "1003 (cmd) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 notanumber\n")
	mapper := newTestPIDToPodMapper(procRoot)

	startTime, err := mapper.readStartTime(1001)
	require.NoError(t, err)
	assert.Equal(t, uint64(987654321), startTime)

	_, err = mapper.readStartTime(1002)
	require.ErrorContains(t, err, "no command name")

	_, err = mapper.readStartTime(1003)
	require.ErrorContains(t, err, "failed to parse start time")
}

// TestReadStartTimeFromHostProc reads the test process's own start time from
// the host /proc, so the field position is checked against the kernel format.
func TestReadStartTimeFromHostProc(t *testing.T) {
	t.Parallel()
	if _, err := stdos.Stat(filepath.Join(defaultProcRoot, "self", "stat")); err != nil {
		t.Skip("host /proc is not available")
	}
	mapper := newPIDToPodMapper()
	pid := uint32(stdos.Getpid()) //nolint:gosec // G115: Linux PIDs fit in uint32

	startTime, err := mapper.readStartTime(pid)
	require.NoError(t, err)
	assert.NotZero(t, startTime)
	require.NoError(t, mapper.checkStartTime(pid, startTime))
}
