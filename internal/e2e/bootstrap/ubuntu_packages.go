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
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// cLocaleEnv makes package manager messages match the English text that the checks look for.
var cLocaleEnv = []string{"LC_ALL=C"}

// aptCacheMadisonCommand lists the versions and sources APT knows for one package.
func aptCacheMadisonCommand(name string) e2eexec.Command {
	return e2eexec.Command{Name: "apt-cache", Args: []string{"madison", name}, Env: cLocaleEnv}
}

// dcgmDevelopmentPackage is the Debian package that carries the DCGM injection development files.
const dcgmDevelopmentPackage = "datacenter-gpu-manager-4-dev"

// These constants name the exact Ubuntu repository URLs and files that E2E owns.
const (
	nvidiaToolkitKeyURL              = "https://nvidia.github.io/libnvidia-container/gpgkey"
	nvidiaToolkitListURL             = "https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list"
	nvidiaToolkitStableRepositoryURL = "https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH)"
	nvidiaToolkitExperimentalRepoURL = "https://nvidia.github.io/libnvidia-container/experimental/deb/$(ARCH)"
	nvidiaToolkitKeyPath             = "/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg"
	nvidiaToolkitListPath            = "/etc/apt/sources.list.d/nvidia-container-toolkit.list"
	cudaKeyringPackageName           = "cuda-keyring_1.1-1_all.deb"
	cudaKeyringUbuntu26AMD64SHA256   = "f7f474b5f6a4adf987aa587920df00e713285958ef6a913dda1945a544a3099e"
	cudaKeyringUbuntu26ARM64SHA256   = "bf74da243be6737d338a8e6f03469eb983825bba7b6dbd0b6d6a6f60229cbb6e"
)

// ubuntuPackageInstaller checks Ubuntu packages and installs E2E-owned repository files when needed.
type ubuntuPackageInstaller struct {
	hostOS         HostOS
	stdout         io.Writer
	runner         e2eexec.Runner
	verifyChecksum func(string, string) error
}

// CheckPackages reports whether the required Ubuntu packages are ready.
func (u ubuntuPackageInstaller) CheckPackages(ctx context.Context, requirements PackageRequirements) (PackageStatus, error) {
	status := PackageStatus{}
	if requirements.Docker {
		status.Docker = commandAvailable(ctx, u.runner, "docker")
	}
	if requirements.DockerSocketACL {
		status.DockerSocketACL = u.debianPackageInstalled(ctx, "acl")
	}
	if requirements.Buildx {
		status.Buildx = status.Docker && u.runner.Run(ctx, e2eexec.Command{
			Name: "docker", Args: []string{"buildx", "version"},
		}).ExitCode == 0
	}
	if requirements.ContainerToolkit {
		status.ContainerToolkit = u.containerToolkitReady(ctx, requirements)
	}
	if requirements.HostDCGM || requirements.DCGMDevelopment {
		var err error
		status.HostDCGM, status.DCGMDevelopment, err = u.checkDCGM(ctx, requirements)
		status.HostDCGMReason, err = notReadyReason(err)
		if err != nil {
			return status, err
		}
	}
	return status, nil
}

// InstallPackages configures signed Ubuntu repositories and installs requested packages.
func (u ubuntuPackageInstaller) InstallPackages(ctx context.Context, requirements PackageRequirements) error {
	if requirements.Docker || requirements.DockerSocketACL || requirements.Buildx {
		if err := u.installDocker(ctx, requirements); err != nil {
			return err
		}
	}
	if requirements.ContainerToolkit {
		if err := u.installContainerToolkit(ctx, requirements); err != nil {
			return err
		}
	}
	if requirements.HostDCGM || requirements.DCGMDevelopment {
		if err := u.installDCGM(ctx, requirements); err != nil {
			return err
		}
	}
	return nil
}

