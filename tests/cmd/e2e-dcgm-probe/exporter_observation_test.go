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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

const acceptanceProgressScrape = acceptanceTestScrape + `DCGM_EXP_XID_ERRORS_COUNT{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",window_size_in_ms="60000",xid="71"} 1
DCGM_EXP_XID_ERRORS_TOTAL{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",xid="71"} 1
`

func acceptanceTestAdvance(report *dcgmprobe.ExporterAcceptance) func() error {
	return func() error {
		report.SecondHistory = append([]dcgmprobe.HistorySample(nil), report.History...)
		report.SecondHistory = append(report.SecondHistory, exporterProgressFixture(*report, time.Now().UnixMicro())...)
		return nil
	}
}

func TestExporterObservationPopulatesPrivateReport(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := acceptanceTestScrape
		if requests.Add(1) > 2 {
			body = acceptanceProgressScrape
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()

	report := currentAcceptanceTestReport()
	app := acceptanceTestExporter(&acceptanceClient{})
	if err := observeExporterScrapes(&report, app, strings.TrimPrefix(server.URL, "http://"), acceptanceTestAdvance(&report)); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || report.FirstScrape != acceptanceTestScrape || report.SecondScrape != acceptanceProgressScrape ||
		report.FirstScrapeUsec <= report.TotalSinceUsec || report.SecondScrapeUsec < report.FirstScrapeUsec {
		t.Fatalf("report lost HTTP observations: %+v (requests=%d)", report, requests.Load())
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var saved dcgmprobe.ExporterAcceptance
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.FirstScrape != report.FirstScrape || saved.SecondScrape != report.SecondScrape || len(saved.History) != len(report.History) || len(saved.SecondHistory) != len(report.History)+1 {
		t.Fatalf("serialized report lost acceptance evidence: %+v", saved)
	}
	if err := verifyExporterScrape(saved, saved.FirstScrape, saved.FirstScrapeUsec); err != nil {
		t.Fatal(err)
	}
	saved.History = saved.SecondHistory
	if err := verifyExporterScrape(saved, saved.SecondScrape, saved.SecondScrapeUsec); err != nil {
		t.Fatal(err)
	}
}

func TestExporterObservationRequiresTotalProgressWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"stale TOTAL after failed poll", acceptanceTestScrape},
		{"COUNT progresses but TOTAL fails", strings.Replace(acceptanceProgressScrape, `DCGM_EXP_XID_ERRORS_TOTAL{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",xid="71"} 1`+"\n", "", 1)},
		{"new code counted but old TOTAL replayed", strings.Replace(acceptanceProgressScrape, `GPU_CI_ID="0",xid="45"} 2`, `GPU_CI_ID="0",xid="45"} 4`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := acceptanceTestExporter(&acceptanceClient{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := acceptanceTestScrape
				if requests.Add(1) > 1 {
					body = test.body
					app.done <- errors.New("test exporter stopped after invalid second scrape")
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			report := currentAcceptanceTestReport()
			err := observeExporterScrapes(&report, app, strings.TrimPrefix(server.URL, "http://"), acceptanceTestAdvance(&report))
			if err == nil || report.FirstScrape == "" || report.SecondScrape != "" || requests.Load() != 2 {
				t.Fatalf("invalid second scrape accepted: err=%v, first=%q, second=%q", err, report.FirstScrape, report.SecondScrape)
			}
		})
	}
}

func TestExporterProgressTimesOutWhileApplicationServesStaleTotals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, acceptanceTestScrape)
	}))
	defer server.Close()
	report := currentAcceptanceTestReport()
	if err := acceptanceTestAdvance(&report)(); err != nil {
		t.Fatal(err)
	}
	report.History = report.SecondHistory
	app := acceptanceTestExporter(&acceptanceClient{})
	body, _, err := app.waitScrape(strings.TrimPrefix(server.URL, "http://"), func(body string, ts int64) error {
		return verifyExporterScrape(report, body, ts)
	}, time.Now().Add(100*time.Millisecond))
	if err == nil || body != "" || !strings.Contains(err.Error(), "timed out") || app.completed() {
		t.Fatalf("live exporter with stale totals accepted: body=%q, err=%v", body, err)
	}
	if app.lastResponse.Body != acceptanceTestScrape || app.lastResponse.Status != http.StatusOK || app.lastResponse.TimestampUsec == 0 || app.lastResponse.Error == "" {
		t.Fatalf("failed polling discarded its last HTTP evidence: %+v", app.lastResponse)
	}
}

func TestExporterScrapeRejectsTruncatedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", (1<<20)+1))
	}))
	defer server.Close()
	app := acceptanceTestExporter(&acceptanceClient{})
	_, _, err := app.waitScrape(strings.TrimPrefix(server.URL, "http://"), func(string, int64) error { return nil }, time.Now().Add(100*time.Millisecond))
	if err == nil || !app.lastResponse.Truncated || len(app.lastResponse.Body) != 1<<20 {
		t.Fatalf("truncated response accepted or unbounded: err=%v, bytes=%d, truncated=%t", err, len(app.lastResponse.Body), app.lastResponse.Truncated)
	}
}

