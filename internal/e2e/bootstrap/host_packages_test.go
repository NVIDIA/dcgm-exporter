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
	"io"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestDependencyBootstrapCheckReportsUnknownWhenRepositoryQueryFails verifies that a failed RHEL
// repository query is an inspection failure, not a not-ready package.
func TestDependencyBootstrapCheckReportsUnknownWhenRepositoryQueryFails(t *testing.T) {
	requirements := PackageRequirements{HostDCGM: true, DCGMVersion: "4.6.1"}
	var output bytes.Buffer
	bootstrap := DependencyBootstrap{
		Output:        &output,
		CommandRunner: &fakeRunner{results: rhelRepositoryQueryFailure(requirements)},
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}
	opts := config.TestOptions{BootstrapMode: config.BootstrapCheck, Dependencies: config.Dependencies{DCGMVersion: "4.6.1"}}

	err := bootstrap.Run(context.Background(), opts, DependencyRequirements{HostDCGM: true})

	if err == nil {
		t.Fatal("DependencyBootstrap.Run() succeeded, want a check failure")
	}
	for _, want := range []string{"host-dcgm=unknown", "package_check_error="} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "not ready:") {
		t.Fatalf("inspection failure printed a not-ready reason:\n%s", output.String())
	}
}

// dcgmInstallScenario is a host whose DCGM packages change when its installer runs.
type dcgmInstallScenario struct {
	runner   *fakeRunner
	host     HostOS
	opts     config.TestOptions
	installs int
}

// TestDependencyBootstrapInstallKeepsOfferedOlderDCGMRevision verifies that install mode leaves a
// ready older revision alone and installs the newest revision only when the installed one is not offered.
func TestDependencyBootstrapInstallKeepsOfferedOlderDCGMRevision(t *testing.T) {
	tests := []struct {
		name         string
		newScenario  func(offersOlder bool) *dcgmInstallScenario
		offersOlder  bool
		wantInstalls int
	}{
		{name: "ubuntu offers installed and newer", newScenario: newUbuntuDCGMScenario, offersOlder: true},
		{name: "ubuntu offers only newer", newScenario: newUbuntuDCGMScenario, wantInstalls: 1},
		{name: "rhel offers installed and newer", newScenario: newRHELDCGMScenario, offersOlder: true},
		{name: "rhel offers only newer", newScenario: newRHELDCGMScenario, wantInstalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := test.newScenario(test.offersOlder)
			var output bytes.Buffer
			bootstrap := DependencyBootstrap{
				Output:        &output,
				CommandRunner: scenario.runner,
				DetectHostOS:  func() (HostOS, error) { return scenario.host, nil },
			}

			err := bootstrap.Run(context.Background(), scenario.opts, DependencyRequirements{HostDCGM: true})
			if err != nil {
				t.Fatalf("DependencyBootstrap.Run() error = %v\n%s", err, output.String())
			}
			if scenario.installs != test.wantInstalls {
				t.Fatalf("DCGM installs = %d, want %d\n%s", scenario.installs, test.wantInstalls, output.String())
			}
			if test.wantInstalls == 0 && !strings.Contains(output.String(), "host-dcgm=ready") {
				t.Fatalf("output lacks host-dcgm=ready:\n%s", output.String())
			}
		})
	}
}

// installOptions returns install-mode options that pin DCGM version.
func installOptions(version string) config.TestOptions {
	return config.TestOptions{BootstrapMode: config.BootstrapInstall, Dependencies: config.Dependencies{DCGMVersion: version}}
}