// checkDCGM reports whether the runtime and development packages are ready.
// Runtime packages are ready when they share one version, that version has the pinned upstream
// part, the approved CUDA repository offers it, and the library is visible. The development package
// must match the runtime version and be offered too. A failed condition comes back as a not-ready
// error; only a failed package query is a plain error.
func (u ubuntuPackageInstaller) checkDCGM(ctx context.Context, requirements PackageRequirements) (bool, bool, error) {
	query := dcgmPackageQueryCommand(requirements.DCGMDevelopment)
	query.Env = cLocaleEnv
	result := u.runner.Run(ctx, query)
	if dpkgQueryFailed(result) {
		return false, false, fmt.Errorf("query DCGM packages: exit %d: %s", result.ExitCode, commandResultText(result))
	}
	installed := parsePackageVersions(result.Stdout)

	runtimeNames := dcgmPackageNames(false)
	version, err := installedDCGMVersion(installed, runtimeNames, requirements.DCGMVersion)
	if err == nil {
		err = u.requireOfferedDCGMVersion(ctx, requirements, runtimeNames, version)
	}
	if err != nil {
		return false, false, err
	}
	if !HostDCGMLibraryAvailable(ctx, u.runner) {
		return false, false, notReady(dcgmLibraryNotVisible)
	}
	if !requirements.DCGMDevelopment {
		return true, true, nil
	}

	// The development package joins the runtime packages in the one-version rule.
	_, err = installedDCGMVersion(installed, dcgmPackageNames(true), requirements.DCGMVersion)
	if err == nil {
		err = u.requireOfferedDCGMVersion(ctx, requirements, []string{dcgmDevelopmentPackage}, version)
	}
	if err != nil {
		return true, false, err
	}
	if !HostDCGMInjectionLibraryAvailable(ctx, u.runner) {
		return true, false, notReady(dcgmInjectionLibraryNotVisible)
	}

	return true, true, nil
}

// dpkgQueryFailed reports whether dpkg-query failed for a reason other than unknown package names.
// dpkg-query exits non-zero when any queried package is unknown but still lists the installed ones,
// and then names the unknown packages on stderr; a failure without that message is a real failure.
func dpkgQueryFailed(result e2eexec.Result) bool {
	if result.ExitCode == 0 {
		return false
	}

	named := false
	for _, line := range strings.Split(string(result.Stderr), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(line, "no packages found matching") {
			return true
		}
		named = true
	}

	return !named
}

// installedDCGMVersion returns the one version shared by the named installed packages.
// It fails when a package is missing, the versions differ, or the upstream part is not the pin.
func installedDCGMVersion(installed map[string]string, names []string, pin string) (string, error) {
	var missing, found []string
	versions := make(map[string]bool)
	for _, name := range names {
		version := installed[name]
		if version == "" {
			missing = append(missing, name)
			continue
		}
		versions[version] = true
		found = append(found, name+"="+version)
	}

	if len(missing) != 0 {
		return "", notReady(dcgmNotInstalledFormat, strings.Join(missing, ", "))
	}
	if len(versions) != 1 {
		return "", notReady("installed DCGM packages are at different versions: %s", strings.Join(found, ", "))
	}
	version := installed[names[0]]
	if !dcgmVersionMatches(version, pin) {
		return "", notReady("installed DCGM version %s does not have the pinned upstream version %s", version, pin)
	}

	return version, nil
}

// dcgmVersionMatches reports whether a Debian version has the pinned upstream part.
func dcgmVersionMatches(version, pin string) bool {
	if _, withoutEpoch, ok := strings.Cut(version, ":"); ok {
		version = withoutEpoch
	}
	return strings.HasPrefix(version, pin+"-")
}

// requireOfferedDCGMVersion fails when the approved CUDA repository lists the pinned DCGM
// but does not offer the installed version of a package.
// Missing or unreadable APT lists are accepted, because prebuilt images remove them.
func (u ubuntuPackageInstaller) requireOfferedDCGMVersion(ctx context.Context, requirements PackageRequirements, names []string, version string) error {
	for _, name := range names {
		result := u.runner.Run(ctx, aptCacheMadisonCommand(name))
		if result.ExitCode != 0 {
			continue
		}

		offered := madisonVersions(result.Stdout, requirements.DCGMVersion, u.approvedRepositoryURL())
		if len(offered) == 0 || slices.Contains(offered, version) {
			continue
		}
		return notReady(
			"installed %s version %s is not offered by the approved NVIDIA CUDA repository, which offers %s",
			name, version, strings.Join(offered, ", "),
		)
	}

	return nil
}

// approvedRepositoryURL returns the one CUDA repository that E2E trusts for DCGM packages.
func (u ubuntuPackageInstaller) approvedRepositoryURL() string {
	return "https://developer.download.nvidia.com/compute/cuda/repos/ubuntu" +
		strings.ReplaceAll(u.hostOS.VersionID, ".", "") + "/" + u.hostOS.NVIDIARepositoryArch
}

// installDocker installs Ubuntu's Docker engine and Buildx plugin packages.
func (u ubuntuPackageInstaller) installDocker(ctx context.Context, requirements PackageRequirements) error {
	if err := runInstallCommand(ctx, u.stdout, u.runner, "apt_update_docker", aptGetCommand("update")); err != nil {
		return aptUpdateError(err)
	}
	args := []string{"install", "-y"}
	if requirements.Docker {
		args = append(args, "docker.io")
	}
	if requirements.DockerSocketACL {
		args = append(args, "acl")
	}
	if requirements.Buildx {
		buildxPackage := "docker-buildx"
		if !requirements.Docker && u.debianPackageInstalled(ctx, "docker-ce") {
			buildxPackage = "docker-buildx-plugin"
		}
		args = append(args, buildxPackage)
	}
	return runInstallCommand(ctx, u.stdout, u.runner, "apt_install_docker", aptGetCommand(args...))
}

