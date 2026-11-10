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
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

func TestDefaultLocalConfigUsesK3DNodeBaseUbuntuTag(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"K3S_VERSION=v1.35.3-k3s1",
		"CUDA_BASE_TAG=13.2.1-base",
		"CUDA_UBUNTU_TAG=26.04",
		"K3D_NODE_BASE_UBUNTU_TAG=24.04",
		"NVIDIA_DEVICE_PLUGIN_VERSION=0.19.0",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "hack", "versions.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := testLocalOptions()
	opts.K3DNodeBaseImage = "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04"
	cfg, err := DefaultLocalConfig(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.CUDAImage, "ubuntu24.04") {
		t.Fatalf("CUDAImage = %q, want k3d node Ubuntu tag", cfg.CUDAImage)
	}
	if strings.Contains(cfg.CUDAImage, "ubuntu26.04") {
		t.Fatalf("CUDAImage = %q, used the ubuntu26.04 tag instead of the k3d node tag", cfg.CUDAImage)
	}
	if cfg.Agents != "0" {
		t.Fatalf("Agents = %q, want zero GPU agents", cfg.Agents)
	}
}

func TestDefaultLocalConfigRequiresContainerToolkitVersion(t *testing.T) {
	opts := testLocalOptions()
	opts.Dependencies.ContainerToolkitVersion = ""

	_, err := DefaultLocalConfig(t.TempDir(), opts)
	if err == nil || !strings.Contains(err.Error(), "NVIDIA_CONTAINER_TOOLKIT_VERSION is required") {
		t.Fatalf("DefaultLocalConfig() error = %v, want missing toolkit version", err)
	}
}

