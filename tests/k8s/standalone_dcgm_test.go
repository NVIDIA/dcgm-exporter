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
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("dcgm-exporter standalone DCGM scenarios", func() {
	Context("DCGM exporter connects to a remote DCGM", Ordered, func() {
		AfterEach(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit the default metric contract through a standalone DCGM", Label("remoteDcgm"), func(ctx context.Context) {
			Expect(testContext.remoteDcgm).NotTo(BeEmpty(), "remote DCGM scenario requires -remote-dcgm")
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments(fmt.Sprintf("-r=%s", testContext.remoteDcgm))},
				nil,
				nil,
				func(metrics []byte) error {
					return defaultMetricsContractError(metrics, false)
				})
		})

		It("should recover metrics after the standalone DCGM restarts", Label("remoteDcgmRestart"), func(ctx context.Context) {
			Expect(testContext.remoteDcgm).NotTo(BeEmpty(), "remote DCGM restart scenario requires -remote-dcgm")
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments(fmt.Sprintf("-r=%s", testContext.remoteDcgm))},
				nil,
				nil,
				func(metrics []byte) error {
					return defaultMetricsContractError(metrics, false)
				})

			shouldRestartRemoteDCGM(ctx)

			Eventually(func(g Gomega) {
				pod := shouldCheckIfPodCreated(ctx, kubeClient, dcgmExporterPodLabels)
				shouldCheckIfPodIsReady(ctx, kubeClient, pod.Namespace, pod.Name)
				metrics, err := kubeClient.DoHTTPRequest(ctx,
					testContext.namespace,
					pod.Name,
					dcgmExporterPort,
					"metrics")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(metrics).NotTo(BeEmpty())
				g.Expect(defaultMetricsContractError(metrics, false)).To(Succeed())
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
		})
	})
})

// shouldRestartRemoteDCGM deletes the standalone DCGM pod and waits for deployment recovery.
func shouldRestartRemoteDCGM(ctx context.Context) {
	namespace := remoteDcgmNamespace()
	name := remoteDcgmName()
	selector := fmt.Sprintf("app.kubernetes.io/name=dcgm,app.kubernetes.io/instance=%s", name)
	podName := strings.TrimSpace(shouldRunKubectl(
		ctx,
		"-n", namespace,
		"get", "pod",
		"-l", selector,
		"-o", "jsonpath={.items[0].metadata.name}",
	))
	Expect(podName).NotTo(BeEmpty(), "standalone DCGM pod should exist before restart")

	shouldRunKubectl(ctx, "-n", namespace, "delete", "pod", podName, "--wait=true")
	shouldRunKubectl(ctx, "-n", namespace, "rollout", "status", "deployment/"+name, "--timeout=120s")
}

// remoteDcgmNamespace returns the standalone DCGM namespace used by e2e.
func remoteDcgmNamespace() string {
	if testContext.dcgmNS != "" {
		return testContext.dcgmNS
	}
	return testContext.namespace + "-dcgm"
}

// remoteDcgmName returns the standalone DCGM deployment and service name used by e2e.
func remoteDcgmName() string {
	if testContext.dcgmName != "" {
		return testContext.dcgmName
	}
	return "dcgm"
}
