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

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	urfavecli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/marker"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/suite"
)

const testImageDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func writeExecutableFile(path string) error {
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func fakeK8sProbeOutputs() map[string][]byte {
	return map[string][]byte{
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
		"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": []byte("0, NVIDIA Test GPU, GPU-test, 1.0, 9.0, [N/A], [N/A]\n"),
		"nvidia-smi topo -m":   []byte("GPU0 CPU Affinity NUMA Affinity GPU NUMA ID\n"),
		"nvidia-smi nvlink -s": []byte(""),
		"nvidia-smi -q":        []byte(""),
		"lscpu":                []byte("Architecture: x86_64\n"),
		"nvidia-smi mig -lgip": []byte("No MIG-supported devices found.\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}{\"\\n\"}{end}": []byte("1\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}":                       []byte("node-0\n"),
		"kubectl get nodes -o json":                                               []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}}]}` + "\n"),
		"kubectl get --raw /api/v1/nodes":                                         []byte("{}\n"),
		"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}": []byte("IPv4\n"),
	}
}

func writeTestVersions(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		"K3D_VERSION=5.8.3",
		"K3S_VERSION=v1.35.3-k3s1",
		"HELM_VERSION=4.1.0",
		"NVIDIA_DEVICE_PLUGIN_VERSION=0.19.0",
		"GPU_OPERATOR_VERSION=v26.3.2",
		"NVIDIA_DRA_DRIVER_VERSION=25.12.0",
		"CUDA_BASE_TAG=13.2.1-base",
		"CUDA_UBUNTU_TAG=24.04",
		"K3D_NODE_BASE_UBUNTU_TAG=24.04",
		"DCGM_VERSION=4.5.3",
		"BUSYBOX_IMAGE_TAG=1.36.1",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "hack", "versions.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"DCGM_VERSION":                     "4.5.3",
		"K3D_VERSION":                      "5.8.3",
		"K3S_VERSION":                      "v1.35.3-k3s1",
		"HELM_VERSION":                     "4.1.0",
		"NVIDIA_DEVICE_PLUGIN_VERSION":     "0.19.0",
		"GPU_OPERATOR_VERSION":             "v26.3.2",
		"NVIDIA_DRA_DRIVER_VERSION":        "25.12.0",
		"E2E_K3S_IMAGE":                    "rancher/k3s:v1.35.3-k3s1",
		"E2E_K3D_NODE_BASE_IMAGE":          "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04",
		"E2E_K3D_NODE_OUTPUT_IMAGE":        "dcgm-exporter/k3s-nvidia:test",
		"E2E_BUSYBOX_IMAGE":                "busybox:1.36.1",
		"E2E_CONTAINER_TOOLKIT_TEST_IMAGE": "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04",
		"E2E_CUDA_WORKLOAD_IMAGE":          "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04",
		"E2E_DCGM_IMAGE":                   "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04",
	} {
		t.Setenv(name, value)
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"bogus"}, &stdout, &stderr); err == nil {
		t.Fatal("run() error = nil, want error")
	}
}

func TestRunRootHelp(t *testing.T) {
	tests := [][]string{
		nil,
		{"--help"},
		{"help"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err != nil {
			t.Fatalf("run(%v) error = %v; stderr = %s", args, err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage: e2e <command> [options]") || !strings.Contains(stdout.String(), "e2e cluster up") {
			t.Fatalf("root help missing usage for %v:\n%s", args, stdout.String())
		}
	}
}

func TestRunTestsHelpDoesNotValidateImages(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"tests", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v; stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "USAGE:\n   e2e tests [options]") {
		t.Fatalf("help output missing usage:\n%s", stdout.String())
	}
	for _, want := range []string{"Dependencies", "Execution", "Feature setup", "Images and registry authentication", "Kubernetes", "$E2E_VERBOSE", "E2E_RESULT_MARKERS"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help output missing %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "exporter-image with") {
		t.Fatalf("help triggered image validation:\n%s", stdout.String())
	}
}

func TestRunTestsHelpMatchesFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/tests-help.txt")
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"tests", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v; stderr = %s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != strings.TrimSpace(string(fixture)) {
		t.Fatalf("help output differs from fixture:\n%s", stdout.String())
	}
}

func TestNVMLInjectionFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "remote DCGM with injection",
			args: []string{"--scenario", "host/dcgmUri", "--dcgm-nvml-injection-yaml", "fixture.yaml"},
			want: "incompatible with NVML injection",
		},
		{
			name: "injection without host selection",
			args: []string{"--suite", "container", "--dcgm-nvml-injection-yaml", "fixture.yaml"},
			want: "requires at least one selected host scenario",
		},
		{
			name: "injection scenario without YAML",
			args: []string{"--scenario", "host/nvmlInjectionMetrics"},
			want: "requires --dcgm-nvml-injection-yaml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTests(tt.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseTests() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRunTestsDryRun(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: map[string][]byte{
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
		"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": []byte("0, NVIDIA Test GPU, GPU-test, 1.0, 9.0, [N/A], [N/A]\n"),
		"nvidia-smi topo -m":   []byte("GPU0 CPU Affinity NUMA Affinity GPU NUMA ID\n"),
		"nvidia-smi nvlink -s": []byte(""),
		"nvidia-smi -q":        []byte(""),
		"lscpu":                []byte("Architecture: x86_64\n"),
		"nvidia-smi mig -lgip": []byte("No MIG-supported devices found.\n"),
	}}

	if err := runWithRootRunner([]string{"tests", "--dry-run", "--scenario", "k8s/nvlink"}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "active NVLink evidence was not detected:\n    k8s/nvlink") {
		t.Fatalf("dry-run output missing skipped nvlink plan:\n%s", output)
	}
	if !strings.Contains(output, "artifact mode: source-tree") {
		t.Fatalf("dry-run output missing artifact mode:\n%s", output)
	}
	if strings.Contains(output, "verbose: command:") {
		t.Fatalf("dry-run emitted verbose command output by default:\n%s", output)
	}
	if strings.Contains(output, "waived") {
		t.Fatalf("dry-run output used waived for pre-execution skip:\n%s", output)
	}
}

func TestRunTestsDryRunVerboseShowsCommandDetails(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: fakeK8sProbeOutputs()}

	if err := runWithRootRunner([]string{"tests", "--dry-run", "--verbose", "--scenario", "k8s/nvlink"}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "[e2e] verbose: command: nvidia-smi -L") {
		t.Fatalf("verbose dry-run output missing command details:\n%s", output)
	}
	if !strings.Contains(output, "[e2e] verbose: selected Kubernetes labels:") {
		t.Fatalf("verbose dry-run output missing label details:\n%s", output)
	}
}

func TestOutputRunnerPrintsFailedQuietCommandOutput(t *testing.T) {
	var stdout bytes.Buffer
	runner := newOutputRunner(&stdout, &fakeRunner{results: map[string]e2eexec.Result{
		"broken setup": {ExitCode: 1, Stdout: []byte("out\n"), Stderr: []byte("err\n")},
	}}, false)

	result := runner.Run(context.Background(), e2eexec.Command{Name: "broken", Args: []string{"setup"}, LogName: "broken_setup", QuietOnSuccess: true})
	if result.ExitCode != 1 {
		t.Fatalf("exit code = %d, want 1", result.ExitCode)
	}
	output := stdout.String()
	for _, want := range []string{"[e2e] FAIL broken_setup", "[e2e] command: broken setup", "[e2e] exit code: 1", "  out", "  err"} {
		if !strings.Contains(output, want) {
			t.Fatalf("quiet failure output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTestsRejectsInvalidRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{"tests", "--suite", "static"}, &stdout, &stderr, t.TempDir(), &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "repo root with go.mod or a package root with bin/") {
		t.Fatalf("runWithRootRunner() error = %v, want invalid root", err)
	}
}

func TestRunTestsRejectsMissingLiveImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{"tests", "--suite", "container", "--scenario", "container/imageStartup"}, &stdout, &stderr, root, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "container and Kubernetes validation require --exporter-image") {
		t.Fatalf("runWithRootRunner() error = %v, want missing image", err)
	}
}

func TestRunTestsRejectsExternalK8sWithoutImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--kubeconfig", "/tmp/kubeconfig"}, &stdout, &stderr, root, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "container and Kubernetes validation require --exporter-image") {
		t.Fatalf("runWithRootRunner() error = %v, want missing image", err)
	}
}

func TestRunSelectedSuitesContinuesAfterK8sSetupFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	runner := &fakeRunner{}
	err := runSelectedSuites(&stdout, root, runner, config.Config{Tests: config.Tests{
		Scenarios:            []string{"k8s/default", "static/paths"},
		Kubeconfig:           "/tmp/kubeconfig",
		ExporterImage:        "registry.example/dcgm-exporter:dev",
		DCGMFailureInjection: "false",
		GPUOperator:          "false",
		ResultMarkers:        true,
		E2EResultMarkers:     true,
	}})
	var ee exitError
	if !errors.As(err, &ee) || ee.code != exitSetupFailure {
		t.Fatalf("runSelectedSuites() error = %v, want setup exit error", err)
	}
	if !runner.hasCommand("dcgm-exporter-static.test") {
		t.Fatalf("static suite did not run after k8s setup failure; commands = %#v", runner.commands)
	}
	output := stdout.String()
	if !strings.Contains(output, "&&&& FAILED dcgm_exporter_e2e_setup") || !strings.Contains(output, "&&&& PASSED dcgm_exporter_e2e_static") {
		t.Fatalf("missing expected setup/static markers:\n%s", output)
	}
}

func TestManagedFailureInjectionCapabilitiesUseDeployableDCGMImage(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "cluster:standalone_dcgm_resources", Status: capability.StatusSupported},
	})
	got := capability.NewSnapshot(caps.Entries()).With(managedFailureInjectionCapabilities(context.Background(), &fakeRunner{}, config.Tests{
		DCGMFailureInjection: "auto",
		DCGMImage:            "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04",
	}, caps)...)

	if got.Lookup("dcgm:failure_injection").Status != capability.StatusSupported {
		t.Fatalf("failure injection capability = %#v, want supported", got.Lookup("dcgm:failure_injection"))
	}
	if got.Lookup("dcgm:remote_dcgm").Status != capability.StatusSupported {
		t.Fatalf("remote DCGM capability = %#v, want supported", got.Lookup("dcgm:remote_dcgm"))
	}
}

func TestFailureNvlinkHealthRequiresStandaloneDCGMImage(t *testing.T) {
	opts := config.Tests{Scenarios: []string{"k8s/failureNvlinkHealth"}, DCGMFailureInjection: "auto"}
	if !dcgmImageNeeded(opts) {
		t.Fatal("failureNvlinkHealth should need DCGM image probes")
	}
	if !standaloneDCGMImageNeeded(opts) {
		t.Fatal("failureNvlinkHealth should need DCGM image import/deploy")
	}
}

func TestManagedFailureInjectionCapabilitiesDoNotOverrideUnsupportedPrerequisite(t *testing.T) {
	caps := capability.NewSnapshot([]capability.Capability{
		{Name: "cluster:standalone_dcgm_resources", Status: capability.StatusSupported},
		{Name: "dcgm:failure_injection", Status: capability.StatusUnsupported, Reason: "image import failed"},
	})
	got := capability.NewSnapshot(caps.Entries()).With(managedFailureInjectionCapabilities(context.Background(), &fakeRunner{}, config.Tests{
		DCGMFailureInjection: "auto",
		DCGMImage:            "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04",
	}, caps)...)

	if got.Lookup("dcgm:failure_injection").Status != capability.StatusUnsupported {
		t.Fatalf("failure injection capability = %#v, want unsupported", got.Lookup("dcgm:failure_injection"))
	}
}

func TestLocalDCGMImageImportFailureFailsWithoutPullSecret(t *testing.T) {
	var stdout bytes.Buffer
	opts := config.Tests{
		DCGMFailureInjection: "auto",
		DCGMImage:            "dcgm:test",
	}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"k3d image import dcgm:test -c dcgm-exporter-gpu": {
			Stderr: []byte("ERRO failed to import images in node 'k3d-dcgm-exporter-gpu-agent-0'\n"),
		},
	}}

	err := ensureLocalDCGMImageImport(context.Background(), &stdout, runner, clusterConfig(t.TempDir(), opts), opts)
	if err == nil || !strings.Contains(err.Error(), "pass --docker-registry-login-file or pre-load the image") {
		t.Fatalf("ensureLocalDCGMImageImport() error = %v, want image prerequisite failure", err)
	}
}

func TestLocalDCGMImageImportFailureAllowsPullSecret(t *testing.T) {
	var stdout bytes.Buffer
	opts := config.Tests{
		DCGMFailureInjection: "auto",
		DCGMImage:            "dcgm:test",
		K8sImagePullSecret:   "pull-secret",
	}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"k3d image import dcgm:test -c dcgm-exporter-gpu": {
			Stderr: []byte("ERRO failed to import images in node 'k3d-dcgm-exporter-gpu-agent-0'\n"),
		},
	}}

	err := ensureLocalDCGMImageImport(context.Background(), &stdout, runner, clusterConfig(t.TempDir(), opts), opts)
	if err != nil {
		t.Fatalf("ensureLocalDCGMImageImport() error = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "relying on Kubernetes image pull secret pull-secret") {
		t.Fatalf("missing pull-secret warning:\n%s", stdout.String())
	}
}

func TestEnsureDCGMImageAvailableFailsWhenManifestUnavailable(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker manifest inspect dcgm:test": {
			ExitCode: 1,
			Stderr:   []byte("unauthorized"),
		},
		"docker image inspect dcgm:test": {
			ExitCode: 1,
			Stderr:   []byte("No such image"),
		},
	}}

	err := ensureDCGMImageAvailable(context.Background(), runner, config.Tests{DCGMImage: "dcgm:test"})
	if err == nil || !strings.Contains(err.Error(), "selected DCGM-probed scenarios require accessible DCGM image dcgm:test: unauthorized") {
		t.Fatalf("ensureDCGMImageAvailable() error = %v, want manifest failure", err)
	}
}

func TestEnsureDCGMImageAvailableUsesDockerConfig(t *testing.T) {
	dockerConfig := t.TempDir()
	runner := &fakeRunner{}

	if err := ensureDCGMImageAvailable(context.Background(), runner, config.Tests{
		DCGMImage:       "dcgm:test",
		DockerConfigDir: dockerConfig,
	}); err != nil {
		t.Fatalf("ensureDCGMImageAvailable() error = %v", err)
	}

	command, ok := runner.commandWithArgs("docker", "manifest", "inspect", "dcgm:test")
	if !ok {
		t.Fatalf("manifest inspect was not run; commands = %#v", runner.commands)
	}
	if !hasArg(command.Env, "DOCKER_CONFIG="+dockerConfig) {
		t.Fatalf("manifest inspect env = %#v, want DOCKER_CONFIG", command.Env)
	}
}

func TestLocalDCGMImageImportFailureFailsRequiredFailureInjection(t *testing.T) {
	var stdout bytes.Buffer
	opts := config.Tests{
		DCGMFailureInjection: "true",
		DCGMImage:            "dcgm:test",
	}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"k3d image import dcgm:test -c dcgm-exporter-gpu": {
			Stderr: []byte("ERRO failed to import images in node 'k3d-dcgm-exporter-gpu-agent-0'\n"),
		},
	}}

	err := ensureLocalDCGMImageImport(context.Background(), &stdout, runner, clusterConfig(t.TempDir(), opts), opts)
	if err == nil || !strings.Contains(err.Error(), "pass --docker-registry-login-file or pre-load the image") {
		t.Fatalf("ensureLocalDCGMImageImport() error = %v, want image prerequisite failure", err)
	}
}

func TestRunClusterCheckProbesLocalTools(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	if err := runWithRunner([]string{"cluster", "check"}, &stdout, &stderr, runner); err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}
	for _, want := range []string{"docker", "k3d", "kubectl", "helm"} {
		if !runner.hasCommandArgContaining("command -v " + want) {
			t.Fatalf("cluster check did not probe %s; commands = %#v", want, runner.commands)
		}
	}
	if !strings.Contains(stdout.String(), "local prerequisites are available") {
		t.Fatalf("cluster check output missing success message:\n%s", stdout.String())
	}
}

func TestRunClusterHelp(t *testing.T) {
	tests := [][]string{
		{"cluster"},
		{"cluster", "--help"},
		{"cluster", "help"},
		{"cluster", "up", "--help"},
		{"cluster", "deploy", "help"},
		{"cluster", "status", "--help"},
		{"cluster", "logs", "--help"},
	}
	for _, args := range tests {
		var stdout, stderr bytes.Buffer
		runner := &fakeRunner{}
		if err := runWithRunner(args, &stdout, &stderr, runner); err != nil {
			t.Fatalf("runWithRunner(%v) error = %v; stderr = %s", args, err, stderr.String())
		}
		if len(runner.commands) != 0 {
			t.Fatalf("runWithRunner(%v) executed commands while showing help: %#v", args, runner.commands)
		}
		wantUsage := "Usage: e2e cluster <command> [options]"
		if len(args) > 2 {
			wantUsage = "USAGE:\n   e2e cluster " + args[1]
		}
		if !strings.Contains(stdout.String(), wantUsage) {
			t.Fatalf("cluster help missing usage for %v:\n%s", args, stdout.String())
		}
		if len(args) <= 2 && !strings.Contains(stdout.String(), "deploy-dcgm") {
			t.Fatalf("cluster help missing deploy-dcgm for %v:\n%s", args, stdout.String())
		}
		if len(args) > 2 && args[1] == "status" && !strings.Contains(stdout.String(), "$E2E_KUBECONFIG") {
			t.Fatalf("cluster status help missing environment variables:\n%s", stdout.String())
		}
	}
}

func TestRunClusterUpCreatesLocalCluster(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	if err := runWithRootRunner([]string{"cluster", "up", "--k3d-ip-family", "dualstack"}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("create") || !runner.hasCommandArg("dcgm-exporter-gpu") {
		t.Fatalf("cluster up did not create local cluster; commands = %#v", runner.commands)
	}
	if !runner.hasCommandArg("--cluster-cidr=10.42.0.0/16,fd42::/56@server:*") {
		t.Fatalf("cluster up did not pass dual-stack k3d args; commands = %#v", runner.commands)
	}
	if !runner.hasCommandArg("nvidia-device-plugin") {
		t.Fatalf("cluster up did not install device plugin; commands = %#v", runner.commands)
	}
}

func TestRunClusterDeployInstallsExporter(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	if err := runWithRootRunner([]string{
		"cluster", "deploy",
		"--exporter-image", "registry.example/dcgm-exporter:dev",
	}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("dcgm-exporter") || !runner.hasCommandArg("--set-string") || !runner.hasCommandArg("image.repository=registry.example/dcgm-exporter") {
		t.Fatalf("cluster deploy did not install exporter with image settings; commands = %#v", runner.commands)
	}
	if !strings.Contains(stdout.String(), "helm_deploy_exporter") {
		t.Fatalf("cluster deploy output missing helm deploy step:\n%s", stdout.String())
	}
}

func TestRunClusterDeployDCGMFailsBeforeClusterSetupWhenImageUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	const image = "registry.example/dcgm:test"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker manifest inspect " + image: {ExitCode: 1, Stderr: []byte("unauthorized")},
		"docker image inspect " + image:    {ExitCode: 1, Stderr: []byte("not found")},
	}}
	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{"cluster", "deploy-dcgm", "--dcgm-image", image}, &stdout, &stderr, root, runner)
	if err == nil || !strings.Contains(err.Error(), "require accessible DCGM image") {
		t.Fatalf("runWithRootRunner() error = %v, want inaccessible DCGM image", err)
	}
	for _, command := range runner.commands {
		if command.Name == "k3d" || command.Name == "kubectl" || command.Name == "helm" {
			t.Fatalf("cluster setup ran after image check failed: %#v", runner.commands)
		}
	}
}

func TestRunClusterCleanupUsesPackageToolPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(root, ".e2e-tools", "bin")
	var sawK3D bool
	runner := &fakeRunner{
		results: map[string]e2eexec.Result{
			"k3d cluster list": {ExitCode: 0, Stdout: []byte("other-cluster 1/1 1/1\n")},
		},
		onRun: func(command e2eexec.Command) {
			if command.Name != "k3d" {
				return
			}
			sawK3D = true
			if got := os.Getenv("PATH"); !strings.HasPrefix(got, wantPrefix+string(os.PathListSeparator)) {
				t.Fatalf("PATH = %q, want prefix %q", got, wantPrefix)
			}
		},
	}
	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{"cluster", "cleanup"}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !sawK3D {
		t.Fatal("cluster cleanup did not invoke k3d")
	}
}

func TestDRAProbeNeededOnlyForDRASelections(t *testing.T) {
	tests := []struct {
		name string
		opts config.Tests
		want bool
	}{
		{
			name: "default scenario does not need DRA pre-probe",
			opts: config.Tests{Scenarios: []string{"k8s/default"}},
		},
		{
			name: "all scenarios need DRA pre-probe",
			opts: config.Tests{},
			want: true,
		},
		{
			name: "explicit DRA scenario needs DRA pre-probe",
			opts: config.Tests{Scenarios: []string{"k8s/dra"}},
			want: true,
		},
		{
			name: "explicit GPU Operator DRA scenario needs DRA pre-probe",
			opts: config.Tests{Scenarios: []string{"k8s/gpuOperatorDRA"}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := draProbeNeeded(tt.opts); got != tt.want {
				t.Fatalf("draProbeNeeded() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestParseTestsDefaultsToMaximumCoverageModes(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseTests(nil, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.DCGMFailureInjection != "true" {
		t.Fatalf("dcgm failure injection = %q, want true", opts.DCGMFailureInjection)
	}
	if opts.GPUOperator != "install" {
		t.Fatalf("gpu operator = %q, want install", opts.GPUOperator)
	}
	if opts.K3dIPFamily != "dualstack" {
		t.Fatalf("k3d ip family = %q, want dualstack", opts.K3dIPFamily)
	}
	if opts.MIGConfigure != "true" {
		t.Fatalf("MIG configure default = %q, want true", opts.MIGConfigure)
	}
	if opts.DRAConfigure != "true" {
		t.Fatalf("DRA configure default = %q, want true", opts.DRAConfigure)
	}
	if opts.SharedGPUConfigure != "true" {
		t.Fatalf("shared GPU configure default = %q, want true", opts.SharedGPUConfigure)
	}
	if !opts.InstallDeps {
		t.Fatalf("install deps default = false, want true")
	}
}

func TestParseTestsCanDisableInstallDeps(t *testing.T) {
	t.Setenv("E2E_INSTALL_DEPS", "false")
	var stderr bytes.Buffer
	opts, err := parseTests(nil, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.InstallDeps {
		t.Fatalf("install deps default = true, want false from env")
	}
}

func TestParseTestsAcceptsFeatureModeValues(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseTests([]string{"--dcgm-failure-injection", "true", "--gpu-operator", "false"}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.DCGMFailureInjection != "true" {
		t.Fatalf("dcgm failure injection = %q, want true", opts.DCGMFailureInjection)
	}
	if opts.GPUOperator != "false" {
		t.Fatalf("gpu operator = %q, want false", opts.GPUOperator)
	}
}

func TestParseTestsRejectsUnknownSelectors(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseTests([]string{"--suite", "bogus"}, &stderr); err == nil {
		t.Fatal("parseTests() suite error = nil")
	}
	if _, err := parseTests([]string{"--scenario", "k8s/doesNotExist"}, &stderr); err == nil {
		t.Fatal("parseTests() scenario error = nil")
	}
	if _, err := parseTests([]string{"--scenario", "k8s/default", "--skip-scenario", "k8s/default"}, &stderr); err == nil {
		t.Fatal("parseTests() contradictory scenario error = nil")
	}
}

func TestParseTestsRejectsInvalidBoolEnv(t *testing.T) {
	t.Setenv("E2E_DRY_RUN", "treu")
	var stderr bytes.Buffer
	if _, err := parseTests(nil, &stderr); err == nil || !strings.Contains(err.Error(), "E2E_DRY_RUN") {
		t.Fatalf("parseTests() error = %v, want E2E_DRY_RUN parse error", err)
	}
}

func TestParseTestsAcceptsPackagedRunFlags(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseTests([]string{
		"--tools-dir", "/tmp/e2e-tools",
		"--install-deps",
		"--install-deps-docker=false",
		"--install-deps-nvidia-container-toolkit", "false",
		"--install-deps-dcgm", "true",
		"--install-deps-vsock", "false",
		"--docker-registry-login", "registry.example,user,/run/secret",
		"--docker-registry-login-file", "/run/logins",
		"--k8s-image-pull-secret", "pull-secret",
		"--exporter-image", "registry.example/dcgm-exporter:dev@" + testImageDigest,
		"--dcgm-image", "nvcr.io/nvidia/cloud-native/dcgm:4.0.0-1-ubuntu22.04@" + testImageDigest,
		"--k8s-runtime-class", "nvidia",
		"--k8s-node-selector-key", "nvidia.com/gpu.present",
		"--k8s-node-selector-value", "true",
		"--k3d-ip-family", "dualstack",
		"--shared-gpu-replicas", "4",
		"--gpu-operator", "existing",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}

	if !opts.InstallDeps {
		t.Fatal("expected install-deps to be true")
	}
	if opts.ToolsDir != "/tmp/e2e-tools" {
		t.Fatalf("tools dir = %q", opts.ToolsDir)
	}
	if opts.InstallDepsDocker != "false" || opts.InstallDepsNvidiaToolkit != "false" || opts.InstallDepsDCGM != "true" || opts.InstallDepsVSOCK != "false" {
		t.Fatalf("install-deps controls = %#v", opts)
	}
	if len(opts.DockerRegistryLogins) != 1 || opts.DockerRegistryLogins[0] != "registry.example,user,/run/secret" {
		t.Fatalf("docker registry logins = %#v", opts.DockerRegistryLogins)
	}
	if opts.DockerRegistryLoginFile != "/run/logins" || opts.K8sImagePullSecret != "pull-secret" {
		t.Fatalf("registry/k8s auth fields = %#v", opts)
	}
	if opts.ExporterImage == "" {
		t.Fatalf("exporter image fields missing: %#v", opts)
	}
	if opts.DCGMImage == "" {
		t.Fatalf("dcgm image fields missing: %#v", opts)
	}
	if opts.K8sRuntimeClass != "nvidia" || opts.K8sNodeSelectorKey == "" || opts.K8sNodeSelectorValue != "true" {
		t.Fatalf("k8s placement fields = %#v", opts)
	}
	if opts.K3dIPFamily != "dualstack" || opts.SharedGPUReplicas != "4" || opts.GPUOperator != "existing" {
		t.Fatalf("feature mode fields = %#v", opts)
	}
}

func TestEnsureDockerImageAvailablePullsMissingImage(t *testing.T) {
	var stdout bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect registry.example/dcgm-exporter:dev": {ExitCode: 1},
	}}
	err := ensureDockerImageAvailable(context.Background(), &stdout, runner, config.Tests{}, "registry.example/dcgm-exporter:dev")
	if err != nil {
		t.Fatalf("ensureDockerImageAvailable() error = %v", err)
	}
	if !runner.hasCommandArg("pull") || !runner.hasCommandArg("registry.example/dcgm-exporter:dev") {
		t.Fatalf("missing docker pull command: %#v", runner.commands)
	}
}

func TestParseTestsAcceptsEnvironmentDefaults(t *testing.T) {
	t.Setenv("E2E_SUITE", "host\ncontainer")
	t.Setenv("E2E_SKIP_SUITE", "static")
	t.Setenv("E2E_SCENARIO", "host/startupMetrics\ncontainer/imageStartup")
	t.Setenv("E2E_SKIP_SCENARIO", "host/reload")
	t.Setenv("E2E_KUBECONFIG", "/tmp/kubeconfig")
	t.Setenv("E2E_DOCKER_REGISTRY_LOGIN", "registry.example,user,/run/secret\nregistry2.example,user2,/run/secret2")
	t.Setenv("E2E_DOCKER_CONFIG_DIR", "/tmp/docker-config")
	t.Setenv("E2E_EXPORTER_UBUNTU_IMAGE", "registry.example/dcgm-exporter:dev-ubuntu24.04-amd64@"+testImageDigest)
	t.Setenv("E2E_MIG_INSTANCE_ENTITY_ID", "101")
	t.Setenv("E2E_MIG_INSTANCE_NVML_ID", "7")
	t.Setenv("E2E_UNSUPPORTED_FIELD_CANDIDATE", "DCGM_FI_DEV_RETIRED_PENDING")
	t.Setenv("E2E_SUITE_RESULT_MARKERS", "false")
	t.Setenv("E2E_BUILD_ONLY", "true")
	t.Setenv("E2E_LIST_SCENARIOS", "true")

	var stderr bytes.Buffer
	opts, err := parseTests(nil, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if strings.Join(opts.Suites, ",") != "host,container" {
		t.Fatalf("suites = %#v", opts.Suites)
	}
	if strings.Join(opts.SkipSuites, ",") != "static" {
		t.Fatalf("skip suites = %#v", opts.SkipSuites)
	}
	if strings.Join(opts.Scenarios, ",") != "host/startupMetrics,container/imageStartup" {
		t.Fatalf("scenarios = %#v", opts.Scenarios)
	}
	if strings.Join(opts.SkipScenarios, ",") != "host/reload" {
		t.Fatalf("skip scenarios = %#v", opts.SkipScenarios)
	}
	if opts.Kubeconfig != "/tmp/kubeconfig" || opts.DockerConfigDir != "/tmp/docker-config" {
		t.Fatalf("env fields missing: %#v", opts)
	}
	if opts.ExporterUbuntuImage != "registry.example/dcgm-exporter:dev-ubuntu24.04-amd64@"+testImageDigest {
		t.Fatalf("ubuntu image env fields missing: %#v", opts)
	}
	if len(opts.DockerRegistryLogins) != 2 || opts.DockerRegistryLogins[0] != "registry.example,user,/run/secret" || opts.DockerRegistryLogins[1] != "registry2.example,user2,/run/secret2" {
		t.Fatalf("registry env = %#v", opts.DockerRegistryLogins)
	}
	if opts.MIGInstanceEntityID != "101" || opts.MIGInstanceNVMLID != "7" || opts.UnsupportedFieldCandidate == "" {
		t.Fatalf("suite env fields missing: %#v", opts)
	}
	if opts.E2EResultMarkers {
		t.Fatalf("E2E result markers = true, want false from env")
	}
	if !opts.BuildOnly || !opts.ListScenarios {
		t.Fatalf("build-only/list-scenarios env fields missing: %#v", opts)
	}
	if opts.MIGConfigure != "true" {
		t.Fatalf("MIG configure default = %q, want true", opts.MIGConfigure)
	}
}

func TestImageFlagUsesUrfavePrecedence(t *testing.T) {
	t.Setenv("E2E_EXPORTER_IMAGE", "environment/image:tag")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "environment over default", want: "environment/image:tag"},
		{name: "flag over environment", args: []string{"--exporter-image", "argument/image:tag"}, want: "argument/image:tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := defaultTestsConfig()
			opts.ExporterImage = "default/image:tag"
			app := urfavecli.NewApp()
			app.Writer = io.Discard
			app.ErrWriter = io.Discard
			app.HideHelp = true
			app.Commands = []*urfavecli.Command{newTestsCLICommand(&opts)}
			if err := app.Run(append([]string{"e2e", "tests"}, tt.args...)); err != nil {
				t.Fatal(err)
			}
			if opts.ExporterImage != tt.want {
				t.Fatalf("ExporterImage = %q, want %q", opts.ExporterImage, tt.want)
			}
		})
	}
}

func TestDCGMVersionUsesFirstConfiguredEnvironmentVariable(t *testing.T) {
	t.Setenv("DCGM_VERSION", "4.5.3")
	t.Setenv("E2E_DCGM_VERSION", "4.6.0")
	var stderr bytes.Buffer
	opts, err := parseTests(nil, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Dependencies.DCGMVersion != "4.6.0" {
		t.Fatalf("DCGM version = %q, want E2E_DCGM_VERSION", opts.Dependencies.DCGMVersion)
	}
}

func TestDCGMVersionFallsBackToCheckedInPinEnvironment(t *testing.T) {
	previous, existed := os.LookupEnv("E2E_DCGM_VERSION")
	if err := os.Unsetenv("E2E_DCGM_VERSION"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("E2E_DCGM_VERSION", previous)
		} else {
			_ = os.Unsetenv("E2E_DCGM_VERSION")
		}
	})
	t.Setenv("DCGM_VERSION", "4.5.3")
	var stderr bytes.Buffer
	opts, err := parseTests(nil, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Dependencies.DCGMVersion != "4.5.3" {
		t.Fatalf("DCGM version = %q, want DCGM_VERSION fallback", opts.Dependencies.DCGMVersion)
	}
}

func TestImageFlagUsesConfiguredDefault(t *testing.T) {
	previous, existed := os.LookupEnv("E2E_EXPORTER_IMAGE")
	if err := os.Unsetenv("E2E_EXPORTER_IMAGE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("E2E_EXPORTER_IMAGE", previous)
		} else {
			_ = os.Unsetenv("E2E_EXPORTER_IMAGE")
		}
	})
	opts := defaultTestsConfig()
	opts.ExporterImage = "default/image:tag"
	app := urfavecli.NewApp()
	app.Writer = io.Discard
	app.ErrWriter = io.Discard
	app.HideHelp = true
	app.Commands = []*urfavecli.Command{newTestsCLICommand(&opts)}
	if err := app.Run([]string{"e2e", "tests"}); err != nil {
		t.Fatal(err)
	}
	if opts.ExporterImage != "default/image:tag" {
		t.Fatalf("ExporterImage = %q, want configured default", opts.ExporterImage)
	}
}

func TestDefaultMIGRunSplitsK8sScenarios(t *testing.T) {
	if !splitDefaultMIGRun(config.Tests{MIGConfigure: "true"}) {
		t.Fatal("default MIG run should split k8s phases")
	}
	if splitDefaultMIGRun(config.Tests{MIGConfigure: "auto"}) {
		t.Fatal("auto MIG run should not split k8s phases")
	}
	if splitDefaultMIGRun(config.Tests{MIGConfigure: "true", Scenarios: []string{"k8s/mig"}}) {
		t.Fatal("focused scenario run should not split k8s phases")
	}

	nonMIG := map[string]struct{}{}
	for _, selector := range nonMIGScenarioSelectors() {
		nonMIG[selector] = struct{}{}
	}
	for _, selector := range migScenarioSelectors() {
		if _, ok := nonMIG[selector]; ok {
			t.Fatalf("MIG selector %s also appears in non-MIG selectors", selector)
		}
	}
}

func TestMakefileExposesOnlyPublicE2EWorkflows(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(makefile)
	for _, want := range []string{
		"build-e2e: ## Build the e2e validation CLI",
		"test-e2e: build-e2e",
		"-o bin/e2e",
		"-o bin/dcgm-exporter ./cmd/dcgm-exporter",
		"$(MAKE) local CONTAINER=distroless IMAGE_REF=\"$${base_image}\"",
		"E2E_EXPORTER_IMAGE=\"$${final_image}\" ./bin/e2e tests",
		"docker image inspect --format '{{.Id}}'",
		"-local-$(E2E_LOCAL_COMMIT)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Makefile missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"build-e2e-cli-artifacts:",
		"build-e2e-product:",
		"build-e2e-artifacts:",
		"test-e2e-static:",
		"test-e2e-host:",
		"test-e2e-container:",
		"test-e2e-k8s:",
		"test-e2e-run:",
		"test-e2e-cli:",
		"e2e-k3d-up:",
		"package-e2e:",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("Makefile unexpectedly exposes %q", unwanted)
		}
	}
}

func TestRunStaticSuiteExplicitlyEnablesMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tests", "static"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{"tests", "--suite", "static", "--scenario", "static/paths", "--result-markers"}, &stdout, &stderr, root, &fakeRunner{}); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	for _, want := range []string{
		"&&&& RUNNING dcgm_exporter_e2e_static",
		"&&&& PASSED dcgm_exporter_e2e_static",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("static suite missing marker %q:\n%s", want, stdout.String())
		}
	}
}

func TestMarkerEnvironmentCanDisableMarkers(t *testing.T) {
	t.Setenv("E2E_RESULT_MARKERS", "false")
	var stderr bytes.Buffer
	opts, err := parseTests(nil, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if opts.ResultMarkers || opts.E2EResultMarkers {
		t.Fatalf("markers = (%t, %t), want explicit environment override off", opts.ResultMarkers, opts.E2EResultMarkers)
	}
}

func TestMarkerOverridePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		resultEnv  string
		suiteEnv   string
		args       []string
		wantResult bool
		wantSuite  bool
	}{
		{
			name:       "positive CLI overrides disabled environment",
			resultEnv:  "false",
			suiteEnv:   "false",
			args:       []string{"--result-markers=true"},
			wantResult: true,
			wantSuite:  true,
		},
		{
			name:      "negative CLI overrides enabled environment",
			resultEnv: "true",
			suiteEnv:  "true",
			args:      []string{"--no-result-markers"},
		},
		{
			name:       "suite environment independently disables per-spec markers",
			resultEnv:  "true",
			suiteEnv:   "false",
			wantResult: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("E2E_RESULT_MARKERS", test.resultEnv)
			t.Setenv("E2E_SUITE_RESULT_MARKERS", test.suiteEnv)
			var stderr bytes.Buffer
			opts, err := parseTests(test.args, &stderr)
			if err != nil {
				t.Fatalf("parseTests() error = %v", err)
			}
			if opts.ResultMarkers != test.wantResult || opts.E2EResultMarkers != test.wantSuite {
				t.Fatalf("markers = (%t, %t), want (%t, %t)", opts.ResultMarkers, opts.E2EResultMarkers, test.wantResult, test.wantSuite)
			}
		})
	}
}

func TestResultMarkersFalseDisablesLifecycleAndSpecMarkers(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseTests([]string{"--result-markers=false"}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if opts.ResultMarkers || opts.E2EResultMarkers {
		t.Fatalf("markers = (%t, %t), want explicit flag off", opts.ResultMarkers, opts.E2EResultMarkers)
	}
}

func TestPrebuiltRunSuppressesMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-static.test")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{"tests", "--suite", "static", "--scenario", "static/paths", "--no-result-markers"}, &stdout, &stderr, root, &fakeRunner{}); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "&&&&") {
		t.Fatalf("prebuilt run emitted disabled markers:\n%s", stdout.String())
	}
}

func TestRunStaticSuiteDefaultsMarkersOff(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tests", "static"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{"tests", "--suite", "static", "--scenario", "static/paths"}, &stdout, &stderr, root, &fakeRunner{}); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "&&&&") {
		t.Fatalf("default run emitted result markers:\n%s", stdout.String())
	}
}

// TestSpecMarkersEnabled verifies --no-result-markers also disables Ginkgo markers.
func TestSpecMarkersEnabled(t *testing.T) {
	tests := []struct {
		name string
		opts config.Tests
		want bool
	}{
		{name: "enabled", opts: config.Tests{E2EResultMarkers: true}, want: true},
		{name: "suite markers disabled", opts: config.Tests{}, want: false},
		{name: "all markers disabled", opts: config.Tests{E2EResultMarkers: true, NoResultMarkers: true}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := specMarkersEnabled(test.opts); got != test.want {
				t.Fatalf("specMarkersEnabled() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRunMarkedSuiteEmitsSkippedTerminalMarker(t *testing.T) {
	var stdout bytes.Buffer
	err := runMarkedSuite(&stdout, config.Tests{ResultMarkers: true}, "dcgm_exporter_e2e_skipped", func() error {
		return errSuiteSkipped
	})
	if err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	if !strings.Contains(output, "&&&& RUNNING dcgm_exporter_e2e_skipped") ||
		!strings.Contains(output, "&&&& SKIPPED dcgm_exporter_e2e_skipped") {
		t.Fatalf("missing skipped markers:\n%s", output)
	}
	if strings.Contains(output, "&&&& PASSED dcgm_exporter_e2e_skipped") {
		t.Fatalf("skipped suite emitted passed marker:\n%s", output)
	}
}

func TestRunHostSuiteUsesPackageAssets(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "tests", "host", "testdata"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configurePackageTestEnvironment(t)
	for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter"} {
		if err := writeExecutableFile(filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--install-deps=false", "--result-markers"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
	if !ok {
		t.Fatalf("host binary was not run; commands = %#v", runner.commands)
	}
	if want := filepath.Join(root, "tests", "host"); hostCommand.Dir != want {
		t.Fatalf("host command dir = %q, want %q", hostCommand.Dir, want)
	}
	if want := "-exporter-binary=" + filepath.Join(root, "bin", "dcgm-exporter"); !hasArg(hostCommand.Args, want) {
		t.Fatalf("host command missing %q: %#v", want, hostCommand.Args)
	}
	if !hasArg(hostCommand.Args, "--ginkgo.label-filter=startupMetrics") {
		t.Fatalf("host command missing label filter: %#v", hostCommand.Args)
	}
	if !hasArg(hostCommand.Env, "E2E_SUITE_RESULT_MARKERS=true") {
		t.Fatalf("host command missing per-spec result markers: %#v", hostCommand.Env)
	}
	for _, want := range []string{
		"[e2e] === Host suite ===",
		"[e2e] === Host scenarios ===",
		"&&&& RUNNING dcgm_exporter_e2e_integration_host",
		"&&&& PASSED dcgm_exporter_e2e_integration_host",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("host suite missing marker %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunHostSuitePassesNVMLInjectionInputs(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "etc"), filepath.Join(root, "tests", "host", "testdata")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter", "e2e-dcgm-probe"} {
		if err := writeExecutableFile(filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	fixture := filepath.Join(root, "fixture.yaml")
	if err := os.WriteFile(fixture, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fields := filepath.Join(root, "etc", "go-dcgm-const-fields.go")
	if err := os.WriteFile(fields, []byte("package dcgm\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{
		"tests", "--suite", "host", "--scenario", "host/nvmlInjectionMetrics", "--install-deps=false",
		"--dcgm-nvml-injection-yaml", fixture,
	}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
	if !ok {
		t.Fatalf("host binary was not run; commands = %#v", runner.commands)
	}
	for _, want := range []string{
		"NVML_INJECTION_MODE=True",
		"NVML_YAML_FILE=" + fixture,
	} {
		if !hasArg(hostCommand.Env, want) {
			t.Fatalf("host command missing environment %q: %#v", want, hostCommand.Env)
		}
	}
	if !hasArg(hostCommand.Args, "--ginkgo.label-filter=(nvmlInjectionMetrics) && !dcgmUri && !profiling") {
		t.Fatalf("host command missing injection-safe label filter: %#v", hostCommand.Args)
	}
	for _, want := range []string{
		"-dcgm-probe-binary=" + filepath.Join(root, "bin", "e2e-dcgm-probe"),
		"-dcgm-fields-file=" + fields,
	} {
		if !hasArg(hostCommand.Args, want) {
			t.Fatalf("host command missing %q: %#v", want, hostCommand.Args)
		}
	}
}

func TestRunHostSuiteMissingDCGMSkipsImplicitHost(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c ldconfig -p 2>/dev/null | grep -Eq 'libdcgm\\.so(\\.4)?' || test -e /usr/lib/x86_64-linux-gnu/libdcgm.so.4 || test -e /usr/lib/aarch64-linux-gnu/libdcgm.so.4": {ExitCode: 1},
	}}
	err := runHostSuite(context.Background(), &stdout, suite.NewManager(root, runner), runner, config.Config{Tests: config.Tests{ResultMarkers: true}})
	if err != nil {
		t.Fatalf("runHostSuite() error = %v, want nil skipped suite", err)
	}
	if !strings.Contains(stdout.String(), "&&&& SKIPPED dcgm_exporter_e2e_integration_host") {
		t.Fatalf("missing skipped marker:\n%s", stdout.String())
	}
}

func TestRunHostSuiteMissingDCGMFailsExplicitHost(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c ldconfig -p 2>/dev/null | grep -Eq 'libdcgm\\.so(\\.4)?' || test -e /usr/lib/x86_64-linux-gnu/libdcgm.so.4 || test -e /usr/lib/aarch64-linux-gnu/libdcgm.so.4": {ExitCode: 1},
	}}
	err := runHostSuite(context.Background(), &stdout, suite.NewManager(root, runner), runner, config.Config{Tests: config.Tests{Suites: []string{"host"}, ResultMarkers: true}})
	if err == nil || !strings.Contains(err.Error(), "explicit host validation cannot run") {
		t.Fatalf("runHostSuite() error = %v, want explicit host failure", err)
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_integration_host") {
		t.Fatalf("missing failed marker:\n%s", stdout.String())
	}
}

func TestRunHostSuiteSplitsDCGMURIWithVSOCKEnv(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "tests", "host", "testdata"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configurePackageTestEnvironment(t)
	for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter"} {
		if err := writeExecutableFile(filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{"tests", "--suite", "host", "--scenario", "host/dcgmUri", "--install-deps=false", "--result-markers"}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	uriRuns := 0
	for _, command := range runner.commands {
		if !strings.HasSuffix(command.Name, "dcgm-exporter-host.test") {
			continue
		}
		if hasArg(command.Args, "--ginkgo.label-filter=dcgmUri") {
			uriRuns++
			if !hasArg(command.Env, "E2E_REQUIRE_VSOCK=1") {
				t.Fatalf("dcgmUri host command missing E2E_REQUIRE_VSOCK=1: %#v", command)
			}
			if !hasArg(command.Env, "E2E_REQUIRE_DCGM=1") {
				t.Fatalf("dcgmUri host command missing E2E_REQUIRE_DCGM=1: %#v", command)
			}
			if !hasArg(command.Env, "E2E_SUITE_RESULT_MARKERS=true") {
				t.Fatalf("dcgmUri host command missing per-spec result markers: %#v", command)
			}
		}
	}
	if uriRuns != 1 {
		t.Fatalf("dcgmUri host runs = %d, want 1; commands = %#v", uriRuns, runner.commands)
	}
	if !strings.Contains(stdout.String(), "[e2e] === Host DCGM URI scenarios ===") {
		t.Fatalf("host output missing DCGM URI section:\n%s", stdout.String())
	}
}

func TestRunHostSuiteRequiresProductBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tests", "host"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--install-deps=false"}, &stdout, &stderr, root, &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), "dcgm-exporter binary is missing") {
		t.Fatalf("runWithRootRunner() error = %v, want missing exporter binary", err)
	}
}

func TestRunContainerSuiteUsesEnvironmentImages(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-container.test")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("E2E_EXPORTER_IMAGE", "registry.example/dcgm-exporter:dev-distroless-amd64@"+testImageDigest)
	t.Setenv("E2E_EXPORTER_UBUNTU_IMAGE", "registry.example/dcgm-exporter:dev-ubuntu24.04-amd64@"+testImageDigest)
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loginFile := filepath.Join(t.TempDir(), "login")
	if err := os.WriteFile(loginFile, []byte("registry.example,user,"+passwordFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "container", "--scenario", "container/imageStartup", "--docker-registry-login-file", loginFile, "--result-markers"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	containerCommand, ok := runner.commandNamedSuffix("dcgm-exporter-container.test")
	if !ok {
		t.Fatalf("container binary was not run; commands = %#v", runner.commands)
	}
	for _, want := range []string{
		"E2E_REQUIRE_CONTAINER_IMAGES=1",
		"E2E_SUITE_RESULT_MARKERS=true",
		"EXPORTER_UBUNTU_IMAGE=registry.example/dcgm-exporter@" + testImageDigest,
		"EXPORTER_DISTROLESS_IMAGE=registry.example/dcgm-exporter@" + testImageDigest,
	} {
		if !hasArg(containerCommand.Env, want) {
			t.Fatalf("container command missing env %q: %#v", want, containerCommand.Env)
		}
	}
	if !runner.hasCommandArg("--password-stdin") {
		t.Fatalf("container suite did not log in to Docker registry: %#v", runner.commands)
	}
	if !hasEnvPrefix(containerCommand.Env, "DOCKER_CONFIG=") {
		t.Fatalf("container command missing DOCKER_CONFIG env: %#v", containerCommand.Env)
	}
	for _, want := range []string{
		"[e2e] === Container suite ===",
		"[e2e] === Container scenarios ===",
		"&&&& RUNNING dcgm_exporter_e2e_integration_container",
		"&&&& PASSED dcgm_exporter_e2e_integration_container",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("container suite missing marker %q:\n%s", want, stdout.String())
		}
	}
	for _, unexpected := range []string{
		"dcgm_exporter_e2e_static",
		"dcgm_exporter_e2e_integration_host",
	} {
		if strings.Contains(stdout.String(), unexpected) {
			t.Fatalf("container-only run emitted unselected marker %q:\n%s", unexpected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "secret-password") {
		t.Fatalf("password leaked to stdout:\n%s", stdout.String())
	}
}

func TestRunContainerSuiteUsesProductionDCGMImage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	configurePackageTestEnvironment(t)
	if err := writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-container.test")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "container", "--scenario", "container/remoteDcgmUri", "--exporter-image", "registry.example/dcgm-exporter:dev"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	containerCommand, ok := runner.commandNamedSuffix("dcgm-exporter-container.test")
	if !ok {
		t.Fatalf("container binary was not run; commands = %#v", runner.commands)
	}
	if want := "DCGM_IMAGE=nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"; !hasArg(containerCommand.Env, want) {
		t.Fatalf("container command missing env %q: %#v", want, containerCommand.Env)
	}
}

func TestRunContainerSuiteFailsWhenDCGMImageUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	configurePackageTestEnvironment(t)
	if err := writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-container.test")); err != nil {
		t.Fatal(err)
	}

	production := "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker manifest inspect " + production: {ExitCode: 1, Stderr: []byte("no such manifest")},
		"docker image inspect " + production:    {ExitCode: 1, Stderr: []byte("no such image")},
	}}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "container", "--scenario", "container/remoteDcgmUri", "--exporter-image", "registry.example/dcgm-exporter:dev"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err == nil || !strings.Contains(err.Error(), "selected DCGM-probed scenarios require accessible DCGM image "+production) {
		t.Fatalf("runWithRootRunner() error = %v, want unavailable-image failure", err)
	}
	if _, ok := runner.commandNamedSuffix("dcgm-exporter-container.test"); ok {
		t.Fatalf("container binary must not run when the DCGM image is unavailable; commands = %#v", runner.commands)
	}
}

func TestRunContainerSuiteImageStartupDoesNotResolveDCGMImage(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	configurePackageTestEnvironment(t)
	if err := writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-container.test")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "container", "--scenario", "container/imageStartup", "--exporter-image", "registry.example/dcgm-exporter:dev"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	containerCommand, ok := runner.commandNamedSuffix("dcgm-exporter-container.test")
	if !ok {
		t.Fatalf("container binary was not run; commands = %#v", runner.commands)
	}
	for _, env := range containerCommand.Env {
		if strings.HasPrefix(env, "DCGM_IMAGE=") {
			t.Fatalf("imageStartup should not receive DCGM_IMAGE: %#v", containerCommand.Env)
		}
	}
	if runner.hasCommandArg("manifest") {
		t.Fatalf("imageStartup should not inspect DCGM image manifests; commands = %#v", runner.commands)
	}
}

func TestRunBuildOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(root, "tests", "static"),
		filepath.Join(root, "tests", "host"),
		filepath.Join(root, "tests", "container"),
		filepath.Join(root, "tests", "k8s"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{"tests", "--build-only"}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	for _, suffix := range []string{
		"dcgm-exporter-static.test",
		"dcgm-exporter-host.test",
		"dcgm-exporter-container.test",
		"dcgm-exporter-k8s.test",
	} {
		if !runner.hasCommand(suffix) {
			t.Fatalf("%s was not built; commands = %#v", suffix, runner.commands)
		}
	}
}

func TestRunK8sSuiteWithExternalKubeconfig(t *testing.T) {
	t.Setenv("E2E_MIG_INSTANCE_ENTITY_ID", "101")
	t.Setenv("E2E_MIG_INSTANCE_NVML_ID", "7")
	t.Setenv("E2E_UNSUPPORTED_FIELD_CANDIDATE", "DCGM_FI_DEV_RETIRED_PENDING")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tests", "k8s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: map[string][]byte{
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
		"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": []byte("0, NVIDIA Test GPU, GPU-test, 1.0, 9.0, [N/A], [N/A]\n"),
		"nvidia-smi topo -m":   []byte("GPU0 CPU Affinity NUMA Affinity GPU NUMA ID\n"),
		"nvidia-smi nvlink -s": []byte(""),
		"nvidia-smi -q":        []byte(""),
		"lscpu":                []byte("Architecture: x86_64\n"),
		"nvidia-smi mig -lgip": []byte("No MIG-supported devices found.\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}{\"\\n\"}{end}": []byte("1\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}":                       []byte("node-0\n"),
		"kubectl get nodes -o json":                                               []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}}]}` + "\n"),
		"kubectl get --raw /api/v1/nodes":                                         []byte("{}\n"),
		"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}": []byte("IPv4\n"),
	}}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--kubeconfig", "/tmp/kubeconfig", "--exporter-image", "registry.example/dcgm-exporter:dev", "--install-deps=false", "--result-markers"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "&&&& RUNNING dcgm_exporter_e2e_setup") {
		t.Fatalf("missing k8s setup marker:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "&&&& PASSED dcgm_exporter_e2e_setup") {
		t.Fatalf("missing passed k8s setup marker:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "&&&& RUNNING dcgm_exporter_e2e_group_embedded_baseline") {
		t.Fatalf("default suite marker mode should not emit duplicate group marker:\n%s", stdout.String())
	}
	for _, unexpected := range []string{
		"dcgm_exporter_e2e_static",
		"dcgm_exporter_e2e_integration_host",
		"dcgm_exporter_e2e_integration_container",
	} {
		if strings.Contains(stdout.String(), unexpected) {
			t.Fatalf("k8s-only run emitted unselected marker %q:\n%s", unexpected, stdout.String())
		}
	}
	if !runner.hasCommand("dcgm-exporter-k8s.test") {
		t.Fatalf("k8s binary was not run; commands = %#v", runner.commands)
	}
	if !runner.hasCommandArg("--ginkgo.label-filter=default") {
		t.Fatalf("k8s label filter missing; commands = %#v", runner.commands)
	}
	if !runner.hasCommandArg("-result-markers=true") {
		t.Fatalf("k8s result marker flag missing; commands = %#v", runner.commands)
	}
	if !runner.hasNamespaceCleanupLookup("dcgm-exporter") {
		t.Fatalf("external namespace cleanup was not attempted; commands = %#v", runner.commands)
	}
	for _, want := range []string{
		"-mig-instance-entity-id=101",
		"-mig-instance-nvml-id=7",
		"-unsupported-field-candidate=DCGM_FI_DEV_RETIRED_PENDING",
	} {
		if !runner.hasCommandArg(want) {
			t.Fatalf("k8s command missing %q; commands = %#v", want, runner.commands)
		}
	}
}

