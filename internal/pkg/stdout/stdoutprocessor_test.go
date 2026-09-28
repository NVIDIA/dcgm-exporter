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

package stdout

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseOutputEntry(t *testing.T) {
	parsed := parseOutputEntry("2024-02-07 18:01:05.641 INFO [1:1] Linux driver started")
	assert.False(t, parsed.IsRawString)
	assert.Equal(t, "INFO", parsed.Level)
	assert.Equal(t, "Linux driver started", parsed.Message)
	assert.Equal(t, 2024, parsed.Timestamp.Year())

	raw := parseOutputEntry("not enough fields")
	assert.True(t, raw.IsRawString)
	assert.Equal(t, "not enough fields", raw.Message)

	badTimestamp := parseOutputEntry("2024-99-99 18:01:05.641 INFO [1:1] bad")
	assert.True(t, badTimestamp.IsRawString)
	assert.Equal(t, "2024-99-99 18:01:05.641 INFO [1:1] bad", badTimestamp.Message)
}
