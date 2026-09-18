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
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

// exporterMetrics owns optional process instrumentation and wraps the existing /metrics handler.
type exporterMetrics struct {
	registry *prometheus.Registry
	handler  http.Handler
}

// newExporterMetrics registers the standard collectors and instruments completed HTTP requests.
func newExporterMetrics(next http.Handler) *exporterMetrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return &exporterMetrics{
		registry: registry,
		handler:  promhttp.InstrumentMetricHandler(registry, next),
	}
}

// ServeHTTP records handler activity while serving the existing DCGM metrics response.
func (m *exporterMetrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.handler.ServeHTTP(w, r)
}

// appendTo gathers and encodes exporter metrics atomically after DCGM metrics are rendered.
func (m *exporterMetrics) appendTo(destination *bytes.Buffer) error {
	if m == nil {
		return nil
	}

	families, err := m.registry.Gather()
	if err != nil {
		return fmt.Errorf("gather exporter metrics: %w", err)
	}

	var encoded bytes.Buffer
	for _, family := range families {
		if _, err := expfmt.MetricFamilyToText(&encoded, family); err != nil {
			return fmt.Errorf("encode exporter metric %q: %w", family.GetName(), err)
		}
	}

	if _, err := encoded.WriteTo(destination); err != nil {
		return fmt.Errorf("append exporter metrics: %w", err)
	}

	return nil
}
