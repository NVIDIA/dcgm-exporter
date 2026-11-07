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

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

var _ = Describe("dcgm-exporter MIG scenarios", func() {
	Context("DCGM exporter emits MIG attribution", Ordered, Label("mig"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit MIG labels when MIG instances are present with flex device selection", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("-d=f")},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return errors.Join(
						metricsHaveAnyLabel(metrics, "GPU_I_ID"),
						metricsHaveAnyLabel(metrics, "GPU_I_PROFILE"),
						metricsHaveConsistentMIGDeviceSelection(metrics),
						metricsReportGPULevelFieldsOnParentGPUs(metrics),
					)
				})
		})
	})

	Context("DCGM exporter selects full GPUs in mixed MIG mode", Ordered, Label("migFullGpuSelection"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should honor DCGM_EXPORTER_DEVICES_STR for a selected full GPU", func(ctx context.Context) {
			_, discoveryMetrics := shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("-d=f")},
				nil,
				testRunLabels,
				metricsHaveMixedMIGAndFullGPUSamples)

			fullGPUID, err := firstFullGPUID(discoveryMetrics)
			Expect(err).NotTo(HaveOccurred())
			cleanupTestContext(ctx, kubeClient, helmClient)

			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					"extraEnv[0].name=DCGM_EXPORTER_DEVICES_STR",
					fmt.Sprintf("extraEnv[0].value=g:%s", fullGPUID),
				},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return metricsHaveOnlyFullGPUSamples(metrics, fullGPUID)
				})

			cleanupTestContext(ctx, kubeClient, helmClient)

			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("-d=g")},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return metricsHaveOnlyFullGPUSamples(metrics, "")
				})
		})
	})

	Context("DCGM exporter selects all MIG instances in mixed MIG mode", Ordered, Label("migInstanceSelection"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should honor DCGM_EXPORTER_DEVICES_STR for all MIG instances", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					"extraEnv[0].name=DCGM_EXPORTER_DEVICES_STR",
					"extraEnv[0].value=i",
				},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return errors.Join(
						metricsHaveOnlyMIGInstanceSamples(metrics),
						metricsReportGPULevelFieldsOnParentGPUs(metrics),
					)
				})
		})
	})

	Context("DCGM exporter selects one MIG instance in mixed MIG mode", Ordered, Label("migSpecificInstanceSelection"), func() {
		var baseline []byte

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should scrape MIG metrics when all instances are selected", func(ctx context.Context) {
			settled := settledScrape(migSignature)
			_, baseline = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					"extraEnv[0].name=DCGM_EXPORTER_DEVICES_STR",
					"extraEnv[0].value=i",
				},
				nil,
				testRunLabels,
				settled)
		})

		It("should honor DCGM_EXPORTER_DEVICES_STR for a selected MIG instance", func(ctx context.Context) {
			Expect(testContext.migEntityID).NotTo(BeEmpty(), "specific MIG selection requires -mig-instance-entity-id")
			Expect(testContext.migNVMLID).NotTo(BeEmpty(), "specific MIG selection requires -mig-instance-nvml-id")

			skipWhenSelectionCoversAll(requireSeveralMIGInstances(baseline))

			shouldReinstallExporterAndReadMetrics(ctx,
				[]string{
					"extraEnv[0].name=DCGM_EXPORTER_DEVICES_STR",
					fmt.Sprintf("extraEnv[0].value=i:%s", testContext.migEntityID),
				},
				nil,
				func(metrics []byte) error {
					return errors.Join(
						metricsHaveOnlySelectedMIGInstanceSamples(metrics, testContext.migNVMLID),
						onlyOneMIGInstance(metrics, testContext.migNVMLID),
						metricsReportGPULevelFieldsOnParentGPUs(metrics),
					)
				})
		})
	})

	Context("DCGM exporter attributes a MIG pod to its MIG instance only", Ordered, Label("migPodAttribution"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		// The exporter enriches each entity group separately, so the CPU and
		// CPU core samples check that a MIG pod stays off non-GPU metrics.
		It("should name the MIG pod on its MIG instance and on no CPU sample", func(ctx context.Context) {
			shouldInstallHelmChartWithJSONValues(ctx, helmClient,
				[]string{exporterArguments("-d=f", "--cpu-devices=f", "--kubernetes-gpu-id-type=uid")},
				[]string{customMetricsJSONValue(gpuTempRow, fbUsedRow, cpuUtilRow)})

			// The workload runs on the node whose MIG resource it requests.
			mig := shouldFindMIGResource(ctx)
			workload := shouldCreateWorkloadPodRequesting(ctx, kubeClient, testRunLabels, mig.resource, mig.node)
			exporterPod := shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)

			// CPU_UTIL_TOTAL has CPU samples and, with --cpu-devices=f, CPU core samples.
			want := shouldReadMIGWorkloadAttribution(ctx, workload)
			shouldAttributeOnlyWorkloadMIGInstance(ctx, exporterPod, want, []metriccontract.FamilyContract{
				{Name: cpuUtilFamily, Labels: []string{cpuLabel}, WithoutLabels: []string{coreLabel}},
				{Name: cpuUtilFamily, Labels: []string{cpuLabel, coreLabel}},
			})
		})
	})

	Context("DCGM exporter selects full GPUs and MIG instances in mixed MIG mode", Ordered, Label("migCombinedDeviceSelection"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should honor combined DCGM_EXPORTER_DEVICES_STR full GPU and MIG instance selection", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					"extraEnv[0].name=DCGM_EXPORTER_DEVICES_STR",
					"extraEnv[0].value=g+i",
				},
				nil,
				testRunLabels,
				metricsHaveMixedMIGAndFullGPUSamples)
		})
	})
})

