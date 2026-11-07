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

package host

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Families the injected-events scenario reads.
const (
	xidErrorFamily         = "DCGM_FI_DEV_XID_ERROR"
	xidCountFamily         = "DCGM_EXP_XID_ERRORS_COUNT"
	xidTotalFamily         = "DCGM_EXP_XID_ERRORS_TOTAL"
	clockEventsCountFamily = "DCGM_EXP_CLOCK_EVENTS_COUNT"
	clockEventsTotalFamily = "DCGM_EXP_CLOCK_EVENTS_TOTAL"
)

// DCGM field IDs the scenario injects into. They are go-dcgm's
// dcgm.DCGM_FI_DEV_XID_ERRORS, which the XID collectors in
// internal/pkg/collector/xid_collector.go and xid_total_collector.go read, and
// dcgm.DCGM_FI_DEV_CLOCKS_EVENT_REASONS, which clock_events_collector.go and
// clock_events_total_collector.go read. This cgo-free test binary cannot
// import go-dcgm, so the numbers are written out.
const (
	fieldXIDErrors         = 230
	fieldClockEventReasons = 112
)

// injectedEventWindow is the XID and clock-event count window the exporter
// runs with. It is long enough to read the counts after an injection and short
// enough to see them expire.
const injectedEventWindow = 15 * time.Second

// expiryWait is how long past the window the scenario waits for the counts to
// drop to zero, which allows for collection intervals and slow scrapes.
const expiryWait = 10 * time.Second

// injectedEventCounters enables the event families and the last XID value.
// The raw clock-event reason bitmap is not listed because the exporter refuses
// to export it as a scalar.
var injectedEventCounters = []string{
	xidErrorFamily + ", gauge, Value of the last XID error encountered.",
	xidCountFamily + ", gauge, XID errors observed during the configured time window.",
	xidTotalFamily + ", counter, Total XID errors observed since the counter was last reset.",
	clockEventsCountFamily + ", gauge, Clock events observed during the configured time window.",
	clockEventsTotalFamily + ", counter, Total clock events observed since the counter was last reset (edge-counted).",
}

// injectedXIDCandidates are XID values the scenario may inject. It uses the
// first one that no GPU reports before the injection.
var injectedXIDCandidates = []int64{31, 43, 48, 79}

// clockEventReason is one clock-event reason bit and the clock_event label
// the exporter gives it.
type clockEventReason struct {
	label string
	bit   int64
}

// quietClockEventReasons are reasons that an idle GPU rarely reports, in the
// order the scenario tries them. The bits are DCGM's
// DCGM_CLOCKS_EVENT_REASON_* values, which clockEventToString in
// internal/pkg/collector/clock_events_collector.go maps to these labels.
var quietClockEventReasons = []clockEventReason{
	{label: "hw_power_brake", bit: 0x80},
	{label: "display_clocks", bit: 0x100},
	{label: "hw_thermal", bit: 0x40},
	{label: "hw_slowdown", bit: 0x08},
	{label: "sw_thermal", bit: 0x20},
	{label: "sync_boost", bit: 0x10},
}

// injectedEvent is the XID and clock-event reason injected into one GPU.
type injectedEvent struct {
	target string
	xid    int64
	reason clockEventReason
}

