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
	defaultK3dAgentCount               = "0"
	defaultK3dGPUs                     = "all"
	defaultK3dWaitTimeout              = "10m"
	defaultRuntimeClass                = "nvidia"
	defaultExporterNodeLabelKey        = "dcgm-exporter.nvidia.com/gpu-node"
	defaultExporterNodeLabelVal        = "enabled"
	defaultNvidiaCapabilitiesProbePath = "/proc/driver/nvidia/capabilities"
	defaultK3dHostRootMount            = "/run/nvidia/host-driver"
	defaultK3dIPFamily                 = "auto"
	defaultK3dCgroupV1                 = config.FeatureAuto
	defaultK3dIPv4ClusterCIDR          = "10.42.0.0/16"
	defaultK3dIPv4ServiceCIDR          = "10.43.0.0/16"
	defaultK3dIPv6ClusterCIDR          = "fd42::/56"
	defaultK3dIPv6ServiceCIDR          = "fd43::/112"
	defaultK3dDockerIPv4Subnet         = "172.31.0.0/16"
	defaultK3dDockerIPv6Subnet         = "fd00:4443:474d::/64"
	defaultK3dDockerIPv4Gateway        = "172.31.0.1"
	defaultK3dDockerIPv6Gateway        = "fd00:4443:474d::1"
	gpuOperatorIPv6ServiceName         = "dcgm-exporter-e2e-ipv6"
)

// LocalConfig configures the owned local k3d cluster path.
type LocalConfig struct {
	RunName                 string
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
	CgroupV1                config.FeatureMode
	NetworkName             string
	IPv4ClusterCIDR         string
	IPv4ServiceCIDR         string
	IPv6ClusterCIDR         string
	IPv6ServiceCIDR         string
	DockerIPv4Subnet        string
	DockerIPv6Subnet        string
	DockerIPv4Gateway       string
	DockerIPv6Gateway       string
	DevicePluginVersion     string
	ContainerToolkitVersion string
	// RuntimePreflightImage is the host-local image used to verify the nested NVIDIA runtime.
	RuntimePreflightImage       string
	K3SImage                    string
	CUDAImage                   string
	NvidiaCapabilitiesProbePath string
	HostRootPath                string
	HostRootMount               string
}

// FeatureConfig carries optional scenario feature setup settings.
type FeatureConfig struct {
	Root                         string
	DRAConfigure                 config.FeatureMode
	GPUOperator                  config.GPUOperatorMode
	GPUOperatorChart             string
	GPUOperatorImage             string
	GPUOperatorDCGMImage         string
	SharedGPUConfigure           config.FeatureMode
	SharedGPUReplicas            string
	WaitTimeout                  string
	ExporterImage                string
	BusyboxImage                 string
	Dependencies                 config.Dependencies
	ImagePullSecret              string
	RegistryDockerConfigJSONPath string
	RetainResources              bool
}

