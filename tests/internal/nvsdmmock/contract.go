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

// Package nvsdmmock defines the temporary contract produced by the direct
// DCGM probe and consumed by black-box NVSDM mock host tests.
package nvsdmmock

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
)

const (
	CurrentVersion   = 1
	RegularBatchSize = 127

	EntityGroupSwitch = "NvSwitch"
	EntityGroupLink   = "NvLink"

	int32Blank      int64 = 2147483632
	int64BlankFloat       = float64(9223372036854775792)
	fp64Blank             = 140737488355328.0
)

// Contract describes the switch and switch-owned link telemetry exposed by DCGM.
type Contract struct {
	Version     int           `json:"version"`
	SwitchCount int           `json:"switchCount"`
	LinkCount   int           `json:"linkCount"`
	Metrics     []Metric      `json:"metrics"`
	Unavailable []Unavailable `json:"unavailable,omitempty"`
}

// Metric describes one available numeric DCGM field for one entity group.
type Metric struct {
	ID      int      `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Samples []Sample `json:"samples"`
}

// Sample describes one public metric value and the labels expected from dcgm-exporter.
type Sample struct {
	EntityGroup string            `json:"entityGroup"`
	EntityID    uint              `json:"entityId"`
	ParentID    uint              `json:"parentId,omitempty"`
	Labels      map[string]string `json:"labels"`
	Value       float64           `json:"value"`
}

// Unavailable records why a candidate field did not produce a usable value.
type Unavailable struct {
	Name        string `json:"name"`
	EntityGroup string `json:"entityGroup"`
	Reason      string `json:"reason"`
}

// Read decodes and validates a temporary probe contract.
func Read(r io.Reader) (Contract, error) {
	var contract Contract
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, fmt.Errorf("decode NVSDM mock contract: %w", err)
	}
	var trailing json.RawMessage
	switch err := decoder.Decode(&trailing); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return Contract{}, errors.New("decode NVSDM mock contract: unexpected trailing data")
	default:
		return Contract{}, fmt.Errorf("decode NVSDM mock contract trailing data: %w", err)
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

// Validate checks that a contract is deterministic and usable by the host test.
func (c Contract) Validate() error {
	var errs []error
	var hasSwitchSamples, hasLinkSamples bool
	if c.Version != CurrentVersion {
		errs = append(errs, fmt.Errorf("version must be %d", CurrentVersion))
	}
	if c.SwitchCount < 0 || c.LinkCount < 0 {
		errs = append(errs, errors.New("entity counts must not be negative"))
	}
	if c.LinkCount > 0 && c.SwitchCount == 0 {
		errs = append(errs, errors.New("switch-owned links require at least one switch"))
	}
	if (c.SwitchCount > 0 || c.LinkCount > 0) && len(c.Metrics) == 0 {
		errs = append(errs, errors.New("NVSDM mock exposes no supported switch/link metrics"))
	}
	if c.SwitchCount == 0 && c.LinkCount == 0 && len(c.Metrics) != 0 {
		errs = append(errs, errors.New("zero-entity NVSDM mock must not expose switch/link metrics"))
	}

	seenNames := map[string]struct{}{}
	seenIDs := map[int]struct{}{}
	for metricIndex, metric := range c.Metrics {
		prefix := fmt.Sprintf("metrics[%d]", metricIndex)
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
		if metric.Type != "gauge" {
			errs = append(errs, fmt.Errorf("%s.type must be gauge", prefix))
		}
		if len(metric.Samples) == 0 {
			errs = append(errs, fmt.Errorf("%s.samples must not be empty", prefix))
		}

		seenSamples := map[string]struct{}{}
		for sampleIndex, sample := range metric.Samples {
			switch sample.EntityGroup {
			case EntityGroupSwitch:
				hasSwitchSamples = true
			case EntityGroupLink:
				hasLinkSamples = true
			}
			labels := canonicalLabels(sample.Labels)
			if err := validateSample(sample); err != nil {
				errs = append(errs, fmt.Errorf("%s.samples[%d] labels: %w", prefix, sampleIndex, err))
			}
			if _, found := seenSamples[labels]; found {
				errs = append(errs, fmt.Errorf("%s has duplicate sample labels %s", prefix, labels))
			}
			seenSamples[labels] = struct{}{}
			if !saneValue(sample.Value) {
				errs = append(errs, fmt.Errorf("%s.samples[%d] has an invalid value", prefix, sampleIndex))
			}
		}
	}
	if hasSwitchSamples && c.SwitchCount == 0 {
		errs = append(errs, errors.New("switch samples require switchCount > 0"))
	}
	if hasLinkSamples && c.LinkCount == 0 {
		errs = append(errs, errors.New("link samples require linkCount > 0"))
	}
	return errors.Join(errs...)
}

func validateSample(sample Sample) error {
	if sample.EntityGroup != EntityGroupSwitch && sample.EntityGroup != EntityGroupLink {
		return fmt.Errorf("entity group %q is unsupported", sample.EntityGroup)
	}
	required := []string{"nvswitch", "hostname"}
	if sample.EntityGroup == EntityGroupLink {
		required = append(required, "nvlink")
	}
	if len(sample.Labels) != len(required) {
		return fmt.Errorf("got %s, expected exactly %s", canonicalLabels(sample.Labels), strings.Join(required, ", "))
	}
	for _, name := range required {
		if strings.TrimSpace(sample.Labels[name]) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	switchID := sample.EntityID
	if sample.EntityGroup == EntityGroupLink {
		switchID = sample.ParentID
		if want := strconv.FormatUint(uint64(sample.EntityID), 10); sample.Labels["nvlink"] != want {
			return fmt.Errorf("nvlink %q does not identify link entity %s", sample.Labels["nvlink"], want)
		}
	}
	if want := fmt.Sprintf("nvswitch%d", switchID); sample.Labels["nvswitch"] != want {
		return fmt.Errorf("nvswitch %q does not identify switch entity %d", sample.Labels["nvswitch"], switchID)
	}
	return nil
}

// Batches returns metric batches within DCGM's field-group limit.
func (c Contract) Batches() [][]Metric {
	var batches [][]Metric
	for first := 0; first < len(c.Metrics); first += RegularBatchSize {
		last := min(first+RegularBatchSize, len(c.Metrics))
		batches = append(batches, c.Metrics[first:last])
	}
	return batches
}

// WriteCollectors writes an exporter counter CSV for one metric batch.
func WriteCollectors(w io.Writer, metrics []Metric) error {
	writer := csv.NewWriter(w)
	for _, metric := range metrics {
		if err := writer.Write([]string{metric.Name, metric.Type, "NVSDM mock DCGM-oracle field."}); err != nil {
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
				errs = append(errs, fmt.Errorf("metric family %q is missing sample with labels %s", expected.Name, canonicalLabels(sample.Labels)))
				continue
			}
			matched[index] = true
			if actual[index].GetGauge() == nil {
				errs = append(errs, fmt.Errorf("metric family %q sample %s lacks a gauge value", expected.Name, canonicalLabels(sample.Labels)))
				continue
			}
			if value := actual[index].GetGauge().GetValue(); value != sample.Value {
				errs = append(errs, fmt.Errorf("metric family %q sample %s has value %v, expected %v", expected.Name, canonicalLabels(sample.Labels), value, sample.Value))
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
		actual := labelsByName(metric)
		if len(actual) != len(expected) {
			continue
		}
		matches := true
		for name, value := range expected {
			if actual[name] != value {
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

func labelsByName(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, pair := range metric.GetLabel() {
		labels[pair.GetName()] = pair.GetValue()
	}
	return labels
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
