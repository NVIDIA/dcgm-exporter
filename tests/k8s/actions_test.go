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
	"errors"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/imageref"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/k8s/internal/framework"
)

// shouldCreateK8SConfig loads the suite kubeconfig into a REST client configuration.
func shouldCreateK8SConfig() *restclient.Config {
	config, err := clientcmd.BuildConfigFromFlags("", testContext.kubeconfig)
	Expect(err).ShouldNot(HaveOccurred(), "unable to load kubeconfig from %s; err: %s",
		testContext.kubeconfig, err)
	return config
}

// shouldResolvePath normalizes the kubeconfig path before Kubernetes clients are created.
func shouldResolvePath() {
	var err error
	testContext.kubeconfig, err = framework.ResolvePath(testContext.kubeconfig)
	Expect(err).ShouldNot(HaveOccurred(),
		"cannot resolve path to kubeconfig: %s, err: %v", testContext.kubeconfig, err)
}

// shouldCreateNamespace creates a clean environment for this run, resetting a
// same-run namespace left behind by an interrupted invocation.
func shouldCreateNamespace(ctx context.Context, kubeClient *framework.KubeClient, labels map[string]string) {
	By(fmt.Sprintf("Creating namespace: %q started.", testContext.namespace))

	pullSecret := shouldResetOwnedNamespace(ctx, kubeClient)
	_, err := kubeClient.CreateNamespace(ctx, testContext.namespace, labels)
	if k8serrors.IsAlreadyExists(err) {
		shouldAcceptConcurrentNamespace(ctx, kubeClient)
		shouldRestorePullSecret(ctx, kubeClient, pullSecret)
		return
	}
	Expect(err).ShouldNot(HaveOccurred(), "Creating namespace: failed")
	shouldRestorePullSecret(ctx, kubeClient, pullSecret)
	By(fmt.Sprintf("Creating namespace: %q completed\n", testContext.namespace))
}

// shouldResetOwnedNamespace deletes, or waits out, an existing namespace this run
// owns so setup starts clean. It returns the configured pull Secret to restore.
func shouldResetOwnedNamespace(ctx context.Context, kubeClient *framework.KubeClient) *corev1.Secret {
	existingNamespace, err := kubeClient.GetNamespace(ctx, testContext.namespace)
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		// A non-NotFound error is normally connectivity or authorization failure.
		Fail(fmt.Sprintf("Creating namespace: Failed to check if namespace %q exists due to error: %v", testContext.namespace, err))
	}
	if !namespaceOwnedByRun(existingNamespace) {
		Fail(fmt.Sprintf(
			"Creating namespace: namespace %q is not owned by run %q",
			testContext.namespace,
			testContext.runName,
		))
	}
	phase := existingNamespace.Status.Phase
	if phase != corev1.NamespaceActive && phase != corev1.NamespaceTerminating {
		Fail(fmt.Sprintf("Creating namespace: namespace %q has unexpected phase %q", testContext.namespace, phase))
	}
	var pullSecret *corev1.Secret
	if testContext.k8sImagePullSecret != "" {
		pullSecret, err = snapshotPullSecret(ctx, kubeClient, testContext.namespace, testContext.k8sImagePullSecret)
		Expect(err).ShouldNot(HaveOccurred(), "snapshot image pull Secret %s/%s", testContext.namespace, testContext.k8sImagePullSecret)
	}
	if phase == corev1.NamespaceActive {
		By(fmt.Sprintf("Creating namespace: resetting owned environment %q", testContext.namespace))
		shouldDeleteNamespace(ctx, kubeClient, testContext.namespace)
		return pullSecret
	}
	By(fmt.Sprintf("Creating namespace: waiting for terminating environment %q", testContext.namespace))
	Eventually(func() bool {
		_, getErr := kubeClient.GetNamespace(ctx, testContext.namespace)
		return k8serrors.IsNotFound(getErr)
	}).WithTimeout(namespaceDeletionTimeout).WithPolling(pollingIntervalSlow).Should(BeTrue(),
		fmt.Sprintf("Creating namespace: namespace %q did not finish deleting", testContext.namespace))
	return pullSecret
}

// shouldAcceptConcurrentNamespace accepts a namespace created concurrently only when this run owns it and it is active.
func shouldAcceptConcurrentNamespace(ctx context.Context, kubeClient *framework.KubeClient) {
	existingNamespace, err := kubeClient.GetNamespace(ctx, testContext.namespace)
	Expect(err).ShouldNot(HaveOccurred(), "Creating namespace: failed to inspect a concurrent namespace")
	if !namespaceOwnedByRun(existingNamespace) {
		Fail(fmt.Sprintf("Creating namespace: concurrent namespace %q is not owned by run %q", testContext.namespace, testContext.runName))
	}
	if existingNamespace.Status.Phase != corev1.NamespaceActive {
		Fail(fmt.Sprintf("Creating namespace: concurrent namespace %q is in phase %q", testContext.namespace, existingNamespace.Status.Phase))
	}
}

