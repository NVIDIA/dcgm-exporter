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
	"strings"
	"testing"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// TestCheckAPIReadyUsesConfiguredKubeconfig verifies the readiness command uses the selected cluster.
func TestCheckAPIReadyUsesConfiguredKubeconfig(t *testing.T) {
	runner := &recordingRunner{}
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/external-kubeconfig"

	if err := CheckAPIReady(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatalf("CheckAPIReady() error = %v", err)
	}
	if !runner.has("kubectl", "get", "--raw=/readyz") {
		t.Fatalf("CheckAPIReady() did not run kubectl readiness check: %#v", runner.commands)
	}
	if !runner.hasEnv("KUBECONFIG=/tmp/external-kubeconfig") {
		t.Fatalf("CheckAPIReady() did not use the configured kubeconfig: %#v", runner.commands)
	}
}

// TestCheckAPIReadyKeepsKubectlFailure verifies readiness errors retain the kubectl result.
func TestCheckAPIReadyKeepsKubectlFailure(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"kubectl get --raw=/readyz": {ExitCode: 1, Stderr: []byte("server is starting\n")},
	}}

	err := CheckAPIReady(context.Background(), runner, io.Discard, DefaultConfig())
	if err == nil {
		t.Fatal("CheckAPIReady() error = nil")
	}
	for _, want := range []string{"kubernetes API readiness failed", "kubectl get --raw=/readyz", "server is starting"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("CheckAPIReady() error = %v, missing %q", err, want)
		}
	}
}

// TestConfigForRunNameUsesOneNameForTheResourceSet verifies derived resource names stay together.
func TestConfigForRunNameUsesOneNameForTheResourceSet(t *testing.T) {
	cfg, err := ConfigForRunName("ci-17")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RunName != "ci-17" || cfg.ClusterName != "ci-17-gpu" || cfg.Namespace != "ci-17" || cfg.ReleaseName != "ci-17" || cfg.DCGMNamespace != "ci-17-dcgm" {
		t.Fatalf("ConfigForRunName() = %#v", cfg)
	}
}

// TestConfigForRunNameRejectsInvalidNames covers empty, uppercase, invalid punctuation, edge hyphens, and overlong names.
func TestConfigForRunNameRejectsInvalidNames(t *testing.T) {
	for _, runName := range []string{"", "Upper", "-bad", "bad-", "bad_name", strings.Repeat("a", 54)} {
		if _, err := ConfigForRunName(runName); err == nil {
			t.Errorf("ConfigForRunName(%q) succeeded, want invalid-name error", runName)
		}
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
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		outputs: map[string]string{
			"docker info --format {{.DockerRootDir}}":                                                          "/var/lib/docker\n",
			"docker ps -a --filter name=^/k3d-dcgm-exporter-gpu- --format {{.Names}}\t{{.Image}}\t{{.Status}}": "k3d-dcgm-exporter-gpu-agent-0\tnode:test\tUp 1 minute\n",
		},
	}
	var out bytes.Buffer
	cfg := clusterConfigForTest(t)
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
		{"helm", "status", "nvidia-device-plugin"},
		{"kubectl", "-n", "kube-system", "logs", "-l", "app.kubernetes.io/name=nvidia-device-plugin", "--all-containers=true", "--ignore-errors=true"},
		{"kubectl", "-n", "kube-system", "logs", "-l", "app.kubernetes.io/name=nvidia-device-plugin", "--all-containers=true", "--ignore-errors=true", "--previous"},
		{"kubectl", "get", "deviceclasses"},
		{"kubectl", "get", "resourceslices"},
		{"kubectl", "get", "resourceclaims", "-A"},
		{"kubectl", "-n", "nvidia-dra-driver-gpu", "logs"},
		{"nvidia-smi", "-L"},
		{"nvidia-smi", "--query-gpu=index,name,mig.mode.current,mig.mode.pending"},
		{"docker", "info", "--format", "{{.DockerRootDir}}"},
		{"docker", "system", "df"},
		{"nvidia-ctk", "--version"},
		{"nvidia-container-cli", "--version"},
		{"docker", "info", "--format", "{{json .Runtimes}}"},
		{"docker", "inspect", "k3d-dcgm-exporter-gpu-agent-0", "--format", "{{json .State}}"},
		{"docker", "logs", "--tail", "200", "k3d-dcgm-exporter-gpu-agent-0"},
		{"docker", "exec", "k3d-dcgm-exporter-gpu-agent-0", "nvidia-ctk", "--version"},
		{"docker", "exec", "k3d-dcgm-exporter-gpu-agent-0", "nvidia-container-cli", "--version"},
		{"docker", "exec", "k3d-dcgm-exporter-gpu-agent-0", "sh", "-c", "grep -E"},
		{"df", "-h", "/var/lib/docker"},
		{"kubectl", "-n", "dcgm-exporter-dcgm", "describe", "pods,deploy,svc"},
	} {
		if !runner.has(want[0], want[1:]...) {
			t.Fatalf("missing diagnostic command %v: %#v", want, runner.commands)
		}
	}
}