func TestRunK8sSuiteWithExternalKubeconfigCleansUpAfterFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tests", "k8s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{
		outputs: map[string][]byte{
			"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
			"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": []byte("0, NVIDIA Test GPU, GPU-test, 1.0, 9.0, [N/A], [N/A]\n"),
			"nvidia-smi topo -m":   []byte("GPU0 CPU Affinity NUMA Affinity GPU NUMA ID\n"),
			"nvidia-smi nvlink -s": []byte(""),
			"nvidia-smi -q":        []byte(""),
			"lscpu":                []byte("Architecture: x86_64\n"),
			"nvidia-smi mig -lgip": []byte("No MIG-supported devices found.\n"),
			"kubectl get nodes -o jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}{\"\\n\"}{end}": []byte("1\n"),
			"kubectl get nodes -o jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}":                       []byte("node-0\n"),
			"kubectl get nodes -o json":                                               []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}}]}` + "\n"),
			"kubectl get --raw /api/v1/nodes":                                         []byte("{}\n"),
			"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}": []byte("IPv4\n"),
		},
		results: map[string]e2eexec.Result{
			"dcgm-exporter-k8s.test": {ExitCode: 1, Stderr: []byte("suite failed\n")},
		},
	}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--kubeconfig", "/tmp/kubeconfig", "--exporter-image", "registry.example/dcgm-exporter:dev", "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err == nil {
		t.Fatal("runWithRootRunner() error = nil, want k8s suite failure")
	}
	if !runner.hasNamespaceCleanupLookup("dcgm-exporter") {
		t.Fatalf("external namespace cleanup was not attempted after failure; commands = %#v", runner.commands)
	}
}

func TestRunK8sSuiteLocalImportsDCGMImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tests", "k8s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: map[string][]byte{
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
		"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": []byte("0, NVIDIA Test GPU, GPU-test, 1.0, 9.0, [N/A], [N/A]\n"),
		"nvidia-smi topo -m":   []byte("GPU0 CPU Affinity NUMA Affinity GPU NUMA ID\n"),
		"nvidia-smi nvlink -s": []byte(""),
		"nvidia-smi -q":        []byte(""),
		"lscpu":                []byte("Architecture: x86_64\n"),
		"nvidia-smi mig -lgip": []byte("No MIG-supported devices found.\n"),
		"docker run --rm --entrypoint /usr/bin/nv-hostengine dcgm:test --version":                             []byte("Version : 4.5.3\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}{\"\\n\"}{end}": []byte("1\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}":                       []byte("node-0\n"),
		"kubectl get nodes -o json":                                               []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}}]}` + "\n"),
		"kubectl get --raw /api/v1/nodes":                                         []byte("{}\n"),
		"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}": []byte("IPv4\n"),
	}}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/remoteDcgm", "--exporter-image", "registry.example/dcgm-exporter:dev", "--dcgm-image", "dcgm:test", "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	if !runner.hasK3dImageImport("dcgm:test") {
		t.Fatalf("local k3d run did not import DCGM image; commands = %#v", runner.commands)
	}
}

