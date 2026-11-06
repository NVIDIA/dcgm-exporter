/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package bootstrap

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestUbuntu2604KeyringChecksumsAreSHA256(t *testing.T) {
	for architecture, checksum := range map[string]string{
		"amd64": cudaKeyringUbuntu26AMD64SHA256,
		"arm64": cudaKeyringUbuntu26ARM64SHA256,
	} {
		t.Run(architecture, func(t *testing.T) {
			digest, err := hex.DecodeString(checksum)
			if err != nil {
				t.Fatalf("checksum %q is not hexadecimal: %v", checksum, err)
			}
			if len(digest) != 32 {
				t.Fatalf("checksum decodes to %d bytes, want 32", len(digest))
			}
		})
	}
}

func TestUbuntu2604KeepsConfiguredRepositoryLazy(t *testing.T) {
	baseURL := "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2604/x86_64"
	available := e2eexec.Result{Stdout: []byte("package | 1:4.6.1-1 | " + baseURL + " Packages\n")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core":        available,
		"apt-cache madison datacenter-gpu-manager-4-proprietary": available,
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "26.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
		verifyChecksum: func(string, string) error {
			t.Fatal("configured repository unexpectedly downloaded a keyring package")
			return nil
		},
	}
	if err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandArgContaining(cudaKeyringPackageName) || runner.commandIndex("dpkg", "-i") >= 0 {
		t.Fatalf("configured repository was mutated: %#v", runner.commands)
	}
}

func TestUbuntu2604InstallsPinnedKeyringOnRepositoryMiss(t *testing.T) {
	for _, test := range []struct {
		name, goarch, repositoryArch, checksum string
	}{
		{name: "amd64", goarch: "amd64", repositoryArch: "x86_64", checksum: cudaKeyringUbuntu26AMD64SHA256},
		{name: "arm64", goarch: "arm64", repositoryArch: "sbsa", checksum: cudaKeyringUbuntu26ARM64SHA256},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseURL := "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2604/" + test.repositoryArch
			available := e2eexec.Result{Stdout: []byte("package | 1:4.6.1-1 | " + baseURL + " Packages\n")}
			runner := &fakeRunner{
				resultSequences: map[string][]e2eexec.Result{
					"apt-cache madison datacenter-gpu-manager-4-core": {{}, {}, available, available},
				},
				results: map[string]e2eexec.Result{
					"apt-cache madison datacenter-gpu-manager-4-proprietary": available,
				},
			}
			verifiedChecksum := ""
			packages := ubuntuPackageInstaller{
				hostOS: HostOS{Distribution: "ubuntu", VersionID: "26.04", Architecture: test.goarch, NVIDIARepositoryArch: test.repositoryArch},
				stdout: io.Discard,
				runner: runner,
				verifyChecksum: func(_ string, expected string) error {
					verifiedChecksum = expected
					return nil
				},
			}
			if err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}); err != nil {
				t.Fatal(err)
			}
			if verifiedChecksum != test.checksum {
				t.Fatalf("verified checksum = %q, want %q", verifiedChecksum, test.checksum)
			}
			dpkgIndex := runner.commandIndex("dpkg", "-i")
			aptIndex := runner.commandIndex("apt-get", "update")
			if dpkgIndex < 0 || aptIndex < 0 || dpkgIndex > aptIndex {
				t.Fatalf("keyring fallback did not run before the APT refresh: %#v", runner.commands)
			}
			if !runner.hasCommandArgContaining(baseURL + "/" + cudaKeyringPackageName) {
				t.Fatalf("missing Ubuntu 26.04 keyring package download: %#v", runner.commands)
			}
		})
	}
}

func TestUbuntu2604KeyringChecksumFailureStopsMutation(t *testing.T) {
	runner := &fakeRunner{}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "26.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
		verifyChecksum: func(string, string) error {
			return errors.New("checksum mismatch")
		},
	}
	err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("InstallPackages() error = %v, want checksum mismatch", err)
	}
	if runner.commandIndex("dpkg", "-i") >= 0 || runner.hasCommandArg("install") {
		t.Fatalf("checksum failure mutated package state: %#v", runner.commands)
	}
}

// TestUbuntuDCGMUpdateKeepsUnownedSourceFailure verifies that E2E reports an unowned APT source without changing it.
func TestUbuntuDCGMUpdateKeepsUnownedSourceFailure(t *testing.T) {
	updateCommand := commandKey(aptGetCommand("update"))
	runner := &fakeRunner{
		resultSequences: map[string][]e2eexec.Result{
			"apt-cache madison datacenter-gpu-manager-4-core": {{}, {}},
		},
		results: map[string]e2eexec.Result{
			updateCommand: {ExitCode: 100, Stderr: []byte("E: The repository 'https://packages.example.invalid stable Release' is not signed\n")},
		},
	}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}
	err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"})
	if err == nil || !strings.Contains(err.Error(), "does not provide") || !strings.Contains(err.Error(), "packages.example.invalid") || !strings.Contains(err.Error(), "does not change other sources") {
		t.Fatalf("InstallPackages() error = %v, want original resolution and unowned source details", err)
	}
	for _, command := range runner.commands {
		if command.Name == "rm" || (command.Name == "sudo" && len(command.Args) > 0 && command.Args[0] == "rm") {
			t.Fatalf("unowned source failure removed a host file: %#v", runner.commands)
		}
	}
}

// TestUbuntuDCGMRepairsItsMissingKeyOnce verifies that E2E repairs its own missing CUDA key once.
func TestUbuntuDCGMRepairsItsMissingKeyOnce(t *testing.T) {
	baseURL := "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64"
	updateCommand := commandKey(aptGetCommand("update"))
	runner := &fakeRunner{
		resultSequences: map[string][]e2eexec.Result{
			updateCommand: {
				{ExitCode: 100, Stderr: []byte("W: " + baseURL + " NO_PUBKEY 1234\n")},
				{},
			},
		},
	}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}
	if err := packages.updateCUDARepository(context.Background(), "ubuntu2404", baseURL); err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, command := range runner.commands {
		if commandKey(command) == updateCommand {
			updates++
		}
	}
	if updates != 2 {
		t.Fatalf("APT updates = %d, want initial update and one retry; commands = %#v", updates, runner.commands)
	}
	if !runner.hasCommandArgContaining("cuda-archive-keyring.gpg") {
		t.Fatalf("missing CUDA keyring repair: %#v", runner.commands)
	}
}

