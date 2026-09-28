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

package debug

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestFileDumper_DumpToFile(t *testing.T) {
	tests := []struct {
		name        string
		compression bool
		suffix      string
		read        func(*testing.T, string) map[string]string
	}{
		{
			name:        "plain JSON",
			compression: false,
			suffix:      ".json",
			read: func(t *testing.T, path string) map[string]string {
				t.Helper()
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				var got map[string]string
				require.NoError(t, json.Unmarshal(data, &got))
				return got
			},
		},
		{
			name:        "gzip JSON",
			compression: true,
			suffix:      ".json.gz",
			read: func(t *testing.T, path string) map[string]string {
				t.Helper()
				f, err := os.Open(path)
				require.NoError(t, err)
				defer f.Close()

				gz, err := gzip.NewReader(f)
				require.NoError(t, err)
				defer gz.Close()

				var got map[string]string
				require.NoError(t, json.NewDecoder(gz).Decode(&got))
				return got
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fd := NewFileDumper(appconfig.DumpConfig{
				Enabled:     true,
				Directory:   t.TempDir(),
				Compression: tt.compression,
			})

			path, err := fd.DumpToFile(map[string]string{"gpu": "0"}, "metrics", "GPU")

			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(path, tt.suffix))
			assert.Equal(t, map[string]string{"gpu": "0"}, tt.read(t, path))
		})
	}
}

func TestFileDumper_DumpToFileDisabled(t *testing.T) {
	fd := NewFileDumper(appconfig.DumpConfig{Enabled: false, Directory: t.TempDir()})

	path, err := fd.DumpToFile(map[string]string{"ignored": "true"}, "metrics", "GPU")

	require.NoError(t, err)
	assert.Empty(t, path)
}

func TestFileDumper_DumpToFileErrors(t *testing.T) {
	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "not-a-directory")
	require.NoError(t, os.WriteFile(blockingFile, []byte("x"), 0o600))

	fd := NewFileDumper(appconfig.DumpConfig{
		Enabled:   true,
		Directory: filepath.Join(blockingFile, "child"),
	})

	_, err := fd.DumpToFile(map[string]string{"gpu": "0"}, "metrics", "GPU")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create debug directory")
}

func TestFileDumper_CleanupOldFiles(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "old.json")
	newFile := filepath.Join(dir, "new.json")
	require.NoError(t, os.WriteFile(oldFile, []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(newFile, []byte("{}"), 0o600))

	oldTime := time.Now().Add(-3 * time.Hour)
	require.NoError(t, os.Chtimes(oldFile, oldTime, oldTime))

	fd := NewFileDumper(appconfig.DumpConfig{Directory: dir, Retention: 1})
	require.NoError(t, fd.CleanupOldFiles())

	_, err := os.Stat(oldFile)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(newFile)
	assert.NoError(t, err)
}

func TestFileDumper_CleanupOldFilesNoopAndErrors(t *testing.T) {
	assert.NoError(t, NewFileDumper(appconfig.DumpConfig{Retention: 0}).CleanupOldFiles())
	assert.NoError(t, NewFileDumper(appconfig.DumpConfig{
		Directory: filepath.Join(t.TempDir(), "missing"),
		Retention: 1,
	}).CleanupOldFiles())

	dir := t.TempDir()
	notDirectory := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(notDirectory, []byte("x"), 0o600))

	err := NewFileDumper(appconfig.DumpConfig{Directory: notDirectory, Retention: 1}).CleanupOldFiles()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read debug directory")
}