// debianPackageInstalled reports whether dpkg considers a package fully installed.
func (u ubuntuPackageInstaller) debianPackageInstalled(ctx context.Context, name string) bool {
	result := u.runner.Run(ctx, e2eexec.Command{
		Name: "dpkg-query",
		Args: []string{"-W", "-f=${db:Status-Abbrev}", name},
	})
	return result.ExitCode == 0 && strings.TrimSpace(string(result.Stdout)) == "ii"
}

// containerToolkitReady requires the Toolkit command and every package that the local k3d node pins.
func (u ubuntuPackageInstaller) containerToolkitReady(ctx context.Context, requirements PackageRequirements) bool {
	if !commandAvailable(ctx, u.runner, "nvidia-ctk") {
		return false
	}
	result := u.runner.Run(ctx, containerToolkitPackageQueryCommand())
	if result.ExitCode != 0 {
		return false
	}
	installed := parsePackageVersions(result.Stdout)
	for _, name := range containerToolkitPackageNames() {
		if installed[name] != requirements.ContainerToolkitVersion {
			return false
		}
	}
	return true
}

// installContainerToolkit configures NVIDIA's signed stable APT repository and exact package versions.
func (u ubuntuPackageInstaller) installContainerToolkit(ctx context.Context, requirements PackageRequirements) error {
	tempDir, err := os.MkdirTemp("", "dcgm-exporter-e2e-toolkit-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	key := filepath.Join(tempDir, "toolkit.key")
	keyring := filepath.Join(tempDir, "toolkit.gpg")
	list := filepath.Join(tempDir, "toolkit.list")
	if err := runInstallCommand(ctx, u.stdout, u.runner, "download_nvidia_toolkit_key", e2eexec.Command{
		Name: "curl", Args: []string{"-fsSL", nvidiaToolkitKeyURL, "-o", key},
	}); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, u.stdout, u.runner, "dearmor_nvidia_toolkit_key", e2eexec.Command{
		Name: "gpg", Args: []string{"--batch", "--dearmor", "--output", keyring, key},
	}); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, u.stdout, u.runner, "install_nvidia_toolkit_key", sudoCommand("install", "-m", "0644", keyring, nvidiaToolkitKeyPath)); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, u.stdout, u.runner, "download_nvidia_toolkit_repository", e2eexec.Command{
		Name: "curl", Args: []string{"-fsSL", nvidiaToolkitListURL, "-o", list},
	}); err != nil {
		return err
	}
	content, err := os.ReadFile(list)
	if err != nil {
		return fmt.Errorf("read NVIDIA Container Toolkit repository: %w", err)
	}
	content, err = signedToolkitRepository(content)
	if err != nil {
		return err
	}
	if err := runInstallCommand(ctx, u.stdout, u.runner, "install_nvidia_toolkit_repository", sudoCommandWithInput(content, "tee", nvidiaToolkitListPath)); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, u.stdout, u.runner, "apt_update_nvidia_toolkit", aptGetCommand("update")); err != nil {
		return aptUpdateError(err)
	}
	args := []string{"install", "-y", "--allow-downgrades"}
	for _, name := range containerToolkitPackageNames() {
		args = append(args, name+"="+requirements.ContainerToolkitVersion)
	}
	return runInstallCommand(ctx, u.stdout, u.runner, "apt_install_nvidia_toolkit", aptGetCommand(args...))
}

// containerToolkitPackageNames returns the Toolkit package closure installed in local k3d nodes.
func containerToolkitPackageNames() []string {
	return []string{"nvidia-container-toolkit", "nvidia-container-toolkit-base", "libnvidia-container-tools", "libnvidia-container1"}
}

// containerToolkitPackageQueryCommand returns the native package query used to verify the exact Toolkit pin.
func containerToolkitPackageQueryCommand() e2eexec.Command {
	return e2eexec.Command{
		Name: "dpkg-query",
		Args: append([]string{"-W", "-f=${db:Status-Abbrev} ${Package}=${Version}\\n"}, containerToolkitPackageNames()...),
	}
}

