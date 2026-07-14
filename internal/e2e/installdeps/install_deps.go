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

// Package installdeps installs or verifies host tools used by e2e runs.
package installdeps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// IfRequested installs or verifies only the host dependencies needed by selected scenarios.
func IfRequested(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts *config.Tests) error {
	if !opts.InstallDeps || opts.BuildOnly {
		return nil
	}
	cfg := config.Config{Tests: *opts}
	k8sSelected := len(scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteK8s, cfg)) != 0
	hostSelected := len(scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteHost, cfg)) != 0
	containerSelected := len(scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteContainer, cfg)) != 0
	if k8sSelected {
		if err := installK8sClientTools(ctx, stdout, root, runner, *opts); err != nil {
			return err
		}
	}
	needsDocker := containerSelected || len(opts.DockerRegistryLogins) != 0 || opts.DockerRegistryLoginFile != "" || (k8sSelected && opts.Kubeconfig == "")
	if needsDocker {
		if err := ensureDocker(ctx, stdout, runner, *opts); err != nil {
			return err
		}
	}
	if k8sSelected && opts.Kubeconfig == "" {
		if err := ensureNvidiaContainerToolkit(ctx, stdout, runner, *opts); err != nil {
			return err
		}
	}
	if hostSelected {
		if err := ensureHostDCGM(ctx, stdout, runner, *opts); err != nil {
			return err
		}
	}
	hostDCGMURI := scenario.MustFind(scenario.Catalog, scenario.SuiteHost, "dcgmUri")
	containerRemoteDCGMURI := scenario.MustFind(scenario.Catalog, scenario.SuiteContainer, "remoteDcgmUri")
	hostDCGMURISelected := opts.DCGMNVMLInjectionYAML == "" && hostScenarioSelected(*opts, hostDCGMURI.Selector())
	if hostDCGMURISelected || containerScenarioSelected(*opts, containerRemoteDCGMURI.Selector()) {
		if err := ensureVSOCK(ctx, stdout, runner, *opts); err != nil {
			return err
		}
	}
	return nil
}

// installK8sClientTools installs pinned k3d, kubectl, and Helm helpers when missing or mismatched.
func installK8sClientTools(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.Tests) error {
	binDir := toolsBinDir(root, opts)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	arch, err := helperToolArch()
	if err != nil {
		return err
	}

	if opts.Kubeconfig == "" {
		k3dVersion := strings.TrimSpace(opts.Dependencies.K3DVersion)
		if k3dVersion == "" {
			return fmt.Errorf("K3D_VERSION is required")
		}
		if err := installExecutableIfMissing(ctx, stdout, runner, "k3d", k3dVersion, "https://github.com/k3d-io/k3d/releases/download/v"+k3dVersion+"/k3d-linux-"+arch, filepath.Join(binDir, "k3d"), dependencySHA(opts.Dependencies, "K3D", arch)); err != nil {
			return err
		}
	}

	k3sVersion := strings.TrimSpace(opts.Dependencies.K3SVersion)
	if k3sVersion == "" {
		return fmt.Errorf("K3S_VERSION is required")
	}
	kubectlVersion := strings.SplitN(k3sVersion, "-", 2)[0]
	if err := installExecutableIfMissing(ctx, stdout, runner, "kubectl", kubectlVersion, "https://dl.k8s.io/release/"+kubectlVersion+"/bin/linux/"+arch+"/kubectl", filepath.Join(binDir, "kubectl"), dependencySHA(opts.Dependencies, "KUBECTL", arch)); err != nil {
		return err
	}

	helmVersion := strings.TrimSpace(opts.Dependencies.HelmVersion)
	if helmVersion == "" {
		return fmt.Errorf("HELM_VERSION is required")
	}
	return installHelmIfMissing(ctx, stdout, runner, helmVersion, arch, filepath.Join(binDir, "helm"), dependencySHA(opts.Dependencies, "HELM", arch))
}

