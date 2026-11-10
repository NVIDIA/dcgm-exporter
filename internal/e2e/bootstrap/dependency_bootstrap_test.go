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
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// dockerStateRunner models Docker package state without depending on command order.
type dockerStateRunner struct {
	installed bool
	mutations int
}

// Run observes commands and makes the Docker package transaction change state.
func (r *dockerStateRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	if command.Name == "sh" && hasArg(command.Args, "command -v docker") {
		if !r.installed {
			return e2eexec.Result{ExitCode: 1}
		}
		return e2eexec.Result{}
	}
	if hasArg(command.Args, "docker.io") {
		r.installed = true
		r.mutations++
	}
	return e2eexec.Result{}
}

// TestDependencyBootstrapSkipsHostDetectionWithoutMissingNativePackages verifies irrelevant paths.
func TestDependencyBootstrapSkipsHostDetectionWithoutMissingNativePackages(t *testing.T) {
	reads := 0
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: &fakeRunner{},
		DetectHostOS: func() (HostOS, error) {
			reads++
			return HostOS{}, nil
		},
	}

	if err := bootstrap.Run(context.Background(), config.TestOptions{}, DependencyRequirements{}); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Run(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapInstall}, DependencyRequirements{Docker: true}); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("host identity reads = %d, want 0", reads)
	}
}

// TestDependencyBootstrapRejectsUnsupportedHostBeforeMutation verifies an unsupported host fails before any change command runs.
func TestDependencyBootstrapRejectsUnsupportedHostBeforeMutation(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c command -v docker": {ExitCode: 1},
	}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "debian", VersionID: "12", Architecture: "amd64"}, nil
		},
	}

	err := bootstrap.Run(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapInstall}, DependencyRequirements{Docker: true})
	want := `bootstrap host_os=debian-12-amd64 component=docker version=not-applicable phase=select-installer: unsupported host distribution "debian"`
	if err == nil || err.Error() != want {
		t.Fatalf("DependencyBootstrap.Run() error = %q, want %q", err, want)
	}
	if runner.hasCommandArg("install") || runner.hasCommandArg("update") {
		t.Fatalf("unsupported host triggered a change command: %#v", runner.commands)
	}
}

// TestDependencyBootstrapStopsBeforePackagesWhenGPUDriverIsUnhealthy verifies that a failed driver check stops package work.
func TestDependencyBootstrapStopsBeforePackagesWhenGPUDriverIsUnhealthy(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"nvidia-smi --query-gpu=driver_version --format=csv,noheader": {ExitCode: 1, Stderr: []byte("driver communication failed")},
	}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			t.Fatal("host detection ran before the GPU health check")
			return HostOS{}, nil
		},
	}
	err := bootstrap.Run(context.Background(), config.TestOptions{
		BootstrapMode: config.BootstrapInstall,
		Dependencies:  config.Dependencies{ContainerToolkitVersion: "1.19.1-1"},
	}, DependencyRequirements{Docker: true, ContainerToolkit: true, GPURuntime: true})
	if err == nil || !strings.Contains(err.Error(), "driver communication failed") {
		t.Fatalf("DependencyBootstrap.Run() error = %v, want GPU health error", err)
	}
	if runner.hasCommandName("apt-get") || runner.hasCommandName("dnf") || runner.hasCommandName("nvidia-ctk") {
		t.Fatalf("unhealthy driver triggered setup: %#v", runner.commands)
	}
}

// TestDependencyBootstrapSkipsGPURuntimeChecksWhenRequested verifies that gpu-runtime=skip does not run GPU checks.
func TestDependencyBootstrapSkipsGPURuntimeChecksWhenRequested(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"nvidia-smi --query-gpu=driver_version --format=csv,noheader": {ExitCode: 1, Stderr: []byte("driver communication failed")},
	}}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner}
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapCheck,
		Dependencies:  config.Dependencies{ContainerToolkitVersion: "1.19.1-1"},
		BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{
			config.BootstrapGPURuntime: config.BootstrapSkip,
		},
	}

	requirements := DependencyRequirements{Docker: true, ContainerToolkit: true, GPURuntime: true}
	if err := bootstrap.Run(context.Background(), opts, requirements); err != nil {
		t.Fatal(err)
	}
	if err := CheckGPURuntime(context.Background(), io.Discard, runner, opts, PreparedDependencies{Requirements: requirements}); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandName("nvidia-smi") || runner.hasCommandArg("--gpus") || runner.hasCommandName("nvidia-ctk") {
		t.Fatalf("gpu-runtime=skip ran runtime checks: %#v", runner.commands)
	}
}

