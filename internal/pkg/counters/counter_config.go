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

package counters

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	stdos "os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/fieldexport"
	"github.com/NVIDIA/dcgm-exporter/internal/pkg/kubeclient"
)

// getKubeClient is an unexported test seam for ConfigMap counter loading.
// Production code leaves it bound to kubeclient.GetKubeClient.
var getKubeClient = kubeclient.GetKubeClient

// FilterWatchSources applies the same profiling capabilities as counter parsing.
func FilterWatchSources(collections []appconfig.OTLPWatchCollection, dcp DCPCapabilities) []appconfig.OTLPWatchCollection {
	var supported []appconfig.OTLPWatchCollection
	for _, collection := range collections {
		sources := make([]appconfig.WatchSource, 0, len(collection.Sources))
		hasMetric := false
		for _, source := range collection.Sources {
			if source.Source.Kind == appconfig.SourceDCGM && !fieldIsSupported(uint(source.Source.FieldID), dcp) {
				continue
			}
			sources = append(sources, source)
			hasMetric = hasMetric || !source.Metadata
		}
		if !hasMetric {
			continue
		}
		collection.Sources = sources
		supported = append(supported, collection)
	}
	return supported
}

// GetCounterSet loads the selected metric source and parses its counters for a
// registry build. Startup and reload supply the profiling fields supported by
// the current GPU topology so unsupported fields are skipped during parsing.
func GetCounterSet(ctx context.Context, c *appconfig.Config, dcp DCPCapabilities) (*CounterSet, error) {
	var (
		err     error
		records [][]string
		lines   []int
	)

	if c == nil {
		return nil, fmt.Errorf("config is nil")
	}
	source := c.MetricSource
	if source.Kind == "" {
		return nil, fmt.Errorf("metric source is not resolved")
	}

	switch source.Kind {
	case appconfig.MetricSourceConfigMap:
		var client kubernetes.Interface
		client, err = getKubeClient()
		if err != nil {
			return nil, err
		}
		records, err = readConfigMapSource(ctx, client, source.ConfigMap)
		if err != nil {
			return nil, err
		}
	case appconfig.MetricSourceInline, appconfig.MetricSourceFile:
		if source.Kind == appconfig.MetricSourceFile {
			slog.Info(fmt.Sprintf("Using metric file '%s'", source.File))
			if warning, deprecated := deprecatedMetricFileWarning(source.File); deprecated {
				slog.Warn(warning)
			}
		}
		records, lines, err = ReadLocalRecords(source)
		if err != nil {
			slog.Error(fmt.Sprintf("Could not read metrics file '%s'; err: %v", source.File, err))
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported metric source %q", source.Kind)
	}

	set, err := ExtractCounters(records, dcp)
	if err != nil {
		// Only a file has line numbers; for other sources lines is nil and err is unchanged.
		return nil, AtLines(err, lines)
	}

	return set, nil
}

// deprecatedMetricFiles maps the base name of each deprecated shipped metric
// file to what a user should do instead.
var deprecatedMetricFiles = map[string]string{
	"1.x-compatibility-metrics.csv": "use default-counters.csv and update dashboards and alerts to its metric names",
	"dcp-metrics-included.csv":      "use default-counters.csv, which has the same contents",
}

// deprecatedMetricFileWarning returns the startup warning for a metric file
// whose base name is a deprecated shipped file. A copy under another name
// gets no warning.
func deprecatedMetricFileWarning(path string) (string, bool) {
	name := filepath.Base(path)
	replacement, deprecated := deprecatedMetricFiles[name]
	if !deprecated {
		return "", false
	}

	return fmt.Sprintf("metric file %s is deprecated and will be removed in a later release; %s", name, replacement), true
}

// ErrSourceNotLocal is returned by ReadLocalRecords for a metric source whose
// definitions are not in a file or in the config itself.
var ErrSourceNotLocal = errors.New("metric source is not a file or inline definitions")

// ReadLocalRecords returns the metric records of a file or inline source
// without DCGM or Kubernetes. For a file, lines holds the file line of each
// record; for inline definitions it is nil. Any other source returns
// ErrSourceNotLocal. GetCounterSet, config validate and the startup check of
// the overrides all read local sources through it.
func ReadLocalRecords(source appconfig.MetricSource) (records [][]string, lines []int, err error) {
	switch source.Kind {
	case appconfig.MetricSourceFile:
		return ReadCSVFileWithLines(source.File)
	case appconfig.MetricSourceInline:
		return prometheusFieldsToRecords(source.Fields), nil, nil
	default:
		return nil, nil, ErrSourceNotLocal
	}
}

// recordError is a problem with one metric record. Index is the record's
// position among the records, not its line in the file; AtLines adds the line.
type recordError struct {
	Index int
	Err   error
}

// Error gives the problem without the position, so a source with no line
// numbers reports it as before.
func (e *recordError) Error() string { return e.Err.Error() }

// Unwrap returns the problem itself.
func (e *recordError) Unwrap() error { return e.Err }

// AtLines puts the file line of the bad record in front of err when err is a
// recordError and lines, from ReadCSVFileWithLines, has that record. Any other
// error comes back unchanged.
func AtLines(err error, lines []int) error {
	var recordErr *recordError
	if !errors.As(err, &recordErr) || recordErr.Index < 0 || recordErr.Index >= len(lines) {
		return err
	}

	return fmt.Errorf("line %d: %w", lines[recordErr.Index], err)
}

// prometheusFieldsToRecords converts inline YAML metric fields to the existing three-column CSV record shape.
func prometheusFieldsToRecords(fields []appconfig.MetricField) [][]string {
	records := make([][]string, 0, len(fields))
	for _, field := range fields {
		records = append(records, []string{field.Name, field.PrometheusType, field.Help})
	}
	return records
}

// maxMetricFileBytes caps how much of a metric file is read.
const maxMetricFileBytes = 8 << 20

// ReadCSVFile skips comment lines in a file-backed metric source and returns
// raw rows for ExtractCounters to validate before registry use.
func ReadCSVFile(filename string) ([][]string, error) {
	records, _, err := ReadCSVFileWithLines(filename)

	return records, err
}

// ReadCSVFileWithLines is ReadCSVFile that also returns the file line each
// record starts on, counting comment and blank lines, so a caller can say where
// a bad record is. lines[i] belongs to records[i].
func ReadCSVFileWithLines(filename string) ([][]string, []int, error) {
	// Open without blocking so a FIFO is refused instead of waiting for a writer.
	// The flag has no effect on regular files.
	file, err := stdos.OpenFile(filename, stdos.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}

	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("metric file %q is not a regular file", filename)
	}

	limited := &io.LimitedReader{R: file, N: maxMetricFileBytes + 1}
	r := csv.NewReader(limited)
	r.Comment = '#'

	var (
		records [][]string
		lines   []int
	)

	for {
		record, readErr := r.Read()
		if readErr == io.EOF {
			break
		}

		if readErr != nil {
			err = readErr
			records, lines = nil, nil

			break
		}

		line, _ := r.FieldPos(0)
		records = append(records, record)
		lines = append(lines, line)
	}

	if limited.N <= 0 {
		return nil, nil, fmt.Errorf("metric file %q is larger than %d bytes", filename, maxMetricFileBytes)
	}

	return records, lines, err
}

