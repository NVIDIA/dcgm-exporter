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
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/dcgm-exporter/tests/e2e/internal/framework"
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
	By(fmt.Sprintf("Creating namespace: %q started.", testContext.namespace))

	// First, check if namespace already exists and is stuck
	existingNamespace, err := kubeClient.GetNamespace(ctx, testContext.namespace)
	if err == nil {
		// Namespace exists, check if it's stuck in terminating state
		if existingNamespace.Status.Phase == corev1.NamespaceTerminating {
			By(fmt.Sprintf("Creating namespace: %q exists but is stuck in terminating state, waiting for cleanup.", testContext.namespace))
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
				fmt.Sprintf("Creating namespace: Namespace %q was not deleted within the timeout period.", testContext.namespace))
		} else {
			// Namespace exists and is active, clean it up first to ensure a clean state
			By(fmt.Sprintf("Creating namespace: %q already exists and is active, cleaning up first.", testContext.namespace))
			shouldDeleteNamespace(ctx, kubeClient)
		}
	} else if !k8serrors.IsNotFound(err) {
		// If the error is not "NotFound", it's likely a network, auth, or other issue
		// Fail the test instead of attempting to create the namespace
		Fail(fmt.Sprintf("Creating namespace: Failed to check if namespace %q exists due to error: %v", testContext.namespace, err))
	}

	// Now try to create the namespace (only if it doesn't exist or was cleaned up)
	_, err = kubeClient.CreateNamespace(ctx, testContext.namespace, labels)
	Expect(err).ShouldNot(HaveOccurred(), "Creating namespace: failed")
	By(fmt.Sprintf("Creating namespace: %q completed\n", testContext.namespace))
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

// addHelmRelease safely adds a release name to the helmReleases slice
func addHelmRelease(releaseName string) {
	helmReleasesMutex.Lock()
	defer helmReleasesMutex.Unlock()
	helmReleases = append(helmReleases, releaseName)
}

// clearHelmReleases safely clears the helmReleases slice
func clearHelmReleases() {
	helmReleasesMutex.Lock()
	defer helmReleasesMutex.Unlock()
	helmReleases = nil
}

// getHelmReleases safely returns a copy of the helmReleases slice
func getHelmReleases() []string {
	helmReleasesMutex.Lock()
	defer helmReleasesMutex.Unlock()
	releases := make([]string, len(helmReleases))
	copy(releases, helmReleases)
	return releases
}

func shouldInstallHelmChart(ctx context.Context, helmClient *framework.HelmClient, additionalValues []string) string {
	By(fmt.Sprintf("Helm chart installation: %q chart started.", testContext.chart))

	// Get default values and merge with additional ones
	values := getDefaultHelmValues()
	if len(additionalValues) > 0 {
		values = mergeHelmValues(values, additionalValues)
	}

	helmReleaseName, err := helmClient.Install(ctx, framework.HelmChartOptions{
		CleanupOnFail: true,
		GenerateName:  true,
		Timeout:       helmInstallTimeout,
		Wait:          true,
		DryRun:        false,
	}, framework.WithValues(values...))
	Expect(err).ShouldNot(HaveOccurred(), "Helm chart installation: %q chart failed with error: %v", testContext.chart, err)

	By(fmt.Sprintf("Helm chart installation: %q completed.", testContext.chart))
	By(fmt.Sprintf("Helm chart installation: new %q release name.", helmReleaseName))

	// Track the release for cleanup
	addHelmRelease(helmReleaseName)

	return helmReleaseName
}

// mergeHelmValues merges two slices of Helm values by key, where later values override earlier ones.
// Values are expected to be in the format "key=value".
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

	// Iterate over sorted keys to build result slice
	for _, key := range keys {
		result = append(result, key+"="+valueMap[key])
	}

	return result
}

// parseKeyValue parses a string in the format "key=value" and returns the key and value.
// Returns empty strings if the format is invalid.
func parseKeyValue(s string) (key, value string) {
	if s == "" {
		return "", ""
	}

	// Use strings.Cut for cleaner parsing (Go 1.18+)
	if k, v, found := strings.Cut(s, "="); found {
		return k, v
	}

	return "", ""
}

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

func shouldCleanupHelmClient(helmClient *framework.HelmClient) {
	if helmClient != nil {
		err := helmClient.Cleanup()
		if err != nil {
			// Log warning but don't fail - cleanup might fail due to already cleaned up resources
			By(fmt.Sprintf("Warning: Helm Client cleanup failed: %v", err))
		}
	}
}