// TestUbuntuDCGMKeepsBothUpdateFailures verifies that the first and retry APT errors remain available to callers.
func TestUbuntuDCGMKeepsBothUpdateFailures(t *testing.T) {
	baseURL := "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64"
	updateCommand := commandKey(aptGetCommand("update"))
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		updateCommand: {
			{ExitCode: 100, Stderr: []byte("W: " + baseURL + " NO_PUBKEY 1234\n")},
			{ExitCode: 100, Stderr: []byte("E: retry still failed\n")},
		},
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}
	err := packages.updateCUDARepository(context.Background(), "ubuntu2404", baseURL)
	if err == nil || !strings.Contains(err.Error(), "NO_PUBKEY") || !strings.Contains(err.Error(), "retry still failed") {
		t.Fatalf("updateCUDARepository() error = %v, want both update failures", err)
	}
}

// TestUbuntuInstallPackagesOwnsRepositoriesAndNames verifies complete APT transactions.
func TestUbuntuInstallPackagesOwnsRepositoriesAndNames(t *testing.T) {
	versionOutput := e2eexec.Result{Stdout: []byte("package | 1:4.6.1-2 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	runner := &fakeRunner{
		results: map[string]e2eexec.Result{
			"apt-cache madison datacenter-gpu-manager-4-core":        versionOutput,
			"apt-cache madison datacenter-gpu-manager-4-proprietary": versionOutput,
			"apt-cache madison datacenter-gpu-manager-4-dev":         versionOutput,
		},
		downloads: map[string][]byte{
			nvidiaToolkitListURL: []byte("deb https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /\n"),
		},
	}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}
	requirements := PackageRequirements{
		Docker: true, Buildx: true, ContainerToolkit: true,
		HostDCGM: true, DCGMDevelopment: true, DCGMVersion: "4.6.1", ContainerToolkitVersion: "1.19.1-1",
	}

	if err := packages.InstallPackages(context.Background(), requirements); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"docker.io",
		"docker-buildx",
		"nvidia-container-toolkit=1.19.1-1",
		"nvidia-container-toolkit-base=1.19.1-1",
		"libnvidia-container-tools=1.19.1-1",
		"libnvidia-container1=1.19.1-1",
		"datacenter-gpu-manager-4-core=1:4.6.1-2",
		"datacenter-gpu-manager-4-proprietary=1:4.6.1-2",
		"datacenter-gpu-manager-4-dev=1:4.6.1-2",
	} {
		if !runner.hasCommandArg(want) {
			t.Fatalf("missing package %q: %#v", want, runner.commands)
		}
	}
	if runner.hasCommandArgContaining("compute/cuda/repos/ubuntu2404/x86_64/cuda-archive-keyring.gpg") {
		t.Fatalf("existing approved CUDA repository was configured again: %#v", runner.commands)
	}
	if !runner.hasStdinContaining("signed-by=" + nvidiaToolkitKeyPath) {
		t.Fatalf("toolkit repository did not bind its signing key: %#v", runner.commands)
	}
}

func TestUbuntuContainerToolkitRequiresExactPackageClosure(t *testing.T) {
	requirements := PackageRequirements{ContainerToolkit: true, ContainerToolkitVersion: "1.19.1-1"}
	for _, test := range []struct {
		name     string
		packages []byte
		want     bool
	}{
		{name: "exact", packages: testContainerToolkitDebianPackages("1.19.1-1"), want: true},
		{name: "base package drift", packages: []byte("ii  nvidia-container-toolkit=1.19.1-1\n" +
			"ii  nvidia-container-toolkit-base=1.20.0-1\n" +
			"ii  libnvidia-container-tools=1.19.1-1\n" +
			"ii  libnvidia-container1=1.19.1-1\n"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]e2eexec.Result{
				commandKey(containerToolkitPackageQueryCommand()): {Stdout: test.packages},
			}}
			status, err := (&ubuntuPackageInstaller{stdout: io.Discard, runner: runner}).CheckPackages(context.Background(), requirements)
			if err != nil {
				t.Fatal(err)
			}
			if status.ContainerToolkit != test.want {
				t.Fatalf("Container Toolkit ready = %t, want %t", status.ContainerToolkit, test.want)
			}
		})
	}
}

func TestUbuntuBuildxUsesDockerCEPluginForExistingDockerCE(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"dpkg-query -W -f=${db:Status-Abbrev} docker-ce": {Stdout: []byte("ii ")},
	}}
	packages := ubuntuPackageInstaller{
		stdout: io.Discard,
		runner: runner,
	}

	if err := packages.InstallPackages(context.Background(), PackageRequirements{Buildx: true}); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("docker-buildx-plugin") {
		t.Fatalf("Docker CE Buildx plugin was not selected: %#v", runner.commands)
	}
	if runner.hasCommandArg("docker-buildx") {
		t.Fatalf("Ubuntu docker-buildx conflicts with existing Docker CE: %#v", runner.commands)
	}
}

// TestUbuntuDockerSocketACLMapsToACLPackage keeps the access-repair package
// independent from Docker engine installation.
func TestUbuntuDockerSocketACLMapsToACLPackage(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"dpkg-query -W -f=${db:Status-Abbrev} acl": {ExitCode: 1},
	}}
	packages := ubuntuPackageInstaller{stdout: io.Discard, runner: runner}
	requirements := PackageRequirements{DockerSocketACL: true}

	status, err := packages.CheckPackages(context.Background(), requirements)
	if err != nil {
		t.Fatal(err)
	}
	if status.DockerSocketACL {
		t.Fatalf("CheckPackages() = %#v, want missing Docker socket ACL package", status)
	}
	if err := packages.InstallPackages(context.Background(), requirements); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("acl") || runner.hasCommandArg("docker.io") {
		t.Fatalf("ACL package install did not stay scoped to access repair: %#v", runner.commands)
	}
}

// TestUbuntuDCGMConfiguresMissingApprovedRepository verifies a source is added only when the pin is unavailable.
func TestUbuntuDCGMConfiguresMissingApprovedRepository(t *testing.T) {
	approved := e2eexec.Result{Stdout: []byte("package | 1:4.6.1-2 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core": {
			{Stdout: []byte("package | 1:4.6.1-2 | https://unapproved.invalid Packages\n")},
			{Stdout: []byte("package | 1:4.6.1-2 | https://unapproved.invalid Packages\n")},
			approved,
			approved,
		},
		"apt-cache madison datacenter-gpu-manager-4-proprietary": {approved, approved},
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}

	err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArgContaining("compute/cuda/repos/ubuntu2404/x86_64/cuda-archive-keyring.gpg") {
		t.Fatalf("missing approved CUDA repository setup: %#v", runner.commands)
	}
}

// TestUbuntuDCGMDoesNotRefreshKnownPin verifies that a known DCGM pin does not trigger an APT update.
func TestUbuntuDCGMDoesNotRefreshKnownPin(t *testing.T) {
	baseURL := "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64"
	available := e2eexec.Result{Stdout: []byte("package | 1:4.6.1-2 | " + baseURL + " Packages\n")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core":        available,
		"apt-cache madison datacenter-gpu-manager-4-proprietary": available,
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}

	if err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}); err != nil {
		t.Fatal(err)
	}
	if runner.commandIndex("apt-get", "update") >= 0 || runner.hasCommandArgContaining("cuda-archive-keyring.gpg") {
		t.Fatalf("known pin refreshed or changed its repository: %#v", runner.commands)
	}
	if !runner.hasCommandArg("datacenter-gpu-manager-4-core=1:4.6.1-2") {
		t.Fatalf("known pin was not installed: %#v", runner.commands)
	}
}