// newUbuntuDCGMScenario starts with revision 1 installed; the repository offers revision 2 and,
// when offersOlder is set, revision 1. Installing the core package moves the host to revision 2.
func newUbuntuDCGMScenario(offersOlder bool) *dcgmInstallScenario {
	scenario := &dcgmInstallScenario{
		host: HostOS{Distribution: "ubuntu", VersionID: "26.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		opts: installOptions("4.7.0"),
	}
	revision := "1"
	offered := []string{"1:4.7.0-2"}
	if offersOlder {
		offered = []string{"1:4.7.0-1", "1:4.7.0-2"}
	}
	scenario.runner = &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		switch {
		case command.Name == "dpkg-query" && hasArg(command.Args, "datacenter-gpu-manager-4-core"):
			return ubuntuInstalledDCGM("1:4.7.0-"+revision, "1:4.7.0-"+revision, ""), true
		case command.Name == "apt-cache":
			return ubuntuOfferedDCGM(offered...), true
		case strings.Contains(commandKey(command), "install -y --allow-downgrades"):
			if !hasArg(command.Args, "datacenter-gpu-manager-4-core=1:4.7.0-2") || !hasArg(command.Args, "datacenter-gpu-manager-4-proprietary=1:4.7.0-2") {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("unexpected install selectors")}, true
			}
			scenario.installs++
			revision = "2"
			return e2eexec.Result{}, true
		}
		return e2eexec.Result{}, false
	}}

	return scenario
}

// newRHELDCGMScenario starts with release 1 installed; the repository offers release 2 and, when
// offersOlder is set, release 1. Installing the core package moves the host to release 2.
func newRHELDCGMScenario(offersOlder bool) *dcgmInstallScenario {
	scenario := &dcgmInstallScenario{
		host: HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
		opts: installOptions("4.7.0"),
	}
	release := "1"
	scenario.runner = &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		key := commandKey(command)
		switch {
		case command.Name == "rpm":
			return e2eexec.Result{Stdout: []byte(rhelInstalledDCGM("4.7.0|"+release, "4.7.0|"+release, ""))}, true
		case command.Name == "curl":
			return writeRHELRepositoryDownload(command), true
		case strings.Contains(key, "repoquery"):
			return e2eexec.Result{Stdout: []byte(rhelRepoquery(key, offersOlder))}, true
		case strings.Contains(key, "install -y datacenter-gpu-manager-4"):
			if !hasArg(command.Args, "datacenter-gpu-manager-4-core-1:4.7.0-2.x86_64") || !hasArg(command.Args, "datacenter-gpu-manager-4-proprietary-1:4.7.0-2.x86_64") {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("unexpected install selectors")}, true
			}
			scenario.installs++
			release = "2"
			return e2eexec.Result{}, true
		}
		return e2eexec.Result{}, false
	}}

	return scenario
}

// rhelRepoquery answers a dnf repoquery for the DCGM test repository. The newest query returns
// release 2; an exact query returns the release it names when the repository offers it.
func rhelRepoquery(command string, offersOlder bool) string {
	name := "datacenter-gpu-manager-4-core"
	switch {
	case strings.Contains(command, "proprietary"):
		name = "datacenter-gpu-manager-4-proprietary"
	case strings.Contains(command, "devel"):
		name = rhelDCGMDevelopmentPackage
	}
	release := "2"
	if !strings.Contains(command, "--latest-limit") && strings.Contains(command, "4.7.0-1.x86_64") {
		if !offersOlder {
			return ""
		}
		release = "1"
	}

	return name + "|1|4.7.0|" + release + "|x86_64|cuda-rhel8-x86_64\n"
}

// TestDCGMReasonLabelNamesTheRequirementThatIsNotReady verifies which label introduces a reason.
func TestDCGMReasonLabelNamesTheRequirementThatIsNotReady(t *testing.T) {
	tests := []struct {
		name         string
		requirements PackageRequirements
		status       PackageStatus
		want         string
	}{
		{name: "runtime not ready", requirements: PackageRequirements{HostDCGM: true, DCGMDevelopment: true}, want: "host-dcgm"},
		{name: "only development not ready", requirements: PackageRequirements{HostDCGM: true, DCGMDevelopment: true}, status: PackageStatus{HostDCGM: true}, want: "dcgm-development"},
		{name: "development requested alone", requirements: PackageRequirements{DCGMDevelopment: true}, want: "dcgm-development"},
		{name: "runtime requested alone", requirements: PackageRequirements{HostDCGM: true}, want: "host-dcgm"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			status := test.status
			status.HostDCGMReason = "why"

			writePackageStatus(&output, test.requirements, config.TestOptions{}, status, nil)

			if got := dcgmReasonLabel(test.requirements, status); got != test.want {
				t.Fatalf("dcgmReasonLabel() = %q, want %q", got, test.want)
			}
			if want := "bootstrap: " + test.want + " not ready: why"; !strings.Contains(output.String(), want) {
				t.Fatalf("output lacks %q:\n%s", want, output.String())
			}
		})
	}
}