func TestDiagnosticsAbsentLocalClusterPreservesHostEvidence(t *testing.T) {
	runner := &recordingRunner{outputs: map[string]string{
		"docker info --format {{.DockerRootDir}}": "/var/lib/docker\n",
	}}
	var out bytes.Buffer
	if err := Diagnostics(context.Background(), runner, &out, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if runner.has("kubectl") {
		t.Fatalf("diagnostics queried Kubernetes without a local cluster: %#v", runner.commands)
	}
	if !runner.has("nvidia-smi", "-L") || !runner.has("nvidia-smi", "--query-gpu=index,name,mig.mode.current,mig.mode.pending") {
		t.Fatalf("diagnostics skipped host GPU evidence without a local cluster: %#v", runner.commands)
	}
	if !runner.has("df", "-h", "/var/lib/docker") || !runner.has("docker", "system", "df") {
		t.Fatalf("diagnostics skipped local Docker storage evidence without a cluster: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "is absent; skipping Kubernetes diagnostics") {
		t.Fatalf("missing absent-cluster diagnostic:\n%s", out.String())
	}
}

func TestDiagnosticsExternalClusterSkipsRunnerLocalNVIDIASMI(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/external-kubeconfig"
	if err := Diagnostics(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	for _, command := range runner.commands {
		if command.Name == "nvidia-smi" {
			t.Fatalf("external-cluster diagnostics ran runner-local nvidia-smi: %#v", runner.commands)
		}
	}
	if !runner.has("kubectl", "get", "resourceslices") {
		t.Fatalf("external-cluster diagnostics skipped Kubernetes DRA state: %#v", runner.commands)
	}
	if !runner.hasEnv("KUBECONFIG=/tmp/external-kubeconfig") {
		t.Fatalf("external-cluster diagnostics did not use the configured kubeconfig: %#v", runner.commands)
	}
}

// TestDiagnosticsIncludesGPUOperatorWhenNamespaceExists confirms the full diagnostics add the Operator evidence when the namespace exists.
func TestDiagnosticsIncludesGPUOperatorWhenNamespaceExists(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	runner := (&recordingRunner{}).respond("kubectl -n gpu-operator get pods -o json", e2eexec.Result{Stdout: []byte(operatorPodListJSON)})

	if err := Diagnostics(context.Background(), runner, io.Discard, cfg); err != nil {
		t.Fatalf("Diagnostics() error = %v, want none", err)
	}

	for _, want := range [][]string{
		{"get", "clusterpolicy", "-o", "yaml"},
		{"-n", "gpu-operator", "describe", "pods"},
		{"-n", "gpu-operator", "logs", "operator-a", "-c", "plugin"},
		{"-n", "gpu-operator", "logs", "operator-b", "-c", "main", "--previous"},
	} {
		if runner.commandIndex("kubectl", want...) < 0 {
			t.Fatalf("missing Operator diagnostic %v: %#v", want, runner.commands)
		}
	}
}

// operatorPodListJSON lists operator-a with an init container and one container, and operator-b with one container.
const operatorPodListJSON = `{"items":[
{"metadata":{"name":"operator-a"},"spec":{"initContainers":[{"name":"init"}],"containers":[{"name":"plugin"}]}},
{"metadata":{"name":"operator-b"},"spec":{"containers":[{"name":"main"}]}}]}`

// TestGPUOperatorDiagnosticsSkippedWithoutNamespace confirms the Operator extras are skipped without error when the namespace is absent.
func TestGPUOperatorDiagnosticsSkippedWithoutNamespace(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	// A zero Result is what kubectl get namespace --ignore-not-found -o json prints for a missing namespace:
	// exit 0 with empty output, which getNamespaceInfo treats as not found.
	runner := (&recordingRunner{}).queue(gpuOperatorNamespaceLookup, e2eexec.Result{})

	if err := errors.Join(gpuOperatorDiagnostics(context.Background(), runner, io.Discard, cfg)...); err != nil {
		t.Fatalf("gpuOperatorDiagnostics() error = %v, want none", err)
	}
	if runner.commandIndex("kubectl", "-n", "gpu-operator", "describe", "pods") >= 0 || runner.commandIndex("kubectl", "get", "clusterpolicy") >= 0 {
		t.Fatalf("Operator diagnostics ran without a namespace: %#v", runner.commands)
	}
}

// TestGPUOperatorDiagnosticsCollectsEvidence confirms ClusterPolicy, pod descriptions, and current and previous logs are collected.
func TestGPUOperatorDiagnosticsCollectsEvidence(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	runner := (&recordingRunner{}).respond("kubectl -n gpu-operator get pods -o json", e2eexec.Result{Stdout: []byte(operatorPodListJSON)})

	if err := errors.Join(gpuOperatorDiagnostics(context.Background(), runner, io.Discard, cfg)...); err != nil {
		t.Fatalf("gpuOperatorDiagnostics() error = %v, want none", err)
	}
	for _, want := range [][]string{
		{"get", "clusterpolicy", "-o", "yaml"},
		{"-n", "gpu-operator", "get", "pods", "-o", "wide"},
		{"-n", "gpu-operator", "get", "events", "--sort-by=.lastTimestamp"},
		{"-n", "gpu-operator", "describe", "pods"},
		{"-n", "gpu-operator", "logs", "operator-a", "-c", "init", "--tail=200"},
		{"-n", "gpu-operator", "logs", "operator-a", "-c", "plugin", "--previous", "--tail=200"},
		{"-n", "gpu-operator", "logs", "operator-b", "-c", "main", "--tail=200"},
		{"-n", "gpu-operator", "logs", "operator-b", "-c", "main", "--previous", "--tail=200"},
	} {
		if runner.commandIndex("kubectl", want...) < 0 {
			t.Fatalf("missing Operator diagnostic %v: %#v", want, runner.commands)
		}
	}
}

// TestGPUOperatorDiagnosticsReturnsNamespaceLookupError reports a failed namespace lookup instead of skipping silently.
func TestGPUOperatorDiagnosticsReturnsNamespaceLookupError(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	runner := (&recordingRunner{}).queue(gpuOperatorNamespaceLookup, e2eexec.Result{ExitCode: 1, Stderr: []byte("connection refused")})

	err := errors.Join(gpuOperatorDiagnostics(context.Background(), runner, io.Discard, cfg)...)

	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("gpuOperatorDiagnostics() error = %v, want namespace lookup error", err)
	}
}

// TestRunClusterPolicyDiagnosticResults checks which ClusterPolicy lookup failures count as errors.
func TestRunClusterPolicyDiagnosticResults(t *testing.T) {
	for _, test := range []struct {
		name       string
		result     e2eexec.Result
		wantErr    bool
		wantOutput string
	}{
		{name: "found", result: e2eexec.Result{Stdout: []byte("kind: ClusterPolicy\n")}},
		{name: "missing CRD", result: e2eexec.Result{ExitCode: 1, Stderr: []byte(`error: the server doesn't have a resource type "clusterpolicy"`)}, wantOutput: `kubectl_gpu_operator_clusterpolicy unavailable: error: the server doesn't have a resource type "clusterpolicy"`},
		{name: "forbidden", result: e2eexec.Result{ExitCode: 1, Stderr: []byte(`clusterpolicies is forbidden`)}, wantErr: true, wantOutput: `kubectl_gpu_operator_clusterpolicy failed: clusterpolicies is forbidden`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := clusterConfigForTest(t)
			cfg.Kubeconfig = "/tmp/kubeconfig"
			runner := (&recordingRunner{}).respond("get clusterpolicy", test.result)

			var output strings.Builder

			err := runClusterPolicyDiagnostic(context.Background(), runner, &output, cfg)

			if (err != nil) != test.wantErr {
				t.Fatalf("runClusterPolicyDiagnostic() error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantOutput != "" && !strings.Contains(output.String(), test.wantOutput) {
				t.Fatalf("output = %q, want it to contain %q", output.String(), test.wantOutput)
			}
		})
	}
}

// TestGPUOperatorLogDiagnosticsMixedContainers reads every container, treating waiting and never-restarted containers as normal.
func TestGPUOperatorLogDiagnosticsMixedContainers(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &recordingRunner{matching: map[string]e2eexec.Result{
		"kubectl -n gpu-operator get pods -o json": {Stdout: []byte(operatorPodListJSON)},
		"operator-a -c init":                       {ExitCode: 1, Stderr: []byte(`container "init" in pod "operator-a" is waiting to start: PodInitializing`)},
		"operator-b -c main --previous":            {ExitCode: 1, Stderr: []byte(`previous terminated container "main" in pod "operator-b" not found`)},
		"operator-a -c plugin --previous":          {Stdout: []byte("crash log\n")},
	}}

	if err := errors.Join(gpuOperatorLogDiagnostics(context.Background(), runner, io.Discard, cfg)...); err != nil {
		t.Fatalf("gpuOperatorLogDiagnostics() error = %v, want none", err)
	}
	for _, want := range [][]string{
		{"operator-a", "-c", "init"},
		{"operator-a", "-c", "init", "--previous"},
		{"operator-a", "-c", "plugin"},
		{"operator-a", "-c", "plugin", "--previous"},
		{"operator-b", "-c", "main"},
		{"operator-b", "-c", "main", "--previous"},
	} {
		if runner.commandIndex("kubectl", append([]string{"logs"}, want...)...) < 0 {
			t.Fatalf("missing log command %v: %#v", want, runner.commands)
		}
	}
}

// TestGPUOperatorLogDiagnosticsReportsRealFailure keeps genuine log failures as errors.
func TestGPUOperatorLogDiagnosticsReportsRealFailure(t *testing.T) {
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	runner := &recordingRunner{matching: map[string]e2eexec.Result{
		"kubectl -n gpu-operator get pods -o json": {Stdout: []byte(operatorPodListJSON)}, "operator-b -c main": {ExitCode: 1, Stderr: []byte("connection refused")},
	}}

	err := errors.Join(gpuOperatorLogDiagnostics(context.Background(), runner, io.Discard, cfg)...)

	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("gpuOperatorLogDiagnostics() error = %v, want connection refused", err)
	}
}

// TestEnsureManagedNamespaceReportsCreationAndKeepsRunOwnership covers new creation, same-run reuse, and foreign-run rejection.
func TestEnsureManagedNamespaceReportsCreationAndKeepsRunOwnership(t *testing.T) {
	for _, test := range []struct {
		name        string
		createError bool
		labels      map[string]string
		wantCreated bool
		wantError   string
	}{
		{name: "created", wantCreated: true},
		{name: "reuse same run", createError: true, labels: ownedNamespaceLabels("dcgm-exporter")},
		{name: "reject other run", createError: true, labels: ownedNamespaceLabels("another-run"), wantError: `namespace "gpu-operator" already exists`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := clusterConfigForTest(t)
			cfg.Kubeconfig = "/tmp/kubeconfig"
			runner := (&recordingRunner{}).queue(gpuOperatorNamespaceLookup, namespaceResult(test.labels))
			if test.createError {
				runner.respond(namespaceCreate, namespaceExists)
			}
			created, err := ensureManagedNamespace(context.Background(), runner, io.Discard, cfg, "gpu-operator")
			if created != test.wantCreated {
				t.Fatalf("ensureManagedNamespace() created = %t, want %t", created, test.wantCreated)
			}
			requireErrorContains(t, "ensureManagedNamespace()", err, test.wantError)
		})
	}
}