// TestUbuntuDCGMDoesNotReplaceApprovedRepositoryWithoutPin guards against duplicate Signed-By sources.
func TestUbuntuDCGMDoesNotReplaceApprovedRepositoryWithoutPin(t *testing.T) {
	available := e2eexec.Result{Stdout: []byte("package | 1:4.5.3-1 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core": available,
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}

	err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"})
	if err == nil || !strings.Contains(err.Error(), "does not provide") {
		t.Fatalf("InstallPackages() error = %v, want unavailable pin", err)
	}
	if runner.hasCommandArgContaining("cuda-archive-keyring.gpg") {
		t.Fatalf("approved CUDA repository was replaced because its pin was unavailable: %#v", runner.commands)
	}
}

// TestUbuntuDCGMRejectsUnavailablePin verifies repository errors occur before packages are changed.
func TestUbuntuDCGMRejectsUnavailablePin(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core": {Stdout: []byte("package | 1:4.5.3-1 | repository\n")},
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}

	err := packages.InstallPackages(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"})
	if err == nil || !strings.Contains(err.Error(), "does not provide") {
		t.Fatalf("InstallPackages() error = %v, want unavailable pin", err)
	}
	if runner.hasCommandArg("datacenter-gpu-manager-4-core=1:4.6.1-1") {
		t.Fatalf("package installation ran without a resolvable pin: %#v", runner.commands)
	}
}

// TestUbuntuDCGMRejectsMatchingIdentityFromUnapprovedRepository rejects a matching version from an unapproved repository.
func TestUbuntuDCGMRejectsMatchingIdentityFromUnapprovedRepository(t *testing.T) {
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{VersionID: "24.04", NVIDIARepositoryArch: "x86_64"}, stdout: io.Discard,
		runner: &fakeRunner{results: map[string]e2eexec.Result{
			"apt-cache madison datacenter-gpu-manager-4-core": {Stdout: []byte("package | 1:4.6.1-9 | https://hostile.invalid/developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")},
		}},
	}
	if _, err := packages.resolveDCGMVersion(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}); err == nil {
		t.Fatal("unapproved Ubuntu repository candidate was accepted")
	}
}

// TestUbuntuDCGMResolutionOrdersCompleteDebianIdentities covers epoch and rebuild selection.
func TestUbuntuDCGMResolutionOrdersCompleteDebianIdentities(t *testing.T) {
	output := strings.Join([]string{
		"package | 1:4.6.1-3 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages",
		"package | 2:4.6.1-1 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages",
	}, "\n")
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core":        {Stdout: []byte(output)},
		"apt-cache madison datacenter-gpu-manager-4-proprietary": {Stdout: []byte(output)},
		"dpkg --compare-versions 2:4.6.1-1 gt 1:4.6.1-3":         {},
	}}
	packages := ubuntuPackageInstaller{hostOS: HostOS{VersionID: "24.04", NVIDIARepositoryArch: "x86_64"}, runner: runner}
	version, err := packages.resolveDCGMVersion(context.Background(), PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"})
	if err != nil {
		t.Fatal(err)
	}
	if version != "2:4.6.1-1" {
		t.Fatalf("resolved identity = %q, want highest epoch rebuild", version)
	}
}

// TestSignedToolkitRepositoryRejectsUntrustedContent verifies approved origins and key binding.
func TestSignedToolkitRepositoryRejectsUntrustedContent(t *testing.T) {
	for _, content := range []string{
		"",
		"rpm https://example.invalid/repository\n",
		"deb http://example.invalid/repository /\n",
		"deb https://hostile.invalid/libnvidia-container/stable/deb/$(ARCH) /\n",
		"deb https://nvidia.github.io/libnvidia-container/stable/deb/other /\n",
	} {
		if _, err := signedToolkitRepository([]byte(content)); err == nil {
			t.Fatalf("signedToolkitRepository(%q) succeeded, want error", content)
		}
	}
}

// TestSignedToolkitRepositoryAcceptsApprovedOrigins verifies both published toolkit channels.
func TestSignedToolkitRepositoryAcceptsApprovedOrigins(t *testing.T) {
	content := strings.Join([]string{
		"deb https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /",
		"deb [signed-by=" + nvidiaToolkitKeyPath + "] https://nvidia.github.io/libnvidia-container/experimental/deb/$(ARCH) /",
	}, "\n")

	got, err := signedToolkitRepository([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "signed-by="+nvidiaToolkitKeyPath) != 2 {
		t.Fatalf("signedToolkitRepository() = %q, want both entries bound to approved key", got)
	}
}

// TestPackageRequirementsStringListsRequiredGroups verifies operator diagnostics.
func TestPackageRequirementsStringListsRequiredGroups(t *testing.T) {
	requirements := PackageRequirements{Docker: true, Buildx: true, ContainerToolkit: true, HostDCGM: true, DCGMDevelopment: true}
	want := "Docker, Docker Buildx, NVIDIA Container Toolkit, host DCGM, DCGM development files"
	if got := requirements.String(); got != want {
		t.Fatalf("PackageRequirements.String() = %q, want %q", got, want)
	}
}

func TestUbuntuDCGMChecksInstalledPackagesWithoutAPTPackageLists(t *testing.T) {
	query := commandKey(dcgmPackageQueryCommand(true))
	for _, test := range []struct {
		name, installedVersion string
		wantReady              bool
	}{
		{name: "matching pin", installedVersion: "1:4.7.0-1", wantReady: true},
		{name: "different pin", installedVersion: "1:4.6.1-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]e2eexec.Result{
				query: {Stdout: []byte(strings.Join([]string{
					"ii  datacenter-gpu-manager-4-core=" + test.installedVersion,
					"ii  datacenter-gpu-manager-4-proprietary=" + test.installedVersion,
					"ii  datacenter-gpu-manager-4-dev=" + test.installedVersion,
				}, "\n"))},
			}}
			packages := ubuntuPackageInstaller{
				hostOS: HostOS{Distribution: "ubuntu", VersionID: "26.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
				runner: runner,
			}
			status, err := packages.CheckPackages(context.Background(), PackageRequirements{
				HostDCGM: true, DCGMDevelopment: true, DCGMVersion: "4.7.0",
			})
			if test.wantReady {
				if err != nil || !status.HostDCGM || !status.DCGMDevelopment {
					t.Fatalf("CheckPackages() = %#v, %v; want installed packages ready", status, err)
				}
				if runner.commandIndex("apt-cache", "madison") < 0 {
					t.Fatalf("ready packages bypassed APT repository resolution: %#v", runner.commands)
				}
			} else if err != nil || status.HostDCGM || status.DCGMDevelopment || status.HostDCGMReason == "" {
				t.Fatalf("CheckPackages() = %#v, %v; want mismatched packages not ready with a reason", status, err)
			}
		})
	}
}

