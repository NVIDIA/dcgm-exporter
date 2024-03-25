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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
)

const (
	podLabel       = "pod"
	namespaceLabel = "namespace"
	containerLabel = "container"

	dcgmExporterPort = 9400

	dcgmExporterPodNameLabel      = "app.kubernetes.io/name"
	dcgmExporterPodNameLabelValue = "dcgm-exporter"

	workloadPodName       = "cuda-vector-add"
	workloadContainerName = "cuda-vector-add"
	workloadImage         = "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda11.7.1-ubuntu20.04"
)

var expectedLabels = []string{podLabel, namespaceLabel, containerLabel}

type testContextType struct {
	kubeconfig      string
	chart           string
	imageRepository string
	imageTag        string
	arguments       string
	namespace       string
}

var _ = Describe("dcgm-exporter-e2e-suite", func() {
	When("DCGM exporter is deployed on kubernetes", Ordered, func() {
		// Init global suite vars
		var (
			kubeClient *framework.KubeClient
			helmClient *framework.HelmClient

			labels = map[string]string{
				"e2eRunID": runID.String(),
			}

			helmReleaseName string
			dcgmExpPod      *corev1.Pod
			workloadPod     *corev1.Pod
		)

		BeforeAll(func(ctx context.Context) {
			if testContext.kubeconfig == "" {
				_, _ = fmt.Fprintln(GinkgoWriter, "kubeconfig parameter is empty. Defaulting to ~/.kube/config")
			}

			if len(testContext.chart) == 0 {
				Fail("chart parameter is empty")
			}

			shouldResolvePath()

			kubeConfigShouldExists()

			k8sConfig := shouldCreateK8SConfig()

			kubeClient = shouldCreateKubeClient(k8sConfig)

			helmClient = shouldCreateHelmClient(k8sConfig)
		})

		AfterAll(func(ctx context.Context) {
			_, _ = fmt.Fprintln(GinkgoWriter, "Clean up: starting")

			shouldUninstallHelmChart(helmClient, helmReleaseName)
			shouldCleanupHelmClient(helmClient)

			shouldDeleteNamespace(ctx, kubeClient)

			_, _ = fmt.Fprintln(GinkgoWriter, "Clean up: completed")
		})

		It("should create namespace", func(ctx context.Context) {
			shouldCreateNamespace(ctx, kubeClient, labels)
		})

		It("should install dcgm-exporter helm chart", func(ctx context.Context) {
			_, _ = fmt.Fprintf(GinkgoWriter, "Helm chart installation: %q chart started.\n",
				testContext.chart)

			values := []string{
				fmt.Sprintf("serviceMonitor.enabled=%v", false),
			}

			if testContext.arguments != "" {
				values = append(values, fmt.Sprintf("arguments=%s", testContext.arguments))
			}

			if testContext.imageRepository != "" {
				values = append(values, fmt.Sprintf("image.repository=%s", testContext.imageRepository))
			}
			if testContext.imageTag != "" {
				values = append(values, fmt.Sprintf("image.tag=%s", testContext.imageTag))
			}

			var err error

			helmReleaseName, err = helmClient.Install(ctx, values, framework.HelmChartOptions{
				CleanupOnFail: true,
				GenerateName:  true,
				Timeout:       5 * time.Minute,
				Wait:          true,
				DryRun:        false,
			})
			Expect(err).ShouldNot(HaveOccurred(), "Helm chart installation: %q chart failed with error err: %v", testContext.chart, err)

			_, _ = fmt.Fprintf(GinkgoWriter, "Helm chart installation: %q completed.\n",
				testContext.chart)
			_, _ = fmt.Fprintf(GinkgoWriter, "Helm chart installation: new %q release name.\n",
				helmReleaseName)
		})

		labelMap := map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}

		It("should create dcgm-exporter pod", func(ctx context.Context) {
			_, _ = fmt.Fprintln(GinkgoWriter, "Pod creation verification: started")

			Eventually(func(ctx context.Context) bool {
				pods, err := kubeClient.GetPodsByLabel(ctx, testContext.namespace, labelMap)
				if err != nil {
					Fail(fmt.Sprintf("Pod creation: Failed with error: %v", err))
					return false
				}

				if len(pods) == 1 {
					dcgmExpPod = &pods[0]
					return true
				}

				return false
			}).WithPolling(time.Second).Within(15 * time.Minute).WithContext(ctx).Should(BeTrue())

			_, _ = fmt.Fprintln(GinkgoWriter, "Pod creation verification: completed")
		})

		It("should ensure that the dcgm-exporter pod is ready", func(ctx context.Context) {
			_, _ = fmt.Fprintln(GinkgoWriter, "Checking pod status: started")
			Eventually(func(ctx context.Context) bool {
				isReady, err := kubeClient.CheckPodStatus(ctx,
					testContext.namespace,
					dcgmExpPod.Name,
					func(namespace, podName string, status corev1.PodStatus) (bool, error) {
						for _, c := range status.Conditions {
							if c.Type != corev1.PodReady {
								continue
							}
							if c.Status == corev1.ConditionTrue {
								return true, nil
							}
						}

						for _, c := range status.ContainerStatuses {
							if c.State.Waiting != nil && c.State.Waiting.Reason == "CrashLoopBackOff" {
								return false, fmt.Errorf("pod %s in namespace %s is in CrashLoopBackOff", podName, namespace)
							}
						}

						return false, nil
					})
				if err != nil {
					Fail(fmt.Sprintf("Checking pod status: Failed with error: %v", err))
				}

				return isReady
			}).WithPolling(time.Second).Within(15 * time.Minute).WithContext(ctx).Should(BeTrue())
			_, _ = fmt.Fprintln(GinkgoWriter, "Checking pod status: completed")
		})

		It("should create a workload pod", func(ctx context.Context) {
			_, _ = fmt.Fprintln(GinkgoWriter, "Workload pod creation: started")

			var err error

			workloadPod, err = kubeClient.CreatePod(ctx,
				testContext.namespace,
				labels,
				workloadPodName,
				workloadContainerName,
				workloadImage,
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

			_, _ = fmt.Fprintln(GinkgoWriter, "Workload pod creation: completed")
		})

		It("should wait for 30 seconds, to read metrics", func() {
			time.Sleep(30 * time.Second)
		})

		var metricsResponse []byte

		It("should read metrics", func(ctx context.Context) {
			_, _ = fmt.Fprintln(GinkgoWriter, "Read metrics: started")

			Eventually(func(ctx context.Context) bool {
				var err error

				metricsResponse, err = kubeClient.DoHttpRequest(ctx,
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
							Expect(ptr.Deref(label.Value, "")).ShouldNot(BeEmpty(), "The %s metric contains a label named %q label with empty value.",
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
})
