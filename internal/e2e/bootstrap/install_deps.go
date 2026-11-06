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

// Package bootstrap installs or verifies host tools used by E2E runs.
package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// PreparedDependencies records the host requirements and host name prepared before suite execution.
// The CLI passes it to the GPU runtime smoke check after Docker login.
type PreparedDependencies struct {
	Requirements DependencyRequirements
	HostOS       string
}

// RunIfEnabled installs or verifies only the supplied host requirements.
// The CLI selects scenarios and builds requirements before calling this function.
func RunIfEnabled(
	ctx context.Context,
	stdout io.Writer,
	root string,
	runner e2eexec.Runner,
	opts *config.TestOptions,
	requirements []scenario.SelectedRequirement,
) (PreparedDependencies, error) {
	if opts.BuildOnly || opts.SkipsAllBootstrap() {
		return PreparedDependencies{}, nil
	}
	if opts.BootstrapMode == "" {
		opts.BootstrapMode = config.BootstrapCheck
	}
	dependencies, err := BuildDependencyRequirements(requirements)
	if err != nil {
		return PreparedDependencies{}, err
	}
	prepared := PreparedDependencies{Requirements: dependencies, HostOS: "not-detected"}
	detectedHost := HostOS{}
	hostDetected := false
	detectHost := func() (HostOS, error) {
		if hostDetected {
			return detectedHost, nil
		}
		host, detectErr := DetectHostOS()
		if detectErr != nil {
			return HostOS{}, detectErr
		}
		detectedHost = host
		hostDetected = true
		prepared.HostOS = hostOSName(host)
		return detectedHost, nil
	}
	if dependencies.GPURuntime && opts.BootstrapModeFor(config.BootstrapGPURuntime) != config.BootstrapSkip {
		if _, err := detectHost(); err != nil {
			return PreparedDependencies{}, bootstrapError("unknown", "gpu-runtime", "not-applicable", "detect-host", err)
		}
	}
	bootstrap := DependencyBootstrap{
		RootDir:       root,
		Output:        stdout,
		CommandRunner: runner,
		DetectHostOS:  detectHost,
		KnownHostOS:   detectedHost,
	}
	if err := bootstrap.Run(ctx, *opts, dependencies); err != nil {
		return PreparedDependencies{}, err
	}
	return prepared, nil
}

// CheckGPURuntime runs the GPU-container smoke check after the CLI has completed Docker login.
// It uses the existing gpu-runtime policy to decide whether repair is allowed.
func CheckGPURuntime(
	ctx context.Context,
	stdout io.Writer,
	runner e2eexec.Runner,
	opts config.TestOptions,
	prepared PreparedDependencies,
) error {
	if opts.BuildOnly || opts.SkipsAllBootstrap() || !prepared.Requirements.GPURuntime ||
		opts.BootstrapModeFor(config.BootstrapGPURuntime) == config.BootstrapSkip {
		return nil
	}
	if err := checkGPUContainerRuntime(ctx, stdout, runner, opts, opts.BootstrapModeFor(config.BootstrapGPURuntime) == config.BootstrapInstall); err != nil {
		return bootstrapError(prepared.HostOS, "gpu-runtime", "not-applicable", phaseForBootstrapMode(opts.BootstrapModeFor(config.BootstrapGPURuntime)), err)
	}
	return nil
}

// checkGPUHealth records the installed driver before E2E changes host dependencies.
// DependencyBootstrap.Run uses it for GPU-dependent runs before package checks begin.
func checkGPUHealth(ctx context.Context, stdout io.Writer, runner e2eexec.Runner) error {
	result := runner.Run(ctx, e2eexec.Command{
		Name: "nvidia-smi",
		Args: []string{"--query-gpu=driver_version", "--format=csv,noheader"},
	})
	if result.ExitCode != 0 || result.Err != nil {
		return fmt.Errorf("nvidia-smi did not report a working driver: %s", commandResultText(result))
	}
	versions := strings.TrimSpace(string(result.Stdout))
	if versions == "" {
		versions = "not-reported"
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "[e2e] bootstrap: gpu_driver=%s\n", versions)
	}
	return nil
}

