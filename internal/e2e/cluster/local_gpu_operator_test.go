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
	"time"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestPrepareGPUOperatorClusterPolicyLookup checks which ClusterPolicy lookups mean an existing Operator, no
// Operator, or a failure that must stop setup before it changes the cluster.
func TestPrepareGPUOperatorClusterPolicyLookup(t *testing.T) {
	for _, test := range []struct {
		name        string
		lookup      e2eexec.Result
		wantErr     string
		wantInstall bool
	}{
		{name: "existing Operator", lookup: e2eexec.Result{Stdout: []byte("clusterpolicy/cluster-policy\n")}},
		{name: "no ClusterPolicy", lookup: e2eexec.Result{}, wantInstall: true},
		{
			name:        "no ClusterPolicy resource type",
			lookup:      e2eexec.Result{ExitCode: 1, Stderr: []byte(`error: the server doesn't have a resource type "clusterpolicy"`)},
			wantInstall: true,
		},
		{
			name:    "lookup forbidden",
			lookup:  e2eexec.Result{ExitCode: 1, Stderr: []byte("clusterpolicies.nvidia.com is forbidden")},
			wantErr: "check for an existing GPU Operator ClusterPolicy",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := clusterConfigForTest(t)
			cfg.Kubeconfig = "/tmp/kubeconfig"
			featureCfg := FeatureConfig{GPUOperator: config.GPUOperatorInstall, WaitTimeout: "1m"}
			featureCfg.Dependencies.GPUOperatorVersion = "1.0.0"
			runner := &recordingRunner{
				resultSequences: map[string][]e2eexec.Result{
					"kubectl get clusterpolicy -o name": {test.lookup, {Stdout: []byte("clusterpolicy/cluster-policy\n")}},
				},
				results: map[string]e2eexec.Result{
					"kubectl -n gpu-operator get pods -o json": {Stdout: []byte(readyOperatorExporterPods)},
				},
			}

			_, err := PrepareGPUOperator(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorChart")
			if test.wantErr == "" && err != nil {
				t.Fatalf("PrepareGPUOperator() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("PrepareGPUOperator() error = %v, want %q", err, test.wantErr)
			}
			if installed := runner.commandIndex("helm", "upgrade", "--install", "gpu-operator") >= 0; installed != test.wantInstall {
				t.Fatalf("GPU Operator installed = %t, want %t; commands = %q", installed, test.wantInstall, commandKeys(runner.commands))
			}
		})
	}
}

func TestPrepareGPUOperatorSharedGPUInstallsAndWaitsForResources(t *testing.T) {
	runner := newGPUOperatorRunner()
	var out bytes.Buffer
	cfg := clusterConfigForTest(t)
	cfg.LocalKubeconfig = "/tmp/kubeconfig"
	featureCfg := FeatureConfig{
		GPUOperator:       "install",
		Root:              versionRootForTest(t),
		SharedGPUReplicas: "4",
		WaitTimeout:       "1ms",
		ExporterImage:     "registry.example/dcgm-exporter:dev",
		Dependencies:      testDependencies(),
	}

	runner.results["kubectl get nodes -o yaml"] = e2eexec.Result{Stdout: []byte("nvidia.com/gpu.replicas: \"4\"\n")}

	cleanup, err := PrepareGPUOperator(context.Background(), runner, &out, cfg, featureCfg, "gpuOperatorSharedGpu")
	if err != nil {
		t.Fatal(err)
	}
	cleanupAfterTest(t, cleanup)

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
	runner := newGPUOperatorRunner()
	var out bytes.Buffer
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	cfg.Namespace = "dcgm-exporter-ipv6-test"
	featureCfg := FeatureConfig{
		GPUOperator:   "install",
		Root:          versionRootForTest(t),
		WaitTimeout:   "1ms",
		ExporterImage: "registry.example/dcgm-exporter:dev",
		BusyboxImage:  "busybox:1.36.1",
		Dependencies:  testDependencies(),
	}

	runner.respond("-n gpu-operator get svc -o", e2eexec.Result{Stdout: []byte("nvidia-dcgm-exporter 9400\n")}).
		respond("-n gpu-operator get service nvidia-dcgm-exporter -o json", e2eexec.Result{Stdout: []byte(`{"spec":{"selector":{"app":"nvidia-dcgm-exporter"}}}`)}).
		respond("-n gpu-operator get service "+gpuOperatorIPv6ServiceName+" -o", e2eexec.Result{Stdout: []byte("RequireDualStack IPv4 IPv6 10.43.0.10 fd43::10\n")})

	cleanup, err := PrepareGPUOperator(context.Background(), runner, &out, cfg, featureCfg, "gpuOperatorIPv6")
	if err != nil {
		t.Fatal(err)
	}
	cleanupAfterTest(t, cleanup)

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
	if !runner.has("kubectl", "-n", cfg.Namespace, "run", "gpu-operator-ipv6-preflight") {
		t.Fatalf("missing GPU Operator IPv6 connectivity preflight: %#v", runner.commands)
	}
}

func TestPrepareGPUOperatorMainUsesLocalChartForEachScenario(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		config   FeatureConfig
		want     []string
	}{
		{
			name:     "embedded custom metrics and metadata",
			scenario: "gpuOperatorMainEmbedded",
			want: []string{
				"dcgmExporter.config.name=" + gpuOperatorMainMetricsConfigName,
				"dcgmExporter.enablePodLabels=true",
				"dcgmExporter.enablePodUID=true",
				"dcgmExporter.podLabelAllowlistRegex[0]=^e2e_allowed$",
			},
		},
		{
			name:     "remote DCGM digest",
			scenario: "gpuOperatorMainRemote",
			config: FeatureConfig{
				GPUOperatorImage:     "registry.example/nvidia/gpu-operator:main",
				GPUOperatorDCGMImage: "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04@" + clusterTestDigest,
			},
			want: []string{
				"operator.repository=registry.example/nvidia",
				"operator.image=gpu-operator",
				"operator.version=main",
				"validator.repository=registry.example/nvidia",
				"validator.image=gpu-operator",
				"validator.version=main",
				"dcgm.enabled=true",
				"dcgm.repository=nvcr.io/nvidia/cloud-native",
				"dcgm.image=dcgm",
				"dcgm.version=" + clusterTestDigest,
			},
		},
		{
			name:     "GPUCluster DRA",
			scenario: "gpuOperatorMainDRA",
			want: []string{
				"clusterPolicy.deployCR=false",
				"gpuCluster.deployCR=true",
				"dcgm.enabled=true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newGPUOperatorRunner()
			chart := mainGPUOperatorChartForTest(t)
			featureCfg := tt.config
			featureCfg.GPUOperator = "install"
			featureCfg.GPUOperatorChart = chart
			featureCfg.WaitTimeout = "1ms"
			featureCfg.ExporterImage = "registry.example/nvidia/dcgm-exporter:dev"
			featureCfg.Dependencies = testDependencies()

			cleanup, err := PrepareGPUOperator(context.Background(), runner, io.Discard, clusterConfigForTest(t), featureCfg, tt.scenario)
			if err != nil {
				t.Fatal(err)
			}
			cleanupAfterTest(t, cleanup)

			checkGPUOperatorMainInstall(t, runner, chart, tt.want)
			checkGPUOperatorMainScenarioResources(t, runner, tt.scenario)
		})
	}
}