// shouldRestorePullSecret recreates the pull Secret captured before the namespace was reset.
func shouldRestorePullSecret(ctx context.Context, kubeClient *framework.KubeClient, pullSecret *corev1.Secret) {
	if pullSecret == nil {
		return
	}
	Expect(restorePullSecret(ctx, kubeClient, testContext.namespace, testContext.k8sImagePullSecret, pullSecret)).To(Succeed(),
		"restore image pull Secret %s/%s", testContext.namespace, testContext.k8sImagePullSecret)
}

// namespaceOwnedByRun requires both harness ownership labels to match the current run.
func namespaceOwnedByRun(namespace *corev1.Namespace) bool {
	return namespace != nil &&
		namespace.Labels[validationManagedByLabel] == validationManagedByValue &&
		namespace.Labels[e2eRunNameLabel] == testContext.runName
}

// shouldCreateKubeClient constructs the framework Kubernetes client for the suite.
func shouldCreateKubeClient(config *restclient.Config) *framework.KubeClient {
	kubeClient, err := framework.NewKubeClient(config)
	Expect(err).ShouldNot(HaveOccurred(), "cannot create k8s client: %s", err)
	return kubeClient
}

// kubeConfigShouldExists fails early when the configured kubeconfig path is missing.
func kubeConfigShouldExists() {
	if _, err := os.Stat(testContext.kubeconfig); os.IsNotExist(err) {
		Fail(fmt.Sprintf("kubeconfig file does not exist: %s", testContext.kubeconfig))
	}
}

// shouldCreateHelmClient constructs the framework Helm client for the test namespace and chart.
func shouldCreateHelmClient(config *restclient.Config) *framework.HelmClient {
	helmClient, err := framework.NewHelmClient(
		framework.HelmWithNamespace(testContext.namespace),
		framework.HelmWithKubeConfig(config),
		framework.HelmWithChart(testContext.chart),
	)
	Expect(err).ShouldNot(HaveOccurred(), "Creating helm client: %q failed",
		testContext.namespace)

	return helmClient
}

// addHelmRelease safely adds a release name to the helmReleases slice.
func addHelmRelease(releaseName string) {
	helmReleasesMutex.Lock()
	defer helmReleasesMutex.Unlock()
	helmReleases = append(helmReleases, releaseName)
}

// clearHelmReleases safely clears the helmReleases slice.
func clearHelmReleases() {
	helmReleasesMutex.Lock()
	defer helmReleasesMutex.Unlock()
	helmReleases = nil
}

// getHelmReleases safely returns a copy of the helmReleases slice.
func getHelmReleases() []string {
	helmReleasesMutex.Lock()
	defer helmReleasesMutex.Unlock()
	releases := make([]string, len(helmReleases))
	copy(releases, helmReleases)
	return releases
}

// shouldInstallHelmChart installs the exporter chart with default and scenario values.
func shouldInstallHelmChart(ctx context.Context, helmClient *framework.HelmClient, additionalValues []string) string {
	return shouldInstallHelmChartWithJSONValues(ctx, helmClient, additionalValues, nil)
}

// shouldInstallHelmChartWithJSONValues installs the exporter chart with scalar and JSON values.
func shouldInstallHelmChartWithJSONValues(
	ctx context.Context,
	helmClient *framework.HelmClient,
	additionalValues []string,
	additionalJSONValues []string,
) string {
	By(fmt.Sprintf("Helm chart installation: %q chart started.", testContext.chart))

	// Get default values and merge with additional ones
	values := getDefaultHelmValues()
	if len(additionalValues) > 0 {
		values = mergeHelmValues(values, additionalValues)
	}

	helmReleaseName, err := helmClient.Install(
		ctx, framework.HelmChartOptions{
			CleanupOnFail: false,
			GenerateName:  true,
			Timeout:       helmInstallTimeout,
			Wait:          true,
			DryRun:        false,
		},
		framework.WithValues(values...),
		framework.WithStringValues(getDefaultHelmStringValues()...),
		framework.WithJSONValues(additionalJSONValues...),
	)
	Expect(err).ShouldNot(HaveOccurred(), "Helm chart installation: %q chart failed with error: %v", testContext.chart, err)

	By(fmt.Sprintf("Helm chart installation: %q completed.", testContext.chart))
	By(fmt.Sprintf("Helm chart installation: new %q release name.", helmReleaseName))

	// Track the release for cleanup
	addHelmRelease(helmReleaseName)

	return helmReleaseName
}

