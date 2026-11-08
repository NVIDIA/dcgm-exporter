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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// shortReadTimeout is the read timeout the cases set. It is far below the
	// exporter's default of 10s, so a connection that closes in time shows the
	// setting took effect.
	shortReadTimeout = 2 * time.Second
	// readTimeoutMargin is the time past the read timeout the exporter gets to
	// close a stalled connection, which covers scheduling delay.
	readTimeoutMargin = 3 * time.Second
	// defaultReadHoldWait is how long a stalled connection must stay open with
	// the default read timeout. It is at least shortReadTimeout plus the margin
	// and below the 10s default.
	defaultReadHoldWait = 5 * time.Second
	// partialRequest is the start of a request line the client never finishes.
	partialRequest = "GET /metr"
)

const (
	// shortWriteTimeout is shorter than gathering DCGM values usually takes.
	// The server's write deadline starts when it has read the request, so it
	// has often passed before the handler writes the response and the scrape
	// fails. A fast host can still finish some scrapes in time.
	// It is not smaller because the exporter also uses the write timeout as
	// its shutdown deadline, and a nanosecond leaves shutdown no time to
	// finish cleanly.
	shortWriteTimeout = "1ms"
	// failedWriteExtraScrapes is how long the run keeps scraping after the
	// failed write is first logged.
	failedWriteExtraScrapes = 5 * time.Second
	// failedWriteLogText is the message the exporter logs when it cannot write
	// a metrics response.
	failedWriteLogText = "Failed to write response."
)

// runReadTimeoutEffect runs the exporter with the default read timeout and
// with a short one, as the flag and as the config key, and checks the short
// ones close a stalled connection, the default does not, and every run serves
// the baseline families.
func runReadTimeoutEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{
			name: "default read timeout",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return baselineArgs(base.collectors, port)
			},
			check: checkStalledConnectionStaysOpen,
		},
		{
			name: "--web-read-timeout",
			args: func(_ testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--web-read-timeout", shortReadTimeout.String())
			},
			check: checkStalledConnectionCloses,
		},
		{
			name: "server.read_timeout",
			args: func(t testing.TB, base *settingBaseline, port int) []string {
				return append(baselineArgs(base.collectors, port), "--config-file", writeSettingConfig(t, map[string]any{
					"version": 2,
					"server":  map[string]any{"read_timeout": shortReadTimeout.String()},
				}))
			},
			check: checkStalledConnectionCloses,
		},
	})
}

// checkStalledConnectionCloses checks the run has the baseline families and the exporter closes a connection that
// stalls inside a request line within the short read timeout plus the margin.
func checkStalledConnectionCloses(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	checkSameDCGMShapeAsBaseline(t, base, run)

	wait := shortReadTimeout + readTimeoutMargin
	closed, err := connectionClosedWithin(targetAddress(t, run.target), partialRequest, wait)
	require.NoError(t, err)
	require.Truef(t, closed, "the exporter kept a stalled connection open for %s with a read timeout of %s", wait, shortReadTimeout)
}

// checkStalledConnectionStaysOpen checks the run has the baseline families and the exporter still holds a stalled
// connection after defaultReadHoldWait, which the short read timeout would
// have closed.
func checkStalledConnectionStaysOpen(t testing.TB, base *settingBaseline, run settingRun) {
	t.Helper()
	checkSameDCGMShapeAsBaseline(t, base, run)

	closed, err := connectionClosedWithin(targetAddress(t, run.target), partialRequest, defaultReadHoldWait)
	require.NoError(t, err)
	require.Falsef(t, closed, "the exporter closed a stalled connection within %s with the default read timeout", defaultReadHoldWait)
}

// targetAddress returns the host and port of target's URL.
func targetAddress(t testing.TB, target scrapeTarget) string {
	t.Helper()
	parsed, err := url.Parse(target.url)
	require.NoError(t, err)
	return parsed.Host
}