// installDCGM configures NVIDIA's signed CUDA repository and pinned DCGM packages.
func (u ubuntuPackageInstaller) installDCGM(ctx context.Context, requirements PackageRequirements) error {
	if !requirements.HostDCGM {
		installed, err := u.installDevelopmentOnly(ctx, requirements)
		if installed || err != nil {
			return err
		}
	}

	repository := "ubuntu" + strings.ReplaceAll(u.hostOS.VersionID, ".", "")
	baseURL := u.approvedRepositoryURL()
	version, initialResolveErr := u.resolveDCGMVersion(ctx, requirements)
	if initialResolveErr != nil {
		configured, checkErr := u.cudaRepositoryProvidesDCGM(ctx, baseURL)
		if checkErr != nil {
			return errors.Join(initialResolveErr, checkErr)
		}
		if configured {
			return initialResolveErr
		}
		if err := u.configureCUDARepository(ctx, repository, baseURL); err != nil {
			return errors.Join(initialResolveErr, err)
		}
		if err := u.updateCUDARepository(ctx, repository, baseURL); err != nil {
			return errors.Join(initialResolveErr, err)
		}
		var err error
		version, err = u.resolveDCGMVersion(ctx, requirements)
		if err != nil {
			return errors.Join(initialResolveErr, err)
		}
	}

	return u.installDCGMPackages(ctx, dcgmPackageNames(requirements.DCGMDevelopment), version)
}

// installDevelopmentOnly leaves a ready runtime at its installed revision and installs just the
// development package at it. It reports false when the runtime is not ready or the development
// package is not offered at that revision, so the caller installs a complete newest set instead.
func (u ubuntuPackageInstaller) installDevelopmentOnly(ctx context.Context, requirements PackageRequirements) (bool, error) {
	version, ok := u.readyRuntimeVersion(ctx, requirements)
	if !ok {
		return false, nil
	}

	return u.installDevelopmentAtRuntimeVersion(ctx, requirements, version)
}

// installDevelopmentAtRuntimeVersion installs only the development package at the runtime's version.
// It refreshes the approved CUDA repository first when APT lists no DCGM from it, and reports false
// without installing when the repository does not offer the development package at that version.
func (u ubuntuPackageInstaller) installDevelopmentAtRuntimeVersion(ctx context.Context, requirements PackageRequirements, version string) (bool, error) {
	offered, err := u.offeredDevelopmentVersions(ctx, requirements)
	if err != nil {
		return false, err
	}

	if len(offered) == 0 {
		repository := "ubuntu" + strings.ReplaceAll(u.hostOS.VersionID, ".", "")
		baseURL := u.approvedRepositoryURL()
		configured, err := u.cudaRepositoryProvidesDCGM(ctx, baseURL)
		if err != nil {
			return false, err
		}
		if !configured {
			if err := u.configureCUDARepository(ctx, repository, baseURL); err != nil {
				return false, err
			}
			if err := u.updateCUDARepository(ctx, repository, baseURL); err != nil {
				return false, err
			}
		}
		offered, err = u.offeredDCGMVersions(ctx, requirements, dcgmDevelopmentPackage)
		if err != nil {
			return false, err
		}
	}
	if !slices.Contains(offered, version) {
		return false, nil
	}

	return true, u.installDCGMPackages(ctx, []string{dcgmDevelopmentPackage}, version)
}

// offeredDevelopmentVersions lists the development package's offered versions.
// The runtime readiness check accepts unreadable APT lists, so when the query
// fails it refreshes the lists once and asks again.
func (u ubuntuPackageInstaller) offeredDevelopmentVersions(ctx context.Context, requirements PackageRequirements) ([]string, error) {
	offered, queryErr := u.offeredDCGMVersions(ctx, requirements, dcgmDevelopmentPackage)
	if queryErr == nil {
		return offered, nil
	}

	if err := runInstallCommand(ctx, u.stdout, u.runner, "apt_update_dcgm", aptGetCommand("update")); err != nil {
		return nil, errors.Join(queryErr, aptUpdateError(err))
	}

	return u.offeredDCGMVersions(ctx, requirements, dcgmDevelopmentPackage)
}

// offeredDCGMVersions lists the pinned upstream's versions of one package in the approved repository.
func (u ubuntuPackageInstaller) offeredDCGMVersions(ctx context.Context, requirements PackageRequirements, name string) ([]string, error) {
	result := u.runner.Run(ctx, aptCacheMadisonCommand(name))
	if result.ExitCode != 0 {
		return nil, ubuntuPackageQueryError(name, result)
	}

	return madisonVersions(result.Stdout, requirements.DCGMVersion, u.approvedRepositoryURL()), nil
}

