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

// Package suite builds and runs e2e suite test binaries.
package suite

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// Definition describes one suite test binary.
type Definition struct {
	Suite         scenario.Suite
	DisplayName   string
	Package       string
	Tags          string
	BinaryName    string
	StalenessDirs []string
	StalenessGlob string
	BuildEnv      []string
}

// Manager builds and locates suite binaries.
type Manager struct {
	Root     string
	ToolsDir string
	Runner   e2eexec.Runner
}

// NewManager creates a suite binary manager.
func NewManager(root string, runner e2eexec.Runner) Manager {
	return Manager{
		Root:     root,
		ToolsDir: filepath.Join(root, ".e2e-tools"),
		Runner:   runner,
	}
}

// DefinitionFor returns the suite entrypoint plus suite-specific build metadata.
func DefinitionFor(name scenario.Suite) (Definition, bool) {
	if !scenario.ValidSuite(name) {
		return Definition{}, false
	}
	def := Definition{
		Suite:       name,
		DisplayName: string(name) + " test binary",
		Package:     "./tests/" + string(name),
		BinaryName:  "dcgm-exporter-" + string(name) + ".test",
	}
	overrides := map[scenario.Suite]Definition{
		scenario.SuiteStatic: {
			DisplayName:   "static test binary",
			StalenessDirs: []string{"tests/static", "deployment", "docker", "etc"},
		},
		scenario.SuiteHost: {
			DisplayName:   "host integration binary",
			Tags:          "dcgm_uri_integration",
			StalenessDirs: []string{"tests/host", "tests/internal"},
			StalenessGlob: "*.go",
			BuildEnv:      []string{"CGO_ENABLED=0"},
		},
		scenario.SuiteContainer: {
			DisplayName:   "container integration binary",
			Tags:          "container",
			StalenessDirs: []string{"tests/container", "tests/internal", "docker"},
		},
		scenario.SuiteK8s: {
			DisplayName:   "k8s test binary",
			Tags:          "e2e",
			StalenessDirs: []string{"tests/k8s", "tests/internal", "internal", "pkg"},
			StalenessGlob: "*.go",
		},
	}
	override := overrides[name]
	if override.DisplayName != "" {
		def.DisplayName = override.DisplayName
	}
	def.Tags = override.Tags
	def.StalenessDirs = override.StalenessDirs
	def.StalenessGlob = override.StalenessGlob
	def.BuildEnv = override.BuildEnv
	return def, true
}

// BinaryPath returns the validation binary path for the current layout.
func (m Manager) BinaryPath(def Definition) string {
	if !m.SourceTreeAvailable() {
		return filepath.Join(m.Root, "bin", def.BinaryName)
	}
	return filepath.Join(m.ToolsDir, def.BinaryName)
}

// ProductBinaryPath returns the dcgm-exporter product binary path for the current layout.
func (m Manager) ProductBinaryPath() string {
	return filepath.Join(m.Root, "bin", "dcgm-exporter")
}

// DCGMProbeBinaryPath returns the helper used to query injected DCGM directly.
func (m Manager) DCGMProbeBinaryPath() string {
	if !m.SourceTreeAvailable() {
		return filepath.Join(m.Root, "bin", "e2e-dcgm-probe")
	}
	return filepath.Join(m.ToolsDir, "e2e-dcgm-probe")
}

// SourceTreeAvailable reports whether the current root is a Go source tree.
func (m Manager) SourceTreeAvailable() bool {
	_, err := os.Stat(filepath.Join(m.Root, "go.mod"))
	return err == nil
}

// EnsureProductBinary builds or locates the dcgm-exporter product binary used by host tests.
func (m Manager) EnsureProductBinary(ctx context.Context) (string, error) {
	_ = ctx
	path := m.ProductBinaryPath()
	if info, err := os.Stat(path); err == nil {
		if info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("dcgm-exporter product binary is not executable: %s", path)
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "", fmt.Errorf("dcgm-exporter binary is missing: %s; use the complete make test-e2e workflow or place the binary at that path", path)
}

// EnsureDCGMProbeBinary builds or locates the helper used by NVML-injection tests.
func (m Manager) EnsureDCGMProbeBinary(ctx context.Context) (string, error) {
	path := m.DCGMProbeBinaryPath()
	if !m.SourceTreeAvailable() {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("DCGM probe binary is not executable: %s", path)
		}
		return path, nil
	}

	definition := Definition{
		DisplayName:   "DCGM injection probe",
		Package:       "./tests/cmd/e2e-dcgm-probe",
		StalenessDirs: []string{"tests/cmd/e2e-dcgm-probe", "tests/internal/nvmlinjection"},
		StalenessGlob: "*.go",
		BuildEnv:      []string{"CGO_ENABLED=1"},
	}
	stale, err := m.Stale(definition, path)
	if err != nil {
		return "", err
	}
	if !stale {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	result := m.Runner.Run(ctx, e2eexec.Command{
		Name: "go",
		Args: []string{"build", "-buildvcs=false", "-trimpath", "-o", path, definition.Package},
		Env:  definition.BuildEnv,
		Dir:  m.Root,
	})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("build %s: %w\n%s%s", definition.DisplayName, errFromResult(result), result.Stdout, result.Stderr)
	}
	return path, nil
}

