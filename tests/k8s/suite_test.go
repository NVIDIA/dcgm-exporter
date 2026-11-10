//go:build k8s

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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/k8s/internal/framework"
)

const (
	podLabel        = "pod"
	namespaceLabel  = "namespace"
	containerLabel  = "container"
	e2eRunIDLabel   = "e2eRunID"
	e2eRunNameLabel = "e2eRunName"

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
	workloadPodDeletionTimeout = 45 * time.Second
	metricsReadTimeout         = 1 * time.Minute
	metricsWaitTimeout         = 90 * time.Second
	helmInstallTimeout         = 5 * time.Minute
	httpClientTimeout          = 5 * time.Second

	// Polling interval constants
	pollingIntervalFast   = 500 * time.Millisecond
	pollingIntervalNormal = 1 * time.Second
	pollingIntervalSlow   = 2 * time.Second
)

var (
	expectedLabels        = []string{podLabel, namespaceLabel, containerLabel}
	dcgmExporterPodLabels = map[string]string{dcgmExporterPodNameLabel: dcgmExporterPodNameLabelValue}
	testRunLabels         = map[string]string{
		validationManagedByLabel: validationManagedByValue,
	}
	kubeClient        *framework.KubeClient
	helmClient        *framework.HelmClient
	helmReleases      []string   // Track all installed Helm releases for cleanup
	helmReleasesMutex sync.Mutex // Protect concurrent access to helmReleases
)

// testContextType holds command-line settings shared by the Ginkgo suite.
// Scenarios use its namespace names as stable environment identifiers.
type testContextType struct {
	kubeconfig             string
	runName                string
	chart                  string
	exporterImage          string
	gpuOperatorImage       string
	k8sImagePullSecret     string
	arguments              string
	namespace              string
	runtimeClass           string
	nvidiaVisibleDevices   string
	nodeSelectorKey        string
	nodeSelectorVal        string
	dcgmProbeBinary        string
	remoteDcgm             string
	dcgmImage              string
	busyboxImage           string
	cudaWorkloadImage      string
	dcgmNS                 string
	dcgmName               string
	dcgmPort               string
	migEntityID            string
	migNVMLID              string
	unsupportedField       string
	nvlinkFailureFieldName string
	resultMarkers          bool
	noCleanup              bool
	ownedLocalCluster      bool

	// defaultCounterRows and fieldSupport are parsed once in BeforeSuite.
	defaultCounterRows []metriccontract.DefaultCounterRow
	fieldSupport       metriccontract.FieldSupport
}

// BeforeSuite creates the client and converges the configured test namespace.
var _ = BeforeSuite(func(ctx context.Context) {
	shouldLoadDefaultCounterContract()

	if testContext.kubeconfig != "" {
		shouldResolvePath()
		kubeConfigShouldExists()
		k8sConfig := shouldCreateK8SConfig()
		kubeClient = shouldCreateKubeClient(k8sConfig)
		helmClient = shouldCreateHelmClient(k8sConfig)
		shouldCreateNamespace(ctx, kubeClient, testRunLabels)
	}
})

var _ = AfterSuite(func(ctx context.Context) {
	if testContext.noCleanup {
		By("After suite cleanup: retaining configured environments (--no-cleanup)")
		return
	}
	// Use safe cleanup that can be called multiple times without errors
	safeCleanup(ctx, kubeClient, helmClient, true)
	shouldDeleteNamespace(ctx, kubeClient, testContext.dcgmNS)
})