// TestDependencyBootstrapInstallsOnlyMissingDevelopmentPackageAtRuntimeRevision verifies that a ready
// older runtime revision is not upgraded when only the development package is missing.
func TestDependencyBootstrapInstallsOnlyMissingDevelopmentPackageAtRuntimeRevision(t *testing.T) {
	tests := []struct {
		name  string
		host  HostOS
		build func(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool)
		want  string
	}{
		{
			name:  "ubuntu",
			host:  HostOS{Distribution: "ubuntu", VersionID: "26.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
			build: ubuntuDevelopmentOnlyMatcher,
			want:  "install -y --allow-downgrades datacenter-gpu-manager-4-dev=1:4.7.0-1",
		},
		{
			name:  "rhel",
			host:  HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"},
			build: rhelDevelopmentOnlyMatcher,
			want:  "install -y datacenter-gpu-manager-4-devel-1:4.7.0-1.x86_64",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var installs []string
			bootstrap := DependencyBootstrap{
				Output:        io.Discard,
				CommandRunner: &fakeRunner{resultMatcher: test.build(&installs)},
				DetectHostOS:  func() (HostOS, error) { return test.host, nil },
			}

			err := bootstrap.Run(context.Background(), installOptions("4.7.0"), DependencyRequirements{HostDCGM: true, DCGMDevelopment: true})
			if err != nil {
				t.Fatalf("DependencyBootstrap.Run() error = %v", err)
			}
			if len(installs) != 1 || !strings.HasSuffix(installs[0], test.want) {
				t.Fatalf("DCGM installs = %q, want one ending in %q", installs, test.want)
			}
		})
	}
}

// ubuntuDevelopmentOnlyMatcher models runtime revision 1 installed, revisions 1 and 2 offered and
// the development package absent until it is installed; it records each DCGM install command.
func ubuntuDevelopmentOnlyMatcher(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool) {
	developmentInstalled := false
	return func(command e2eexec.Command) (e2eexec.Result, bool) {
		key := commandKey(command)
		switch {
		case command.Name == "dpkg-query" && hasArg(command.Args, dcgmDevelopmentPackage):
			if developmentInstalled {
				return ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-1", "1:4.7.0-1"), true
			}
			return e2eexec.Result{
				ExitCode: 1,
				Stdout:   ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-1", "").Stdout,
				Stderr:   []byte("dpkg-query: no packages found matching " + dcgmDevelopmentPackage + "\n"),
			}, true
		case command.Name == "dpkg-query" && hasArg(command.Args, "datacenter-gpu-manager-4-core"):
			return ubuntuInstalledDCGM("1:4.7.0-1", "1:4.7.0-1", ""), true
		case command.Name == "apt-cache":
			return ubuntuOfferedDCGM("1:4.7.0-1", "1:4.7.0-2"), true
		case strings.Contains(key, "install -y --allow-downgrades"):
			*installs = append(*installs, key)
			developmentInstalled = true
			return e2eexec.Result{}, true
		}
		return e2eexec.Result{}, false
	}
}

// rhelDevelopmentOnlyMatcher models runtime release 1 installed, releases 1 and 2 offered and the
// development package absent until it is installed; it records each DCGM install command.
func rhelDevelopmentOnlyMatcher(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool) {
	developmentInstalled := false
	return func(command e2eexec.Command) (e2eexec.Result, bool) {
		key := commandKey(command)
		switch {
		case command.Name == "rpm" && developmentInstalled:
			return e2eexec.Result{Stdout: []byte(rhelInstalledDCGM("4.7.0|1", "4.7.0|1", "4.7.0|1"))}, true
		case command.Name == "rpm":
			return e2eexec.Result{
				ExitCode: 1,
				Stdout:   []byte(rhelInstalledDCGM("4.7.0|1", "4.7.0|1", "") + rhelNotInstalledLines(rhelDCGMDevelopmentPackage)),
			}, true
		case command.Name == "curl":
			return writeRHELRepositoryDownload(command), true
		case strings.Contains(key, "repoquery"):
			return e2eexec.Result{Stdout: []byte(rhelRepoquery(key, true))}, true
		case strings.Contains(key, "dnf install -y datacenter-gpu-manager-4"):
			*installs = append(*installs, key)
			developmentInstalled = true
			return e2eexec.Result{}, true
		}
		return e2eexec.Result{}, false
	}
}

