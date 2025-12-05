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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractPodUID(t *testing.T) {
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
			result := extractPodUID(tc.path)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestExtractPodUIDFromPaths(t *testing.T) {
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
			result := extractPodUIDFromPaths(tc.subsystems, tc.unified)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestBuildPIDToPodMap(t *testing.T) {
	mapper := newPIDToPodMapper()

	pods := []PodInfo{
		{Name: "pod1", Namespace: "default", UID: "uid-1"},
		{Name: "pod2", Namespace: "default", UID: "uid-2"},
		{Name: "pod3", Namespace: "kube-system", UID: ""},
	}

	mapper.pidToUID[1001] = "uid-1"
	mapper.pidToUID[1002] = "uid-2"
	mapper.pidToUID[1003] = "uid-unknown"

	result := mapper.buildPIDToPodMap([]uint32{1001, 1002, 1003, 1004}, pods)

	assert.Len(t, result, 2)
	assert.Equal(t, "pod1", result[1001].Name)
	assert.Equal(t, "pod2", result[1002].Name)
	assert.Nil(t, result[1003])
	assert.Nil(t, result[1004])
}