// checkGPUOperatorMainInstall fails the test unless the recorded main-chart install used the local chart with
// every wanted Helm argument, left the released chart and its repository alone, and ran the empty-cluster
// preflight lookups.
func checkGPUOperatorMainInstall(t *testing.T, runner *recordingRunner, chart string, want []string) {
	t.Helper()

	helmArgs := gpuOperatorMainHelmArgs(t, runner)
	if !strings.Contains(helmArgs, chart) {
		t.Fatalf("main chart install did not use %q: %s", chart, helmArgs)
	}
	for _, arg := range want {
		if !strings.Contains(helmArgs, arg) {
			t.Fatalf("main chart install missing %q: %s", arg, helmArgs)
		}
	}
	if strings.Contains(helmArgs, "--version") || strings.Contains(helmArgs, "nvidia-gpu-operator/gpu-operator") {
		t.Fatalf("main chart install unexpectedly used released chart arguments: %s", helmArgs)
	}
	if runner.has("helm", "repo", "add", "nvidia-gpu-operator") || runner.has("helm", "repo", "update", "nvidia-gpu-operator") {
		t.Fatalf("main chart install updated the released Helm repository: %#v", runner.commands)
	}

	// The namespace and the cluster-scoped Operator resources must be looked up before anything is created.
	if !runner.has("kubectl", "get", "namespace", "gpu-operator", "-o", "name") {
		t.Fatalf("main chart did not check for an existing namespace: %#v", runner.commands)
	}
	for _, resource := range []string{"clusterpolicy", "gpucluster"} {
		if !runner.has("kubectl", "get", resource, "-o", "name") || runner.has("kubectl", "get", resource, "-A", "-o", "name") {
			t.Fatalf("main chart preflight used the wrong command for cluster-scoped %s: %#v", resource, runner.commands)
		}
	}
}

