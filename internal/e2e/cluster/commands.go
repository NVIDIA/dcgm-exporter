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

// Package cluster implements operational Kubernetes/k3d commands for e2e.
package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
)

const (
	managedByLabel               = "app.kubernetes.io/managed-by"
	managedByValue               = "dcgm-exporter-e2e"
	runNameLabel                 = "e2eRunName"
	DefaultRunName               = "dcgm-exporter"
	namespaceDeletionWaitTimeout = "90s"
	kubeSystemNamespace          = "kube-system"
	devicePluginLabelSelector    = "app.kubernetes.io/name=nvidia-device-plugin"
	dcgmLabelSelector            = "app.kubernetes.io/name=dcgm"
	kubectlLogTailOption         = "--tail=200"
	kubectlAllContainersOption   = "--all-containers=true"
	kubectlIgnoreErrorsOption    = "--ignore-errors=true"
)

// Config is the operational cluster command configuration.
type Config struct {
	// Root is the absolute writable E2E artifact directory.
	Root            string
	RunName         string
	ClusterName     string
	Namespace       string
	ReleaseName     string
	DCGMNamespace   string
	Kubeconfig      string
	LocalKubeconfig string
	Follow          bool
	// pollInterval replaces the wait between checks of a cluster wait when set.
	pollInterval time.Duration
}

// diagnosticSectionEnd closes a diagnostic command's section in the output.
const diagnosticSectionEnd = "[e2e] ----- end %s -----\n"

// DefaultConfig returns the e2e local-k3d defaults.
func DefaultConfig() Config {
	cfg, _ := ConfigForRunName(DefaultRunName)
	return cfg
}

// ConfigForRunName derives resource names from a run name that is safe for
// Kubernetes namespaces and labels.
func ConfigForRunName(runName string) (Config, error) {
	if !validRunName(runName) {
		return Config{}, fmt.Errorf("run name must be a lowercase DNS label of at most 53 characters, got %q", runName)
	}
	return Config{
		RunName:       runName,
		ClusterName:   runName + "-gpu",
		Namespace:     runName,
		ReleaseName:   runName,
		DCGMNamespace: runName + "-dcgm",
	}, nil
}

// validRunName accepts lowercase ASCII letters, digits, and internal hyphens within the namespace length limit.
func validRunName(runName string) bool {
	if runName == "" || len(runName) > 53 || !isLowerAlphaNumericRune(rune(runName[0])) || !isLowerAlphaNumericRune(rune(runName[len(runName)-1])) {
		return false
	}
	for _, r := range runName {
		if r > 127 || !isLowerAlphaNumericRune(r) && r != '-' {
			return false
		}
	}
	return true
}

// runName returns the configured run name or the default for an unset configuration.
func (c Config) runName() string {
	if c.RunName == "" {
		return DefaultRunName
	}
	return c.RunName
}

