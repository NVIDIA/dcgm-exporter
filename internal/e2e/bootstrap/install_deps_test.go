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

package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// TestBootstrapFailureDoesNotEmitResultMarker verifies that dependency
// setup failures cannot be mistaken for completed test scenarios by a result parser.
func TestBootstrapFailureDoesNotEmitResultMarker(t *testing.T) {
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall, Suites: []string{"host"}}
	var stdout bytes.Buffer

	_, err := RunIfEnabled(context.Background(), &stdout, t.TempDir(), &fakeRunner{}, &opts, requirementsForTest(opts))
	if err == nil || !strings.Contains(err.Error(), "DCGM_VERSION is required") {
		t.Fatalf("RunIfEnabled() error = %v, want missing DCGM version", err)
	}
	if strings.Contains(stdout.String(), "&&&&") {
		t.Fatalf("bootstrap failure emitted a result marker:\n%s", stdout.String())
	}
}

// TestRunIfEnabledHonorsNonExecutingModes verifies skip and build-only return before planning.
func TestRunIfEnabledHonorsNonExecutingModes(t *testing.T) {
	for _, opts := range []config.TestOptions{
		{BootstrapMode: config.BootstrapSkip},
		{BootstrapMode: config.BootstrapInstall, BuildOnly: true},
	} {
		runner := &fakeRunner{}
		if _, err := RunIfEnabled(context.Background(), io.Discard, t.TempDir(), runner, &opts, requirementsForTest(opts)); err != nil {
			t.Fatal(err)
		}
		if len(runner.commands) != 0 {
			t.Fatalf("non-executing bootstrap mode ran commands: %#v", runner.commands)
		}
	}
}

// TestRunIfEnabledChecksAnEmptyPlan verifies check mode can finish without changing the host.
func TestRunIfEnabledChecksAnEmptyPlan(t *testing.T) {
	opts := config.TestOptions{BootstrapMode: config.BootstrapCheck, Suites: []string{"static"}}
	runner := &fakeRunner{}
	if _, err := RunIfEnabled(context.Background(), io.Discard, t.TempDir(), runner, &opts, requirementsForTest(opts)); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("empty bootstrap plan ran commands: %#v", runner.commands)
	}
}

// TestCheckGPUHealthRecordsDriverVersion verifies that the early driver check writes the reported version.
func TestCheckGPUHealthRecordsDriverVersion(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"nvidia-smi --query-gpu=driver_version --format=csv,noheader": {Stdout: []byte("615.72\n")},
	}}
	var output bytes.Buffer
	if err := checkGPUHealth(context.Background(), &output, runner); err != nil {
		t.Fatal(err)
	}
	if output.String() != "[e2e] bootstrap: gpu_driver=615.72\n" {
		t.Fatalf("checkGPUHealth() output = %q", output.String())
	}
}

// TestGPUContainerRuntimeReusesLocalSmokeImage verifies a local smoke image is not pulled again.
func TestGPUContainerRuntimeReusesLocalSmokeImage(t *testing.T) {
	image := "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04"
	runner := &fakeRunner{}
	opts := config.TestOptions{ContainerToolkitTestImage: image}
	if err := checkGPUContainerRuntime(context.Background(), io.Discard, runner, opts, false); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandArg("pull") {
		t.Fatalf("local smoke image triggered a pull: %#v", runner.commands)
	}
	if !runner.hasCommandArg("--pull=never") {
		t.Fatalf("smoke run did not disable implicit pulls: %#v", runner.commands)
	}
}

// TestGPUContainerRuntimePullFailureDoesNotRepairRuntime verifies image access fails before runtime repair.
func TestGPUContainerRuntimePullFailureDoesNotRepairRuntime(t *testing.T) {
	image := "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + image: {ExitCode: 1},
		"docker pull " + image:          {ExitCode: 1, Stderr: []byte("denied: Access Denied")},
	}}
	opts := config.TestOptions{ContainerToolkitTestImage: image}
	err := checkGPUContainerRuntime(context.Background(), io.Discard, runner, opts, true)
	if err == nil || !strings.Contains(err.Error(), "docker pull failed") || !strings.Contains(err.Error(), "Access Denied") {
		t.Fatalf("checkGPUContainerRuntime() error = %v, want image acquisition failure", err)
	}
	if runner.hasCommandName("nvidia-ctk") || runner.hasCommandArg("restart") || runner.hasCommandArg("--gpus") {
		t.Fatalf("pull failure triggered runtime work: %#v", runner.commands)
	}
}

