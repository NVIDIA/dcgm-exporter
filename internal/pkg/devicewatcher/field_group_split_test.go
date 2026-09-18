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
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkFields(t *testing.T) {
	fields := make([]dcgm.Short, 255)
	for i := range fields {
		fields[i] = dcgm.Short(1000 + i)
	}

	tests := []struct {
		name      string
		fields    []dcgm.Short
		limit     int
		wantSizes []int
	}{
		{name: "empty", fields: nil, limit: 127, wantSizes: nil},
		{name: "single", fields: fields[:1], limit: 127, wantSizes: []int{1}},
		{name: "boundary", fields: fields[:127], limit: 127, wantSizes: []int{127}},
		{name: "boundary plus one", fields: fields[:128], limit: 127, wantSizes: []int{127, 1}},
		{name: "two full chunks", fields: fields[:254], limit: 127, wantSizes: []int{127, 127}},
		{name: "two full chunks plus remainder", fields: fields, limit: 127, wantSizes: []int{127, 127, 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chunkFields(tt.fields, tt.limit)
			if tt.wantSizes == nil {
				assert.Nil(t, got)
				return
			}

			requireSizes := make([]int, len(got))
			for i, chunk := range got {
				requireSizes[i] = len(chunk)
			}
			require.Equal(t, tt.wantSizes, requireSizes)

			if len(tt.fields) > 0 {
				assert.Equal(t, tt.fields[0], got[0][0])
				assert.Equal(t, tt.fields[len(tt.fields)-1], got[len(got)-1][len(got[len(got)-1])-1])
			}
		})
	}
}

func TestDedupeFieldsThenChunkFields(t *testing.T) {
	fields := dedupeFields([]dcgm.Short{1, 2, 2, 3})
	got := chunkFields(fields, maxFieldIDsPerFieldGroup)

	require.Len(t, got, 1)
	assert.Equal(t, []dcgm.Short{1, 2, 3}, got[0])
}
