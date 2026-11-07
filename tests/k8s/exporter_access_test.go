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
	"fmt"
	"net"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	metricsClientLabel        = "dcgm-exporter-e2e/metrics-client"
	allowedMetricsClientName  = "metrics-client-allowed"
	deniedMetricsClientName   = "metrics-client-denied"
	metricsClientContainer    = "client"
	networkPolicyApplyTimeout = 2 * time.Minute
)

var _ = Describe("dcgm-exporter access scenarios", func() {
	Context("DCGM exporter metrics are reachable directly and through the service", Ordered, Label("serviceAccess"), func() {
		var (
			dcgmExpPod      *corev1.Pod
			helmReleaseName string
		)

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should deploy the exporter and workload", func(ctx context.Context) {
			helmReleaseName = shouldInstallHelmChart(ctx, helmClient, nil)
			workload := shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)
			dcgmExpPod = shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should expose pod labels directly and baseline metrics through the Service", func(ctx context.Context) {
			shouldEventuallyMeetBaselineMetricContract(func(g Gomega) []byte {
				metrics, err := kubeClient.DoHTTPRequest(ctx, testContext.namespace, dcgmExpPod.Name, dcgmExporterPort, "metrics")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(metrics).NotTo(BeEmpty())
				return metrics
			}, true)
			shouldEventuallyMeetBaselineMetricContract(func(g Gomega) []byte {
				services, err := kubeClient.GetServicesByLabel(ctx, testContext.namespace, map[string]string{
					dcgmExporterPodNameLabel:     dcgmExporterPodNameLabelValue,
					"app.kubernetes.io/instance": helmReleaseName,
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(services).To(HaveLen(1), "expected one dcgm-exporter service for Helm release %q", helmReleaseName)

				metrics, err := kubeClient.DoServiceHTTPRequest(ctx, testContext.namespace, services[0].Name, dcgmExporterPort, "metrics")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(metrics).NotTo(BeEmpty())
				return metrics
			}, false)
		})
	})

	Context("DCGM exporter metrics NetworkPolicy admits only allowed peers", Ordered, Label("metricsNetworkPolicy"), func() {
		var dcgmExpPod *corev1.Pod

		AfterAll(func(ctx context.Context) {
			for _, name := range []string{allowedMetricsClientName, deniedMetricsClientName} {
				Expect(kubeClient.DeletePod(ctx, testContext.namespace, name)).To(Succeed())
			}
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should deploy the exporter with the policy", func(ctx context.Context) {
			shouldInstallHelmChartWithJSONValues(ctx, helmClient,
				[]string{"networkPolicy.enabled=true"},
				[]string{fmt.Sprintf(`networkPolicy.allowedPeers=[{"podSelector":{"matchLabels":{%q:"allowed"}}}]`, metricsClientLabel)},
			)
			dcgmExpPod = shouldFindExporterPod(ctx, kubeClient, dcgmExporterPodLabels)
			Expect(dcgmExpPod.Status.PodIP).NotTo(BeEmpty())
		})

		It("should admit the allowed peer and block other pods", func(ctx context.Context) {
			url := fmt.Sprintf("http://%s/metrics", net.JoinHostPort(dcgmExpPod.Status.PodIP, strconv.Itoa(dcgmExporterPort)))
			allowed := shouldCreateMetricsClientPod(ctx, allowedMetricsClientName, "allowed", dcgmExpPod.Spec.NodeName)
			denied := shouldCreateMetricsClientPod(ctx, deniedMetricsClientName, "denied", dcgmExpPod.Spec.NodeName)

			// The allowed scrape proves the listener is up before the denied one is judged.
			Eventually(func(ctx context.Context) error {
				return metricsClientScrape(ctx, allowed, url)
			}).WithPolling(pollingIntervalSlow).Within(networkPolicyApplyTimeout).WithContext(ctx).Should(Succeed())

			// A failed scrape proves the policy only when the denied client can run wget at all.
			_, stderr, err := kubeClient.ExecutePodCommand(ctx, denied.Namespace, denied.Name, metricsClientContainer,
				[]string{"sh", "-c", "command -v wget"})
			Expect(err).NotTo(HaveOccurred(), stderr)

			Eventually(func(ctx context.Context) error {
				return metricsClientScrape(ctx, denied, url)
			}).WithPolling(pollingIntervalSlow).Within(networkPolicyApplyTimeout).WithContext(ctx).ShouldNot(Succeed())
			Consistently(func(ctx context.Context) error {
				return metricsClientScrape(ctx, denied, url)
			}).WithPolling(pollingIntervalSlow).Within(15 * time.Second).WithContext(ctx).ShouldNot(Succeed())
			Expect(metricsClientScrape(ctx, allowed, url)).To(Succeed())
		})
	})
})

// shouldCreateMetricsClientPod starts a busybox pod on the exporter's node with
// the given metrics-client label value.
func shouldCreateMetricsClientPod(ctx context.Context, name, labelValue, nodeName string) *corev1.Pod {
	memory := resource.MustParse("32Mi")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testContext.namespace,
			Labels: map[string]string{
				e2eRunIDLabel:      runID.String(),
				metricsClientLabel: labelValue,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			NodeSelector:  getE2ENodeSelector(),
			NodeName:      nodeName,
			Containers: []corev1.Container{{
				Name:    metricsClientContainer,
				Image:   requiredBusyBoxImage(),
				Command: []string{"sleep", "infinity"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: memory},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: memory},
				},
			}},
		},
	}

	// A pod left by an earlier run may still be terminating; wait until it is gone.
	Expect(kubeClient.DeletePod(ctx, testContext.namespace, name)).To(Succeed())
	Eventually(func(ctx context.Context) bool {
		_, err := kubeClient.GetPod(ctx, testContext.namespace, name)
		return k8serrors.IsNotFound(err)
	}).WithPolling(pollingIntervalFast).Within(workloadPodDeletionTimeout).WithContext(ctx).Should(BeTrue(),
		"metrics client pod %q was not deleted", name)
	created, err := kubeClient.CreatePodFromSpec(ctx, pod)
	Expect(err).NotTo(HaveOccurred())
	Eventually(func(ctx context.Context) (bool, error) {
		return kubeClient.CheckPodStatus(ctx, created.Namespace, created.Name, func(_, _ string, status corev1.PodStatus) (bool, error) {
			return status.Phase == corev1.PodRunning, nil
		})
	}).WithPolling(pollingIntervalNormal).Within(podReadinessTimeout).WithContext(ctx).Should(BeTrue())
	return created
}

// metricsClientScrape fetches url from inside client and returns an error when
// the request fails or times out.
func metricsClientScrape(ctx context.Context, client *corev1.Pod, url string) error {
	_, stderr, err := kubeClient.ExecutePodCommand(ctx, client.Namespace, client.Name, metricsClientContainer,
		[]string{"wget", "-q", "-T", "5", "-O", "/dev/null", url})
	if err != nil {
		return fmt.Errorf("%s: %w: %s", client.Name, err, stderr)
	}
	return nil
}