// checkGPUOperatorMainScenarioResources fails the test unless the recorded commands created the metrics
// ConfigMap, or for DRA waited for GPUCluster and ResourceSlices, as the named main-chart scenario needs.
func checkGPUOperatorMainScenarioResources(t *testing.T, runner *recordingRunner, scenario string) {
	t.Helper()

	switch scenario {
	case "gpuOperatorMainEmbedded":
		if !runner.stdinContains(gpuOperatorMainMetricsConfigKey) || !runner.stdinContains("DCGM_FI_DEV_GPU_UTIL_RATIO") || runner.stdinContains("DCGM_EXP_FIELD_POLICY_ENABLED") {
			t.Fatalf("embedded metrics ConfigMap must contain only the ratio metric: %#v", runner.commands)
		}
	case "gpuOperatorMainRemote":
		for _, field := range []string{
			"DCGM_FI_DEV_GPU_UTIL_RATIO",
			"DCGM_EXP_FIELD_POLICY_ENABLED",
			"DCGM_EXP_FIELD_POLICY_CURRENT_VALUE",
			"DCGM_EXP_FIELD_POLICY_THRESHOLD",
			"DCGM_EXP_FIELD_POLICY_VIOLATION_COUNT",
			"DCGM_EXP_FIELD_POLICY_LAST_TRIGGER_TIMESTAMP_SECONDS",
		} {
			if !runner.stdinContains(field) {
				t.Fatalf("remote metrics ConfigMap is missing %s: %#v", field, runner.commands)
			}
		}
	case "gpuOperatorMainDRA":
		if runner.stdinContains(gpuOperatorMainMetricsConfigKey) {
			t.Fatalf("DRA main scenario unexpectedly created a metrics ConfigMap: %#v", runner.commands)
		}
		if !runner.has("kubectl", "get", "gpucluster", "-o", "name") || !runner.has("kubectl", "get", "resourceslices", "-o", "json") {
			t.Fatalf("main DRA install did not wait for GPUCluster and ResourceSlices: %#v", runner.commands)
		}
	}
}

func TestPrepareGPUOperatorMainRemoteUsesDigestImage(t *testing.T) {
	runner := newGPUOperatorRunner()
	chart := mainGPUOperatorChartForTest(t)
	featureCfg := FeatureConfig{
		GPUOperator:          "install",
		GPUOperatorChart:     chart,
		GPUOperatorDCGMImage: "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04@" + clusterTestDigest,
		WaitTimeout:          "1ms",
		ExporterImage:        "registry.example/nvidia/dcgm-exporter:dev",
		Dependencies:         testDependencies(),
	}

	cleanup, err := PrepareGPUOperator(context.Background(), runner, io.Discard, clusterConfigForTest(t), featureCfg, "gpuOperatorMainRemote")
	if err != nil {
		t.Fatal(err)
	}
	cleanupAfterTest(t, cleanup)

	want := strings.Join([]string{
		"upgrade", "--install", "gpu-operator", chart,
		"--namespace", "gpu-operator",
		"--create-namespace",
		"--set", "driver.enabled=false",
		"--set", "toolkit.enabled=false",
		"--set", "devicePlugin.enabled=true",
		"--set", "dcgmExporter.enabled=true",
		"--set", "dcgmExporter.repository=registry.example/nvidia",
		"--set", "dcgmExporter.image=dcgm-exporter",
		"--set", "dcgmExporter.imagePullPolicy=Always",
		"--set", "dcgmExporter.version=dev",
		"--set", "dcgmExporter.imagePullPolicy=IfNotPresent",
		"--set", "dcgmExporter.config.name=" + gpuOperatorMainMetricsConfigName,
		"--set", "dcgm.enabled=true",
		"--set", "dcgm.repository=nvcr.io/nvidia/cloud-native",
		"--set", "dcgm.image=dcgm",
		"--set", "dcgm.version=" + clusterTestDigest,
		"--wait", "--timeout", "1ms",
	}, " ")
	if got := gpuOperatorMainHelmArgs(t, runner); got != want {
		t.Fatalf("GPU Operator main Helm arguments = %q, want %q", got, want)
	}
}

func TestPrepareGPUOperatorMainRejectsExistingResourcesBeforeConfigMap(t *testing.T) {
	for _, tt := range []struct {
		name    string
		command string
		found   string
	}{
		{name: "namespace", command: "kubectl get namespace gpu-operator -o name", found: "namespace/gpu-operator\n"},
		{name: "ClusterPolicy", command: "kubectl get clusterpolicy -o name", found: "clusterpolicy/gpu-cluster-policy\n"},
		{name: "GPUCluster", command: "kubectl get gpucluster -o name", found: "gpucluster.nvidia.com/gpu-cluster\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Replaces the fixture's missing resource type, so the first lookup finds the resource.
			runner := newGPUOperatorRunner()
			runner.resultSequences[tt.command] = []e2eexec.Result{{Stdout: []byte(tt.found)}}
			_, err := PrepareGPUOperator(context.Background(), runner, io.Discard, DefaultConfig(), FeatureConfig{
				GPUOperator:      "install",
				GPUOperatorChart: mainGPUOperatorChartForTest(t),
			}, "gpuOperatorMainEmbedded")
			if err == nil || !strings.Contains(err.Error(), "empty cluster") {
				t.Fatalf("PrepareGPUOperator() error = %v, want pre-existing resource error", err)
			}
			if runner.has("kubectl", "create", "namespace", "gpu-operator") || runner.has("helm", "upgrade", "--install", "gpu-operator") {
				t.Fatalf("main scenario created resources after failed preflight: %#v", runner.commands)
			}
		})
	}
}