func TestDefaultLocalConfigUsesNodeBaseImageForRuntimePreflight(t *testing.T) {
	opts := testLocalOptions()
	opts.ContainerToolkitTestImage = ""

	cfg, err := DefaultLocalConfig(t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimePreflightImage != opts.K3DNodeBaseImage {
		t.Fatalf("RuntimePreflightImage = %q, want node base image %q", cfg.RuntimePreflightImage, opts.K3DNodeBaseImage)
	}
}

func TestDefaultLocalConfigRejectsInvalidRuntimePreflightImage(t *testing.T) {
	opts := testLocalOptions()
	opts.ContainerToolkitTestImage = "nvcr.io/nvidia/cuda"

	_, err := DefaultLocalConfig(t.TempDir(), opts)
	if err == nil || !strings.Contains(err.Error(), "Runtime preflight image") {
		t.Fatalf("DefaultLocalConfig() error = %v, want runtime preflight image validation", err)
	}
}

func TestDefaultLocalConfigK3SRepositoryDefaultsToDockerHub(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"K3S_VERSION=v1.35.3-k3s1",
		"CUDA_BASE_TAG=13.2.1-base",
		"CUDA_UBUNTU_TAG=24.04",
		"K3D_NODE_BASE_UBUNTU_TAG=24.04",
		"NVIDIA_DEVICE_PLUGIN_VERSION=0.19.0",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "hack", "versions.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := testLocalOptions()
	opts.K3SImage = "rancher/k3s:v1.35.3-k3s1"
	cfg, err := DefaultLocalConfig(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.K3SImage != "rancher/k3s:v1.35.3-k3s1" {
		t.Fatalf("K3SImage = %q, want Docker Hub reference", cfg.K3SImage)
	}
}

func TestEnsureLocalCreatesClusterAndDevicePlugin(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.RunName = "setup-run"

	kubeconfig, err := EnsureLocal(context.Background(), runner, &out, cfg)
	if err != nil {
		t.Fatal(err)
	}

	if kubeconfig != cfg.Kubeconfig {
		t.Fatalf("kubeconfig = %q, want %q", kubeconfig, cfg.Kubeconfig)
	}
	if !runner.has("k3d", "cluster", "create", "dcgm-exporter-gpu") {
		t.Fatalf("missing k3d cluster create: %#v", runner.commands)
	}
	if !runner.has("kubectl", "apply", "-f", "-") {
		t.Fatalf("missing RuntimeClass apply: %#v", runner.commands)
	}
	if !runner.has("helm", "upgrade", "--install", "nvidia-device-plugin") {
		t.Fatalf("missing device plugin install: %#v", runner.commands)
	}
	if !runner.has("k3d", "image", "import", cfg.RuntimePreflightImage, "-c", cfg.ClusterName) {
		t.Fatalf("missing runtime preflight image import: %#v", runner.commands)
	}
	if !runner.has("kubectl", "wait", "--for=jsonpath={.status.phase}=Succeeded", "pod/"+runtimePreflightPodName) {
		t.Fatalf("missing runtime preflight pod wait: %#v", runner.commands)
	}
	if runtimeWait := runner.commandIndex("kubectl", "wait", "--for=jsonpath={.status.phase}=Succeeded", "pod/"+runtimePreflightPodName); runtimeWait < 0 || runtimeWait > runner.commandIndex("helm", "upgrade", "--install", "nvidia-device-plugin") {
		t.Fatalf("runtime preflight did not precede the device plugin: %#v", runner.commands)
	}
	if !runner.hasEnv("HELM_REPOSITORY_CONFIG=" + filepath.Join(cfg.Root, ".local-e2e", "helm-setup-run", "repositories.yaml")) {
		t.Fatal("local setup did not use the named run Helm state")
	}
	if !runner.hasEnv("KUBECONFIG=" + cfg.Kubeconfig) {
		t.Fatalf("missing kubeconfig env: %#v", runner.commands)
	}
}

func TestEnsureLocalReportsNestedRuntimeFailureBeforeDevicePlugin(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"kubectl wait --for=jsonpath={.status.phase}=Succeeded pod/" + runtimePreflightPodName + " --timeout=" + localRuntimePreflightTimeout(cfg.WaitTimeout).String(): {
			ExitCode: 1,
			Stderr:   []byte("nvidia-container-runtime: initialization error\n"),
		},
		"kubectl logs pod/" + runtimePreflightPodName + " --all-containers=true --ignore-errors=true --tail=200": {
			Stderr: []byte("inner runtime log\n"),
		},
		"docker ps -a --format {{.Names}}": {
			Stdout: []byte("k3d-dcgm-exporter-gpu-server-0\n"),
		},
	}}
	var out bytes.Buffer

	_, err := EnsureLocal(context.Background(), runner, &out, cfg)
	if err == nil || !strings.Contains(err.Error(), "nested GPU runtime preflight failed") {
		t.Fatalf("EnsureLocal() error = %v, want nested runtime failure", err)
	}
	if runner.has("helm", "upgrade", "--install", "nvidia-device-plugin") {
		t.Fatalf("nested runtime failure installed the device plugin: %#v", runner.commands)
	}
	if !runner.has("kubectl", "describe", "pod", runtimePreflightPodName) || !runner.has("docker", "logs", "--tail=200", "k3d-dcgm-exporter-gpu-server-0") {
		t.Fatalf("nested runtime failure did not collect diagnostics: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "inner runtime log") {
		t.Fatalf("nested runtime logs missing from output:\n%s", out.String())
	}
}

func TestEnsureLocalWaitsForGPUAllocatable(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if !runner.has("sh", "allocatable.nvidia") {
		t.Fatalf("missing allocatable GPU wait: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "kubectl_wait_gpu_allocatable") {
		t.Fatalf("missing allocatable GPU wait log:\n%s", out.String())
	}
}

func TestEnsureLocalReturnsGPUAllocatableWaitFailure(t *testing.T) {
	runner := (&recordingRunner{}).respond("allocatable.nvidia", e2eexec.Result{ExitCode: 1, Stderr: []byte("cluster did not report allocatable nvidia.com/gpu resources before timeout\n")})
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.WaitTimeout = "1ms"

	_, err := EnsureLocal(context.Background(), runner, &out, cfg)
	if err == nil || !strings.Contains(err.Error(), "cluster did not report allocatable nvidia.com/gpu resources before timeout") {
		t.Fatalf("EnsureLocal() error = %v, want allocatable GPU wait failure", err)
	}
}

func TestEnsureLocalConfiguresGPUOnlyOnServer(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.NvidiaCapabilitiesProbePath = t.TempDir()
	cfg.HostRootPath = "/"
	cfg.HostRootMount = "/run/nvidia/host-driver"

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"--agents 0",
		"NVIDIA_VISIBLE_DEVICES=all@server:*",
		"NVIDIA_DRIVER_CAPABILITIES=all@server:*",
		"/:/run/nvidia/host-driver:ro@server:*",
	} {
		if !runner.has("k3d", want) {
			t.Fatalf("missing server GPU option %q: %#v", want, runner.commands)
		}
	}
	for _, unwanted := range []string{
		"NVIDIA_VISIBLE_DEVICES=all@agent:*",
		"NVIDIA_DRIVER_CAPABILITIES=all@agent:*",
		"/:/run/nvidia/host-driver:ro@agent:*",
	} {
		if runner.has("k3d", unwanted) {
			t.Fatalf("agent GPU option %q was configured: %#v", unwanted, runner.commands)
		}
	}
}

// TestEnsureLocalCgroupV1Modes verifies host cgroup detection controls the kubelet setting before local k3d changes.
func TestEnsureLocalCgroupV1Modes(t *testing.T) {
	tests := []struct {
		name          string
		filesystem    string
		mode          config.FeatureMode
		wantKubelet   bool
		wantCreate    bool
		wantErrorText string
	}{
		{name: "auto cgroup v1", filesystem: "tmpfs", mode: "auto", wantKubelet: true, wantCreate: true},
		{name: "enabled cgroup v1", filesystem: "tmpfs", mode: "true", wantKubelet: true, wantCreate: true},
		{name: "disabled cgroup v1", filesystem: "tmpfs", mode: "false", wantErrorText: "cannot run on cgroup v1"},
		{name: "auto cgroup v2", filesystem: "cgroup2fs", mode: "auto", wantCreate: true},
		{name: "enabled cgroup v2", filesystem: "cgroup2fs", mode: "true", wantKubelet: true, wantCreate: true},
		{name: "disabled cgroup v2", filesystem: "cgroup2fs", mode: "false", wantCreate: true},
		{name: "spaced upper-case true on cgroup v2", filesystem: "cgroup2fs", mode: " TRUE ", wantKubelet: true, wantCreate: true},
		{name: "upper-case auto on cgroup v1", filesystem: "tmpfs", mode: "AUTO", wantKubelet: true, wantCreate: true},
		{name: "upper-case false on cgroup v1", filesystem: "tmpfs", mode: "False", wantErrorText: "cannot run on cgroup v1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultLocalConfigForTest(t)
			cfg.BuildNodeImage = false
			cfg.CgroupV1 = test.mode
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				"stat -fc %T /sys/fs/cgroup": {Stdout: []byte(test.filesystem + "\n")},
			}}

			_, err := EnsureLocal(context.Background(), runner, io.Discard, cfg)
			requireErrorContains(t, "EnsureLocal()", err, test.wantErrorText)

			serverArg := runner.has("k3d", "--kubelet-arg=fail-cgroupv1=false@server:*")
			agentArg := runner.has("k3d", "--kubelet-arg=fail-cgroupv1=false@agent:*")
			if serverArg != test.wantKubelet || agentArg {
				t.Fatalf("kubelet arguments server=%t agent=%t, want server=%t agent=false; commands = %#v", serverArg, agentArg, test.wantKubelet, runner.commands)
			}
			created := runner.has("k3d", "cluster", "create", cfg.ClusterName)
			if created != test.wantCreate {
				t.Fatalf("cluster creation = %t, want %t; commands = %#v", created, test.wantCreate, runner.commands)
			}
			if test.wantErrorText != "" && runner.has("k3d", "cluster", "list") {
				t.Fatalf("cgroup failure inspected an existing cluster: %#v", runner.commands)
			}
		})
	}
}

