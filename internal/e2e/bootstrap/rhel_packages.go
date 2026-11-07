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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// These are the approved RHEL repository URLs and RPM query formats.
const (
	dockerRHELRepositoryURL    = "https://download.docker.com/linux/rhel/docker-ce.repo"
	toolkitRHELRepositoryURL   = "https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo"
	cudaRHELBaseURL            = "https://developer.download.nvidia.com/compute/cuda/repos/rhel8/x86_64"
	cudaRHELGPGKeyURL          = cudaRHELBaseURL + "/D42D0685.pub"
	toolkitRHELStableBaseURL   = "https://nvidia.github.io/libnvidia-container/stable/rpm/$basearch"
	toolkitRHELExperimentalURL = "https://nvidia.github.io/libnvidia-container/experimental/rpm/$basearch"
	toolkitRHELGPGKeyURL       = "https://nvidia.github.io/libnvidia-container/gpgkey"
	rhelRepositoryDir          = "/etc/yum.repos.d"
	rhelCUDARepositoryID       = "cuda-rhel8-x86_64"
	rpmIdentityFormat          = "%{NAME}|%{EPOCH}|%{VERSION}|%{RELEASE}|%{ARCH}|%{REPOID}\\n"
	rpmInstalledIdentityFormat = "%{NAME}|%{EPOCHNUM}|%{VERSION}|%{RELEASE}|%{ARCH}\\n"
)

// rhelDCGMDevelopmentPackage is the RPM that carries the DCGM injection development files.
const rhelDCGMDevelopmentPackage = "datacenter-gpu-manager-4-devel"

// rpmIdentity records an RPM's name, epoch, version, release, architecture, and repository.
type rpmIdentity struct {
	Name         string
	Epoch        string
	Version      string
	Release      string
	Architecture string
	Repository   string
}

// rpmRepository identifies the approved endpoints for one repository section.
type rpmRepository struct {
	BaseURL      string
	GPGKey       string
	GPGCheck     string
	RepoGPGCheck string
}

// NEVRA returns the exact package selector accepted by DNF.
func (i rpmIdentity) NEVRA() string {
	return i.Name + "-" + i.Epoch + ":" + i.Version + "-" + i.Release + "." + i.Architecture
}

// evra returns epoch:version-release.arch, the NEVRA without the name.
func (i rpmIdentity) evra() string {
	return i.Epoch + ":" + i.Version + "-" + i.Release + "." + i.Architecture
}

// rhelPackageInstaller manages approved RHEL repositories and exact RPM versions.
type rhelPackageInstaller struct {
	hostOS HostOS
	stdout io.Writer
	runner e2eexec.Runner
}

// CheckPackages reports whether the required RHEL packages are ready.
func (r *rhelPackageInstaller) CheckPackages(ctx context.Context, requirements PackageRequirements) (PackageStatus, error) {
	status := PackageStatus{}
	if requirements.Docker {
		status.Docker = commandAvailable(ctx, r.runner, "docker")
	}
	if requirements.DockerSocketACL {
		status.DockerSocketACL = r.runner.Run(ctx, e2eexec.Command{Name: "rpm", Args: []string{"-q", "acl"}}).ExitCode == 0
	}
	if requirements.Buildx {
		status.Buildx = status.Docker && r.runner.Run(ctx, e2eexec.Command{
			Name: "docker", Args: []string{"buildx", "version"},
		}).ExitCode == 0
	}
	if requirements.ContainerToolkit {
		status.ContainerToolkit = r.containerToolkitReady(ctx, requirements)
	}
	if requirements.HostDCGM || requirements.DCGMDevelopment {
		var err error
		status.HostDCGM, status.DCGMDevelopment, err = r.checkDCGM(ctx, requirements)
		status.HostDCGMReason, err = notReadyReason(err)
		if err != nil {
			return status, err
		}
	}
	return status, nil
}

// InstallPackages configures signed RHEL repositories and installs requested packages.
func (r *rhelPackageInstaller) InstallPackages(ctx context.Context, requirements PackageRequirements) error {
	if requirements.Docker || requirements.Buildx {
		if err := r.installDocker(ctx, requirements); err != nil {
			return err
		}
	}
	if requirements.DockerSocketACL {
		if err := r.installDockerSocketACL(ctx); err != nil {
			return err
		}
	}
	if requirements.ContainerToolkit {
		if err := r.installContainerToolkit(ctx, requirements); err != nil {
			return err
		}
	}
	if requirements.HostDCGM || requirements.DCGMDevelopment {
		if err := r.installDCGM(ctx, requirements); err != nil {
			return err
		}
	}
	return nil
}