// TestGPUOperatorMainPreflightsBeforeCreatingAuthNamespace checks preflight and namespace setup before secret creation,
// confirms install is skipped on secret failure, and checks that the returned cleanup deletes the new namespace.
func TestGPUOperatorMainPreflightsBeforeCreatingAuthNamespace(t *testing.T) {
	chart := t.TempDir()
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	runner := newGPUOperatorRunner().respond(gpuOperatorRegistryAuthCreate, failedWith(errors.New("registry secret rejected")))
	featureCfg := FeatureConfig{
		GPUOperator:                  "install",
		GPUOperatorChart:             chart,
		ImagePullSecret:              "registry-auth",
		RegistryDockerConfigJSONPath: "/tmp/docker-config.json",
	}

	cleanup, err := PrepareGPUOperator(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorMainEmbedded")
	if err == nil || !strings.Contains(err.Error(), "prepare GPU Operator image pull secret") {
		t.Fatalf("PrepareGPUOperator() error = %v, want pull secret failure", err)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}

	preflight := runner.commandIndex("kubectl", "get", "namespace", "gpu-operator", "-o", "name")
	create := runner.commandIndexWithStdin("kubectl", []string{"create", "-f", "-"}, `"name":"gpu-operator"`)
	secret := runner.commandIndex("kubectl", "-n", "gpu-operator", "create", "secret", "generic", "registry-auth")
	delete := runner.commandIndex("kubectl", "delete", "namespace", "gpu-operator")
	if preflight < 0 || create < 0 || secret < 0 || delete < 0 ||
		preflight >= create || create >= secret || secret >= delete {
		t.Fatalf("main preflight/auth namespace order = %d/%d/%d/%d; commands = %#v", preflight, create, secret, delete, runner.commands)
	}
	if runner.commandIndex("helm", "upgrade", "--install", "gpu-operator") >= 0 {
		t.Fatalf("GPU Operator installed after auth setup failed: %#v", runner.commands)
	}
}

// TestInstallGPUOperatorStopsWhenDevicePluginRemovalFails checks that the local device plugin is not left
// running beside the Operator's own device plugin.
func TestInstallGPUOperatorStopsWhenDevicePluginRemovalFails(t *testing.T) {
	cfg := clusterConfigForTest(t)
	featureCfg := FeatureConfig{WaitTimeout: "1m"}
	featureCfg.Dependencies.GPUOperatorVersion = "1.0.0"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"helm uninstall nvidia-device-plugin --namespace kube-system --ignore-not-found --wait --timeout 1m": {
			ExitCode: 1, Stderr: []byte("injected uninstall failure"),
		},
	}}

	err := InstallGPUOperator(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorChart", nil)
	if err == nil || !strings.Contains(err.Error(), "remove the NVIDIA device plugin before installing GPU Operator") {
		t.Fatalf("InstallGPUOperator() error = %v, want device plugin removal failure", err)
	}
	if runner.commandIndex("helm", "upgrade", "--install", "gpu-operator") >= 0 {
		t.Fatalf("GPU Operator was installed after the device plugin removal failed: %q", commandKeys(runner.commands))
	}
}

// TestGPUOperatorSetupFailureCleanupDeletesOnlyCreatedNamespace checks that setup changes nothing after a failure,
// and that its cleanup deletes the namespace only when this run created it.
func TestGPUOperatorSetupFailureCleanupDeletesOnlyCreatedNamespace(t *testing.T) {
	for _, test := range []struct {
		name                 string
		createNamespaceError bool
		wantDelete           bool
	}{
		{name: "delete newly created namespace", wantDelete: true},
		{name: "keep reused namespace", createNamespaceError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := clusterConfigForTest(t)
			cfg.Kubeconfig = "/tmp/kubeconfig"
			runner := newGPUOperatorRunner().respond(gpuOperatorRegistryAuthCreate, failedWith(errors.New("registry secret rejected")))
			if test.createNamespaceError {
				runner.respond(namespaceCreate, namespaceExists)
			}
			featureCfg := FeatureConfig{
				GPUOperator:                  "install",
				ImagePullSecret:              "registry-auth",
				RegistryDockerConfigJSONPath: "/tmp/docker-config.json",
			}

			cleanup, err := PrepareGPUOperator(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorSharedGpu")
			if err == nil || !strings.Contains(err.Error(), "prepare GPU Operator image pull secret") {
				t.Fatalf("PrepareGPUOperator() error = %v, want pull secret failure", err)
			}
			if runner.commandIndex("kubectl", "delete", "namespace", "gpu-operator") >= 0 {
				t.Fatalf("setup deleted the namespace before its cleanup ran: %#v", runner.commands)
			}

			runCleanup(t, cleanup)
			deleted := runner.commandIndex("kubectl", "delete", "namespace", "gpu-operator") >= 0
			if deleted != test.wantDelete {
				t.Fatalf("namespace deletion = %t, want %t; commands = %#v", deleted, test.wantDelete, runner.commands)
			}
		})
	}
}