// TestEnsureLocalReportsCgroupEvidence verifies unreadable and unknown cgroup results keep their command evidence.
func TestEnsureLocalReportsCgroupEvidence(t *testing.T) {
	tests := []struct {
		name   string
		result e2eexec.Result
		want   []string
	}{
		{
			name:   "unreadable",
			result: e2eexec.Result{ExitCode: 1, Stderr: []byte("permission denied\n")},
			want:   []string{"stat -fc %T /sys/fs/cgroup", "permission denied"},
		},
		{
			name:   "unexpected filesystem",
			result: e2eexec.Result{Stdout: []byte("squashfs\n")},
			want:   []string{"squashfs", "cgroup2fs", "tmpfs"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultLocalConfigForTest(t)
			cfg.BuildNodeImage = false
			runner := &recordingRunner{results: map[string]e2eexec.Result{
				"stat -fc %T /sys/fs/cgroup": test.result,
			}}

			_, err := EnsureLocal(context.Background(), runner, io.Discard, cfg)
			if err == nil {
				t.Fatal("EnsureLocal() error = nil")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("EnsureLocal() error = %v, missing %q", err, want)
				}
			}
			if runner.has("k3d", "cluster", "list") || runner.has("docker", "image", "inspect") {
				t.Fatalf("cgroup evidence failure changed local k3d state: %#v", runner.commands)
			}
		})
	}
}