func shouldCleanupWorkloadPods(ctx context.Context, kubeClient *framework.KubeClient) {
	By("Cleaning up workload pods")

	// Delete workload pods by name
	workloadPods := []string{workloadPodName}

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

func shouldCleanupHelmResources(ctx context.Context, kubeClient *framework.KubeClient) {
	By("Cleaning up Helm resources")

	// Clean up ConfigMap that might be left from previous tests
	By("Cleaning up ConfigMap from previous tests")
	err := kubeClient.DeleteConfigMap(ctx, testContext.namespace, "exporter-metrics-config-map")
	if err != nil && !k8serrors.IsNotFound(err) {
		By(fmt.Sprintf("Warning: Failed to delete ConfigMap: %v", err))
	}

	By("Helm resources cleanup completed")
}

func shouldDeleteNamespace(ctx context.Context, kubeClient *framework.KubeClient) {
	By(fmt.Sprintf("Namespace deletion: %q namespace started.", testContext.namespace))
	if kubeClient != nil {
		err := kubeClient.DeleteNamespace(ctx, testContext.namespace)
		if err != nil {
			// Log warning but don't fail - namespace might already be deleted
			By(fmt.Sprintf("Warning: Namespace deletion: Failed to delete namespace %q with error: %v", testContext.namespace, err))
		} else {
			By(fmt.Sprintf("Namespace deletion: %q deletion initiated.", testContext.namespace))
		}

		By(fmt.Sprintf("Namespace deletion: %q waiting for completion.", testContext.namespace))

		// Try normal deletion first
		deletionSucceeded := false
		Eventually(func() bool {
			// Directly check if namespace exists - more efficient than listing pods
			_, err := kubeClient.GetNamespace(ctx, testContext.namespace)
			if err == nil {
				// Namespace still exists
				return false
			}
			// Check if the error is specifically a "NotFound" error
			if k8serrors.IsNotFound(err) {
				// Namespace was successfully deleted
				deletionSucceeded = true
				return true
			}
			// Other errors (network, auth, etc.) should not be treated as success
			return false
		}).WithTimeout(namespaceDeletionTimeout).WithPolling(pollingIntervalSlow).Should(BeTrue(),
			fmt.Sprintf("Namespace deletion: Namespace %q was not deleted within the timeout period.", testContext.namespace))

		// If normal deletion failed, log a warning
		if !deletionSucceeded {
			By(fmt.Sprintf("Warning: Namespace deletion: %q stuck, but continuing without force cleanup.", testContext.namespace))
		}

		By(fmt.Sprintf("Namespace deletion: %q namespace fully deleted.", testContext.namespace))
	}
}

func shouldCheckIfPodCreated(
	ctx context.Context, kubeClient *framework.KubeClient, labels map[string]string,
) *corev1.Pod {
	By("Pod creation verification: started")

	var dcgmExpPod *corev1.Pod

	Eventually(func(ctx context.Context) bool {
		pods, err := kubeClient.GetPodsByLabel(ctx, testContext.namespace, labels)
		if err != nil {
			Fail(fmt.Sprintf("Pod creation: Failed with error: %v", err))
			return false
		}

		if len(pods) == 1 {
			dcgmExpPod = &pods[0]
			return true
		}

		return false
	}).WithPolling(pollingIntervalFast).Within(podCreationTimeout).WithContext(ctx).Should(BeTrue())

	By("Pod creation verification: completed")

	return dcgmExpPod
}

func getDefaultHelmValues() []string {
	values := []string{
		fmt.Sprintf("serviceMonitor.enabled=%v", false),
		// Set resource requests to avoid scheduling delays and OOMKilled
		"resources.requests.cpu=50m",
		"resources.requests.memory=256Mi",
		"resources.limits.cpu=200m",
		"resources.limits.memory=512Mi",
		// Optimize image pull policy for faster startup
		"image.pullPolicy=IfNotPresent",
		// Reduce probe delays for faster test execution
		"readinessProbe.initialDelaySeconds=10",
		"livenessProbe.initialDelaySeconds=10",
	}

	if testContext.arguments != "" {
		values = append(values, fmt.Sprintf("arguments=%s", testContext.arguments))
	}

	if testContext.imageRepository != "" {
		values = append(values, fmt.Sprintf("image.repository=%s", testContext.imageRepository))
	}

	if testContext.imageTag != "" {
		values = append(values, fmt.Sprintf("image.tag=%s", testContext.imageTag))
	}

	if testContext.runtimeClass != "" {
		values = append(values, fmt.Sprintf("runtimeClassName=%s", testContext.runtimeClass))
	}

	return values
}

func shouldCheckIfPodIsReady(ctx context.Context, kubeClient *framework.KubeClient, namespace, podName string) {
	By("Checking pod status: started")
	Eventually(func(ctx context.Context) bool {
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
			Fail(fmt.Sprintf("Checking pod status: Failed with error: %v", err))
		}

		return isReady
	}).WithPolling(pollingIntervalNormal).Within(podReadinessTimeout).WithContext(ctx).Should(BeTrue())
	By("Checking pod status: completed")
}

