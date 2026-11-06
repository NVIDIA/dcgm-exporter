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
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

const (
	// kubectlGetTimeout bounds one kubectlGetJSON call, so a stalled API
	// request cannot hold a polling callback past its own deadline.
	kubectlGetTimeout = 30 * time.Second

	gpuOperatorNamespace             = "gpu-operator"
	gpuOperatorDCGMContainer         = "nvidia-dcgm-ctr"
	gpuOperatorMainMetricsConfigName = "dcgm-exporter-e2e-metrics"

	// gpuOperatorRemoteHostengineEnv is the variable the operator sets on the
	// exporter container to point it at a standalone hostengine.
	gpuOperatorRemoteHostengineEnv = "DCGM_REMOTE_HOSTENGINE_INFO"

	// gpuOperatorDRAHostengine is the hostengine the operator's GPUCluster
	// points the exporter at.
	gpuOperatorDRAHostengine = "nvidia-dcgm-dra:5555"
)

// nodeShowsMIG reports whether a node shows MIG in use: a MIG resource, a MIG
// GPU product label, or a successful MIG configuration other than all-disabled.
// The operator reports the success state after applying the all-disabled layout
// too, and labels such as nvidia.com/mig.capable appear on every labeled node,
// so neither alone shows that MIG is on.
func nodeShowsMIG(node corev1.Node) bool {
	for _, resources := range []corev1.ResourceList{node.Status.Capacity, node.Status.Allocatable} {
		for name := range resources {
			if strings.HasPrefix(string(name), migResourcePrefix) {
				return true
			}
		}
	}

	labels := node.Labels
	if strings.Contains(labels["nvidia.com/gpu.product"], "-MIG-") {
		return true
	}

	config, hasConfig := labels["nvidia.com/mig.config"]

	return labels["nvidia.com/mig.config.state"] == "success" && hasConfig && config != "all-disabled"
}

