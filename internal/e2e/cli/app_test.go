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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	urfavecli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/capability"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/clioutput"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/cluster"
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

// fakeK8sProbeOutputs supplies internally consistent host and owned-cluster evidence for local Kubernetes CLI tests.
// External-target tests use narrower fixtures so any accidental controller probe fails instead of borrowing these facts.
func fakeK8sProbeOutputs() map[string][]byte {
	return map[string][]byte{
		"nvidia-smi -L": []byte("GPU 0: NVIDIA Test GPU (UUID: GPU-test)\n"),
		"nvidia-smi --query-gpu=index,name,uuid,driver_version,compute_cap,mig.mode.current,mig.mode.pending --format=csv,noheader": []byte("0, NVIDIA Test GPU, GPU-test, 1.0, 9.0, [N/A], [N/A]\n"),
		"nvidia-smi topo -m":   []byte("GPU0 CPU Affinity NUMA Affinity GPU NUMA ID\n"),
		"nvidia-smi nvlink -s": []byte("Link 0: 25.781 GB/s\n"),
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

// newK8sGroupPreparationRun builds a Kubernetes suite run with the supplied test dependencies.
func newK8sGroupPreparationRun(ctx context.Context, stdout io.Writer, runner e2eexec.Runner, root string, clusterCfg cluster.Config) *k8sSuiteRun {
	return &k8sSuiteRun{
		ctx:        ctx,
		stdout:     stdout,
		runner:     runner,
		root:       root,
		clusterCfg: clusterCfg,
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
		"NVIDIA_CONTAINER_TOOLKIT_VERSION=1.19.1-1",
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
		"NVIDIA_CONTAINER_TOOLKIT_VERSION": "1.19.1-1",
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

func TestNewAppUsesOneUrfaveCommandTree(t *testing.T) {
	app := NewApp(Options{Root: t.TempDir(), Stdout: io.Discard, Stderr: io.Discard, Runner: &fakeRunner{}})
	if app.SkipFlagParsing || app.HideHelp {
		t.Fatalf("root urfave parsing disabled: SkipFlagParsing=%t HideHelp=%t", app.SkipFlagParsing, app.HideHelp)
	}
	tests := app.Command("tests")
	if tests == nil || tests.SkipFlagParsing || tests.HideHelp {
		t.Fatalf("tests command does not use native urfave parsing: %#v", tests)
	}
	clusterCommand := app.Command("cluster")
	if clusterCommand == nil || clusterCommand.SkipFlagParsing || clusterCommand.HideHelp {
		t.Fatalf("cluster command does not use native urfave parsing: %#v", clusterCommand)
	}
	for _, name := range []string{"check", "up", "deploy", "deploy-dcgm", "status", "logs", "cleanup"} {
		command := clusterCommand.Command(name)
		if command == nil || command.SkipFlagParsing || command.HideHelp {
			t.Fatalf("cluster command %q does not use native urfave parsing: %#v", name, command)
		}
	}
}

// TestResolveE2ERootFindsSourceAndPackageParents accepts nested directories in both layouts.
func TestResolveE2ERootFindsSourceAndPackageParents(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{name: "source", setup: func(t *testing.T, root string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600))
		}},
		{name: "package", setup: func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, "bin"), 0o755))
			require.NoError(t, writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-static.test")))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			start := filepath.Join(root, "tests", "host")
			require.NoError(t, os.MkdirAll(start, 0o755))
			got, err := resolveE2ERoot(start)
			require.NoError(t, err)
			if got != root {
				t.Fatalf("root = %q, want %q", got, root)
			}
		})
	}
}

func TestNewAppMarksRenderedUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"e2e", "--unknown"},
		{"e2e", "tests", "--unknown"},
		{"e2e", "cluster", "status", "--unknown"},
	} {
		t.Run(strings.Join(args[1:], "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := NewApp(Options{
				Root:   t.TempDir(),
				Stdout: &stdout,
				Stderr: &stderr,
				Runner: &fakeRunner{},
			})
			err := app.Run(args)
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("Run(%v) error = %v, want unknown flag", args, err)
			}
			clioutput.WriteError(&stderr, err)
			if got := strings.Count(stdout.String(), "flag provided but not defined"); got != 1 {
				t.Fatalf("Run(%v) rendered parser error %d times, want once:\n%s", args, got, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("Run(%v) stderr = %q, want empty", args, stderr.String())
			}
		})
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
		if !strings.Contains(stdout.String(), "USAGE:\n   e2e <command> [options]") || !strings.Contains(stdout.String(), "cluster") {
			t.Fatalf("root help missing usage for %v:\n%s", args, stdout.String())
		}
	}
}

// TestListScenariosVerbosePublishesDirectGoCommands verifies every catalog entry is source-runnable.
func TestListScenariosVerbosePublishesDirectGoCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner(
		[]string{"tests", "--list-scenarios", "--verbose"},
		&stdout,
		&stderr,
		t.TempDir(),
		&fakeRunner{},
	); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	output := stdout.String()
	if count := strings.Count(output, " go test "); count != len(scenario.Catalog) {
		t.Fatalf("direct Go commands = %d, want %d", count, len(scenario.Catalog))
	}
	for _, want := range []string{
		"static/chartRbacRendering go test ./tests/static",
		"CGO_ENABLED=0 go test --tags=dcgm_uri_integration ./tests/host",
		"go test --tags=container ./tests/container",
		"go test --tags=k8s ./tests/k8s",
		"--ginkgo.label-filter=startupMetrics",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("direct command output missing %q:\n%s", want, output)
		}
	}
}

// TestRunTestsHelpDoesNotValidateImages checks that help exits without validating or resolving container images.
func TestRunTestsHelpDoesNotValidateImages(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"tests", "--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error = %v; stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "USAGE:\n   e2e tests [options]") {
		t.Fatalf("help output missing usage:\n%s", stdout.String())
	}
	for _, want := range []string{"Dependencies", "Execution", "Feature setup", "Images and registry authentication", "Kubernetes", "E2E_VERBOSE", "E2E_RESULT_MARKERS"} {
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
			name: "configuration without explicit scenario",
			args: []string{"--suite", "host", "--dcgm-nvml-injection-yaml", "fixture.yaml"},
			want: "requires --scenario host/configServesMetrics or host/nvmlInjectionMetrics or host/migXidCounters",
		},
		{
			name: "configuration mixed with hardware scenario",
			args: []string{
				"--scenario", "host/nvmlInjectionMetrics",
				"--scenario", "host/startupMetrics",
				"--dcgm-nvml-injection-yaml", "fixture.yaml",
			},
			want: "which does not use --dcgm-nvml-injection-yaml",
		},
		{
			name: "injection scenario without YAML",
			args: []string{"--scenario", "host/nvmlInjectionMetrics"},
			want: "requires --dcgm-nvml-injection-yaml",
		},
		{
			name: "MIG counter scenario without YAML",
			args: []string{"--scenario", "host/migXidCounters"},
			want: "requires --dcgm-nvml-injection-yaml",
		},
		{
			name: "config scenario mixed with hardware scenario",
			args: []string{"--scenario", "host/configServesMetrics", "--scenario", "host/startupMetrics", "--dcgm-nvml-injection-yaml", "fixture.yaml"},
			want: "which does not use --dcgm-nvml-injection-yaml",
		},
		{
			name: "config scenario without YAML",
			args: []string{"--scenario", "host/configServesMetrics"},
			want: "requires --dcgm-nvml-injection-yaml",
		},
		{
			name: "configuration excluded by suite",
			args: []string{
				"--suite", "container",
				"--scenario", "host/nvmlInjectionMetrics",
				"--dcgm-nvml-injection-yaml", "fixture.yaml",
			},
			want: "requires the host suite",
		},
		{
			name: "optional scenario excluded by suite",
			args: []string{
				"--suite", "container",
				"--scenario", "host/configFile",
				"--dcgm-nvml-injection-yaml", "fixture.yaml",
			},
			want: "--dcgm-nvml-injection-yaml requires the host suite",
		},
		{
			name: "both configured modes",
			args: []string{
				"--scenario", "host/nvmlInjectionMetrics",
				"--dcgm-nvml-injection-yaml", "fixture.yaml",
				"--dcgm-nvsdm-mock-yaml", "mock.yaml",
			},
			want: "mutually exclusive",
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

func TestNVSDMMockFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "configuration without explicit scenario",
			args: []string{"--suite", "host", "--dcgm-nvsdm-mock-yaml", "fixture.yaml"},
			want: "requires --scenario host/nvsdmMockMetrics",
		},
		{
			name: "configuration mixed with hardware scenario",
			args: []string{
				"--scenario", "host/nvsdmMockMetrics",
				"--scenario", "host/startupMetrics",
				"--dcgm-nvsdm-mock-yaml", "fixture.yaml",
			},
			want: "which does not use --dcgm-nvsdm-mock-yaml",
		},
		{
			name: "mock scenario without YAML",
			args: []string{"--scenario", "host/nvsdmMockMetrics"},
			want: "requires --dcgm-nvsdm-mock-yaml",
		},
		{
			name: "configuration excluded by skipped suite",
			args: []string{
				"--skip-suite", "host",
				"--scenario", "host/nvsdmMockMetrics",
				"--dcgm-nvsdm-mock-yaml", "fixture.yaml",
			},
			want: "requires the host suite",
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

// TestRunTestsRejectsEmptyExplicitSelection checks that an explicitly empty scenario selection is rejected.
func TestRunTestsRejectsEmptyExplicitSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode []string
	}{
		{name: "dry run", mode: []string{"--dry-run"}},
		{name: "build only", mode: []string{"--build-only"}},
		{name: "bootstrap only", mode: []string{"--bootstrap-only"}},
		{name: "dry run with bootstrap only", mode: []string{"--dry-run", "--bootstrap-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			runner := &fakeRunner{}
			args := append([]string{"tests"}, tc.mode...)
			args = append(args, "--suite", "host", "--scenario", "k8s/default")
			err := runWithRootRunner(args, &stdout, &stderr, root, runner)
			if err == nil || !strings.Contains(err.Error(), "selectors choose no runnable catalog scenarios") {
				t.Fatalf("empty selection error = %v, want selector rejection", err)
			}
			if code, ok := ExitCode(err); !ok || code != exitSetupFailure {
				t.Fatalf("empty selection exit code = %d, classified = %t; want setup failure", code, ok)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("empty selection executed commands: %#v", runner.commands)
			}
		})
	}
}

// TestRunTestsDryRunSelectsActiveNVLink checks that dry-run output includes the active NVLink scenario.
func TestRunTestsDryRunSelectsActiveNVLink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}

	args := []string{
		"tests",
		"--dry-run",
		"--bootstrap=install",
		"--scenario", "k8s/nvlink",
		"--dcgm-image", "dcgm:test",
		"--dcgm-version", "4.5.3",
	}
	if err := runWithRootRunner(args, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}

	output := stdout.String()
	for _, want := range []string{
		"[e2e] === Selected work ===",
		"k8s:\n  k8s/nvlink",
		"[e2e] === Required artifacts and preparation ===",
		"kubernetes-tools: k8s/nvlink",
		"not probed during dry-run",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, output)
		}
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
	if len(runner.commands) != 0 {
		t.Fatalf("dry-run executed commands: %#v", runner.commands)
	}
}

// TestRunTestsDryRunVerboseShowsCommandDetails checks that verbose dry-run output includes direct command details.
func TestRunTestsDryRunVerboseShowsCommandDetails(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}

	args := []string{
		"tests",
		"--dry-run",
		"--verbose",
		"--scenario", "k8s/nvlink",
		"--dcgm-image", "dcgm:test",
		"--dcgm-version", "4.5.3",
	}
	if err := runWithRootRunner(args, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}

	output := stdout.String()
	if strings.Contains(output, "verbose: command:") {
		t.Fatalf("verbose dry-run executed a command:\n%s", output)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("verbose dry-run executed commands: %#v", runner.commands)
	}
}

func TestOutputRunnerPrintsFailedQuietCommandOutput(t *testing.T) {
	var stdout bytes.Buffer
	runner := newCommandOutputRunner(&stdout, &fakeRunner{results: map[string]e2eexec.Result{
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

// TestClusterDiagnosticsPrintsHelmStatusInNormalOutput covers the CLI output
// policy while retaining the run-owned Helm environment used by diagnostics.
func TestClusterDiagnosticsPrintsHelmStatusInNormalOutput(t *testing.T) {
	const status = "STATUS: deployed (diagnostic Helm fixture)\n"
	base := &fakeRunner{
		results: map[string]e2eexec.Result{
			"helm status nvidia-device-plugin --namespace kube-system": {Stdout: []byte(status)},
		},
		resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
			if command.Name == "kubectl" && containsArgs(command.Args, "-n", "gpu-operator", "get", "pods", "-o", "json") {
				return e2eexec.Result{Stdout: []byte(`{"items":[]}`)}, true
			}
			return e2eexec.Result{}, false
		},
	}
	var output bytes.Buffer
	runner := newCommandOutputRunner(&output, base, false)
	cfg := clusterConfig(t.TempDir(), config.TestOptions{RunName: "helm-diagnostics", Kubeconfig: "/caller/kubeconfig"})
	if err := cluster.Diagnostics(context.Background(), runner, &output, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), status) != 1 {
		t.Fatalf("normal diagnostics should print successful Helm status once:\n%s", output.String())
	}
	for _, command := range base.commands {
		if command.Name != "helm" {
			continue
		}
		state := filepath.Join(cfg.Root, ".local-e2e", "helm-helm-diagnostics")
		requireArgs(t, "Helm diagnostic environment", command.Env,
			"KUBECONFIG="+cfg.Kubeconfig,
			"HELM_REPOSITORY_CONFIG="+filepath.Join(state, "repositories.yaml"),
			"HELM_REPOSITORY_CACHE="+filepath.Join(state, "cache", "repository"),
			"HELM_CACHE_HOME="+filepath.Join(state, "cache"),
			"HELM_CONTENT_CACHE="+filepath.Join(state, "cache", "content"),
		)
		return
	}
	t.Fatal("diagnostics did not run Helm status")
}

// TestSensitiveOutputRunnerWithholdsProvidedStreams keeps captured sensitive output off writer streams.
func TestSensitiveOutputRunnerWithholdsProvidedStreams(t *testing.T) {
	const marker = "SYNTHETIC-SECRET-DO-NOT-LOG"
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprintf("verbose=%t", verbose), func(t *testing.T) {
			var output, commandStdout, commandStderr bytes.Buffer
			runner := newCommandOutputRunner(&output, &fakeRunner{results: map[string]e2eexec.Result{
				"kubectl get secret": {ExitCode: 1, Stdout: []byte(marker + " stdout"), Stderr: []byte(marker + " stderr"), Err: errors.New(marker + " error")},
			}}, verbose)
			result := runner.Run(context.Background(), e2eexec.Command{
				Name:            "kubectl",
				Args:            []string{"get", "secret"},
				Stdout:          &commandStdout,
				Stderr:          &commandStderr,
				QuietOnSuccess:  true,
				SensitiveOutput: true,
			})
			if string(result.Stdout) != marker+" stdout" || string(result.Stderr) != marker+" stderr" {
				t.Fatalf("private result output = (%q, %q), want captured bytes", result.Stdout, result.Stderr)
			}
			for _, stream := range []string{output.String(), commandStdout.String(), commandStderr.String()} {
				if strings.Contains(stream, marker) {
					t.Fatalf("sensitive output was streamed: %q", stream)
				}
			}
		})
	}
}

// TestImagePullSecretOutputIsNotLogged checks that secret contents stay out of logs and errors in verbose and quiet modes.
func TestImagePullSecretOutputIsNotLogged(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		for _, operation := range []string{"create-success", "create-failure", "apply-failure", "get-success", "get-failure"} {
			t.Run(fmt.Sprintf("%s/verbose=%t", operation, verbose), func(t *testing.T) {
				runImagePullSecretOutputCase(t, verbose, operation)
			})
		}
	}
}

// runImagePullSecretOutputCase runs one image pull secret outcome and checks its output and commands.
func runImagePullSecretOutputCase(t *testing.T, verbose bool, operation string) {
	t.Helper()
	const marker = "SYNTHETIC-SECRET-DO-NOT-LOG"
	encodedMarker := base64.StdEncoding.EncodeToString([]byte(marker))
	secretYAML := "apiVersion: v1\nkind: Secret\nstringData:\n  marker: " + marker + "\n"
	runner := &fakeRunner{resultMatcher: imagePullSecretResultMatcher(operation, marker, encodedMarker, secretYAML)}
	var output bytes.Buffer
	wrapped := newCommandOutputRunner(&output, runner, verbose)
	err := runImagePullSecretOperation(operation, wrapped, &output)
	wantError := strings.HasSuffix(operation, "failure")
	if (err != nil) != wantError {
		t.Fatalf("secret operation error = %v, wantError = %t", err, wantError)
	}
	assertImagePullSecretOutputHidden(t, output.String(), err, marker, encodedMarker)
	assertImagePullSecretCommands(t, runner, operation, secretYAML)
}

// imagePullSecretResultMatcher returns fake kubectl results for image pull secret create, apply, and get commands.
func imagePullSecretResultMatcher(operation, marker, encodedMarker, secretYAML string) func(e2eexec.Command) (e2eexec.Result, bool) {
	return func(command e2eexec.Command) (e2eexec.Result, bool) {
		switch {
		case containsArgs(command.Args, "create", "secret", "generic"):
			return imagePullSecretCreateResult(operation, marker, secretYAML), true
		case containsArgs(command.Args, "apply", "-f", "-"):
			return imagePullSecretApplyResult(operation, marker), true
		case containsArgs(command.Args, "get", "secret"):
			return imagePullSecretGetResult(operation, marker, encodedMarker), true
		default:
			return e2eexec.Result{Stdout: []byte("ok\n")}, true
		}
	}
}

// imagePullSecretCreateResult builds the fake result for creating a registry secret.
func imagePullSecretCreateResult(operation, marker, secretYAML string) e2eexec.Result {
	result := e2eexec.Result{Stdout: []byte(secretYAML)}
	if operation == "create-failure" {
		result.ExitCode = 1
		result.Stderr = []byte(marker + " create diagnostic")
		result.Err = errors.New(marker + " create runner error")
	}
	return result
}

// imagePullSecretApplyResult builds the fake result for applying a registry secret manifest.
func imagePullSecretApplyResult(operation, marker string) e2eexec.Result {
	if operation == "apply-failure" {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte(marker + " apply diagnostic"), Err: errors.New(marker + " apply runner error")}
	}
	return e2eexec.Result{}
}

// imagePullSecretGetResult builds the fake result for reading a registry secret.
func imagePullSecretGetResult(operation, marker, encodedMarker string) e2eexec.Result {
	if operation == "get-failure" {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte(marker + " get diagnostic"), Err: errors.New(marker + " get runner error")}
	}
	return e2eexec.Result{Stdout: []byte("kubernetes.io/dockerconfigjson\t" + encodedMarker)}
}

// runImagePullSecretOperation runs the requested image pull secret operation against a fake command runner.
func runImagePullSecretOperation(operation string, runner e2eexec.Runner, output io.Writer) error {
	if strings.HasPrefix(operation, "get-") {
		return cluster.RequireImagePullSecret(context.Background(), runner, cluster.DefaultConfig(), "dcgm-exporter", "pull-secret")
	}
	return cluster.EnsureImagePullSecret(context.Background(), runner, output, cluster.DefaultConfig(), "dcgm-exporter", "pull-secret", "/tmp/docker-config.json")
}

// assertImagePullSecretOutputHidden checks that secret values are absent from command output and returned errors.
func assertImagePullSecretOutputHidden(t *testing.T, output string, err error, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(output, value) || (err != nil && strings.Contains(err.Error(), value)) {
			t.Fatalf("sensitive value appeared in logs or error: output=%q error=%v", output, err)
		}
	}
	if err != nil && (!strings.Contains(err.Error(), "kubectl") || !strings.Contains(err.Error(), "exit code")) {
		t.Fatalf("failure error = %v, want safe command and exit context", err)
	}
}

// assertImagePullSecretCommands checks that secret commands are marked sensitive and apply payloads use stdin.
func assertImagePullSecretCommands(t *testing.T, runner *fakeRunner, operation, secretYAML string) {
	t.Helper()
	var sawSensitive, sawApplyPayload bool
	for _, command := range runner.commands {
		if isImagePullSecretCommand(command) {
			if !command.SensitiveOutput {
				t.Errorf("secret command is not marked sensitive: %#v", command)
			}
			sawSensitive = true
		}
		if containsArgs(command.Args, "apply", "-f", "-") && string(command.Stdin) == secretYAML {
			sawApplyPayload = true
		}
	}
	if !sawSensitive {
		t.Fatal("no image pull secret command was marked sensitive")
	}
	if (operation == "create-success" || operation == "apply-failure") && !sawApplyPayload {
		t.Fatal("secret YAML was not passed privately to kubectl apply")
	}
}

// isImagePullSecretCommand identifies kubectl commands that create, apply, or read image pull secrets.
func isImagePullSecretCommand(command e2eexec.Command) bool {
	return containsArgs(command.Args, "create", "secret", "generic") ||
		containsArgs(command.Args, "apply", "-f", "-") ||
		containsArgs(command.Args, "get", "secret")
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

	runner := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{"tests", "--suite", "container", "--scenario", "container/imageStartup"}, &stdout, &stderr, root, runner)
	if err == nil || !strings.Contains(err.Error(), "container image exporter required by container/imageStartup: --exporter-image is required") {
		t.Fatalf("runWithRootRunner() error = %v, want missing image", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("missing image setting ran host checks: %#v", runner.commands)
	}
}

func TestRunTestsRejectsExternalK8sWithoutImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{outputs: map[string][]byte{
		"kubectl version --client=true": []byte("Client Version: v1.35.3\n"),
		"helm version --short":          []byte("v4.1.0\n"),
	}}
	err := runWithRootRunner([]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--kubeconfig", "/tmp/kubeconfig"}, &stdout, &stderr, root, runner)
	if err == nil || !strings.Contains(err.Error(), "container image exporter required by k8s/default: --exporter-image is required") {
		t.Fatalf("runWithRootRunner() error = %v, want missing image", err)
	}
}

// TestRunTestsBootstrapOnlySkipsScenarioImages verifies bootstrap-only runs do not inspect scenario images.
func TestRunTestsBootstrapOnlySkipsScenarioImages(t *testing.T) {
	const smokeImage = "registry.example/cuda:dev"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{
		"tests",
		"--suite", "container",
		"--scenario", "container/imageStartup",
		"--bootstrap-only",
		"--container-toolkit-test-image", smokeImage,
		"--container-toolkit-version", "1.19.1-1",
	}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("--gpus") {
		t.Fatalf("bootstrap-only did not check the GPU runtime: %#v", runner.commands)
	}
	for _, command := range runner.commands {
		if command.Name != "docker" {
			continue
		}
		if containsArgs(command.Args, "manifest", "inspect") ||
			(containsArgs(command.Args, "image", "inspect") && !containsArgs(command.Args, smokeImage)) {
			t.Fatalf("bootstrap-only inspected a scenario image: %#v", runner.commands)
		}
	}
}

// TestRunTestsLogsInBeforeGPURuntimeSmoke verifies private GPU smoke images are not checked before Docker login.
func TestRunTestsLogsInBeforeGPURuntimeSmoke(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{outputs: map[string][]byte{
		"k3d version":                   []byte("k3d version v5.8.3\n"),
		"kubectl version --client=true": []byte("Client Version: v1.35.3\n"),
		"helm version --short":          []byte("v4.1.0\n"),
		"nvidia-smi --query-gpu=driver_version --format=csv,noheader": []byte("615.72\n"),
	}}
	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{
		"tests",
		"--suite", "k8s",
		"--scenario", "k8s/default",
		"--bootstrap=check",
		"--bootstrap-only",
		"--exporter-image", "registry.example/dcgm-exporter:dev",
		"--docker-registry-login", "registry.example,user," + passwordFile,
	}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	login := runner.commandIndex("docker", "login", "registry.example", "-u", "user", "--password-stdin")
	inspect := runner.commandIndex("docker", "image", "inspect", "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04")
	smoke := runner.commandIndex("docker", "run", "--pull=never", "--rm", "--gpus", "all")
	if login < 0 || inspect < 0 || smoke < 0 {
		t.Fatalf("missing login or GPU smoke commands: %#v", runner.commands)
	}
	if inspect < login || smoke < login {
		t.Fatalf("GPU smoke used the image before Docker login: %#v", runner.commands)
	}
	for index, command := range runner.commands {
		if command.Name == "docker" && containsArgs(command.Args, "pull") && index < login {
			t.Fatalf("Docker pulled an image before login: %#v", runner.commands)
		}
	}
}

// TestRunTestsChecksSelectedImagesBeforeGPURuntimeSmoke verifies selected-image checks run after login and before GPU setup.
func TestRunTestsChecksSelectedImagesBeforeGPURuntimeSmoke(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const exporterImage = "registry.example/dcgm-exporter:dev"
	runner := &fakeRunner{outputs: map[string][]byte{
		"k3d version":                   []byte("k3d version v5.8.3\n"),
		"kubectl version --client=true": []byte("Client Version: v1.35.3\n"),
		"helm version --short":          []byte("v4.1.0\n"),
		"nvidia-smi --query-gpu=driver_version --format=csv,noheader": []byte("615.72\n"),
	}}
	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{
		"tests",
		"--suite", "container",
		"--scenario", "container/imageStartup",
		"--bootstrap=check",
		"--exporter-image", exporterImage,
		"--docker-registry-login", "registry.example,user," + passwordFile,
	}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	login := runner.commandIndex("docker", "login", "registry.example", "-u", "user", "--password-stdin")
	imageCheck := runner.commandIndex("docker", "image", "inspect", exporterImage)
	smoke := runner.commandIndex("docker", "run", "--pull=never", "--rm", "--gpus", "all")
	if login < 0 || imageCheck < 0 || smoke < 0 {
		t.Fatalf("missing login, selected-image, or GPU smoke command: %#v", runner.commands)
	}
	if login > imageCheck || imageCheck > smoke {
		t.Fatalf("login/image/smoke order = %d/%d/%d; commands = %#v", login, imageCheck, smoke, runner.commands)
	}
}

// TestRunTestsStopsBeforeGPURuntimeWhenSelectedImageIsPrivate verifies image access failures do not start GPU or Kubernetes setup.
func TestRunTestsStopsBeforeGPURuntimeWhenSelectedImageIsPrivate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	const exporterImage = "registry.example/private-exporter:dev"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + exporterImage:    {ExitCode: 1, Stderr: []byte("No such image\n")},
		"docker manifest inspect " + exporterImage: {ExitCode: 1, Stderr: []byte("denied: requested access to the resource is denied\n")},
	}}
	var stdout, stderr bytes.Buffer
	err := runWithRootRunner([]string{
		"tests",
		"--suite", "k8s",
		"--scenario", "k8s/default",
		"--bootstrap=skip",
		"--exporter-image", exporterImage,
	}, &stdout, &stderr, root, runner)
	if err == nil || !strings.Contains(err.Error(), "requested access") {
		t.Fatalf("runWithRootRunner() error = %v, want private image failure", err)
	}
	if runner.hasCommandArg("--gpus") || runner.hasCommandArg("create") || runner.hasCommand("dcgm-exporter-k8s.test") {
		t.Fatalf("private image failure started GPU, k3d, or suite work: %#v", runner.commands)
	}
}

