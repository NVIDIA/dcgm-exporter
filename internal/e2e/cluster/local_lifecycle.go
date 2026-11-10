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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const containerToolkitVersionLabel = "com.nvidia.dcgm-exporter.e2e.nvidia-container-toolkit.version"

// writeLocalKubeconfig writes the owned k3d cluster kubeconfig to the configured path.
func writeLocalKubeconfig(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	return runRequired(ctx, runner, w, "k3d_kubeconfig_write", e2eexec.Command{Name: "k3d", Args: []string{"kubeconfig", "write", cfg.ClusterName, "--output", cfg.Kubeconfig}})
}

// ensureNodeImage builds the local k3d node image when it is not already present.
func ensureNodeImage(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	inspect := runner.Run(ctx, e2eexec.Command{
		Name: "docker",
		Args: []string{
			"image", "inspect", cfg.NodeImage,
			"--format={{index .Config.Labels \"" + containerToolkitVersionLabel + "\"}}",
		},
	})
	if inspect.ExitCode == 0 && strings.TrimSpace(string(inspect.Stdout)) == cfg.ContainerToolkitVersion {
		fmt.Fprintf(w, "[e2e] local k3d node image ready: %s\n", cfg.NodeImage)
		return nil
	}
	if inspect.ExitCode == 0 {
		fmt.Fprintf(
			w,
			"[e2e] local k3d node image %s has Container Toolkit %q, want %q; rebuilding\n",
			cfg.NodeImage,
			strings.TrimSpace(string(inspect.Stdout)),
			cfg.ContainerToolkitVersion,
		)
	}
	if err := runRequired(ctx, runner, w, "docker_buildx_available", e2eexec.Command{Name: "docker", Args: []string{"buildx", "version"}}); err != nil {
		return fmt.Errorf("local k3d node image build requires Docker Buildx; rerun tests with --bootstrap=install or install docker-buildx: %w", err)
	}
	return runRequired(ctx, runner, w, "docker_build_k3d_node_image", e2eexec.Command{
		Name: "docker",
		Args: []string{
			"buildx", "build",
			"--load",
			"--file", filepath.Join(cfg.Root, "hack", "e2e-local-node.Dockerfile"),
			"--build-arg", "K3S_IMAGE=" + cfg.K3SImage,
			"--build-arg", "CUDA_IMAGE=" + cfg.CUDAImage,
			"--build-arg", "NVIDIA_CONTAINER_TOOLKIT_VERSION=" + cfg.ContainerToolkitVersion,
			"--tag", cfg.NodeImage,
			cfg.Root,
		},
	})
}

// localClusterExists reports whether k3d lists the named cluster.
func localClusterExists(ctx context.Context, runner e2eexec.Runner, name string) (bool, error) {
	result := runner.Run(ctx, e2eexec.Command{Name: "k3d", Args: []string{"cluster", "list"}})
	if result.ExitCode != 0 {
		return false, resultError("k3d cluster list", result)
	}
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 0 && fields[0] == name {
			return true, nil
		}
	}
	return false, nil
}

// localClusterMatchesDesiredState decides whether an existing cluster can be reused safely.
func localClusterMatchesDesiredState(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) (bool, error) {
	image := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"inspect", "k3d-" + cfg.ClusterName + "-server-0", "--format={{.Config.Image}}"}})
	if image.ExitCode != 0 {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s has no inspectable server node; recreating\n", cfg.ClusterName)
		return false, nil
	}
	if got := strings.TrimSpace(string(image.Stdout)); got != cfg.NodeImage {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s node image is %s, want %s; recreating\n", cfg.ClusterName, got, cfg.NodeImage)
		return false, nil
	}
	nodes, err := localClusterDockerNodes(ctx, runner, cfg.ClusterName)
	if err != nil {
		return false, err
	}
	if gotServers := localClusterServerCount(nodes); gotServers != 1 {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s has %d servers, want 1; recreating\n", cfg.ClusterName, gotServers)
		return false, nil
	}
	if gotAgents := localClusterAgentCount(nodes); gotAgents != 0 {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s has %d agents, want 0; recreating\n", cfg.ClusterName, gotAgents)
		return false, nil
	}
	if !localClusterServerGPUConfigMatches(ctx, runner, cfg, nodes) {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s is missing the server GPU configuration; recreating\n", cfg.ClusterName)
		return false, nil
	}
	wantIPFamily := normalizedIPFamily(cfg)
	if wantIPFamily == "auto" {
		return true, nil
	}
	ipv6 := probeDualStackServices(ctx, runner, Config{Kubeconfig: cfg.Kubeconfig})
	if ipv6.Status == capability.StatusUnknown {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s service IP family check failed; recreating\n", cfg.ClusterName)
		return false, nil
	}
	hasIPv6 := ipv6.Status == capability.StatusSupported
	if hasIPv6 != (wantIPFamily == "dualstack") {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s service IP families are %q, want %s; recreating\n", cfg.ClusterName, ipv6.Evidence, normalizedIPFamily(cfg))
		return false, nil
	}
	return true, nil
}

