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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

// shouldMatchGPUInventory compares the exporter's GPU identity labels and
// values with nvidia-smi. On the owned local cluster nvidia-smi runs on this
// host and lists every GPU the exporter sees; on any other cluster it runs in
// the workload pod and lists only that pod's GPU. The families compared are
// those of the suite's default counters CSV, and the hostname label is the
// exporter pod's node name.
func shouldMatchGPUInventory(ctx context.Context, workload, exporterPod *corev1.Pod) {
	By("GPU inventory: comparing exporter output with nvidia-smi")
	Expect(workload).NotTo(BeNil(), "GPU inventory needs the workload pod")
	Expect(exporterPod).NotTo(BeNil(), "GPU inventory needs the exporter pod")

	inventory, opts := gpuInventorySource(workload)
	opts.CSVRows = testContext.defaultCounterRows
	opts.ExpectedHostname = exporterPod.Spec.NodeName
	opts.HostnameSource = "exporter pod spec.nodeName"

	valueFields := metriccontract.ValueQueryFields(opts.CSVRows)
	var lastReport metriccontract.GPUInventoryReport
	Eventually(func(g Gomega) {
		gpus, err := inventory.Identity(ctx)
		failUnlessMissing(err)
		g.Expect(err).NotTo(HaveOccurred(), "nvidia-smi identity query failed")
		readings, err := inventory.Readings(ctx, valueFields)
		failUnlessMissing(err)
		g.Expect(err).NotTo(HaveOccurred(), "nvidia-smi readings query failed")

		metricsResponse := shouldReadMetrics(ctx, kubeClient, exporterPod, dcgmExporterPort)
		families, err := metriccontract.ParseText(metricsResponse)
		g.Expect(err).NotTo(HaveOccurred(), "metrics should parse as Prometheus text")

		lastReport = metriccontract.CompareGPUInventory(families, gpus, readings, opts)
		g.Expect(lastReport.Err()).To(Succeed())
	}).WithPolling(5 * time.Second).Within(2 * time.Minute).WithContext(ctx).Should(Succeed())

	for _, reason := range lastReport.Skipped {
		fmt.Fprintf(GinkgoWriter, "GPU inventory comparison skipped: %s\n", reason)
	}
	By("GPU inventory: completed")
}

// failUnlessMissing stops the retries when nvidia-smi is not installed, on this
// host or in the workload pod. Any other error, such as an NVML or exec
// failure, is left for the caller to report so the poll tries again.
func failUnlessMissing(err error) {
	if err == nil || !nvidiaSmiMissing(err) {
		return
	}
	StopTrying("setup: GPU inventory needs nvidia-smi").Wrap(err).Now()
}

// nvidiaSmiMissing reports whether err says nvidia-smi is not installed. In a
// pod the runtime reports that as an exec error whose text names the missing
// executable.
func nvidiaSmiMissing(err error) bool {
	return nvidiasmi.IsMissing(err) || strings.Contains(err.Error(), "executable file not found")
}

// localInventory reads GPU data from nvidia-smi on this host. Its identity read
// is shared by every comparison in the test process.
var localInventory = nvidiasmi.NewInventory(nvidiasmi.LocalRunner)

// gpuInventorySource picks where nvidia-smi runs: on this host for the owned
// local cluster, otherwise inside the workload pod.
func gpuInventorySource(workload *corev1.Pod) (*nvidiasmi.Inventory, metriccontract.GPUInventoryOptions) {
	if testContext.ownedLocalCluster {
		return localInventory, metriccontract.GPUInventoryOptions{}
	}
	return nvidiasmi.NewInventory(podNVIDIASMIRunner(workload)), metriccontract.GPUInventoryOptions{PodGPUsOnly: true}
}

// nvidiaSMIRunner returns the runner for the same place gpuInventorySource
// reads: this host for the owned local cluster, otherwise the workload pod.
func nvidiaSMIRunner(workload *corev1.Pod) nvidiasmi.Runner {
	if testContext.ownedLocalCluster {
		return nvidiasmi.LocalRunner
	}
	return podNVIDIASMIRunner(workload)
}

// podNVIDIASMIRunner runs nvidia-smi in the workload container. The CUDA
// image's driver capabilities include utility, so the runtime mounts it.
func podNVIDIASMIRunner(workload *corev1.Pod) nvidiasmi.Runner {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		command := append([]string{"nvidia-smi"}, args...)
		stdout, stderr, err := kubeClient.ExecutePodCommand(ctx, workload.Namespace, workload.Name, workloadContainerName, command)
		if err != nil {
			return []byte(stdout), fmt.Errorf("run nvidia-smi in pod %s/%s: %w (stderr: %s)",
				workload.Namespace, workload.Name, err, strings.TrimSpace(stderr))
		}
		return []byte(stdout), nil
	}
}