// verifyK8sClientTools checks pinned helper versions without creating directories or downloading files.
func verifyK8sClientTools(ctx context.Context, runner e2eexec.Runner, opts config.TestOptions) error {
	if opts.Kubeconfig == "" {
		version := strings.TrimSpace(opts.Dependencies.K3DVersion)
		if version == "" {
			return fmt.Errorf("K3D_VERSION is required")
		}
		if !helperToolVersionMatches(ctx, runner, "k3d", version) {
			return fmt.Errorf("k3d %s is required; rerun with --bootstrap=install", version)
		}
	}
	k3sVersion := strings.TrimSpace(opts.Dependencies.K3SVersion)
	if k3sVersion == "" {
		return fmt.Errorf("K3S_VERSION is required")
	}
	kubectlVersion := strings.SplitN(k3sVersion, "-", 2)[0]
	if !helperToolVersionMatches(ctx, runner, "kubectl", kubectlVersion) {
		return fmt.Errorf("kubectl %s is required; rerun with --bootstrap=install", kubectlVersion)
	}
	helmVersion := strings.TrimSpace(opts.Dependencies.HelmVersion)
	if helmVersion == "" {
		return fmt.Errorf("HELM_VERSION is required")
	}
	if !helperToolVersionMatches(ctx, runner, "helm", helmVersion) {
		return fmt.Errorf("helm %s is required; rerun with --bootstrap=install", helmVersion)
	}
	return nil
}

// installK8sClientTools installs pinned k3d, kubectl, and Helm helpers when missing or mismatched.
func installK8sClientTools(ctx context.Context, stdout io.Writer, root string, runner e2eexec.Runner, opts config.TestOptions) error {
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
	temporary, err := os.CreateTemp(filepath.Dir(dest), "."+name+"-download-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	if err := runInstallCommand(ctx, stdout, runner, "install_"+name+"_download", e2eexec.Command{Name: "curl", Args: []string{"-fsSL", url, "-o", temporaryPath}}); err != nil {
		return err
	}
	if err := verifySHA256(temporaryPath, expectedSHA); err != nil {
		return fmt.Errorf("%s checksum verification failed: %w", name, err)
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, dest); err != nil {
		return fmt.Errorf("install %s: %w", name, err)
	}
	return nil
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

// versionOutputMatches accepts an exact version token with an optional leading v.
func versionOutputMatches(output, version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		return false
	}
	pattern := `(^|[^0-9A-Za-z.])v?` + regexp.QuoteMeta(version) + `([^0-9A-Za-z.]|$)`
	return regexp.MustCompile(pattern).MatchString(output)
}

// commandAvailable checks whether a command can be resolved by the current shell.
func commandAvailable(ctx context.Context, runner e2eexec.Runner, name string) bool {
	return runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", "command -v " + name}}).ExitCode == 0
}

// ensureDocker verifies Docker access and, for local-cluster builds, Docker Buildx.
// Platform implementations own package installation; this function only performs common service setup.
func ensureDocker(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, requireBuildx, install bool) error {
	dockerMissing := !commandAvailable(ctx, runner, "docker")
	buildxMissing := requireBuildx && (dockerMissing || runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"buildx", "version"}}).ExitCode != 0)
	if dockerMissing || buildxMissing {
		name := "Docker"
		if !dockerMissing {
			name = "Docker Buildx"
		}
		if !install {
			return fmt.Errorf("%s is required; rerun with --bootstrap=install", name)
		}
		return fmt.Errorf("host package setup left %s unavailable", name)
	}
	if dockerInfoAvailable(ctx, runner) {
		return nil
	}
	if !install {
		return fmt.Errorf("docker socket access is unavailable for the current user; rerun with --bootstrap-component docker=install")
	}
	for _, step := range []struct {
		name    string
		command e2eexec.Command
	}{
		{"systemctl_reset_docker", sudoCommand("systemctl", "reset-failed", "docker", "docker.socket")},
		{"systemctl_start_containerd", sudoCommand("systemctl", "enable", "--now", "containerd")},
		{"systemctl_start_docker_socket", sudoCommand("systemctl", "enable", "--now", "docker.socket")},
		{"systemctl_start_docker", sudoCommand("systemctl", "start", "docker")},
	} {
		if err := runInstallCommand(ctx, stdout, runner, step.name, step.command); err != nil {
			return fmt.Errorf("start Docker service: %w", err)
		}
	}
	if err := ensureDockerSocketAccess(ctx, stdout, runner); err != nil {
		return err
	}
	return nil
}

// dockerInfoAvailable reports whether the current user can reach Docker's daemon.
func dockerInfoAvailable(ctx context.Context, runner e2eexec.Runner) bool {
	return runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"info"}}).ExitCode == 0
}

