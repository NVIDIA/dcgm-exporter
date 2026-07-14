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

package installdeps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestDownloadsMissingK8sTools(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	arch, err := helperToolArch()
	if err != nil {
		t.Fatal(err)
	}
	downloads := installDepsDownloadBytes(arch)
	deps := writeInstallDepsVersions(t, root, arch, downloads)

	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c command -v k3d":     {ExitCode: 1},
		"sh -c command -v kubectl": {ExitCode: 1},
		"sh -c command -v helm":    {ExitCode: 1},
	}, downloads: downloads}
	opts := config.Tests{InstallDeps: true, Suites: []string{"k8s"}, ContainerToolkitTestImage: "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04", Dependencies: deps}
	restore, err := ConfigureToolPath(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()

	var stdout bytes.Buffer
	if err := IfRequested(context.Background(), &stdout, root, runner, &opts); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"github.com/k3d-io/k3d/releases/download/v5.8.3",
		"dl.k8s.io/release/v1.35.3/bin/linux/",
		"get.helm.sh/helm-v4.1.0-linux-",
	} {
		if !runner.hasCommandArgContaining(want) {
			t.Fatalf("missing download containing %q; commands = %#v", want, runner.commands)
		}
	}
	if !strings.Contains(stdout.String(), "Installing k3d 5.8.3") {
		t.Fatalf("install log missing k3d install:\n%s", stdout.String())
	}
}

func TestEnsureHostDCGMSkipsMatchingPinnedVersion(t *testing.T) {
	version := "1:4.5.3-1"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		commandKey(dcgmPackageQueryCommand(false)): {
			Stdout: []byte("datacenter-gpu-manager-4-core=" + version + "\ndatacenter-gpu-manager-4-proprietary=" + version + "\n"),
		},
	}}
	var stdout bytes.Buffer
	if err := ensureHostDCGM(context.Background(), &stdout, runner, config.Tests{InstallDeps: true, Dependencies: config.Dependencies{DCGMVersion: "4.5.3"}}); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandArg("install") && runner.hasCommandArg("datacenter-gpu-manager-4-core="+version) {
		t.Fatalf("matching DCGM version should not reinstall; commands = %#v", runner.commands)
	}
}

func TestEnsureHostDCGMInstallsPinnedVersionOnMismatch(t *testing.T) {
	version := "1:4.5.3-1"
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		commandKey(dcgmPackageQueryCommand(false)): {
			{Stdout: []byte("datacenter-gpu-manager-4-core=1:4.4.1-1\ndatacenter-gpu-manager-4-proprietary=1:4.4.1-1\n")},
			{Stdout: []byte("datacenter-gpu-manager-4-core=" + version + "\ndatacenter-gpu-manager-4-proprietary=" + version + "\n")},
		},
	}}
	var stdout bytes.Buffer
	if err := ensureHostDCGM(context.Background(), &stdout, runner, config.Tests{InstallDeps: true, Dependencies: config.Dependencies{DCGMVersion: "4.5.3"}}); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("datacenter-gpu-manager-4-core="+version) ||
		!runner.hasCommandArg("datacenter-gpu-manager-4-proprietary="+version) {
		t.Fatalf("stale DCGM version did not install pinned packages; commands = %#v", runner.commands)
	}
}

func TestEnsureHostDCGMInstallsDevPackageForNVMLInjection(t *testing.T) {
	version := "1:4.5.3-1"
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		commandKey(dcgmPackageQueryCommand(true)): {
			{Stdout: []byte("datacenter-gpu-manager-4-core=" + version + "\ndatacenter-gpu-manager-4-proprietary=" + version + "\n")},
			{Stdout: []byte("datacenter-gpu-manager-4-core=" + version + "\ndatacenter-gpu-manager-4-proprietary=" + version + "\ndatacenter-gpu-manager-4-dev=" + version + "\n")},
		},
	}}
	var stdout bytes.Buffer
	opts := config.Tests{
		InstallDeps:           true,
		DCGMNVMLInjectionYAML: "fixture.yaml",
		Dependencies:          config.Dependencies{DCGMVersion: "4.5.3"},
	}
	if err := ensureHostDCGM(context.Background(), &stdout, runner, opts); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("datacenter-gpu-manager-4-dev=" + version) {
		t.Fatalf("NVML injection did not install pinned dev package; commands = %#v", runner.commands)
	}
}

func TestFailsWhenChecksumMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("K3D_VERSION=5.8.3\nK3S_VERSION=v1.35.3-k3s1\nHELM_VERSION=4.1.0\n")
	if err := os.WriteFile(filepath.Join(root, "hack", "versions.env"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{results: map[string]e2eexec.Result{"sh -c command -v k3d": {ExitCode: 1}}}
	opts := config.Tests{InstallDeps: true, Suites: []string{"k8s"}, Dependencies: config.Dependencies{
		K3DVersion: "5.8.3", K3SVersion: "v1.35.3-k3s1", HelmVersion: "4.1.0",
	}}

	var stdout bytes.Buffer
	err := IfRequested(context.Background(), &stdout, root, runner, &opts)
	if err == nil || !strings.Contains(err.Error(), "K3D_LINUX_"+strings.ToUpper(runtime.GOARCH)+"_SHA256") {
		t.Fatalf("IfRequested() error = %v, want missing checksum", err)
	}
	if runner.hasCommandArgContaining("github.com/k3d-io/k3d/releases/download") {
		t.Fatalf("download ran despite missing checksum: %#v", runner.commands)
	}
}

func TestFailsOnChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	arch, err := helperToolArch()
	if err != nil {
		t.Fatal(err)
	}
	downloads := installDepsDownloadBytes(arch)
	bad := map[string][]byte{}
	for url, content := range downloads {
		bad[url] = content
	}
	deps := writeInstallDepsVersions(t, root, arch, map[string][]byte{
		installDepsURLK3D(arch):     []byte("not what curl writes"),
		installDepsURLKubectl(arch): downloads[installDepsURLKubectl(arch)],
		installDepsURLHelm(arch):    downloads[installDepsURLHelm(arch)],
	})
	runner := &fakeRunner{results: map[string]e2eexec.Result{"sh -c command -v k3d": {ExitCode: 1}}, downloads: bad}
	opts := config.Tests{InstallDeps: true, Suites: []string{"k8s"}, Dependencies: deps}

	var stdout bytes.Buffer
	err = IfRequested(context.Background(), &stdout, root, runner, &opts)
	if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("IfRequested() error = %v, want checksum failure", err)
	}
	if runner.hasCommandArg("+x") {
		t.Fatalf("chmod ran despite checksum failure: %#v", runner.commands)
	}
}