// TestUbuntuObservePackagesComparesPinnedDCGMIdentity verifies version mismatch behavior.
func TestUbuntuObservePackagesComparesPinnedDCGMIdentity(t *testing.T) {
	query := commandKey(dcgmPackageQueryCommand(true))
	available := e2eexec.Result{Stdout: []byte("package | 1:4.6.1-1 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core":        available,
		"apt-cache madison datacenter-gpu-manager-4-proprietary": available,
		"apt-cache madison datacenter-gpu-manager-4-dev":         available,
		query: {Stdout: []byte(strings.Join([]string{
			"ii  datacenter-gpu-manager-4-core=1:4.6.1-1",
			"ii  datacenter-gpu-manager-4-proprietary=1:4.6.1-1",
			"ii  datacenter-gpu-manager-4-dev=1:4.5.3-1",
		}, "\n"))},
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		runner: runner,
		stdout: io.Discard,
	}

	status, err := packages.CheckPackages(context.Background(), PackageRequirements{
		HostDCGM: true, DCGMDevelopment: true, DCGMVersion: "4.6.1",
	})
	if err != nil || !strings.Contains(status.HostDCGMReason, "different versions") {
		t.Fatalf("CheckPackages() = %#v, %v; want mixed versions reason", status, err)
	}
	if !status.HostDCGM || status.DCGMDevelopment {
		t.Fatalf("CheckPackages() = %#v, want runtime ready and development mismatch", status)
	}
}

// ubuntuOfferedDCGM returns apt-cache madison output listing versions from the approved repository.
func ubuntuOfferedDCGM(versions ...string) e2eexec.Result {
	return ubuntuOfferedFrom("https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2604/x86_64", versions...)
}

// ubuntuOfferedFrom returns apt-cache madison output listing versions from one repository URL.
func ubuntuOfferedFrom(repositoryURL string, versions ...string) e2eexec.Result {
	var output string
	for _, version := range versions {
		output += "package | " + version + " | " + repositoryURL + " Packages\n"
	}
	return e2eexec.Result{Stdout: []byte(output)}
}

// ubuntuInstalledDCGM returns dpkg-query output for the given versions; an empty version means not installed.
func ubuntuInstalledDCGM(core, proprietary, development string) e2eexec.Result {
	var lines []string
	for _, entry := range [][2]string{
		{"datacenter-gpu-manager-4-core", core},
		{"datacenter-gpu-manager-4-proprietary", proprietary},
		{"datacenter-gpu-manager-4-dev", development},
	} {
		if entry[1] != "" {
			lines = append(lines, "ii  "+entry[0]+"="+entry[1])
		}
	}
	return e2eexec.Result{Stdout: []byte(strings.Join(lines, "\n"))}
}

// ubuntuDCGMCase is one readiness scenario for the Ubuntu DCGM check.
type ubuntuDCGMCase struct {
	name string
	// runtimeOnly drops the development package from the requirements.
	runtimeOnly bool
	installed   e2eexec.Result
	madison     e2eexec.Result
	// developmentMadison replaces madison for the development package when it has output.
	developmentMadison e2eexec.Result
	// missingLibrary names a library the dynamic loader does not see.
	missingLibrary  string
	wantRuntime     bool
	wantDevelopment bool
	wantReason      string
	wantError       string
}

// TestUbuntuDCGMChecksInstalledVersionAgainstApprovedRepository covers each readiness condition
// and the message of each failure.
func TestUbuntuDCGMChecksInstalledVersionAgainstApprovedRepository(t *testing.T) {
	allOne := ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-1", "1:4.7.0-1")
	bothRevisions := ubuntuOfferedDCGM("1:4.7.0-1", "1:4.7.0-2")
	unapproved := e2eexec.Result{Stdout: []byte("package | 1:4.7.0-2 | https://example.invalid/ubuntu2604/x86_64 Packages\n")}
	tests := []ubuntuDCGMCase{
		// A development package that is not required counts as ready.
		{name: "runtime only ready", runtimeOnly: true, installed: ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-1", ""), madison: bothRevisions, wantRuntime: true, wantDevelopment: true},
		{name: "installed identity offered", installed: allOne, madison: bothRevisions, wantRuntime: true, wantDevelopment: true},
		{name: "lists unavailable", installed: allOne, madison: e2eexec.Result{ExitCode: 1}, wantRuntime: true, wantDevelopment: true},
		{name: "lists empty", installed: allOne, madison: e2eexec.Result{}, wantRuntime: true, wantDevelopment: true},
		{name: "pin only from unapproved source", installed: allOne, madison: unapproved, wantRuntime: true, wantDevelopment: true},
		{name: "not installed", installed: ubuntuInstalledDCGM("", "", ""), madison: bothRevisions, wantReason: "DCGM packages are not installed: datacenter-gpu-manager-4-core, datacenter-gpu-manager-4-proprietary"},
		{
			name: "development not installed",
			installed: e2eexec.Result{
				ExitCode: 1,
				Stdout:   ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-1", "").Stdout,
				Stderr:   []byte("dpkg-query: no packages found matching datacenter-gpu-manager-4-dev\n"),
			},
			madison: bothRevisions, wantRuntime: true, wantReason: "DCGM packages are not installed: datacenter-gpu-manager-4-dev",
		},
		{name: "mixed runtime versions", installed: ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-2", "1:4.7.0-1"), madison: bothRevisions, wantReason: "different versions"},
		{name: "mixed development version", installed: ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-1", "1:4.7.0-2"), madison: bothRevisions, wantRuntime: true, wantReason: "different versions"},
		{name: "other upstream version", installed: ubuntuInstalledDCGM("1:4.6.1-1", "1:4.6.1-1", "1:4.6.1-1"), madison: ubuntuOfferedDCGM("1:4.7.0-1"), wantReason: "does not have the pinned upstream version 4.7.0"},
		{name: "revision not offered", installed: allOne, madison: ubuntuOfferedDCGM("1:4.7.0-2"), wantReason: "is not offered by the approved NVIDIA CUDA repository"},
		{name: "development revision not offered", installed: allOne, madison: bothRevisions, developmentMadison: ubuntuOfferedDCGM("1:4.7.0-2"), wantRuntime: true, wantReason: "installed datacenter-gpu-manager-4-dev version 1:4.7.0-1 is not offered"},
		{name: "query failure", installed: e2eexec.Result{ExitCode: 2, Stderr: []byte("dpkg-query: error: database locked\n")}, madison: bothRevisions, wantError: "query DCGM packages"},
		{name: "query failure without message", installed: e2eexec.Result{ExitCode: 1}, madison: bothRevisions, wantError: "query DCGM packages"},
		{name: "library not visible", installed: allOne, madison: bothRevisions, missingLibrary: "libdcgm.so", wantReason: dcgmLibraryNotVisible},
		{name: "injection library not visible", installed: allOne, madison: bothRevisions, missingLibrary: "libnvml_injection.so", wantRuntime: true, wantReason: dcgmInjectionLibraryNotVisible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, err := checkUbuntuDCGM(test)

			assertDCGMStatus(t, status, err, test.wantRuntime, test.wantDevelopment, test.wantReason, test.wantError)
		})
	}
}

// assertDCGMStatus fails unless the check reported the expected readiness, reason and error.
func assertDCGMStatus(t *testing.T, status PackageStatus, err error, wantRuntime, wantDevelopment bool, wantReason, wantError string) {
	t.Helper()

	if wantError != "" {
		if err == nil || !strings.Contains(err.Error(), wantError) || status.HostDCGMReason != "" {
			t.Fatalf("CheckPackages() = %#v, %v; want inspection error %q", status, err, wantError)
		}
		return
	}
	if err != nil {
		t.Fatalf("CheckPackages() error = %v", err)
	}
	if status.HostDCGM != wantRuntime || status.DCGMDevelopment != wantDevelopment {
		t.Fatalf("CheckPackages() = %#v, want runtime %v and development %v", status, wantRuntime, wantDevelopment)
	}
	if (wantReason == "" && status.HostDCGMReason != "") || !strings.Contains(status.HostDCGMReason, wantReason) {
		t.Fatalf("HostDCGMReason = %q, want %q", status.HostDCGMReason, wantReason)
	}
}

// libraryMatcher makes the dynamic-loader probe for one library fail and leaves other commands alone.
func libraryMatcher(missing string) func(e2eexec.Command) (e2eexec.Result, bool) {
	return func(command e2eexec.Command) (e2eexec.Result, bool) {
		if missing != "" && command.Name == "sh" && strings.Contains(commandKey(command), missing) {
			return e2eexec.Result{ExitCode: 1}, true
		}
		return e2eexec.Result{}, false
	}
}

// checkUbuntuDCGM runs the Ubuntu DCGM check against fixed dpkg-query and apt-cache madison output.
func checkUbuntuDCGM(test ubuntuDCGMCase) (PackageStatus, error) {
	results := map[string]e2eexec.Result{commandKey(dcgmPackageQueryCommand(!test.runtimeOnly)): test.installed}
	for _, name := range dcgmPackageNames(true) {
		results["apt-cache madison "+name] = test.madison
	}
	if test.developmentMadison.Stdout != nil {
		results["apt-cache madison "+dcgmDevelopmentPackage] = test.developmentMadison
	}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "26.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		runner: &fakeRunner{results: results, resultMatcher: libraryMatcher(test.missingLibrary)},
	}

	return packages.CheckPackages(context.Background(), PackageRequirements{
		HostDCGM: true, DCGMDevelopment: !test.runtimeOnly, DCGMVersion: "4.7.0",
	})
}

// TestApprovedAPTSourceRequiresTheExactRepository rejects child paths and URL decorations.
func TestApprovedAPTSourceRequiresTheExactRepository(t *testing.T) {
	approved := "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64"
	for _, source := range []string{
		approved + "/hostile Packages",
		approved + "?mirror=hostile Packages",
		approved + "#hostile Packages",
		"https://user@developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages",
	} {
		if approvedAPTSource(source, approved) {
			t.Fatalf("approvedAPTSource(%q) = true", source)
		}
	}
	if !approvedAPTSource(approved+"/ Packages", approved) {
		t.Fatal("approvedAPTSource() rejected the approved repository with a trailing slash")
	}
}

// TestParsePackageVersionsIgnoresRemovedPackages prevents config-file remnants from satisfying checks.
func TestParsePackageVersionsIgnoresRemovedPackages(t *testing.T) {
	installed := parsePackageVersions([]byte(strings.Join([]string{
		"ii  datacenter-gpu-manager-4-core=1:4.6.1-1",
		"rc  datacenter-gpu-manager-4-proprietary=1:4.6.1-1",
	}, "\n")))

	if installed["datacenter-gpu-manager-4-core"] != "1:4.6.1-1" {
		t.Fatalf("installed packages = %#v, want core package", installed)
	}
	if _, ok := installed["datacenter-gpu-manager-4-proprietary"]; ok {
		t.Fatalf("removed package was treated as installed: %#v", installed)
	}
}

// TestUbuntuToolkitRejectsKeyConversionFailure verifies repository trust errors stop installation.
func TestUbuntuToolkitRejectsKeyConversionFailure(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"gpg": {ExitCode: 2, Stderr: []byte("invalid key")},
	}}
	packages := ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}

	err := packages.InstallPackages(context.Background(), PackageRequirements{ContainerToolkit: true, ContainerToolkitVersion: "1.19.1-1"})
	if err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("InstallPackages() error = %v, want key failure", err)
	}
	if runner.hasCommandArg("nvidia-container-toolkit") {
		t.Fatalf("package install continued after key failure: %#v", runner.commands)
	}
}

