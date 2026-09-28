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
	"errors"
	"fmt"
	"io"
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
	"DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL": {},
}

// FamilyContract describes the expected shape of a Prometheus metric family.
type FamilyContract struct {
	Name        string
	Type        dto.MetricType
	CheckType   bool
	Labels      []string
	NonNegative bool
}

// DefaultCounterRow describes one enabled row from etc/default-counters.csv.
type DefaultCounterRow struct {
	Name string
	Type dto.MetricType
}

// DefaultCounterOptions controls how strictly default-counters rows are required.
type DefaultCounterOptions struct {
	SupportedFields map[string]bool
	MetricLabels    []string
	SkipWriter      io.Writer
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
		{Name: "DCGM_FI_DEV_SM_CLOCK", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_MEM_CLOCK", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_MEMORY_TEMP", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_GPU_TEMP", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_POWER_USAGE", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_TOTAL_ENERGY_CONSUMPTION", Type: dto.MetricType_COUNTER, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_PCIE_REPLAY_COUNTER", Type: dto.MetricType_COUNTER, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_GPU_UTIL", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_MEM_COPY_UTIL", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_ENC_UTIL", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_DEC_UTIL", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_FB_FREE", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
		{Name: "DCGM_FI_DEV_FB_USED", Type: dto.MetricType_GAUGE, CheckType: true, Labels: GPUIdentityLabels, NonNegative: true},
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
		if len(record) < 3 {
			return nil, fmt.Errorf("default counter row %q has %d fields, expected at least 3", name, len(record))
		}
		metricType, ok := prometheusMetricType(strings.TrimSpace(record[1]))
		if !ok {
			return nil, fmt.Errorf("default counter row %q has unsupported metric type %q", name, record[1])
		}
		rows = append(rows, DefaultCounterRow{Name: name, Type: metricType})
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
		if row.Type == dto.MetricType_UNTYPED && isLabelOnlyCounter(row.Name) {
			errs = append(errs, validateLabelOnlyDefaultCounter(families, row, opts))
			continue
		}
		errs = append(errs, validateDefaultMetricFamily(families, row, opts))
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

// isLabelOnlyCounter reports whether a default-counter row is rendered as a label.
func isLabelOnlyCounter(name string) bool {
	return strings.HasPrefix(name, "DCGM_FI_")
}

// validateLabelOnlyDefaultCounter checks label-only CSV rows on emitted GPU samples.
func validateLabelOnlyDefaultCounter(
	families map[string]*dto.MetricFamily,
	row DefaultCounterRow,
	opts DefaultCounterOptions,
) error {
	if supported, known := opts.SupportedFields[row.Name]; known && !supported {
		writeDefaultCounterSkip(opts.SkipWriter, row.Name, "reported unsupported by DCGM field probe")
		return nil
	}
	if anyEmittedGPUFamilyHasAnyLabel(families, []string{row.Name}) {
		return nil
	}
	if supported, known := opts.SupportedFields[row.Name]; known && supported {
		return fmt.Errorf("supported default label field %q is missing from GPU samples", row.Name)
	}
	writeDefaultCounterSkip(opts.SkipWriter, row.Name, "field support unknown and label was not emitted")
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
	if supported, known := opts.SupportedFields[row.Name]; known && !supported {
		writeDefaultCounterSkip(opts.SkipWriter, row.Name, "reported unsupported by DCGM field probe")
		return nil
	}

	contract := FamilyContract{
		Name:        row.Name,
		Type:        row.Type,
		CheckType:   true,
		Labels:      defaultCounterMetricLabels(opts),
		NonNegative: true,
	}
	if err := ValidateContracts(families, []FamilyContract{contract}); err == nil {
		return nil
	}
	if supported, known := opts.SupportedFields[row.Name]; known && supported {
		return fmt.Errorf("supported default metric field %q is missing or invalid", row.Name)
	}

	writeDefaultCounterSkip(opts.SkipWriter, row.Name, "field support unknown and family was not emitted")
	return nil
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
		family := families[contract.Name]
		if family == nil {
			errs = append(errs, fmt.Errorf("metric family %q is missing", contract.Name))
			continue
		}
		if contract.CheckType && family.GetType() != contract.Type {
			errs = append(errs, fmt.Errorf("metric family %q has type %s, expected %s",
				contract.Name, family.GetType(), contract.Type))
		}
		if len(family.GetMetric()) == 0 {
			errs = append(errs, fmt.Errorf("metric family %q has no samples", contract.Name))
			continue
		}
		if len(contract.Labels) > 0 {
			if err := ValidateLabels(families, []string{contract.Name}, contract.Labels); err != nil {
				errs = append(errs, err)
			}
		}
		if contract.NonNegative {
			if err := ValidateNonNegativeSamples(family); err != nil {
				errs = append(errs, err)
			}
		}
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

// validateMatchingFamilies applies common label and sample checks to families matching a capability.
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

// validateAnyMatchingSampleHasLabels requires at least one matching family sample with all labels.
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

// validateModelSamples checks that model-specific samples also carry GPU identity.
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
	for _, metric := range family.GetMetric() {
		metricLabels := metricLabels(metric)
		foundAllLabels := true
		for _, label := range labels {
			if metricLabels[label] == "" {
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
