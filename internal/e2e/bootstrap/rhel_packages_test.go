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
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// rhelStateRunner models repository and installed-package state across bootstrap passes.
type rhelStateRunner struct {
	installed   bool
	installRuns int
}

// Run implements the commands used by an exact RHEL DCGM transaction.
func (r *rhelStateRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	joined := commandKey(command)
	if strings.Contains(joined, "command -v apt-get") {
		return e2eexec.Result{ExitCode: 1}
	}
	if strings.Contains(joined, "command -v dnf") {
		return e2eexec.Result{}
	}
	if command.Name == "rpm" {
		if !r.installed {
			return e2eexec.Result{ExitCode: 1}
		}
		return e2eexec.Result{Stdout: []byte(strings.Join([]string{
			"datacenter-gpu-manager-4-core|1|4.6.1|1|x86_64",
			"datacenter-gpu-manager-4-proprietary|1|4.6.1|1|x86_64",
		}, "\n"))}
	}
	if command.Name == "curl" {
		return writeRHELRepositoryDownload(command)
	}
	if strings.Contains(joined, "repoquery") {
		name := "datacenter-gpu-manager-4-core"
		if strings.Contains(joined, "proprietary") {
			name = "datacenter-gpu-manager-4-proprietary"
		}
		return e2eexec.Result{Stdout: []byte(name + "|1|4.6.1|1|x86_64|cuda-rhel8-x86_64\n")}
	}
	if hasArg(command.Args, "datacenter-gpu-manager-4-core-1:4.6.1-1.x86_64") {
		r.installed = true
		r.installRuns++
	}
	if strings.Contains(joined, "libdcgm") && !r.installed {
		return e2eexec.Result{ExitCode: 1}
	}
	return e2eexec.Result{}
}

// writeRHELRepositoryDownload writes the repository fixture requested by curl.
func writeRHELRepositoryDownload(command e2eexec.Command) e2eexec.Result {
	var destination, content string
	for index, arg := range command.Args {
		if arg == "-o" && index+1 < len(command.Args) {
			destination = command.Args[index+1]
		}
		if arg == rhelCUDARepositoryURL {
			content = rhelCUDARepository
		}
	}
	if destination == "" || content == "" {
		return e2eexec.Result{ExitCode: 1}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return e2eexec.Result{ExitCode: 1, Err: err}
	}
	if err := os.WriteFile(destination, []byte(content), 0o600); err != nil {
		return e2eexec.Result{ExitCode: 1, Err: err}
	}
	return e2eexec.Result{}
}

// RHEL repository fixtures model the approved upstream trust configuration.
const (
	rhelCUDARepositoryURL = "https://developer.download.nvidia.com/compute/cuda/repos/rhel8/x86_64/cuda-rhel8.repo"
	rhelCUDARepository    = "[cuda-rhel8-x86_64]\nbaseurl=" + cudaRHELBaseURL + "\ngpgcheck=1\ngpgkey=" + cudaRHELGPGKeyURL + "\n"
	rhelToolkitRepository = "[nvidia-container-toolkit]\nbaseurl=" + toolkitRHELStableBaseURL + "\nrepo_gpgcheck=1\ngpgcheck=0\ngpgkey=" + toolkitRHELGPGKeyURL + "\n\n[nvidia-container-toolkit-experimental]\nbaseurl=" + toolkitRHELExperimentalURL + "\nrepo_gpgcheck=1\ngpgcheck=0\nenabled=0\ngpgkey=" + toolkitRHELGPGKeyURL + "\n"
)