func TestRunK8sSuiteLocalUsesTaggedExporterImageInsideK3d(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tests", "k8s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: fakeK8sProbeOutputs()}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--exporter-image", "registry.example/dcgm-exporter:dev@" + testImageDigest, "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	if !runner.hasDockerTag("registry.example/dcgm-exporter@"+testImageDigest, "registry.example/dcgm-exporter:dev") {
		t.Fatalf("local k3d run did not tag digest image for local import; commands = %#v", runner.commands)
	}
	if !runner.hasK3dImageImport("registry.example/dcgm-exporter:dev") {
		t.Fatalf("local k3d run did not import tagged exporter image; commands = %#v", runner.commands)
	}
	k8sCommand, ok := runner.commandNamedSuffix("dcgm-exporter-k8s.test")
	if !ok {
		t.Fatalf("k8s test binary was not invoked; commands = %#v", runner.commands)
	}
	if !hasArg(k8sCommand.Args, "-exporter-image=registry.example/dcgm-exporter:dev") {
		t.Fatalf("local k3d k8s command did not use the tagged cluster-local image: %#v", k8sCommand.Args)
	}
}

func TestRunK8sSuiteBaselineDoesNotResolveDCGMImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tests", "k8s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: fakeK8sProbeOutputs()}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--exporter-image", "registry.example/dcgm-exporter:dev", "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if runner.hasCommandArg("manifest") || runner.hasK3dImageImport("nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04") {
		t.Fatalf("baseline scenario should not resolve or import the DCGM image; commands = %#v", runner.commands)
	}
}

