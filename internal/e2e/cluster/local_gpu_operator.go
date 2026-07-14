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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
)

// PrepareGPUOperator installs or validates GPU Operator resources for a selected scenario.
func PrepareGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (func(context.Context), error) {
	if gpuOperatorExisting(ctx, runner, cfg) {
		if scenarioName == "gpuOperatorDRA" {
			return PrepareDRA(ctx, runner, w, cfg, featureCfg)
		}
		if scenarioName == "gpuOperatorIPv6" {
			cleanup := func(cleanupCtx context.Context) {
				_ = CleanupGPUOperatorIPv6Service(cleanupCtx, runner, w, cfg)
			}
			return cleanup, EnsureGPUOperatorIPv6Service(ctx, runner, w, cfg, featureCfg)
		}
		if scenarioName == "gpuOperatorMig" {
			return func(context.Context) {}, WaitForGPUOperatorMIGResources(ctx, runner, cfg, timeoutFor(featureCfg))
		}
		return func(context.Context) {}, nil
	}
	if !gpuOperatorInstallEnabled(featureCfg, cfg) {
		return func(context.Context) {}, nil
	}
	extra := []string{}
	preCleanup := func(context.Context) {}
	switch scenarioName {
	case "gpuOperatorSharedGpu":
		if err := CreateGPUOperatorSharedGPUConfig(ctx, runner, w, cfg, featureCfg); err != nil {
			return func(context.Context) {}, err
		}
		extra = append(extra, "--set", "devicePlugin.config.name=dcgm-exporter-e2e-time-slicing", "--set", "devicePlugin.config.default=any")
	case "gpuOperatorDRA":
		if err := runRequired(ctx, runner, w, "kubectl_label_dra_nodes", kubectl(cfg, "label", "nodes", "--all", "nvidia.com/dra-kubelet-plugin=true", "--overwrite")); err != nil {
			return func(context.Context) {}, err
		}
		preCleanup = func(cleanupCtx context.Context) {
			_ = runRequired(cleanupCtx, runner, w, "kubectl_unlabel_dra_nodes", kubectl(cfg, "label", "nodes", "--all", "nvidia.com/dra-kubelet-plugin-", "--overwrite"))
		}
		extra = append(extra, "--set", "devicePlugin.enabled=false", "--set", "driver.manager.env[0].name=NODE_LABEL_FOR_GPU_POD_EVICTION", "--set", "driver.manager.env[0].value=nvidia.com/dra-kubelet-plugin")
	case "gpuOperatorMig":
		extra = append(extra, "--set", "mig.strategy=mixed")
	}
	if err := InstallGPUOperator(ctx, runner, w, cfg, featureCfg, extra); err != nil {
		preCleanup(context.WithoutCancel(ctx))
		return func(context.Context) {}, err
	}
	cleanup := func(cleanupCtx context.Context) {
		_ = CleanupGPUOperator(cleanupCtx, runner, w, cfg, featureCfg)
		preCleanup(cleanupCtx)
	}
	switch scenarioName {
	case "gpuOperatorDRA":
		draCleanup, err := PrepareDRA(ctx, runner, w, cfg, featureCfg)
		if err != nil {
			cleanup(context.WithoutCancel(ctx))
			return func(context.Context) {}, err
		}
		return func(cleanupCtx context.Context) { draCleanup(cleanupCtx); cleanup(cleanupCtx) }, nil
	case "gpuOperatorSharedGpu":
		if err := WaitForGPUOperatorSharedResources(ctx, runner, cfg, timeoutFor(featureCfg)); err != nil {
			cleanup(context.WithoutCancel(ctx))
			return func(context.Context) {}, err
		}
	case "gpuOperatorMig":
		if err := WaitForGPUOperatorMIGResources(ctx, runner, cfg, timeoutFor(featureCfg)); err != nil {
			cleanup(context.WithoutCancel(ctx))
			return func(context.Context) {}, err
		}
	case "gpuOperatorIPv6":
		if err := EnsureGPUOperatorIPv6Service(ctx, runner, w, cfg, featureCfg); err != nil {
			cleanup(context.WithoutCancel(ctx))
			return func(context.Context) {}, err
		}
	}
	return cleanup, nil
}