func installDepsDownloadBytes(arch string) map[string][]byte {
	return map[string][]byte{
		installDepsURLK3D(arch):     []byte("fake k3d"),
		installDepsURLKubectl(arch): []byte("fake kubectl"),
		installDepsURLHelm(arch):    []byte("fake helm"),
	}
}

func installDepsURLK3D(arch string) string {
	return "https://github.com/k3d-io/k3d/releases/download/v5.8.3/k3d-linux-" + arch
}

func installDepsURLKubectl(arch string) string {
	return "https://dl.k8s.io/release/v1.35.3/bin/linux/" + arch + "/kubectl"
}

func installDepsURLHelm(arch string) string {
	return "https://get.helm.sh/helm-v4.1.0-linux-" + arch + ".tar.gz"
}

func writeInstallDepsVersions(t *testing.T, root, arch string, downloads map[string][]byte) config.Dependencies {
	t.Helper()
	content := strings.Join([]string{
		"K3D_VERSION=5.8.3",
		"K3S_VERSION=v1.35.3-k3s1",
		"HELM_VERSION=4.1.0",
		"CUDA_BASE_TAG=13.2.1-base",
		"CUDA_UBUNTU_TAG=24.04",
		helperSHAKey("K3D", arch) + "=" + sha256Hex(downloads[installDepsURLK3D(arch)]),
		helperSHAKey("KUBECTL", arch) + "=" + sha256Hex(downloads[installDepsURLKubectl(arch)]),
		helperSHAKey("HELM", arch) + "=" + sha256Hex(downloads[installDepsURLHelm(arch)]),
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "hack", "versions.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := config.Dependencies{
		K3DVersion:  "5.8.3",
		K3SVersion:  "v1.35.3-k3s1",
		HelmVersion: "4.1.0",
	}
	switch arch {
	case "amd64":
		deps.K3DAMD64SHA256 = sha256Hex(downloads[installDepsURLK3D(arch)])
		deps.KubectlAMD64SHA256 = sha256Hex(downloads[installDepsURLKubectl(arch)])
		deps.HelmAMD64SHA256 = sha256Hex(downloads[installDepsURLHelm(arch)])
	case "arm64":
		deps.K3DARM64SHA256 = sha256Hex(downloads[installDepsURLK3D(arch)])
		deps.KubectlARM64SHA256 = sha256Hex(downloads[installDepsURLKubectl(arch)])
		deps.HelmARM64SHA256 = sha256Hex(downloads[installDepsURLHelm(arch)])
	}
	return deps
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type fakeRunner struct {
	results         map[string]e2eexec.Result
	resultSequences map[string][]e2eexec.Result
	downloads       map[string][]byte
	commands        []e2eexec.Command
}

func (f *fakeRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	f.commands = append(f.commands, command)
	key := commandKey(command)
	if f.resultSequences != nil {
		if results := f.resultSequences[key]; len(results) != 0 {
			result := results[0]
			f.resultSequences[key] = results[1:]
			return streamFakeResult(command, result)
		}
	}
	if f.results != nil {
		if result, ok := f.results[key]; ok {
			return streamFakeResult(command, result)
		}
		if result, ok := f.results[filepath.Base(command.Name)]; ok {
			return streamFakeResult(command, result)
		}
	}
	if command.Name == "curl" {
		if err := f.writeCurlOutput(command); err != nil {
			return e2eexec.Result{ExitCode: 1, Err: err}
		}
		return e2eexec.Result{}
	}
	return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

func commandKey(command e2eexec.Command) string {
	return strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
}

func streamFakeResult(command e2eexec.Command, result e2eexec.Result) e2eexec.Result {
	if command.Stdout != nil && len(result.Stdout) != 0 {
		_, _ = command.Stdout.Write(result.Stdout)
	}
	if command.Stderr != nil && len(result.Stderr) != 0 {
		_, _ = command.Stderr.Write(result.Stderr)
	}
	return result
}

func (f *fakeRunner) writeCurlOutput(command e2eexec.Command) error {
	var url, dest string
	for index, arg := range command.Args {
		if strings.HasPrefix(arg, "http") {
			url = arg
		}
		if arg == "-o" && index+1 < len(command.Args) {
			dest = command.Args[index+1]
		}
	}
	if dest == "" {
		return nil
	}
	content := f.downloads[url]
	if content == nil {
		content = []byte("download:" + url)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, content, 0o600)
}

func (f *fakeRunner) hasCommandArg(value string) bool {
	for _, command := range f.commands {
		if hasArg(command.Args, value) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) hasCommandArgContaining(value string) bool {
	for _, command := range f.commands {
		for _, arg := range command.Args {
			if strings.Contains(arg, value) {
				return true
			}
		}
	}
	return false
}

func hasArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}
