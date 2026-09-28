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
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

const clusterTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testDependencies() config.Dependencies {
	return config.Dependencies{
		K3SVersion:          "v1.35.3-k3s1",
		DevicePluginVersion: "0.19.0",
		GPUOperatorVersion:  "v26.3.2",
		DRADriverVersion:    "25.12.0",
	}
}

func testLocalOptions() config.Tests {
	return config.Tests{
		K3SImage:           "mirror.example/rancher/k3s:v1.35.3-k3s1",
		K3DNodeBaseImage:   "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04",
		K3DNodeOutputImage: "dcgm-exporter/k3s-nvidia:v1.35.3-test",
		Dependencies:       testDependencies(),
	}
}

func versionRootForTest(t *testing.T) string {
	t.Helper()
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
		"GPU_OPERATOR_VERSION=v26.3.2",
		"NVIDIA_DRA_DRIVER_VERSION=25.12.0",
		"BUSYBOX_IMAGE_TAG=1.36.1",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "hack", "versions.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func defaultLocalConfigForTest(t *testing.T) LocalConfig {
	t.Helper()
	cfg, err := DefaultLocalConfig(versionRootForTest(t), testLocalOptions())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

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
		t.Fatalf("CUDAImage = %q, used exporter CUDA Ubuntu tag", cfg.CUDAImage)
	}
}