// TestDependencyBootstrapDoesNotRunGPURuntimeForToolkitOnly verifies toolkit package checks do not use the smoke image.
func TestDependencyBootstrapDoesNotRunGPURuntimeForToolkitOnly(t *testing.T) {
	runner := &fakeRunner{}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner}
	if err := bootstrap.Run(
		context.Background(),
		config.TestOptions{BootstrapMode: config.BootstrapCheck, Dependencies: config.Dependencies{ContainerToolkitVersion: "1.19.1-1"}},
		DependencyRequirements{Docker: true, ContainerToolkit: true},
	); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandName("nvidia-smi") || runner.hasCommandArg("--gpus") || runner.hasCommandArg("pull") {
		t.Fatalf("toolkit-only bootstrap ran GPU smoke work: %#v", runner.commands)
	}
}

// TestCheckGPURuntimeReportsPreparedHostOS verifies the later smoke check keeps the host detected during preparation.
func TestCheckGPURuntimeReportsPreparedHostOS(t *testing.T) {
	image := "nvcr.io/nvidia/cuda:13.3.1-base-ubuntu24.04"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker run --pull=never --rm --gpus all " + image + " nvidia-smi": {
			ExitCode: 1,
			Stderr:   []byte("GPU runtime is unavailable"),
		},
	}}
	opts := config.TestOptions{
		BootstrapMode:             config.BootstrapCheck,
		ContainerToolkitTestImage: image,
	}
	prepared := PreparedDependencies{
		Requirements: DependencyRequirements{GPURuntime: true},
		HostOS:       "rhel-9-amd64",
	}

	err := CheckGPURuntime(context.Background(), io.Discard, runner, opts, prepared)
	if err == nil || !strings.Contains(err.Error(), "bootstrap host_os=rhel-9-amd64 component=gpu-runtime") ||
		!strings.Contains(err.Error(), "GPU runtime is unavailable") {
		t.Fatalf("CheckGPURuntime() error = %v, want host and smoke error", err)
	}
}

// TestDependencyBootstrapReportsHostDetectionFailure verifies stable context before a host tuple is known.
func TestDependencyBootstrapReportsHostDetectionFailure(t *testing.T) {
	bootstrap := DependencyBootstrap{
		Output: io.Discard,
		CommandRunner: &fakeRunner{results: map[string]e2eexec.Result{
			"sh -c command -v docker": {ExitCode: 1},
		}},
		DetectHostOS: func() (HostOS, error) {
			return HostOS{}, errors.New("read /etc/os-release: permission denied")
		},
	}

	err := bootstrap.Run(context.Background(), config.TestOptions{}, DependencyRequirements{Docker: true})
	want := "bootstrap host_os=unknown component=docker version=not-applicable phase=detect-host: read /etc/os-release: permission denied"
	if err == nil || err.Error() != want {
		t.Fatalf("DependencyBootstrap.Run() error = %q, want %q", err, want)
	}
}

// TestDependencyBootstrapReportsPortableVerificationFailure verifies an installed tool can fail without forcing host detection.
func TestDependencyBootstrapReportsPortableVerificationFailure(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker info": {ExitCode: 1},
	}}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner}

	err := bootstrap.Run(
		context.Background(),
		config.TestOptions{BootstrapMode: config.BootstrapCheck},
		DependencyRequirements{Docker: true},
	)
	want := "bootstrap host_os=not-detected component=docker version=not-applicable phase=verify: docker socket access is unavailable for the current user; rerun with --bootstrap-component docker=install"
	if err == nil || err.Error() != want {
		t.Fatalf("DependencyBootstrap.Run() error = %q, want %q", err, want)
	}
}

// TestDockerCLIRequirementAvoidsDaemonSetup keeps a client-only prerequisite from probing or repairing the daemon.
func TestDockerCLIRequirementAvoidsDaemonSetup(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{"docker info": {ExitCode: 1}}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			t.Fatal("host detection ran with an available Docker CLI")
			return HostOS{}, nil
		},
	}

	err := bootstrap.Run(
		context.Background(),
		config.TestOptions{BootstrapMode: config.BootstrapInstall},
		DependencyRequirements{DockerCLI: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Name == "docker" || command.Name == "systemctl" || command.Name == "apt-get" || command.Name == "dnf" ||
			hasArg(command.Args, "setfacl") {
			t.Fatalf("Docker CLI requirement performed daemon or package setup: %#v", runner.commands)
		}
	}
}