func shouldCreateWorkloadPod(ctx context.Context, kubeClient *framework.KubeClient, labels map[string]string) {
	By("Workload pod creation: started")

	// Create a long-running pod instead of a completing one
	workloadPod, err := kubeClient.CreatePod(ctx,
		testContext.namespace,
		labels,
		workloadPodName,
		workloadContainerName,
		workloadImage,
		testContext.runtimeClass,
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

	By("Workload pod creation: completed - long-running pod is now active")
}

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

// cleanupTestContext performs cleanup for individual test contexts without deleting the namespace
func cleanupTestContext(ctx context.Context, kubeClient *framework.KubeClient, helmClient *framework.HelmClient) {
	By("Test context cleanup: Starting cleanup")

	var wg sync.WaitGroup

	// Start workload pod cleanup in parallel with Helm operations
	wg.Add(1)
	go func() {
		defer wg.Done()
		By("Test context cleanup: Cleaning up workload pods (parallel)")
		shouldCleanupWorkloadPods(ctx, kubeClient)
	}()

	// Clean up Helm releases in parallel with workload cleanup
	By("Test context cleanup: Uninstalling Helm releases (parallel)")
	releases := getHelmReleases()
	for _, releaseName := range releases {
		if releaseName != "" {
			By(fmt.Sprintf("Test context cleanup: Uninstalling Helm release %q", releaseName))
			shouldUninstallHelmChart(helmClient, releaseName)
		}
	}
	// Clear the releases list
	clearHelmReleases()

	// Wait for workload cleanup to complete
	By("Test context cleanup: Waiting for workload cleanup to complete")
	wg.Wait()
	By("Test context cleanup: Workload cleanup completed")

	// Clean up any remaining Helm resources
	By("Test context cleanup: Cleaning up Helm resources")
	shouldCleanupHelmResources(ctx, kubeClient)

	By("Test context cleanup: Cleanup completed")
}

// safeCleanup performs comprehensive cleanup that can be called multiple times safely
func safeCleanup(ctx context.Context, kubeClient *framework.KubeClient, helmClient *framework.HelmClient, deleteNamespace bool) {
	By("Safe cleanup: Starting comprehensive cleanup")

	var wg sync.WaitGroup

	// Start workload pod cleanup in parallel with Helm operations
	wg.Add(1)
	go func() {
		defer wg.Done()
		By("Safe cleanup: Cleaning up workload pods (parallel)")
		shouldCleanupWorkloadPods(ctx, kubeClient)
	}()

	// Clean up Helm releases in parallel with workload cleanup
	By("Safe cleanup: Uninstalling Helm releases (parallel)")
	releases := getHelmReleases()
	for _, releaseName := range releases {
		if releaseName != "" {
			By(fmt.Sprintf("Safe cleanup: Uninstalling Helm release %q", releaseName))
			shouldUninstallHelmChart(helmClient, releaseName)
		}
	}
	// Clear the releases list
	clearHelmReleases()

	// Wait for workload cleanup to complete
	By("Safe cleanup: Waiting for workload cleanup to complete")
	wg.Wait()
	By("Safe cleanup: Workload cleanup completed")

	// Clean up any remaining Helm resources
	By("Safe cleanup: Cleaning up Helm resources")
	shouldCleanupHelmResources(ctx, kubeClient)

	// Clean up Helm client
	By("Safe cleanup: Cleaning up Helm client")
	shouldCleanupHelmClient(helmClient)

	// Delete namespace only if requested (typically only from AfterSuite)
	if deleteNamespace {
		By("Safe cleanup: Deleting namespace")
		shouldDeleteNamespace(ctx, kubeClient)
	}

	By("Safe cleanup: Comprehensive cleanup completed")
}

// shouldWaitForMetrics waits for metrics to be available by actively polling the metrics endpoint
func shouldWaitForMetrics(ctx context.Context, kubeClient *framework.KubeClient, dcgmExpPod *corev1.Pod, dcgmExporterPort uint) {
	By("Waiting for metrics to be available")

	Eventually(func(ctx context.Context) bool {
		metricsResponse, err := kubeClient.DoHTTPRequest(ctx,
			testContext.namespace,
			dcgmExpPod.Name,
			dcgmExporterPort,
			"metrics")
		if err != nil {
			return false
		}

		// Check if we have meaningful metrics (not just empty response)
		return len(metricsResponse) > 100 // At least 100 bytes to ensure we have actual metrics
	}).WithPolling(pollingIntervalNormal).Within(metricsWaitTimeout).WithContext(ctx).Should(BeTrue(),
		"Metrics endpoint did not return meaningful data within timeout")

	By("Metrics are now available")
}
