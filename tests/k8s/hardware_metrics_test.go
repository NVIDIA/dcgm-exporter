//go:build e2e

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
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"

	. "github.com/onsi/ginkgo/v2"
	ginkgoTypes "github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

var _ = Describe("dcgm-exporter hardware metric scenarios", func() {
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

		It("should emit P2P status labels on capable multi-GPU hosts", Label("p2pStatus"), func(ctx context.Context) {
			_ = sharedHardwareBasicMetrics.shouldReadMetrics(ctx, metricsHaveP2PStatusContract)
		})

		It("should omit configured fields that DCGM reports without valid samples", Label("fieldUnsupported"), func(ctx context.Context) {
			candidate := strings.TrimSpace(testContext.unsupportedField)
			if candidate == "" {
				Skip("no unsupported DCGM field candidate was selected by the host capability probe")
			}

			metrics := sharedHardwareBasicMetrics.shouldReadMetrics(ctx, func(metrics []byte) error {
				return metricsHaveFamilyName(metrics, "DCGM_FI_DEV_GPU_TEMP")
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

	Context("DCGM exporter emits NVSwitch metrics", Ordered, Label("nvswitch"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit NVSwitch metrics on capable hosts", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--switch-devices=f")},
				[]string{customMetricsJSONValue(
					"DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).",
					"DCGM_FI_DEV_NVSWITCH_TEMPERATURE_CURRENT, gauge, NVSwitch temperature.",
					"DCGM_FI_DEV_NVSWITCH_LINK_FLIT_ERRORS, counter, NVSwitch link flit errors.",
				)},
				testRunLabels,
				func(metrics []byte) error {
					families, err := metriccontract.ParseText(metrics)
					if err != nil {
						return err
					}
					return metriccontract.ValidateFamilyPrefix(families, "DCGM_FI_DEV_NVSWITCH", []string{"nvswitch"}, true, "NVSwitch")
				})
		})
	})

	Context("DCGM exporter emits Grace CPU metrics", Ordered, Label("graceCpu"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should emit CPU metrics on Grace CPU hosts", func(ctx context.Context) {
			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{exporterArguments("--cpu-devices=f")},
				[]string{customMetricsJSONValue(
					"DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).",
					"DCGM_FI_DEV_CPU_UTIL_TOTAL, gauge, Total CPU utilization.",
				)},
				testRunLabels,
				func(metrics []byte) error {
					families, err := metriccontract.ParseText(metrics)
					if err != nil {
						return err
					}
					return errors.Join(
						metriccontract.ValidateContracts(families, []metriccontract.FamilyContract{{
							Name:        "DCGM_FI_DEV_CPU_UTIL_TOTAL",
							Type:        dto.MetricType_GAUGE,
							CheckType:   true,
							Labels:      []string{"cpu"},
							NonNegative: true,
						}}),
						graceCPUSerialLabelContract(metrics),
					)
				})
		})
	})
})

type hardwareBasicMetricsSetup func(context.Context) (*corev1.Pod, error)

type hardwareBasicMetricsDeployment struct {
	setup       hardwareBasicMetricsSetup
	setupOnce   sync.Once
	cleanupOnce sync.Once
	pod         *corev1.Pod
	err         error
}

var sharedHardwareBasicMetrics = newHardwareBasicMetricsDeployment(setupHardwareBasicMetricsDeployment)

func newHardwareBasicMetricsDeployment(setup hardwareBasicMetricsSetup) *hardwareBasicMetricsDeployment {
	return &hardwareBasicMetricsDeployment{setup: setup}
}

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

func (d *hardwareBasicMetricsDeployment) podFor(ctx context.Context) (*corev1.Pod, error) {
	d.setupOnce.Do(func() {
		d.pod, d.err = d.setup(ctx)
	})
	return d.pod, d.err
}

func (d *hardwareBasicMetricsDeployment) cleanup(ctx context.Context) {
	d.cleanupOnce.Do(func() {
		cleanupTestContext(ctx, kubeClient, helmClient)
	})
}

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
		nil)
	return pod, nil
}

func hardwareBasicMetricsRows() []string {
	return hardwareBasicMetricsRowsFor(hardwareBasicMetricsScenarioSelected("fieldUnsupported"))
}

func hardwareBasicMetricsRowsFor(includeUnsupported bool) []string {
	rows := []string{
		"DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).",
		"DCGM_FI_PROF_GR_ENGINE_ACTIVE, gauge, Ratio of time the graphics engine is active.",
		"DCGM_FI_PROF_DRAM_ACTIVE, gauge, Ratio of cycles active in device memory.",
		"DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL, gauge, Total NVLink bandwidth.",
		"DCGM_EXP_P2P_STATUS, gauge, Current NVLink P2P status per peer link.",
		"DCGM_FI_DEV_C2C_LINK_COUNT, gauge, Number of C2C links.",
		"DCGM_FI_DEV_C2C_LINK_STATUS, gauge, C2C link status.",
	}
	if includeUnsupported {
		candidate := strings.TrimSpace(testContext.unsupportedField)
		if candidate != "" {
			rows = append(rows, candidate+", gauge, candidate field for unsupported-field omission validation.")
		}
	}
	return rows
}

func hardwareBasicMetricsScenarioSelected(label string) bool {
	suiteConfig, _ := GinkgoConfiguration()
	return labelFilterSelectsScenario(suiteConfig.LabelFilter, label)
}

func labelFilterSelectsScenario(filter, label string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return true
	}
	parsed, err := ginkgoTypes.ParseLabelFilter(filter)
	if err != nil {
		return false
	}
	return parsed([]string{label})
}

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