// TestDockerCLIRequirementChecksMissingClient reports a missing client in check mode without installing packages.
func TestDockerCLIRequirementChecksMissingClient(t *testing.T) {
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		"sh -c command -v docker": {{ExitCode: 1}, {ExitCode: 1}},
	}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}

	err := bootstrap.Run(
		context.Background(),
		config.TestOptions{BootstrapMode: config.BootstrapCheck},
		DependencyRequirements{DockerCLI: true},
	)
	if err == nil || !strings.Contains(err.Error(), "component=docker") || !strings.Contains(err.Error(), "phase=check") ||
		!strings.Contains(err.Error(), "missing Docker") {
		t.Fatalf("Docker CLI check error = %v, want a missing Docker client diagnostic", err)
	}
	if runner.hasCommandName("apt-get") || runner.hasCommandName("dnf") {
		t.Fatalf("Docker CLI check installed packages: %#v", runner.commands)
	}
}

// TestDockerCLIRequirementInstallsSupportedPackageWithoutDaemonRepair installs the supported client package without explicit daemon repair.
func TestDockerCLIRequirementInstallsSupportedPackageWithoutDaemonRepair(t *testing.T) {
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		"sh -c command -v docker": {{ExitCode: 1}, {ExitCode: 1}, {}},
	}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}

	err := bootstrap.Run(
		context.Background(),
		config.TestOptions{BootstrapMode: config.BootstrapInstall},
		DependencyRequirements{DockerCLI: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("docker.io") {
		t.Fatalf("Docker client installation did not use the supported Ubuntu package: %#v", runner.commands)
	}
	for _, command := range runner.commands {
		if command.Name == "docker" || command.Name == "systemctl" || hasArg(command.Args, "setfacl") {
			t.Fatalf("Docker CLI installation attempted explicit daemon repair: %#v", runner.commands)
		}
	}
}

// TestDependencyBootstrapInstallsDockerSocketACL verifies that a present Docker
// CLI with inaccessible user socket installs only the ACL package before repair.
func TestDependencyBootstrapInstallsDockerSocketACL(t *testing.T) {
	runner := &fakeRunner{resultSequences: map[string][]e2eexec.Result{
		"docker info": {
			{ExitCode: 1}, // Select the ACL package requirement.
			{ExitCode: 1}, // Docker is still unavailable before service repair.
			{ExitCode: 1}, // Apply the ACL after service repair.
			{},            // Verify the repaired current-user access.
		},
		"dpkg-query -W -f=${db:Status-Abbrev} acl": {
			{ExitCode: 1},
			{Stdout: []byte("ii")},
		},
	}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}

	if err := bootstrap.Run(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapInstall}, DependencyRequirements{Docker: true}); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("acl") || !runner.hasCommandName("setfacl") {
		t.Fatalf("Docker socket repair did not install and apply the ACL: %#v", runner.commands)
	}
	if runner.hasCommandArg("docker.io") {
		t.Fatalf("Docker socket repair reinstalled an available Docker CLI: %#v", runner.commands)
	}
}

// TestWithDockerSocketACLRequirementPreparesForRuntimeRepair verifies that a
// later GPU runtime repair can restore current-user Docker access after restart.
func TestWithDockerSocketACLRequirementPreparesForRuntimeRepair(t *testing.T) {
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall}
	requirements := withDockerSocketACLRequirement(
		context.Background(),
		&fakeRunner{results: map[string]e2eexec.Result{
			"sh -c command -v setfacl": {ExitCode: 1},
		}},
		opts,
		PackageRequirements{Docker: true},
		true,
		true,
	)

	if !requirements.DockerSocketACL {
		t.Fatalf("DockerSocketACL = false, want runtime repair prerequisite")
	}
}

// TestWithDockerSocketACLRequirementSkipsDockerProbesWithoutDockerInstall avoids Docker and ACL checks for unrelated package work.
func TestWithDockerSocketACLRequirementSkipsDockerProbesWithoutDockerInstall(t *testing.T) {
	runner := &fakeRunner{}
	requirements := withDockerSocketACLRequirement(
		context.Background(),
		runner,
		config.TestOptions{BootstrapMode: config.BootstrapInstall},
		PackageRequirements{HostDCGM: true},
		false,
		true,
	)

	if requirements.DockerSocketACL {
		t.Fatalf("DockerSocketACL = true, want false without Docker installation")
	}
	if runner.hasCommandName("docker") || runner.hasCommandArg("setfacl") {
		t.Fatalf("non-Docker requirements probed Docker access: %#v", runner.commands)
	}
}

