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
package k8s

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/NVIDIA/dcgm-exporter/tests/k8s/internal/framework"
)

const (
	podLabel       = "pod"
	namespaceLabel = "namespace"
	containerLabel = "container"
	e2eRunIDLabel  = "e2eRunID"

	dcgmExporterPort = 9400

	dcgmExporterPodNameLabel      = "app.kubernetes.io/name"
	dcgmExporterPodNameLabelValue = "dcgm-exporter"
	validationManagedByLabel      = "app.kubernetes.io/managed-by"
	validationManagedByValue      = "dcgm-exporter-e2e"

	workloadPodName       = "cuda-workload"
	workloadContainerName = "cuda-workload"

	// Timeout constants for test operations
	podCreationTimeout         = 2 * time.Minute
	podReadinessTimeout        = 3 * time.Minute
	namespaceDeletionTimeout   = 90 * time.Second
	namespaceStuckCheckTimeout = 2 * time.Minute
	workloadPodDeletionTimeout = 45 * time.Second
	metricsReadTimeout         = 1 * time.Minute
	metricsWaitTimeout         = 90 * time.Second
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
		validationManagedByLabel: validationManagedByValue,
		e2eRunIDLabel:            runID.String(),
	}
	kubeClient        *framework.KubeClient
	helmClient        *framework.HelmClient
	helmReleases      []string   // Track all installed Helm releases for cleanup
	helmReleasesMutex sync.Mutex // Protect concurrent access to helmReleases
)

type testContextType struct {
	kubeconfig         string
	chart              string
	exporterImage      string
	gpuOperatorImage   string
	k8sImagePullSecret string
	arguments          string
	namespace          string
	runtimeClass       string
	nodeSelectorKey    string
	nodeSelectorVal    string
	remoteDcgm         string
	dcgmImage          string
	busyboxImage       string
	cudaWorkloadImage  string
	dcgmNS             string
	dcgmName           string
	dcgmPort           string
	migEntityID        string
	migNVMLID          string
	unsupportedField   string
	resultMarkers      bool
	noCleanup          bool
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
