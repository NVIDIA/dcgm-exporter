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
	"errors"
	"fmt"
	"io"
	"strings"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "dcgm-exporter-e2e"
)

// Config is the operational cluster command configuration.
type Config struct {
	ClusterName     string
	Namespace       string
	ReleaseName     string
	DCGMNamespace   string
	Kubeconfig      string
	LocalKubeconfig string
	Follow          bool
}

// DefaultConfig returns the e2e local-k3d defaults.
func DefaultConfig() Config {
	return Config{
		ClusterName:   "dcgm-exporter-gpu",
		Namespace:     "dcgm-exporter",
		ReleaseName:   "dcgm-exporter",
		DCGMNamespace: "dcgm-exporter-dcgm",
	}
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
	return runRequired(ctx, runner, w, "helm_deploy_exporter", e2eexec.Command{Name: "helm", Args: args, Env: kubeEnv(cfg)})
}

// EnsureManagedNamespace creates an e2e-owned namespace or validates ownership before reuse.
func EnsureManagedNamespace(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace string) error {
	if namespace == "" {
		return nil
	}
	label := runner.Run(ctx, kubectl(cfg, "get", "namespace", namespace, "-o", "jsonpath={.metadata.labels.app\\.kubernetes\\.io/managed-by}"))
	if label.ExitCode == 0 {
		got := strings.TrimSpace(string(label.Stdout))
		if got == managedByValue {
			return nil
		}
		if got != "" {
			return fmt.Errorf("namespace %s is managed by %s, not %s", namespace, got, managedByValue)
		}
		return fmt.Errorf("namespace %s already exists without %s=%s", namespace, managedByLabel, managedByValue)
	}
	namespaceYAML := runner.Run(ctx, kubectl(cfg, "create", "namespace", namespace, "--dry-run=client", "-o", "yaml"))
	if namespaceYAML.ExitCode != 0 {
		return resultError("kubectl create namespace", namespaceYAML)
	}
	if err := runRequired(ctx, runner, w, "kubectl_apply_namespace", kubectlWithStdin(cfg, namespaceYAML.Stdout, "apply", "-f", "-")); err != nil {
		return err
	}
	return runRequired(ctx, runner, w, "kubectl_label_namespace", kubectl(cfg, "label", "namespace", namespace, managedByLabel+"="+managedByValue, "app.kubernetes.io/part-of=dcgm-exporter", "--overwrite"))
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
		run(w, runner, ctx, "kubectl_device_plugin", kubectl(cfg, "-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=nvidia-device-plugin", "-o", "wide")),
		run(w, runner, ctx, "kubectl_exporter", kubectl(cfg, "-n", cfg.Namespace, "get", "pods,ds,svc", "-l", "app.kubernetes.io/instance="+cfg.ReleaseName, "-o", "wide")),
		run(w, runner, ctx, "kubectl_dcgm", kubectl(cfg, "-n", cfg.DCGMNamespace, "get", "pods,deploy,svc", "-l", "app.kubernetes.io/name=dcgm", "-o", "wide")),
	)
	return errors.Join(errs...)
}

// Logs prints recent exporter and DCGM logs.
func Logs(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	errs := []error{ensureLocalKubeconfig(ctx, runner, w, cfg)}
	args := []string{"-n", cfg.Namespace, "logs", "-l", "app.kubernetes.io/name=dcgm-exporter,app.kubernetes.io/instance=" + cfg.ReleaseName, "-c", "exporter", "--tail=200"}
	if cfg.Follow {
		args = append(args, "-f")
	}
	errs = append(errs, run(w, runner, ctx, "kubectl_exporter_logs", kubectl(cfg, args...)))
	if cfg.Follow {
		return errors.Join(errs...)
	}
	errs = append(errs, run(w, runner, ctx, "kubectl_dcgm_logs", kubectl(cfg, "-n", cfg.DCGMNamespace, "logs", "-l", "app.kubernetes.io/name=dcgm", "-c", "dcgm", "--tail=200")))
	return errors.Join(errs...)
}