// ensureDockerSocketAccess restores and verifies the current user's socket ACL.
// It is safe to call after a Docker restart because it only changes access when needed.
func ensureDockerSocketAccess(ctx context.Context, stdout io.Writer, runner e2eexec.Runner) error {
	if dockerInfoAvailable(ctx, runner) {
		return nil
	}
	username := currentUsername()
	if username == "" {
		return fmt.Errorf("docker socket access is unavailable and the current username could not be determined")
	}
	if !commandAvailable(ctx, runner, "setfacl") {
		return fmt.Errorf("docker socket access is unavailable and setfacl is missing; rerun with --bootstrap-component docker=install")
	}
	if err := runInstallCommand(ctx, stdout, runner, "docker_socket_acl", sudoCommand("setfacl", "-m", "u:"+username+":rw", "/var/run/docker.sock")); err != nil {
		return fmt.Errorf("grant Docker socket access to %s: %w", username, err)
	}
	if !dockerInfoAvailable(ctx, runner) {
		return fmt.Errorf("docker is not reachable by the current user after repairing /var/run/docker.sock")
	}
	return nil
}

// checkGPUContainerRuntime verifies Docker GPU access and repairs the configured runtime when install mode allows it.
func checkGPUContainerRuntime(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, opts config.TestOptions, install bool) error {
	if !commandAvailable(ctx, runner, "nvidia-smi") {
		return fmt.Errorf("nvidia-smi is required for local GPU k3d validation")
	}
	ref, err := e2eimage.Parse(opts.ContainerToolkitTestImage)
	if err != nil {
		return fmt.Errorf("--container-toolkit-test-image/E2E_CONTAINER_TOOLKIT_TEST_IMAGE: %w", err)
	}
	smokeImage := ref.Pull()
	inspect := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"image", "inspect", smokeImage}})
	if inspect.ExitCode != 0 {
		pull := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"pull", smokeImage}})
		if pull.ExitCode != 0 {
			return fmt.Errorf("acquire Container Toolkit smoke image %s: docker pull failed: %s%s", smokeImage, pull.Stdout, pull.Stderr)
		}
	}
	runSmoke := func() e2eexec.Result {
		return runner.Run(ctx, e2eexec.Command{
			Name: "docker",
			Args: []string{"run", "--pull=never", "--rm", "--gpus", "all", smokeImage, "nvidia-smi"},
		})
	}
	initialRun := runSmoke()
	if initialRun.ExitCode == 0 {
		return nil
	}
	runtimeErr := fmt.Errorf("docker GPU runtime smoke test failed: %s%s", initialRun.Stdout, initialRun.Stderr)
	if !install {
		return fmt.Errorf("%w; rerun with --bootstrap=install", runtimeErr)
	}
	if !commandAvailable(ctx, runner, "nvidia-ctk") {
		return errors.Join(runtimeErr, fmt.Errorf("NVIDIA Container Toolkit package installation left nvidia-ctk unavailable"))
	}
	if err := runInstallCommand(ctx, stdout, runner, "nvidia_ctk_docker_runtime", sudoCommand("nvidia-ctk", "runtime", "configure", "--runtime=docker")); err != nil {
		return errors.Join(runtimeErr, fmt.Errorf("repair NVIDIA Container Toolkit Docker runtime: %w", err))
	}
	if err := runInstallCommand(ctx, stdout, runner, "systemctl_restart_docker_socket", sudoCommand("systemctl", "restart", "docker.socket")); err != nil {
		return errors.Join(runtimeErr, fmt.Errorf("restart Docker socket after Container Toolkit repair: %w", err))
	}
	if err := runInstallCommand(ctx, stdout, runner, "systemctl_restart_docker", sudoCommand("systemctl", "restart", "docker")); err != nil {
		return errors.Join(runtimeErr, fmt.Errorf("restart Docker after Container Toolkit repair: %w", err))
	}
	if err := ensureDockerSocketAccess(ctx, stdout, runner); err != nil {
		return errors.Join(runtimeErr, fmt.Errorf("restore Docker socket access after Container Toolkit repair: %w", err))
	}
	retry := runSmoke()
	if retry.ExitCode != 0 {
		return errors.Join(runtimeErr, fmt.Errorf("docker GPU runtime smoke test failed after NVIDIA Container Toolkit setup: %s%s", retry.Stdout, retry.Stderr))
	}
	return nil
}