// validateRHELHostOS accepts only the supported RHEL version and architecture.
func validateRHELHostOS(host HostOS) (HostOS, error) {
	if host.Distribution != "rhel" {
		return HostOS{}, fmt.Errorf("unsupported RHEL distribution %q", host.Distribution)
	}
	if host.VersionID != "8.10" {
		return HostOS{}, fmt.Errorf("unsupported RHEL version %q; supported version is 8.10", host.VersionID)
	}
	if host.Architecture != "amd64" || host.NVIDIARepositoryArch != "x86_64" {
		return HostOS{}, fmt.Errorf("unsupported RHEL 8.10 architecture %q", host.Architecture)
	}
	return host, nil
}

// installDocker configures Docker's signed RHEL repository and engine packages.
func (r *rhelPackageInstaller) installDocker(ctx context.Context, requirements PackageRequirements) error {
	if err := runInstallCommand(ctx, r.stdout, r.runner, "dnf_install_docker_repository_tools", sudoCommand("dnf", "install", "-y", "dnf-plugins-core")); err != nil {
		return fmt.Errorf("RHEL BaseOS/AppStream access is required for Docker repository tools: %w", err)
	}
	if err := runInstallCommand(ctx, r.stdout, r.runner, "dnf_add_docker_repository", sudoCommand("dnf", "config-manager", "--add-repo", dockerRHELRepositoryURL)); err != nil {
		return err
	}
	packages := []string{"install", "-y"}
	if requirements.Docker {
		packages = append(packages, "docker-ce", "docker-ce-cli", "containerd.io")
	}
	if requirements.Buildx {
		packages = append(packages, "docker-buildx-plugin")
	}
	return runInstallCommand(ctx, r.stdout, r.runner, "dnf_install_docker", sudoCommand("dnf", packages...))
}

// installDockerSocketACL installs the local ACL utility without changing Docker repositories.
func (r *rhelPackageInstaller) installDockerSocketACL(ctx context.Context) error {
	return runInstallCommand(ctx, r.stdout, r.runner, "dnf_install_docker_socket_acl", sudoCommand("dnf", "install", "-y", "acl"))
}

// installContainerToolkit configures NVIDIA's signed stable RPM repository and exact package versions.
func (r *rhelPackageInstaller) installContainerToolkit(ctx context.Context, requirements PackageRequirements) error {
	content, err := r.downloadRPMRepository(ctx, "nvidia-container-toolkit", toolkitRHELRepositoryURL)
	if err != nil {
		return err
	}
	approved := map[string]rpmRepository{
		"nvidia-container-toolkit": {
			BaseURL:      toolkitRHELStableBaseURL,
			GPGKey:       toolkitRHELGPGKeyURL,
			GPGCheck:     "0",
			RepoGPGCheck: "1",
		},
		"nvidia-container-toolkit-experimental": {
			BaseURL:      toolkitRHELExperimentalURL,
			GPGKey:       toolkitRHELGPGKeyURL,
			GPGCheck:     "0",
			RepoGPGCheck: "1",
		},
	}
	if err := validateRPMRepository(content, "NVIDIA Container Toolkit", approved); err != nil {
		return err
	}
	destination := filepath.Join(rhelRepositoryDir, "nvidia-container-toolkit.repo")
	if err := runInstallCommand(ctx, r.stdout, r.runner, "install_nvidia_toolkit_repository", sudoCommandWithInput(content, "tee", destination)); err != nil {
		return err
	}
	args := []string{"install", "-y"}
	for _, name := range rhelContainerToolkitPackageNames() {
		args = append(args, name+"-"+requirements.ContainerToolkitVersion)
	}
	return runInstallCommand(ctx, r.stdout, r.runner, "dnf_install_nvidia_toolkit", sudoCommand("dnf", args...))
}

