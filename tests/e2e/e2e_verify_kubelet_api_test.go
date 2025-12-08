//go:build e2e

/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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
package e2e

import (
	"bytes"
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("dcgm-exporter-e2e-suite", func() {
	Context("DCGM exporter with kubelet API", Ordered, Label("kubelet-api"), func() {
		var (
			dcgmExpPod   *corev1.Pod
			customLabels = map[string]string{
				"example.com/accelerator": "nvidia-h20",
				"example.com/appid":       "test-appid",
			}
			metricsResponse []byte
		)

		AfterAll(func(ctx context.Context) {
			if testContext.noCleanup {
				return
			}
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter with kubelet API enabled", func(ctx context.Context) {
			shouldInstallHelmChart(ctx, helmClient, []string{
				"arguments={--kubernetes-enable-pod-labels,--kubernetes-use-kubelet-api,--kubernetes-pod-label-allowlist-regex=^example\\.com/accelerator$,--kubernetes-pod-label-allowlist-regex=^example\\.com/appid$}",
				"kubernetes.enablePodLabels=true",
				"kubernetes.rbac.create=true",
				"serviceAccount.create=true",
			})
		})

		It("should create dcgm-exporter pod", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, map[string]string{
				dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue,
			})
		})

		It("should ensure dcgm-exporter pod is ready", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should create workload pod with custom labels", func(ctx context.Context) {
			shouldCreateWorkloadPod(ctx, kubeClient, customLabels)
		})

		It("should wait for metrics to be available", func(ctx context.Context) {
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should read metrics", func(ctx context.Context) {
			metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
			Expect(metricsResponse).ShouldNot(BeEmpty())
		})

		It("should verify pod labels from kubelet API", func(ctx context.Context) {
			var parser expfmt.TextParser
			metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(metricsResponse))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(metricFamilies).ShouldNot(BeEmpty())

			expectedLabels := map[string]string{
				"example_com_accelerator": "nvidia-h20",
				"example_com_appid":       "test-appid",
			}

			labelsFound := map[string]bool{}

			for _, metricFamily := range metricFamilies {
				for _, metric := range metricFamily.GetMetric() {
					for _, label := range metric.Label {
						labelName := ptr.Deref(label.Name, "")
						labelValue := ptr.Deref(label.Value, "")

						if expectedValue, exists := expectedLabels[labelName]; exists {
							Expect(labelValue).Should(Equal(expectedValue))
							labelsFound[labelName] = true
						}
					}
				}
			}

			for expectedLabel := range expectedLabels {
				Expect(labelsFound[expectedLabel]).Should(BeTrue(),
					"Label %q not found in metrics", expectedLabel)
			}
		})

		It("should verify logs show kubelet API usage", func(ctx context.Context) {
			tailLines := int64(100)
			logs, err := kubeClient.GetPodLogs(ctx, dcgmExpPod.Namespace, dcgmExpPod.Name, "", &tailLines)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(logs).Should(ContainSubstring("Using kubelet API"))
		})
	})
})