func TestPrepareHostDCGMProbeUsesReachableDefaultInCheckMode(t *testing.T) {
	runner := &fakeRunner{}
	address, cleanup, err := PrepareHostDCGMProbe(
		context.Background(),
		io.Discard,
		runner,
		config.TestOptions{BootstrapMode: config.BootstrapCheck},
	)
	if err != nil {
		t.Fatal(err)
	}
	if address != "localhost" {
		t.Fatalf("hostengine address = %q, want localhost", address)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.hasCommandName("nv-hostengine") {
		t.Fatalf("check mode started a hostengine: %#v", runner.commands)
	}
}

func TestPrepareHostDCGMProbeOwnsIsolatedHostengine(t *testing.T) {
	runner := &fakeRunner{}
	address, cleanup, err := PrepareHostDCGMProbe(
		context.Background(),
		io.Discard,
		runner,
		config.TestOptions{BootstrapMode: config.BootstrapInstall},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatalf("hostengine address = %q, want isolated loopback endpoint", address)
	}
	if !runner.hasCommandName("nv-hostengine") || !runner.hasCommandArg("--pid") || !runner.hasCommandArg("-p") {
		t.Fatalf("isolated hostengine was not started: %#v", runner.commands)
	}
	if runner.hasCommandArg("--term") {
		t.Fatalf("isolated hostengine stopped before cleanup: %#v", runner.commands)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("--term") {
		t.Fatalf("isolated hostengine was not stopped: %#v", runner.commands)
	}
}

func TestPrepareHostDCGMProbeCheckDoesNotMutate(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"dcgmi discovery -l": {ExitCode: 1, Stderr: []byte("cannot connect")},
	}}
	_, _, err := PrepareHostDCGMProbe(
		context.Background(),
		io.Discard,
		runner,
		config.TestOptions{BootstrapMode: config.BootstrapCheck},
	)
	if err == nil || !strings.Contains(err.Error(), "cannot connect") ||
		!strings.Contains(err.Error(), "start nvidia-dcgm or use --bootstrap=install") {
		t.Fatalf("PrepareHostDCGMProbe() error = %v, want readiness guidance", err)
	}
	if runner.hasCommandName("nv-hostengine") {
		t.Fatalf("check mode mutated hostengine state: %#v", runner.commands)
	}
}

func TestPrepareHostDCGMProbeReportsProbeAndRollbackFailures(t *testing.T) {
	runner := &fakeRunner{
		resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
			switch {
			case command.Name == "dcgmi" && hasArg(command.Args, "--host"):
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("post-start connection failure")}, true
			case hasArg(command.Args, "--term"):
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("stop failed")}, true
			default:
				return e2eexec.Result{}, false
			}
		},
	}
	_, _, err := PrepareHostDCGMProbe(
		context.Background(),
		io.Discard,
		runner,
		config.TestOptions{BootstrapMode: config.BootstrapInstall},
	)
	if err == nil || !strings.Contains(err.Error(), "post-start connection failure") ||
		!strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("PrepareHostDCGMProbe() error = %v, want probe and rollback failures", err)
	}
	if !runner.hasCommandName("rm") && !runner.hasCommandArg("rm") {
		t.Fatalf("hostengine stop failure skipped PID cleanup: %#v", runner.commands)
	}
}

// TestDependencyBootstrapInstallIsIdempotent verifies a second pass makes no changes and does not read the host OS.
func TestDependencyBootstrapInstallIsIdempotent(t *testing.T) {
	runner := &dockerStateRunner{}
	reads := 0
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			reads++
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall}
	requirements := DependencyRequirements{Docker: true}

	if err := bootstrap.Run(context.Background(), opts, requirements); err != nil {
		t.Fatal(err)
	}
	if !runner.installed || runner.mutations != 1 {
		t.Fatalf("first pass state = %#v, want one Docker installation", runner)
	}
	if err := bootstrap.Run(context.Background(), opts, requirements); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("host identity reads = %d, want 1", reads)
	}
	if runner.mutations != 1 {
		t.Fatalf("second pass mutations = %d, want 1 total", runner.mutations)
	}
}

// TestDependencyBootstrapValidatesDCGMVersionBeforeHostRead verifies incomplete pins fail early.
func TestDependencyBootstrapValidatesDCGMVersionBeforeHostRead(t *testing.T) {
	reads := 0
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: &fakeRunner{},
		DetectHostOS: func() (HostOS, error) {
			reads++
			return HostOS{}, nil
		},
	}

	err := bootstrap.Run(context.Background(), config.TestOptions{}, DependencyRequirements{HostDCGM: true})
	want := "bootstrap host_os=not-detected component=host-dcgm version=missing phase=validate-config: --dcgm-version/E2E_DCGM_VERSION is required"
	if err == nil || err.Error() != want {
		t.Fatalf("DependencyBootstrap.Run() error = %q, want %q", err, want)
	}
	if reads != 0 {
		t.Fatalf("host identity reads = %d, want 0", reads)
	}
}