// TestGPUContainerRuntimeReportsRepairFailure verifies repair errors retain the first runtime error.
func TestGPUContainerRuntimeReportsRepairFailure(t *testing.T) {
	image := "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker run --pull=never --rm --gpus all " + image + " nvidia-smi":                {ExitCode: 1, Stderr: []byte("runtime unavailable")},
		commandKey(sudoCommand("nvidia-ctk", "runtime", "configure", "--runtime=docker")): {ExitCode: 1, Stderr: []byte("invalid runtime config")},
	}}
	opts := config.TestOptions{ContainerToolkitTestImage: image}
	err := checkGPUContainerRuntime(context.Background(), io.Discard, runner, opts, true)
	if err == nil || !strings.Contains(err.Error(), "repair NVIDIA Container Toolkit") || !strings.Contains(err.Error(), "invalid runtime config") {
		t.Fatalf("checkGPUContainerRuntime() error = %v, want repair failure", err)
	}
	if runner.hasCommandArg("restart") {
		t.Fatalf("failed runtime configuration still restarted Docker: %#v", runner.commands)
	}
}

// TestGPUContainerRuntimeKeepsInitialAndRetryFailures verifies GPU smoke, repair, and retry errors are all returned.
func TestGPUContainerRuntimeKeepsInitialAndRetryFailures(t *testing.T) {
	image := "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04"
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		"docker run --pull=never --rm --gpus all " + image + " nvidia-smi": {
			{ExitCode: 1, Stderr: []byte("initial GPU runtime failure")},
			{ExitCode: 1, Stderr: []byte("retry GPU runtime failure")},
		},
	}}
	opts := config.TestOptions{ContainerToolkitTestImage: image}
	err := checkGPUContainerRuntime(context.Background(), io.Discard, runner, opts, true)
	if err == nil || !strings.Contains(err.Error(), "initial GPU runtime failure") || !strings.Contains(err.Error(), "retry GPU runtime failure") {
		t.Fatalf("checkGPUContainerRuntime() error = %v, want both smoke failures", err)
	}
	if !runner.hasCommandName("nvidia-ctk") || !runner.hasCommandArg("restart") {
		t.Fatalf("GPU runtime repair did not run once: %#v", runner.commands)
	}
	for _, command := range runner.commands {
		if command.Name == "nvidia-installer" || hasArg(command.Args, "driver") || hasArg(command.Args, "reboot") {
			t.Fatalf("GPU runtime repair touched the driver: %#v", runner.commands)
		}
	}
}

// TestGPUContainerRuntimeRestoresSocketAccessAfterRestart verifies that a
// Docker restart restores current-user socket access before the GPU smoke retry.
func TestGPUContainerRuntimeRestoresSocketAccessAfterRestart(t *testing.T) {
	image := "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04"
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		"docker run --pull=never --rm --gpus all " + image + " nvidia-smi": {
			{ExitCode: 1, Stderr: []byte("initial GPU runtime failure")},
			{},
		},
		"docker info": {{ExitCode: 1}, {}},
	}}
	opts := config.TestOptions{ContainerToolkitTestImage: image}

	if err := checkGPUContainerRuntime(context.Background(), io.Discard, runner, opts, true); err != nil {
		t.Fatal(err)
	}
	restart, acl, retry := -1, -1, -1
	for index, command := range runner.commands {
		switch command.LogName {
		case "systemctl_restart_docker":
			restart = index
		case "docker_socket_acl":
			acl = index
		}
		if command.Name == "docker" && hasArg(command.Args, "run") {
			retry = index
		}
	}
	if restart < 0 || acl < 0 || retry < 0 || restart >= acl || acl >= retry {
		t.Fatalf("Toolkit restart, Docker ACL repair, and retry order is invalid: %#v", runner.commands)
	}
}