// installDCGMPackages installs the named DCGM packages at one version and refreshes the library cache.
// It also names, at that version, every split package they require at exactly that version, so a
// host APT pin that prefers another DCGM build cannot leave a dependency unresolvable. Every package
// it installs must come from the approved CUDA repository; see dcgmPackageClosure.
func (u ubuntuPackageInstaller) installDCGMPackages(ctx context.Context, names []string, version string) error {
	names, err := u.dcgmPackageClosure(ctx, names, version)
	if err != nil {
		return err
	}

	args := []string{"install", "-y", "--allow-downgrades"}
	for _, name := range names {
		args = append(args, name+"="+version)
	}
	if err := runInstallCommand(ctx, u.stdout, u.runner, "apt_install_dcgm", aptGetCommand(args...)); err != nil {
		return err
	}

	return runInstallCommand(ctx, u.stdout, u.runner, "ldconfig_dcgm", sudoCommand("ldconfig"))
}

// dcgmPackageClosure returns names followed by every package that they, and in turn those packages,
// require at exactly version, as the package metadata lists them. APT keeps its own candidate for a
// dependency, so each of these must be named with the version to install. Every returned package
// must be offered at version by the approved CUDA repository and by no other source, so that the
// install fetches it from there; otherwise it returns an error naming the package and the sources.
func (u ubuntuPackageInstaller) dcgmPackageClosure(ctx context.Context, names []string, version string) ([]string, error) {
	closure := slices.Clone(names)
	pending := names
	for len(pending) != 0 {
		for _, name := range pending {
			if err := u.requireApprovedDCGMSource(ctx, name, version); err != nil {
				return nil, err
			}
		}

		command := aptCacheShowCommand(pending, version)
		result := u.runner.Run(ctx, command)
		if result.ExitCode != 0 || result.Err != nil {
			return nil, fmt.Errorf("read the dependencies of DCGM %s packages %s: %s%s: %w",
				version, strings.Join(pending, " "), result.Stdout, result.Stderr, result.Err)
		}

		pending = nil
		for _, name := range exactVersionDependencies(string(result.Stdout), version) {
			if !slices.Contains(closure, name) {
				closure = append(closure, name)
				pending = append(pending, name)
			}
		}
	}
	return closure, nil
}

// requireApprovedDCGMSource checks that apt-cache madison lists name at version from the approved
// CUDA repository and from no other source.
func (u ubuntuPackageInstaller) requireApprovedDCGMSource(ctx context.Context, name, version string) error {
	result := u.runner.Run(ctx, aptCacheMadisonCommand(name))
	if result.ExitCode != 0 {
		return ubuntuPackageQueryError(name, result)
	}

	approvedURL := u.approvedRepositoryURL()
	approved, others := madisonSourcesOf(result.Stdout, version, approvedURL)
	if len(others) != 0 {
		return fmt.Errorf("DCGM package %s=%s is also offered by %s; E2E installs DCGM only from %s, so remove or disable the other source",
			name, version, strings.Join(others, ", "), approvedURL)
	}
	if !approved {
		return fmt.Errorf("DCGM package %s=%s is not offered by the approved CUDA repository %s", name, version, approvedURL)
	}
	return nil
}

// madisonSourcesOf reads apt-cache madison output for one package and reports whether the approved
// repository lists version, and which other package sources list it.
func madisonSourcesOf(output []byte, version, approvedURL string) (bool, []string) {
	approved := false
	var others []string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) < 3 || strings.TrimSpace(fields[1]) != version {
			continue
		}

		source := strings.TrimSpace(fields[2])
		switch {
		case approvedAPTSource(source, approvedURL):
			approved = true
		case !slices.Contains(others, source):
			others = append(others, source)
		}
	}
	return approved, others
}

// aptCacheShowCommand returns "apt-cache show" for each name at version.
func aptCacheShowCommand(names []string, version string) e2eexec.Command {
	args := []string{"show"}
	for _, name := range names {
		args = append(args, name+"="+version)
	}
	return e2eexec.Command{Name: "apt-cache", Args: args, Env: cLocaleEnv}
}

