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
	"strings"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/dcgmprobe"
)

func TestExporterAcceptanceValidatesBeforeNativeInitialization(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"field-entity-exporter"}, "--output is required"},
		{[]string{"field-entity-exporter", "--output", "private.json", "--mode", "standalone"}, "isolated absolute"},
		{[]string{"field-entity-exporter", "--output", "private.json", "--scenario", "other"}, "--scenario"},
		{[]string{"field-entity-exporter", "--output", "private.json", "--scenario", " plain "}, "--scenario"},
		{[]string{"field-entity-exporter", "extra"}, "unexpected positional"},
	} {
		if err := run(test.args); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("run(%v)=%v, want %q", test.args, err, test.want)
		}
	}
}

func acceptanceTestReport() dcgmprobe.ExporterAcceptance {
	ci := dcgmprobe.Entity{Group: 5, ID: 64}
	report := dcgmprobe.ExporterAcceptance{Scenario: "ci-derived", WindowSizeMS: 60000, TotalSinceUsec: 100_000_000, Targets: []dcgmprobe.ExporterTarget{
		{Entity: ci, Selected: true, Labels: map[string]string{"gpu": "8", "GPU_I_ID": "3", "GPU_CI_ID": "0"}},
		{Entity: dcgmprobe.Entity{Group: 1, ID: 8}, Selected: false, Labels: map[string]string{"gpu": "8"}},
	}}
	for i, value := range []int64{45, 45, 0, 46} {
		report.History = append(report.History, dcgmprobe.HistorySample{Entity: ci, TimestampUsec: 100_000_010 + int64(i), Value: value})
	}
	report.History = append(report.History, dcgmprobe.HistorySample{Entity: ci, TimestampUsec: 10_000_000, Value: 13}, dcgmprobe.HistorySample{Entity: dcgmprobe.Entity{Group: 1, ID: 8}, TimestampUsec: 100_000_010, Value: 99}, dcgmprobe.HistorySample{Entity: dcgmprobe.Entity{Group: 4, ID: 64}, TimestampUsec: 100_000_010, Value: 77})
	return report
}

const acceptanceTestScrape = `# HELP DCGM_EXP_XID_ERRORS_COUNT Sampled count.
# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge
DCGM_EXP_XID_ERRORS_COUNT{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",window_size_in_ms="60000",xid="45"} 2
DCGM_EXP_XID_ERRORS_COUNT{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",window_size_in_ms="60000",xid="46"} 1
DCGM_EXP_XID_ERRORS_COUNT{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",window_size_in_ms="60000",xid="0"} 1
# HELP DCGM_EXP_XID_ERRORS_TOTAL Sampled total.
# TYPE DCGM_EXP_XID_ERRORS_TOTAL counter
DCGM_EXP_XID_ERRORS_TOTAL{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",xid="45"} 2
DCGM_EXP_XID_ERRORS_TOTAL{gpu="8",GPU_I_ID="3",GPU_CI_ID="0",xid="46"} 1
`

