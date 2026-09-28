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
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/benchmarktest"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatcher"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/devicewatchlistmanager"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
)

// fakeDCGMBenchmarkCollector returns prebuilt metrics without loading or calling DCGM.
type fakeDCGMBenchmarkCollector struct {
	metrics collector.MetricsByCounter
}

func (c *fakeDCGMBenchmarkCollector) GetMetrics() (collector.MetricsByCounter, error) {
	return c.metrics, nil
}

func (*fakeDCGMBenchmarkCollector) Cleanup() {}

// fakeDCGMBenchmarkWatchListManager exposes a GPU watch list without device information.
// The benchmark intentionally omits transformations, so the zero-value watch list is sufficient.
type fakeDCGMBenchmarkWatchListManager struct{}

func (*fakeDCGMBenchmarkWatchListManager) CreateEntityWatchList(
	dcgm.Field_Entity_Group,
	devicewatcher.Watcher,
	int64,
) error {
	return nil
}

func (*fakeDCGMBenchmarkWatchListManager) EntityWatchList(
	group dcgm.Field_Entity_Group,
) (devicewatchlistmanager.WatchList, bool) {
	return devicewatchlistmanager.WatchList{}, group == dcgm.FE_GPU
}

// benchmarkResponseWriter records status and byte counts without retaining a second response copy.
type benchmarkResponseWriter struct {
	header http.Header
	status int
	bytes  int64
}

func newBenchmarkResponseWriter() *benchmarkResponseWriter {
	return &benchmarkResponseWriter{
		header: make(http.Header, 2),
	}
}

func (w *benchmarkResponseWriter) Header() http.Header {
	return w.header
}

func (w *benchmarkResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *benchmarkResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.bytes += int64(len(p))

	return len(p), nil
}

func (w *benchmarkResponseWriter) Reset() {
	w.status = 0
	w.bytes = 0
	clear(w.header)
}

// BenchmarkFakeDCGMScrape measures the in-process /metrics handler with a prebuilt collector.
// Each timed iteration includes writer reset, scrape coordination, registry gather, the empty
// transformation pass, rendering, handler response buffering, and count-only response writes.
// Fixture, server, and request construction, DCGM calls, production transformations, HTTP
// transport, and response-body retention by the test harness are excluded.
func BenchmarkFakeDCGMScrape(b *testing.B) {
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	for _, benchmarkCase := range benchmarktest.Cases() {
		b.Run(benchmarkCase.Name(), func(b *testing.B) {
			handler := newFakeDCGMBenchmarkHandler(benchmarkCase)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			validateFakeDCGMBenchmarkResponse(b, recorder, benchmarkCase)

			writer := newBenchmarkResponseWriter()
			var responseBytes int64
			b.ReportAllocs()

			for b.Loop() {
				writer.Reset()
				handler.ServeHTTP(writer, request)
				if writer.status != http.StatusOK {
					b.Fatalf("response status = %d, want %d", writer.status, http.StatusOK)
				}
				responseBytes += writer.bytes
			}

			b.ReportMetric(float64(responseBytes)/float64(b.N), "response-bytes/op")
			b.ReportMetric(float64(benchmarkCase.Series()), "series/op")
		})
	}
}

func TestFakeDCGMBenchmarkServer(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	for _, benchmarkCase := range benchmarktest.Cases() {
		t.Run(benchmarkCase.Name(), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newFakeDCGMBenchmarkHandler(benchmarkCase).ServeHTTP(recorder, request)
			validateFakeDCGMBenchmarkResponse(t, recorder, benchmarkCase)
		})
	}
}

func validateFakeDCGMBenchmarkResponse(
	tb testing.TB,
	recorder *httptest.ResponseRecorder,
	benchmarkCase benchmarktest.Case,
) {
	tb.Helper()
	if recorder.Code != http.StatusOK {
		tb.Fatalf("response status = %d, want %d", recorder.Code, http.StatusOK)
	}

	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		tb.Fatalf("parse benchmark response: %v", err)
	}

	syntheticFamilies := 0
	syntheticSeries := 0
	for name, family := range families {
		if !strings.HasPrefix(name, benchmarktest.SyntheticMetricNamePrefix) {
			continue
		}
		syntheticFamilies++
		syntheticSeries += len(family.Metric)
	}
	if syntheticFamilies != benchmarkCase.FieldCount {
		tb.Fatalf("synthetic metric family count = %d, want %d",
			syntheticFamilies, benchmarkCase.FieldCount)
	}
	if syntheticSeries != benchmarkCase.Series() {
		tb.Fatalf("synthetic series count = %d, want %d", syntheticSeries, benchmarkCase.Series())
	}
}

// newFakeDCGMBenchmarkHandler enables exporter metrics around the fake DCGM scrape benchmark.
func newFakeDCGMBenchmarkHandler(benchmarkCase benchmarktest.Case) http.Handler {
	reg := registry.NewRegistry()
	tuple := collector.EntityCollectorTuple{}
	tuple.SetEntity(dcgm.FE_GPU)
	tuple.SetCollector(&fakeDCGMBenchmarkCollector{
		metrics: benchmarktest.SyntheticGPUMetrics(benchmarkCase),
	})
	reg.Register(tuple)

	metricServer := &MetricsServer{
		deviceWatchListManager: &fakeDCGMBenchmarkWatchListManager{},
		scrapes:                newScrapeCoordinator(1),
	}
	metricServer.registry.Store(reg)
	metricServer.exporterMetrics = newExporterMetrics(http.HandlerFunc(metricServer.Metrics))

	return metricServer.exporterMetrics
}
