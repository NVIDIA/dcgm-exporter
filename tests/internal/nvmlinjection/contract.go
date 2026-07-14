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

// Package nvmlinjection defines the temporary contract produced by the direct
// DCGM probe and consumed by black-box host integration tests.
package nvmlinjection

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
)

const (
	CurrentVersion           = 1
	RegularBatchSize         = 127
	ProfilingBatchSize       = 64
	int32Blank         int64 = 2147483632
	int64BlankFloat          = float64(9223372036854775792)
	fp64Blank                = 140737488355328.0
)

// Contract describes every field/entity sample DCGM exposes for one fixture.
type Contract struct {
	Version     int           `json:"version"`
	DeviceCount int           `json:"deviceCount"`
	Metrics     []Metric      `json:"metrics"`
	Unavailable []Unavailable `json:"unavailable,omitempty"`
}

// Metric describes one available numeric DCGM field.
type Metric struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	Profiling bool     `json:"profiling,omitempty"`
	Samples   []Sample `json:"samples"`
}

// Sample identifies one expected GPU or GPU-instance sample.
type Sample struct {
	EntityGroup string            `json:"entityGroup"`
	EntityID    uint              `json:"entityId"`
	Labels      map[string]string `json:"labels"`
}

// Unavailable records why a candidate field did not produce a usable value.
type Unavailable struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Read decodes and validates a probe contract.
func Read(r io.Reader) (Contract, error) {
	var contract Contract
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, fmt.Errorf("decode NVML injection contract: %w", err)
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

// Validate checks that a contract is deterministic and usable by the host test.
func (c Contract) Validate() error {
	var errs []error
	if c.Version != CurrentVersion {
		errs = append(errs, fmt.Errorf("version must be %d", CurrentVersion))
	}
	if c.DeviceCount < 0 {
		errs = append(errs, errors.New("deviceCount must not be negative"))
	}
	if c.DeviceCount > 0 && len(c.Metrics) == 0 {
		errs = append(errs, errors.New("device fixture exposes no exportable metrics"))
	}
	if c.DeviceCount == 0 && len(c.Metrics) != 0 {
		errs = append(errs, errors.New("zero-device fixture must not expose GPU metrics"))
	}
	seenNames := map[string]struct{}{}
	seenIDs := map[int]struct{}{}
	for index, metric := range c.Metrics {
		prefix := fmt.Sprintf("metrics[%d]", index)
		if !model.LegacyValidation.IsValidMetricName(metric.Name) || !strings.HasPrefix(metric.Name, "DCGM_FI_") {
			errs = append(errs, fmt.Errorf("%s.name %q is not a DCGM field name", prefix, metric.Name))
		}
		if _, found := seenNames[metric.Name]; found {
			errs = append(errs, fmt.Errorf("duplicate metric name %q", metric.Name))
		}
		seenNames[metric.Name] = struct{}{}
		if _, found := seenIDs[metric.ID]; found {
			errs = append(errs, fmt.Errorf("duplicate field ID %d", metric.ID))
		}
		seenIDs[metric.ID] = struct{}{}
		if len(metric.Samples) == 0 {
			errs = append(errs, fmt.Errorf("%s.samples must not be empty", prefix))
		}
		seenSamples := map[string]struct{}{}
		for sampleIndex, sample := range metric.Samples {
			key := canonicalLabels(sample.Labels)
			if sample.EntityGroup == "" || len(sample.Labels) == 0 {
				errs = append(errs, fmt.Errorf("%s.samples[%d] lacks entity identity", prefix, sampleIndex))
			}
			if _, found := seenSamples[key]; found {
				errs = append(errs, fmt.Errorf("%s has duplicate %s entity %d", prefix, sample.EntityGroup, sample.EntityID))
			}
			seenSamples[key] = struct{}{}
		}
	}
	return errors.Join(errs...)
}

// Batches returns limit-aware regular and profiling metric batches.
func (c Contract) Batches() [][]Metric {
	var regular, profiling []Metric
	for _, metric := range c.Metrics {
		if metric.Profiling {
			profiling = append(profiling, metric)
		} else {
			regular = append(regular, metric)
		}
	}
	var batches [][]Metric
	batches = appendBatches(batches, regular, RegularBatchSize)
	return appendBatches(batches, profiling, ProfilingBatchSize)
}

func appendBatches(dst [][]Metric, metrics []Metric, size int) [][]Metric {
	for first := 0; first < len(metrics); first += size {
		last := min(first+size, len(metrics))
		dst = append(dst, metrics[first:last])
	}
	return dst
}

// WriteCollectors writes an exporter counter CSV for one metric batch.
func WriteCollectors(w io.Writer, metrics []Metric) error {
	writer := csv.NewWriter(w)
	for _, metric := range metrics {
		if err := writer.Write([]string{metric.Name, "gauge", "NVML injection DCGM-oracle field."}); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

// ValidateFamilies compares one exporter scrape with a direct-DCGM batch.
func ValidateFamilies(families map[string]*dto.MetricFamily, metrics []Metric) error {
	var errs []error
	expectedNames := make(map[string]struct{}, len(metrics))
	for _, expected := range metrics {
		expectedNames[expected.Name] = struct{}{}
		family, found := families[expected.Name]
		if !found {
			errs = append(errs, fmt.Errorf("expected metric family %q is missing", expected.Name))
			continue
		}
		if family.GetType() != dto.MetricType_GAUGE {
			errs = append(errs, fmt.Errorf("metric family %q has type %s, expected GAUGE", expected.Name, family.GetType()))
		}
		actual := family.GetMetric()
		if len(actual) != len(expected.Samples) {
			errs = append(errs, fmt.Errorf("metric family %q has %d samples, expected %d", expected.Name, len(actual), len(expected.Samples)))
		}
		matched := make([]bool, len(actual))
		for _, sample := range expected.Samples {
			index := matchingSample(actual, sample.Labels, matched)
			if index < 0 {
				errs = append(errs, fmt.Errorf("metric family %q is missing %s entity %d", expected.Name, sample.EntityGroup, sample.EntityID))
				continue
			}
			matched[index] = true
			if actual[index].GetGauge() == nil {
				errs = append(errs, fmt.Errorf("metric family %q %s entity %d lacks a gauge value", expected.Name, sample.EntityGroup, sample.EntityID))
				continue
			}
			value := actual[index].GetGauge().GetValue()
			if !saneValue(value) {
				errs = append(errs, fmt.Errorf("metric family %q %s entity %d has an invalid value", expected.Name, sample.EntityGroup, sample.EntityID))
			}
		}
	}
	for name := range families {
		if !strings.HasPrefix(name, "DCGM_FI_") {
			continue
		}
		if _, found := expectedNames[name]; !found {
			errs = append(errs, fmt.Errorf("unexpected DCGM metric family %q", name))
		}
	}
	return errors.Join(errs...)
}

func matchingSample(metrics []*dto.Metric, expected map[string]string, matched []bool) int {
	for index, metric := range metrics {
		if matched[index] {
			continue
		}
		labels := labelsByName(metric)
		matches := true
		for name, value := range expected {
			actual, found := labels[name]
			if !found || actual != value {
				matches = false
				break
			}
		}
		if matches {
			return index
		}
	}
	return -1
}

func saneValue(value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	if value >= float64(int32Blank) && value <= float64(int32Blank+3) {
		return false
	}
	if value >= int64BlankFloat {
		return false
	}
	return value < fp64Blank || value > fp64Blank+3
}

func labelsByName(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}
	return labels
}

func canonicalLabels(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%q", name, labels[name]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
