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

package suite

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

func writeExecutableFile(path string) error {
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func TestDefinitionFor(t *testing.T) {
	def, ok := TestBinaryDefinitionFor(scenario.SuiteK8s)
	if !ok {
		t.Fatal("k8s definition not found")
	}
	if def.Package != "./tests/k8s" || def.Tags != "k8s" || def.BinaryName != "dcgm-exporter-k8s.test" {
		t.Fatalf("unexpected k8s definition: %#v", def)
	}
}

func TestDefinitionForAllCatalogSuites(t *testing.T) {
	for _, suiteName := range scenario.AllSuites() {
		def, ok := TestBinaryDefinitionFor(suiteName)
		if !ok {
			t.Fatalf("TestBinaryDefinitionFor(%q) ok = false", suiteName)
		}
		if def.Suite != suiteName {
			t.Fatalf("TestBinaryDefinitionFor(%q) suite = %q", suiteName, def.Suite)
		}
		if def.Package != "./tests/"+string(suiteName) {
			t.Fatalf("TestBinaryDefinitionFor(%q) package = %q", suiteName, def.Package)
		}
		if def.BinaryName != "dcgm-exporter-"+string(suiteName)+".test" {
			t.Fatalf("TestBinaryDefinitionFor(%q) binary = %q", suiteName, def.BinaryName)
		}
	}
}

// TestCatalogScenariosHaveDirectGoExecutionMetadata verifies that every CLI
// scenario can be translated into a package-level Go test invocation.
func TestCatalogScenariosHaveDirectGoExecutionMetadata(t *testing.T) {
	for _, entry := range scenario.Catalog {
		definition, ok := TestBinaryDefinitionFor(entry.Suite)
		if !ok {
			t.Fatalf("TestBinaryDefinitionFor(%q) ok = false", entry.Suite)
		}
		if definition.Package == "" || definition.BinaryName == "" {
			t.Fatalf("scenario %q has incomplete suite definition: %#v", entry.Selector(), definition)
		}
		command, err := DirectGoCommand(entry)
		if err != nil {
			t.Fatalf("DirectGoCommand(%q) error = %v", entry.Selector(), err)
		}
		if command.Name != "go" || !containsArg(command.Args, definition.Package) {
			t.Fatalf("scenario %q direct command = %#v", entry.Selector(), command)
		}
	}
}

// TestDirectGoCommandUsesSuiteMetadata verifies representative commands for every suite type.
func TestDirectGoCommandUsesSuiteMetadata(t *testing.T) {
	tests := []struct {
		selector string
		want     string
	}{
		{selector: "static/chartRbacRendering", want: "go test ./tests/static -run ^(TestChartKubernetesRBACRenderContract|TestChartKubernetesAccessFollowsTheConfig|TestChartDRAEnrichmentDefaultsToAuto)$"},
		{selector: "host/startupMetrics", want: "go test --tags=dcgm_uri_integration ./tests/host -run ^TestHostIntegration$ -args --ginkgo.fail-on-empty --ginkgo.label-filter=startupMetrics"},
		{selector: "host/dcgmUri", want: "go test --tags=dcgm_uri_integration ./tests/host -run ^TestHostIntegration$ -args --ginkgo.fail-on-empty --ginkgo.label-filter=dcgmUri"},
		{selector: "host/dcgmUriUnix", want: "go test --tags=dcgm_uri_integration ./tests/host -run ^TestHostIntegration$ -args --ginkgo.fail-on-empty --ginkgo.label-filter=dcgmUriUnix"},
		{selector: "host/dcgmUriVSOCK", want: "go test --tags=dcgm_uri_integration ./tests/host -run ^TestHostIntegration$ -args --ginkgo.fail-on-empty --ginkgo.label-filter=dcgmUriVSOCK"},
		{selector: "container/imageStartup", want: "go test --tags=container ./tests/container -run ^TestDockerImages$ -args --ginkgo.fail-on-empty --ginkgo.label-filter=imageStartup"},
		{selector: "k8s/default", want: "go test --tags=k8s ./tests/k8s -run ^TestKubernetesIntegration$ -args --ginkgo.fail-on-empty --ginkgo.label-filter=default"},
	}
	for _, test := range tests {
		t.Run(test.selector, func(t *testing.T) {
			suiteName, scenarioName, _ := strings.Cut(test.selector, "/")
			entry, ok := scenario.Find(scenario.Catalog, scenario.Suite(suiteName), scenarioName)
			if !ok {
				t.Fatalf("catalog scenario %q is missing", test.selector)
			}
			command, err := DirectGoCommand(entry)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(append([]string{command.Name}, command.Args...), " ")
			if got != test.want {
				t.Fatalf("DirectGoCommand() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDirectGoCommandRejectsIncompleteMetadata verifies catalog drift fails closed.
func TestDirectGoCommandRejectsIncompleteMetadata(t *testing.T) {
	tests := []scenario.Scenario{
		{Suite: scenario.Suite("unknown"), Name: "missing-suite", ResultName: "test"},
		{Suite: scenario.SuiteStatic, Name: "missing-result"},
	}
	for _, entry := range tests {
		if _, err := DirectGoCommand(entry); err == nil {
			t.Fatalf("DirectGoCommand(%#v) error = nil", entry)
		}
	}
}

// TestValidateStaticTestNames verifies catalog checks report both missing and
// unreachable static tests instead of silently accepting selector drift.
func TestValidateStaticTestNames(t *testing.T) {
	cat := []scenario.Scenario{
		{Suite: scenario.SuiteStatic, Name: "one", ResultName: "TestOne"},
		{Suite: scenario.SuiteStatic, Name: "two", ResultName: "TestTwo,TestThree"},
	}
	if err := ValidateStaticTestNames(cat, []string{"TestOne", "TestTwo", "TestThree"}); err != nil {
		t.Fatalf("ValidateStaticTestNames() error = %v", err)
	}
	err := ValidateStaticTestNames(cat, []string{"TestOne", "TestFour"})
	if err == nil {
		t.Fatal("ValidateStaticTestNames() error = nil, want catalog mismatch")
	}
	for _, want := range []string{"TestTwo", "TestThree", "TestFour"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateStaticTestNames() error = %v, want %q", err, want)
		}
	}
}

// containsArg reports whether args contains the exact value.
func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func TestEnsureBuildsStaleBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tests", "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tests", "static", "static_test.go"), []byte("package static\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	manager := NewTestBinaryManager(root, runner)
	path, err := manager.Ensure(context.Background(), scenario.SuiteStatic, false)
	if err != nil {
		t.Fatal(err)
	}

	if path != filepath.Join(root, ".e2e-tools", "dcgm-exporter-static.test") {
		t.Fatalf("path = %q", path)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	wantArgs := "test -c -o " + path + " ./tests/static"
	if gotArgs := joinCommand(runner.commands[0]); gotArgs != "go "+wantArgs {
		t.Fatalf("command = %q, want %q", gotArgs, "go "+wantArgs)
	}
}

func TestEnsureUsesPackagedBinaryWithoutSource(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "dcgm-exporter-k8s.test")
	if err := writeExecutableFile(binary); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	got, err := NewTestBinaryManager(root, runner).Ensure(context.Background(), scenario.SuiteK8s, false)
	if err != nil {
		t.Fatal(err)
	}

	if got != binary {
		t.Fatalf("path = %q, want %q", got, binary)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %#v, want none", runner.commands)
	}
}

func TestEnsurePrebuiltUsesToolsDirWithoutCompiling(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolsDir := filepath.Join(root, "bundle", "bin")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(toolsDir, "dcgm-exporter-static.test")
	if err := writeExecutableFile(binary); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	manager := NewManager(root, runner)
	manager.ToolsDir = toolsDir
	manager.PrebuiltBinaries = true
	got, err := manager.Ensure(context.Background(), scenario.SuiteStatic, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != binary {
		t.Fatalf("path = %q, want %q", got, binary)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %#v, want none", runner.commands)
	}
}

func TestEnsurePrebuiltRejectsMissingAndNonExecutableBinaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolsDir := filepath.Join(root, "bundle", "bin")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		mode fs.FileMode
		want string
	}{
		{name: "missing", want: "is missing"},
		{name: "not executable", mode: 0o600, want: "is not executable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(toolsDir, "dcgm-exporter-host.test")
			if test.mode != 0 {
				if err := os.WriteFile(binary, []byte("binary"), test.mode); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(binary) })
			}
			runner := &recordingRunner{}
			manager := NewManager(root, runner)
			manager.ToolsDir = toolsDir
			manager.PrebuiltBinaries = true
			if _, err := manager.Ensure(context.Background(), scenario.SuiteHost, false); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Ensure() error = %v, want %q", err, test.want)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("commands = %#v, want none", runner.commands)
			}
		})
	}
}

