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

package counters

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	stdos "os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

type metricContractRow struct {
	record []string
	lineNo int
}

func TestShippedMetricSourcesLoadThroughCounterExtraction(t *testing.T) {
	sources := []string{
		"etc/default-counters.csv",
		"etc/dcp-metrics-included.csv",
		"etc/1.x-compatibility-metrics.csv",
		"tests/host/testdata/default-counters.csv",
		"tests/host/testdata/dcp-counters.csv",
		"deployment/templates/metrics-configmap.yaml",
	}

	repoRoot := repoRootFromThisTest(t)
	for _, source := range sources {
		source := source
		t.Run(source, func(t *testing.T) {
			rows := readMetricContractRows(t, filepath.Join(repoRoot, source), source)
			if len(rows) == 0 {
				t.Fatalf("%s: no active metric rows found", source)
			}

			for _, row := range rows {
				_, err := ExtractCounters([][]string{row.record}, metricContractConfig(row.record))
				if err != nil {
					t.Errorf("%s:%d: row does not satisfy counter extraction contract: %v", source, row.lineNo, err)
				}
			}
		})
	}
}

func repoRootFromThisTest(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test source path")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(filename), "../../.."))
}

func readMetricContractRows(t *testing.T, path string, source string) []metricContractRow {
	t.Helper()

	file, err := stdos.Open(path)
	if err != nil {
		t.Fatalf("%s: open failed: %v", source, err)
	}
	defer file.Close()

	var rows []metricContractRow
	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if !isMetricContractRow(line) {
			continue
		}

		record, err := parseMetricContractRecord(line)
		if err != nil {
			t.Fatalf("%s:%d: malformed metric row: %v", source, lineNo, err)
		}
		if len(record) != 3 {
			t.Fatalf("%s:%d: expected 3 CSV fields, got %d: %v", source, lineNo, len(record), record)
		}

		rows = append(rows, metricContractRow{record: record, lineNo: lineNo})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("%s: scan failed: %v", source, err)
	}

	return rows
}

func isMetricContractRow(line string) bool {
	if line == "" || strings.HasPrefix(line, "#") {
		return false
	}

	return strings.HasPrefix(line, "DCGM_") || strings.HasPrefix(line, "dcgm_")
}

func parseMetricContractRecord(line string) ([]string, error) {
	reader := csv.NewReader(strings.NewReader(line))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	record, err := reader.Read()
	if err != nil {
		return nil, err
	}
	if extraRecord, err := reader.Read(); err == nil {
		return nil, fmt.Errorf("unexpected extra CSV record %v", extraRecord)
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}

	for i := range record {
		record[i] = strings.TrimSpace(record[i])
	}

	return record, nil
}

func metricContractConfig(record []string) *appconfig.Config {
	cfg := &appconfig.Config{CollectDCP: false}

	if len(record) == 0 {
		return cfg
	}

	fieldID, ok := dcgm.GetFieldID(record[0])
	if !ok || fieldID < dcpFieldsStart || fieldID >= cpuFieldsStart {
		return cfg
	}

	cfg.CollectDCP = true
	cfg.MetricGroups = []dcgm.MetricGroup{{FieldIds: []uint{uint(fieldID)}}}
	return cfg
}

func TestParseMetricContractRecordRejectsExtraRecords(t *testing.T) {
	record, err := parseMetricContractRecord("DCGM_FI_DEV_GPU_TEMP,gauge,temp\nDCGM_FI_DEV_POWER_USAGE,gauge,power")

	if err == nil {
		t.Fatalf("expected extra CSV record to fail, got record %v", record)
	}
	if !strings.Contains(err.Error(), "unexpected extra CSV record") {
		t.Fatalf("expected explicit extra-record error, got %v", err)
	}
}
