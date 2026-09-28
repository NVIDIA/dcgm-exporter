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
	"os"
	"path/filepath"
	"testing"
	"time"

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
	def, ok := DefinitionFor(scenario.SuiteK8s)
	if !ok {
		t.Fatal("k8s definition not found")
	}
	if def.Package != "./tests/k8s" || def.Tags != "e2e" || def.BinaryName != "dcgm-exporter-k8s.test" {
		t.Fatalf("unexpected k8s definition: %#v", def)
	}
}

func TestDefinitionForAllCatalogSuites(t *testing.T) {
	for _, suiteName := range scenario.AllSuites() {
		def, ok := DefinitionFor(suiteName)
		if !ok {
			t.Fatalf("DefinitionFor(%q) ok = false", suiteName)
		}
		if def.Suite != suiteName {
			t.Fatalf("DefinitionFor(%q) suite = %q", suiteName, def.Suite)
		}
		if def.Package != "./tests/"+string(suiteName) {
			t.Fatalf("DefinitionFor(%q) package = %q", suiteName, def.Package)
		}
		if def.BinaryName != "dcgm-exporter-"+string(suiteName)+".test" {
			t.Fatalf("DefinitionFor(%q) binary = %q", suiteName, def.BinaryName)
		}
	}
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
	manager := NewManager(root, runner)
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
	got, err := NewManager(root, runner).Ensure(context.Background(), scenario.SuiteK8s, false)
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

func TestEnsureDCGMProbeBuildsSourceBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	manager := NewManager(root, runner)
	path, err := manager.EnsureDCGMProbeBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, ".e2e-tools", "e2e-dcgm-probe") {
		t.Fatalf("path = %q", path)
	}
	if len(runner.commands) != 1 || runner.commands[0].Name != "go" {
		t.Fatalf("commands = %#v", runner.commands)
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
	got, err := NewManager(root, &recordingRunner{}).EnsureDCGMProbeBinary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("path = %q, want %q", got, path)
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

	def, _ := DefinitionFor(scenario.SuiteHost)
	stale, err := NewManager(root, &recordingRunner{}).Stale(def, binary)
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

	def, _ := DefinitionFor(scenario.SuiteHost)
	stale, err := NewManager(root, &recordingRunner{}).Stale(def, binary)
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

	def, _ := DefinitionFor(scenario.SuiteK8s)
	stale, err := NewManager(root, &recordingRunner{}).Stale(def, binary)
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
