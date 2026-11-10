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

package metriccontract

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

var (
	GPUIdentityLabels = []string{"gpu", "UUID", "device", "modelName"}
	GPUHostnameLabels = []string{"Hostname", "hostname"}
	KubernetesLabels  = []string{"pod", "namespace", "container"}
)

var aggregateNVLinkFamilies = map[string]struct{}{
	"DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL":             {},
	"DCGM_FI_DEV_NVLINK_THROUGHPUT_BYTES_PER_SECOND": {},
}

// FamilyContract describes the expected shape of a Prometheus metric family.
type FamilyContract struct {
	Name      string
	Type      dto.MetricType
	CheckType bool
	// Labels must all be non-empty on at least one sample.
	Labels []string
	// WithoutLabels must be absent or empty on that same sample, so a contract
	// can require a CPU sample that is not also a CPU core sample.
	WithoutLabels []string
	NonNegative   bool
	Finite        bool
}

// DefaultCounterRow describes one enabled row from etc/default-counters.csv.
// Help is the help column with surrounding spaces removed, as the exporter
// renders it on the HELP line.
type DefaultCounterRow struct {
	Name string
	Type dto.MetricType
	Help string
}

// DefaultCounterOptions controls how strictly default-counters rows are required.
type DefaultCounterOptions struct {
	// FieldSupport decides whether a missing row fails or is reported as a skip.
	FieldSupport FieldSupport
	// ExactCSV states that the rows are the CSV the exporter was started with:
	// every DCGM_FI_* family must be a row and its HELP text must equal the
	// row's help. Leave it off when the exporter may use another CSV.
	ExactCSV     bool
	MetricLabels []string
	SkipWriter   io.Writer
}

// CapabilityReport records optional GPU capabilities detected from emitted metrics.
type CapabilityReport struct {
	Models       []string
	HasProfiling bool
	HasMIG       bool
	HasNVLink    bool
	HasB200      bool
	HasGB200     bool
}

// BaselineGPUFamilies returns the default GPU metric families required on supported GPUs.
func BaselineGPUFamilies() []FamilyContract {
	return []FamilyContract{
		{Name: "DCGM_FI_DEV_SM_CLOCK_HERTZ", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_MEMORY_CLOCK_HERTZ", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_BOARD_POWER_WATTS", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_GPU_ENERGY_JOULES_TOTAL", Type: dto.MetricType_COUNTER, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_PCIE_REPLAY_TOTAL", Type: dto.MetricType_COUNTER, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_GPU_UTIL_RATIO", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_MEMORY_UTIL_RATIO", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_ENC_UTIL_RATIO", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_DEC_UTIL_RATIO", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_FB_FREE_BYTES", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_FB_USED_BYTES", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_VGPU_LICENSE_STATUS", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
	}
}

// BaselineGPUFamilyNames returns just the metric names from BaselineGPUFamilies.
func BaselineGPUFamilyNames() []string {
	contracts := BaselineGPUFamilies()
	names := make([]string, 0, len(contracts))
	for _, contract := range contracts {
		names = append(names, contract.Name)
	}
	return names
}

// ReadDefaultCounterRows reads enabled rows from a DCGM exporter metrics CSV.
func ReadDefaultCounterRows(r io.Reader) ([]DefaultCounterRow, error) {
	reader := csv.NewReader(r)
	reader.Comment = '#'
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	rows := make([]DefaultCounterRow, 0, len(records))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		name := strings.TrimSpace(record[0])
		if name == "" {
			continue
		}
		if len(record) != 3 {
			return nil, fmt.Errorf("default counter row %q has %d fields, expected exactly 3", name, len(record))
		}
		metricType, ok := prometheusMetricType(strings.TrimSpace(record[1]))
		if !ok {
			return nil, fmt.Errorf("default counter row %q has unsupported metric type %q", name, record[1])
		}
		rows = append(rows, DefaultCounterRow{Name: name, Type: metricType, Help: strings.Trim(record[2], " ")})
	}
	return rows, nil
}