func TestRunSelectedSuitesContinuesAfterK8sSetupFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	runner := &fakeRunner{}
	err := runSelectedSuites(&stdout, root, runner, config.Config{TestOptions: config.TestOptions{
		Scenarios:        []string{"k8s/default", "static/paths"},
		Kubeconfig:       "/tmp/kubeconfig",
		ExporterImage:    "registry.example/dcgm-exporter:dev",
		GPUOperator:      "false",
		ResultMarkers:    true,
		E2EResultMarkers: true,
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

// TestK8sSetupFailureCollectsDiagnosticsBeforeCleanup preserves diagnostic collection before teardown.
func TestK8sSetupFailureCollectsDiagnosticsBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	opts, err := parseTests([]string{"--scenario", "k8s/default"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	runner := devicePluginFailureRunner()

	var stdout bytes.Buffer
	manager := suite.NewTestBinaryManager(root, runner)
	_, err = runK8sSuiteOnce(context.Background(), &stdout, root, manager, runner, config.Config{TestOptions: opts}, false)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("runK8sSuiteOnce() error = %v, want original device-plugin failure", err)
	}
	pluginFailure := runner.commandIndex("helm", "upgrade", "--install", "nvidia-device-plugin")
	diagnostics := runner.commandIndex("kubectl", "logs", "--previous")
	cleanup := runner.commandIndex("k3d", "cluster", "delete")
	if pluginFailure < 0 || diagnostics <= pluginFailure || cleanup <= diagnostics {
		t.Fatalf("failure/diagnostics/cleanup order = %d/%d/%d; commands = %#v", pluginFailure, diagnostics, cleanup, runner.commands)
	}
	requireOutput(t, stdout.String(), "previous device-plugin crash")
}

// devicePluginFailureRunner simulates a local k3d cluster whose device-plugin install
// times out and whose previous plugin logs explain the crash.
func devicePluginFailureRunner() *fakeRunner {
	clusterExists := false
	runner := &fakeRunner{}
	runner.onRun = func(command e2eexec.Command) {
		if command.Name == "k3d" && containsArgs(command.Args, "cluster", "create") {
			clusterExists = true
		}
	}
	runner.resultMatcher = func(command e2eexec.Command) (e2eexec.Result, bool) {
		switch {
		case command.Name == "k3d" && containsArgs(command.Args, "cluster", "list"):
			if clusterExists {
				return e2eexec.Result{Stdout: []byte("dcgm-exporter-gpu 1/1 1/1\n")}, true
			}
			return e2eexec.Result{}, true
		case command.Name == "helm" && containsArgs(command.Args, "upgrade", "--install", "nvidia-device-plugin"):
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("context deadline exceeded\n")}, true
		case command.Name == "kubectl" && containsArgs(command.Args, "logs", "--previous", "app.kubernetes.io/name=nvidia-device-plugin"):
			return e2eexec.Result{Stdout: []byte("previous device-plugin crash\n")}, true
		default:
			return e2eexec.Result{}, false
		}
	}
	return runner
}

func TestNVLinkAndNVSwitchSelectionDoesNotProbeExactFields(t *testing.T) {
	for _, selector := range []string{"k8s/nvlink", "k8s/nvswitch", "k8s/nvswitchLink"} {
		selected := scenario.Selected(scenario.Catalog, config.Config{TestOptions: config.TestOptions{Scenarios: []string{selector}}})
		if containsCapabilityProbeInput(scenario.CapabilityProbeInputsFor(selected), scenario.CapabilityProbeInputDCGMImage) {
			t.Fatalf("%s should not need a DCGM metric-sample probe", selector)
		}
	}
}

func TestLocalDCGMImageImportFailureFailsWithoutPullSecret(t *testing.T) {
	var stdout bytes.Buffer
	opts := config.TestOptions{
		DCGMImage: "dcgm:test",
	}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"k3d image import dcgm:test -c dcgm-exporter-gpu": {
			Stderr: []byte("ERRO failed to import images in node 'k3d-dcgm-exporter-gpu-agent-0'\n"),
		},
	}}

	_, err := ensureLocalDCGMImageImport(context.Background(), &stdout, runner, clusterConfig(t.TempDir(), opts), opts)
	if err == nil || !strings.Contains(err.Error(), "pass --docker-registry-login-file or pre-load the image") {
		t.Fatalf("ensureLocalDCGMImageImport() error = %v, want image prerequisite failure", err)
	}
}

func TestLocalDCGMImageImportFailureAllowsPullSecret(t *testing.T) {
	var stdout bytes.Buffer
	opts := config.TestOptions{
		DCGMImage:          "dcgm:test",
		K8sImagePullSecret: "pull-secret",
	}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"k3d image import dcgm:test -c dcgm-exporter-gpu": {
			Stderr: []byte("ERRO failed to import images in node 'k3d-dcgm-exporter-gpu-agent-0'\n"),
		},
	}}

	image, err := ensureLocalDCGMImageImport(context.Background(), &stdout, runner, clusterConfig(t.TempDir(), opts), opts)
	if err != nil {
		t.Fatalf("ensureLocalDCGMImageImport() error = %v, want nil", err)
	}
	if image != "dcgm:test" {
		t.Fatalf("DCGM image = %q, want source image when using a pull secret", image)
	}
	if !strings.Contains(stdout.String(), "relying on Kubernetes image pull secret pull-secret") {
		t.Fatalf("missing pull-secret warning:\n%s", stdout.String())
	}
}

func TestLocalDCGMImageTagFailureReportsImportFailure(t *testing.T) {
	var stdout bytes.Buffer
	image := "registry.example/dcgm@" + testImageDigest
	localImage := "registry.example/dcgm:dcgm-exporter-e2e-aaaaaaaaaaaa"
	opts := config.TestOptions{DCGMImage: image}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker tag " + image + " " + localImage: {
			ExitCode: 1,
			Stderr:   []byte("tag failed"),
		},
	}}

	_, err := ensureLocalDCGMImageImport(context.Background(), &stdout, runner, clusterConfig(t.TempDir(), opts), opts)
	if err == nil || !strings.Contains(err.Error(), "could not import the configured DCGM image") || strings.Contains(err.Error(), "could not inspect") {
		t.Fatalf("ensureLocalDCGMImageImport() error = %v, want tag/import failure", err)
	}
}

// TestPrepareK8sGroupMainImportsOperatorAndValidatorImageBeforeHelm verifies controller and validator imports precede Helm.
func TestPrepareK8sGroupMainImportsOperatorAndValidatorImageBeforeHelm(t *testing.T) {
	const controllerImage = "registry.example/nvidia/gpu-operator:main"

	runner := &fakeRunner{
		resultSequences: map[string][]e2eexec.Result{
			"docker image inspect " + controllerImage: {{ExitCode: 1}},
		},
	}
	matcher := &gpuOperatorMainInstallMatcher{}
	runner.resultMatcher = matcher.match

	chart := filepath.Join(t.TempDir(), "gpu-operator")
	if err := os.MkdirAll(chart, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := config.TestOptions{
		GPUOperator:      "install",
		GPUOperatorChart: chart,
		GPUOperatorImage: controllerImage,
		ExporterImage:    "registry.example/nvidia/dcgm-exporter:dev",
		WaitTimeout:      "1ms",
	}
	var stdout bytes.Buffer
	root := t.TempDir()
	run := newK8sGroupPreparationRun(context.Background(), &stdout, runner, root, clusterConfig(root, opts))
	cleanup, err := run.prepareK8sGroup(opts, scenario.PlanGroup{Name: "gpu-operator-main-embedded"})
	if err != nil {
		t.Fatalf("prepareK8sGroup() error = %v", err)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("GPU Operator cleanup error = %v", err)
	}

	pull := runner.commandIndex("docker", "pull", controllerImage)
	importIndex := runner.commandIndex("k3d", "image", "import", controllerImage)
	helm := runner.commandIndex("helm", "upgrade", "--install", "gpu-operator")
	if pull < 0 || importIndex < 0 || helm < 0 || pull > importIndex || importIndex > helm {
		t.Fatalf("controller image preparation did not run before Helm: pull/import/helm = %d/%d/%d; commands = %#v", pull, importIndex, helm, runner.commands)
	}
	importCommand, ok := runner.commandWithArgs("k3d", "image", "import", controllerImage)
	if !ok || importCommand.LogName != "k3d_import_gpu_operator_image" {
		t.Fatalf("controller image import step = %#v, want k3d_import_gpu_operator_image", importCommand)
	}
	if got := runner.commandCount("k3d", "image", "import", controllerImage); got != 1 {
		t.Fatalf("GPU Operator image imports = %d, want one shared operator and validator image import: %#v", got, runner.commands)
	}
	helmArgs := strings.Join(runner.commands[helm].Args, " ")
	for _, want := range []string{
		"operator.repository=registry.example/nvidia",
		"operator.image=gpu-operator",
		"operator.version=main",
		"validator.repository=registry.example/nvidia",
		"validator.image=gpu-operator",
		"validator.version=main",
	} {
		if !strings.Contains(helmArgs, want) {
			t.Fatalf("GPU Operator Helm arguments missing %q: %s", want, helmArgs)
		}
	}
	for _, node := range []string{"k3d-dcgm-exporter-gpu-server-0", "k3d-dcgm-exporter-gpu-agent-0"} {
		if runner.commandIndex("docker", "exec", node, "ctr", "--namespace", "k8s.io", "images", "check", "--quiet", "name=="+controllerImage) < 0 {
			t.Fatalf("controller image was not verified on %s: %#v", node, runner.commands)
		}
	}
}

// gpuOperatorMainInstallMatcher matches commands from a simulated GPU Operator install and exporter readiness check.
type gpuOperatorMainInstallMatcher struct {
	clusterPolicyChecks int
	namespaceCreated    bool
}

// match tries the namespace, resource, and exporter pod command matchers.
func (m *gpuOperatorMainInstallMatcher) match(command e2eexec.Command) (e2eexec.Result, bool) {
	for _, respond := range []func(e2eexec.Command) (e2eexec.Result, bool){
		m.matchNamespace,
		m.matchResources,
		m.matchExporterPods,
	} {
		if result, matched := respond(command); matched {
			return result, true
		}
	}
	return e2eexec.Result{}, false
}

// matchNamespace simulates creation and lookup of the GPU Operator namespace.
func (m *gpuOperatorMainInstallMatcher) matchNamespace(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name != "kubectl" {
		return e2eexec.Result{}, false
	}
	if containsArgs(command.Args, "get", "namespace", "gpu-operator", "--ignore-not-found=true", "-o", "json") && m.namespaceCreated {
		return e2eexec.Result{Stdout: []byte(`{"metadata":{"labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"dcgm-exporter"}},"status":{"phase":"Active"}}`)}, true
	}
	if containsArgs(command.Args, "get", "namespace", "gpu-operator") {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte("Error from server (NotFound): namespaces \"gpu-operator\" not found\n")}, true
	}
	if containsArgs(command.Args, "create", "-f", "-") && strings.Contains(string(command.Stdin), `"name":"gpu-operator"`) {
		m.namespaceCreated = true
		return e2eexec.Result{}, true
	}
	return e2eexec.Result{}, false
}

// matchResources simulates checks for GPU Operator custom resources.
func (m *gpuOperatorMainInstallMatcher) matchResources(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name != "kubectl" {
		return e2eexec.Result{}, false
	}
	if containsArgs(command.Args, "get", "clusterpolicy", "-o", "name") {
		m.clusterPolicyChecks++
		if m.clusterPolicyChecks == 1 {
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("the server doesn't have a resource type \"clusterpolicy\"\n")}, true
		}
		return e2eexec.Result{Stdout: []byte("clusterpolicy/cluster-policy\n")}, true
	}
	if containsArgs(command.Args, "get", "gpucluster", "-o", "name") {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte("the server doesn't have a resource type \"gpucluster\"\n")}, true
	}
	return e2eexec.Result{}, false
}

// matchExporterPods simulates the exporter pod readiness lookup.
func (m *gpuOperatorMainInstallMatcher) matchExporterPods(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name == "kubectl" && containsArgs(command.Args, "-n", "gpu-operator", "get", "pods") {
		return e2eexec.Result{Stdout: []byte(`{"items":[{"metadata":{"name":"nvidia-dcgm-exporter"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`)}, true
	}
	return e2eexec.Result{}, false
}

func TestEnsureLocalGPUOperatorImageImportStopsWhenImageCannotBePulled(t *testing.T) {
	const controllerImage = "registry.example/nvidia/gpu-operator:main"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect " + controllerImage: {ExitCode: 1, Stderr: []byte("No such image\n")},
		"docker pull " + controllerImage:          {ExitCode: 1, Stderr: []byte("denied\n")},
	}}

	err := ensureLocalGPUOperatorImageImport(
		context.Background(),
		io.Discard,
		runner,
		cluster.DefaultConfig(),
		config.TestOptions{GPUOperatorImage: controllerImage},
	)
	if err == nil || !strings.Contains(err.Error(), "make GPU Operator controller image available locally") {
		t.Fatalf("ensureLocalGPUOperatorImageImport() error = %v, want local image error", err)
	}
	if runner.hasK3dImageImport(controllerImage) {
		t.Fatalf("failed controller image pull attempted k3d import: %#v", runner.commands)
	}
}

func TestEnsureDCGMImageAvailableChecksLocalImageBeforeRegistry(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect dcgm:test": {
			ExitCode: 1,
			Stderr:   []byte("No such image"),
		},
		"docker manifest inspect dcgm:test": {
			ExitCode: 1,
			Stderr:   []byte("unauthorized"),
		},
	}}

	err := ensureDCGMImageAvailable(context.Background(), runner, config.TestOptions{DCGMImage: "dcgm:test"})
	if err == nil {
		t.Fatal("ensureDCGMImageAvailable() error = nil")
	}
	for _, want := range []string{"No such image", "unauthorized"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ensureDCGMImageAvailable() error = %v, missing %q", err, want)
		}
	}
	local := runner.commandIndex("docker", "image", "inspect", "dcgm:test")
	remote := runner.commandIndex("docker", "manifest", "inspect", "dcgm:test")
	if local < 0 || remote < 0 || local > remote {
		t.Fatalf("image checks ran in the wrong order: %#v", runner.commands)
	}
}

func TestEnsureDCGMImageAvailableUsesDockerConfig(t *testing.T) {
	dockerConfig := t.TempDir()
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect dcgm:test":    {ExitCode: 1, Stderr: []byte("No such image")},
		"docker manifest inspect dcgm:test": {Stdout: hostPlatformManifest()},
	}}

	if err := ensureDCGMImageAvailable(context.Background(), runner, config.TestOptions{
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

func TestEnsureDCGMImageVersionMatchesPinnedVersion(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"docker run --rm --entrypoint /usr/bin/nv-hostengine dcgm:test --version": []byte("Version : 4.5.3\n"),
	}}
	opts := config.TestOptions{
		DCGMImage: "dcgm:test",
		Dependencies: config.Dependencies{
			DCGMVersion: "4.5.3",
		},
	}

	if err := ensureDCGMImageVersion(context.Background(), runner, opts); err != nil {
		t.Fatalf("ensureDCGMImageVersion() error = %v", err)
	}
}

// TestDCGMImageVersionReadsStdoutAfterAPull reads the version from stdout when
// a first docker run also writes image pull progress to stderr. The progress
// names a tag carrying a different version, so the check fails if the version
// is taken from stderr before stdout.
func TestDCGMImageVersionReadsStdoutAfterAPull(t *testing.T) {
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		return e2eexec.Result{
			Stdout: []byte("Version : 4.7.0\n"),
			Stderr: []byte("Unable to find image 'dcgm:4.6.0' locally\ntest: Pulling from dcgm\nStatus: Downloaded newer image for dcgm:4.6.0\n"),
		}, true
	}}

	version, err := dcgmImageVersion(context.Background(), runner, "dcgm:4.6.0", nil)

	require.NoError(t, err)
	require.Equal(t, "4.7.0", version)
}

func TestEnsureDCGMImageVersionRejectsMismatch(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"docker run --rm --entrypoint /usr/bin/nv-hostengine dcgm:test --version": []byte("Version : 4.6.0\n"),
	}}
	opts := config.TestOptions{
		DCGMImage: "dcgm:test",
		Dependencies: config.Dependencies{
			DCGMVersion: "4.5.3",
		},
	}

	err := ensureDCGMImageVersion(context.Background(), runner, opts)
	if err == nil || !strings.Contains(err.Error(), "provides nv-hostengine 4.6.0, want 4.5.3") {
		t.Fatalf("ensureDCGMImageVersion() error = %v, want version mismatch", err)
	}
}

// TestEnsureDCGMImageVersionReportsMissingHostPlatform verifies the typed platform error.
func TestEnsureDCGMImageVersionReportsMissingHostPlatform(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker run --rm --entrypoint /usr/bin/nv-hostengine dcgm:test --version": {
			ExitCode: 1,
			Stderr:   []byte("no matching manifest for linux/arm64/v8 in the manifest list entries"),
		},
	}}
	opts := config.TestOptions{
		DCGMImage: "dcgm:test",
		Dependencies: config.Dependencies{
			DCGMVersion: "4.5.3",
		},
	}

	err := ensureDCGMImageVersion(context.Background(), runner, opts)
	if !errors.Is(err, errDCGMImagePlatformUnavailable) {
		t.Fatalf("ensureDCGMImageVersion() error = %v, want platform-unavailable error", err)
	}
}

// TestEnsureHostDCGMVersionRejectsMismatch requires the pinned host DCGM version.
func TestEnsureHostDCGMVersionRejectsMismatch(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"dcgmi --version": []byte("Version : 4.6.0\n"),
	}}
	opts := config.TestOptions{Dependencies: config.Dependencies{DCGMVersion: "4.5.3"}}

	err := ensureHostDCGMVersion(context.Background(), runner, opts)
	if err == nil || !strings.Contains(err.Error(), "provides DCGM 4.6.0, want 4.5.3") {
		t.Fatalf("ensureHostDCGMVersion() error = %v, want version mismatch", err)
	}
}

// TestPrepareProbeInputsRejectsWrongDCGMImageVersion fails before running hardware probes.
func TestPrepareProbeInputsRejectsWrongDCGMImageVersion(t *testing.T) {
	production := "dcgm:test"
	runner := &fakeRunner{outputs: map[string][]byte{
		"docker run --rm --entrypoint /usr/bin/nv-hostengine " + production + " --version": []byte("Version : 4.6.0\n"),
	}}
	run := &k8sSuiteRun{
		ctx:    context.Background(),
		stdout: io.Discard,
		runner: runner,
		cfg: config.Config{TestOptions: config.TestOptions{
			Scenarios: []string{"k8s/profiling"},
			DCGMImage: production,
			Dependencies: config.Dependencies{
				DCGMVersion: "4.5.3",
			},
		}},
		selected: []scenario.Scenario{
			scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "profiling"),
		},
		requirements: scenario.RequirementsFor([]scenario.Scenario{
			scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "profiling"),
		}, scenario.RunConditions{LocalKubernetes: true}),
	}

	_, err := run.prepareProbeInputs()
	if err == nil || !strings.Contains(err.Error(), "provides nv-hostengine 4.6.0, want 4.5.3") {
		t.Fatalf("prepareProbeInputs() error = %v, want image version mismatch", err)
	}
}

// TestPrepareProbeInputsQualifiesHostFallback checks host DCGM before selecting it.
func TestPrepareProbeInputsQualifiesHostFallback(t *testing.T) {
	production := "dcgm:test"
	hostDCGMPrepared := false
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		switch {
		case command.Name == "docker" && containsArgs(command.Args, "/usr/bin/nv-hostengine", "--version"):
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("no matching manifest for linux/arm64/v8")}, true
		case command.Name == "dcgmi" && containsArgs(command.Args, "--version"):
			return e2eexec.Result{Stdout: []byte("Version : 4.5.3\n")}, true
		default:
			return e2eexec.Result{}, false
		}
	}}
	run := &k8sSuiteRun{
		ctx:    context.Background(),
		stdout: io.Discard,
		root:   t.TempDir(),
		runner: runner,
		ensureHostDCGM: func(context.Context, io.Writer, string, e2eexec.Runner, config.TestOptions) error {
			hostDCGMPrepared = true
			return nil
		},
		cfg: config.Config{TestOptions: config.TestOptions{
			BootstrapMode: config.BootstrapCheck,
			Scenarios:     []string{"k8s/profiling"},
			DCGMImage:     production,
			Dependencies: config.Dependencies{
				DCGMVersion: "4.5.3",
			},
		}},
		selected: []scenario.Scenario{
			scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "profiling"),
		},
		requirements: scenario.RequirementsFor([]scenario.Scenario{
			scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "profiling"),
		}, scenario.RunConditions{LocalKubernetes: true}),
	}

	inputs, err := run.prepareProbeInputs()
	if err != nil {
		t.Fatalf("prepareProbeInputs() error = %v", err)
	}
	if inputs.hostDCGMAddress == "" {
		t.Fatal("prepareProbeInputs() did not select the qualified host backend")
	}
	if !hostDCGMPrepared {
		t.Fatal("prepareProbeInputs() did not prepare host DCGM")
	}
	if runner.commandIndex("dcgmi", "--version") == -1 {
		t.Fatalf("host DCGM version was not checked: %#v", runner.commands)
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

// TestRunClusterRejectsUnknownCommandBeforeToolPathSetup preserves the useful command error.
func TestRunClusterRejectsUnknownCommandBeforeToolPathSetup(t *testing.T) {
	err := runWithRootRunner([]string{"cluster", "bogus"}, io.Discard, io.Discard, "invalid\x00root", &fakeRunner{})
	if err == nil || !strings.Contains(err.Error(), `unknown e2e cluster command "bogus"`) {
		t.Fatalf("runWithRootRunner() error = %v, want unknown command", err)
	}
}

// TestRunClusterHelp checks that cluster command help is available without starting cluster setup.
func TestRunClusterHelp(t *testing.T) {
	const timeoutHelp = "bound cluster preparation or deployment to a Go duration (zero means unlimited)"
	tests := []struct {
		args        []string
		wantOutput  []string
		avoidOutput []string
	}{
		{
			args:       []string{"cluster"},
			wantOutput: []string{"USAGE:\n   e2e cluster <command> [options]", "deploy-dcgm"},
		},
		{
			args:       []string{"cluster", "--help"},
			wantOutput: []string{"USAGE:\n   e2e cluster <command> [options]", "deploy-dcgm"},
		},
		{
			args:       []string{"cluster", "help"},
			wantOutput: []string{"USAGE:\n   e2e cluster <command> [options]", "deploy-dcgm"},
		},
		{
			args:       []string{"cluster", "up", "--help"},
			wantOutput: []string{"USAGE:\n   e2e cluster up", timeoutHelp},
			avoidOutput: []string{
				"--bootstrap",
				"two-minute grace period",
			},
		},
		{
			args:       []string{"cluster", "deploy", "--help"},
			wantOutput: []string{"USAGE:\n   e2e cluster deploy", timeoutHelp},
			avoidOutput: []string{
				"--bootstrap",
				"two-minute grace period",
			},
		},
		{
			args:       []string{"cluster", "deploy-dcgm", "--help"},
			wantOutput: []string{"USAGE:\n   e2e cluster deploy-dcgm", timeoutHelp},
			avoidOutput: []string{
				"two-minute grace period",
			},
		},
		{
			args:       []string{"cluster", "help", "deploy"},
			wantOutput: []string{"USAGE:\n   e2e cluster deploy"},
		},
		{
			args:       []string{"cluster", "status", "--help"},
			wantOutput: []string{"USAGE:\n   e2e cluster status", "$E2E_KUBECONFIG"},
		},
		{
			args:       []string{"cluster", "logs", "--help"},
			wantOutput: []string{"USAGE:\n   e2e cluster logs"},
		},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		runner := &fakeRunner{}
		err := runWithRunner(test.args, &stdout, &stderr, runner)
		require.NoError(t, err, "runWithRunner(%v); stderr = %s", test.args, stderr.String())
		require.Empty(t, runner.commands, "runWithRunner(%v) executed commands while showing help", test.args)
		for _, text := range test.wantOutput {
			require.Contains(t, stdout.String(), text, "cluster help missing %q for %v", text, test.args)
		}
		for _, text := range test.avoidOutput {
			require.NotContains(t, stdout.String(), text, "cluster help unexpectedly contains %q for %v", text, test.args)
		}
	}
}

// TestClusterLifecycleCommandsApplyTimeout checks that cluster lifecycle commands receive the configured timeout.
func TestClusterLifecycleCommandsApplyTimeout(t *testing.T) {
	configurePackageTestEnvironment(t)
	t.Setenv("E2E_TIMEOUT", "1h")

	commands := []struct {
		name string
		args []string
	}{
		{name: "up", args: []string{"cluster", "up"}},
		{name: "deploy", args: []string{"cluster", "deploy", "--exporter-image", "registry.example/dcgm-exporter:dev@" + testImageDigest}},
		{name: "deploy-dcgm", args: []string{"cluster", "deploy-dcgm", "--dcgm-image", "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04@" + testImageDigest}},
	}
	for _, test := range commands {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600))
			writeTestVersions(t, root)

			runner := &clusterActionContextRunner{waitForCancellation: true}
			args := append(append([]string(nil), test.args...), "--timeout", "25ms")
			err := runWithRootRunnerContext(context.Background(), args, io.Discard, io.Discard, root, runner)
			require.Error(t, err, "first helper command should stop at the CLI deadline")
			require.ErrorContains(t, err, context.DeadlineExceeded.Error(), "want the deadline cause")
			require.NotNil(t, runner.firstContext, "lifecycle command did not invoke a helper")
			require.True(t, runner.canceled, "first helper context did not reach its CLI deadline; error = %v", err)
			deadline, ok := runner.firstContext.Deadline()
			require.True(t, ok, "first helper context has no execution deadline")
			remaining := time.Until(deadline)
			require.False(t, remaining > time.Second, "helper deadline is more than one second away after cancellation, suggesting E2E_TIMEOUT won over --timeout: %s", remaining)
		})
	}
}

// TestClusterLifecycleTimeoutZeroOverridesEnvironment checks that an explicit zero timeout overrides the environment value.
func TestClusterLifecycleTimeoutZeroOverridesEnvironment(t *testing.T) {
	configurePackageTestEnvironment(t)
	t.Setenv("E2E_TIMEOUT", "20ms")
	parent, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	runner := &clusterActionContextRunner{}
	err := runWithRootRunnerContext(parent, []string{"cluster", "up", "--timeout", "0"}, io.Discard, io.Discard, root, runner)
	if err == nil {
		t.Fatal("runWithRootRunnerContext() error = nil, want the first helper command to stop the test")
	}
	if runner.firstContext == nil {
		t.Fatal("cluster up did not invoke a helper")
	}
	gotDeadline, gotDeadlineSet := runner.firstContext.Deadline()
	wantDeadline, wantDeadlineSet := parent.Deadline()
	if !gotDeadlineSet || !wantDeadlineSet || !gotDeadline.Equal(wantDeadline) {
		t.Fatalf("helper deadline = (%v, %t), want caller deadline (%v, %t); --timeout 0 must override E2E_TIMEOUT", gotDeadline, gotDeadlineSet, wantDeadline, wantDeadlineSet)
	}
}

// TestRunClusterLifecycleRejectsBootstrapFlags verifies unsupported controls fail explicitly.
func TestRunClusterLifecycleRejectsBootstrapFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRunner([]string{"cluster", "up", "--bootstrap=check"}, &stdout, &stderr, runner)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("runWithRunner() error = %v, want unknown flag", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("cluster command ran after rejected bootstrap flag: %#v", runner.commands)
	}
}

func TestRunClusterLifecycleIgnoresBootstrapEnvironment(t *testing.T) {
	t.Setenv("E2E_BOOTSTRAP", "skip")
	t.Setenv("E2E_BOOTSTRAP_ONLY", "true")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runWithRootRunner(
		[]string{"cluster", "up", "--kubeconfig", filepath.Join(root, "kubeconfig")},
		io.Discard,
		io.Discard,
		root,
		&fakeRunner{},
	)
	if err == nil || !strings.Contains(err.Error(), "manages local k3d only") {
		t.Fatalf("runWithRootRunner() error = %v, want cluster target validation", err)
	}
}

