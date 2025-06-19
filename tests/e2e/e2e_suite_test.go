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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
)

const (
	podLabel       = "pod"
	namespaceLabel = "namespace"
	containerLabel = "container"
	e2eRunIDLabel  = "e2eRunID"

	dcgmExporterPort = 9400

	dcgmExporterPodNameLabel      = "app.kubernetes.io/name"
	dcgmExporterPodNameLabelValue = "dcgm-exporter"

	workloadPodName       = "cuda-vector-add"
	workloadContainerName = "cuda-vector-add"
	workloadImage         = "nvcr.io/nvidia/k8s/cuda-sample:vectoradd-cuda11.7.1-ubuntu20.04"
)

var (
	expectedLabels        = []string{podLabel, namespaceLabel, containerLabel}
	dcgmExporterPodLabels = map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}
)

type testContextType struct {
	kubeconfig      string
	chart           string
	imageRepository string
	imageTag        string
	arguments       string
	namespace       string
	runtimeClass    string
	noCleanup       bool
}

var _ = Describe("dcgm-exporter-e2e-suite", func() {
	Context("DCGM exporter is deployed on kubernetes", Ordered, func() {
		// Init global suite vars
		var (
			kubeClient    *framework.KubeClient
			helmClient    *framework.HelmClient
			testRunLabels = map[string]string{
				e2eRunIDLabel: runID.String(),
			}
		)

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

		BeforeAll(func(ctx context.Context) {
			shouldCreateNamespace(ctx, kubeClient, testRunLabels)
		})

		AfterAll(func(ctx context.Context) {
			if testContext.noCleanup {
				_, _ = fmt.Fprintln(GinkgoWriter, "Clean up: skipped")
				Skip("Clean up skipped, by user request")
			}

			By("Clean up: starting")

			shouldCleanupHelmClient(helmClient)

			shouldDeleteNamespace(ctx, kubeClient)

			By("Clean up: completed")
		})

		VerifyDefaultHelmConfiguration(kubeClient, helmClient, testRunLabels)

		VerifyHelmConfigurationWhenTLSEnabled(kubeClient, helmClient, testRunLabels)

		VerifyHelmConfigurationWhenHttpBasicAuthEnabled(kubeClient, helmClient, testRunLabels)
	})

	Context("DCGM exporter with pod labels collection enabled", Ordered, func() {
		var (
			kubeClient      *framework.KubeClient
			helmClient      *framework.HelmClient
			helmReleaseName string
			dcgmExpPod      *corev1.Pod
			customLabels    = map[string]string{
				"valid_key":       "value-valid",
				"key-with-dashes": "value-dashes",
				"key.with.dots":   "value-dots",
			}
			labelMap        = map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}
			metricsResponse []byte
			testRunLabels   = map[string]string{
				e2eRunIDLabel: runID.String(),
			}
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

			// Create namespace for pod labels test
			shouldCreateNamespace(ctx, kubeClient, testRunLabels)
		})

		AfterAll(func(ctx context.Context) {
			if testContext.noCleanup {
				_, _ = fmt.Fprintln(GinkgoWriter, "Clean up: skipped")
				return
			}

			By("Starting cleanup for DCGM exporter with pod labels")

			shouldUninstallHelmChart(helmClient, helmReleaseName)
			shouldCleanupHelmClient(helmClient)
			shouldDeleteNamespace(ctx, kubeClient)

			By("Cleanup completed")
		})

		It("should install dcgm-exporter helm chart with pod labels enabled", func(ctx context.Context) {
			helmReleaseName = shouldInstallHelmChart(ctx, helmClient, []string{
				"arguments={--kubernetes-enable-pod-labels}",
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

		It("should wait for metrics to be collected", func() {
			By("Waiting 30 seconds for metrics collection")
			time.Sleep(30 * time.Second)
		})

		It("should read metrics from dcgm-exporter", func(ctx context.Context) {
			metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
			Expect(metricsResponse).ShouldNot(BeEmpty(), "Metrics response should not be empty")
		})

		It("should verify metrics contain sanitized pod labels", func(ctx context.Context) {
			By("Parsing and verifying metrics contain custom pod labels")

			// Parse metrics
			var parser expfmt.TextParser
			metricFamilies, err := parser.TextToMetricFamilies(bytes.NewReader(metricsResponse))
			Expect(err).ShouldNot(HaveOccurred(), "Error parsing metrics")
			Expect(metricFamilies).ShouldNot(BeEmpty(), "No metrics found")

			// Expected sanitized label mappings
			expectedSanitizedLabels := map[string]string{
				"valid_key":       "value-valid",  // no change needed
				"key_with_dashes": "value-dashes", // dashes become underscores
				"key_with_dots":   "value-dots",   // dots become underscores
			}

			labelsFound := map[string]bool{}

			// Search for sanitized labels in metrics
			for _, metricFamily := range metricFamilies {
				for _, metric := range metricFamily.GetMetric() {
					for _, label := range metric.Label {
						labelName := ptr.Deref(label.Name, "")
						labelValue := ptr.Deref(label.Value, "")

						if expectedValue, exists := expectedSanitizedLabels[labelName]; exists {
							Expect(labelValue).Should(
								Equal(expectedValue),
								"Expected sanitized label %q to have value %q, but got %q",
								labelName, expectedValue, labelValue,
							)
							labelsFound[labelName] = true
						}
					}
				}
			}

			// Verify all expected labels were found
			for expectedLabel := range expectedSanitizedLabels {
				Expect(labelsFound[expectedLabel]).Should(
					BeTrue(),
					"Expected to find sanitized label %q in metrics", expectedLabel,
				)
			}

			By("Pod labels verified successfully in metrics")
		})
	})
})
