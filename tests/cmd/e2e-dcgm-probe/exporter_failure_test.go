/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

func TestFailedExporterAttemptWritesSeparateEvidence(t *testing.T) {
	for _, test := range []struct {
		name         string
		unwritable   bool
		largeHistory bool
	}{
		{"assertion failure", false, false},
		{"diagnostic write failure", true, false},
		{"bounded history", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			if test.unwritable {
				path = filepath.Join(path, "missing", "report.json")
			}
			report := acceptanceTestReport()
			if test.largeHistory {
				report.History = make([]dcgmprobe.HistorySample, 65)
			}
			cause := errors.New("CI counter assertion failed")
			failure := dcgmprobe.ExporterFailure{
				Stage: "first scrape", HistoryVerified: true, Cleanup: "complete",
				Response: dcgmprobe.ExporterHTTPResponse{Body: "rejected metric", Status: 200, TimestampUsec: 123, Error: cause.Error()},
			}
			err := writeExporterAcceptanceResult(exporterAcceptanceOptions{entityHistoryOptions: entityHistoryOptions{outputPath: path}}, report, failure, cause)
			require.ErrorIs(t, err, cause)
			_, statErr := os.Stat(path)
			require.True(t, os.IsNotExist(statErr), "failed attempt created a success report")
			if test.unwritable {
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			data, readErr := os.ReadFile(path + ".failed.json")
			require.NoError(t, readErr)
			var saved dcgmprobe.ExporterFailure
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, "failed", saved.Outcome)
			require.Equal(t, 1, saved.Version)
			require.True(t, saved.HistoryVerified)
			require.Equal(t, "complete", saved.Cleanup)
			require.Equal(t, "rejected metric", saved.Response.Body)
			require.Equal(t, 200, saved.Response.Status)
			require.Equal(t, cause.Error(), saved.Error)
			require.Equal(t, test.largeHistory, saved.HistoryTruncated)
			require.LessOrEqual(t, len(saved.Report.History), 64)
		})
	}
}