// connectionClosedWithin opens a connection to address, sends request and
// nothing more, and reports whether the server closes the connection within
// wait. A server that answers and then closes also counts as closed. A server
// that keeps the connection open for the whole wait gives false.
func connectionClosedWithin(address, request string, wait time.Duration) (bool, error) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return false, fmt.Errorf("connect to %s: %w", address, err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(request)); err != nil {
		return false, fmt.Errorf("send the partial request to %s: %w", address, err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return false, fmt.Errorf("set the read deadline: %w", err)
	}

	buf := make([]byte, 512)
	for {
		_, err := conn.Read(buf)
		if err == nil {
			continue
		}

		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return false, nil
		}
		return true, nil
	}
}

// runWriteTimeoutEffect starts a baseline and checks the write timeout against
// its counter file; see runWriteTimeout.
func runWriteTimeoutEffect(t testing.TB) {
	runWriteTimeout(t, startSettingBaseline(t))
}

// runWriteTimeout checks the write timeout against the baseline's counter
// file. With the default, the scrape settles and the exporter logs no failed
// write. With a 1ms timeout, by flag and by key, the exporter logs a failed
// write and a scrape fails. How many scrapes finish within 1ms depends on the
// host, so the check needs one failed scrape, not most of them. These runs
// have no settled scrape to compare.
func runWriteTimeout(t testing.TB, base *settingBaseline) {
	port := getRandomAvailablePort(t)
	process := startExporterProcess(t, baselineArgs(base.collectors, port)...)
	waitForSettledScrape(t, plainTarget(port), process)
	require.Empty(t, defaultWriteTimeoutProblems(process.output.String()), "default write timeout")
	process.terminate(t)

	shortForms := []struct {
		name string
		args func(port int) []string
	}{
		{"--web-write-timeout", func(port int) []string {
			return append(baselineArgs(base.collectors, port), "--web-write-timeout", shortWriteTimeout)
		}},
		{"server.write_timeout", func(port int) []string {
			return append(baselineArgs(base.collectors, port), "--config-file", writeSettingConfig(t, map[string]any{
				"version": 2,
				"server":  map[string]any{"write_timeout": shortWriteTimeout},
			}))
		}},
	}
	for _, form := range shortForms {
		port := getRandomAvailablePort(t)
		process := startExporterProcess(t, form.args(port)...)
		served, failed := scrapeUntilFailedWrite(t, port, process)
		require.Emptyf(t, shortWriteTimeoutProblems(served, failed, process.output.String()), "%s of %s\n%s",
			form.name, shortWriteTimeout, process.output.String())
		process.terminate(t)
	}
}

// scrapeUntilFailedWrite scrapes the exporter until it has logged a failed
// write and then for failedWriteExtraScrapes more, or until the startup wait
// ends. It returns how many scrapes after the first logged failure served a
// DCGM family and how many did not; earlier scrapes can fail only because the
// exporter is still starting. Each scrape uses a new connection: on a reused
// connection the HTTP client silently retries a GET whose response never
// arrived, which hides the failed write.
func scrapeUntilFailedWrite(t testing.TB, port int, process *hostExporterProcess) (int, int) {
	t.Helper()
	metricsURL := plainTarget(port).url
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	defer client.CloseIdleConnections()

	deadline := time.Now().Add(startupWaitTimeout)
	served, failed := 0, 0
	var loggedAt time.Time
	for time.Now().Before(deadline) {
		require.NoErrorf(t, process.running(), "the exporter stopped\n%s", process.output.String())

		req, err := http.NewRequest(http.MethodGet, metricsURL, nil)
		require.NoError(t, err)
		body, status, err := fetchWith(client, req)
		ok := err == nil && status == http.StatusOK && scrapeHasDCGMFamily(body)
		switch {
		case loggedAt.IsZero() && strings.Contains(process.output.String(), failedWriteLogText):
			loggedAt = time.Now()
		case loggedAt.IsZero():
			// The exporter may still be starting, so this scrape proves nothing.
		case ok:
			served++
		default:
			failed++
		}

		if !loggedAt.IsZero() && time.Since(loggedAt) >= failedWriteExtraScrapes {
			break
		}
		time.Sleep(startupPollInterval)
	}
	return served, failed
}

