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
	"fmt"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/exportercontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// noHostnameMetricContractError validates that metrics omit hostname labels.
func noHostnameMetricContractError(metrics []byte) error {
	return exportercontract.NoHostnameMetrics(metrics)
}

// metricsHaveFamilyName verifies a metric family exists and has sane non-negative samples.
func metricsHaveFamilyName(metrics []byte, familyName string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	family := families[familyName]
	if family == nil || len(family.GetMetric()) == 0 {
		return fmt.Errorf("metric family %q is missing or empty", familyName)
	}
	return metriccontract.ValidateNonNegativeSamples(family)
}

// metricsHaveNonNegativeFamilyIfPresent validates an optional family when samples exist.
func metricsHaveNonNegativeFamilyIfPresent(metrics []byte, familyName string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	family := families[familyName]
	if family == nil || len(family.GetMetric()) == 0 {
		return nil
	}
	return metriccontract.ValidateNonNegativeSamples(family)
}

// missingMetricFamilies returns configured family names that are absent or empty in a scrape.
func missingMetricFamilies(metrics []byte, familyNames []string) ([]string, error) {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0, len(familyNames))
	for _, familyName := range familyNames {
		family := families[familyName]
		if family == nil || len(family.GetMetric()) == 0 {
			missing = append(missing, familyName)
		}
	}
	return missing, nil
}

// metricsHaveP2PStatusContract verifies exporter-owned P2P status samples and labels.
func metricsHaveP2PStatusContract(metrics []byte) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	family := families["DCGM_EXP_P2P_STATUS"]
	if family == nil || len(family.GetMetric()) == 0 {
		return fmt.Errorf("metric family %q is missing or empty", "DCGM_EXP_P2P_STATUS")
	}
	validStatus := map[string]bool{
		"OK":                   true,
		"ChipsetNotSupported":  true,
		"GPUNotSupported":      true,
		"TopologyNotSupported": true,
		"DisabledByRegKey":     true,
		"NotSupported":         true,
		"Unknown":              true,
	}
	for _, metric := range family.GetMetric() {
		labels := metricLabels(metric)
		if labels["gpu"] == "" {
			return fmt.Errorf("DCGM_EXP_P2P_STATUS sample is missing gpu label")
		}
		if labels["peer_gpu"] == "" {
			return fmt.Errorf("DCGM_EXP_P2P_STATUS sample is missing peer_gpu label")
		}
		if !validStatus[labels["link_status"]] {
			return fmt.Errorf("DCGM_EXP_P2P_STATUS sample has invalid link_status label %q", labels["link_status"])
		}
	}
	return metriccontract.ValidateNonNegativeSamples(family)
}

// metricsHaveAnyLabel verifies at least one metric sample has a non-empty label value.
func metricsHaveAnyLabel(metrics []byte, labelName string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			if metricLabels(metric)[labelName] != "" {
				return nil
			}
		}
	}
	return fmt.Errorf("no metric sample contains non-empty label %q", labelName)
}

// metricsHaveLabelValue verifies at least one metric sample has an exact label value.
func metricsHaveLabelValue(metrics []byte, labelName string, labelValue string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			if metricLabels(metric)[labelName] == labelValue {
				return nil
			}
		}
	}
	return fmt.Errorf("no metric sample contains label %q=%q", labelName, labelValue)
}

// metricsDoNotHaveAnyLabel verifies blocked labels are absent or empty across all samples.
func metricsDoNotHaveAnyLabel(metrics []byte, labelNames ...string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	blocked := map[string]bool{}
	for _, labelName := range labelNames {
		blocked[labelName] = true
	}
	for familyName, family := range families {
		for _, metric := range family.GetMetric() {
			for labelName, labelValue := range metricLabels(metric) {
				if blocked[labelName] && labelValue != "" {
					return fmt.Errorf("metric family %q unexpectedly contains label %q=%q", familyName, labelName, labelValue)
				}
			}
		}
	}
	return nil
}

// metricLabelValuesDoNotContain verifies a label is present but never contains a disallowed token.
func metricLabelValuesDoNotContain(metrics []byte, labelName string, disallowed string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return err
	}
	found := false
	for familyName, family := range families {
		for _, metric := range family.GetMetric() {
			value := metricLabels(metric)[labelName]
			if value == "" {
				continue
			}
			found = true
			if strings.Contains(value, disallowed) {
				return fmt.Errorf("metric family %q has %q label value %q containing %q", familyName, labelName, value, disallowed)
			}
		}
	}
	if !found {
		return fmt.Errorf("no metric sample contains label %q", labelName)
	}
	return nil
}

// metricLabels converts one Prometheus metric's labels into a lookup map.
func metricLabels(metric *dto.Metric) map[string]string {
	labels := map[string]string{}
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
