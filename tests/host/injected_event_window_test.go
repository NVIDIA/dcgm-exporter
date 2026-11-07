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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// overrideWindow is the XID count window the config file's override sets.
	// It is far below the exporter's default of five minutes, so a count that
	// expires on this schedule shows the override took effect.
	overrideWindow = 10 * time.Second
	// overrideExpiryWait is how long past the override window the scenario
	// waits for the count to drop to zero, which allows for the collection
	// interval, the scrape interval and a slow host.
	overrideExpiryWait = 15 * time.Second
	// overrideLateMargin is how long before the override window ends the
	// scenario checks the count is still positive, which leaves room for one
	// collection interval.
	overrideLateMargin = 3 * time.Second
)

// runInjectedEventOverrideWindow injects an XID into one real GPU through a
// private hostengine and checks that the window a metrics override sets, not
// the exporter's default window, decides how long the XID is counted: the
// count carries the override's window in window_size_in_ms and returns to zero
// within the window plus overrideExpiryWait. The run sets no
// --xid-count-window-size, because an explicit flag takes precedence over the
// file's override.
func runInjectedEventOverrideWindow(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	t.Helper()
	engine := startUnixHostEngine(t)
	port := getRandomAvailablePort(t)
	metricsURL := fmt.Sprintf("http://127.0.0.1:%d/metrics", port)
	process, _ := startExporterAndWaitForFamilies(
		t,
		metricsURL,
		nil,
		[]string{xidCountFamily},
		"--config-file", writeSettingConfig(t, overrideWindowConfig(port)),
		"--remote-hostengine-info", engine.uri(),
	)

	before := scrapeGPUMetrics(t, metricsURL, process)
	gpus := before.gpus(xidCountFamily)
	require.NotEmpty(t, gpus, "%s has no GPU samples", xidCountFamily)

	// Inject into the highest GPU ID, so a GPU ID mix-up with GPU 0 shows.
	target := gpus[len(gpus)-1]
	xid, ok := pickUnusedXID(before, gpus)
	require.Truef(t, ok, "every candidate XID %v is already reported by a GPU", injectedXIDCandidates)
	t.Logf("injecting XID %d into GPU %s of GPUs %v", xid, target, gpus)

	injectEvent(t, engine, target, fieldXIDErrors, xid)
	injected := time.Now()

	// The count appears with the override's window, well inside that window.
	waitForXIDCount(t, metricsURL, process, injected.Add(overrideWindow/2), func(scrape gpuScrape) []string {
		return xidCountedProblems(scrape, target, xid, overrideWindow)
	})

	// Near the end of the window it is still counted, so an early expiry fails.
	time.Sleep(time.Until(injected.Add(overrideWindow - overrideLateMargin)))
	waitForXIDCount(t, metricsURL, process, time.Now(), func(scrape gpuScrape) []string {
		return xidCountedProblems(scrape, target, xid, overrideWindow)
	})

	// Past the override window, and long before the default one, it is gone.
	waitForXIDCount(t, metricsURL, process, injected.Add(overrideWindow+overrideExpiryWait), func(scrape gpuScrape) []string {
		return expiredFamilyProblems(scrape, gpus, xidCountFamily, map[string]string{"xid": strconv.FormatInt(xid, 10)}, overrideWindow)
	})
}

// overrideWindowConfig is a version 2 document that collects the XID count,
// total and last-XID fields every second and sets overrideWindow on the count
// with a metrics override, with a Prometheus output on port.
func overrideWindowConfig(port int) map[string]any {
	field := func(name, promType, help string) map[string]any {
		return map[string]any{"name": name, "prometheus_type": promType, "help": help}
	}

	return map[string]any{
		"version": 2,
		"metrics": []any{map[string]any{
			"name":  "gpu",
			"every": "1s",
			"fields": []any{
				field(xidErrorFamily, "gauge", "Value of the last XID error encountered."),
				field(xidCountFamily, "gauge", "XID errors observed during the configured time window."),
				field(xidTotalFamily, "counter", "Total XID errors observed since the counter was last reset."),
			},
			"overrides": []any{map[string]any{
				"name":   "xid-window",
				"match":  []string{xidCountFamily},
				"window": overrideWindow.String(),
			}},
			"enrichment": map[string]any{"kubernetes": false, "container": false, "hpc_jobs": false},
		}},
		"outputs": map[string]any{"prometheus": prometheusOutput(fmt.Sprintf(":%d", port))},
	}
}