// TestValidateRHELHostOSUsesSupportedHost verifies the supported RHEL version and architecture.
func TestValidateRHELHostOSUsesSupportedHost(t *testing.T) {
	supported := HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}
	if _, err := validateRHELHostOS(supported); err != nil {
		t.Fatal(err)
	}

	for _, host := range []HostOS{
		{Distribution: "rocky", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		{Distribution: "rhel", VersionID: "8.9", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		{Distribution: "rhel", VersionID: "8.10", Architecture: "arm64", NVIDIARepositoryArch: "sbsa"},
	} {
		if _, err := validateRHELHostOS(host); err == nil {
			t.Fatalf("validateRHELHostOS(%#v) succeeded, want error", host)
		}
	}
}

// TestNewHostPackageInstallerPropagatesRHELMatrixErrors verifies installer selection diagnostics.
func TestNewHostPackageInstallerPropagatesRHELMatrixErrors(t *testing.T) {
	host := HostOS{Distribution: "rhel", VersionID: "9.0", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}
	if _, err := newHostPackageInstaller(host, io.Discard, &fakeRunner{}); err == nil {
		t.Fatal("newHostPackageInstaller() succeeded for unsupported RHEL tuple")
	}
}

// TestRHELInstallPackagesConfiguresCompleteTransactions verifies repository and package ownership.
func TestRHELInstallPackagesConfiguresCompleteTransactions(t *testing.T) {
	requirements := PackageRequirements{
		Docker: true, Buildx: true, ContainerToolkit: true,
		HostDCGM: true, DCGMDevelopment: true, DCGMVersion: "4.6.1", ContainerToolkitVersion: "1.19.1-1",
	}
	runner := &fakeRunner{
		results: rhelResolutionResults(requirements, "1"),
		downloads: map[string][]byte{
			toolkitRHELRepositoryURL: []byte(rhelToolkitRepository),
			rhelCUDARepositoryURL:    []byte(rhelCUDARepository),
		},
	}
	packages := rhelPackageInstaller{
		hostOS: HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		stdout: io.Discard,
		runner: runner,
	}

	if err := packages.InstallPackages(context.Background(), requirements); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin",
		"nvidia-container-toolkit-1.19.1-1",
		"nvidia-container-toolkit-base-1.19.1-1",
		"libnvidia-container-tools-1.19.1-1",
		"libnvidia-container1-1.19.1-1",
		"datacenter-gpu-manager-4-core-1:4.6.1-1.x86_64",
		"datacenter-gpu-manager-4-proprietary-1:4.6.1-1.x86_64",
		"datacenter-gpu-manager-4-devel-1:4.6.1-1.x86_64",
	} {
		if !runner.hasCommandArg(want) {
			t.Fatalf("missing RHEL package %q: %#v", want, runner.commands)
		}
	}
	for _, want := range []string{dockerRHELRepositoryURL, toolkitRHELRepositoryURL, rhelCUDARepositoryURL} {
		if !runner.hasCommandArgContaining(want) && !runner.hasStdinContaining(want) {
			t.Fatalf("missing repository %q: %#v", want, runner.commands)
		}
	}
}

// TestRHELInstallerRepairsMissingDCGMIdempotently reproduces the missing-repository package failure.
func TestRHELInstallerRepairsMissingDCGMIdempotently(t *testing.T) {
	runner := &rhelStateRunner{}
	reads := 0
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			reads++
			return HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall, Dependencies: config.Dependencies{DCGMVersion: "4.6.1"}}
	requirements := DependencyRequirements{HostDCGM: true}

	if err := bootstrap.Run(context.Background(), opts, requirements); err != nil {
		t.Fatal(err)
	}
	if !runner.installed || runner.installRuns != 1 {
		t.Fatalf("first pass state = %#v, want one exact DCGM install", runner)
	}
	if err := bootstrap.Run(context.Background(), opts, requirements); err != nil {
		t.Fatal(err)
	}
	if runner.installRuns != 1 || reads != 2 {
		t.Fatalf("second pass installs = %d, host reads = %d", runner.installRuns, reads)
	}
}

// rhelDCGMCase is one readiness scenario for the RHEL DCGM check.
type rhelDCGMCase struct {
	name string
	// runtimeOnly drops the development package from the requirements.
	runtimeOnly bool
	installed   e2eexec.Result
	// offeredPackages lists the packages whose 4.6.1-1 identity the approved repository offers.
	offeredPackages []string
	// missingLibrary names a library the dynamic loader does not see.
	missingLibrary  string
	wantRuntime     bool
	wantDevelopment bool
	wantReason      string
	wantError       string
}

