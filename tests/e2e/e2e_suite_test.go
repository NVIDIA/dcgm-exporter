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
	"context"
	"fmt"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
	. "github.com/onsi/ginkgo/v2"
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
})