func TestExporterScrapeMatchesIndependentTypedHistory(t *testing.T) {
	report := acceptanceTestReport()
	if err := verifyExporterScrape(report, acceptanceTestScrape, 101_000_000); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		body string
	}{
		{"parent substitution", strings.ReplaceAll(acceptanceTestScrape, `,GPU_I_ID="3",GPU_CI_ID="0"`, ``)},
		{"CI zero omitted", strings.ReplaceAll(acceptanceTestScrape, `GPU_CI_ID="0"`, `GPU_CI_ID=""`)},
		{"duplicate identity replaces another series", strings.Replace(acceptanceTestScrape, `GPU_CI_ID="0",xid="46"} 1`, `GPU_CI_ID="0",xid="45"} 2`, 1)},
		{"unexpected identity label", strings.ReplaceAll(acceptanceTestScrape, `gpu="8"`, `gpu="8",Hostname="unexpected"`)},
		{"GI history substituted for CI", strings.ReplaceAll(acceptanceTestScrape, `xid="45"`, `xid="77"`)},
		{"wrong GI", strings.ReplaceAll(acceptanceTestScrape, `GPU_I_ID="3"`, `GPU_I_ID="64"`)},
		{"rollup", strings.ReplaceAll(acceptanceTestScrape, `xid="45"} 2`, `xid="45"} 4`)},
		{"wrong window", strings.ReplaceAll(acceptanceTestScrape, `window_size_in_ms="60000"`, `window_size_in_ms="5000"`)},
		{"TOTAL duplicate", strings.Replace(acceptanceTestScrape, `GPU_CI_ID="0",xid="45"} 2`, `GPU_CI_ID="0",xid="45"} 4`, 1)},
		{"TOTAL zero", acceptanceTestScrape + "DCGM_EXP_XID_ERRORS_TOTAL{gpu=\"8\",GPU_I_ID=\"3\",GPU_CI_ID=\"0\",xid=\"0\"} 1\n"},
		{"unselected parent", acceptanceTestScrape + "DCGM_EXP_XID_ERRORS_TOTAL{gpu=\"8\",xid=\"99\"} 1\n"},
		{"missing observations", strings.ReplaceAll(acceptanceTestScrape, `xid="45"} 2`, `xid="45"} 1`)},
		{"raw dependency leaked", acceptanceTestScrape + "# TYPE DCGM_FI_DEV_XID_ERROR gauge\nDCGM_FI_DEV_XID_ERROR{gpu=\"8\"} 46\n"},
		{"wrong type", strings.Replace(acceptanceTestScrape, "COUNT gauge", "COUNT counter", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyExporterScrape(report, test.body, 101_000_000); err == nil {
				t.Fatal("corrupt exposition accepted")
			}
		})
	}
	if err := verifyExporterScrape(report, acceptanceTestScrape, 170_000_000); err == nil {
		t.Fatal("out-of-window observations accepted")
	}
}

func TestRawGaugeRequiresNativeLatestValueAndCIIdentity(t *testing.T) {
	report := acceptanceTestReport()
	report.Scenario = "ci-raw"
	raw := "# HELP DCGM_FI_DEV_XID_ERROR Latest XID.\n# TYPE DCGM_FI_DEV_XID_ERROR gauge\nDCGM_FI_DEV_XID_ERROR{gpu=\"8\",GPU_I_ID=\"3\",GPU_CI_ID=\"0\",err_code=\"46\",err_msg=\"description\"} 46\n"
	if err := verifyExporterScrape(report, acceptanceTestScrape+raw, 101_000_000); err != nil {
		t.Fatal(err)
	}
	if err := verifyExporterScrape(report, acceptanceTestScrape, 101_000_000); err == nil {
		t.Fatal("missing enabled raw gauge accepted")
	}
	if err := verifyExporterScrape(report, acceptanceTestScrape+strings.Replace(raw, "} 46", "} 45", 1), 101_000_000); err == nil {
		t.Fatal("wrong raw latest value accepted")
	}
}

func TestDerivedOnlyCSVDoesNotExplicitlyEnableBackingGauge(t *testing.T) {
	for _, scenario := range []string{"ci-derived", "ci-only", "plain", "mixed"} {
		if strings.Contains(exporterCSV(scenario), xidRawFamily) {
			t.Fatalf("scenario %s enables raw gauge", scenario)
		}
	}
	if !strings.Contains(exporterCSV("ci-raw"), xidRawFamily) {
		t.Fatal("raw scenario omitted gauge")
	}
}