// Ensure builds or reuses one validation binary.
func (m Manager) Ensure(ctx context.Context, suiteName scenario.Suite, dryRun bool) (string, error) {
	def, ok := DefinitionFor(suiteName)
	if !ok {
		return "", fmt.Errorf("unknown validation suite %s", suiteName)
	}
	if dryRun && suiteName == scenario.SuiteK8s {
		return m.BinaryPath(def), nil
	}

	path := m.BinaryPath(def)
	if !m.SourceTreeAvailable() {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("%s is not executable: %s", def.DisplayName, path)
		}
		return path, nil
	}
	if stale, err := m.Stale(def, path); err != nil {
		return "", err
	} else if !stale {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	args := []string{"test", "-c"}
	if def.Tags != "" {
		args = append(args, "--tags="+def.Tags)
	}
	args = append(args, "-o", path, def.Package)
	result := m.Runner.Run(ctx, e2eexec.Command{Name: "go", Args: args, Env: def.BuildEnv, Dir: m.Root})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("build %s: %w\n%s%s", def.DisplayName, errFromResult(result), result.Stdout, result.Stderr)
	}
	if info, err := os.Stat(path); err != nil {
		return "", err
	} else if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable: %s", def.DisplayName, path)
	}
	return path, nil
}

// Stale reports whether a source-mode validation binary predates its inputs.
func (m Manager) Stale(def Definition, binary string) (bool, error) {
	if !m.SourceTreeAvailable() {
		return false, nil
	}
	binaryInfo, err := os.Stat(binary)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if binaryInfo.Mode()&0o111 == 0 {
		return true, nil
	}

	for _, name := range []string{"go.mod", "go.sum"} {
		path := filepath.Join(m.Root, name)
		info, err := os.Stat(path)
		if err == nil && info.ModTime().After(binaryInfo.ModTime()) {
			return true, nil
		}
	}

	for _, dir := range def.StalenessDirs {
		root := filepath.Join(m.Root, dir)
		rootInfo, err := os.Stat(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		if rootInfo.ModTime().After(binaryInfo.ModTime()) {
			return true, nil
		}
		newer := false
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				info, err := entry.Info()
				if err == nil && info.ModTime().After(binaryInfo.ModTime()) {
					newer = true
					return fs.SkipAll
				}
				return nil
			}
			if def.StalenessGlob != "" {
				matched, err := filepath.Match(def.StalenessGlob, filepath.Base(path))
				if err != nil || !matched {
					return nil
				}
			}
			info, err := entry.Info()
			if err == nil && info.ModTime().After(binaryInfo.ModTime()) {
				newer = true
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		if newer {
			return true, nil
		}
	}
	return false, nil
}

// StaticRegex returns the Go test regex for selected static scenarios.
func StaticRegex(selected []scenario.Scenario) string {
	return strings.Join(splitResultNames(selected), "|")
}

// LabelFilter returns the Ginkgo label filter for selected host/container scenarios.
func LabelFilter(selected []scenario.Scenario) string {
	return strings.Join(splitResultNames(selected), " || ")
}

// splitResultNames expands comma-separated catalog result names for regex and label filters.
func splitResultNames(selected []scenario.Scenario) []string {
	var names []string
	for _, entry := range selected {
		for _, name := range strings.Split(entry.ResultName, ",") {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				names = append(names, trimmed)
			}
		}
	}
	return names
}

// errFromResult preserves an execution error or falls back to the command exit code.
func errFromResult(result e2eexec.Result) error {
	if result.Err != nil {
		return result.Err
	}
	return fmt.Errorf("exit code %d", result.ExitCode)
}