// exactVersionDependencies lists, in order and without repeats, the packages that the Depends and
// Pre-Depends fields of the apt-cache show stanzas require as "name (= version)". Other version
// constraints and other packages' versions are left out, and an architecture suffix is dropped.
// A group of alternatives ("a | b") is left to APT, because naming every choice would require all of them.
func exactVersionDependencies(stanzas, version string) []string {
	var names []string
	for _, value := range dependencyFieldValues(stanzas) {
		for _, group := range strings.Split(value, ",") {
			if strings.Contains(group, "|") {
				continue
			}

			name, constraint, ok := strings.Cut(strings.TrimSpace(group), " ")
			name, _, _ = strings.Cut(name, ":")
			if ok && strings.TrimSpace(constraint) == "(= "+version+")" && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// dependencyFieldValues returns the values of every Depends and Pre-Depends field in the stanzas,
// with each field's continuation lines, which start with a space or a tab, joined to it.
func dependencyFieldValues(stanzas string) []string {
	var values []string
	inField := false
	for _, line := range strings.Split(stanzas, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if inField {
				values[len(values)-1] += " " + strings.TrimSpace(line)
			}
			continue
		}

		field, value, ok := strings.Cut(line, ":")
		inField = ok && (field == "Depends" || field == "Pre-Depends")
		if inField {
			values = append(values, value)
		}
	}
	return values
}

// readyRuntimeVersion returns the installed runtime version when the runtime packages are already
// ready: one version, the pinned upstream part, and offered by the approved repository.
func (u ubuntuPackageInstaller) readyRuntimeVersion(ctx context.Context, requirements PackageRequirements) (string, bool) {
	query := dcgmPackageQueryCommand(false)
	query.Env = cLocaleEnv
	result := u.runner.Run(ctx, query)
	if dpkgQueryFailed(result) {
		return "", false
	}

	names := dcgmPackageNames(false)
	version, err := installedDCGMVersion(parsePackageVersions(result.Stdout), names, requirements.DCGMVersion)
	if err != nil || u.requireOfferedDCGMVersion(ctx, requirements, names, version) != nil {
		return "", false
	}

	return version, true
}

// updateCUDARepository refreshes the package index and repairs only a missing key for E2E's exact CUDA source.
// installDCGM calls it after adding that source so unowned APT failures remain unchanged.
func (u ubuntuPackageInstaller) updateCUDARepository(ctx context.Context, repository, baseURL string) error {
	err := runInstallCommand(ctx, u.stdout, u.runner, "apt_update_dcgm", aptGetCommand("update"))
	if err == nil {
		return nil
	}
	initialUpdateErr := aptUpdateError(err)
	if !cudaRepositoryMissingKey(err, baseURL) {
		return initialUpdateErr
	}
	if err := u.configureCUDARepository(ctx, repository, baseURL); err != nil {
		return errors.Join(initialUpdateErr, err)
	}
	if err := runInstallCommand(ctx, u.stdout, u.runner, "apt_update_dcgm_retry", aptGetCommand("update")); err != nil {
		return errors.Join(initialUpdateErr, aptUpdateError(err))
	}
	return nil
}

// aptUpdateError returns the package-manager error and explains that E2E leaves other sources alone.
// Ubuntu package installation uses it when an APT update prevents an E2E-owned install.
func aptUpdateError(err error) error {
	return fmt.Errorf("APT update failed before E2E could install its own dependency; repair the reported host package source or signing key. E2E does not change other sources: %w", err)
}

// cudaRepositoryMissingKey identifies a missing signing key from E2E's exact CUDA repository.
// updateCUDARepository uses it to decide whether one key repair is allowed.
func cudaRepositoryMissingKey(err error, baseURL string) bool {
	return strings.Contains(strings.ToUpper(err.Error()), "NO_PUBKEY") && strings.Contains(err.Error(), baseURL)
}

// cudaRepositoryProvidesDCGM reports whether APT sees any DCGM package from the approved CUDA repository.
func (u ubuntuPackageInstaller) cudaRepositoryProvidesDCGM(ctx context.Context, approvedURL string) (bool, error) {
	name := dcgmPackageNames(false)[0]
	result := u.runner.Run(ctx, aptCacheMadisonCommand(name))
	if result.ExitCode != 0 {
		return false, ubuntuPackageQueryError(name, result)
	}
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) >= 3 && approvedAPTSource(strings.TrimSpace(fields[2]), approvedURL) {
			return true, nil
		}
	}
	return false, nil
}

// configureCUDARepository installs the approved CUDA source when APT cannot already resolve the requested DCGM pin.
func (u ubuntuPackageInstaller) configureCUDARepository(ctx context.Context, repository, baseURL string) error {
	tempDir, err := os.MkdirTemp("", "dcgm-exporter-e2e-cuda-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	if u.hostOS.VersionID == "26.04" {
		return u.installCUDAKeyringPackage(ctx, tempDir, baseURL)
	}

	keyring := filepath.Join(tempDir, "cuda-archive-keyring.gpg")
	if err := runInstallCommand(ctx, u.stdout, u.runner, "download_cuda_keyring", e2eexec.Command{
		Name: "curl", Args: []string{"-fsSL", baseURL + "/cuda-archive-keyring.gpg", "-o", keyring},
	}); err != nil {
		return err
	}
	keyringPath := "/usr/share/keyrings/cuda-archive-keyring.gpg"
	if err := runInstallCommand(ctx, u.stdout, u.runner, "install_cuda_keyring", sudoCommand("install", "-m", "0644", keyring, keyringPath)); err != nil {
		return err
	}
	listPath := "/etc/apt/sources.list.d/cuda-" + repository + "-" + u.hostOS.NVIDIARepositoryArch + ".list"
	list := []byte("deb [signed-by=" + keyringPath + "] " + baseURL + "/ /\n")
	if err := runInstallCommand(ctx, u.stdout, u.runner, "install_cuda_repository", sudoCommandWithInput(list, "tee", listPath)); err != nil {
		return err
	}
	return nil
}