// shouldFindMIGResource returns the MIG resource a MIG workload pod requests
// and the node it must run on: the first resource, in name order, that a node
// the suite may use can allocate.
func shouldFindMIGResource(ctx context.Context) migResourceOnNode {
	nodes, err := kubeClient.GetNodesByLabel(ctx, getE2ENodeSelector())
	Expect(err).NotTo(HaveOccurred(), "setup: list the nodes the suite may use")

	mig, err := migResourceName(nodes)
	Expect(err).NotTo(HaveOccurred(), "setup: find a MIG resource for the workload pod")
	return mig
}

// metricsHaveConsistentMIGDeviceSelection verifies flex selection emits MIG-labeled GPU samples.
func metricsHaveConsistentMIGDeviceSelection(metrics []byte) error {
	_, hasMIG, err := migAndFullGPUSampleState(metrics)
	if err != nil {
		return err
	}
	if !hasMIG {
		return fmt.Errorf("no DCGM GPU sample contains MIG labels")
	}
	return nil
}

// metricsHaveOnlyFullGPUSamples verifies device selection emits only one full GPU and no MIG labels.
func metricsHaveOnlyFullGPUSamples(metrics []byte, expectedGPU string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}

	found := false
	for familyName, family := range families {
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			if !isDCGMGPUSample(familyName, labels) {
				continue
			}
			if labels["GPU_I_ID"] != "" || labels["GPU_I_PROFILE"] != "" {
				return fmt.Errorf("metric family %q unexpectedly contains MIG labels for GPU selection", familyName)
			}
			if expectedGPU != "" && labels["gpu"] != expectedGPU {
				return fmt.Errorf("metric family %q has gpu=%q, expected only gpu=%q", familyName, labels["gpu"], expectedGPU)
			}
			found = true
		}
	}
	if !found {
		if expectedGPU == "" {
			return fmt.Errorf("no full GPU DCGM sample was emitted")
		}
		return fmt.Errorf("no DCGM GPU sample was emitted for gpu=%q", expectedGPU)
	}
	return nil
}

// metricsHaveOnlyMIGInstanceSamples verifies device selection emits MIG instances and no full GPUs.
func metricsHaveOnlyMIGInstanceSamples(metrics []byte) error {
	return metricsHaveOnlySelectedMIGInstanceSamples(metrics, "")
}