func TestEnsureLocalUsesDualStackK3DArgs(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "ipv6"

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if !runner.has("k3d", "--network", "k3d-dcgm-exporter-gpu") {
		t.Fatalf("missing k3d dual-stack network args: %#v", runner.commands)
	}
	if !runner.has("k3d", "--cluster-cidr=10.42.0.0/16,fd42::/56@server:*") {
		t.Fatalf("missing dual-stack cluster CIDR: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "ipv6 uses dualstack") {
		t.Fatalf("missing ipv6 dualstack warning:\n%s", out.String())
	}
}

func TestEnsureLocalAutoFallsBackToIPv4WhenDockerIPv6IsUnavailable(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "auto"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker network inspect " + cfg.NetworkName: {
			ExitCode: 1,
			Stderr:   []byte("Error: No such network: " + cfg.NetworkName),
		},
		"docker network create --driver bridge --ipv6 --subnet " + cfg.DockerIPv4Subnet + " --gateway " + cfg.DockerIPv4Gateway + " --subnet " + cfg.DockerIPv6Subnet + " --gateway " + cfg.DockerIPv6Gateway + " " + cfg.NetworkName: {
			ExitCode: 1,
			Stderr:   []byte("Cannot read IPv6 setup for bridge br-test: open /proc/sys/net/ipv6/conf/br-test/disable_ipv6: no such file or directory"),
		},
	}}
	var out bytes.Buffer

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if runner.has("k3d", "--network", cfg.NetworkName) {
		t.Fatalf("IPv4 fallback retained dual-stack network arguments: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "using IPv4 for the local k3d cluster") {
		t.Fatalf("missing IPv4 fallback explanation:\n%s", out.String())
	}
}

func TestEnsureLocalExplicitDualStackFailsWhenDockerIPv6IsUnavailable(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "dualstack"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker network inspect " + cfg.NetworkName: {
			ExitCode: 1,
			Stderr:   []byte("Error: No such network: " + cfg.NetworkName),
		},
		"docker network create --driver bridge --ipv6 --subnet " + cfg.DockerIPv4Subnet + " --gateway " + cfg.DockerIPv4Gateway + " --subnet " + cfg.DockerIPv6Subnet + " --gateway " + cfg.DockerIPv6Gateway + " " + cfg.NetworkName: {
			ExitCode: 1,
			Stderr:   []byte("IPv6 is not enabled on this system"),
		},
	}}

	_, err := EnsureLocal(context.Background(), runner, io.Discard, cfg)
	if !errors.Is(err, errDockerIPv6Unavailable) {
		t.Fatalf("EnsureLocal() error = %v, want Docker IPv6 unavailable", err)
	}
}