// installExecutableIfMissing downloads and checksum-verifies a single helper executable.
func installExecutableIfMissing(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, name, version, url, dest, expectedSHA string) error {
	if helperToolVersionMatches(ctx, runner, name, version) {
		return nil
	}
	if strings.TrimSpace(expectedSHA) == "" {
		return fmt.Errorf("%s is required", helperSHAKey(strings.ToUpper(name), runtime.GOARCH))
	}
	fmt.Fprintf(stdout, "[e2e] Installing %s %s into %s\n", name, version, filepath.Dir(dest))
	if err := runInstallCommand(ctx, stdout, runner, "install_"+name+"_download", e2eexec.Command{Name: "curl", Args: []string{"-fsSL", url, "-o", dest}}); err != nil {
		return err
	}
	if err := verifySHA256(dest, expectedSHA); err != nil {
		return fmt.Errorf("%s checksum verification failed: %w", name, err)
	}
	return runInstallCommand(ctx, stdout, runner, "install_"+name+"_chmod", e2eexec.Command{Name: "chmod", Args: []string{"+x", dest}})
}

// installHelmIfMissing downloads, verifies, and extracts the pinned Helm release.
func installHelmIfMissing(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, version, arch, dest, expectedSHA string) error {
	if helperToolVersionMatches(ctx, runner, "helm", version) {
		return nil
	}
	if strings.TrimSpace(expectedSHA) == "" {
		return fmt.Errorf("%s is required", helperSHAKey("HELM", arch))
	}
	tmpDir, err := os.MkdirTemp("", "dcgm-exporter-e2e-helm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	tarball := filepath.Join(tmpDir, "helm.tar.gz")
	url := "https://get.helm.sh/helm-v" + version + "-linux-" + arch + ".tar.gz"
	fmt.Fprintf(stdout, "[e2e] Installing Helm %s into %s\n", version, filepath.Dir(dest))
	if err := runInstallCommand(ctx, stdout, runner, "install_helm_download", e2eexec.Command{Name: "curl", Args: []string{"-fsSL", url, "-o", tarball}}); err != nil {
		return err
	}
	if err := verifySHA256(tarball, expectedSHA); err != nil {
		return fmt.Errorf("helm checksum verification failed: %w", err)
	}
	if err := runInstallCommand(ctx, stdout, runner, "install_helm_extract", e2eexec.Command{Name: "tar", Args: []string{"-xzf", tarball, "-C", tmpDir}}); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, stdout, runner, "install_helm_move", e2eexec.Command{Name: "mv", Args: []string{"-f", filepath.Join(tmpDir, "linux-"+arch, "helm"), dest}}); err != nil {
		return err
	}
	return runInstallCommand(ctx, stdout, runner, "install_helm_chmod", e2eexec.Command{Name: "chmod", Args: []string{"+x", dest}})
}

// helperSHAKey builds the checked-in checksum key for a helper tool and architecture.
func helperSHAKey(name, arch string) string {
	return name + "_LINUX_" + strings.ToUpper(arch) + "_SHA256"
}

// dependencySHA returns a helper-tool checksum supplied through the CLI environment.
func dependencySHA(deps config.Dependencies, name, arch string) string {
	switch helperSHAKey(name, arch) {
	case "K3D_LINUX_AMD64_SHA256":
		return deps.K3DAMD64SHA256
	case "K3D_LINUX_ARM64_SHA256":
		return deps.K3DARM64SHA256
	case "KUBECTL_LINUX_AMD64_SHA256":
		return deps.KubectlAMD64SHA256
	case "KUBECTL_LINUX_ARM64_SHA256":
		return deps.KubectlARM64SHA256
	case "HELM_LINUX_AMD64_SHA256":
		return deps.HelmAMD64SHA256
	case "HELM_LINUX_ARM64_SHA256":
		return deps.HelmARM64SHA256
	default:
		return ""
	}
}

