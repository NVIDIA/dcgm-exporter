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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// unattributedObservation is how long the exporter must keep a pod off every
// GPU before the scenario accepts that it does not attribute the pod.
const unattributedObservation = 30 * time.Second

// shouldReadWorkloadAttribution reads the identity Kubernetes attribution must
// put on the workload pod's GPU: the one GPU nvidia-smi lists inside the pod,
// and the pod's name, namespace and container. It leaves PodUID empty; set it
// from workload.UID when pod UID collection is on.
func shouldReadWorkloadAttribution(ctx context.Context, workload *corev1.Pod) metriccontract.PodAttribution {
	Expect(workload).NotTo(BeNil(), "attribution needs the workload pod")

	gpus, err := nvidiasmi.QueryGPUIdentity(ctx, podNVIDIASMIRunner(workload))
	Expect(err).NotTo(HaveOccurred(), "setup: read the workload pod's GPU with nvidia-smi")
	Expect(gpus).To(HaveLen(1), "the workload pod requests one GPU, so nvidia-smi in the pod should list one")

	fmt.Fprintf(GinkgoWriter, "Workload pod %s/%s (uid %s) has GPU %s\n", workload.Namespace, workload.Name, workload.UID, gpus[0].UUID)
	return metriccontract.PodAttribution{
		GPUUUID:   gpus[0].UUID,
		Pod:       workload.Name,
		Namespace: workload.Namespace,
		Container: workloadContainerName,
	}
}

// shouldGetWorkloadPod returns the current workload pod created by the shared
// install helpers.
func shouldGetWorkloadPod(ctx context.Context) *corev1.Pod {
	workload, err := kubeClient.GetPod(ctx, testContext.namespace, workloadPodName)
	Expect(err).NotTo(HaveOccurred(), "get workload pod")
	return workload
}

// shouldAttributeOnlyWorkloadGPU polls the exporter until every whole-GPU DCGM
// field sample of the workload's GPU names the workload pod and no other GPU
// does.
func shouldAttributeOnlyWorkloadGPU(ctx context.Context, exporterPod *corev1.Pod, want metriccontract.PodAttribution) {
	By("Attribution: checking that only the workload's GPU names the workload pod")
	Eventually(func(g Gomega) {
		families := shouldReadMetricFamilies(ctx, g, exporterPod)
		g.Expect(metriccontract.ValidatePodAttribution(families, want)).To(Succeed())
	}).WithPolling(5 * time.Second).Within(2 * time.Minute).WithContext(ctx).Should(Succeed())
	By("Attribution: completed")
}

// shouldNotAttributeWorkload checks, over unattributedObservation, that the
// exporter reports the workload's GPU and names the workload pod on no GPU.
func shouldNotAttributeWorkload(ctx context.Context, exporterPod *corev1.Pod, want metriccontract.PodAttribution) {
	By("Attribution: checking that no GPU names the workload pod")
	Consistently(func(g Gomega) {
		families := shouldReadMetricFamilies(ctx, g, exporterPod)
		g.Expect(metriccontract.ValidatePodNotAttributed(families, want)).To(Succeed())
	}).WithPolling(5 * time.Second).WithTimeout(unattributedObservation).WithContext(ctx).Should(Succeed())
	By("Attribution: completed")
}

// shouldReadMIGWorkloadAttribution reads the identity Kubernetes attribution
// must put on the workload pod's MIG instance: the parent GPU and GPU instance
// of the one MIG device nvidia-smi lists inside the pod, and the pod's name,
// namespace and container.
func shouldReadMIGWorkloadAttribution(ctx context.Context, workload *corev1.Pod) metriccontract.MIGPodAttribution {
	Expect(workload).NotTo(BeNil(), "attribution needs the workload pod")

	devices, err := nvidiasmi.QueryMIGDevices(ctx, podNVIDIASMIRunner(workload))
	Expect(err).NotTo(HaveOccurred(), "setup: read the workload pod's MIG device with nvidia-smi")
	Expect(devices).To(HaveLen(1), "the workload pod requests one MIG device, so nvidia-smi in the pod should list one")

	fmt.Fprintf(GinkgoWriter, "Workload pod %s/%s has GPU instance %s of GPU %s\n",
		workload.Namespace, workload.Name, devices[0].GPUInstanceID, devices[0].GPUUUID)
	return metriccontract.MIGPodAttribution{
		PodAttribution: metriccontract.PodAttribution{
			GPUUUID:   devices[0].GPUUUID,
			Pod:       workload.Name,
			Namespace: workload.Namespace,
			Container: workloadContainerName,
		},
		GPUInstanceID: devices[0].GPUInstanceID,
	}
}