// isLowerAlphaNumericRune reports whether a rune is an ASCII lowercase letter or digit.
func isLowerAlphaNumericRune(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

// LocalK3D reports whether e2e owns the local k3d lifecycle.
func (c Config) LocalK3D() bool {
	return c.Kubeconfig == ""
}

// ExporterConfig configures a standalone dcgm-exporter Helm deployment.
type ExporterConfig struct {
	Chart             string
	Image             string
	ImagePullSecret   string
	RuntimeClass      string
	NodeSelectorKey   string
	NodeSelectorValue string
	WaitTimeout       string
}

// CheckLocal verifies local k3d prerequisites without creating resources.
func CheckLocal(ctx context.Context, runner e2eexec.Runner, w io.Writer) error {
	for _, name := range []string{"docker", "k3d", "kubectl", "helm"} {
		if err := runRequired(ctx, runner, w, name+"_available", e2eexec.Command{Name: "sh", Args: []string{"-c", "command -v " + name}}); err != nil {
			return err
		}
	}
	if err := runRequired(ctx, runner, w, "docker_reachable", e2eexec.Command{Name: "docker", Args: []string{"ps"}}); err != nil {
		return err
	}
	if err := runRequired(ctx, runner, w, "nvidia_smi", e2eexec.Command{Name: "nvidia-smi", Args: []string{"-L"}}); err != nil {
		return err
	}
	fmt.Fprintln(w, "[e2e] local prerequisites are available")
	return nil
}

// CheckAPIReady verifies that the selected Kubernetes API is ready for e2e setup.
// It uses the configured kubeconfig and makes no changes to the cluster.
func CheckAPIReady(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	if err := runRequired(ctx, runner, w, "kubectl_api_ready", kubectl(cfg, "get", "--raw=/readyz")); err != nil {
		return fmt.Errorf("kubernetes API readiness failed: %w", err)
	}
	return nil
}

// DeployExporter installs or upgrades dcgm-exporter through the checked-in Helm chart.
func DeployExporter(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, exporter ExporterConfig) error {
	image, err := e2eimage.Parse(exporter.Image)
	if err != nil {
		return fmt.Errorf("dcgm-exporter deploy requires a complete exporter image: %w", err)
	}
	if exporter.Chart == "" {
		return fmt.Errorf("dcgm-exporter deploy requires a Helm chart path")
	}
	if exporter.WaitTimeout == "" {
		exporter.WaitTimeout = defaultK3dWaitTimeout
	}
	if err := RequireImagePullSecret(ctx, runner, cfg, cfg.Namespace, exporter.ImagePullSecret); err != nil {
		return err
	}
	if err := EnsureManagedNamespace(ctx, runner, w, cfg, cfg.Namespace); err != nil {
		return err
	}
	args := []string{
		"upgrade", "--install", cfg.ReleaseName, exporter.Chart,
		"--namespace", cfg.Namespace,
		"--set", "image.repository=" + image.Repository(),
	}
	if image.Tag() != "" {
		args = append(args, "--set", "image.tag="+image.Tag())
	}
	if image.Digest() != "" {
		args = append(args, "--set", "image.digest="+image.Digest())
	}
	if exporter.ImagePullSecret != "" {
		args = append(args, "--set", "imagePullSecrets[0].name="+exporter.ImagePullSecret)
	}
	if exporter.RuntimeClass != "" {
		args = append(args, "--set", "runtimeClassName="+exporter.RuntimeClass)
	}
	if exporter.NodeSelectorKey != "" && exporter.NodeSelectorValue != "" {
		key := strings.ReplaceAll(exporter.NodeSelectorKey, ".", "\\.")
		args = append(args, "--set-string", "nodeSelector."+key+"="+exporter.NodeSelectorValue)
	}
	args = append(args, "--wait", "--timeout", exporter.WaitTimeout)
	return runHelm(ctx, runner, w, cfg, "helm_deploy_exporter", args)
}

// EnsureManagedNamespace creates a namespace for this run or reuses one that
// already carries the same ownership labels.
func EnsureManagedNamespace(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace string) error {
	_, err := ensureManagedNamespace(ctx, runner, w, cfg, namespace)
	return err
}

// ensureManagedNamespace reports whether this run created the namespace.
func ensureManagedNamespace(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace string) (bool, error) {
	if namespace == "" {
		return false, nil
	}
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name": namespace,
			"labels": map[string]string{
				managedByLabel:              managedByValue,
				runNameLabel:                cfg.runName(),
				"app.kubernetes.io/part-of": "dcgm-exporter",
			},
		},
	})
	if err != nil {
		return false, fmt.Errorf("encode ownership for namespace %q: %w", namespace, err)
	}
	create := kubectlWithStdin(cfg, manifest, "create", "-f", "-")
	createErr := runRequired(ctx, runner, w, "kubectl_create_namespace", create)
	if createErr == nil {
		return true, nil
	}
	info, err := ownedExistingNamespace(ctx, runner, w, cfg, namespace, createErr)
	if err != nil {
		return false, err
	}
	if info.Phase == "Terminating" {
		var created bool
		info, created, err = recreateAfterDeletion(ctx, runner, w, cfg, namespace, create, createErr)
		if err != nil || created {
			return created, err
		}
	}
	if info.Phase != "Active" {
		return false, fmt.Errorf("namespace %q has phase %q and cannot be reused by run %q", namespace, info.Phase, cfg.runName())
	}
	fmt.Fprintf(w, "[e2e] reusing namespace %s owned by run %s\n", namespace, cfg.runName())
	return false, nil
}

