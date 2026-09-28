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
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
)

const (
	defaultK3dAgentCount        = "1"
	defaultK3dGPUs              = "all"
	defaultK3dWaitTimeout       = "5m"
	defaultRuntimeClass         = "nvidia"
	defaultExporterNodeLabelKey = "dcgm-exporter.nvidia.com/gpu-node"
	defaultExporterNodeLabelVal = "enabled"
	defaultK3dCapabilitiesPath  = "/proc/driver/nvidia/capabilities"
	defaultK3dCapabilitiesMount = "/run/nvidia/host-driver"
	defaultK3dIPFamily          = "dualstack"
	defaultK3dIPv4ClusterCIDR   = "10.42.0.0/16"
	defaultK3dIPv4ServiceCIDR   = "10.43.0.0/16"
	defaultK3dIPv6ClusterCIDR   = "fd42::/56"
	defaultK3dIPv6ServiceCIDR   = "fd43::/112"
	defaultK3dDockerIPv4Subnet  = "172.31.0.0/16"
	defaultK3dDockerIPv6Subnet  = "fd00:4443:474d::/64"
	gpuOperatorIPv6ServiceName  = "dcgm-exporter-e2e-ipv6"
)

// LocalConfig configures the owned local k3d cluster path.
type LocalConfig struct {
	Root                    string
	ClusterName             string
	Kubeconfig              string
	Agents                  string
	GPUs                    string
	WaitTimeout             string
	RuntimeClass            string
	NodeSelectorKey         string
	NodeSelectorValue       string
	NodeImage               string
	BuildNodeImage          bool
	IPFamily                string
	NetworkName             string
	IPv4ClusterCIDR         string
	IPv4ServiceCIDR         string
	IPv6ClusterCIDR         string
	IPv6ServiceCIDR         string
	DockerIPv4Subnet        string
	DockerIPv6Subnet        string
	DevicePluginVersion     string
	K3SImage                string
	CUDAImage               string
	NvidiaCapabilitiesPath  string
	NvidiaCapabilitiesMount string
}

// FeatureConfig carries optional scenario feature setup settings.
type FeatureConfig struct {
	Root               string
	DRAConfigure       string
	GPUOperator        string
	SharedGPUConfigure string
	SharedGPUReplicas  string
	WaitTimeout        string
	ExporterImage      string
	BusyboxImage       string
	Dependencies       config.Dependencies
	ImagePullSecret    string
}