// TestEnsureManagedNamespaceCreatesOwnedEnvironment adds the run name when it creates a namespace.
func TestEnsureManagedNamespaceCreatesOwnedEnvironment(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	if err := EnsureManagedNamespace(context.Background(), runner, &out, cfg, "dcgm-exporter"); err != nil {
		t.Fatalf("EnsureManagedNamespace() error = %v", err)
	}
	if !runner.has("kubectl", "create", "-f", "-") ||
		!runner.stdinContains(`"kind":"Namespace"`) ||
		!runner.stdinContains(`"name":"dcgm-exporter"`) ||
		!runner.stdinContains(`"app.kubernetes.io/managed-by":"dcgm-exporter-e2e"`) ||
		!runner.stdinContains(`"e2eRunName":"dcgm-exporter"`) ||
		!runner.stdinContains(`"app.kubernetes.io/part-of":"dcgm-exporter"`) {
		t.Fatalf("namespace was not created with run ownership labels: %#v", runner.commands)
	}
	if runner.has("kubectl", "get", "namespace", "dcgm-exporter") {
		t.Fatalf("new namespace unexpectedly needed an ownership lookup: %#v", runner.commands)
	}
}

// TestEnsureManagedNamespaceReusesOnlyTheSameRun reuses an active namespace without changing its ownership labels.
func TestEnsureManagedNamespaceReusesOnlyTheSameRun(t *testing.T) {
	create := "kubectl create -f -"
	get := "kubectl get namespace ci-17 --ignore-not-found=true -o json"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		create: {ExitCode: 1, Stderr: []byte("already exists")},
		get:    {Stdout: []byte(`{"metadata":{"uid":"test-namespace-uid","labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"ci-17"}},"status":{"phase":"Active"}}`)},
	}}
	cfg, err := ConfigForRunName("ci-17")
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureManagedNamespace(context.Background(), runner, io.Discard, cfg, cfg.Namespace); err != nil {
		t.Fatalf("EnsureManagedNamespace() error = %v", err)
	}
	if runner.hasVerb("kubectl", "apply") || runner.hasVerb("kubectl", "label") {
		t.Fatalf("namespace reuse changed its ownership labels: %#v", runner.commands)
	}
}

