//go:build container

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

package container

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const exporterStartupMessage = "Starting dcgm-exporter"

// exporterStartupLogRecord contains the structured field used to identify the
// exporter's startup message among combined container logs.
type exporterStartupLogRecord struct {
	Message string `json:"msg"`
}

// exporterStartupJSONLine finds the exporter-owned JSON startup record. DCGM
// may write unrelated and malformed lines to the same Docker stream.
func exporterStartupJSONLine(logs string) (string, error) {
	for _, line := range strings.Split(logs, "\n") {
		line = strings.TrimSpace(line)
		var record exporterStartupLogRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record.Message == exporterStartupMessage {
			return line, nil
		}
	}
	return "", fmt.Errorf("exporter startup log record %q was not found", exporterStartupMessage)
}

// parseExporterStartupLogRecord validates the selected record's JSON structure
// instead of treating the presence of a text fragment as a logging contract.
func parseExporterStartupLogRecord(line string) (exporterStartupLogRecord, error) {
	var record exporterStartupLogRecord
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return exporterStartupLogRecord{}, fmt.Errorf("parse exporter startup log record: %w", err)
	}
	if record.Message != exporterStartupMessage {
		return exporterStartupLogRecord{}, fmt.Errorf("startup log message = %q, want %q", record.Message, exporterStartupMessage)
	}
	return record, nil
}

// TestExporterStartupJSONLogRecord checks that the startup line can be selected
// and decoded from mixed exporter and DCGM output.
func TestExporterStartupJSONLogRecord(t *testing.T) {
	logs := strings.Join([]string{
		"DCGM: host engine initialized",
		`{"time":"2026-09-11T00:00:00Z", "level":"INFO", "msg": "Starting dcgm-exporter", "Version":"test"}`,
		`{"time":"2026-09-11T00:00:01Z","level":"INFO","msg":"DCGM successfully initialized!"}`,
	}, "\n")

	line, err := exporterStartupJSONLine(logs)
	if err != nil {
		t.Fatalf("exporterStartupJSONLine() error = %v", err)
	}
	record, err := parseExporterStartupLogRecord(line)
	if err != nil {
		t.Fatalf("parseExporterStartupLogRecord() error = %v", err)
	}
	if record.Message != exporterStartupMessage {
		t.Fatalf("startup log message = %q, want %q", record.Message, exporterStartupMessage)
	}
}

// TestExporterStartupJSONLogRecordSkipsMalformedCandidate checks that a broken
// line cannot hide a later valid startup record.
func TestExporterStartupJSONLogRecordSkipsMalformedCandidate(t *testing.T) {
	logs := "{\"msg\":\"Starting dcgm-exporter\"\n" +
		`{"msg": "Starting dcgm-exporter"}`

	line, err := exporterStartupJSONLine(logs)
	if err != nil {
		t.Fatalf("exporterStartupJSONLine() error = %v", err)
	}
	if _, err := parseExporterStartupLogRecord(line); err != nil {
		t.Fatalf("parseExporterStartupLogRecord() error = %v", err)
	}
}

var _ = Describe("dcgm-exporter JSON logs", Serial, Label("logFormat"), func() {
	It("writes its startup record as structured JSON", func(ctx context.Context) {
		image := requireExporterImage(ctx)
		containerID := runExporterContainer(ctx, image, nil, "--log-format=json")

		var record exporterStartupLogRecord
		Eventually(ctx, func(ctx context.Context) error {
			logs, err := containerLogsCombined(ctx, containerID)
			if err != nil {
				return fmt.Errorf("read container logs: %w", err)
			}
			line, err := exporterStartupJSONLine(logs)
			if err != nil {
				return err
			}
			record, err = parseExporterStartupLogRecord(line)
			return err
		}).WithTimeout(startupTimeout).WithPolling(time.Second).Should(Succeed())

		Expect(record.Message).To(Equal(exporterStartupMessage))
	})
})