// TestRHELDevelopmentInstallReturnsRepositoryQueryErrors verifies that a failed repository query
// is returned and installs nothing, instead of falling back to the newest DCGM set.
func TestRHELDevelopmentInstallReturnsRepositoryQueryErrors(t *testing.T) {
	var installs []string
	offerMatcher := rhelDevelopmentOnlyMatcher(&installs)
	bootstrap := DependencyBootstrap{
		Output: io.Discard,
		CommandRunner: &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
			key := commandKey(command)
			// Offer checks query an exact release; the newest-set lookup uses --latest-limit.
			if strings.Contains(key, "repoquery") && !strings.Contains(key, "--latest-limit") {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("Failed to download metadata for repo\n")}, true
			}
			return offerMatcher(command)
		}},
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}

	err := bootstrap.Run(context.Background(), installOptions("4.7.0"), DependencyRequirements{HostDCGM: true, DCGMDevelopment: true})

	if err == nil || !strings.Contains(err.Error(), "Failed to download metadata") {
		t.Fatalf("DependencyBootstrap.Run() error = %v, want the repository query error", err)
	}
	if len(installs) != 0 {
		t.Fatalf("DCGM installs = %q, want none after a failed repository query", installs)
	}
}

// TestDependencyBootstrapDevelopmentInstallRefreshesRepositoryAndChecksOffer verifies that the
// development-only install sets up the repository when APT lists are missing and falls back to a
// complete newest set when the development package is not offered at the runtime's revision.
func TestDependencyBootstrapDevelopmentInstallRefreshesRepositoryAndChecksOffer(t *testing.T) {
	ubuntu := HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}
	rhel := HostOS{Distribution: "rhel", VersionID: "8.10", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}
	tests := []struct {
		name         string
		host         HostOS
		matcher      func(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool)
		wantSelector []string
		// wantBefore names an argument that must appear in a command before the install.
		wantBefore string
	}{
		{
			name: "ubuntu lists missing", host: ubuntu, matcher: ubuntuDevelopmentHost(true, "1:4.7.0-1", "1:4.7.0-2"),
			wantSelector: []string{"datacenter-gpu-manager-4-dev=1:4.7.0-1"}, wantBefore: "cuda-archive-keyring.gpg",
		},
		{
			name: "ubuntu development not offered at runtime revision", host: ubuntu, matcher: ubuntuDevelopmentHost(false, "1:4.7.0-2"),
			wantSelector: []string{"datacenter-gpu-manager-4-core=1:4.7.0-2", "datacenter-gpu-manager-4-proprietary=1:4.7.0-2", "datacenter-gpu-manager-4-dev=1:4.7.0-2"},
		},
		{
			name: "rhel development not offered at runtime release", host: rhel, matcher: rhelDevelopmentNotOffered,
			wantSelector: []string{"datacenter-gpu-manager-4-core-1:4.7.0-2.x86_64", "datacenter-gpu-manager-4-proprietary-1:4.7.0-2.x86_64", "datacenter-gpu-manager-4-devel-1:4.7.0-2.x86_64"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installs, runner := runDevelopmentInstall(t, test.host, test.matcher)

			if len(installs) != 1 {
				t.Fatalf("DCGM installs = %q, want one", installs)
			}
			for _, selector := range test.wantSelector {
				if !strings.Contains(installs[0], selector) {
					t.Fatalf("install %q lacks %q", installs[0], selector)
				}
			}
			if test.wantBefore != "" && !commandWithArgBefore(runner.commands, test.wantBefore, installs[0]) {
				t.Fatalf("%q did not run before the install: %#v", test.wantBefore, runner.commands)
			}
		})
	}
}