// runInjectedEventCounts injects an XID and a clock-event reason into one real
// GPU through a private hostengine and checks that only that GPU's counts,
// totals and last XID follow, and that the counts drop to zero after the window.
func runInjectedEventCounts(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	engine := startUnixHostEngine(t)
	window := strconv.FormatInt(injectedEventWindow.Milliseconds(), 10)
	process, metricsURL := startFastExporterOnHostEngine(t, engine, injectedEventCounters,
		[]string{xidCountFamily, clockEventsCountFamily},
		"--xid-count-window-size", window,
		"--clock-events-count-window-size", window,
	)

	// Fill one window so the first scrape shows the events the GPUs report on their own.
	time.Sleep(injectedEventWindow)
	before := scrapeGPUMetrics(t, metricsURL, process)
	gpus := before.gpus(xidCountFamily)
	require.NotEmpty(t, gpus, "%s has no GPU samples", xidCountFamily)
	if len(gpus) < 2 {
		t.Logf("only GPU %v is monitored, so the checks that no other GPU shows the events have nothing to compare", gpus)
	}

	// Inject into the highest GPU ID, so a GPU ID mix-up with GPU 0 shows.
	event := injectedEvent{target: gpus[len(gpus)-1]}
	var ok bool
	event.xid, ok = pickUnusedXID(before, gpus)
	require.Truef(t, ok, "every candidate XID %v is already reported by a GPU", injectedXIDCandidates)
	event.reason, ok = pickQuietClockEvent(before, gpus)
	require.True(t, ok, "every candidate clock-event reason is already reported by a GPU")
	t.Logf("injecting XID %d and clock-event reason %s (0x%x) into GPU %s of GPUs %v",
		event.xid, event.reason.label, event.reason.bit, event.target, gpus)

	// The window of the first event starts at its injection, so time the checks from before it.
	injected := time.Now()
	injectEvent(t, engine, event.target, fieldXIDErrors, event.xid)
	injectEvent(t, engine, event.target, fieldClockEventReasons, event.reason.bit)

	waitForInjectedEvents(t, metricsURL, process, event, injected.Add(injectedEventWindow/2))

	// Near the end of the window the events are still counted, on that GPU only.
	time.Sleep(time.Until(injected.Add(injectedEventWindow - 3*time.Second)))
	late := scrapeGPUMetrics(t, metricsURL, process)
	require.Emptyf(t, injectedEventProblems(late, gpus, event),
		"3s before the end of the %s window\nnv-hostengine log:\n%s", injectedEventWindow, engine.logTail())

	// Past the window the counts are zero again, and the totals still hold the events.
	waitForExpiredCounts(t, metricsURL, process, gpus, event, injected.Add(injectedEventWindow+expiryWait))
}

// injectEvent writes one value for field into the hostengine's cache for gpu
// with dcgmi. It changes only DCGM's cache, not the GPU.
func injectEvent(t testing.TB, engine privateHostEngine, gpu string, field int, value int64) {
	t.Helper()
	out, ok := engine.dcgmi(t, "test", "--inject", "--gpuid", gpu,
		"-f", strconv.Itoa(field), "-v", strconv.FormatInt(value, 10))
	require.Truef(t, ok && strings.Contains(out, "Successfully injected"),
		"inject field %d value %d into GPU %s: %s", field, value, gpu, out)
}