// shouldInstallPreviousChart installs the last released chart from its
// published repository with the suite's default values plus additionalValues,
// and returns the release name. The values must use that chart's own keys.
func shouldInstallPreviousChart(ctx context.Context, additionalValues []string) string {
	By(fmt.Sprintf("Helm chart installation: released chart %s started.", previousChartVersion))

	previousClient, err := framework.NewHelmClient(
		framework.HelmWithNamespace(testContext.namespace),
		framework.HelmWithKubeConfig(shouldCreateK8SConfig()),
		framework.HelmWithChart(previousChartRepositoryName+"/"+chartName),
	)
	Expect(err).ShouldNot(HaveOccurred(), "Creating helm client for the released chart")
	defer func() {
		if cleanupErr := previousClient.Cleanup(); cleanupErr != nil {
			By(fmt.Sprintf("Warning: released chart client cleanup failed: %v", cleanupErr))
		}
	}()

	Expect(previousClient.AddRepository(previousChartRepositoryName, previousChartRepositoryURL)).To(Succeed())

	values := mergeHelmValues(getDefaultHelmValues(), additionalValues)
	releaseName, err := previousClient.Install(
		ctx, framework.HelmChartOptions{
			GenerateName: true,
			Version:      previousChartVersion,
			Timeout:      helmInstallTimeout,
			Wait:         true,
		},
		framework.WithValues(values...),
		framework.WithStringValues(getDefaultHelmStringValues()...),
	)
	Expect(err).ShouldNot(HaveOccurred(), "Helm chart installation: released chart %s failed", previousChartVersion)

	addHelmRelease(releaseName)

	return releaseName
}

// shouldUpgradeHelmChart upgrades an existing release with merged default and scenario-specific values.
func shouldUpgradeHelmChart(ctx context.Context, helmClient *framework.HelmClient, releaseName string, additionalValues []string, additionalJSONValues []string) string {
	By(fmt.Sprintf("Helm chart upgrade: %q release started.", releaseName))

	values := getDefaultHelmValues()
	if len(additionalValues) > 0 {
		values = mergeHelmValues(values, additionalValues)
	}

	helmReleaseName, err := helmClient.Upgrade(
		ctx, releaseName, framework.HelmChartOptions{
			CleanupOnFail: false,
			Timeout:       helmInstallTimeout,
			Wait:          true,
			DryRun:        false,
		},
		framework.WithValues(values...),
		framework.WithStringValues(getDefaultHelmStringValues()...),
		framework.WithJSONValues(additionalJSONValues...),
	)
	Expect(err).ShouldNot(HaveOccurred(), "Helm chart upgrade: %q release failed with error: %v", releaseName, err)

	By(fmt.Sprintf("Helm chart upgrade: %q completed.", helmReleaseName))
	return helmReleaseName
}

// mergeHelmValues merges Helm values by key, with later values overriding earlier ones.
func mergeHelmValues(defaultValues, additionalValues []string) []string {
	// Use a map to track the latest value for each key
	valueMap := make(map[string]string, len(defaultValues)+len(additionalValues))

	// Process default values first
	for _, value := range defaultValues {
		if key, val := parseKeyValue(value); key != "" {
			valueMap[key] = val
		}
	}

	// Process additional values, which will override default values
	for _, value := range additionalValues {
		if key, val := parseKeyValue(value); key != "" {
			valueMap[key] = val
		}
	}

	// Convert map back to slice with deterministic ordering
	result := make([]string, 0, len(valueMap))

	// Extract and sort keys for consistent ordering
	keys := make([]string, 0, len(valueMap))
	for key := range valueMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		result = append(result, key+"="+valueMap[key])
	}

	return result
}

// parseKeyValue parses a Helm set value in key=value format.
func parseKeyValue(s string) (key, value string) {
	if s == "" {
		return "", ""
	}

	if k, v, found := strings.Cut(s, "="); found {
		return k, v
	}

	return "", ""
}

// shouldUninstallHelmChart removes one tracked Helm release if it exists.
func shouldUninstallHelmChart(helmClient *framework.HelmClient, helmReleaseName string) {
	if helmClient != nil && helmReleaseName != "" {
		By(fmt.Sprintf("Helm chart uninstall: release %q of the helm chart: %q started.",
			helmReleaseName,
			testContext.chart))

		err := helmClient.Uninstall(helmReleaseName)
		if err != nil {
			// Log warning but don't fail - release might already be deleted
			By(fmt.Sprintf("Warning: Helm chart uninstall: release %q failed with error: %v", helmReleaseName, err))
		} else {
			By(fmt.Sprintf("Helm chart uninstall: release %q of the helm chart: %q completed.",
				helmReleaseName,
				testContext.chart))
		}
	}
}

