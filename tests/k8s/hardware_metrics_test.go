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
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/nvidiasmi"
)

var _ = Describe("dcgm-exporter hardware metric scenarios", func() {
	Context("DCGM exporter reports every active NVLink", Ordered, Label("nvlinkFabric"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should report a healthy NVLink fabric with active links", func(ctx context.Context) {
			shouldInstallHelmChartWithJSONValues(ctx, helmClient, nil,
				[]string{customMetricsJSONValue(nvlinkFabricCustomMetrics()...)})
			workload := shouldCreateWorkloadPod(ctx, kubeClient, testRunLabels)
			exporterPod := shouldFindReadyExporterPodForWorkload(ctx, kubeClient, workload)

			// nvidia-smi reads NVML directly, so its active links are the
			// expectation. On the owned local cluster it runs on this host and
			// sees every GPU the exporter scrapes; on other clusters it runs
			// in the workload pod and sees only that pod's GPUs, so the
			// expectation covers a subset of the exporter's node-wide scrape.
			// Each call gets its own timeout.
			run := nvidiaSMIRunner(workload)
			query := func(args ...string) ([]byte, error) {
				queryCtx, cancel := context.WithTimeout(ctx, nvidiasmi.QueryTimeout)
				defer cancel()
				return run(queryCtx, args...)
			}

			queryOutput, err := query("-q")
			Expect(err).NotTo(HaveOccurred(), "nvidia-smi -q output:\n%s", queryOutput)
			reported, healthy, evidence := nvidiasmi.FabricHealth(string(queryOutput))
			if reported {
				Expect(healthy).To(BeTrue(), evidence)
			}

			statusOutput, err := query("nvlink", "-s")
			Expect(err).NotTo(HaveOccurred(), "nvidia-smi nvlink -s output:\n%s", statusOutput)
			active, err := nvidiasmi.ActiveNVLinks(string(statusOutput))
			Expect(err).NotTo(HaveOccurred(), "nvidia-smi nvlink -s output:\n%s", statusOutput)
			Expect(active).NotTo(BeEmpty(), "expected at least one active NVLink; output:\n%s", statusOutput)

			Eventually(func(g Gomega) {
				metrics := shouldReadMetrics(ctx, kubeClient, exporterPod, dcgmExporterPort)
				g.Expect(nvlinkFabricContract(metrics, active)).To(Succeed())
			}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
		})
	})

	Context("DCGM exporter emits basic hardware metrics", Ordered, ContinueOnFailure, func() {
		AfterAll(func(ctx context.Context) {
			sharedHardwareBasicMetrics.cleanup(ctx)
		})

		It("should emit DCGM_FI_PROF metrics on capable hosts", Label("profiling"), func(ctx context.Context) {
			_ = sharedHardwareBasicMetrics.shouldReadMetrics(ctx, func(metrics []byte) error {
				families, err := metriccontract.ParseText(metrics)
				if err != nil {
					return err
				}
				return metriccontract.ValidateFamilyPrefix(families, "DCGM_FI_PROF_", []string{"gpu", "UUID"}, true, "profiling/DCP")
			})
		})

		It("should emit NVLink metrics on capable hosts", Label("nvlink"), func(ctx context.Context) {
			_ = sharedHardwareBasicMetrics.shouldReadMetrics(ctx, func(metrics []byte) error {
				families, err := metriccontract.ParseText(metrics)
				if err != nil {
					return err
				}
				return metriccontract.ValidateContracts(families, []metriccontract.FamilyContract{{
					Name:        "DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL",
					Type:        dto.MetricType_GAUGE,
					CheckType:   true,
					Labels:      metriccontract.GPUIdentityLabels,
					NonNegative: true,
				}})
			})
		})

		It("should emit NVLink 5+ integrity metrics on capable hosts", Label("nvlinkIntegrity"), func(ctx context.Context) {
			_ = sharedHardwareBasicMetrics.shouldReadMetrics(ctx, func(metrics []byte) error {
				families, err := metriccontract.ParseText(metrics)
				if err != nil {
					return err
				}
				return metriccontract.ValidateContracts(families, []metriccontract.FamilyContract{{
					Name:        "DCGM_FI_DEV_NVLINK_INTEGRITY_ERROR_TOTAL",
					Type:        dto.MetricType_COUNTER,
					CheckType:   true,
					Labels:      metriccontract.GPUIdentityLabels,
					NonNegative: true,
				}})
			})
		})

		It("should emit P2P status labels on capable multi-GPU hosts", Label("p2pStatus"), func(ctx context.Context) {
			_ = sharedHardwareBasicMetrics.shouldReadMetrics(ctx, metricsHaveP2PStatusContract)
		})

		It("should omit configured fields that DCGM reports without valid samples", Label("fieldUnsupported"), func(ctx context.Context) {
			candidate := strings.TrimSpace(testContext.unsupportedField)
			Expect(candidate).NotTo(BeEmpty(), "scenario setup must select an unsupported DCGM field")

			metrics := sharedHardwareBasicMetrics.shouldReadMetrics(ctx, func(metrics []byte) error {
				return metricsHaveFamilyName(metrics, "DCGM_FI_DEV_GPU_TEMP_CELSIUS")
			})

			omitted, err := missingMetricFamilies(metrics, []string{candidate})
			Expect(err).NotTo(HaveOccurred())
			Expect(omitted).To(ContainElement(candidate), "expected exporter to omit the probe-selected unsupported field")
			By("DCGM omitted probe-selected unsupported field: " + strings.Join(omitted, ", "))
		})

		It("should emit C2C metrics on capable hosts", Label("c2c"), func(ctx context.Context) {
			_ = sharedHardwareBasicMetrics.shouldReadMetrics(ctx, func(metrics []byte) error {
				families, err := metriccontract.ParseText(metrics)
				if err != nil {
					return err
				}
				return metriccontract.ValidateFamilyPrefix(families, "DCGM_FI_DEV_C2C", []string{"gpu", "UUID"}, true, "C2C")
			})
		})
	})

	Context("DCGM exporter emits NVSwitch temperature metrics", Ordered, Label("nvswitch"), func() {
		var baseline []byte

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit NVSwitch temperature metrics on capable hosts", func(ctx context.Context) {
			settled := settledScrape(entitySignature(switchTempFamily, switchLabel))
			_, baseline = shouldInstallExporterAndReadMetrics(ctx,
				switchPrivilegedValues(exporterArguments("--switch-devices=f")),
				[]string{customMetricsJSONValue(gpuTempRow, switchTempRow)},
				testRunLabels,
				func(metrics []byte) error {
					families, err := metriccontract.ParseText(metrics)
					if err != nil {
						return err
					}
					return errors.Join(
						metriccontract.ValidateContracts(families, []metriccontract.FamilyContract{{
							Name:        switchTempFamily,
							Type:        dto.MetricType_GAUGE,
							CheckType:   true,
							Labels:      []string{switchLabel},
							NonNegative: true,
						}}),
						settled(metrics),
					)
				})
		})

		It("should report only the switch that --switch-devices selects", func(ctx context.Context) {
			selection, err := selectHighestEntity(baseline, switchTempFamily, switchLabel, switchPrefix)
			skipWhenSelectionCoversAll(err)

			shouldReinstallExporterAndReadMetrics(ctx,
				switchPrivilegedValues(exporterArguments("--switch-devices="+switchSelector(selection))),
				[]string{customMetricsJSONValue(gpuTempRow, switchTempRow)},
				onlySelectedEntity(switchTempFamily, switchLabel, switchPrefix+selection.entityID))
		})
	})

	Context("DCGM exporter emits NVSwitch link metrics", Ordered, Label("nvswitchLink"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit NVSwitch link metrics on capable hosts", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				switchPrivilegedValues(exporterArguments("--switch-devices=f")),
				[]string{customMetricsJSONValue(
					gpuTempRow,
					"DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS, counter, NVSwitch link flit errors.",
				)},
				testRunLabels,
				func(metrics []byte) error {
					families, err := metriccontract.ParseText(metrics)
					if err != nil {
						return err
					}
					return metriccontract.ValidateContracts(families, []metriccontract.FamilyContract{{
						Name:        "DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS",
						Type:        dto.MetricType_COUNTER,
						CheckType:   true,
						Labels:      []string{"nvswitch", "nvlink"},
						NonNegative: true,
					}})
				})
		})
	})

	Context("DCGM exporter emits NVSwitch throughput metrics", Ordered, Label("nvswitchThroughput"), func() {
		var baseline []byte

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit NVSwitch link throughput metrics on capable hosts", func(ctx context.Context) {
			settled := settledScrape(entitySignature(switchLinkFamily, switchLabel, linkLabel))
			_, baseline = shouldInstallExporterAndReadMetrics(ctx,
				switchPrivilegedValues(exporterArguments("--switch-devices=f")),
				[]string{customMetricsJSONValue(gpuTempRow, switchLinkRow)},
				testRunLabels,
				func(metrics []byte) error {
					families, err := metriccontract.ParseText(metrics)
					if err != nil {
						return err
					}
					return errors.Join(
						metriccontract.ValidateContracts(families, []metriccontract.FamilyContract{{
							Name:        switchLinkFamily,
							Type:        dto.MetricType_COUNTER,
							CheckType:   true,
							Labels:      []string{switchLabel, linkLabel},
							NonNegative: true,
						}}),
						settled(metrics),
					)
				})
		})

		It("should report only the links that --switch-devices selects", func(ctx context.Context) {
			selection, err := selectHighestMembers(baseline, switchLinkFamily, switchLabel, switchPrefix, linkLabel)
			skipWhenSelectionCoversAll(err)

			shouldReinstallExporterAndReadMetrics(ctx,
				switchPrivilegedValues(exporterArguments("--switch-devices="+switchSelector(selection))),
				[]string{customMetricsJSONValue(gpuTempRow, switchLinkRow)},
				onlySelectedLinks(selection))
		})
	})

	Context("DCGM exporter emits Grace CPU metrics", Ordered, Label("graceCpu"), func() {
		var baseline []byte

		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit CPU metrics on Grace CPU hosts", func(ctx context.Context) {
			settled := settledScrape(entitySignature(cpuUtilFamily, cpuLabel, coreLabel))
			_, baseline = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--cpu-devices=f")},
				[]string{customMetricsJSONValue(gpuTempRow, cpuUtilRow)},
				testRunLabels,
				func(metrics []byte) error {
					families, err := metriccontract.ParseText(metrics)
					if err != nil {
						return err
					}
					return errors.Join(
						metriccontract.ValidateContracts(families, []metriccontract.FamilyContract{{
							Name:        cpuUtilFamily,
							Type:        dto.MetricType_GAUGE,
							CheckType:   true,
							Labels:      []string{cpuLabel},
							NonNegative: true,
						}}),
						graceCPUSerialLabelContract(metrics),
						settled(metrics),
					)
				})
		})

		It("should report only the CPU that --cpu-devices selects", func(ctx context.Context) {
			selection, err := selectHighestEntity(baseline, cpuUtilFamily, cpuLabel, "")
			skipWhenSelectionCoversAll(err)

			shouldReinstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--cpu-devices=" + cpuSelector(selection))},
				[]string{customMetricsJSONValue(gpuTempRow, cpuUtilRow)},
				onlySelectedEntity(cpuUtilFamily, cpuLabel, selection.entityID))
		})

		It("should report only the cores that --cpu-devices selects", func(ctx context.Context) {
			selection, err := selectHighestMembers(baseline, cpuUtilFamily, cpuLabel, "", coreLabel)
			skipWhenSelectionCoversAll(err)

			shouldReinstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--cpu-devices=" + cpuSelector(selection))},
				[]string{customMetricsJSONValue(gpuTempRow, cpuUtilRow)},
				onlySelectedCores(selection))
		})
	})
})