// ownedExistingNamespace explains a failed create: it returns the existing
// namespace only when this run owns it, and otherwise the create or ownership error.
func ownedExistingNamespace(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace string, createErr error) (namespaceInfo, error) {
	info, found, err := getNamespaceInfo(ctx, runner, w, cfg, namespace)
	if err != nil {
		return namespaceInfo{}, errors.Join(createErr, err)
	}
	if !found {
		return namespaceInfo{}, createErr
	}
	if !namespaceOwnedByRun(info.Labels, cfg.runName()) {
		return namespaceInfo{}, fmt.Errorf("namespace %q already exists and is not owned by run %q", namespace, cfg.runName())
	}
	return info, nil
}

// recreateAfterDeletion waits for an owned namespace to finish terminating and
// creates it again. It reports created=true, or the owned namespace left behind.
func recreateAfterDeletion(
	ctx context.Context,
	runner e2eexec.Runner,
	w io.Writer,
	cfg Config,
	namespace string,
	create e2eexec.Command,
	createErr error,
) (namespaceInfo, bool, error) {
	wait := kubectl(cfg, "wait", "--for=delete", "namespace/"+namespace, "--timeout="+namespaceDeletionWaitTimeout)
	if err := runRequired(ctx, runner, w, "kubectl_wait_namespace_deletion", wait); err != nil {
		return namespaceInfo{}, false, errors.Join(createErr, fmt.Errorf("wait for namespace %q to finish deleting: %w", namespace, err))
	}
	createErr = runRequired(ctx, runner, w, "kubectl_create_namespace", create)
	if createErr == nil {
		return namespaceInfo{}, true, nil
	}
	info, err := ownedExistingNamespace(ctx, runner, w, cfg, namespace, createErr)
	if err != nil {
		return namespaceInfo{}, false, err
	}
	if info.Phase == "Terminating" {
		return namespaceInfo{}, false, fmt.Errorf("namespace %q is still terminating after waiting for deletion: %w", namespace, createErr)
	}
	return info, false, nil
}

// namespaceOwnedByRun requires both the e2e manager label and this run name to match.
func namespaceOwnedByRun(labels map[string]string, runName string) bool {
	return labels[managedByLabel] == managedByValue && labels[runNameLabel] == runName
}

// namespaceInfo contains the ownership labels and lifecycle phase read from a namespace.
type namespaceInfo struct {
	Labels map[string]string
	Phase  string
}

// getNamespaceInfo reads namespace labels and phase, distinguishing a missing namespace from query or JSON errors.
func getNamespaceInfo(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace string) (namespaceInfo, bool, error) {
	command := kubectl(cfg, "get", "namespace", namespace, "--ignore-not-found=true", "-o", "json")
	fmt.Fprintf(w, "[e2e] ----- kubectl_get_namespace: %s -----\n", commandString(command))
	result := runner.Run(ctx, command)
	fmt.Fprintln(w, "[e2e] ----- end kubectl_get_namespace -----")
	if result.ExitCode != 0 {
		return namespaceInfo{}, false, resultError(commandString(command), result)
	}
	if len(strings.TrimSpace(string(result.Stdout))) == 0 {
		return namespaceInfo{}, false, nil
	}
	var namespaceObject struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.Unmarshal(result.Stdout, &namespaceObject); err != nil {
		return namespaceInfo{}, false, fmt.Errorf("read ownership for namespace %q: %w", namespace, err)
	}
	return namespaceInfo{Labels: namespaceObject.Metadata.Labels, Phase: namespaceObject.Status.Phase}, true, nil
}