// scrapeHasDCGMFamily reports whether body parses and holds a DCGM family.
func scrapeHasDCGMFamily(body string) bool {
	families, err := parseMetricFamilies(body)
	return err == nil && len(familiesShape(families)) > 0
}

// defaultWriteTimeoutProblems lists the problems of a run with the default
// write timeout whose scrape settled: any failed write in its log.
func defaultWriteTimeoutProblems(log string) []string {
	if strings.Contains(log, failedWriteLogText) {
		return []string{fmt.Sprintf("the log holds %q", failedWriteLogText)}
	}
	return nil
}

// shortWriteTimeoutProblems lists how a run with a write timeout that scrapes
// often miss differs from the expected: the log must hold the failed write,
// and a scrape after it must fail. Served scrapes are allowed because a fast
// host can finish a scrape within the timeout; served only appears in the
// message.
func shortWriteTimeoutProblems(served, failed int, log string) []string {
	var problems []string
	if failed == 0 {
		problems = append(problems, fmt.Sprintf("%d scrapes were served and none failed", served))
	}

	if !strings.Contains(log, failedWriteLogText) {
		problems = append(problems, fmt.Sprintf("the log does not hold %q", failedWriteLogText))
	}
	return problems
}

// TestWriteTimeoutProblems checks each way a run can miss the write timeout's
// effect, in both directions.
func TestWriteTimeoutProblems(t *testing.T) {
	logged := "time=x level=ERROR msg=\"" + failedWriteLogText + "\" error=timeout\n"

	assert.Empty(t, shortWriteTimeoutProblems(0, 3, logged), "short timeout fails and logs")
	assert.Empty(t, shortWriteTimeoutProblems(1, 3, logged), "one fast scrape is served")
	assert.Empty(t, shortWriteTimeoutProblems(6, 4, logged), "most scrapes are served on a fast host")
	assert.Len(t, shortWriteTimeoutProblems(5, 0, logged), 1, "logged once but no scrape failed after it")
	assert.Len(t, shortWriteTimeoutProblems(3, 0, "time=x level=INFO msg=ok\n"), 2, "short timeout had no effect")
	assert.Len(t, shortWriteTimeoutProblems(0, 3, ""), 1, "short timeout failed without the log")
	assert.Empty(t, defaultWriteTimeoutProblems(""), "default stays quiet")
	assert.Len(t, defaultWriteTimeoutProblems(logged), 1, "default logged a failed write")
}

// TestConnectionClosedWithin checks the helper tells a server that closes a
// stalled connection from one that holds it open.
func TestConnectionClosedWithin(t *testing.T) {
	tests := []struct {
		name       string
		holdFor    time.Duration
		wait       time.Duration
		wantClosed bool
	}{
		{"server closes before the wait ends", 50 * time.Millisecond, 2 * time.Second, true},
		{"server holds past the wait", time.Second, 100 * time.Millisecond, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()

			done := make(chan struct{})
			defer func() { <-done }()
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}

				time.Sleep(tt.holdFor)
				conn.Close()
			}()

			closed, err := connectionClosedWithin(listener.Addr().String(), partialRequest, tt.wait)
			require.NoError(t, err)
			assert.Equal(t, tt.wantClosed, closed)
		})
	}
}

// TestConnectionClosedWithinRefused checks an unreachable address is an error,
// not a closed connection.
func TestConnectionClosedWithinRefused(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	_, err = connectionClosedWithin(address, partialRequest, time.Second)
	require.Error(t, err)
}