// TestRHELCheckDCGMComparesInstalledIdentityWithApprovedRepository covers each readiness condition
// and the message of each failure.
func TestRHELCheckDCGMComparesInstalledIdentityWithApprovedRepository(t *testing.T) {
	all := rhelDCGMPackageNames(true)
	runtimeOnly := rhelDCGMPackageNames(false)
	allOne := e2eexec.Result{Stdout: []byte(rhelInstalledDCGM("4.6.1|1", "4.6.1|1", "4.6.1|1"))}
	tests := []rhelDCGMCase{
		// A development package that is not required counts as ready.
		{name: "runtime only ready", runtimeOnly: true, installed: e2eexec.Result{Stdout: []byte(rhelInstalledDCGM("4.6.1|1", "4.6.1|1", ""))}, offeredPackages: runtimeOnly, wantRuntime: true, wantDevelopment: true},
		{name: "installed identity offered", installed: allOne, offeredPackages: all, wantRuntime: true, wantDevelopment: true},
		{name: "not installed", installed: e2eexec.Result{ExitCode: 1, Stdout: []byte(rhelNotInstalledLines(all...))}, wantReason: "DCGM packages are not installed: datacenter-gpu-manager-4-core, datacenter-gpu-manager-4-proprietary"},
		{
			name: "development not installed",
			installed: e2eexec.Result{
				ExitCode: 1,
				Stdout:   []byte(rhelInstalledDCGM("4.6.1|1", "4.6.1|1", "") + rhelNotInstalledLines(rhelDCGMDevelopmentPackage)),
			},
			offeredPackages: all, wantRuntime: true, wantReason: "DCGM packages are not installed: " + rhelDCGMDevelopmentPackage,
		},
		{name: "mixed releases", installed: e2eexec.Result{Stdout: []byte(rhelInstalledDCGM("4.6.1|1", "4.6.1|1", "4.6.1|2"))}, offeredPackages: all, wantRuntime: true, wantReason: "different versions"},
		{name: "other upstream version", installed: e2eexec.Result{Stdout: []byte(rhelInstalledDCGM("4.5.3|1", "4.5.3|1", "4.5.3|1"))}, offeredPackages: all, wantReason: "is not the pinned upstream version 4.6.1"},
		{name: "revision not offered", installed: allOne, wantReason: "is not offered by the approved NVIDIA CUDA repository"},
		{name: "development revision not offered", installed: allOne, offeredPackages: runtimeOnly, wantRuntime: true, wantReason: "datacenter-gpu-manager-4-devel-1:4.6.1-1.x86_64 is not offered"},
		{name: "query failure", installed: e2eexec.Result{ExitCode: 1, Stderr: []byte("error: cannot open Packages database\n")}, offeredPackages: all, wantError: "query installed DCGM packages"},
		{name: "query failure without message", installed: e2eexec.Result{ExitCode: 1}, offeredPackages: all, wantError: "without package records"},
		{name: "library not visible", installed: allOne, offeredPackages: all, missingLibrary: "libdcgm.so", wantReason: dcgmLibraryNotVisible},
		{name: "injection library not visible", installed: allOne, offeredPackages: all, missingLibrary: "libnvml_injection.so", wantRuntime: true, wantReason: dcgmInjectionLibraryNotVisible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, err := checkRHELDCGM(test)

			assertDCGMStatus(t, status, err, test.wantRuntime, test.wantDevelopment, test.wantReason, test.wantError)
		})
	}
}

// TestRHELCheckDCGMKeepsFailedRepositoryQueryAsError separates a failed inspection from a not-ready package.
func TestRHELCheckDCGMKeepsFailedRepositoryQueryAsError(t *testing.T) {
	requirements := PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}
	runner := &fakeRunner{results: rhelRepositoryQueryFailure(requirements)}
	packages := rhelPackageInstaller{stdout: io.Discard, runner: runner}

	status, err := packages.CheckPackages(context.Background(), requirements)

	if err == nil || !strings.Contains(err.Error(), "repo unreachable") || status.HostDCGMReason != "" {
		t.Fatalf("CheckPackages() = %#v, %v; want inspection error", status, err)
	}
}

// rhelRepositoryQueryFailure returns fixtures where the installed packages are fine but dnf repoquery fails.
func rhelRepositoryQueryFailure(requirements PackageRequirements) map[string]e2eexec.Result {
	results := map[string]e2eexec.Result{
		commandKey(rhelInstalledQuery(requirements)): {Stdout: []byte(rhelInstalledDCGM("4.6.1|1", "4.6.1|1", "4.6.1|1"))},
	}
	for _, name := range rhelDCGMPackageNames(false) {
		results[commandKey(rhelOfferedCommand(name, "1", "4.6.1", "1"))] = e2eexec.Result{ExitCode: 1, Stderr: []byte("repo unreachable")}
	}

	return results
}

// rhelNotInstalledLines returns the rpm lines for packages that are absent.
func rhelNotInstalledLines(names ...string) string {
	var lines string
	for _, name := range names {
		lines += "package " + name + " is not installed\n"
	}

	return lines
}