var _ = Describe("GPU Operator integration", Ordered, func() {
	Context("GPU Operator chart resources are installed", Ordered, Label("gpuOperatorChart"), func() {
		It("should expose a ClusterPolicy", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				policies, err := kubectlGetJSON(ctx, "get", "clusterpolicy")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(clusterPolicyReadyError(policies)).To(Succeed())
			}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})
	})

	Context("GPU Operator exporter is running", Ordered, Label("gpuOperatorExporter"), func() {
		It("should roll out the operator-managed exporter DaemonSet with the image under test", func(ctx context.Context) {
			shouldMatchGPUOperatorExporterRollout(ctx)
		})
	})

	Context("GPU Operator shared GPU integration", Ordered, Label("gpuOperatorSharedGpu"), func() {
		It("should advertise shared GPU or time-slicing labels", func(ctx context.Context) {
			// The expected replica count comes from the device plugin config
			// that the ClusterPolicy names.
			policies, err := kubectlGetJSON(ctx, "get", "clusterpolicy")
			Expect(err).NotTo(HaveOccurred())
			configName, configKey, err := clusterPolicyDevicePluginConfig(policies)
			Expect(err).NotTo(HaveOccurred())
			configMap, err := kubectlGetJSON(ctx, "-n", gpuOperatorNamespace, "get", "configmap", configName)
			Expect(err).NotTo(HaveOccurred())
			replicas, err := timeSlicingGPUReplicas(configMap, configKey)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				nodes, err := kubectlGetJSON(ctx, "get", "nodes")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sharedGPUNodesError(nodes, replicas)).To(Succeed())
			}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})
	})

	Context("GPU Operator MIG integration", Ordered, Label("gpuOperatorMig"), func() {
		It("should advertise MIG resources or a successful MIG configuration", func(ctx context.Context) {
			output := shouldRunKubectl(ctx, "get", "nodes", "-o", "json")

			var nodes corev1.NodeList
			Expect(json.Unmarshal([]byte(output), &nodes)).To(Succeed())
			Expect(nodes.Items).To(ContainElement(Satisfy(nodeShowsMIG)))
		})
	})

	Context("GPU Operator DRA integration", Ordered, Label("gpuOperatorDRA"), func() {
		It("should expose DRA resources", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				// Name the v1 API so a cluster without it fails with "not served".
				deviceClasses, err := kubectlGetJSON(ctx, "get", "deviceclasses.v1.resource.k8s.io")
				g.Expect(err).NotTo(HaveOccurred())
				resourceSlices, err := kubectlGetJSON(ctx, "get", "resourceslices.v1.resource.k8s.io")
				g.Expect(err).NotTo(HaveOccurred())
				nodes, err := kubectlGetJSON(ctx, "get", "nodes")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(draResourcesError(deviceClasses, resourceSlices, nodes)).To(Succeed())
			}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})
	})

	Context("GPU Operator IPv6 exporter integration", Ordered, Label("gpuOperatorIPv6"), func() {
		It("should expose operator-managed exporter metrics over IPv6 service networking", func(ctx context.Context) {
			service := shouldFindGPUOperatorExporterService(ctx)
			ipv6 := shouldFindIPv6ClusterIP(service)
			port := shouldFindExporterServicePort(service)
			probePod := shouldCreateGPUOperatorProbePod(ctx, "gpu-operator-ipv6-probe")
			defer shouldDeleteProbePod(ctx, probePod)

			metrics := shouldScrapeIPv6ServiceFromPod(ctx, probePod, ipv6, port)
			Expect(baselineMetricContractError(metrics, false)).To(Succeed())
		})
	})

	Context("GPU Operator main chart embedded custom metrics and pod metadata", Ordered, Label("gpuOperatorMainEmbedded"), func() {
		AfterAll(func(ctx context.Context) {
			shouldCleanupWorkloadPods(ctx, kubeClient)
		})

		It("should reconcile the custom metrics and metadata settings", func(ctx context.Context) {
			policies, err := kubectlGetJSON(ctx, "get", "clusterpolicy")
			Expect(err).NotTo(HaveOccurred())
			Expect(clusterPolicyEmbeddedExporterError(policies, gpuOperatorMainMetricsConfigName, "^e2e_allowed$")).To(Succeed())

			shouldMatchGPUOperatorExporterRollout(ctx)
		})

		It("should scrape the custom ratio metric with only allowed pod metadata", func(ctx context.Context) {
			shouldCreateWorkloadPod(ctx, kubeClient, map[string]string{
				"e2e_allowed": "present",
				"e2e_blocked": "hidden",
			})
			service := shouldFindGPUOperatorExporterService(ctx)
			probePod := shouldCreateGPUOperatorProbePod(ctx, "gpu-operator-main-embedded-probe")
			defer shouldDeleteProbePod(ctx, probePod)
			workloadOutput := shouldRunKubectl(ctx, "-n", testContext.namespace, "get", "pod", workloadPodName, "-o", "json")
			var workload corev1.Pod
			Expect(json.Unmarshal([]byte(workloadOutput), &workload)).To(Succeed())
			exporter := shouldFindGPUOperatorExporterPodOnNode(ctx, workload.Spec.NodeName)
			Eventually(func(g Gomega) {
				serviceMetrics := shouldReadGPUOperatorServiceMetricsFromProbePod(ctx, probePod, service)
				exporterMetrics := shouldReadGPUOperatorExporterMetrics(ctx, exporter)
				g.Expect(errors.Join(
					metricsHaveFamilyName(serviceMetrics, "DCGM_FI_DEV_GPU_UTIL_RATIO"),
					metricsHaveLabelValue(exporterMetrics, "e2e_allowed", "present"),
					metricsHaveAnyLabel(exporterMetrics, "pod_uid"),
					metricsDoNotHaveAnyLabel(exporterMetrics, "e2e_blocked"),
				)).To(Succeed())
			}).WithTimeout(metricsWaitTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})
	})

	Context("GPU Operator main chart remote DCGM custom metrics", Ordered, Label("gpuOperatorMainRemote"), func() {
		It("should reconcile a remote hostengine and scrape the custom ratio metric", func(ctx context.Context) {
			policies, err := kubectlGetJSON(ctx, "get", "clusterpolicy")
			Expect(err).NotTo(HaveOccurred())
			Expect(clusterPolicyRemoteDCGMError(policies, gpuOperatorMainMetricsConfigName)).To(Succeed())

			pod := shouldFindGPUOperatorExporterPod(ctx)
			Expect(exporterContainerEnvError(*pod, gpuOperatorRemoteHostengineEnv, "")).To(Succeed())

			Eventually(func(g Gomega) {
				metrics := shouldReadGPUOperatorExporterMetrics(ctx, pod)
				g.Expect(metricsHaveFamilyName(metrics, "DCGM_FI_DEV_GPU_UTIL_RATIO")).To(Succeed())
			}).WithTimeout(metricsWaitTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})

		It("should scrape and remove a DCGM field policy", func(ctx context.Context) {
			exporter := shouldFindGPUOperatorExporterPod(ctx)
			hostengine := shouldFindGPUOperatorDCGMPod(ctx, exporter)
			policyID := shouldCreateGPUOperatorFieldPolicy(ctx, hostengine)
			DeferCleanup(func(cleanupCtx context.Context) {
				shouldDeleteGPUOperatorFieldPolicy(context.WithoutCancel(cleanupCtx), hostengine, policyID)
			})

			Eventually(func(g Gomega) {
				metrics := shouldReadGPUOperatorExporterMetrics(ctx, exporter)
				g.Expect(errors.Join(
					metricsHaveFamilyName(metrics, "DCGM_EXP_FIELD_POLICY_ENABLED"),
					metricsHaveFamilyName(metrics, "DCGM_EXP_FIELD_POLICY_CURRENT_VALUE"),
					metricsHaveFamilyName(metrics, "DCGM_EXP_FIELD_POLICY_THRESHOLD"),
					metricsHaveFamilyName(metrics, "DCGM_EXP_FIELD_POLICY_VIOLATION_COUNT"),
					metricsHaveFamilyName(metrics, "DCGM_EXP_FIELD_POLICY_LAST_TRIGGER_TIMESTAMP_SECONDS"),
					metricsHaveLabelValue(metrics, "policy_id", strconv.FormatUint(policyID, 10)),
					metricsHaveLabelValue(metrics, "field_id", "150"),
				)).To(Succeed())
			}).WithTimeout(metricsWaitTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})
	})

	Context("GPU Operator main chart GPUCluster DRA attribution", Ordered, Label("gpuOperatorMainDRA"), func() {
		AfterAll(func(ctx context.Context) {
			shouldCleanupWorkloadPods(ctx, kubeClient)
		})

		It("should allocate a ResourceClaim and expose DRA-attributed metrics", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				clusters, err := kubectlGetJSON(ctx, "get", "gpucluster")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(gpuClusterReadyError(clusters)).To(Succeed())
			}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())

			shouldCreateDRAWorkload(ctx)
			var allocation regularDRAAllocation
			var workloadNode string
			Eventually(func(g Gomega) {
				var err error
				allocation, workloadNode, err = readRegularDRAAllocation(ctx)
				g.Expect(err).NotTo(HaveOccurred())
			}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalNormal).Should(Succeed())

			pod := shouldFindGPUOperatorExporterPodOnNode(ctx, workloadNode)
			Expect(exporterContainerEnvError(*pod, gpuOperatorRemoteHostengineEnv, gpuOperatorDRAHostengine)).To(Succeed())

			Eventually(func(g Gomega) {
				metrics := shouldReadGPUOperatorExporterMetrics(ctx, pod)
				g.Expect(regularDRAAttributionContract(metrics, regularDRAMetricExpectation{
					Pod:                  workloadPodName,
					Namespace:            testContext.namespace,
					Container:            workloadContainerName,
					regularDRAAllocation: allocation,
				})).To(Succeed())
			}).WithTimeout(metricsWaitTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
		})
	})
})

