/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package rendermetrics

import (
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/counters"
)

/*
* The goal here is to get to the following format:
* ```
* # HELP FIELD_ID HELP_MSG
* # TYPE FIELD_ID PROM_TYPE
* FIELD_ID{gpu="GPU_INDEX_0",uuid="GPU_UUID", attr...} VALUE
* FIELD_ID{gpu="GPU_INDEX_N",uuid="GPU_UUID", attr...} VALUE
* ...
* ```
 */

// metricFamilyBuilder keeps one Prometheus family plus the sample signatures already added to it.
type metricFamilyBuilder struct {
	family *dto.MetricFamily
	seen   map[string]struct{}
}

// Render writes all metric groups as Prometheus text exposition.
func Render(
	w io.Writer,
	metricGroups map[dcgm.Field_Entity_Group]collector.MetricsByCounter,
) error {
	builders := map[string]*metricFamilyBuilder{}

	// Build families in stable entity-group order so test output and scrapes are deterministic.
	groups := make([]dcgm.Field_Entity_Group, 0, len(metricGroups))
	for group := range metricGroups {
		groups = append(groups, group)
	}
	slices.SortFunc(groups, func(a, b dcgm.Field_Entity_Group) int {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	})

	for _, group := range groups {
		if err := addGroup(builders, group, metricGroups[group]); err != nil {
			return err
		}
	}

	// Emit each family once after all groups have contributed their samples.
	names := make([]string, 0, len(builders))
	for name := range builders {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		builder := builders[name]
		sort.SliceStable(builder.family.Metric, func(i, j int) bool {
			left := labelSignature(builder.family.Metric[i].Label)
			right := labelSignature(builder.family.Metric[j].Label)

			return left < right
		})

		if _, err := expfmt.MetricFamilyToText(w, builder.family); err != nil {
			return fmt.Errorf("render metric family %q: %w", name, err)
		}
	}

	return nil
}

// RenderGroup writes one entity group's metrics using the multi-group renderer.
func RenderGroup(
	w io.Writer,
	group dcgm.Field_Entity_Group,
	metrics collector.MetricsByCounter,
) error {
	return Render(w, map[dcgm.Field_Entity_Group]collector.MetricsByCounter{group: metrics})
}

// addGroup folds one entity group into the shared metric-family builders.
func addGroup(
	builders map[string]*metricFamilyBuilder,
	group dcgm.Field_Entity_Group,
	metrics collector.MetricsByCounter,
) error {
	switch group {
	case dcgm.FE_GPU, dcgm.FE_SWITCH, dcgm.FE_LINK, dcgm.FE_CPU, dcgm.FE_CPU_CORE:
	default:

		return fmt.Errorf("unexpected group: %s", group.String())
	}

	// Counters are map keys, so sort them before appending to families.
	countersForGroup := make([]counters.Counter, 0, len(metrics))
	for counter := range metrics {
		countersForGroup = append(countersForGroup, counter)
	}
	sort.Slice(countersForGroup, func(i, j int) bool {
		return countersForGroup[i].FieldName < countersForGroup[j].FieldName
	})

	for _, counter := range countersForGroup {
		if err := addCounterMetrics(builders, group, counter, metrics[counter]); err != nil {
			return err
		}
	}

	return nil
}

// addCounterMetrics validates and appends all samples for one configured counter.
func addCounterMetrics(
	builders map[string]*metricFamilyBuilder,
	group dcgm.Field_Entity_Group,
	counter counters.Counter,
	metrics []collector.Metric,
) error {
	if len(metrics) == 0 {
		return nil
	}

	// Validate family metadata before converting any individual sample.
	metricType, err := metricTypeForCounter(counter)
	if err != nil {
		return err
	}

	if !model.LegacyValidation.IsValidMetricName(counter.FieldName) {
		return fmt.Errorf("invalid Prometheus metric name %q", counter.FieldName)
	}

	builder, err := metricFamilyBuilderFor(builders, counter, metricType)
	if err != nil {
		return err
	}

	for _, metric := range metrics {
		dtoMetric, signature, err := buildMetric(group, counter, metric, metricType)
		if err != nil {
			return err
		}

		// Prometheus samples in a family are identified entirely by their label set.
		if _, exists := builder.seen[signature]; exists {
			return fmt.Errorf("metric family %q has duplicate sample labels %s", counter.FieldName, signature)
		}

		builder.seen[signature] = struct{}{}
		builder.family.Metric = append(builder.family.Metric, dtoMetric)
	}

	return nil
}