// Status prints cluster and exporter status.
func Status(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	errs := []error{ensureLocalKubeconfig(ctx, runner, w, cfg)}
	errs = append(
		errs,
		run(w, runner, ctx, "k3d_cluster_list", e2eexec.Command{Name: "k3d", Args: []string{"cluster", "list"}}),
		run(w, runner, ctx, "k3d_registry_list", e2eexec.Command{Name: "k3d", Args: []string{"registry", "list"}}),
		run(w, runner, ctx, "kubectl_nodes", kubectl(cfg, "get", "nodes", "-o", "wide")),
		run(w, runner, ctx, "kubectl_node_gpu", kubectl(cfg, "get", "nodes", "-o", "custom-columns=NAME:.metadata.name,GPU:.status.allocatable.nvidia\\.com/gpu")),
		run(w, runner, ctx, "kubectl_device_plugin", kubectl(cfg, "-n", kubeSystemNamespace, "get", "pods", "-l", devicePluginLabelSelector, "-o", "wide")),
		run(w, runner, ctx, "kubectl_exporter", kubectl(cfg, "-n", cfg.Namespace, "get", "pods,ds,svc", "-l", "app.kubernetes.io/instance="+cfg.ReleaseName, "-o", "wide")),
		run(w, runner, ctx, "kubectl_dcgm", kubectl(cfg, "-n", cfg.DCGMNamespace, "get", "pods,deploy,svc", "-l", dcgmLabelSelector, "-o", "wide")),
	)
	return errors.Join(errs...)
}

// Logs prints recent exporter and DCGM logs.
func Logs(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	errs := []error{ensureLocalKubeconfig(ctx, runner, w, cfg)}
	args := []string{"-n", cfg.Namespace, "logs", "-l", "app.kubernetes.io/name=dcgm-exporter,app.kubernetes.io/instance=" + cfg.ReleaseName, "-c", "exporter", kubectlLogTailOption}
	if cfg.Follow {
		args = append(args, "-f")
	}
	errs = append(errs, run(w, runner, ctx, "kubectl_exporter_logs", kubectl(cfg, args...)))
	if cfg.Follow {
		return errors.Join(errs...)
	}
	errs = append(errs, run(w, runner, ctx, "kubectl_dcgm_logs", kubectl(cfg, "-n", cfg.DCGMNamespace, "logs", "-l", dcgmLabelSelector, "-c", "dcgm", kubectlLogTailOption)))
	return errors.Join(errs...)
}

