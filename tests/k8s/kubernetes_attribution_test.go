//go:build e2e

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

package k8s

import (
	"context"
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("dcgm-exporter Kubernetes attribution scenarios", func() {
	Context("DCGM exporter with pod labels collection enabled", Ordered, Label("podLabels"), func() {
		var (
			dcgmExpPod   *corev1.Pod
			customLabels = map[string]string{
				"valid_key":       "value-valid",
				"key-with-dashes": "value-dashes",
				"key.with.dots":   "value-dots",
			}
			labelMap        = map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}
			metricsResponse []byte
		)

		AfterAll(func(ctx context.Context) {
			if testContext.noCleanup {
				_, _ = fmt.Fprintln(GinkgoWriter, "Clean up: skipped")
				return
			}
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter helm chart with pod labels enabled", func(ctx context.Context) {
			shouldInstallHelmChart(ctx, helmClient, []string{
				exporterArguments("--kubernetes-enable-pod-labels"),
				"kubernetes.enablePodLabels=true",
				"kubernetes.rbac.create=true",
			})
		})

		It("should create dcgm-exporter pod", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, labelMap)
		})

		It("should ensure that the dcgm-exporter pod is ready", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should create a workload pod with custom labels", func(ctx context.Context) {
			shouldCreateWorkloadPod(ctx, kubeClient, customLabels)
		})

		It("should wait for metrics to be available", func(ctx context.Context) {
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should read metrics from dcgm-exporter", func(ctx context.Context) {
			metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
			Expect(metricsResponse).ShouldNot(BeEmpty(), "Metrics response should not be empty")
		})

		It("should verify metrics contain sanitized pod labels", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
				g.Expect(metricsResponse).ShouldNot(BeEmpty(), "Metrics response should not be empty")
				g.Expect(errors.Join(
					metricsHaveLabelValue(metricsResponse, "valid_key", "value-valid"),
					metricsHaveLabelValue(metricsResponse, "key_with_dashes", "value-dashes"),
					metricsHaveLabelValue(metricsResponse, "key_with_dots", "value-dots"),
				)).Should(Succeed())
			}).WithPolling(pollingIntervalSlow).Within(metricsWaitTimeout).Should(Succeed())
		})
	})

	Context("DCGM exporter emits pod UID attribution", Ordered, Label("podUID"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should include pod_uid when pod UID collection is enabled", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					exporterArguments("--kubernetes-enable-pod-uid"),
					"kubernetes.enablePodUID=true",
					"kubernetes.rbac.create=true",
				},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return errors.Join(
						baselineMetricContractError(metrics, true),
						metricsHaveAnyLabel(metrics, "pod_uid"),
					)
				})
		})
	})

	Context("DCGM exporter applies pod label allowlists", Ordered, Label("podLabelAllowlist"), func() {
		customLabels := map[string]string{
			"valid_key":       "value-valid",
			"blocked_key":     "value-blocked",
			"key-with-dashes": "value-dashes",
		}

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should include allowed labels and suppress blocked labels", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					exporterArguments("--kubernetes-enable-pod-labels"),
					"kubernetes.enablePodLabels=true",
					"kubernetes.rbac.create=true",
					"kubernetes.podLabelAllowlistRegex[0]=^valid_key$",
				},
				nil,
				customLabels,
				func(metrics []byte) error {
					return errors.Join(
						baselineMetricContractError(metrics, true),
						metricsHaveLabelValue(metrics, "valid_key", "value-valid"),
						metricsDoNotHaveAnyLabel(metrics, "blocked_key", "key_with_dashes"),
					)
				})
		})
	})

	Context("DCGM exporter maps Kubernetes GPU IDs by UID", Ordered, Label("kubernetesGpuId"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should keep workload attribution valid when UID mapping is selected", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--kubernetes-gpu-id-type=uid")},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return defaultMetricsContractError(metrics, true)
				})
		})
	})

	Context("DCGM exporter emits DRA attribution", Ordered, Label("dra"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit DRA labels when Kubernetes DRA resources are present", func(ctx context.Context) {
			shouldInstallHelmChartWithJSONValues(ctx, helmClient, []string{"kubernetesDRA.enabled=true"}, nil)
			dcgmExpPod := shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
			shouldCreateDRAWorkload(ctx)
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)

			var metricsResponse []byte
			Eventually(func(g Gomega) {
				metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
				g.Expect(errors.Join(
					metricsHaveAnyLabel(metricsResponse, "dra_driver_name"),
					metricsHaveAnyLabel(metricsResponse, "dra_device_name"),
				)).To(Succeed())
			}).WithTimeout(metricsWaitTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})
	})

	Context("DCGM exporter emits shared GPU attribution", Ordered, Label("sharedGpu"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit vgpu labels when shared GPU resources are present", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--kubernetes-virtual-gpus")},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return errors.Join(
						baselineMetricContractError(metrics, true),
						metricsHaveAnyLabel(metrics, "vgpu"),
					)
				})
		})
	})
})

// shouldCreateDRAWorkload creates a ResourceClaimTemplate-backed GPU workload for DRA attribution.
func shouldCreateDRAWorkload(ctx context.Context) {
	Expect(testContext.cudaWorkloadImage).NotTo(BeEmpty(), "E2E_CUDA_WORKLOAD_IMAGE or -cuda-workload-image is required")
	manifest := fmt.Sprintf(`
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  name: gpu-claim-template
  namespace: %s
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.nvidia.com
---
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    e2eRunID: %s
spec:
  restartPolicy: Never
  resourceClaims:
  - name: gpu
    resourceClaimTemplateName: gpu-claim-template
  containers:
  - name: %s
    image: %s
    command: ["sh", "-c", "nvidia-smi && sleep 120"]
    resources:
      claims:
      - name: gpu
`, testContext.namespace, workloadPodName, testContext.namespace, workloadPodName, runID.String(), workloadContainerName, testContext.cudaWorkloadImage)
	shouldRunKubectlWithInput(ctx, manifest, "apply", "-f", "-")
	Eventually(func(g Gomega) {
		output := shouldRunKubectl(ctx, "-n", testContext.namespace, "get", "pod", workloadPodName, "-o", "jsonpath={.status.phase}")
		g.Expect(output).To(Equal("Running"))
	}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalNormal).Should(Succeed())
}
