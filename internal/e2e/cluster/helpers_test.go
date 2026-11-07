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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
)

// Image digests and Service probe answers shared by the cluster tests.
const (
	clusterTestDigest       = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	clusterTestSHA512Digest = "sha512:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dualStackProbeCommand   = "kubectl create --dry-run=server -f - -o json"
	dualStackProbeResponse  = `{"spec":{"ipFamilies":["IPv4","IPv6"]}}`
	singleStackProbeError   = `Service is invalid: spec.ipFamilyPolicy: Invalid value: "RequireDualStack": this cluster is not configured for dual-stack services`
	ipv6SingleStackResponse = `{"spec":{"ipFamilies":["IPv6"]}}`
	ipv6SingleStackError    = `Service is invalid: spec.ipFamilies[0]: Invalid value: "IPv6": this cluster is not configured for IPv6`
)

// clusterConfigForTest keeps real filesystem cleanup away from working state and makes cluster waits
// recheck without a real delay.
func clusterConfigForTest(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Root = t.TempDir()
	cfg.pollInterval = time.Millisecond
	return cfg
}

// testDependencies returns dependency versions for setup that installs charts and images.
func testDependencies() config.Dependencies {
	return config.Dependencies{
		K3SVersion:              "v1.35.3-k3s1",
		DevicePluginVersion:     "0.19.0",
		ContainerToolkitVersion: "1.19.1-1",
		GPUOperatorVersion:      "v26.3.2",
		DRADriverVersion:        "25.12.0",
	}
}

// testLocalOptions returns the test options a local k3d cluster config is built from.
func testLocalOptions() config.TestOptions {
	return config.TestOptions{
		K3SImage:                  "mirror.example/rancher/k3s:v1.35.3-k3s1",
		K3DNodeBaseImage:          "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04",
		K3DNodeOutputImage:        "dcgm-exporter/k3s-nvidia:v1.35.3-test",
		ContainerToolkitTestImage: "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04",
		Dependencies:              testDependencies(),
	}
}

// versionRootForTest returns a temporary repository root with a versions file.
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
		"NVIDIA_CONTAINER_TOOLKIT_VERSION=1.19.1-1",
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

// defaultLocalConfigForTest returns the local k3d config for testLocalOptions, failing the test on error.
func defaultLocalConfigForTest(t *testing.T) LocalConfig {
	t.Helper()
	cfg, err := DefaultLocalConfig(versionRootForTest(t), testLocalOptions())
	if err != nil {
		t.Fatal(err)
	}
	cfg.NvidiaCapabilitiesProbePath = filepath.Join(t.TempDir(), "nvidia-capabilities")
	return cfg
}

// cleanupAfterTest runs a setup cleanup when the test ends and fails the test if the cleanup fails.
func cleanupAfterTest(t *testing.T, cleanup func(context.Context) error) {
	t.Helper()
	t.Cleanup(func() {
		if err := cleanup(context.Background()); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})
}

// runCleanup runs a setup cleanup now and fails the test if the cleanup fails.
func runCleanup(t *testing.T, cleanup func(context.Context) error) {
	t.Helper()

	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
}

// requireErrorContains fails the test when err does not match want: an empty want expects no error, and any
// other want expects an error whose text contains it. call names the function in the failure message.
func requireErrorContains(t *testing.T, call string, err error, want string) {
	t.Helper()

	if want == "" {
		if err != nil {
			t.Fatalf("%s error = %v", call, err)
		}
		return
	}

	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s error = %v, want %q", call, err, want)
	}
}