func TestPrepareK8sGroupRecreatesDCGMPullSecret(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dockerConfig := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := configWithRegistryLogin("nvcr.io,$oauthtoken," + passwordFile)
	opts.DockerConfigDir = dockerConfig
	opts.DCGMImage = "registry.example/dcgm:4.5.3-1-ubuntu24.04"
	clusterCfg := clusterConfig(t.TempDir(), opts)
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &fakeRunner{}

	cleanup, err := prepareK8sGroup(context.Background(), io.Discard, runner, t.TempDir(), clusterCfg, opts, scenario.PlanGroup{Name: "standalone-failure-injection"})
	if err != nil {
		t.Fatalf("prepareK8sGroup() error = %v", err)
	}
	cleanup(context.Background())

	secretIndex := runner.commandIndex("kubectl", "-n", clusterCfg.DCGMNamespace, "create", "secret", "generic", "dcgm-exporter-e2e-registry-auth")
	applyIndex := runner.commandLastIndex("kubectl", "apply", "-f", "-")
	if secretIndex == -1 {
		t.Fatalf("DCGM pull secret was not recreated; commands = %#v", runner.commands)
	}
	if applyIndex == -1 {
		t.Fatalf("DCGM manifest was not applied; commands = %#v", runner.commands)
	}
	if secretIndex > applyIndex {
		t.Fatalf("DCGM pull secret was created after standalone DCGM deploy; commands = %#v", runner.commands)
	}
}