func TestRunClusterCommandsRejectPositionalArgumentsWithCommandName(t *testing.T) {
	for _, name := range []string{"up", "deploy", "deploy-dcgm", "status", "logs", "cleanup"} {
		t.Run(name, func(t *testing.T) {
			err := runWithRootRunner(
				[]string{"cluster", name, "extra"},
				io.Discard,
				io.Discard,
				"invalid\x00root",
				&fakeRunner{},
			)
			want := "unexpected e2e cluster " + name + " arguments"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("runWithRootRunner() error = %v, want containing %q", err, want)
			}
		})
	}
}

func TestRunClusterUpCreatesLocalClusterWithoutExplicitRuntimeImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	t.Setenv("E2E_CONTAINER_TOOLKIT_TEST_IMAGE", "")
	t.Setenv("E2E_WAIT_TIMEOUT", "3m")
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
	if _, ok := runner.commandWithArgs("helm", "upgrade", "--install", "nvidia-device-plugin", "--timeout", "3m"); !ok {
		t.Fatalf("cluster up did not pass the configured wait timeout; commands = %#v", runner.commands)
	}
	if !runner.hasK3dImageImport("nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04") {
		t.Fatalf("cluster up did not import the node-base runtime preflight image: %#v", runner.commands)
	}
}

// TestRunClusterUpUsesConfiguredToolPath verifies lifecycle commands honor --tools-dir.
func TestRunClusterUpUsesConfiguredToolPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	wantPrefix := filepath.Join(root, "custom-tools", "bin")
	var sawK3D bool
	runner := &fakeRunner{onRun: func(command e2eexec.Command) {
		if command.Name != "k3d" {
			return
		}
		sawK3D = true
		if got := os.Getenv("PATH"); !strings.HasPrefix(got, wantPrefix+string(os.PathListSeparator)) {
			t.Fatalf("PATH = %q, want prefix %q", got, wantPrefix)
		}
	}}
	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{"cluster", "up", "--tools-dir", "custom-tools"}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !sawK3D {
		t.Fatal("cluster up did not run k3d")
	}
}

func TestRunClusterDeployWithoutExporterImageSuggestsSourceTask(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	t.Setenv("E2E_EXPORTER_IMAGE", "")

	runner := &fakeRunner{}
	err := runWithRootRunner([]string{"cluster", "deploy"}, io.Discard, io.Discard, root, runner)
	if err == nil || !strings.Contains(err.Error(), "source runs should use task test:e2e") {
		t.Fatalf("runWithRootRunner() error = %v, want source task guidance", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("cluster deploy ran commands before rejecting the image: %#v", runner.commands)
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
		"--exporter-image", "registry.example/dcgm-exporter:dev@" + testImageDigest,
	}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasCommandArg("dcgm-exporter") || !runner.hasCommandArg("--set-string") || !runner.hasCommandArg("image.repository=registry.example/dcgm-exporter") {
		t.Fatalf("cluster deploy did not install exporter with image settings; commands = %#v", runner.commands)
	}
	if !runner.hasDockerTag("registry.example/dcgm-exporter@"+testImageDigest, "registry.example/dcgm-exporter:dev") ||
		!runner.hasK3dImageImport("registry.example/dcgm-exporter:dev") {
		t.Fatalf("cluster deploy did not tag and import the local image alias: commands = %#v", runner.commands)
	}
	if !runner.hasCommandArg("image.tag=dev") || runner.hasCommandArg("image.digest="+testImageDigest) {
		t.Fatalf("cluster deploy did not use the imported tag alias in Helm: commands = %#v", runner.commands)
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

func TestRunClusterDeployDCGMUsesTaggedLocalImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	image := "registry.example/dcgm@" + testImageDigest
	localImage := "registry.example/dcgm:dcgm-exporter-e2e-aaaaaaaaaaaa"
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}

	if err := runWithRootRunner([]string{"cluster", "deploy-dcgm", "--dcgm-image", image}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !runner.hasDockerTag(image, localImage) || !runner.hasK3dImageImport(localImage) {
		t.Fatalf("cluster deploy-dcgm did not tag and import the local DCGM image: %#v", runner.commands)
	}
	deployed := false
	for _, command := range runner.commands {
		if command.Name != "kubectl" || !containsArgs(command.Args, "apply", "-f", "-") {
			continue
		}
		if strings.Contains(string(command.Stdin), `image: "`+localImage+`"`) {
			deployed = true
			break
		}
	}
	if !deployed {
		t.Fatalf("cluster deploy-dcgm did not deploy the imported DCGM image: %#v", runner.commands)
	}
}

func TestRunClusterDeployDCGMPullsMissingImageBeforeClusterSetup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)
	const image = "registry.example/dcgm:test"
	runner := &fakeRunner{
		resultSequences: map[string][]e2eexec.Result{
			"docker image inspect " + image: {
				{ExitCode: 1, Stderr: []byte("not found")},
				{ExitCode: 1, Stderr: []byte("not found")},
			},
		},
		results: map[string]e2eexec.Result{
			"docker manifest inspect " + image: {Stdout: hostPlatformManifest()},
		},
	}
	var stdout, stderr bytes.Buffer
	if err := runWithRootRunner([]string{"cluster", "deploy-dcgm", "--dcgm-image", image}, &stdout, &stderr, root, runner); err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	pull := runner.commandIndex("docker", "pull", image)
	clusterSetup := runner.commandIndex("k3d", "cluster", "create")
	if pull < 0 || clusterSetup < 0 || pull > clusterSetup {
		t.Fatalf("DCGM image pull must run before k3d cluster setup: %#v", runner.commands)
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

// TestParseTestsDefaultFeatureModes checks the default feature modes: install or
// configure everything the host allows, except MIG, which defaults to auto and
// uses MIG as it is.
func TestParseTestsDefaultFeatureModes(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseTests(nil, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.GPUOperator != "install" {
		t.Fatalf("gpu operator = %q, want install", opts.GPUOperator)
	}
	if opts.K3dIPFamily != "auto" {
		t.Fatalf("k3d ip family = %q, want auto", opts.K3dIPFamily)
	}
	if opts.K3dCgroupV1 != "auto" {
		t.Fatalf("k3d cgroup v1 = %q, want auto", opts.K3dCgroupV1)
	}
	if opts.MIGConfigure != "auto" || splitMixedMIGRun(opts) {
		t.Fatalf("MIG configure default = %q, want auto with no split full-GPU and MIG phases", opts.MIGConfigure)
	}
	trueOpts, err := parseTests([]string{"--mig-configure", "true"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !splitMixedMIGRun(trueOpts) {
		t.Fatalf("--mig-configure true did not split the full-GPU and MIG phases: %#v", trueOpts)
	}
	if opts.DRAConfigure != "true" {
		t.Fatalf("DRA configure default = %q, want true", opts.DRAConfigure)
	}
	if opts.SharedGPUConfigure != "true" {
		t.Fatalf("shared GPU configure default = %q, want true", opts.SharedGPUConfigure)
	}
	if !opts.Cleanup {
		t.Fatal("cleanup default = false, want true")
	}
	if opts.BootstrapMode != "" {
		t.Fatalf("bootstrap mode should remain unset before artifact detection: %#v", opts)
	}
}

func TestParseTestsDefaultsWaitTimeout(t *testing.T) {
	previous, existed := os.LookupEnv("E2E_WAIT_TIMEOUT")
	if err := os.Unsetenv("E2E_WAIT_TIMEOUT"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("E2E_WAIT_TIMEOUT", previous)
		} else {
			_ = os.Unsetenv("E2E_WAIT_TIMEOUT")
		}
	})
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.WaitTimeout != cluster.DefaultWaitTimeout() {
		t.Fatalf("wait timeout = %q, want %q", opts.WaitTimeout, cluster.DefaultWaitTimeout())
	}
}

func TestParseTestsAcceptsFeatureModeValues(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseTests([]string{"--gpu-operator", "false"}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.GPUOperator != "false" {
		t.Fatalf("gpu operator = %q, want false", opts.GPUOperator)
	}
}

func TestParseTestsValidatesGPUOperatorMainInputs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "chart requires install mode",
			args: []string{"--gpu-operator", "existing", "--gpu-operator-chart", "/tmp/gpu-operator"},
			want: "requires --gpu-operator=install",
		},
		{
			name: "controller image requires chart",
			args: []string{"--gpu-operator-image", "registry.example/nvidia/gpu-operator:main"},
			want: "requires --gpu-operator-chart",
		},
		{
			name: "DCGM image requires chart",
			args: []string{"--gpu-operator-dcgm-image", "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04"},
			want: "requires --gpu-operator-chart",
		},
		{
			name: "explicit main scenario requires chart",
			args: []string{"--scenario", "k8s/gpuOperatorMainEmbedded"},
			want: "requires --gpu-operator-chart",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTests(tt.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseTests(%v) error = %v, want %q", tt.args, err, tt.want)
			}
		})
	}

	var stderr bytes.Buffer
	opts, err := parseTests([]string{
		"--gpu-operator=install",
		"--gpu-operator-chart", "/tmp/gpu-operator",
		"--gpu-operator-image", "registry.example/nvidia/gpu-operator:main",
		"--gpu-operator-dcgm-image", "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}
	if opts.GPUOperatorChart == "" || opts.GPUOperatorImage == "" || opts.GPUOperatorDCGMImage == "" {
		t.Fatalf("missing main GPU Operator options: %#v", opts)
	}
}

// TestParseTestsAcceptsK3dCgroupV1Modes verifies the local k3d cgroup setting accepts every documented value.
func TestParseTestsAcceptsK3dCgroupV1Modes(t *testing.T) {
	for _, value := range []string{"auto", "true", "false"} {
		t.Run(value, func(t *testing.T) {
			opts, err := parseTests([]string{"--k3d-cgroup-v1", value}, io.Discard)
			if err != nil {
				t.Fatalf("parseTests() error = %v", err)
			}
			if string(opts.K3dCgroupV1) != value {
				t.Fatalf("K3dCgroupV1 = %q, want %q", opts.K3dCgroupV1, value)
			}
		})
	}
}

// TestParseTestsRejectsInvalidK3dCgroupV1 verifies invalid local k3d cgroup settings fail before any host work.
func TestParseTestsRejectsInvalidK3dCgroupV1(t *testing.T) {
	for _, test := range []struct {
		name string
		env  bool
	}{
		{name: "flag"},
		{name: "environment", env: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"--k3d-cgroup-v1", "sometimes"}
			if test.env {
				t.Setenv("E2E_K3D_CGROUP_V1", "sometimes")
				args = nil
			}
			_, err := parseTests(args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "--k3d-cgroup-v1 must be auto, true, or false") {
				t.Fatalf("parseTests() error = %v", err)
			}
		})
	}
}

func TestParseTestsUsesUrfaveBooleanSyntax(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseTests([]string{"--dry-run", "false"}, &stderr); err == nil || !strings.Contains(err.Error(), "unexpected e2e tests arguments") {
		t.Fatalf("parseTests() error = %v, want separated boolean value rejection", err)
	}
	opts, err := parseTests([]string{"--dry-run=false"}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if opts.DryRun {
		t.Fatal("dry-run = true, want false")
	}
	opts, err = parseTests([]string{"--cleanup=false"}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() cleanup error = %v", err)
	}
	if opts.Cleanup {
		t.Fatal("cleanup = true, want false")
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

// TestParseTestsRejectsInvalidBoolEnv checks that invalid boolean environment values are rejected.
func TestParseTestsRejectsInvalidBoolEnv(t *testing.T) {
	for _, name := range []string{
		"E2E_BUILD_ONLY",
		"E2E_PREBUILT_BINARIES",
		"E2E_VERBOSE",
		"E2E_LIST_SCENARIOS",
		"E2E_K3D_KEEP_CLUSTER",
		"E2E_BOOTSTRAP_ONLY",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "treu")
			var stderr bytes.Buffer
			if _, err := parseTests(nil, &stderr); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("parseTests() error = %v, want %s parse error", err, name)
			}
		})
	}
}

// TestEnvBoolFlagDefaultPreservesUrfaveEnvironmentValues keeps environment values when a flag uses defaults.
func TestEnvBoolFlagDefaultPreservesUrfaveEnvironmentValues(t *testing.T) {
	unsetTestEnvironment(t, "E2E_DRY_RUN")
	if got, err := envBoolFlagDefault("E2E_DRY_RUN", true); err != nil || !got {
		t.Fatalf("unset environment = (%t, %v), want (true, nil)", got, err)
	}
	t.Setenv("E2E_DRY_RUN", "")
	if got, err := envBoolFlagDefault("E2E_DRY_RUN", true); err != nil || got {
		t.Fatalf("empty environment = (%t, %v), want (false, nil)", got, err)
	}
	t.Setenv("E2E_DRY_RUN", "1")
	if got, err := envBoolFlagDefault("E2E_DRY_RUN", false); err != nil || !got {
		t.Fatalf("ParseBool environment = (%t, %v), want (true, nil)", got, err)
	}
	t.Setenv("E2E_DRY_RUN", " true ")
	if got, err := envBoolFlagDefault("E2E_DRY_RUN", false); err == nil || got {
		t.Fatalf("whitespace-padded environment = (%t, %v), want an error", got, err)
	}
}

// TestParseTestsDryRunPrecedence checks which dry-run setting wins when sources provide different values.
func TestParseTestsDryRunPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		envValue  string
		args      []string
		want      bool
		wantError bool
	}{
		{name: "default"},
		{name: "environment", envValue: "true", want: true},
		{name: "CLI true overrides environment false", envValue: "false", args: []string{"--dry-run=true"}, want: true},
		{name: "explicit false overrides environment true", envValue: "true", args: []string{"--dry-run=false"}},
		{name: "invalid winning environment", envValue: "invalid", wantError: true},
		{name: "invalid losing environment", envValue: "invalid", args: []string{"--dry-run=true"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unsetTestEnvironment(t, "E2E_DRY_RUN")
			if test.envValue != "" {
				t.Setenv("E2E_DRY_RUN", test.envValue)
			}
			requireDryRun(t, test.args, test.want, test.wantError)
		})
	}
}

// requireDryRun parses args and checks the resolved dry-run value or the expected E2E_DRY_RUN error.
func requireDryRun(t *testing.T, args []string, want, wantError bool) {
	t.Helper()
	opts, err := parseTests(args, io.Discard)
	switch {
	case wantError && (err == nil || !strings.Contains(err.Error(), "E2E_DRY_RUN")):
		t.Fatalf("parseTests() error = %v, want E2E_DRY_RUN parse error", err)
	case wantError:
		return
	case err != nil:
		t.Fatalf("parseTests() error = %v", err)
	case opts.DryRun != want:
		t.Fatalf("DryRun = %t, want %t", opts.DryRun, want)
	}
}

// TestParseTestsValidBoolFlagsOverrideInvalidEnvironment accepts valid boolean flags over invalid env values.
func TestParseTestsValidBoolFlagsOverrideInvalidEnvironment(t *testing.T) {
	tests := []struct {
		name    string
		envName string
		flag    string
		value   func(config.TestOptions) bool
	}{
		{name: "build only", envName: "E2E_BUILD_ONLY", flag: "--build-only=true", value: func(opts config.TestOptions) bool { return opts.BuildOnly }},
		{name: "prebuilt binaries", envName: "E2E_PREBUILT_BINARIES", flag: "--prebuilt-binaries=true", value: func(opts config.TestOptions) bool { return opts.PrebuiltBinaries }},
		{name: "verbose", envName: "E2E_VERBOSE", flag: "--verbose=true", value: func(opts config.TestOptions) bool { return opts.Verbose }},
		{name: "list scenarios", envName: "E2E_LIST_SCENARIOS", flag: "--list-scenarios=true", value: func(opts config.TestOptions) bool { return opts.ListScenarios }},
		{name: "keep cluster", envName: "E2E_K3D_KEEP_CLUSTER", flag: "--k3d-keep-cluster=true", value: func(opts config.TestOptions) bool { return opts.KeepCluster }},
		{name: "bootstrap only", envName: "E2E_BOOTSTRAP_ONLY", flag: "--bootstrap-only=true", value: func(opts config.TestOptions) bool { return opts.BootstrapOnly }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unsetTestEnvironment(t, test.envName)
			t.Setenv(test.envName, "invalid")
			opts, err := parseTests([]string{test.flag}, io.Discard)
			if err != nil {
				t.Fatalf("parseTests() error = %v", err)
			}
			if !test.value(opts) {
				t.Fatalf("parseTests() did not apply %s", test.flag)
			}
		})
	}
}

// TestParseTestsValidCLIValuesOverrideInvalidCustomEnvironment accepts valid CLI values over invalid env values.
func TestParseTestsValidCLIValuesOverrideInvalidCustomEnvironment(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   map[string]string
		unset []string
		args  []string
		valid func(config.TestOptions) bool
	}{
		{
			name: "bootstrap mode", env: map[string]string{"E2E_BOOTSTRAP": "invalid"}, args: []string{"--bootstrap=skip"},
			valid: func(opts config.TestOptions) bool {
				return opts.BootstrapMode == config.BootstrapSkip && opts.BootstrapSource == config.ValueCLI
			},
		},
		{
			name: "result markers", env: map[string]string{"E2E_RESULT_MARKERS": "invalid"}, args: []string{"--result-markers=true"},
			valid: func(opts config.TestOptions) bool { return opts.ResultMarkers && opts.E2EResultMarkers },
		},
		{
			name: "suite result markers", env: map[string]string{"E2E_SUITE_RESULT_MARKERS": "invalid"}, args: []string{"--no-result-markers"},
			valid: func(opts config.TestOptions) bool { return !opts.ResultMarkers && !opts.E2EResultMarkers },
		},
		{
			name: "run name", env: map[string]string{"E2E_RUN_NAME": "bad name"}, args: []string{"--run-name=valid"},
			unset: []string{"E2E_K3D_CLUSTER_NAME", "E2E_HELM_NAMESPACE", "E2E_HELM_RELEASE_NAME", "E2E_DCGM_NAMESPACE"},
			valid: func(opts config.TestOptions) bool {
				return opts.RunName == "valid" && opts.ClusterName == "valid-gpu" && opts.Namespace == "valid" && opts.ReleaseName == "valid"
			},
		},
		{
			name: "bootstrap component", env: map[string]string{"E2E_BOOTSTRAP_COMPONENTS": " Docker =invalid,host-dcgm=skip"}, args: []string{"--bootstrap-component=docker=install"},
			valid: validDockerInstallComponent,
		},
		{
			name: "bootstrap component without mode", env: map[string]string{"E2E_BOOTSTRAP_COMPONENTS": " Docker,host-dcgm=skip"}, args: []string{"--bootstrap-component=docker=install"},
			valid: validDockerInstallComponent,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := parseTestsWithEnvironment(t, test.env, test.unset, test.args...)
			if !test.valid(opts) {
				t.Fatalf("parseTests(%v) = %#v, want the CLI value to win", test.args, opts)
			}
		})
	}
}

// validDockerInstallComponent reports the CLI docker=install override with the environment host-dcgm=skip kept.
func validDockerInstallComponent(opts config.TestOptions) bool {
	return opts.BootstrapModeFor(config.BootstrapDocker) == config.BootstrapInstall &&
		opts.BootstrapModeFor(config.BootstrapHostDCGM) == config.BootstrapSkip
}

// parseTestsWithEnvironment parses args after replacing the named environment values.
func parseTestsWithEnvironment(t *testing.T, env map[string]string, unset []string, args ...string) config.TestOptions {
	t.Helper()
	for name := range env {
		unset = append(unset, name)
	}
	unsetTestEnvironment(t, unset...)
	for name, value := range env {
		t.Setenv(name, value)
	}
	opts, err := parseTests(args, io.Discard)
	if err != nil {
		t.Fatalf("parseTests(%v) error = %v", args, err)
	}
	return opts
}

// TestClusterLifecycleRunNameFlagOverridesInvalidEnvironment lets the run-name flag override an invalid env value.
func TestClusterLifecycleRunNameFlagOverridesInvalidEnvironment(t *testing.T) {
	unsetTestEnvironment(t, "E2E_RUN_NAME", "E2E_K3D_CLUSTER_NAME", "E2E_HELM_NAMESPACE", "E2E_HELM_RELEASE_NAME", "E2E_DCGM_NAMESPACE")
	t.Setenv("E2E_RUN_NAME", "bad name")
	opts, err := clusterLifecycleOptionsFromEnv()
	if err != nil {
		t.Fatalf("clusterLifecycleOptionsFromEnv() error = %v", err)
	}

	command := newTestsCLICommand(&opts, false)
	app := urfavecli.NewApp()
	app.Name = "e2e"
	app.Writer = io.Discard
	app.ErrWriter = io.Discard
	app.HideHelp = true
	app.Commands = []*urfavecli.Command{command}
	if err := app.Run([]string{"e2e", "tests", "--run-name=valid"}); err != nil {
		t.Fatalf("cluster lifecycle parse error = %v", err)
	}
	if opts.RunName != "valid" || opts.ClusterName != "valid-gpu" || opts.Namespace != "valid" || opts.ReleaseName != "valid" {
		t.Fatalf("cluster lifecycle run-name = %#v, want valid-derived defaults", opts)
	}
}

// TestParseTestsRejectsInvalidWinningCustomEnvironment checks that an invalid winning custom environment value is rejected.
func TestParseTestsRejectsInvalidWinningCustomEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		envName   string
		envValue  string
		wantError string
	}{
		{name: "bootstrap mode", envName: "E2E_BOOTSTRAP", envValue: "invalid", wantError: "E2E_BOOTSTRAP"},
		{name: "bootstrap component", envName: "E2E_BOOTSTRAP_COMPONENTS", envValue: "docker=invalid", wantError: "E2E_BOOTSTRAP_COMPONENTS"},
		{name: "result markers", envName: "E2E_RESULT_MARKERS", envValue: "invalid", wantError: "E2E_RESULT_MARKERS"},
		{name: "suite result markers", envName: "E2E_SUITE_RESULT_MARKERS", envValue: "invalid", wantError: "E2E_SUITE_RESULT_MARKERS"},
		{name: "run name", envName: "E2E_RUN_NAME", envValue: "bad name", wantError: "run name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unsetTestEnvironment(t, test.envName)
			t.Setenv(test.envName, test.envValue)
			if _, err := parseTests(nil, io.Discard); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("parseTests() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

// TestParseTestsVerboseAliasWinsOverEnvironment keeps the explicit -v alias authoritative over E2E_VERBOSE.
func TestParseTestsVerboseAliasWinsOverEnvironment(t *testing.T) {
	for _, env := range []string{"false", "invalid"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("E2E_VERBOSE", env)
			opts, err := parseTests([]string{"-v"}, io.Discard)
			if err != nil || !opts.Verbose {
				t.Fatalf("parseTests(-v) with E2E_VERBOSE=%s = verbose %t, error %v; want true, nil", env, opts.Verbose, err)
			}
		})
	}
}

// TestParseTestsNoResultMarkersFalseKeepsEnvironment checks that --no-result-markers=false leaves E2E_RESULT_MARKERS in effect.
func TestParseTestsNoResultMarkersFalseKeepsEnvironment(t *testing.T) {
	t.Setenv("E2E_RESULT_MARKERS", "true")
	opts, err := parseTests([]string{"--no-result-markers=false"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.ResultMarkers || !opts.E2EResultMarkers {
		t.Fatalf("markers = %t/%t, want the environment value true", opts.ResultMarkers, opts.E2EResultMarkers)
	}
}

// TestParseTestsEmptyCleanupEnvironmentDisablesCleanup checks that an empty cleanup environment value disables cleanup.
func TestParseTestsEmptyCleanupEnvironmentDisablesCleanup(t *testing.T) {
	t.Setenv("E2E_CLEANUP", "")
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Cleanup {
		t.Fatal("E2E_CLEANUP= kept cleanup enabled; urfave treats an empty boolean environment value as false")
	}
}

// TestParseTestsValidCleanupFlagWinsOverInvalidEnvironment keeps explicit CLI input authoritative.
func TestParseTestsValidCleanupFlagWinsOverInvalidEnvironment(t *testing.T) {
	t.Setenv("E2E_CLEANUP", "invalid")
	for _, test := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "explicit true", args: []string{"--cleanup"}, want: true},
		{name: "explicit false", args: []string{"--cleanup=false"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, err := parseTests(test.args, io.Discard)
			if err != nil {
				t.Fatalf("parseTests() error = %v", err)
			}
			if opts.Cleanup != test.want {
				t.Fatalf("Cleanup = %t, want %t", opts.Cleanup, test.want)
			}
		})
	}
}

// TestParseTestsRejectsInvalidWinningCleanupEnvironment rejects an invalid environment default.
func TestParseTestsRejectsInvalidWinningCleanupEnvironment(t *testing.T) {
	t.Setenv("E2E_CLEANUP", "invalid")
	if _, err := parseTests(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "E2E_CLEANUP") {
		t.Fatalf("parseTests() error = %v, want E2E_CLEANUP parse error", err)
	}
}

// TestParseTestsAcceptsPackagedRunFlags checks that packaged run flags are accepted by the tests command.
func TestParseTestsAcceptsPackagedRunFlags(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := parseTests([]string{
		"--tools-dir", "/tmp/e2e-tools",
		"--bootstrap=install",
		"--docker-registry-login", "registry.example,user,/run/secret",
		"--docker-registry-login-file", "/run/logins",
		"--k8s-image-pull-secret", "pull-secret",
		"--exporter-image", "registry.example/dcgm-exporter:dev@" + testImageDigest,
		"--dcgm-image", "nvcr.io/nvidia/cloud-native/dcgm:4.0.0-1-ubuntu22.04@" + testImageDigest,
		"--k8s-runtime-class", "nvidia",
		"--k8s-node-selector-key", "nvidia.com/gpu.present",
		"--k8s-node-selector-value", "true",
		"--k3d-ip-family", "dualstack",
		"--wait-timeout", "3m",
		"--shared-gpu-replicas", "4",
		"--gpu-operator", "existing",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseTests() error = %v; stderr = %s", err, stderr.String())
	}

	if opts.BootstrapMode != config.BootstrapInstall || opts.BootstrapSource != config.ValueCLI {
		t.Fatalf("bootstrap mode = %#v", opts)
	}
	if opts.ToolsDir != "/tmp/e2e-tools" {
		t.Fatalf("tools dir = %q", opts.ToolsDir)
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
	if opts.WaitTimeout != "3m" {
		t.Fatalf("wait timeout = %q, want 3m", opts.WaitTimeout)
	}
}

func TestParseTestsRejectsInvalidWaitTimeout(t *testing.T) {
	for _, value := range []string{"invalid", "0", "-1s"} {
		t.Run(value, func(t *testing.T) {
			_, err := parseTests([]string{"--wait-timeout", value}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "--wait-timeout must be a positive Go duration") {
				t.Fatalf("parseTests() error = %v", err)
			}
		})
	}
}

// TestParseTestsTimeoutPrecedenceAndValidation checks timeout precedence and non-negative durations.
func TestParseTestsTimeoutPrecedenceAndValidation(t *testing.T) {
	t.Setenv("E2E_TIMEOUT", "invalid")
	opts, err := parseTests([]string{"--timeout", "2s"}, io.Discard)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if opts.Timeout != "2s" {
		t.Fatalf("Timeout = %q, want 2s", opts.Timeout)
	}

	for _, value := range []string{"invalid", "-1s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("E2E_TIMEOUT", value)
			_, err := parseTests(nil, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "--timeout must be a non-negative Go duration") {
				t.Fatalf("parseTests() error = %v", err)
			}
		})
	}
}

// TestTimeoutZeroKeepsParentDeadline checks unlimited execution preserves caller cancellation.
func TestTimeoutZeroKeepsParentDeadline(t *testing.T) {
	for _, value := range []string{"", "0", "0s"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("E2E_TIMEOUT", "invalid")
			opts, err := parseTests([]string{"--timeout", value}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			parent, stop := context.WithCancel(context.Background())
			defer stop()
			ctx, cancel, err := timeoutContext(parent, opts.Timeout)
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			if ctx != parent || ctx.Err() != nil {
				t.Fatal("unlimited timeout replaced or canceled its parent")
			}
			stop()
			if ctx.Err() != context.Canceled {
				t.Fatal("unlimited timeout lost parent cancellation")
			}
		})
	}
}