// TestUbuntuToolkitPropagatesRepositoryFailures verifies every repository setup error stops installation.
func TestUbuntuToolkitPropagatesRepositoryFailures(t *testing.T) {
	tests := []struct {
		name    string
		results func() map[string]e2eexec.Result
		want    string
	}{
		{
			name: "install repository",
			results: func() map[string]e2eexec.Result {
				return map[string]e2eexec.Result{
					commandKey(sudoCommand("tee", nvidiaToolkitListPath)): {ExitCode: 1, Stderr: []byte("list install failure")},
				}
			},
			want: "list install failure",
		},
		{
			name: "update",
			results: func() map[string]e2eexec.Result {
				return map[string]e2eexec.Result{
					commandKey(aptGetCommand("update")): {ExitCode: 100, Stderr: []byte("update failure")},
				}
			},
			want: "update failure",
		},
		{
			name: "install package",
			results: func() map[string]e2eexec.Result {
				return map[string]e2eexec.Result{
					commandKey(aptGetCommand("install", "-y", "--allow-downgrades", "nvidia-container-toolkit=1.19.1-1", "nvidia-container-toolkit-base=1.19.1-1", "libnvidia-container-tools=1.19.1-1", "libnvidia-container1=1.19.1-1")): {ExitCode: 100, Stderr: []byte("package failure")},
				}
			},
			want: "package failure",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{
				results: test.results(),
				downloads: map[string][]byte{
					nvidiaToolkitListURL: []byte("deb https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /\n"),
				},
			}
			packages := ubuntuPackageInstaller{stdout: io.Discard, runner: runner}
			if err := packages.InstallPackages(context.Background(), PackageRequirements{ContainerToolkit: true, ContainerToolkitVersion: "1.19.1-1"}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("InstallPackages() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestUbuntuDockerStopsOnAPTFailure verifies package transaction failures propagate.
func TestUbuntuDockerStopsOnAPTFailure(t *testing.T) {
	update := aptGetCommand("update")
	install := aptGetCommand("install", "-y", "docker.io")
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		commandKey(update):  {},
		commandKey(install): {ExitCode: 100, Stderr: []byte("package failure")},
	}}
	packages := ubuntuPackageInstaller{stdout: io.Discard, runner: runner}

	err := packages.InstallPackages(context.Background(), PackageRequirements{Docker: true})
	if err == nil || !strings.Contains(err.Error(), "package failure") {
		t.Fatalf("InstallPackages() error = %v, want APT failure", err)
	}
}

// TestUbuntuAPTNeverWaitsForInput verifies every apt-get run by a Docker
// install carries the settings that stop debconf and needrestart from
// prompting, both in its environment and, under sudo, on the sudo command line,
// and keeps changed configuration files.
func TestUbuntuAPTNeverWaitsForInput(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{}}
	packages := ubuntuPackageInstaller{stdout: io.Discard, runner: runner}
	if err := packages.InstallPackages(context.Background(), PackageRequirements{Docker: true}); err != nil {
		t.Fatalf("InstallPackages() error = %v", err)
	}

	apt := 0
	for _, command := range runner.commands {
		if executableName(command) != "apt-get" {
			continue
		}
		apt++
		checkAPTNeverPrompts(t, command)
	}
	if apt != 2 {
		t.Fatalf("apt-get ran %d times, want update and install: %#v", apt, runner.commands)
	}
}

// checkAPTNeverPrompts reports an error for each prompt-stopping setting the
// apt-get command lacks: both settings in its environment, both on the sudo
// command line when run through sudo, and the option that keeps changed
// configuration files.
func checkAPTNeverPrompts(t *testing.T, command e2eexec.Command) {
	t.Helper()

	for _, setting := range []string{"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_SUSPEND=1"} {
		if !slices.Contains(command.Env, setting) {
			t.Errorf("%s: environment lacks %s", commandKey(command), setting)
		}
		if command.Name == "sudo" && !slices.Contains(command.Args, setting) {
			t.Errorf("%s: sudo command line lacks %s", commandKey(command), setting)
		}
	}

	if !slices.Contains(command.Args, "Dpkg::Options::=--force-confold") {
		t.Errorf("%s: does not keep changed configuration files", commandKey(command))
	}
}

// TestNoninteractivePutsSettingsOnTheSudoCommandLine verifies a sudo command
// gets the settings before the program, where sudo keeps them, and a direct
// command gets them in its environment. It builds both shapes, so it does not
// depend on whether the test runs as root.
func TestNoninteractivePutsSettingsOnTheSudoCommandLine(t *testing.T) {
	sudo := noninteractive(e2eexec.Command{Name: "sudo", Args: []string{"apt-get", "update"}})
	want := []string{"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_SUSPEND=1", "apt-get", "update"}
	if !slices.Equal(sudo.Args, want) {
		t.Errorf("sudo args = %q, want %q", sudo.Args, want)
	}

	direct := noninteractive(e2eexec.Command{Name: "apt-get", Args: []string{"update"}})
	if !slices.Equal(direct.Args, []string{"update"}) || !slices.Contains(direct.Env, "DEBIAN_FRONTEND=noninteractive") ||
		!slices.Contains(direct.Env, "NEEDRESTART_SUSPEND=1") {
		t.Errorf("direct command = %q with environment %q, want unchanged args and both settings", direct.Args, direct.Env)
	}

	keyring := dpkgInstallCommand("/tmp/cuda-keyring.deb")
	if !slices.Contains(keyring.Args, "--force-confold") || !slices.Contains(keyring.Env, "DEBIAN_FRONTEND=noninteractive") {
		t.Errorf("keyring install = %q with environment %q, want kept config files and no prompts", keyring.Args, keyring.Env)
	}
}

// TestBootstrapRunsAPTAndDpkgOnlyThroughNoninteractiveHelpers fails when a
// bootstrap source file starts apt-get or dpkg through sudoCommand without
// noninteractive, which would bring back prompts that can hang an unattended
// run.
func TestBootstrapRunsAPTAndDpkgOnlyThroughNoninteractiveHelpers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range []string{`sudoCommand("apt-get"`, `sudoCommand("dpkg"`} {
			bare := strings.Count(string(source), call) - strings.Count(string(source), "noninteractive("+call)
			if bare != 0 {
				t.Errorf("%s calls %s %d time(s) without noninteractive; use aptGetCommand or dpkgInstallCommand", file, call, bare)
			}
		}
	}
}

