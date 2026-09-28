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

package devicewatcher

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
)

// maxFieldIDsPerFieldGroup limits each physical group created when splitting a logical watch group.
const maxFieldIDsPerFieldGroup = 127

// chunkFields splits fields into stable, order-preserving chunks of at most limit IDs.
func chunkFields(fields []dcgm.Short, limit int) [][]dcgm.Short {
	if len(fields) == 0 || limit <= 0 {
		return nil
	}

	chunkCount := (len(fields) + limit - 1) / limit
	chunks := make([][]dcgm.Short, 0, chunkCount)
	for start := 0; start < len(fields); start += limit {
		end := start + limit
		if end > len(fields) {
			end = len(fields)
		}
		chunks = append(chunks, append([]dcgm.Short(nil), fields[start:end]...))
	}

	return chunks
}

func logSplitFieldWatchGroup(logicalGroup string, totalFields, chunkIndex, chunkCount, chunkFieldCount int) {
	if chunkCount <= 1 {
		return
	}

	slog.LogAttrs(
		context.Background(),
		slog.LevelInfo,
		"split oversized watch group into physical DCGM field groups",
		slog.String("logical_group", logicalGroup),
		slog.Int("total_fields", totalFields),
		slog.Int("chunk_index", chunkIndex),
		slog.Int("chunk_count", chunkCount),
		slog.Int("chunk_fields", chunkFieldCount),
		slog.Int("capacity", maxFieldIDsPerFieldGroup),
	)
}

func fieldGroupCreateError(
	logicalGroup string,
	chunkIndex, chunkCount, fieldStart, fieldEnd int,
	err error,
) error {
	return fmt.Errorf(
		"create physical field group for logical group %q chunk %d/%d (fields %d-%d, capacity %d): %w",
		logicalGroup,
		chunkIndex,
		chunkCount,
		fieldStart,
		fieldEnd,
		maxFieldIDsPerFieldGroup,
		err,
	)
}