// verifySHA256 checks a downloaded helper artifact against its pinned checksum.
func verifySHA256(path, expected string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, strings.TrimSpace(expected)) {
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s", path, got, strings.TrimSpace(expected))
	}
	return nil
}

// helperToolVersionMatches reports whether an existing helper satisfies the required version.
func helperToolVersionMatches(ctx context.Context, runner e2eexec.Runner, name, version string) bool {
	args := map[string][]string{
		"k3d":     {"version"},
		"kubectl": {"version", "--client=true"},
		"helm":    {"version", "--short"},
	}[name]
	if len(args) == 0 {
		return commandAvailable(ctx, runner, name)
	}
	result := runner.Run(ctx, e2eexec.Command{Name: name, Args: args})
	if result.ExitCode != 0 {
		return false
	}
	output := string(result.Stdout) + string(result.Stderr)
	return versionOutputMatches(output, version)
}

// versionOutputMatches accepts version output with or without a leading v.
func versionOutputMatches(output, version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	if strings.Contains(output, version) {
		return true
	}
	if !strings.HasPrefix(version, "v") && strings.Contains(output, "v"+version) {
		return true
	}
	return false
}

// commandAvailable checks whether a command can be resolved by the current shell.
func commandAvailable(ctx context.Context, runner e2eexec.Runner, name string) bool {
	return runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", "command -v " + name}}).ExitCode == 0
}

// ensureDocker verifies Docker access and optionally installs or restarts it on apt-based hosts.
func ensureDocker(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.Tests) error {
	if !commandAvailable(ctx, runner, "docker") {
		if !installControlEnabled(opts.InstallDepsDocker, opts.InstallDeps) {
			return fmt.Errorf("docker is missing and --install-deps-docker=false disabled Docker installation")
		}
		if !commandAvailable(ctx, runner, "apt-get") {
			return fmt.Errorf("docker is missing and this host does not have apt-get for automatic installation")
		}
		if err := runInstallCommand(ctx, stdout, runner, "apt_update_docker", sudoCommand("apt-get", "update")); err != nil {
			return err
		}
		if err := runInstallCommand(ctx, stdout, runner, "apt_install_docker", sudoCommand("apt-get", "install", "-y", "docker.io")); err != nil {
			return err
		}
	}
	if runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"info"}}).ExitCode == 0 {
		return nil
	}
	if !installControlEnabled(opts.InstallDepsDocker, opts.InstallDeps) {
		return fmt.Errorf("docker is not reachable and --install-deps-docker=false disabled Docker setup")
	}
	_ = runInstallCommand(ctx, stdout, runner, "systemctl_reset_docker", sudoCommand("systemctl", "reset-failed", "docker", "docker.socket"))
	_ = runInstallCommand(ctx, stdout, runner, "systemctl_start_containerd", sudoCommand("systemctl", "enable", "--now", "containerd"))
	_ = runInstallCommand(ctx, stdout, runner, "systemctl_start_docker_socket", sudoCommand("systemctl", "enable", "--now", "docker.socket"))
	_ = runInstallCommand(ctx, stdout, runner, "systemctl_start_docker", sudoCommand("systemctl", "start", "docker"))
	if username := currentUsername(); username != "" && commandAvailable(ctx, runner, "setfacl") {
		_ = runInstallCommand(ctx, stdout, runner, "docker_socket_acl", sudoCommand("setfacl", "-m", "u:"+username+":rw", "/var/run/docker.sock"))
	}
	if runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"info"}}).ExitCode != 0 {
		return fmt.Errorf("docker is not reachable by this user")
	}
	return nil
}