// containerToolkitReady requires the Toolkit command and every package that the local k3d node pins.
func (r *rhelPackageInstaller) containerToolkitReady(ctx context.Context, requirements PackageRequirements) bool {
	if !commandAvailable(ctx, r.runner, "nvidia-ctk") {
		return false
	}
	version, release, ok := splitRPMVersionRelease(requirements.ContainerToolkitVersion)
	if !ok {
		return false
	}
	installed, err := r.queryInstalledContainerToolkitPackages(ctx)
	if err != nil {
		return false
	}
	for _, name := range rhelContainerToolkitPackageNames() {
		identity, found := installed[name]
		if !found || identity.Version != version || identity.Release != release || identity.Architecture != "x86_64" {
			return false
		}
	}
	return true
}

// queryInstalledContainerToolkitPackages reads exact installed RPM identities for the Toolkit closure.
func (r *rhelPackageInstaller) queryInstalledContainerToolkitPackages(ctx context.Context) (map[string]rpmIdentity, error) {
	result := r.runner.Run(ctx, e2eexec.Command{
		Name: "rpm",
		Args: append([]string{"-q", "--queryformat", rpmInstalledIdentityFormat}, rhelContainerToolkitPackageNames()...),
	})
	if result.ExitCode != 0 {
		return nil, nil
	}
	identities, err := parseRPMIdentities(result.Stdout)
	if err != nil {
		return nil, err
	}
	installed := make(map[string]rpmIdentity, len(identities))
	for _, identity := range identities {
		installed[identity.Name] = identity
	}
	return installed, nil
}

// splitRPMVersionRelease separates the pinned RPM version from its release field.
func splitRPMVersionRelease(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	index := strings.LastIndex(value, "-")
	if index <= 0 || index == len(value)-1 {
		return "", "", false
	}
	return value[:index], value[index+1:], true
}

// installDCGM configures NVIDIA's CUDA repository and installs exact DCGM package NEVRAs.
func (r *rhelPackageInstaller) installDCGM(ctx context.Context, requirements PackageRequirements) error {
	if err := runInstallCommand(ctx, r.stdout, r.runner, "dnf_install_dcgm_repository_tools", sudoCommand("dnf", "install", "-y", "dnf-plugins-core")); err != nil {
		return fmt.Errorf("RHEL BaseOS/AppStream access is required for DCGM repository tools: %w", err)
	}
	repositoryURL := cudaRHELBaseURL + "/cuda-rhel8.repo"
	content, err := r.downloadRPMRepository(ctx, "cuda-rhel8", repositoryURL)
	if err != nil {
		return err
	}
	approved := map[string]rpmRepository{
		rhelCUDARepositoryID: {
			BaseURL:  cudaRHELBaseURL,
			GPGKey:   cudaRHELGPGKeyURL,
			GPGCheck: "1",
		},
	}
	if err := validateRPMRepository(content, "NVIDIA CUDA", approved); err != nil {
		return err
	}
	destination := filepath.Join(rhelRepositoryDir, "cuda-rhel8.repo")
	if err := runInstallCommand(ctx, r.stdout, r.runner, "install_cuda_repository", sudoCommandWithInput(content, "tee", destination)); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, r.stdout, r.runner, "dnf_clean_cuda_repository", sudoCommand("dnf", "clean", "expire-cache")); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, r.stdout, r.runner, "dnf_cache_cuda_repository", sudoCommand("dnf", "makecache")); err != nil {
		return fmt.Errorf("RHEL entitlement and enabled BaseOS/AppStream repositories are required: %w", err)
	}

	runtime, ready, err := r.readyRuntimeIdentity(ctx, requirements)
	if err != nil {
		return err
	}
	if ready && !requirements.HostDCGM {
		// Only the development package is missing, so leave the ready runtime at its installed release
		// when the approved repository offers the development package at that release.
		runtime.Name = rhelDCGMDevelopmentPackage
		offered := map[string]rpmIdentity{rhelDCGMDevelopmentPackage: runtime}
		offerErr := r.requireOfferedDCGM(ctx, offered, []string{rhelDCGMDevelopmentPackage})
		if offerErr == nil {
			return r.installDCGMPackages(ctx, []string{runtime.NEVRA()})
		}
		// Fall back to the newest set only when the repository confirms the release is not offered.
		if _, inspectErr := notReadyReason(offerErr); inspectErr != nil {
			return fmt.Errorf("check development package offer: %w", offerErr)
		}
	}

	resolvedDCGMPackages, err := r.resolveDCGM(ctx, requirements)
	if err != nil {
		return err
	}
	var selectors []string
	for _, name := range rhelDCGMPackageNames(requirements.DCGMDevelopment) {
		selectors = append(selectors, resolvedDCGMPackages[name].NEVRA())
	}

	return r.installDCGMPackages(ctx, selectors)
}

