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
	"os"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/e2e/config"
	e2eexec "github.com/NVIDIA/dcgm-exporter/internal/e2e/exec"
	e2eimage "github.com/NVIDIA/dcgm-exporter/internal/e2e/image"
)

const (
	gpuOperatorNamespace             = "gpu-operator"
	gpuOperatorMainMetricsConfigName = "dcgm-exporter-e2e-metrics"
	gpuOperatorMainMetricsConfigKey  = "dcgm-metrics.csv"
	DefaultGPUOperatorMainDCGMImage  = "nvcr.io/nvidia/cloud-native/dcgm:4.7.0-ubuntu24.04"
	// gpuOperatorCleanupTimeout bounds a cleanup step made of kubectl calls only.
	gpuOperatorCleanupTimeout = 90 * time.Second
)

// PrepareGPUOperator installs or validates GPU Operator resources for a selected scenario.
func PrepareGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (func(context.Context) error, error) {
	if gpuOperatorMainScenario(scenarioName) {
		return prepareGPUOperatorMain(ctx, runner, w, cfg, featureCfg, scenarioName)
	}
	existing, err := gpuOperatorExisting(ctx, runner, cfg)
	if err != nil {
		return noLocalCleanup, err
	}
	if existing {
		return prepareExistingGPUOperator(ctx, runner, w, cfg, featureCfg, scenarioName)
	}
	if !gpuOperatorInstallEnabled(featureCfg, cfg) {
		return noLocalCleanup, nil
	}
	return prepareNewGPUOperator(ctx, runner, w, cfg, featureCfg, scenarioName)
}

// prepareExistingGPUOperator runs the selected setup or readiness check when GPU Operator already exists.
// A setup error comes with the cleanup for whatever the setup changed.
func prepareExistingGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (func(context.Context) error, error) {
	switch scenarioName {
	case "gpuOperatorDRA":
		return PrepareDRA(ctx, runner, w, cfg, featureCfg)
	case "gpuOperatorIPv6":
		cleanup := func(cleanupCtx context.Context) error {
			return CleanupGPUOperatorIPv6Service(cleanupCtx, runner, w, cfg)
		}
		return cleanup, EnsureGPUOperatorIPv6Service(ctx, runner, w, cfg, featureCfg)
	case "gpuOperatorMig":
		return noLocalCleanup, WaitForGPUOperatorMIGResources(ctx, runner, cfg, timeoutFor(featureCfg))
	default:
		return noLocalCleanup, nil
	}
}

// prepareNewGPUOperator installs GPU Operator and prepares scenario resources. It returns the cleanup for
// everything it changed, with a setup error too, so the caller can collect diagnostics before removing it.
func prepareNewGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (func(context.Context) error, error) {
	uninstall, err := installGPUOperatorScenario(ctx, runner, w, cfg, featureCfg, scenarioName)
	if err != nil {
		return uninstall, err
	}

	// Scenario resources depend on the Operator, so they are removed first.
	scenarioCleanup, err := prepareInstalledGPUOperatorScenario(ctx, runner, w, cfg, featureCfg, scenarioName)
	return joinCleanups(boundedCleanup(gpuOperatorHelmCleanupLimit(featureCfg), scenarioCleanup), uninstall), err
}