// ValidateDefaultCounterRows validates every supported enabled default-counter row.
func ValidateDefaultCounterRows(
	families map[string]*dto.MetricFamily,
	rows []DefaultCounterRow,
	opts DefaultCounterOptions,
) error {
	var errs []error
	for _, row := range rows {
		if IsLabelRow(row) {
			errs = append(errs, validateLabelOnlyDefaultCounter(families, row, opts))
			continue
		}
		errs = append(errs, validateDefaultMetricFamily(families, row, opts))
	}

	if opts.ExactCSV {
		errs = append(errs, validateNoFamiliesOutsideCSV(families, rows))
	}
	return errors.Join(errs...)
}

// validateNoFamiliesOutsideCSV fails every emitted DCGM_FI_* family that is
// not a metric row of the CSV the exporter was started with. A label row
// emitted as a family fails too, because the exporter renders it as a label.
func validateNoFamiliesOutsideCSV(families map[string]*dto.MetricFamily, rows []DefaultCounterRow) error {
	inCSV := make(map[string]bool, len(rows))
	for _, row := range rows {
		if !IsLabelRow(row) {
			inCSV[row.Name] = true
		}
	}

	var extra []string
	for name := range families {
		if strings.HasPrefix(name, "DCGM_FI_") && !inCSV[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)

	errs := make([]error, 0, len(extra))
	for _, name := range extra {
		errs = append(errs, fmt.Errorf("metric family %q is not a metric row of the CSV the exporter was started with", name))
	}
	return errors.Join(errs...)
}

// prometheusMetricType maps CSV metric type names to Prometheus model types.
func prometheusMetricType(metricType string) (dto.MetricType, bool) {
	switch strings.ToLower(strings.TrimSpace(metricType)) {
	case "counter":
		return dto.MetricType_COUNTER, true
	case "gauge":
		return dto.MetricType_GAUGE, true
	case "label":
		return dto.MetricType_UNTYPED, true
	default:
		return dto.MetricType_UNTYPED, false
	}
}

// IsLabelRow reports whether a CSV row is a label row: a DCGM field the
// exporter renders as a label on GPU samples instead of as a metric family.
func IsLabelRow(row DefaultCounterRow) bool {
	return row.Type == dto.MetricType_UNTYPED && strings.HasPrefix(row.Name, "DCGM_FI_")
}

// validateLabelOnlyDefaultCounter checks label-only CSV rows on emitted GPU samples.
func validateLabelOnlyDefaultCounter(
	families map[string]*dto.MetricFamily,
	row DefaultCounterRow,
	opts DefaultCounterOptions,
) error {
	if anyEmittedGPUFamilyHasAnyLabel(families, []string{row.Name}) {
		return nil
	}
	return missingDefaultCounterRow(row, opts, "default label field")
}

// missingDefaultCounterRow decides the outcome for a CSV row the exporter did
// not emit: a row DCGM supports fails, a row DCGM refused or returned no
// value for is a skip with the probe's reason, and an unclassified row is a
// skip that says the row was not checked. what names the missing output,
// such as "default metric family".
func missingDefaultCounterRow(row DefaultCounterRow, opts DefaultCounterOptions, what string) error {
	status, reason := opts.FieldSupport.Classify(row.Name)
	switch status {
	case FieldSupported:
		return fmt.Errorf("%s %q is missing, but DCGM returns a value for it on a GPU in the run", what, row.Name)
	case FieldUnsupported:
		writeDefaultCounterSkip(opts.SkipWriter, row.Name, "not required: the DCGM field support evidence has no value for it: "+reason)
	default:
		writeDefaultCounterSkip(opts.SkipWriter, row.Name, what+" was not emitted and "+reason+"; coverage unqualified")
	}
	return nil
}

// anyEmittedGPUFamilyHasAnyLabel reports whether an emitted GPU sample carries any label.
func anyEmittedGPUFamilyHasAnyLabel(families map[string]*dto.MetricFamily, labels []string) bool {
	for name, family := range families {
		if !strings.HasPrefix(name, "DCGM_FI_") {
			continue
		}
		if familyHasMetricWithAnyLabel(family, labels) {
			return true
		}
	}
	return false
}

// validateDefaultMetricFamily checks one metric-family row from the default CSV.
func validateDefaultMetricFamily(
	families map[string]*dto.MetricFamily,
	row DefaultCounterRow,
	opts DefaultCounterOptions,
) error {
	if families[row.Name] != nil {
		return validateEmittedDefaultMetricFamily(families, row, opts)
	}
	return missingDefaultCounterRow(row, opts, "default metric family")
}

// validateEmittedDefaultMetricFamily checks every emitted series. A present
// default-counter family is product output, so its shape must not be waived by
// a separate support hint.
func validateEmittedDefaultMetricFamily(
	families map[string]*dto.MetricFamily,
	row DefaultCounterRow,
	opts DefaultCounterOptions,
) error {
	contract := FamilyContract{
		Name:        row.Name,
		Type:        row.Type,
		CheckType:   true,
		Labels:      defaultCounterMetricLabels(opts),
		NonNegative: true,
	}
	errs := []error{
		ValidateContracts(families, []FamilyContract{contract}),
		validateEveryDefaultMetricHasLabels(families[row.Name], defaultCounterMetricLabels(opts)),
	}

	// The exporter renders the CSV help column, with surrounding spaces removed, as the HELP text.
	if opts.ExactCSV && families[row.Name].GetHelp() != row.Help {
		errs = append(errs, fmt.Errorf("metric family %q has HELP %q, expected the CSV help %q",
			row.Name, families[row.Name].GetHelp(), row.Help))
	}
	return errors.Join(errs...)
}

// validateEveryDefaultMetricHasLabels requires default-counter identity data on
// every emitted series, rather than accepting one well-formed sample.
func validateEveryDefaultMetricHasLabels(family *dto.MetricFamily, required []string) error {
	var errs []error
	for _, metric := range family.GetMetric() {
		labels := metricLabels(metric)
		for _, name := range required {
			if labels[name] == "" {
				errs = append(errs, fmt.Errorf("default metric family %q has a sample without non-empty label %q", family.GetName(), name))
			}
		}
	}
	return errors.Join(errs...)
}

// defaultCounterMetricLabels returns the identity labels expected on metric rows.
func defaultCounterMetricLabels(opts DefaultCounterOptions) []string {
	if len(opts.MetricLabels) > 0 {
		return opts.MetricLabels
	}
	return GPUIdentityLabels
}

// writeDefaultCounterSkip records why a default-counter row was not required.
func writeDefaultCounterSkip(w io.Writer, name string, reason string) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "metric contract: skipping default counter %s; %s\n", name, reason)
}