func TestDryRunK8sBaselineDoesNotResolveDCGMImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: fakeK8sProbeOutputs()}
	err := runWithRootRunner(
		[]string{"tests", "--dry-run", "--suite", "k8s", "--scenario", "k8s/default", "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if runner.hasCommandArg("manifest") {
		t.Fatalf("baseline dry-run should not inspect the DCGM image; commands = %#v", runner.commands)
	}
	if strings.Contains(stdout.String(), "dry-run verified the DCGM image is accessible") {
		t.Fatalf("baseline dry-run should not claim the DCGM image was checked:\n%s", stdout.String())
	}
}

func TestRunK8sSuiteHardwareProbeResolvesDCGMImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	if err := os.MkdirAll(filepath.Join(root, "tests", "k8s"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0o755); err != nil {
		t.Fatal(err)
	}
	production := "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: fakeK8sProbeOutputs()}
	_ = runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/profiling", "--exporter-image", "registry.example/dcgm-exporter:dev", "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if !runner.hasCommandArg("manifest") || !runner.hasCommandArg(production) {
		t.Fatalf("hardware-probe scenario should probe the production DCGM image; commands = %#v", runner.commands)
	}
}

func TestDryRunK8sHardwareProbeResolvesDCGMImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)

	production := "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: fakeK8sProbeOutputs()}
	err := runWithRootRunner(
		[]string{"tests", "--dry-run", "--suite", "k8s", "--scenario", "k8s/profiling", "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("manifest") || !runner.hasCommandArg(production) {
		t.Fatalf("hardware-probe dry-run should check the production DCGM image; commands = %#v", runner.commands)
	}
	if !strings.Contains(stdout.String(), "dry-run verified the DCGM image is accessible") {
		t.Fatalf("hardware-probe dry-run should report the scoped DCGM image check:\n%s", stdout.String())
	}
}

