/*
 * Copyright (c) 2023, NVIDIA CORPORATION.  All rights reserved.
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

package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/dcgm-exporter/pkg/cmd"
)

func TestStartAndReadMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	port := getRandomAvailablePort(t)

	// Create test signal source for proper cleanup
	testSigs := cmd.NewTestSignalSource()

	// Create CLI context
	cliCtx := createTestCLIContext(t, "./testdata/default-counters.csv", fmt.Sprintf(":%d", port))

	// Run exporter with test signal source in goroutine
	appDone := make(chan error, 1)
	go func() {
		err := cmd.StartDCGMExporterWithSignalSource(cliCtx, testSigs)
		appDone <- err
	}()

	// Ensure cleanup happens even if test fails
	defer func() {
		t.Log("Sending termination signal for cleanup...")
		testSigs.SendSignal(syscall.SIGTERM)
		select {
		case <-appDone:
			t.Log("App shutdown completed")
		case <-time.After(10 * time.Second):
			t.Log("Warning: App did not shutdown within timeout")
		}
	}()

	t.Logf("Read metrics from http://localhost:%d/metrics", port)

	metricsResp, _ := retry.DoWithData(
		func() (string, error) {
			metricsResp, _, err := httpGet(t, fmt.Sprintf("http://localhost:%d/metrics", port))
			if err != nil {
				return "", err
			}

			if len(metricsResp) == 0 {
				return "", errors.New("empty response")
			}
			return metricsResp, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)

	require.NotEmpty(t, metricsResp)
	parser := expfmt.NewTextParser(model.UTF8Validation)
	mf, err := parser.TextToMetricFamilies(strings.NewReader(metricsResp))
	require.NoError(t, err)
	require.Greater(t, len(mf), 0, "expected number of metrics more than 0")
}

// TestShutdownViaContextCancel verifies that cancelling the root
// cli.Context shuts the daemon down within the same budget as a SIGTERM.
// This is the ctx-driven sibling of TestStartAndReadMetrics; together
// they prove that both shutdown paths (signal channel and ctx cancel)
// reach the same place. See docs/CONTEXTS.md.
func TestShutdownViaContextCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	port := getRandomAvailablePort(t)

	// SignalSource present but unused — we drive shutdown via ctx cancel.
	// Still need it as a defer-cleanup safety net in case the test fails
	// before the cancel and we want to make sure the daemon goroutine ends.
	testSigs := cmd.NewTestSignalSource()

	cliCtx := createTestCLIContext(t, "./testdata/default-counters.csv", fmt.Sprintf(":%d", port))

	// Override cli.Context.Context with a cancellable ctx so the test
	// can simulate signal.NotifyContext firing from main.go.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cliCtx.Context = ctx

	appDone := make(chan error, 1)
	go func() {
		appDone <- cmd.StartDCGMExporterWithSignalSource(cliCtx, testSigs)
	}()

	// Belt-and-suspenders: SIGTERM if ctx-cancel didn't shut things down.
	defer func() {
		select {
		case <-appDone:
			// already shut down
		default:
			t.Log("daemon still running after test body; sending SIGTERM as cleanup")
			testSigs.SendSignal(syscall.SIGTERM)
			select {
			case <-appDone:
			case <-time.After(10 * time.Second):
				t.Log("warning: app did not shut down within timeout")
			}
		}
	}()

	// Wait until /metrics is responsive (proves the daemon started).
	t.Logf("Waiting for metrics endpoint on http://localhost:%d/metrics", port)
	metricsResp, _ := retry.DoWithData(
		func() (string, error) {
			resp, _, err := httpGet(t, fmt.Sprintf("http://localhost:%d/metrics", port))
			if err != nil {
				return "", err
			}
			if len(resp) == 0 {
				return "", errors.New("empty response")
			}
			return resp, nil
		},
		retry.Attempts(10),
		retry.MaxDelay(10*time.Second),
	)
	require.NotEmpty(t, metricsResp, "daemon never produced metrics")

	// Drive shutdown by cancelling the root ctx.
	t.Log("Cancelling root ctx to drive shutdown")
	start := time.Now()
	cancel()

	select {
	case err := <-appDone:
		elapsed := time.Since(start)
		t.Logf("Daemon exited after ctx cancel in %v (err=%v)", elapsed, err)
		require.Less(t, elapsed, 10*time.Second,
			"ctx-cancel shutdown took too long; either ctx is not threaded through "+
				"to the shutdown loop, or some subsystem is hung")
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not shut down within 10s of ctx cancellation")
	}
}
