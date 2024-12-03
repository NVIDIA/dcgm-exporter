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
			labelMap = map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}

			helmReleaseName string
			dcgmExpPod      *corev1.Pod

			metricsResponse []byte
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
			helmReleaseName = shouldInstallHelmChart(ctx, helmClient, []string{
				"serviceMonitor.enabled=false",
			})
		})

		It("should create dcgm-exporter pod", func(ctx context.Context) {
			dcgmExpPod = shouldCreateDCGMPod(ctx, kubeClient, testContext.namespace, labelMap)
		})

		It("should ensure that the dcgm-exporter pod is ready", func(ctx context.Context) {
			shouldEnsurePodReadiness(ctx, kubeClient, dcgmExpPod)
		})

		It("should create a workload pod", func(ctx context.Context) {
			shouldCreateWorkloadPod(ctx, kubeClient, labels)
		})

		It("should wait for 30 seconds, to read metrics", func() {
			time.Sleep(30 * time.Second)
		})

		It("should read metrics", func(ctx context.Context) {
			metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
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

	When("DCGM exporter is deployed on kubernetes with pod labels collection enabled", Ordered, func() {
		var (
			kubeClient      *framework.KubeClient
			helmClient      *framework.HelmClient
			helmReleaseName string
			dcgmExpPod      *corev1.Pod
			customLabels    = map[string]string{"custom-key": "custom-value", "another-key": "another-value"}
			labelMap        = map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}
			metricsResponse []byte
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
			_, _ = fmt.Fprintln(GinkgoWriter, "Starting cleanup for DCGM exporter with pod labels")

			shouldUninstallHelmChart(helmClient, helmReleaseName)
			shouldCleanupHelmClient(helmClient)
			shouldDeleteNamespace(ctx, kubeClient)

			_, _ = fmt.Fprintln(GinkgoWriter, "Cleanup completed")
		})

		It("should create namespace", func(ctx context.Context) {
			shouldCreateNamespace(ctx, kubeClient, map[string]string{})
		})

		It("should install dcgm-exporter helm chart with pod labels enabled", func(ctx context.Context) {
			helmReleaseName = shouldInstallHelmChart(ctx, helmClient, []string{
				"serviceMonitor.enabled=false",
				"extraEnvVars[0].name=DCGM_EXPORTER_ENABLE_POD_LABELS",
				"extraEnvVars[0].value=true",
			})
		})

		It("should create dcgm-exporter pod", func(ctx context.Context) {
			dcgmExpPod = shouldCreateDCGMPod(ctx, kubeClient, testContext.namespace, labelMap)
		})

		It("should ensure that the dcgm-exporter pod is ready", func(ctx context.Context) {
			shouldEnsurePodReadiness(ctx, kubeClient, dcgmExpPod)
		})

		It("should create a workload pod", func(ctx context.Context) {
			shouldCreateWorkloadPod(ctx, kubeClient, customLabels)
		})

		It("should wait for 30 seconds, to read metrics", func() {
			time.Sleep(30 * time.Second)
		})

		It("should read metrics", func(ctx context.Context) {
			metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should verify metrics have pod labels inside", func(ctx context.Context) {
			Expect(metricsResponse).ShouldNot(BeEmpty())

			_, _ = fmt.Fprintln(GinkgoWriter, "Read metrics: started")

			// Parse and verify metrics contain custom pod labels
			var parser expfmt.TextParser
			metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(metricsResponse))
			Expect(err).ShouldNot(HaveOccurred(), "Error parsing metrics")
			Expect(metricFamilies).ShouldNot(BeEmpty(), "No metrics found")

			for _, metricFamily := range metricFamilies {
				for _, metric := range metricFamily.GetMetric() {
					for _, label := range metric.Label {
						labelName := ptr.Deref(label.Name, "")
						if slices.Contains([]string{"custom-key", "another-key"}, labelName) {
							Expect(ptr.Deref(label.Value, "")).Should(Equal(customLabels[labelName]),
								"Expected metric to include label %q with value %q, but got %q",
								labelName, customLabels[labelName], ptr.Deref(label.Value, ""),
							)
						}
					}
				}
			}

			_, _ = fmt.Fprintln(GinkgoWriter, "Pod labels verified successfully in metrics")
		})
	})
})
