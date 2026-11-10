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

package static_test

import (
	"encoding/csv"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var dcgmFieldMapEntry = regexp.MustCompile(`"(DCGM_FI_[A-Z0-9_]+)"\s*:\s*([0-9]+),`)

// TestFieldDefinitionsPathUsesStagedMetadata prefers field metadata staged beside the test binary.
func TestFieldDefinitionsPathUsesStagedMetadata(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "bundle", "bin", "dcgm-exporter-static.test")
	staged := filepath.Join(root, "bundle", "etc", "go-dcgm-const-fields.go")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("staged fields"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fieldDefinitionsPath(root, binary); got != staged {
		t.Fatalf("field definitions path = %q, want staged metadata %q", got, staged)
	}
}

// TestFieldDefinitionsPathFallsBackWhenStagedMetadataIsUnavailable uses repository metadata when staged data is absent or not a file.
func TestFieldDefinitionsPathFallsBackWhenStagedMetadataIsUnavailable(t *testing.T) {
	tests := []struct {
		name              string
		stagedIsDirectory bool
	}{
		{name: "missing"},
		{name: "directory", stagedIsDirectory: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "bundle", "bin", "dcgm-exporter-static.test")
			if test.stagedIsDirectory {
				staged := filepath.Join(root, "bundle", "etc", "go-dcgm-const-fields.go")
				if err := os.MkdirAll(staged, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			want := filepath.Join(root, "etc", "go-dcgm-const-fields.go")
			if got := fieldDefinitionsPath(root, binary); got != want {
				t.Fatalf("field definitions path = %q, want repository fallback %q", got, want)
			}
		})
	}
}

// TestLatestValueBatchingFixtureUsesUniqueDCGMFieldIDs prevents aliases from weakening batching boundary coverage.
func TestLatestValueBatchingFixtureUsesUniqueDCGMFieldIDs(t *testing.T) {
	fixturePath := repoPath(t, "tests", "host", "testdata", "latest-value-batching-counters.csv")
	root := filepath.Clean(filepath.Join(filepath.Dir(fixturePath), "..", "..", ".."))
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate static test executable: %v", err)
	}
	definitionsPath := fieldDefinitionsPath(root, executable)
	if _, err := os.Stat(definitionsPath); os.IsNotExist(err) {
		//nolint:gosec // Bazel supplies the executable from the declared Go SDK target.
		download := exec.Command(goExecutable(), "mod", "download", "github.com/NVIDIA/go-dcgm")
		download.Dir = root
		if output, downloadErr := download.CombinedOutput(); downloadErr != nil {
			if errors.Is(downloadErr, exec.ErrNotFound) {
				t.Fatalf("go-dcgm field definitions missing: stage ../etc/go-dcgm-const-fields.go relative to the test binary's directory (etc beside bin), or provide Go for source runs: %v", downloadErr)
			}
			t.Fatalf("download go-dcgm module: %v\n%s", downloadErr, output)
		}

		//nolint:gosec // Bazel supplies the executable from the declared Go SDK target.
		cmd := exec.Command(goExecutable(), "list", "-m", "-f={{.Dir}}", "github.com/NVIDIA/go-dcgm")
		cmd.Dir = root
		var stderr strings.Builder
		cmd.Stderr = &stderr
		moduleDir, commandErr := cmd.Output()
		if commandErr != nil {
			t.Fatalf("locate go-dcgm module: %v\n%s", commandErr, stderr.String())
		}
		definitionsPath = filepath.Join(strings.TrimSpace(string(moduleDir)), "pkg", "dcgm", "const_fields.go")
	} else if err != nil {
		t.Fatal(err)
	}

	definitions, err := os.ReadFile(definitionsPath)
	if err != nil {
		t.Fatal(err)
	}
	fieldIDs := make(map[string]uint64)
	for _, match := range dcgmFieldMapEntry.FindAllSubmatch(definitions, -1) {
		fieldID, err := strconv.ParseUint(string(match[2]), 10, 16)
		if err != nil {
			t.Fatal(err)
		}
		fieldIDs[string(match[1])] = fieldID
	}

	file, err := os.Open(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.Comment = '#'
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[uint64]string, len(records))
	for row, record := range records {
		name := strings.TrimSpace(record[0])
		fieldID, ok := fieldIDs[name]
		if !ok {
			t.Fatalf("fixture row %d uses unknown field %q", row+1, name)
		}
		if existing, duplicate := seen[fieldID]; duplicate {
			t.Fatalf("fixture row %d field %q resolves to ID %d already used by %q", row+1, name, fieldID, existing)
		}
		seen[fieldID] = name
	}
}

// fieldDefinitionsPath prefers metadata staged beside the test binary and otherwise uses the repository copy.
func fieldDefinitionsPath(root, executable string) string {
	staged := filepath.Join(filepath.Dir(executable), "..", "etc", "go-dcgm-const-fields.go")
	if info, err := os.Stat(staged); err == nil && info.Mode().IsRegular() {
		return staged
	}
	return filepath.Join(root, "etc", "go-dcgm-const-fields.go")
}