// DefaultLocalConfig returns local k3d defaults from environment-resolved inputs.
func DefaultLocalConfig(root string, opts config.Tests) (LocalConfig, error) {
	for name, value := range map[string]string{
		"K3S image":             opts.K3SImage,
		"K3D node base image":   opts.K3DNodeBaseImage,
		"K3D node output image": opts.K3DNodeOutputImage,
	} {
		if _, err := e2eimage.Parse(value); err != nil {
			return LocalConfig{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	if strings.TrimSpace(opts.Dependencies.DevicePluginVersion) == "" {
		return LocalConfig{}, fmt.Errorf("NVIDIA_DEVICE_PLUGIN_VERSION is required")
	}
	return LocalConfig{
		Root:                    root,
		ClusterName:             DefaultConfig().ClusterName,
		Kubeconfig:              filepath.Join(root, ".local-e2e", "kubeconfig-"+DefaultConfig().ClusterName+".yaml"),
		Agents:                  defaultK3dAgentCount,
		GPUs:                    defaultK3dGPUs,
		WaitTimeout:             defaultK3dWaitTimeout,
		RuntimeClass:            defaultRuntimeClass,
		NodeSelectorKey:         defaultExporterNodeLabelKey,
		NodeSelectorValue:       defaultExporterNodeLabelVal,
		NodeImage:               opts.K3DNodeOutputImage,
		BuildNodeImage:          true,
		IPFamily:                defaultK3dIPFamily,
		NetworkName:             "k3d-" + DefaultConfig().ClusterName,
		IPv4ClusterCIDR:         defaultK3dIPv4ClusterCIDR,
		IPv4ServiceCIDR:         defaultK3dIPv4ServiceCIDR,
		IPv6ClusterCIDR:         defaultK3dIPv6ClusterCIDR,
		IPv6ServiceCIDR:         defaultK3dIPv6ServiceCIDR,
		DockerIPv4Subnet:        defaultK3dDockerIPv4Subnet,
		DockerIPv6Subnet:        defaultK3dDockerIPv6Subnet,
		DevicePluginVersion:     opts.Dependencies.DevicePluginVersion,
		K3SImage:                opts.K3SImage,
		CUDAImage:               opts.K3DNodeBaseImage,
		NvidiaCapabilitiesPath:  defaultK3dCapabilitiesPath,
		NvidiaCapabilitiesMount: defaultK3dCapabilitiesMount,
	}, nil
}

// DefaultRuntimeClass returns the local k3d runtime class used for validation pods.
func DefaultRuntimeClass() string {
	return defaultRuntimeClass
}

// DefaultNodeSelectorKey returns the local k3d node selector key used for validation pods.
func DefaultNodeSelectorKey() string {
	return defaultExporterNodeLabelKey
}

// DefaultNodeSelectorValue returns the local k3d node selector value used for validation pods.
func DefaultNodeSelectorValue() string {
	return defaultExporterNodeLabelVal
}

// EnsureLocal creates or reuses a local GPU k3d cluster and returns its kubeconfig.
func EnsureLocal(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg LocalConfig) (string, error) {
	for _, name := range []string{"docker", "k3d", "kubectl", "helm"} {
		if err := runRequired(ctx, runner, w, name+"_available", e2eexec.Command{Name: "sh", Args: []string{"-c", "command -v " + name}}); err != nil {
			return "", err
		}
	}
	if err := runRequired(ctx, runner, w, "docker_reachable", e2eexec.Command{Name: "docker", Args: []string{"ps"}}); err != nil {
		return "", fmt.Errorf("docker is not reachable by this user; rerun e2e as root/become, or grant this user Docker socket access before local k3d setup: %w", err)
	}
	if err := runRequired(ctx, runner, w, "nvidia_smi", e2eexec.Command{Name: "nvidia-smi", Args: []string{"-L"}}); err != nil {
		return "", err
	}
	if cfg.BuildNodeImage {
		if err := ensureNodeImage(ctx, runner, w, cfg); err != nil {
			return "", err
		}
	}
	if strings.EqualFold(strings.TrimSpace(cfg.IPFamily), "ipv6") {
		fmt.Fprintln(w, "[e2e] WARN --k3d-ip-family ipv6 uses dualstack in local k3d")
	}
	exists, err := localClusterExists(ctx, runner, cfg.ClusterName)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := cleanupStaleLocalK3DResources(ctx, runner, w, cfg); err != nil {
			return "", err
		}
	}
	if normalizedIPFamily(cfg) == "dualstack" {
		if err := ensureDualStackNetwork(ctx, runner, w, cfg); err != nil {
			return "", err
		}
	}

	if err := os.MkdirAll(filepath.Dir(cfg.Kubeconfig), 0o755); err != nil {
		return "", err
	}
	if exists {
		if err := writeLocalKubeconfig(ctx, runner, w, cfg); err != nil {
			return "", err
		}
		match, err := localClusterMatchesDesiredState(ctx, runner, w, cfg)
		if err != nil {
			return "", err
		}
		if !match {
			if err := runRequired(ctx, runner, w, "k3d_cluster_recreate_delete", e2eexec.Command{Name: "k3d", Args: []string{"cluster", "delete", cfg.ClusterName}}); err != nil {
				return "", err
			}
			exists = false
		}
	}
	if !exists {
		if err := createLocalCluster(ctx, runner, w, cfg); err != nil {
			_ = cleanupStaleLocalK3DResources(context.WithoutCancel(ctx), runner, w, cfg)
			return "", err
		}
	}
	if err := writeLocalKubeconfig(ctx, runner, w, cfg); err != nil {
		return "", err
	}

	clusterCfg := DefaultConfig()
	clusterCfg.ClusterName = cfg.ClusterName
	clusterCfg.Kubeconfig = cfg.Kubeconfig
	if err := runRequired(ctx, runner, w, "kubectl_nodes_ready", kubectl(clusterCfg, "wait", "--for=condition=Ready", "node", "--all", "--timeout="+cfg.WaitTimeout)); err != nil {
		return "", err
	}
	if err := ensureRuntimeClass(ctx, runner, w, clusterCfg, cfg.RuntimeClass); err != nil {
		return "", err
	}
	if err := labelFirstNode(ctx, runner, w, clusterCfg, cfg); err != nil {
		return "", err
	}
	if err := ensureDevicePlugin(ctx, runner, w, clusterCfg, cfg); err != nil {
		return "", err
	}
	if err := waitForGPUAllocatable(ctx, runner, w, clusterCfg, cfg.WaitTimeout); err != nil {
		return "", err
	}
	return cfg.Kubeconfig, nil
}
