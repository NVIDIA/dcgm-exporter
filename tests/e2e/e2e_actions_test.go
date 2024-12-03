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
	"os"
	"time"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func shouldCreateK8SConfig() *restclient.Config {
	config, err := clientcmd.BuildConfigFromFlags("", testContext.kubeconfig)
	Expect(err).ShouldNot(HaveOccurred(), "unable to load kubeconfig from %s; err: %s",
		testContext.kubeconfig, err)
	return config
}

func shouldResolvePath() {
	var err error
	testContext.kubeconfig, err = framework.ResolvePath(testContext.kubeconfig)
	Expect(err).ShouldNot(HaveOccurred(),
		"cannot resolve path to kubeconfig: %s, err: %v", testContext.kubeconfig, err)
}

func shouldCreateNamespace(ctx context.Context, kubeClient *framework.KubeClient, labels map[string]string) {
	_, _ = fmt.Fprintf(GinkgoWriter, "Creating namespace: %q started.\n", testContext.namespace)
	_, err := kubeClient.CreateNamespace(ctx, testContext.namespace, labels)
	Expect(err).ShouldNot(HaveOccurred(), "Creating namespace: failed")
	_, _ = fmt.Fprintf(GinkgoWriter, "Creating namespace: %q completed\n", testContext.namespace)
}

func shouldCreateKubeClient(config *rest.Config) *framework.KubeClient {
	kubeClient, err := framework.NewKubeClient(config)
	Expect(err).ShouldNot(HaveOccurred(), "cannot create k8s client: %s", err)
	return kubeClient
}

func kubeConfigShouldExists() {
	if _, err := os.Stat(testContext.kubeconfig); os.IsNotExist(err) {
		Fail(fmt.Sprintf("kubeconfig file does not exist: %s", testContext.kubeconfig))
	}
}

func shouldCreateHelmClient(config *rest.Config) *framework.HelmClient {
	helmClient, err := framework.NewHelmClient(
		framework.HelmWithNamespace(testContext.namespace),
		framework.HelmWithKubeConfig(config),
		framework.HelmWithChart(testContext.chart),
	)
	Expect(err).ShouldNot(HaveOccurred(), "Creating helm client: %q failed",
		testContext.namespace)

	return helmClient
}

func shouldUninstallHelmChart(helmClient *framework.HelmClient, helmReleaseName string) {
	if helmClient != nil && helmReleaseName != "" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Helm chart uninstall: release %q of the helm chart: %q started.\n",
			helmReleaseName,
			testContext.chart)

		err := helmClient.Uninstall(helmReleaseName)
		if err != nil {
			Fail(fmt.Sprintf("Helm chart uninstall: release: %s uninstall failed with error: %v", helmReleaseName, err))
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "Helm chart uninstall: release %q of the helm chart: %q completed.\n",
				helmReleaseName,
				testContext.chart)
		}
	}
}

func shouldCleanupHelmClient(helmClient *framework.HelmClient) {
	if helmClient != nil {
		err := helmClient.Cleanup()
		if err != nil {
			Fail(fmt.Sprintf("Helm Client: clean up failed: %v", err))
		}
	}
}

func shouldDeleteNamespace(ctx context.Context, kubeClient *framework.KubeClient) {
	_, _ = fmt.Fprintf(GinkgoWriter, "Namespace deletion: %q namespace started.\n", testContext.namespace)
	if kubeClient != nil {
		err := kubeClient.DeleteNamespace(ctx, testContext.namespace)
		if err != nil {
			Fail(fmt.Sprintf("Namespace deletion: Failed to delete namespace %q with error: %v", testContext.namespace, err))
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "Namespace deletion: %q deletion initiated.\n", testContext.namespace)
		}

		_, _ = fmt.Fprintf(GinkgoWriter, "Namespace deletion: %q waiting for completion.\n", testContext.namespace)

		Eventually(func() bool {
			_, err := kubeClient.GetNamespace(ctx, testContext.namespace)
			return err != nil // True if namespace no longer exists
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(BeTrue(),
			fmt.Sprintf("Namespace deletion: Namespace %q was not deleted within the timeout period.", testContext.namespace))

		_, _ = fmt.Fprintf(GinkgoWriter, "Namespace deletion: %q namespace fully deleted.\n", testContext.namespace)
	}
}