// ParseText parses Prometheus text exposition into metric families.
func ParseText(body []byte) (map[string]*dto.MetricFamily, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if len(families) == 0 {
		return nil, errors.New("no metric families found")
	}
	return families, nil
}

// ValidateContracts checks metric families against required type, label, sample, and value contracts.
func ValidateContracts(families map[string]*dto.MetricFamily, contracts []FamilyContract) error {
	var errs []error
	for _, contract := range contracts {
		errs = append(errs, validateFamilyContract(families, contract))
	}
	return errors.Join(errs...)
}

// validateFamilyContract checks one family while preserving every applicable
// label and value error in the result.
func validateFamilyContract(families map[string]*dto.MetricFamily, contract FamilyContract) error {
	family := families[contract.Name]
	if family == nil {
		return fmt.Errorf("metric family %q is missing", contract.Name)
	}
	var errs []error
	if contract.CheckType && family.GetType() != contract.Type {
		errs = append(errs, fmt.Errorf("metric family %q has type %s, expected %s",
			contract.Name, family.GetType(), contract.Type))
	}
	if len(family.GetMetric()) == 0 {
		errs = append(errs, fmt.Errorf("metric family %q has no samples", contract.Name))
		return errors.Join(errs...)
	}
	if len(contract.WithoutLabels) > 0 {
		if !familyHasMetricWithLabelsWithout(family, contract.Labels, contract.WithoutLabels) {
			errs = append(errs, fmt.Errorf("metric family %q has no sample with non-empty labels %v and without labels %v",
				contract.Name, contract.Labels, contract.WithoutLabels))
		}
	} else if len(contract.Labels) > 0 {
		errs = append(errs, ValidateLabels(families, []string{contract.Name}, contract.Labels))
	}
	if contract.NonNegative {
		errs = append(errs, ValidateNonNegativeSamples(family))
	}
	if contract.Finite {
		errs = append(errs, ValidateFiniteSamples(family))
	}
	return errors.Join(errs...)
}