// installGPUOperatorScenario prepares the owned namespace, applies scenario overrides, and installs the chart.
// It returns the cleanup for what it changed, with a setup error too.
func installGPUOperatorScenario(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (func(context.Context) error, error) {
	removeNamespace, err := prepareGPUOperatorNamespace(ctx, runner, w, cfg, featureCfg)
	if err != nil {
		return removeNamespace, err
	}

	extra, restoreNodes, err := gpuOperatorInstallOverrides(ctx, runner, w, cfg, featureCfg, scenarioName)
	restoreNodes = boundedCleanup(gpuOperatorCleanupTimeout, restoreNodes)
	if err != nil {
		return joinCleanups(removeNamespace, restoreNodes), err
	}

	// The Operator depends on the node state restoreNodes restores, so it is removed first.
	uninstall := joinCleanups(cleanupGPUOperator(runner, w, cfg, featureCfg), restoreNodes)
	return uninstall, InstallGPUOperator(ctx, runner, w, cfg, featureCfg, scenarioName, extra)
}

// gpuOperatorInstallOverrides returns scenario-specific Helm flags and the restoration of any node state
// it changed. The restoration is returned with an error too.
func gpuOperatorInstallOverrides(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) ([]string, func(context.Context) error, error) {
	switch scenarioName {
	case "gpuOperatorSharedGpu":
		if err := CreateGPUOperatorSharedGPUConfig(ctx, runner, w, cfg, featureCfg); err != nil {
			return nil, noLocalCleanup, err
		}
		return []string{"--set", "devicePlugin.config.name=dcgm-exporter-e2e-time-slicing", "--set", "devicePlugin.config.default=any"}, noLocalCleanup, nil
	case "gpuOperatorDRA":
		// A driver install needs a version; check it first so a missing one leaves the nodes untouched.
		if err := requireDRADriverVersionIfInstalling(ctx, runner, cfg, featureCfg); err != nil {
			return nil, noLocalCleanup, err
		}
		// The Operator's driver manager reads the label, so it is set before the install.
		unlabel, err := labelDRANodes(ctx, runner, w, cfg)
		if err != nil {
			return nil, unlabel, err
		}
		return []string{"--set", "devicePlugin.enabled=false", "--set", "driver.manager.env[0].name=NODE_LABEL_FOR_GPU_POD_EVICTION", "--set", "driver.manager.env[0].value=" + draNodePluginLabelKey}, unlabel, nil
	case "gpuOperatorMig":
		return []string{"--set", "mig.strategy=mixed"}, noLocalCleanup, nil
	default:
		return nil, noLocalCleanup, nil
	}
}

// prepareInstalledGPUOperatorScenario waits for scenario resources or configures them after GPU Operator installation.
// A setup error comes with the cleanup for whatever the setup changed.
func prepareInstalledGPUOperatorScenario(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (func(context.Context) error, error) {
	switch scenarioName {
	case "gpuOperatorDRA":
		return prepareDRADriver(ctx, runner, w, cfg, featureCfg)
	case "gpuOperatorSharedGpu":
		return noLocalCleanup, WaitForGPUOperatorSharedResources(ctx, runner, cfg, timeoutFor(featureCfg))
	case "gpuOperatorMig":
		return noLocalCleanup, WaitForGPUOperatorMIGResources(ctx, runner, cfg, timeoutFor(featureCfg))
	case "gpuOperatorIPv6":
		return noLocalCleanup, EnsureGPUOperatorIPv6Service(ctx, runner, w, cfg, featureCfg)
	default:
		return noLocalCleanup, nil
	}
}

// prepareGPUOperatorNamespace creates the run-owned namespace before installing Operator resources.
// It returns the cleanup that deletes a namespace this run created, with a setup error too; a namespace
// this run reused is left in place.
func prepareGPUOperatorNamespace(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) (func(context.Context) error, error) {
	if featureCfg.RegistryDockerConfigJSONPath != "" && featureCfg.ImagePullSecret == "" {
		return noLocalCleanup, fmt.Errorf("GPU Operator registry credentials require an image pull secret name")
	}

	created, err := ensureManagedNamespace(ctx, runner, w, cfg, gpuOperatorNamespace)
	removeNamespace := noLocalCleanup
	if created {
		removeNamespace = boundedCleanup(gpuOperatorCleanupTimeout, func(cleanupCtx context.Context) error {
			return cleanupManagedNamespace(cleanupCtx, runner, w, cfg, gpuOperatorNamespace, "kubectl_delete_gpu_operator_namespace")
		})
	}
	if err != nil || featureCfg.RegistryDockerConfigJSONPath == "" {
		return removeNamespace, err
	}

	if err := EnsureImagePullSecret(ctx, runner, w, cfg, gpuOperatorNamespace, featureCfg.ImagePullSecret, featureCfg.RegistryDockerConfigJSONPath); err != nil {
		return removeNamespace, fmt.Errorf("prepare GPU Operator image pull secret: %w", err)
	}
	return removeNamespace, nil
}

// gpuOperatorHelmCleanupLimit returns the time limit for a cleanup step that waits on one Helm release:
// the Helm wait timeout plus gpuOperatorCleanupTimeout for the kubectl calls around it.
func gpuOperatorHelmCleanupLimit(featureCfg FeatureConfig) time.Duration {
	return parseDuration(timeoutFor(featureCfg), parseDuration(defaultK3dWaitTimeout, 0)) + gpuOperatorCleanupTimeout
}

// boundedCleanup returns cleanup with its own time limit. The limit starts when the cleanup runs and
// ignores the caller's deadline and cancellation, so a slow earlier step cannot cut a later one short.
func boundedCleanup(limit time.Duration, cleanup func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
		defer cancel()
		return cleanup(cleanupCtx)
	}
}

// resetGPUOperatorDevicePlugin restores device plugin settings for locally managed k3d clusters.
func resetGPUOperatorDevicePlugin(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	if !cfg.LocalK3D() {
		return nil
	}
	timeout := timeoutFor(featureCfg)
	err := ResetDevicePlugin(ctx, runner, w, cfg, LocalConfig{
		ClusterName:         cfg.ClusterName,
		Kubeconfig:          cfg.EffectiveKubeconfig(),
		RuntimeClass:        defaultRuntimeClass,
		NodeSelectorKey:     defaultExporterNodeLabelKey,
		NodeSelectorValue:   defaultExporterNodeLabelVal,
		WaitTimeout:         timeout,
		DevicePluginVersion: featureCfg.Dependencies.DevicePluginVersion,
	})
	if err != nil {
		return fmt.Errorf("reset the local NVIDIA device plugin: %w", err)
	}
	return nil
}

// prepareGPUOperatorMain installs a local GPU Operator main chart for one isolated scenario group.
// It returns the cleanup for what it changed, with a setup error too.
func prepareGPUOperatorMain(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (func(context.Context) error, error) {
	if err := validateGPUOperatorMainInstall(ctx, runner, cfg, featureCfg); err != nil {
		return noLocalCleanup, err
	}

	removeNamespace, err := prepareGPUOperatorNamespace(ctx, runner, w, cfg, featureCfg)
	if err != nil {
		return removeNamespace, err
	}

	extra, err := gpuOperatorMainInstallOverrides(ctx, runner, w, cfg, featureCfg, scenarioName)
	if err != nil {
		return removeNamespace, err
	}

	return cleanupGPUOperator(runner, w, cfg, featureCfg), InstallGPUOperator(ctx, runner, w, cfg, featureCfg, scenarioName, extra)
}

// gpuOperatorMainInstallOverrides creates the scenario's metrics ConfigMap when it needs one and returns
// the scenario's Helm flags for the local main chart.
func gpuOperatorMainInstallOverrides(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) ([]string, error) {
	extra := []string{}
	if scenarioName == "gpuOperatorMainEmbedded" || scenarioName == "gpuOperatorMainRemote" {
		if err := createGPUOperatorMainMetricsConfig(ctx, runner, w, cfg, scenarioName); err != nil {
			return nil, err
		}
		extra = append(extra, "--set", "dcgmExporter.config.name="+gpuOperatorMainMetricsConfigName)
	}

	switch scenarioName {
	case "gpuOperatorMainEmbedded":
		return append(extra,
			"--set", "dcgmExporter.enablePodLabels=true",
			"--set", "dcgmExporter.enablePodUID=true",
			"--set", "dcgmExporter.podLabelAllowlistRegex[0]=^e2e_allowed$",
		), nil
	case "gpuOperatorMainRemote":
		args, err := gpuOperatorMainImageArgs("dcgm", gpuOperatorMainDCGMImageFor(featureCfg))
		if err != nil {
			return nil, err
		}
		extra = append(extra, "--set", "dcgm.enabled=true")
		return append(extra, args...), nil
	case "gpuOperatorMainDRA":
		args, err := gpuOperatorMainImageArgs("dcgm", gpuOperatorMainDCGMImageFor(featureCfg))
		if err != nil {
			return nil, err
		}
		extra = append(extra,
			"--set", "clusterPolicy.deployCR=false",
			"--set", "gpuCluster.deployCR=true",
			"--set", "dcgm.enabled=true",
		)
		return append(extra, args...), nil
	default:
		return extra, nil
	}
}

// validateGPUOperatorMainInstall rejects existing Operator resources before a scenario creates its ConfigMap.
func validateGPUOperatorMainInstall(ctx context.Context, runner e2eexec.Runner, cfg Config, featureCfg FeatureConfig) error {
	if featureCfg.GPUOperator != config.GPUOperatorInstall {
		return fmt.Errorf("GPU Operator main scenarios require E2E_GPU_OPERATOR=install")
	}
	chart := strings.TrimSpace(featureCfg.GPUOperatorChart)
	if chart == "" {
		return fmt.Errorf("GPU Operator main scenarios require E2E_GPU_OPERATOR_CHART")
	}
	info, err := os.Stat(chart)
	if err != nil {
		return fmt.Errorf("GPU Operator main chart %q: %w", chart, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("GPU Operator main chart %q is not a directory", chart)
	}
	namespace := runner.Run(ctx, kubectl(cfg, "get", "namespace", gpuOperatorNamespace, "-o", "name"))
	if namespace.ExitCode == 0 && strings.TrimSpace(string(namespace.Stdout)) != "" {
		return fmt.Errorf("GPU Operator main scenarios require an empty cluster; found namespace %s", strings.TrimSpace(string(namespace.Stdout)))
	}
	if namespace.ExitCode != 0 && !gpuOperatorNamespaceMissing(namespace) {
		return fmt.Errorf("check existing GPU Operator namespace: %s", resultOutput(namespace))
	}
	for _, resource := range []string{"clusterpolicy", "gpucluster"} {
		result := runner.Run(ctx, kubectl(cfg, "get", resource, "-o", "name"))
		if result.ExitCode == 0 && strings.TrimSpace(string(result.Stdout)) != "" {
			return fmt.Errorf("GPU Operator main scenarios require an empty cluster; found %s %s", resource, strings.TrimSpace(string(result.Stdout)))
		}
		if result.ExitCode != 0 && !gpuOperatorResourceTypeMissing(result) {
			return fmt.Errorf("check existing GPU Operator %s: %s", resource, resultOutput(result))
		}
	}
	return nil
}

func gpuOperatorNamespaceMissing(result e2eexec.Result) bool {
	output := strings.ToLower(resultOutput(result))
	return strings.Contains(output, "notfound") || strings.Contains(output, "not found")
}

// gpuOperatorResourceTypeMissing accepts a missing CRD because the local chart installs its CRD.
func gpuOperatorResourceTypeMissing(result e2eexec.Result) bool {
	output := strings.ToLower(resultOutput(result))
	return strings.Contains(output, "the server doesn't have a resource type") ||
		strings.Contains(output, "the server could not find the requested resource")
}

// InstallGPUOperator installs the NVIDIA GPU Operator chart.
func InstallGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string, extra []string) error {
	timeout := timeoutFor(featureCfg)
	if cfg.LocalK3D() {
		if err := uninstallHelmRelease(ctx, runner, w, cfg, "helm_uninstall_device_plugin", devicePluginReleaseName, kubeSystemNamespace, timeout); err != nil {
			return fmt.Errorf("remove the NVIDIA device plugin before installing GPU Operator: %w", err)
		}
	}
	chart, versionArgs, err := gpuOperatorChartAndVersionArgs(ctx, runner, w, cfg, featureCfg, scenarioName)
	if err != nil {
		return err
	}
	args, err := gpuOperatorInstallArgs(cfg, featureCfg, scenarioName, chart, versionArgs, extra, timeout)
	if err != nil {
		return err
	}
	if err := runHelm(ctx, runner, w, cfg, "helm_install_gpu_operator", args); err != nil {
		return err
	}
	return waitForGPUOperatorInstall(ctx, runner, cfg, scenarioName, timeout)
}

// gpuOperatorChartAndVersionArgs selects the local main chart or the configured repository chart and version.
func gpuOperatorChartAndVersionArgs(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig, scenarioName string) (string, []string, error) {
	if gpuOperatorMainScenario(scenarioName) {
		return featureCfg.GPUOperatorChart, nil, nil
	}
	version := strings.TrimSpace(featureCfg.Dependencies.GPUOperatorVersion)
	if version == "" {
		return "", nil, fmt.Errorf("GPU_OPERATOR_VERSION is required")
	}
	if err := runHelm(ctx, runner, w, cfg, "helm_repo_gpu_operator", []string{"repo", "add", "nvidia-gpu-operator", "https://helm.ngc.nvidia.com/nvidia", "--force-update"}); err != nil {
		return "", nil, err
	}
	if err := runHelm(ctx, runner, w, cfg, "helm_repo_update_gpu_operator", []string{"repo", "update", "nvidia-gpu-operator"}); err != nil {
		return "", nil, err
	}
	return "nvidia-gpu-operator/gpu-operator", []string{"--version", version}, nil
}

// gpuOperatorInstallArgs assembles Helm flags for images, pull secrets, scenario overrides, and the install timeout.
func gpuOperatorInstallArgs(cfg Config, featureCfg FeatureConfig, scenarioName, chart string, versionArgs, extra []string, timeout string) ([]string, error) {
	mainScenario := gpuOperatorMainScenario(scenarioName)
	args := []string{
		"upgrade", "--install", gpuOperatorNamespace, chart,
		"--namespace", gpuOperatorNamespace,
		"--create-namespace",
		"--set", "driver.enabled=false",
		"--set", "toolkit.enabled=false",
		"--set", "devicePlugin.enabled=true",
		"--set", "dcgmExporter.enabled=true",
	}
	exporterImageArgs, err := gpuOperatorExporterImageArgs(featureCfg)
	if err != nil {
		return nil, err
	}
	args = append(args, exporterImageArgs...)
	if mainScenario && strings.TrimSpace(featureCfg.GPUOperatorImage) != "" {
		controllerArgs, err := gpuOperatorMainImageArgs("operator", featureCfg.GPUOperatorImage)
		if err != nil {
			return nil, err
		}
		args = append(args, controllerArgs...)
		validatorArgs, err := gpuOperatorMainImageArgs("validator", featureCfg.GPUOperatorImage)
		if err != nil {
			return nil, err
		}
		args = append(args, validatorArgs...)
	}
	if cfg.LocalK3D() {
		args = append(args, "--set", "dcgmExporter.imagePullPolicy=IfNotPresent")
	}
	args = append(args, versionArgs...)
	args = append(args, gpuOperatorPullSecretArgs(featureCfg)...)
	args = append(args, extra...)
	args = append(args, "--wait", "--timeout", timeout)
	return args, nil
}

// waitForGPUOperatorInstall waits for GPU Operator readiness and the exporter, plus DRA resources for the main DRA scenario.
func waitForGPUOperatorInstall(ctx context.Context, runner e2eexec.Runner, cfg Config, scenarioName, timeout string) error {
	mainDRA := scenarioName == "gpuOperatorMainDRA"
	if mainDRA {
		if err := waitForGPUOperatorGPUCluster(ctx, runner, cfg, timeout); err != nil {
			return err
		}
	} else if err := WaitForGPUOperator(ctx, runner, cfg, timeout); err != nil {
		return err
	}
	if err := WaitForGPUOperatorExporter(ctx, runner, cfg, timeout); err != nil {
		return err
	}
	if mainDRA {
		return WaitForDRAResources(ctx, runner, cfg, timeout)
	}
	return nil
}

// CleanupGPUOperator removes the Operator release and namespace when this run owns the namespace, then
// restores the local device plugin. It attempts every step and reports all failures.
func CleanupGPUOperator(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	return errors.Join(
		cleanupOwnedRelease(ctx, runner, w, cfg, "gpu_operator", gpuOperatorNamespace, gpuOperatorNamespace, timeoutFor(featureCfg)),
		resetGPUOperatorDevicePlugin(ctx, runner, w, cfg, featureCfg),
	)
}

// cleanupGPUOperator returns CleanupGPUOperator as a setup cleanup. Its limit covers two Helm waits: the
// release uninstall and the local device plugin reset.
func cleanupGPUOperator(runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) func(context.Context) error {
	return boundedCleanup(2*gpuOperatorHelmCleanupLimit(featureCfg), func(cleanupCtx context.Context) error {
		return CleanupGPUOperator(cleanupCtx, runner, w, cfg, featureCfg)
	})
}

// CreateGPUOperatorSharedGPUConfig writes the time-slicing ConfigMap consumed by GPU Operator.
func CreateGPUOperatorSharedGPUConfig(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	replicas := featureCfg.SharedGPUReplicas
	if replicas == "" {
		replicas = "2"
	}
	manifest := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: dcgm-exporter-e2e-time-slicing
  namespace: %s
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
`, gpuOperatorNamespace, replicas)
	return runRequired(ctx, runner, w, "kubectl_gpu_operator_shared_config", kubectlWithStdin(cfg, []byte(manifest), "apply", "-f", "-"))
}

// createGPUOperatorMainMetricsConfig creates the scenario-specific metrics ConfigMap before Helm starts reconciliation.
func createGPUOperatorMainMetricsConfig(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, scenarioName string) error {
	metrics := []string{
		"DCGM_FI_DEV_GPU_UTIL_RATIO, gauge, GPU utilization ratio.",
	}
	switch scenarioName {
	case "gpuOperatorMainEmbedded":
		// The embedded test needs only the ratio metric and pod metadata.
	case "gpuOperatorMainRemote":
		metrics = append(metrics,
			"DCGM_EXP_FIELD_POLICY_ENABLED, gauge, Whether a DCGM field policy is enabled.",
			"DCGM_EXP_FIELD_POLICY_CURRENT_VALUE, gauge, Current value recorded by a DCGM field policy.",
			"DCGM_EXP_FIELD_POLICY_THRESHOLD, gauge, Threshold configured for a DCGM field policy.",
			"DCGM_EXP_FIELD_POLICY_VIOLATION_COUNT, gauge, Number of violations recorded by a DCGM field policy.",
			"DCGM_EXP_FIELD_POLICY_LAST_TRIGGER_TIMESTAMP_SECONDS, gauge, Most recent DCGM field policy violation time in seconds since the Unix epoch.",
		)
	default:
		return fmt.Errorf("GPU Operator main scenario %q does not use a custom metrics ConfigMap", scenarioName)
	}
	manifest := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  %s: |-
    %s
`, gpuOperatorMainMetricsConfigName, gpuOperatorNamespace, gpuOperatorMainMetricsConfigKey, strings.Join(metrics, "\n    "))
	return runRequired(ctx, runner, w, "kubectl_gpu_operator_main_metrics_config", kubectlWithStdin(cfg, []byte(manifest), "apply", "-f", "-"))
}

// EnsureGPUOperatorIPv6Service creates and verifies a dual-stack service for operator-managed exporter pods.
func EnsureGPUOperatorIPv6Service(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, featureCfg FeatureConfig) error {
	preflight, err := e2eimage.Parse(featureCfg.BusyboxImage)
	if err != nil {
		return fmt.Errorf("GPU Operator IPv6 preflight image: %w", err)
	}
	preflightImage := preflight.Pull()
	service := text(ctx, runner, kubectl(cfg, "-n", gpuOperatorNamespace, "get", "svc", "-o", `jsonpath={range .items[*]}{.metadata.name}{" "}{range .spec.ports[*]}{.port}{" "}{end}{"\n"}{end}`))
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
	source := text(ctx, runner, kubectl(cfg, "-n", gpuOperatorNamespace, "get", "service", sourceServiceName, "-o", "json"))
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
			"namespace": gpuOperatorNamespace,
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
	return runRequired(ctx, runner, w, "kubectl_delete_gpu_operator_ipv6_service", kubectl(cfg, "-n", gpuOperatorNamespace, "delete", "service", gpuOperatorIPv6ServiceName, "--ignore-not-found=true"))
}

// WaitForGPUOperatorIPv6Service waits until the operator-managed exporter Service has an IPv6 ClusterIP.
func WaitForGPUOperatorIPv6Service(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, serviceName, timeout, preflightImage string) error {
	var last string
	exposesIPv6 := func() bool {
		last = text(ctx, runner, kubectl(cfg, "-n", gpuOperatorNamespace, "get", "service", serviceName, "-o", `jsonpath={.spec.ipFamilyPolicy}{" "}{.spec.ipFamilies[*]}{" "}{.spec.clusterIPs[*]}`))
		return strings.Contains(last, ":") && strings.Contains(last, "IPv6")
	}
	err := cfg.poll(ctx, timeout, exposesIPv6, func() error {
		return fmt.Errorf("timed out waiting for GPU Operator dcgm-exporter service %s to expose an IPv6 ClusterIP; observed %q", serviceName, strings.TrimSpace(last))
	})
	if err != nil {
		return err
	}

	fields := strings.Fields(last)
	return waitForGPUOperatorIPv6Connectivity(ctx, runner, w, cfg, fields[len(fields)-1], timeout, preflightImage)
}

// waitForGPUOperatorIPv6Connectivity curls the IPv6 service endpoint from an in-cluster preflight pod.
func waitForGPUOperatorIPv6Connectivity(ctx context.Context, runner e2eexec.Runner, w io.Writer, cfg Config, ipv6, timeout, preflightImage string) error {
	command := fmt.Sprintf("wget -qO- -T 10 'http://[%s]:9400/metrics' >/dev/null", ipv6)
	var last string
	connected := func() bool {
		result := runner.Run(ctx, kubectl(cfg, "-n", cfg.Namespace, "run", "gpu-operator-ipv6-preflight", "--rm", "-i", "--restart=Never", "--image="+preflightImage, "--command", "--", "sh", "-c", command))
		last = resultOutput(result)
		if result.ExitCode == 0 {
			return true
		}
		if w != nil {
			fmt.Fprintf(w, "[e2e] WARN GPU Operator IPv6 preflight failed: %s\n", strings.TrimSpace(last))
		}
		return false
	}
	return cfg.poll(ctx, timeout, connected, func() error {
		return fmt.Errorf("timed out waiting for GPU Operator IPv6 service connectivity to [%s]:9400; last error: %s", ipv6, strings.TrimSpace(last))
	})
}

// resultOutput returns command output or the execution error text.
func resultOutput(result e2eexec.Result) string {
	output := strings.TrimSpace(string(result.Stdout) + string(result.Stderr))
	if output == "" && result.Err != nil {
		return result.Err.Error()
	}
	return output
}

// WaitForGPUOperator waits for a GPU Operator ClusterPolicy to appear.
// A timeout reports the last lookup failure, if any.
func WaitForGPUOperator(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	var lookupErr error
	existing := func() bool {
		var found bool
		found, lookupErr = gpuOperatorExisting(ctx, runner, cfg)
		return found
	}
	return cfg.poll(ctx, timeout, existing, func() error {
		return errors.Join(errors.New("timed out waiting for GPU Operator ClusterPolicy"), lookupErr)
	})
}

// waitForGPUOperatorGPUCluster waits for the DRA GPUCluster resource created by the local main chart.
func waitForGPUOperatorGPUCluster(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	present := func() bool {
		result := runner.Run(ctx, kubectl(cfg, "get", "gpucluster", "-o", "name"))
		return result.ExitCode == 0 && strings.TrimSpace(string(result.Stdout)) != ""
	}
	return cfg.poll(ctx, timeout, present, func() error {
		return errors.New("timed out waiting for GPU Operator GPUCluster")
	})
}

// WaitForGPUOperatorExporter waits for an operator-managed dcgm-exporter pod to be ready.
func WaitForGPUOperatorExporter(ctx context.Context, runner e2eexec.Runner, cfg Config, timeout string) error {
	ready := func() bool {
		result := runner.Run(ctx, kubectl(cfg, "-n", gpuOperatorNamespace, "get", "pods", "-o", "json"))
		return result.ExitCode == 0 && gpuOperatorExporterReady(result.Stdout)
	}
	return cfg.poll(ctx, timeout, ready, func() error {
		return errors.New("timed out waiting for ready GPU Operator dcgm-exporter pod")
	})
}

func gpuOperatorExporterReady(output []byte) bool {
	var pods corev1.PodList
	if err := json.Unmarshal(output, &pods); err != nil {
		return false
	}
	for _, pod := range pods.Items {
		name := strings.ToLower(pod.Name)
		if strings.Contains(name, "dcgm") && strings.Contains(name, "exporter") &&
			pod.Status.Phase == corev1.PodRunning && podReady(pod) {
			return true
		}
	}
	return false
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
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
	published := func() bool {
		return pattern.MatchString(text(ctx, runner, kubectl(cfg, "get", "nodes", "-o", "yaml")))
	}
	return cfg.poll(ctx, timeout, published, func() error {
		return fmt.Errorf("timed out waiting for GPU Operator %s", description)
	})
}

// gpuOperatorInstallEnabled reports whether validation may install GPU Operator.
func gpuOperatorInstallEnabled(featureCfg FeatureConfig, cfg Config) bool {
	return featureCfg.GPUOperator.InstallOn(cfg.LocalK3D())
}

// gpuOperatorExisting reports whether a GPU Operator ClusterPolicy is already present.
// A missing ClusterPolicy resource type means no Operator; any other lookup failure is an error,
// so setup never installs over an Operator it could not see.
func gpuOperatorExisting(ctx context.Context, runner e2eexec.Runner, cfg Config) (bool, error) {
	command := kubectl(cfg, "get", "clusterpolicy", "-o", "name")
	result := runner.Run(ctx, command)
	if result.ExitCode == 0 {
		return strings.TrimSpace(string(result.Stdout)) != "", nil
	}
	if gpuOperatorResourceTypeMissing(result) {
		return false, nil
	}
	return false, fmt.Errorf("check for an existing GPU Operator ClusterPolicy: %w", resultError(commandString(command), result))
}

// gpuOperatorMainScenario identifies a scenario that installs a local GPU Operator main chart.
func gpuOperatorMainScenario(scenarioName string) bool {
	switch scenarioName {
	case "gpuOperatorMainEmbedded", "gpuOperatorMainRemote", "gpuOperatorMainDRA":
		return true
	default:
		return false
	}
}

// gpuOperatorMainDCGMImageFor returns the required DCGM 4.7 image unless the e2e caller supplies an override.
func gpuOperatorMainDCGMImageFor(featureCfg FeatureConfig) string {
	if image := strings.TrimSpace(featureCfg.GPUOperatorDCGMImage); image != "" {
		return image
	}
	return DefaultGPUOperatorMainDCGMImage
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
	if ref.Digest() != "" && ref.Tag() == "" {
		return nil, fmt.Errorf(
			"GPU Operator exporter image %q is digest-only; GPU Operator %s requires a tag-bearing digest reference such as repository/image:tag@%s",
			featureCfg.ExporterImage,
			featureCfg.Dependencies.GPUOperatorVersion,
			ref.Digest(),
		)
	}
	if ref.Tag() != "" {
		version := ref.Tag()
		if ref.Digest() != "" {
			version += "@" + ref.Digest()
		}
		args = append(args, "--set", "dcgmExporter.version="+version)
	}
	return args, nil
}

// gpuOperatorMainImageArgs translates tagged images and DCGM digest images to main-chart repository, image, and version values.
func gpuOperatorMainImageArgs(component, value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("GPU Operator main %s image is required", component)
	}
	ref, err := e2eimage.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("GPU Operator main %s image: %w", component, err)
	}
	if ref.Digest() != "" && component != "dcgm" {
		return nil, fmt.Errorf("GPU Operator main chart cannot preserve a digest for %s image %q", component, value)
	}
	if ref.Digest() != "" && !strings.HasPrefix(ref.Digest(), "sha256:") {
		return nil, fmt.Errorf("GPU Operator main DCGM image %q must use a sha256 digest", value)
	}
	repository := ref.Repository()
	slash := strings.LastIndex(repository, "/")
	if slash <= 0 || slash == len(repository)-1 {
		return nil, fmt.Errorf("GPU Operator main %s image %q must include a repository and image name", component, value)
	}
	version := ref.Tag()
	if ref.Digest() != "" {
		// The DCGM ImagePath contract renders a sha256 version as repository/image@digest.
		version = ref.Digest()
	}
	return []string{
		"--set", component + ".repository=" + repository[:slash],
		"--set", component + ".image=" + repository[slash+1:],
		"--set", component + ".version=" + version,
	}, nil
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
