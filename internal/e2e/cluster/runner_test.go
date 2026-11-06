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
	"os"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
)

// recordingRunner is the command runner fake for cluster tests. It records every command and answers it
// from the scripted responses first, then from defaults that describe an empty, healthy cluster.
type recordingRunner struct {
	commands    []e2eexec.Command
	clusterList string
	// outputs, results and resultSequences answer the command whose full text equals the key. A sequence
	// answers with its results in order, then lets the other responses answer.
	outputs         map[string]string
	results         map[string]e2eexec.Result
	resultSequences map[string][]e2eexec.Result
	// matching answers any command whose full text contains the key; the longest matching key wins.
	matching map[string]e2eexec.Result
}

// respond scripts result for every command whose full text contains text, and returns the runner.
func (r *recordingRunner) respond(text string, result e2eexec.Result) *recordingRunner {
	if r.matching == nil {
		r.matching = map[string]e2eexec.Result{}
	}
	r.matching[text] = result
	return r
}

// queue scripts results, in order, for the command whose full text is key, and returns the runner.
func (r *recordingRunner) queue(key string, results ...e2eexec.Result) *recordingRunner {
	if r.resultSequences == nil {
		r.resultSequences = map[string][]e2eexec.Result{}
	}
	r.resultSequences[key] = append(r.resultSequences[key], results...)
	return r
}

// Run records a command, applies configured responses, and falls back to synthetic system responses.
func (r *recordingRunner) Run(_ context.Context, command e2eexec.Command) e2eexec.Result {
	r.commands = append(r.commands, command)
	key := strings.TrimSpace(command.Name + " " + strings.Join(command.Args, " "))
	if result, ok := r.configuredResult(command, key); ok {
		return result
	}
	if command.Name == "nvidia-ctk" {
		if err := writeRecordedCDISpec(command.Args); err != nil {
			return e2eexec.Result{ExitCode: 1, Err: err}
		}
	}
	if result, ok := r.defaultResult(command, key); ok {
		return result
	}
	if result, ok := r.dockerResult(command); ok {
		return result
	}
	return streamResult(command, e2eexec.Result{Stdout: []byte("ok\n")})
}

// configuredResult returns the scripted result for key: a sequence entry, a fixed result, or the result of
// the longest matching text.
func (r *recordingRunner) configuredResult(command e2eexec.Command, key string) (e2eexec.Result, bool) {
	if sequence := r.resultSequences[key]; len(sequence) != 0 {
		r.resultSequences[key] = sequence[1:]
		return streamResult(command, sequence[0]), true
	}
	if result, ok := r.results[key]; ok {
		return streamResult(command, result), true
	}

	match := ""
	for text := range r.matching {
		if strings.Contains(key, text) && len(text) > len(match) {
			match = text
		}
	}
	if match != "" {
		return streamResult(command, r.matching[match]), true
	}
	return e2eexec.Result{}, false
}