func TestEnsureLocalAutoDoesNotHideUnknownNetworkFailure(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "auto"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker network inspect " + cfg.NetworkName: {
			ExitCode: 1,
			Stderr:   []byte("Error: No such network: " + cfg.NetworkName),
		},
		"docker network create --driver bridge --ipv6 --subnet " + cfg.DockerIPv4Subnet + " --gateway " + cfg.DockerIPv4Gateway + " --subnet " + cfg.DockerIPv6Subnet + " --gateway " + cfg.DockerIPv6Gateway + " " + cfg.NetworkName: {
			ExitCode: 1,
			Stderr:   []byte("permission denied"),
		},
	}}

	_, err := EnsureLocal(context.Background(), runner, io.Discard, cfg)
	if err == nil || errors.Is(err, errDockerIPv6Unavailable) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("EnsureLocal() error = %v, want original unknown network failure", err)
	}
}

func TestEnsureLocalRemovesStaleOwnedDockerResourcesBeforeCreate(t *testing.T) {
	runner := &recordingRunner{
		results: map[string]e2eexec.Result{
			"docker network inspect k3d-dcgm-exporter-gpu":       {},
			"docker volume inspect k3d-dcgm-exporter-gpu-images": {},
		},
	}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "ipv4"

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("docker", "network", "rm", "k3d-dcgm-exporter-gpu") {
		t.Fatalf("stale k3d network was not removed: %#v", runner.commands)
	}
	if !runner.has("docker", "volume", "rm", "k3d-dcgm-exporter-gpu-images") {
		t.Fatalf("stale k3d image volume was not removed: %#v", runner.commands)
	}
	if !runner.has("k3d", "cluster", "create", "dcgm-exporter-gpu") {
		t.Fatalf("cluster was not created after stale cleanup: %#v", runner.commands)
	}
}

func TestEnsureLocalRecreatesStaleNodeImage(t *testing.T) {
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": "stale-image\n",
		},
	}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("stale cluster was not deleted: %#v", runner.commands)
	}
	if !runner.has("k3d", "cluster", "create", "dcgm-exporter-gpu") {
		t.Fatalf("stale cluster was not recreated: %#v", runner.commands)
	}
}

func TestEnsureLocalRemovesOwnedDockerResourcesAfterClusterDelete(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "ipv4"
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": "stale-image\n",
		},
		results: map[string]e2eexec.Result{
			"docker network inspect " + cfg.NetworkName:                {},
			"docker volume inspect k3d-" + cfg.ClusterName + "-images": {},
		},
	}

	if _, err := EnsureLocal(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", cfg.ClusterName) {
		t.Fatalf("stale cluster was not deleted: %#v", runner.commands)
	}
	if !runner.has("docker", "network", "rm", cfg.NetworkName) {
		t.Fatalf("owned Docker network was not removed after cluster deletion: %#v", runner.commands)
	}
	if !runner.has("docker", "volume", "rm", "k3d-"+cfg.ClusterName+"-images") {
		t.Fatalf("owned Docker volume was not removed after cluster deletion: %#v", runner.commands)
	}
}

func TestEnsureLocalAutoReusesClusterWithoutIPFamilyProbe(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "auto"
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
			"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\n",
		},
		results: map[string]e2eexec.Result{
			dualStackProbeCommand: {
				ExitCode: 1,
				Stderr:   []byte("API server unavailable"),
			},
		},
	}

	if _, err := EnsureLocal(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatal(err)
	}
	if runner.has("kubectl", "create", "--dry-run=server") {
		t.Fatalf("auto mode unnecessarily probed dual-stack Service support: %#v", runner.commands)
	}
	if runner.has("k3d", "cluster", "delete", cfg.ClusterName) {
		t.Fatalf("auto mode deleted a reusable cluster: %#v", runner.commands)
	}
}