// TestTimeoutContextCancelsWholeRunContext confirms the execution deadline cancels its context.
func TestTimeoutContextCancelsWholeRunContext(t *testing.T) {
	ctx, cancel, err := timeoutContext(context.Background(), "10ms")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("whole-run timeout did not cancel context")
	}
}

func TestEnsureDockerImageAvailablePullsMissingImage(t *testing.T) {
	var stdout bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker image inspect registry.example/dcgm-exporter:dev": {ExitCode: 1},
	}}
	err := ensureDockerImageAvailable(context.Background(), &stdout, runner, config.TestOptions{}, "registry.example/dcgm-exporter:dev")
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
	t.Setenv("E2E_K3D_CGROUP_V1", "true")
	t.Setenv("E2E_DOCKER_REGISTRY_LOGIN", "registry.example,user,/run/secret\nregistry2.example,user2,/run/secret2")
	t.Setenv("E2E_DOCKER_CONFIG_DIR", "/tmp/docker-config")
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
	checkList(t, "suites", opts.Suites, "host", "container")
	checkList(t, "skip suites", opts.SkipSuites, "static")
	checkList(t, "scenarios", opts.Scenarios, "host/startupMetrics", "container/imageStartup")
	checkList(t, "skip scenarios", opts.SkipScenarios, "host/reload")
	if opts.Kubeconfig != "/tmp/kubeconfig" || opts.DockerConfigDir != "/tmp/docker-config" {
		t.Fatalf("env fields missing: %#v", opts)
	}
	if opts.K3dCgroupV1 != "true" {
		t.Fatalf("k3d cgroup v1 environment value = %q, want true", opts.K3dCgroupV1)
	}
	if len(opts.DockerRegistryLogins) != 2 || opts.DockerRegistryLogins[0] != "registry.example,user,/run/secret" || opts.DockerRegistryLogins[1] != "registry2.example,user2,/run/secret2" {
		t.Fatalf("registry env = %#v", opts.DockerRegistryLogins)
	}
	hasMIGSelection := opts.MIGInstanceEntityID == "101" && opts.MIGInstanceNVMLID == "7"
	hasUnsupportedField := opts.UnsupportedFieldCandidate != ""
	if !hasMIGSelection || !hasUnsupportedField {
		t.Fatalf("suite env fields missing: %#v", opts)
	}
	if opts.E2EResultMarkers {
		t.Fatalf("E2E result markers = true, want false from env")
	}
	if !opts.BuildOnly || !opts.ListScenarios {
		t.Fatalf("build-only/list-scenarios env fields missing: %#v", opts)
	}
	if opts.MIGConfigure != "auto" {
		t.Fatalf("MIG configure default = %q, want auto", opts.MIGConfigure)
	}
}

// checkList fails the test when values differ from want, entry by entry.
func checkList(t *testing.T, name string, values []string, want ...string) {
	t.Helper()
	if !slices.Equal(values, want) {
		t.Fatalf("%s = %#v, want %#v", name, values, want)
	}
}

// TestRunNameDerivesResourceNamesAndAllowsOverrides uses run-name defaults but honors explicit resource names.
func TestRunNameDerivesResourceNamesAndAllowsOverrides(t *testing.T) {
	t.Setenv("E2E_RUN_NAME", "ci-17")
	unsetTestEnvironment(t, "E2E_K3D_CLUSTER_NAME", "E2E_HELM_NAMESPACE", "E2E_HELM_RELEASE_NAME", "E2E_DCGM_NAMESPACE")
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if opts.RunName != "ci-17" || opts.ClusterName != "ci-17-gpu" || opts.Namespace != "ci-17" || opts.ReleaseName != "ci-17" {
		t.Fatalf("run-name defaults = %#v", opts)
	}
	if cfg := clusterConfig(t.TempDir(), opts); cfg.DCGMNamespace != "ci-17-dcgm" {
		t.Fatalf("DCGM namespace = %q, want ci-17-dcgm", cfg.DCGMNamespace)
	}

	opts, err = parseTests([]string{"--run-name", "smoke-4", "--helm-namespace", "exporter-test"}, io.Discard)
	if err != nil {
		t.Fatalf("parseTests() with overrides error = %v", err)
	}
	if opts.ClusterName != "smoke-4-gpu" || opts.Namespace != "exporter-test" || opts.ReleaseName != "smoke-4" || opts.DCGMNamespace != "exporter-test-dcgm" {
		t.Fatalf("run-name overrides = %#v", opts)
	}
}

// TestClusterConfigKeepsHelmStateRootIndependentOfKubeconfig covers both cluster modes.
func TestClusterConfigKeepsHelmStateRootIndependentOfKubeconfig(t *testing.T) {
	root := t.TempDir()
	for _, kubeconfig := range []string{"", "/caller/config"} {
		cfg := clusterConfig(root, config.TestOptions{RunName: "helm-run", Kubeconfig: kubeconfig})
		if cfg.Root != root || cfg.RunName != "helm-run" || cfg.Kubeconfig != kubeconfig {
			t.Fatalf("cluster config lost artifact root or caller selection: %#v", cfg)
		}
	}
}

// TestHelmNamespaceOverrideDerivesDCGMNamespaceWithoutRunName derives the DCGM namespace without a run name.
func TestHelmNamespaceOverrideDerivesDCGMNamespaceWithoutRunName(t *testing.T) {
	unsetTestEnvironment(t, "E2E_RUN_NAME", "E2E_HELM_NAMESPACE", "E2E_DCGM_NAMESPACE")

	opts, err := parseTests([]string{"--helm-namespace", "exporter-test"}, io.Discard)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if opts.RunName != cluster.DefaultRunName || opts.Namespace != "exporter-test" || opts.DCGMNamespace != "exporter-test-dcgm" {
		t.Fatalf("namespace overrides = %#v, want default run name and exporter-test-dcgm", opts)
	}
}

// TestHelmNamespaceEnvironmentOverrideDerivesDCGMNamespace derives the DCGM namespace from an environment override.
func TestHelmNamespaceEnvironmentOverrideDerivesDCGMNamespace(t *testing.T) {
	unsetTestEnvironment(t, "E2E_RUN_NAME", "E2E_DCGM_NAMESPACE")
	t.Setenv("E2E_HELM_NAMESPACE", "env-exporter")

	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if opts.Namespace != "env-exporter" || opts.DCGMNamespace != "env-exporter-dcgm" {
		t.Fatalf("namespace environment overrides = %#v, want env-exporter and env-exporter-dcgm", opts)
	}
}

// unsetTestEnvironment removes the test environment variables used by CLI parsing tests.
func unsetTestEnvironment(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		value, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("Unsetenv(%q) error = %v", name, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func TestParseTestsAcceptsConfiguredScenarioFromEnvironment(t *testing.T) {
	t.Setenv("E2E_SCENARIO", nvsdmMockScenario)
	t.Setenv("E2E_DCGM_NVSDM_MOCK_YAML", "/tmp/mock.yaml")

	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatalf("parseTests() error = %v", err)
	}
	if len(opts.Scenarios) != 1 || opts.Scenarios[0] != nvsdmMockScenario {
		t.Fatalf("scenarios = %#v", opts.Scenarios)
	}
	if opts.DCGMNVSDMMockYAML != "/tmp/mock.yaml" {
		t.Fatalf("NVSDM mock YAML = %q", opts.DCGMNVSDMMockYAML)
	}
}

// TestImageFlagUsesUrfavePrecedence checks that urfave environment precedence is used for the image flag.
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
			app.Commands = []*urfavecli.Command{newTestsCLICommand(&opts, true)}
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

// TestImageFlagUsesConfiguredDefault checks that the configured image is used when no flag or environment override is set.
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
	app.Commands = []*urfavecli.Command{newTestsCLICommand(&opts, true)}
	if err := app.Run([]string{"e2e", "tests"}); err != nil {
		t.Fatal(err)
	}
	if opts.ExporterImage != "default/image:tag" {
		t.Fatalf("ExporterImage = %q, want configured default", opts.ExporterImage)
	}
}

func TestMixedMIGRunSplitsK8sScenarios(t *testing.T) {
	tests := []struct {
		name string
		opts config.TestOptions
		want bool
	}{
		{
			name: "default selection",
			opts: config.TestOptions{MIGConfigure: "true"},
			want: true,
		},
		{
			name: "explicit k8s suite",
			opts: config.TestOptions{MIGConfigure: "true", Suites: []string{"k8s"}},
			want: true,
		},
		{
			name: "explicit baseline and MIG scenarios",
			opts: config.TestOptions{
				MIGConfigure: "true",
				Scenarios:    []string{"k8s/default", "k8s/podUID", "k8s/hpcJobMapping", "k8s/mig"},
			},
			want: true,
		},
		{
			name: "MIG scenario only",
			opts: config.TestOptions{MIGConfigure: "true", Scenarios: []string{"k8s/mig"}},
			want: false,
		},
		{
			name: "baseline scenario only",
			opts: config.TestOptions{MIGConfigure: "true", Scenarios: []string{"k8s/default"}},
			want: false,
		},
		{
			name: "MIG mutation not authorized",
			opts: config.TestOptions{MIGConfigure: "auto"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitMixedMIGRun(tt.opts); got != tt.want {
				t.Fatalf("splitMixedMIGRun() = %t, want %t", got, tt.want)
			}
		})
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

// TestRunK8sSuiteStopsMIGPhaseAfterSetupFailure checks that a setup failure stops the later split MIG phase.
func TestRunK8sSuiteStopsMIGPhaseAfterSetupFailure(t *testing.T) {
	root := t.TempDir()
	writeSplitMIGTestRoot(t, root)
	var stdout bytes.Buffer
	runner := &fakeRunner{
		resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
			if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "get --raw=/readyz") {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("API unavailable")}, true
			}
			return e2eexec.Result{}, false
		},
	}
	err := runK8sSuite(
		context.Background(),
		&stdout,
		root,
		suite.NewTestBinaryManager(root, runner),
		runner,
		config.Config{TestOptions: splitMIGTestOptions()},
	)
	code, ok := ExitCode(err)
	if !ok || code != exitSetupFailure {
		t.Fatalf("runK8sSuite() error = %v, want setup failure", err)
	}
	if strings.Contains(stdout.String(), "k8s maximum-coverage phase: MIG") {
		t.Fatalf("MIG phase ran after setup failure:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_setup") {
		t.Fatalf("first-phase setup failure marker missing:\n%s", stdout.String())
	}
}

// TestRunK8sSuiteSplitMIGSkipsUnsupportedPhaseOnly checks that a split MIG run skips only the unsupported phase.
func TestRunK8sSuiteSplitMIGSkipsUnsupportedPhaseOnly(t *testing.T) {
	tests := []struct {
		name         string
		allocatable  string
		wantExecuted string
	}{
		{
			name:         "GPU-only target skips MIG phase",
			allocatable:  `"nvidia.com/gpu":"1"`,
			wantExecuted: "default",
		},
		{
			name:         "MIG-only target skips full-GPU phase",
			allocatable:  `"nvidia.com/mig-1g.10gb":"1"`,
			wantExecuted: "mig",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeSplitMIGTestRoot(t, root)
			runner := splitMIGRunner(tt.allocatable)
			var stdout bytes.Buffer
			err := runK8sSuite(
				context.Background(),
				&stdout,
				root,
				suite.NewTestBinaryManager(root, runner),
				runner,
				config.Config{TestOptions: splitMIGTestOptions()},
			)
			if err != nil {
				t.Fatalf("runK8sSuite() error = %v, want one supported phase to run", err)
			}

			output := stdout.String()
			requireOutputCount(t, output, "&&&& SKIPPED dcgm_exporter_e2e_setup", 1)
			requireOutputCount(t, output, "&&&& PASSED dcgm_exporter_e2e_setup", 1)
			requireOutputCount(t, output, "&&&& FAILED dcgm_exporter_e2e_setup", 0)
			if !runner.hasCommandArgContaining("--ginkgo.label-filter=" + tt.wantExecuted) {
				t.Fatalf("supported %s phase did not execute; commands = %#v", tt.wantExecuted, runner.commands)
			}
		})
	}
}

// TestRunK8sSuiteSplitMIGFailsWhenBothPhasesAreSkipped checks that a split MIG run fails when both phases are skipped.
func TestRunK8sSuiteSplitMIGFailsWhenBothPhasesAreSkipped(t *testing.T) {
	root := t.TempDir()
	writeSplitMIGTestRoot(t, root)
	runner := splitMIGRunner(``)
	var stdout bytes.Buffer
	err := runK8sSuite(
		context.Background(),
		&stdout,
		root,
		suite.NewTestBinaryManager(root, runner),
		runner,
		config.Config{TestOptions: splitMIGTestOptions()},
	)
	code, ok := ExitCode(err)
	allSelectedSkipped := err != nil && strings.Contains(err.Error(), "all selected Kubernetes scenarios were skipped")
	if !ok || code != exitSetupFailure || !allSelectedSkipped {
		t.Fatalf("runK8sSuite() error = %v, want final setup failure for all-skipped phases", err)
	}
	output := stdout.String()
	if got := strings.Count(output, "&&&& SKIPPED dcgm_exporter_e2e_setup"); got != 1 {
		t.Fatalf("setup SKIPPED marker count = %d, want 1 for the tolerated first phase:\n%s", got, output)
	}
	if got := strings.Count(output, "&&&& FAILED dcgm_exporter_e2e_setup"); got != 1 {
		t.Fatalf("setup FAILED marker count = %d, want one final failure:\n%s", got, output)
	}
	if strings.Contains(output, "&&&& PASSED dcgm_exporter_e2e_setup") {
		t.Fatalf("all-skipped phases passed the setup marker:\n%s", output)
	}
	if strings.Count(output, "k8s maximum-coverage phase:") != 2 {
		t.Fatalf("both split phases were not attempted:\n%s", output)
	}
	for _, command := range runner.commands {
		if strings.HasSuffix(command.Name, "dcgm-exporter-k8s.test") {
			t.Fatalf("all-skipped phases unexpectedly executed the suite binary: %#v", runner.commands)
		}
	}
}

// splitMIGTestOptions returns options for the two-phase MIG scenario tests.
func splitMIGTestOptions() config.TestOptions {
	return config.TestOptions{
		Scenarios:         []string{"k8s/default", "k8s/mig"},
		MIGConfigure:      "true",
		Kubeconfig:        "/tmp/kubeconfig",
		ExporterImage:     "registry.example/dcgm-exporter:dev",
		CUDAWorkloadImage: "registry.example/cuda-workload:dev",
		ResultMarkers:     true,
	}
}

// writeSplitMIGTestRoot creates the minimal module and Kubernetes test tree used by split MIG cases.
func writeSplitMIGTestRoot(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tests", "k8s"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// splitMIGRunner simulates Kubernetes readiness and node allocation for split MIG cases.
func splitMIGRunner(allocatable string) *fakeRunner {
	nodeList := `{"items":[{"status":{"allocatable":{` + allocatable + `}}}]}`
	return &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		joined := strings.Join(command.Args, " ")
		switch {
		case command.Name == "kubectl" && strings.Contains(joined, "get nodes -o json"):
			return e2eexec.Result{Stdout: []byte(nodeList)}, true
		case command.Name == "kubectl" && strings.Contains(joined, "get --raw=/readyz"):
			return e2eexec.Result{Stdout: []byte("ok\n")}, true
		case command.Name == "kubectl" && strings.Contains(joined, "get --raw /api/v1/nodes"):
			return e2eexec.Result{Stdout: []byte(`{"items":[]}`)}, true
		default:
			return e2eexec.Result{}, false
		}
	}}
}

// TestRunStaticSuiteExplicitlyEnablesMarkers checks that an explicit static suite selection enables result markers.
func TestRunStaticSuiteExplicitlyEnablesMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tests", "static"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	if err := runWithRootRunner([]string{"tests", "--suite", "static", "--scenario", "static/paths", "--result-markers"}, &stdout, &stderr, root, runner); err != nil {
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
	foundStaticBinary := false
	for _, command := range runner.commands {
		if !strings.HasSuffix(command.Name, "dcgm-exporter-static.test") {
			continue
		}
		foundStaticBinary = true
		if command.Dir != filepath.Join(root, "tests", "static") {
			t.Fatalf("static suite directory = %q, want package directory", command.Dir)
		}
	}
	if !foundStaticBinary {
		t.Fatalf("static suite binary was not run: %#v", runner.commands)
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
		opts config.TestOptions
		want bool
	}{
		{name: "enabled", opts: config.TestOptions{E2EResultMarkers: true}, want: true},
		{name: "suite markers disabled", opts: config.TestOptions{}, want: false},
		{name: "all markers disabled", opts: config.TestOptions{E2EResultMarkers: true, NoResultMarkers: true}, want: false},
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
	err := runMarkedSuite(&stdout, config.TestOptions{ResultMarkers: true}, "dcgm_exporter_e2e_skipped", func() error {
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

// TestRunHostSuiteUsesPackageAssets checks that the host suite receives assets from the package root.
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
		[]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--bootstrap=skip", "--result-markers"},
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
	hostDefinition, _ := suite.TestBinaryDefinitionFor(scenario.SuiteHost)
	requireArgs(t, "host command", hostCommand.Args,
		"-exporter-binary="+filepath.Join(root, "bin", "dcgm-exporter"),
		"--ginkgo.label-filter=startupMetrics",
		"--ginkgo.fail-on-empty",
		"^"+hostDefinition.TestName+"$",
	)
	requireArgs(t, "host command environment", hostCommand.Env, "E2E_SUITE_RESULT_MARKERS=true")
	requireOutput(t, stdout.String(),
		"[e2e] === Host suite ===",
		"[e2e] === Host scenarios ===",
		"&&&& RUNNING dcgm_exporter_e2e_integration_host",
		"&&&& PASSED dcgm_exporter_e2e_integration_host",
	)
}

func TestRunHostSuitePassesNVMLInjectionInputs(t *testing.T) {
	for _, label := range []string{"nvmlInjectionMetrics", "migXidCounters"} {
		t.Run(label, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "etc"), filepath.Join(root, "tests", "host", "testdata")} {
				require.NoError(t, os.MkdirAll(dir, 0o755))
			}
			for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter", "e2e-dcgm-probe"} {
				require.NoError(t, writeExecutableFile(filepath.Join(root, "bin", name)))
			}
			fixture := filepath.Join(root, "fixture.yaml")
			require.NoError(t, os.WriteFile(fixture, []byte("version: 1\n"), 0o600))
			fields := filepath.Join(root, "etc", "go-dcgm-const-fields.go")
			require.NoError(t, os.WriteFile(fields, []byte("package dcgm\n"), 0o600))

			var stdout, stderr bytes.Buffer
			runner := &fakeRunner{}
			err := runWithRootRunner([]string{
				"tests", "--suite", "host", "--scenario", "host/" + label, "--bootstrap=skip",
				"--dcgm-nvml-injection-yaml", fixture,
			}, &stdout, &stderr, root, runner)
			require.NoError(t, err, "stderr = %s", stderr.String())

			hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
			require.True(t, ok, "host binary was not run; commands = %#v", runner.commands)
			for _, want := range []string{"NVML_INJECTION_MODE=True", "NVML_YAML_FILE=" + fixture} {
				require.Contains(t, hostCommand.Env, want)
			}
			for _, want := range []string{
				"--ginkgo.label-filter=" + label,
				"-dcgm-probe-binary=" + filepath.Join(root, "bin", "e2e-dcgm-probe"),
				"-dcgm-fields-file=" + fields,
			} {
				require.Contains(t, hostCommand.Args, want)
			}
		})
	}
}

