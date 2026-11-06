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
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("dcgm-exporter hardening scenarios", func() {
	Context("DCGM exporter pod runs with the chart's security settings and least-privilege RBAC", Ordered, Label("podHardening"), func() {
		var (
			dcgmExpPod      *corev1.Pod
			helmReleaseName string
		)

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should run with the chart security context and no API token by default", func(ctx context.Context) {
			helmReleaseName = shouldInstallHelmChart(ctx, helmClient, nil)
			dcgmExpPod = shouldFindExporterPod(ctx, kubeClient, dcgmExporterPodLabels)

			Expect(exporterPodSecurityError(dcgmExpPod)).To(Succeed())
			Expect(exporterTokenMountError(dcgmExpPod, false)).To(Succeed())
			shouldMatchExporterAccess(ctx, dcgmExpPod, exporterAccessChecks(dcgmExpPod.Namespace, false, false))
		})

		It("should grant only pod reads when pod labels are on", func(ctx context.Context) {
			shouldUpgradeHelmChart(ctx, helmClient, helmReleaseName, []string{
				exporterArguments(),
				"config.sources.kubernetes.pod_labels.enabled=true",
				"config.sources.kubernetes.pod_labels.allow[0]=.*",
				"kubernetes.rbac.create=true",
			}, nil)
			dcgmExpPod = shouldCheckIfReplacementPodCreated(ctx, kubeClient, dcgmExporterPodLabels, dcgmExpPod)
			shouldCheckIfPodIsReady(ctx, kubeClient, dcgmExpPod.Namespace, dcgmExpPod.Name)

			// Read the pod again so the checks see the running container's status.
			var err error
			dcgmExpPod, err = kubeClient.GetPod(ctx, dcgmExpPod.Namespace, dcgmExpPod.Name)
			Expect(err).NotTo(HaveOccurred())

			Expect(exporterPodSecurityError(dcgmExpPod)).To(Succeed())
			Expect(exporterTokenMountError(dcgmExpPod, true)).To(Succeed())
			shouldMatchExporterAccess(ctx, dcgmExpPod, exporterAccessChecks(dcgmExpPod.Namespace, true, false))
		})
	})
})

// shouldMatchExporterAccess asks the API server about each check as the
// exporter pod's ServiceAccount and fails with every mismatch at once.
func shouldMatchExporterAccess(ctx context.Context, pod *corev1.Pod, checks []exporterAccessCheck) {
	serviceAccount := pod.Spec.ServiceAccountName
	Expect(serviceAccount).NotTo(BeEmpty(), "exporter pod %s has no ServiceAccount", pod.Name)

	var mismatches []error
	for _, check := range checks {
		allowed, err := kubeClient.ServiceAccountCan(ctx, pod.Namespace, serviceAccount, check.attributes)
		Expect(err).NotTo(HaveOccurred())
		if allowed != check.allowed {
			mismatches = append(mismatches, fmt.Errorf("%s: allowed = %t, want %t", check, allowed, check.allowed))
		}
	}
	Expect(errors.Join(mismatches...)).To(Succeed(), "ServiceAccount %s/%s access", pod.Namespace, serviceAccount)
}