// rhelInstalledDCGM returns rpm output for the given "version|release" pairs of the three DCGM
// packages; an empty pair leaves that package out.
func rhelInstalledDCGM(core, proprietary, development string) string {
	var lines string
	for _, entry := range [][2]string{
		{"datacenter-gpu-manager-4-core", core},
		{"datacenter-gpu-manager-4-proprietary", proprietary},
		{rhelDCGMDevelopmentPackage, development},
	} {
		if entry[1] != "" {
			lines += entry[0] + "|1|" + entry[1] + "|x86_64\n"
		}
	}

	return lines
}

// checkRHELDCGM runs the RHEL DCGM check with fixed rpm output and the repository offering the listed packages.
func checkRHELDCGM(test rhelDCGMCase) (PackageStatus, error) {
	requirements := PackageRequirements{HostDCGM: true, DCGMDevelopment: !test.runtimeOnly, DCGMVersion: "4.6.1"}
	results := map[string]e2eexec.Result{commandKey(rhelInstalledQuery(requirements)): test.installed}
	for _, name := range rhelDCGMPackageNames(true) {
		result := e2eexec.Result{}
		if slices.Contains(test.offeredPackages, name) {
			result.Stdout = []byte(name + "|1|4.6.1|1|x86_64|cuda-rhel8-x86_64\n")
		}
		results[commandKey(rhelOfferedCommand(name, "1", "4.6.1", "1"))] = result
	}

	packages := rhelPackageInstaller{stdout: io.Discard, runner: &fakeRunner{results: results, resultMatcher: libraryMatcher(test.missingLibrary)}}

	return packages.CheckPackages(context.Background(), requirements)
}

// TestRHELObservePackagesChecksRuntimeCommands verifies non-DCGM package observations.
func TestRHELObservePackagesChecksRuntimeCommands(t *testing.T) {
	packages := rhelPackageInstaller{stdout: io.Discard, runner: &fakeRunner{}}
	status, err := packages.CheckPackages(context.Background(), PackageRequirements{
		Docker: true, Buildx: true, ContainerToolkit: true, ContainerToolkitVersion: "1.19.1-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Docker || !status.Buildx || !status.ContainerToolkit {
		t.Fatalf("CheckPackages() = %#v, want all runtime commands", status)
	}
}

// TestRHELDockerSocketACLMapsToACLPackage keeps the access-repair package
// independent from Docker engine installation.
func TestRHELDockerSocketACLMapsToACLPackage(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"rpm -q acl": {ExitCode: 1},
	}}
	packages := rhelPackageInstaller{stdout: io.Discard, runner: runner}
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
	if !runner.hasCommandArg("acl") || runner.hasCommandArg("docker-ce") ||
		runner.hasCommandArg("dnf-plugins-core") || runner.hasCommandArg("config-manager") ||
		runner.hasCommandArg(dockerRHELRepositoryURL) {
		t.Fatalf("ACL package install did not stay scoped to access repair: %#v", runner.commands)
	}
}

func TestRHELContainerToolkitRequiresExactPackageClosure(t *testing.T) {
	requirements := PackageRequirements{ContainerToolkit: true, ContainerToolkitVersion: "1.19.1-1"}
	query := e2eexec.Command{
		Name: "rpm",
		Args: append([]string{"-q", "--queryformat", rpmInstalledIdentityFormat}, rhelContainerToolkitPackageNames()...),
	}
	for _, test := range []struct {
		name     string
		packages []byte
		want     bool
	}{
		{name: "exact", packages: testContainerToolkitRPMPackages("1.19.1", "1"), want: true},
		{name: "release drift", packages: testContainerToolkitRPMPackages("1.19.1", "2"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]e2eexec.Result{
				commandKey(query): {Stdout: test.packages},
			}}
			status, err := (&rhelPackageInstaller{stdout: io.Discard, runner: runner}).CheckPackages(context.Background(), requirements)
			if err != nil {
				t.Fatal(err)
			}
			if status.ContainerToolkit != test.want {
				t.Fatalf("Container Toolkit ready = %t, want %t", status.ContainerToolkit, test.want)
			}
		})
	}
}