func TestDependencyBootstrapValidatesContainerToolkitVersionBeforeHostRead(t *testing.T) {
	reads := 0
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: &fakeRunner{},
		DetectHostOS: func() (HostOS, error) {
			reads++
			return HostOS{}, nil
		},
	}

	err := bootstrap.Run(context.Background(), config.TestOptions{}, DependencyRequirements{ContainerToolkit: true})
	want := "bootstrap host_os=not-detected component=container-toolkit version=missing phase=validate-config: --container-toolkit-version/NVIDIA_CONTAINER_TOOLKIT_VERSION is required"
	if err == nil || err.Error() != want {
		t.Fatalf("DependencyBootstrap.Run() error = %q, want %q", err, want)
	}
	if reads != 0 {
		t.Fatalf("host identity reads = %d, want 0", reads)
	}
}

func TestPackageVersionMatchesRequestedPackageGroups(t *testing.T) {
	tests := []struct {
		name         string
		requirements PackageRequirements
		want         string
	}{
		{
			name: "container toolkit ignores unrelated DCGM pin",
			requirements: PackageRequirements{
				ContainerToolkit:        true,
				ContainerToolkitVersion: "1.20.0-1",
				DCGMVersion:             "4.5.3",
			},
			want: "1.20.0-1",
		},
		{
			name: "DCGM packages ignore unrelated container toolkit pin",
			requirements: PackageRequirements{
				HostDCGM:                true,
				DCGMVersion:             "4.5.3",
				ContainerToolkitVersion: "1.20.0-1",
			},
			want: "4.5.3",
		},
		{
			name: "mixed packages preserve component order",
			requirements: PackageRequirements{
				ContainerToolkit:        true,
				HostDCGM:                true,
				ContainerToolkitVersion: "1.20.0-1",
				DCGMVersion:             "4.5.3",
			},
			want: "1.20.0-1,4.5.3",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := packageVersion(test.requirements); got != test.want {
				t.Fatalf("packageVersion() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDependencyBootstrapCheckDoesNotMutateMissingDocker verifies check mode is read-only.
func TestDependencyBootstrapCheckDoesNotMutateMissingDocker(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c command -v docker": {ExitCode: 1},
	}}
	var output bytes.Buffer
	bootstrap := DependencyBootstrap{
		Output:        &output,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}

	err := bootstrap.Run(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapCheck}, DependencyRequirements{Docker: true})
	want := "bootstrap host_os=ubuntu-24.04-amd64 component=docker version=not-applicable phase=check: missing Docker; set that component to install to allow changes"
	if err == nil || err.Error() != want {
		t.Fatalf("DependencyBootstrap.Run() error = %q, want %q", err, want)
	}
	wantOutput := "[e2e] bootstrap: host_os=ubuntu-24.04-amd64 installer=ubuntu\n[e2e] bootstrap: package_status docker=blocked\n"
	if output.String() != wantOutput {
		t.Fatalf("DependencyBootstrap.Run() output = %q, want %q", output.String(), wantOutput)
	}
	if runner.hasCommandArg("docker.io") || runner.hasCommandArg("update") {
		t.Fatalf("check mode mutated the host: %#v", runner.commands)
	}
}

// TestDependencyBootstrapCheckDoesNotInstallReadyHostPackages verifies check mode does not install packages.
func TestDependencyBootstrapCheckDoesNotInstallReadyHostPackages(t *testing.T) {
	version := "1:4.6.1-1"
	available := e2eexec.Result{Stdout: []byte("package | " + version + " | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core":        available,
		"apt-cache madison datacenter-gpu-manager-4-proprietary": available,
		commandKey(dcgmPackageQueryCommand(false)): {Stdout: []byte(
			"ii  datacenter-gpu-manager-4-core=" + version + "\n" +
				"ii  datacenter-gpu-manager-4-proprietary=" + version + "\n")},
	}}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner, DetectHostOS: func() (HostOS, error) {
		return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
	}}
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapCheck,
		Dependencies:  config.Dependencies{DCGMVersion: "4.6.1"},
	}

	if err := bootstrap.Run(context.Background(), opts, DependencyRequirements{HostDCGM: true}); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.commands {
		if command.Name == "apt-get" || command.Name == "sudo" {
			t.Fatalf("check mode invoked a package change: %#v", runner.commands)
		}
	}
}