func TestRunHostSuitePassesNVSDMMockInput(t *testing.T) {
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
	fixture := filepath.Join(root, "mock.yaml")
	if err := os.WriteFile(fixture, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fields := filepath.Join(root, "etc", "go-dcgm-const-fields.go")
	if err := os.WriteFile(fields, []byte("package dcgm\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{onRun: func(command e2eexec.Command) {
		if strings.Contains(strings.Join(command.Args, " "), "libnvml_injection") {
			t.Fatalf("NVSDM mock unexpectedly checked for the NVML injection library: %#v", command)
		}
	}}
	err := runWithRootRunner([]string{
		"tests", "--suite", "host", "--scenario", "host/nvsdmMockMetrics", "--bootstrap=skip",
		"--dcgm-nvsdm-mock-yaml", fixture,
	}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
	if !ok {
		t.Fatalf("host binary was not run; commands = %#v", runner.commands)
	}
	if !hasArg(hostCommand.Env, "DCGM_NVSDM_MOCK_YAML="+fixture) {
		t.Fatalf("host command missing NVSDM mock environment: %#v", hostCommand.Env)
	}
	if !hasArg(hostCommand.Args, "--ginkgo.label-filter=nvsdmMockMetrics") {
		t.Fatalf("host command missing exact NVSDM label filter: %#v", hostCommand.Args)
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

func TestRunHostSuitePassesGPULinkFieldProbeInputs(t *testing.T) {
	t.Setenv(hostDCGMHostengineAddressEnv, "127.0.0.1:5556")
	root, fields := writeHostSuitePackageRoot(t, true)
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{
		"tests", "--suite", "host", "--scenario", "host/gpuLinkFields", "--bootstrap=skip",
	}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
	if !ok {
		t.Fatalf("host binary was not run; commands = %#v", runner.commands)
	}
	if !hasArg(hostCommand.Args, "--ginkgo.label-filter=gpuLinkFields") {
		t.Fatalf("host command missing exact GPU-link label filter: %#v", hostCommand.Args)
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

// TestRunHostProfilingReloadDoesNotNeedGPULinkProbeInputs keeps profiling reload independent of GPU-link inputs.
func TestRunHostProfilingReloadDoesNotNeedGPULinkProbeInputs(t *testing.T) {
	t.Setenv(hostDCGMHostengineAddressEnv, "")
	root, _ := writeHostSuitePackageRoot(t, false)
	var stdout, stderr bytes.Buffer
	fixture := &hostProfilingProbeFixture{caseData: hostProfilingProbeCase{
		profile: e2eexec.Result{Stdout: []byte("DCGM_FI_PROF_GR_ENGINE_ACTIVE")},
	}}
	runner := &fakeRunner{resultMatcher: fixture.match}
	err := runWithRootRunner([]string{
		"tests", "--suite", "host", "--scenario", "host/profilingReload", "--bootstrap=skip",
	}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	hostCommand, ok := runner.commandNamedSuffix("dcgm-exporter-host.test")
	if !ok {
		t.Fatalf("host binary was not run; commands = %#v", runner.commands)
	}
	if !hasArg(hostCommand.Args, "--ginkgo.label-filter=profilingReload") {
		t.Fatalf("host command missing exact profiling label filter: %#v", hostCommand.Args)
	}
	for _, arg := range hostCommand.Args {
		if strings.HasPrefix(arg, "-dcgm-probe-binary=") || strings.HasPrefix(arg, "-dcgm-fields-file=") {
			t.Fatalf("profiling command has GPU-link probe input %q: %#v", arg, hostCommand.Args)
		}
	}
}

func TestRunHostGPULinkFieldsRequiresPrivateHostengineAddress(t *testing.T) {
	t.Setenv(hostDCGMHostengineAddressEnv, " ")
	root, _ := writeHostSuitePackageRoot(t, false)
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{
		"tests", "--suite", "host", "--scenario", "host/gpuLinkFields", "--bootstrap=skip",
	}, io.Discard, io.Discard, root, runner)
	if err == nil || !strings.Contains(err.Error(), hostDCGMHostengineAddressEnv) {
		t.Fatalf("runWithRootRunner() error = %v, want a clear %s requirement", err, hostDCGMHostengineAddressEnv)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("host setup ran before validating %s: %#v", hostDCGMHostengineAddressEnv, runner.commands)
	}
}

func writeHostSuitePackageRoot(t *testing.T, includeProbe bool) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "etc"), filepath.Join(root, "tests", "host", "testdata")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binaries := []string{"dcgm-exporter-host.test", "dcgm-exporter"}
	if includeProbe {
		binaries = append(binaries, "e2e-dcgm-probe")
	}
	for _, name := range binaries {
		if err := writeExecutableFile(filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	fields := filepath.Join(root, "etc", "go-dcgm-const-fields.go")
	if includeProbe {
		if err := os.WriteFile(fields, []byte("package dcgm\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, fields
}

// TestPrepareHostGroupRuntimeKeepsSpecialInputsOutOfBaseline keeps special probe inputs out of the baseline group.
func TestPrepareHostGroupRuntimeKeepsSpecialInputsOutOfBaseline(t *testing.T) {
	runner := &fakeRunner{onRun: func(command e2eexec.Command) {
		t.Fatalf("baseline group unexpectedly ran special setup: %#v", command)
	}}
	opts := config.TestOptions{
		DCGMNVMLInjectionYAML: "/tmp/injection.yaml",
		DCGMNVSDMMockYAML:     "/tmp/nvsdm.yaml",
	}
	args, env, err := prepareHostGroupRuntime(
		context.Background(),
		io.Discard,
		suite.NewTestBinaryManager(t.TempDir(), runner),
		runner,
		opts,
		scenario.GroupHostBaseline,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 0 {
		t.Fatalf("baseline probe args = %#v, want none", args)
	}
	for _, value := range env {
		if strings.HasPrefix(value, "NVML_") || strings.HasPrefix(value, "DCGM_NVSDM_") {
			t.Fatalf("baseline environment contains special input %q", value)
		}
	}
}

// TestPrepareHostGPULoadRuntimeNamesTheDCGMImage gives the GPU load group the
// configured DCGM image and the session's Docker configuration.
func TestPrepareHostGPULoadRuntimeNamesTheDCGMImage(t *testing.T) {
	runner := &fakeRunner{onRun: func(command e2eexec.Command) {
		t.Fatalf("GPU load group unexpectedly ran setup: %#v", command)
	}}
	opts := config.TestOptions{
		DCGMImage:       "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04",
		DockerConfigDir: "/tmp/e2e-docker-config",
	}
	args, env, err := prepareHostGroupRuntime(
		context.Background(), io.Discard, suite.NewTestBinaryManager(t.TempDir(), runner), runner, opts, scenario.GroupHostGPULoad,
	)
	require.NoError(t, err)
	require.Empty(t, args)
	require.Contains(t, env, "DCGM_IMAGE=nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04")
	require.Contains(t, env, "DOCKER_CONFIG=/tmp/e2e-docker-config")
}

// TestPrepareHostGPUBindUnbindRuntimeAllowsTheUnbind sets the opt-in variable
// for the bind/unbind group only, so no other host group lets a test unbind a GPU.
func TestPrepareHostGPUBindUnbindRuntimeAllowsTheUnbind(t *testing.T) {
	runner := &fakeRunner{onRun: func(command e2eexec.Command) {
		t.Fatalf("bind/unbind group unexpectedly ran setup: %#v", command)
	}}

	for group, want := range map[string]bool{
		scenario.GroupHostGPUBindUnbind: true,
		scenario.GroupHostGPULoad:       false,
		scenario.GroupHostBaseline:      false,
	} {
		_, env, err := prepareHostGroupRuntime(
			context.Background(), io.Discard, suite.NewTestBinaryManager(t.TempDir(), runner), runner, config.TestOptions{}, group,
		)
		require.NoError(t, err, group)
		require.Equal(t, want, slices.Contains(env, "E2E_ALLOW_GPU_UNBIND=1"), group)
	}
}

// prepareConfigInjectionRuntime prepares the config-injection host group against a fake
// runner that does or does not find the NVML injection library.
func prepareConfigInjectionRuntime(t *testing.T, opts config.TestOptions, libraryPresent bool) ([]string, []string, error) {
	t.Helper()
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if command.Name == "sh" && strings.Contains(strings.Join(command.Args, " "), "libnvml_injection") {
			if libraryPresent {
				return e2eexec.Result{}, true
			}
			return e2eexec.Result{ExitCode: 1}, true
		}
		return e2eexec.Result{}, false
	}}
	root, _ := writeHostSuitePackageRoot(t, true)
	return prepareHostGroupRuntime(context.Background(), io.Discard, suite.NewTestBinaryManager(root, runner), runner, opts, scenario.GroupHostConfigInjection)
}

// TestPrepareHostConfigInjectionRuntimeWithFixture gives the group its probe
// arguments and sets the NVML injection environment when a fixture is given.
func TestPrepareHostConfigInjectionRuntimeWithFixture(t *testing.T) {
	injected := config.TestOptions{DCGMNVMLInjectionYAML: "/tmp/injection.yaml"}
	args, env, err := prepareConfigInjectionRuntime(t, injected, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || !strings.HasPrefix(args[0], "-dcgm-probe-binary=") || !strings.HasPrefix(args[1], "-dcgm-fields-file=") {
		t.Fatalf("probe args = %#v, want the probe binary and fields file", args)
	}
	for _, want := range []string{"NVML_INJECTION_MODE=True", "NVML_YAML_FILE=/tmp/injection.yaml"} {
		if !slices.Contains(env, want) {
			t.Fatalf("environment %#v is missing %q", env, want)
		}
	}
}

// TestPrepareHostConfigInjectionRuntimeFailsWithoutTheLibrary fails when a
// fixture is given and the injection library is missing.
func TestPrepareHostConfigInjectionRuntimeFailsWithoutTheLibrary(t *testing.T) {
	injected := config.TestOptions{DCGMNVMLInjectionYAML: "/tmp/injection.yaml"}
	if _, _, err := prepareConfigInjectionRuntime(t, injected, false); err == nil || !strings.Contains(err.Error(), "libnvml_injection.so was not found") {
		t.Fatalf("missing library error = %v, want libnvml_injection.so was not found", err)
	}
}

// TestPrepareHostConfigInjectionRuntimeWithoutFixture runs the scenarios on the
// host's own GPUs, so no NVML injection environment is set.
func TestPrepareHostConfigInjectionRuntimeWithoutFixture(t *testing.T) {
	args, env, err := prepareConfigInjectionRuntime(t, config.TestOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 {
		t.Fatalf("probe args without a fixture = %#v, want the probe binary and fields file", args)
	}
	for _, value := range env {
		if strings.HasPrefix(value, "NVML_") {
			t.Fatalf("environment without a fixture contains %q", value)
		}
	}
}

// hostProfilingProbeCase holds one profiling probe outcome and the expected setup or cleanup result.
type hostProfilingProbeCase struct {
	name        string
	mode        config.BootstrapMode
	profile     e2eexec.Result
	stopFailure bool
	wantErr     string
	wantSkip    bool
}

// hostProfilingProbeFixture records commands and state for an owned host profiling probe.
type hostProfilingProbeFixture struct {
	caseData    hostProfilingProbeCase
	address     string
	started     bool
	stopped     bool
	profiled    bool
	profileArgs []string
}

// match records probe start and stop commands and returns the configured profiling result.
func (f *hostProfilingProbeFixture) match(command e2eexec.Command) (e2eexec.Result, bool) {
	if isHostProfilingEngineCommand(command) {
		if hasArg(command.Args, "--term") {
			f.stopped = true
			if f.caseData.stopFailure {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("stop failed")}, true
			}
		} else {
			f.started = true
			f.address = hostProfilingEngineAddress(command.Args)
		}
		return e2eexec.Result{}, true
	}
	if command.Name == "dcgmi" && len(command.Args) > 0 && command.Args[0] == "profile" {
		f.profiled = true
		f.profileArgs = command.Args
		return f.caseData.profile, true
	}
	return e2eexec.Result{}, false
}

// isHostProfilingEngineCommand recognizes direct and sudo-wrapped hostengine commands.
func isHostProfilingEngineCommand(command e2eexec.Command) bool {
	return command.Name == "nv-hostengine" ||
		(command.Name == "sudo" && len(command.Args) > 0 && command.Args[0] == "nv-hostengine")
}

// hostProfilingEngineAddress extracts the local hostengine address from its port argument.
func hostProfilingEngineAddress(args []string) string {
	for i, arg := range args {
		if arg == "-p" && i+1 < len(args) {
			return "127.0.0.1:" + args[i+1]
		}
	}
	return ""
}

// assert checks the expected probe result, endpoint, and hostengine cleanup.
func (f *hostProfilingProbeFixture) assert(t *testing.T, err error) {
	t.Helper()
	if f.caseData.wantErr != "" {
		require.ErrorContains(t, err, f.caseData.wantErr)
	} else if !f.caseData.wantSkip {
		require.NoError(t, err)
	}
	require.Equal(t, f.caseData.wantSkip, errors.Is(err, errSuiteSkipped))
	require.True(t, f.profiled)
	require.True(t, containsArgs(f.profileArgs, "--host", f.address), "probe did not use the prepared endpoint: %v", f.profileArgs)
	require.Equal(t, f.caseData.mode == config.BootstrapInstall, f.started)
	require.Equal(t, f.started, f.stopped, "every owned probe must stop, including failed or unsupported probes")
}

// TestPrepareHostProfilingOwnsInstallProbe checks that install mode starts and stops a probe across outcomes.
func TestPrepareHostProfilingOwnsInstallProbe(t *testing.T) {
	cases := []hostProfilingProbeCase{
		{name: "install supported", mode: config.BootstrapInstall, profile: e2eexec.Result{Stdout: []byte("DCGM_FI_PROF_GR_ENGINE_ACTIVE")}},
		{name: "install unsupported", mode: config.BootstrapInstall, profile: e2eexec.Result{Stdout: []byte("Profiling is not supported")}, wantSkip: true},
		{name: "install unavailable", mode: config.BootstrapInstall, profile: e2eexec.Result{ExitCode: 1, Stderr: []byte("Error connecting to host")}, wantErr: "could not determine DCGM profiling support"},
		{name: "unsupported cleanup failure", mode: config.BootstrapInstall, profile: e2eexec.Result{Stdout: []byte("Profiling is not supported")}, stopFailure: true, wantErr: "clean up host profiling probe"},
		{name: "check prepared service", mode: config.BootstrapCheck, profile: e2eexec.Result{Stdout: []byte("DCGM_FI_PROF_GR_ENGINE_ACTIVE")}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := &hostProfilingProbeFixture{caseData: test, address: "localhost"}
			runner := &fakeRunner{resultMatcher: fixture.match}
			_, env, err := prepareHostGroupRuntime(context.Background(), io.Discard, suite.TestBinaryManager{}, runner,
				config.TestOptions{BootstrapMode: test.mode}, scenario.GroupHostProfiling)
			fixture.assert(t, err)
			if err == nil {
				require.Contains(t, env, "E2E_REQUIRE_PROFILING=1")
			} else {
				require.NotContains(t, env, "E2E_REQUIRE_PROFILING=1")
			}
		})
	}
}

// TestPrepareHostProfilingCheckFailsWithoutService reports a missing service as an error without starting a hostengine.
func TestPrepareHostProfilingCheckFailsWithoutService(t *testing.T) {
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"dcgmi discovery -l": {ExitCode: 1, Stderr: []byte("Error connecting to host")},
	}}
	err := prepareHostProfiling(context.Background(), io.Discard, runner, config.TestOptions{BootstrapMode: config.BootstrapCheck})
	require.ErrorContains(t, err, "start nvidia-dcgm or use --bootstrap=install")
	require.False(t, errors.Is(err, errSuiteSkipped))
	for _, command := range runner.commands {
		require.NotEqual(t, "sudo", command.Name)
		require.False(t, isHostProfilingEngineCommand(command), "check mode started a hostengine: %#v", command)
	}
}

// TestProfilingGroupRuntimeUsesIndependentCapabilityStatus maps every probe result to a group outcome.
func TestProfilingGroupRuntimeUsesIndependentCapabilityStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   capability.Status
		wantSkip bool
		wantErr  string
	}{
		{name: "supported", status: capability.StatusSupported},
		{name: "unsupported", status: capability.StatusUnsupported, wantSkip: true},
		{name: "unknown", status: capability.StatusUnknown, wantErr: "could not determine DCGM profiling support"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := profilingGroupRuntime(capability.Capability{Status: test.status, Reason: "probe evidence"})
			if test.wantErr == "" && !test.wantSkip && err != nil {
				t.Fatalf("profilingGroupRuntime() error = %v", err)
			}
			if test.wantSkip != errors.Is(err, errSuiteSkipped) {
				t.Fatalf("profilingGroupRuntime() skip = %t, want %t; error = %v", errors.Is(err, errSuiteSkipped), test.wantSkip, err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("profilingGroupRuntime() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestRunHostSuiteMissingDCGMSkipsImplicitHost(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"sh -c ldconfig -p 2>/dev/null | grep -Eq 'libdcgm\\.so(\\.4)?' || test -e /usr/lib/x86_64-linux-gnu/libdcgm.so.4 || test -e /usr/lib/aarch64-linux-gnu/libdcgm.so.4 || test -e /usr/lib64/libdcgm.so.4": {ExitCode: 1},
	}}
	err := runHostSuite(context.Background(), &stdout, suite.NewTestBinaryManager(root, runner), runner, config.Config{TestOptions: config.TestOptions{ResultMarkers: true}})
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
		"sh -c ldconfig -p 2>/dev/null | grep -Eq 'libdcgm\\.so(\\.4)?' || test -e /usr/lib/x86_64-linux-gnu/libdcgm.so.4 || test -e /usr/lib/aarch64-linux-gnu/libdcgm.so.4 || test -e /usr/lib64/libdcgm.so.4": {ExitCode: 1},
	}}
	err := runHostSuite(context.Background(), &stdout, suite.NewTestBinaryManager(root, runner), runner, config.Config{TestOptions: config.TestOptions{Suites: []string{"host"}, ResultMarkers: true}})
	if err == nil || !strings.Contains(err.Error(), "explicit host validation cannot run") {
		t.Fatalf("runHostSuite() error = %v, want explicit host failure", err)
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_integration_host") {
		t.Fatalf("missing failed marker:\n%s", stdout.String())
	}
}

// TestRunHostSuiteReportsSkippedGroupScenarioMarkers checks that skipped host groups emit scenario markers when enabled.
func TestRunHostSuiteReportsSkippedGroupScenarioMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter"} {
		if err := writeExecutableFile(filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	var stdout bytes.Buffer
	runner := &fakeRunner{}
	opts := config.TestOptions{
		Scenarios:        []string{"host/startupMetrics", "host/profilingReload"},
		ResultMarkers:    true,
		E2EResultMarkers: true,
	}
	if err := runHostSuite(context.Background(), &stdout, suite.NewTestBinaryManager(root, runner), runner, config.Config{TestOptions: opts}); err != nil {
		t.Fatalf("runHostSuite() error = %v", err)
	}
	for _, want := range []string{
		"host group host-profiling is unsupported: DCGM did not report profiling metric groups; skipping",
		"&&&& RUNNING dcgm_exporter_e2e_host_profiling_reload",
		"&&&& SKIPPED dcgm_exporter_e2e_host_profiling_reload",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing %q:\n%s", want, stdout.String())
		}
	}
}

// TestRunHostSuiteFailsExplicitSelectionWhenEveryGroupIsUnsupported fails explicit selection when every group is skipped.
func TestRunHostSuiteFailsExplicitSelectionWhenEveryGroupIsUnsupported(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dcgm-exporter-host.test", "dcgm-exporter"} {
		if err := writeExecutableFile(filepath.Join(root, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	var stdout bytes.Buffer
	runner := &fakeRunner{}
	opts := config.TestOptions{Scenarios: []string{"host/profilingReload"}, ResultMarkers: true}
	err := runHostSuite(context.Background(), &stdout, suite.NewTestBinaryManager(root, runner), runner, config.Config{TestOptions: opts})
	if err == nil || !strings.Contains(err.Error(), "explicit host validation did not run") {
		t.Fatalf("runHostSuite() error = %v, want explicit wholly skipped failure", err)
	}
	if _, ran := runner.commandNamedSuffix("dcgm-exporter-host.test"); ran {
		t.Fatalf("host binary ran for an unsupported group: %#v", runner.commands)
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_integration_host") {
		t.Fatalf("missing failed marker:\n%s", stdout.String())
	}
}

// TestRunHostSuiteSetsRemoteDCGMTransportPrerequisites sets transport inputs required by remote DCGM.
func TestRunHostSuiteSetsRemoteDCGMTransportPrerequisites(t *testing.T) {
	root := writeHostPackageRoot(t)

	tests := []struct {
		name      string
		scenario  string
		label     string
		wantVSOCK bool
	}{
		{name: "TCP", scenario: "host/dcgmUri", label: "dcgmUri"},
		{name: "Unix", scenario: "host/dcgmUriUnix", label: "dcgmUriUnix"},
		{name: "VSOCK with bootstrap skipped", scenario: "host/dcgmUriVSOCK", label: "dcgmUriVSOCK"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			runner := &fakeRunner{}
			err := runWithRootRunner([]string{"tests", "--suite", "host", "--scenario", test.scenario, "--bootstrap=skip", "--result-markers"}, &stdout, &stderr, root, runner)
			if err != nil {
				t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
			}

			commands := hostCommandsWithLabel(runner, test.label)
			if len(commands) != 1 {
				t.Fatalf("host %s runs = %d, want 1; commands = %#v", test.scenario, len(commands), runner.commands)
			}
			command := commands[0]
			requireArgs(t, "host command environment", command.Env, "E2E_REQUIRE_DCGM=1", "E2E_SUITE_RESULT_MARKERS=true")
			if hasArg(command.Env, "E2E_REQUIRE_VSOCK=1") != test.wantVSOCK {
				t.Fatalf("host command VSOCK prerequisite = %v, want %v: %#v", !test.wantVSOCK, test.wantVSOCK, command)
			}
			requireOutput(t, stdout.String(), "[e2e] === Host DCGM URI scenarios ===")
		})
	}
}

// writeHostPackageRoot creates a package root with host suite and product binaries
// and configures the package image environment.
func writeHostPackageRoot(t *testing.T) string {
	t.Helper()
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
	return root
}

// hostCommandsWithLabel returns host suite commands whose Ginkgo label filter is exactly label.
func hostCommandsWithLabel(runner *fakeRunner, label string) []e2eexec.Command {
	var commands []e2eexec.Command
	for _, command := range runner.commands {
		if strings.HasSuffix(command.Name, "dcgm-exporter-host.test") && hasArg(command.Args, "--ginkgo.label-filter="+label) {
			commands = append(commands, command)
		}
	}
	return commands
}

// TestRunHostSuiteUsesSuppliedProductBinary keeps host validation on the explicitly supplied build.
func TestRunHostSuiteUsesSuppliedProductBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tests", "host"), 0o755); err != nil {
		t.Fatal(err)
	}

	product := filepath.Join(root, "supplied-exporter")
	if err := writeExecutableFile(product); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--bootstrap=skip", "--exporter-binary", product}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v", err)
	}
	found := false
	for _, command := range runner.commands {
		if command.Name == "go" && containsArgs(command.Args, "build", "./cmd/dcgm-exporter") {
			t.Fatalf("host suite rebuilt the supplied product: %#v", command)
		}
		if strings.HasSuffix(command.Name, "dcgm-exporter-host.test") && containsArgs(command.Args, "-exporter-binary="+product) {
			found = true
		}
	}
	if !found {
		t.Fatalf("host suite did not use supplied binary: %#v", runner.commands)
	}
	if !strings.Contains(stdout.String(), "[e2e] product binary: "+product) {
		t.Fatalf("host suite did not report supplied binary identity:\n%s", stdout.String())
	}
}

// TestRunHostSuiteBuildsSourceProductFromNestedDirectory loads assets from a nested source product directory.
func TestRunHostSuiteBuildsSourceProductFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(root, "tests", "host")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	product := filepath.Join(root, "bin", "dcgm-exporter")
	writeStaleExecutable(t, product)

	unsetTestEnvironment(t, "E2E_EXPORTER_BINARY", "E2E_PREBUILT_BINARIES", "E2E_SUITE", "E2E_SCENARIO")
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{"tests", "--suite", "host", "--scenario", "host/startupMetrics", "--bootstrap=skip"}, &stdout, &stderr, start, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() from nested directory error = %v; stderr = %s", err, stderr.String())
	}

	if !buildsAndUsesProduct(runner.commands, root, product) {
		t.Fatalf("host suite did not build and use the source-root product: %#v", runner.commands)
	}
	requireOutput(t, stdout.String(), "[e2e] artifact mode: source-tree", "[e2e] product binary: "+product)
}

// buildsAndUsesProduct reports whether the last product build wrote product from
// root and the host suite binary received that product.
func buildsAndUsesProduct(commands []e2eexec.Command, root, product string) bool {
	buildsProduct := false
	usesProduct := false
	for _, command := range commands {
		if command.Name == "go" && containsArgs(command.Args, "build", "./cmd/dcgm-exporter") {
			buildsProduct = command.Dir == root && containsArgs(command.Args, "-o", product)
		}
		if strings.HasSuffix(command.Name, "dcgm-exporter-host.test") && containsArgs(command.Args, "-exporter-binary="+product) {
			usesProduct = true
		}
	}
	return buildsProduct && usesProduct
}

// writeStaleExecutable creates an executable at path with an hour-old modification time.
func writeStaleExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(path); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// TestRunContainerSuiteUsesEnvironmentImages honors image overrides from the environment.
func TestRunContainerSuiteUsesEnvironmentImages(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-container.test")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("E2E_EXPORTER_IMAGE", "registry.example/dcgm-exporter:dev-distroless-amd64@"+testImageDigest)
	t.Setenv("E2E_CONTAINER_TOOLKIT_TEST_IMAGE", "registry.example/cuda:dev")
	t.Setenv("NVIDIA_CONTAINER_TOOLKIT_VERSION", "1.19.1-1")
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
	requireArgs(t, "container command environment", containerCommand.Env,
		"E2E_REQUIRE_CONTAINER_IMAGES=1",
		"E2E_SUITE_RESULT_MARKERS=true",
		"EXPORTER_DISTROLESS_IMAGE=registry.example/dcgm-exporter@"+testImageDigest,
	)
	requireRunnerArgs(t, runner, "--password-stdin")
	login := runner.commandIndex("docker", "login", "registry.example", "-u", "user", "--password-stdin")
	exporterCheck := runner.commandIndex("docker", "image", "inspect", "registry.example/dcgm-exporter@"+testImageDigest)
	if login < 0 || exporterCheck < 0 || exporterCheck < login {
		t.Fatalf("container image check did not run after Docker login: %#v", runner.commands)
	}
	if !hasEnvPrefix(containerCommand.Env, "DOCKER_CONFIG=") {
		t.Fatalf("container command missing DOCKER_CONFIG env: %#v", containerCommand.Env)
	}
	containerDefinition, _ := suite.TestBinaryDefinitionFor(scenario.SuiteContainer)
	requireArgs(t, "container command", containerCommand.Args, "^"+containerDefinition.TestName+"$", "--ginkgo.fail-on-empty")
	requireOutput(t, stdout.String(),
		"[e2e] === Container suite ===",
		"[e2e] === Container scenarios ===",
		"[e2e] image exporter: registry.example/dcgm-exporter@"+testImageDigest,
		"&&&& RUNNING dcgm_exporter_e2e_integration_container",
		"&&&& PASSED dcgm_exporter_e2e_integration_container",
	)
	// Only container markers are expected, and the registry password must never be printed.
	requireNoOutput(t, stdout.String(), "dcgm_exporter_e2e_static", "dcgm_exporter_e2e_integration_host", "secret-password")
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
	if err == nil || !strings.Contains(err.Error(), "container image "+production+" required by container/remoteDcgmUri is unavailable") {
		t.Fatalf("runWithRootRunner() error = %v, want unavailable-image failure", err)
	}
	if _, ok := runner.commandNamedSuffix("dcgm-exporter-container.test"); ok {
		t.Fatalf("container binary must not run when the DCGM image is unavailable; commands = %#v", runner.commands)
	}
}

func TestRunContainerSuiteRejectsUnavailableImageBeforeAnyGroupRuns(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	configurePackageTestEnvironment(t)
	if err := writeExecutableFile(filepath.Join(root, "bin", "dcgm-exporter-container.test")); err != nil {
		t.Fatal(err)
	}

	production := "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker manifest inspect " + production: {ExitCode: 1, Stderr: []byte("no such manifest")},
		"docker image inspect " + production:    {ExitCode: 1, Stderr: []byte("no such image")},
	}}
	var stdout, stderr bytes.Buffer
	err := runWithRootRunner(
		[]string{
			"tests", "--suite", "container",
			"--scenario", "container/imageStartup",
			"--scenario", "container/remoteDcgmUri",
			"--exporter-image", "registry.example/dcgm-exporter:dev",
		},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err == nil || !strings.Contains(err.Error(), "container image "+production+" required by container/remoteDcgmUri is unavailable") {
		t.Fatalf("runWithRootRunner() error = %v, want selected-image preflight failure", err)
	}
	runs := 0
	for _, command := range runner.commands {
		if filepath.Base(command.Name) == "dcgm-exporter-container.test" {
			runs++
		}
	}
	if runs != 0 {
		t.Fatalf("container suite runs = %d, want no groups after preflight failure; commands = %#v", runs, runner.commands)
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
	passwordFile := filepath.Join(root, "registry-password")
	if err := os.WriteFile(passwordFile, []byte("unused-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	err := runWithRootRunner([]string{
		"tests", "--build-only", "--bootstrap=install",
		"--docker-registry-login", "nvcr.io,user," + passwordFile,
	}, &stdout, &stderr, root, runner)
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
	assertNoBootstrapMutationCommands(t, runner)
	if runner.commandCount("docker") != 0 {
		t.Fatalf("build-only execution invoked Docker: %#v", runner.commands)
	}
}

// assertNoBootstrapMutationCommands rejects package, download, and service changes in plan-only paths.
func assertNoBootstrapMutationCommands(t *testing.T, runner *fakeRunner) {
	t.Helper()
	for _, command := range runner.commands {
		switch command.Name {
		case "apt-get", "dnf", "curl", "systemctl", "modprobe":
			t.Fatalf("plan-only execution invoked a bootstrap change: %#v", command)
		}
	}
}

func TestRunBuildOnlyWithPrebuiltBinariesNeverInvokesGo(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolsDir := filepath.Join(root, "bundle", "bin")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"dcgm-exporter-static.test",
		"dcgm-exporter-host.test",
		"dcgm-exporter-container.test",
		"dcgm-exporter-k8s.test",
	} {
		if err := writeExecutableFile(filepath.Join(toolsDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner([]string{
		"tests",
		"--build-only",
		"--prebuilt-binaries",
		"--tools-dir", toolsDir,
	}, &stdout, &stderr, root, runner)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	for _, command := range runner.commands {
		if command.Name == "go" {
			t.Fatalf("prebuilt mode invoked Go: %#v", runner.commands)
		}
	}
}

// TestRunK8sSuiteWithExternalKubeconfigUsesTargetGPU protects the controller-target boundary for a prepared remote run.
// Its deliberately sparse fixture makes a new controller GPU or Docker prerequisite fail before the Kubernetes suite starts.
func TestRunK8sSuiteWithExternalKubeconfigUsesTargetGPU(t *testing.T) {
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
		"kubectl get nodes -o json":                                               []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}}]}` + "\n"),
		"kubectl get --raw /api/v1/nodes":                                         []byte("{}\n"),
		"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}": []byte("IPv4\n"),
	}, resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if command.Name == "kubectl" && containsArgs(command.Args, "get", "namespace", "--ignore-not-found=true") {
			return e2eexec.Result{Stdout: []byte(`{"metadata":{"uid":"test-namespace-uid","labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"dcgm-exporter"}},"status":{"phase":"Active"}}`)}, true
		}
		return e2eexec.Result{}, false
	}}
	err := runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--kubeconfig", "/tmp/kubeconfig", "--exporter-image", "registry.example/dcgm-exporter:dev@" + testImageDigest, "--bootstrap=skip", "--result-markers"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	requireOutput(t, stdout.String(),
		"&&&& RUNNING dcgm_exporter_e2e_setup",
		"&&&& PASSED dcgm_exporter_e2e_setup",
		"[e2e] image exporter: registry.example/dcgm-exporter@"+testImageDigest,
	)
	// The suite emits per-spec markers, so the CLI must not add group or other-suite markers.
	requireNoOutput(t, stdout.String(),
		"&&&& RUNNING dcgm_exporter_e2e_group_embedded_baseline",
		"dcgm_exporter_e2e_static",
		"dcgm_exporter_e2e_integration_host",
		"dcgm_exporter_e2e_integration_container",
	)
	k8sCommand, ok := runner.commandNamedSuffix("dcgm-exporter-k8s.test")
	if !ok {
		t.Fatalf("k8s binary was not run; commands = %#v", runner.commands)
	}
	k8sDefinition, _ := suite.TestBinaryDefinitionFor(scenario.SuiteK8s)
	requireArgs(t, "k8s command", k8sCommand.Args, "^"+k8sDefinition.TestName+"$")
	requireRunnerArgs(t, runner,
		"--ginkgo.label-filter=default",
		"--ginkgo.fail-on-empty",
		"-result-markers=true",
		"-exporter-image=registry.example/dcgm-exporter@"+testImageDigest,
		"-gpu-operator-exporter-image=registry.example/dcgm-exporter:dev@"+testImageDigest,
		"-mig-instance-entity-id=101",
		"-mig-instance-nvml-id=7",
		"-unsupported-field-candidate=DCGM_FI_DEV_RETIRED_PENDING",
	)
	if runner.commandCount("kubectl", "delete") != 2 {
		t.Fatalf("CLI did not clean configured external namespaces: %#v", runner.commands)
	}
	requireNoControllerPrerequisites(t, runner)
}

// requireNoControllerPrerequisites fails when an external-target run used controller Docker or GPU tools.
func requireNoControllerPrerequisites(t *testing.T, runner *fakeRunner) {
	t.Helper()
	for _, command := range runner.commands {
		if command.Name == "docker" || command.Name == "nvidia-smi" {
			t.Fatalf("external target run used a controller prerequisite: %#v", command)
		}
	}
}