func TestEnsureDCGMProbeBuildsSourceBinary(t *testing.T) {
	for _, changed := range []string{"", "internal/pkg/collector/expcollector.go", "pkg/cmd/app.go"} {
		t.Run("cached/"+changed, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600))
			runner := &recordingRunner{}
			manager := NewTestBinaryManager(root, runner)
			cached := manager.DCGMProbeBinaryPath()
			require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
			require.NoError(t, writeExecutableFile(cached))
			future := time.Now().Add(time.Hour)
			require.NoError(t, os.Chtimes(cached, future, future))
			if changed != "" {
				path := filepath.Join(root, changed)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte("package fixture\n"), 0o600))
			}
			path, err := manager.EnsureDCGMProbeBinary(context.Background())
			require.NoError(t, err)
			require.Equal(t, cached, path)
			require.Len(t, runner.commands, 1)
			require.Equal(t, "go", runner.commands[0].Name)
			require.Equal(t, []string{"build", "-buildvcs=false", "-trimpath", "-o", cached, "./tests/cmd/e2e-dcgm-probe"}, runner.commands[0].Args)
			require.Contains(t, runner.commands[0].Env, "CGO_ENABLED=1")
		})
	}
}

func TestEnsureDCGMProbeUsesPackagedBinary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bin", "e2e-dcgm-probe")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := NewTestBinaryManager(root, &recordingRunner{}).EnsureDCGMProbeBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
	}
}

