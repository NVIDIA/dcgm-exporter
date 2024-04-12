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
	"fmt"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
)

// VerifyDefaultHelmConfiguration tests the helm chart with default configuration
var VerifyDefaultHelmConfiguration = func(
	kubeClient *framework.KubeClient,
	helmClient *framework.HelmClient,
	testRunLabels map[string]string,
) bool {
	return Context("and uses a default helm configuration", Label("default"), func() {
		var (
			helmReleaseName string
			dcgmExpPod      *corev1.Pod
			workloadPod     *corev1.Pod
		)

		AfterAll(func(ctx context.Context) {
			shouldUninstallHelmChart(helmClient, helmReleaseName)
		})

		It("should install dcgm-exporter helm chart", func(ctx context.Context) {
			By(fmt.Sprintf("Helm chart installation: %q chart started.",
				testContext.chart))

			values := getDefaultHelmValues()

			var err error

			helmReleaseName, err = helmClient.Install(ctx, framework.HelmChartOptions{
				CleanupOnFail: true,
				GenerateName:  true,
				Timeout:       5 * time.Minute,
				Wait:          true,
				DryRun:        false,
			}, framework.WithValues(values...))
			Expect(err).ShouldNot(HaveOccurred(), "Helm chart installation: %q chart failed with error err: %v",
				testContext.chart, err)

			By(fmt.Sprintf("Helm chart installation: %q completed.",
				testContext.chart))
			By(fmt.Sprintf("Helm chart installation: new %q release name.",
				helmReleaseName))
		})

		It("should create dcgm-exporter pod", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
		})

		It("should ensure that the dcgm-exporter pod is ready", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should create a workload pod", func(ctx context.Context) {
			_, _ = fmt.Fprintln(GinkgoWriter, "Workload pod creation: started")

			var err error

			workloadPod, err = kubeClient.CreatePod(ctx,
				testContext.namespace,
				testRunLabels,
				workloadPodName,
				workloadContainerName,
				workloadImage,
				testContext.runtimeClass,
			)

			Expect(err).ShouldNot(HaveOccurred(),
				"Workload pod creation: Failed create workload pod with err: %v", err)
			Eventually(func(ctx context.Context) bool {
				isReady, err := kubeClient.CheckPodStatus(ctx,
					testContext.namespace,
					workloadPod.Name, func(namespace, podName string, status corev1.PodStatus) (bool, error) {
						return status.Phase == corev1.PodSucceeded, nil
					})
				if err != nil {
					Fail(fmt.Sprintf("Workload pod creation: Checking pod status: Failed with error: %v", err))
				}

				return isReady
			}).WithPolling(time.Second).Within(15 * time.Minute).WithContext(ctx).Should(BeTrue())

			By("Workload pod creation: completed")
		})

		It("should wait for 30 seconds, to read metrics", func() {
			time.Sleep(30 * time.Second)
		})

		var metricsResponse []byte

		It("should read metrics", func(ctx context.Context) {
			_, _ = fmt.Fprintln(GinkgoWriter, "Read metrics: started")

			Eventually(func(ctx context.Context) bool {
				var err error

				metricsResponse, err = kubeClient.DoHTTPRequest(ctx,
					testContext.namespace,
					dcgmExpPod.Name,
					dcgmExporterPort,
					"metrics")
				if err != nil {
					Fail(fmt.Sprintf("Read metrics: Failed with error: %v", err))
				}

				return len(metricsResponse) > 0
			}).WithPolling(time.Second).Within(time.Minute).WithContext(ctx).Should(BeTrue())
			_, _ = fmt.Fprintln(GinkgoWriter, "Read metrics: completed")
		})

		It("should verify metrics", func(ctx context.Context) {
			Expect(metricsResponse).ShouldNot(BeEmpty())

			var parser expfmt.TextParser
			metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(metricsResponse))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(len(metricFamilies)).Should(BeNumerically(">", 0))

			for _, metricFamily := range metricFamilies {
				Expect(metricFamily).ShouldNot(BeNil())
				metrics := metricFamily.GetMetric()
				Expect(metrics).ShouldNot(BeNil())

				// Each metric must have namespace, pod and container labels
				for _, metric := range metrics {
					var actualLabels []string
					for _, label := range metric.Label {
						labelName := ptr.Deref(label.Name, "")
						if slices.Contains(expectedLabels, labelName) {
							actualLabels = append(actualLabels, labelName)
							Expect(label.Value).ShouldNot(BeNil())
							Expect(ptr.Deref(label.Value, "")).ShouldNot(BeEmpty(),
								"The %s metric contains a label named %q label with empty value.",
								ptr.Deref(metricFamily.Name, ""),
								labelName,
							)
						}
					}
					Expect(len(actualLabels)).Should(Equal(len(expectedLabels)),
						"Metric %s doesn't contains expected labels: %v, actual labels: %v",
						ptr.Deref(metricFamily.Name, ""), expectedLabels, metric.Label)
				}
			}
		})
	})
}