// shouldCleanupHelmClient releases framework Helm client state after a suite or context.
func shouldCleanupHelmClient(helmClient *framework.HelmClient) {
	if helmClient != nil {
		err := helmClient.Cleanup()
		if err != nil {
			// Log warning but don't fail - cleanup might fail due to already cleaned up resources
			By(fmt.Sprintf("Warning: Helm Client cleanup failed: %v", err))
		}
	}
}

// shouldCleanupWorkloadPods deletes workload and debug-reader pods created by specs.
func shouldCleanupWorkloadPods(ctx context.Context, kubeClient *framework.KubeClient) {
	By("Cleaning up workload pods")

	// Delete workload pods by name
	workloadPods := []string{workloadPodName, debugDumpReaderPodName}

	for _, podName := range workloadPods {
		By(fmt.Sprintf("Cleaning up workload pod: %q", podName))
		err := kubeClient.DeletePod(ctx, testContext.namespace, podName)
		if err != nil {
			// Log warning but don't fail - pod might already be deleted
			By(fmt.Sprintf("Warning: Failed to delete workload pod %q: %v", podName, err))
		} else {
			By(fmt.Sprintf("Successfully deleted workload pod: %q", podName))
		}
	}

	// Wait for pods to be fully deleted
	By("Waiting for workload pods to be fully deleted")
	Eventually(func() bool {
		for _, podName := range workloadPods {
			// Use CheckPodStatus to check if pod exists
			_, err := kubeClient.CheckPodStatus(ctx, testContext.namespace, podName, func(namespace, podName string, status corev1.PodStatus) (bool, error) {
				// If we get here, the pod exists
				return true, nil
			})
			if err == nil {
				// Pod still exists
				return false
			}
			// Check if the error is specifically a "NotFound" error
			if !k8serrors.IsNotFound(err) {
				// Other errors (network, auth, etc.) should not be treated as success
				return false
			}
		}
		// All pods are deleted
		return true
	}).WithTimeout(workloadPodDeletionTimeout).WithPolling(pollingIntervalFast).Should(BeTrue(),
		"Workload pods were not deleted within the timeout period")

	By("Workload pod cleanup completed")
}

// shouldCleanupHelmResources deletes shared ConfigMaps that may be left by scenario setup.
func shouldCleanupHelmResources(ctx context.Context, kubeClient *framework.KubeClient) {
	By("Cleaning up Helm resources")

	// Clean up ConfigMap that might be left from previous tests
	By("Cleaning up ConfigMap from previous tests")
	err := kubeClient.DeleteConfigMap(ctx, testContext.namespace, hpcJobMappingConfigMapName)
	if err != nil && !k8serrors.IsNotFound(err) {
		By(fmt.Sprintf("Warning: Failed to delete ConfigMap %q: %v", hpcJobMappingConfigMapName, err))
	}

	By("Helm resources cleanup completed")
}

// shouldDeleteNamespace deletes a configured test environment by name and
// waits for completion. Repeated cleanup succeeds when the namespace is gone.
func shouldDeleteNamespace(ctx context.Context, kubeClient *framework.KubeClient, namespaceName string) {
	if kubeClient == nil || namespaceName == "" {
		return
	}
	By(fmt.Sprintf("Namespace deletion: %q namespace started.", namespaceName))
	namespace, err := kubeClient.GetNamespace(ctx, namespaceName)
	if k8serrors.IsNotFound(err) {
		By(fmt.Sprintf("Namespace deletion: %q namespace already absent.", namespaceName))
		return
	}
	Expect(err).ShouldNot(HaveOccurred(), "Namespace deletion: failed to inspect ownership")
	if !namespaceOwnedByRun(namespace) {
		By(fmt.Sprintf("Namespace deletion: %q is not owned by run %q; skipping.", namespaceName, testContext.runName))
		return
	}
	err = kubeClient.DeleteNamespace(ctx, namespaceName, namespace.UID)
	Expect(err).ShouldNot(HaveOccurred(), "Namespace deletion: failed")
	By(fmt.Sprintf("Namespace deletion: %q deletion initiated.", namespaceName))

	By(fmt.Sprintf("Namespace deletion: %q waiting for completion.", namespaceName))

	Eventually(func() bool {
		// Directly check if namespace exists - more efficient than listing pods.
		_, err := kubeClient.GetNamespace(ctx, namespaceName)
		if err == nil {
			// Namespace still exists.
			return false
		}
		// Check if the error is specifically a NotFound error.
		if k8serrors.IsNotFound(err) {
			// Namespace was successfully deleted.
			return true
		}
		// Other errors (network, auth, etc.) should not be treated as success.
		return false
	}).WithTimeout(namespaceDeletionTimeout).WithPolling(pollingIntervalSlow).Should(BeTrue(),
		fmt.Sprintf("Namespace deletion: Namespace %q was not deleted within the timeout period.", namespaceName))

	By(fmt.Sprintf("Namespace deletion: %q namespace fully deleted.", namespaceName))
}