// TestUbuntuDevelopmentInstallRefreshesUnreadableLists verifies that the
// development-only install refreshes APT lists that cannot be read, which the
// runtime readiness check accepts, and then installs at the runtime's version.
func TestUbuntuDevelopmentInstallRefreshesUnreadableLists(t *testing.T) {
	ubuntu := HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}
	unreadable := func(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool) {
		host := ubuntuDevelopmentHost(false, "1:4.7.0-1", "1:4.7.0-2")(installs)
		refreshed := false
		return func(command e2eexec.Command) (e2eexec.Result, bool) {
			if executableName(command) == "apt-get" && hasArg(command.Args, "update") {
				refreshed = true
			}
			if command.Name == "apt-cache" && !refreshed {
				return e2eexec.Result{ExitCode: 100, Stderr: []byte("E: Could not open file /var/lib/apt/lists/partial\n")}, true
			}

			return host(command)
		}
	}

	installs, _ := runDevelopmentInstall(t, ubuntu, unreadable)

	if len(installs) != 1 || !strings.Contains(installs[0], "datacenter-gpu-manager-4-dev=1:4.7.0-1") {
		t.Fatalf("DCGM installs = %q, want only the development package at the runtime's version", installs)
	}
}

// runDevelopmentInstall runs install-mode bootstrap for runtime and development DCGM on a modelled
// host and returns the recorded DCGM install commands and the runner.
func runDevelopmentInstall(t *testing.T, host HostOS, matcher func(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool)) ([]string, *fakeRunner) {
	t.Helper()

	var installs []string
	runner := &fakeRunner{resultMatcher: matcher(&installs)}
	bootstrap := DependencyBootstrap{
		Output:        io.Discard,
		CommandRunner: runner,
		DetectHostOS:  func() (HostOS, error) { return host, nil },
	}

	err := bootstrap.Run(context.Background(), installOptions("4.7.0"), DependencyRequirements{HostDCGM: true, DCGMDevelopment: true})
	if err != nil {
		t.Fatalf("DependencyBootstrap.Run() error = %v", err)
	}

	return installs, runner
}

// commandWithArgBefore reports whether a command with an argument containing want ran before the command keyed install.
func commandWithArgBefore(commands []e2eexec.Command, want, install string) bool {
	seen := false
	for _, command := range commands {
		if commandKey(command) == install {
			return seen
		}
		for _, arg := range command.Args {
			if strings.Contains(arg, want) {
				seen = true
			}
		}
	}

	return false
}

// ubuntuDevelopmentHost models runtime revision 1 installed and the development package absent.
// When listsMissing is set, apt-cache lists nothing until apt-get update runs. The development
// package is offered at developmentVersions and the runtime packages at revisions 1 and 2.
func ubuntuDevelopmentHost(listsMissing bool, developmentVersions ...string) func(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool) {
	return func(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool) {
		const repository = "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64"
		versions := map[string]string{"datacenter-gpu-manager-4-core": "1:4.7.0-1", "datacenter-gpu-manager-4-proprietary": "1:4.7.0-1"}
		return func(command e2eexec.Command) (e2eexec.Result, bool) {
			key := commandKey(command)
			switch {
			case command.Name == "dpkg-query":
				return ubuntuInstalledResult(versions), true
			case command.Name == "apt-cache" && listsMissing:
				return e2eexec.Result{}, true
			case command.Name == "apt-cache" && hasArg(command.Args, dcgmDevelopmentPackage):
				return ubuntuOfferedFrom(repository, developmentVersions...), true
			case command.Name == "apt-cache":
				return ubuntuOfferedFrom(repository, "1:4.7.0-1", "1:4.7.0-2"), true
			case executableName(command) == "apt-get" && hasArg(command.Args, "update"):
				listsMissing = false
				return e2eexec.Result{}, true
			case strings.Contains(key, "install -y --allow-downgrades"):
				*installs = append(*installs, key)
				for _, arg := range command.Args {
					if name, version, ok := strings.Cut(arg, "="); ok {
						versions[name] = version
					}
				}
				return e2eexec.Result{}, true
			}
			return e2eexec.Result{}, false
		}
	}
}