// installDCGMPackages installs exact RPM selectors and refreshes the library cache.
func (r *rhelPackageInstaller) installDCGMPackages(ctx context.Context, selectors []string) error {
	args := append([]string{"install", "-y"}, selectors...)
	if err := runInstallCommand(ctx, r.stdout, r.runner, "dnf_install_dcgm", sudoCommand("dnf", args...)); err != nil {
		return err
	}

	return runInstallCommand(ctx, r.stdout, r.runner, "ldconfig_dcgm", sudoCommand("ldconfig"))
}

// readyRuntimeIdentity returns the installed core package identity when the runtime packages are
// already ready: one identity, the pinned version, and offered by the approved repository. A
// condition that is not ready, or an unreadable installed-package list, returns false with no
// error; a failed repository query returns an error so it is never mistaken for a runtime that
// needs reinstalling.
func (r *rhelPackageInstaller) readyRuntimeIdentity(ctx context.Context, requirements PackageRequirements) (rpmIdentity, bool, error) {
	installed, err := r.queryInstalledDCGMPackages(ctx, requirements)
	if err != nil {
		// A host with no readable package records is installed from scratch, not preserved.
		return rpmIdentity{}, false, nil
	}

	names := rhelDCGMPackageNames(false)
	err = checkInstalledDCGMIdentity(installed, names, requirements.DCGMVersion)
	if err == nil {
		err = r.requireOfferedDCGM(ctx, installed, names)
	}
	if err != nil {
		if _, inspectErr := notReadyReason(err); inspectErr != nil {
			return rpmIdentity{}, false, inspectErr
		}
		return rpmIdentity{}, false, nil
	}

	return installed[names[0]], true, nil
}

// checkDCGM reports whether the runtime and development packages are ready.
// Runtime packages are ready when they share one version and release, that version is the pin,
// the approved CUDA repository offers the installed identity, and the library is visible. The
// development package must match the runtime identity and be offered too. A failed condition comes
// back as a not-ready error; only a failed package or repository query is a plain error.
func (r *rhelPackageInstaller) checkDCGM(ctx context.Context, requirements PackageRequirements) (bool, bool, error) {
	installed, err := r.queryInstalledDCGMPackages(ctx, requirements)
	if err != nil {
		return false, false, err
	}

	runtimeNames := rhelDCGMPackageNames(false)
	err = checkInstalledDCGMIdentity(installed, runtimeNames, requirements.DCGMVersion)
	if err == nil {
		err = r.requireOfferedDCGM(ctx, installed, runtimeNames)
	}
	if err != nil {
		return false, false, err
	}

	if !HostDCGMLibraryAvailable(ctx, r.runner) {
		return false, false, notReady(dcgmLibraryNotVisible)
	}

	if !requirements.DCGMDevelopment {
		return true, true, nil
	}

	// The development package joins the runtime packages in the one-identity rule.
	err = checkInstalledDCGMIdentity(installed, rhelDCGMPackageNames(true), requirements.DCGMVersion)
	if err == nil {
		err = r.requireOfferedDCGM(ctx, installed, []string{rhelDCGMDevelopmentPackage})
	}
	if err != nil {
		return true, false, err
	}

	if !HostDCGMInjectionLibraryAvailable(ctx, r.runner) {
		return true, false, notReady(dcgmInjectionLibraryNotVisible)
	}

	return true, true, nil
}

