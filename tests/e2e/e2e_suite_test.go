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
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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

	// Timeout constants for test operations
	podCreationTimeout         = 2 * time.Minute
	podReadinessTimeout        = 3 * time.Minute
	namespaceDeletionTimeout   = 90 * time.Second
	namespaceStuckCheckTimeout = 2 * time.Minute
	workloadPodDeletionTimeout = 45 * time.Second
	metricsReadTimeout         = 1 * time.Minute
	metricsWaitTimeout         = 30 * time.Second
	helmInstallTimeout         = 5 * time.Minute
	httpClientTimeout          = 5 * time.Second

	// Polling interval constants
	pollingIntervalFast     = 500 * time.Millisecond
	pollingIntervalNormal   = 1 * time.Second
	pollingIntervalSlow     = 2 * time.Second
	pollingIntervalVerySlow = 3 * time.Second
)

var (
	expectedLabels        = []string{podLabel, namespaceLabel, containerLabel}
	dcgmExporterPodLabels = map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}
	testRunLabels         = map[string]string{
		e2eRunIDLabel: runID.String(),
	}
	kubeClient        *framework.KubeClient
	helmClient        *framework.HelmClient
	helmReleases      []string   // Track all installed Helm releases for cleanup
	helmReleasesMutex sync.Mutex // Protect concurrent access to helmReleases
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

// Global cleanup of stuck namespaces before any tests run
var _ = BeforeSuite(func(ctx context.Context) {
	By("Global cleanup: Checking for stuck namespaces")

	// Only run cleanup if we have a valid kubeconfig
	if testContext.kubeconfig != "" {
		shouldResolvePath()
		kubeConfigShouldExists()
		k8sConfig := shouldCreateK8SConfig()
		kubeClient = shouldCreateKubeClient(k8sConfig)
		helmClient = shouldCreateHelmClient(k8sConfig)
		shouldCreateNamespace(ctx, kubeClient, testRunLabels)

		// Check if our test namespace is stuck and wait for cleanup
		existingNamespace, err := kubeClient.GetNamespace(ctx, testContext.namespace)
		if err == nil && existingNamespace.Status.Phase == corev1.NamespaceTerminating {
			By(fmt.Sprintf("Global cleanup: Found stuck namespace %q, waiting for cleanup", testContext.namespace))
			// Wait for the namespace to be fully deleted
			Eventually(func() bool {
				_, err := kubeClient.GetNamespace(ctx, testContext.namespace)
				if err == nil {
					// Namespace still exists
					return false
				}
				// Check if the error is specifically a "NotFound" error
				if k8serrors.IsNotFound(err) {
					// Namespace was successfully deleted
					return true
				}
				// Other errors (network, auth, etc.) should not be treated as success
				return false
			}).WithTimeout(namespaceStuckCheckTimeout).WithPolling(pollingIntervalVerySlow).Should(BeTrue(),
				fmt.Sprintf("Global cleanup: Namespace %q was not deleted within the timeout period.", testContext.namespace))
		}
	}
})

var _ = AfterSuite(func(ctx context.Context) {
	// Use safe cleanup that can be called multiple times without errors
	safeCleanup(ctx, kubeClient, helmClient, true)
})

var _ = Describe("dcgm-exporter-e2e-suite", func() {
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

	Context("DCGM exporter is deployed on kubernetes and TLS is enabled", Ordered, Label("tls"), func() {
		var dcgmExpPod *corev1.Pod

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter helm chart [tls]", func(ctx context.Context) {
			shouldInstallHelmChart(ctx, helmClient, []string{
				"tlsServerConfig.enabled=true",
				"tlsServerConfig.autoGenerated=true",
			})
		})

		It("should create dcgm-exporter pod [tls]", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
		})

		It("should ensure that the dcgm-exporter pod is ready [tls]", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should check that the port accepts TLS [tls]", func(ctx context.Context) {
			// Test TLS connection using port forwarding
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			kubeClient.ErrWriter = GinkgoWriter
			kubeClient.OutWriter = GinkgoWriter
			localPort, err := kubeClient.PortForward(ctx, dcgmExpPod.Namespace, dcgmExpPod.Name, 9400)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(localPort).Should(BeNumerically(">", 0))
			httpClient := &http.Client{
				Timeout: httpClientTimeout,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				},
			}

			By("Ensure that HTTP request returns 400 error")
			resp, err := httpClient.Get(fmt.Sprintf("http://localhost:%d/metrics", localPort))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(400))
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("Client sent an HTTP request to an HTTPS server"))

			By("Ensure that HTTPS request returns 200")
			resp, err = httpClient.Get(fmt.Sprintf("https://localhost:%d/metrics", localPort))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200))
			_, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("DCGM exporter is deployed on kubernetes and HTTP basic auth is enabled", Ordered, Label("basicAuth"), func() {
		var dcgmExpPod *corev1.Pod

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter helm chart [basicAuth]", func(ctx context.Context) {
			shouldInstallHelmChart(ctx, helmClient, []string{
				"basicAuth.users.admin=admin",
			})
		})

		It("should create dcgm-exporter pod [basicAuth]", func(ctx context.Context) {
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
		})

		It("should ensure that the dcgm-exporter pod is ready [basicAuth]", func(ctx context.Context) {
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
		})

		It("should check that the metric endpoint protected by basic HTTP auth [basicAuth]", func(ctx context.Context) {
			// Test basic auth using port forwarding
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			kubeClient.ErrWriter = GinkgoWriter
			kubeClient.OutWriter = GinkgoWriter
			localPort, err := kubeClient.PortForward(ctx, dcgmExpPod.Namespace, dcgmExpPod.Name, 9400)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(localPort).Should(BeNumerically(">", 0))
			httpClient := &http.Client{
				Timeout: httpClientTimeout,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				},
			}

			By("Ensure that HTTP request returns 401 error when no credentials provided")
			resp, err := httpClient.Get(fmt.Sprintf("http://localhost:%d/metrics", localPort))
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(401))
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(body)).To(ContainSubstring("Unauthorized"))

			By("Ensure that HTTP request returns 200 with valid credentials")
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://localhost:%d/metrics", localPort), http.NoBody)
			Expect(err).ShouldNot(HaveOccurred())
			req.SetBasicAuth("admin", "admin")
			resp, err = httpClient.Do(req)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200))
			_, err = io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("DCGM exporter with pod labels collection enabled", Ordered, Label("labels"), func() {
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
				"arguments={--kubernetes-enable-pod-labels}",
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