// writeRecordedCDISpec writes the minimal CDI spec that nvidia-ctk would generate.
func writeRecordedCDISpec(args []string) error {
	data := []byte("kind: nvidia.com/gpu\ndevices:\n  - name: all\ncontainerEdits: {}\n")
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

// defaultResult returns configured outputs and default host, kubectl, and k3d responses.
func (r *recordingRunner) defaultResult(command e2eexec.Command, key string) (e2eexec.Result, bool) {
	if command.Name == "docker" && len(command.Args) == 3 && command.Args[0] == "image" && command.Args[1] == "inspect" {
		return streamResult(command, e2eexec.Result{Stdout: []byte(`[{"Os":"linux","Architecture":"amd64"}]`)}), true
	}
	if command.Name == "stat" && strings.Join(command.Args, " ") == "-fc %T /sys/fs/cgroup" {
		return streamResult(command, e2eexec.Result{Stdout: []byte("cgroup2fs\n")}), true
	}
	if output, ok := r.outputs[key]; ok {
		return streamResult(command, e2eexec.Result{Stdout: []byte(output)}), true
	}
	if command.Name == "kubectl" && len(command.Args) >= 3 && command.Args[0] == "get" && command.Args[1] == "namespace" {
		return streamResult(command, e2eexec.Result{Stdout: []byte(`{"metadata":{"labels":{"app.kubernetes.io/managed-by":"dcgm-exporter-e2e","e2eRunName":"dcgm-exporter"}},"status":{"phase":"Active"}}`)}), true
	}
	if command.Name == "kubectl" && includesArgs(command.Args, "get", "nodes", "-l") {
		// No node matches a label selector, so kubectl prints no names.
		return streamResult(command, e2eexec.Result{Stderr: []byte("No resources found\n")}), true
	}
	if command.Name == "kubectl" && hasArg(command.Args, "gpu-operator") && includesArgs(command.Args, "get", "pods", "-o", "json") {
		return streamResult(command, e2eexec.Result{Stdout: []byte(`{"items":[]}`)}), true
	}
	if command.Name == "k3d" && strings.Join(command.Args, " ") == "cluster list" && r.clusterList != "" {
		return streamResult(command, e2eexec.Result{Stdout: []byte(r.clusterList)}), true
	}
	return e2eexec.Result{}, false
}

// dockerResult reports the k3d node this runner created and absent networks and volumes.
func (r *recordingRunner) dockerResult(command e2eexec.Command) (e2eexec.Result, bool) {
	if command.Name != "docker" {
		return e2eexec.Result{}, false
	}
	if strings.Join(command.Args, " ") == "ps -a --format {{.Names}}" {
		for _, prior := range r.commands {
			if prior.Name == "k3d" && len(prior.Args) >= 3 && prior.Args[0] == "cluster" && prior.Args[1] == "create" {
				return streamResult(command, e2eexec.Result{Stdout: []byte("k3d-" + prior.Args[2] + "-server-0\n")}), true
			}
		}
	}
	if len(command.Args) < 3 {
		return e2eexec.Result{}, false
	}
	switch strings.Join(command.Args[:2], " ") {
	case "network inspect":
		return streamResult(command, e2eexec.Result{ExitCode: 1, Stderr: []byte("Error response from daemon: network " + command.Args[2] + " not found\n")}), true
	case "volume inspect":
		return streamResult(command, e2eexec.Result{ExitCode: 1, Stderr: []byte("Error response from daemon: get " + command.Args[2] + ": no such volume\n")}), true
	}
	return e2eexec.Result{}, false
}

// streamResult writes result's output to the command's writers, as a real run would, and returns result.
func streamResult(command e2eexec.Command, result e2eexec.Result) e2eexec.Result {
	if command.Stdout != nil && len(result.Stdout) != 0 {
		_, _ = command.Stdout.Write(result.Stdout)
	}
	if command.Stderr != nil && len(result.Stderr) != 0 {
		_, _ = command.Stderr.Write(result.Stderr)
	}
	return result
}

// commandIndex returns the index of the first recorded command with this name and all of args, or -1.
func (r *recordingRunner) commandIndex(name string, args ...string) int {
	for index, command := range r.commands {
		if command.Name == name && includesArgs(command.Args, args...) {
			return index
		}
	}
	return -1
}

// has reports whether a recorded command with this name has args, in order, in its arguments.
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

// hasVerb reports whether a recorded command begins with the requested executable and verb.
func (r *recordingRunner) hasVerb(name, verb string) bool {
	for _, command := range r.commands {
		if command.Name == name && len(command.Args) > 0 && command.Args[0] == verb {
			return true
		}
	}
	return false
}

// hasEnv reports whether a recorded command set the environment entry value.
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

// deletedNamespace reports whether a recorded kubectl command deleted namespace.
func (r *recordingRunner) deletedNamespace(namespace string) bool {
	for _, command := range r.commands {
		if command.Name == "kubectl" && len(command.Args) >= 3 && command.Args[0] == "delete" && command.Args[1] == "namespace" && command.Args[2] == namespace {
			return true
		}
	}
	return false
}

// stdinContains reports whether a recorded command's input contains value.
func (r *recordingRunner) stdinContains(value string) bool {
	for _, command := range r.commands {
		if strings.Contains(string(command.Stdin), value) {
			return true
		}
	}
	return false
}

// includesArgs reports whether haystack contains every needle, in any order.
func includesArgs(haystack []string, needles ...string) bool {
	for _, needle := range needles {
		if !hasArg(haystack, needle) {
			return false
		}
	}
	return true
}

// hasArg reports whether args contains want.
func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// commandIndexWithStdin also requires the recorded command input to contain the requested text.
func (r *recordingRunner) commandIndexWithStdin(name string, args []string, stdinContains string) int {
	for index, command := range r.commands {
		if command.Name == name && includesArgs(command.Args, args...) && strings.Contains(string(command.Stdin), stdinContains) {
			return index
		}
	}
	return -1
}

// ownedNamespaceLabels builds the manager and run labels used by namespace ownership fixtures.
func ownedNamespaceLabels(runName string) map[string]string {
	return map[string]string{managedByLabel: managedByValue, runNameLabel: runName}
}

// namespaceResult encodes fixture labels and an active phase in the JSON returned by kubectl.
func namespaceResult(labels map[string]string) e2eexec.Result {
	if labels == nil {
		labels = ownedNamespaceLabels("dcgm-exporter")
	}
	return e2eexec.Result{Stdout: []byte(fmt.Sprintf(`{"metadata":{"labels":{"%s":%q,"%s":%q}},"status":{"phase":"Active"}}`, managedByLabel, labels[managedByLabel], runNameLabel, labels[runNameLabel]))}
}

// Scripted command texts for GPU Operator setup and cleanup.
const (
	gpuOperatorNamespaceLookup    = "kubectl get namespace gpu-operator --ignore-not-found=true -o json"
	gpuOperatorInstall            = "helm upgrade --install gpu-operator"
	gpuOperatorUninstall          = "helm uninstall gpu-operator"
	gpuOperatorRegistryAuthCreate = "-n gpu-operator create secret generic"
	gpuOperatorNamespaceDelete    = "kubectl delete namespace gpu-operator"
	namespaceCreate               = "kubectl create -f -"
)

// readyOperatorExporterPods lists one ready operator-managed dcgm-exporter pod.
const readyOperatorExporterPods = `{"items":[{"metadata":{"name":"nvidia-dcgm-exporter"},` +
	`"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}`

// newGPUOperatorRunner returns a runner for a cluster without GPU Operator. The first lookup of each
// Operator resource type finds no such type and later lookups find the resource, as after an install; the
// Operator namespace is absent and its exporter pod is ready.
func newGPUOperatorRunner() *recordingRunner {
	runner := &recordingRunner{results: map[string]e2eexec.Result{
		"kubectl get clusterpolicy -o name":          {Stdout: []byte("clusterpolicy/gpu-cluster-policy\n")},
		"kubectl get gpucluster -o name":             {Stdout: []byte("gpucluster.nvidia.com/gpu-cluster\n")},
		"kubectl get namespace gpu-operator -o name": {ExitCode: 1, Stderr: []byte(`Error from server (NotFound): namespaces "gpu-operator" not found`)},
		"kubectl -n gpu-operator get pods -o json":   {Stdout: []byte(readyOperatorExporterPods)},
		"kubectl get resourceslices -o json":         {Stdout: []byte(`{"items":[{"spec":{"driver":"gpu.nvidia.com","devices":[{"basic":{"attributes":{"type":{"string":"gpu"}}}}]}}]}`)},
	}}
	return runner.
		queue("kubectl get clusterpolicy -o name", missingResourceType("clusterpolicy")).
		queue("kubectl get gpucluster -o name", missingResourceType("gpucluster"))
}

// missingResourceType is kubectl's answer for a resource type the cluster does not serve.
func missingResourceType(resource string) e2eexec.Result {
	return e2eexec.Result{ExitCode: 1, Stderr: []byte(`error: the server doesn't have a resource type "` + resource + `"`)}
}

// failedWith is a failed command result that carries err.
func failedWith(err error) e2eexec.Result {
	return e2eexec.Result{ExitCode: 1, Err: err}
}

// namespaceExists is kubectl's answer when a namespace create finds the namespace already there.
var namespaceExists = e2eexec.Result{ExitCode: 1, Stderr: []byte(`namespaces "gpu-operator" already exists`)}
