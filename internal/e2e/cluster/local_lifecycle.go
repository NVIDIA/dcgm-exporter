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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// writeLocalKubeconfig writes the owned k3d cluster kubeconfig to the configured path.
func writeLocalKubeconfig(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	return runRequired(ctx, runner, w, "k3d_kubeconfig_write", e2eexec.Command{Name: "k3d", Args: []string{"kubeconfig", "write", cfg.ClusterName, "--output", cfg.Kubeconfig}})
}

// ensureNodeImage builds the local k3d node image when it is not already present.
func ensureNodeImage(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	inspect := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"image", "inspect", cfg.NodeImage}})
	if inspect.ExitCode == 0 {
		fmt.Fprintf(w, "[e2e] local k3d node image ready: %s\n", cfg.NodeImage)
		return nil
	}
	return runRequired(ctx, runner, w, "docker_build_k3d_node_image", e2eexec.Command{
		Name: "docker",
		Args: []string{
			"build",
			"--file", filepath.Join(cfg.Root, "hack", "e2e-local-node.Dockerfile"),
			"--build-arg", "K3S_IMAGE=" + cfg.K3SImage,
			"--build-arg", "CUDA_IMAGE=" + cfg.CUDAImage,
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
	wantAgents, err := strconv.Atoi(strings.TrimSpace(cfg.Agents))
	if err != nil {
		return false, fmt.Errorf("invalid k3d agent count %q: %w", cfg.Agents, err)
	}
	if gotAgents := localClusterAgentCount(nodes); gotAgents != wantAgents {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s has %d agents, want %d; recreating\n", cfg.ClusterName, gotAgents, wantAgents)
		return false, nil
	}
	if !localClusterGPUDeviceVisible(ctx, runner, nodes) {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s does not expose /dev/nvidiactl; recreating\n", cfg.ClusterName)
		return false, nil
	}
	families := runner.Run(ctx, kubectl(Config{Kubeconfig: cfg.Kubeconfig}, "get", "svc", "kubernetes", "-n", "default", "-o", "jsonpath={.spec.ipFamilies[*]}"))
	if families.ExitCode != 0 {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s service IP family check failed; recreating\n", cfg.ClusterName)
		return false, nil
	}
	hasIPv6 := strings.Contains(string(families.Stdout), "IPv6")
	wantDualStack := normalizedIPFamily(cfg) == "dualstack"
	if hasIPv6 != wantDualStack {
		fmt.Fprintf(w, "[e2e] existing k3d cluster %s IP family is %q, want %s; recreating\n", cfg.ClusterName, strings.TrimSpace(string(families.Stdout)), normalizedIPFamily(cfg))
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
		if strings.Contains(node, "-agent-") {
			count++
		}
	}
	return count
}

// localClusterGPUDeviceVisible verifies at least one k3d node sees the NVIDIA control device.
func localClusterGPUDeviceVisible(ctx context.Context, runner e2eexec.Runner, nodes []string) bool {
	for _, node := range nodes {
		if runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"exec", node, "test", "-e", "/dev/nvidiactl"}}).ExitCode == 0 {
			return true
		}
	}
	return false
}

// createLocalCluster creates the GPU-enabled k3d cluster with the requested IP-family settings.
func createLocalCluster(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
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
	appendIPFamilyArgs(&args, cfg)
	args = append(
		args,
		"--env", "NVIDIA_VISIBLE_DEVICES=all@server:*",
		"--env", "NVIDIA_VISIBLE_DEVICES=all@agent:*",
		"--env", "NVIDIA_DRIVER_CAPABILITIES=all@server:*",
		"--env", "NVIDIA_DRIVER_CAPABILITIES=all@agent:*",
	)
	if info, err := os.Stat(cfg.NvidiaCapabilitiesPath); err == nil && info.IsDir() {
		args = append(
			args,
			"--volume", "/:"+cfg.NvidiaCapabilitiesMount+":ro@server:*",
			"--volume", "/:"+cfg.NvidiaCapabilitiesMount+":ro@agent:*",
		)
	}
	return runRequired(ctx, runner, w, "k3d_cluster_create", e2eexec.Command{Name: "k3d", Args: args})
}

// cleanupStaleLocalK3DResources removes orphaned Docker resources that block k3d recreation.
func cleanupStaleLocalK3DResources(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	if cfg.NetworkName != "" && runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"network", "inspect", cfg.NetworkName}}).ExitCode == 0 {
		if err := runRequired(ctx, runner, w, "docker_remove_stale_k3d_network", e2eexec.Command{Name: "docker", Args: []string{"network", "rm", cfg.NetworkName}}); err != nil {
			return err
		}
	}
	volumeName := "k3d-" + cfg.ClusterName + "-images"
	if runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"volume", "inspect", volumeName}}).ExitCode == 0 {
		if err := runRequired(ctx, runner, w, "docker_remove_stale_k3d_volume", e2eexec.Command{Name: "docker", Args: []string{"volume", "rm", volumeName}}); err != nil {
			return err
		}
	}
	return nil
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

// normalizedIPFamily maps ipv6 to the dual-stack k3d setup this harness creates.
func normalizedIPFamily(cfg LocalConfig) string {
	switch strings.ToLower(strings.TrimSpace(cfg.IPFamily)) {
	case "", "ipv4":
		return "ipv4"
	case "ipv6", "dualstack":
		return "dualstack"
	default:
		return strings.ToLower(strings.TrimSpace(cfg.IPFamily))
	}
}

// ensureDualStackNetwork creates the Docker network used by dual-stack k3d clusters.
func ensureDualStackNetwork(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) error {
	if inspect := runner.Run(ctx, e2eexec.Command{Name: "docker", Args: []string{"network", "inspect", cfg.NetworkName}}); inspect.ExitCode == 0 {
		return nil
	}
	return runRequired(ctx, runner, w, "docker_create_dualstack_network", e2eexec.Command{
		Name: "docker",
		Args: []string{
			"network", "create",
			"--driver", "bridge",
			"--ipv6",
			"--subnet", cfg.DockerIPv4Subnet,
			"--subnet", cfg.DockerIPv6Subnet,
			cfg.NetworkName,
		},
	})
}
