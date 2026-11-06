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
	"log/slog"
	stdos "os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/containerd/cgroups/v3"
)

var podUIDRegex = regexp.MustCompile(`pod([a-f0-9_-]+)`)

// defaultProcRoot is where the kernel exposes per-process files.
const defaultProcRoot = "/proc"

// statStartTimeIndex is the position of the process start time among the
// /proc/<pid>/stat fields that follow the command name. The start time is
// field 22 of the file; the fields after the command name begin at field 3.
const statStartTimeIndex = 22 - 3

// cachedPodUID is a resolved pod UID and the start time of the process it was
// read for.
type cachedPodUID struct {
	uid       string
	startTime uint64
}

// pidToPodMapper caches resolved process cgroup UIDs for one collection pass.
type pidToPodMapper struct {
	procRoot       string
	pidToUID       map[uint32]cachedPodUID
	cgroupWarnOnce sync.Once
}

// newPIDToPodMapper creates a fresh cgroup-to-pod cache for a collection pass.
func newPIDToPodMapper() *pidToPodMapper {
	return &pidToPodMapper{procRoot: defaultProcRoot, pidToUID: make(map[uint32]cachedPodUID)}
}

// getPodUIDForPID resolves a process cgroup UID and caches successful matches.
// A cached UID is used only while the process start time is unchanged.
//
// The process start time is read before and after the cgroup file. A missing
// or changed start time means the process exited or its PID was reused, so the
// PID is not attributed in this pass. NVML reports no start time, so a PID
// reused between the NVML sample and the first start-time read can still be
// attributed. That window is short, and the new process must belong to a pod
// already assigned to the same GPU.
func (m *pidToPodMapper) getPodUIDForPID(pid uint32) (string, error) {
	startTime, err := m.readStartTime(pid)
	if err != nil {
		return "", err
	}

	if cached, ok := m.pidToUID[pid]; ok && cached.startTime == startTime {
		return cached.uid, nil
	}

	cgroupPath := filepath.Join(m.procRoot, strconv.FormatUint(uint64(pid), 10), "cgroup")
	subsystems, unified, err := cgroups.ParseCgroupFileUnified(cgroupPath)
	if err != nil {
		return "", fmt.Errorf("failed to parse cgroup file for PID %d: %w", pid, err)
	}

	if err := m.checkStartTime(pid, startTime); err != nil {
		return "", err
	}

	uid := extractPodUIDFromPaths(subsystems, unified)
	if uid != "" {
		m.pidToUID[pid] = cachedPodUID{uid: uid, startTime: startTime}
	}
	return uid, nil
}

// checkStartTime fails when the process start time no longer matches the one
// read before the cgroup file, which means the PID now names another process.
func (m *pidToPodMapper) checkStartTime(pid uint32, want uint64) error {
	got, err := m.readStartTime(pid)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("PID %d was reused while its cgroup was read", pid)
	}
	return nil
}

// readStartTime returns the process start time from /proc/<pid>/stat.
// The command name can contain spaces and parentheses, so fields are counted
// from the last closing parenthesis.
func (m *pidToPodMapper) readStartTime(pid uint32) (uint64, error) {
	statPath := filepath.Join(m.procRoot, strconv.FormatUint(uint64(pid), 10), "stat")
	data, err := stdos.ReadFile(statPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read stat file for PID %d: %w", pid, err)
	}

	commEnd := strings.LastIndexByte(string(data), ')')
	if commEnd < 0 {
		return 0, fmt.Errorf("stat file for PID %d has no command name", pid)
	}

	fields := strings.Fields(string(data[commEnd+1:]))
	if len(fields) <= statStartTimeIndex {
		return 0, fmt.Errorf("stat file for PID %d has no start time", pid)
	}

	startTime, err := strconv.ParseUint(fields[statStartTimeIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse start time for PID %d: %w", pid, err)
	}
	return startTime, nil
}

// extractPodUIDFromPaths checks controller paths before the unified cgroup path.
func extractPodUIDFromPaths(subsystems map[string]string, unified string) string {
	for _, path := range subsystems {
		if uid := extractPodUID(path); uid != "" {
			return uid
		}
	}
	if uid := extractPodUID(unified); uid != "" {
		return uid
	}
	return ""
}

// extractPodUID normalizes the pod UID encoded in a cgroup path.
func extractPodUID(path string) string {
	matches := podUIDRegex.FindStringSubmatch(path)
	if len(matches) < 2 {
		return ""
	}
	uid := strings.ReplaceAll(matches[1], "_", "-")
	if len(uid) < 32 {
		return ""
	}
	return uid
}

// buildPIDToPodMap joins process cgroup UIDs to the current allocation metadata.
func (m *pidToPodMapper) buildPIDToPodMap(pids []uint32, pods []PodInfo) map[uint32]*PodInfo {
	uidToPod := make(map[string]*PodInfo)
	for i := range pods {
		if pods[i].UID != "" {
			uidToPod[pods[i].UID] = &pods[i]
		}
	}
	result := make(map[uint32]*PodInfo)
	for _, pid := range pids {
		uid, err := m.getPodUIDForPID(pid)
		if err != nil {
			slog.Debug("Failed to map PID to pod", "pid", pid, "error", err)
			// Warn once per mapper while retaining individual failures at debug level.
			m.cgroupWarnOnce.Do(func() {
				slog.Warn("Failed to map PID to pod, per-process metrics may be incomplete", "pid", pid, "error", err)
			})
			continue
		}
		if uid == "" {
			continue
		}
		if pod, ok := uidToPod[uid]; ok {
			result[pid] = pod
		}
	}

	return result
}
