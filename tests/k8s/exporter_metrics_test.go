//go:build k8s

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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("dcgm-exporter metric scenarios", func() {
	Context("DCGM exporter is deployed on kubernetes and uses a default helm configuration", Ordered, Label("default"), func() {
		var (
			dcgmExpPod *corev1.Pod
			workload   *corev1.Pod
		)

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter helm chart [default]", func(ctx context.Context) {
			shouldInstallHelmChart(ctx, helmClient, []string{})
		})

		It("should create a workload pod and select its node-local exporter [default]", func(ctx context.Context) {
			workload = shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)
			dcgmExpPod = shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)
		})

		It("should wait for metrics to be available [default]", func(ctx context.Context) {
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		var metricsResponse []byte

		It("should read metrics [default]", func(ctx context.Context) {
			metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should verify metrics [default]", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
				g.Expect(metricsResponse).ShouldNot(BeEmpty())

				g.Expect(defaultCSVMetricContractError(metricsResponse)).Should(Succeed())
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
		})

		It("should match the GPUs nvidia-smi reports [default]", func(ctx context.Context) {
			shouldMatchGPUInventory(ctx, workload, dcgmExpPod)
		})

		It("should name the workload pod on its GPU only, without pod_uid [default]", func(ctx context.Context) {
			want := shouldReadWorkloadAttribution(ctx, workload)
			want.ForbidPodUID = true
			shouldAttributeOnlyWorkloadGPU(ctx, dcgmExpPod, want)
		})
	})

	Context("DCGM exporter is deployed with a custom metrics config matrix", Ordered, Label("configMatrix"), func() {
		var (
			dcgmExpPod      *corev1.Pod
			helmReleaseName string
		)

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter helm chart with default metrics [configMatrix]", func(ctx context.Context) {
			helmReleaseName = shouldInstallHelmChart(ctx, helmClient, []string{})
		})

		It("should create a workload pod and select its node-local exporter [configMatrix]", func(ctx context.Context) {
			workload := shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)
			dcgmExpPod = shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)
		})

		It("should wait for metrics to be available [configMatrix]", func(ctx context.Context) {
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should verify the default metrics contract before upgrade [configMatrix]", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				metricsResponse := shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
				g.Expect(metricsResponse).ShouldNot(BeEmpty())
				g.Expect(baselineMetricContractError(metricsResponse, true)).Should(Succeed())
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
		})

		It("should upgrade custom metrics and restart the exporter pod [configMatrix]", func(ctx context.Context) {
			jsonValues := []string{
				customMetricsJSONValue(
					"DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C).",
					"DCGM_FI_DEV_FB_USED_BYTES, gauge, Framebuffer memory used (in bytes).",
					"DCGM_FI_DRIVER_VERSION, label, Driver Version",
				),
			}
			shouldUpgradeHelmChart(ctx, helmClient, helmReleaseName, nil, jsonValues)

			Expect(kubeClient.DeletePod(ctx, dcgmExpPod.Namespace, dcgmExpPod.Name)).Should(Succeed(),
				"replace the pod so this check does not wait for the kubelet's ConfigMap sync; the defaultChartMounts scenario checks the reload in a running pod")
			dcgmExpPod = shouldCheckIfReplacementPodCreated(ctx, kubeClient, dcgmExporterPodLabels, dcgmExpPod)
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should verify the upgraded custom metrics contract [configMatrix]", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				metricsResponse := shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
				g.Expect(metricsResponse).ShouldNot(BeEmpty())
				g.Expect(customMetricsContractError(metricsResponse)).Should(Succeed())
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
		})
	})

	Context("DCGM exporter loads metrics from Kubernetes ConfigMap data", Ordered, Label("configMapData"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should use --configmap-data as the metrics source", func(ctx context.Context) {
			values := []string{
				"config.sources.kubernetes.pod_labels.enabled=true",
				"config.sources.kubernetes.pod_labels.allow[0]=.*",
				"kubernetes.rbac.create=true",
			}
			jsonValues := []string{customMetricsJSONValue(
				"DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C).",
				"DCGM_FI_DEV_FB_USED_BYTES, gauge, Framebuffer memory used (in bytes).",
				"DCGM_FI_DRIVER_VERSION, label, Driver Version",
			)}

			// The ConfigMap's name holds the Helm release name, which Helm picks at install.
			// The chart creates no Role for the deprecated flag, so the test grants the read.
			releaseName := shouldInstallHelmChartWithJSONValues(ctx, helmClient, values, jsonValues)

			grantName := chartFullName(releaseName) + "-read-cm"
			DeferCleanup(func(ctx context.Context) {
				Expect(kubeClient.DeleteConfigMapReadGrant(ctx, testContext.namespace, grantName)).To(Succeed())
			})
			Expect(kubeClient.GrantConfigMapRead(ctx, testContext.namespace, grantName,
				chartMetricsConfigMapName(releaseName), chartFullName(releaseName))).To(Succeed())

			configMapData := "--configmap-data=" + testContext.namespace + ":" + chartMetricsConfigMapName(releaseName)
			shouldUpgradeHelmChart(ctx, helmClient, releaseName,
				append(values, exporterArguments(configMapData)), jsonValues)

			workload := shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)

			// Only the upgraded pod has the flag, so wait for it: the metrics must come from that pod.
			var dcgmExpPod *corev1.Pod
			Eventually(func(g Gomega) {
				dcgmExpPod = shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)
				exporter := findContainer(dcgmExpPod.Spec.Containers, exporterContainerName)
				g.Expect(exporter).NotTo(BeNil(), "exporter container")
				g.Expect(exporter.Args).To(ContainElement(configMapData))
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())

			Eventually(func(g Gomega) {
				metrics := shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
				g.Expect(metrics).NotTo(BeEmpty())
				g.Expect(customMetricsContractError(metrics)).To(Succeed())
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
		})
	})

	Context("DCGM exporter loads YAML config mounted by Helm", Ordered, Label("yamlConfig"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should use inline YAML metrics from the generated config ConfigMap", func(ctx context.Context) {
			yamlConfig := strings.TrimSpace(`
version: 2
metrics:
  - name: gpu
    every: 1s
    fields:
      - name: DCGM_FI_DEV_GPU_TEMP_CELSIUS
        prometheus_type: gauge
        help: GPU temperature (in C).
      - name: DCGM_FI_DEV_FB_USED_BYTES
        prometheus_type: gauge
        help: Framebuffer memory used (in bytes).
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
`)

			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					"arguments={}",
				},
				[]string{"config=" + configJSON(yamlConfig)},
				nil,
				func(metrics []byte) error {
					omitted, err := missingMetricFamilies(metrics, []string{"DCGM_FI_DEV_BOARD_POWER_WATTS"})
					if err != nil {
						return err
					}
					var defaultMetricErr error
					if len(omitted) == 0 {
						defaultMetricErr = errors.New("YAML config should replace the default DCGM_FI_DEV_BOARD_POWER_WATTS metric")
					}
					return errors.Join(
						metricsHaveFamilyName(metrics, "DCGM_FI_DEV_GPU_TEMP_CELSIUS"),
						metricsHaveFamilyName(metrics, "DCGM_FI_DEV_FB_USED_BYTES"),
						defaultMetricErr,
					)
				})
		})
	})

	Context("DCGM exporter emits legacy Kubernetes namespace labels", Ordered, Label("oldNamespace"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should use old pod_name, pod_namespace, and container_name labels", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					exporterArguments("--use-old-namespace"),
					"config.sources.kubernetes.pod_labels.enabled=true",
					"config.sources.kubernetes.pod_labels.allow[0]=.*",
					"kubernetes.rbac.create=true",
				},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return errors.Join(
						legacyNamespaceMetricContractError(metrics),
						metricsHaveLabelValue(metrics, "pod_name", workloadPodName),
						metricsHaveLabelValue(metrics, "pod_namespace", testContext.namespace),
						metricsHaveLabelValue(metrics, "container_name", workloadContainerName),
						metricsDoNotHaveAnyLabel(metrics, "pod", "namespace", "container", "UUID", "Hostname"),
					)
				})
		})
	})

	Context("DCGM exporter emits clock event counters", Ordered, Label("clockEventsCounters"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit exporter-owned clock event counters", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				nil,
				[]string{customMetricsJSONValue(
					"DCGM_FI_DEV_GPU_TEMP_CELSIUS, gauge, GPU temperature (in C).",
					"DCGM_EXP_CLOCK_EVENTS_COUNT, gauge, Clock events observed during the configured time window.",
					"DCGM_EXP_CLOCK_EVENTS_TOTAL, counter, Total clock events observed since exporter start.",
				)},
				testRunLabels,
				func(metrics []byte) error {
					return errors.Join(
						metricsHaveFamilyName(metrics, "DCGM_EXP_CLOCK_EVENTS_COUNT"),
						metricsHaveNonNegativeFamilyIfPresent(metrics, "DCGM_EXP_CLOCK_EVENTS_TOTAL"),
					)
				})
		})
	})
})