// TestEnsureManagedNamespaceRejectsAnotherRun leaves a namespace owned by a different run untouched.
func TestEnsureManagedNamespaceRejectsAnotherRun(t *testing.T) {
	create := "kubectl create -f -"
	get := "kubectl get namespace ci-17 --ignore-not-found=true -o json"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		create: {ExitCode: 1, Stderr: []byte("already exists")},
		get:    {Stdout: []byte(`{"metadata":{"uid":"foreign-namespace-uid","labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"ci-18"}},"status":{"phase":"Active"}}`)},
	}}
	cfg, err := ConfigForRunName("ci-17")
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureManagedNamespace(context.Background(), runner, io.Discard, cfg, cfg.Namespace)
	if err == nil || !strings.Contains(err.Error(), "not owned by run \"ci-17\"") {
		t.Fatalf("EnsureManagedNamespace() error = %v, want an ownership conflict", err)
	}
	if runner.hasVerb("kubectl", "apply") || runner.hasVerb("kubectl", "label") || runner.hasVerb("kubectl", "delete") {
		t.Fatalf("foreign namespace was modified: %#v", runner.commands)
	}
}

// TestEnsureManagedNamespaceWaitsForOwnedNamespaceDeletion retries creation only after the owned terminating namespace is deleted.
func TestEnsureManagedNamespaceWaitsForOwnedNamespaceDeletion(t *testing.T) {
	create := "kubectl create -f -"
	get := "kubectl get namespace ci-17 --ignore-not-found=true -o json"
	runner := &recordingRunner{
		results: map[string]e2eexec.Result{
			get: {Stdout: []byte(`{"metadata":{"uid":"test-namespace-uid","labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"ci-17"}},"status":{"phase":"Terminating"}}`)},
		},
		resultSequences: map[string][]e2eexec.Result{
			create: {
				{ExitCode: 1, Stderr: []byte("namespace is terminating")},
				{},
			},
		},
	}
	cfg, err := ConfigForRunName("ci-17")
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureManagedNamespace(context.Background(), runner, io.Discard, cfg, cfg.Namespace); err != nil {
		t.Fatalf("EnsureManagedNamespace() error = %v", err)
	}
	firstCreate := runner.commandIndex("kubectl", "create", "-f", "-")
	wait := runner.commandIndex("kubectl", "wait", "--for=delete", "namespace/ci-17", "--timeout=90s")
	secondCreate := -1
	createCount := 0
	for index, command := range runner.commands {
		if command.Name == "kubectl" && includesArgs(command.Args, "create", "-f", "-") {
			createCount++
			if createCount == 2 {
				secondCreate = index
			}
		}
	}
	if firstCreate < 0 || wait < firstCreate || secondCreate < 0 || secondCreate < wait || createCount != 2 {
		t.Fatalf("namespace was not recreated after waiting for deletion: %#v", runner.commands)
	}
}

