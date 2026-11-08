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
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/exportercontract"
	"github.com/NVIDIA/dcgm-exporter/tests/internal/metriccontract"
)

// shouldEventuallyMeetBaselineMetricContract scrapes with read until the
// baseline contract holds. A row DCGM supports can lack a value in its first
// samples (profiling fields are N/A in the first one, and the chart collects
// every 30 seconds), so one scrape can miss it. read must report a failed
// scrape through the Gomega handle it is given, so the poll retries instead of
// failing the spec.
func shouldEventuallyMeetBaselineMetricContract(read func(g Gomega) []byte, requireKubernetesLabels bool) {
	Eventually(func(g Gomega) {
		g.Expect(baselineMetricContractError(read(g), requireKubernetesLabels)).To(Succeed())
	}).WithPolling(5 * time.Second).Within(2 * time.Minute).Should(Succeed())
}

// baselineMetricContractError returns all baseline contract failures found in a metrics response.
func baselineMetricContractError(metricsResponse []byte, requireKubernetesLabels bool) error {
	return exportercontract.BaselineMetrics(metricsResponse, baselineMetricContractOptions(requireKubernetesLabels))
}

// defaultCSVMetricContractError is baselineMetricContractError for an exporter
// that runs with the chart's default CSV: it also fails DCGM_FI_* families
// outside that CSV and HELP text that differs from it.
func defaultCSVMetricContractError(metricsResponse []byte) error {
	opts := baselineMetricContractOptions(true)
	opts.ExactCSV = true
	return exportercontract.BaselineMetrics(metricsResponse, opts)
}

// baselineMetricContractOptions returns the baseline contract options shared
// by the Kubernetes scenarios.
func baselineMetricContractOptions(requireKubernetesLabels bool) exportercontract.BaselineOptions {
	return exportercontract.BaselineOptions{
		RequireHostname:         true,
		RequireKubernetesLabels: requireKubernetesLabels,
		KubernetesLabels:        workloadLabelValues(),
		DefaultCounterRows:      testContext.defaultCounterRows,
		FieldSupport:            testContext.fieldSupport,
		CapabilityWriter:        GinkgoWriter,
	}
}

// shouldLoadDefaultCounterContract parses the default counters CSV and the
// E2E_DCGM_FIELD_SUPPORT evidence once for the suite. A CSV that does not
// parse or evidence that names a row the CSV does not enable fails the suite
// before any scenario starts.
func shouldLoadDefaultCounterContract() {
	rows, err := metriccontract.ReadDefaultCounterRows(bytes.NewReader(defaultCountersData()))
	Expect(err).NotTo(HaveOccurred(), "default counters CSV should parse")

	support, err := metriccontract.ParseFieldSupport(os.Getenv(metriccontract.FieldSupportEnv), rows)
	Expect(err).NotTo(HaveOccurred(), "DCGM field support evidence should name enabled default counters")

	testContext.defaultCounterRows = rows
	testContext.fieldSupport = support
}

// legacyNamespaceMetricContractError checks default metrics emitted with legacy label names.
func legacyNamespaceMetricContractError(metricsResponse []byte) error {
	families, err := metriccontract.ParseText(metricsResponse)
	if err != nil {
		return fmt.Errorf("metrics should parse as Prometheus text: %w", err)
	}

	return errors.Join(
		metriccontract.ValidateDefaultCounterRows(
			families,
			testContext.defaultCounterRows,
			metriccontract.DefaultCounterOptions{
				FieldSupport: testContext.fieldSupport,
				MetricLabels: []string{"gpu", "uuid", "device", "modelName"},
				SkipWriter:   GinkgoWriter,
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

// customMetricsJSONValue formats custom metric CSV rows as a Helm JSON value.
func customMetricsJSONValue(lines ...string) string {
	return fmt.Sprintf("customMetrics=%s", strconv.Quote(strings.Join(lines, "\n")))
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