// TestDependencyBootstrapReportsBlockedDCGMWithReason verifies that DCGM packages at mixed versions are
// blocked in check mode with their reason, not reported as unknown.
func TestDependencyBootstrapReportsBlockedDCGMWithReason(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		commandKey(dcgmPackageQueryCommand(false)): {Stdout: []byte(
			"ii  datacenter-gpu-manager-4-core=1:4.6.1-1\nii  datacenter-gpu-manager-4-proprietary=1:4.6.1-2\n")},
	}}
	var output bytes.Buffer
	bootstrap := DependencyBootstrap{Output: &output, CommandRunner: runner, DetectHostOS: func() (HostOS, error) {
		return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
	}}
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapCheck,
		Dependencies:  config.Dependencies{DCGMVersion: "4.6.1"},
	}

	err := bootstrap.Run(context.Background(), opts, DependencyRequirements{HostDCGM: true})

	if err == nil || !strings.Contains(err.Error(), "different versions") {
		t.Fatalf("DependencyBootstrap.Run() error = %v, want mixed versions reason", err)
	}
	if !strings.Contains(output.String(), "host-dcgm=blocked") || strings.Contains(output.String(), "unknown") ||
		!strings.Contains(output.String(), "host-dcgm not ready: installed DCGM packages are at different versions") {
		t.Fatalf("package status = %q, want blocked with reason", output.String())
	}
}

// TestDependencyBootstrapInstallResolutionFailureKeepsReadyDocker verifies that a DCGM version lookup
// failure during install does not hide ready Docker state or reinstall Docker.
func TestDependencyBootstrapInstallResolutionFailureKeepsReadyDocker(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"apt-cache madison datacenter-gpu-manager-4-core": {ExitCode: 100, Stderr: []byte("E: broken unrelated source\n")},
	}}
	var output bytes.Buffer
	bootstrap := DependencyBootstrap{
		Output:        &output,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapInstall,
		Dependencies:  config.Dependencies{DCGMVersion: "4.6.1"},
	}
	err := bootstrap.Run(context.Background(), opts, DependencyRequirements{Docker: true, Buildx: true, HostDCGM: true})
	if err == nil || !strings.Contains(err.Error(), "E: broken unrelated source") {
		t.Fatalf("DependencyBootstrap.Run() error = %v, want raw DCGM source error", err)
	}
	if runner.hasCommandArg("docker.io") || runner.hasCommandArg("docker-buildx") {
		t.Fatalf("DCGM check failure tried to install ready Docker packages: %#v", runner.commands)
	}
	if !strings.Contains(output.String(), "package_status docker=ready buildx=ready host-dcgm=missing") {
		t.Fatalf("package status did not preserve ready Docker state:\n%s", output.String())
	}
}

// TestDependencyBootstrapSkipsNamedComponent verifies that a skipped dependency does not inspect the host.
func TestDependencyBootstrapSkipsNamedComponent(t *testing.T) {
	runner := &fakeRunner{}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			t.Fatal("host detection ran for a skipped component")
			return HostOS{}, nil
		},
	}
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapInstall,
		BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{
			config.BootstrapDocker: config.BootstrapSkip,
		},
	}
	if err := bootstrap.Run(context.Background(), opts, DependencyRequirements{Docker: true}); err != nil {
		t.Fatal(err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("skipped Docker component ran commands: %#v", runner.commands)
	}
}

// TestDependencyBootstrapNamedCheckDoesNotWritePackages verifies that check mode reports missing packages without installing them.
func TestDependencyBootstrapNamedCheckDoesNotWritePackages(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c command -v docker": {ExitCode: 1},
	}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapInstall,
		BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{
			config.BootstrapDocker: config.BootstrapCheck,
		},
	}
	if err := bootstrap.Run(context.Background(), opts, DependencyRequirements{Docker: true}); err == nil {
		t.Fatal("DependencyBootstrap.Run() accepted missing Docker in check mode")
	}
	if runner.hasCommandName("apt-get") || runner.hasCommandName("dnf") {
		t.Fatalf("named check mode wrote packages: %#v", runner.commands)
	}
}

// TestDependencyBootstrapRejectsSkippedDockerPrerequisite verifies that Docker cannot be skipped when another dependency needs it.
func TestDependencyBootstrapRejectsSkippedDockerPrerequisite(t *testing.T) {
	bootstrap := DependencyBootstrap{CommandRunner: &fakeRunner{}}
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapInstall,
		BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{
			config.BootstrapDocker: config.BootstrapSkip,
		},
	}
	err := bootstrap.Run(context.Background(), opts, DependencyRequirements{ContainerToolkit: true})
	if err == nil || !strings.Contains(err.Error(), "container-toolkit cannot run when docker is skipped") {
		t.Fatalf("DependencyBootstrap.Run() error = %v, want dependency mode error", err)
	}
}

