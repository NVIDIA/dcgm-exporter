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
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestE2ELocalNodeConfiguresLegacyRuntimeDriverRoot keeps the local e2e image on the legacy runtime and host-driver root.
func TestE2ELocalNodeConfiguresLegacyRuntimeDriverRoot(t *testing.T) {
	contents, err := os.ReadFile(repoPath(t, "hack", "e2e-local-node.Dockerfile"))
	if err != nil {
		t.Fatalf("read e2e local node Dockerfile: %v", err)
	}
	var effectiveLines []string
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			effectiveLines = append(effectiveLines, line)
		}
	}
	effective := strings.Join(effectiveLines, "\n")
	if !strings.Contains(effective, `sed -i 's/^mode = .*/mode = "legacy"/' /etc/nvidia-container-runtime/config.toml`) {
		t.Fatal("e2e local node Dockerfile must configure the legacy NVIDIA runtime")
	}
	if !strings.Contains(effective, `sed -i 's|^#root = "/run/nvidia/driver"|root = "/run/nvidia/host-driver"|' /etc/nvidia-container-runtime/config.toml`) {
		t.Fatal("e2e local node Dockerfile must configure the legacy runtime driver root")
	}
}

// repoPath resolves a repository-relative file for source and package-mode tests.
func repoPath(t *testing.T, elems ...string) string {
	t.Helper()

	workingDir := mustGetwd(t)
	packageRoot, err := isValidationPackageRoot(workingDir)
	if err != nil {
		t.Fatalf("inspect validation package root %s: %v", workingDir, err)
	}

	var candidates [][]string
	if packageRoot {
		// Packaged tests run from the package root. Do not fall back to a source
		// checkout above it, which could hide a missing packaged asset.
		candidates = append(candidates, append([]string{workingDir}, elems...))
	} else {
		if testSrcDir, workspace := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); testSrcDir != "" && workspace != "" {
			candidates = append(candidates, append([]string{testSrcDir, workspace}, elems...))
		}
		candidates = append(
			candidates,
			append([]string{"..", ".."}, elems...),
			elems,
		)
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

// isValidationPackageRoot reports whether root is the extracted validation package.
func isValidationPackageRoot(root string) (bool, error) {
	info, err := os.Stat(filepath.Join(root, "bin", "dcgm-exporter-static.test"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}

	_, err = os.Stat(filepath.Join(root, "go.mod"))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
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