// ExtractCounters parses metric rows using the supplied DCP capability
// snapshot. Registry construction calls it after querying the active DCGM
// topology, so profiling fields are accepted only when supported.
func ExtractCounters(records [][]string, dcp DCPCapabilities) (*CounterSet, error) {
	res := CounterSet{}

	for i, record := range records {
		if err := extractCounterRecord(&res, record, i, &dcp); err != nil {
			return nil, &recordError{Index: i, Err: err}
		}
	}

	return &res, nil
}

// ValidateRecords checks local metric definitions without topology filtering or
// native initialization. Inspection uses the same row and field rules as the
// runtime parser; profiling availability remains a runtime check.
func ValidateRecords(records [][]string) (*CounterSet, error) {
	res := CounterSet{}
	for i, record := range records {
		if err := extractCounterRecord(&res, append([]string(nil), record...), i, nil); err != nil {
			return nil, &recordError{Index: i, Err: err}
		}
	}
	return &res, nil
}

// extractCounterRecord trims one row, then routes exporter and DCGM fields
// into separate lists used by registry construction.
func extractCounterRecord(res *CounterSet, record []string, index int, dcp *DCPCapabilities) error {
	if len(record) == 0 {
		return nil
	}

	for j, field := range record {
		record[j] = strings.Trim(field, " ")
	}
	if err := validateCounterRecord(record, index); err != nil {
		return err
	}

	fieldID, knownField := dcgm.GetFieldID(record[0])
	isLegacyField := dcgm.IsLegacyField(record[0])
	added, err := appendExporterCounterIfNeeded(res, record, knownField, isLegacyField)
	if err != nil {
		return err
	}
	if added {
		return nil
	}

	return appendDCGMCounterIfSupported(res, record, index, fieldID, dcp)
}

// validateCounterRecord rejects rows with the wrong column count, invalid
// UTF-8 help text, or a type the metric renderer cannot emit.
func validateCounterRecord(record []string, index int) error {
	if len(record) != 3 {
		return fmt.Errorf("malformed CSV record: expected 3 fields, got %d (`%v`)", len(record), record)
	}
	if !utf8.ValidString(record[2]) {
		return fmt.Errorf("metric %q has invalid UTF-8 HELP text at metric record %d", record[0], index+1)
	}

	// Config may only declare scalar shapes the renderer can actually emit.
	if _, ok := promMetricType[record[1]]; !ok {
		return fmt.Errorf(
			"unsupported Prometheus metric type %q; supported types are counter, gauge, untyped, and label",
			record[1],
		)
	}
	return nil
}

