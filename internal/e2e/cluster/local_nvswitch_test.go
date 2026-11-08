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

package cluster

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestPrepareNVSwitchRuntimeGeneratesRebasedHostCDIAndRestoresLegacy verifies the
// complete temporary CDI lifecycle for every local GPU node.
func TestPrepareNVSwitchRuntimeGeneratesRebasedHostCDIAndRestoresLegacy(t *testing.T) {
	runner := &recordingRunner{outputs: map[string]string{
		"docker ps -a --format {{.Names}}": "k3d-dcgm-exporter-gpu-server-0\nk3d-dcgm-exporter-gpu-agent-0\n",
	}}
	local := defaultLocalConfigForTest(t)
	local.HostRootPath = writeNVSwitchLibrariesForTest(t)
	var out bytes.Buffer

	cleanup, err := PrepareNVSwitchRuntime(context.Background(), runner, &out, DefaultConfig(), local, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !runner.has("nvidia-ctk", "cdi", "generate", "--driver-root="+local.HostRootPath, "--dev-root=/dev") {
		t.Fatalf("fresh host CDI spec was not generated: %#v", runner.commands)
	}
	for _, node := range []string{"k3d-dcgm-exporter-gpu-server-0", "k3d-dcgm-exporter-gpu-agent-0"} {
		if !runner.has("docker", "exec", node, "cp", local.HostRootMount+"/tmp/") {
			t.Fatalf("generated CDI spec was not copied into %s: %#v", node, runner.commands)
		}
		if !runner.has(
			"docker",
			"exec",
			node,
			"sed",
			"-i",
			`/hostPath: \//s#hostPath: /#hostPath: /run/nvidia/host-driver/#`,
			localCDISpecPath,
		) {
			t.Fatalf("host CDI paths were not rebased in %s: %#v", node, runner.commands)
		}
		if !runner.has("docker", "exec", node, "sed", "-i", localRuntimeModeCDI, localRuntimeConfig) {
			t.Fatalf("CDI mode was not enabled in %s: %#v", node, runner.commands)
		}
		if !runner.has("docker", "exec", node, "sed", "-i", localRuntimeModeLegacy, localRuntimeConfig) {
			t.Fatalf("legacy mode was not restored in %s: %#v", node, runner.commands)
		}
		if !runner.has("docker", "exec", node, "rm", "-f", localCDISpecPath) {
			t.Fatalf("temporary CDI spec was not removed from %s: %#v", node, runner.commands)
		}
	}
	if !strings.Contains(out.String(), "enabled host CDI device edits on 2 local GPU nodes") {
		t.Fatalf("missing NVSwitch runtime summary:\n%s", out.String())
	}
}

// TestPrepareNVSwitchRuntimeRejectsClusterNameMismatch avoids changing the wrong k3d cluster.
func TestPrepareNVSwitchRuntimeRejectsClusterNameMismatch(t *testing.T) {
	runner := &recordingRunner{}
	cfg := DefaultConfig()
	local := defaultLocalConfigForTest(t)
	local.ClusterName = "different-cluster"

	_, err := PrepareNVSwitchRuntime(context.Background(), runner, io.Discard, cfg, local, true)
	if err == nil || !strings.Contains(err.Error(), "cluster name mismatch") {
		t.Fatalf("PrepareNVSwitchRuntime() error = %v, want cluster name mismatch", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("cluster name mismatch ran commands: %#v", runner.commands)
	}
}

// TestPrepareNVSwitchRuntimeReportsCleanupFailures verifies that a failed
// legacy-mode restore makes the group cleanup fail.
func TestPrepareNVSwitchRuntimeReportsCleanupFailures(t *testing.T) {
	runner := &recordingRunner{outputs: map[string]string{
		"docker ps -a --format {{.Names}}": "k3d-dcgm-exporter-gpu-agent-0\n",
	}}
	local := defaultLocalConfigForTest(t)
	local.HostRootPath = writeNVSwitchLibrariesForTest(t)

	cleanup, err := PrepareNVSwitchRuntime(context.Background(), runner, io.Discard, DefaultConfig(), local, true)
	if err != nil {
		t.Fatal(err)
	}
	runner.results = map[string]e2eexec.Result{
		"docker exec k3d-dcgm-exporter-gpu-agent-0 sed -i " + localRuntimeModeLegacy + " " + localRuntimeConfig: {
			ExitCode: 1,
			Stderr:   []byte("restore failed"),
		},
		"docker exec k3d-dcgm-exporter-gpu-agent-0 rm -f " + localCDISpecPath: {
			ExitCode: 1,
			Stderr:   []byte("remove failed"),
		},
	}

	err = cleanup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mode = \"legacy\"") || !strings.Contains(err.Error(), "rm -f") {
		t.Fatalf("cleanup() error = %v, want joined restore and removal failures", err)
	}
}

// TestPrepareNVSwitchRuntimeSkipsNVSDMLibrariesOnOtherHosts verifies that a host
// without NVSDM needs none of the NVSDM libraries.
func TestPrepareNVSwitchRuntimeSkipsNVSDMLibrariesOnOtherHosts(t *testing.T) {
	runner := &recordingRunner{outputs: map[string]string{
		"docker ps -a --format {{.Names}}": "k3d-dcgm-exporter-gpu-agent-0\n",
	}}
	local := defaultLocalConfigForTest(t)
	local.HostRootPath = t.TempDir()
	if err := os.Mkdir(filepath.Join(local.HostRootPath, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := PrepareNVSwitchRuntime(context.Background(), runner, io.Discard, DefaultConfig(), local, false); err != nil {
		t.Fatalf("PrepareNVSwitchRuntime() error = %v, want success without NVSDM libraries", err)
	}
}

// TestPrepareNVSwitchRuntimeRequiresHostLibraries verifies fail-fast behavior
// when the generated spec cannot include the NVSwitch runtime libraries.
func TestPrepareNVSwitchRuntimeRequiresHostLibraries(t *testing.T) {
	runner := &recordingRunner{}
	local := defaultLocalConfigForTest(t)
	local.HostRootPath = t.TempDir()
	if err := os.Mkdir(filepath.Join(local.HostRootPath, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := PrepareNVSwitchRuntime(context.Background(), runner, io.Discard, DefaultConfig(), local, true)
	if err == nil || !strings.Contains(err.Error(), "required NVSwitch library libnvsdm.so.1 was not found") {
		t.Fatalf("PrepareNVSwitchRuntime() error = %v, want missing library", err)
	}
	if runner.has("docker", "exec") {
		t.Fatalf("cluster nodes were changed without required libraries: %#v", runner.commands)
	}
}

// TestAddNVSDMLibrariesPreservesGeneratedSpec verifies the focused CDI edit.
func TestAddNVSDMLibrariesPreservesGeneratedSpec(t *testing.T) {
	root := writeNVSwitchLibrariesForTest(t)
	specPath := filepath.Join(t.TempDir(), "nvidia.yaml")
	input := "kind: nvidia.com/gpu\ndevices:\n  - name: all\ncontainerEdits:\n  env:\n    - NVIDIA_VISIBLE_DEVICES=void\n"
	if err := os.WriteFile(specPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addNVSDMLibraries(specPath, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, want := range []string{"name: all", "NVIDIA_VISIBLE_DEVICES=void", "libnvsdm.so.1", "libibmad.so.5", "libibumad.so.3", "libmnl.so.0"} {
		if !strings.Contains(output, want) {
			t.Errorf("updated CDI spec missing %q:\n%s", want, output)
		}
	}
}

// TestAddNVSDMLibrariesSupportsRHELLayout verifies standard lib64 paths.
func TestAddNVSDMLibrariesSupportsRHELLayout(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "usr", "lib64")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"libnvsdm.so.1", "libibmad.so.5", "libibumad.so.3", "libmnl.so.0"} {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	specPath := filepath.Join(t.TempDir(), "nvidia.yaml")
	if err := os.WriteFile(specPath, []byte("kind: nvidia.com/gpu\ncontainerEdits: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := addNVSDMLibraries(specPath, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/usr/lib64/libnvsdm.so.1") {
		t.Fatalf("updated CDI spec does not use the RHEL lib64 path:\n%s", data)
	}
}

// TestFindNVSwitchLibraryRejectsDistinctMatches verifies fail-closed lookup.
func TestFindNVSwitchLibraryRejectsDistinctMatches(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"usr/lib/x86_64-linux-gnu", "usr/lib/aarch64-linux-gnu", "usr/lib64"} {
		path := filepath.Join(root, directory)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "libnvsdm.so.1"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err := findNVSwitchLibrary(root, "libnvsdm.so.1")
	if err == nil || !strings.Contains(err.Error(), "multiple distinct matches") {
		t.Fatalf("findNVSwitchLibrary() error = %v, want ambiguous match", err)
	}
}

// writeNVSwitchLibrariesForTest creates the library layout used by CDI tests. It uses usr/lib64,
// which findNVSwitchLibrary searches on every supported architecture.
func writeNVSwitchLibrariesForTest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "usr", "lib64")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"libnvsdm.so.1", "libibmad.so.5", "libibumad.so.3", "libmnl.so.0"} {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