// dcgmPackageNames returns the runtime packages and optional injection development package.
func dcgmPackageNames(requireInjection bool) []string {
	packages := []string{
		"datacenter-gpu-manager-4-core",
		"datacenter-gpu-manager-4-proprietary",
	}
	if requireInjection {
		packages = append(packages, dcgmDevelopmentPackage)
	}
	return packages
}

// HostOS DCGM cache probes cover runtime and injection libraries across distributions.
const (
	hostDCGMLibraryCacheScript          = "ldconfig -p 2>/dev/null | grep -Eq 'libdcgm\\.so(\\.4)?'"
	hostDCGMInjectionLibraryCacheScript = "ldconfig -p 2>/dev/null | grep -Eq 'libnvml_injection\\.so(\\.1)?'"
)

// ensureVSOCK loads the vsock_loopback kernel module and verifies VSOCK is then available.
// It does nothing when VSOCK is already ready.
func ensureVSOCK(ctx context.Context, stdout io.Writer, runner e2eexec.Runner) error {
	ready, err := vsockReady(ctx, runner)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}

	if err := runInstallCommand(ctx, stdout, runner, "modprobe_vsock_loopback", sudoCommand("modprobe", "vsock_loopback")); err != nil {
		return err
	}

	ready, err = vsockReady(ctx, runner)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("VSOCK loopback is unavailable after loading vsock_loopback")
	}
	return nil
}

// vsockReady reports whether the loopback device and kernel module are both available.
// A probe that exits non-zero means "not available". It returns an error only when a
// probe could not run or was stopped (exit code -1), so a broken probe is not mistaken
// for missing VSOCK.
func vsockReady(ctx context.Context, runner e2eexec.Runner) (bool, error) {
	device := runner.Run(ctx, e2eexec.Command{Name: "test", Args: []string{"-e", "/dev/vsock"}})
	if device.ExitCode < 0 {
		return false, fmt.Errorf("probe /dev/vsock: %w", device.Err)
	}
	if device.ExitCode != 0 {
		return false, nil
	}

	module := runner.Run(ctx, e2eexec.Command{
		Name: "sh",
		Args: []string{"-c", "test -d /sys/module/vsock_loopback || grep -q '^vsock_loopback[[:space:]]' /proc/modules"},
	})
	if module.ExitCode < 0 {
		return false, fmt.Errorf("probe vsock_loopback module: %w", module.Err)
	}
	return module.ExitCode == 0, nil
}

// HostDCGMLibraryAvailable reports whether libdcgm.so is visible to direct host tests.
func HostDCGMLibraryAvailable(ctx context.Context, runner e2eexec.Runner) bool {
	const script = hostDCGMLibraryCacheScript + " || " +
		"test -e /usr/lib/x86_64-linux-gnu/libdcgm.so.4 || " +
		"test -e /usr/lib/aarch64-linux-gnu/libdcgm.so.4 || " +
		"test -e /usr/lib64/libdcgm.so.4"
	return runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", script}}).ExitCode == 0
}

// HostDCGMInjectionLibraryAvailable reports whether the official NVML injection library is installed.
func HostDCGMInjectionLibraryAvailable(ctx context.Context, runner e2eexec.Runner) bool {
	const script = hostDCGMInjectionLibraryCacheScript + " || " +
		"test -e /usr/lib/x86_64-linux-gnu/libnvml_injection.so || " +
		"test -e /usr/lib/aarch64-linux-gnu/libnvml_injection.so || " +
		"test -e /usr/lib64/libnvml_injection.so"
	return runner.Run(ctx, e2eexec.Command{Name: "sh", Args: []string{"-c", script}}).ExitCode == 0
}

// dcgmPackageQueryCommand returns the native package query used to verify host DCGM packages.
func dcgmPackageQueryCommand(requireInjection bool) e2eexec.Command {
	packages := dcgmPackageNames(requireInjection)
	return e2eexec.Command{
		Name: "dpkg-query",
		Args: append([]string{
			"-W",
			"-f=${db:Status-Abbrev} ${Package}=${Version}\n",
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
func ConfigureToolPath(root string, opts config.TestOptions) (func(), error) {
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
func toolsBinDir(root string, opts config.TestOptions) string {
	return filepath.Join(TestsToolsDir(root, opts), "bin")
}

// TestsToolsDir returns the root directory used for helper tools and source-tree suite binaries.
func TestsToolsDir(root string, opts config.TestOptions) string {
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