// TestEnsureManagedNamespaceRejectsTerminatingNamespaceWhenWaitFails does not reuse a namespace whose deletion wait failed.
func TestEnsureManagedNamespaceRejectsTerminatingNamespaceWhenWaitFails(t *testing.T) {
	create := "kubectl create -f -"
	wait := "kubectl wait --for=delete namespace/ci-17 --timeout=90s"
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		create: {ExitCode: 1, Stderr: []byte("namespace is terminating")},
		"kubectl get namespace ci-17 --ignore-not-found=true -o json": {
			Stdout: []byte(`{"metadata":{"uid":"test-namespace-uid","labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"ci-17"}},"status":{"phase":"Terminating"}}`),
		},
		wait: {ExitCode: 1, Stderr: []byte("timed out waiting for namespace deletion")},
	}}
	cfg, err := ConfigForRunName("ci-17")
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureManagedNamespace(context.Background(), runner, io.Discard, cfg, cfg.Namespace)
	if err == nil || !strings.Contains(err.Error(), "finish deleting") {
		t.Fatalf("EnsureManagedNamespace() error = %v, want a namespace deletion wait error", err)
	}
	if runner.commandIndex("kubectl", "create", "-f", "-") < 0 || runner.commandIndex("kubectl", "wait", "--for=delete") < 0 {
		t.Fatalf("namespace was reused without a successful deletion wait: %#v", runner.commands)
	}
}

