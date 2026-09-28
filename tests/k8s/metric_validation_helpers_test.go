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
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/exportercontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// shouldVerifyBaselineMetricContract checks that default metrics satisfy the GPU contract.
func shouldVerifyBaselineMetricContract(metricsResponse []byte, requireKubernetesLabels bool) {
	Expect(baselineMetricContractError(metricsResponse, requireKubernetesLabels)).Should(Succeed())
}

// baselineMetricContractError returns all baseline contract failures found in a metrics response.
func baselineMetricContractError(metricsResponse []byte, requireKubernetesLabels bool) error {
	return exportercontract.BaselineMetrics(metricsResponse, exportercontract.BaselineOptions{
		RequireHostname:         true,
		RequireKubernetesLabels: requireKubernetesLabels,
		KubernetesLabels:        workloadLabelValues(),
		DefaultCounters:         bytes.NewReader(defaultCountersData()),
		SupportedFields:         supportedDefaultCounterFields(),
		CapabilityWriter:        GinkgoWriter,
	})
}

// legacyNamespaceMetricContractError checks default metrics emitted with legacy label names.
func legacyNamespaceMetricContractError(metricsResponse []byte) error {
	families, err := metriccontract.ParseText(metricsResponse)
	if err != nil {
		return fmt.Errorf("metrics should parse as Prometheus text: %w", err)
	}

	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(defaultCountersData()))
	if err != nil {
		return fmt.Errorf("default counters should parse: %w", err)
	}

	return errors.Join(
		metriccontract.ValidateDefaultCounterRows(
			families,
			rows,
			metriccontract.DefaultCounterOptions{
				SupportedFields: supportedDefaultCounterFields(),
				MetricLabels:    []string{"gpu", "uuid", "device", "modelName"},
				SkipWriter:      GinkgoWriter,
			},
		),
		metriccontract.ValidateLabels(
			families,
			metriccontract.BaselineGPUFamilyNames(),
			[]string{"gpu", "uuid", "device", "modelName", "hostname"},
		),
		metriccontract.ValidateLabelValues(
			families,
			metriccontract.BaselineGPUFamilyNames(),
			map[string]string{
				"pod_name":       workloadPodName,
				"pod_namespace":  testContext.namespace,
				"container_name": workloadContainerName,
			},
		),
	)
}

// defaultMetricsContractError checks the default chart metrics using the baseline GPU contract.
func defaultMetricsContractError(metricsResponse []byte, requireKubernetesLabels bool) error {
	return baselineMetricContractError(metricsResponse, requireKubernetesLabels)
}

// customMetricsJSONValue formats custom metric CSV rows as a Helm JSON value.
func customMetricsJSONValue(lines ...string) string {
	return fmt.Sprintf("customMetrics=%s", strconv.Quote(strings.Join(lines, "\n")))
}

// shouldVerifyCustomMetricsContract checks that customMetrics fully replaces the default metric set.
func shouldVerifyCustomMetricsContract(metricsResponse []byte) {
	Expect(customMetricsContractError(metricsResponse)).Should(Succeed())
}

// customMetricsContractError returns all custom metrics contract failures found in a response.
func customMetricsContractError(metricsResponse []byte) error {
	return exportercontract.CustomMetrics(metricsResponse, workloadLabelValues())
}

// workloadLabelValues returns the Kubernetes labels expected on workload-bound GPU samples.
func workloadLabelValues() map[string]string {
	return map[string]string{
		"pod":       workloadPodName,
		"namespace": testContext.namespace,
		"container": workloadContainerName,
	}
}

// defaultCountersData reads the source or packaged default counters CSV used by the chart.
func defaultCountersData() []byte {
	candidates := []string{}
	if testContext.chart != "" {
		candidates = append(
			candidates,
			filepath.Clean(filepath.Join(testContext.chart, "..", "etc", "default-counters.csv")),
			filepath.Clean(filepath.Join(filepath.Dir(testContext.chart), "etc", "default-counters.csv")),
		)
	}
	candidates = append(
		candidates,
		filepath.Clean(filepath.Join("etc", "default-counters.csv")),
		filepath.Clean(filepath.Join("..", "..", "etc", "default-counters.csv")),
	)

	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err == nil {
			return data
		}
	}
	Fail(fmt.Sprintf("could not find etc/default-counters.csv; checked %s", strings.Join(candidates, ", ")))
	return nil
}

// supportedDefaultCounterFields parses optional field-support evidence from e2e.
func supportedDefaultCounterFields() map[string]bool {
	raw := os.Getenv("E2E_SUPPORTED_DCGM_FIELDS")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	supported := map[string]bool{}
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if strings.TrimSpace(field) == "" {
			continue
		}
		supported[strings.TrimSpace(field)] = true
	}
	return supported
}