// TestDependencyBootstrapReadyDockerDoesNotWritePackages verifies that a ready dependency needs no package-manager work.
func TestDependencyBootstrapReadyDockerDoesNotWritePackages(t *testing.T) {
	runner := &fakeRunner{}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner}
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall}
	for range 2 {
		if err := bootstrap.Run(context.Background(), opts, DependencyRequirements{Docker: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range runner.commands {
		if command.Name == "apt-get" || command.Name == "dnf" || command.Name == "curl" || command.Name == "sudo" {
			t.Fatalf("ready Docker bootstrap wrote to the host: %#v", runner.commands)
		}
	}
}

// TestDependencyBootstrapRunsReadyCommonComponents verifies toolkit, DCGM, and VSOCK checks.
func TestDependencyBootstrapRunsReadyCommonComponents(t *testing.T) {
	version := "1:4.6.1-1"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		commandKey(dcgmPackageQueryCommand(false)): {
			Stdout: []byte("ii  datacenter-gpu-manager-4-core=" + version + "\n" +
				"ii  datacenter-gpu-manager-4-proprietary=" + version + "\n"),
		},
	}}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner, DetectHostOS: func() (HostOS, error) {
		return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
	}}
	runner.results["apt-cache madison datacenter-gpu-manager-4-core"] = e2eexec.Result{Stdout: []byte("package | 1:4.6.1-1 | https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64 Packages\n")}
	runner.results["apt-cache madison datacenter-gpu-manager-4-proprietary"] = runner.results["apt-cache madison datacenter-gpu-manager-4-core"]
	opts := config.TestOptions{
		BootstrapMode:             config.BootstrapInstall,
		ContainerToolkitTestImage: "nvcr.io/nvidia/cuda:13.3.1-base-ubuntu24.04",
		Dependencies:              config.Dependencies{DCGMVersion: "4.6.1", ContainerToolkitVersion: "1.19.1-1"},
	}
	requirements := DependencyRequirements{ContainerToolkit: true, GPURuntime: true, HostDCGM: true, VSOCK: true}

	if err := bootstrap.Run(context.Background(), opts, requirements); err != nil {
		t.Fatal(err)
	}
	if err := CheckGPURuntime(context.Background(), io.Discard, runner, opts, PreparedDependencies{Requirements: requirements}); err != nil {
		t.Fatal(err)
	}
	if !runner.hasCommandArg("--gpus") || !runner.hasCommandArg("/dev/vsock") {
		t.Fatalf("common post-checks were not run: %#v", runner.commands)
	}
}

// TestDependencyBootstrapRejectsIncompleteUbuntuInstall verifies the post-install check is authoritative.
func TestDependencyBootstrapRejectsIncompleteUbuntuInstall(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c command -v docker": {ExitCode: 1},
	}}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}

	err := bootstrap.Run(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapInstall}, DependencyRequirements{Docker: true})
	if err == nil || !strings.Contains(err.Error(), "left prerequisites missing") {
		t.Fatalf("DependencyBootstrap.Run() error = %v, want post-install failure", err)
	}
}

// TestPrepareVSOCKCheckModeReportsBlocked verifies a missing VSOCK device does not stop a check-mode run.
func TestPrepareVSOCKCheckModeReportsBlocked(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{"test -e /dev/vsock": exitStatus1}}
	var output strings.Builder
	bootstrap := DependencyBootstrap{Output: &output, CommandRunner: runner}
	opts := config.TestOptions{BootstrapMode: config.BootstrapCheck}

	err := bootstrap.prepareVSOCK(context.Background(), opts, DependencyRequirements{VSOCK: true}, "host")
	if err != nil {
		t.Fatalf("prepareVSOCK() error = %v, want nil", err)
	}
	if !strings.Contains(output.String(), "bootstrap: vsock=blocked") ||
		!strings.Contains(output.String(), "--bootstrap-component vsock=install") {
		t.Fatalf("output = %q, want vsock=blocked line naming vsock=install", output.String())
	}
	if runner.hasCommandArg("vsock_loopback") {
		t.Fatalf("check mode attempted modprobe: %#v", runner.commands)
	}
}

// TestPrepareVSOCKInstallModeFailsWhenStillMissing verifies install mode still fails if VSOCK stays unavailable.
func TestPrepareVSOCKInstallModeFailsWhenStillMissing(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{"test -e /dev/vsock": exitStatus1}}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner}
	opts := config.TestOptions{BootstrapMode: config.BootstrapInstall}

	err := bootstrap.prepareVSOCK(context.Background(), opts, DependencyRequirements{VSOCK: true}, "host")
	if err == nil {
		t.Fatal("prepareVSOCK() error = nil, want failure")
	}
	if !runner.hasCommandArg("vsock_loopback") {
		t.Fatalf("install mode did not load vsock_loopback: %#v", runner.commands)
	}
}