var sharedHardwareBasicMetrics = newHardwareBasicMetricsDeployment(setupHardwareBasicMetricsDeployment)

// shouldReadMetrics creates the shared deployment and validates its metrics.
func (d *hardwareBasicMetricsDeployment) shouldReadMetrics(ctx context.Context, validate func([]byte) error) []byte {
	pod, err := d.podFor(ctx)
	Expect(err).NotTo(HaveOccurred())

	var metricsResponse []byte
	Eventually(func(g Gomega) {
		metricsResponse = shouldReadMetrics(ctx, kubeClient, pod, dcgmExporterPort)
		g.Expect(metricsResponse).NotTo(BeEmpty())
		g.Expect(validate(metricsResponse)).To(Succeed())
	}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())

	return metricsResponse
}

// cleanup removes the shared exporter deployment at most once.
func (d *hardwareBasicMetricsDeployment) cleanup(ctx context.Context) {
	d.runCleanupOnce(func() {
		cleanupTestContext(ctx, kubeClient, helmClient)
	})
}

// setupHardwareBasicMetricsDeployment installs the exporter configured for selected shared scenarios.
func setupHardwareBasicMetricsDeployment(ctx context.Context) (pod *corev1.Pod, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("hardware-basic-metrics setup failed: %v", recovered)
		}
	}()

	pod, _ = shouldInstallExporterAndReadMetrics(
		ctx,
		nil,
		[]string{customMetricsJSONValue(hardwareBasicMetricsRows()...)},
		testRunLabels,
		nil,
	)
	return pod, nil
}

