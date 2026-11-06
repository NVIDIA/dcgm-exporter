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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHostSuiteDoesNotImportExporterInternals keeps host integration tests black-box.
func TestHostSuiteDoesNotImportExporterInternals(t *testing.T) {
	root := repoPath(t)
	skipWithoutGoSourceTree(t, root)
	for _, tags := range []string{"", "dcgm_uri_integration"} {
		t.Run("tags="+tags, func(t *testing.T) {
			for _, dep := range hostSuiteDependencies(t, root, tags) {
				if reason := forbiddenHostSuiteDependency(dep); reason != "" {
					t.Fatalf("%s %s", reason, dep)
				}
			}
		})
	}
}

// skipWithoutGoSourceTree skips dependency analysis outside a source checkout or without a Go toolchain.
func skipWithoutGoSourceTree(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "go.mod")); os.IsNotExist(err) {
		t.Skip("host dependency analysis requires a source checkout and Go toolchain")
	} else if err != nil {
		t.Fatal(err)
	}
	if goExecutable() != "go" {
		return
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("host dependency analysis requires a source checkout and Go toolchain")
	}
}

// hostSuiteDependencies lists every package the host suite test binary depends on for one tag set.
func hostSuiteDependencies(t *testing.T, root, tags string) []string {
	t.Helper()
	args := []string{"list", "-deps", "-test", "-buildvcs=false"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "./tests/host")

	//nolint:gosec // Bazel supplies the executable from the declared Go SDK target.
	cmd := exec.Command(goExecutable(), args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.Fields(string(out))
}

// forbiddenHostSuiteDependency explains why a host suite dependency breaks the black-box boundary.
func forbiddenHostSuiteDependency(dep string) string {
	switch {
	case dep == "github.com/NVIDIA/dcgm-exporter/pkg/cmd":
		return "host suite must exec the product binary, not import"
	case strings.HasPrefix(dep, "github.com/NVIDIA/dcgm-exporter/internal/"):
		return "host suite must not import exporter internal package"
	case dep == "github.com/NVIDIA/go-dcgm" || strings.HasPrefix(dep, "github.com/NVIDIA/go-dcgm/"):
		return "host suite must not depend on go-dcgm package"
	}
	return ""
}

// goExecutable resolves the Go tool from Bazel runfiles or uses PATH outside Bazel.
func goExecutable() string {
	executable := os.Getenv("DCGM_EXPORTER_GO_RLOCATION")
	if executable == "" {
		return "go"
	}
	if filepath.IsAbs(executable) {
		return executable
	}
	for _, runfilesDirectory := range []string{
		os.Getenv("TEST_SRCDIR"),
		os.Getenv("RUNFILES_DIR"),
	} {
		if runfilesDirectory != "" {
			return filepath.Join(runfilesDirectory, executable)
		}
	}
	if testExecutable, err := os.Executable(); err == nil {
		if runfilesDirectory := runfilesRoot(testExecutable); runfilesDirectory != "" {
			return filepath.Join(runfilesDirectory, executable)
		}
	}
	return executable
}

// runfilesRoot returns the executable's runfiles root when its path contains the Bazel marker.
func runfilesRoot(executable string) string {
	marker := ".runfiles" + string(os.PathSeparator)
	markerIndex := strings.Index(executable, marker)
	if markerIndex == -1 {
		return ""
	}
	return executable[:markerIndex+len(".runfiles")]
}

// TestGoExecutable covers PATH, configured runfiles, and absolute Go tool locations.
func TestGoExecutable(t *testing.T) {
	t.Run("uses the host Go executable outside Bazel", func(t *testing.T) {
		t.Setenv("DCGM_EXPORTER_GO_RLOCATION", "")
		t.Setenv("TEST_SRCDIR", "")
		t.Setenv("RUNFILES_DIR", "")

		assert.Equal(t, "go", goExecutable())
	})

	t.Run("resolves a Bazel test source directory", func(t *testing.T) {
		t.Setenv("DCGM_EXPORTER_GO_RLOCATION", "rules_go/bin/go")
		t.Setenv("TEST_SRCDIR", "/tmp/runfiles")
		t.Setenv("RUNFILES_DIR", "/tmp/fallback")

		assert.Equal(t, "/tmp/runfiles/rules_go/bin/go", goExecutable())
	})

	t.Run("resolves a Bazel runfiles directory", func(t *testing.T) {
		t.Setenv("DCGM_EXPORTER_GO_RLOCATION", "rules_go/bin/go")
		t.Setenv("TEST_SRCDIR", "")
		t.Setenv("RUNFILES_DIR", "/tmp/runfiles")

		assert.Equal(t, "/tmp/runfiles/rules_go/bin/go", goExecutable())
	})

	t.Run("preserves an absolute Bazel runfile", func(t *testing.T) {
		t.Setenv("DCGM_EXPORTER_GO_RLOCATION", "/opt/go/bin/go")
		t.Setenv("TEST_SRCDIR", "/tmp/runfiles")
		t.Setenv("RUNFILES_DIR", "/tmp/fallback")

		assert.Equal(t, "/opt/go/bin/go", goExecutable())
	})
}

// TestRunfilesRoot extracts a Bazel runfiles directory and leaves ordinary paths unmatched.
func TestRunfilesRoot(t *testing.T) {
	assert.Equal(
		t,
		"/tmp/static_test.runfiles",
		runfilesRoot("/tmp/static_test.runfiles/_main/tests/static/static_test"),
	)
	assert.Empty(t, runfilesRoot("/tmp/static_test"))
}