// TestDownloadsMissingK8sTools verifies pinned helper installation and reuse.
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
	deps := writeBootstrapVersions(t, root, arch, downloads)

	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c command -v k3d":     {ExitCode: 1},
		"sh -c command -v kubectl": {ExitCode: 1},
		"sh -c command -v helm":    {ExitCode: 1},
	}, downloads: downloads}
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall, Suites: []string{"k8s"}, ContainerToolkitTestImage: "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04", Dependencies: deps}
	restore, err := ConfigureToolPath(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()

	var stdout bytes.Buffer
	if err := ifRequestedWithUbuntu(context.Background(), &stdout, root, runner, &opts); err != nil {
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

// TestVerifyK8sClientToolsIsReadOnly verifies check mode accepts pinned tools.
func TestVerifyK8sClientToolsIsReadOnly(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"k3d version":                   {Stdout: []byte("k3d version v5.8.3\n")},
		"kubectl version --client=true": {Stdout: []byte("Client Version: v1.35.3\n")},
		"helm version --short":          {Stdout: []byte("v4.1.0+g123\n")},
	}}
	opts := config.TestOptions{
		Dependencies: config.Dependencies{
			K3DVersion:  "5.8.3",
			K3SVersion:  "v1.35.3-k3s1",
			HelmVersion: "4.1.0",
		},
	}

	if err := verifyK8sClientTools(context.Background(), runner, opts); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandName("curl") || runner.hasCommandArg("install") {
		t.Fatalf("tool verification mutated state: %#v", runner.commands)
	}
}

// TestVerifyK8sClientToolsRequiresPinnedVersions verifies incomplete configuration fails clearly.
func TestVerifyK8sClientToolsRequiresPinnedVersions(t *testing.T) {
	tests := []struct {
		name string
		opts config.TestOptions
		want string
	}{
		{name: "k3d", opts: config.TestOptions{}, want: "K3D_VERSION"},
		{name: "k3s", opts: config.TestOptions{Kubeconfig: "/tmp/kubeconfig"}, want: "K3S_VERSION"},
		{
			name: "helm",
			opts: config.TestOptions{Kubeconfig: "/tmp/kubeconfig", Dependencies: config.Dependencies{K3SVersion: "v1.35.3-k3s1"}},
			want: "HELM_VERSION",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]e2eexec.Result{
				"kubectl version --client=true": {Stdout: []byte("Client Version: v1.35.3\n")},
			}}
			err := verifyK8sClientTools(context.Background(), runner, test.opts)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyK8sClientTools() error = %v, want %s", err, test.want)
			}
		})
	}
}

// TestVerifyK8sClientToolsReportsInstallRecovery verifies missing tools remain read-only.
func TestVerifyK8sClientToolsReportsInstallRecovery(t *testing.T) {
	runner := &fakeRunner{}
	opts := config.TestOptions{
		Kubeconfig: "/tmp/kubeconfig",
		Dependencies: config.Dependencies{
			K3SVersion:  "v1.35.3-k3s1",
			HelmVersion: "4.1.0",
		},
	}

	err := verifyK8sClientTools(context.Background(), runner, opts)
	if err == nil || !strings.Contains(err.Error(), "--bootstrap=install") {
		t.Fatalf("verifyK8sClientTools() error = %v, want recovery command", err)
	}
	if runner.hasCommandName("curl") {
		t.Fatalf("tool verification downloaded content: %#v", runner.commands)
	}
}

// TestEnsureDockerDoesNotBypassPlatformPackageBootstrap keeps package installation in the host installer.
func TestEnsureDockerDoesNotBypassPlatformPackageBootstrap(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c command -v docker": {ExitCode: 1},
	}}

	err := ensureDocker(context.Background(), io.Discard, runner, true, true)
	if err == nil || !strings.Contains(err.Error(), "host package setup") {
		t.Fatalf("ensureDocker() error = %v, want host package setup failure", err)
	}
	if runner.hasCommandName("apt-get") || runner.hasCommandName("dnf") {
		t.Fatalf("common Docker verification bypassed the host package installer: %#v", runner.commands)
	}
}

// TestEnsureDockerRejectsMissingBuildxWhenInstallDisabled verifies read-only failure behavior.
func TestEnsureDockerRejectsMissingBuildxWhenInstallDisabled(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker buildx version": {ExitCode: 1, Stderr: []byte("docker: 'buildx' is not a docker command")},
	}}

	err := ensureDocker(context.Background(), io.Discard, runner, true, false)
	if err == nil || !strings.Contains(err.Error(), "Docker Buildx is required") {
		t.Fatalf("ensureDocker() error = %v, want missing Buildx error", err)
	}
	if runner.hasCommandArg("docker-buildx") {
		t.Fatalf("Buildx installation ran despite disabled dependency installation: %#v", runner.commands)
	}
}

