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
	"sort"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// TestBinaryDefinition describes one suite test binary.
type TestBinaryDefinition struct {
	Suite         scenario.Suite
	DisplayName   string
	Package       string
	Tags          string
	TestName      string
	BinaryName    string
	StalenessDirs []string
	StalenessGlob string
	BuildEnv      []string
}

// TestBinaryManager builds and locates suite binaries.
type TestBinaryManager struct {
	Root             string
	ToolsDir         string
	ExporterBinary   string
	PrebuiltBinaries bool
	Runner           e2eexec.Runner
}

// NewTestBinaryManager creates a test binary manager.
func NewTestBinaryManager(root string, runner e2eexec.Runner) TestBinaryManager {
	return TestBinaryManager{
		Root:     root,
		ToolsDir: filepath.Join(root, ".e2e-tools"),
		Runner:   runner,
	}
}

// NewManager creates a test binary manager for callers using the prior package API.
func NewManager(root string, runner e2eexec.Runner) TestBinaryManager {
	return NewTestBinaryManager(root, runner)
}

// TestBinaryDefinitionFor returns the suite entrypoint plus suite-specific build metadata.
func TestBinaryDefinitionFor(name scenario.Suite) (TestBinaryDefinition, bool) {
	if !scenario.ValidSuite(name) {
		return TestBinaryDefinition{}, false
	}
	def := TestBinaryDefinition{
		Suite:       name,
		DisplayName: string(name) + " test binary",
		Package:     "./tests/" + string(name),
		BinaryName:  "dcgm-exporter-" + string(name) + ".test",
	}
	overrides := map[scenario.Suite]TestBinaryDefinition{
		scenario.SuiteStatic: {
			DisplayName:   "static test binary",
			StalenessDirs: []string{"tests/static", "deployment", "docker", "etc"},
		},
		scenario.SuiteHost: {
			DisplayName:   "host integration binary",
			Tags:          "dcgm_uri_integration",
			TestName:      "TestHostIntegration",
			StalenessDirs: []string{"tests/host", "tests/internal"},
			StalenessGlob: "*.go",
			BuildEnv:      []string{"CGO_ENABLED=0"},
		},
		scenario.SuiteContainer: {
			DisplayName:   "container integration binary",
			Tags:          "container",
			TestName:      "TestDockerImages",
			StalenessDirs: []string{"tests/container", "tests/internal", "docker"},
		},
		scenario.SuiteK8s: {
			DisplayName:   "k8s test binary",
			Tags:          "k8s",
			TestName:      "TestKubernetesIntegration",
			StalenessDirs: []string{"tests/k8s", "tests/internal", "internal", "pkg"},
			StalenessGlob: "*.go",
		},
	}
	override := overrides[name]
	if override.DisplayName != "" {
		def.DisplayName = override.DisplayName
	}
	def.Tags = override.Tags
	def.TestName = override.TestName
	def.StalenessDirs = override.StalenessDirs
	def.StalenessGlob = override.StalenessGlob
	def.BuildEnv = override.BuildEnv
	return def, true
}

// DirectGoCommand renders one catalog scenario as a focused source-tree go test command.
func DirectGoCommand(entry scenario.Scenario) (e2eexec.Command, error) {
	definition, ok := TestBinaryDefinitionFor(entry.Suite)
	if !ok {
		return e2eexec.Command{}, fmt.Errorf("scenario %q has no suite definition", entry.Selector())
	}
	if strings.TrimSpace(entry.ResultName) == "" {
		return e2eexec.Command{}, fmt.Errorf("scenario %q has no result name", entry.Selector())
	}

	args := []string{"test"}
	if definition.Tags != "" {
		args = append(args, "--tags="+definition.Tags)
	}
	args = append(args, definition.Package)
	if entry.Suite == scenario.SuiteStatic {
		args = append(args, "-run", "^("+StaticRegex([]scenario.Scenario{entry})+")$")
	} else {
		if definition.TestName == "" {
			return e2eexec.Command{}, fmt.Errorf("scenario %q has no suite test name", entry.Selector())
		}
		args = append(
			args,
			"-run", "^"+definition.TestName+"$",
			"-args", "--ginkgo.fail-on-empty", "--ginkgo.label-filter="+LabelFilter([]scenario.Scenario{entry}),
		)
	}
	return e2eexec.Command{Name: "go", Args: args, Env: definition.BuildEnv}, nil
}

// BinaryPath returns the validation binary path for the current layout.
func (m TestBinaryManager) BinaryPath(def TestBinaryDefinition) string {
	if !m.SourceTreeAvailable() {
		return filepath.Join(m.Root, "bin", def.BinaryName)
	}
	return filepath.Join(m.ToolsDir, def.BinaryName)
}

// ProductBinaryPath returns the dcgm-exporter product binary path for the current layout.
func (m TestBinaryManager) ProductBinaryPath() string {
	if m.ExporterBinary != "" {
		if filepath.IsAbs(m.ExporterBinary) {
			return m.ExporterBinary
		}
		return filepath.Join(m.Root, m.ExporterBinary)
	}
	return filepath.Join(m.Root, "bin", "dcgm-exporter")
}