// TestGPUOperatorFailureReturnsCleanup checks that a failed install, or a failure after the install, leaves the
// Operator for diagnostics and returns a cleanup that uninstalls it before deleting its namespace.
func TestGPUOperatorFailureReturnsCleanup(t *testing.T) {
	installErr := errors.New("GPU Operator install failed")
	for _, test := range []struct {
		name       string
		helmErr    error
		wantErr    string
		createdNow bool
	}{
		{name: "install failure in a reused namespace", helmErr: installErr, wantErr: installErr.Error()},
		{name: "scenario failure after install", createdNow: true, wantErr: "timed out waiting for GPU Operator"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := clusterConfigForTest(t)
			cfg.Kubeconfig = "/tmp/kubeconfig"
			runner := newGPUOperatorRunner()
			if !test.createdNow {
				runner.respond(namespaceCreate, namespaceExists)
			}
			if test.helmErr != nil {
				runner.respond(gpuOperatorInstall, failedWith(test.helmErr))
			}
			featureCfg := FeatureConfig{GPUOperator: "install", WaitTimeout: "1ms"}
			featureCfg.Dependencies.GPUOperatorVersion = "1.0.0"

			cleanup, err := PrepareGPUOperator(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorSharedGpu")
			requireErrorContains(t, "PrepareGPUOperator()", err, test.wantErr)
			if runner.commandIndex("helm", "uninstall", "gpu-operator") >= 0 || runner.commandIndex("kubectl", "-n", "gpu-operator", "describe", "pods") >= 0 {
				t.Fatalf("setup collected diagnostics or uninstalled the Operator itself: %#v", runner.commands)
			}

			runCleanup(t, cleanup)
			install := runner.commandIndex("helm", "upgrade", "--install", "gpu-operator")
			uninstall := runner.commandIndex("helm", "uninstall", "gpu-operator")
			delete := runner.commandIndex("kubectl", "delete", "namespace", "gpu-operator")
			if install < 0 || uninstall <= install || delete <= uninstall {
				t.Fatalf("failed setup cleanup order = install %d, uninstall %d, delete %d; commands = %#v", install, uninstall, delete, runner.commands)
			}
		})
	}
}

// TestGPUOperatorDRAFailureCleanupRemovesDriverBeforeOperator checks that a DRA driver failure after a fresh
// Operator install changes nothing more, and that the returned cleanup removes the driver, then the Operator,
// then the node label, and reports cleanup failures.
func TestGPUOperatorDRAFailureCleanupRemovesDriverBeforeOperator(t *testing.T) {
	cfg, featureCfg := draSetupForTest(t)
	featureCfg.GPUOperator = config.GPUOperatorInstall
	featureCfg.Dependencies.GPUOperatorVersion = "1.0.0"
	runner := &recordingRunner{
		resultSequences: map[string][]e2eexec.Result{
			// Absent before the install, present once the install waits for it.
			"kubectl get clusterpolicy -o name": {
				{ExitCode: 1, Stderr: []byte(`error: the server doesn't have a resource type "clusterpolicy"`)},
				{Stdout: []byte("clusterpolicy/cluster-policy\n")},
			},
		},
		results: map[string]e2eexec.Result{
			"kubectl -n gpu-operator get pods -o json": {Stdout: []byte(readyOperatorExporterPods)},
			draResourceSlicesCommand:                   {Stdout: []byte(noResourceSlices)},
			draInstallCommand:                          {ExitCode: 1, Stderr: []byte("injected DRA install failure")},
			draUnlabelCommand:                          {ExitCode: 1, Stderr: []byte("injected unlabel failure")},
		},
	}

	cleanup, err := PrepareGPUOperator(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorDRA")
	if err == nil || !strings.Contains(err.Error(), "injected DRA install failure") {
		t.Fatalf("PrepareGPUOperator() error = %v, want the DRA install failure", err)
	}
	if runner.commandIndex("helm", "uninstall", draDriverReleaseName) >= 0 {
		t.Fatalf("setup removed the DRA driver before its cleanup ran: %q", commandKeys(runner.commands))
	}

	cleanupErr := cleanup(context.Background())
	if cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "injected unlabel failure") {
		t.Fatalf("cleanup() error = %v, want the unlabel failure", cleanupErr)
	}

	draInstall := runner.commandIndex("helm", "upgrade", "--install", draDriverReleaseName)
	draUninstall := runner.commandIndex("helm", "uninstall", draDriverReleaseName)
	operatorUninstall := runner.commandIndex("helm", "uninstall", "gpu-operator")
	unlabel := runner.commandIndex("kubectl", "label", "nodes", "--all", draNodePluginLabelKey+"-")
	if draInstall < 0 || draUninstall < draInstall || operatorUninstall < draUninstall || unlabel < operatorUninstall {
		t.Fatalf("order = DRA install %d, DRA uninstall %d, Operator uninstall %d, unlabel %d; commands = %q",
			draInstall, draUninstall, operatorUninstall, unlabel, commandKeys(runner.commands))
	}
}

