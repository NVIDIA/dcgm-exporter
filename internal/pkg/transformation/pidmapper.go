//go:build linux

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
	"fmt"
	"regexp"
	"strings"

	"github.com/containerd/cgroups/v3"
)

var podUIDRegex = regexp.MustCompile(`pod([a-f0-9_-]+)`)

type pidToPodMapper struct {
	pidToUID map[uint32]string
}

func newPIDToPodMapper() *pidToPodMapper {
	return &pidToPodMapper{pidToUID: make(map[uint32]string)}
}

func (m *pidToPodMapper) getPodUIDForPID(pid uint32) (string, error) {
	if uid, ok := m.pidToUID[pid]; ok {
		return uid, nil
	}

	cgroupPath := fmt.Sprintf("/proc/%d/cgroup", pid)
	subsystems, unified, err := cgroups.ParseCgroupFileUnified(cgroupPath)
	if err != nil {
		return "", fmt.Errorf("failed to parse cgroup file for PID %d: %w", pid, err)
	}

	uid := extractPodUIDFromPaths(subsystems, unified)
	if uid != "" {
		m.pidToUID[pid] = uid
	}
	return uid, nil
}

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
