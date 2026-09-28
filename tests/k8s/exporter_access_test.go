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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
			dcgmExpPod = shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)
			shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should serve the same metric contract through pod and service proxies", func(ctx context.Context) {
			podMetrics := shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
			Expect(defaultMetricsContractError(podMetrics, true)).To(Succeed())

			serviceMetrics := shouldReadServiceMetrics(ctx, helmReleaseName)
			Expect(defaultMetricsContractError(serviceMetrics, true)).To(Succeed())
		})
	})
})