func TestCIOnlyCountersWithEmptySelectedParents(t *testing.T) {
	report := acceptanceTestReport()
	report.Scenario = "ci-only"
	report.History = report.History[:5]
	report.Targets[1].Selected = true
	report.Targets[0].Labels["GPU_I_PROFILE"] = "2g.20gb"
	report.Targets = append(report.Targets, dcgmprobe.ExporterTarget{
		Entity: dcgmprobe.Entity{Group: 4, ID: 64}, Selected: true,
		Labels: map[string]string{"gpu": "8", "GPU_I_ID": "3", "GPU_I_PROFILE": "2g.20gb"},
	})
	body := strings.ReplaceAll(acceptanceTestScrape, `GPU_I_ID="3",GPU_CI_ID="0"`, `GPU_I_ID="3",GPU_I_PROFILE="2g.20gb",GPU_CI_ID="0"`) +
		"DCGM_EXP_XID_ERRORS_COUNT{gpu=\"8\",window_size_in_ms=\"60000\"} 0\n" +
		"DCGM_EXP_XID_ERRORS_COUNT{gpu=\"8\",GPU_I_ID=\"3\",GPU_I_PROFILE=\"2g.20gb\",window_size_in_ms=\"60000\"} 0\n"
	var withoutCI []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "GPU_CI_ID") {
			withoutCI = append(withoutCI, line)
		}
	}
	emptyCI := strings.Join(withoutCI, "\n")
	for _, test := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{"valid", body, false},
		{"CI omitted", emptyCI, true},
		{"CI zero placeholder", emptyCI + "DCGM_EXP_XID_ERRORS_COUNT{gpu=\"8\",GPU_I_ID=\"3\",GPU_I_PROFILE=\"2g.20gb\",GPU_CI_ID=\"0\",window_size_in_ms=\"60000\"} 0\n", true},
		{"missing profile", strings.ReplaceAll(body, `,GPU_I_PROFILE="2g.20gb"`, ""), true},
		{"wrong profile", strings.ReplaceAll(body, "2g.20gb", "1g.10gb"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyExporterScrape(report, test.body, 101_000_000)
			if (err != nil) != test.wantError {
				t.Fatalf("scrape error = %v, want error %t", err, test.wantError)
			}
		})
	}
}

func TestCIOnlyScenarioIsAccepted(t *testing.T) {
	if !validExporterScenario("ci-only") {
		t.Fatal("CI-only native regression is unavailable")
	}
}

// A plain GPU sharing an XID with the CI must retain its own observations.
func TestPlainAndMixedScrapesKeepEntityObservationsSeparate(t *testing.T) {
	const plainScrape = `# HELP DCGM_EXP_XID_ERRORS_COUNT Sampled count.
# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge
DCGM_EXP_XID_ERRORS_COUNT{gpu="9",window_size_in_ms="60000",xid="45"} 1
DCGM_EXP_XID_ERRORS_COUNT{gpu="9",window_size_in_ms="60000",xid="0"} 1
# HELP DCGM_EXP_XID_ERRORS_TOTAL Sampled total.
# TYPE DCGM_EXP_XID_ERRORS_TOTAL counter
DCGM_EXP_XID_ERRORS_TOTAL{gpu="9",xid="45"} 1
`
	plain := dcgmprobe.Entity{Group: 1, ID: 9}
	report := acceptanceTestReport()
	report.Targets = append(report.Targets, dcgmprobe.ExporterTarget{Entity: plain, Selected: true, Labels: map[string]string{"gpu": "9"}})
	report.History = append(report.History,
		dcgmprobe.HistorySample{Entity: plain, TimestampUsec: 100_000_015, Value: 45},
		dcgmprobe.HistorySample{Entity: plain, TimestampUsec: 100_000_016, Value: 0})
	report.Scenario = "mixed"
	// Remove repeated metadata declarations when combining the two independent bodies.
	mixed := acceptanceTestScrape + strings.Join([]string{
		`DCGM_EXP_XID_ERRORS_COUNT{gpu="9",window_size_in_ms="60000",xid="45"} 1`,
		`DCGM_EXP_XID_ERRORS_COUNT{gpu="9",window_size_in_ms="60000",xid="0"} 1`,
		`DCGM_EXP_XID_ERRORS_TOTAL{gpu="9",xid="45"} 1`, "",
	}, "\n")
	if err := verifyExporterScrape(report, mixed, 101_000_000); err != nil {
		t.Fatal(err)
	}
	if err := verifyExporterScrape(report, strings.ReplaceAll(mixed, `gpu="9",xid="45"} 1`, `gpu="9",xid="45"} 3`), 101_000_000); err == nil {
		t.Fatal("plain/CI observations rolled into plain TOTAL")
	}
	report.Scenario = "plain"
	report.Targets[0].Selected = false
	if err := verifyExporterScrape(report, plainScrape, 101_000_000); err != nil {
		t.Fatal(err)
	}
	if err := verifyExporterScrape(report, mixed, 101_000_000); err == nil {
		t.Fatal("plain selection emitted unselected CI observations")
	}
}