func TestEnsureNodeImageBuildsFromMirroredK3SRepository(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + cfg.NodeImage: {ExitCode: 1, Stderr: []byte("no such image")},
	}}

	if err := ensureNodeImage(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatalf("ensureNodeImage() error = %v", err)
	}
	if !runner.has("docker", "--build-arg", "K3S_IMAGE=mirror.example/rancher/k3s:v1.35.3-k3s1") {
		t.Fatalf("docker build missing mirrored K3S_IMAGE build arg: %#v", runner.commands)
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

func TestStatusCommands(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.LocalKubeconfig = "/tmp/local-kubeconfig"
	if err := Status(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if !runner.has("k3d", "cluster", "list") {
		t.Fatalf("missing k3d cluster list: %#v", runner.commands)
	}
	if !runner.has("k3d", "kubeconfig", "write", "dcgm-exporter-gpu") {
		t.Fatalf("missing local kubeconfig refresh: %#v", runner.commands)
	}
	if !runner.has("kubectl", "-n", "dcgm-exporter", "get", "pods,ds,svc") {
		t.Fatalf("missing exporter status: %#v", runner.commands)
	}
	if !runner.hasEnv("KUBECONFIG=/tmp/local-kubeconfig") {
		t.Fatalf("missing local kubeconfig env: %#v", runner.commands)
	}
}

func TestLogsFollowSkipsStandaloneDCGMLogs(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Follow = true
	if err := Logs(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if !runner.has("kubectl", "-n", "dcgm-exporter", "logs") {
		t.Fatalf("missing exporter logs: %#v", runner.commands)
	}
	if runner.has("kubectl", "-n", "dcgm-exporter-dcgm", "logs") {
		t.Fatalf("follow mode should not fetch DCGM logs: %#v", runner.commands)
	}
}

func TestDiagnosticsCommands(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.LocalKubeconfig = "/tmp/local-kubeconfig"
	if err := Diagnostics(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	for _, want := range [][]string{
		{"kubectl", "describe", "nodes"},
		{"kubectl", "get", "pods", "-A"},
		{"kubectl", "get", "events", "-A"},
		{"kubectl", "-n", "dcgm-exporter", "describe", "pods"},
		{"kubectl", "-n", "kube-system", "describe", "daemonset,pods"},
		{"kubectl", "-n", "dcgm-exporter-dcgm", "describe", "pods,deploy,svc"},
	} {
		if !runner.has(want[0], want[1:]...) {
			t.Fatalf("missing diagnostic command %v: %#v", want, runner.commands)
		}
	}
}

func TestCleanupCommands(t *testing.T) {
	runner := &recordingRunner{clusterList: "dcgm-exporter-gpu 1/1 1/1\n"}
	var out bytes.Buffer
	if err := Cleanup(context.Background(), runner, &out, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("missing k3d cleanup: %#v", runner.commands)
	}

	runner = &recordingRunner{}
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	if err := Cleanup(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("kubectl", "delete", "namespace", "dcgm-exporter") {
		t.Fatalf("missing external namespace cleanup: %#v", runner.commands)
	}
}

func TestCleanupSkipsAbsentLocalCluster(t *testing.T) {
	runner := &recordingRunner{clusterList: "other-cluster 1/1 1/1\n"}
	var out bytes.Buffer
	if err := Cleanup(context.Background(), runner, &out, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("deleted absent k3d cluster: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "already absent") {
		t.Fatalf("missing absent-cluster message:\n%s", out.String())
	}
}

func TestCleanupReturnsLocalClusterListFailure(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"k3d cluster list": {ExitCode: 1, Stderr: []byte("k3d failed\n")},
	}}
	var out bytes.Buffer
	err := Cleanup(context.Background(), runner, &out, DefaultConfig())
	if err == nil || !strings.Contains(err.Error(), "k3d cluster list") {
		t.Fatalf("Cleanup() error = %v, want k3d list failure", err)
	}
	if runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("deleted k3d cluster after list failure: %#v", runner.commands)
	}
}

func TestCleanupSkipsUnmanagedExternalNamespace(t *testing.T) {
	runner := &recordingRunner{namespaceLabels: map[string]string{
		"dcgm-exporter":      "someone-else",
		"dcgm-exporter-dcgm": managedByValue,
	}}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	if err := Cleanup(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if runner.deletedNamespace("dcgm-exporter") {
		t.Fatalf("deleted unmanaged namespace: %#v", runner.commands)
	}
	if !runner.deletedNamespace("dcgm-exporter-dcgm") {
		t.Fatalf("managed DCGM namespace was not deleted: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "WARN skipping cleanup for namespace dcgm-exporter") {
		t.Fatalf("missing unmanaged namespace warning:\n%s", out.String())
	}
}

func TestCleanupSkipsAbsentExternalNamespace(t *testing.T) {
	runner := &recordingRunner{namespaceLabels: map[string]string{}}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	if err := Cleanup(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if runner.deletedNamespace("dcgm-exporter") || runner.deletedNamespace("dcgm-exporter-dcgm") {
		t.Fatalf("deleted absent namespace: %#v", runner.commands)
	}
}

func TestCleanupReturnsExternalNamespaceLookupFailure(t *testing.T) {
	runner := &recordingRunner{namespaceLookupFailures: map[string]e2eexec.Result{
		"dcgm-exporter": {ExitCode: 1, Stderr: []byte("Unable to connect to the server\n")},
	}}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	err := Cleanup(context.Background(), runner, &out, cfg)

	if err == nil || !strings.Contains(err.Error(), "kubectl get namespace dcgm-exporter") {
		t.Fatalf("Cleanup() error = %v, want namespace lookup failure", err)
	}
	if runner.deletedNamespace("dcgm-exporter") {
		t.Fatalf("deleted namespace after lookup failure: %#v", runner.commands)
	}
}

func TestEnsureManagedNamespaceRejectsExistingUnlabeledNamespace(t *testing.T) {
	runner := &recordingRunner{namespaceLabels: map[string]string{"dcgm-exporter": ""}}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	err := EnsureManagedNamespace(context.Background(), runner, &out, cfg, "dcgm-exporter")
	if err == nil || !strings.Contains(err.Error(), "already exists without app.kubernetes.io/managed-by=dcgm-exporter-e2e") {
		t.Fatalf("EnsureManagedNamespace() error = %v, want unlabeled namespace", err)
	}
	if runner.has("kubectl", "label", "namespace", "dcgm-exporter") {
		t.Fatalf("unlabeled namespace was adopted: %#v", runner.commands)
	}
}

func TestDeployDCGMRejectsExistingUnlabeledNamespace(t *testing.T) {
	runner := &recordingRunner{namespaceLabels: map[string]string{"dcgm-exporter-dcgm": ""}}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	dcgm := DefaultDCGMConfig()
	dcgm.Image = "dcgm:test"
	err := DeployDCGM(context.Background(), runner, &out, cfg, dcgm)
	if err == nil || !strings.Contains(err.Error(), "already exists without app.kubernetes.io/managed-by=dcgm-exporter-e2e") {
		t.Fatalf("DeployDCGM() error = %v, want unlabeled namespace", err)
	}
	if runner.has("kubectl", "apply", "-f", "-") {
		t.Fatalf("unlabeled DCGM namespace was adopted: %#v", runner.commands)
	}
}

func TestDeployDCGMDoesNotApplyNamespaceManifest(t *testing.T) {
	runner := &recordingRunner{namespaceLabels: map[string]string{"dcgm-exporter-dcgm": managedByValue}}
	var out bytes.Buffer
	dcgm := DefaultDCGMConfig()
	dcgm.Image = "dcgm:test"
	if err := DeployDCGM(context.Background(), runner, &out, DefaultConfig(), dcgm); err != nil {
		t.Fatal(err)
	}
	if runner.stdinContains("kind: Namespace") {
		t.Fatalf("standalone DCGM deploy applied a namespace manifest: %#v", runner.commands)
	}
}

func TestDCGMManifestParsesAsYAML(t *testing.T) {
	cfg := DefaultConfig()
	dcgm := DefaultDCGMConfig()
	dcgm.Image = "dcgm:test"
	dcgm.ImagePullSecret = "pull-secret"

	manifest := dcgmManifest(cfg, dcgm)
	if strings.Contains(manifest, "\t") {
		t.Fatalf("standalone DCGM manifest contains tab indentation:\n%s", manifest)
	}
	for _, document := range strings.Split(manifest, "\n---\n") {
		document = strings.TrimSpace(document)
		if document == "" {
			continue
		}
		var resource map[string]any
		if err := yaml.Unmarshal([]byte(document), &resource); err != nil {
			t.Fatalf("standalone DCGM manifest did not parse:\n%s\nerror: %v", document, err)
		}
	}
}

func TestEnsureImagePullSecretRejectsPartialInputs(t *testing.T) {
	err := EnsureImagePullSecret(context.Background(), &recordingRunner{}, io.Discard, DefaultConfig(), "dcgm-exporter", "pull-secret", "")
	if err == nil || !strings.Contains(err.Error(), "requires both secret name and docker config path") {
		t.Fatalf("EnsureImagePullSecret() error = %v, want partial input error", err)
	}
}

func TestImportImageIfPresentDetectsK3dImportErrors(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"k3d image import dcgm:test -c dcgm-exporter-gpu": {
			Stderr: []byte("ERRO failed to import images in node 'k3d-dcgm-exporter-gpu-agent-0'\n"),
		},
	}}
	var out bytes.Buffer

	imported, err := ImportImageIfPresentWithResult(context.Background(), runner, &out, DefaultConfig(), "dcgm:test", "k3d_import_dcgm_image")
	if !imported {
		t.Fatal("ImportImageIfPresentWithResult() imported = false, want true")
	}
	if err == nil || !strings.Contains(err.Error(), "reported image import errors") {
		t.Fatalf("ImportImageIfPresentWithResult() error = %v, want import error", err)
	}
	if !strings.Contains(out.String(), "failed to import images") {
		t.Fatalf("import log did not include k3d error output:\n%s", out.String())
	}
}

func TestEnsureLocalCreatesClusterAndDevicePlugin(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false

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
	if !runner.hasEnv("KUBECONFIG=" + cfg.Kubeconfig) {
		t.Fatalf("missing kubeconfig env: %#v", runner.commands)
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
	runner := &gpuWaitFailureRunner{}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.WaitTimeout = "1ms"

	_, err := EnsureLocal(context.Background(), runner, &out, cfg)
	if err == nil || !strings.Contains(err.Error(), "cluster did not report allocatable nvidia.com/gpu resources before timeout") {
		t.Fatalf("EnsureLocal() error = %v, want allocatable GPU wait failure", err)
	}
}

func TestResetDevicePluginReappliesRuntimeClass(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer

	if err := ResetDevicePlugin(context.Background(), runner, &out, DefaultConfig(), defaultLocalConfigForTest(t)); err != nil {
		t.Fatal(err)
	}
	if !runner.has("kubectl", "apply", "-f", "-") {
		t.Fatalf("reset did not reapply RuntimeClass: %#v", runner.commands)
	}
	if !runner.has("helm", "upgrade", "--install", "nvidia-device-plugin") {
		t.Fatalf("reset did not reinstall device plugin: %#v", runner.commands)
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

func TestEnsureLocalMountsNvidiaCapabilitiesAtRuntimePath(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.NvidiaCapabilitiesPath = t.TempDir()
	cfg.NvidiaCapabilitiesMount = "/run/nvidia/host-driver"

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if !runner.has("k3d", "--volume", "/:/run/nvidia/host-driver:ro@agent:*") {
		t.Fatalf("missing NVIDIA host driver root mount: %#v", runner.commands)
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
			"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}":  "IPv4 IPv6\n",
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

func TestEnsureLocalReusesMatchingCluster(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
			"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\nk3d-dcgm-exporter-gpu-agent-0\n",
			"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}":  "IPv4 IPv6\n",
		},
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
}

func TestEnsureLocalRecreatesWrongAgentCount(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
			"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\n",
		},
	}
	var out bytes.Buffer

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("wrong-agent-count cluster was not deleted: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "has 0 agents, want 1") {
		t.Fatalf("missing agent-count recreate log:\n%s", out.String())
	}
}

func TestEnsureLocalRecreatesWithoutGPUDevice(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
			"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\nk3d-dcgm-exporter-gpu-agent-0\n",
		},
		results: map[string]e2eexec.Result{
			"docker exec k3d-dcgm-exporter-gpu-server-0 test -e /dev/nvidiactl": {ExitCode: 1},
			"docker exec k3d-dcgm-exporter-gpu-agent-0 test -e /dev/nvidiactl":  {ExitCode: 1},
		},
	}
	var out bytes.Buffer

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("no-GPU cluster was not deleted: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "does not expose /dev/nvidiactl") {
		t.Fatalf("missing GPU-device recreate log:\n%s", out.String())
	}
}

func TestEnsureLocalRecreatesWrongIPFamily(t *testing.T) {
	cfg := defaultLocalConfigForTest(t)
	cfg.BuildNodeImage = false
	cfg.IPFamily = "dualstack"
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker inspect k3d-dcgm-exporter-gpu-server-0 --format={{.Config.Image}}": cfg.NodeImage + "\n",
			"docker ps -a --format {{.Names}}":                                         "k3d-dcgm-exporter-gpu-server-0\nk3d-dcgm-exporter-gpu-agent-0\n",
			"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}":  "IPv4\n",
		},
	}
	var out bytes.Buffer

	if _, err := EnsureLocal(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("wrong-IP-family cluster was not deleted: %#v", runner.commands)
	}
}

func TestPrepareGPUOperatorSharedGPUInstallsAndWaitsForResources(t *testing.T) {
	runner := &gpuOperatorRunner{}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.LocalKubeconfig = "/tmp/kubeconfig"
	featureCfg := FeatureConfig{
		GPUOperator:       "install",
		Root:              versionRootForTest(t),
		SharedGPUReplicas: "4",
		WaitTimeout:       "1ms",
		ExporterImage:     "registry.example/dcgm-exporter:dev",
		Dependencies:      testDependencies(),
	}

	cleanup, err := PrepareGPUOperator(context.Background(), runner, &out, cfg, featureCfg, "gpuOperatorSharedGpu")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())

	if !runner.has("kubectl", "apply", "-f", "-") {
		t.Fatalf("missing shared GPU ConfigMap apply: %#v", runner.commands)
	}
	if !runner.has("helm", "upgrade", "--install", "gpu-operator") {
		t.Fatalf("missing GPU Operator install: %#v", runner.commands)
	}
	if !runner.has("helm", "dcgmExporter.imagePullPolicy=IfNotPresent") {
		t.Fatalf("local GPU Operator install should prefer imported images: %#v", runner.commands)
	}
	if !runner.has("kubectl", "-n", "gpu-operator", "get", "pods") {
		t.Fatalf("missing GPU Operator exporter wait: %#v", runner.commands)
	}
	if !runner.has("kubectl", "get", "nodes", "-o", "yaml") {
		t.Fatalf("missing shared GPU resource wait: %#v", runner.commands)
	}
	if !runner.stdinContains("replicas: 4") {
		t.Fatalf("shared GPU ConfigMap did not use requested replicas; commands = %#v", runner.commands)
	}
}

func TestPrepareGPUOperatorIPv6InstallsDualStackServiceAndWaitsForIPv6(t *testing.T) {
	runner := &gpuOperatorRunner{}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	featureCfg := FeatureConfig{
		GPUOperator:   "install",
		Root:          versionRootForTest(t),
		WaitTimeout:   "1ms",
		ExporterImage: "registry.example/dcgm-exporter:dev",
		BusyboxImage:  "busybox:1.36.1",
		Dependencies:  testDependencies(),
	}

	cleanup, err := PrepareGPUOperator(context.Background(), runner, &out, cfg, featureCfg, "gpuOperatorIPv6")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())

	if runner.has("helm", "dcgmExporter.service.ipFamilyPolicy") {
		t.Fatalf("GPU Operator install used unsupported service fields: %#v", runner.commands)
	}
	if !runner.has("kubectl", "-n", "gpu-operator", "get", "service", "nvidia-dcgm-exporter") {
		t.Fatalf("missing GPU Operator source service read: %#v", runner.commands)
	}
	if !runner.has("kubectl", "apply", "-f", "-") || !runner.stdinContains(`"ipFamilyPolicy":"RequireDualStack"`) || !runner.stdinContains(`"ipFamilies":["IPv4","IPv6"]`) {
		t.Fatalf("missing e2e-owned dual-stack service apply: %#v", runner.commands)
	}
	if !runner.has("kubectl", "-n", "gpu-operator", "get", "service", gpuOperatorIPv6ServiceName) {
		t.Fatalf("missing GPU Operator IPv6 service verification: %#v", runner.commands)
	}
	if !runner.has("kubectl", "-n", "dcgm-exporter", "run", "gpu-operator-ipv6-preflight") {
		t.Fatalf("missing GPU Operator IPv6 connectivity preflight: %#v", runner.commands)
	}
}