// localClusterDockerNodes returns Docker container names for the k3d cluster nodes.
func localClusterDockerNodes(ctx context.Context, runner e2eexec.Runner, clusterName string) ([]string, error) {
	result := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"ps", "-a", "--format", "{{.Names}}"}})
	if result.ExitCode != 0 {
		return nil, resultError("docker ps k3d nodes", result)
	}
	matches := []string{}
	pattern := regexp.MustCompile(`^k3d-` + regexp.QuoteMeta(clusterName) + `-(server|agent)-[0-9]+$`)
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		name := strings.TrimSpace(line)
		if pattern.MatchString(name) {
			matches = append(matches, name)
		}
	}
	return matches, nil
}

// localClusterAgentCount counts agent nodes among k3d Docker containers.
func localClusterAgentCount(nodes []string) int {
	count := 0
	for _, node := range nodes {
		if localClusterNodeRole(node) == "agent" {
			count++
		}
	}
	return count
}

// localClusterServerCount counts server nodes among k3d Docker containers.
func localClusterServerCount(nodes []string) int {
	count := 0
	for _, node := range nodes {
		if localClusterNodeRole(node) == "server" {
			count++
		}
	}
	return count
}

// localClusterNodeRole returns the role suffix k3d placed after the cluster name.
func localClusterNodeRole(node string) string {
	if strings.LastIndex(node, "-server-") > strings.LastIndex(node, "-agent-") {
		return "server"
	}
	if strings.LastIndex(node, "-agent-") >= 0 {
		return "agent"
	}
	return ""
}

// localClusterServerGPUConfigMatches verifies the server has the GPU configuration createLocalCluster applies.
func localClusterServerGPUConfigMatches(ctx context.Context, runner e2eexec.Runner, cfg LocalConfig, nodes []string) bool {
	for _, node := range nodes {
		if localClusterNodeRole(node) != "server" {
			continue
		}
		result := runner.Run(ctx, e2eexec.Command{
			Name: "docker",
			Args: []string{
				"exec",
				node,
				"sh",
				"-ec",
				`test -e /dev/nvidiactl && test "$NVIDIA_VISIBLE_DEVICES" = all && test "$NVIDIA_DRIVER_CAPABILITIES" = all`,
			},
		})
		if result.ExitCode != 0 {
			return false
		}
		if info, err := os.Stat(cfg.NvidiaCapabilitiesProbePath); err == nil && info.IsDir() {
			binds := runner.Run(ctx, e2eexec.Command{
				Name: "docker",
				Args: []string{
					"inspect",
					node,
					"--format={{range .HostConfig.Binds}}{{println .}}{{end}}",
				},
			})
			if binds.ExitCode != 0 || !strings.Contains(string(binds.Stdout), cfg.HostRootPath+":"+cfg.HostRootMount+":ro") {
				return false
			}
		}
		return true
	}
	return false
}

// validateLocalGPUAgentCount prevents k3d from assigning the host GPU request to agent nodes.
func validateLocalGPUAgentCount(value string) error {
	count, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("invalid k3d agent count %q: %w", value, err)
	}
	if count != 0 {
		return fmt.Errorf("local GPU k3d requires zero agents: k3d applies GPU requests to every node role")
	}
	return nil
}