// ValidateLabels requires each named family to have a sample with all requested labels.
func ValidateLabels(families map[string]*dto.MetricFamily, familyNames []string, labels []string) error {
	var errs []error
	for _, name := range familyNames {
		family := families[name]
		if family == nil {
			errs = append(errs, fmt.Errorf("metric family %q is missing", name))
			continue
		}
		if !familyHasMetricWithLabels(family, labels) {
			errs = append(errs, fmt.Errorf("metric family %q has no sample with non-empty labels %v", name, labels))
		}
	}
	return errors.Join(errs...)
}

// ValidateAnyLabel requires each named family to have a sample with at least one requested label.
func ValidateAnyLabel(families map[string]*dto.MetricFamily, familyNames []string, labels []string) error {
	var errs []error
	for _, name := range familyNames {
		family := families[name]
		if family == nil {
			errs = append(errs, fmt.Errorf("metric family %q is missing", name))
			continue
		}
		if !familyHasMetricWithAnyLabel(family, labels) {
			errs = append(errs, fmt.Errorf("metric family %q has no sample with any non-empty label in %v", name, labels))
		}
	}
	return errors.Join(errs...)
}

// ValidateLabelValues requires each named family to have a sample matching all expected label values.
func ValidateLabelValues(families map[string]*dto.MetricFamily, familyNames []string, labels map[string]string) error {
	var errs []error
	for _, name := range familyNames {
		family := families[name]
		if family == nil {
			errs = append(errs, fmt.Errorf("metric family %q is missing", name))
			continue
		}
		if !familyHasMetricWithLabelValues(family, labels) {
			errs = append(errs, fmt.Errorf("metric family %q has no sample with label values %v", name, labels))
		}
	}
	return errors.Join(errs...)
}

// ValidateExactlyOneLabelMatch requires one sample per family with the given
// identity. Other samples may represent other GPUs or links.
func ValidateExactlyOneLabelMatch(families map[string]*dto.MetricFamily, familyNames []string, labels map[string]string) error {
	var errs []error
	for _, name := range familyNames {
		family := families[name]
		if family == nil {
			errs = append(errs, fmt.Errorf("metric family %q is missing", name))
			continue
		}
		matches := countSamplesWithLabels(family, labels)
		if matches != 1 {
			errs = append(errs, fmt.Errorf("metric family %q has %d samples with label values %v, expected one", name, matches, labels))
		}
	}
	return errors.Join(errs...)
}

// countSamplesWithLabels counts samples for one GPU and link identity.
func countSamplesWithLabels(family *dto.MetricFamily, labels map[string]string) int {
	matches := 0
	for _, sample := range family.GetMetric() {
		actual := metricLabels(sample)
		matched := true
		for label, expected := range labels {
			if actual[label] != expected {
				matched = false
				break
			}
		}
		if matched {
			matches++
		}
	}
	return matches
}

// ValidateNonNegativeSamples rejects gauge, counter, or untyped samples with negative values.
func ValidateNonNegativeSamples(family *dto.MetricFamily) error {
	for _, metric := range family.GetMetric() {
		value, ok := metricSampleValue(metric)
		if !ok {
			continue
		}
		if value < 0 {
			return fmt.Errorf("metric family %q has negative sample %f", family.GetName(), value)
		}
	}
	return nil
}

// ValidateFiniteSamples requires a numeric value in every sample of a family.
func ValidateFiniteSamples(family *dto.MetricFamily) error {
	for _, metric := range family.GetMetric() {
		value, ok := metricSampleValue(metric)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("metric family %q has a missing or non-finite sample", family.GetName())
		}
	}
	return nil
}

// ValidateFamilyPrefix applies common label and sample checks to all families matching a prefix.
func ValidateFamilyPrefix(
	families map[string]*dto.MetricFamily,
	prefix string,
	labels []string,
	nonNegative bool,
	capabilityName string,
) error {
	return validateMatchingFamilies(
		families,
		func(name string, _ *dto.MetricFamily) bool { return strings.HasPrefix(name, prefix) },
		labels,
		nonNegative,
		capabilityName,
	)
}