// TestEnsureDockerDoesNotRequireBuildxWithoutLocalCluster limits Buildx to relevant run conditions.
func TestEnsureDockerDoesNotRequireBuildxWithoutLocalCluster(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker buildx version": {ExitCode: 1, Stderr: []byte("docker: 'buildx' is not a docker command")},
	}}

	if err := ensureDocker(context.Background(), io.Discard, runner, false, true); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Name == "docker" && len(command.Args) >= 2 && command.Args[0] == "buildx" {
			t.Fatalf("non-local Docker dependency check invoked Buildx: %#v", runner.commands)
		}
	}
}

// TestEnsureDockerDoesNotRecheckAvailableBuildx avoids redundant readiness probes.
func TestEnsureDockerDoesNotRecheckAvailableBuildx(t *testing.T) {
	runner := &fakeRunner{}

	if err := ensureDocker(context.Background(), io.Discard, runner, true, true); err != nil {
		t.Fatal(err)
	}
	checks := 0
	for _, command := range runner.commands {
		if command.Name == "docker" && len(command.Args) >= 2 && command.Args[0] == "buildx" && command.Args[1] == "version" {
			checks++
		}
	}
	if checks != 1 {
		t.Fatalf("Docker Buildx availability checks = %d, want 1: %#v", checks, runner.commands)
	}
}

// TestEnsureDockerReportsServiceRepairFailure keeps a failed service operation visible.
func TestEnsureDockerReportsServiceRepairFailure(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker info": {ExitCode: 1},
		commandKey(sudoCommand("systemctl", "reset-failed", "docker", "docker.socket")): {
			ExitCode: 1,
			Stderr:   []byte("systemd is unavailable"),
		},
	}}

	err := ensureDocker(context.Background(), io.Discard, runner, false, true)
	if err == nil || !strings.Contains(err.Error(), "start Docker service") || !strings.Contains(err.Error(), "systemd is unavailable") {
		t.Fatalf("ensureDocker() error = %v, want service repair failure", err)
	}
	if runner.hasCommandArg("setfacl") {
		t.Fatalf("service repair failure attempted ACL mutation: %#v", runner.commands)
	}
}

// TestEnsureDockerRepairsAndVerifiesSocketACL verifies the repair command is
// followed by a current-user Docker access check.
func TestEnsureDockerRepairsAndVerifiesSocketACL(t *testing.T) {
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		"docker info": {{ExitCode: 1}, {ExitCode: 1}, {}},
	}}

	if err := ensureDocker(context.Background(), io.Discard, runner, false, true); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandName("setfacl") {
		t.Fatalf("ensureDocker() did not repair the Docker socket ACL: %#v", runner.commands)
	}
	checks := 0
	for _, command := range runner.commands {
		if command.Name == "docker" && len(command.Args) == 1 && command.Args[0] == "info" {
			checks++
		}
	}
	if checks != 3 {
		t.Fatalf("Docker access checks = %d, want 3: %#v", checks, runner.commands)
	}
}

// TestEnsureVSOCKVerifiesModprobeResult rejects a module load that leaves VSOCK unavailable.
func TestEnsureVSOCKVerifiesModprobeResult(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"test -e /dev/vsock": exitStatus1,
	}}

	err := ensureVSOCK(context.Background(), io.Discard, runner)
	if err == nil || !strings.Contains(err.Error(), "unavailable after loading") {
		t.Fatalf("ensureVSOCK() error = %v, want post-modprobe readiness failure", err)
	}
	if !runner.hasCommandArg("vsock_loopback") {
		t.Fatalf("ensureVSOCK() did not load vsock_loopback: %#v", runner.commands)
	}
}

