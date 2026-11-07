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
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// scrapeCapacityExceededMessage is the body the exporter sends with a 503 when
// more scrapes arrive than --max-concurrent-scrapes admits. It repeats the
// product text because host tests do not import exporter packages.
const scrapeCapacityExceededMessage = "scrape concurrency limit reached"

const (
	// scrapeBurstSize is how many scrapes one burst sends at the same moment.
	scrapeBurstSize = 20
	// scrapeBurstAttempts is how many bursts may run before one is refused.
	scrapeBurstAttempts = 20
)

// runScrapeLimitEffect runs the exporter with a scrape limit of one, as the
// flag and as the config key, and checks each run refuses overlapping scrapes
// with 503 and still serves the baseline families.
func runScrapeLimitEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{
			name: "--max-concurrent-scrapes",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--max-concurrent-scrapes", "1")
			},
			check: checkOverlappingScrapesRefused,
		},
		{
			name: "server.max_concurrent_scrapes",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--config-file", writeSettingConfig(t, map[string]any{
					"version": 2,
					"server":  map[string]any{"max_concurrent_scrapes": 1},
				}))
			},
			check: checkOverlappingScrapesRefused,
		},
	})
}

// checkOverlappingScrapesRefused sends bursts of simultaneous scrapes until
// one is refused with 503 and the capacity message. Every response must be a
// 200 or that 503. The DCGM families, before the bursts and in one scrape after
// them, must match the baseline.
func checkOverlappingScrapesRefused(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	refused := false
	for attempt := 0; attempt < scrapeBurstAttempts && !refused; attempt++ {
		for _, result := range scrapeBurst(run.target.url, scrapeBurstSize) {
			require.NoError(t, result.err, "scrape during a burst")

			switch {
			case result.status == http.StatusOK:
			case result.status == http.StatusServiceUnavailable && strings.TrimSpace(result.body) == scrapeCapacityExceededMessage:
				refused = true
			default:
				require.Failf(t, "unexpected scrape response", "status %d, body %q", result.status, result.body)
			}
		}
	}
	require.Truef(t, refused, "no scrape of %d bursts of %d was refused with 503 %q",
		scrapeBurstAttempts, scrapeBurstSize, scrapeCapacityExceededMessage)

	checkSameDCGMShapeAsBaseline(t, base, run)

	// A scrape after the bursts shows the limit left the exporter's output unchanged.
	body, status, err := run.target.fetch(t)()
	require.NoError(t, err, "scrape after the bursts")
	require.Equal(t, http.StatusOK, status, "scrape after the bursts")
	after, err := parseMetricFamilies(body)
	require.NoError(t, err, "parse the scrape after the bursts")
	checkSameDCGMShapeAsBaseline(t, base, settingRun{families: after})
}

// burstResult is the outcome of one scrape of a burst.
type burstResult struct {
	body   string
	status int
	err    error
}

// scrapeBurst sends size scrapes of url at the same moment and returns every
// outcome. Each scrape waits behind one barrier so they overlap.
func scrapeBurst(url string, size int) []burstResult {
	results := make([]burstResult, size)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			body, status, err := fetchRaw(url)
			results[i] = burstResult{body: body, status: status, err: err}
		}()
	}

	close(release)
	wg.Wait()
	return results
}