// ValidateAtLeastOneDCGMGPUFamily requires at least one emitted DCGM GPU metric with identity labels.
func ValidateAtLeastOneDCGMGPUFamily(families map[string]*dto.MetricFamily) error {
	for name, family := range families {
		if !strings.HasPrefix(name, "DCGM_FI_") || len(family.GetMetric()) == 0 {
			continue
		}
		if familyHasMetricWithLabels(family, []string{"gpu", "UUID"}) {
			return nil
		}
	}
	return errors.New("no DCGM_FI_ metric family with GPU identity labels found")
}

// ValidateDCGMSamples rejects malformed emitted DCGM samples while leaving
// field-specific optionality to the caller's contract. DCGM values must be
// finite, cannot be one of DCGM's unavailable sentinels, and cannot repeat a
// label set within a metric family. Known ratio and utilization fields also
// have their documented upper bounds.
func ValidateDCGMSamples(families map[string]*dto.MetricFamily) error {
	var errs []error
	for name, family := range families {
		if strings.HasPrefix(name, "DCGM_FI_") {
			errs = append(errs, validateDCGMFamilySamples(name, family)...)
		}
	}
	return errors.Join(errs...)
}

// validateDCGMFamilySamples checks one DCGM family for GPU identity, duplicate
// label sets, and field-appropriate numeric values.
func validateDCGMFamilySamples(name string, family *dto.MetricFamily) []error {
	var errs []error
	seen := map[string]struct{}{}
	for _, metric := range family.GetMetric() {
		labels := metricLabels(metric)
		if labels["gpu"] != "" && sampleGPUUUID(labels) == "" {
			errs = append(errs, fmt.Errorf("DCGM metric family %q has a sample without GPU UUID", name))
		}
		key := canonicalMetricLabels(metric)
		if _, duplicate := seen[key]; duplicate {
			errs = append(errs, fmt.Errorf("DCGM metric family %q duplicates label set %s", name, key))
		}
		seen[key] = struct{}{}
		if value, numeric := metricSampleValue(metric); numeric {
			// A pod's GPU utilization sums its processes, so it can exceed the
			// per-GPU bound. Other families and checks are unchanged.
			podSum := labels["pod"] != "" && podSummedFields[name]
			if err := validateDCGMNumericValue(name, value, podSum); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errs
}

// canonicalMetricLabels serializes a sample's labels for duplicate-series detection.
func canonicalMetricLabels(metric *dto.Metric) string {
	labels := metricLabels(metric)
	encoded, _ := json.Marshal(labels)
	return string(encoded)
}

// podSummedFields are the families whose pod-attributed samples add up per-process values.
var podSummedFields = map[string]bool{
	"DCGM_FI_DEV_GPU_UTIL":       true,
	"DCGM_FI_DEV_GPU_UTIL_RATIO": true,
}

// validateDCGMNumericValue rejects non-finite values, unavailable sentinels, and known upper-bound violations.
func validateDCGMNumericValue(name string, value float64, podSum bool) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("DCGM metric family %q has non-finite sample %v", name, value)
	}
	for _, sentinel := range dcgmNumericSentinels {
		if value == sentinel {
			return fmt.Errorf("DCGM metric family %q has DCGM unavailable sentinel %v", name, value)
		}
	}
	if upper, bounded := dcgmFieldUpperBounds[name]; bounded && !podSum && value > upper {
		return fmt.Errorf("DCGM metric family %q has sample %v above maximum %v", name, value, upper)
	}
	return nil
}

// DCGM uses each blank value and its next three values for unavailable
// numeric samples (not found, unsupported, and permission denied). These
// definitions intentionally mirror the public DCGM field constants without
// importing the CGO-backed DCGM package into CPU-only contract tests.
const (
	dcgmFTInt32Blank = 2147483632
	dcgmFTInt64Blank = 9223372036854775792
	dcgmFTFP64Blank  = 140737488355328
)

var dcgmNumericSentinels = unavailableDCGMValues(
	dcgmFTInt32Blank,
	dcgmFTInt64Blank,
	dcgmFTFP64Blank,
)