// installCUDAKeyringPackage installs the checksum-pinned trust package published for Ubuntu 26.04.
func (u ubuntuPackageInstaller) installCUDAKeyringPackage(ctx context.Context, tempDir, baseURL string) error {
	expectedSHA := ""
	switch u.hostOS.Architecture {
	case "amd64":
		expectedSHA = cudaKeyringUbuntu26AMD64SHA256
	case "arm64":
		expectedSHA = cudaKeyringUbuntu26ARM64SHA256
	default:
		return fmt.Errorf("unsupported Ubuntu 26.04 keyring architecture %q", u.hostOS.Architecture)
	}
	keyringPackage := filepath.Join(tempDir, cudaKeyringPackageName)
	if err := runInstallCommand(ctx, u.stdout, u.runner, "download_cuda_keyring_package", e2eexec.Command{
		Name: "curl", Args: []string{"-fsSL", baseURL + "/" + cudaKeyringPackageName, "-o", keyringPackage},
	}); err != nil {
		return err
	}
	verify := u.verifyChecksum
	if verify == nil {
		verify = verifySHA256
	}
	if err := verify(keyringPackage, expectedSHA); err != nil {
		return fmt.Errorf("CUDA keyring package checksum verification failed: %w", err)
	}
	return runInstallCommand(ctx, u.stdout, u.runner, "install_cuda_keyring_package", dpkgInstallCommand(keyringPackage))
}

// signedToolkitRepository adds the downloaded keyring to every active toolkit repository entry.
// installContainerToolkit uses it before writing E2E's toolkit source file.
func signedToolkitRepository(content []byte) ([]byte, error) {
	lines := strings.Split(string(content), "\n")
	active := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "deb ") {
			return nil, fmt.Errorf("unsupported NVIDIA Container Toolkit repository entry %q", trimmed)
		}
		if !approvedToolkitAPTEntry(trimmed) {
			return nil, fmt.Errorf("NVIDIA Container Toolkit repository uses an unapproved origin in entry %q", trimmed)
		}
		active++
		if !strings.Contains(trimmed, "signed-by=") {
			lines[index] = strings.Replace(line, "deb https://", "deb [signed-by="+nvidiaToolkitKeyPath+"] https://", 1)
		}
		if !strings.Contains(lines[index], "signed-by="+nvidiaToolkitKeyPath) {
			return nil, fmt.Errorf("NVIDIA Container Toolkit repository is not bound to %s", nvidiaToolkitKeyPath)
		}
	}
	if active == 0 {
		return nil, fmt.Errorf("NVIDIA Container Toolkit repository contains no active entries")
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// approvedToolkitAPTEntry reports whether an APT entry uses an approved toolkit repository URL.
func approvedToolkitAPTEntry(entry string) bool {
	fields := strings.Fields(entry)
	if len(fields) < 3 || fields[0] != "deb" {
		return false
	}

	urlIndex := 1
	if strings.HasPrefix(fields[urlIndex], "[") {
		for urlIndex < len(fields) && !strings.HasSuffix(fields[urlIndex], "]") {
			urlIndex++
		}
		urlIndex++
	}
	if urlIndex >= len(fields) {
		return false
	}

	switch fields[urlIndex] {
	case nvidiaToolkitStableRepositoryURL, nvidiaToolkitExperimentalRepoURL:
		return true
	default:
		return false
	}
}

// resolveDCGMVersion selects one full Debian version shared by all DCGM packages.
func (u ubuntuPackageInstaller) resolveDCGMVersion(ctx context.Context, requirements PackageRequirements) (string, error) {
	approvedURL := u.approvedRepositoryURL()
	resolved := ""
	for _, name := range dcgmPackageNames(requirements.DCGMDevelopment) {
		result := u.runner.Run(ctx, aptCacheMadisonCommand(name))
		if result.ExitCode != 0 {
			return "", ubuntuPackageQueryError(name, result)
		}
		versions := madisonVersions(result.Stdout, requirements.DCGMVersion, approvedURL)
		if len(versions) == 0 {
			return "", fmt.Errorf("approved Ubuntu CUDA repository does not provide %s version %s", name, requirements.DCGMVersion)
		}
		version := versions[0]
		for _, candidate := range versions[1:] {
			comparison := u.runner.Run(ctx, e2eexec.Command{Name: "dpkg", Args: []string{"--compare-versions", candidate, "gt", version}})
			if comparison.ExitCode == 0 {
				version = candidate
			}
		}
		if resolved != "" && resolved != version {
			return "", fmt.Errorf("approved Ubuntu CUDA repository has inconsistent DCGM identities %s and %s", resolved, version)
		}
		resolved = version
	}
	return resolved, nil
}

// madisonVersions returns complete identities from the exact approved repository.
func madisonVersions(output []byte, semanticVersion, approvedURL string) []string {
	var versions []string
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			continue
		}
		version := strings.TrimSpace(fields[1])
		source := strings.TrimSpace(fields[2])
		withoutEpoch := version
		if _, remainder, ok := strings.Cut(version, ":"); ok {
			withoutEpoch = remainder
		}
		if strings.HasPrefix(withoutEpoch, semanticVersion+"-") && approvedAPTSource(source, approvedURL) {
			versions = append(versions, version)
		}
	}
	return versions
}