// ensureNvidiaContainerToolkit verifies Docker GPU access and optionally installs the toolkit.
func ensureNvidiaContainerToolkit(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.Tests) error {
	if !commandAvailable(ctx, runner, "nvidia-smi") {
		return fmt.Errorf("nvidia-smi is required for local GPU k3d validation")
	}
	ref, err := e2eimage.Parse(opts.ContainerToolkitTestImage)
	if err != nil {
		return fmt.Errorf("--container-toolkit-test-image/E2E_CONTAINER_TOOLKIT_TEST_IMAGE: %w", err)
	}
	smokeImage := ref.Pull()
	if runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"run", "--rm", "--gpus", "all", smokeImage, "nvidia-smi"}}).ExitCode == 0 {
		return nil
	}
	if !installControlEnabled(opts.InstallDepsNvidiaToolkit, opts.InstallDeps) {
		return fmt.Errorf("docker GPU smoke test failed and --install-deps-nvidia-container-toolkit=false disabled NVIDIA Container Toolkit setup")
	}
	if !commandAvailable(ctx, runner, "apt-get") {
		return fmt.Errorf("nvidia container toolkit setup requires apt-get on this host")
	}
	if err := runInstallCommand(ctx, stdout, runner, "apt_update_nvidia_toolkit", sudoCommand("apt-get", "update")); err != nil {
		return err
	}
	if err := runInstallCommand(ctx, stdout, runner, "apt_install_nvidia_toolkit", sudoCommand("apt-get", "install", "-y", "nvidia-container-toolkit")); err != nil {
		return err
	}
	_ = runInstallCommand(ctx, stdout, runner, "nvidia_ctk_docker_runtime", sudoCommand("nvidia-ctk", "runtime", "configure", "--runtime=docker"))
	_ = runInstallCommand(ctx, stdout, runner, "systemctl_restart_docker_socket", sudoCommand("systemctl", "restart", "docker.socket"))
	_ = runInstallCommand(ctx, stdout, runner, "systemctl_restart_docker", sudoCommand("systemctl", "restart", "docker"))
	if runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"run", "--rm", "--gpus", "all", smokeImage, "nvidia-smi"}}).ExitCode != 0 {
		return fmt.Errorf("docker GPU smoke test failed after NVIDIA Container Toolkit setup")
	}
	return nil
}

// ensureHostDCGM verifies libdcgm and optionally installs pinned host DCGM packages.
func ensureHostDCGM(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.Tests) error {
	dcgmVersion := strings.TrimSpace(opts.Dependencies.DCGMVersion)
	if dcgmVersion == "" {
		return fmt.Errorf("--dcgm-version/E2E_DCGM_VERSION is required")
	}
	version := "1:" + dcgmVersion + "-1"
	requireInjection := opts.DCGMNVMLInjectionYAML != ""
	librariesAvailable := HostDCGMLibraryAvailable(ctx, runner) &&
		(!requireInjection || HostDCGMInjectionLibraryAvailable(ctx, runner))
	if librariesAvailable && hostDCGMPackagesMatch(ctx, runner, version, requireInjection) {
		return nil
	}
	if !installControlEnabled(opts.InstallDepsDCGM, opts.InstallDeps) {
		fmt.Fprintln(stdout, "[e2e] Host DCGM install disabled; direct host integration will run only if libdcgm.so.4 is already available")
		return nil
	}
	if !commandAvailable(ctx, runner, "apt-get") {
		return fmt.Errorf("host DCGM is missing and this host does not have apt-get for automatic installation")
	}
	if err := runInstallCommand(ctx, stdout, runner, "apt_update_dcgm", sudoCommand("apt-get", "update")); err != nil {
		return err
	}
	packages := []string{
		"install",
		"-y",
		"--allow-downgrades",
		"datacenter-gpu-manager-4-core=" + version,
		"datacenter-gpu-manager-4-proprietary=" + version,
	}
	if requireInjection {
		packages = append(packages, "datacenter-gpu-manager-4-dev="+version)
	}
	if err := runInstallCommand(ctx, stdout, runner, "apt_install_dcgm", sudoCommand("apt-get", packages...)); err != nil {
		return err
	}
	_ = runInstallCommand(ctx, stdout, runner, "ldconfig_dcgm", sudoCommand("ldconfig"))
	if !HostDCGMLibraryAvailable(ctx, runner) {
		return fmt.Errorf("host DCGM %s was installed but libdcgm.so.4 is still unavailable", version)
	}
	if requireInjection && !HostDCGMInjectionLibraryAvailable(ctx, runner) {
		return fmt.Errorf("host DCGM %s was installed but libnvml_injection.so is still unavailable", version)
	}
	if !hostDCGMPackagesMatch(ctx, runner, version, requireInjection) {
		return fmt.Errorf("host DCGM package version does not match selected version %s after installation", version)
	}
	return nil
}