// TestEnsureManagedNamespaceDoesNotReuseUnknownPhase avoids treating an unrecognized phase as active or terminating.
func TestEnsureManagedNamespaceDoesNotReuseUnknownPhase(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"kubectl create -f -": {ExitCode: 1, Stderr: []byte("already exists")},
		"kubectl get namespace ci-17 --ignore-not-found=true -o json": {
			Stdout: []byte(`{"metadata":{"labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"ci-17"}},"status":{"phase":"Unknown"}}`),
		},
	}}
	cfg, err := ConfigForRunName("ci-17")
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureManagedNamespace(context.Background(), runner, io.Discard, cfg, cfg.Namespace)
	if err == nil || !strings.Contains(err.Error(), `has phase "Unknown"`) {
		t.Fatalf("EnsureManagedNamespace() error = %v, want an unknown-phase error", err)
	}
	if runner.hasVerb("kubectl", "wait") {
		t.Fatalf("namespace with an unknown phase was treated as terminating: %#v", runner.commands)
	}
}

func TestCleanupCommands(t *testing.T) {
	runner := &recordingRunner{
		clusterList: "dcgm-exporter-gpu 1/1 1/1\n",
		results: map[string]e2eexec.Result{
			"docker network inspect k3d-dcgm-exporter-gpu":       {},
			"docker volume inspect k3d-dcgm-exporter-gpu-images": {},
		},
	}
	var out bytes.Buffer
	if err := Cleanup(context.Background(), runner, &out, clusterConfigForTest(t)); err != nil {
		t.Fatal(err)
	}
	if !runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("missing k3d cleanup: %#v", runner.commands)
	}
	if !runner.has("docker", "network", "rm", "k3d-dcgm-exporter-gpu") ||
		!runner.has("docker", "volume", "rm", "k3d-dcgm-exporter-gpu-images") {
		t.Fatalf("missing stale local resource cleanup: %#v", runner.commands)
	}

	runner = &recordingRunner{}
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	if err := Cleanup(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}
	if !runner.has("kubectl", "delete", "namespace", "dcgm-exporter") {
		t.Fatalf("missing external namespace cleanup: %#v", runner.commands)
	}
}

