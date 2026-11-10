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

package metriccontract

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	dto "github.com/prometheus/client_model/go"
)

// GPULevelMIGFamilies are default-CSV families whose DCGM field is declared at
// GPU level. On a MIG GPU the exporter reports them once per physical GPU,
// without MIG labels.
var GPULevelMIGFamilies = []string{
	"DCGM_FI_DEV_SM_CLOCK_HERTZ",
	"DCGM_FI_DEV_MEMORY_CLOCK_HERTZ",
	"DCGM_FI_DEV_MEMORY_TEMP_CELSIUS",
	"DCGM_FI_DEV_GPU_TEMP_CELSIUS",
	"DCGM_FI_DEV_BOARD_POWER_WATTS",
	"DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL",
	"DCGM_FI_DEV_PCIE_REPLAY_TOTAL",
	"DCGM_FI_PROF_PCIE_TX_BYTES",
	"DCGM_FI_PROF_PCIE_RX_BYTES",
}

// InstanceLevelMIGFamilies are default-CSV families whose DCGM field is
// declared at GPU-instance or compute-instance level. On a MIG GPU the exporter
// reports them once per instance, with MIG labels.
var InstanceLevelMIGFamilies = []string{
	"DCGM_FI_DEV_FB_FREE_BYTES",
	"DCGM_FI_DEV_FB_USED_BYTES",
	"DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO",
	"DCGM_FI_PROF_TENSOR_UTIL_RATIO",
	"DCGM_FI_PROF_DRAM_UTIL_RATIO",
}

// requiredGPULevelMIGFamily and requiredInstanceLevelMIGFamily are emitted on
// every MIG GPU with the default CSV, so a scrape without them cannot show the
// series shape.
const (
	requiredGPULevelMIGFamily      = "DCGM_FI_DEV_BOARD_POWER_WATTS"
	requiredInstanceLevelMIGFamily = "DCGM_FI_DEV_FB_USED_BYTES"
)

// MIGInstances returns, for each gpu label value with MIG-labeled DCGM
// samples, the GPU_I_ID values seen on it.
func MIGInstances(families map[string]*dto.MetricFamily) map[string][]string {
	instances := map[string][]string{}
	forEachDCGMGPUSample(families, func(_ string, labels map[string]string) {
		gpu, instance := labels["gpu"], labels["GPU_I_ID"]
		if instance != "" && !slices.Contains(instances[gpu], instance) {
			instances[gpu] = append(instances[gpu], instance)
		}
	})
	return instances
}

// ValidateMIGSeriesShape checks the series of every MIG GPU in a scrape. Each
// emitted GPU-level family has exactly one sample per MIG GPU and no MIG
// labels. Each emitted instance-level family has MIG-labeled samples on every
// MIG GPU and no sample without MIG labels there. The board power and
// framebuffer-used families must be emitted.
func ValidateMIGSeriesShape(families map[string]*dto.MetricFamily) error {
	instances := MIGInstances(families)
	if len(instances) == 0 {
		return errors.New("no MIG instance DCGM sample was emitted")
	}

	var errs []error
	for _, required := range []string{requiredGPULevelMIGFamily, requiredInstanceLevelMIGFamily} {
		if _, ok := families[required]; !ok {
			errs = append(errs, fmt.Errorf("family %q was not emitted", required))
		}
	}
	for _, name := range GPULevelMIGFamilies {
		if family, ok := families[name]; ok {
			errs = append(errs, gpuLevelFamilyOnMIGGPUs(name, family, instances))
		}
	}
	for _, name := range InstanceLevelMIGFamilies {
		if family, ok := families[name]; ok {
			errs = append(errs, instanceFamilyOnMIGGPUs(name, family, instances))
		}
	}
	return errors.Join(errs...)
}

// gpuLevelFamilyOnMIGGPUs checks that a GPU-level family has no MIG labels and
// exactly one sample for every MIG GPU. No pod owns a whole MIG GPU, so a
// second sample would be a duplicate series.
func gpuLevelFamilyOnMIGGPUs(name string, family *dto.MetricFamily, instances map[string][]string) error {
	var errs []error
	samples := map[string]int{}
	for _, metric := range family.GetMetric() {
		labels := sampleLabels(metric)
		if hasMIGLabels(labels) || labels["GPU_CI_ID"] != "" {
			errs = append(errs, fmt.Errorf("GPU-level family %q has a sample with MIG labels on gpu=%q", name, labels["gpu"]))
			continue
		}
		samples[labels["gpu"]]++
	}

	for _, gpu := range sortedKeys(instances) {
		if samples[gpu] != 1 {
			errs = append(errs, fmt.Errorf("GPU-level family %q has %d samples for MIG gpu=%q, want 1", name, samples[gpu], gpu))
		}
	}
	return errors.Join(errs...)
}

// instanceFamilyOnMIGGPUs checks that an instance-level family has a
// MIG-labeled sample on every MIG GPU and no sample without complete MIG labels
// there.
func instanceFamilyOnMIGGPUs(name string, family *dto.MetricFamily, instances map[string][]string) error {
	var errs []error
	labeled := map[string]bool{}
	for _, metric := range family.GetMetric() {
		labels := sampleLabels(metric)
		gpu := labels["gpu"]
		if _, ok := instances[gpu]; !ok {
			continue
		}
		if labels["GPU_I_ID"] == "" || labels["GPU_I_PROFILE"] == "" {
			errs = append(errs, fmt.Errorf("instance-level family %q has a sample without MIG labels on gpu=%q", name, gpu))
			continue
		}
		labeled[gpu] = true
	}

	for _, gpu := range sortedKeys(instances) {
		if !labeled[gpu] {
			errs = append(errs, fmt.Errorf("instance-level family %q has no MIG-labeled sample on gpu=%q", name, gpu))
		}
	}
	return errors.Join(errs...)
}

// forEachDCGMGPUSample calls visit with the labels of every DCGM field sample
// that names a GPU.
func forEachDCGMGPUSample(families map[string]*dto.MetricFamily, visit func(name string, labels map[string]string)) {
	for name, family := range families {
		if !strings.HasPrefix(name, "DCGM_FI_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			if labels := sampleLabels(metric); labels["gpu"] != "" {
				visit(name, labels)
			}
		}
	}
}

// hasMIGLabels reports whether a sample has either GPU-instance label.
func hasMIGLabels(labels map[string]string) bool {
	return labels["GPU_I_ID"] != "" || labels["GPU_I_PROFILE"] != ""
}

// sampleLabels returns one sample's labels by name.
func sampleLabels(metric *dto.Metric) map[string]string {
	labels := map[string]string{}
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}

// sortedKeys returns the keys of m in order, so errors list GPUs stably.
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
