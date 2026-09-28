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

package rendermetrics_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/benchmarktest"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/rendermetrics"
)

// BenchmarkRender measures metric-family construction, validation, sorting, and Prometheus
// text encoding to io.Discard. Fixture construction, the preflight buffered render, and
// output retention are excluded from the timed loop.
func BenchmarkRender(b *testing.B) {
	for _, benchmarkCase := range benchmarktest.Cases() {
		b.Run(benchmarkCase.Name(), func(b *testing.B) {
			metricGroups := benchmarkMetricGroups(benchmarkCase)
			var output bytes.Buffer
			if err := rendermetrics.Render(&output, metricGroups); err != nil {
				b.Fatalf("validate benchmark fixture: %v", err)
			}

			outputBytes := output.Len()
			b.ReportAllocs()
			b.SetBytes(int64(outputBytes))

			for b.Loop() {
				if err := rendermetrics.Render(io.Discard, metricGroups); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(outputBytes), "output-bytes/op")
			b.ReportMetric(float64(benchmarkCase.Series()), "series/op")
		})
	}
}

func TestBenchmarkMetricGroups(t *testing.T) {
	for _, benchmarkCase := range benchmarktest.Cases() {
		t.Run(benchmarkCase.Name(), func(t *testing.T) {
			var output bytes.Buffer
			if err := rendermetrics.Render(&output, benchmarkMetricGroups(benchmarkCase)); err != nil {
				t.Fatalf("render benchmark fixture: %v", err)
			}

			parser := expfmt.NewTextParser(model.LegacyValidation)
			families, err := parser.TextToMetricFamilies(&output)
			if err != nil {
				t.Fatalf("parse rendered benchmark fixture: %v", err)
			}
			if len(families) != benchmarkCase.FieldCount {
				t.Fatalf("metric family count = %d, want %d", len(families), benchmarkCase.FieldCount)
			}

			series := 0
			for _, family := range families {
				series += len(family.Metric)
			}
			if series != benchmarkCase.Series() {
				t.Fatalf("series count = %d, want %d", series, benchmarkCase.Series())
			}
		})
	}
}

func benchmarkMetricGroups(benchmarkCase benchmarktest.Case) map[dcgm.Field_Entity_Group]collector.MetricsByCounter {
	return map[dcgm.Field_Entity_Group]collector.MetricsByCounter{
		dcgm.FE_GPU: benchmarktest.SyntheticGPUMetrics(benchmarkCase),
	}
}