// TestGPUOperatorFailureCleanupJoinsUninstallAndDeleteErrors keeps the install error from setup and reports
// both uninstall and namespace deletion errors from its cleanup.
func TestGPUOperatorFailureCleanupJoinsUninstallAndDeleteErrors(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	installErr := errors.New("GPU Operator install failed")
	uninstallErr := errors.New("Helm uninstall failed")
	deleteErr := errors.New("namespace delete failed")
	runner := newGPUOperatorRunner().
		respond(gpuOperatorInstall, failedWith(installErr)).
		respond(gpuOperatorUninstall, failedWith(uninstallErr)).
		respond(gpuOperatorNamespaceDelete, failedWith(deleteErr))
	featureCfg := FeatureConfig{GPUOperator: "install"}
	featureCfg.Dependencies.GPUOperatorVersion = "1.0.0"

	cleanup, err := PrepareGPUOperator(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorSharedGpu")
	if !errors.Is(err, installErr) {
		t.Fatalf("PrepareGPUOperator() error = %v, want the install error", err)
	}

	err = cleanup(context.Background())
	for _, want := range []error{uninstallErr, deleteErr} {
		if !errors.Is(err, want) {
			t.Fatalf("cleanup() error = %v, want joined error %q", err, want)
		}
	}
}

// TestCleanupGPUOperatorRechecksOwnershipBeforeDelete refuses deletion if ownership changes during cleanup.
func TestCleanupGPUOperatorRechecksOwnershipBeforeDelete(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	runner := (&recordingRunner{}).queue(gpuOperatorNamespaceLookup,
		// Passes the first ownership check before the Helm uninstall.
		namespaceResult(ownedNamespaceLabels(cfg.RunName)),
		// Fails the ownership recheck before the namespace delete.
		namespaceResult(ownedNamespaceLabels("another-run")),
	)
	err := CleanupGPUOperator(context.Background(), runner, io.Discard, cfg, FeatureConfig{})
	if err == nil || !strings.Contains(err.Error(), "not owned by run") {
		t.Fatalf("CleanupGPUOperator() error = %v, want changed-owner refusal", err)
	}
	if runner.commandIndex("helm", "uninstall", "gpu-operator") < 0 || len(runner.resultSequences[gpuOperatorNamespaceLookup]) != 0 {
		t.Fatalf("uninstall ran = %t, queued namespace results left = %d; want uninstall and an empty queue", runner.commandIndex("helm", "uninstall", "gpu-operator") >= 0, len(runner.resultSequences[gpuOperatorNamespaceLookup]))
	}
	if runner.commandIndex("kubectl", "delete", "namespace", "gpu-operator") >= 0 {
		t.Fatalf("namespace was deleted after its owner changed: %#v", runner.commands)
	}
}

// TestCleanupGPUOperatorReportsDeleteFailureAndRefusesForeignOwner covers normal cleanup errors and its separate ownership guard.
func TestCleanupGPUOperatorReportsDeleteFailureAndRefusesForeignOwner(t *testing.T) {
	t.Run("delete failure", func(t *testing.T) {
		cfg := clusterConfigForTest(t)
		cfg.Kubeconfig = "/tmp/kubeconfig"
		runner := (&recordingRunner{}).respond(gpuOperatorNamespaceDelete, failedWith(errors.New("namespace delete failed")))
		err := CleanupGPUOperator(context.Background(), runner, io.Discard, cfg, FeatureConfig{})
		if err == nil || !strings.Contains(err.Error(), "namespace delete failed") {
			t.Fatalf("CleanupGPUOperator() error = %v, want delete failure", err)
		}
	})

	t.Run("foreign owner", func(t *testing.T) {
		cfg := clusterConfigForTest(t)
		cfg.Kubeconfig = "/tmp/kubeconfig"
		runner := (&recordingRunner{}).queue(gpuOperatorNamespaceLookup, namespaceResult(ownedNamespaceLabels("another-run")))
		err := CleanupGPUOperator(context.Background(), runner, io.Discard, cfg, FeatureConfig{})
		if err == nil || !strings.Contains(err.Error(), "not owned by run") {
			t.Fatalf("CleanupGPUOperator() error = %v, want foreign-owner refusal", err)
		}
		if runner.commandIndex("helm", "uninstall", "gpu-operator") >= 0 || runner.commandIndex("kubectl", "delete", "namespace", "gpu-operator") >= 0 {
			t.Fatalf("CleanupGPUOperator() changed a foreign namespace: %#v", runner.commands)
		}
	})
}

func TestGPUOperatorExporterReady(t *testing.T) {
	tests := []struct {
		name string
		pods string
		want bool
	}{
		{
			name: "ready exporter",
			pods: `{"items":[{"metadata":{"name":"nvidia-dcgm-exporter-abc"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`,
			want: true,
		},
		{
			name: "running exporter is not ready",
			pods: `{"items":[{"metadata":{"name":"nvidia-dcgm-exporter-abc"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}]}}]}`,
			want: false,
		},
		{
			name: "separate running DCGM and pending exporter",
			pods: `{"items":[{"metadata":{"name":"nvidia-dcgm-abc"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"nvidia-dcgm-exporter-abc"},"status":{"phase":"Pending"}}]}`,
			want: false,
		},
		{
			name: "invalid response",
			pods: "not json",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gpuOperatorExporterReady([]byte(tt.pods)); got != tt.want {
				t.Fatalf("gpuOperatorExporterReady() = %t, want %t", got, tt.want)
			}
		})
	}
}