// localCgroupV1Enabled checks the host cgroup version and chooses whether new k3d nodes need the cgroup-v1 kubelet setting.
func localCgroupV1Enabled(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) (bool, error) {
	// An unset mode means auto here: it only matches the node setting to the
	// host's cgroup version and changes nothing else.
	mode := cfg.CgroupV1
	if mode == "" {
		mode = config.FeatureAuto
	}
	parsed, err := config.ParseFeatureMode(string(mode))
	if err != nil {
		return false, fmt.Errorf("unsupported local k3d cgroup v1 mode %q", cfg.CgroupV1)
	}
	mode = parsed

	command := e2eexec.Command{Name: "stat", Args: []string{"-fc", "%T", "/sys/fs/cgroup"}}
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return false, resultError(commandString(command), result)
	}

	output := string(result.Stdout)
	filesystem := strings.TrimSpace(output)
	switch filesystem {
	case "cgroup2fs":
		if mode == config.FeatureOn {
			fmt.Fprintln(w, "[e2e] local k3d cgroup v1: enabled (true; requested)")
			return true, nil
		}
		fmt.Fprintf(w, "[e2e] local k3d cgroup v1: disabled (%s; host uses cgroup v2)\n", mode)
		return false, nil
	case "tmpfs":
		switch mode {
		case config.FeatureAuto, config.FeatureOn:
			fmt.Fprintf(w, "[e2e] local k3d cgroup v1: enabled (%s)\n", mode)
			return true, nil
		case config.FeatureOff:
			return false, fmt.Errorf("local k3d cannot run on cgroup v1 when --k3d-cgroup-v1=false; rerun with --k3d-cgroup-v1=auto or --k3d-cgroup-v1=true")
		default:
			return false, fmt.Errorf("unsupported local k3d cgroup v1 mode %q", cfg.CgroupV1)
		}
	default:
		return false, fmt.Errorf("%s returned %q; expected cgroup2fs for cgroup v2 or tmpfs for cgroup v1", commandString(command), output)
	}
}

// createLocalCluster creates the GPU-enabled k3d cluster with the requested IP-family and cgroup-v1 settings.
func createLocalCluster(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig, allowCgroupV1 bool) error {
	args := []string{
		"cluster", "create", cfg.ClusterName,
		"--image", cfg.NodeImage,
		"--kubeconfig-update-default=false",
		"--agents", cfg.Agents,
		"--gpus", cfg.GPUs,
		"--wait",
		"--k3s-arg", "--disable=traefik@server:*",
		"--k3s-arg", "--disable=metrics-server@server:*",
	}
	if allowCgroupV1 {
		args = append(
			args,
			"--k3s-arg", "--kubelet-arg=fail-cgroupv1=false@server:*",
		)
	}
	appendIPFamilyArgs(&args, cfg)
	args = append(
		args,
		"--env", "NVIDIA_VISIBLE_DEVICES=all@server:*",
		"--env", "NVIDIA_DRIVER_CAPABILITIES=all@server:*",
	)
	if info, err := os.Stat(cfg.NvidiaCapabilitiesProbePath); err == nil && info.IsDir() {
		args = append(
			args,
			"--volume", cfg.HostRootPath+":"+cfg.HostRootMount+":ro@server:*",
		)
	}
	timeout := parseDuration(cfg.WaitTimeout, 10*time.Minute)
	createCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runRequired(createCtx, runner, w, "k3d_cluster_create", e2eexec.Command{Name: "k3d", Args: args}); err != nil {
		if errors.Is(createCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return fmt.Errorf("k3d cluster creation timed out after %s: %w", timeout, err)
		}
		return err
	}
	return nil
}