// shouldFindExporterPod waits for a ready, non-terminating exporter Pod in
// scenarios that do not create a workload to anchor the scrape to a node.
func shouldFindExporterPod(
	ctx context.Context, kubeClient *framework.KubeClient, labels map[string]string,
) *corev1.Pod {
	By("Pod creation verification: started")

	var dcgmExpPod corev1.Pod

	Eventually(func(ctx context.Context) error {
		pods, err := kubeClient.GetPodsByLabel(ctx, testContext.namespace, labels)
		if err != nil {
			return fmt.Errorf("list exporter pods: %w", err)
		}

		dcgmExpPod, err = firstReadyExporterPod(pods)
		return err
	}).WithPolling(pollingIntervalFast).Within(podCreationTimeout + podReadinessTimeout).WithContext(ctx).Should(Succeed())

	By("Pod creation verification: completed")

	return &dcgmExpPod
}

// shouldFindReadyExporterPodOnNode waits for the ready exporter that observes
// nodeName. Workload scenarios use it to avoid reading metrics from another node.
func shouldFindReadyExporterPodOnNode(
	ctx context.Context, kubeClient *framework.KubeClient, nodeName string,
) *corev1.Pod {
	Expect(strings.TrimSpace(nodeName)).NotTo(BeEmpty(), "workload node is required to select its local exporter")
	By(fmt.Sprintf("Node-local exporter selection: waiting for an exporter on node %q", nodeName))

	var exporterPod corev1.Pod
	Eventually(func(ctx context.Context) error {
		pods, err := kubeClient.GetPodsByLabel(ctx, testContext.namespace, dcgmExporterPodLabels)
		if err != nil {
			return fmt.Errorf("list dcgm-exporter pods: %w", err)
		}
		exporterPod, err = readyExporterPodOnNode(pods, nodeName)
		if err != nil {
			return fmt.Errorf(
				"%w; exporter status on node %q: %s",
				err,
				nodeName,
				exporterPodReadinessSummaryOnNode(pods, nodeName),
			)
		}
		return nil
	}).WithPolling(pollingIntervalNormal).Within(podReadinessTimeout).WithContext(ctx).Should(Succeed())

	By(fmt.Sprintf("Node-local exporter selection: found ready exporter %q", exporterPod.Name))
	return &exporterPod
}

// shouldFindReadyExporterPodForWorkload selects the ready exporter on the node
// assigned to workload. Call it after the workload reaches Running.
func shouldFindReadyExporterPodForWorkload(
	ctx context.Context, kubeClient *framework.KubeClient, workload *corev1.Pod,
) *corev1.Pod {
	Expect(workload).NotTo(BeNil(), "workload is required to select its node-local exporter")
	return shouldFindReadyExporterPodOnNode(ctx, kubeClient, workload.Spec.NodeName)
}

// shouldCheckIfReplacementPodCreated waits for a newly created replacement pod after deletion.
func shouldCheckIfReplacementPodCreated(
	ctx context.Context,
	kubeClient *framework.KubeClient,
	labels map[string]string,
	previousPod *corev1.Pod,
) *corev1.Pod {
	By("Replacement pod creation verification: started")

	var replacementPod corev1.Pod

	Eventually(func(ctx context.Context) error {
		pods, err := kubeClient.GetPodsByLabel(ctx, testContext.namespace, labels)
		if err != nil {
			return fmt.Errorf("list replacement exporter pods: %w", err)
		}

		replacementPod, err = replacementExporterPod(pods, *previousPod)
		return err
	}).WithPolling(pollingIntervalFast).Within(podCreationTimeout).WithContext(ctx).Should(Succeed())

	By("Replacement pod creation verification: completed")

	return &replacementPod
}