// InstallGPUOperator installs the NVIDIA GPU Operator chart.
func InstallGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, extra []string) error {
	version := strings.TrimSpace(featureCfg.Dependencies.GPUOperatorVersion)
	if version == "" {
		return fmt.Errorf("GPU_OPERATOR_VERSION is required")
	}
	timeout := timeoutFor(featureCfg)
	if cfg.LocalK3D() {
		_ = runRequired(ctx, runner, w, "helm_uninstall_device_plugin", e2eexec.Command{Name: "helm", Args: []string{"uninstall", "nvidia-device-plugin", "--namespace", "kube-system", "--ignore-not-found", "--wait", "--timeout", timeout}, Env: kubeEnv(cfg)})
	}
	if err := runRequired(ctx, runner, w, "helm_repo_gpu_operator", e2eexec.Command{Name: "helm", Args: []string{"repo", "add", "nvidia-gpu-operator", "https://helm.ngc.nvidia.com/nvidia", "--force-update"}, Env: kubeEnv(cfg)}); err != nil {
		return err
	}
	if err := runRequired(ctx, runner, w, "helm_repo_update_gpu_operator", e2eexec.Command{Name: "helm", Args: []string{"repo", "update", "nvidia-gpu-operator"}, Env: kubeEnv(cfg)}); err != nil {
		return err
	}
	args := []string{
		"upgrade", "--install", "gpu-operator", "nvidia-gpu-operator/gpu-operator",
		"--namespace", "gpu-operator",
		"--create-namespace",
		"--version", version,
		"--set", "driver.enabled=false",
		"--set", "toolkit.enabled=false",
		"--set", "devicePlugin.enabled=true",
		"--set", "dcgmExporter.enabled=true",
	}
	exporterImageArgs, err := gpuOperatorExporterImageArgs(featureCfg)
	if err != nil {
		return err
	}
	args = append(args, exporterImageArgs...)
	if cfg.LocalK3D() {
		args = append(args, "--set", "dcgmExporter.imagePullPolicy=IfNotPresent")
	}
	args = append(args, gpuOperatorPullSecretArgs(featureCfg)...)
	args = append(args, extra...)
	args = append(args, "--wait", "--timeout", timeout)
	if err := runRequired(ctx, runner, w, "helm_install_gpu_operator", e2eexec.Command{Name: "helm", Args: args, Env: kubeEnv(cfg)}); err != nil {
		return err
	}
	if err := WaitForGPUOperator(ctx, runner, cfg, timeout); err != nil {
		return err
	}
	return WaitForGPUOperatorExporter(ctx, runner, cfg, timeout)
}

// CleanupGPUOperator removes the GPU Operator installed by validation.
func CleanupGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	timeout := timeoutFor(featureCfg)
	_ = runRequired(ctx, runner, w, "helm_uninstall_gpu_operator", e2eexec.Command{Name: "helm", Args: []string{"uninstall", "gpu-operator", "--namespace", "gpu-operator", "--ignore-not-found", "--wait", "--timeout", timeout}, Env: kubeEnv(cfg)})
	_ = runRequired(ctx, runner, w, "kubectl_delete_gpu_operator_namespace", kubectl(cfg, "delete", "namespace", "gpu-operator", "--ignore-not-found=true"))
	if cfg.LocalK3D() {
		local := LocalConfig{
			ClusterName:         cfg.ClusterName,
			Kubeconfig:          cfg.EffectiveKubeconfig(),
			RuntimeClass:        defaultRuntimeClass,
			NodeSelectorKey:     defaultExporterNodeLabelKey,
			NodeSelectorValue:   defaultExporterNodeLabelVal,
			WaitTimeout:         timeout,
			DevicePluginVersion: featureCfg.Dependencies.DevicePluginVersion,
		}
		_ = ResetDevicePlugin(ctx, runner, w, cfg, local)
	}
	return nil
}