// Diagnostics prints best-effort pre-cleanup evidence for failed k8s groups.
func Diagnostics(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	errs := []error{ensureLocalKubeconfig(ctx, runner, w, cfg)}
	errs = append(
		errs,
		run(w, runner, ctx, "kubectl_describe_nodes", kubectl(cfg, "describe", "nodes")),
		run(w, runner, ctx, "kubectl_all_pods", kubectl(cfg, "get", "pods", "-A", "-o", "wide")),
		run(w, runner, ctx, "kubectl_all_daemonsets", kubectl(cfg, "get", "daemonsets", "-A", "-o", "wide")),
		run(w, runner, ctx, "kubectl_all_deployments", kubectl(cfg, "get", "deployments", "-A", "-o", "wide")),
		run(w, runner, ctx, "kubectl_events", kubectl(cfg, "get", "events", "-A", "--sort-by=.lastTimestamp")),
		run(w, runner, ctx, "kubectl_describe_exporter_pods", kubectl(cfg, "-n", cfg.Namespace, "describe", "pods")),
		run(w, runner, ctx, "kubectl_exporter_logs", kubectl(cfg, "-n", cfg.Namespace, "logs", "-l", "app.kubernetes.io/name=dcgm-exporter,app.kubernetes.io/instance="+cfg.ReleaseName, "-c", "exporter", "--tail=200")),
		run(w, runner, ctx, "kubectl_describe_device_plugin", kubectl(cfg, "-n", "kube-system", "describe", "daemonset,pods", "-l", "app.kubernetes.io/name=nvidia-device-plugin")),
		run(w, runner, ctx, "kubectl_device_plugin_logs", kubectl(cfg, "-n", "kube-system", "logs", "-l", "app.kubernetes.io/name=nvidia-device-plugin", "--tail=200")),
		run(w, runner, ctx, "kubectl_describe_dcgm", kubectl(cfg, "-n", cfg.DCGMNamespace, "describe", "pods,deploy,svc")),
		run(w, runner, ctx, "kubectl_dcgm_logs", kubectl(cfg, "-n", cfg.DCGMNamespace, "logs", "-l", "app.kubernetes.io/name=dcgm", "-c", "dcgm", "--tail=200")),
	)
	return errors.Join(errs...)
}

// Cleanup removes resources owned by e2e validation.
func Cleanup(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	if cfg.LocalK3D() {
		exists, err := localClusterExists(ctx, runner, cfg.ClusterName)
		if err != nil {
			return err
		}
		if !exists {
			fmt.Fprintf(w, "[e2e] local k3d cluster %s already absent\n", cfg.ClusterName)
			return nil
		}
		return run(w, runner, ctx, "k3d_cluster_delete", e2eexec.Command{Name: "k3d", Args: []string{"cluster", "delete", cfg.ClusterName}})
	}
	return errors.Join(
		cleanupManagedNamespace(ctx, runner, w, cfg, cfg.Namespace, "kubectl_delete_namespace"),
		cleanupManagedNamespace(ctx, runner, w, cfg, cfg.DCGMNamespace, "kubectl_delete_dcgm_namespace"),
	)
}

// cleanupManagedNamespace deletes only namespaces labeled as e2e-managed.
func cleanupManagedNamespace(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, namespace, commandName string) error {
	if namespace == "" {
		return nil
	}
	label := runner.Run(ctx, kubectl(cfg, "get", "namespace", namespace, "--ignore-not-found=true", "-o", "jsonpath={.metadata.name}{\"\\t\"}{.metadata.labels.app\\.kubernetes\\.io/managed-by}"))
	if label.ExitCode != 0 {
		return resultError("kubectl get namespace "+namespace, label)
	}
	output := strings.TrimRight(string(label.Stdout), "\r\n")
	if output == "" {
		return nil
	}
	parts := strings.SplitN(output, "\t", 2)
	owner := ""
	if len(parts) == 2 {
		owner = strings.TrimSpace(parts[1])
	}
	if owner != managedByValue {
		fmt.Fprintf(w, "[e2e] WARN skipping cleanup for namespace %s: not managed by %s\n", namespace, managedByValue)
		return nil
	}
	return run(w, runner, ctx, commandName, kubectl(cfg, "delete", "namespace", namespace, "--ignore-not-found=true"))
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
	fmt.Fprintf(w, "[e2e] ----- end %s -----\n", name)
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
