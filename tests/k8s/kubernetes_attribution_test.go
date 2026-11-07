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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/tests/k8s/internal/framework"
)

// draMetricsJSONValue is the --set-json value that turns DRA labels on in the
// chart's metrics entry. The chart also needs config.sources.dra.mode=kubernetes.
const draMetricsJSONValue = `config.metrics=[{"name":"default","file":"` + chartMetricsFile +
	`","every":"30s","enrichment":{"kubernetes":"auto","dra":true}}]`

var _ = Describe("dcgm-exporter Kubernetes attribution scenarios", func() {
	Context("DCGM exporter with pod labels collection enabled", Ordered, Label("podLabels"), func() {
		var (
			dcgmExpPod   *corev1.Pod
			customLabels = map[string]string{
				"valid_key":       "value-valid",
				"key-with-dashes": "value-dashes",
				"key.with.dots":   "value-dots",
			}
			metricsResponse []byte
		)

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should install dcgm-exporter helm chart with pod labels enabled", func(ctx context.Context) {
			shouldInstallHelmChart(ctx, helmClient, []string{
				exporterArguments(),
				"config.sources.kubernetes.pod_labels.enabled=true",
				"config.sources.kubernetes.pod_labels.allow[0]=.*",
				"kubernetes.rbac.create=true",
			})
		})

		It("should create a workload pod and select its node-local exporter", func(ctx context.Context) {
			workload := shouldCreateWorkloadPod(ctx, kubeClient, customLabels)
			dcgmExpPod = shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)
		})

		It("should wait for metrics to be available", func(ctx context.Context) {
			shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
		})

		It("should read metrics from dcgm-exporter", func(ctx context.Context) {
			metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
			Expect(metricsResponse).ShouldNot(BeEmpty(), "Metrics response should not be empty")
		})

		It("should verify metrics contain sanitized pod labels", func(ctx context.Context) {
			Eventually(func(g Gomega) {
				metricsResponse = shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
				g.Expect(metricsResponse).ShouldNot(BeEmpty(), "Metrics response should not be empty")
				g.Expect(errors.Join(
					metricsHaveLabelValue(metricsResponse, "valid_key", "value-valid"),
					metricsHaveLabelValue(metricsResponse, "key_with_dashes", "value-dashes"),
					metricsHaveLabelValue(metricsResponse, "key_with_dots", "value-dots"),
				)).Should(Succeed())
			}).WithPolling(pollingIntervalSlow).Within(metricsWaitTimeout).Should(Succeed())
		})
	})

	Context("DCGM exporter emits pod UID attribution", Ordered, Label("podUID"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should set pod_uid to the workload pod's metadata.uid on its GPU only", func(ctx context.Context) {
			exporterPod, _ := shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					exporterArguments(),
					"config.sources.kubernetes.pod_uid=true",
					"kubernetes.rbac.create=true",
				},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return baselineMetricContractError(metrics, true)
				})

			workload := shouldGetWorkloadPod(ctx)
			want := shouldReadWorkloadAttribution(ctx, workload)
			want.PodUID = string(workload.UID)
			shouldAttributeOnlyWorkloadGPU(ctx, exporterPod, want)
		})
	})

	Context("DCGM exporter applies pod label allowlists", Ordered, Label("podLabelAllowlist"), func() {
		customLabels := map[string]string{
			"valid_key":       "value-valid",
			"blocked_key":     "value-blocked",
			"key-with-dashes": "value-dashes",
		}

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should include allowed labels and suppress blocked labels", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					exporterArguments(),
					"config.sources.kubernetes.pod_labels.enabled=true",
					"kubernetes.rbac.create=true",
					"config.sources.kubernetes.pod_labels.allow[0]=^valid_key$",
				},
				nil,
				customLabels,
				func(metrics []byte) error {
					return errors.Join(
						baselineMetricContractError(metrics, true),
						metricsHaveLabelValue(metrics, "valid_key", "value-valid"),
						metricsDoNotHaveAnyLabel(metrics, "blocked_key", "key_with_dashes"),
					)
				})
		})
	})

	Context("DCGM exporter maps Kubernetes GPU IDs", Label("kubernetesGpuId"), func() {
		// The NVIDIA device plugin gives the kubelet GPU UUIDs as device IDs.
		// uid mode matches them; device-name mode looks for nvidia<N> names, so
		// it matches no pod. TestProcessPodMapper_WithD_Different_Format_Of_DeviceID
		// in internal/pkg/transformation covers device-name matching.
		for _, mode := range []struct {
			idType     string
			spec       string
			attributed bool
		}{
			{idType: "uid", spec: "should name the workload pod on its GPU only", attributed: true},
			{idType: "device-name", spec: "should name the workload pod on no GPU"},
		} {
			Context(mode.idType+" mode", Ordered, func() {
				AfterAll(func(ctx context.Context) {
					cleanupTestContext(ctx, kubeClient, helmClient)
				})

				It(mode.spec, func(ctx context.Context) {
					exporterPod, _ := shouldInstallExporterAndReadMetrics(ctx,
						[]string{exporterArguments("--kubernetes-gpu-id-type=" + mode.idType)},
						nil,
						testRunLabels,
						func(metrics []byte) error {
							return baselineMetricContractError(metrics, mode.attributed)
						})

					want := shouldReadWorkloadAttribution(ctx, shouldGetWorkloadPod(ctx))
					if mode.attributed {
						shouldAttributeOnlyWorkloadGPU(ctx, exporterPod, want)
					} else {
						shouldNotAttributeWorkload(ctx, exporterPod, want)
					}
				})
			})
		}
	})

	Context("DCGM exporter maps renamed NVIDIA GPU resources", Label("customResourceNames"), func() {
		// The scenario group time-slices GPUs with renameByDefault, so the
		// device plugin advertises them as nvidia.com/gpu.shared. The exporter
		// attributes pods holding that resource only when it is named in
		// --nvidia-resource-names.
		for _, mode := range []struct {
			name       string
			spec       string
			args       []string
			attributed bool
		}{
			{name: "without --nvidia-resource-names", spec: "should name the workload pod on no GPU"},
			{
				name:       "with --nvidia-resource-names",
				spec:       "should name the workload pod on its GPU only",
				args:       []string{"--nvidia-resource-names=" + string(framework.NVIDIASharedGPUResourceName)},
				attributed: true,
			},
		} {
			Context(mode.name, Ordered, func() {
				AfterAll(func(ctx context.Context) {
					cleanupTestContext(ctx, kubeClient, helmClient)
				})

				It(mode.spec, func(ctx context.Context) {
					shouldInstallHelmChart(ctx, helmClient, []string{exporterArguments(mode.args...)})
					workload := shouldCreateWorkloadPodRequesting(ctx, kubeClient, testRunLabels, framework.NVIDIASharedGPUResourceName, "")
					exporterPod := shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)
					shouldWaitForMetrics(ctx, kubeClient, exporterPod, dcgmExporterPort)

					want := shouldReadWorkloadAttribution(ctx, workload)
					if mode.attributed {
						shouldAttributeOnlyWorkloadGPU(ctx, exporterPod, want)
					} else {
						shouldNotAttributeWorkload(ctx, exporterPod, want)
					}
				})
			})
		}
	})

	Context("DCGM exporter emits DRA attribution", Label("dra"), func() {
		// Each mode owns a separate Helm release and claim workload, so the
		// virtual-GPU pass must preserve live DRA identity without stale samples.
		for _, mode := range []struct {
			name        string
			virtualGPUs bool
		}{
			{name: "dedicated GPU mode"},
			{name: "virtual GPU mode", virtualGPUs: true},
		} {
			Context(mode.name, Ordered, func() {
				AfterAll(func(ctx context.Context) {
					cleanupTestContext(ctx, kubeClient, helmClient)
				})

				It("should emit DRA labels when Kubernetes DRA resources are present", func(ctx context.Context) {
					values := []string{"config.sources.dra.mode=kubernetes"}
					if mode.virtualGPUs {
						values = append(values, exporterArguments("--kubernetes-virtual-gpus"))
					}
					shouldInstallHelmChartWithJSONValues(ctx, helmClient, values, []string{draMetricsJSONValue})
					shouldCreateDRAWorkload(ctx)

					var allocation regularDRAAllocation
					var workloadNode string
					Eventually(func(g Gomega) {
						observedAllocation, observedNode, err := readRegularDRAAllocation(ctx)
						g.Expect(err).NotTo(HaveOccurred())
						allocation = observedAllocation
						workloadNode = observedNode
					}).WithTimeout(podReadinessTimeout).WithPolling(pollingIntervalNormal).Should(Succeed())

					dcgmExpPod := shouldFindReadyExporterPodOnNode(ctx, kubeClient, workloadNode)
					shouldMatchExporterAccess(ctx, dcgmExpPod, exporterAccessChecks(dcgmExpPod.Namespace, true, true))
					shouldWaitForMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)

					Eventually(func(g Gomega) {
						metrics := shouldReadMetrics(ctx, kubeClient, dcgmExpPod, dcgmExporterPort)
						g.Expect(regularDRAAttributionContract(metrics, regularDRAMetricExpectation{
							Pod:                  workloadPodName,
							Namespace:            testContext.namespace,
							Container:            workloadContainerName,
							regularDRAAllocation: allocation,
						})).To(Succeed())
					}).WithTimeout(metricsWaitTimeout).WithPolling(pollingIntervalSlow).Should(Succeed())
				})
			})
		}
	})

	Context("DCGM exporter emits shared GPU attribution", Ordered, Label("sharedGpu"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit vgpu labels when shared GPU resources are present", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--kubernetes-virtual-gpus")},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return errors.Join(
						baselineMetricContractError(metrics, true),
						metricsHaveAnyLabel(metrics, "vgpu"),
					)
				})
		})
	})
})