func TestEnsureLocalReusesMatchingCluster(t *testing.T) {
	dualStack := e2eexec.Result{Stdout: []byte(dualStackProbeResponse)}
	singleStack := e2eexec.Result{ExitCode: 1, Stderr: []byte(singleStackProbeError)}
	for _, tc := range []struct {
		name        string
		requested   string
		probeResult e2eexec.Result
		cgroupV1    config.FeatureMode
	}{
		// Auto reuses the cluster without probing its service IP family.
		{name: "auto", requested: "auto"},
		{name: "IPv4", requested: "ipv4", probeResult: singleStack},
		{name: "IPv6 alias", requested: "ipv6", probeResult: dualStack},
		{name: "dual-stack", requested: "dualstack", probeResult: dualStack},
		{name: "cgroup v1 enabled", requested: "auto", cgroupV1: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultLocalConfigForTest(t)
			cfg.BuildNodeImage = false
			cfg.IPFamily = tc.requested
			cfg.CgroupV1 = tc.cgroupV1
			runner := &recordingRunner{
				clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
				outputs: map[string]string{
					"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
					"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\n",
				},
				results: map[string]e2eexec.Result{dualStackProbeCommand: tc.probeResult},
			}
			var out bytes.Buffer

			if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
				t.Fatal(err)
			}
			if runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
				t.Fatalf("matching cluster was deleted: %#v", runner.commands)
			}
			if runner.has("k3d", "cluster", "create", "dcgm-exporter-gpu") {
				t.Fatalf("matching cluster was recreated: %#v", runner.commands)
			}
		})
	}
}

func TestEnsureLocalReusesServerWhenClusterNameContainsAgent(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.ClusterName = "team-agent-pool"
	runner := &recordingRunner{
		clusterList: "team-agent-pool 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-team-agent-pool-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
			"docker ps -a --format {{.Names}}":                                       "k3d-team-agent-pool-server-0\n",
		},
	}

	if _, err := EnsureLocal(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatal(err)
	}
	if runner.has("k3d", "cluster", "delete", cfg.ClusterName) || runner.has("k3d", "cluster", "create", cfg.ClusterName) {
		t.Fatalf("server-only cluster was recreated: %#v", runner.commands)
	}
}

func TestEnsureLocalRecreatesUnexpectedNodeTopology(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nodes string
		want  string
	}{
		{
			name:  "agent node",
			nodes: "k3d-dcgm-exporter-gpu-server-0\nk3d-dcgm-exporter-gpu-agent-0\n",
			want:  "has 1 agents, want 0",
		},
		{
			name:  "additional server",
			nodes: "k3d-dcgm-exporter-gpu-server-0\nk3d-dcgm-exporter-gpu-server-1\n",
			want:  "has 2 servers, want 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultLocalConfigForTest(t)
			cfg.BuildNodeImage = false
			runner := &recordingRunner{
				clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
				outputs: map[string]string{
					"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
					"docker ps -a --format {{.Names}}":                                         tc.nodes,
				},
			}
			var out bytes.Buffer

			if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
				t.Fatal(err)
			}
			if !runner.has("k3d", "cluster", "delete", cfg.ClusterName) {
				t.Fatalf("unexpected topology cluster was not deleted: %#v", runner.commands)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing topology recreate log %q:\n%s", tc.want, out.String())
			}
		})
	}
}