// DCGMProbeBinaryPath returns the helper used to query DCGM directly.
func (m TestBinaryManager) DCGMProbeBinaryPath() string {
	if !m.SourceTreeAvailable() {
		return filepath.Join(m.Root, "bin", "e2e-dcgm-probe")
	}
	return filepath.Join(m.ToolsDir, "e2e-dcgm-probe")
}

// SourceTreeAvailable reports whether the current root is a Go source tree.
func (m TestBinaryManager) SourceTreeAvailable() bool {
	_, err := os.Stat(filepath.Join(m.Root, "go.mod"))
	return err == nil
}

// EnsureProductBinary builds the source-tree exporter or verifies a supplied artifact.
func (m TestBinaryManager) EnsureProductBinary(ctx context.Context) (string, error) {
	path := m.ProductBinaryPath()
	if m.ExporterBinary != "" || m.PrebuiltBinaries || !m.SourceTreeAvailable() {
		if err := requireExecutable(path, "dcgm-exporter binary"); err != nil {
			return "", err
		}
		return path, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	result := m.Runner.Run(ctx, e2eexec.Command{
		Name: "go",
		Args: []string{"build", "-buildvcs=false", "-trimpath", "-o", path, "./cmd/dcgm-exporter"},
		Env:  []string{"CGO_ENABLED=1"},
		Dir:  m.Root,
	})
	if result.ExitCode != 0 {
		return "", fmt.Errorf("build dcgm-exporter binary: %w\n%s%s", errFromResult(result), result.Stdout, result.Stderr)
	}
	if err := requireExecutable(path, "dcgm-exporter binary"); err != nil {
		return "", err
	}
	return path, nil
}

// EnsureDCGMProbeBinary builds or locates the helper used by DCGM e2e tests.
func (m TestBinaryManager) EnsureDCGMProbeBinary(ctx context.Context) (string, error) {
	path := m.DCGMProbeBinaryPath()
	if m.PrebuiltBinaries || !m.SourceTreeAvailable() {
		if err := requireExecutable(path, "DCGM probe binary"); err != nil {
			return "", err
		}
		return path, nil
	}

	definition := TestBinaryDefinition{
		DisplayName: "DCGM e2e probe",
		Package:     "./tests/cmd/e2e-dcgm-probe",
		BuildEnv:    []string{"CGO_ENABLED=1"},
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
func (m TestBinaryManager) Ensure(ctx context.Context, suiteName scenario.Suite, dryRun bool) (string, error) {
	def, ok := TestBinaryDefinitionFor(suiteName)
	if !ok {
		return "", fmt.Errorf("unknown validation suite %s", suiteName)
	}
	if dryRun && suiteName == scenario.SuiteK8s {
		return m.BinaryPath(def), nil
	}

	path := m.BinaryPath(def)
	if m.PrebuiltBinaries || !m.SourceTreeAvailable() {
		if err := requireExecutable(path, def.DisplayName); err != nil {
			return "", err
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

func requireExecutable(path, description string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s is missing: %s", description, path)
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file: %s", description, path)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable: %s", description, path)
	}
	return nil
}

// Stale reports whether a source-mode validation binary predates its inputs.
func (m TestBinaryManager) Stale(def TestBinaryDefinition, binary string) (bool, error) {
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

// VerifyStaticCatalog compares the catalog with the native static test binary
// before the selected tests run. It keeps catalog drift from turning a valid Go
// command with a non-matching -test.run expression into a successful no-op.
func VerifyStaticCatalog(ctx context.Context, runner e2eexec.Runner, binary, root string, cat []scenario.Scenario) error {
	result := runner.Run(ctx, e2eexec.Command{
		Name: binary,
		Args: []string{"-test.list", "."},
		Dir:  root,
	})
	if result.ExitCode != 0 {
		return fmt.Errorf("list static tests: %w\n%s%s", errFromResult(result), result.Stdout, result.Stderr)
	}
	return ValidateStaticTestNames(cat, strings.Fields(string(result.Stdout)))
}

// ValidateStaticTestNames rejects catalog entries without a native test and
// native static tests that no catalog selector can reach.
func ValidateStaticTestNames(cat []scenario.Scenario, listed []string) error {
	want := map[string]struct{}{}
	for _, entry := range cat {
		if entry.Suite != scenario.SuiteStatic {
			continue
		}
		for _, name := range strings.Split(entry.ResultName, ",") {
			if name = strings.TrimSpace(name); name != "" {
				want[name] = struct{}{}
			}
		}
	}
	have := map[string]struct{}{}
	for _, name := range listed {
		if strings.HasPrefix(name, "Test") {
			have[name] = struct{}{}
		}
	}
	missing := setDifference(want, have)
	unreachable := setDifference(have, want)
	if len(missing) == 0 && len(unreachable) == 0 {
		return nil
	}
	parts := make([]string, 0, 2)
	if len(missing) != 0 {
		parts = append(parts, "catalog tests missing from static binary: "+strings.Join(missing, ", "))
	}
	if len(unreachable) != 0 {
		parts = append(parts, "static tests missing from catalog: "+strings.Join(unreachable, ", "))
	}
	return errors.New(strings.Join(parts, "; "))
}

// setDifference returns sorted values present in left but missing from right.
// Static catalog validation uses it to make drift errors stable and readable.
func setDifference(left, right map[string]struct{}) []string {
	values := make([]string, 0)
	for value := range left {
		if _, ok := right[value]; !ok {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
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