// TestEnsureVSOCKAcceptsBuiltInLoopback verifies readiness does not require a loadable module entry.
func TestEnsureVSOCKAcceptsBuiltInLoopback(t *testing.T) {
	runner := &fakeRunner{}

	if err := ensureVSOCK(context.Background(), io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArgContaining("/sys/module/vsock_loopback") {
		t.Fatalf("VSOCK readiness did not check the built-in module path: %#v", runner.commands)
	}
	if runner.hasCommandArg("modprobe") {
		t.Fatalf("ready built-in VSOCK attempted modprobe: %#v", runner.commands)
	}
}

// TestHostDCGMLibraryChecksIncludeRPMPath covers the RHEL library layout.
func TestHostDCGMLibraryChecksIncludeRPMPath(t *testing.T) {
	runner := &fakeRunner{}
	HostDCGMLibraryAvailable(context.Background(), runner)
	HostDCGMInjectionLibraryAvailable(context.Background(), runner)
	if !runner.hasCommandArgContaining("/usr/lib64/libdcgm.so.4") {
		t.Fatalf("libdcgm check does not include RPM library path; commands = %#v", runner.commands)
	}
	if !runner.hasCommandArgContaining("/usr/lib64/libnvml_injection.so") {
		t.Fatalf("NVML injection check does not include RPM library path; commands = %#v", runner.commands)
	}
}

// TestNVSDMMockDoesNotRequireVSOCK verifies mock configuration suppresses VSOCK setup.
func TestNVSDMMockDoesNotRequireVSOCK(t *testing.T) {
	version := "1:4.5.3-1"
	available := e2eexec.Result{Stdout: []byte("package | 1:4.5.3-1 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core":        available,
		"apt-cache madison datacenter-gpu-manager-4-proprietary": available,
		commandKey(dcgmPackageQueryCommand(false)): {
			Stdout: []byte("ii  datacenter-gpu-manager-4-core=" + version + "\nii  datacenter-gpu-manager-4-proprietary=" + version + "\n"),
		},
	}}
	opts := config.TestOptions{
		BootstrapMode:     config.BootstrapInstall,
		Suites:            []string{"host"},
		DCGMNVSDMMockYAML: "fixture.yaml",
		Dependencies:      config.Dependencies{DCGMVersion: "4.5.3"},
	}
	if err := ifRequestedWithUbuntu(context.Background(), &bytes.Buffer{}, t.TempDir(), runner, &opts); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandArg("/dev/vsock") || runner.hasCommandArg("vsock_loopback") {
		t.Fatalf("NVSDM mock unexpectedly required VSOCK; commands = %#v", runner.commands)
	}
}

// TestVersionOutputMatchesRequiresExactToken rejects substring false positives.
func TestVersionOutputMatchesRequiresExactToken(t *testing.T) {
	for _, output := range []string{"v15.8.30", "5.8.30", "x5.8.3suffix"} {
		if versionOutputMatches(output, "5.8.3") {
			t.Fatalf("versionOutputMatches(%q) accepted a non-exact version", output)
		}
	}
	if !versionOutputMatches("k3d version v5.8.3", "5.8.3") {
		t.Fatal("exact version token was rejected")
	}
}

// TestPureBootstrapHelpers covers configuration branches outside OS transactions.
func TestPureBootstrapHelpers(t *testing.T) {
	deps := config.Dependencies{
		K3DAMD64SHA256: "k3d-amd", K3DARM64SHA256: "k3d-arm",
		KubectlAMD64SHA256: "kubectl-amd", KubectlARM64SHA256: "kubectl-arm",
		HelmAMD64SHA256: "helm-amd", HelmARM64SHA256: "helm-arm",
	}
	for _, test := range []struct{ name, arch, want string }{
		{"K3D", "amd64", "k3d-amd"},
		{"K3D", "arm64", "k3d-arm"},
		{"KUBECTL", "amd64", "kubectl-amd"},
		{"KUBECTL", "arm64", "kubectl-arm"},
		{"HELM", "amd64", "helm-amd"},
		{"HELM", "arm64", "helm-arm"},
		{"UNKNOWN", "amd64", ""},
	} {
		if got := dependencySHA(deps, test.name, test.arch); got != test.want {
			t.Fatalf("dependencySHA(%s, %s) = %q, want %q", test.name, test.arch, got, test.want)
		}
	}
	root := t.TempDir()
	if got := resolveRootPath(root, "relative"); got != filepath.Join(root, "relative") {
		t.Fatalf("relative path = %q", got)
	}
	if got := resolveRootPath(root, "/absolute"); got != "/absolute" {
		t.Fatalf("absolute path = %q", got)
	}
	if currentUsername() == "" {
		t.Fatal("currentUsername returned an empty local identity")
	}
}

// TestFailsWhenChecksumMissing rejects unverified helper downloads.
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
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall, Suites: []string{"k8s"}, Dependencies: config.Dependencies{
		K3DVersion: "5.8.3", K3SVersion: "v1.35.3-k3s1", HelmVersion: "4.1.0", ContainerToolkitVersion: "1.19.1-1",
	}}

	var stdout bytes.Buffer
	err := ifRequestedWithUbuntu(context.Background(), &stdout, root, runner, &opts)
	if err == nil || !strings.Contains(err.Error(), "K3D_LINUX_"+strings.ToUpper(runtime.GOARCH)+"_SHA256") {
		t.Fatalf("RunIfEnabled() error = %v, want missing checksum", err)
	}
	if runner.hasCommandArgContaining("github.com/k3d-io/k3d/releases/download") {
		t.Fatalf("download ran despite missing checksum: %#v", runner.commands)
	}
}