// TestExternalK8sPlanUsesSelectedTargetAllocation proves allocation types do not imply an ordinary GPU prerequisite.
// The MIG-only and DRA-only fixtures have no controller evidence, so planning must use the selected target's allocation evidence.
func TestExternalK8sPlanUsesSelectedTargetAllocation(t *testing.T) {
	tests := []struct {
		name      string
		scenario  string
		nodes     string
		resources string
		api       string
	}{
		{
			name:     "MIG only target",
			scenario: "k8s/mig",
			nodes:    `{"items":[{"status":{"allocatable":{"nvidia.com/mig-1g.10gb":"7"}}}]}`,
		},
		{
			name:      "DRA only target",
			scenario:  "k8s/dra",
			nodes:     `{"items":[{"status":{"allocatable":{}}}]}`,
			api:       "resourceslices\n",
			resources: `{"items":[{"spec":{"driver":"gpu.nvidia.com","devices":[{"attributes":{"type":{"string":"gpu"}}}]}}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{outputs: map[string][]byte{
				"kubectl get nodes -o json":          []byte(test.nodes),
				"kubectl get --raw /api/v1/nodes":    []byte("{}"),
				"kubectl api-resources":              []byte(test.api),
				"kubectl get resourceslices -o json": []byte(test.resources),
			}}
			run := &k8sSuiteRun{
				ctx:        context.Background(),
				stdout:     io.Discard,
				runner:     runner,
				cfg:        config.Config{TestOptions: config.TestOptions{Scenarios: []string{test.scenario}, Kubeconfig: "/tmp/kubeconfig"}},
				binary:     "dcgm-exporter-k8s.test",
				clusterCfg: cluster.Config{Kubeconfig: "/tmp/kubeconfig"},
			}
			plan, err := run.buildPlan(k8sProbeInputs{})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Groups) != 1 || len(plan.Groups[0].Labels) != 1 || plan.Groups[0].Labels[0] != strings.TrimPrefix(test.scenario, "k8s/") {
				t.Fatalf("plan = %#v, want selected %s scenario", plan, test.scenario)
			}
			if err := run.executePlan(plan); err != nil {
				t.Fatalf("executePlan() for supported %s allocation = %v", test.scenario, err)
			}
			if !runner.hasCommand("dcgm-exporter-k8s.test") {
				t.Fatalf("supported %s allocation did not execute its scenario group", test.scenario)
			}
			requireNoControllerPrerequisites(t, runner)
		})
	}
}

// TestExternalK8sPlanSelectsDRAWhenSetupInstallsDriver accounts for DRA installed by setup.
func TestExternalK8sPlanSelectsDRAWhenSetupInstallsDriver(t *testing.T) {
	for _, test := range []struct {
		draConfigure config.FeatureMode
		wantSelected bool
	}{
		{draConfigure: "true", wantSelected: true},
		{draConfigure: "auto", wantSelected: false},
	} {
		t.Run(string(test.draConfigure), func(t *testing.T) {
			runner := &fakeRunner{outputs: map[string][]byte{
				"kubectl get nodes -o json":          []byte(`{"items":[{"status":{"allocatable":{}}}]}`),
				"kubectl get --raw /api/v1/nodes":    []byte("{}"),
				"kubectl api-resources":              []byte("resourceslices\n"),
				"kubectl get resourceslices -o json": []byte(`{"items":[]}`),
			}}
			run := &k8sSuiteRun{
				ctx:    context.Background(),
				stdout: io.Discard,
				runner: runner,
				cfg: config.Config{TestOptions: config.TestOptions{
					Scenarios:    []string{"k8s/dra"},
					Kubeconfig:   "/tmp/kubeconfig",
					DRAConfigure: test.draConfigure,
				}},
				binary:     "dcgm-exporter-k8s.test",
				clusterCfg: cluster.Config{Kubeconfig: "/tmp/kubeconfig"},
			}
			plan, err := run.buildPlan(k8sProbeInputs{})
			if err != nil {
				t.Fatal(err)
			}
			if selected := len(plan.Groups) == 1; selected != test.wantSelected {
				t.Fatalf("k8s/dra selected = %t, want %t; plan = %#v", selected, test.wantSelected, plan)
			}
		})
	}
}

// TestExternalK8sExecutionFailsWhenAllSelectedScenariosAreSkipped prevents a resource-less target from passing without running checks.
func TestExternalK8sExecutionFailsWhenAllSelectedScenariosAreSkipped(t *testing.T) {
	var stdout bytes.Buffer
	runner := &fakeRunner{outputs: map[string][]byte{
		"kubectl get nodes -o json":       []byte(`{"items":[{"status":{"allocatable":{}}}]}`),
		"kubectl get --raw /api/v1/nodes": []byte(`{"items":[]}`),
	}}
	run := &k8sSuiteRun{
		ctx:           context.Background(),
		stdout:        &stdout,
		runner:        runner,
		binary:        "dcgm-exporter-k8s.test",
		reporter:      marker.NewReporter(&stdout),
		resultMarkers: true,
		cfg: config.Config{TestOptions: config.TestOptions{
			Scenarios:  []string{"k8s/default"},
			Kubeconfig: "/tmp/kubeconfig",
		}},
		clusterCfg: cluster.Config{Kubeconfig: "/tmp/kubeconfig"},
	}
	if err := run.emitSetupRunning(); err != nil {
		t.Fatal(err)
	}
	defaultScenario, ok := scenario.Find(scenario.Catalog, scenario.SuiteK8s, "default")
	if !ok {
		t.Fatal("k8s/default scenario missing")
	}
	defaultMarker, ok := defaultScenario.MarkerBaseName()
	if !ok {
		t.Fatal("k8s/default marker name missing")
	}

	plan, err := run.buildPlan(k8sProbeInputs{})
	if err != nil {
		t.Fatalf("buildPlan() error = %v", err)
	}
	if len(plan.Groups) != 0 || len(plan.Scenarios) != 1 || plan.Scenarios[0].Outcome != scenario.OutcomeSkipped {
		t.Fatalf("plan = %#v, want selected default scenario skipped with no groups", plan)
	}

	err = run.executePlan(plan)
	if err == nil {
		t.Fatal("executePlan() error = nil, want setup failure for a wholly skipped selection")
	}
	code, ok := ExitCode(err)
	if !ok || code != exitSetupFailure || !strings.Contains(err.Error(), "all selected Kubernetes scenarios were skipped") {
		t.Fatalf("executePlan() error = %v, want setup failure for a wholly skipped selection", err)
	}
	if runner.hasCommand("dcgm-exporter-k8s.test") {
		t.Fatalf("wholly skipped selection ran the suite binary: %#v", runner.commands)
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_setup") {
		t.Fatalf("wholly skipped selection did not fail the setup marker:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "&&&& PASSED dcgm_exporter_e2e_setup") {
		t.Fatalf("wholly skipped selection passed the setup marker:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "&&&& SKIPPED "+defaultMarker) {
		t.Fatalf("wholly skipped selection did not report its scenario skip:\n%s", stdout.String())
	}
}

// TestExternalK8sPlanSkipsUnprobedControllerCapability preserves the target-only planning boundary for host-gated scenarios.
// The selected target has its GPU allocation, but controller profiling data is intentionally absent and must become a reported skip.
func TestExternalK8sPlanSkipsUnprobedControllerCapability(t *testing.T) {
	runner := &fakeRunner{outputs: map[string][]byte{
		"kubectl get nodes -o json":       []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}}]}`),
		"kubectl get --raw /api/v1/nodes": []byte("{}"),
	}}
	run := &k8sSuiteRun{
		ctx:        context.Background(),
		stdout:     io.Discard,
		runner:     runner,
		cfg:        config.Config{TestOptions: config.TestOptions{Scenarios: []string{"k8s/profiling"}}},
		clusterCfg: cluster.Config{Kubeconfig: "/tmp/kubeconfig"},
	}
	plan, err := run.buildPlan(k8sProbeInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 0 || len(plan.Scenarios) != 1 || plan.Scenarios[0].Outcome != scenario.OutcomeSkipped {
		t.Fatalf("plan = %#v, want one skipped scenario and no execution groups", plan)
	}
	if !strings.Contains(plan.Scenarios[0].Reason, "not evaluated for the selected Kubernetes target") {
		t.Fatalf("skip reason = %q, want explicit controller boundary", plan.Scenarios[0].Reason)
	}
	for _, command := range runner.commands {
		if command.Name == "docker" || command.Name == "nvidia-smi" {
			t.Fatalf("external target plan used a controller prerequisite: %#v", command)
		}
	}
}

// TestExternalK8sPlanSkipsControllerProbeScenario verifies target selection never falls back to controller DCGM data.
// The mixed-MIG target qualifies the allocation, but the scenario's extra data has no remote Kubernetes probe yet.
func TestExternalK8sPlanSkipsControllerProbeScenario(t *testing.T) {
	selected := scenario.Selected(scenario.Catalog, config.Config{TestOptions: config.TestOptions{
		Scenarios: []string{"k8s/migSpecificInstanceSelection"},
	}})
	requirements := scenario.RequirementsFor(selected, scenario.RunConditions{})
	runner := &fakeRunner{outputs: map[string][]byte{
		"kubectl get nodes -o json":       []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1","nvidia.com/mig-1g.10gb":"7"}}}]}`),
		"kubectl get --raw /api/v1/nodes": []byte("{}"),
	}}
	run := newK8sSuiteRun(
		context.Background(),
		io.Discard,
		t.TempDir(),
		runner,
		config.Config{TestOptions: config.TestOptions{
			Scenarios:  []string{"k8s/migSpecificInstanceSelection"},
			Kubeconfig: "/tmp/kubeconfig",
		}},
		"",
		"",
		selected,
		requirements,
	)
	inputs, err := run.prepareProbeInputs()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := run.buildPlan(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 0 || len(plan.Scenarios) != 1 || plan.Scenarios[0].Outcome != scenario.OutcomeSkipped {
		t.Fatalf("plan = %#v, want one skipped scenario and no execution groups", plan)
	}
	if !strings.Contains(plan.Scenarios[0].Reason, "cannot probe that data remotely") {
		t.Fatalf("skip reason = %q, want explicit remote probe boundary", plan.Scenarios[0].Reason)
	}
	for _, command := range runner.commands {
		if command.Name == "docker" || command.Name == "nvidia-smi" {
			t.Fatalf("external target plan used a controller prerequisite: %#v", command)
		}
	}
}

func TestK8sSuiteCleanupReturnsRegisteredFailure(t *testing.T) {
	want := errors.New("restore MIG state")
	run := &k8sSuiteRun{
		clusterCfg: cluster.Config{Kubeconfig: "/tmp/kubeconfig"},
		cleanupFuncs: []func(context.Context) error{
			func(context.Context) error { return want },
		},
	}

	if err := run.cleanup(context.Background()); !errors.Is(err, want) {
		t.Fatalf("cleanup() error = %v, want %v", err, want)
	}
}

// TestRunK8sSuiteWithExternalKubeconfigCleansUpAfterFailure cleans up after a failed external-cluster run.
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
		namespaceOwner: cluster.DefaultConfig().RunName,
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
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--kubeconfig", "/tmp/kubeconfig", "--exporter-image", "registry.example/dcgm-exporter:dev", "--bootstrap=skip"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err == nil {
		t.Fatal("runWithRootRunner() error = nil, want k8s suite failure")
	}
	if runner.commandCount("kubectl", "delete") != 2 {
		t.Fatalf("CLI did not clean configured external namespaces after failure: %#v", runner.commands)
	}
}

func TestRunK8sSuiteWatchRetentionImportsDCGMAndPassesProbe(t *testing.T) {
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
		"docker run --rm --entrypoint /usr/bin/nv-hostengine registry.example/dcgm@" + testImageDigest + " --version": []byte("Version : 4.5.3\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.status.allocatable.nvidia\\.com/gpu}{\"\\n\"}{end}":         []byte("1\n"),
		"kubectl get nodes -o jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}":                               []byte("node-0\n"),
		"kubectl get nodes -o json":                                               []byte(`{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"1"}}}]}` + "\n"),
		"kubectl get --raw /api/v1/nodes":                                         []byte("{}\n"),
		"kubectl get svc kubernetes -n default -o jsonpath={.spec.ipFamilies[*]}": []byte("IPv4\n"),
	}}
	err := runWithRootRunner(
		[]string{
			"tests",
			"--suite", "k8s",
			"--scenario", "k8s/watchRetention",
			"--exporter-image", "registry.example/dcgm-exporter:dev",
			"--dcgm-image", "registry.example/dcgm@" + testImageDigest,
			"--bootstrap=skip",
		},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}

	localDCGMImage := "registry.example/dcgm:dcgm-exporter-e2e-aaaaaaaaaaaa"
	if !runner.hasDockerTag("registry.example/dcgm@"+testImageDigest, localDCGMImage) || !runner.hasK3dImageImport(localDCGMImage) {
		t.Fatalf("local k3d run did not tag and import the DCGM image alias: commands = %#v", runner.commands)
	}
	k8sCommand, ok := runner.commandNamedSuffix("dcgm-exporter-k8s.test")
	if !ok {
		t.Fatalf("k8s test binary was not invoked; commands = %#v", runner.commands)
	}
	wantProbe := "-dcgm-probe-binary=" + filepath.Join(root, ".e2e-tools", "e2e-dcgm-probe")
	if !hasArg(k8sCommand.Args, wantProbe) {
		t.Fatalf("watch retention k8s command missing %q: %#v", wantProbe, k8sCommand.Args)
	}
	if !hasArg(k8sCommand.Args, "-dcgm-image="+localDCGMImage) {
		t.Fatalf("watch retention k8s command did not use the imported DCGM image: %#v", k8sCommand.Args)
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
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--exporter-image", "registry.example/dcgm-exporter:dev@" + testImageDigest, "--bootstrap=skip"},
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
	if !hasArg(k8sCommand.Args, "-gpu-operator-exporter-image=registry.example/dcgm-exporter:dev") {
		t.Fatalf("local k3d k8s command did not use the tagged GPU Operator image: %#v", k8sCommand.Args)
	}
}

