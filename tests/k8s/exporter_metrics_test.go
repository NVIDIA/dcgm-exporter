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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("dcgm-exporter metric scenarios", func() {
	Context("DCGM exporter is deployed on kubernetes and uses a default helm configuration", Ordered, Label("default"), func() {
		var dcgmExpPod *corev1.Pod

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter helm chart [default]", func(ctx context.Context) {
			shouldInstallHelmChart(ctx, helmClient, []string{})
		})

		It("should create dcgm-exporter pod [default]", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
		})

		It("should ensure that the dcgm-exporter pod is ready [default]", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should create a workload pod [default]", func(ctx context.Context) {
			shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)
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

				g.Expect(baselineMetricContractError(metricsResponse, true)).Should(Succeed())
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
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

		It("should create dcgm-exporter pod [configMatrix]", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
		})

		It("should ensure that the dcgm-exporter pod is ready [configMatrix]", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should create a workload pod [configMatrix]", func(ctx context.Context) {
			shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)
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
					"DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).",
					"DCGM_FI_DEV_FB_USED, gauge, Framebuffer memory used (in MiB).",
					"DCGM_FI_DRIVER_VERSION, label, Driver Version",
				),
			}
			shouldUpgradeHelmChart(ctx, helmClient, helmReleaseName, nil, jsonValues)

			Expect(kubeClient.DeletePod(ctx, dcgmExpPod.Namespace, dcgmExpPod.Name)).Should(Succeed(),
				"configMap-backed metrics file uses subPath; the exporter pod must restart after customMetrics changes")
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
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					exporterArguments(
						"--configmap-data="+testContext.namespace+":exporter-metrics-config-map",
						"--kubernetes-enable-pod-labels",
					),
					"kubernetes.enablePodLabels=true",
					"kubernetes.rbac.create=true",
				},
				[]string{customMetricsJSONValue(
					"DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).",
					"DCGM_FI_DEV_FB_USED, gauge, Framebuffer memory used (in MiB).",
					"DCGM_FI_DRIVER_VERSION, label, Driver Version",
				)},
				testRunLabels,
				customMetricsContractError)
		})
	})

	Context("DCGM exporter loads YAML config mounted by Helm", Ordered, Label("yamlConfig"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should use inline YAML metrics from the generated config ConfigMap", func(ctx context.Context) {
			yamlConfig := strings.TrimSpace(`
version: 1
metrics:
  fields:
    - name: DCGM_FI_DEV_GPU_TEMP
      prometheusType: gauge
      help: GPU temperature (in C).
    - name: DCGM_FI_DEV_FB_USED
      prometheusType: gauge
      help: Framebuffer memory used (in MiB).
collection:
  interval: 1s
`)

			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					"arguments={}",
					"config.enabled=true",
					"config.create=true",
					"config.key=dcgm-exporter.yaml",
					"config.mountPath=/etc/dcgm-exporter/config.yaml",
				},
				[]string{"config.data=" + strconv.Quote(yamlConfig)},
				nil,
				func(metrics []byte) error {
					omitted, err := missingMetricFamilies(metrics, []string{"DCGM_FI_DEV_POWER_USAGE"})
					if err != nil {
						return err
					}
					var defaultMetricErr error
					if len(omitted) == 0 {
						defaultMetricErr = errors.New("YAML config should replace the default DCGM_FI_DEV_POWER_USAGE metric")
					}
					return errors.Join(
						metricsHaveFamilyName(metrics, "DCGM_FI_DEV_GPU_TEMP"),
						metricsHaveFamilyName(metrics, "DCGM_FI_DEV_FB_USED"),
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
					exporterArguments("--use-old-namespace", "--kubernetes-enable-pod-labels"),
					"kubernetes.enablePodLabels=true",
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
					"DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).",
					"DCGM_FI_DEV_CLOCKS_EVENT_REASONS, gauge, Current clock event reasons.",
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