// TestRHELRepositoryTransactionsPropagateFailures verifies every repository and package error is returned.
func TestRHELRepositoryTransactionsPropagateFailures(t *testing.T) {
	host := HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}
	tests := []struct {
		name         string
		requirements PackageRequirements
		runner       *fakeRunner
		want         string
	}{
		{
			name:         "Docker prerequisites",
			requirements: PackageRequirements{Docker: true},
			runner: &fakeRunner{results: map[string]e2eexec.Result{
				commandKey(sudoCommand("dnf", "install", "-y", "dnf-plugins-core")): {ExitCode: 1, Stderr: []byte("base repository failure")},
			}},
			want: "BaseOS/AppStream",
		},
		{
			name:         "DCGM prerequisites",
			requirements: PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"},
			runner: &fakeRunner{results: map[string]e2eexec.Result{
				commandKey(sudoCommand("dnf", "install", "-y", "dnf-plugins-core")): {ExitCode: 1, Stderr: []byte("base repository failure")},
			}},
			want: "DCGM repository tools",
		},
		{
			name:         "toolkit trust",
			requirements: PackageRequirements{ContainerToolkit: true, ContainerToolkitVersion: "1.19.1-1"},
			runner: &fakeRunner{downloads: map[string][]byte{
				toolkitRHELRepositoryURL: []byte(strings.ReplaceAll(rhelToolkitRepository, "repo_gpgcheck=1", "repo_gpgcheck=0")),
			}},
			want: "signature verification",
		},
		{
			name:         "CUDA trust",
			requirements: PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"},
			runner: &fakeRunner{downloads: map[string][]byte{
				rhelCUDARepositoryURL: []byte(strings.ReplaceAll(rhelCUDARepository, "gpgcheck=1", "gpgcheck=0")),
			}},
			want: "signature verification",
		},
		{
			name:         "RHEL entitlement",
			requirements: PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"},
			runner: &fakeRunner{
				results: map[string]e2eexec.Result{
					commandKey(sudoCommand("dnf", "makecache")): {ExitCode: 1, Stderr: []byte("metadata failure")},
				},
				downloads: map[string][]byte{rhelCUDARepositoryURL: []byte(rhelCUDARepository)},
			},
			want: "entitlement",
		},
		{
			name:         "repository download",
			requirements: PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"},
			runner: &fakeRunner{results: map[string]e2eexec.Result{
				"curl": {ExitCode: 22, Stderr: []byte("download failure")},
			}},
			want: "download failure",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packages := rhelPackageInstaller{hostOS: host, stdout: io.Discard, runner: test.runner}
			err := packages.InstallPackages(context.Background(), test.requirements)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("InstallPackages() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestRHELResolveDCGMRejectsMissingAndAmbiguousPins verifies deterministic resolution.
func TestRHELResolveDCGMRejectsMissingAndAmbiguousPins(t *testing.T) {
	requirements := PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}
	query := commandKey(rhelResolveCommand("datacenter-gpu-manager-4-core", requirements.DCGMVersion))
	tests := []struct {
		name   string
		result e2eexec.Result
	}{
		{name: "missing", result: e2eexec.Result{}},
		{name: "ambiguous", result: e2eexec.Result{Stdout: []byte(strings.Join([]string{
			"datacenter-gpu-manager-4-core|1|4.6.1|1|x86_64|cuda-rhel8-x86_64",
			"datacenter-gpu-manager-4-core|1|4.6.1|2|x86_64|cuda-rhel8-x86_64",
		}, "\n"))}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{results: map[string]e2eexec.Result{query: test.result}}
			packages := rhelPackageInstaller{stdout: io.Discard, runner: runner}
			_, err := packages.resolveDCGM(context.Background(), requirements)
			if err == nil || !strings.Contains(err.Error(), "do not provide one approved") {
				t.Fatalf("resolveDCGM() error = %v, want approved pin failure", err)
			}
		})
	}
}

// TestRHELResolveDCGMRejectsUnapprovedRepository validates the parsed repository identity.
func TestRHELResolveDCGMRejectsUnapprovedRepository(t *testing.T) {
	requirements := PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}
	query := commandKey(rhelResolveCommand("datacenter-gpu-manager-4-core", requirements.DCGMVersion))
	runner := &fakeRunner{results: map[string]e2eexec.Result{query: {Stdout: []byte(
		"datacenter-gpu-manager-4-core|1|4.6.1|1|x86_64|hostile\n")}}}
	if _, err := (&rhelPackageInstaller{runner: runner}).resolveDCGM(context.Background(), requirements); err == nil {
		t.Fatal("unapproved RHEL repository candidate was accepted")
	}
}