// waitForXIDCount scrapes until problems finds nothing wrong with a scrape, or
// fails at deadline with the problems of the last scrape.
func waitForXIDCount(
	t testing.TB, metricsURL string, process *hostExporterProcess, deadline time.Time, problems func(gpuScrape) []string,
) {
	t.Helper()
	for {
		found := problems(scrapeGPUMetrics(t, metricsURL, process))
		if len(found) == 0 {
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("the XID count did not follow the %s override window:\n%s", overrideWindow, strings.Join(found, "\n"))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// xidCountedProblems lists how scrape differs from the XID count after an
// injection into target: the target's count of xid must be positive and carry
// window as window_size_in_ms.
func xidCountedProblems(scrape gpuScrape, target string, xid int64, window time.Duration) []string {
	match := map[string]string{"xid": strconv.FormatInt(xid, 10), "window_size_in_ms": strconv.FormatInt(window.Milliseconds(), 10)}
	if got := scrape.sum(xidCountFamily, target, match); got <= 0 {
		return []string{fmt.Sprintf("GPU %s %s = %g, want > 0", target, seriesSelector(xidCountFamily, match), got)}
	}
	return nil
}

// xidCountExposition builds scrape text for the XID count family from one line
// per sample: each is a GPU, a window in milliseconds, an XID ("" for none) and
// a value.
func xidCountExposition(samples ...[4]string) string {
	var text strings.Builder
	text.WriteString("# TYPE DCGM_EXP_XID_ERRORS_COUNT gauge\n")
	for _, sample := range samples {
		labels := fmt.Sprintf("gpu=%q,window_size_in_ms=%q", sample[0], sample[1])
		if sample[2] != "" {
			labels += fmt.Sprintf(",xid=%q", sample[2])
		}
		fmt.Fprintf(&text, "DCGM_EXP_XID_ERRORS_COUNT{%s} %s\n", labels, sample[3])
	}
	return text.String()
}

// TestXIDCountWindowChecks checks the counted and expired comparisons fail when
// the override has no effect, as a count labelled with another window or one
// that does not expire, and pass when the count follows the window.
func TestXIDCountWindowChecks(t *testing.T) {
	const window, defaultWindow = "10000", "300000"
	counted := xidCountExposition([4]string{"1", window, "31", "1"}, [4]string{"0", window, "", "0"})
	defaultWindowCounted := xidCountExposition([4]string{"1", defaultWindow, "31", "1"}, [4]string{"0", defaultWindow, "", "0"})
	expired := xidCountExposition([4]string{"1", window, "", "0"}, [4]string{"0", window, "", "0"})
	oneGPU := xidCountExposition([4]string{"0", window, "", "0"})

	countedTests := []struct {
		name string
		text string
		want int
	}{
		{"counted with the override window", counted, 0},
		{"counted with the default window", defaultWindowCounted, 1},
		{"not counted yet", expired, 1},
	}
	for _, tt := range countedTests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, xidCountedProblems(parseTestScrape(t, tt.text), "1", 31, overrideWindow), tt.want)
		})
	}

	expiredTests := []struct {
		name string
		text string
		want int
	}{
		{"expired", expired, 0},
		{"still counted after the override window", counted, 1},
		{"target GPU missing", oneGPU, 1},
	}
	match := map[string]string{"xid": "31"}
	for _, tt := range expiredTests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, expiredFamilyProblems(parseTestScrape(t, tt.text), []string{"0", "1"}, xidCountFamily, match, overrideWindow), tt.want)
		})
	}
}