// approvedAPTSource compares the candidate's exact HTTPS origin and repository path.
func approvedAPTSource(source, approved string) bool {
	candidateText := strings.Fields(source)
	if len(candidateText) == 0 {
		return false
	}
	candidate, err := url.Parse(candidateText[0])
	if err != nil {
		return false
	}
	expected, err := url.Parse(approved)
	if err != nil {
		return false
	}
	return candidate.Scheme == "https" &&
		candidate.User == nil && candidate.RawQuery == "" && candidate.Fragment == "" &&
		strings.EqualFold(candidate.Host, expected.Host) &&
		strings.TrimSuffix(candidate.Path, "/") == strings.TrimSuffix(expected.Path, "/")
}

// parsePackageVersions returns versions only for packages in the installed state.
func parsePackageVersions(output []byte) map[string]string {
	versions := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "ii" {
			continue
		}
		name, version, ok := strings.Cut(fields[1], "=")
		if ok {
			versions[name] = version
		}
	}
	return versions
}

// debianNoninteractiveEnv keeps apt-get and dpkg from waiting for an answer:
// debconf takes its defaults, and needrestart, which Ubuntu runs after each
// install, is skipped so it neither asks nor restarts services on a shared host.
var debianNoninteractiveEnv = []string{"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_SUSPEND=1"}

// keepConfigFilesOptions make dpkg keep a configuration file the host has
// changed instead of asking whether to replace it.
var keepConfigFilesOptions = []string{"--force-confdef", "--force-confold"}

// aptGetCommand returns apt-get with args, run as root, so that it never stops
// at a prompt and keeps changed configuration files.
func aptGetCommand(args ...string) e2eexec.Command {
	var options []string
	for _, option := range keepConfigFilesOptions {
		options = append(options, "-o", "Dpkg::Options::="+option)
	}
	return noninteractive(sudoCommand("apt-get", append(options, args...)...))
}

// dpkgInstallCommand returns "dpkg -i path", run as root, so that it never
// stops at a prompt and keeps changed configuration files.
func dpkgInstallCommand(path string) e2eexec.Command {
	return noninteractive(sudoCommand("dpkg", append(slices.Clone(keepConfigFilesOptions), "-i", path)...))
}

// noninteractive adds debianNoninteractiveEnv to a command. sudo resets the
// environment, so for a sudo command the variables also go on its command
// line, where sudo allows them for a rule that permits ALL commands.
func noninteractive(command e2eexec.Command) e2eexec.Command {
	if command.Name == "sudo" {
		command.Args = append(slices.Clone(debianNoninteractiveEnv), command.Args...)
	}
	command.Env = slices.Clone(debianNoninteractiveEnv)
	return command
}

// sudoCommandWithInput wraps a command with sudo while preserving standard input.
func sudoCommandWithInput(input []byte, name string, args ...string) e2eexec.Command {
	command := sudoCommand(name, args...)
	command.Stdin = input
	return command
}

// ubuntuPackageQueryError reports a failed query for one Ubuntu package with
// the command's output.
func ubuntuPackageQueryError(name string, result e2eexec.Result) error {
	return fmt.Errorf("query Ubuntu package %s: %s%s", name, result.Stdout, result.Stderr)
}