func TestDryRunK8sHardwareProbeFailsWhenDCGMImageUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)

	production := "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{
		outputs: fakeK8sProbeOutputs(),
		results: map[string]e2eexec.Result{
			"docker manifest inspect " + production: {ExitCode: 1, Stderr: []byte("no such manifest")},
			"docker image inspect " + production:    {ExitCode: 1, Stderr: []byte("no such image")},
		},
	}
	err := runWithRootRunner(
		[]string{"tests", "--dry-run", "--suite", "k8s", "--scenario", "k8s/profiling", "--install-deps=false"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err == nil || !strings.Contains(err.Error(), "selected DCGM-probed scenarios require accessible DCGM image "+production) {
		t.Fatalf("runWithRootRunner() error = %v, want unavailable-image failure", err)
	}
}

func TestRunK8sGroupPrepareFailureEmitsGroupFailedMarker(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	var stdout bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"helm repo add nvidia-device-plugin https://nvidia.github.io/k8s-device-plugin --force-update": {
			ExitCode: 1,
			Stderr:   []byte("repo failed"),
		},
	}}
	err := runK8sGroup(
		context.Background(),
		&stdout,
		runner,
		"/tmp/dcgm-exporter-k8s.test",
		root,
		clusterConfig(root, config.Tests{}),
		config.Tests{},
		scenario.PlanGroup{Name: "embedded-mig", Labels: []string{"mig"}},
		true,
		true,
	)
	if err == nil {
		t.Fatal("runK8sGroup() error = nil, want prepare failure")
	}
	if !strings.Contains(stdout.String(), "&&&& RUNNING dcgm_exporter_e2e_group_embedded_mig") {
		t.Fatalf("missing group running marker:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_group_embedded_mig") {
		t.Fatalf("missing group failed marker:\n%s", stdout.String())
	}
	if diagnostics := runner.commandIndex("kubectl", "describe", "nodes"); diagnostics < 0 {
		t.Fatalf("missing prepare-failure diagnostics: %#v", runner.commands)
	}

	stdout.Reset()
	err = runK8sGroup(
		context.Background(),
		&stdout,
		runner,
		"/tmp/dcgm-exporter-k8s.test",
		root,
		clusterConfig(root, config.Tests{}),
		config.Tests{},
		scenario.PlanGroup{Name: "embedded-mig", Labels: []string{"mig"}},
		true,
		false,
	)
	if err == nil {
		t.Fatal("runK8sGroup() error = nil, want prepare failure")
	}
	if got := strings.Count(stdout.String(), "&&&& RUNNING dcgm_exporter_e2e_group_embedded_mig"); got != 1 {
		t.Fatalf("group RUNNING marker count = %d, want 1:\n%s", got, stdout.String())
	}
}

