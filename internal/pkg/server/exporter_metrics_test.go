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

package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mockcollectorpkg "github.com/NVIDIA/dcgm-exporter/internal/mocks/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
)

// TestExporterMetricsShareTheMetricsEndpointAndSurviveReload verifies the public metric families and reload lifetime.
func TestExporterMetricsShareTheMetricsEndpointAndSurviveReload(t *testing.T) {
	metricServer, handler := newExporterMetricsTestHandler(t)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, first.Code)
	firstFamilies := parseMetricFamilies(t, first.Body.String())

	assert.Contains(t, firstFamilies, "TEST_METRIC")
	assert.Contains(t, firstFamilies, "go_goroutines")
	assert.Contains(t, firstFamilies, "promhttp_metric_handler_requests_in_flight")
	assert.Contains(t, firstFamilies, "promhttp_metric_handler_requests_total")
	assert.True(t, hasMetricFamilyPrefix(firstFamilies, "process_"))
	assert.Equal(t, float64(1), metricFamilyValue(
		t,
		firstFamilies["promhttp_metric_handler_requests_in_flight"],
		nil,
	))
	assert.Equal(t, float64(0), metricFamilyValue(
		t,
		firstFamilies["promhttp_metric_handler_requests_total"],
		map[string]string{"code": "200"},
	))

	dcgmOffset := strings.Index(first.Body.String(), "# HELP TEST_METRIC")
	goOffset := strings.Index(first.Body.String(), "# HELP go_")
	require.NotEqual(t, -1, dcgmOffset)
	require.NotEqual(t, -1, goOffset)
	assert.Less(t, dcgmOffset, goOffset)

	oldRegistry := metricServer.SwapMetricsRuntime(
		registry.NewRegistry(),
		metricServer.deviceWatchListManager,
	)
	require.NotNil(t, oldRegistry)

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, second.Code)
	secondFamilies := parseMetricFamilies(t, second.Body.String())
	assert.NotContains(t, secondFamilies, "TEST_METRIC")
	assert.Equal(t, float64(1), metricFamilyValue(
		t,
		secondFamilies["promhttp_metric_handler_requests_total"],
		map[string]string{"code": "200"},
	))
}

// TestExporterMetricsAreDisabledByDefault preserves the pre-4.8.4 /metrics response unless explicitly enabled.
func TestExporterMetricsAreDisabledByDefault(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(getMetricsByCounterWithTestMetric(), nil).AnyTimes()
	testServer := newMetricsTestServer(t, ctrl, mockCollector, nil, 1)

	metricServer, cleanup, err := NewMetricsServer(
		&appconfig.Config{Address: ":0", MaxConcurrentScrapes: 1},
		testServer.deviceWatchListManager,
		testServer.registry.Load(),
	)
	require.NoError(t, err)
	defer cleanup()
	require.Nil(t, metricServer.exporterMetrics)

	recorder := httptest.NewRecorder()
	metricServer.server.Handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, expectedResponse, recorder.Body.String())
}

// TestExporterMetricFailureDoesNotFailDCGMMetrics verifies that optional instrumentation cannot corrupt a DCGM response.
func TestExporterMetricFailureDoesNotFailDCGMMetrics(t *testing.T) {
	_, handler := newExporterMetricsTestHandlerWithSetup(t, func(metrics *exporterMetrics) {
		metrics.registry.MustRegister(invalidExporterMetricsCollector{})
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, expectedResponse, recorder.Body.String())
}

// invalidExporterMetricsCollector injects a gather failure for failure-isolation testing.
type invalidExporterMetricsCollector struct{}

// Describe implements prometheus.Collector without publishing a descriptor.
func (invalidExporterMetricsCollector) Describe(chan<- *prometheus.Desc) {}

// Collect emits an invalid metric so registry gathering fails.
func (invalidExporterMetricsCollector) Collect(metrics chan<- prometheus.Metric) {
	metrics <- prometheus.NewInvalidMetric(
		prometheus.NewDesc("invalid_exporter_metric", "Invalid test metric.", nil, nil),
		errors.New("injected exporter metric failure"),
	)
}

// newExporterMetricsTestHandler constructs an enabled exporter-metrics handler for endpoint tests.
func newExporterMetricsTestHandler(t *testing.T) (*MetricsServer, http.Handler) {
	t.Helper()

	return newExporterMetricsTestHandlerWithSetup(t, nil)
}

// newExporterMetricsTestHandlerWithSetup lets failure tests modify the exporter registry before scraping.
func newExporterMetricsTestHandlerWithSetup(
	t *testing.T,
	setup func(*exporterMetrics),
) (*MetricsServer, http.Handler) {
	t.Helper()

	ctrl := gomock.NewController(t)
	mockCollector := mockcollectorpkg.NewMockCollector(ctrl)
	mockCollector.EXPECT().GetMetrics().Return(getMetricsByCounterWithTestMetric(), nil).AnyTimes()

	testServer := newMetricsTestServer(t, ctrl, mockCollector, nil, 1)
	metricServer, cleanup, err := NewMetricsServer(
		&appconfig.Config{
			Address:               ":0",
			MaxConcurrentScrapes:  1,
			EnableExporterMetrics: true,
		},
		testServer.deviceWatchListManager,
		testServer.registry.Load(),
	)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	if setup != nil {
		setup(metricServer.exporterMetrics)
	}

	return metricServer, metricServer.server.Handler
}

// parseMetricFamilies parses one Prometheus text response for contract assertions.
func parseMetricFamilies(t *testing.T, text string) map[string]*dto.MetricFamily {
	t.Helper()

	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(text))
	require.NoError(t, err)

	return families
}

// hasMetricFamilyPrefix reports whether a response contains at least one family with the prefix.
func hasMetricFamilyPrefix(families map[string]*dto.MetricFamily, prefix string) bool {
	for name := range families {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

// metricFamilyValue returns the counter or gauge value for the exact requested label set.
func metricFamilyValue(
	t *testing.T,
	family *dto.MetricFamily,
	wantLabels map[string]string,
) float64 {
	t.Helper()
	require.NotNil(t, family)

	for _, metric := range family.Metric {
		if !metricLabelsMatch(metric, wantLabels) {
			continue
		}
		if metric.Counter != nil {
			return metric.Counter.GetValue()
		}
		if metric.Gauge != nil {
			return metric.Gauge.GetValue()
		}
		t.Fatalf("metric family %q has unsupported sample type", family.GetName())
	}

	t.Fatalf("metric family %q has no sample with labels %v", family.GetName(), wantLabels)

	return 0
}

// metricLabelsMatch requires an exact label-name and label-value match.
func metricLabelsMatch(metric *dto.Metric, want map[string]string) bool {
	if len(metric.Label) != len(want) {
		return false
	}
	for name, value := range want {
		matched := false
		for _, label := range metric.Label {
			if label.GetName() == name && label.GetValue() == value {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}