// checkInstalledDCGMIdentity fails unless the named installed RPMs share one epoch, version,
// release and architecture, and that version is the pin.
func checkInstalledDCGMIdentity(installed map[string]rpmIdentity, names []string, pin string) error {
	var missing, found []string
	identities := make(map[string]bool)
	for _, name := range names {
		identity, ok := installed[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		identities[identity.evra()] = true
		found = append(found, name+"="+identity.evra())
	}

	if len(missing) != 0 {
		return notReady(dcgmNotInstalledFormat, strings.Join(missing, ", "))
	}
	if len(identities) != 1 {
		return notReady("installed DCGM packages are at different versions: %s", strings.Join(found, ", "))
	}
	if version := installed[names[0]].Version; version != pin {
		return notReady("installed DCGM version %s is not the pinned upstream version %s", version, pin)
	}

	return nil
}

// requireOfferedDCGM fails when the approved CUDA repository does not offer the installed identity of a package.
func (r *rhelPackageInstaller) requireOfferedDCGM(ctx context.Context, installed map[string]rpmIdentity, names []string) error {
	for _, name := range names {
		identity := installed[name]
		result := r.runner.Run(ctx, e2eexec.Command{
			Name: "dnf",
			Args: []string{"-q", "repoquery", "--repoid", rhelCUDARepositoryID, "--available", "--qf", rpmIdentityFormat, identity.NEVRA()},
		})
		if result.ExitCode != 0 {
			return fmt.Errorf("query RHEL package %s: %s%s", identity.NEVRA(), result.Stdout, result.Stderr)
		}

		offered, err := parseRPMIdentities(result.Stdout)
		if err != nil {
			return fmt.Errorf("query RHEL package %s: %w", identity.NEVRA(), err)
		}

		offeredMatch := slices.ContainsFunc(offered, func(candidate rpmIdentity) bool {
			return candidate.Name == name && candidate.evra() == identity.evra() &&
				candidate.Repository == rhelCUDARepositoryID
		})
		if !offeredMatch {
			return notReady("installed %s is not offered by the approved NVIDIA CUDA repository", identity.NEVRA())
		}
	}

	return nil
}

// resolveDCGM selects the newest matching RPM for every required DCGM package.
func (r *rhelPackageInstaller) resolveDCGM(ctx context.Context, requirements PackageRequirements) (map[string]rpmIdentity, error) {
	resolvedDCGMPackages := make(map[string]rpmIdentity)
	for _, name := range rhelDCGMPackageNames(requirements.DCGMDevelopment) {
		result := r.runner.Run(ctx, e2eexec.Command{
			Name: "dnf",
			Args: []string{"-q", "repoquery", "--repoid", rhelCUDARepositoryID, "--available", "--latest-limit", "1", "--qf", rpmIdentityFormat, name + "-" + requirements.DCGMVersion + "*"},
		})
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("query RHEL package %s: %s%s", name, result.Stdout, result.Stderr)
		}
		identities, err := parseRPMIdentities(result.Stdout)
		if err != nil {
			return nil, fmt.Errorf("query RHEL package %s: %w", name, err)
		}
		if len(identities) != 1 || identities[0].Name != name || identities[0].Version != requirements.DCGMVersion || identities[0].Architecture != "x86_64" || identities[0].Repository != rhelCUDARepositoryID {
			return nil, fmt.Errorf("RHEL repositories do not provide one approved %s version %s for x86_64", name, requirements.DCGMVersion)
		}
		resolvedDCGMPackages[name] = identities[0]
	}
	return resolvedDCGMPackages, nil
}

// queryInstalledDCGMPackages queries complete identities for every required DCGM package.
// rpm exits non-zero when any queried package is absent but still lists the installed ones, so
// absent packages are left out of the result. A non-zero exit that names no absent package and
// lists no record is an error, as is any unrecognised output line.
func (r *rhelPackageInstaller) queryInstalledDCGMPackages(ctx context.Context, requirements PackageRequirements) (map[string]rpmIdentity, error) {
	result := r.runner.Run(ctx, e2eexec.Command{
		Name: "rpm",
		Args: append([]string{"-q", "--queryformat", rpmInstalledIdentityFormat}, rhelDCGMPackageNames(requirements.DCGMDevelopment)...),
		Env:  cLocaleEnv,
	})

	var records []string
	absent := false
	for _, line := range strings.Split(string(result.Stdout)+"\n"+string(result.Stderr), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "|"):
			records = append(records, line)
		case strings.Contains(line, "is not installed"):
			absent = true
		case result.ExitCode != 0 && line != "":
			return nil, fmt.Errorf("query installed DCGM packages: %s", line)
		}
	}
	if result.ExitCode != 0 && len(records) == 0 && !absent {
		return nil, fmt.Errorf("query installed DCGM packages: exit %d without package records: %v", result.ExitCode, result.Err)
	}

	identities, err := parseRPMIdentities([]byte(strings.Join(records, "\n")))
	if err != nil {
		return nil, err
	}
	installed := make(map[string]rpmIdentity, len(identities))
	for _, identity := range identities {
		installed[identity.Name] = identity
	}

	return installed, nil
}

