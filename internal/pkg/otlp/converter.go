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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"go.opentelemetry.io/otel/attribute"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/collector"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/registry"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/signalcatalog"
)

// Converter builds OTLP metrics without modifying the collected samples.
// Calls on one instance must be sequential. Create a new instance when
// collectors restart or hardware topology changes to restart cumulative periods.
// The converter does not collect or send metrics.
type Converter struct {
	signals  []signalcatalog.Signal
	resource attribute.Set
	periods  map[sourceIdentity]sourcePeriod
}

// NewConverter checks the selected signal IDs and resource identity once.
// Catalog omissions and attribute-only signals do not create numeric metrics.
func NewConverter(ids []signalcatalog.SignalID, identity ResourceIdentity) (*Converter, error) {
	resource, err := BuildResourceAttributes(identity)
	if err != nil {
		return nil, fmt.Errorf("resource identity: %w", err)
	}
	c := &Converter{resource: attribute.NewSet(resource...), periods: make(map[sourceIdentity]sourcePeriod)}
	seen := make(map[signalcatalog.SignalID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("duplicate signal %q", id)
		}
		seen[id] = true
		signal, ok := signalcatalog.Lookup(id)
		if !ok {
			return nil, fmt.Errorf("unknown signal %q", id)
		}
		if signal.Source.Kind != signalcatalog.SourceDCGM && !supportsExporterMetric(signal.Source.Name) {
			return nil, fmt.Errorf("signal %q has an unsupported source %q", id, signal.Source.Name)
		}
		if signal.Output.OutputKind == signalcatalog.OutputMetric {
			if signal.Output.SourceOverride.FieldID != 0 {
				signal.Source = signal.Output.SourceOverride
			}
			c.signals = append(c.signals, signal)
		}
	}
	return c, nil
}

type sourceSamples struct {
	group   dcgm.Field_Entity_Group
	metrics []collector.Metric
}

type sampleKey struct {
	fieldID      dcgm.Short
	exporterName string
}

// sourceKey prevents exporter family names from colliding with native field IDs.
func sourceKey(source signalcatalog.Source) sampleKey {
	if source.Kind == signalcatalog.SourceExporter {
		return sampleKey{exporterName: source.Name}
	}
	return sampleKey{fieldID: dcgm.Short(source.FieldID)}
}

type pointIdentity struct {
	name       string
	attributes attribute.Distinct
}

type sourceIdentity struct {
	signal   signalcatalog.SignalID
	hardware string
}

// sourcePeriod records the first and latest sample times in nanoseconds and
// the latest raw DCGM counter value, before unit conversion.
type sourcePeriod struct {
	start, last uint64
	value       int64
}

// Convert returns OTLP metrics that the caller may modify.
// The snapshot must remain unchanged during the call. Missing fields and
// unsupported hardware types and incomplete hardware identities are skipped.
// Other errors, including cancellation, return nil and leave cumulative start times unchanged.
func (c *Converter) Convert(ctx context.Context, snapshot registry.MetricsByCounterGroup) (*metricspb.ResourceMetrics, error) {
	return c.convert(ctx, snapshot, make(map[pointIdentity]bool), make(map[sourceIdentity]sourcePeriod))
}

// convert owns the supplied scratch maps and saves periods only after the final cancellation check.
func (c *Converter) convert(ctx context.Context, snapshot registry.MetricsByCounterGroup, seen map[pointIdentity]bool, pending map[sourceIdentity]sourcePeriod) (*metricspb.ResourceMetrics, error) {
	index, err := indexSourceSamples(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	resource, err := protoAttributes(c.resource)
	if err != nil {
		return nil, err
	}

	scope := &metricspb.ScopeMetrics{Scope: &commonpb.InstrumentationScope{Name: "github.com/NVIDIA/dcgm-exporter"}}
	families := make(map[string]*metricspb.Metric)
	for _, signal := range c.signals {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, samples := range index[sourceKey(signal.Source)] {
			points, err := c.convertSamples(ctx, signal, samples, seen, pending)
			if err != nil {
				return nil, fmt.Errorf("signal %q (source %q): %w", signal.ID, signal.Source.Name, err)
			}
			appendMetricPoints(scope, families, signal, points)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for key, period := range pending {
		c.periods[key] = period
	}

	return &metricspb.ResourceMetrics{Resource: &resourcepb.Resource{Attributes: resource}, ScopeMetrics: []*metricspb.ScopeMetrics{scope}}, nil
}

func indexSourceSamples(ctx context.Context, snapshot registry.MetricsByCounterGroup) (map[sampleKey][]sourceSamples, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index := make(map[sampleKey][]sourceSamples)
	for group, counters := range snapshot {
		for counter, metrics := range counters {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key := sampleKey{fieldID: counter.FieldID}
			if supportsExporterMetric(counter.FieldName) {
				key = sampleKey{exporterName: counter.FieldName}
			}
			index[key] = append(index[key], sourceSamples{group, metrics})
		}
	}
	return index, nil
}

func appendMetricPoints(scope *metricspb.ScopeMetrics, families map[string]*metricspb.Metric, signal signalcatalog.Signal, points []*metricspb.NumberDataPoint) {
	if len(points) == 0 {
		return
	}
	family := families[signal.Output.Name]
	if family == nil {
		family = &metricspb.Metric{Name: signal.Output.Name, Unit: signal.Output.Unit}
		if signal.Output.Instrument == signalcatalog.InstrumentGauge {
			family.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{}}
		} else {
			family.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE, IsMonotonic: signal.Output.Monotonic}}
		}
		families[family.Name] = family
		scope.Metrics = append(scope.Metrics, family)
	}
	if gauge := family.GetGauge(); gauge != nil {
		gauge.DataPoints = append(gauge.DataPoints, points...)
	} else {
		family.GetSum().DataPoints = append(family.GetSum().DataPoints, points...)
	}
}