// TestValidateRPMRepositoryRequiresTrust verifies unsigned and plaintext files fail closed.
func TestValidateRPMRepositoryRequiresTrust(t *testing.T) {
	approved := map[string]rpmRepository{
		"approved": {
			BaseURL:  "https://packages.example.test/rhel8",
			GPGKey:   "https://packages.example.test/key.pub",
			GPGCheck: "1",
		},
	}
	valid := "[approved]\nbaseurl=https://packages.example.test/rhel8\ngpgcheck=1\ngpgkey=https://packages.example.test/key.pub\n"
	if err := validateRPMRepository([]byte(valid), "test", approved); err != nil {
		t.Fatalf("validateRPMRepository(valid) = %v", err)
	}

	for _, content := range []string{
		"[approved]\nbaseurl=https://packages.example.test/rhel8\ngpgkey=https://packages.example.test/key.pub\n# gpgcheck=1\n",
		"[approved]\nbaseurl=https://packages.example.test/rhel8\ngpgcheck=0\nrepo_gpgcheck=1\ngpgkey=https://packages.example.test/key.pub\n",
		"[approved]\nbaseurl=https://packages.example.test/rhel8\ngpgcheck=1\ngpgcheck=0\ngpgkey=https://packages.example.test/key.pub\n",
		"[approved]\nbaseurl=https://packages.example.test/rhel8\ngpgcheck=1\ngpgcheck=1\ngpgkey=https://packages.example.test/key.pub\n",
		"[approved]\nbaseurl=https://hostile.example.test/rhel8\ngpgcheck=1\ngpgkey=https://packages.example.test/key.pub\n",
		"[approved]\nbaseurl=https://packages.example.test/rhel8\nmirrorlist=https://hostile.example.test/mirrors\ngpgcheck=1\ngpgkey=https://packages.example.test/key.pub\n",
		"[approved]\nbaseurl=https://packages.example.test/rhel8\nmetalink=https://hostile.example.test/metalink\ngpgcheck=1\ngpgkey=https://packages.example.test/key.pub\n",
		valid + "[unexpected]\nbaseurl=https://packages.example.test/rhel8\ngpgcheck=1\ngpgkey=https://packages.example.test/key.pub\n",
	} {
		if err := validateRPMRepository([]byte(content), "test", approved); err == nil {
			t.Fatalf("validateRPMRepository(%q) succeeded, want error", content)
		}
	}
}

// TestParseRPMIdentitiesRejectsMalformedOutput verifies native query drift is explicit.
func TestParseRPMIdentitiesRejectsMalformedOutput(t *testing.T) {
	if _, err := parseRPMIdentities([]byte("not|a|complete|identity\n")); err == nil {
		t.Fatal("parseRPMIdentities() succeeded, want error")
	}
	identity := rpmIdentity{Name: "package", Epoch: "1", Version: "4.6.1", Release: "1", Architecture: "x86_64"}
	if got := identity.NEVRA(); got != "package-1:4.6.1-1.x86_64" {
		t.Fatalf("NEVRA() = %q", got)
	}
}

// rhelResolutionResults returns exact one-candidate repository fixtures.
func rhelResolutionResults(requirements PackageRequirements, release string) map[string]e2eexec.Result {
	results := make(map[string]e2eexec.Result)
	for _, name := range rhelDCGMPackageNames(requirements.DCGMDevelopment) {
		identity := name + "|1|" + requirements.DCGMVersion + "|" + release + "|x86_64|cuda-rhel8-x86_64\n"
		results[commandKey(rhelResolveCommand(name, requirements.DCGMVersion))] = e2eexec.Result{Stdout: []byte(identity)}
	}
	return results
}

// rhelResolveCommand returns the installer's exact available-package query.
func rhelResolveCommand(name, version string) e2eexec.Command {
	return e2eexec.Command{
		Name: "dnf",
		Args: []string{"-q", "repoquery", "--repoid", "cuda-rhel8-x86_64", "--available", "--latest-limit", "1", "--qf", rpmIdentityFormat, name + "-" + version + "*"},
	}
}

// rhelOfferedCommand returns the installer's query for one installed identity in the approved repository.
func rhelOfferedCommand(name, epoch, version, release string) e2eexec.Command {
	return e2eexec.Command{
		Name: "dnf",
		Args: []string{"-q", "repoquery", "--repoid", "cuda-rhel8-x86_64", "--available", "--qf", rpmIdentityFormat, name + "-" + epoch + ":" + version + "-" + release + ".x86_64"},
	}
}

// rhelInstalledQuery returns the installer's exact installed-package query.
func rhelInstalledQuery(requirements PackageRequirements) e2eexec.Command {
	return e2eexec.Command{
		Name: "rpm",
		Args: append([]string{"-q", "--queryformat", rpmInstalledIdentityFormat}, rhelDCGMPackageNames(requirements.DCGMDevelopment)...),
	}
}