// appendExporterCounterIfNeeded adds supported exporter counters to the resolved set.
func appendExporterCounterIfNeeded(
	res *CounterSet,
	record []string,
	knownField, legacyField bool,
) (bool, error) {
	if knownField || legacyField {
		return false, nil
	}

	exporterField, err := IdentifyMetricType(record[0])
	if err != nil {
		return false, fmt.Errorf("could not find DCGM field; err: %w", err)
	}
	if exporterField == DCGMFIUnknown {
		return false, nil
	}

	res.ExporterCounters = append(res.ExporterCounters, Counter{
		FieldID:   dcgm.Short(exporterField),
		FieldName: record[0],
		PromType:  record[1],
		Help:      record[2],
	})
	return true, nil
}

// appendDCGMCounterIfSupported applies shared field rules and optional topology checks.
// A nil capability set leaves profiling availability for runtime admission.
func appendDCGMCounterIfSupported(
	res *CounterSet,
	record []string,
	index int,
	fieldID dcgm.Short,
	dcp *DCPCapabilities,
) error {
	if replacement, retired := retiredPCIeFieldReplacement(fieldID); retired {
		return fmt.Errorf(
			"DCGM field %q (ID %d) is no longer supported; use %q instead",
			record[0], fieldID, replacement,
		)
	}

	if rule := fieldexport.RuleFor(fieldID); !rule.AllowsGenericScalar() {
		if rule.Alternative != "" {
			return fmt.Errorf(
				"DCGM field %q (ID %d) cannot be exported safely by the generic scalar collector: %s; use %s instead",
				record[0], fieldID, rule.Reason, rule.Alternative,
			)
		}
		return fmt.Errorf(
			"DCGM field %q (ID %d) cannot be exported safely by the generic scalar collector: %s",
			record[0], fieldID, rule.Reason,
		)
	}

	if dcp != nil && !fieldIsSupported(uint(fieldID), *dcp) {
		slog.Warn(fmt.Sprintf("Skipping line %d ('%s'): metric not enabled", index, record[0]))
		return nil
	}

	res.DCGMCounters = append(res.DCGMCounters,
		Counter{FieldID: fieldID, FieldName: record[0], PromType: record[1], Help: record[2]})
	return nil
}

// retiredPCIeFieldReplacement returns the profiling field that replaces a PCIe
// throughput field which DCGM no longer supports.
func retiredPCIeFieldReplacement(fieldID dcgm.Short) (string, bool) {
	switch fieldID {
	case dcgm.DCGM_FI_DEV_PCIE_TX_THROUGHPUT:
		return "DCGM_FI_PROF_PCIE_TX_BYTES", true
	case dcgm.DCGM_FI_DEV_PCIE_RX_THROUGHPUT:
		return "DCGM_FI_PROF_PCIE_RX_BYTES", true
	default:
		return "", false
	}
}

// fieldIsSupported accepts ordinary and CPU fields, while requiring the DCP
// capability snapshot to contain profiling fields. ExtractCounters calls it
// for every resolved DCGM field before adding it to a counter set.
func fieldIsSupported(fieldID uint, dcp DCPCapabilities) bool {
	if fieldID < dcpFieldsStart || fieldID >= cpuFieldsStart {
		return true
	}
	return dcp.Supports(fieldID)
}

// readConfigMapSource loads the compatibility API-backed ConfigMap source from DefaultConfigMapKey.
func readConfigMapSource(
	ctx context.Context,
	kubeClient kubernetes.Interface,
	source appconfig.ConfigMapMetricSource,
) ([][]string, error) {
	var cm *corev1.ConfigMap
	cm, err := kubeClient.CoreV1().ConfigMaps(source.Namespace).Get(ctx, source.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not retrieve ConfigMap '%s:%s'; err: %w", source.Namespace, source.Name, err)
	}

	if _, ok := cm.Data[appconfig.DefaultConfigMapKey]; !ok {
		return nil, fmt.Errorf("malformed ConfigMap '%s:%s'; no '%s' key", source.Namespace, source.Name, appconfig.DefaultConfigMapKey)
	}

	r := csv.NewReader(strings.NewReader(cm.Data[appconfig.DefaultConfigMapKey]))
	r.Comment = '#'
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("malformed ConfigMap '%s:%s' key '%s': %w",
			source.Namespace, source.Name, appconfig.DefaultConfigMapKey, err)
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("malformed ConfigMap '%s:%s' key '%s': no metrics found",
			source.Namespace, source.Name, appconfig.DefaultConfigMapKey)
	}

	return records, nil
}
