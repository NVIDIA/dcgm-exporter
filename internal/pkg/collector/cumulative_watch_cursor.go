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

package collector

import (
	"sync"
	"time"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// cumulativeWatchCursorKey identifies a GetValuesSince cursor for one entity
// group and one physical field group. A logical watch group can span several
// field groups after oversized groups are split, so each pair keeps its own cursor.
type cumulativeWatchCursorKey struct {
	groupHandle      uintptr
	fieldGroupHandle uintptr
}

type cumulativeWatchPoll struct {
	group      dcgm.GroupHandle
	fieldGroup dcgm.FieldHandle
	values     []dcgm.FieldValue_v2
	nextSince  time.Time
}

func newCumulativeWatchCursorKey(group dcgm.GroupHandle, fieldGroup dcgm.FieldHandle) cumulativeWatchCursorKey {
	return cumulativeWatchCursorKey{
		groupHandle:      group.GetHandle(),
		fieldGroupHandle: fieldGroup.GetHandle(),
	}
}

// cumulativeWatchCursor returns the stored cursor for the entity/field-group pair,
// or initialSince when the pair has not been polled yet.
func cumulativeWatchCursor(
	mu *sync.RWMutex,
	cursors map[cumulativeWatchCursorKey]time.Time,
	initialSince time.Time,
	group dcgm.GroupHandle,
	fieldGroup dcgm.FieldHandle,
) time.Time {
	mu.RLock()
	defer mu.RUnlock()

	if cursor, exists := cursors[newCumulativeWatchCursorKey(group, fieldGroup)]; exists {
		return cursor
	}
	return initialSince
}
