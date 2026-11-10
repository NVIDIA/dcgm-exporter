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

package otlp

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"go.opentelemetry.io/otel/attribute"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
)

var healthStates = [...]string{"ok", "degraded", "failed"}

// exporterRule describes the labels and value rules of one supported family.
type exporterRule struct {
	label, attribute                         string
	windowed, numeric, health, allowNegative bool
}

var exporterRules = map[string]exporterRule{
	signalcatalog.ExporterFieldGPUHealthStatus:  {health: true},
	signalcatalog.ExporterFieldClockEventsCount: {label: collector.ClockEventLabel, attribute: "com.nvidia.gpu.clock_event.type", windowed: true},
	signalcatalog.ExporterFieldClockEventsTotal: {label: collector.ClockEventLabel, attribute: "com.nvidia.gpu.clock_event.type"},
	signalcatalog.ExporterFieldXIDErrorsCount:   {label: collector.XIDLabel, attribute: "com.nvidia.gpu.xid.code", windowed: true, numeric: true},
	signalcatalog.ExporterFieldXIDErrorsTotal:   {label: collector.XIDLabel, attribute: "com.nvidia.gpu.xid.code", numeric: true},
	signalcatalog.ExporterFieldP2PStatus:        {label: collector.LinkStatusLabel, attribute: "com.nvidia.gpu.p2p.link.status", allowNegative: true},
}

// supportsExporterMetric limits conversion to the family rules implemented here.
func supportsExporterMetric(name string) bool {
	_, ok := exporterRules[name]
	return ok
}

// isWindowPlaceholder identifies zero window rows without an event identity.
func isWindowPlaceholder(name string, metric collector.Metric, raw int64) bool {
	rule := exporterRules[name]
	return raw == 0 && rule.windowed && metric.Labels[rule.label] == ""
}

// exporterAttributes maps supported exporter labels to their typed OTLP attributes.
func exporterAttributes(name string, metric collector.Metric, raw int64) ([]attribute.KeyValue, error) {
	rule, ok := exporterRules[name]
	if !ok {
		return nil, fmt.Errorf("unsupported exporter family %q", name)
	}

	if raw < 0 && !rule.allowNegative {
		return nil, errors.New("exporter count must be nonnegative")
	}

	if rule.health {
		return healthAttributes(metric.Labels), nil
	}

	return eventAttributes(metric.Labels, rule)
}

// eventAttributes maps one family's event label and optional sample window.
func eventAttributes(labels map[string]string, rule exporterRule) ([]attribute.KeyValue, error) {
	var attrs []attribute.KeyValue
	if value := labels[rule.label]; value != "" {
		attr := attribute.String(rule.attribute, value)
		if rule.numeric {
			var err error
			attr, err = numericExporterAttribute(rule.attribute, value)
			if err != nil {
				return nil, err
			}
		}
		attrs = append(attrs, attr)
	}

	if rule.windowed {
		attr, err := numericExporterAttribute("com.nvidia.dcgm.exporter.sample.window", labels[collector.WindowSizeInMSLabel])
		if err != nil {
			return nil, err
		}
		attrs = append(attrs, attr)
	}

	return attrs, nil
}

// numericExporterAttribute rejects malformed, negative, or overflowing integer labels.
func numericExporterAttribute(key, value string) (attribute.KeyValue, error) {
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return attribute.KeyValue{}, fmt.Errorf("parse %s: %w", key, err)
	}

	if number < 0 {
		return attribute.KeyValue{}, fmt.Errorf("%s must be nonnegative", key)
	}

	return attribute.Int64(key, number), nil
}

// healthState translates the raw DCGM health value to its OTLP state.
func healthState(raw int64) (string, error) {
	switch raw {
	case int64(dcgm.DCGM_HEALTH_RESULT_PASS):
		return healthStates[0], nil
	case int64(dcgm.DCGM_HEALTH_RESULT_WARN):
		return healthStates[1], nil
	case int64(dcgm.DCGM_HEALTH_RESULT_FAIL):
		return healthStates[2], nil
	default:
		return "", fmt.Errorf("unsupported health value %d", raw)
	}
}

// healthAttributes preserves health metadata; the converter adds each final state once.
func healthAttributes(labels map[string]string) []attribute.KeyValue {
	var attrs []attribute.KeyValue
	for _, pair := range [][2]string{
		{collector.HealthWatchLabel, "com.nvidia.dcgm.health.watch"},
		{collector.HealthErrorCodeLabel, "com.nvidia.dcgm.health.error.code"},
		{collector.HealthErrorSeverityLabel, "com.nvidia.dcgm.health.error.severity"},
		{collector.HealthErrorCategoryLabel, "com.nvidia.dcgm.health.error.category"},
	} {
		if value := labels[pair[0]]; value != "" {
			attrs = append(attrs, attribute.String(pair[1], value))
		}
	}

	return attrs
}

// exporterStart validates the collector start and converts Unix microseconds to nanoseconds.
func exporterStart(metric collector.Metric) (uint64, error) {
	if metric.StartTimestamp <= 0 || uint64(metric.StartTimestamp) > math.MaxUint64/1000 {
		return 0, errors.New("collector start is outside Unix nanosecond range")
	}

	return uint64(metric.StartTimestamp) * 1000, nil
}