func TestEnsureLocalRejectsNonzeroAgentCountBeforeClusterCreation(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.Agents = "1"
	runner := &recordingRunner{}

	_, err := EnsureLocal(context.Background(), runner, io.Discard, cfg)
	if err == nil || !strings.Contains(err.Error(), "k3d applies GPU requests to every node role") {
		t.Fatalf("EnsureLocal() error = %v, want GPU-agent isolation diagnostic", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("EnsureLocal() ran commands before rejecting nonzero agents: %#v", runner.commands)
	}
}

func TestEnsureLocalRejectsNonNumericAgentCountBeforeClusterCreation(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.Agents = "abc"
	runner := &recordingRunner{}

	_, err := EnsureLocal(context.Background(), runner, io.Discard, cfg)
	if err == nil || !strings.Contains(err.Error(), `invalid k3d agent count "abc"`) {
		t.Fatalf("EnsureLocal() error = %v, want invalid agent-count diagnostic", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("EnsureLocal() ran commands before rejecting nonnumeric agents: %#v", runner.commands)
	}
}

func TestEnsureLocalRecreatesWithoutGPUDevice(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
			"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\n",
		},
		results: map[string]e2eexec.Result{
			`docker exec k3d-dcgm-exporter-gpu-server-0 sh -ec test -e /dev/nvidiactl && test "$NVIDIA_VISIBLE_DEVICES" = all && test "$NVIDIA_DRIVER_CAPABILITIES" = all`: {ExitCode: 1},
		},
	}
	var out bytes.Buffer

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("no-GPU cluster was not deleted: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "is missing the server GPU configuration") {
		t.Fatalf("missing server-GPU-config recreate log:\n%s", out.String())
	}
}

func TestEnsureLocalRecreatesWithoutServerGPUConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		results    map[string]e2eexec.Result
		outputs    map[string]string
		checkBinds bool
	}{
		{
			name: "GPU environment",
			results: map[string]e2eexec.Result{
				`docker exec k3d-dcgm-exporter-gpu-server-0 sh -ec test -e /dev/nvidiactl && test "$NVIDIA_VISIBLE_DEVICES" = all && test "$NVIDIA_DRIVER_CAPABILITIES" = all`: {ExitCode: 1},
			},
		},
		{
			name:       "host root mount",
			checkBinds: true,
			outputs: map[string]string{
				"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{range .HostConfig.Binds}}{{println .}}{{end}}": "\n",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultLocalConfigForTest(t)
			cfg.BuildNodeImage = false
			if tc.checkBinds {
				cfg.NvidiaCapabilitiesProbePath = t.TempDir()
			}
			runner := &recordingRunner{
				clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
				outputs: map[string]string{
					"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
					"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\n",
				},
				results: tc.results,
			}
			for command, output := range tc.outputs {
				runner.outputs[command] = output
			}

			if _, err := EnsureLocal(context.Background(), runner, io.Discard, cfg); err != nil {
				t.Fatal(err)
			}
			if !runner.has("k3d", "cluster", "delete", cfg.ClusterName) {
				t.Fatalf("stale server GPU configuration was not deleted: %#v", runner.commands)
			}
		})
	}
}

func TestEnsureLocalRecreatesWrongIPFamily(t *testing.T) {
	dualStack := e2eexec.Result{Stdout: []byte(dualStackProbeResponse)}
	singleStack := e2eexec.Result{ExitCode: 1, Stderr: []byte(singleStackProbeError)}
	unknown := e2eexec.Result{ExitCode: 1, Stderr: []byte("services is forbidden")}
	for _, tc := range []struct {
		name        string
		requested   string
		probeResult e2eexec.Result
	}{
		{name: "IPv4 cluster requested as IPv6", requested: "ipv6", probeResult: singleStack},
		{name: "IPv4 cluster requested as dual-stack", requested: "dualstack", probeResult: singleStack},
		{name: "dual-stack cluster requested as IPv4", requested: "ipv4", probeResult: dualStack},
		{name: "probe failure", requested: "dualstack", probeResult: unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultLocalConfigForTest(t)
			cfg.BuildNodeImage = false
			cfg.IPFamily = tc.requested
			runner := &recordingRunner{
				clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
				outputs: map[string]string{
					"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
					"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\n",
				},
				results: map[string]e2eexec.Result{dualStackProbeCommand: tc.probeResult},
			}
			var out bytes.Buffer

			if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
				t.Fatal(err)
			}
			if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
				t.Fatalf("wrong-IP-family cluster was not deleted: %#v", runner.commands)
			}
		})
	}
}