func TestProbeCapabilitiesSupportsConfigurableSharedGPU(t *testing.T) {
	runner := &capabilityRunner{}
	cfg := DefaultConfig()
	caps, err := ProbeCapabilities(context.Background(), runner, cfg, FeatureConfig{
		Root:               versionRootForTest(t),
		SharedGPUConfigure: "auto",
		SharedGPUReplicas:  "4",
		Dependencies:       testDependencies(),
	})
	if err != nil {
		t.Fatal(err)
	}

	sharedGPU := capabilityByName(caps, "cluster:shared_gpu")
	if sharedGPU.Status != "supported" {
		t.Fatalf("shared_gpu status = %s, want supported; capability = %#v", sharedGPU.Status, sharedGPU)
	}
	if sharedGPU.Source != "validation shared GPU setup" {
		t.Fatalf("shared_gpu source = %q, want validation shared GPU setup", sharedGPU.Source)
	}
	if !strings.Contains(sharedGPU.Evidence, "replicas=4") {
		t.Fatalf("shared_gpu evidence = %q, want replicas=4", sharedGPU.Evidence)
	}
}

func TestGPUOperatorExporterImageArgsIncludesDigest(t *testing.T) {
	imageArgs, err := gpuOperatorExporterImageArgs(FeatureConfig{
		ExporterImage: "registry.example/nvidia/dcgm-exporter:dev@" + clusterTestDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(imageArgs, " ")
	for _, want := range []string{
		"dcgmExporter.repository=registry.example/nvidia",
		"dcgmExporter.image=dcgm-exporter",
		"dcgmExporter.digest=" + clusterTestDigest,
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("gpuOperatorExporterImageArgs() = %q, missing %q", args, want)
		}
	}
}

func TestGPUOperatorExporterImageArgsRejectsInvalidReference(t *testing.T) {
	if _, err := gpuOperatorExporterImageArgs(FeatureConfig{ExporterImage: "invalid"}); err == nil {
		t.Fatal("gpuOperatorExporterImageArgs() error = nil")
	}
}

type recordingRunner struct {
	commands                []e2eexec.Command
	namespaceLabels         map[string]string
	namespaceLookupFailures map[string]e2eexec.Result
	clusterList             string
	outputs                 map[string]string
	results                 map[string]e2eexec.Result
}

type gpuWaitFailureRunner struct {
	recordingRunner
}

func (r *gpuWaitFailureRunner) Run(ctx context.Context, command e2eexec.Command) e2eexec.Result {
	if command.Name == "sh" && strings.Contains(strings.Join(command.Args, " "), "allocatable.nvidia") {
		r.commands = append(r.commands, command)
		return streamResult(command, e2eexec.Result{ExitCode: 1, Stderr: []byte("cluster did not report allocatable nvidia.com/gpu resources before timeout\n")})
	}
	return r.recordingRunner.Run(ctx, command)
}

func (r *recordingRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	r.commands = append(r.commands, command)
	key := strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
	if r.results != nil {
		if result, ok := r.results[key]; ok {
			return streamResult(command, result)
		}
	}
	if command.Name == "kubectl" && len(command.Args) >= 4 && command.Args[0] == "get" && command.Args[1] == "namespace" {
		namespace := command.Args[2]
		if r.namespaceLookupFailures != nil {
			if result, ok := r.namespaceLookupFailures[namespace]; ok {
				return streamResult(command, result)
			}
		}
		label := managedByValue
		if r.namespaceLabels != nil {
			var ok bool
			label, ok = r.namespaceLabels[namespace]
			if !ok {
				if hasArg(command.Args, "--ignore-not-found=true") {
					return e2eexec.Result{}
				}
				return e2eexec.Result{ExitCode: 1}
			}
		}
		if strings.Contains(strings.Join(command.Args, " "), "{.metadata.name}") {
			return streamResult(command, e2eexec.Result{Stdout: []byte(namespace + "\t" + label + "\n")})
		}
		return streamResult(command, e2eexec.Result{Stdout: []byte(label + "\n")})
	}
	if r.outputs != nil {
		if output, ok := r.outputs[key]; ok {
			return streamResult(command, e2eexec.Result{Stdout: []byte(output)})
		}
	}
	if command.Name == "k3d" && strings.Join(command.Args, " ") == "cluster list" && r.clusterList != "" {
		return streamResult(command, e2eexec.Result{Stdout: []byte(r.clusterList)})
	}
	if command.Name == "docker" && len(command.Args) >= 3 {
		switch strings.Join(command.Args[:2], " ") {
		case "network inspect", "volume inspect":
			return streamResult(command, e2eexec.Result{ExitCode: 1})
		}
	}
	return streamResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

func streamResult(command e2eexec.Command, result e2eexec.Result) e2eexec.Result {
	if command.Stdout != nil && len(result.Stdout) != 0 {
		_, _ = command.Stdout.Write(result.Stdout)
	}
	if command.Stderr != nil && len(result.Stderr) != 0 {
		_, _ = command.Stderr.Write(result.Stderr)
	}
	return result
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func (r *recordingRunner) has(name string, args ...string) bool {
	for _, command := range r.commands {
		if command.Name != name {
			continue
		}
		joined := strings.Join(command.Args, " ")
		if strings.Contains(joined, strings.Join(args, " ")) {
			return true
		}
	}
	return false
}

func (r *recordingRunner) hasEnv(value string) bool {
	for _, command := range r.commands {
		for _, env := range command.Env {
			if env == value {
				return true
			}
		}
	}
	return false
}

func (r *recordingRunner) deletedNamespace(namespace string) bool {
	for _, command := range r.commands {
		if command.Name == "kubectl" && len(command.Args) >= 3 && command.Args[0] == "delete" && command.Args[1] == "namespace" && command.Args[2] == namespace {
			return true
		}
	}
	return false
}

func (r *recordingRunner) stdinContains(value string) bool {
	for _, command := range r.commands {
		if strings.Contains(string(command.Stdin), value) {
			return true
		}
	}
	return false
}

type gpuOperatorRunner struct {
	recordingRunner
	clusterPolicyCalls int
}

func (r *gpuOperatorRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	r.commands = append(r.commands, command)
	if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "get clusterpolicy -o name") {
		r.clusterPolicyCalls++
		if r.clusterPolicyCalls == 1 {
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("not found\n")}
		}
		return e2eexec.Result{Stdout: []byte("clusterpolicy/gpu-cluster-policy\n")}
	}
	if command.Name == "kubectl" && len(command.Args) >= 3 && command.Args[0] == "get" && command.Args[1] == "namespace" {
		return e2eexec.Result{ExitCode: 1}
	}
	if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "get nodes -o yaml") {
		return e2eexec.Result{Stdout: []byte("nvidia.com/gpu.replicas: \"4\"\n")}
	}
	if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "-n gpu-operator get pods") {
		return e2eexec.Result{Stdout: []byte("nvidia-dcgm-exporter Running registry.example/nvidia/dcgm-exporter:dev\n")}
	}
	if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "-n gpu-operator get svc -o") {
		return e2eexec.Result{Stdout: []byte("nvidia-dcgm-exporter 9400\n")}
	}
	if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "-n gpu-operator get service nvidia-dcgm-exporter -o json") {
		return e2eexec.Result{Stdout: []byte(`{"spec":{"selector":{"app":"nvidia-dcgm-exporter"}}}` + "\n")}
	}
	if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "-n gpu-operator get service "+gpuOperatorIPv6ServiceName+" -o") {
		return e2eexec.Result{Stdout: []byte("RequireDualStack IPv4 IPv6 10.43.0.10 fd43::10\n")}
	}
	return e2eexec.Result{Stdout: []byte("ok\n")}
}

type capabilityRunner struct {
	recordingRunner
}

func (r *capabilityRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	r.commands = append(r.commands, command)
	joined := strings.Join(command.Args, " ")
	switch {
	case strings.Contains(joined, "jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}"):
		return e2eexec.Result{Stdout: []byte("1\n")}
	case strings.Contains(joined, "get nodes -o json"):
		return e2eexec.Result{Stdout: []byte("{}\n")}
	case strings.Contains(joined, "get clusterpolicy"):
		return e2eexec.Result{ExitCode: 1, Stderr: []byte("not found\n")}
	default:
		return e2eexec.Result{Stdout: []byte("ok\n")}
	}
}

func capabilityByName(caps []capability.Capability, name string) capability.Capability {
	for _, cap := range caps {
		if cap.Name == name {
			return cap
		}
	}
	return capability.Capability{Name: name}
}