// ensureVSOCK verifies the loopback VSOCK device and optionally loads the kernel module.
func ensureVSOCK(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.Tests) error {
	if runner.Run(ctx, e2eexec.Command{Name: "test", Args: []string{"-e", "/dev/vsock"}}).ExitCode == 0 &&
		runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", "grep -q '^vsock_loopback[[:space:]]' /proc/modules"}}).ExitCode == 0 {
		return nil
	}
	if !installControlEnabled(opts.InstallDepsVSOCK, opts.InstallDeps) {
		return fmt.Errorf("vsock loopback is unavailable and --install-deps-vsock=false disabled loading it")
	}
	if err := runInstallCommand(ctx, stdout, runner, "modprobe_vsock_loopback", sudoCommand("modprobe", "vsock_loopback")); err != nil {
		return err
	}
	return nil
}

// HostDCGMLibraryAvailable reports whether libdcgm.so is visible to direct host tests.
func HostDCGMLibraryAvailable(ctx context.Context, runner e2eexec.Runner) bool {
	return runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", "ldconfig -p 2>/dev/null | grep -Eq 'libdcgm\\.so(\\.4)?' || test -e /usr/lib/x86_64-linux-gnu/libdcgm.so.4 || test -e /usr/lib/aarch64-linux-gnu/libdcgm.so.4"}}).ExitCode == 0
}

// HostDCGMInjectionLibraryAvailable reports whether the official NVML injection library is installed.
func HostDCGMInjectionLibraryAvailable(ctx context.Context, runner e2eexec.Runner) bool {
	const script = "ldconfig -p 2>/dev/null | grep -Eq 'libnvml_injection\\.so(\\.1)?' || " +
		"test -e /usr/lib/x86_64-linux-gnu/libnvml_injection.so || " +
		"test -e /usr/lib/aarch64-linux-gnu/libnvml_injection.so"
	return runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", script}}).ExitCode == 0
}

// hostDCGMPackagesMatch checks installed DCGM package versions against the pinned version.
func hostDCGMPackagesMatch(ctx context.Context, runner e2eexec.Runner, version string, requireInjection bool) bool {
	result := runner.Run(ctx, dcgmPackageQueryCommand(requireInjection))
	if result.ExitCode != 0 {
		return false
	}
	versions := map[string]string{}
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			versions[name] = value
		}
	}
	matched := versions["datacenter-gpu-manager-4-core"] == version &&
		versions["datacenter-gpu-manager-4-proprietary"] == version
	return matched && (!requireInjection || versions["datacenter-gpu-manager-4-dev"] == version)
}

// dcgmPackageQueryCommand returns the dpkg query used to verify host DCGM packages.
func dcgmPackageQueryCommand(requireInjection bool) e2eexec.Command {
	packages := []string{
		"datacenter-gpu-manager-4-core",
		"datacenter-gpu-manager-4-proprietary",
	}
	if requireInjection {
		packages = append(packages, "datacenter-gpu-manager-4-dev")
	}
	return e2eexec.Command{
		Name: "dpkg-query",
		Args: append([]string{
			"-W",
			"-f=${Package}=${Version}\n",
		}, packages...),
	}
}

// sudoCommand wraps a command with sudo when the current process is not root and sudo exists.
func sudoCommand(name string, args ...string) e2eexec.Command {
	if os.Geteuid() == 0 {
		return e2eexec.Command{Name: name, Args: args}
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return e2eexec.Command{Name: name, Args: args}
	}
	return e2eexec.Command{Name: "sudo", Args: append([]string{name}, args...)}
}

