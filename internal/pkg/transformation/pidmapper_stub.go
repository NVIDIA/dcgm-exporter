//go:build !linux

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

import "fmt"

type pidToPodMapper struct{}

func newPIDToPodMapper() *pidToPodMapper {
	return &pidToPodMapper{}
}

func (m *pidToPodMapper) getPodUIDForPID(pid uint32) (string, error) {
	return "", fmt.Errorf("PID to Pod mapping is only supported on Linux")
}

func (m *pidToPodMapper) buildPIDToPodMap(pids []uint32, pods []PodInfo) map[uint32]*PodInfo {
	return make(map[uint32]*PodInfo)
}