// TestPrepareVSOCKCheckModeBlockedWhenModuleMissing verifies a present device without the loopback module is blocked.
func TestPrepareVSOCKCheckModeBlockedWhenModuleMissing(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c test -d /sys/module/vsock_loopback || grep -q '^vsock_loopback[[:space:]]' /proc/modules": exitStatus1,
	}}
	var output strings.Builder
	bootstrap := DependencyBootstrap{Output: &output, CommandRunner: runner}

	err := bootstrap.prepareVSOCK(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapCheck}, DependencyRequirements{VSOCK: true}, "host")
	if err != nil {
		t.Fatalf("prepareVSOCK() error = %v, want nil", err)
	}
	if !strings.Contains(output.String(), "vsock=blocked") {
		t.Fatalf("output = %q, want vsock=blocked", output.String())
	}
}

// TestPrepareVSOCKCheckModeReadyIsSilent verifies a ready VSOCK prints nothing and loads nothing.
func TestPrepareVSOCKCheckModeReadyIsSilent(t *testing.T) {
	runner := &fakeRunner{}
	var output strings.Builder
	bootstrap := DependencyBootstrap{Output: &output, CommandRunner: runner}

	err := bootstrap.prepareVSOCK(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapCheck}, DependencyRequirements{VSOCK: true}, "host")
	if err != nil {
		t.Fatalf("prepareVSOCK() error = %v, want nil", err)
	}
	if output.Len() != 0 || runner.hasCommandArg("vsock_loopback") {
		t.Fatalf("ready VSOCK output = %q, commands = %#v", output.String(), runner.commands)
	}
}

// TestPrepareVSOCKSkipModeRunsNothing verifies vsock=skip neither probes nor reports.
func TestPrepareVSOCKSkipModeRunsNothing(t *testing.T) {
	runner := &fakeRunner{}
	var output strings.Builder
	bootstrap := DependencyBootstrap{Output: &output, CommandRunner: runner}
	opts := config.TestOptions{BootstrapMode: config.BootstrapCheck, BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{config.BootstrapVSOCK: config.BootstrapSkip}}

	err := bootstrap.prepareVSOCK(context.Background(), opts, DependencyRequirements{VSOCK: true}, "host")
	if err != nil {
		t.Fatalf("prepareVSOCK() error = %v, want nil", err)
	}
	if output.Len() != 0 || len(runner.commands) != 0 {
		t.Fatalf("skip mode output = %q, commands = %#v", output.String(), runner.commands)
	}
}

// TestPrepareVSOCKUnrecognizedModeDoesNotInstall verifies only an explicit install loads the module.
func TestPrepareVSOCKUnrecognizedModeDoesNotInstall(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{"test -e /dev/vsock": exitStatus1}}
	bootstrap := DependencyBootstrap{Output: io.Discard, CommandRunner: runner}
	opts := config.TestOptions{BootstrapMode: config.BootstrapMode("bogus")}

	if err := bootstrap.prepareVSOCK(context.Background(), opts, DependencyRequirements{VSOCK: true}, "host"); err != nil {
		t.Fatalf("prepareVSOCK() error = %v, want nil", err)
	}
	if runner.hasCommandArg("vsock_loopback") || runner.hasCommandArg("modprobe") {
		t.Fatalf("unrecognized mode attempted modprobe: %#v", runner.commands)
	}
}

// TestPrepareVSOCKCanceledContextIsNotBlocked verifies cancellation is returned, not reported as blocked.
func TestPrepareVSOCKCanceledContextIsNotBlocked(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{"test -e /dev/vsock": exitStatus1}}
	var output strings.Builder
	bootstrap := DependencyBootstrap{Output: &output, CommandRunner: runner}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := bootstrap.prepareVSOCK(ctx, config.TestOptions{BootstrapMode: config.BootstrapCheck}, DependencyRequirements{VSOCK: true}, "host")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prepareVSOCK() error = %v, want context.Canceled", err)
	}
	if strings.Contains(output.String(), "blocked") {
		t.Fatalf("output = %q, want no blocked line", output.String())
	}
}

// TestPrepareVSOCKProbeFailureIsAnError verifies a probe that cannot run is not reported as blocked.
func TestPrepareVSOCKProbeFailureIsAnError(t *testing.T) {
	probeErr := errors.New("exec: \"test\": executable file not found")
	runner := &fakeRunner{results: map[string]e2eexec.Result{"test -e /dev/vsock": {ExitCode: -1, Err: probeErr}}}
	var output strings.Builder
	bootstrap := DependencyBootstrap{Output: &output, CommandRunner: runner}

	err := bootstrap.prepareVSOCK(context.Background(), config.TestOptions{BootstrapMode: config.BootstrapCheck}, DependencyRequirements{VSOCK: true}, "host")
	if !errors.Is(err, probeErr) {
		t.Fatalf("prepareVSOCK() error = %v, want probe error", err)
	}
	if strings.Contains(output.String(), "blocked") {
		t.Fatalf("output = %q, want no blocked line", output.String())
	}
}