// shouldAttributeOnlyWorkloadMIGInstance polls the exporter until every DCGM
// field sample of the workload's MIG instance names the workload pod, no other
// GPU or MIG instance sample does, and no sample without a GPU UUID carries pod
// labels. nonGPU must hold in the same scrape, so the non-GPU check never
// passes on an empty set.
func shouldAttributeOnlyWorkloadMIGInstance(
	ctx context.Context, exporterPod *corev1.Pod, want metriccontract.MIGPodAttribution,
	nonGPU []metriccontract.FamilyContract,
) {
	By("Attribution: checking that only the workload's MIG instance names the workload pod")
	Eventually(func(g Gomega) {
		families := shouldReadMetricFamilies(ctx, g, exporterPod)
		g.Expect(metriccontract.ValidateContracts(families, nonGPU)).
			To(Succeed(), "the scrape must have non-GPU samples to check")
		g.Expect(metriccontract.ValidateMIGPodAttribution(families, want)).To(Succeed())
	}).WithPolling(5 * time.Second).Within(2 * time.Minute).WithContext(ctx).Should(Succeed())
	By("Attribution: completed")
}

// shouldReadMetricFamilies scrapes the exporter and parses the response.
func shouldReadMetricFamilies(ctx context.Context, g Gomega, exporterPod *corev1.Pod) map[string]*dto.MetricFamily {
	families, err := metriccontract.ParseText(shouldReadMetrics(ctx, kubeClient, exporterPod, dcgmExporterPort))
	g.Expect(err).NotTo(HaveOccurred(), "metrics should parse as Prometheus text")
	return families
}

// shouldCreateDRAWorkload creates a ResourceClaimTemplate-backed GPU workload for DRA attribution.
func shouldCreateDRAWorkload(ctx context.Context) {
	Expect(testContext.cudaWorkloadImage).NotTo(BeEmpty(), "E2E_CUDA_WORKLOAD_IMAGE or -cuda-workload-image is required")
	manifest := renderRegularDRAWorkloadManifest(
		testContext.namespace,
		testContext.cudaWorkloadImage,
		getE2ENodeSelector(),
	)
	shouldRunKubectlWithInput(ctx, manifest, "apply", "-f", "-")
	Eventually(func(g Gomega) {
		output := shouldRunKubectl(ctx, "-n", testContext.namespace, "get", "pod", workloadPodName, "-o", "jsonpath={.status.phase}")
		g.Expect(output).To(Equal("Running"))
	}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalNormal).Should(Succeed())
}

// renderRegularDRAWorkloadManifest builds the live workload from the current suite identity.
func renderRegularDRAWorkloadManifest(namespace, image string, nodeSelector map[string]string) string {
	return renderRegularDRAWorkloadManifestForIdentity(
		namespace, image, nodeSelector, runID.String(), workloadPodName, workloadContainerName,
	)
}

// readRegularDRAAllocation reads the live claim selected by the workload pod.
func readRegularDRAAllocation(ctx context.Context) (regularDRAAllocation, string, error) {
	podOutput, err := runKubectlAllowOutput(ctx, "-n", testContext.namespace, "get", "pod", workloadPodName, "-o", "json")
	if err != nil {
		return regularDRAAllocation{}, "", fmt.Errorf("read regular DRA workload pod: %w", err)
	}
	claimName, workloadNode, err := regularDRAWorkloadClaimAndNode([]byte(podOutput))
	if err != nil {
		return regularDRAAllocation{}, "", err
	}

	claimOutput, err := runKubectlAllowOutput(ctx, "-n", testContext.namespace, "get", "resourceclaim", claimName, "-o", "json")
	if err != nil {
		return regularDRAAllocation{}, "", fmt.Errorf("read regular DRA ResourceClaim %q: %w", claimName, err)
	}
	allocation, err := regularDRAAllocationFromClaim([]byte(claimOutput), testContext.namespace, claimName)
	if err != nil {
		return regularDRAAllocation{}, "", err
	}
	return allocation, workloadNode, nil
}