// waitForInjectedEvents scrapes until the injected events show on the target
// GPU and no other, or fails at deadline with the problems of the last scrape.
func waitForInjectedEvents(t testing.TB, metricsURL string, process *hostExporterProcess, event injectedEvent, deadline time.Time) {
	t.Helper()
	for {
		scrape := scrapeGPUMetrics(t, metricsURL, process)
		problems := injectedEventProblems(scrape, scrape.gpus(xidCountFamily), event)
		if len(problems) == 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the injected events did not show on GPU %s in time:\n%s", event.target, strings.Join(problems, "\n"))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitForExpiredCounts scrapes until no GPU counts the injected events, or
// fails at deadline with the problems of the last scrape.
func waitForExpiredCounts(
	t testing.TB, metricsURL string, process *hostExporterProcess, gpus []string, event injectedEvent, deadline time.Time,
) {
	t.Helper()
	for {
		problems := expiredCountProblems(scrapeGPUMetrics(t, metricsURL, process), gpus, event)
		if len(problems) == 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the counts did not expire after the %s window:\n%s", injectedEventWindow, strings.Join(problems, "\n"))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// pickUnusedXID returns the first candidate XID that no GPU counts, totals or
// reports as its last XID.
func pickUnusedXID(scrape gpuScrape, gpus []string) (int64, bool) {
	for _, xid := range injectedXIDCandidates {
		label := map[string]string{"xid": strconv.FormatInt(xid, 10)}
		used := false
		for _, gpu := range gpus {
			last, ok := scrape.value(xidErrorFamily, gpu)
			if scrape.sum(xidCountFamily, gpu, label) != 0 || scrape.sum(xidTotalFamily, gpu, label) != 0 ||
				(ok && int64(last) == xid) {
				used = true
				break
			}
		}

		if !used {
			return xid, true
		}
	}
	return 0, false
}

// pickQuietClockEvent returns the first candidate reason that no GPU counts
// or totals. The scrape must follow a full count window, so a reason a GPU
// reports now shows in its count: the count holds every sample with the
// reason set, including a reason set since before the exporter started, which
// has no edge for the total. Injecting such a reason would change nothing.
func pickQuietClockEvent(scrape gpuScrape, gpus []string) (clockEventReason, bool) {
	for _, reason := range quietClockEventReasons {
		label := map[string]string{"clock_event": reason.label}
		quiet := true
		for _, gpu := range gpus {
			if scrape.sum(clockEventsCountFamily, gpu, label) != 0 || scrape.sum(clockEventsTotalFamily, gpu, label) != 0 {
				quiet = false
				break
			}
		}

		if quiet {
			return reason, true
		}
	}
	return clockEventReason{}, false
}

// eventChecks returns the families and labels that show event. The count
// series also carry the window the exporter runs with.
func eventChecks(event injectedEvent) []struct {
	family string
	match  map[string]string
} {
	window := strconv.FormatInt(injectedEventWindow.Milliseconds(), 10)
	xid := strconv.FormatInt(event.xid, 10)
	return []struct {
		family string
		match  map[string]string
	}{
		{xidCountFamily, map[string]string{"xid": xid, "window_size_in_ms": window}},
		{xidTotalFamily, map[string]string{"xid": xid}},
		{clockEventsCountFamily, map[string]string{"clock_event": event.reason.label, "window_size_in_ms": window}},
		{clockEventsTotalFamily, map[string]string{"clock_event": event.reason.label}},
	}
}

// injectedEventProblems lists how scrape differs from event injected into its
// target: the target's counts and totals for it are positive and its last XID
// equals the injected XID, while every other GPU in gpus shows none of them.
func injectedEventProblems(scrape gpuScrape, gpus []string, event injectedEvent) []string {
	var problems []string
	for _, gpu := range gpus {
		problems = append(problems, injectedCountProblems(scrape, gpu, event)...)
		problems = append(problems, lastXIDProblems(scrape, gpu, event)...)
	}
	return problems
}

// injectedCountProblems lists the count and total series of one GPU that do
// not match the injection: positive on the target, zero everywhere else.
func injectedCountProblems(scrape gpuScrape, gpu string, event injectedEvent) []string {
	var problems []string
	for _, check := range eventChecks(event) {
		got := scrape.sum(check.family, gpu, check.match)
		if gpu == event.target && got <= 0 {
			problems = append(problems, fmt.Sprintf("GPU %s %s = %g, want > 0 (injected into this GPU)",
				gpu, seriesSelector(check.family, check.match), got))
		}
		if gpu != event.target && got != 0 {
			problems = append(problems, fmt.Sprintf("GPU %s %s = %g, want 0 (injected into GPU %s only)",
				gpu, seriesSelector(check.family, check.match), got, event.target))
		}
	}
	return problems
}

// lastXIDProblems checks one GPU's last-XID gauge: the injected XID on the
// target, and not the injected XID on any other GPU.
func lastXIDProblems(scrape gpuScrape, gpu string, event injectedEvent) []string {
	last, ok := scrape.value(xidErrorFamily, gpu)
	if gpu == event.target && (!ok || int64(last) != event.xid) {
		return []string{fmt.Sprintf("GPU %s %s = %g (present %t), want %d (the injected XID)",
			gpu, xidErrorFamily, last, ok, event.xid)}
	}
	if gpu != event.target && ok && int64(last) == event.xid {
		return []string{fmt.Sprintf("GPU %s %s = %d, want another value (XID injected into GPU %s only)",
			gpu, xidErrorFamily, event.xid, event.target)}
	}
	return nil
}

// expiredCountProblems lists how scrape differs from a scrape taken after the
// count window: both count families still report every GPU, no GPU counts the
// injected XID or reason, and the target's totals still hold them. A missing
// count family or GPU is a problem, not an expired count.
func expiredCountProblems(scrape gpuScrape, gpus []string, event injectedEvent) []string {
	var problems []string
	for _, check := range eventChecks(event) {
		if strings.HasSuffix(check.family, "_TOTAL") {
			problems = append(problems, keptTotalProblems(scrape, event.target, check.family, check.match)...)
			continue
		}

		problems = append(problems, expiredFamilyProblems(scrape, gpus, check.family, check.match, injectedEventWindow)...)
	}
	return problems
}

// keptTotalProblems requires the total of family on target to stay positive.
func keptTotalProblems(scrape gpuScrape, target, family string, match map[string]string) []string {
	if got := scrape.sum(family, target, match); got <= 0 {
		return []string{fmt.Sprintf("GPU %s %s = %g, want > 0 (a total never drops)", target, seriesSelector(family, match), got)}
	}
	return nil
}

// expiredFamilyProblems requires the count family to have a sample for every
// GPU and none of them to count match, after a count window of window.
func expiredFamilyProblems(scrape gpuScrape, gpus []string, family string, match map[string]string, window time.Duration) []string {
	if scrape[family] == nil {
		return []string{fmt.Sprintf("%s is missing after the %s window, want a sample for every GPU", family, window)}
	}

	reported := scrape.gpus(family)
	var problems []string
	for _, gpu := range gpus {
		if !slices.Contains(reported, gpu) {
			problems = append(problems, fmt.Sprintf("GPU %s has no %s sample after the %s window", gpu, family, window))
			continue
		}

		if got := scrape.sum(family, gpu, match); got != 0 {
			problems = append(problems, fmt.Sprintf("GPU %s %s = %g, want 0 after the %s window",
				gpu, seriesSelector(family, match), got, window))
		}
	}
	return problems
}

// TestPickUnusedXIDSkipsReportedValues checks that an XID counted, totalled
// or reported as the last XID on any GPU is not chosen.
func TestPickUnusedXIDSkipsReportedValues(t *testing.T) {
	scrape := parseTestScrape(t, `# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge
DCGM_EXP_XID_ERRORS_COUNT{gpu="0",xid="31"} 1
DCGM_EXP_XID_ERRORS_COUNT{gpu="1"} 0
# TYPE DCGM_EXP_XID_ERRORS_TOTAL counter
DCGM_EXP_XID_ERRORS_TOTAL{gpu="1",xid="43"} 2
# TYPE DCGM_FI_DEV_XID_ERROR gauge
DCGM_FI_DEV_XID_ERROR{gpu="1"} 48
`)
	xid, ok := pickUnusedXID(scrape, []string{"0", "1"})
	require.True(t, ok)
	require.Equal(t, int64(79), xid)

	clean := parseTestScrape(t, "# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge\nDCGM_EXP_XID_ERRORS_COUNT{gpu=\"0\"} 0\n")
	xid, ok = pickUnusedXID(clean, []string{"0"})
	require.True(t, ok)
	require.Equal(t, int64(31), xid)
}

// TestPickQuietClockEventSkipsReportedReasons checks that a reason counted or
// totalled on any GPU is not chosen, and that none is chosen when all are.
func TestPickQuietClockEventSkipsReportedReasons(t *testing.T) {
	scrape := parseTestScrape(t, `# TYPE DCGM_EXP_CLOCK_EVENTS_COUNT gauge
DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="0",clock_event="gpu_idle"} 15
DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="1",clock_event="hw_power_brake"} 1
DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="1",clock_event="hw_thermal"} 15
# TYPE DCGM_EXP_CLOCK_EVENTS_TOTAL counter
DCGM_EXP_CLOCK_EVENTS_TOTAL{gpu="0",clock_event="display_clocks"} 1
`)
	reason, ok := pickQuietClockEvent(scrape, []string{"0", "1"})
	require.True(t, ok)
	require.Equal(t, clockEventReason{label: "hw_slowdown", bit: 0x08}, reason)

	var busy strings.Builder
	busy.WriteString("# TYPE DCGM_EXP_CLOCK_EVENTS_COUNT gauge\n")
	for _, candidate := range quietClockEventReasons {
		fmt.Fprintf(&busy, "DCGM_EXP_CLOCK_EVENTS_COUNT{gpu=\"0\",clock_event=%q} 1\n", candidate.label)
	}
	_, ok = pickQuietClockEvent(parseTestScrape(t, busy.String()), []string{"0"})
	require.False(t, ok)
}

// injectedGPU1 is an XID and a clock-event reason injected into GPU 1 for the
// unit tests below, and injectedScrape a scrape that shows exactly that.
var (
	injectedGPU1   = injectedEvent{target: "1", xid: 31, reason: clockEventReason{label: "hw_power_brake", bit: 0x80}}
	injectedScrape = `# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge
DCGM_EXP_XID_ERRORS_COUNT{gpu="1",window_size_in_ms="15000",xid="31"} 1
DCGM_EXP_XID_ERRORS_COUNT{gpu="0",window_size_in_ms="15000"} 0
# TYPE DCGM_EXP_XID_ERRORS_TOTAL counter
DCGM_EXP_XID_ERRORS_TOTAL{gpu="1",xid="31"} 1
# TYPE DCGM_EXP_CLOCK_EVENTS_COUNT gauge
DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="1",clock_event="hw_power_brake",window_size_in_ms="15000"} 1
DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="0",clock_event="gpu_idle",window_size_in_ms="15000"} 15
# TYPE DCGM_EXP_CLOCK_EVENTS_TOTAL counter
DCGM_EXP_CLOCK_EVENTS_TOTAL{gpu="1",clock_event="hw_power_brake"} 1
`
)

// TestInjectedEventProblems checks the expectations after an injection into GPU 1.
func TestInjectedEventProblems(t *testing.T) {
	const lastXID = "# TYPE DCGM_FI_DEV_XID_ERROR gauge\nDCGM_FI_DEV_XID_ERROR{gpu=\"1\"} 31\n"
	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "only the target", text: injectedScrape + lastXID},
		{
			name: "last XID on another GPU",
			text: injectedScrape + lastXID + "DCGM_FI_DEV_XID_ERROR{gpu=\"0\"} 31\n",
			want: []string{"GPU 0 DCGM_FI_DEV_XID_ERROR = 31, want another value (XID injected into GPU 1 only)"},
		},
		{
			name: "last XID missing on the target",
			text: injectedScrape,
			want: []string{"GPU 1 DCGM_FI_DEV_XID_ERROR = 0 (present false), want 31 (the injected XID)"},
		},
		{
			name: "XID count and total on another GPU",
			text: strings.NewReplacer(
				`DCGM_EXP_XID_ERRORS_COUNT{gpu="0",window_size_in_ms="15000"} 0`,
				`DCGM_EXP_XID_ERRORS_COUNT{gpu="0",window_size_in_ms="15000",xid="31"} 2`,
				`DCGM_EXP_XID_ERRORS_TOTAL{gpu="1",xid="31"} 1`,
				"DCGM_EXP_XID_ERRORS_TOTAL{gpu=\"1\",xid=\"31\"} 1\nDCGM_EXP_XID_ERRORS_TOTAL{gpu=\"0\",xid=\"31\"} 3",
			).Replace(injectedScrape) + lastXID,
			want: []string{
				`GPU 0 DCGM_EXP_XID_ERRORS_COUNT{window_size_in_ms="15000",xid="31"} = 2, want 0 (injected into GPU 1 only)`,
				`GPU 0 DCGM_EXP_XID_ERRORS_TOTAL{xid="31"} = 3, want 0 (injected into GPU 1 only)`,
			},
		},
		{
			name: "clock-event count and total on another GPU",
			text: strings.NewReplacer(
				`DCGM_EXP_CLOCK_EVENTS_TOTAL{gpu="1",clock_event="hw_power_brake"} 1`,
				"DCGM_EXP_CLOCK_EVENTS_TOTAL{gpu=\"1\",clock_event=\"hw_power_brake\"} 1\n"+
					"DCGM_EXP_CLOCK_EVENTS_TOTAL{gpu=\"0\",clock_event=\"hw_power_brake\"} 1\n"+
					"DCGM_EXP_CLOCK_EVENTS_TOTAL{gpu=\"0\",clock_event=\"gpu_idle\"} 4",
				`DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="0",clock_event="gpu_idle",window_size_in_ms="15000"} 15`,
				"DCGM_EXP_CLOCK_EVENTS_COUNT{gpu=\"0\",clock_event=\"gpu_idle\",window_size_in_ms=\"15000\"} 15\n"+
					"DCGM_EXP_CLOCK_EVENTS_COUNT{gpu=\"0\",clock_event=\"hw_power_brake\",window_size_in_ms=\"15000\"} 1",
			).Replace(injectedScrape) + lastXID,
			want: []string{
				`GPU 0 DCGM_EXP_CLOCK_EVENTS_COUNT{clock_event="hw_power_brake",window_size_in_ms="15000"} = 1, want 0 (injected into GPU 1 only)`,
				`GPU 0 DCGM_EXP_CLOCK_EVENTS_TOTAL{clock_event="hw_power_brake"} = 1, want 0 (injected into GPU 1 only)`,
			},
		},
		{
			name: "count with another window",
			text: strings.Replace(injectedScrape, `{gpu="1",window_size_in_ms="15000",xid="31"}`, `{gpu="1",window_size_in_ms="300000",xid="31"}`, 1) + lastXID,
			want: []string{`GPU 1 DCGM_EXP_XID_ERRORS_COUNT{window_size_in_ms="15000",xid="31"} = 0, want > 0 (injected into this GPU)`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, injectedEventProblems(parseTestScrape(t, tc.text), []string{"0", "1"}, injectedGPU1))
		})
	}
}

// TestExpiredCountProblems checks the expectations after the count window.
func TestExpiredCountProblems(t *testing.T) {
	const xidCounts = `# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge
DCGM_EXP_XID_ERRORS_COUNT{gpu="0",window_size_in_ms="15000"} 0
DCGM_EXP_XID_ERRORS_COUNT{gpu="1",window_size_in_ms="15000"} 0
`
	const totals = `# TYPE DCGM_EXP_XID_ERRORS_TOTAL counter
DCGM_EXP_XID_ERRORS_TOTAL{gpu="1",xid="31"} 1
# TYPE DCGM_EXP_CLOCK_EVENTS_TOTAL counter
DCGM_EXP_CLOCK_EVENTS_TOTAL{gpu="1",clock_event="hw_power_brake"} 1
`
	const clockCounts = `# TYPE DCGM_EXP_CLOCK_EVENTS_COUNT gauge
DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="0",clock_event="gpu_idle",window_size_in_ms="15000"} 15
DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="1",window_size_in_ms="15000"} 0
`
	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "expired", text: xidCounts + totals + clockCounts},
		{
			name: "still counted",
			text: xidCounts + totals + strings.Replace(clockCounts,
				`DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="1",window_size_in_ms="15000"} 0`,
				`DCGM_EXP_CLOCK_EVENTS_COUNT{gpu="1",clock_event="hw_power_brake",window_size_in_ms="15000"} 1`, 1),
			want: []string{`GPU 1 DCGM_EXP_CLOCK_EVENTS_COUNT{clock_event="hw_power_brake",window_size_in_ms="15000"} = 1, want 0 after the 15s window`},
		},
		{
			name: "missing count family",
			text: xidCounts + totals,
			want: []string{"DCGM_EXP_CLOCK_EVENTS_COUNT is missing after the 15s window, want a sample for every GPU"},
		},
		{
			name: "GPU without a count sample",
			text: strings.Replace(xidCounts, "DCGM_EXP_XID_ERRORS_COUNT{gpu=\"0\",window_size_in_ms=\"15000\"} 0\n", "", 1) + totals + clockCounts,
			want: []string{"GPU 0 has no DCGM_EXP_XID_ERRORS_COUNT sample after the 15s window"},
		},
		{
			name: "total dropped",
			text: xidCounts + strings.Replace(totals, `DCGM_EXP_XID_ERRORS_TOTAL{gpu="1",xid="31"} 1`, `DCGM_EXP_XID_ERRORS_TOTAL{gpu="1",xid="43"} 1`, 1) + clockCounts,
			want: []string{`GPU 1 DCGM_EXP_XID_ERRORS_TOTAL{xid="31"} = 0, want > 0 (a total never drops)`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, expiredCountProblems(parseTestScrape(t, tc.text), []string{"0", "1"}, injectedGPU1))
		})
	}
}