// DefaultLocalConfig returns local k3d defaults from environment-resolved inputs.
func DefaultLocalConfig(root string, opts config.TestOptions) (LocalConfig, error) {
	runtimePreflightImage := strings.TrimSpace(opts.ContainerToolkitTestImage)
	if runtimePreflightImage == "" {
		runtimePreflightImage = opts.K3DNodeBaseImage
	}
	for name, value := range map[string]string{
		"K3S image":               opts.K3SImage,
		"K3D node base image":     opts.K3DNodeBaseImage,
		"K3D node output image":   opts.K3DNodeOutputImage,
		"Runtime preflight image": runtimePreflightImage,
	} {
		if _, err := e2eimage.Parse(value); err != nil {
			return LocalConfig{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	if strings.TrimSpace(opts.Dependencies.DevicePluginVersion) == "" {
		return LocalConfig{}, fmt.Errorf("NVIDIA_DEVICE_PLUGIN_VERSION is required")
	}
	if strings.TrimSpace(opts.Dependencies.ContainerToolkitVersion) == "" {
		return LocalConfig{}, fmt.Errorf("NVIDIA_CONTAINER_TOOLKIT_VERSION is required")
	}
	clusterDefaults := DefaultConfig()
	if opts.RunName != "" {
		var err error
		clusterDefaults, err = ConfigForRunName(opts.RunName)
		if err != nil {
			return LocalConfig{}, err
		}
	}
	if opts.ClusterName != "" {
		clusterDefaults.ClusterName = opts.ClusterName
	}
	return LocalConfig{
		RunName:                     clusterDefaults.runName(),
		Root:                        root,
		ClusterName:                 clusterDefaults.ClusterName,
		Kubeconfig:                  filepath.Join(root, ".local-e2e", "kubeconfig-"+clusterDefaults.ClusterName+".yaml"),
		Agents:                      defaultK3dAgentCount,
		GPUs:                        defaultK3dGPUs,
		WaitTimeout:                 defaultK3dWaitTimeout,
		RuntimeClass:                defaultRuntimeClass,
		NodeSelectorKey:             defaultExporterNodeLabelKey,
		NodeSelectorValue:           defaultExporterNodeLabelVal,
		NodeImage:                   opts.K3DNodeOutputImage,
		BuildNodeImage:              true,
		IPFamily:                    defaultK3dIPFamily,
		CgroupV1:                    defaultK3dCgroupV1,
		NetworkName:                 "k3d-" + clusterDefaults.ClusterName,
		IPv4ClusterCIDR:             defaultK3dIPv4ClusterCIDR,
		IPv4ServiceCIDR:             defaultK3dIPv4ServiceCIDR,
		IPv6ClusterCIDR:             defaultK3dIPv6ClusterCIDR,
		IPv6ServiceCIDR:             defaultK3dIPv6ServiceCIDR,
		DockerIPv4Subnet:            defaultK3dDockerIPv4Subnet,
		DockerIPv6Subnet:            defaultK3dDockerIPv6Subnet,
		DockerIPv4Gateway:           defaultK3dDockerIPv4Gateway,
		DockerIPv6Gateway:           defaultK3dDockerIPv6Gateway,
		DevicePluginVersion:         opts.Dependencies.DevicePluginVersion,
		ContainerToolkitVersion:     opts.Dependencies.ContainerToolkitVersion,
		RuntimePreflightImage:       runtimePreflightImage,
		K3SImage:                    opts.K3SImage,
		CUDAImage:                   opts.K3DNodeBaseImage,
		NvidiaCapabilitiesProbePath: defaultNvidiaCapabilitiesProbePath,
		HostRootPath:                "/",
		HostRootMount:               defaultK3dHostRootMount,
	}, nil
}

// DefaultRuntimeClass returns the local k3d runtime class used for validation pods.
func DefaultRuntimeClass() string {
	return defaultRuntimeClass
}

// DefaultWaitTimeout returns the timeout used for Kubernetes readiness waits.
func DefaultWaitTimeout() string {
	return defaultK3dWaitTimeout
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
	if err := validateLocalGPUAgentCount(cfg.Agents); err != nil {
		return "", err
	}
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
	if strings.EqualFold(strings.TrimSpace(cfg.IPFamily), "ipv6") {
		fmt.Fprintln(w, "[e2e] WARN --k3d-ip-family ipv6 uses dualstack in local k3d")
	}
	allowCgroupV1, err := localCgroupV1Enabled(ctx, runner, w, cfg)
	if err != nil {
		return "", err
	}
	exists, err := localClusterExists(ctx, runner, cfg.ClusterName)
	if err != nil {
		return "", err
	}
	if cfg.BuildNodeImage {
		if err := ensureNodeImage(ctx, runner, w, cfg); err != nil {
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
		if err := cleanupStaleLocalK3DResources(ctx, runner, w, cfg); err != nil {
			return "", err
		}
		resolved, err := resolveLocalIPFamily(ctx, runner, w, cfg)
		if err != nil {
			return "", err
		}
		cfg = resolved
		if err := createLocalCluster(ctx, runner, w, cfg, allowCgroupV1); err != nil {
			_ = cleanupStaleLocalK3DResources(context.WithoutCancel(ctx), runner, w, cfg)
			return "", err
		}
	}
	if err := writeLocalKubeconfig(ctx, runner, w, cfg); err != nil {
		return "", err
	}

	clusterCfg := DefaultConfig()
	clusterCfg.Root = cfg.Root
	clusterCfg.RunName = cfg.RunName
	clusterCfg.ClusterName = cfg.ClusterName
	clusterCfg.LocalKubeconfig = cfg.Kubeconfig
	if err := runRequired(ctx, runner, w, "kubectl_nodes_ready", kubectl(clusterCfg, "wait", "--for=condition=Ready", "node", "--all", "--timeout="+cfg.WaitTimeout)); err != nil {
		return "", err
	}
	if err := ensureRuntimeClass(ctx, runner, w, clusterCfg, cfg.RuntimeClass); err != nil {
		return "", err
	}
	if err := labelFirstNode(ctx, runner, w, clusterCfg, cfg); err != nil {
		return "", err
	}
	if err := preflightLocalGPURuntime(ctx, runner, w, clusterCfg, cfg); err != nil {
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
