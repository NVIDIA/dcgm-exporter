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
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	"github.com/NVIDIA/dcgm-exporter/internal/e2e/scenario"
)

// TestRegistryAuthKeepsGeneratedDockerConfigUntilRunCleanup keeps generated credentials until run cleanup.
func TestRegistryAuthKeepsGeneratedDockerConfigUntilRunCleanup(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("registry-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := configWithRegistryLogin("registry.example,user," + passwordFile)
	clusterCfg := clusterConfig(t.TempDir(), opts)
	clusterCfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &fakeRunner{}

	dockerConfigPath, cleanup, err := prepareRegistryAuthSessionForNamespaces(context.Background(), io.Discard, runner, clusterCfg, &opts, []string{clusterCfg.Namespace})
	if err != nil {
		t.Fatalf("prepareRegistryAuthSessionForNamespaces() error = %v", err)
	}
	dockerConfigDir := opts.DockerConfigDir
	if dockerConfigPath != filepath.Join(dockerConfigDir, "config.json") {
		t.Fatalf("Docker config path = %q, want config under %q", dockerConfigPath, dockerConfigDir)
	}
	if _, err := os.Stat(dockerConfigDir); err != nil {
		t.Fatalf("temporary Docker config is unavailable before run cleanup: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dockerConfigDir); !os.IsNotExist(err) {
		t.Fatalf("temporary Docker config remains after run cleanup: %v", err)
	}
	if opts.DockerConfigDir != "" {
		t.Fatalf("DockerConfigDir = %q after cleanup, want empty", opts.DockerConfigDir)
	}
}

// TestExecutePlanDoesNotPrepareSkippedGPUOperatorGroup checks that skipped GPU Operator groups run no setup commands.
func TestExecutePlanDoesNotPrepareSkippedGPUOperatorGroup(t *testing.T) {
	runner := &fakeRunner{}
	run := &k8sSuiteRun{
		ctx:    context.Background(),
		stdout: io.Discard,
		runner: runner,
		cfg:    config.Config{TestOptions: config.TestOptions{GPUOperator: "install"}},
	}
	plan := scenario.Plan{Groups: []scenario.PlanGroup{{Name: "gpu-operator-main-embedded"}}}
	if err := run.executePlan(plan); err != nil {
		t.Fatalf("executePlan() error = %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("skipped GPU Operator group ran setup commands: %#v", runner.commands)
	}
}

// TestRunK8sGroupJoinsGPUOperatorCleanupFailureWithScenarioFailure returns both scenario and cleanup errors.
func TestRunK8sGroupJoinsGPUOperatorCleanupFailureWithScenarioFailure(t *testing.T) {
	root := t.TempDir()
	chart := filepath.Join(root, "gpu-operator-chart")
	if err := os.MkdirAll(chart, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := config.TestOptions{
		GPUOperator:      "install",
		GPUOperatorChart: chart,
		ExporterImage:    "registry.example/nvidia/dcgm-exporter:dev",
		WaitTimeout:      "1ms",
		Cleanup:          true,
	}
	clusterCfg := clusterConfig(root, opts)
	clusterCfg.Kubeconfig = filepath.Join(root, "kubeconfig")
	matcher := &gpuOperatorCleanupFailureMatcher{
		testFailure:    "GPU Operator metric assertion failed",
		cleanupFailure: "GPU Operator namespace delete denied",
	}
	runner := &fakeRunner{namespaceOwner: clusterCfg.RunName, resultMatcher: matcher.match}
	run := &k8sSuiteRun{
		ctx:        context.Background(),
		stdout:     io.Discard,
		root:       root,
		runner:     runner,
		cfg:        config.Config{TestOptions: opts},
		binary:     "/tmp/dcgm-exporter-k8s.test",
		clusterCfg: clusterCfg,
	}
	err := run.runK8sGroup(&opts, scenario.PlanGroup{Name: "gpu-operator-main-embedded", Labels: []string{"gpuOperatorMainEmbedded"}})
	if err == nil || !strings.Contains(err.Error(), matcher.testFailure) || !strings.Contains(err.Error(), matcher.cleanupFailure) {
		t.Fatalf("runK8sGroup() error = %v, want scenario and Operator cleanup failures", err)
	}
}

// TestRunK8sGroupDiagnosesFailedGPUOperatorSetupBeforeRollback checks that a failed GPU Operator install gets
// one round of diagnostics before its rollback, and that kept resources are not rolled back.
func TestRunK8sGroupDiagnosesFailedGPUOperatorSetupBeforeRollback(t *testing.T) {
	for _, test := range []struct {
		name         string
		cleanup      bool
		wantRollback bool
	}{
		{name: "cleanup", cleanup: true, wantRollback: true},
		{name: "keep resources"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			chart := filepath.Join(root, "gpu-operator-chart")
			if err := os.MkdirAll(chart, 0o755); err != nil {
				t.Fatal(err)
			}
			opts := config.TestOptions{
				GPUOperator:      "install",
				GPUOperatorChart: chart,
				ExporterImage:    "registry.example/nvidia/dcgm-exporter:dev",
				Cleanup:          test.cleanup,
			}
			clusterCfg := clusterConfig(root, opts)
			clusterCfg.Kubeconfig = filepath.Join(root, "kubeconfig")
			matcher := &gpuOperatorCleanupFailureMatcher{}
			runner := &fakeRunner{namespaceOwner: clusterCfg.RunName, resultMatcher: func(command e2eexec.Command) (e2eexec.Result, bool) {
				if command.Name == "helm" && containsArgs(command.Args, "upgrade", "--install", "gpu-operator") {
					return e2eexec.Result{ExitCode: 1, Stderr: []byte("GPU Operator install failed")}, true
				}
				return matcher.match(command)
			}}
			run := &k8sSuiteRun{ctx: context.Background(), stdout: io.Discard, root: root, runner: runner, cfg: config.Config{TestOptions: opts}, clusterCfg: clusterCfg}

			err := run.runK8sGroup(&opts, scenario.PlanGroup{Name: "gpu-operator-main-embedded", Labels: []string{"gpuOperatorMainEmbedded"}})
			if err == nil || !strings.Contains(err.Error(), "GPU Operator install failed") {
				t.Fatalf("runK8sGroup() error = %v, want the install failure", err)
			}

			checkGPUOperatorDiagnosticsBeforeRollback(t, runner, test.wantRollback)
		})
	}
}

// checkGPUOperatorDiagnosticsBeforeRollback fails the test unless GPU Operator diagnostics ran exactly once and
// the Helm rollback ran at most once: after the diagnostics when wantRollback is set, and not at all otherwise.
func checkGPUOperatorDiagnosticsBeforeRollback(t *testing.T, runner *fakeRunner, wantRollback bool) {
	t.Helper()

	diagnosticsArgs := []string{"-n", "gpu-operator", "describe", "pods"}
	uninstallArgs := []string{"uninstall", "gpu-operator"}
	if runner.commandCount("kubectl", diagnosticsArgs...) > 1 {
		t.Fatalf("GPU Operator diagnostics ran more than once: %#v", runner.commands)
	}
	if runner.commandCount("helm", uninstallArgs...) > 1 {
		t.Fatalf("GPU Operator rollback ran more than once: %#v", runner.commands)
	}

	diagnostics := runner.commandIndex("kubectl", diagnosticsArgs...)
	uninstall := runner.commandIndex("helm", uninstallArgs...)
	if diagnostics < 0 {
		t.Fatalf("GPU Operator diagnostics did not run: %#v", runner.commands)
	}
	if (uninstall >= 0) != wantRollback || (wantRollback && uninstall < diagnostics) {
		t.Fatalf("diagnostics %d, uninstall %d, want rollback %t after diagnostics", diagnostics, uninstall, wantRollback)
	}
}

// gpuOperatorCleanupFailureMatcher matches the expected setup, cleanup, and suite commands while injecting failures.
type gpuOperatorCleanupFailureMatcher struct {
	installed      bool
	testFailure    string
	cleanupFailure string
}

// match tries each command matcher in the order used by the simulated install.
func (m *gpuOperatorCleanupFailureMatcher) match(command e2eexec.Command) (e2eexec.Result, bool) {
	for _, respond := range []func(e2eexec.Command) (e2eexec.Result, bool){
		m.matchMainPreflight,
		m.matchMainResources,
		m.matchInstallAndWait,
		m.matchCleanupAndScenario,
	} {
		if result, matched := respond(command); matched {
			return result, true
		}
	}
	return e2eexec.Result{}, false
}

// matchMainPreflight simulates the initial lookup of the GPU Operator namespace.
func (m *gpuOperatorCleanupFailureMatcher) matchMainPreflight(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name == "kubectl" && containsArgs(command.Args, "get", "namespace", "gpu-operator", "-o", "name") {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte(`namespaces "gpu-operator" not found`)}, true
	}
	return e2eexec.Result{}, false
}

// matchMainResources simulates checks for GPU Operator custom resources.
func (m *gpuOperatorCleanupFailureMatcher) matchMainResources(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name != "kubectl" {
		return e2eexec.Result{}, false
	}
	if containsArgs(command.Args, "get", "clusterpolicy", "-o", "name") {
		if m.installed {
			return e2eexec.Result{Stdout: []byte("clusterpolicy/gpu-cluster-policy\n")}, true
		}
		return e2eexec.Result{ExitCode: 1, Stderr: []byte(`the server doesn't have a resource type "clusterpolicy"`)}, true
	}
	if containsArgs(command.Args, "get", "gpucluster", "-o", "name") {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte(`the server doesn't have a resource type "gpucluster"`)}, true
	}
	return e2eexec.Result{}, false
}

// matchInstallAndWait simulates GPU Operator installation and exporter readiness.
func (m *gpuOperatorCleanupFailureMatcher) matchInstallAndWait(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name == "helm" && containsArgs(command.Args, "upgrade", "--install", "gpu-operator") {
		m.installed = true
		return e2eexec.Result{}, true
	}
	if command.Name == "kubectl" && containsArgs(command.Args, "-n", "gpu-operator", "get", "pods", "-o", "json") {
		return e2eexec.Result{Stdout: []byte(`{"items":[{"metadata":{"name":"dcgm-exporter"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`)}, true
	}
	return e2eexec.Result{}, false
}

// matchCleanupAndScenario injects failures for GPU Operator cleanup and the scenario binary.
func (m *gpuOperatorCleanupFailureMatcher) matchCleanupAndScenario(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name == "kubectl" && containsArgs(command.Args, "delete", "namespace", "gpu-operator") {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte(m.cleanupFailure)}, true
	}
	if command.Name == "/tmp/dcgm-exporter-k8s.test" {
		return e2eexec.Result{ExitCode: 1, Stderr: []byte(m.testFailure)}, true
	}
	return e2eexec.Result{}, false
}