// unavailableDCGMValues expands each DCGM blank value to its four unavailable sentinels.
func unavailableDCGMValues(blanks ...float64) []float64 {
	values := make([]float64, 0, len(blanks)*4)
	for _, blank := range blanks {
		for offset := 0.0; offset <= 3; offset++ {
			values = append(values, blank+offset)
		}
	}
	return values
}

var dcgmFieldUpperBounds = map[string]float64{
	"DCGM_FI_DEV_GPU_UTIL":              100,
	"DCGM_FI_DEV_MEM_COPY_UTIL":         100,
	"DCGM_FI_DEV_ENC_UTIL":              100,
	"DCGM_FI_DEV_DEC_UTIL":              100,
	"DCGM_FI_DEV_GPU_UTIL_RATIO":        1,
	"DCGM_FI_DEV_MEMORY_UTIL_RATIO":     1,
	"DCGM_FI_DEV_ENC_UTIL_RATIO":        1,
	"DCGM_FI_DEV_DEC_UTIL_RATIO":        1,
	"DCGM_FI_PROF_GR_ENGINE_ACTIVE":     1,
	"DCGM_FI_PROF_PIPE_TENSOR_ACTIVE":   1,
	"DCGM_FI_PROF_DRAM_ACTIVE":          1,
	"DCGM_FI_PROF_GR_ENGINE_UTIL_RATIO": 1,
	"DCGM_FI_PROF_TENSOR_UTIL_RATIO":    1,
	"DCGM_FI_PROF_DRAM_UTIL_RATIO":      1,
}

// ValidateCapabilityContracts runs optional assertions for capabilities detected in a scrape.
func ValidateCapabilityContracts(families map[string]*dto.MetricFamily, report CapabilityReport) error {
	var errs []error

	if report.HasProfiling {
		errs = append(errs, validateMatchingFamilies(
			families,
			func(name string, _ *dto.MetricFamily) bool { return strings.HasPrefix(name, "DCGM_FI_PROF_") },
			[]string{"gpu", "UUID"},
			true,
			"profiling/DCP",
		))
	}
	if report.HasMIG {
		errs = append(errs, validateAnyMatchingSampleHasLabels(
			families,
			func(name string, _ *dto.MetricFamily) bool { return strings.HasPrefix(name, "DCGM_FI_") },
			[]string{"GPU_I_ID", "GPU_I_PROFILE"},
			"MIG",
		))
	}
	if report.HasNVLink {
		errs = append(errs, validateNVLinkContracts(families))
	}
	if report.HasB200 {
		errs = append(errs, validateModelSamples(families, "B200"))
	}
	if report.HasGB200 {
		errs = append(errs, validateModelSamples(families, "GB200"))
	}

	return errors.Join(errs...)
}

// DetectCapabilities infers optional GPU capabilities from metric names and sample labels.
func DetectCapabilities(families map[string]*dto.MetricFamily) CapabilityReport {
	models := map[string]bool{}
	report := CapabilityReport{}

	for name, family := range families {
		if strings.HasPrefix(name, "DCGM_FI_PROF_") && len(family.GetMetric()) > 0 {
			report.HasProfiling = true
		}
		if (strings.Contains(name, "NVLINK") || strings.Contains(name, "NVSWITCH")) && len(family.GetMetric()) > 0 {
			report.HasNVLink = true
		}
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			if labels["GPU_I_ID"] != "" || labels["GPU_I_PROFILE"] != "" {
				report.HasMIG = true
			}
			if labels["nvlink"] != "" || labels["nvswitch"] != "" {
				report.HasNVLink = true
			}
			for _, labelName := range []string{"modelName", "model_name"} {
				model := labels[labelName]
				if model == "" {
					continue
				}
				models[model] = true
				if hasModelToken(model, "B200") {
					report.HasB200 = true
				}
				if hasModelToken(model, "GB200") {
					report.HasGB200 = true
				}
			}
		}
	}

	report.Models = make([]string, 0, len(models))
	for model := range models {
		report.Models = append(report.Models, model)
	}
	sort.Strings(report.Models)
	return report
}