// metricFamilyBuilderFor returns the existing family builder or creates one with matching metadata.
func metricFamilyBuilderFor(
	builders map[string]*metricFamilyBuilder,
	counter counters.Counter,
	metricType dto.MetricType,
) (*metricFamilyBuilder, error) {
	if !utf8.ValidString(counter.Help) {
		return nil, fmt.Errorf("metric family %q has invalid UTF-8 HELP text", counter.FieldName)
	}

	existing := builders[counter.FieldName]
	if existing != nil {
		// A metric family may span entity groups, but HELP and TYPE must stay identical.
		if existing.family.GetType() != metricType {
			return nil, fmt.Errorf("metric family %q has conflicting types %s and %s",
				counter.FieldName, existing.family.GetType(), metricType)
		}

		if existing.family.GetHelp() != counter.Help {
			return nil, fmt.Errorf("metric family %q has conflicting HELP text", counter.FieldName)
		}

		return existing, nil
	}

	family := &dto.MetricFamily{
		Name: stringPtr(counter.FieldName),
		Help: stringPtr(counter.Help),
		Type: metricTypePtr(metricType),
	}
	builders[counter.FieldName] = &metricFamilyBuilder{
		family: family,
		seen:   map[string]struct{}{},
	}

	return builders[counter.FieldName], nil
}

// buildMetric converts one collected metric into the matching Prometheus DTO sample.
func buildMetric(
	group dcgm.Field_Entity_Group,
	counter counters.Counter,
	metric collector.Metric,
	metricType dto.MetricType,
) (*dto.Metric, string, error) {
	value, err := parseMetricValue(counter, metric.Value)
	if err != nil {
		return nil, "", err
	}

	labels, err := metricLabels(group, metric)
	if err != nil {
		return nil, "", fmt.Errorf("metric family %q: %w", counter.FieldName, err)
	}

	dtoMetric := &dto.Metric{
		Label: labels,
	}

	// dcgm-exporter emits scalar samples only; histogram and summary need different shapes.
	switch metricType {
	case dto.MetricType_COUNTER:
		dtoMetric.Counter = &dto.Counter{Value: float64Ptr(value)}
	case dto.MetricType_GAUGE:
		dtoMetric.Gauge = &dto.Gauge{Value: float64Ptr(value)}
	case dto.MetricType_UNTYPED:
		dtoMetric.Untyped = &dto.Untyped{Value: float64Ptr(value)}
	default:

		return nil, "", fmt.Errorf("unsupported rendered metric type %s", metricType)
	}

	return dtoMetric, labelSignature(labels), nil
}

// metricTypeForCounter maps config metric types to scalar Prometheus DTO types.
func metricTypeForCounter(counter counters.Counter) (dto.MetricType, error) {
	switch counter.PromType {
	case "counter":

		return dto.MetricType_COUNTER, nil
	case "gauge":

		return dto.MetricType_GAUGE, nil
	case "untyped":

		return dto.MetricType_UNTYPED, nil
	case "label":

		return dto.MetricType_UNTYPED, fmt.Errorf("label-only counter %q cannot be rendered as a metric family",
			counter.FieldName)
	default:

		return dto.MetricType_UNTYPED, fmt.Errorf("unsupported Prometheus metric type %q for %q",
			counter.PromType, counter.FieldName)
	}
}

// parseMetricValue validates a raw collector value as a finite Prometheus scalar.
func parseMetricValue(counter counters.Counter, raw string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("metric family %q has non-numeric sample value %q", counter.FieldName, raw)
	}

	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("metric family %q has non-finite sample value %q", counter.FieldName, raw)
	}

	if counter.PromType == "counter" && value < 0 {
		return 0, fmt.Errorf("metric family %q has negative counter sample %f", counter.FieldName, value)
	}

	return value, nil
}

