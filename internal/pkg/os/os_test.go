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

package os

import (
	stdos "os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealOSFilesystemWrappers(t *testing.T) {
	realOS := RealOS{}

	dir, err := realOS.MkdirTemp("", "dcgm-exporter-os-test-*")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, realOS.RemoveAll(dir))
	}()

	file, err := realOS.CreateTemp(dir, "sample-*")
	require.NoError(t, err)
	_, err = file.WriteString("data")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	info, err := realOS.Stat(file.Name())
	require.NoError(t, err)
	assert.False(t, info.IsDir())

	opened, err := realOS.Open(file.Name())
	require.NoError(t, err)
	require.NoError(t, opened.Close())

	entries, err := realOS.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	require.NoError(t, realOS.Remove(file.Name()))
	_, err = realOS.Stat(file.Name())
	assert.True(t, realOS.IsNotExist(err))
}

func TestRealOSEnvironmentWrappers(t *testing.T) {
	realOS := RealOS{}
	t.Setenv("DCGM_EXPORTER_OS_TEST", "set")

	assert.Equal(t, "set", realOS.Getenv("DCGM_EXPORTER_OS_TEST"))
	assert.NotEmpty(t, realOS.TempDir())
	hostname, err := realOS.Hostname()
	stdHostname, stdErr := stdos.Hostname()
	assert.Equal(t, stdHostname, hostname)
	assert.Equal(t, stdErr, err)

	missing := filepath.Join(t.TempDir(), "missing")
	_, err = realOS.Stat(missing)
	assert.True(t, realOS.IsNotExist(err))
}