// TestGPUOperatorExporterImageArgsRendersTagAndDigestAsVersion verifies the GPU
// Operator receives its supported tag-at-digest version representation.
func TestGPUOperatorExporterImageArgsRendersTagAndDigestAsVersion(t *testing.T) {
	imageArgs, err := gpuOperatorExporterImageArgs(FeatureConfig{
		ExporterImage: "registry.example/nvidia/dcgm-exporter:dev@" + clusterTestDigest,
		Dependencies:  config.Dependencies{GPUOperatorVersion: "v26.3.2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(imageArgs, " ")
	for _, want := range []string{
		"dcgmExporter.repository=registry.example/nvidia",
		"dcgmExporter.image=dcgm-exporter",
		"dcgmExporter.version=dev@" + clusterTestDigest,
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("gpuOperatorExporterImageArgs() = %q, missing %q", args, want)
		}
	}
	if strings.Contains(args, "dcgmExporter.digest=") {
		t.Fatalf("gpuOperatorExporterImageArgs() = %q, must not use unsupported dcgmExporter.digest", args)
	}
}

// TestGPUOperatorExporterImageArgsRejectsDigestOnlyReference verifies the operator
// integration rejects references it cannot render as a version.
func TestGPUOperatorExporterImageArgsRejectsDigestOnlyReference(t *testing.T) {
	_, err := gpuOperatorExporterImageArgs(FeatureConfig{
		ExporterImage: "registry.example/nvidia/dcgm-exporter@" + clusterTestDigest,
		Dependencies:  config.Dependencies{GPUOperatorVersion: "v26.3.2"},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a tag-bearing digest reference") {
		t.Fatalf("gpuOperatorExporterImageArgs() error = %v, want tag-bearing digest reference error", err)
	}
}

func TestGPUOperatorExporterImageArgsRejectsInvalidReference(t *testing.T) {
	if _, err := gpuOperatorExporterImageArgs(FeatureConfig{ExporterImage: "invalid"}); err == nil {
		t.Fatal("gpuOperatorExporterImageArgs() error = nil")
	}
}

func TestGPUOperatorMainImageArgs(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  []string
	}{
		{
			name:  "tag",
			image: "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04",
			want: []string{
				"--set", "dcgm.repository=nvcr.io/nvidia/cloud-native",
				"--set", "dcgm.image=dcgm",
				"--set", "dcgm.version=4.7.0-ubuntu24.04",
			},
		},
		{
			name:  "digest",
			image: "nvcr.io/nvidia/cloud-native/dcgm@" + clusterTestDigest,
			want: []string{
				"--set", "dcgm.repository=nvcr.io/nvidia/cloud-native",
				"--set", "dcgm.image=dcgm",
				"--set", "dcgm.version=" + clusterTestDigest,
			},
		},
		{
			name:  "tag and digest",
			image: "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04@" + clusterTestDigest,
			want: []string{
				"--set", "dcgm.repository=nvcr.io/nvidia/cloud-native",
				"--set", "dcgm.image=dcgm",
				"--set", "dcgm.version=" + clusterTestDigest,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gpuOperatorMainImageArgs("dcgm", tt.image)
			if err != nil {
				t.Fatalf("gpuOperatorMainImageArgs() error = %v", err)
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("gpuOperatorMainImageArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestGPUOperatorMainImageArgsRejectsControllerDigest(t *testing.T) {
	image := "registry.example/nvidia/gpu-operator:main@" + clusterTestDigest
	_, err := gpuOperatorMainImageArgs("operator", image)
	want := "GPU Operator main chart cannot preserve a digest for operator image \"" + image + "\""
	if err == nil || err.Error() != want {
		t.Fatalf("gpuOperatorMainImageArgs() error = %v, want %q", err, want)
	}
}

func TestGPUOperatorMainImageArgsRejectsNonSHA256Digest(t *testing.T) {
	image := "nvcr.io/nvidia/cloud-native/dcgm@" + clusterTestSHA512Digest
	_, err := gpuOperatorMainImageArgs("dcgm", image)
	want := "GPU Operator main DCGM image \"" + image + "\" must use a sha256 digest"
	if err == nil || err.Error() != want {
		t.Fatalf("gpuOperatorMainImageArgs() error = %v, want %q", err, want)
	}
}

func TestGPUOperatorMainImageArgsRejectsUntaggedImage(t *testing.T) {
	_, err := gpuOperatorMainImageArgs("dcgm", "nvcr.io/nvidia/cloud-native/dcgm")
	if err == nil || !strings.Contains(err.Error(), "requires a tag or digest") {
		t.Fatalf("gpuOperatorMainImageArgs() error = %v, want missing-tag error", err)
	}
}

// mainGPUOperatorChartForTest returns an empty local GPU Operator chart directory.
func mainGPUOperatorChartForTest(t *testing.T) string {
	t.Helper()
	chart := filepath.Join(t.TempDir(), "gpu-operator")
	if err := os.MkdirAll(chart, 0o755); err != nil {
		t.Fatal(err)
	}
	return chart
}

// gpuOperatorMainHelmArgs returns the arguments of the recorded GPU Operator chart install.
func gpuOperatorMainHelmArgs(t *testing.T, runner *recordingRunner) string {
	t.Helper()
	for _, command := range runner.commands {
		if command.Name == "helm" && strings.Contains(strings.Join(command.Args, " "), "upgrade --install gpu-operator") {
			return strings.Join(command.Args, " ")
		}
	}
	t.Fatalf("missing GPU Operator main install: %#v", runner.commands)
	return ""
}

// TestBoundedCleanupGetsItsOwnLimit checks that a GPU Operator cleanup step gets a limit sized for one Helm
// wait, even when the group cleanup that calls it has already run out of time.
func TestBoundedCleanupGetsItsOwnLimit(t *testing.T) {
	featureCfg := FeatureConfig{WaitTimeout: "5m"}
	groupCtx, cancel := context.WithCancel(context.Background())
	cancel()
	var stepLimit time.Duration
	var stepErr error

	start := time.Now()
	err := boundedCleanup(gpuOperatorHelmCleanupLimit(featureCfg), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("cleanup step context has no deadline")
		}
		stepLimit = deadline.Sub(start)
		stepErr = ctx.Err()
		return nil
	})(groupCtx)
	if err != nil {
		t.Fatalf("boundedCleanup() error = %v", err)
	}

	if stepErr != nil {
		t.Fatalf("cleanup step context error = %v, want nil after the group context ended", stepErr)
	}
	want := 5*time.Minute + gpuOperatorCleanupTimeout
	if stepLimit < want-time.Second || stepLimit > want+time.Second {
		t.Fatalf("cleanup step limit = %s, want about %s", stepLimit, want)
	}
}

// TestGPUOperatorDRAOverridesRequireDriverVersionBeforeLabeling checks that the GPU Operator DRA scenario stops
// on a missing NVIDIA_DRA_DRIVER_VERSION before labeling nodes when it must install the driver, and goes on
// without a version when the cluster already publishes NVIDIA GPU ResourceSlices.
func TestGPUOperatorDRAOverridesRequireDriverVersionBeforeLabeling(t *testing.T) {
	cfg, featureCfg := draSetupForTest(t)
	featureCfg.Dependencies.DRADriverVersion = ""

	for _, test := range []struct {
		name      string
		slices    string
		wantErr   bool
		wantLabel bool
	}{
		{name: "driver must be installed", slices: noResourceSlices, wantErr: true},
		{name: "cluster already publishes NVIDIA GPU slices", slices: standardDRAResourceSlice, wantLabel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{results: map[string]e2eexec.Result{draResourceSlicesCommand: {Stdout: []byte(test.slices)}}}

			_, _, err := gpuOperatorInstallOverrides(context.Background(), runner, io.Discard, cfg, featureCfg, "gpuOperatorDRA")

			if test.wantErr && (err == nil || !strings.Contains(err.Error(), "NVIDIA_DRA_DRIVER_VERSION is required")) {
				t.Fatalf("gpuOperatorInstallOverrides() error = %v, want the missing-version error", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("gpuOperatorInstallOverrides() error = %v", err)
			}
			if labeled := runner.commandIndex("kubectl", "label", "nodes", "--all", draNodePluginLabelKey+"=true") >= 0; labeled != test.wantLabel {
				t.Fatalf("labeled nodes = %t, want %t; commands = %q", labeled, test.wantLabel, commandKeys(runner.commands))
			}
		})
	}
}