func TestSelectedParentAndGIKeepTheirOwnHistoryAndIdentity(t *testing.T) {
	report := acceptanceTestReport()
	report.Targets[1].Selected = true
	report.History[len(report.History)-2].Value = 31
	gi := dcgmprobe.Entity{Group: 4, ID: 64}
	report.Targets = append(report.Targets, dcgmprobe.ExporterTarget{Entity: gi, Selected: true, Labels: map[string]string{"gpu": "8", "GPU_I_ID": "3"}})
	body := acceptanceTestScrape + `DCGM_EXP_XID_ERRORS_COUNT{gpu="8",window_size_in_ms="60000",xid="31"} 1
DCGM_EXP_XID_ERRORS_COUNT{gpu="8",GPU_I_ID="3",window_size_in_ms="60000",xid="77"} 1
DCGM_EXP_XID_ERRORS_TOTAL{gpu="8",xid="31"} 1
DCGM_EXP_XID_ERRORS_TOTAL{gpu="8",GPU_I_ID="3",xid="77"} 1
`
	if err := verifyExporterScrape(report, body, 101_000_000); err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []string{
		strings.ReplaceAll(body, `xid="31"`, `xid="45"`),
		strings.ReplaceAll(body, `xid="77"`, `xid="45"`),
		strings.ReplaceAll(body, `GPU_I_ID="3",xid="77"`, `GPU_I_ID="3",GPU_CI_ID="0",xid="77"`),
		strings.ReplaceAll(body, `gpu="8",xid="31"} 1`, `gpu="8",xid="31"} 2`),
	} {
		if err := verifyExporterScrape(report, corrupt, 101_000_000); err == nil {
			t.Fatal("substituted or duplicated parent/GI history accepted")
		}
	}
}

func TestCIOnlyNativeHistoryRequiresExactNoDataMarkers(t *testing.T) {
	gpu, gi, ci := dcgmprobe.Entity{Group: 1, ID: 8}, dcgmprobe.Entity{Group: 4, ID: 64}, dcgmprobe.Entity{Group: 5, ID: 64}
	value := dcgmprobe.HistorySample{Entity: ci, FieldID: 230, FieldType: 'i', TimestampUsec: 123, Value: 45}
	gpuEmpty := dcgmprobe.HistorySample{Entity: gpu, FieldID: 230, FieldType: 'i', Status: -14, Value: 9223372036854775792}
	giEmpty := gpuEmpty
	giEmpty.Entity = gi
	report := dcgmprobe.ExporterAcceptance{Scenario: "ci-only", Injected: []dcgmprobe.HistorySample{value}, Targets: []dcgmprobe.ExporterTarget{
		{Entity: gpu, Selected: true}, {Entity: gi, Selected: true}, {Entity: ci, Selected: true},
	}}
	wrongStatus := gpuEmpty
	wrongStatus.Status = -16
	parentValue := gpuEmpty
	parentValue.Status, parentValue.TimestampUsec, parentValue.Value = 0, 123, 45
	for _, test := range []struct {
		name   string
		values []dcgmprobe.HistorySample
		want   bool
	}{
		{"native no data", []dcgmprobe.HistorySample{gpuEmpty, giEmpty, value}, true},
		{"missing CI observation", []dcgmprobe.HistorySample{gpuEmpty, giEmpty}, false},
		{"parent XID", []dcgmprobe.HistorySample{parentValue, giEmpty, value}, false},
		{"other native error", []dcgmprobe.HistorySample{wrongStatus, giEmpty, value}, false},
		{"duplicate marker", []dcgmprobe.HistorySample{gpuEmpty, gpuEmpty, giEmpty, value}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sameExporterHistory(report, test.values); got != test.want {
				t.Fatalf("history accepted = %t, want %t", got, test.want)
			}
		})
	}
}