func TestRunK8sGroupFailureRunsDiagnosticsBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"dcgm-exporter-k8s.test": {ExitCode: 1, Stderr: []byte("suite failed")},
	}}
	err = runK8sGroup(
		context.Background(),
		&stdout,
		runner,
		"/tmp/dcgm-exporter-k8s.test",
		root,
		clusterConfig(root, opts),
		opts,
		scenario.PlanGroup{Name: "embedded-shared-gpu", Labels: []string{"sharedGpu"}},
		true,
		true,
	)
	if err == nil {
		t.Fatal("runK8sGroup() error = nil, want suite failure")
	}
	diagnostics := runner.commandIndex("kubectl", "describe", "nodes")
	cleanup := runner.commandLastIndex("helm", "upgrade", "--install", "nvidia-device-plugin")
	if diagnostics < 0 {
		t.Fatalf("missing diagnostic node describe: %#v", runner.commands)
	}
	if cleanup < 0 {
		t.Fatalf("missing cleanup device-plugin reset: %#v", runner.commands)
	}
	if diagnostics > cleanup {
		t.Fatalf("diagnostics ran after cleanup: diagnostics=%d cleanup=%d commands=%#v", diagnostics, cleanup, runner.commands)
	}
}

func TestEmitSkippedScenarioMarkersUsesPreExecutionLifecycle(t *testing.T) {
	var nvlink scenario.Scenario
	found := false
	for _, entry := range scenario.Catalog {
		if entry.Selector() == "k8s/nvlink" {
			nvlink = entry
			found = true
			break
		}
	}
	if !found {
		t.Fatal("k8s/nvlink scenario missing")
	}
	name, ok := nvlink.MarkerBaseName()
	if !ok {
		t.Fatal("k8s/nvlink marker name missing")
	}
	var stdout bytes.Buffer
	err := emitSkippedScenarioMarkers(marker.NewReporter(&stdout), scenario.Plan{
		Scenarios: []scenario.PlannedScenario{{
			Scenario: nvlink,
			Outcome:  scenario.OutcomeSkipped,
			Reason:   "active NVLink evidence was not detected",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "&&&& RUNNING " + name + "\n&&&& SKIPPED " + name + "\n"
	if stdout.String() != want {
		t.Fatalf("skipped marker = %q, want %q", stdout.String(), want)
	}
	if strings.Contains(stdout.String(), "WAIVED") {
		t.Fatalf("pre-execution skip used WAIVED:\n%s", stdout.String())
	}
}

func TestPrepareRegistryAuthLogsInAndCreatesPullSecret(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dockerConfig := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerConfig, "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := configWithRegistryLogin("registry.example,user," + passwordFile)
	opts.DockerConfigDir = dockerConfig
	opts.GPUOperator = "install"
	clusterCfg := clusterConfig(t.TempDir(), opts)
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &fakeRunner{}
	var stdout bytes.Buffer
	if err := prepareRegistryAuth(context.Background(), &stdout, runner, clusterCfg, &opts); err != nil {
		t.Fatal(err)
	}

	if opts.K8sImagePullSecret != "dcgm-exporter-e2e-registry-auth" {
		t.Fatalf("pull secret = %q", opts.K8sImagePullSecret)
	}
	if !runner.hasCommandArg("--password-stdin") {
		t.Fatalf("docker login did not use password stdin: %#v", runner.commands)
	}
	if !runner.hasCommandArg("--from-file=.dockerconfigjson=" + filepath.Join(dockerConfig, "config.json")) {
		t.Fatalf("pull secret did not use Docker config: %#v", runner.commands)
	}
	if _, ok := runner.commandWithArgs("kubectl", "-n", "gpu-operator", "create", "secret", "generic", "dcgm-exporter-e2e-registry-auth"); !ok {
		t.Fatalf("GPU Operator namespace pull secret was not created: %#v", runner.commands)
	}
	if strings.Contains(stdout.String(), "secret-password") {
		t.Fatalf("password leaked to stdout:\n%s", stdout.String())
	}
}

func TestPrepareRegistryAuthUsesTemporaryDockerConfig(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := configWithRegistryLogin("registry.example,user," + passwordFile)
	clusterCfg := clusterConfig(t.TempDir(), opts)
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &fakeRunner{}
	var stdout bytes.Buffer
	if err := prepareRegistryAuthForNamespaces(context.Background(), &stdout, runner, clusterCfg, &opts, []string{clusterCfg.Namespace}); err != nil {
		t.Fatal(err)
	}
	var dockerConfigDir string
	for _, command := range runner.commands {
		if command.Name != "docker" {
			continue
		}
		for _, env := range command.Env {
			if strings.HasPrefix(env, "DOCKER_CONFIG=") {
				dockerConfigDir = strings.TrimPrefix(env, "DOCKER_CONFIG=")
				break
			}
		}
	}
	if dockerConfigDir == "" {
		t.Fatalf("docker login did not use isolated DOCKER_CONFIG: %#v", runner.commands)
	}
	if _, err := os.Stat(dockerConfigDir); !os.IsNotExist(err) {
		t.Fatalf("temporary Docker config still exists at %s: %v", dockerConfigDir, err)
	}
	if opts.DockerConfigDir != "" {
		t.Fatalf("DockerConfigDir = %q, want restored empty", opts.DockerConfigDir)
	}
}

func TestPrepareRegistryAuthRejectsUnmanagedNamespaceBeforeLogin(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := configWithRegistryLogin("registry.example,user," + passwordFile)
	clusterCfg := clusterConfig(t.TempDir(), opts)
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"kubectl get namespace dcgm-exporter -o jsonpath={.metadata.labels.app\\.kubernetes\\.io/managed-by}": {Stdout: []byte("")},
	}}
	var stdout bytes.Buffer
	err := prepareRegistryAuthForNamespaces(context.Background(), &stdout, runner, clusterCfg, &opts, []string{clusterCfg.Namespace})
	if err == nil || !strings.Contains(err.Error(), "already exists without app.kubernetes.io/managed-by=dcgm-exporter-e2e") {
		t.Fatalf("prepareRegistryAuthForNamespaces() error = %v, want unmanaged namespace", err)
	}
	for _, command := range runner.commands {
		if command.Name == "docker" {
			t.Fatalf("docker login ran before namespace ownership validation: %#v", runner.commands)
		}
	}
}

func TestRunClusterStatusDerivesDCGMNamespaceFromHelmNamespace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRunner([]string{"cluster", "status", "--kubeconfig", "/tmp/kubeconfig", "--helm-namespace", "custom"}, &stdout, &stderr, runner)
	if err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("custom-dcgm") {
		t.Fatalf("cluster status did not derive DCGM namespace; commands = %#v", runner.commands)
	}
}

func TestRunClusterStatusKeepsExplicitDCGMNamespace(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRunner([]string{"cluster", "status", "--kubeconfig", "/tmp/kubeconfig", "--helm-namespace", "custom", "--dcgm-namespace", "explicit"}, &stdout, &stderr, runner)
	if err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("explicit") {
		t.Fatalf("cluster status did not keep explicit DCGM namespace; commands = %#v", runner.commands)
	}
}

func TestRunCleanupUsesClusterName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: map[string][]byte{"k3d cluster list": []byte("test-cluster 1/1 1/1\n")}}
	err := runWithRunner([]string{"cluster", "cleanup", "--k3d-cluster-name", "test-cluster"}, &stdout, &stderr, runner)
	if err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("test-cluster") {
		t.Fatalf("cleanup did not use cluster name; commands = %#v", runner.commands)
	}
}