func TestEnsureDCGMProbePrebuiltNeverCompiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolsDir := filepath.Join(root, "bundle", "bin")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(toolsDir, "e2e-dcgm-probe")
	if err := writeExecutableFile(probe); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	manager := NewManager(root, runner)
	manager.ToolsDir = toolsDir
	manager.PrebuiltBinaries = true
	got, err := manager.EnsureDCGMProbeBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != probe {
		t.Fatalf("path = %q, want %q", got, probe)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %#v, want none", runner.commands)
	}
}

// TestEnsureProductBinaryUsesExplicitPath resolves relative and absolute supplied artifacts without running a build.
func TestEnsureProductBinaryUsesExplicitPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("bundle", "bin", "dcgm-exporter")
	absolute := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(absolute); err != nil {
		t.Fatal(err)
	}

	for _, configured := range []string{relative, absolute} {
		runner := &recordingRunner{}
		manager := NewManager(root, runner)
		manager.ExporterBinary = configured
		got, err := manager.EnsureProductBinary(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got != absolute {
			t.Fatalf("path = %q, want %q", got, absolute)
		}
		if len(runner.commands) != 0 {
			t.Fatalf("commands = %#v, want none for supplied artifact", runner.commands)
		}
	}
}

// TestEnsureProductBinaryPrebuiltSourceTreeNeverCompiles reports a missing prebuilt artifact without compiling source.
func TestEnsureProductBinaryPrebuiltSourceTreeNeverCompiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	manager := NewTestBinaryManager(root, runner)
	manager.PrebuiltBinaries = true
	if _, err := manager.EnsureProductBinary(context.Background()); err == nil {
		t.Fatal("EnsureProductBinary() error = nil, want missing prebuilt binary")
	}
	if len(runner.commands) != 0 {
		t.Fatalf("prebuilt mode ran %#v, want no build", runner.commands)
	}
}

// TestEnsureProductBinaryBuildsSourceArtifact builds when the artifact is missing or older than the source tree.
func TestEnsureProductBinaryBuildsSourceArtifact(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "missing artifact"
		if existing {
			name = "old artifact"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(root, "bin", "dcgm-exporter")
			if existing {
				writeStaleExecutable(t, binary)
			}

			runner := &recordingRunner{}
			got, err := NewTestBinaryManager(root, runner).EnsureProductBinary(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got != binary {
				t.Fatalf("path = %q, want %q", got, binary)
			}
			requireSourceBuild(t, runner.commands, root, binary)
		})
	}
}

// writeStaleExecutable creates an executable at path with an hour-old modification time.
func writeStaleExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(path); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// requireSourceBuild checks that exactly one CGO go build produced binary from root.
func requireSourceBuild(t *testing.T, commands []e2eexec.Command, root, binary string) {
	t.Helper()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v, want one source build", commands)
	}
	command := commands[0]
	if command.Name != "go" || strings.Join(command.Args, " ") != "build -buildvcs=false -trimpath -o "+binary+" ./cmd/dcgm-exporter" {
		t.Fatalf("build command = %#v", command)
	}
	if command.Dir != root || strings.Join(command.Env, " ") != "CGO_ENABLED=1" {
		t.Fatalf("build environment = %#v, want root %q with CGO_ENABLED=1", command, root)
	}
	if err := requireExecutable(binary, "dcgm-exporter binary"); err != nil {
		t.Fatalf("source build did not produce an executable artifact: %v", err)
	}
}