// TestFailsOnChecksumMismatch rejects helper content that differs from its pin.
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
	deps := writeBootstrapVersions(t, root, arch, map[string][]byte{
		installDepsURLK3D(arch):     []byte("not what curl writes"),
		installDepsURLKubectl(arch): downloads[installDepsURLKubectl(arch)],
		installDepsURLHelm(arch):    downloads[installDepsURLHelm(arch)],
	})
	runner := &fakeRunner{results: map[string]e2eexec.Result{"sh -c command -v k3d": {ExitCode: 1}}, downloads: bad}
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall, Suites: []string{"k8s"}, Dependencies: deps}

	var stdout bytes.Buffer
	err = ifRequestedWithUbuntu(context.Background(), &stdout, root, runner, &opts)
	if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("RunIfEnabled() error = %v, want checksum failure", err)
	}
	if runner.hasCommandArg("+x") {
		t.Fatalf("chmod ran despite checksum failure: %#v", runner.commands)
	}
}

// installDepsDownloadBytes returns deterministic helper artifacts for one architecture.
func installDepsDownloadBytes(arch string) map[string][]byte {
	return map[string][]byte{
		installDepsURLK3D(arch):     []byte("fake k3d"),
		installDepsURLKubectl(arch): []byte("fake kubectl"),
		installDepsURLHelm(arch):    []byte("fake helm"),
	}
}

// installDepsURLK3D returns the pinned k3d fixture URL.
func installDepsURLK3D(arch string) string {
	return "https://github.com/k3d-io/k3d/releases/download/v5.8.3/k3d-linux-" + arch
}

// installDepsURLKubectl returns the pinned kubectl fixture URL.
func installDepsURLKubectl(arch string) string {
	return "https://dl.k8s.io/release/v1.35.3/bin/linux/" + arch + "/kubectl"
}

// installDepsURLHelm returns the pinned Helm fixture URL.
func installDepsURLHelm(arch string) string {
	return "https://get.helm.sh/helm-v4.1.0-linux-" + arch + ".tar.gz"
}