// TestUbuntuDCGMPropagatesTransactionFailures verifies each trusted-repository stage stops.
func TestUbuntuDCGMPropagatesTransactionFailures(t *testing.T) {
	requirements := PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}
	versionOutput := e2eexec.Result{Stdout: []byte("package | 1:4.6.1-1 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	installArgs := []string{
		"install", "-y", "--allow-downgrades",
		"datacenter-gpu-manager-4-core=1:4.6.1-1",
		"datacenter-gpu-manager-4-proprietary=1:4.6.1-1",
	}
	tests := []struct {
		name    string
		results map[string]e2eexec.Result
	}{
		{
			name: "repository download",
			results: map[string]e2eexec.Result{
				"curl": {ExitCode: 22, Stderr: []byte("download failure")},
			},
		},
		{
			name: "package query",
			results: map[string]e2eexec.Result{
				"apt-cache": {ExitCode: 1, Stderr: []byte("query failure")},
			},
		},
		{
			name: "package install",
			results: map[string]e2eexec.Result{
				"apt-cache madison datacenter-gpu-manager-4-core":        versionOutput,
				"apt-cache madison datacenter-gpu-manager-4-proprietary": versionOutput,
				commandKey(aptGetCommand(installArgs...)):                {ExitCode: 100, Stderr: []byte("install failure")},
			},
		},
		{
			name: "library cache",
			results: map[string]e2eexec.Result{
				"apt-cache madison datacenter-gpu-manager-4-core":        versionOutput,
				"apt-cache madison datacenter-gpu-manager-4-proprietary": versionOutput,
				commandKey(sudoCommand("ldconfig")):                      {ExitCode: 1, Stderr: []byte("ldconfig failure")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: test.results}
			packages := ubuntuPackageInstaller{
				hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
				stdout: io.Discard,
				runner: runner,
			}
			if err := packages.InstallPackages(context.Background(), requirements); err == nil {
				t.Fatal("InstallPackages() succeeded, want transaction failure")
			}
		})
	}
}