// shouldFindGPUOperatorExporterService finds the operator-managed dcgm-exporter Service.
func shouldFindGPUOperatorExporterService(ctx context.Context) corev1.Service {
	output, err := kubectlGetJSON(ctx, "-n", gpuOperatorNamespace, "get", "svc")
	Expect(err).NotTo(HaveOccurred())
	var services corev1.ServiceList
	Expect(json.Unmarshal(output, &services)).To(Succeed())

	var fallback corev1.Service
	for _, service := range services.Items {
		if !isGPUOperatorExporterName(service.Name) {
			continue
		}
		if shouldFindExporterServicePort(service) != 0 {
			if serviceHasIPv6ClusterIP(service) {
				return service
			}
			fallback = service
		}
	}
	if fallback.Name != "" {
		return fallback
	}

	Fail(fmt.Sprintf("no GPU Operator dcgm-exporter service exposing port %d found in %s", dcgmExporterPort, gpuOperatorNamespace))
	return corev1.Service{}
}

// shouldMatchGPUOperatorExporterRollout waits until the operator-managed
// exporter DaemonSet is fully ready and runs the image under test.
func shouldMatchGPUOperatorExporterRollout(ctx context.Context) {
	expectedImage, err := gpuOperatorExpectedExporterImage(testContext.gpuOperatorImage, testContext.exporterImage)
	Expect(err).NotTo(HaveOccurred())

	Eventually(func(g Gomega) {
		daemonSets, err := kubectlGetJSON(ctx, "-n", gpuOperatorNamespace, "get", "daemonsets")
		g.Expect(err).NotTo(HaveOccurred())
		pods, err := kubectlGetJSON(ctx, "-n", gpuOperatorNamespace, "get", "pods")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(gpuOperatorExporterRolloutError(daemonSets, pods, expectedImage)).To(Succeed())
	}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
}

// shouldListGPUOperatorPods returns the pods in the GPU Operator namespace.
func shouldListGPUOperatorPods(ctx context.Context) corev1.PodList {
	output, err := kubectlGetJSON(ctx, "-n", gpuOperatorNamespace, "get", "pods")
	Expect(err).NotTo(HaveOccurred())

	var pods corev1.PodList
	Expect(json.Unmarshal(output, &pods)).To(Succeed())
	return pods
}

// shouldFindGPUOperatorExporterPod finds a running operator-managed exporter pod.
func shouldFindGPUOperatorExporterPod(ctx context.Context) *corev1.Pod {
	pods := shouldListGPUOperatorPods(ctx)
	for index := range pods.Items {
		pod := &pods.Items[index]
		if isGPUOperatorExporterName(pod.Name) && pod.Status.Phase == corev1.PodRunning {
			return pod
		}
	}
	Fail("no running GPU Operator dcgm-exporter pod found")
	return nil
}

// shouldFindGPUOperatorExporterPodOnNode finds the operator-managed exporter on one workload node.
func shouldFindGPUOperatorExporterPodOnNode(ctx context.Context, nodeName string) *corev1.Pod {
	Expect(strings.TrimSpace(nodeName)).NotTo(BeEmpty())
	var exporterPod *corev1.Pod
	Eventually(func(g Gomega) {
		output, err := kubectlGetJSON(ctx, "-n", gpuOperatorNamespace, "get", "pods")
		g.Expect(err).NotTo(HaveOccurred())
		var pods corev1.PodList
		g.Expect(json.Unmarshal(output, &pods)).To(Succeed())

		exporterPod = nil
		for index := range pods.Items {
			pod := &pods.Items[index]
			if isGPUOperatorExporterName(pod.Name) &&
				pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName == nodeName {
				exporterPod = pod
				break
			}
		}
		g.Expect(exporterPod).NotTo(BeNil(),
			fmt.Sprintf("no running GPU Operator exporter pod runs on workload node %q", nodeName))
	}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalNormal).Should(Succeed())
	return exporterPod
}

// shouldFindGPUOperatorDCGMPod returns the remote host engine on the exporter's node.
func shouldFindGPUOperatorDCGMPod(ctx context.Context, exporter *corev1.Pod) *corev1.Pod {
	output := shouldRunKubectl(ctx, "-n", gpuOperatorNamespace, "get", "pods", "-l", "app=nvidia-dcgm", "-o", "json")
	var pods corev1.PodList
	Expect(json.Unmarshal([]byte(output), &pods)).To(Succeed())
	for index := range pods.Items {
		pod := &pods.Items[index]
		if pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName == exporter.Spec.NodeName {
			return pod
		}
	}
	Fail(fmt.Sprintf("no running GPU Operator DCGM pod found on exporter node %q", exporter.Spec.NodeName))
	return nil
}

// shouldCreateGPUOperatorFieldPolicy creates a non-triggering temperature policy for scrape validation.
func shouldCreateGPUOperatorFieldPolicy(ctx context.Context, hostengine *corev1.Pod) uint64 {
	output := shouldRunKubectl(ctx,
		"-n", gpuOperatorNamespace,
		"exec", hostengine.Name, "-c", gpuOperatorDCGMContainer, "--",
		"dcgmi", "policy", "--host", "localhost", "--create",
		"--field", "150", "--name", "dcgm-exporter-e2e-policy",
		"--threshold", "1000", "--operator", "gt", "--entities", "all",
		"--channels", "console", "--rate-limit", "1", "--json",
	)
	var response struct {
		PolicyID uint64 `json:"policyId"`
	}
	Expect(json.Unmarshal([]byte(output), &response)).To(Succeed(), "dcgmi field-policy create output: %q", output)
	Expect(response.PolicyID).NotTo(BeZero())
	return response.PolicyID
}

// shouldDeleteGPUOperatorFieldPolicy deletes the policy created by this test only.
func shouldDeleteGPUOperatorFieldPolicy(ctx context.Context, hostengine *corev1.Pod, policyID uint64) {
	shouldRunKubectl(ctx,
		"-n", gpuOperatorNamespace,
		"exec", hostengine.Name, "-c", gpuOperatorDCGMContainer, "--",
		"dcgmi", "policy", "--host", "localhost", "--delete",
		"--field", "150", "--policy-id", strconv.FormatUint(policyID, 10),
	)
	policies := shouldRunKubectl(ctx,
		"-n", gpuOperatorNamespace,
		"exec", hostengine.Name, "-c", gpuOperatorDCGMContainer, "--",
		"dcgmi", "policy", "--host", "localhost", "--list", "--json",
	)
	Expect(policies).NotTo(ContainSubstring("dcgm-exporter-e2e-policy"))
}

// shouldReadGPUOperatorExporterMetrics scrapes an operator-managed exporter pod through its own namespace.
func shouldReadGPUOperatorExporterMetrics(ctx context.Context, pod *corev1.Pod) []byte {
	var metrics []byte
	Eventually(func(g Gomega) {
		var err error
		metrics, err = kubeClient.DoHTTPRequest(ctx, gpuOperatorNamespace, pod.Name, dcgmExporterPort, "metrics")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(metrics).NotTo(BeEmpty())
	}).WithTimeout(metricsReadTimeout).WithPolling(pollingIntervalFast).Should(Succeed())
	return metrics
}

// shouldReadGPUOperatorServiceMetricsFromProbePod scrapes the operator-managed exporter Service from a ready BusyBox probe pod.
func shouldReadGPUOperatorServiceMetricsFromProbePod(ctx context.Context, podName string, service corev1.Service) []byte {
	Expect(service.Name).NotTo(BeEmpty(), "GPU Operator exporter service name is required")
	Expect(service.Namespace).NotTo(BeEmpty(), "GPU Operator exporter service namespace is required")
	port := shouldFindExporterServicePort(service)
	Expect(port).NotTo(BeZero(), "GPU Operator exporter service must expose the metrics port")

	endpoint := fmt.Sprintf("http://%s.%s.svc:%d/metrics", service.Name, service.Namespace, port)
	var metrics string
	Eventually(func(g Gomega) {
		var err error
		metrics, err = runKubectlAllowOutput(
			ctx,
			"-n", testContext.namespace,
			"exec", podName,
			"--", "wget", "-qO-", "-T", "10", endpoint,
		)
		g.Expect(err).NotTo(HaveOccurred(), metrics)
		g.Expect(metrics).NotTo(BeEmpty())
	}).WithTimeout(metricsReadTimeout).WithPolling(pollingIntervalFast).Should(Succeed())
	return []byte(metrics)
}

// shouldFindExporterServicePort finds the metrics port on an exporter Service.
func shouldFindExporterServicePort(service corev1.Service) int32 {
	for _, port := range service.Spec.Ports {
		if uint(port.Port) == dcgmExporterPort {
			return port.Port
		}
	}
	return 0
}

// shouldFindIPv6ClusterIP finds an IPv6 ClusterIP on an exporter Service.
func shouldFindIPv6ClusterIP(service corev1.Service) string {
	for index, family := range service.Spec.IPFamilies {
		if family == corev1.IPv6Protocol && index < len(service.Spec.ClusterIPs) {
			return service.Spec.ClusterIPs[index]
		}
	}
	for _, ip := range service.Spec.ClusterIPs {
		if strings.Contains(ip, ":") {
			return ip
		}
	}

	Fail(fmt.Sprintf("GPU Operator exporter service %s/%s has no IPv6 ClusterIP; families=%v clusterIPs=%v",
		service.Namespace, service.Name, service.Spec.IPFamilies, service.Spec.ClusterIPs))
	return ""
}

// serviceHasIPv6ClusterIP reports whether a Service publishes any IPv6 cluster address.
func serviceHasIPv6ClusterIP(service corev1.Service) bool {
	for _, ip := range service.Spec.ClusterIPs {
		if strings.Contains(ip, ":") {
			return true
		}
	}
	return false
}

// shouldCreateGPUOperatorProbePod starts a temporary BusyBox pod for in-cluster service scraping.
func shouldCreateGPUOperatorProbePod(ctx context.Context, name string) string {
	shouldDeleteProbePod(ctx, name)
	shouldRunKubectl(
		ctx,
		"-n", testContext.namespace,
		"run", name,
		"--image="+requiredBusyBoxImage(),
		"--restart=Never",
		"--command",
		"--", "sh", "-c", "sleep 3600",
	)
	shouldRunKubectl(ctx, "-n", testContext.namespace, "wait", "--for=condition=Ready", "pod/"+name, "--timeout=90s")
	return name
}

// shouldDeleteProbePod removes a temporary GPU Operator probe pod if present.
func shouldDeleteProbePod(ctx context.Context, name string) {
	_ = shouldRunKubectlAllowFailure(ctx, "-n", testContext.namespace, "delete", "pod", name, "--ignore-not-found=true", "--wait=false")
}

// shouldScrapeIPv6ServiceFromPod scrapes exporter metrics through an IPv6 ClusterIP.
func shouldScrapeIPv6ServiceFromPod(ctx context.Context, podName, ipv6 string, port int32) []byte {
	command := fmt.Sprintf("wget -qO- -T 10 'http://[%s]:%d/metrics'", ipv6, port)
	var output string
	Eventually(func(g Gomega) {
		var err error
		output, err = runKubectlAllowOutput(ctx, "-n", testContext.namespace, "exec", podName, "--", "sh", "-c", command)
		g.Expect(err).NotTo(HaveOccurred(), output)
		g.Expect(output).NotTo(BeEmpty())
		g.Expect(baselineMetricContractError([]byte(output), false)).To(Succeed())
	}).WithPolling(pollingIntervalSlow).Within(metricsWaitTimeout).Should(Succeed())
	return []byte(output)
}

// shouldRunKubectl runs kubectl and fails the spec if the command exits non-zero.
func shouldRunKubectl(ctx context.Context, args ...string) string {
	allArgs := append([]string{"--kubeconfig", testContext.kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("kubectl %s failed: %s", strings.Join(args, " "), string(out)))
	return string(out)
}

// kubectlGetJSON runs kubectl with the given arguments plus "-o json" and
// returns stdout only, so warnings kubectl prints on stderr cannot corrupt the
// JSON. The error carries stderr. The call runs under kubectlGetTimeout, both
// as a command deadline and as kubectl's own request timeout.
func kubectlGetJSON(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, kubectlGetTimeout)
	defer cancel()

	allArgs := append([]string{"--kubeconfig", testContext.kubeconfig, "--request-timeout", kubectlGetTimeout.String()}, args...)
	allArgs = append(allArgs, "-o", "json")
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl %s -o json: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}

// shouldRunKubectlAllowFailure runs kubectl and returns only the command error.
func shouldRunKubectlAllowFailure(ctx context.Context, args ...string) error {
	_, err := runKubectlAllowOutput(ctx, args...)
	return err
}

// runKubectlAllowOutput runs kubectl and returns combined output with the command error.
func runKubectlAllowOutput(ctx context.Context, args ...string) (string, error) {
	allArgs := append([]string{"--kubeconfig", testContext.kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// shouldRunKubectlWithInput runs kubectl with stdin and fails the spec on command errors.
func shouldRunKubectlWithInput(ctx context.Context, input string, args ...string) string {
	allArgs := append([]string{"--kubeconfig", testContext.kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allArgs...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("kubectl %s failed: %s", strings.Join(args, " "), string(out)))
	return string(out)
}
