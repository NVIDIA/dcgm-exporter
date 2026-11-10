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
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const versionNotApplicable = "not-applicable"

// DependencyBootstrap checks E2E dependencies and installs only the allowed missing ones.
// The tests command creates one with its root directory, output writer, and command runner.
type DependencyBootstrap struct {
	RootDir       string
	Output        io.Writer
	CommandRunner e2eexec.Runner
	DetectHostOS  HostOSDetector
	// KnownHostOS keeps one earlier host detection available for later errors.
	KnownHostOS HostOS
}

// EnsureHostDCGM installs or checks the exact host DCGM runtime needed for a probe fallback.
func EnsureHostDCGM(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.TestOptions) error {
	if opts.BootstrapModeFor(config.BootstrapHostDCGM) == config.BootstrapSkip {
		return verifyHostDCGMLibraries(ctx, runner, false, opts.Dependencies.DCGMVersion)
	}
	bootstrap := DependencyBootstrap{RootDir: root, Output: stdout, CommandRunner: runner}
	return bootstrap.Run(ctx, opts, DependencyRequirements{HostDCGM: true})
}

// PrepareHostDCGMProbe returns a working nv-hostengine address for direct DCGM probes.
// In install mode it starts a private hostengine so probes do not use an existing stale service.
func PrepareHostDCGMProbe(
	ctx context.Context,
	stdout io.Writer,
	runner e2eexec.Runner,
	opts config.TestOptions,
) (string, func(context.Context) error, error) {
	cleanup := func(context.Context) error { return nil }
	if opts.BootstrapModeFor(config.BootstrapHostDCGM) != config.BootstrapInstall {
		result := runner.Run(ctx, e2eexec.Command{Name: "dcgmi", Args: []string{"discovery", "-l"}})
		if result.ExitCode != 0 || result.Err != nil {
			return "", cleanup, fmt.Errorf(
				"host DCGM probe fallback cannot reach nv-hostengine: %s; start nvidia-dcgm or use --bootstrap=install",
				commandResultText(result),
			)
		}
		return "localhost", cleanup, nil
	}

	tempDir, err := os.MkdirTemp("", "dcgm-exporter-e2e-hostengine-")
	if err != nil {
		return "", cleanup, fmt.Errorf("create hostengine runtime directory: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return "", cleanup, fmt.Errorf("reserve hostengine port: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		_ = os.RemoveAll(tempDir)
		return "", cleanup, fmt.Errorf("release reserved hostengine port %s: %w", address, err)
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return "", cleanup, fmt.Errorf("parse reserved hostengine address %s: %w", address, err)
	}
	pidFile := filepath.Join(tempDir, "nv-hostengine.pid")
	if err := runInstallCommand(
		ctx,
		stdout,
		runner,
		"start_host_dcgm_probe",
		sudoCommand("nv-hostengine", "--pid", pidFile, "-b", "127.0.0.1", "-p", port),
	); err != nil {
		_ = os.RemoveAll(tempDir)
		return "", cleanup, fmt.Errorf("start isolated hostengine for host DCGM probe fallback: %w", err)
	}
	stop := func(cleanupCtx context.Context) error {
		stopErr := runInstallCommand(
			cleanupCtx,
			stdout,
			runner,
			"stop_host_dcgm_probe",
			sudoCommand("nv-hostengine", "--term", "--pid", pidFile),
		)
		removePIDErr := runInstallCommand(
			cleanupCtx,
			stdout,
			runner,
			"remove_host_dcgm_probe_pid",
			sudoCommand("rm", "-f", pidFile),
		)
		return errors.Join(stopErr, removePIDErr, os.RemoveAll(tempDir))
	}
	result := runner.Run(ctx, e2eexec.Command{
		Name: "dcgmi",
		Args: []string{"discovery", "--host", address, "-l"},
	})
	if result.ExitCode != 0 || result.Err != nil {
		probeErr := fmt.Errorf("isolated nv-hostengine started, but host dcgmi still cannot reach it: %s", commandResultText(result))
		cleanupErr := stop(context.WithoutCancel(ctx))
		return "", cleanup, errors.Join(probeErr, cleanupErr)
	}
	return address, stop, nil
}

func commandResultText(result e2eexec.Result) string {
	if len(result.Stderr) != 0 {
		return strings.TrimSpace(string(result.Stderr))
	}
	if len(result.Stdout) != 0 {
		return strings.TrimSpace(string(result.Stdout))
	}
	if result.Err != nil {
		return result.Err.Error()
	}
	return fmt.Sprintf("command exited with status %d", result.ExitCode)
}

// Run checks only the requested prerequisites and installs missing ones when their mode allows it.
func (e DependencyBootstrap) Run(ctx context.Context, opts config.TestOptions, requirements DependencyRequirements) error {
	hostName := "not-detected"
	if e.KnownHostOS.Distribution != "" {
		hostName = hostOSName(e.KnownHostOS)
	}
	packagesNeeded, err := validatedPackageRequirements(opts, requirements, hostName)
	if err != nil {
		return err
	}
	if err := e.checkRequiredGPUHealth(ctx, opts, requirements, hostName); err != nil {
		return err
	}
	hostName, err = e.checkRequiredHostPackages(ctx, opts, requirements, packagesNeeded, hostName)
	if err != nil {
		return err
	}
	if err := e.prepareKubernetesTools(ctx, opts, requirements, hostName); err != nil {
		return err
	}
	if err := e.prepareDockerDaemon(ctx, opts, requirements, packagesNeeded, hostName); err != nil {
		return err
	}
	if err := e.verifyRequiredHostDCGM(ctx, opts, packagesNeeded, hostName); err != nil {
		return err
	}
	if err := e.prepareVSOCK(ctx, opts, requirements, hostName); err != nil {
		return err
	}
	return nil
}

// validatedPackageRequirements validates selected bootstrap modes and package requirements before host checks begin.
func validatedPackageRequirements(
	opts config.TestOptions,
	requirements DependencyRequirements,
	hostName string,
) (PackageRequirements, error) {
	if err := validateBootstrapComponentModes(opts, requirements); err != nil {
		return PackageRequirements{}, bootstrapError(hostName, "bootstrap-component", versionNotApplicable, "validate-config", err)
	}
	packagesNeeded := buildPackageRequirements(opts, requirements)
	if err := validatePackageRequirements(opts, packagesNeeded); err != nil {
		return PackageRequirements{}, bootstrapError(
			hostName,
			packageComponent(packagesNeeded),
			packageVersion(packagesNeeded),
			"validate-config",
			err,
		)
	}
	return packagesNeeded, nil
}

// checkRequiredGPUHealth checks the GPU runtime only when a selected requirement needs it and bootstrap is not skipped.
func (e DependencyBootstrap) checkRequiredGPUHealth(
	ctx context.Context,
	opts config.TestOptions,
	requirements DependencyRequirements,
	hostName string,
) error {
	if !requirements.GPURuntime || opts.BootstrapModeFor(config.BootstrapGPURuntime) == config.BootstrapSkip {
		return nil
	}
	if err := checkGPUHealth(ctx, e.Output, e.CommandRunner); err != nil {
		return bootstrapError(hostName, "gpu-driver", versionNotApplicable, "check", err)
	}
	return nil
}

// checkRequiredHostPackages checks or installs the selected host packages and keeps the detected host name for later diagnostics.
func (e DependencyBootstrap) checkRequiredHostPackages(
	ctx context.Context,
	opts config.TestOptions,
	requirements DependencyRequirements,
	packagesNeeded PackageRequirements,
	hostName string,
) (string, error) {
	if packagesNeeded.Empty() {
		return hostName, nil
	}
	runtimeRepairMayRestartDocker := requirements.GPURuntime &&
		opts.BootstrapModeFor(config.BootstrapGPURuntime) == config.BootstrapInstall
	host, err := e.checkOrInstallHostPackages(ctx, opts, packagesNeeded, requirements.Docker, runtimeRepairMayRestartDocker)
	if err != nil {
		return "", err
	}
	if host.Distribution != "" {
		hostName = hostOSName(host)
	}
	return hostName, nil
}

// prepareKubernetesTools verifies or installs Kubernetes client tools when a selected requirement needs them.
func (e DependencyBootstrap) prepareKubernetesTools(
	ctx context.Context,
	opts config.TestOptions,
	requirements DependencyRequirements,
	hostName string,
) error {
	mode := opts.BootstrapModeFor(config.BootstrapKubernetesTools)
	if !requirements.KubernetesTools || mode == config.BootstrapSkip {
		return nil
	}
	var err error
	if mode == config.BootstrapInstall {
		err = installK8sClientTools(ctx, e.Output, e.RootDir, e.CommandRunner, opts)
	} else {
		err = verifyK8sClientTools(ctx, e.CommandRunner, opts)
	}
	if err != nil {
		return bootstrapError(hostName, "kubernetes-tools", versionNotApplicable, phaseForBootstrapMode(mode), err)
	}
	return nil
}

// prepareDockerDaemon checks daemon access only when selected Docker setup also requires Docker packages.
func (e DependencyBootstrap) prepareDockerDaemon(
	ctx context.Context,
	opts config.TestOptions,
	requirements DependencyRequirements,
	packagesNeeded PackageRequirements,
	hostName string,
) error {
	if !requirements.Docker || !packagesNeeded.Docker {
		return nil
	}
	install := opts.BootstrapModeFor(config.BootstrapDocker) == config.BootstrapInstall
	if err := ensureDocker(ctx, e.Output, e.CommandRunner, packagesNeeded.Buildx, install); err != nil {
		return bootstrapError(
			hostName,
			"docker",
			versionNotApplicable,
			phaseForBootstrapMode(opts.BootstrapModeFor(config.BootstrapDocker)),
			err,
		)
	}
	return nil
}

// verifyRequiredHostDCGM confirms installed host DCGM libraries remain available after package setup.
func (e DependencyBootstrap) verifyRequiredHostDCGM(
	ctx context.Context,
	opts config.TestOptions,
	packagesNeeded PackageRequirements,
	hostName string,
) error {
	if !packagesNeeded.HostDCGM {
		return nil
	}
	if err := verifyHostDCGMLibraries(ctx, e.CommandRunner, packagesNeeded.DCGMDevelopment, opts.Dependencies.DCGMVersion); err != nil {
		return bootstrapError(hostName, hostDCGMComponent, opts.Dependencies.DCGMVersion, phasePostVerify, err)
	}
	return nil
}

// prepareVSOCK verifies or installs VSOCK support only when a selected requirement needs it.
// Only the VSOCK scenarios need it, so in check mode missing VSOCK loopback is reported as
// blocked and the run continues; those scenarios then fail or skip with their own message.
func (e DependencyBootstrap) prepareVSOCK(
	ctx context.Context,
	opts config.TestOptions,
	requirements DependencyRequirements,
	hostName string,
) error {
	mode := opts.BootstrapModeFor(config.BootstrapVSOCK)
	if !requirements.VSOCK || mode == config.BootstrapSkip {
		return nil
	}

	ready, err := vsockReady(ctx, e.CommandRunner)
	if err != nil {
		return bootstrapError(hostName, "vsock", versionNotApplicable, phaseForBootstrapMode(mode), err)
	}
	if ready {
		return nil
	}

	// A canceled run is not a missing device, so report the cancellation instead.
	if err := ctx.Err(); err != nil {
		return bootstrapError(hostName, "vsock", versionNotApplicable, phaseForBootstrapMode(mode), fmt.Errorf("check VSOCK: %w", err))
	}

	// Only an explicit install may load the kernel module; any other mode only reports.
	if mode != config.BootstrapInstall {
		if e.Output != nil {
			fmt.Fprintln(e.Output, "[e2e] bootstrap: vsock=blocked (VSOCK loopback is unavailable; set --bootstrap-component vsock=install to load vsock_loopback)")
		}
		return nil
	}

	if err := ensureVSOCK(ctx, e.Output, e.CommandRunner); err != nil {
		return bootstrapError(hostName, "vsock", versionNotApplicable, "install", err)
	}
	return nil
}

// verifyHostDCGMLibraries checks DCGM libraries the same way on every supported host.
func verifyHostDCGMLibraries(ctx context.Context, runner e2eexec.Runner, development bool, version string) error {
	if !HostDCGMLibraryAvailable(ctx, runner) {
		return fmt.Errorf("host DCGM %s is installed but libdcgm.so.4 is unavailable", version)
	}
	if development && !HostDCGMInjectionLibraryAvailable(ctx, runner) {
		return fmt.Errorf("host DCGM %s development files are installed but libnvml_injection.so is unavailable", version)
	}
	return nil
}

// checkOrInstallHostPackages checks native packages and installs only components in install mode.
// Run calls it after common command checks show that a native package may be missing.
func (e DependencyBootstrap) checkOrInstallHostPackages(
	ctx context.Context,
	opts config.TestOptions,
	packagesNeeded PackageRequirements,
	dockerDaemonRequired bool,
	runtimeRepairMayRestartDocker bool,
) (HostOS, error) {
	packagesNeeded = withDockerSocketACLRequirement(
		ctx,
		e.CommandRunner,
		opts,
		packagesNeeded,
		dockerDaemonRequired,
		runtimeRepairMayRestartDocker,
	)
	status := checkHostIndependentDependencies(ctx, e.CommandRunner, packagesNeeded)
	if status.MissingPackages(packagesNeeded).Empty() {
		return HostOS{}, nil
	}

	readHost := e.DetectHostOS
	if readHost == nil {
		readHost = DetectHostOS
	}
	host, err := readHost()
	if err != nil {
		return HostOS{}, bootstrapError(
			"unknown", packageComponent(packagesNeeded), packageVersion(packagesNeeded), "detect-host", err,
		)
	}
	installer, err := newHostPackageInstaller(host, e.Output, e.CommandRunner)
	if err != nil {
		return HostOS{}, bootstrapError(
			hostOSName(host), packageComponent(packagesNeeded), packageVersion(packagesNeeded), "select-installer", err,
		)
	}
	if e.Output != nil {
		fmt.Fprintf(e.Output, "[e2e] bootstrap: host_os=%s installer=%s\n", hostOSName(host), host.Distribution)
	}

	status, checkErr := installer.CheckPackages(ctx, packagesNeeded)
	writePackageStatus(e.Output, packagesNeeded, opts, status, checkErr)
	missing := status.MissingPackages(packagesNeeded)
	if missing.Empty() {
		if checkErr != nil {
			return HostOS{}, bootstrapError(hostOSName(host), packageComponent(packagesNeeded), packageVersion(packagesNeeded), "check", checkErr)
		}
		return host, nil
	}
	checked := packageRequirementsWithMode(missing, opts, config.BootstrapCheck)
	if !checked.Empty() {
		cause := checkModeMissingError(checked, status)
		if checkErr != nil {
			cause = errors.Join(checkErr, cause)
		}
		return HostOS{}, bootstrapError(
			hostOSName(host), packageComponent(checked), packageVersion(checked), "check", cause,
		)
	}
	toInstall := packageRequirementsWithMode(missing, opts, config.BootstrapInstall)
	if toInstall.Empty() {
		if checkErr != nil {
			return HostOS{}, bootstrapError(hostOSName(host), packageComponent(missing), packageVersion(missing), "check", checkErr)
		}
		return HostOS{}, bootstrapError(hostOSName(host), packageComponent(missing), packageVersion(missing), "check", fmt.Errorf("missing %s", missing))
	}
	if err := installer.InstallPackages(ctx, toInstall); err != nil {
		return HostOS{}, bootstrapError(hostOSName(host), packageComponent(toInstall), packageVersion(toInstall), "install", errors.Join(checkErr, err))
	}

	status, err = installer.CheckPackages(ctx, toInstall)
	if err != nil {
		return HostOS{}, bootstrapError(hostOSName(host), packageComponent(toInstall), packageVersion(toInstall), phasePostVerify, errors.Join(checkErr, err))
	}
	if remaining := status.MissingPackages(toInstall); !remaining.Empty() {
		writeDCGMReason(e.Output, toInstall, status)
		cause := fmt.Errorf("installation left prerequisites missing: %s%s", remaining, dcgmReasonDetail(remaining, status))
		return HostOS{}, bootstrapError(
			hostOSName(host), packageComponent(remaining), packageVersion(remaining), phasePostVerify, errors.Join(checkErr, cause),
		)
	}
	return host, nil
}

// hostDCGMComponent names the host DCGM bootstrap component in reports and errors.
const hostDCGMComponent = "host-dcgm"

// phasePostVerify names the bootstrap phase that checks packages after an install.
const phasePostVerify = "post-verify"

// dcgmReasonLabel names what HostDCGMReason describes: the development package when it is the
// requirement that is not ready and the runtime is ready or not required, otherwise host DCGM.
func dcgmReasonLabel(requirements PackageRequirements, status PackageStatus) string {
	if requirements.DCGMDevelopment && !status.DCGMDevelopment && (!requirements.HostDCGM || status.HostDCGM) {
		return "dcgm-development"
	}

	return hostDCGMComponent
}

// writeDCGMReason prints why host DCGM or its development package is not ready, when a reason is known.
func writeDCGMReason(output io.Writer, requirements PackageRequirements, status PackageStatus) {
	if output == nil || status.HostDCGMReason == "" {
		return
	}

	fmt.Fprintf(output, "[e2e] bootstrap: %s not ready: %s\n", dcgmReasonLabel(requirements, status), status.HostDCGMReason)
}

// dcgmReasonDetail returns the parenthesised DCGM reason for an error about the given missing
// packages, or nothing when DCGM is not among them or no reason is known.
func dcgmReasonDetail(missing PackageRequirements, status PackageStatus) string {
	if status.HostDCGMReason == "" || (!missing.HostDCGM && !missing.DCGMDevelopment) {
		return ""
	}

	return " (" + dcgmReasonLabel(missing, status) + " not ready: " + status.HostDCGMReason + ")"
}

// checkModeMissingError explains which checked components are missing and, for DCGM, why.
func checkModeMissingError(checked PackageRequirements, status PackageStatus) error {
	return fmt.Errorf("missing %s%s; set that component to install to allow changes", checked, dcgmReasonDetail(checked, status))
}

// withDockerSocketACLRequirement asks the supported package installer for acl
// only when Docker daemon access or a runtime repair needs socket setup.
func withDockerSocketACLRequirement(
	ctx context.Context,
	runner e2eexec.Runner,
	opts config.TestOptions,
	requirements PackageRequirements,
	dockerDaemonRequired bool,
	runtimeRepairMayRestartDocker bool,
) PackageRequirements {
	if !requirements.Docker || !dockerDaemonRequired || opts.BootstrapModeFor(config.BootstrapDocker) != config.BootstrapInstall {
		return requirements
	}
	needsAccessRepair := !dockerInfoAvailable(ctx, runner)
	needsRuntimeRepairTool := runtimeRepairMayRestartDocker && !commandAvailable(ctx, runner, "setfacl")
	if needsAccessRepair || needsRuntimeRepairTool {
		requirements.DockerSocketACL = true
	}
	return requirements
}

// bootstrapError adds the host, component, version, and phase without including credentials.
func bootstrapError(host, component, version, phase string, err error) error {
	return fmt.Errorf("bootstrap host_os=%s component=%s version=%s phase=%s: %w", host, component, version, phase, err)
}

// hostOSName formats the checked distribution tuple used in logs and errors.
func hostOSName(host HostOS) string {
	return fmt.Sprintf("%s-%s-%s", host.Distribution, host.VersionID, host.Architecture)
}

// packageComponent lists the requested native package groups for an error.
func packageComponent(requirements PackageRequirements) string {
	var components []string
	if requirements.Docker {
		components = append(components, "docker")
	}
	if requirements.DockerSocketACL {
		components = append(components, "docker-socket-acl")
	}
	if requirements.Buildx {
		components = append(components, "docker-buildx")
	}
	if requirements.ContainerToolkit {
		components = append(components, "container-toolkit")
	}
	if requirements.HostDCGM {
		components = append(components, hostDCGMComponent)
	}
	if requirements.DCGMDevelopment {
		components = append(components, "dcgm-development")
	}
	return strings.Join(components, ",")
}

// packageVersion returns the requested version for an error's package groups.
func packageVersion(requirements PackageRequirements) string {
	var versions []string
	if requirements.ContainerToolkit {
		versions = append(versions, requiredPackageVersion(requirements.ContainerToolkitVersion))
	}
	if requirements.HostDCGM || requirements.DCGMDevelopment {
		versions = append(versions, requiredPackageVersion(requirements.DCGMVersion))
	}
	if len(versions) > 0 {
		return strings.Join(versions, ",")
	}
	return versionNotApplicable
}

// requiredPackageVersion returns a diagnostic placeholder when a requested
// package group has no pin, so bootstrap errors do not imply it was versioned.
func requiredPackageVersion(version string) string {
	if version == "" {
		return "missing"
	}
	return version
}

// checkHostIndependentDependencies avoids host detection when portable dependencies are ready.
func checkHostIndependentDependencies(ctx context.Context, runner e2eexec.Runner, requirements PackageRequirements) PackageStatus {
	status := PackageStatus{}
	if requirements.Docker {
		status.Docker = commandAvailable(ctx, runner, "docker")
	}
	if requirements.Buildx {
		status.Buildx = status.Docker && runner.Run(ctx, e2eexec.Command{
			Name: "docker", Args: []string{"buildx", "version"},
		}).ExitCode == 0
	}
	// Package revisions are distribution-specific, so the supported installer
	// must inspect them instead of treating nvidia-ctk command presence as ready.
	return status
}

// validatePackageRequirements rejects incomplete package plans before reading or changing the host.
func validatePackageRequirements(opts config.TestOptions, requirements PackageRequirements) error {
	if requirements.HostDCGM && strings.TrimSpace(opts.Dependencies.DCGMVersion) == "" {
		return fmt.Errorf("--dcgm-version/E2E_DCGM_VERSION is required")
	}
	if requirements.ContainerToolkit && strings.TrimSpace(opts.Dependencies.ContainerToolkitVersion) == "" {
		return fmt.Errorf("--container-toolkit-version/NVIDIA_CONTAINER_TOOLKIT_VERSION is required")
	}
	return nil
}

// buildPackageRequirements translates run dependencies into host package requirements.
// Docker CLI use shares the package check, but only Docker adds daemon setup.
func buildPackageRequirements(opts config.TestOptions, requirements DependencyRequirements) PackageRequirements {
	dockerMode := opts.BootstrapModeFor(config.BootstrapDocker)
	return PackageRequirements{
		Docker:                  (requirements.Docker || requirements.DockerCLI) && dockerMode != config.BootstrapSkip,
		Buildx:                  requirements.Buildx && opts.BootstrapModeFor(config.BootstrapBuildx) != config.BootstrapSkip,
		ContainerToolkit:        requirements.ContainerToolkit && opts.BootstrapModeFor(config.BootstrapContainerToolkit) != config.BootstrapSkip,
		HostDCGM:                requirements.HostDCGM && opts.BootstrapModeFor(config.BootstrapHostDCGM) != config.BootstrapSkip,
		DCGMDevelopment:         requirements.DCGMDevelopment && opts.BootstrapModeFor(config.BootstrapHostDCGM) != config.BootstrapSkip,
		DCGMVersion:             strings.TrimSpace(opts.Dependencies.DCGMVersion),
		ContainerToolkitVersion: strings.TrimSpace(opts.Dependencies.ContainerToolkitVersion),
	}
}

// writePackageStatus writes one status for each requested package group.
// checkOrInstallHostPackages calls it before deciding whether to install missing packages.
func writePackageStatus(output io.Writer, requirements PackageRequirements, opts config.TestOptions, status PackageStatus, checkErr error) {
	if output == nil {
		return
	}
	parts := make([]string, 0, 6)
	if requirements.Docker {
		parts = append(parts, "docker="+packageStatusText(status.Docker, false, opts.BootstrapModeFor(config.BootstrapDocker)))
	}
	if requirements.DockerSocketACL {
		parts = append(parts, "docker-socket-acl="+packageStatusText(status.DockerSocketACL, false, opts.BootstrapModeFor(config.BootstrapDocker)))
	}
	if requirements.Buildx {
		parts = append(parts, "buildx="+packageStatusText(status.Buildx, false, opts.BootstrapModeFor(config.BootstrapBuildx)))
	}
	if requirements.ContainerToolkit {
		parts = append(parts, "container-toolkit="+packageStatusText(status.ContainerToolkit, false, opts.BootstrapModeFor(config.BootstrapContainerToolkit)))
	}
	if requirements.HostDCGM {
		parts = append(parts, "host-dcgm="+packageStatusText(status.HostDCGM, checkErr != nil, opts.BootstrapModeFor(config.BootstrapHostDCGM)))
	}
	if requirements.DCGMDevelopment {
		parts = append(parts, "dcgm-development="+packageStatusText(status.DCGMDevelopment, checkErr != nil, opts.BootstrapModeFor(config.BootstrapHostDCGM)))
	}
	fmt.Fprintf(output, "[e2e] bootstrap: package_status %s\n", strings.Join(parts, " "))
	writeDCGMReason(output, requirements, status)
	if checkErr != nil {
		fmt.Fprintf(output, "[e2e] bootstrap: package_check_error=%v\n", checkErr)
	}
}

// packageStatusText returns the report word for one package group.
// writePackageStatus uses it to distinguish an absent package from an uninspectable one.
func packageStatusText(ready, unknown bool, mode config.BootstrapMode) string {
	if unknown {
		return "unknown"
	}
	if ready {
		return "ready"
	}
	if mode == config.BootstrapCheck {
		return "blocked"
	}
	return "missing"
}

// packageRequirementsWithMode returns only components whose configured mode matches mode.
// checkOrInstallHostPackages uses it to keep checks separate from authorized installs.
func packageRequirementsWithMode(requirements PackageRequirements, opts config.TestOptions, mode config.BootstrapMode) PackageRequirements {
	return PackageRequirements{
		Docker:                  requirements.Docker && opts.BootstrapModeFor(config.BootstrapDocker) == mode,
		DockerSocketACL:         requirements.DockerSocketACL && opts.BootstrapModeFor(config.BootstrapDocker) == mode,
		Buildx:                  requirements.Buildx && opts.BootstrapModeFor(config.BootstrapBuildx) == mode,
		ContainerToolkit:        requirements.ContainerToolkit && opts.BootstrapModeFor(config.BootstrapContainerToolkit) == mode,
		HostDCGM:                requirements.HostDCGM && opts.BootstrapModeFor(config.BootstrapHostDCGM) == mode,
		DCGMDevelopment:         requirements.DCGMDevelopment && opts.BootstrapModeFor(config.BootstrapHostDCGM) == mode,
		DCGMVersion:             requirements.DCGMVersion,
		ContainerToolkitVersion: requirements.ContainerToolkitVersion,
	}
}

// validateBootstrapComponentModes rejects unknown component modes and impossible prerequisites.
// Run calls it before checking or changing the host.
func validateBootstrapComponentModes(opts config.TestOptions, requirements DependencyRequirements) error {
	for component, mode := range opts.BootstrapComponents {
		if !config.ValidBootstrapComponent(component) {
			return fmt.Errorf("unknown bootstrap component %q", component)
		}
		if _, err := config.ParseBootstrapMode(string(mode)); err != nil {
			return fmt.Errorf("bootstrap component %q: %w", component, err)
		}
	}
	mode := opts.BootstrapModeFor
	if requirements.Buildx && mode(config.BootstrapBuildx) != config.BootstrapSkip && mode(config.BootstrapDocker) == config.BootstrapSkip {
		return fmt.Errorf("buildx cannot run when docker is skipped")
	}
	if requirements.ContainerToolkit && mode(config.BootstrapContainerToolkit) != config.BootstrapSkip && mode(config.BootstrapDocker) == config.BootstrapSkip {
		return fmt.Errorf("container-toolkit cannot run when docker is skipped")
	}
	if requirements.GPURuntime && mode(config.BootstrapGPURuntime) != config.BootstrapSkip && mode(config.BootstrapContainerToolkit) == config.BootstrapSkip {
		return fmt.Errorf("gpu-runtime cannot run when container-toolkit is skipped")
	}
	return nil
}

// phaseForBootstrapMode names the check or install phase for a bootstrap error.
// Run uses it to keep component errors clear and consistent.
func phaseForBootstrapMode(mode config.BootstrapMode) string {
	if mode == config.BootstrapInstall {
		return "install"
	}
	return "verify"
}