// Diagnostics prints best-effort pre-cleanup evidence for failed Kubernetes operations.
func Diagnostics(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	var errs []error
	if cfg.LocalK3D() {
		errs = append(
			errs,
			run(w, runner, ctx, "nvidia_smi_layout", e2eexec.Command{Name: "nvidia-smi", Args: []string{"-L"}}),
			run(w, runner, ctx, "nvidia_smi_mig_mode", e2eexec.Command{Name: "nvidia-smi", Args: []string{"--query-gpu=index,name,mig.mode.current,mig.mode.pending", "--format=csv,noheader"}}),
			run(w, runner, ctx, "host_nvidia_ctk_version", e2eexec.Command{Name: "nvidia-ctk", Args: []string{"--version"}}),
			run(w, runner, ctx, "host_nvidia_container_cli_version", e2eexec.Command{Name: "nvidia-container-cli", Args: []string{"--version"}}),
			run(w, runner, ctx, "docker_runtimes", e2eexec.Command{Name: "docker", Args: []string{"info", "--format", "{{json .Runtimes}}"}}),
		)
		errs = append(errs, localDockerStorageDiagnostics(ctx, runner, w)...)
		errs = append(errs, localK3DContainerDiagnostics(ctx, runner, w, cfg.ClusterName)...)
		exists, err := localClusterExists(ctx, runner, cfg.ClusterName)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		if !exists {
			fmt.Fprintf(w, "[e2e] local k3d cluster %s is absent; skipping Kubernetes diagnostics\n", cfg.ClusterName)
			return errors.Join(errs...)
		}
	}
	errs = append(errs, ensureLocalKubeconfig(ctx, runner, w, cfg))
	errs = append(
		errs,
		run(w, runner, ctx, "kubectl_describe_nodes", kubectl(cfg, "describe", "nodes")),
		run(w, runner, ctx, "kubectl_all_pods", kubectl(cfg, "get", "pods", "-A", "-o", "wide")),
		run(w, runner, ctx, "kubectl_all_daemonsets", kubectl(cfg, "get", "daemonsets", "-A", "-o", "wide")),
		run(w, runner, ctx, "kubectl_all_deployments", kubectl(cfg, "get", "deployments", "-A", "-o", "wide")),
		run(w, runner, ctx, "kubectl_events", kubectl(cfg, "get", "events", "-A", "--sort-by=.lastTimestamp")),
		run(w, runner, ctx, "kubectl_describe_exporter_pods", kubectl(cfg, "-n", cfg.Namespace, "describe", "pods")),
		run(w, runner, ctx, "kubectl_exporter_logs", kubectl(cfg, "-n", cfg.Namespace, "logs", "-l", "app.kubernetes.io/name=dcgm-exporter,app.kubernetes.io/instance="+cfg.ReleaseName, "-c", "exporter", kubectlLogTailOption)),
		run(w, runner, ctx, "kubectl_describe_device_plugin", kubectl(cfg, "-n", kubeSystemNamespace, "describe", "daemonset,pods", "-l", devicePluginLabelSelector)),
		runHelmDiagnostic(ctx, runner, w, cfg, "helm_device_plugin_status", []string{"status", devicePluginReleaseName, "--namespace", kubeSystemNamespace}),
		run(w, runner, ctx, "kubectl_device_plugin_logs", kubectl(cfg, "-n", kubeSystemNamespace, "logs", "-l", devicePluginLabelSelector, kubectlAllContainersOption, kubectlIgnoreErrorsOption, kubectlLogTailOption)),
		run(w, runner, ctx, "kubectl_device_plugin_previous_logs", kubectl(cfg, "-n", kubeSystemNamespace, "logs", "-l", devicePluginLabelSelector, kubectlAllContainersOption, kubectlIgnoreErrorsOption, "--previous", kubectlLogTailOption)),
		run(w, runner, ctx, "kubectl_dra_deviceclasses", kubectl(cfg, "get", "deviceclasses", "-o", "yaml")),
		run(w, runner, ctx, "kubectl_dra_resourceslices", kubectl(cfg, "get", "resourceslices", "-o", "yaml")),
		run(w, runner, ctx, "kubectl_dra_resourceclaims", kubectl(cfg, "get", "resourceclaims", "-A", "-o", "yaml")),
		run(w, runner, ctx, "kubectl_dra_plugin_logs", kubectl(cfg, "-n", draDriverNamespace, "logs", "-l", "app.kubernetes.io/name=nvidia-dra-driver-gpu", "-c", "gpus", kubectlLogTailOption)),
	)
	errs = append(
		errs,
		run(w, runner, ctx, "kubectl_describe_dcgm", kubectl(cfg, "-n", cfg.DCGMNamespace, "describe", "pods,deploy,svc")),
		run(w, runner, ctx, "kubectl_dcgm_logs", kubectl(cfg, "-n", cfg.DCGMNamespace, "logs", "-l", dcgmLabelSelector, "-c", "dcgm", kubectlLogTailOption)),
	)
	errs = append(errs, gpuOperatorDiagnostics(ctx, runner, w, cfg)...)
	return errors.Join(errs...)
}

// gpuOperatorDiagnostics prints the ClusterPolicy and the Operator namespace pods, events, and logs.
// It skips the Operator commands when the Operator namespace does not exist.
func gpuOperatorDiagnostics(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) []error {
	_, found, err := getNamespaceInfo(ctx, runner, w, cfg, gpuOperatorNamespace)
	if err != nil {
		return []error{err}
	}
	if !found {
		return nil
	}

	errs := []error{
		runClusterPolicyDiagnostic(ctx, runner, w, cfg),
		run(w, runner, ctx, "kubectl_gpu_operator_pods", kubectl(cfg, "-n", gpuOperatorNamespace, "get", "pods", "-o", "wide")),
		run(w, runner, ctx, "kubectl_gpu_operator_events", kubectl(cfg, "-n", gpuOperatorNamespace, "get", "events", "--sort-by=.lastTimestamp")),
		run(w, runner, ctx, "kubectl_describe_gpu_operator_pods", kubectl(cfg, "-n", gpuOperatorNamespace, "describe", "pods")),
	}

	return append(errs, gpuOperatorLogDiagnostics(ctx, runner, w, cfg)...)
}