// TestClusterFeatureConfigUsesTheImageAvailableToGPUOperator uses the image available to GPU Operator.
func TestClusterFeatureConfigUsesTheImageAvailableToGPUOperator(t *testing.T) {
	opts := config.TestOptions{ExporterImage: "registry.example/dcgm-exporter:dev@" + testImageDigest}
	local := cluster.DefaultConfig()
	external := cluster.DefaultConfig()
	external.Kubeconfig = "/tmp/kubeconfig"

	tests := []struct {
		name string
		cfg  cluster.Config
		want string
	}{
		{name: "local k3d tag", cfg: local, want: "registry.example/dcgm-exporter:dev"},
		{name: "local k3d digest alias", cfg: local, want: "registry.example/dcgm-exporter:dcgm-exporter-e2e-aaaaaaaaaaaa"},
		{name: "external tag and digest", cfg: external, want: opts.ExporterImage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testOpts := opts
			if tt.name == "local k3d digest alias" {
				testOpts.ExporterImage = "registry.example/dcgm-exporter@" + testImageDigest
			}
			got := clusterFeatureConfig("/tmp/root", tt.cfg, testOpts, "").ExporterImage
			if got != tt.want {
				t.Fatalf("clusterFeatureConfig().ExporterImage = %q, want %q", got, tt.want)
			}
		})
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
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/default", "--exporter-image", "registry.example/dcgm-exporter:dev", "--bootstrap=skip"},
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

// TestRunK8sSuiteFailsWhenRemoteScenarioSkipsWithoutHostPlatformImage verifies ARM image handling.
func TestRunK8sSuiteFailsWhenRemoteScenarioSkipsWithoutHostPlatformImage(t *testing.T) {
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

	dcgmImage := "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04"
	versionCommand := "docker run --rm --entrypoint /usr/bin/nv-hostengine " + dcgmImage + " --version"
	runner := &fakeRunner{
		outputs: fakeK8sProbeOutputs(),
		results: map[string]e2eexec.Result{
			versionCommand: {
				ExitCode: 1,
				Stderr:   []byte("no matching manifest for linux/arm64/v8 in the manifest list entries"),
			},
		},
	}
	var stdout, stderr bytes.Buffer
	err := runWithRootRunner(
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/remoteDcgm", "--exporter-image", "registry.example/dcgm-exporter:dev", "--bootstrap=skip"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	code, ok := ExitCode(err)
	if !ok || code != exitSetupFailure || !strings.Contains(err.Error(), "all selected Kubernetes scenarios were skipped") {
		t.Fatalf("runWithRootRunner() error = %v; want setup failure for a wholly skipped selection; stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "required DCGM image is not published for this host architecture") {
		t.Fatalf("scenario plan did not report the platform waiver:\n%s", stdout.String())
	}
	if runner.hasK3dImageImport(dcgmImage) {
		t.Fatalf("platform-incompatible DCGM image was imported; commands = %#v", runner.commands)
	}
}

// TestPrepareK8sGroupRecreatesDCGMPullSecret checks that Kubernetes setup recreates the DCGM image pull secret.
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

	run := newK8sGroupPreparationRun(context.Background(), io.Discard, runner, t.TempDir(), clusterCfg)
	cleanup, err := run.prepareK8sGroup(opts, scenario.PlanGroup{Name: "standalone-baseline"})
	if err != nil {
		t.Fatalf("prepareK8sGroup() error = %v", err)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}

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

// TestPrepareK8sGroupIPv6HostnameUsesOwnedIPv6Service checks that an IPv6 hostname scenario uses its owned IPv6 service.
func TestPrepareK8sGroupIPv6HostnameUsesOwnedIPv6Service(t *testing.T) {
	opts := config.TestOptions{DCGMImage: "registry.example/dcgm:4.5.3-1-ubuntu24.04"}
	clusterCfg := clusterConfig(t.TempDir(), opts)
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &fakeRunner{}

	run := newK8sGroupPreparationRun(context.Background(), io.Discard, runner, t.TempDir(), clusterCfg)
	cleanup, err := run.prepareK8sGroup(opts, scenario.PlanGroup{Name: "standalone-ipv6"})
	if err != nil {
		t.Fatalf("prepareK8sGroup() error = %v", err)
	}
	defer func() { _ = cleanup(context.Background()) }()

	manifest := ""
	for _, command := range runner.commands {
		if command.Name == "kubectl" && containsArgs(command.Args, "apply", "-f", "-") && strings.Contains(string(command.Stdin), "nv-hostengine") {
			manifest = string(command.Stdin)
			break
		}
	}
	if manifest == "" {
		t.Fatalf("standalone DCGM manifest was not applied: %#v", runner.commands)
	}
	if !strings.Contains(manifest, "- \"::\"") ||
		!strings.Contains(manifest, "ipFamilyPolicy: SingleStack") ||
		!strings.Contains(manifest, "ipFamilies:\n  - IPv6") {
		t.Fatalf("IPv6 hostname group manifest =\n%s", manifest)
	}
}

// TestPrepareK8sGroupIPv6HostnameRejectsRemoteDCGMOverride rejects remote DCGM for the owned IPv6 service.
func TestPrepareK8sGroupIPv6HostnameRejectsRemoteDCGMOverride(t *testing.T) {
	opts := config.TestOptions{RemoteDCGM: "[2001:db8::1]:5555"}
	clusterCfg := clusterConfig(t.TempDir(), opts)
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"

	run := newK8sGroupPreparationRun(context.Background(), io.Discard, &fakeRunner{}, t.TempDir(), clusterCfg)
	_, err := run.prepareK8sGroup(opts, scenario.PlanGroup{Name: "standalone-ipv6"})
	if err == nil || !strings.Contains(err.Error(), "do not set --remote-dcgm") {
		t.Fatalf("prepareK8sGroup() error = %v, want remote endpoint override rejection", err)
	}
}

func TestDCGMAddressForUsesStandaloneServiceHostnameWithoutOverride(t *testing.T) {
	opts := config.TestOptions{DCGMName: "dcgm-ipv6", DCGMPort: "5556"}
	cfg := cluster.DefaultConfig()
	cfg.DCGMNamespace = "dcgm-exporter-ipv6"
	if got, want := dcgmAddressFor(opts, cfg), "dcgm-ipv6.dcgm-exporter-ipv6.svc.cluster.local:5556"; got != want {
		t.Fatalf("dcgmAddressFor() = %q, want %q", got, want)
	}

	opts.RemoteDCGM = "[2001:db8::1]:5555"
	if got := dcgmAddressFor(opts, cfg); got != opts.RemoteDCGM {
		t.Fatalf("dcgmAddressFor() with override = %q, want %q", got, opts.RemoteDCGM)
	}
}

// TestGroupUsesNVSwitchOnlyForNVSwitchLabels verifies that temporary CDI setup
// is limited to execution groups that actually need NVSDM.
func TestGroupUsesNVSwitchOnlyForNVSwitchLabels(t *testing.T) {
	for _, label := range []string{"nvswitch", "nvswitchLink", "nvswitchThroughput"} {
		if !groupUsesNVSwitch(scenario.PlanGroup{Name: "embedded-nvswitch", Labels: []string{label}}) {
			t.Fatalf("groupUsesNVSwitch() = false for %s", label)
		}
	}
	withoutNVSwitch := scenario.PlanGroup{
		Name:   "embedded-hardware",
		Labels: []string{"profiling", "nvlink", "nvlinkIntegrity"},
	}
	if groupUsesNVSwitch(withoutNVSwitch) {
		t.Fatal("groupUsesNVSwitch() = true without an NVSwitch scenario")
	}

	local := cluster.DefaultConfig()
	withNVSwitch := scenario.PlanGroup{Name: "embedded-nvswitch", Labels: []string{"nvswitch"}}
	if got := nvidiaVisibleDevicesForGroup(local, withNVSwitch); got != "nvidia.com/gpu=all" {
		t.Fatalf("nvidiaVisibleDevicesForGroup() = %q, want qualified CDI device", got)
	}
	if got := nvidiaVisibleDevicesForGroup(local, withoutNVSwitch); got != "" {
		t.Fatalf("nvidiaVisibleDevicesForGroup() = %q without NVSwitch scenario", got)
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
		[]string{"tests", "--dry-run", "--suite", "k8s", "--scenario", "k8s/default", "--bootstrap=check"},
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
		[]string{"tests", "--suite", "k8s", "--scenario", "k8s/profiling", "--exporter-image", "registry.example/dcgm-exporter:dev", "--bootstrap=skip"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if _, ok := runner.commandWithArgs("docker", "image", "inspect", production); !ok {
		t.Fatalf("hardware-probe scenario did not inspect the production DCGM image; commands = %#v", runner.commands)
	}
}

// TestDryRunK8sHardwareProbeDescribesDCGMImageWithoutResolvingIt keeps previews read-only.
func TestDryRunK8sHardwareProbeDescribesDCGMImageWithoutResolvingIt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestVersions(t, root)

	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRootRunner(
		[]string{"tests", "--dry-run", "--suite", "k8s", "--scenario", "k8s/profiling", "--bootstrap=check"},
		&stdout,
		&stderr,
		root,
		runner,
	)
	if err != nil {
		t.Fatalf("runWithRootRunner() error = %v; stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "container-image "+"nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04") {
		t.Fatalf("hardware-probe dry-run should describe the DCGM image:\n%s", stdout.String())
	}
	if len(runner.commands) != 0 {
		t.Fatalf("hardware-probe dry-run executed commands: %#v", runner.commands)
	}
}

// TestRunK8sGroupPrepareFailureEmitsGroupFailedMarker emits a failed marker for setup errors.
func TestRunK8sGroupPrepareFailureEmitsGroupFailedMarker(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	var stdout bytes.Buffer
	runner := &fakeRunner{
		outputs: map[string][]byte{"k3d cluster list": []byte("dcgm-exporter-gpu 1/1 1/1\n")},
		results: map[string]e2eexec.Result{
			"helm repo add nvidia-device-plugin https://nvidia.github.io/k8s-device-plugin --force-update": {
				ExitCode: 1,
				Stderr:   []byte("repo failed"),
			},
		},
	}
	opts := config.TestOptions{}
	run := newK8sSuiteRun(
		context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	run.resultMarkers = true
	run.suiteResultMarkers = true
	group := scenario.PlanGroup{Name: "embedded-mig", Labels: []string{"mig"}}
	err := run.runK8sGroup(&opts, group)
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
	run.suiteResultMarkers = false
	err = run.runK8sGroup(&opts, group)
	if err == nil {
		t.Fatal("runK8sGroup() error = nil, want prepare failure")
	}
	if got := strings.Count(stdout.String(), "&&&& RUNNING dcgm_exporter_e2e_group_embedded_mig"); got != 1 {
		t.Fatalf("group RUNNING marker count = %d, want 1:\n%s", got, stdout.String())
	}
}

// TestRunK8sGroupPassesQualifiedCDIDevice connects catalog selection to NVSwitch runtime setup.
func TestRunK8sGroupPassesQualifiedCDIDevice(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts.Scenarios = []string{"k8s/nvswitch"}
	plan, err := scenario.Select(scenario.Catalog, capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvswitch", Status: capability.StatusSupported},
		{Name: "host:nvswitch_local_telemetry", Status: capability.StatusSupported},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported},
	}), config.Config{TestOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("selected groups = %#v, want one NVSwitch group", plan.Groups)
	}
	runner := &fakeRunner{}

	run := newK8sSuiteRun(
		context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	err = run.runK8sGroup(&opts, plan.Groups[0])
	if err != nil {
		t.Fatalf("runK8sGroup() error = %v", err)
	}

	for _, command := range runner.commands {
		if command.Name == "/tmp/dcgm-exporter-k8s.test" &&
			containsArgs(command.Args, "-nvidia-visible-devices=nvidia.com/gpu=all") {
			return
		}
	}
	t.Fatalf("Kubernetes suite command did not receive the qualified CDI device: %#v", runner.commands)
}

// TestRunK8sGroupPassesCleanupFlag checks that the Kubernetes group passes the cleanup setting to its suite command.
func TestRunK8sGroupPassesCleanupFlag(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	opts := config.TestOptions{Cleanup: false}
	run := newK8sSuiteRun(
		context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	err := run.runK8sGroup(&opts, scenario.PlanGroup{Name: "default", Labels: []string{"default"}})
	if err != nil {
		t.Fatalf("runK8sGroup() error = %v", err)
	}
	for _, command := range runner.commands {
		if command.Name != "/tmp/dcgm-exporter-k8s.test" {
			continue
		}
		hasRunID := false
		for _, arg := range command.Args {
			if strings.HasPrefix(arg, "-run-id=") {
				hasRunID = true
				break
			}
		}
		if !containsArgs(command.Args, "-no-cleanup=true") || hasRunID {
			t.Fatalf("Kubernetes suite flags = %#v", command.Args)
		}
		return
	}
	t.Fatalf("Kubernetes suite was not executed: %#v", runner.commands)
}

// TestRunK8sGroupRetainsStandaloneDCGMWhenCleanupDisabled keeps standalone DCGM when cleanup is disabled.
func TestRunK8sGroupRetainsStandaloneDCGMWhenCleanupDisabled(t *testing.T) {
	clusterCfg := cluster.DefaultConfig()
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	opts := config.TestOptions{
		Cleanup:   false,
		DCGMImage: "registry.example/dcgm:4.5.3",
	}
	for _, test := range []struct {
		name       string
		suiteFails bool
	}{
		{name: "successful group"},
		{name: "failed group", suiteFails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := suiteFailureRunner(test.suiteFails)
			root := t.TempDir()
			run := newK8sSuiteRun(
				context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
				"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
			)
			run.clusterCfg = clusterCfg
			err := run.runK8sGroup(&opts, scenario.PlanGroup{Name: "standalone-baseline", Labels: []string{"remoteDcgm"}})
			if (err != nil) != test.suiteFails {
				t.Fatalf("runK8sGroup() error = %v, want failure %t", err, test.suiteFails)
			}
			if !runner.hasCommandArg("apply") {
				t.Fatalf("standalone DCGM was not prepared: %#v", runner.commands)
			}
			if runner.hasCommandArg("delete") {
				t.Fatalf("--cleanup=false deleted retained resources: %#v", runner.commands)
			}
		})
	}
}

// TestK8sTestCommand checks the arguments passed to the Kubernetes suite binary.
func TestK8sTestCommand(t *testing.T) {
	root := "/workspace/dcgm-exporter"
	clusterCfg := cluster.Config{
		Kubeconfig:    "/tmp/kubeconfig",
		Namespace:     "exporter",
		DCGMNamespace: "dcgm",
	}
	opts := config.TestOptions{
		Cleanup:                   true,
		ExporterImage:             "registry.example/dcgm-exporter:dev",
		K8sImagePullSecret:        "registry-auth",
		K8sRuntimeClass:           "nvidia",
		K8sNodeSelectorKey:        "gpu.nvidia.com/class",
		K8sNodeSelectorValue:      "test",
		RemoteDCGM:                "remote-dcgm:5556",
		DCGMImage:                 "registry.example/dcgm:4.5.3",
		BusyboxImage:              "registry.example/busybox:1.36",
		CUDAWorkloadImage:         "registry.example/cuda:13.2",
		DCGMName:                  "hostengine",
		DCGMPort:                  "5556",
		MIGInstanceEntityID:       "101",
		MIGInstanceNVMLID:         "7",
		UnsupportedFieldCandidate: "DCGM_FI_DEV_RETIRED_PENDING",
	}
	group := scenario.PlanGroup{Name: "embedded-baseline", Labels: []string{"default", "metrics"}}

	command, err := k8sTestCommand("/tmp/dcgm-exporter-k8s.test", "/tmp/dcgm-probe", root, clusterCfg, opts, group, true)
	require.NoError(t, err)
	require.Equal(t, e2eexec.Command{
		Name: "/tmp/dcgm-exporter-k8s.test",
		Dir:  root,
		Args: []string{
			"-test.v",
			"-test.run", "^TestKubernetesIntegration$",
			"--ginkgo.v",
			"--ginkgo.no-color",
			"--ginkgo.fail-on-empty",
			"--ginkgo.label-filter=default || metrics",
			"-kubeconfig=/tmp/kubeconfig",
			"-run-name=dcgm-exporter",
			"-namespace=exporter",
			"-chart=/workspace/dcgm-exporter/deployment",
			"-exporter-image=registry.example/dcgm-exporter:dev",
			"-gpu-operator-exporter-image=registry.example/dcgm-exporter:dev",
			"-k8s-image-pull-secret=registry-auth",
			"-runtime-class=nvidia",
			"-node-selector-key=gpu.nvidia.com/class",
			"-node-selector-value=test",
			"-remote-dcgm=remote-dcgm:5556",
			"-dcgm-image=registry.example/dcgm:4.5.3",
			"-busybox-image=registry.example/busybox:1.36",
			"-cuda-workload-image=registry.example/cuda:13.2",
			"-dcgm-namespace=dcgm",
			"-dcgm-name=hostengine",
			"-dcgm-port=5556",
			"-mig-instance-entity-id=101",
			"-mig-instance-nvml-id=7",
			"-unsupported-field-candidate=DCGM_FI_DEV_RETIRED_PENDING",
			"-result-markers=true",
			"-no-cleanup=false",
			"-owned-local-cluster=false",
			"-dcgm-probe-binary=/tmp/dcgm-probe",
		},
	}, command)

	local := cluster.DefaultConfig()
	localCommand, err := k8sTestCommand(
		"k8s.test",
		"",
		root,
		local,
		config.TestOptions{Cleanup: false},
		scenario.PlanGroup{Name: "embedded-nvswitch", Labels: []string{"nvswitch"}},
		false,
	)
	require.NoError(t, err)
	require.Contains(t, localCommand.Args, "-nvidia-visible-devices=nvidia.com/gpu=all")
	require.Contains(t, localCommand.Args, "-no-cleanup=true")
	require.Contains(t, localCommand.Args, "-owned-local-cluster=true")
	require.NotContains(t, localCommand.Args, "-dcgm-probe-binary=")
}

// TestRunK8sGroupRegistryFailureUsesGroupFailurePath uses the group failure path for registry errors.
func TestRunK8sGroupRegistryFailureUsesGroupFailurePath(t *testing.T) {
	var stdout bytes.Buffer
	runner := &fakeRunner{}
	root := t.TempDir()
	opts := config.TestOptions{DockerRegistryLogins: []string{"invalid"}}
	run := newK8sSuiteRun(
		context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	run.clusterCfg = cluster.DefaultConfig()
	run.resultMarkers = true
	run.suiteResultMarkers = true
	err := run.runK8sGroup(&opts, scenario.PlanGroup{Name: "default", Labels: []string{"default"}})
	if err == nil || !strings.Contains(err.Error(), "docker registry login spec must be") {
		t.Fatalf("runK8sGroup() error = %v, want registry login validation failure", err)
	}
	if !strings.Contains(stdout.String(), "Kubernetes failure diagnostics") {
		t.Fatalf("registry failure did not collect diagnostics:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_group_default") {
		t.Fatalf("registry failure did not emit failed group marker:\n%s", stdout.String())
	}
	if _, ok := runner.commandNamedSuffix("dcgm-exporter-k8s.test"); ok {
		t.Fatalf("Kubernetes suite ran after registry setup failed: %#v", runner.commands)
	}
}

// TestRunK8sGroupReportsNVSwitchCleanupFailure verifies that failure to restore
// the local runtime makes an otherwise passing group fail.
func TestRunK8sGroupReportsNVSwitchCleanupFailure(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts.Scenarios = []string{"k8s/nvswitch"}
	plan, err := scenario.Select(scenario.Catalog, capability.NewSnapshot([]capability.Capability{
		{Name: "host:nvswitch", Status: capability.StatusSupported},
		{Name: "host:nvswitch_local_telemetry", Status: capability.StatusSupported},
		{Name: "cluster:gpu_resources", Status: capability.StatusSupported},
	}), config.Config{TestOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("selected groups = %#v, want one NVSwitch group", plan.Groups)
	}
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if command.Name == "docker" && strings.Contains(strings.Join(command.Args, " "), `mode = "legacy"`) {
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("restore failed")}, true
		}
		return e2eexec.Result{}, false
	}}

	run := newK8sSuiteRun(
		context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	err = run.runK8sGroup(&opts, plan.Groups[0])
	if err == nil || !strings.Contains(err.Error(), `mode = "legacy"`) {
		t.Fatalf("runK8sGroup() error = %v, want NVSwitch cleanup failure", err)
	}
}

// TestPrepareK8sGroupPreservesDisabledSharedGPUConfiguration preserves disabled shared GPU settings.
func TestPrepareK8sGroupPreservesDisabledSharedGPUConfiguration(t *testing.T) {
	for _, group := range []scenario.PlanGroup{
		{Name: "embedded-shared-gpu", Labels: []string{"sharedGpu"}},
		{Name: "embedded-renamed-gpu", Labels: []string{"customResourceNames"}},
	} {
		t.Run(group.Name, func(t *testing.T) {
			runner := &fakeRunner{}
			clusterCfg := clusterConfig(t.TempDir(), config.TestOptions{})
			run := newK8sGroupPreparationRun(context.Background(), io.Discard, runner, t.TempDir(), clusterCfg)
			cleanup, err := run.prepareK8sGroup(config.TestOptions{SharedGPUConfigure: "false"}, group)
			if err != nil {
				t.Fatalf("prepareK8sGroup() error = %v", err)
			}
			if err := cleanup(context.Background()); err != nil {
				t.Fatalf("cleanup() error = %v", err)
			}
			if len(runner.commands) != 0 {
				t.Fatalf("disabled shared GPU configuration ran commands: %#v", runner.commands)
			}
		})
	}
}

// TestPrepareK8sGroupDoesNotChangeRuntimeForOrdinaryHardware protects unrelated hardware tests.
func TestPrepareK8sGroupDoesNotChangeRuntimeForOrdinaryHardware(t *testing.T) {
	runner := &fakeRunner{}
	clusterCfg := clusterConfig(t.TempDir(), config.TestOptions{})
	run := newK8sGroupPreparationRun(context.Background(), io.Discard, runner, t.TempDir(), clusterCfg)
	cleanup, err := run.prepareK8sGroup(
		config.TestOptions{},
		scenario.PlanGroup{Name: "embedded-hardware", Labels: []string{"profiling", "nvlink"}},
	)
	if err != nil {
		t.Fatalf("prepareK8sGroup() error = %v", err)
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("ordinary hardware group changed the runtime: %#v", runner.commands)
	}
}

// TestRunK8sGroupFailureRunsDiagnosticsBeforeCleanup collects diagnostics before group cleanup.
func TestRunK8sGroupFailureRunsDiagnosticsBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	runner := &fakeRunner{
		outputs: map[string][]byte{"k3d cluster list": []byte("dcgm-exporter-gpu 1/1 1/1\n")},
		results: map[string]e2eexec.Result{
			"dcgm-exporter-k8s.test": {ExitCode: 1, Stderr: []byte("Helm chart installation failed: helm timeout")},
		},
	}
	run := newK8sSuiteRun(
		context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	run.resultMarkers = true
	run.suiteResultMarkers = true
	err = run.runK8sGroup(&opts, scenario.PlanGroup{Name: "embedded-shared-gpu", Labels: []string{"sharedGpu"}})
	if err == nil {
		t.Fatal("runK8sGroup() error = nil, want suite failure")
	}
	if !strings.Contains(err.Error(), "Helm chart installation failed: helm timeout") {
		t.Fatalf("runK8sGroup() error = %v, want original Helm failure", err)
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

func TestK8sExecutorContinuesAfterGroupFailure(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests([]string{"--kubeconfig", filepath.Join(root, "kubeconfig")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	binaryRuns := 0
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if command.Name != "/tmp/dcgm-exporter-k8s.test" {
			return e2eexec.Result{}, false
		}
		binaryRuns++
		if binaryRuns == 1 {
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("first group failed")}, true
		}
		return e2eexec.Result{}, true
	}}
	run := &k8sSuiteRun{
		ctx:        context.Background(),
		stdout:     io.Discard,
		root:       root,
		runner:     runner,
		cfg:        config.Config{TestOptions: opts},
		binary:     "/tmp/dcgm-exporter-k8s.test",
		clusterCfg: clusterConfig(root, opts),
	}
	plan := scenario.Plan{Groups: []scenario.PlanGroup{
		{Name: "embedded-baseline", Labels: []string{"default"}},
		{Name: "embedded-hardware", Labels: []string{"profiling"}},
	}}
	err = run.executePlan(plan)
	if err == nil || !strings.Contains(err.Error(), "first group failed") {
		t.Fatalf("executePlan() error = %v, want first group failure", err)
	}
	if binaryRuns != 2 {
		t.Fatalf("suite binary runs = %d, want both groups", binaryRuns)
	}
}

func TestExternalK8sRunRetainsCLIResourcesWhenCleanupDisabled(t *testing.T) {
	root := t.TempDir()
	opts, err := parseTests([]string{"--kubeconfig", filepath.Join(root, "kubeconfig"), "--k3d-cgroup-v1=false", "--cleanup=false"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	run := newK8sSuiteRun(context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts}, "", "", nil, nil)
	if err := run.prepareCluster(); err != nil {
		t.Fatal(err)
	}
	if err := run.cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.commandCount("helm", "uninstall") != 0 || runner.commandCount("kubectl", "delete") != 0 {
		t.Fatalf("cleanup-disabled external run deleted resources: %#v", runner.commands)
	}
	if runner.commandCount("stat", "-fc", "%T", "/sys/fs/cgroup") != 0 {
		t.Fatalf("external Kubernetes run checked the local cgroup mode: %#v", runner.commands)
	}
}

// TestExternalK8sCleanupRefusesUnownedNamespaces protects external namespaces without run ownership.
func TestExternalK8sCleanupRefusesUnownedNamespaces(t *testing.T) {
	root := t.TempDir()
	opts, err := parseTests([]string{"--kubeconfig", filepath.Join(root, "kubeconfig")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	run := newK8sSuiteRun(context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts}, "", "", nil, nil)
	if err := run.prepareCluster(); err != nil {
		t.Fatal(err)
	}
	if err := run.cleanup(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing to delete namespace") {
		t.Fatalf("cleanup() error = %v, want refusal for namespaces another run owns", err)
	}
	if runner.commandCount("kubectl", "delete") != 0 {
		t.Fatalf("cleanup deleted unowned namespaces: %#v", runner.commands)
	}
}

// TestExternalK8sCleanupDeletesConfiguredNames checks that external cluster cleanup deletes only configured owned names.
func TestExternalK8sCleanupDeletesConfiguredNames(t *testing.T) {
	root := t.TempDir()
	opts, err := parseTests([]string{"--kubeconfig", filepath.Join(root, "kubeconfig")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{namespaceOwner: cluster.DefaultConfig().RunName}
	run := newK8sSuiteRun(context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts}, "", "", nil, nil)
	if err := run.prepareCluster(); err != nil {
		t.Fatal(err)
	}
	if err := run.cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if runner.commandCount("kubectl", "delete") != 2 {
		t.Fatalf("cleanup did not delete configured namespaces: %#v", runner.commands)
	}
}

// TestK8sSuiteChecksLocalAPIReadyBeforeImageAndRegistrySetup verifies local readiness runs before later setup work.
func TestK8sSuiteChecksLocalAPIReadyBeforeImageAndRegistrySetup(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts.ExporterImage = "registry.example/dcgm-exporter:dev"
	opts.CreateK8sImagePullSecret = true
	opts.DockerConfigDir = t.TempDir()
	selected := []scenario.Scenario{scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "default")}
	runner := &fakeRunner{}
	run := newK8sSuiteRun(
		context.Background(),
		io.Discard,
		root,
		runner,
		config.Config{TestOptions: opts},
		"",
		"",
		selected,
		scenario.RequirementsFor(selected, runConditions(opts)),
	)

	if err := run.prepareCluster(); err != nil {
		t.Fatalf("prepareCluster() error = %v", err)
	}
	if _, err := run.prepareProbeInputs(); err != nil {
		t.Fatalf("prepareProbeInputs() error = %v", err)
	}
	ready := runner.commandIndex("kubectl", "get", "--raw=/readyz")
	image := runner.commandIndex("docker", "image", "inspect", opts.ExporterImage)
	namespace := runner.commandIndex("kubectl", "create", "-f", "-")
	if ready < 0 || image < 0 || namespace < 0 {
		t.Fatalf("missing readiness, image, or namespace command: %#v", runner.commands)
	}
	if ready > image || image > namespace {
		t.Fatalf(
			"local setup order = readiness %d, image %d, namespace %d; want readiness before image before registry auth: %#v",
			ready,
			image,
			namespace,
			runner.commands,
		)
	}
}

// TestK8sSuiteChecksExternalAPIReadyBeforeRegistryAuth verifies external readiness runs before namespace and secret setup.
func TestK8sSuiteChecksExternalAPIReadyBeforeRegistryAuth(t *testing.T) {
	opts, err := parseTests([]string{"--kubeconfig", "/tmp/external-kubeconfig"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts.CreateK8sImagePullSecret = true
	opts.DockerConfigDir = t.TempDir()
	selected := []scenario.Scenario{scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "default")}
	runner := &fakeRunner{}
	run := newK8sSuiteRun(
		context.Background(),
		io.Discard,
		t.TempDir(),
		runner,
		config.Config{TestOptions: opts},
		"",
		"",
		selected,
		scenario.RequirementsFor(selected, runConditions(opts)),
	)

	if err := run.prepareCluster(); err != nil {
		t.Fatalf("prepareCluster() error = %v", err)
	}
	if _, err := run.prepareProbeInputs(); err != nil {
		t.Fatalf("prepareProbeInputs() error = %v", err)
	}
	ready := runner.commandIndex("kubectl", "get", "--raw=/readyz")
	namespace := runner.commandIndex("kubectl", "create", "-f", "-")
	if ready < 0 || namespace < 0 {
		t.Fatalf("missing readiness or namespace command: %#v", runner.commands)
	}
	if ready > namespace {
		t.Fatalf("external setup order = readiness %d, namespace %d; want readiness before registry auth: %#v", ready, namespace, runner.commands)
	}
}

// TestK8sSuiteSkipsAPIReadyWithoutKubernetesRequirement verifies non-Kubernetes selection does not contact a cluster.
func TestK8sSuiteSkipsAPIReadyWithoutKubernetesRequirement(t *testing.T) {
	runner := &fakeRunner{}
	run := &k8sSuiteRun{
		ctx:          context.Background(),
		stdout:       io.Discard,
		runner:       runner,
		clusterCfg:   cluster.DefaultConfig(),
		requirements: scenario.RequirementsFor([]scenario.Scenario{{Suite: scenario.SuiteContainer, Name: "imageStartup"}}, scenario.RunConditions{}),
	}

	if err := run.checkKubernetesAPIReady(); err != nil {
		t.Fatalf("checkKubernetesAPIReady() error = %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("non-Kubernetes selection ran cluster commands: %#v", runner.commands)
	}
}

// TestK8sSuiteStopsOnAPIReadinessFailureBeforeRegistryAuth verifies the raw API failure stops before namespace or secret mutation.
func TestK8sSuiteStopsOnAPIReadinessFailureBeforeRegistryAuth(t *testing.T) {
	opts, err := parseTests([]string{"--kubeconfig", "/tmp/external-kubeconfig"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts.CreateK8sImagePullSecret = true
	opts.DockerConfigDir = t.TempDir()
	selected := []scenario.Scenario{scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "default")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"kubectl get --raw=/readyz": {ExitCode: 1, Stderr: []byte("API not ready")},
	}}
	run := newK8sSuiteRun(
		context.Background(),
		io.Discard,
		t.TempDir(),
		runner,
		config.Config{TestOptions: opts},
		"",
		"",
		selected,
		scenario.RequirementsFor(selected, runConditions(opts)),
	)

	err = run.prepareCluster()
	if err == nil {
		t.Fatal("prepareCluster() error = nil")
	}
	for _, want := range []string{"kubernetes API readiness failed", "API not ready"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("prepareCluster() error = %v, missing %q", err, want)
		}
	}
	if runner.commandIndex("kubectl", "create", "-f", "-") >= 0 ||
		runner.commandIndex("kubectl", "create", "secret") >= 0 ||
		runner.commandIndex("kubectl", "apply", "-f", "-") >= 0 {
		t.Fatalf("API readiness failure changed registry resources: %#v", runner.commands)
	}
}

// TestK8sSuiteStopsLocalAPIReadinessFailureBeforeImageAndRegistrySetup verifies a local API failure stops later setup work.
func TestK8sSuiteStopsLocalAPIReadinessFailureBeforeImageAndRegistrySetup(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts.ExporterImage = "registry.example/dcgm-exporter:dev"
	opts.CreateK8sImagePullSecret = true
	opts.DockerConfigDir = t.TempDir()
	selected := []scenario.Scenario{scenario.MustFind(scenario.Catalog, scenario.SuiteK8s, "default")}
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"kubectl get --raw=/readyz": {ExitCode: 1, Stderr: []byte("API not ready")},
	}}
	run := newK8sSuiteRun(
		context.Background(),
		io.Discard,
		root,
		runner,
		config.Config{TestOptions: opts},
		"",
		"",
		selected,
		scenario.RequirementsFor(selected, runConditions(opts)),
	)

	err = run.prepareCluster()
	if err == nil {
		t.Fatal("prepareCluster() error = nil")
	}
	for _, want := range []string{"kubernetes API readiness failed", "API not ready"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("prepareCluster() error = %v, missing %q", err, want)
		}
	}
	if runner.commandIndex("docker", "image", "inspect", opts.ExporterImage) >= 0 ||
		runner.commandIndex("docker", "login") >= 0 ||
		runner.commandIndex("kubectl", "create", "-f", "-") >= 0 ||
		runner.commandIndex("kubectl", "create", "secret") >= 0 {
		t.Fatalf("local API readiness failure ran later setup work: %#v", runner.commands)
	}
}

// TestRunK8sGroupFailsWhenCleanupFails checks that a Kubernetes group returns a cleanup failure.
func TestRunK8sGroupFailsWhenCleanupFails(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests(nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	runner := &fakeRunner{
		resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
			args := strings.Join(command.Args, " ")
			if command.Name == "helm" &&
				strings.Contains(args, "upgrade --install nvidia-device-plugin") &&
				!strings.Contains(args, "--set-file") {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("injected cleanup failure")}, true
			}
			return e2eexec.Result{}, false
		},
	}

	run := newK8sSuiteRun(
		context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	run.resultMarkers = true
	err = run.runK8sGroup(&opts, scenario.PlanGroup{Name: "embedded-shared-gpu", Labels: []string{"sharedGpu"}})
	if err == nil {
		t.Fatal("runK8sGroup() error = nil, want cleanup failure")
	}
	if !strings.Contains(err.Error(), "cleanup k8s group embedded-shared-gpu") ||
		!strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("runK8sGroup() error = %v, want cleanup context", err)
	}
	if strings.Contains(stdout.String(), "&&&& PASSED dcgm_exporter_e2e_group_embedded_shared_gpu") {
		t.Fatalf("cleanup failure emitted a passing marker:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_group_embedded_shared_gpu") {
		t.Fatalf("cleanup failure did not emit a failed marker:\n%s", stdout.String())
	}
}

// TestRunK8sGroupReportsStandaloneDCGMCleanupFailure checks that a failed DCGM cleanup fails the group.
func TestRunK8sGroupReportsStandaloneDCGMCleanupFailure(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests([]string{"--cleanup=false"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	opts.DCGMImage = "registry.example/dcgm:4.5.3"
	clusterCfg := cluster.DefaultConfig()
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	var stdout bytes.Buffer
	runner := &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if command.Name == "kubectl" && containsArgs(command.Args, "delete", "namespace", clusterCfg.DCGMNamespace) {
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("injected DCGM namespace cleanup failure")}, true
		}
		return e2eexec.Result{}, false
	}}
	run := newK8sSuiteRun(
		context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	run.clusterCfg = clusterCfg
	run.resultMarkers = true
	groups := []scenario.PlanGroup{
		{Name: "standalone-baseline", Labels: []string{"remoteDcgm"}},
		{Name: "standalone-ipv6", Labels: []string{"remoteDcgmIPv6Hostname"}},
	}
	groupOptions := k8sGroupOptions(opts, groups, 0)
	if !groupOptions.Cleanup {
		t.Fatal("standalone baseline must clean up before the later IPv6 group")
	}

	err = run.runK8sGroup(&groupOptions, groups[0])
	if err == nil || !strings.Contains(err.Error(), "injected DCGM namespace cleanup failure") {
		t.Fatalf("runK8sGroup() error = %v, want standalone DCGM cleanup failure", err)
	}
	if runner.commandIndex("kubectl", "delete", "namespace", clusterCfg.DCGMNamespace, "--ignore-not-found=true") < 0 {
		t.Fatalf("standalone namespace cleanup was not attempted: %#v", runner.commands)
	}
	if strings.Contains(stdout.String(), "&&&& PASSED dcgm_exporter_e2e_group_standalone_baseline") {
		t.Fatalf("cleanup failure emitted a passing marker:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_group_standalone_baseline") {
		t.Fatalf("cleanup failure did not emit a failed group marker:\n%s", stdout.String())
	}
}

// TestRunK8sGroupReportsDRACleanupFailure checks that a failed DRA driver cleanup fails the group.
func TestRunK8sGroupReportsDRACleanupFailure(t *testing.T) {
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests([]string{"--dra-configure", "true"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	clusterCfg := cluster.DefaultConfig()
	clusterCfg.Root = root
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	var stdout bytes.Buffer
	runner := &fakeRunner{
		// No ResourceSlices before the driver install, and the driver's slice after it.
		resultSequences: map[string][]e2eexec.Result{
			"kubectl get resourceslices -o json": {
				{Stdout: []byte(`{"items":[]}`)},
				{Stdout: []byte(`{"items":[{"spec":{"driver":"gpu.nvidia.com","devices":[{"attributes":{"type":{"string":"gpu"}}}]}}]}`)},
			},
		},
		resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
			if command.Name == "helm" && containsArgs(command.Args, "uninstall", "nvidia-dra-driver-gpu") {
				return e2eexec.Result{ExitCode: 1, Stderr: []byte("injected DRA cleanup failure")}, true
			}
			return e2eexec.Result{}, false
		},
	}
	run := newK8sSuiteRun(
		context.Background(), &stdout, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	run.clusterCfg = clusterCfg
	run.resultMarkers = true

	err = run.runK8sGroup(&opts, scenario.PlanGroup{Name: "embedded-dra", Labels: []string{"dra"}})
	if err == nil || !strings.Contains(err.Error(), "cleanup k8s group embedded-dra") ||
		!strings.Contains(err.Error(), "injected DRA cleanup failure") {
		t.Fatalf("runK8sGroup() error = %v, want DRA cleanup failure", err)
	}
	if !strings.Contains(stdout.String(), "&&&& FAILED dcgm_exporter_e2e_group_embedded_dra") {
		t.Fatalf("cleanup failure did not emit a failed group marker:\n%s", stdout.String())
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

// TestPrepareRegistryAuthLogsInAndCreatesPullSecret logs in and creates the cluster pull secret.
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
	dockerConfigPath, cleanup, err := prepareRegistryAuth(context.Background(), &stdout, runner, clusterCfg, &opts)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if opts.K8sImagePullSecret != "dcgm-exporter-e2e-registry-auth" {
		t.Fatalf("pull secret = %q", opts.K8sImagePullSecret)
	}
	if dockerConfigPath != filepath.Join(dockerConfig, "config.json") {
		t.Fatalf("Docker config path = %q, want %q", dockerConfigPath, filepath.Join(dockerConfig, "config.json"))
	}
	if !runner.hasCommandArg("--password-stdin") {
		t.Fatalf("docker login did not use password stdin: %#v", runner.commands)
	}
	if !runner.hasCommandArg("--from-file=.dockerconfigjson=" + filepath.Join(dockerConfig, "config.json")) {
		t.Fatalf("pull secret did not use Docker config: %#v", runner.commands)
	}
	for _, command := range runner.commands {
		if command.Name != "kubectl" {
			continue
		}
		if containsArgs(command.Args, "-n", "gpu-operator") || strings.Contains(string(command.Stdin), `"name":"gpu-operator"`) {
			t.Fatalf("registry auth prepared the GPU Operator namespace before a selected install: %#v", runner.commands)
		}
	}
	if strings.Contains(stdout.String(), "secret-password") {
		t.Fatalf("password leaked to stdout:\n%s", stdout.String())
	}
}

func TestDockerRunSessionIsolatesAmbientCredentials(t *testing.T) {
	ambient := t.TempDir()
	ambientConfig := filepath.Join(ambient, "config.json")
	if err := os.WriteFile(ambientConfig, []byte(`{"auths":{"nvcr.io":{"auth":"stale"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", ambient)

	opts := config.TestOptions{}
	base := &fakeRunner{}
	runner, login, cleanup, err := prepareDockerRunSession(context.Background(), io.Discard, base, &opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := login(); err != nil {
		t.Fatal(err)
	}
	owned := opts.DockerConfigDir
	if owned == "" || owned == ambient {
		t.Fatalf("DockerConfigDir = %q, want isolated directory", owned)
	}
	runner.Run(context.Background(), e2eexec.Command{Name: "docker", Args: []string{"info"}, Env: []string{"DOCKER_CONFIG=" + ambient}})
	command := base.commands[len(base.commands)-1]
	if !hasArg(command.Env, "DOCKER_CONFIG="+owned) || hasArg(command.Env, "DOCKER_CONFIG="+ambient) {
		t.Fatalf("docker command environment = %#v, want only isolated config", command.Env)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned Docker config remains after cleanup: %v", err)
	}
	if content, err := os.ReadFile(ambientConfig); err != nil || !strings.Contains(string(content), "stale") {
		t.Fatalf("ambient Docker config changed: content=%q err=%v", content, err)
	}
}

func TestDockerRunSessionLogsInOnceAndRetainsCallerConfig(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	callerConfig := t.TempDir()
	opts := config.TestOptions{
		DockerConfigDir:      callerConfig,
		DockerRegistryLogins: []string{"nvcr.io,user," + passwordFile},
	}
	base := &fakeRunner{}
	_, login, cleanup, err := prepareDockerRunSession(context.Background(), io.Discard, base, &opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if base.commandCount("docker", "login") != 0 {
		t.Fatalf("docker login ran before bootstrap: %#v", base.commands)
	}
	if err := login(); err != nil {
		t.Fatal(err)
	}
	if base.commandCount("docker", "login") != 1 {
		t.Fatalf("docker login count = %d, want 1", base.commandCount("docker", "login"))
	}
	if len(opts.DockerRegistryLogins) != 0 || opts.DockerRegistryLoginFile != "" || !opts.CreateK8sImagePullSecret {
		t.Fatalf("run session did not consume login inputs: %#v", opts)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(callerConfig); err != nil {
		t.Fatalf("caller-owned Docker config was removed: %v", err)
	}
}

// TestExternalRegistryLoginUsesDockerClientWithoutDaemon logs in through Docker without daemon access.
func TestExternalRegistryLoginUsesDockerClientWithoutDaemon(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600))
	loginFile := filepath.Join(t.TempDir(), "registry-logins")
	require.NoError(t, os.WriteFile(loginFile, []byte("registry.example,user,"+passwordFile+"\n"), 0o600))
	opts := config.TestOptions{
		BootstrapMode: config.BootstrapInstall,
		BootstrapComponents: map[config.BootstrapComponent]config.BootstrapMode{
			config.BootstrapKubernetesTools: config.BootstrapSkip,
		},
		Kubeconfig:              "/tmp/kubeconfig",
		Scenarios:               []string{"k8s/serviceAccess"},
		ExporterImage:           "registry.example/dcgm-exporter:dev",
		DockerRegistryLoginFile: loginFile,
	}
	selected := scenario.Selected(scenario.Catalog, config.Config{TestOptions: opts})
	requirements := scenario.RequirementsFor(selected, runConditions(opts))
	if scenario.HasRequirement(requirements, scenario.Requirement{Kind: scenario.RequirementDocker}) ||
		!scenario.HasRequirement(requirements, scenario.Requirement{Kind: scenario.RequirementDockerCLI}) {
		t.Fatalf("external registry requirements = %#v, want Docker CLI without daemon", requirements)
	}
	baseRunner := &fakeRunner{results: map[string]e2eexec.Result{"docker info": {ExitCode: 1}}}
	dockerClientRequired := scenario.HasRequirement(requirements, scenario.Requirement{Kind: scenario.RequirementDockerCLI})
	runner, login, cleanup, err := prepareDockerRunSession(context.Background(), io.Discard, baseRunner, &opts, dockerClientRequired)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("clean up Docker session: %v", err)
		}
	})

	if err := prepareRunPrerequisites(context.Background(), io.Discard, t.TempDir(), runner, &opts, requirements, login); err != nil {
		t.Fatal(err)
	}
	clusterCfg := clusterConfig(t.TempDir(), opts)
	if err := prepareRegistryAuthForNamespaces(context.Background(), io.Discard, runner, clusterCfg, &opts, []string{clusterCfg.Namespace}); err != nil {
		t.Fatal(err)
	}
	if baseRunner.commandCount("docker", "login") != 1 {
		t.Fatalf("docker login count = %d, want 1", baseRunner.commandCount("docker", "login"))
	}
	if baseRunner.commandIndex("kubectl", "create", "secret", "generic", "dcgm-exporter-e2e-registry-auth") < baseRunner.commandIndex("docker", "login") {
		t.Fatalf("pull secret was not created after registry login: %#v", baseRunner.commands)
	}
	for _, command := range baseRunner.commands {
		if command.Name == "docker" && containsArgs(command.Args, "info") ||
			command.Name == "systemctl" || command.Name == "apt-get" || command.Name == "dnf" ||
			containsArgs(command.Args, "setfacl") {
			t.Fatalf("external registry auth probed or repaired a Docker daemon: %#v", baseRunner.commands)
		}
	}
}

// TestExternalK8sDCGMImageWorkDoesNotUseDockerSession verifies target image references do not make Docker a controller prerequisite.
func TestExternalK8sDCGMImageWorkDoesNotUseDockerSession(t *testing.T) {
	opts, err := parseTests([]string{
		"--kubeconfig", "/tmp/kubeconfig",
		"--scenario", "k8s/remoteDcgm",
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	requirements := scenario.RequirementsFor(
		scenario.Selected(scenario.Catalog, config.Config{TestOptions: opts}),
		runConditions(opts),
	)
	required := scenario.HasRequirement(requirements, scenario.Requirement{Kind: scenario.RequirementDocker})
	if required {
		t.Fatal("external Kubernetes DCGM image work unexpectedly requested an isolated Docker session")
	}
	runner := &fakeRunner{}
	run := newK8sSuiteRun(
		context.Background(),
		io.Discard,
		t.TempDir(),
		runner,
		config.Config{TestOptions: opts},
		"dcgm-exporter-k8s.test",
		"",
		scenario.Selected(scenario.Catalog, config.Config{TestOptions: opts}),
		requirements,
	)
	if _, err := run.prepareProbeInputs(); err != nil {
		t.Fatalf("prepareProbeInputs() error = %v", err)
	}
	for _, command := range runner.commands {
		if command.Name == "docker" {
			t.Fatalf("external Kubernetes DCGM image work used Docker: %#v", command)
		}
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

func TestDockerLoginFailureIncludesStdout(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := configWithRegistryLogin("nvcr.io,user," + passwordFile)
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker login nvcr.io -u user --password-stdin": {ExitCode: 1, Stdout: []byte("registry denied access")},
	}}

	_, _, err := prepareDockerRegistryLogin(context.Background(), io.Discard, runner, &opts)
	if err == nil || !strings.Contains(err.Error(), "registry denied access") {
		t.Fatalf("prepareDockerRegistryLogin() error = %v, want stdout diagnostic", err)
	}
}

func TestDockerLoginFailureIncludesExecutionError(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("secret-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := configWithRegistryLogin("nvcr.io,user," + passwordFile)
	runner := &fakeRunner{results: map[string]e2eexec.Result{
		"docker login nvcr.io -u user --password-stdin": {ExitCode: -1, Err: errors.New("docker executable not found")},
	}}

	_, _, err := prepareDockerRegistryLogin(context.Background(), io.Discard, runner, &opts)
	if err == nil || !strings.Contains(err.Error(), "docker executable not found") {
		t.Fatalf("prepareDockerRegistryLogin() error = %v, want execution diagnostic", err)
	}
}

// TestPrepareRegistryAuthReusesConfiguredNamespace checks that registry authentication reuses the configured namespace.
func TestPrepareRegistryAuthReusesConfiguredNamespace(t *testing.T) {
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
		t.Fatalf("prepareRegistryAuthForNamespaces() error = %v", err)
	}
	create, created := runner.commandWithArgs("kubectl", "create", "-f", "-")
	if !created || !strings.Contains(string(create.Stdin), `"e2eRunName":"dcgm-exporter"`) || !runner.hasCommandArg("login") {
		t.Fatalf("registry setup did not create an owned namespace and log in: %#v", runner.commands)
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

// TestRunClusterStatusDerivesNamesFromRunName checks that cluster status derives resource names from the run name.
func TestRunClusterStatusDerivesNamesFromRunName(t *testing.T) {
	var stdout, stderr bytes.Buffer
	runner := &fakeRunner{}
	err := runWithRunner([]string{"cluster", "status", "--kubeconfig", "/tmp/kubeconfig", "--run-name", "smoke-4"}, &stdout, &stderr, runner)
	if err != nil {
		t.Fatalf("runWithRunner() error = %v; stderr = %s", err, stderr.String())
	}
	for _, name := range []string{"smoke-4", "smoke-4-dcgm"} {
		if !runner.hasCommandArg(name) {
			t.Errorf("cluster status did not use derived name %q; commands = %#v", name, runner.commands)
		}
	}
}

func TestLocalClusterConfigDerivesNetworkNameFromCustomClusterName(t *testing.T) {
	opts := config.TestOptions{
		K3SImage:           "mirror.example/rancher/k3s:v1.35.3-k3s1",
		K3DNodeBaseImage:   "nvcr.io/nvidia/cuda:13.2.1-base-ubuntu24.04",
		K3DNodeOutputImage: "dcgm-exporter/k3s-nvidia:v1.35.3-test",
		WaitTimeout:        "3m",
		Dependencies: config.Dependencies{
			DevicePluginVersion:     "0.19.0",
			ContainerToolkitVersion: "1.19.1-1",
		},
	}
	clusterCfg := cluster.DefaultConfig()
	clusterCfg.ClusterName = "custom-cluster"

	localCfg, err := localClusterConfig(t.TempDir(), clusterCfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if localCfg.NetworkName != "k3d-custom-cluster" {
		t.Fatalf("NetworkName = %q, want %q", localCfg.NetworkName, "k3d-custom-cluster")
	}
	if localCfg.WaitTimeout != "3m" {
		t.Fatalf("WaitTimeout = %q, want %q", localCfg.WaitTimeout, "3m")
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

func configWithRegistryLogin(login string) config.TestOptions {
	return config.TestOptions{DockerRegistryLogins: []string{login}}
}

func configurePackageTestEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("E2E_EXPORTER_IMAGE", "registry.example/dcgm-exporter:dev")
	t.Setenv("E2E_DCGM_IMAGE", "nvcr.io/nvidia/cloud-native/dcgm:4.5.3-1-ubuntu24.04")
	t.Setenv("E2E_CONTAINER_TOOLKIT_TEST_IMAGE", "registry.example/cuda:dev")
	t.Setenv("NVIDIA_CONTAINER_TOOLKIT_VERSION", "1.19.1-1")
	t.Setenv("E2E_DCGM_VERSION", "4.5.3")
}

// clusterActionContextRunner records the lifecycle context and can wait for its cancellation.
type clusterActionContextRunner struct {
	firstContext        context.Context
	waitForCancellation bool
	canceled            bool
}

// Run returns a simulated action result and records whether the CLI context was canceled.
func (r *clusterActionContextRunner) Run(ctx context.Context, _ e2eexec.Command) e2eexec.Result {
	if r.firstContext == nil {
		r.firstContext = ctx
	}
	if r.waitForCancellation {
		select {
		case <-ctx.Done():
			r.canceled = true
			return e2eexec.Result{ExitCode: 1, Stderr: []byte(ctx.Err().Error())}
		case <-time.After(time.Second):
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("helper did not receive the CLI timeout")}
		}
	}
	return e2eexec.Result{ExitCode: 1, Stderr: []byte("captured helper context")}
}

// fakeRunner records commands and supplies configurable results for CLI tests.
type fakeRunner struct {
	outputs         map[string][]byte
	results         map[string]e2eexec.Result
	resultSequences map[string][]e2eexec.Result
	downloads       map[string][]byte
	commands        []e2eexec.Command
	onRun           func(e2eexec.Command)
	resultMatcher   func(e2eexec.Command) (e2eexec.Result, bool)
	// namespaceOwner is the run-name label on namespaces the suite binary created.
	namespaceOwner string
}

// Run records a command and returns the configured fake result.
func (f *fakeRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	f.commands = append(f.commands, command)
	if f.onRun != nil {
		f.onRun(command)
	}
	for _, respond := range []func(e2eexec.Command) (e2eexec.Result, bool){
		f.configuredResult,
		f.toolResult,
		f.dockerResult,
		f.fileWritingResult,
	} {
		if result, ok := respond(command); ok {
			return result
		}
	}
	if f.outputs != nil {
		return streamFakeResult(command, e2eexec.Result{Stdout: f.outputs[commandKey(command)]})
	}
	return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

// configuredResult returns a configured result for a matching command.
func (f *fakeRunner) configuredResult(command e2eexec.Command) (e2eexec.Result, bool) {
	if f.resultMatcher != nil {
		if result, ok := f.resultMatcher(command); ok {
			return streamFakeResult(command, result), true
		}
	}
	if strings.HasSuffix(command.Name, "dcgm-exporter-static.test") && hasArg(command.Args, "-test.list") {
		return e2eexec.Result{Stdout: []byte(strings.Join(staticTestNames(), "\n") + "\n")}, true
	}
	key := commandKey(command)
	if results := f.resultSequences[key]; len(results) != 0 {
		f.resultSequences[key] = results[1:]
		return streamFakeResult(command, results[0]), true
	}
	if result, ok := f.results[key]; ok {
		return streamFakeResult(command, result), true
	}
	if result, ok := f.results[filepath.Base(command.Name)]; ok {
		return streamFakeResult(command, result), true
	}
	return e2eexec.Result{}, false
}

// toolResult stubs results for host tools, Kubernetes namespace lookups, and node label lookups.
func (f *fakeRunner) toolResult(command e2eexec.Command) (e2eexec.Result, bool) {
	switch {
	case command.Name == "dpkg-query" && containsArgs(command.Args, "nvidia-container-toolkit"):
		return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ii  nvidia-container-toolkit=1.19.1-1\n" +
			"ii  nvidia-container-toolkit-base=1.19.1-1\n" +
			"ii  libnvidia-container-tools=1.19.1-1\n" +
			"ii  libnvidia-container1=1.19.1-1\n")}), true
	case command.Name == "kubectl" && len(command.Args) >= 3 && command.Args[0] == "get" && command.Args[1] == "namespace":
		return f.namespaceResult(command), true
	case command.Name == "kubectl" && containsArgs(command.Args, "get", "nodes", "-l"):
		// No node matches a label selector, so kubectl prints no names.
		return streamFakeResult(command, e2eexec.Result{Stderr: []byte("No resources found\n")}), true
	case command.Name == "stat" && strings.Join(command.Args, " ") == "-fc %T /sys/fs/cgroup":
		return streamFakeResult(command, e2eexec.Result{Stdout: []byte("cgroup2fs\n")}), true
	}
	return e2eexec.Result{}, false
}

// namespaceResult reports a namespace as absent, or, for an ownership lookup,
// as owned by the run whose namespace manifest this runner last created.
func (f *fakeRunner) namespaceResult(command e2eexec.Command) e2eexec.Result {
	if !containsArgs(command.Args, "--ignore-not-found=true") {
		return e2eexec.Result{ExitCode: 1}
	}
	payload := fmt.Sprintf(`{"metadata":{"uid":"test-namespace-uid","labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":%q}},"status":{"phase":"Active"}}`, f.createdNamespaceRunName(command.Args[2]))
	return streamFakeResult(command, e2eexec.Result{Stdout: []byte(payload)})
}

// createdNamespaceRunName returns the run-name label of the latest manifest this runner
// created for namespace, or the configured suite namespace owner when it created none.
func (f *fakeRunner) createdNamespaceRunName(namespace string) string {
	runName := f.namespaceOwner
	for i := len(f.commands) - 2; i >= 0; i-- {
		prior := f.commands[i]
		if prior.Name != "kubectl" || !containsArgs(prior.Args, "create", "-f", "-") {
			continue
		}
		var namespaceObject struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(prior.Stdin, &namespaceObject); err != nil || namespaceObject.Metadata.Name != namespace {
			continue
		}
		runName = namespaceObject.Metadata.Labels["e2eRunName"]
		break
	}
	return runName
}

// dockerResult stubs common Docker image, cluster, and local image checks.
func (f *fakeRunner) dockerResult(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name != "docker" {
		return e2eexec.Result{}, false
	}
	if len(command.Args) == 3 && command.Args[0] == "image" && command.Args[1] == "inspect" {
		if _, configured := f.outputs[commandKey(command)]; !configured {
			return streamFakeResult(command, e2eexec.Result{Stdout: []byte(`[{"Os":"linux","Architecture":"amd64"}]`)}), true
		}
	}
	if containsArgs(command.Args, "ps", "-a", "--format", "{{.Names}}") {
		clusterName := cluster.DefaultConfig().ClusterName
		nodes := "k3d-" + clusterName + "-server-0\nk3d-" + clusterName + "-agent-0\n"
		return streamFakeResult(command, e2eexec.Result{Stdout: []byte(nodes)}), true
	}
	if len(command.Args) >= 3 {
		switch strings.Join(command.Args[:2], " ") {
		case "network inspect":
			return streamFakeResult(command, e2eexec.Result{ExitCode: 1, Stderr: []byte("Error response from daemon: network " + command.Args[2] + " not found\n")}), true
		case "volume inspect":
			return streamFakeResult(command, e2eexec.Result{ExitCode: 1, Stderr: []byte("Error response from daemon: get " + command.Args[2] + ": no such volume\n")}), true
		}
	}
	if containsArgs(command.Args, "exec", "ctr", "--namespace", "k8s.io", "images", "check") {
		return streamFakeResult(command, e2eexec.Result{Stdout: []byte("ready\n")}), true
	}
	return e2eexec.Result{}, false
}

// fileWritingResult simulates Go, NVIDIA toolkit, and curl commands that write files.
func (f *fakeRunner) fileWritingResult(command e2eexec.Command) (e2eexec.Result, bool) {
	var err error
	switch command.Name {
	case "go":
		err = writeFakeGoBuildOutput(command.Args)
	case "nvidia-ctk":
		err = writeFakeCDISpec(command.Args)
	case "curl":
		err = f.writeCurlOutput(command)
	default:
		return e2eexec.Result{}, false
	}
	if err != nil {
		return e2eexec.Result{ExitCode: 1, Err: err}, true
	}
	return e2eexec.Result{}, true
}

// writeFakeGoBuildOutput creates the executable file requested by a fake Go build command.
func writeFakeGoBuildOutput(args []string) error {
	for index, arg := range args {
		if arg == "-o" && index+1 < len(args) {
			if err := writeExecutableFile(args[index+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeFakeCDISpec writes the small CDI spec used by NVIDIA toolkit setup tests.
func writeFakeCDISpec(args []string) error {
	data := []byte("kind: nvidia.com/gpu\ndevices:\n  - name: all\ncontainerEdits:\n  mounts:\n" +
		"    - containerPath: /usr/lib/x86_64-linux-gnu/libnvsdm.so.1\n" +
		"    - containerPath: /usr/lib/x86_64-linux-gnu/libibmad.so.5\n" +
		"    - containerPath: /usr/lib/x86_64-linux-gnu/libibumad.so.3\n" +
		"    - containerPath: /usr/lib/x86_64-linux-gnu/libmnl.so.0\n")
	for _, arg := range args {
		path, ok := strings.CutPrefix(arg, "--output=")
		if !ok {
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// staticTestNames returns static suite test names from the shared scenario catalog.
func staticTestNames() []string {
	var names []string
	for _, entry := range scenario.Catalog {
		if entry.Suite != scenario.SuiteStatic {
			continue
		}
		for _, name := range strings.Split(entry.ResultName, ",") {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
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

func (f *fakeRunner) commandCount(name string, args ...string) int {
	count := 0
	for _, command := range f.commands {
		if command.Name == name && containsArgs(command.Args, args...) {
			count++
		}
	}
	return count
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

func containsArgs(haystack []string, needles ...string) bool {
	for _, needle := range needles {
		if !hasArg(haystack, needle) {
			return false
		}
	}
	return true
}

// requireArgs checks that a command argument list includes each required value.
func requireArgs(t *testing.T, label string, args []string, want ...string) {
	t.Helper()
	for _, value := range want {
		if !hasArg(args, value) {
			t.Fatalf("%s missing %q: %#v", label, value, args)
		}
	}
}

// requireOutput checks that command output contains every expected string.
func requireOutput(t *testing.T, output string, want ...string) {
	t.Helper()
	for _, text := range want {
		if !strings.Contains(output, text) {
			t.Fatalf("output missing %q:\n%s", text, output)
		}
	}
}

// requireOutputCount checks that command output contains an expected string the requested number of times.
func requireOutputCount(t *testing.T, output, text string, want int) {
	t.Helper()
	if got := strings.Count(output, text); got != want {
		t.Fatalf("%q count = %d, want %d:\n%s", text, got, want, output)
	}
}

// requireNoOutput checks that command output excludes every forbidden string.
func requireNoOutput(t *testing.T, output string, unwanted ...string) {
	t.Helper()
	for _, text := range unwanted {
		if strings.Contains(output, text) {
			t.Fatalf("output unexpectedly contains %q:\n%s", text, output)
		}
	}
}

// requireRunnerArgs checks that the fake runner received each required command argument.
func requireRunnerArgs(t *testing.T, runner *fakeRunner, want ...string) {
	t.Helper()
	for _, value := range want {
		if !runner.hasCommandArg(value) {
			t.Fatalf("no command received %q; commands = %#v", value, runner.commands)
		}
	}
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

// TestRunK8sGroupRestoresDevicePluginWhenCleanupDisabled restores temporary device-plugin changes.
func TestRunK8sGroupRestoresDevicePluginWhenCleanupDisabled(t *testing.T) {
	for _, group := range []scenario.PlanGroup{
		{Name: "embedded-mig", Labels: []string{"mig"}},
		{Name: "embedded-shared-gpu", Labels: []string{"sharedGpu"}},
		{Name: "embedded-renamed-gpu", Labels: []string{"customResourceNames"}},
	} {
		for _, suiteFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/suite failure %t", group.Name, suiteFails), func(t *testing.T) {
				runner := runRetainedK8sGroup(t, group, suiteFails)
				suite := runner.commandIndex("/tmp/dcgm-exporter-k8s.test")
				restore := runner.commandLastIndex("helm", "upgrade", "--install", "nvidia-device-plugin")
				if suite < 0 || restore <= suite {
					t.Fatalf("device plugin was not restored after the suite: %#v", runner.commands)
				}
			})
		}
	}
}

// runRetainedK8sGroup runs one group with --cleanup=false, optionally failing its
// suite binary, and checks that no retained Kubernetes resource was deleted.
func runRetainedK8sGroup(t *testing.T, group scenario.PlanGroup, suiteFails bool) *fakeRunner {
	t.Helper()
	root := t.TempDir()
	writeTestVersions(t, root)
	opts, err := parseTests([]string{"--cleanup=false"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	runner := suiteFailureRunner(suiteFails)
	run := newK8sSuiteRun(
		context.Background(), io.Discard, root, runner, config.Config{TestOptions: opts},
		"/tmp/dcgm-exporter-k8s.test", "", nil, nil,
	)
	err = run.runK8sGroup(&opts, group)
	if (err != nil) != suiteFails {
		t.Fatalf("runK8sGroup() = %v", err)
	}
	if runner.hasCommandArg("delete") {
		t.Fatal("retained Kubernetes resources were deleted")
	}
	return runner
}

// TestRunK8sGroupRestoresNVSwitchRuntimeWhenCleanupDisabled restores temporary NVSwitch runtime changes.
func TestRunK8sGroupRestoresNVSwitchRuntimeWhenCleanupDisabled(t *testing.T) {
	for _, suiteFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("suite failure %t", suiteFails), func(t *testing.T) {
			runner := runRetainedK8sGroup(t, scenario.PlanGroup{Name: "embedded-nvswitch", Labels: []string{"nvswitch"}}, suiteFails)
			if !runner.hasDockerArgsContaining(`mode = "legacy"`) || !runner.hasDockerArgsContaining("rm -f /etc/cdi/nvidia-e2e.yaml") {
				t.Fatalf("runtime restoration missing: %#v", runner.commands)
			}
		})
	}
}

// suiteFailureRunner returns a fake runner that can fail the Kubernetes suite binary.
func suiteFailureRunner(fail bool) *fakeRunner {
	return &fakeRunner{resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
		if fail && strings.HasSuffix(command.Name, "dcgm-exporter-k8s.test") {
			return e2eexec.Result{ExitCode: 1, Stderr: []byte("suite failed")}, true
		}
		return e2eexec.Result{}, false
	}}
}

// hasDockerArgsContaining checks whether any Docker command contains the requested text.
func (f *fakeRunner) hasDockerArgsContaining(text string) bool {
	for _, command := range f.commands {
		if command.Name == "docker" && strings.Contains(strings.Join(command.Args, " "), text) {
			return true
		}
	}
	return false
}

// TestK8sGroupOptionsRetainsOnlyFinalSelectedInstallation checks cleanup between
// configurations and retention of the last selected installation in each family.
func TestK8sGroupOptionsRetainsOnlyFinalSelectedInstallation(t *testing.T) {
	groups := []scenario.PlanGroup{
		{Name: "gpu-operator-baseline", Labels: []string{"gpuOperator"}},
		{Name: "gpu-operator-main-embedded", Labels: []string{"gpuOperatorMainEmbedded"}},
		{Name: "gpu-operator-main-remote", Labels: nil}, // Skipped groups do not need cleanup.
		{Name: "embedded-baseline", Labels: []string{"startup"}},
		{Name: "gpu-operator-main-dra", Labels: []string{"gpuOperatorMainDRA"}},
	}
	firstOptions := k8sGroupOptions(config.TestOptions{Cleanup: false}, groups, 0)
	if !firstOptions.Cleanup {
		t.Fatal("earlier non-main Operator group must clean up before a main installation")
	}
	if clusterFeatureConfig("/tmp/root", cluster.DefaultConfig(), firstOptions, "").RetainResources {
		t.Fatal("forced cleanup before a later main group must disable Operator retention")
	}
	if !k8sGroupOptions(config.TestOptions{Cleanup: false}, groups, 1).Cleanup {
		t.Fatal("earlier main group must clean up before the next main installation")
	}
	if k8sGroupOptions(config.TestOptions{Cleanup: false}, groups, 4).Cleanup {
		t.Fatal("final main group should retain its installation")
	}
	if k8sGroupOptions(config.TestOptions{Cleanup: false}, groups, 3).Cleanup {
		t.Fatal("unrelated group should keep the requested retention policy")
	}

	for _, finalGroup := range []scenario.PlanGroup{
		{Name: "gpu-operator-shared-gpu", Labels: []string{"gpuOperatorSharedGpu"}},
		{Name: "gpu-operator-mig", Labels: []string{"gpuOperatorMig"}},
	} {
		t.Run(finalGroup.Name, func(t *testing.T) {
			selected := []scenario.PlanGroup{groups[0], finalGroup}
			first := k8sGroupOptions(config.TestOptions{Cleanup: false}, selected, 0)
			require.True(t, first.Cleanup, "the baseline Operator must be removed before a different configuration")
			require.False(t, clusterFeatureConfig("/tmp/root", cluster.DefaultConfig(), first, "").RetainResources)
			require.False(t, k8sGroupOptions(config.TestOptions{Cleanup: false}, selected, 1).Cleanup,
				"the final selected Operator installation should be retained")
		})
	}
	selected := []scenario.PlanGroup{groups[0], {Name: "gpu-operator-mig"}}
	require.False(t, k8sGroupOptions(config.TestOptions{Cleanup: false}, selected, 0).Cleanup,
		"a skipped Operator group must not force cleanup of the final installation")

	standaloneGroups := []scenario.PlanGroup{
		{Name: "standalone-baseline", Labels: []string{"remoteDcgm"}},
		{Name: "standalone-ipv6", Labels: nil}, // Skipped groups do not need cleanup.
		{Name: "embedded-baseline", Labels: []string{"startup"}},
		{Name: "standalone-ipv6", Labels: []string{"remoteDcgmIPv6Hostname"}},
	}
	if !k8sGroupOptions(config.TestOptions{Cleanup: false}, standaloneGroups, 0).Cleanup {
		t.Fatal("standalone baseline must be cleaned up before the later selected IPv6 group")
	}
	if k8sGroupOptions(config.TestOptions{Cleanup: false}, standaloneGroups, 3).Cleanup {
		t.Fatal("final selected standalone group should be retained")
	}

	skippedIPv6 := []scenario.PlanGroup{
		standaloneGroups[0],
		standaloneGroups[1],
	}
	if k8sGroupOptions(config.TestOptions{Cleanup: false}, skippedIPv6, 0).Cleanup {
		t.Fatal("a skipped standalone IPv6 group must not force cleanup")
	}

	for _, later := range []scenario.PlanGroup{
		{Name: "embedded-baseline", Labels: []string{"startup"}},
		{Name: "gpu-operator-baseline", Labels: []string{"gpuOperator"}},
	} {
		selected := []scenario.PlanGroup{standaloneGroups[0], later}
		if k8sGroupOptions(config.TestOptions{Cleanup: false}, selected, 0).Cleanup {
			t.Fatalf("unrelated %q group must not force standalone cleanup", later.Name)
		}
	}

	explicitCleanup := k8sGroupOptions(config.TestOptions{Cleanup: true}, standaloneGroups, 0)
	if !explicitCleanup.Cleanup {
		t.Fatal("explicit cleanup must stay enabled")
	}
}
