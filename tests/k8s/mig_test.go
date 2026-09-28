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
				metricsHaveOnlyMIGInstanceSamples)
		})
	})

	Context("DCGM exporter selects one MIG instance in mixed MIG mode", Ordered, Label("migSpecificInstanceSelection"), func() {
		AfterAll(func(ctx context.Context) {
			cleanupTestContext(ctx, kubeClient, helmClient)
		})

		It("should honor DCGM_EXPORTER_DEVICES_STR for a selected MIG instance", func(ctx context.Context) {
			Expect(testContext.migEntityID).NotTo(BeEmpty(), "specific MIG selection requires -mig-instance-entity-id")
			Expect(testContext.migNVMLID).NotTo(BeEmpty(), "specific MIG selection requires -mig-instance-nvml-id")

			_, _ = shouldInstallExporterAndReadMetrics(ctx,
				[]string{
					"extraEnv[0].name=DCGM_EXPORTER_DEVICES_STR",
					fmt.Sprintf("extraEnv[0].value=i:%s", testContext.migEntityID),
				},
				nil,
				testRunLabels,
				func(metrics []byte) error {
					return metricsHaveOnlySelectedMIGInstanceSamples(metrics, testContext.migNVMLID)
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

// metricsHaveMixedMIGAndFullGPUSamples verifies mixed mode emits both full-GPU and MIG samples.
func metricsHaveMixedMIGAndFullGPUSamples(metrics []byte) error {
	hasFullGPU, hasMIG, err := migAndFullGPUSampleState(metrics)
	if err != nil {
		return err
	}
	if !hasFullGPU {
		return fmt.Errorf("no full GPU DCGM sample was emitted")
	}
	if !hasMIG {
		return fmt.Errorf("no MIG instance DCGM sample was emitted")
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

// metricsHaveOnlySelectedMIGInstanceSamples verifies selection emits only one expected MIG instance when provided.
func metricsHaveOnlySelectedMIGInstanceSamples(metrics []byte, expectedInstanceID string) error {
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
			if labels["GPU_I_ID"] == "" || labels["GPU_I_PROFILE"] == "" {
				return fmt.Errorf("metric family %q has a GPU sample without MIG labels", familyName)
			}
			if expectedInstanceID != "" && labels["GPU_I_ID"] != expectedInstanceID {
				return fmt.Errorf("metric family %q has GPU_I_ID=%q, expected only %q",
					familyName, labels["GPU_I_ID"], expectedInstanceID)
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no MIG instance DCGM sample was emitted")
	}
	return nil
}

// firstFullGPUID finds a full-GPU label value from mixed MIG discovery metrics.
func firstFullGPUID(metrics []byte) (string, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return "", err
	}
	for familyName, family := range families {
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			if !isDCGMGPUSample(familyName, labels) {
				continue
			}
			if labels["GPU_I_ID"] == "" && labels["GPU_I_PROFILE"] == "" {
				return labels["gpu"], nil
			}
		}
	}
	return "", fmt.Errorf("no full GPU sample was found")
}

// migAndFullGPUSampleState reports whether metrics contain full-GPU samples, MIG samples, or both.
func migAndFullGPUSampleState(metrics []byte) (bool, bool, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return false, false, err
	}

	hasFullGPU := false
	hasMIG := false
	for familyName, family := range families {
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			if !isDCGMGPUSample(familyName, labels) {
				continue
			}
			if labels["GPU_I_ID"] != "" || labels["GPU_I_PROFILE"] != "" {
				if labels["GPU_I_ID"] == "" || labels["GPU_I_PROFILE"] == "" {
					return false, false, fmt.Errorf("metric family %q has incomplete MIG labels", familyName)
				}
				hasMIG = true
				continue
			}
			hasFullGPU = true
		}
	}
	return hasFullGPU, hasMIG, nil
}

// isDCGMGPUSample reports whether a metric sample belongs to a DCGM GPU family.
func isDCGMGPUSample(familyName string, labels map[string]string) bool {
	return strings.HasPrefix(familyName, "DCGM_FI_") && labels["gpu"] != ""
}
