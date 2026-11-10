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
	"errors"
	"fmt"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExitStatusOfErrors checks the exit status for each kind of error.
func TestExitStatusOfErrors(t *testing.T) {
	assert.Equal(t, exitFailure, ExitStatus(errors.New("plain")))
	assert.False(t, AlreadyReported(errors.New("plain")))

	usage := usageError(errors.New("bad flag"))
	assert.Equal(t, exitUsage, ExitStatus(usage))
	assert.False(t, AlreadyReported(usage))
	assert.Equal(t, usage, usageError(usage), "an error with a status keeps it")
	assert.NoError(t, usageError(nil))

	reported := reportedFailure(exitFailure, errors.New("invalid"))
	assert.Equal(t, exitFailure, ExitStatus(reported))
	assert.True(t, AlreadyReported(reported))
	assert.Equal(t, exitUsage, ExitStatus(fmt.Errorf("wrapped: %w", usage)))
	assert.EqualError(t, reported, "invalid")
	require.ErrorIs(t, usageError(fs.ErrNotExist), fs.ErrNotExist)
}

// TestStatusErrorTextIsEscaped checks that status error text is escaped.
func TestStatusErrorTextIsEscaped(t *testing.T) {
	err := usageError(errors.New("bad\x1b[2J value"))
	assert.Equal(t, `bad\x1b[2J value`, err.Error())
}