func TestRunCleanupUsesClusterNameEnv(t *testing.T) {
	t.Setenv("E2E_K3D_CLUSTER_NAME", "env-cluster")
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: map[string][]byte{"k3d cluster list": []byte("env-cluster 1/1 1/1\n")}}
	err := runWithRunner([]string{"cluster", "cleanup"}, &stdout, &stderr, runner)
	if err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("env-cluster") {
		t.Fatalf("cleanup did not use env cluster name; commands = %#v", runner.commands)
	}
}

func configWithRegistryLogin(login string) config.Tests {
	return config.Tests{DockerRegistryLogins: []string{login}}
}

func configurePackageTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("E2E_EXPORTER_IMAGE", "registry.example/dcgm-exporter:dev")
	t.Setenv("E2E_EXPORTER_UBUNTU_IMAGE", "registry.example/dcgm-exporter:dev-ubuntu24.04")
	t.Setenv("E2E_DCGM_IMAGE", "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04")
	t.Setenv("E2E_DCGM_VERSION", "4.5.3")
}

type fakeRunner struct {
	outputs         map[string][]byte
	results         map[string]e2eexec.Result
	resultSequences map[string][]e2eexec.Result
	downloads       map[string][]byte
	commands        []e2eexec.Command
	onRun           func(e2eexec.Command)
}

func (f *fakeRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	f.commands = append(f.commands, command)
	if f.onRun != nil {
		f.onRun(command)
	}
	key := commandKey(command)
	if f.resultSequences != nil {
		if results := f.resultSequences[key]; len(results) != 0 {
			result := results[0]
			f.resultSequences[key] = results[1:]
			return streamFakeResult(command, result)
		}
	}
	if f.results != nil {
		if result, ok := f.results[key]; ok {
			return streamFakeResult(command, result)
		}
		if result, ok := f.results[filepath.Base(command.Name)]; ok {
			return streamFakeResult(command, result)
		}
	}
	if command.Name == "kubectl" && len(command.Args) >= 3 && command.Args[0] == "get" && command.Args[1] == "namespace" {
		return e2eexec.Result{ExitCode: 1}
	}
	if command.Name == "go" {
		for index, arg := range command.Args {
			if arg == "-o" && index+1 < len(command.Args) {
				if err := writeExecutableFile(command.Args[index+1]); err != nil {
					return e2eexec.Result{ExitCode: 1, Err: err}
				}
			}
		}
		return e2eexec.Result{}
	}
	if command.Name == "curl" {
		if err := f.writeCurlOutput(command); err != nil {
			return e2eexec.Result{ExitCode: 1, Err: err}
		}
		return e2eexec.Result{}
	}
	if f.outputs != nil {
		return streamFakeResult(command, e2eexec.Result{Stdout: f.outputs[key]})
	}
	return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

func commandKey(command e2eexec.Command) string {
	return strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
}

func streamFakeResult(command e2eexec.Command, result e2eexec.Result) e2eexec.Result {
	if command.Stdout != nil && len(result.Stdout) != 0 {
		_, _ = command.Stdout.Write(result.Stdout)
	}
	if command.Stderr != nil && len(result.Stderr) != 0 {
		_, _ = command.Stderr.Write(result.Stderr)
	}
	return result
}

func (f *fakeRunner) writeCurlOutput(command e2eexec.Command) error {
	var url, dest string
	for index, arg := range command.Args {
		if strings.HasPrefix(arg, "http") {
			url = arg
		}
		if arg == "-o" && index+1 < len(command.Args) {
			dest = command.Args[index+1]
		}
	}
	if dest == "" {
		return nil
	}
	content := f.downloads[url]
	if content == nil {
		content = []byte("download:" + url)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, content, 0o600)
}

func (f *fakeRunner) hasCommand(suffix string) bool {
	_, ok := f.commandNamedSuffix(suffix)
	if ok {
		return true
	}
	for _, command := range f.commands {
		for _, arg := range command.Args {
			if strings.HasSuffix(arg, suffix) {
				return true
			}
		}
	}
	return false
}

func (f *fakeRunner) commandNamedSuffix(suffix string) (e2eexec.Command, bool) {
	for _, command := range f.commands {
		if strings.HasSuffix(command.Name, suffix) {
			return command, true
		}
	}
	return e2eexec.Command{}, false
}

func (f *fakeRunner) commandWithArgs(name string, args ...string) (e2eexec.Command, bool) {
	for _, command := range f.commands {
		if command.Name == name && containsArgs(command.Args, args...) {
			return command, true
		}
	}
	return e2eexec.Command{}, false
}

func (f *fakeRunner) hasCommandArg(value string) bool {
	for _, command := range f.commands {
		if hasArg(command.Args, value) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) hasCommandArgContaining(value string) bool {
	for _, command := range f.commands {
		for _, arg := range command.Args {
			if strings.Contains(arg, value) {
				return true
			}
		}
	}
	return false
}

func (f *fakeRunner) commandIndex(name string, args ...string) int {
	for index, command := range f.commands {
		if command.Name == name && containsArgs(command.Args, args...) {
			return index
		}
	}
	return -1
}

func (f *fakeRunner) commandLastIndex(name string, args ...string) int {
	last := -1
	for index, command := range f.commands {
		if command.Name == name && containsArgs(command.Args, args...) {
			last = index
		}
	}
	return last
}

func (f *fakeRunner) hasK3dImageImport(image string) bool {
	for _, command := range f.commands {
		if command.Name != "k3d" {
			continue
		}
		if hasArg(command.Args, "image") && hasArg(command.Args, "import") && hasArg(command.Args, image) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) hasDockerTag(source, target string) bool {
	for _, command := range f.commands {
		if command.Name == "docker" && containsArgs(command.Args, "tag", source, target) {
			return true
		}
	}
	return false
}

func (f *fakeRunner) hasNamespaceCleanupLookup(namespace string) bool {
	for _, command := range f.commands {
		if command.Name == "kubectl" &&
			len(command.Args) >= 4 &&
			command.Args[0] == "get" &&
			command.Args[1] == "namespace" &&
			command.Args[2] == namespace &&
			hasArg(command.Args, "--ignore-not-found=true") {
			return true
		}
	}
	return false
}

func containsArgs(haystack []string, needles ...string) bool {
	for _, needle := range needles {
		if !hasArg(haystack, needle) {
			return false
		}
	}
	return true
}

func hasArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}

func hasEnvPrefix(env []string, prefix string) bool {
	for _, value := range env {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