// hardwareBasicMetricsRows returns metric rows for the scenarios selected by Ginkgo.
func hardwareBasicMetricsRows() []string {
	return hardwareBasicMetricsRowsFor(
		testContext.unsupportedField,
		hardwareBasicMetricsScenarioSelected("fieldUnsupported"),
		hardwareBasicMetricsScenarioSelected("nvlinkIntegrity"),
	)
}

// hardwareBasicMetricsScenarioSelected reports whether the active label filter selects a scenario.
func hardwareBasicMetricsScenarioSelected(label string) bool {
	suiteConfig, _ := GinkgoConfiguration()
	return labelFilterSelectsScenario(suiteConfig.LabelFilter, label)
}

// switchPrivilegedValues returns the Helm values the NVSwitch scenarios need:
// the exporter arguments value and a privileged container.
func switchPrivilegedValues(arguments string) []string {
	return []string{
		arguments,
		"securityContext.privileged=true",
		"securityContext.allowPrivilegeEscalation=true",
	}
}

// skipWhenSelectionCoversAll skips the spec when the default run shows no
// more entities than the step selects, because the check could not fail
// there. Any other selection error fails the spec.
func skipWhenSelectionCoversAll(err error) {
	if errors.Is(err, errSelectionCoversAll) {
		Skip(err.Error())
	}

	Expect(err).NotTo(HaveOccurred())
}

// graceCPUSerialLabelContract requires serial-labeled metrics to identify a CPU, not a core.
func graceCPUSerialLabelContract(metrics []byte) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}

	found := false
	for familyName, family := range families {
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			serial := labels["cpu_serial"]
			if serial == "" {
				continue
			}
			found = true
			if labels["cpu"] == "" {
				return fmt.Errorf("metric family %q has cpu_serial=%q without a CPU label", familyName, serial)
			}
			if labels["cpucore"] != "" {
				return fmt.Errorf("metric family %q has cpu_serial=%q on CPU core %q", familyName, serial, labels["cpucore"])
			}
		}
	}
	if !found {
		By("DCGM did not expose cpu_serial labels; serial-specific assertion skipped")
	}
	return nil
}
