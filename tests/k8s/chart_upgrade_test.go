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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/k8s/internal/framework"
)

const (
	// previousChartVersion is the last released chart the upgrade scenario
	// installs. Change it with each release.
	previousChartVersion = "4.8.4"
	// previousChartRepositoryName and previousChartRepositoryURL name the
	// repository that publishes the released charts. The test trusts that
	// repository at the pinned version, so run it on a disposable cluster.
	previousChartRepositoryName = "dcgm-exporter-previous"
	previousChartRepositoryURL  = "https://nvidia.github.io/dcgm-exporter/helm-charts"
	// previousMetricsConfigMapName and previousRoleName are the fixed names the
	// previous chart gave the metrics ConfigMap and its Role.
	previousMetricsConfigMapName = "exporter-metrics-config-map"
	previousRoleName             = "dcgm-exporter-read-cm"
)

// previousChartValues returns Helm values that use keys of the previous chart
// which the current chart removed.
func previousChartValues() []string {
	return []string{
		"arguments={}",
		"kubernetes.enablePodLabels=true",
		"kubernetes.podLabelAllowlistRegex[0]=.*",
		"service.webReadTimeout=10s",
	}
}

// currentChartValues returns the values of previousChartValues written for the
// current chart, as its removed-value error names them.
func currentChartValues() []string {
	return []string{
		"arguments={}",
		"config.sources.kubernetes.pod_labels.enabled=true",
		"config.sources.kubernetes.pod_labels.allow[0]=.*",
		"config.server.read_timeout=10s",
	}
}

var _ = Describe("dcgm-exporter chart upgrade scenarios", func() {
	Context("DCGM exporter upgrades from the last released chart", Ordered, Label("chartUpgrade"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should reject the previous chart's removed values and upgrade with their replacements", func(ctx context.Context) {
			releaseName := shouldInstallPreviousChart(ctx, previousChartValues())
			previousPod := shouldFindExporterPod(ctx, kubeClient, dcgmExporterPodLabels)
			shouldCheckIfPodIsReady(ctx, kubeClient, previousPod.Namespace, previousPod.Name)

			// The upgrade check below only means something if the previous chart created these objects.
			shouldHaveConfigMap(ctx, previousPod.Namespace, previousMetricsConfigMapName, true)
			shouldHaveRole(ctx, previousPod.Namespace, previousRoleName, true)

			By("Upgrading with the previous chart's values, which the chart rejects")
			_, err := helmClient.Upgrade(ctx, releaseName,
				framework.HelmChartOptions{Timeout: helmInstallTimeout, Wait: true},
				framework.WithValues(mergeHelmValues(getDefaultHelmValues(), previousChartValues())...),
				framework.WithStringValues(getDefaultHelmStringValues()...),
			)
			Expect(removedValuesError(err)).To(Succeed())

			By("Upgrading with the replacement values")
			shouldUpgradeHelmChart(ctx, helmClient, releaseName, currentChartValues(), nil)
			upgradedPod := shouldCheckIfReplacementPodCreated(ctx, kubeClient, dcgmExporterPodLabels, previousPod)
			shouldCheckIfPodIsReady(ctx, kubeClient, upgradedPod.Namespace, upgradedPod.Name)
			shouldEventuallyReadMetrics(ctx, upgradedPod, baselineWithoutWorkload, 2*time.Minute)

			By("Checking that the chart replaced the previous chart's fixed-name objects")
			namespace := upgradedPod.Namespace
			shouldHaveConfigMap(ctx, namespace, previousMetricsConfigMapName, false)
			shouldHaveRole(ctx, namespace, previousRoleName, false)
			shouldHaveConfigMap(ctx, namespace, chartMetricsConfigMapName(releaseName), true)
		})
	})
})

// shouldHaveConfigMap fails the spec unless the ConfigMap exists when want is true, or is absent when want is false.
func shouldHaveConfigMap(ctx context.Context, namespace, name string, want bool) {
	Eventually(func(g Gomega) {
		exists, err := kubeClient.ConfigMapExists(ctx, namespace, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(exists).To(Equal(want), "ConfigMap %s/%s", namespace, name)
	}).WithPolling(pollingIntervalNormal).Within(time.Minute).Should(Succeed())
}

// shouldHaveRole fails the spec unless the Role exists when want is true, or is absent when want is false.
func shouldHaveRole(ctx context.Context, namespace, name string, want bool) {
	Eventually(func(g Gomega) {
		exists, err := kubeClient.RoleExists(ctx, namespace, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(exists).To(Equal(want), "Role %s/%s", namespace, name)
	}).WithPolling(pollingIntervalNormal).Within(time.Minute).Should(Succeed())
}