// convertSamples builds points for supported hardware and its workloads.
// It rejects duplicate points and holds counter updates until Convert succeeds.
func (c *Converter) convertSamples(ctx context.Context, signal signalcatalog.Signal, samples sourceSamples, seen map[pointIdentity]bool, pending map[sourceIdentity]sourcePeriod) ([]*metricspb.NumberDataPoint, error) {
	var points []*metricspb.NumberDataPoint
	skipped := 0
	for _, metric := range samples.metrics {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		next, err := c.convertMetric(ctx, signal, samples.group, metric, points, seen, pending)
		if errors.Is(err, errIncompleteHardwareIdentity) {
			skipped++
			continue
		}
		if err != nil {
			return nil, err
		}
		points = next
	}

	if skipped != 0 {
		slog.Warn("Skipping OTLP samples with incomplete hardware identity", "signal", signal.ID, "samples", skipped)
	}

	return points, nil
}

func (c *Converter) convertMetric(ctx context.Context, signal signalcatalog.Signal, group dcgm.Field_Entity_Group, metric collector.Metric, points []*metricspb.NumberDataPoint, seen map[pointIdentity]bool, pending map[sourceIdentity]sourcePeriod) ([]*metricspb.NumberDataPoint, error) {
	if metric.Source == nil {
		return nil, fmt.Errorf("%w: hardware source identity is required", errIncompleteHardwareIdentity)
	}
	if !supportsHardwareEntity(signal, metric.Source.EntityGroup) {
		return points, nil
	}
	number, extra, err := prepareNumber(signal, metric)
	if err != nil {
		return nil, err
	}
	if number.point == nil {
		return points, nil
	}
	containers := workloadContainers(metric)
	for i := range max(1, len(containers)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var container *collector.ContainerIdentity
		if len(containers) != 0 {
			container = &containers[i]
		}
		set, err := pointAttributes(signal, group, metric, container, extra)
		if err != nil {
			return nil, err
		}
		points, err = c.convertDataPoint(signal, set, number.forPoint(i), points, seen, pending)
		if err != nil {
			return nil, err
		}
	}

	return points, nil
}

// supportsHardwareEntity accepts GPU instances only when the signal declares that scope.
func supportsHardwareEntity(signal signalcatalog.Signal, entity dcgm.Field_Entity_Group) bool {
	return entity == dcgm.FE_GPU || (entity == dcgm.FE_GPU_I && signal.Entity == signalcatalog.EntityGPUOrMIGGI)
}

// workloadContainers avoids extra container points for samples already assigned to Kubernetes workloads.
func workloadContainers(metric collector.Metric) []collector.ContainerIdentity {
	if metric.Attributes["pod"] != "" || metric.Attributes["pod_name"] != "" || metric.Attributes["namespace"] != "" || metric.Attributes["pod_namespace"] != "" {
		return nil
	}
	return metric.Source.Containers
}

type numberSample struct {
	point *metricspb.NumberDataPoint
	raw   int64
}

// Each emitted point owns its numeric value.
func (n numberSample) forPoint(index int) numberSample {
	if index == 0 {
		return n
	}
	point := &metricspb.NumberDataPoint{TimeUnixNano: n.point.TimeUnixNano, StartTimeUnixNano: n.point.StartTimeUnixNano}
	if value, ok := n.point.Value.(*metricspb.NumberDataPoint_AsInt); ok {
		point.Value = &metricspb.NumberDataPoint_AsInt{AsInt: value.AsInt}
	} else {
		point.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: n.point.GetAsDouble()}
	}
	return numberSample{point: point, raw: n.raw}
}