// WriteCapabilityReport emits human-readable capability detections and skipped assertions.
func WriteCapabilityReport(w io.Writer, report CapabilityReport) {
	if len(report.Models) == 0 {
		fmt.Fprintln(w, "metric contract: no GPU model labels detected")
	} else {
		fmt.Fprintf(w, "metric contract: detected GPU models: %s\n", strings.Join(report.Models, ", "))
	}
	if !report.HasProfiling {
		fmt.Fprintln(w, "metric contract: skipping profiling/DCP-specific assertions; no DCGM_FI_PROF_* metrics detected")
	}
	if !report.HasMIG {
		fmt.Fprintln(w, "metric contract: skipping MIG assertions; no MIG labels detected")
	}
	if !report.HasNVLink {
		fmt.Fprintln(w, "metric contract: skipping NVLink/NVSwitch assertions; no NVLink/NVSwitch metrics detected")
	}
	if !report.HasB200 {
		fmt.Fprintln(w, "metric contract: skipping B200-specific assertions; no B200 model detected")
	}
	if !report.HasGB200 {
		fmt.Fprintln(w, "metric contract: skipping GB200-specific assertions; no GB200 model detected")
	}
}

// validateMatchingFamilies uses the caller's selector to check every family in a capability.
func validateMatchingFamilies(
	families map[string]*dto.MetricFamily,
	matches func(string, *dto.MetricFamily) bool,
	labels []string,
	nonNegative bool,
	capabilityName string,
) error {
	var (
		errs    []error
		matched bool
	)
	for name, family := range families {
		if !matches(name, family) {
			continue
		}
		matched = true
		if len(family.GetMetric()) == 0 {
			errs = append(errs, fmt.Errorf("%s metric family %q has no samples", capabilityName, name))
			continue
		}
		if len(labels) > 0 && !familyHasMetricWithLabels(family, labels) {
			errs = append(errs, fmt.Errorf("%s metric family %q has no sample with non-empty labels %v", capabilityName, name, labels))
		}
		if nonNegative {
			if err := ValidateNonNegativeSamples(family); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if !matched {
		errs = append(errs, fmt.Errorf("%s capability was detected but no matching metric family was found", capabilityName))
	}
	return errors.Join(errs...)
}

// validateAnyMatchingSampleHasLabels uses the caller's selector to require one complete sample.
func validateAnyMatchingSampleHasLabels(
	families map[string]*dto.MetricFamily,
	matches func(string, *dto.MetricFamily) bool,
	labels []string,
	capabilityName string,
) error {
	for name, family := range families {
		if !matches(name, family) {
			continue
		}
		if familyHasMetricWithLabels(family, labels) {
			return nil
		}
	}
	return fmt.Errorf("%s capability was detected but no sample has non-empty labels %v", capabilityName, labels)
}

// validateNVLinkContracts checks NVLink or NVSwitch samples when link metrics are detected.
func validateNVLinkContracts(families map[string]*dto.MetricFamily) error {
	var (
		errs    []error
		matched bool
	)
	for name, family := range families {
		if !strings.Contains(name, "NVLINK") && !strings.Contains(name, "NVSWITCH") &&
			!familyHasMetricWithAnyLabel(family, []string{"nvlink", "nvswitch"}) {
			continue
		}
		matched = true
		if len(family.GetMetric()) == 0 {
			errs = append(errs, fmt.Errorf("NVLink/NVSwitch metric family %q has no samples", name))
			continue
		}
		if _, aggregate := aggregateNVLinkFamilies[name]; aggregate &&
			!familyHasMetricWithAnyLabel(family, []string{"nvlink", "nvswitch"}) {
			if !familyHasMetricWithLabels(family, GPUIdentityLabels) {
				errs = append(errs, fmt.Errorf("aggregate NVLink metric family %q has no sample with GPU identity labels %v", name, GPUIdentityLabels))
			}
			if err := ValidateNonNegativeSamples(family); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if !familyHasMetricWithAnyLabel(family, []string{"nvlink", "nvswitch"}) {
			errs = append(errs, fmt.Errorf("NVLink/NVSwitch metric family %q has no sample with nvlink or nvswitch label", name))
		}
		if err := ValidateNonNegativeSamples(family); err != nil {
			errs = append(errs, err)
		}
	}
	if !matched {
		errs = append(errs, errors.New("NVLink/NVSwitch capability was detected but no matching metric family was found"))
	}
	return errors.Join(errs...)
}

// validateModelSamples requires at least one sample with the selected model and GPU identity.
func validateModelSamples(families map[string]*dto.MetricFamily, modelName string) error {
	firstFamilyWithoutIdentity := ""
	for name, family := range families {
		if !strings.HasPrefix(name, "DCGM_FI_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := metricLabels(metric)
			model := firstNonEmpty(labels["modelName"], labels["model_name"])
			if !hasModelToken(model, modelName) {
				continue
			}
			if labels["gpu"] != "" || labels["gpu_uuid"] != "" || labels["UUID"] != "" {
				return nil
			}
			if firstFamilyWithoutIdentity == "" {
				firstFamilyWithoutIdentity = name
			}
		}
	}
	if firstFamilyWithoutIdentity != "" {
		return fmt.Errorf("%s metric family %q has model label but no GPU identity label", modelName, firstFamilyWithoutIdentity)
	}
	return fmt.Errorf("%s model was detected but no DCGM_FI_ sample carried that model label", modelName)
}

// hasModelToken reports whether a model label contains an exact alphanumeric token.
func hasModelToken(model string, token string) bool {
	token = strings.ToUpper(token)
	for _, field := range strings.FieldsFunc(strings.ToUpper(model), func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'A' || r > 'Z')
	}) {
		if field == token {
			return true
		}
	}
	return false
}

// familyHasMetricWithLabels reports whether any sample has all requested labels populated.
func familyHasMetricWithLabels(family *dto.MetricFamily, labels []string) bool {
	return familyHasMetricWithLabelsWithout(family, labels, nil)
}

// familyHasMetricWithLabelsWithout reports whether one sample has every label
// in labels non-empty and every label in without absent or empty.
func familyHasMetricWithLabelsWithout(family *dto.MetricFamily, labels, without []string) bool {
	for _, metric := range family.GetMetric() {
		metricLabels := metricLabels(metric)
		if countNonEmpty(metricLabels, labels) == len(labels) && countNonEmpty(metricLabels, without) == 0 {
			return true
		}
	}
	return false
}

// countNonEmpty returns how many of names have a non-empty value in labels.
func countNonEmpty(labels map[string]string, names []string) int {
	count := 0
	for _, name := range names {
		if labels[name] != "" {
			count++
		}
	}
	return count
}

// familyHasMetricWithLabelValues reports whether any sample matches all expected label values.
func familyHasMetricWithLabelValues(family *dto.MetricFamily, labels map[string]string) bool {
	for _, metric := range family.GetMetric() {
		metricLabels := metricLabels(metric)
		foundAllLabels := true
		for label, expected := range labels {
			if metricLabels[label] != expected {
				foundAllLabels = false
				break
			}
		}
		if foundAllLabels {
			return true
		}
	}
	return false
}

// familyHasMetricWithAnyLabel reports whether any sample has one of the requested labels populated.
func familyHasMetricWithAnyLabel(family *dto.MetricFamily, labels []string) bool {
	for _, metric := range family.GetMetric() {
		metricLabels := metricLabels(metric)
		for _, label := range labels {
			if metricLabels[label] != "" {
				return true
			}
		}
	}
	return false
}

// metricLabels converts Prometheus label pairs into a name-to-value map.
func metricLabels(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}

// sampleGPUUUID returns the UUID of the GPU a sample belongs to, or "" for a
// sample of no GPU. GPU NVLink samples name it in gpu_uuid; every other GPU and
// MIG sample uses UUID (uuid with --use-old-namespace).
func sampleGPUUUID(labels map[string]string) string {
	return firstNonEmpty(labels["UUID"], labels["uuid"], labels["gpu_uuid"])
}

// firstNonEmpty returns the first non-empty string from a list.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// metricSampleValue extracts the numeric value from supported Prometheus sample types.
func metricSampleValue(metric *dto.Metric) (float64, bool) {
	switch {
	case metric.Gauge != nil:
		return metric.GetGauge().GetValue(), true
	case metric.Counter != nil:
		return metric.GetCounter().GetValue(), true
	case metric.Untyped != nil:
		return metric.GetUntyped().GetValue(), true
	default:
		return 0, false
	}
}