func TestExporterObservationStopsOnProgressInjectionFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, acceptanceTestScrape)
	}))
	defer server.Close()
	report := currentAcceptanceTestReport()
	want := errors.New("progress injection failed")
	err := observeExporterScrapes(&report, acceptanceTestExporter(&acceptanceClient{}), strings.TrimPrefix(server.URL, "http://"), func() error { return want })
	if !errors.Is(err, want) || requests.Load() != 1 || report.SecondScrape != "" {
		t.Fatalf("injection failure ignored: err=%v, requests=%d", err, requests.Load())
	}
}

func TestExporterProgressUpdatesRawGaugeOracle(t *testing.T) {
	report := currentAcceptanceTestReport()
	report.Scenario = "ci-raw"
	if err := acceptanceTestAdvance(&report)(); err != nil {
		t.Fatal(err)
	}
	report.History = report.SecondHistory
	raw := `# TYPE DCGM_FI_DEV_XID_ERROR gauge
DCGM_FI_DEV_XID_ERROR{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",err_code="71",err_msg="description"} 71
`
	if err := verifyExporterScrape(report, acceptanceProgressScrape+raw, time.Now().UnixMicro()); err != nil {
		t.Fatal(err)
	}
	staleRaw := strings.ReplaceAll(raw, "71", "46")
	if err := verifyExporterScrape(report, acceptanceProgressScrape+staleRaw, time.Now().UnixMicro()); err == nil {
		t.Fatal("stale raw XID accepted after progress injection")
	}
}

func TestExporterProgressFixtureCoversEverySelectedEntity(t *testing.T) {
	for _, selected := range [][]bool{{true, true, true, false, false}, {false, false, false, true, false}, {true, true, true, true, false}} {
		var targets []dcgmprobe.ExporterTarget
		for i, enabled := range selected {
			targets = append(targets, dcgmprobe.ExporterTarget{Entity: dcgmprobe.Entity{Group: uint(i + 1), ID: 64}, Selected: enabled})
		}
		samples := exporterProgressFixture(dcgmprobe.ExporterAcceptance{Targets: targets}, 123)
		for i, target := range targets {
			if !target.Selected {
				continue
			}
			if len(samples) == 0 || samples[0].Entity != target.Entity || samples[0].TimestampUsec != 123 || samples[0].Value != 71+int64(i) {
				t.Fatalf("missing progress sample for target %v: %v", target, samples)
			}
			samples = samples[1:]
		}
		if len(samples) != 0 {
			t.Fatalf("unselected entity received progress sample: %v", samples)
		}
	}
}

func TestCIOnlyFixturesLeaveParentsEmpty(t *testing.T) {
	ci := dcgmprobe.Entity{Group: 5, ID: 64}
	control := dcgmprobe.Entity{Group: 1, ID: 9}
	report := dcgmprobe.ExporterAcceptance{Scenario: "ci-only", Targets: []dcgmprobe.ExporterTarget{
		{Entity: dcgmprobe.Entity{Group: 1, ID: 8}, Selected: true},
		{Entity: dcgmprobe.Entity{Group: 4, ID: 64}, Selected: true},
		{Entity: ci, Selected: true},
		{Entity: control},
	}}
	for _, test := range []struct {
		name    string
		fixture func(dcgmprobe.ExporterAcceptance, int64) []dcgmprobe.HistorySample
		want    []dcgmprobe.HistorySample
	}{
		{"initial", exporterInitialFixture, []dcgmprobe.HistorySample{
			{Entity: ci, FieldID: 230, FieldType: 'i', TimestampUsec: 10_000_000, Value: 13},
			{Entity: ci, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_001, Value: 45},
			{Entity: ci, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_002, Value: 45},
			{Entity: ci, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_003, Value: 0},
			{Entity: ci, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_004, Value: 46},
			{Entity: control, FieldID: 230, FieldType: 'i', TimestampUsec: 10_000_000, Value: 13},
			{Entity: control, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_001, Value: 51},
			{Entity: control, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_002, Value: 51},
			{Entity: control, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_003, Value: 0},
			{Entity: control, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_004, Value: 52},
		}},
		{"progress", exporterProgressFixture, []dcgmprobe.HistorySample{
			{Entity: ci, FieldID: 230, FieldType: 'i', TimestampUsec: 100_000_000, Value: 73},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.fixture(report, 100_000_000)
			if !sameHistory(test.want, got) {
				t.Fatalf("CI-only fixture = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestExporterScrapeReadinessChecksApplicationLifecycle(t *testing.T) {
	for _, state := range []string{"exited", "cleanup requested"} {
		t.Run(state, func(t *testing.T) {
			client := &acceptanceClient{}
			app := acceptanceTestExporter(client)
			if state == "exited" {
				app.done <- errors.New("startup failed")
			} else {
				client.Cleanup()
			}
			_, _, err := app.waitScrape("127.0.0.1:0", func(string, int64) error { return nil }, time.Now().Add(time.Second))
			if err == nil {
				t.Fatal("unavailable application passed readiness")
			}
		})
	}
}

func currentAcceptanceTestReport() dcgmprobe.ExporterAcceptance {
	report := acceptanceTestReport()
	shift := time.Now().Add(-time.Second).UnixMicro() - report.TotalSinceUsec
	report.TotalSinceUsec += shift
	for i := range report.History {
		report.History[i].TimestampUsec += shift
	}
	return report
}