// CreateGPUOperatorSharedGPUConfig writes the time-slicing ConfigMap consumed by GPU Operator.
func CreateGPUOperatorSharedGPUConfig(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	replicas := featureCfg.SharedGPUReplicas
	if replicas == "" {
		replicas = "2"
	}
	manifest := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: gpu-operator
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: dcgm-exporter-e2e-time-slicing
  namespace: gpu-operator
data:
  any: |-
    version: v1
    flags:
      migStrategy: none
    sharing:
      timeSlicing:
        renameByDefault: false
        failRequestsGreaterThanOne: true
        resources:
        - name: nvidia.com/gpu
          replicas: %s
`, replicas)
	return runRequired(ctx, runner, w, "kubectl_gpu_operator_shared_config", kubectlWithStdin(cfg, []byte(manifest), "apply", "-f", "-"))
}

// EnsureGPUOperatorIPv6Service creates and verifies a dual-stack service for operator-managed exporter pods.
func EnsureGPUOperatorIPv6Service(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	preflight, err := e2eimage.Parse(featureCfg.BusyboxImage)
	if err != nil {
		return fmt.Errorf("GPU Operator IPv6 preflight image: %w", err)
	}
	preflightImage := preflight.Pull()
	service := text(ctx, runner, kubectl(cfg, "-n", "gpu-operator", "get", "svc", "-o", `jsonpath={range .items[*]}{.metadata.name}{" "}{range .spec.ports[*]}{.port}{" "}{end}{"\n"}{end}`))
	serviceName := ""
	for _, line := range strings.Split(service, "\n") {
		if strings.Contains(line, "9400") && strings.Contains(strings.ToLower(line), "dcgm") && strings.Contains(strings.ToLower(line), "exporter") {
			serviceName = strings.Fields(line)[0]
			break
		}
	}
	if serviceName == "" {
		return errors.New("gpu operator dcgm-exporter service was not found")
	}
	if err := CreateGPUOperatorIPv6Service(ctx, runner, w, cfg, serviceName); err != nil {
		return err
	}
	if err := EnsureManagedNamespace(ctx, runner, w, cfg, cfg.Namespace); err != nil {
		return err
	}
	return WaitForGPUOperatorIPv6Service(ctx, runner, w, cfg, gpuOperatorIPv6ServiceName, timeoutFor(featureCfg), preflightImage)
}

// CreateGPUOperatorIPv6Service creates an e2e-owned dual-stack service for the operator-managed exporter pods.
func CreateGPUOperatorIPv6Service(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, sourceServiceName string) error {
	source := text(ctx, runner, kubectl(cfg, "-n", "gpu-operator", "get", "service", sourceServiceName, "-o", "json"))
	var service struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(source), &service); err != nil {
		return fmt.Errorf("parse GPU Operator dcgm-exporter service selector: %w", err)
	}
	if len(service.Spec.Selector) == 0 {
		return fmt.Errorf("GPU Operator dcgm-exporter service %s has no selector", sourceServiceName)
	}
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      gpuOperatorIPv6ServiceName,
			"namespace": "gpu-operator",
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": managedByValue,
				"app.kubernetes.io/part-of":    "dcgm-exporter",
			},
		},
		"spec": map[string]any{
			"type":           "ClusterIP",
			"ipFamilyPolicy": "RequireDualStack",
			"ipFamilies":     []string{"IPv4", "IPv6"},
			"selector":       service.Spec.Selector,
			"ports": []map[string]any{{
				"name":       "metrics",
				"port":       9400,
				"protocol":   "TCP",
				"targetPort": 9400,
			}},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("render GPU Operator IPv6 service: %w", err)
	}
	return runRequired(ctx, runner, w, "kubectl_gpu_operator_ipv6_service", kubectlWithStdin(cfg, data, "apply", "-f", "-"))
}

// CleanupGPUOperatorIPv6Service removes the e2e-owned dual-stack service from an existing GPU Operator namespace.
func CleanupGPUOperatorIPv6Service(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config) error {
	return runRequired(ctx, runner, w, "kubectl_delete_gpu_operator_ipv6_service", kubectl(cfg, "-n", "gpu-operator", "delete", "service", gpuOperatorIPv6ServiceName, "--ignore-not-found=true"))
}

// WaitForGPUOperatorIPv6Service waits until the operator-managed exporter Service has an IPv6 ClusterIP.
func WaitForGPUOperatorIPv6Service(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, serviceName, timeout, preflightImage string) error {
	deadline := time.Now().Add(parseDuration(timeout, 5*time.Minute))
	var last string
	for {
		last = text(ctx, runner, kubectl(cfg, "-n", "gpu-operator", "get", "service", serviceName, "-o", `jsonpath={.spec.ipFamilyPolicy}{" "}{.spec.ipFamilies[*]}{" "}{.spec.clusterIPs[*]}`))
		if strings.Contains(last, ":") && strings.Contains(last, "IPv6") {
			fields := strings.Fields(last)
			ipv6 := fields[len(fields)-1]
			if err := waitForGPUOperatorIPv6Connectivity(ctx, runner, w, cfg, ipv6, timeout, preflightImage); err != nil {
				return err
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for GPU Operator dcgm-exporter service %s to expose an IPv6 ClusterIP; observed %q", serviceName, strings.TrimSpace(last))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// waitForGPUOperatorIPv6Connectivity curls the IPv6 service endpoint from an in-cluster preflight pod.
func waitForGPUOperatorIPv6Connectivity(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, ipv6, timeout, preflightImage string) error {
	deadline := time.Now().Add(parseDuration(timeout, 5*time.Minute))
	command := fmt.Sprintf("wget -qO- -T 10 'http://[%s]:9400/metrics' >/dev/null", ipv6)
	var last string
	for {
		result := runner.Run(ctx, kubectl(cfg, "-n", "dcgm-exporter", "run", "gpu-operator-ipv6-preflight", "--rm", "-i", "--restart=Never", "--image="+preflightImage, "--command", "--", "sh", "-c", command))
		last = resultOutput(result)
		if result.ExitCode == 0 {
			return nil
		}
		if w != nil {
			fmt.Fprintf(w, "[e2e] WARN GPU Operator IPv6 preflight failed: %s\n", strings.TrimSpace(last))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for GPU Operator IPv6 service connectivity to [%s]:9400; last error: %s", ipv6, strings.TrimSpace(last))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// resultOutput returns command output or the execution error text.
func resultOutput(result e2eexec.Result) string {
	output := strings.TrimSpace(string(result.Stdout) + string(result.Stderr))
	if output == "" && result.Err != nil {
		return result.Err.Error()
	}
	return output
}

// WaitForGPUOperator waits for the GPU Operator ClusterPolicy API to appear.
func WaitForGPUOperator(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	deadline := time.Now().Add(parseDuration(timeout, 5*time.Minute))
	for {
		if gpuOperatorExisting(ctx, runner, cfg) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for GPU Operator ClusterPolicy")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// WaitForGPUOperatorExporter waits for an operator-managed dcgm-exporter pod to run.
func WaitForGPUOperatorExporter(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	deadline := time.Now().Add(parseDuration(timeout, 5*time.Minute))
	for {
		output := text(ctx, runner, kubectl(cfg, "-n", "gpu-operator", "get", "pods", "-o", `jsonpath={range .items[*]}{.metadata.name} {.status.phase} {range .spec.containers[*]}{.image} {end}{"\n"}{end}`))
		lower := strings.ToLower(output)
		if strings.Contains(lower, "dcgm") && strings.Contains(lower, "exporter") && strings.Contains(lower, "running") {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for GPU Operator dcgm-exporter pod")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// WaitForGPUOperatorSharedResources waits for time-slicing resource evidence on nodes.
func WaitForGPUOperatorSharedResources(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	return waitForGPUOperatorNodeEvidence(ctx, runner, cfg, timeout, "shared GPU labels", regexp.MustCompile(`(?i)nvidia[.]com/.*(replicas|shared|timeslicing)|SHARED`))
}

// WaitForGPUOperatorMIGResources waits for MIG resource evidence on nodes.
func WaitForGPUOperatorMIGResources(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	return waitForGPUOperatorNodeEvidence(ctx, runner, cfg, timeout, "MIG labels or resources", regexp.MustCompile(`(?i)nvidia[.]com/.*mig|nvidia[.]com/mig-`))
}

// waitForGPUOperatorNodeEvidence polls node YAML until GPU Operator publishes expected labels or resources.
func waitForGPUOperatorNodeEvidence(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout, description string, pattern *regexp.Regexp) error {
	deadline := time.Now().Add(parseDuration(timeout, 5*time.Minute))
	for {
		output := text(ctx, runner, kubectl(cfg, "get", "nodes", "-o", "yaml"))
		if pattern.MatchString(output) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for GPU Operator %s", description)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// gpuOperatorMode normalizes the configured GPU Operator mode.
func gpuOperatorMode(featureCfg FeatureConfig) string {
	mode := strings.ToLower(strings.TrimSpace(featureCfg.GPUOperator))
	if mode == "" {
		return "false"
	}
	return mode
}

// gpuOperatorInstallEnabled reports whether validation may install GPU Operator.
func gpuOperatorInstallEnabled(featureCfg FeatureConfig, cfg Config) bool {
	switch gpuOperatorMode(featureCfg) {
	case "install":
		return true
	case "auto":
		return cfg.LocalK3D()
	default:
		return false
	}
}

// gpuOperatorExisting reports whether a GPU Operator ClusterPolicy is already present.
func gpuOperatorExisting(ctx context.Context, runner e2eexec.Runner, cfg Config) bool {
	result := runner.Run(ctx, kubectl(cfg, "get", "clusterpolicy", "-o", "name"))
	return result.ExitCode == 0 && strings.TrimSpace(string(result.Stdout)) != ""
}

// gpuOperatorExporterImageArgs translates the selected exporter image into GPU Operator Helm values.
func gpuOperatorExporterImageArgs(featureCfg FeatureConfig) ([]string, error) {
	if strings.TrimSpace(featureCfg.ExporterImage) == "" {
		return nil, nil
	}
	ref, err := e2eimage.Parse(featureCfg.ExporterImage)
	if err != nil {
		return nil, fmt.Errorf("GPU Operator exporter image: %w", err)
	}
	repository := ref.Repository()
	image := ""
	if slash := strings.LastIndex(repository, "/"); slash >= 0 {
		image = repository[slash+1:]
		repository = repository[:slash]
	} else {
		image = repository
		repository = ""
	}
	args := []string{
		"--set", "dcgmExporter.repository=" + repository,
		"--set", "dcgmExporter.image=" + image,
		"--set", "dcgmExporter.imagePullPolicy=Always",
	}
	if ref.Tag() != "" {
		args = append(args, "--set", "dcgmExporter.version="+ref.Tag())
	}
	if ref.Digest() != "" {
		args = append(args, "--set", "dcgmExporter.digest="+ref.Digest())
	}
	return args, nil
}

// gpuOperatorPullSecretArgs applies the same pull secret to GPU Operator-managed image consumers.
func gpuOperatorPullSecretArgs(featureCfg FeatureConfig) []string {
	if featureCfg.ImagePullSecret == "" {
		return nil
	}
	args := []string{}
	for _, component := range []string{"operator", "validator", "devicePlugin", "gfd", "migManager", "dcgm", "dcgmExporter"} {
		args = append(args, "--set", component+".imagePullSecrets[0]="+featureCfg.ImagePullSecret)
	}
	return args
}