// ubuntuInstalledResult returns dpkg-query output for the installed DCGM package versions. When the
// development package is absent it exits 1 and names it on stderr, like dpkg-query.
func ubuntuInstalledResult(versions map[string]string) e2eexec.Result {
	result := ubuntuInstalledDCGM(versions["datacenter-gpu-manager-4-core"], versions["datacenter-gpu-manager-4-proprietary"], versions[dcgmDevelopmentPackage])
	if versions[dcgmDevelopmentPackage] == "" {
		result.ExitCode = 1
		result.Stderr = []byte("dpkg-query: no packages found matching " + dcgmDevelopmentPackage + "\n")
	}

	return result
}

// rhelDevelopmentNotOffered models runtime release 1 installed, the development package absent, and
// the repository offering the development package only at release 2.
func rhelDevelopmentNotOffered(installs *[]string) func(e2eexec.Command) (e2eexec.Result, bool) {
	release, developmentRelease := "1", ""
	return func(command e2eexec.Command) (e2eexec.Result, bool) {
		key := commandKey(command)
		switch {
		case command.Name == "rpm":
			result := e2eexec.Result{Stdout: []byte(rhelInstalledDCGM("4.7.0|"+release, "4.7.0|"+release, developmentPair(developmentRelease)))}
			if developmentRelease == "" {
				result.ExitCode = 1
				result.Stdout = append(result.Stdout, rhelNotInstalledLines(rhelDCGMDevelopmentPackage)...)
			}
			return result, true
		case command.Name == "curl":
			return writeRHELRepositoryDownload(command), true
		case strings.Contains(key, "repoquery") && strings.Contains(key, "devel") && strings.Contains(key, "4.7.0-1.x86_64"):
			return e2eexec.Result{}, true
		case strings.Contains(key, "repoquery"):
			return e2eexec.Result{Stdout: []byte(rhelRepoquery(key, true))}, true
		case strings.Contains(key, "dnf install -y datacenter-gpu-manager-4"):
			*installs = append(*installs, key)
			release, developmentRelease = "2", "2"
			return e2eexec.Result{}, true
		}
		return e2eexec.Result{}, false
	}
}

// developmentPair returns the "version|release" pair for the development package, or nothing when it is absent.
func developmentPair(release string) string {
	if release == "" {
		return ""
	}

	return "4.7.0|" + release
}

// TestDependencyBootstrapPostVerifyReportsDCGMReason verifies that an install which leaves DCGM not
// ready reports the reason in the error and in the status output.
func TestDependencyBootstrapPostVerifyReportsDCGMReason(t *testing.T) {
	const repository = "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu2404/x86_64"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		commandKey(dcgmPackageQueryCommand(false)):               {},
		"apt-cache madison datacenter-gpu-manager-4-core":        ubuntuOfferedFrom(repository, "1:4.7.0-2"),
		"apt-cache madison datacenter-gpu-manager-4-proprietary": ubuntuOfferedFrom(repository, "1:4.7.0-2"),
	}}
	var output bytes.Buffer
	bootstrap := DependencyBootstrap{
		Output:        &output,
		CommandRunner: runner,
		DetectHostOS: func() (HostOS, error) {
			return HostOS{Distribution: "ubuntu", VersionID: "24.04", Architecture: "amd64", NVIDIARepositoryArch: "x86_64"}, nil
		},
	}

	err := bootstrap.Run(context.Background(), installOptions("4.7.0"), DependencyRequirements{HostDCGM: true})

	want := "installation left prerequisites missing: host DCGM (host-dcgm not ready: DCGM packages are not installed"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("DependencyBootstrap.Run() error = %v, want %q", err, want)
	}
	if strings.Count(output.String(), "host-dcgm not ready:") != 2 {
		t.Fatalf("output should print the reason after the check and after the install:\n%s", output.String())
	}
}
