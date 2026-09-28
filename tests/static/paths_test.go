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
	"os"
	"path/filepath"
	"testing"
)

// TestRepoPathResolvesSourceAndPackageFiles verifies static tests can locate source/package assets.
func TestRepoPathResolvesSourceAndPackageFiles(t *testing.T) {
	for _, elems := range [][]string{
		{"deployment", "Chart.yaml"},
		{"docker", "Dockerfile"},
		{"etc", "default-counters.csv"},
	} {
		if path := repoPath(t, elems...); path == "" {
			t.Fatalf("repoPath(%q) returned an empty path", filepath.Join(elems...))
		}
	}
}

// repoPath resolves a repository-relative file for source and package-mode tests.
func repoPath(t *testing.T, elems ...string) string {
	t.Helper()

	candidates := [][]string{
		elems,
		append([]string{"..", ".."}, elems...),
	}
	for _, candidate := range candidates {
		path := filepath.Join(candidate...)
		if _, err := os.Stat(path); err == nil {
			abs, err := filepath.Abs(path)
			if err != nil {
				t.Fatalf("resolve %s: %v", path, err)
			}
			return abs
		}
	}

	t.Fatalf("repository path %q was not found from %s", filepath.Join(elems...), mustGetwd(t))
	return ""
}

// mustGetwd returns the current working directory for failure messages.
func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return wd
}