// getDefaultHelmValues returns Helm values common to all exporter e2e installs.
func getDefaultHelmValues() []string {
	values := []string{
		fmt.Sprintf("serviceMonitor.enabled=%v", false),
		// Set resource requests to avoid scheduling delays and OOMKilled
		"resources.requests.cpu=1000m",
		"resources.requests.memory=1024Mi",
		"resources.limits.cpu=1000m",
		"resources.limits.memory=1024Mi",
		// Optimize image pull policy for faster startup
		"image.pullPolicy=IfNotPresent",
	}

	if testContext.arguments != "" {
		values = append(values, fmt.Sprintf("arguments=%s", testContext.arguments))
	}

	if testContext.exporterImage != "" {
		ref, err := imageref.Parse(testContext.exporterImage)
		Expect(err).NotTo(HaveOccurred())
		values = append(values, fmt.Sprintf("image.repository=%s", ref.Repository()))
		if ref.Tag() != "" {
			values = append(values, fmt.Sprintf("image.tag=%s", ref.Tag()))
		}
		if ref.Digest() != "" {
			values = append(values, fmt.Sprintf("image.digest=%s", ref.Digest()))
		}
	}

	if testContext.k8sImagePullSecret != "" {
		values = append(values, fmt.Sprintf("imagePullSecrets[0].name=%s", testContext.k8sImagePullSecret))
	}

	if testContext.runtimeClass != "" {
		values = append(values, fmt.Sprintf("runtimeClassName=%s", testContext.runtimeClass))
	}
	values = appendNvidiaVisibleDevicesHelmValues(values, testContext.nvidiaVisibleDevices)

	return values
}

// getDefaultHelmStringValues returns Helm --set-string values common to e2e installs.
func getDefaultHelmStringValues() []string {
	var values []string
	if testContext.nodeSelectorKey != "" || testContext.nodeSelectorVal != "" {
		Expect(testContext.nodeSelectorKey).NotTo(BeEmpty(),
			"node selector value requires node selector key")
		Expect(testContext.nodeSelectorVal).NotTo(BeEmpty(),
			"node selector key requires node selector value")
		values = append(values,
			fmt.Sprintf("nodeSelector.%s=%s", escapeHelmSetKey(testContext.nodeSelectorKey), testContext.nodeSelectorVal))
	}

	return values
}

// escapeHelmSetKey preserves dotted Kubernetes label keys in Helm --set paths.
func escapeHelmSetKey(key string) string {
	return strings.ReplaceAll(key, ".", `\.`)
}

// getE2ENodeSelector returns the suite node selector used by exporter and workload pods.
func getE2ENodeSelector() map[string]string {
	if testContext.nodeSelectorKey == "" && testContext.nodeSelectorVal == "" {
		return nil
	}

	Expect(testContext.nodeSelectorKey).NotTo(BeEmpty(),
		"node selector value requires node selector key")
	Expect(testContext.nodeSelectorVal).NotTo(BeEmpty(),
		"node selector key requires node selector value")

	return map[string]string{
		testContext.nodeSelectorKey: testContext.nodeSelectorVal,
	}
}

// shouldCheckIfPodIsReady waits for one pod to become Ready and surfaces crash details.
func shouldCheckIfPodIsReady(ctx context.Context, kubeClient *framework.KubeClient, namespace, podName string) {
	By("Checking pod status: started")
	lastStatus := "pod status has not been observed"
	Eventually(func(ctx context.Context) string {
		isReady, err := kubeClient.CheckPodStatus(ctx,
			namespace,
			podName,
			func(namespace, podName string, status corev1.PodStatus) (bool, error) {
				for _, c := range status.Conditions {
					if c.Type != corev1.PodReady {
						continue
					}
					if c.Status == corev1.ConditionTrue {
						return true, nil
					}
				}
				lastStatus = podReadinessSummary(status)

				for _, c := range status.ContainerStatuses {
					if c.State.Waiting != nil && c.State.Waiting.Reason == "CrashLoopBackOff" {
						// Extract detailed crash information
						crashDetails := fmt.Sprintf("pod %s in namespace %s is in CrashLoopBackOff", podName, namespace)

						// Add container name if available
						if c.Name != "" {
							crashDetails += fmt.Sprintf(" (container: %s)", c.Name)
						}

						// Add waiting reason details if available
						if c.State.Waiting.Message != "" {
							crashDetails += fmt.Sprintf(" - %s", c.State.Waiting.Message)
						}

						// Check for terminated state with exit code and reason
						if c.LastTerminationState.Terminated != nil {
							term := c.LastTerminationState.Terminated
							crashDetails += fmt.Sprintf(" - Last exit code: %d", term.ExitCode)
							if term.Reason != "" {
								crashDetails += fmt.Sprintf(", reason: %s", term.Reason)
							}
							if term.Message != "" {
								crashDetails += fmt.Sprintf(", message: %s", term.Message)
							}
						}

						// Grab logs from the crashed pod for additional debugging
						if c.Name != "" {
							// Get last 100 lines of logs to limit memory usage
							tailLines := int64(100)
							logs, err := kubeClient.GetPodLogs(ctx, namespace, podName, c.Name, &tailLines)
							if err != nil {
								crashDetails += fmt.Sprintf(" - Failed to get logs: %v", err)
							} else if logs != "" {
								crashDetails += fmt.Sprintf(" - Last 100 lines of logs:\n%s", logs)
							} else {
								crashDetails += " - No logs available"
							}
						}

						return false, errors.New(crashDetails)
					}
				}

				return false, nil
			})
		if err != nil {
			return fmt.Sprintf("error: %v", err)
		}
		if isReady {
			return "ready"
		}
		return lastStatus
	}).WithPolling(pollingIntervalNormal).Within(podReadinessTimeout).WithContext(ctx).Should(Equal("ready"))
	By("Checking pod status: completed")
}