// cleanupStaleLocalK3DResources removes orphaned Docker resources that block k3d recreation.
func cleanupStaleLocalK3DResources(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	var errs []error
	if cfg.NetworkName != "" {
		exists, err := dockerResourceExists(ctx, runner, "network", cfg.NetworkName)
		errs = append(errs, err)
		if err == nil && exists {
			if err := runRequired(ctx, runner, w, "docker_remove_stale_k3d_network", e2eexec.Command{Name: "docker", Args: []string{"network", "rm", cfg.NetworkName}}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	volumeName := "k3d-" + cfg.ClusterName + "-images"
	exists, err := dockerResourceExists(ctx, runner, "volume", volumeName)
	errs = append(errs, err)
	if err == nil && exists {
		if err := runRequired(ctx, runner, w, "docker_remove_stale_k3d_volume", e2eexec.Command{Name: "docker", Args: []string{"volume", "rm", volumeName}}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dockerResourceExists reports whether an owned Docker resource exists without hiding inspect failures.
func dockerResourceExists(ctx context.Context, runner e2eexec.Runner, resource, name string) (bool, error) {
	command := e2eexec.Command{Name: "docker", Args: []string{resource, "inspect", name}}
	result := runner.Run(ctx, command)
	if result.ExitCode == 0 {
		return true, nil
	}
	if dockerResourceNotFound(resource, name, result) {
		return false, nil
	}
	return false, resultError(commandString(command), result)
}

// dockerResourceNotFound recognizes Docker's explicit missing network and volume diagnostics.
func dockerResourceNotFound(resource, name string, result e2eexec.Result) bool {
	output := strings.ToLower(string(result.Stdout) + string(result.Stderr))
	name = strings.ToLower(name)
	switch resource {
	case "network":
		return strings.Contains(output, "no such network") || strings.Contains(output, "network "+name+" not found")
	case "volume":
		return strings.Contains(output, "no such volume") || strings.Contains(output, "volume "+name+" not found")
	default:
		return false
	}
}

// appendIPFamilyArgs adds k3d network arguments for dual-stack clusters.
func appendIPFamilyArgs(args *[]string, cfg LocalConfig) {
	switch normalizedIPFamily(cfg) {
	case "ipv4":
		return
	case "dualstack":
		*args = append(
			*args,
			"--network", cfg.NetworkName,
			"--k3s-arg", "--cluster-cidr="+cfg.IPv4ClusterCIDR+","+cfg.IPv6ClusterCIDR+"@server:*",
			"--k3s-arg", "--service-cidr="+cfg.IPv4ServiceCIDR+","+cfg.IPv6ServiceCIDR+"@server:*",
			"--k3s-arg", "--flannel-ipv6-masq@server:*",
		)
	}
}

// normalizedIPFamily preserves auto selection and maps ipv6 to the dual-stack setup.
func normalizedIPFamily(cfg LocalConfig) string {
	switch strings.ToLower(strings.TrimSpace(cfg.IPFamily)) {
	case "", "auto":
		return "auto"
	case "ipv4":
		return "ipv4"
	case "ipv6", "dualstack":
		return "dualstack"
	default:
		return strings.ToLower(strings.TrimSpace(cfg.IPFamily))
	}
}

var errDockerIPv6Unavailable = errors.New("docker bridge IPv6 is unavailable")

// resolveLocalIPFamily chooses the network mode for a new local cluster.
func resolveLocalIPFamily(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) (LocalConfig, error) {
	switch normalizedIPFamily(cfg) {
	case "ipv4":
		cfg.IPFamily = "ipv4"
		return cfg, nil
	case "dualstack":
		if err := ensureDualStackNetwork(ctx, runner, w, cfg); err != nil {
			return LocalConfig{}, err
		}
		cfg.IPFamily = "dualstack"
		return cfg, nil
	case "auto":
		err := ensureDualStackNetwork(ctx, runner, w, cfg)
		switch {
		case err == nil:
			cfg.IPFamily = "dualstack"
			fmt.Fprintln(w, "[e2e] local k3d network mode: dualstack")
			return cfg, nil
		case errors.Is(err, errDockerIPv6Unavailable):
			cfg.IPFamily = "ipv4"
			fmt.Fprintln(w, "[e2e] WARN Docker bridge IPv6 is unavailable; using IPv4 for the local k3d cluster")
			return cfg, nil
		default:
			return LocalConfig{}, err
		}
	default:
		return LocalConfig{}, fmt.Errorf("unsupported local k3d IP family %q", cfg.IPFamily)
	}
}

// ensureDualStackNetwork creates the Docker network used by dual-stack k3d clusters.
func ensureDualStackNetwork(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	exists, err := dockerResourceExists(ctx, runner, "network", cfg.NetworkName)
	if err != nil {
		return err
	}
	if exists {
		inspect := runner.Run(ctx, e2eexec.Command{
			Name: "docker",
			Args: []string{"network", "inspect", cfg.NetworkName, "--format={{.EnableIPv6}}"},
		})
		if inspect.ExitCode != 0 {
			return resultError("inspect Docker network IPv6 capability", inspect)
		}
		if !strings.EqualFold(strings.TrimSpace(string(inspect.Stdout)), "true") {
			return fmt.Errorf("%w: existing Docker network %s does not have IPv6 enabled", errDockerIPv6Unavailable, cfg.NetworkName)
		}
		return nil
	}
	err = runRequired(ctx, runner, w, "docker_create_dualstack_network", e2eexec.Command{
		Name: "docker",
		Args: []string{
			"network", "create",
			"--driver", "bridge",
			"--ipv6",
			"--subnet", cfg.DockerIPv4Subnet,
			"--gateway", cfg.DockerIPv4Gateway,
			"--subnet", cfg.DockerIPv6Subnet,
			"--gateway", cfg.DockerIPv6Gateway,
			cfg.NetworkName,
		},
	})
	if err != nil && dockerIPv6Unavailable(err.Error()) {
		return fmt.Errorf("%w: %v", errDockerIPv6Unavailable, err)
	}
	return err
}

// dockerIPv6Unavailable recognizes Docker errors caused by missing host IPv6 support.
func dockerIPv6Unavailable(output string) bool {
	output = strings.ToLower(output)
	for _, text := range []string{
		"cannot read ipv6 setup for bridge",
		"ipv6 is not enabled on this system",
		"address family not supported by protocol",
		"protocol not available",
	} {
		if strings.Contains(output, text) {
			return true
		}
	}
	return false
}