func TestCleanupSkipsAbsentLocalCluster(t *testing.T) {
	runner := &recordingRunner{
		clusterList: "other-cluster 1/1 1/1\n",
		results: map[string]e2eexec.Result{
			"docker network inspect k3d-dcgm-exporter-gpu":       {},
			"docker volume inspect k3d-dcgm-exporter-gpu-images": {},
		},
	}
	var out bytes.Buffer
	if err := Cleanup(context.Background(), runner, &out, clusterConfigForTest(t)); err != nil {
		t.Fatal(err)
	}
	if runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("deleted absent k3d cluster: %#v", runner.commands)
	}
	if !strings.Contains(out.String(), "already absent") {
		t.Fatalf("missing absent-cluster message:\n%s", out.String())
	}
	if !runner.has("docker", "network", "rm", "k3d-dcgm-exporter-gpu") {
		t.Fatalf("stale k3d network was not removed: %#v", runner.commands)
	}
	if !runner.has("docker", "volume", "rm", "k3d-dcgm-exporter-gpu-images") {
		t.Fatalf("stale k3d image volume was not removed: %#v", runner.commands)
	}
}

func TestCleanupReturnsStaleLocalResourceFailure(t *testing.T) {
	runner := &recordingRunner{
		clusterList: "other-cluster 1/1 1/1\n",
		results: map[string]e2eexec.Result{
			"docker network inspect k3d-dcgm-exporter-gpu":       {},
			"docker volume inspect k3d-dcgm-exporter-gpu-images": {},
			"docker network rm k3d-dcgm-exporter-gpu": {
				ExitCode: 1,
				Stderr:   []byte("network is in use\n"),
			},
		},
	}

	err := Cleanup(context.Background(), runner, io.Discard, clusterConfigForTest(t))
	if err == nil || !strings.Contains(err.Error(), "docker network rm k3d-dcgm-exporter-gpu") {
		t.Fatalf("Cleanup() error = %v, want stale network removal failure", err)
	}
	if !runner.has("docker", "volume", "rm", "k3d-dcgm-exporter-gpu-images") {
		t.Fatalf("stale k3d image volume was not removed after network failure: %#v", runner.commands)
	}
}

func TestCleanupReportsNetworkInspectFailureAndContinues(t *testing.T) {
	runner := &recordingRunner{
		clusterList: "other-cluster 1/1 1/1\n",
		results: map[string]e2eexec.Result{
			"docker network inspect k3d-dcgm-exporter-gpu": {
				ExitCode: 1,
				Stderr:   []byte("permission denied\n"),
			},
			"docker volume inspect k3d-dcgm-exporter-gpu-images": {},
		},
	}

	err := Cleanup(context.Background(), runner, io.Discard, clusterConfigForTest(t))
	if err == nil || !strings.Contains(err.Error(), "docker network inspect k3d-dcgm-exporter-gpu") ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Cleanup() error = %v, want network inspect failure", err)
	}
	if runner.has("docker", "network", "rm", "k3d-dcgm-exporter-gpu") {
		t.Fatalf("removed network after inspect failure: %#v", runner.commands)
	}
	if !runner.has("docker", "volume", "rm", "k3d-dcgm-exporter-gpu-images") {
		t.Fatalf("stale volume was not removed after network inspect failure: %#v", runner.commands)
	}
}

func TestCleanupReturnsLocalClusterListFailure(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"k3d cluster list": {ExitCode: 1, Stderr: []byte("k3d failed\n")},
	}}
	var out bytes.Buffer
	err := Cleanup(context.Background(), runner, &out, clusterConfigForTest(t))
	if err == nil || !strings.Contains(err.Error(), "k3d cluster list") {
		t.Fatalf("Cleanup() error = %v, want k3d list failure", err)
	}
	if runner.has("k3d", "cluster", "delete", "dcgm-exporter-gpu") {
		t.Fatalf("deleted k3d cluster after list failure: %#v", runner.commands)
	}
}