// metricLabels builds the renderer-owned and collector-provided labels for one sample.
func metricLabels(
	group dcgm.Field_Entity_Group,
	metric collector.Metric,
) ([]*dto.LabelPair, error) {
	builder := newLabelPairs()

	// Preserve the historical label names for each DCGM entity group.
	switch group {
	case dcgm.FE_GPU:
		if err := builder.add("gpu", metric.GPU); err != nil {
			return nil, err
		}

		if err := builder.add(metric.UUID, metric.GPUUUID); err != nil {
			return nil, err
		}

		if err := builder.add("pci_bus_id", metric.GPUPCIBusID); err != nil {
			return nil, err
		}

		if err := builder.add("device", metric.GPUDevice); err != nil {
			return nil, err
		}

		if err := builder.add("modelName", metric.GPUModelName); err != nil {
			return nil, err
		}

		if metric.MigProfile != "" {
			if err := builder.add("GPU_I_PROFILE", metric.MigProfile); err != nil {
				return nil, err
			}

			if err := builder.add("GPU_I_ID", metric.GPUInstanceID); err != nil {
				return nil, err
			}
		}

		if metric.Hostname != "" {
			if err := builder.add("hostname", metric.Hostname); err != nil {
				return nil, err
			}
		}
	case dcgm.FE_LINK:
		if err := builder.add("nvlink", metric.NvLink); err != nil {
			return nil, err
		}

		if metric.NvSwitch != "" {
			if err := builder.add("nvswitch", metric.NvSwitch); err != nil {
				return nil, err
			}
		}

		if metric.GPU != "" {
			if err := builder.add("gpu", metric.GPU); err != nil {
				return nil, err
			}
		}

		if metric.GPUUUID != "" {
			if err := builder.add("gpu_uuid", metric.GPUUUID); err != nil {
				return nil, err
			}
		}

		if metric.GPUPCIBusID != "" {
			if err := builder.add("pci_bus_id", metric.GPUPCIBusID); err != nil {
				return nil, err
			}
		}

		if metric.GPUDevice != "" {
			if err := builder.add("device", metric.GPUDevice); err != nil {
				return nil, err
			}
		}

		if metric.GPUModelName != "" {
			if err := builder.add("model_name", metric.GPUModelName); err != nil {
				return nil, err
			}
		}

		if metric.MigProfile != "" {
			if err := builder.add("GPU_I_PROFILE", metric.MigProfile); err != nil {
				return nil, err
			}

			if err := builder.add("GPU_I_ID", metric.GPUInstanceID); err != nil {
				return nil, err
			}
		}

		if metric.Hostname != "" {
			if err := builder.add("hostname", metric.Hostname); err != nil {
				return nil, err
			}
		}
	case dcgm.FE_SWITCH:
		if err := builder.add("nvswitch", metric.NvSwitch); err != nil {
			return nil, err
		}

		if metric.Hostname != "" {
			if err := builder.add("hostname", metric.Hostname); err != nil {
				return nil, err
			}
		}
	case dcgm.FE_CPU:
		if err := builder.add("cpu", metric.GPU); err != nil {
			return nil, err
		}

		if metric.CPUSerial != "" {
			if err := builder.add("cpu_serial", metric.CPUSerial); err != nil {
				return nil, err
			}
		}

		if metric.Hostname != "" {
			if err := builder.add("hostname", metric.Hostname); err != nil {
				return nil, err
			}
		}
	case dcgm.FE_CPU_CORE:
		if err := builder.add("cpucore", metric.GPU); err != nil {
			return nil, err
		}

		if err := builder.add("cpu", metric.GPUDevice); err != nil {
			return nil, err
		}

		if metric.Hostname != "" {
			if err := builder.add("hostname", metric.Hostname); err != nil {
				return nil, err
			}
		}
	default:

		return nil, fmt.Errorf("unexpected group: %s", group.String())
	}

	// Collector labels and transformed attributes must not collide with renderer labels.
	if err := builder.addMap(metric.Labels); err != nil {
		return nil, err
	}

	if err := builder.addMap(metric.Attributes); err != nil {
		return nil, err
	}

	return builder.labels, nil
}

// labelPairs accumulates label pairs while enforcing Prometheus label invariants.
type labelPairs struct {
	labels []*dto.LabelPair
	seen   map[string]struct{}
}

// newLabelPairs creates an empty checked label accumulator.
func newLabelPairs() *labelPairs {
	return &labelPairs{
		seen: map[string]struct{}{},
	}
}

// add validates and appends one label pair.
func (l *labelPairs) add(name string, value string) error {
	if !model.LegacyValidation.IsValidLabelName(name) {
		return fmt.Errorf("invalid Prometheus label name %q", name)
	}

	if strings.HasPrefix(name, "__") {
		return fmt.Errorf("reserved Prometheus label name %q", name)
	}

	if !utf8.ValidString(value) {
		return fmt.Errorf("prometheus label %q has invalid UTF-8 value", name)
	}

	if _, exists := l.seen[name]; exists {
		return fmt.Errorf("duplicate label name %q", name)
	}

	l.seen[name] = struct{}{}
	l.labels = append(l.labels, &dto.LabelPair{
		Name:  stringPtr(name),
		Value: stringPtr(value),
	})

	return nil
}

// addMap appends labels from a map in stable key order.
func (l *labelPairs) addMap(labels map[string]string) error {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if err := l.add(key, labels[key]); err != nil {
			return err
		}
	}

	return nil
}

// labelSignature returns a collision-safe identity for a metric label set.
func labelSignature(labels []*dto.LabelPair) string {
	sortedLabels := slices.Clone(labels)
	sort.Slice(sortedLabels, func(i, j int) bool {
		if sortedLabels[i].GetName() == sortedLabels[j].GetName() {
			return sortedLabels[i].GetValue() < sortedLabels[j].GetValue()
		}

		return sortedLabels[i].GetName() < sortedLabels[j].GetName()
	})

	// Length prefixes keep embedded separators from collapsing distinct label sets.
	var builder strings.Builder
	for _, label := range sortedLabels {
		builder.WriteString(strconv.Itoa(len(label.GetName())))
		builder.WriteByte(':')
		builder.WriteString(label.GetName())
		builder.WriteByte('=')
		builder.WriteString(strconv.Itoa(len(label.GetValue())))
		builder.WriteByte(':')
		builder.WriteString(label.GetValue())
		builder.WriteByte(';')
	}

	return builder.String()
}

// stringPtr returns a pointer to value for Prometheus DTO fields.
func stringPtr(value string) *string {
	return &value
}

// float64Ptr returns a pointer to value for Prometheus DTO fields.
func float64Ptr(value float64) *float64 {
	return &value
}

// metricTypePtr returns a pointer to value for Prometheus DTO fields.
func metricTypePtr(value dto.MetricType) *dto.MetricType {
	return &value
}