// TestEnsureProductBinaryUsesDefaultPackageArtifact reuses the package-provided executable without a source build.
func TestEnsureProductBinaryUsesDefaultPackageArtifact(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "bin", "dcgm-exporter")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(binary); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	got, err := NewTestBinaryManager(root, runner).EnsureProductBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != binary {
		t.Fatalf("path = %q, want %q", got, binary)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %#v, want none for package artifact", runner.commands)
	}
}

// TestEnsureProductBinaryUsesSuppliedPackageArtifact resolves the configured package binary without a source build.
func TestEnsureProductBinaryUsesSuppliedPackageArtifact(t *testing.T) {
	root := t.TempDir()
	relative := filepath.Join("bundle", "bin", "dcgm-exporter")
	absolute := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(absolute); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	manager := NewTestBinaryManager(root, runner)
	manager.ExporterBinary = relative
	got, err := manager.EnsureProductBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != absolute {
		t.Fatalf("path = %q, want %q", got, absolute)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %#v, want none for supplied package artifact", runner.commands)
	}
}

func TestEnsureProductBinaryRejectsMissingAndNonExecutablePaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dcgm-exporter")
	manager := NewManager(root, &recordingRunner{})
	manager.ExporterBinary = path
	if _, err := manager.EnsureProductBinary(context.Background()); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("EnsureProductBinary() error = %v, want missing", err)
	}
	if err := os.WriteFile(path, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureProductBinary(context.Background()); err == nil || !strings.Contains(err.Error(), "is not executable") {
		t.Fatalf("EnsureProductBinary() error = %v, want non-executable", err)
	}
}

func TestEnsureProductBinaryRejectsDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dcgm-exporter")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, &recordingRunner{})
	manager.ExporterBinary = path
	if _, err := manager.EnsureProductBinary(context.Background()); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("EnsureProductBinary() error = %v, want non-regular file", err)
	}
}

func TestStaleDetectsNewerSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "tests", "host")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "host.test")
	if err := writeExecutableFile(binary); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(binary, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "host_test.go"), []byte("package host\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	def, _ := TestBinaryDefinitionFor(scenario.SuiteHost)
	stale, err := NewTestBinaryManager(root, &recordingRunner{}).Stale(def, binary)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("expected stale binary")
	}
}

func TestStaleDetectsDeletedSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "tests", "host")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "deleted_test.go")
	if err := os.WriteFile(source, []byte("package host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "host.test")
	if err := writeExecutableFile(binary); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	middle := time.Now().Add(-time.Hour)
	newer := time.Now()
	if err := os.Chtimes(source, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sourceDir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(binary, middle, middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sourceDir, newer, newer); err != nil {
		t.Fatal(err)
	}

	def, _ := TestBinaryDefinitionFor(scenario.SuiteHost)
	stale, err := NewTestBinaryManager(root, &recordingRunner{}).Stale(def, binary)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("expected stale binary after source deletion")
	}
}

func TestStaleDetectsDeletedNestedSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "tests", "k8s", "internal", "framework")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "deleted_test.go")
	if err := os.WriteFile(source, []byte("package framework\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "k8s.test")
	if err := writeExecutableFile(binary); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	middle := time.Now().Add(-time.Hour)
	newer := time.Now()
	for _, path := range []string{
		source,
		filepath.Join(root, "tests", "k8s"),
		filepath.Join(root, "tests", "k8s", "internal"),
		sourceDir,
	} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(binary, middle, middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sourceDir, newer, newer); err != nil {
		t.Fatal(err)
	}

	def, _ := TestBinaryDefinitionFor(scenario.SuiteK8s)
	stale, err := NewTestBinaryManager(root, &recordingRunner{}).Stale(def, binary)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatal("expected stale binary after nested source deletion")
	}
}

func TestFilters(t *testing.T) {
	selected := []scenario.Scenario{
		{ResultName: "One,Two"},
		{ResultName: "Three"},
	}
	if got := StaticRegex(selected); got != "One|Two|Three" {
		t.Fatalf("StaticRegex() = %q", got)
	}
	if got := LabelFilter(selected); got != "One || Two || Three" {
		t.Fatalf("LabelFilter() = %q", got)
	}
}

type recordingRunner struct {
	commands []e2eexec.Command
}

func (r *recordingRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	r.commands = append(r.commands, command)
	if command.Name == "go" {
		for index, arg := range command.Args {
			if arg == "-o" && index+1 < len(command.Args) {
				if err := writeExecutableFile(command.Args[index+1]); err != nil {
					return e2eexec.Result{ExitCode: 1, Err: err}
				}
			}
		}
	}
	return e2eexec.Result{}
}

func joinCommand(command e2eexec.Command) string {
	out := command.Name
	for _, arg := range command.Args {
		out += " " + arg
	}
	return out
}