// TestCleanupDeletesConfiguredExternalNamespaces verifies cleanup removes
// namespaces owned by the configured run name.
func TestCleanupDeletesConfiguredExternalNamespaces(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	if err := Cleanup(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if !runner.deletedNamespace("dcgm-exporter") {
		t.Fatalf("configured namespace was not deleted: %#v", runner.commands)
	}
	if !runner.deletedNamespace("dcgm-exporter-dcgm") {
		t.Fatalf("managed DCGM namespace was not deleted: %#v", runner.commands)
	}
	for _, namespace := range []string{"dcgm-exporter", "dcgm-exporter-dcgm"} {
		if !runner.has("kubectl", "delete", "namespace", namespace, "--ignore-not-found=true") {
			t.Fatalf("cleanup did not delete owned namespace %s: %#v", namespace, runner.commands)
		}
	}
	for _, command := range runner.commands {
		if command.Name == "kubectl" && strings.Contains(strings.Join(command.Args, " "), "--preconditions") {
			t.Fatalf("cleanup used an unsupported kubectl precondition flag: %#v", command)
		}
	}
}

// TestCleanupSkipsAbsentExternalNamespaces verifies repeated cleanup handles missing namespaces.
func TestCleanupSkipsAbsentExternalNamespaces(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"kubectl get namespace dcgm-exporter --ignore-not-found=true -o json":      {},
		"kubectl get namespace dcgm-exporter-dcgm --ignore-not-found=true -o json": {},
	}}
	var out bytes.Buffer
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	if err := Cleanup(context.Background(), runner, &out, cfg); err != nil {
		t.Fatal(err)
	}

	if runner.deletedNamespace("dcgm-exporter") || runner.deletedNamespace("dcgm-exporter-dcgm") {
		t.Fatalf("cleanup tried to delete absent namespaces: %#v", runner.commands)
	}
	for _, command := range runner.commands {
		if command.Name != "kubectl" || len(command.Args) < 2 || command.Args[0] != "get" || command.Args[1] != "namespace" {
			continue
		}
		if !hasArg(command.Args, "--ignore-not-found=true") {
			t.Fatalf("cleanup lookup did not tolerate an absent environment: %#v", command)
		}
	}
}

// TestCleanupRefusesAnotherRunsNamespace prevents cleanup from deleting a namespace owned by another run.
func TestCleanupRefusesAnotherRunsNamespace(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"kubectl get namespace dcgm-exporter --ignore-not-found=true -o json": {
			Stdout: []byte(`{"metadata":{"uid":"foreign-namespace-uid","labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"other-run"}},"status":{"phase":"Active"}}`),
		},
		"kubectl get namespace dcgm-exporter-dcgm --ignore-not-found=true -o json": {},
	}}
	cfg := clusterConfigForTest(t)
	cfg.Kubeconfig = "/tmp/kubeconfig"
	err := Cleanup(context.Background(), runner, io.Discard, cfg)
	if err == nil || !strings.Contains(err.Error(), "refusing to delete namespace") {
		t.Fatalf("Cleanup() error = %v, want an ownership error", err)
	}
	if runner.deletedNamespace("dcgm-exporter") {
		t.Fatalf("cleanup deleted a namespace owned by another run: %#v", runner.commands)
	}
}

func TestDeployExporterDoesNotInvokeHelmWhenImagePullSecretPreflightFails(t *testing.T) {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		`kubectl -n dcgm-exporter get secret pull-secret -o jsonpath={.type}{"\t"}{.data.\.dockerconfigjson}`: {
			Stdout: []byte("Opaque\te30="),
		},
	}}
	err := DeployExporter(context.Background(), runner, io.Discard, DefaultConfig(), ExporterConfig{
		Chart:           "./deployment",
		Image:           "registry.example/dcgm-exporter:dev",
		ImagePullSecret: "pull-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "kubernetes.io/dockerconfigjson") {
		t.Fatalf("DeployExporter() error = %v, want invalid pull Secret error", err)
	}
	if runner.has("helm", "upgrade") {
		t.Fatalf("DeployExporter() invoked Helm after pull Secret preflight failed: %#v", runner.commands)
	}
	if runner.has("kubectl", "get", "namespace") || runner.has("kubectl", "create", "namespace") {
		t.Fatalf("DeployExporter() changed namespace state after pull Secret preflight failed: %#v", runner.commands)
	}
}