// gpuOperatorLogDiagnostics prints current and previous logs of every container of each Operator namespace pod.
// Each container is read on its own because kubectl stops at the first container that has no log.
func gpuOperatorLogDiagnostics(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) []error {
	pods, err := listGPUOperatorPodContainers(ctx, runner, cfg)
	if err != nil {
		return []error{err}
	}

	var errs []error
	for _, pod := range pods {
		for _, container := range pod.Containers {
			logs := kubectl(cfg, "-n", gpuOperatorNamespace, "logs", pod.Name, "-c", container, kubectlLogTailOption)
			previous := kubectl(cfg, "-n", gpuOperatorNamespace, "logs", pod.Name, "-c", container, "--previous", kubectlLogTailOption)
			errs = append(
				errs,
				runTolerantDiagnostic(ctx, runner, w, "kubectl_gpu_operator_logs", logs, containerLogsUnavailable),
				runTolerantDiagnostic(ctx, runner, w, "kubectl_gpu_operator_previous_logs", previous, containerLogsUnavailable),
			)
		}
	}

	return errs
}

// gpuOperatorPod names an Operator namespace pod and its init and regular containers.
type gpuOperatorPod struct {
	Name       string
	Containers []string
}

// listGPUOperatorPodContainers reads the pod and container names of the Operator namespace with one kubectl call.
func listGPUOperatorPodContainers(ctx context.Context, runner e2eexec.Runner, cfg Config) ([]gpuOperatorPod, error) {
	command := kubectl(cfg, "-n", gpuOperatorNamespace, "get", "pods", "-o", "json")
	result := runner.Run(ctx, command)
	if result.ExitCode != 0 {
		return nil, resultError(commandString(command), result)
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				InitContainers []struct {
					Name string `json:"name"`
				} `json:"initContainers"`
				Containers []struct {
					Name string `json:"name"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(result.Stdout, &list); err != nil {
		return nil, fmt.Errorf("read pods in namespace %q: %w", gpuOperatorNamespace, err)
	}

	pods := make([]gpuOperatorPod, 0, len(list.Items))
	for _, item := range list.Items {
		pod := gpuOperatorPod{Name: item.Metadata.Name}
		for _, container := range item.Spec.InitContainers {
			pod.Containers = append(pod.Containers, container.Name)
		}
		for _, container := range item.Spec.Containers {
			pod.Containers = append(pod.Containers, container.Name)
		}
		pods = append(pods, pod)
	}

	return pods, nil
}

// containerLogsUnavailable reports a container with no log to read yet: it is still starting, or it never restarted.
func containerLogsUnavailable(result e2eexec.Result) bool {
	output := strings.ToLower(resultOutput(result))
	return strings.Contains(output, "waiting to start") ||
		strings.Contains(output, "containercreating") ||
		strings.Contains(output, "podinitializing") ||
		strings.Contains(output, "previous terminated container")
}

// runClusterPolicyDiagnostic prints the ClusterPolicy objects and accepts a cluster without the ClusterPolicy CRD.
func runClusterPolicyDiagnostic(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	return runTolerantDiagnostic(ctx, runner, w, "kubectl_gpu_operator_clusterpolicy", kubectl(cfg, "get", "clusterpolicy", "-o", "yaml"), gpuOperatorResourceTypeMissing)
}

// runTolerantDiagnostic runs a command and treats a failure that tolerated accepts as success.
// Only stdout is streamed; stderr stays in the result so tolerated can read it.
// Every failure prints its reason inside the section, so the artifact explains
// an empty or partial section.
func runTolerantDiagnostic(ctx context.Context, runner e2eexec.Runner, w io.Writer, name string, command e2eexec.Command, tolerated func(e2eexec.Result) bool) error {
	fmt.Fprintf(w, "[e2e] ----- %s: %s -----\n", name, commandString(command))
	command.Stdout = w
	result := runner.Run(ctx, command)
	if result.ExitCode == 0 {
		fmt.Fprintf(w, diagnosticSectionEnd, name)
		return nil
	}

	if tolerated(result) {
		fmt.Fprintf(w, "[e2e] %s unavailable: %s\n", name, resultOutput(result))
		fmt.Fprintf(w, diagnosticSectionEnd, name)
		return nil
	}

	fmt.Fprintf(w, "[e2e] %s failed: %s\n", name, resultOutput(result))
	fmt.Fprintf(w, diagnosticSectionEnd, name)

	return resultError(commandString(command), result)
}

func localK3DContainerDiagnostics(ctx context.Context, runner e2eexec.Runner, w io.Writer, clusterName string) []error {
	command := e2eexec.Command{
		Name: "docker",
		Args: []string{
			"ps", "-a",
			"--filter", "name=^/k3d-" + clusterName + "-",
			"--format", "{{.Names}}\t{{.Image}}\t{{.Status}}",
		},
	}
	fmt.Fprintf(w, "[e2e] ----- docker_k3d_containers: %s -----\n", commandString(command))
	command.Stdout = w
	command.Stderr = w
	result := runner.Run(ctx, command)
	fmt.Fprintln(w, "[e2e] ----- end docker_k3d_containers -----")
	if result.ExitCode != 0 {
		return []error{resultError(commandString(command), result)}
	}

	var errs []error
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		container := fields[0]
		errs = append(
			errs,
			run(w, runner, ctx, "docker_inspect_"+container, e2eexec.Command{Name: "docker", Args: []string{"inspect", container, "--format", "{{json .State}}"}}),
			run(w, runner, ctx, "docker_logs_"+container, e2eexec.Command{Name: "docker", Args: []string{"logs", "--tail", "200", container}}),
		)
		if !strings.Contains(container, "-server-") && !strings.Contains(container, "-agent-") {
			continue
		}
		errs = append(
			errs,
			run(w, runner, ctx, "node_nvidia_ctk_version_"+container, e2eexec.Command{Name: "docker", Args: []string{"exec", container, "nvidia-ctk", "--version"}}),
			run(w, runner, ctx, "node_nvidia_container_cli_version_"+container, e2eexec.Command{Name: "docker", Args: []string{"exec", container, "nvidia-container-cli", "--version"}}),
			run(w, runner, ctx, "node_nvidia_runtime_config_"+container, e2eexec.Command{Name: "docker", Args: []string{"exec", container, "sh", "-c", "grep -E '^(mode|root) =' /etc/nvidia-container-runtime/config.toml"}}),
		)
	}
	return errs
}

func localDockerStorageDiagnostics(ctx context.Context, runner e2eexec.Runner, w io.Writer) []error {
	var errs []error
	command := e2eexec.Command{Name: "docker", Args: []string{"info", "--format", "{{.DockerRootDir}}"}}
	fmt.Fprintf(w, "[e2e] ----- docker_root_dir: %s -----\n", commandString(command))
	command.Stdout = w
	command.Stderr = w
	result := runner.Run(ctx, command)
	fmt.Fprintln(w, "[e2e] ----- end docker_root_dir -----")
	if result.ExitCode != 0 {
		errs = append(errs, resultError(commandString(command), result))
	} else if rootDir := strings.TrimSpace(string(result.Stdout)); rootDir == "" {
		errs = append(errs, errors.New("docker info returned an empty DockerRootDir"))
	} else {
		errs = append(errs, run(w, runner, ctx, "docker_root_filesystem", e2eexec.Command{Name: "df", Args: []string{"-h", rootDir}}))
	}
	errs = append(errs, run(w, runner, ctx, "docker_system_df", e2eexec.Command{Name: "docker", Args: []string{"system", "df"}}))
	return errs
}

// Cleanup removes resources owned by e2e validation.
func Cleanup(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) (err error) {
	defer func() { err = errors.Join(err, cleanupHelmState(cfg)) }()
	if cfg.LocalK3D() {
		exists, err := localClusterExists(ctx, runner, cfg.ClusterName)
		if err != nil {
			return err
		}
		var errs []error
		if !exists {
			fmt.Fprintf(w, "[e2e] local k3d cluster %s already absent\n", cfg.ClusterName)
		} else {
			errs = append(errs, run(w, runner, ctx, "k3d_cluster_delete", e2eexec.Command{Name: "k3d", Args: []string{"cluster", "delete", cfg.ClusterName}}))
		}
		errs = append(errs, cleanupStaleLocalK3DResources(ctx, runner, w, LocalConfig{
			ClusterName: cfg.ClusterName,
			NetworkName: "k3d-" + cfg.ClusterName,
		}))
		return errors.Join(errs...)
	}
	return errors.Join(
		cleanupManagedNamespace(ctx, runner, w, cfg, cfg.Namespace, "kubectl_delete_namespace"),
		cleanupManagedNamespace(ctx, runner, w, cfg, cfg.DCGMNamespace, "kubectl_delete_dcgm_namespace"),
	)
}

// cleanupManagedNamespace deletes the named external test environment. The
// configured name and ownership labels prevent deleting another run's namespace.
func cleanupManagedNamespace(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace, commandName string) error {
	if namespace == "" {
		return nil
	}
	info, found, err := getNamespaceInfo(ctx, runner, w, cfg, namespace)
	if err != nil {
		return err
	}
	if !found {
		fmt.Fprintf(w, "[e2e] namespace %s already absent\n", namespace)
		return nil
	}
	if !namespaceOwnedByRun(info.Labels, cfg.runName()) {
		return fmt.Errorf("refusing to delete namespace %q: it is not owned by run %q", namespace, cfg.runName())
	}
	// kubectl delete does not expose DeleteOptions.Preconditions. Recheck ownership
	// immediately before the named delete and let missing namespaces succeed on retries.
	return run(w, runner, ctx, commandName, kubectl(cfg, "delete", "namespace", namespace, "--ignore-not-found=true"))
}

// cleanupOwnedRelease uninstalls a Helm release and deletes its namespace when this run owns the
// namespace. An absent namespace needs no cleanup; a namespace another run owns is left unchanged and
// reported as an error.
// The step names are helm_uninstall_<name> and kubectl_delete_<name>_namespace.
func cleanupOwnedRelease(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, name, release, namespace, timeout string) error {
	info, found, err := getNamespaceInfo(ctx, runner, w, cfg, namespace)
	if err != nil {
		return fmt.Errorf("check namespace %q before removing release %s: %w", namespace, release, err)
	}
	if !found {
		return nil
	}
	if !namespaceOwnedByRun(info.Labels, cfg.runName()) {
		return fmt.Errorf("refusing to remove release %s: namespace %q is not owned by run %q", release, namespace, cfg.runName())
	}

	var errs []error
	if err := uninstallHelmRelease(ctx, runner, w, cfg, "helm_uninstall_"+name, release, namespace, timeout); err != nil {
		errs = append(errs, fmt.Errorf("uninstall release %s: %w", release, err))
	}
	if err := cleanupManagedNamespace(ctx, runner, w, cfg, namespace, "kubectl_delete_"+name+"_namespace"); err != nil {
		errs = append(errs, fmt.Errorf("delete namespace %q: %w", namespace, err))
	}
	return errors.Join(errs...)
}

// ensureLocalKubeconfig refreshes the kubeconfig for owned local k3d clusters.
func ensureLocalKubeconfig(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	if !cfg.LocalK3D() || cfg.LocalKubeconfig == "" {
		return nil
	}
	return run(w, runner, ctx, "k3d_kubeconfig_write", e2eexec.Command{Name: "k3d", Args: []string{"kubeconfig", "write", cfg.ClusterName, "--output", cfg.LocalKubeconfig}})
}

// kubectl builds a command with the effective kubeconfig wired through the environment.
func kubectl(cfg Config, args ...string) e2eexec.Command {
	command := e2eexec.Command{Name: "kubectl", Args: args}
	if kubeconfig := cfg.EffectiveKubeconfig(); kubeconfig != "" {
		command.Env = []string{"KUBECONFIG=" + kubeconfig}
	}
	return command
}

// EffectiveKubeconfig returns the kubeconfig path kubectl should use.
func (c Config) EffectiveKubeconfig() string {
	if c.Kubeconfig != "" {
		return c.Kubeconfig
	}
	return c.LocalKubeconfig
}

// run logs, streams, and checks one cluster command.
func run(w io.Writer, runner e2eexec.Runner, ctx context.Context, name string, command e2eexec.Command) error {
	fmt.Fprintf(w, "[e2e] ----- %s: %s -----\n", name, commandString(command))
	command.Stdout = w
	command.Stderr = w
	result := runner.Run(ctx, command)
	fmt.Fprintf(w, diagnosticSectionEnd, name)
	if result.ExitCode != 0 {
		return resultError(commandString(command), result)
	}
	return nil
}

// commandString renders a command for human-readable e2e logs.
func commandString(command e2eexec.Command) string {
	out := command.Name
	for _, arg := range command.Args {
		out += " " + arg
	}
	return out
}