// shouldCreateWorkloadPod creates a long-running workload that requests one
// nvidia.com/gpu and returns its current Pod object after Kubernetes assigns
// its node.
func shouldCreateWorkloadPod(ctx context.Context, kubeClient *framework.KubeClient, labels map[string]string) *corev1.Pod {
	return shouldCreateWorkloadPodRequesting(ctx, kubeClient, labels, framework.NVIDIAGPUResourceName, "")
}

// shouldCreateWorkloadPodRequesting creates a long-running workload that
// requests one unit of gpuResourceName and returns its current Pod object
// after Kubernetes assigns its node. A non-empty nodeName pins the workload to
// that node, in addition to the suite's node selector.
func shouldCreateWorkloadPodRequesting(
	ctx context.Context, kubeClient *framework.KubeClient, labels map[string]string, gpuResourceName corev1.ResourceName,
	nodeName string,
) *corev1.Pod {
	By("Workload pod creation: started")
	Expect(testContext.cudaWorkloadImage).NotTo(BeEmpty(), "E2E_CUDA_WORKLOAD_IMAGE or -cuda-workload-image is required")

	nodeSelector := maps.Clone(getE2ENodeSelector())
	if nodeName != "" {
		if nodeSelector == nil {
			nodeSelector = map[string]string{}
		}
		nodeSelector[corev1.LabelHostname] = nodeName
	}

	// Create a long-running pod instead of a completing one
	workloadPod, err := kubeClient.CreatePod(
		ctx,
		testContext.namespace,
		labels,
		workloadPodName,
		workloadContainerName,
		testContext.cudaWorkloadImage,
		testContext.runtimeClass,
		nodeSelector,
		gpuResourceName,
	)

	Expect(err).ShouldNot(HaveOccurred(),
		"Workload pod creation: Failed create workload pod with err: %v", err)

	// Wait for the pod to be running instead of completed
	Eventually(func(ctx context.Context) bool {
		isReady, err := kubeClient.CheckPodStatus(ctx,
			testContext.namespace,
			workloadPod.Name, func(namespace, podName string, status corev1.PodStatus) (bool, error) {
				return status.Phase == corev1.PodRunning, nil
			})
		if err != nil {
			Fail(fmt.Sprintf("Workload pod creation: Checking pod status: Failed with error: %v", err))
		}

		return isReady
	}).WithPolling(pollingIntervalNormal).Within(podReadinessTimeout).WithContext(ctx).Should(BeTrue())

	currentWorkload, err := kubeClient.GetPod(ctx, testContext.namespace, workloadPod.Name)
	Expect(err).NotTo(HaveOccurred(), "get scheduled workload pod")
	Expect(strings.TrimSpace(currentWorkload.Spec.NodeName)).NotTo(BeEmpty(), "running workload must have an assigned node")

	By("Workload pod creation: completed - long-running pod is now active")
	return currentWorkload
}

// shouldReadMetrics scrapes metrics from the exporter pod through the Kubernetes client.
func shouldReadMetrics(ctx context.Context, kubeClient *framework.KubeClient, dcgmExpPod *corev1.Pod, dcgmExporterPort uint) []byte {
	By("Read metrics: started")

	var metricsResponse []byte

	Eventually(func(ctx context.Context) bool {
		var err error

		metricsResponse, err = kubeClient.DoHTTPRequest(ctx,
			testContext.namespace,
			dcgmExpPod.Name,
			dcgmExporterPort,
			"metrics")
		if err != nil {
			Fail(fmt.Sprintf("Read metrics: Failed with error: %v", err))
		}

		return len(metricsResponse) > 0
	}).WithPolling(pollingIntervalFast).Within(metricsReadTimeout).WithContext(ctx).Should(BeTrue())

	By("Read metrics: completed")

	return metricsResponse
}