// currentUsername returns the local username used for Docker socket ACL repair.
func currentUsername() string {
	if current, err := user.Current(); err == nil && current.Username != "" {
		if slash := strings.LastIndexAny(current.Username, `\`); slash >= 0 {
			return current.Username[slash+1:]
		}
		return current.Username
	}
	return os.Getenv("USER")
}

// installControlEnabled combines a per-dependency install flag with the global install default.
func installControlEnabled(value string, global bool) bool {
	if value == "" {
		return global
	}
	parsed, err := parseBool(value)
	if err != nil {
		return false
	}
	return parsed
}

// parseBool accepts common shell-style boolean spellings.
func parseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "t", "yes", "y", "on":
		return true, nil
	case "false", "0", "f", "no", "n", "off":
		return false, nil
	default:
		return strconv.ParseBool(value)
	}
}

// hostScenarioSelected reports whether selector survives host suite selection filters.
func hostScenarioSelected(opts config.Tests, selector string) bool {
	for _, entry := range scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteHost, config.Config{Tests: opts}) {
		if entry.Selector() == selector {
			return true
		}
	}
	return false
}

// containerScenarioSelected reports whether selector survives container suite selection filters.
func containerScenarioSelected(opts config.Tests, selector string) bool {
	for _, entry := range scenario.SelectedForSuite(scenario.Catalog, scenario.SuiteContainer, config.Config{Tests: opts}) {
		if entry.Selector() == selector {
			return true
		}
	}
	return false
}

// runInstallCommand logs a host mutation step and fails when it exits non-zero.
func runInstallCommand(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, name string, command e2eexec.Command) error {
	fmt.Fprintf(stdout, "[e2e] step: %s\n", name)
	command.Stdout = stdout
	command.Stderr = stdout
	command.LogName = name
	command.QuietOnSuccess = true
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return fmt.Errorf("%s failed: %s%s", installCommandString(command), result.Stdout, result.Stderr)
	}
	return nil
}

// installCommandString renders a command for human-readable e2e logs.
func installCommandString(command e2eexec.Command) string {
	out := command.Name
	for _, arg := range command.Args {
		out += " " + arg
	}
	return out
}

// helperToolArch returns the helper-tool architecture name supported by upstream release assets.
func helperToolArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("unsupported architecture for helper tool auto-install: %s", runtime.GOARCH)
	}
}

// ConfigureToolPath prepends the harness tool directory to PATH and returns a restore function.
func ConfigureToolPath(root string, opts config.Tests) (func(), error) {
	binDir := toolsBinDir(root, opts)
	oldPath, hadPath := os.LookupEnv("PATH")
	newPath := binDir
	if oldPath != "" {
		newPath += string(os.PathListSeparator) + oldPath
	}
	if err := os.Setenv("PATH", newPath); err != nil {
		return nil, err
	}
	return func() {
		if hadPath {
			_ = os.Setenv("PATH", oldPath)
			return
		}
		_ = os.Unsetenv("PATH")
	}, nil
}

// toolsBinDir returns the bin subdirectory used for helper tools.
func toolsBinDir(root string, opts config.Tests) string {
	return filepath.Join(TestsToolsDir(root, opts), "bin")
}

// TestsToolsDir returns the root directory used for helper tools and source-tree suite binaries.
func TestsToolsDir(root string, opts config.Tests) string {
	if opts.ToolsDir != "" {
		return resolveRootPath(root, opts.ToolsDir)
	}
	if env := os.Getenv("E2E_TOOLS_DIR"); env != "" {
		return resolveRootPath(root, env)
	}
	return filepath.Join(root, ".e2e-tools")
}

// resolveRootPath resolves relative option paths against the source or package root.
func resolveRootPath(root, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(root, value)
}