// prepareNumber parses each sample once before workload and health-state expansion.
// An empty numberSample means an unclassified zero window row must be skipped.
func prepareNumber(signal signalcatalog.Signal, metric collector.Metric) (numberSample, []attribute.KeyValue, error) {
	point, raw, err := convertNumber(signal, metric)
	if err != nil {
		return numberSample{}, nil, err
	}
	number := numberSample{point: point, raw: raw}
	if signal.Source.Kind != signalcatalog.SourceExporter {
		return number, nil, nil
	}

	// Unclassified zero window rows remain in Prometheus but have no OTLP identity.
	if isWindowPlaceholder(signal.Source.Name, metric, raw) {
		return numberSample{}, nil, nil
	}

	attrs, err := exporterAttributes(signal.Source.Name, metric, raw)
	if err != nil {
		return numberSample{}, nil, err
	}

	if signal.Output.Reset == signalcatalog.ResetExporterRestart {
		point.StartTimeUnixNano, err = exporterStart(metric)
		if err != nil {
			return numberSample{}, nil, err
		}

		// A backward clock step can put the observation before the collector start.
		point.StartTimeUnixNano = min(point.StartTimeUnixNano, point.TimeUnixNano)
	}

	return number, attrs, nil
}

// convertDataPoint emits independently owned points and defers native period updates until conversion succeeds.
func (c *Converter) convertDataPoint(signal signalcatalog.Signal, set attribute.Set, number numberSample, points []*metricspb.NumberDataPoint, seen map[pointIdentity]bool, pending map[sourceIdentity]sourcePeriod) ([]*metricspb.NumberDataPoint, error) {
	if signal.Output.Temporality == signalcatalog.TemporalityCumulative && signal.Source.Kind == signalcatalog.SourceDCGM {
		hardware, _ := set.Value("hw.id")
		start, err := c.sourceStart(signal, hardware.AsString(), number.point.TimeUnixNano, number.raw, pending)
		if err != nil {
			return nil, err
		}

		number.point.StartTimeUnixNano = start
	}

	states := []string{""}
	var active string
	if signal.Output.Conversion == signalcatalog.ConversionHealthState {
		var err error
		active, err = healthState(number.raw)
		if err != nil {
			return nil, err
		}

		states = healthStates[:]
	}

	for i, state := range states {
		current := number.forPoint(i).point
		attrs := set
		if state != "" {
			attrs = setHealthState(current, set, state, active)
		}

		point, err := finishDataPoint(signal, attrs, current, seen)
		if err != nil {
			return nil, err
		}
		points = append(points, point)
	}

	return points, nil
}

// setHealthState writes one independently owned state value and adds its final attribute.
func setHealthState(point *metricspb.NumberDataPoint, set attribute.Set, state, active string) attribute.Set {
	var value int64
	if state == active {
		value = 1
	}

	point.Value = &metricspb.NumberDataPoint_AsInt{AsInt: value}
	return attribute.NewSet(append(set.ToSlice(), attribute.String("hw.state", state))...)
}

// finishDataPoint validates the complete point identity and encodes independently owned attributes.
func finishDataPoint(signal signalcatalog.Signal, set attribute.Set, point *metricspb.NumberDataPoint, seen map[pointIdentity]bool) (*metricspb.NumberDataPoint, error) {
	for _, key := range signal.RequiredIdentity {
		if _, ok := set.Value(attribute.Key(key)); !ok {
			if key == "com.nvidia.gpu.peer.uuid" {
				return nil, fmt.Errorf("%w: required identity %q is missing", errIncompleteHardwareIdentity, key)
			}
			return nil, fmt.Errorf("required identity %q is missing", key)
		}
	}

	key := pointIdentity{signal.Output.Name, set.Equivalent()}
	if seen[key] {
		return nil, errors.New("duplicate data point identity")
	}

	seen[key] = true
	encoded, err := protoAttributes(set)
	if err != nil {
		return nil, err
	}

	point.Attributes = encoded

	return point, nil
}