// hasStdinContaining reports whether a recorded command received matching input.
func (f *fakeRunner) hasStdinContaining(value string) bool {
	for _, command := range f.commands {
		if strings.Contains(string(command.Stdin), value) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) commandIndex(name, arg string) int {
	for index, command := range f.commands {
		if executableName(command) == name && hasArg(command.Args, arg) {
			return index
		}
	}
	return -1
}

// testDCGMShowOutput is the apt-cache show metadata of DCGM 1:4.7.1-2 that the
// closure tests read: core requires its split packages at exactly its version.
var testDCGMShowOutput = map[string]string{
	"datacenter-gpu-manager-4-core=1:4.7.1-2": "Package: datacenter-gpu-manager-4-core\nVersion: 1:4.7.1-2\n" +
		"Depends: dcgmi (= 1:4.7.1-2), libdcgm (= 1:4.7.1-2), datacenter-gpu-manager-4-module-diag (= 1:4.7.1-2),\n" +
		" nv-hostengine (= 1:4.7.1-2), libc6 (>= 2.34)\n" +
		"Recommends: datacenter-gpu-manager-4-proprietary (= 1:4.7.1-2)\n",
	"datacenter-gpu-manager-4-proprietary=1:4.7.1-2": "Package: datacenter-gpu-manager-4-proprietary\nVersion: 1:4.7.1-2\nDepends: libdcgm (= 1:4.7.1-2)\n",
	"datacenter-gpu-manager-4-dev=1:4.7.1-2":         "Package: datacenter-gpu-manager-4-dev\nVersion: 1:4.7.1-2\nDepends: libdcgm (= 1:4.7.1-2)\n",
	"datacenter-gpu-manager-4-module-diag=1:4.7.1-2": "Package: datacenter-gpu-manager-4-module-diag\nVersion: 1:4.7.1-2\n" +
		"Pre-Depends: datacenter-gpu-manager-4-dcgmproftesterkernels (= 1:4.7.1-2)\n",
	"dcgmi=1:4.7.1-2":         "Package: dcgmi\nVersion: 1:4.7.1-2\nDepends: libdcgm (= 1:4.7.1-2)\n",
	"libdcgm=1:4.7.1-2":       "Package: libdcgm\nVersion: 1:4.7.1-2\nDepends: libc6 (>= 2.34)\n",
	"nv-hostengine=1:4.7.1-2": "Package: nv-hostengine\nVersion: 1:4.7.1-2\nDepends: libdcgm (= 1:4.7.1-2)\n",
	"datacenter-gpu-manager-4-dcgmproftesterkernels=1:4.7.1-2": "Package: datacenter-gpu-manager-4-dcgmproftesterkernels\nVersion: 1:4.7.1-2\n",
}

// testApprovedUbuntu2404 is the approved CUDA repository of the closure tests' host.
const testApprovedUbuntu2404 = "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64"

// dcgmShowRunner answers apt-cache show from testDCGMShowOutput, failing on a
// selector it has no stanza for, and apt-cache madison with the approved
// repository plus any extra source lines in alsoOffered for that package.
func dcgmShowRunner(t *testing.T, alsoOffered map[string]string) *fakeRunner {
	return &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if command.Name != "apt-cache" || len(command.Args) < 2 {
			return e2eexec.Result{}, false
		}

		var out strings.Builder
		switch command.Args[0] {
		case "show":
			for _, selector := range command.Args[1:] {
				stanza, ok := testDCGMShowOutput[selector]
				if !ok {
					t.Errorf("unexpected apt-cache show selector %q", selector)
					return e2eexec.Result{ExitCode: 100, Err: errors.New("exit status 100")}, true
				}
				out.WriteString(stanza + "\n")
			}
		case "madison":
			name := command.Args[1]
			out.WriteString(name + " | 1:4.7.1-2 | " + testApprovedUbuntu2404 + " Packages\n")
			out.WriteString(alsoOffered[name])
		default:
			return e2eexec.Result{}, false
		}
		return e2eexec.Result{Stdout: []byte(out.String())}, true
	}}
}

// testUbuntu2404Installer returns an installer for an Ubuntu 24.04 amd64 host with runner.
func testUbuntu2404Installer(runner e2eexec.Runner) ubuntuPackageInstaller {
	return ubuntuPackageInstaller{
		hostOS: HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}
}