// cleanupTestContext removes per-context workloads and Helm releases without deleting the namespace.
func cleanupTestContext(ctx context.Context, kubeClient *framework.KubeClient, helmClient *framework.HelmClient) {
	// Each context releases its workloads and Helm charts; --no-cleanup retains the suite environment.
	By("Test context cleanup: Starting cleanup")

	// Cleanup runs on the Ginkgo test goroutine so assertion failures are
	// reported to the active spec instead of panicking in a background goroutine.
	By("Test context cleanup: Cleaning up workload pods")
	shouldCleanupWorkloadPods(ctx, kubeClient)

	By("Test context cleanup: Uninstalling Helm releases")
	releases := getHelmReleases()
	for _, releaseName := range releases {
		if releaseName != "" {
			By(fmt.Sprintf("Test context cleanup: Uninstalling Helm release %q", releaseName))
			shouldUninstallHelmChart(helmClient, releaseName)
		}
	}
	// Clear the releases list
	clearHelmReleases()

	// Clean up any remaining Helm resources
	By("Test context cleanup: Cleaning up Helm resources")
	shouldCleanupHelmResources(ctx, kubeClient)

	By("Test context cleanup: Cleanup completed")
}

// safeCleanup performs comprehensive cleanup that can be called multiple times safely.
func safeCleanup(ctx context.Context, kubeClient *framework.KubeClient, helmClient *framework.HelmClient, deleteNamespace bool) {
	if testContext.noCleanup {
		By("Safe cleanup: Retaining test resources (--no-cleanup)")
		return
	}
	By("Safe cleanup: Starting comprehensive cleanup")

	// Keep assertion-bearing cleanup on the active Ginkgo goroutine.
	By("Safe cleanup: Cleaning up workload pods")
	shouldCleanupWorkloadPods(ctx, kubeClient)

	By("Safe cleanup: Uninstalling Helm releases")
	releases := getHelmReleases()
	for _, releaseName := range releases {
		if releaseName != "" {
			By(fmt.Sprintf("Safe cleanup: Uninstalling Helm release %q", releaseName))
			shouldUninstallHelmChart(helmClient, releaseName)
		}
	}
	// Clear the releases list
	clearHelmReleases()

	// Clean up any remaining Helm resources
	By("Safe cleanup: Cleaning up Helm resources")
	shouldCleanupHelmResources(ctx, kubeClient)

	// Clean up Helm client
	By("Safe cleanup: Cleaning up Helm client")
	shouldCleanupHelmClient(helmClient)

	// Delete namespace only if requested (typically only from AfterSuite)
	if deleteNamespace {
		By("Safe cleanup: Deleting namespace")
		shouldDeleteNamespace(ctx, kubeClient, testContext.namespace)
	}

	By("Safe cleanup: Comprehensive cleanup completed")
}

// shouldWaitForMetrics waits until exporter metrics include at least one real GPU family.
func shouldWaitForMetrics(ctx context.Context, kubeClient *framework.KubeClient, dcgmExpPod *corev1.Pod, dcgmExporterPort uint) {
	By("Waiting for metrics to be available")

	Eventually(func(ctx context.Context) string {
		metricsResponse, err := kubeClient.DoHTTPRequest(ctx,
			testContext.namespace,
			dcgmExpPod.Name,
			dcgmExporterPort,
			"metrics")
		if err != nil {
			return fmt.Sprintf("scrape failed: %v", err)
		}

		families, err := metriccontract.ParseText(metricsResponse)
		if err != nil {
			return fmt.Sprintf("metrics parse failed: %v; response: %s", err, metricsResponseSnippet(metricsResponse))
		}

		if err := metriccontract.ValidateAtLeastOneDCGMGPUFamily(families); err != nil {
			return fmt.Sprintf("%v; response: %s", err, metricsResponseSnippet(metricsResponse))
		}
		return "ready"
	}).WithPolling(pollingIntervalNormal).Within(metricsWaitTimeout).WithContext(ctx).Should(Equal("ready"),
		"Metrics endpoint did not return meaningful data within timeout")

	By("Metrics are now available")
}

// metricsResponseSnippet returns a compact response excerpt for timeout diagnostics.
func metricsResponseSnippet(metricsResponse []byte) string {
	const maxSnippetLength = 800
	snippet := strings.TrimSpace(string(metricsResponse))
	snippet = strings.Join(strings.Fields(snippet), " ")
	if len(snippet) > maxSnippetLength {
		return snippet[:maxSnippetLength] + "...<truncated>"
	}
	if snippet == "" {
		return "<empty response>"
	}
	return snippet
}