// downloadRPMRepository downloads one repository file to a private temporary directory.
func (r *rhelPackageInstaller) downloadRPMRepository(ctx context.Context, name, url string) ([]byte, error) {
	tempDir, err := os.MkdirTemp("", "dcgm-exporter-e2e-rpm-repo-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	destination := filepath.Join(tempDir, name+".repo")
	if err := runInstallCommand(ctx, r.stdout, r.runner, "download_"+name+"_repository", e2eexec.Command{
		Name: "curl", Args: []string{"-fsSL", url, "-o", destination},
	}); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		return nil, fmt.Errorf("read %s repository: %w", name, err)
	}
	return content, nil
}

// validateRPMRepository accepts only the expected signed repository sections and endpoints.
func validateRPMRepository(content []byte, name string, approved map[string]rpmRepository) error {
	sections := make(map[string]map[string]string)
	sectionName := ""
	for lineNumber, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sectionName = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if sectionName == "" {
				return fmt.Errorf("%s repository has an empty section on line %d", name, lineNumber+1)
			}
			if _, exists := sections[sectionName]; exists {
				return fmt.Errorf("%s repository repeats section %q", name, sectionName)
			}
			sections[sectionName] = make(map[string]string)
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || sectionName == "" {
			return fmt.Errorf("%s repository has an invalid entry on line %d", name, lineNumber+1)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "mirrorlist" || key == "metalink" {
			return fmt.Errorf("%s repository section %q uses unapproved source directive %q", name, sectionName, key)
		}
		if _, exists := sections[sectionName][key]; exists {
			return fmt.Errorf("%s repository section %q repeats key %q", name, sectionName, key)
		}
		sections[sectionName][key] = strings.TrimSpace(value)
	}

	if len(sections) != len(approved) {
		return fmt.Errorf("%s repository has unexpected or missing sections", name)
	}
	for sectionName, endpoints := range approved {
		values, found := sections[sectionName]
		if !found {
			return fmt.Errorf("%s repository is missing section %q", name, sectionName)
		}
		if values["baseurl"] != endpoints.BaseURL || values["gpgkey"] != endpoints.GPGKey {
			return fmt.Errorf("%s repository section %q uses unapproved endpoints", name, sectionName)
		}
		if values["gpgcheck"] != endpoints.GPGCheck || values["repo_gpgcheck"] != endpoints.RepoGPGCheck {
			return fmt.Errorf("%s repository section %q uses unapproved signature verification settings", name, sectionName)
		}
	}
	return nil
}

// parseRPMIdentities parses pipe-delimited NEVRA and repository records.
func parseRPMIdentities(output []byte) ([]rpmIdentity, error) {
	var identities []rpmIdentity
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 5 && len(fields) != 6 {
			return nil, fmt.Errorf("invalid RPM identity %q", line)
		}
		identity := rpmIdentity{
			Name: fields[0], Epoch: fields[1], Version: fields[2],
			Release: fields[3], Architecture: fields[4],
		}
		if len(fields) == 6 {
			identity.Repository = fields[5]
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

// rhelDCGMPackageNames returns RHEL runtime packages and the optional development package.
func rhelDCGMPackageNames(requireDevelopment bool) []string {
	packages := []string{"datacenter-gpu-manager-4-core", "datacenter-gpu-manager-4-proprietary"}
	if requireDevelopment {
		packages = append(packages, rhelDCGMDevelopmentPackage)
	}
	return packages
}

// rhelContainerToolkitPackageNames returns the RPM closure used by the local k3d node image.
func rhelContainerToolkitPackageNames() []string {
	return []string{"nvidia-container-toolkit", "nvidia-container-toolkit-base", "libnvidia-container-tools", "libnvidia-container1"}
}