// TestUbuntuDCGMInstallPinsEverySplitPackage checks the apt-get install command
// names each requested package and every package it requires at exactly the
// selected version, so a host pin on another DCGM build cannot pick their versions.
func TestUbuntuDCGMInstallPinsEverySplitPackage(t *testing.T) {
	const version = "1:4.7.1-2"
	tests := []struct {
		name  string
		names []string
		want  []string
	}{
		{
			name:  "runtime",
			names: dcgmPackageNames(false),
			want: []string{
				"datacenter-gpu-manager-4-core", "datacenter-gpu-manager-4-proprietary",
				"dcgmi", "libdcgm", "datacenter-gpu-manager-4-module-diag", "nv-hostengine",
				"datacenter-gpu-manager-4-dcgmproftesterkernels",
			},
		},
		{
			name:  "runtime and development",
			names: dcgmPackageNames(true),
			want: []string{
				"datacenter-gpu-manager-4-core", "datacenter-gpu-manager-4-proprietary", "datacenter-gpu-manager-4-dev",
				"dcgmi", "libdcgm", "datacenter-gpu-manager-4-module-diag", "nv-hostengine",
				"datacenter-gpu-manager-4-dcgmproftesterkernels",
			},
		},
		{name: "development only", names: []string{dcgmDevelopmentPackage}, want: []string{"datacenter-gpu-manager-4-dev", "libdcgm"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := dcgmShowRunner(t, nil)
			if err := testUbuntu2404Installer(runner).installDCGMPackages(context.Background(), test.names, version); err != nil {
				t.Fatal(err)
			}

			want := []string{"install", "-y", "--allow-downgrades"}
			for _, name := range test.want {
				want = append(want, name+"="+version)
			}
			install := aptGetCommand(want...)
			if !slices.ContainsFunc(runner.commands, func(command e2eexec.Command) bool {
				return command.Name == install.Name && slices.Equal(command.Args, install.Args)
			}) {
				t.Fatalf("missing %v in %#v", install.Args, runner.commands)
			}
		})
	}
}

// TestUbuntuDCGMInstallRefusesPackagesOutsideTheApprovedRepository checks that
// the install stops before apt-get runs when a package of the closure is also
// offered at the same version by another source, or is not offered by the
// approved repository, so APT cannot fetch DCGM from elsewhere.
func TestUbuntuDCGMInstallRefusesPackagesOutsideTheApprovedRepository(t *testing.T) {
	tests := []struct {
		name        string
		alsoOffered map[string]string
		matcher     func(e2eexec.Command) (e2eexec.Result, bool)
		wantErr     string
	}{
		{
			name:        "pinned local repository lists the same dependency build",
			alsoOffered: map[string]string{"libdcgm": "libdcgm | 1:4.7.1-2 | file:/usr/share/dcgm-local-repo  Packages\n"},
			wantErr:     "DCGM package libdcgm=1:4.7.1-2 is also offered by file:/usr/share/dcgm-local-repo  Packages",
		},
		{
			name: "requested package from another repository too",
			alsoOffered: map[string]string{
				"datacenter-gpu-manager-4-core": "datacenter-gpu-manager-4-core | 1:4.7.1-2 | https://mirror.invalid/cuda Packages\n",
			},
			wantErr: "DCGM package datacenter-gpu-manager-4-core=1:4.7.1-2 is also offered by https://mirror.invalid/cuda Packages",
		},
		{
			name: "dependency missing from the approved repository",
			matcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
				if command.Name == "apt-cache" && slices.Equal(command.Args, []string{"madison", "nv-hostengine"}) {
					return e2eexec.Result{Stdout: []byte("nv-hostengine | 1:4.7.0-1 | " + testApprovedUbuntu2404 + " Packages\n")}, true
				}
				return e2eexec.Result{}, false
			},
			wantErr: "DCGM package nv-hostengine=1:4.7.1-2 is not offered by the approved CUDA repository",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := dcgmShowRunner(t, test.alsoOffered)
			if test.matcher != nil {
				answerFirst(runner, test.matcher)
			}

			err := testUbuntu2404Installer(runner).installDCGMPackages(context.Background(), dcgmPackageNames(false), "1:4.7.1-2")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
			if runner.hasCommandArg("install") {
				t.Fatalf("apt-get install ran: %#v", runner.commands)
			}
		})
	}
}

// answerFirst makes the runner try matcher before its existing answers, so a
// test can replace the reply to one command and keep the rest.
func answerFirst(runner *fakeRunner, matcher func(e2eexec.Command) (e2eexec.Result, bool)) {
	existing := runner.resultMatcher
	runner.resultMatcher = func(command e2eexec.Command) (e2eexec.Result, bool) {
		if result, ok := matcher(command); ok {
			return result, true
		}
		return existing(command)
	}
}

// TestUbuntuDCGMInstallStopsWhenMetadataIsUnreadable checks that a failed
// apt-cache show stops the install before apt-get runs.
func TestUbuntuDCGMInstallStopsWhenMetadataIsUnreadable(t *testing.T) {
	runner := dcgmShowRunner(t, nil)
	show := runner.resultMatcher
	runner.resultMatcher = func(command e2eexec.Command) (e2eexec.Result, bool) {
		if command.Name == "apt-cache" && len(command.Args) != 0 && command.Args[0] == "show" {
			return e2eexec.Result{ExitCode: 100, Stderr: []byte("E: Can't select version"), Err: errors.New("exit status 100")}, true
		}
		return show(command)
	}
	err := testUbuntu2404Installer(runner).installDCGMPackages(context.Background(), dcgmPackageNames(false), "1:4.7.1-2")
	if err == nil || !strings.Contains(err.Error(), "Can't select version") {
		t.Fatalf("error = %v, want the apt-cache failure", err)
	}
	if runner.hasCommandArg("install") {
		t.Fatalf("apt-get install ran after the metadata read failed: %#v", runner.commands)
	}
}

// TestExactVersionDependencies checks which dependencies count as exact-version requirements.
func TestExactVersionDependencies(t *testing.T) {
	tests := []struct {
		name    string
		stanzas string
		want    []string
	}{
		{name: "exact pins only", stanzas: "Depends: a (= 1:2-1), b (>= 1:2-1), c, d (= 1:2-2)\n", want: []string{"a"}},
		{name: "pre-depends", stanzas: "Pre-Depends: e (= 1:2-1)\n", want: []string{"e"}},
		{name: "alternatives left to apt", stanzas: "Depends: e (= 1:2-1) | f (= 1:2-1), g (= 1:2-1)\n", want: []string{"g"}},
		{name: "architecture suffix", stanzas: "Depends: k:amd64 (= 1:2-1)\n", want: []string{"k"}},
		{name: "repeats across stanzas", stanzas: "Depends: a (= 1:2-1)\n\nDepends: a (= 1:2-1), h (= 1:2-1)\n", want: []string{"a", "h"}},
		{name: "other fields", stanzas: "Recommends: i (= 1:2-1)\nPackage: j\n", want: nil},
		{name: "continuation lines", stanzas: "Depends: a (= 1:2-1),\n b (= 1:2-1),\n\tc (= 1:2-1)\nRecommends: d (= 1:2-1),\n e (= 1:2-1)\n", want: []string{"a", "b", "c"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := exactVersionDependencies(test.stanzas, "1:2-1"); !slices.Equal(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}