// sourceStart keeps one counter start time per signal and hardware.
// Counters that only increase must match at equal timestamps.
// A later decrease starts a new period; framebuffer usage may decrease without a reset.
func (c *Converter) sourceStart(signal signalcatalog.Signal, hardware string, timestamp uint64, raw int64, pending map[sourceIdentity]sourcePeriod) (uint64, error) {
	key := sourceIdentity{signal.ID, hardware}
	period, ok := pending[key]
	if !ok {
		period, ok = c.periods[key]
	}
	if !ok {
		period.start = timestamp
	} else {
		if timestamp < period.last {
			return 0, errors.New("source timestamp moved backwards")
		}
		if signal.Output.Monotonic {
			if timestamp == period.last && raw != period.value {
				return 0, errors.New("conflicting source values at one timestamp")
			}
			if raw < period.value {
				period.start = timestamp
			}
		}
	}
	period.last, period.value = timestamp, raw
	pending[key] = period
	return period.start, nil
}

// pointAttributes combines hardware, workload, catalog, and metric-specific attributes.
func pointAttributes(signal signalcatalog.Signal, group dcgm.Field_Entity_Group, metric collector.Metric, container *collector.ContainerIdentity, extra []attribute.KeyValue) (attribute.Set, error) {
	set, err := buildDataPointAttributeSet(group, metric, container)
	if err != nil {
		return attribute.Set{}, err
	}
	if len(signal.Output.Attributes) != 0 || len(extra) != 0 {
		attrs := set.ToSlice()
		attrs = append(attrs, extra...)
		for _, fixed := range signal.Output.Attributes {
			attrs = append(attrs, attribute.String(fixed.Name, fixed.Value))
			if fixed.Name == "hw.sensor_location" {
				parent := "gpu:" + metric.GPUUUID
				attrs = append(attrs, attribute.String("hw.id", parent+":temperature:"+fixed.Value), attribute.String("hw.parent", parent))
			}
		}
		set = attribute.NewSet(attrs...)
	}
	return set, nil
}

// convertNumber parses the sample and converts its units without integer overflow.
// It also returns the original integer for exact counter reset comparisons.
func convertNumber(signal signalcatalog.Signal, metric collector.Metric) (*metricspb.NumberDataPoint, int64, error) {
	if metric.Timestamp <= 0 || uint64(metric.Timestamp) > math.MaxUint64/1000 {
		return nil, 0, errors.New("timestamp is outside Unix nanosecond range")
	}
	point := &metricspb.NumberDataPoint{TimeUnixNano: uint64(metric.Timestamp) * 1000}
	if signal.ValueType == signalcatalog.ValueFloat64 {
		value, err := strconv.ParseFloat(metric.Value, 64)
		if err != nil {
			return nil, 0, fmt.Errorf("parse float: %w", err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, 0, errors.New("value must be finite")
		}
		point.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: value}
		return point, 0, nil
	}

	raw, err := strconv.ParseInt(metric.Value, 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("parse integer: %w", err)
	}
	if err := convertIntegerValue(signal, raw, point); err != nil {
		return nil, 0, err
	}
	return point, raw, nil
}

func convertIntegerValue(signal signalcatalog.Signal, raw int64, point *metricspb.NumberDataPoint) error {
	if signal.Output.Temporality == signalcatalog.TemporalityCumulative && raw < 0 {
		return errors.New("cumulative source value must be nonnegative")
	}
	switch signal.Output.Conversion {
	case signalcatalog.ConversionPercentToRatio:
		point.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(raw) / 100}
		return nil
	case signalcatalog.ConversionMillijoulesToJoules:
		point.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(raw) / 1000}
		return nil
	}
	scale := int64(1)
	if signal.Output.Conversion == signalcatalog.ConversionMegahertzToHertz {
		scale = 1_000_000
	}
	if signal.Output.Conversion == signalcatalog.ConversionMebibytesToBytes {
		scale = 1_048_576
	}
	if raw > math.MaxInt64/scale || raw < math.MinInt64/scale {
		return errors.New("integer conversion overflow")
	}
	point.Value = &metricspb.NumberDataPoint_AsInt{AsInt: raw * scale}
	return nil
}

// protoAttributes copies string and integer attributes into OTLP messages.
func protoAttributes(attrs attribute.Set) ([]*commonpb.KeyValue, error) {
	result := make([]*commonpb.KeyValue, 0, attrs.Len())
	for i := 0; i < attrs.Len(); i++ {
		attr, _ := attrs.Get(i)
		value := &commonpb.AnyValue{}
		switch attr.Value.Type() {
		case attribute.STRING:
			value.Value = &commonpb.AnyValue_StringValue{StringValue: attr.Value.AsString()}
		case attribute.INT64:
			value.Value = &commonpb.AnyValue_IntValue{IntValue: attr.Value.AsInt64()}
		default:
			return nil, fmt.Errorf("unsupported attribute type for %q", attr.Key)
		}
		result = append(result, &commonpb.KeyValue{Key: string(attr.Key), Value: value})
	}
	return result, nil
}
