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

package exportercontract

import (
	"errors"
	"fmt"
	"io"
	"strings"

	dto "github.com/prometheus/client_model/go"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// BaselineOptions describes environment-specific labels expected on baseline metrics.
type BaselineOptions struct {
	RequireHostname         bool
	RequireKubernetesLabels bool
	KubernetesLabels        map[string]string
	DefaultCounterRows      []metriccontract.DefaultCounterRow
	FieldSupport            metriccontract.FieldSupport
	// ExactCSV states that DefaultCounterRows is the CSV the exporter runs
	// with; see metriccontract.DefaultCounterOptions.
	ExactCSV         bool
	CapabilityWriter io.Writer
}

// BaselineMetrics validates the default dcgm-exporter metrics contract,
// including the shared DCGM sample checks: finite values, no DCGM unavailable
// sentinels, documented upper bounds, and no repeated label sets.
func BaselineMetrics(metrics []byte, opts BaselineOptions) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return fmt.Errorf("metrics should parse as Prometheus text: %w", err)
	}

	var errs []error
	errs = append(errs, metriccontract.ValidateContracts(families, metriccontract.BaselineGPUFamilies()))
	if opts.DefaultCounterRows != nil {
		errs = append(errs, metriccontract.ValidateDefaultCounterRows(
			families,
			opts.DefaultCounterRows,
			metriccontract.DefaultCounterOptions{
				FieldSupport: opts.FieldSupport,
				ExactCSV:     opts.ExactCSV,
				SkipWriter:   opts.CapabilityWriter,
			},
		))
	}
	if opts.RequireHostname {
		errs = append(errs, metriccontract.ValidateAnyLabel(
			families,
			metriccontract.BaselineGPUFamilyNames(),
			metriccontract.GPUHostnameLabels,
		))
	}
	errs = append(errs, metriccontract.ValidateAtLeastOneDCGMGPUFamily(families))
	errs = append(errs, metriccontract.ValidateDCGMSamples(families))

	if opts.RequireKubernetesLabels {
		errs = append(errs, metriccontract.ValidateLabelValues(
			families,
			metriccontract.BaselineGPUFamilyNames(),
			opts.KubernetesLabels,
		))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	report := metriccontract.DetectCapabilities(families)
	if opts.CapabilityWriter != nil {
		metriccontract.WriteCapabilityReport(opts.CapabilityWriter, report)
	}
	return metriccontract.ValidateCapabilityContracts(families, report)
}

// NoHostnameMetrics validates baseline metrics when hostname labels are disabled.
func NoHostnameMetrics(metrics []byte) error {
	return errors.Join(
		BaselineMetrics(metrics, BaselineOptions{RequireHostname: false}),
		MetricsDoNotHaveAnyLabel(metrics, "Hostname", "hostname"),
	)
}

// CustomMetrics validates that customMetrics fully replaces the default DCGM metric set.
func CustomMetrics(metrics []byte, kubernetesLabels map[string]string) error {
	families, err := metriccontract.ParseText(metrics)
	if err != nil {
		return fmt.Errorf("custom metrics response should parse as Prometheus text: %w", err)
	}

	names := []string{"DCGM_FI_DEV_GPU_TEMP_CELSIUS", "DCGM_FI_DEV_FB_USED_BYTES"}
	return errors.Join(
		metriccontract.ValidateContracts(families, customMetricsContracts()),
		metriccontract.ValidateAnyLabel(families, names, metriccontract.GPUHostnameLabels),
		metriccontract.ValidateLabelValues(families, names, kubernetesLabels),
		metriccontract.ValidateLabels(families, names, []string{"DCGM_FI_DRIVER_VERSION"}),
		validateCustomMetricAllowlist(families),
	)
}

// MetricsDoNotHaveAnyLabel rejects responses with non-empty values for blocked labels.
func MetricsDoNotHaveAnyLabel(metrics []byte, labelNames ...string) error {
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
			for _, label := range metric.GetLabel() {
				if blocked[label.GetName()] && label.GetValue() != "" {
					return fmt.Errorf("metric family %q unexpectedly contains label %q=%q", familyName, label.GetName(), label.GetValue())
				}
			}
		}
	}
	return nil
}

// customMetricsContracts returns the metric families expected from the customMetrics scenario.
func customMetricsContracts() []metriccontract.FamilyContract {
	return []metriccontract.FamilyContract{
		{
			Name:        "DCGM_FI_DEV_GPU_TEMP_CELSIUS",
			Type:        dto.MetricType_GAUGE,
			CheckType:   true,
			Labels:      metriccontract.GPUIdentityLabels,
			NonNegative: true,
		},
		{
			Name:        "DCGM_FI_DEV_FB_USED_BYTES",
			Type:        dto.MetricType_GAUGE,
			CheckType:   true,
			Labels:      metriccontract.GPUIdentityLabels,
			NonNegative: true,
		},
	}
}

// validateCustomMetricAllowlist requires customMetrics to emit exactly the configured DCGM families.
func validateCustomMetricAllowlist(families map[string]*dto.MetricFamily) error {
	expected := map[string]struct{}{
		"DCGM_FI_DEV_GPU_TEMP_CELSIUS": {},
		"DCGM_FI_DEV_FB_USED_BYTES":    {},
	}

	var errs []error
	for name := range families {
		if !strings.HasPrefix(name, "DCGM_FI_") {
			continue
		}
		if _, ok := expected[name]; !ok {
			errs = append(errs, fmt.Errorf("customMetrics must fully replace default DCGM metrics, but unexpected family %q is present", name))
		}
	}

	for name := range expected {
		if _, ok := families[name]; !ok {
			errs = append(errs, fmt.Errorf("customMetrics must emit configured family %q", name))
		}
	}

	return errors.Join(errs...)
}