// writeBootstrapVersions writes fixture versions and returns the parsed configuration.
func writeBootstrapVersions(t *testing.T, root, arch string, downloads map[string][]byte) config.Dependencies {
	t.Helper()
	content := strings.Join([]string{
		"K3D_VERSION=5.8.3",
		"K3S_VERSION=v1.35.3-k3s1",
		"HELM_VERSION=4.1.0",
		"NVIDIA_CONTAINER_TOOLKIT_VERSION=1.19.1-1",
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
		K3DVersion:              "5.8.3",
		K3SVersion:              "v1.35.3-k3s1",
		HelmVersion:             "4.1.0",
		ContainerToolkitVersion: "1.19.1-1",
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

// sha256Hex returns the lowercase checksum used by helper fixtures.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// exitStatus1 is what the real runner returns for a command that ran and exited 1:
// the exit code and the matching error.
var exitStatus1 = e2eexec.Result{ExitCode: 1, Err: errors.New("exit status 1")}

// fakeRunner records commands and supplies deterministic process and download results.
type fakeRunner struct {
	results         map[string]e2eexec.Result
	resultSequences map[string][]e2eexec.Result
	resultMatcher   func(e2eexec.Command) (e2eexec.Result, bool)
	downloads       map[string][]byte
	commands        []e2eexec.Command
}

// ifRequestedWithUbuntu runs the production planner with a deterministic supported host.
func ifRequestedWithUbuntu(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts *config.TestOptions) error {
	if opts.BootstrapMode != config.BootstrapInstall || opts.BuildOnly {
		return nil
	}
	requirements, err := BuildDependencyRequirements(requirementsForTest(*opts))
	if err != nil {
		return err
	}
	bootstrap := DependencyBootstrap{
		RootDir:       root,
		Output:        stdout,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}
	return bootstrap.Run(ctx, *opts, requirements)
}

// requirementsForTest builds the same complete requirement list that the CLI supplies to bootstrap.
func requirementsForTest(opts config.TestOptions) []scenario.SelectedRequirement {
	selected := scenario.Selected(scenario.Catalog, config.Config{TestOptions: opts})
	return scenario.RequirementsFor(selected, scenario.RunConditions{
		LocalKubernetes:         opts.Kubeconfig == "",
		RegistryLoginConfigured: len(opts.DockerRegistryLogins) != 0 || opts.DockerRegistryLoginFile != "",
		NVMLInjectionConfigured: opts.DCGMNVMLInjectionYAML != "",
		HostDCGMIsMocked:        opts.DCGMNVMLInjectionYAML != "" || opts.DCGMNVSDMMockYAML != "",
	})
}

// Run implements the process runner used by bootstrap tests.
func (f *fakeRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	f.commands = append(f.commands, command)
	if f.resultMatcher != nil {
		if result, ok := f.resultMatcher(command); ok {
			return streamFakeResult(command, result)
		}
	}
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
	if command.Name == "dpkg-query" && hasArg(command.Args, "nvidia-container-toolkit") {
		return streamFakeResult(command, e2eexec.Result{Stdout: testContainerToolkitDebianPackages("1.19.1-1")})
	}
	if command.Name == "rpm" && hasArg(command.Args, "nvidia-container-toolkit") {
		return streamFakeResult(command, e2eexec.Result{Stdout: testContainerToolkitRPMPackages("1.19.1", "1")})
	}
	if command.Name == "curl" {
		if err := f.writeCurlOutput(command); err != nil {
			return e2eexec.Result{ExitCode: 1, Err: err}
		}
		return e2eexec.Result{}
	}
	return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

func testContainerToolkitDebianPackages(version string) []byte {
	var output strings.Builder
	for _, name := range containerToolkitPackageNames() {
		output.WriteString("ii  " + name + "=" + version + "\n")
	}
	return []byte(output.String())
}

func testContainerToolkitRPMPackages(version, release string) []byte {
	var output strings.Builder
	for _, name := range rhelContainerToolkitPackageNames() {
		output.WriteString(name + "|0|" + version + "|" + release + "|x86_64\n")
	}
	return []byte(output.String())
}

// commandKey produces the stable lookup key used by fake results.
func commandKey(command e2eexec.Command) string {
	return strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
}

// streamFakeResult mirrors configured output into command writers.
func streamFakeResult(command e2eexec.Command, result e2eexec.Result) e2eexec.Result {
	if command.Stdout != nil && len(result.Stdout) != 0 {
		_, _ = command.Stdout.Write(result.Stdout)
	}
	if command.Stderr != nil && len(result.Stderr) != 0 {
		_, _ = command.Stderr.Write(result.Stderr)
	}
	return result
}

// writeCurlOutput materializes a configured URL at curl's output path.
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

// hasCommandArg reports whether any recorded command contains an exact argument.
func (f *fakeRunner) hasCommandArg(value string) bool {
	for _, command := range f.commands {
		if hasArg(command.Args, value) {
			return true
		}
	}
	return false
}

// hasCommandArgContaining reports whether any recorded argument contains value.
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

// hasCommandName reports whether a command with the given executable was recorded.
func (f *fakeRunner) hasCommandName(value string) bool {
	for _, command := range f.commands {
		if executableName(command) == value {
			return true
		}
	}
	return false
}

// executableName returns the program a command runs: its name, or for sudo the
// first argument that is neither a sudo option nor a VAR=value assignment. It
// does not know which sudo options take a value; bootstrap passes none.
func executableName(command e2eexec.Command) string {
	if command.Name != "sudo" {
		return command.Name
	}
	for _, arg := range command.Args {
		if !strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") {
			return arg
		}
	}
	return ""
}

// hasArg reports whether an argument slice contains an exact value.
func hasArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}
