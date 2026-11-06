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
	"cmp"
	"fmt"
	"slices"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

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

// metricsHaveOnlySelectedMIGInstanceSamples verifies selection emits only one expected MIG instance when provided.
// GPU-level fields are reported on the parent GPU of a selected instance, so a
// sample without MIG labels is allowed only for such a parent GPU.
func metricsHaveOnlySelectedMIGInstanceSamples(metrics []byte, expectedInstanceID string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}

	parents := migParentGPUs(families)
	found := false
	var firstErr error
	forEachDCGMGPUSample(families, func(familyName string, labels map[string]string) {
		isInstance, err := selectedMIGSample(familyName, labels, parents, expectedInstanceID)
		found = found || isInstance
		firstErr = cmp.Or(firstErr, err)
	})
	if firstErr != nil {
		return firstErr
	}
	if !found {
		return fmt.Errorf("no MIG instance DCGM sample was emitted")
	}
	return nil
}

// selectedMIGSample checks one sample from an instance selection and reports
// whether it is a MIG instance sample.
func selectedMIGSample(familyName string, labels map[string]string, parents map[string]bool, expectedInstanceID string) (bool, error) {
	if !hasMIGLabels(labels) {
		if labels["GPU_CI_ID"] != "" {
			return false, fmt.Errorf("metric family %q has GPU_CI_ID=%q without GPU-instance labels", familyName, labels["GPU_CI_ID"])
		}
		if !parents[labels["gpu"]] {
			return false, fmt.Errorf("metric family %q has gpu=%q, which has no selected MIG instance", familyName, labels["gpu"])
		}
		return false, nil
	}
	if err := completeMIGLabels(familyName, labels); err != nil {
		return false, err
	}
	if expectedInstanceID != "" && labels["GPU_I_ID"] != expectedInstanceID {
		return false, fmt.Errorf("metric family %q has GPU_I_ID=%q, expected only %q",
			familyName, labels["GPU_I_ID"], expectedInstanceID)
	}
	return true, nil
}

// firstFullGPUID finds a full-GPU label value from mixed MIG discovery metrics,
// the first in sorted order. Parent GPUs of MIG instances also have samples
// without MIG labels, so they are skipped.
func firstFullGPUID(metrics []byte) (string, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return "", err
	}
	gpus := fullGPUs(families)
	if len(gpus) == 0 {
		return "", fmt.Errorf("no full GPU sample was found")
	}
	return slices.Min(gpus), nil
}

// migAndFullGPUSampleState reports whether metrics contain full-GPU samples, MIG samples, or both.
func migAndFullGPUSampleState(metrics []byte) (bool, bool, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return false, false, err
	}

	hasMIG := false
	var firstErr error
	forEachDCGMGPUSample(families, func(familyName string, labels map[string]string) {
		if hasMIGLabels(labels) {
			hasMIG = true
			firstErr = cmp.Or(firstErr, completeMIGLabels(familyName, labels))
		}
	})
	if firstErr != nil {
		return false, false, firstErr
	}
	return len(fullGPUs(families)) > 0, hasMIG, nil
}

// metricsReportGPULevelFieldsOnParentGPUs verifies the MIG series shape:
// GPU-level families have one sample for every parent GPU of a MIG instance
// and never carry MIG labels, and instance-level families keep one MIG-labeled
// sample per instance.
func metricsReportGPULevelFieldsOnParentGPUs(metrics []byte) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	return metriccontract.ValidateMIGSeriesShape(families)
}

// migParentGPUs returns the gpu label values that have MIG-labeled DCGM samples.
func migParentGPUs(families map[string]*dto.MetricFamily) map[string]bool {
	parents := map[string]bool{}
	forEachDCGMGPUSample(families, func(_ string, labels map[string]string) {
		if labels["GPU_I_ID"] != "" {
			parents[labels["gpu"]] = true
		}
	})
	return parents
}

// fullGPUs returns the gpu label values of GPUs without MIG instances that have
// DCGM samples.
func fullGPUs(families map[string]*dto.MetricFamily) []string {
	parents := migParentGPUs(families)
	seen := map[string]bool{}
	var gpus []string
	forEachDCGMGPUSample(families, func(_ string, labels map[string]string) {
		gpu := labels["gpu"]
		if !parents[gpu] && !seen[gpu] {
			seen[gpu] = true
			gpus = append(gpus, gpu)
		}
	})
	return gpus
}

// forEachDCGMGPUSample calls visit for every DCGM GPU sample.
func forEachDCGMGPUSample(families map[string]*dto.MetricFamily, visit func(familyName string, labels map[string]string)) {
	for familyName, family := range families {
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			if isDCGMGPUSample(familyName, labels) {
				visit(familyName, labels)
			}
		}
	}
}

// hasMIGLabels reports whether a sample has either MIG instance label.
func hasMIGLabels(labels map[string]string) bool {
	return labels["GPU_I_ID"] != "" || labels["GPU_I_PROFILE"] != ""
}

// completeMIGLabels requires both MIG instance labels on a MIG sample.
func completeMIGLabels(familyName string, labels map[string]string) error {
	if labels["GPU_I_ID"] == "" || labels["GPU_I_PROFILE"] == "" {
		return fmt.Errorf("metric family %q has incomplete MIG labels", familyName)
	}
	return nil
}

// isDCGMGPUSample reports whether a metric sample belongs to a DCGM GPU family.
func isDCGMGPUSample(familyName string, labels map[string]string) bool {
	return strings.HasPrefix(familyName, "DCGM_FI_") && labels["gpu"] != ""
}
