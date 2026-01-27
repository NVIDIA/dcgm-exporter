/*
 * Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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
	"flag"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/goleak"

	"github.com/NVIDIA/dcgm-exporter/pkg/cmd"
)

// createTestCLIContext creates a CLI context with the given arguments for testing
func createTestCLIContext(t *testing.T, collectors string, address string) *cli.Context {
	t.Helper()

	app := cmd.NewApp()

	// Create a flagset with all the flags from the app
	set := flag.NewFlagSet("test", 0)
	for _, f := range app.Flags {
		switch flag := f.(type) {
		case *cli.StringFlag:
			set.String(flag.Name, flag.Value, flag.Usage)
			// Also add aliases
			for _, alias := range flag.Aliases {
				set.String(alias, flag.Value, flag.Usage)
			}
		case *cli.BoolFlag:
			set.Bool(flag.Name, flag.Value, flag.Usage)
			for _, alias := range flag.Aliases {
				set.Bool(alias, flag.Value, flag.Usage)
			}
		case *cli.IntFlag:
			set.Int(flag.Name, flag.Value, flag.Usage)
			for _, alias := range flag.Aliases {
				set.Int(alias, flag.Value, flag.Usage)
			}
		}
	}

	// Set the specific values we care about
	require.NoError(t, set.Set("collectors", collectors))
	require.NoError(t, set.Set("address", address))

	return cli.NewContext(app, set, nil)
}

// TestMultipleSIGHUPReloads verifies that the exporter can handle multiple SIGHUP signals
// and continues functioning correctly after each reload
func TestMultipleSIGHUPReloads(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	)

	// Create test signal source for programmatic signal injection
	testSigs := cmd.NewTestSignalSource()

	port := getRandomAvailablePort(t)

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

	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)

	// Wait for app to start
	// DCGM initialization can take a long time (30+ seconds) on CI systems
	// as it initializes GPU, NvSwitch, NvLink, CPU, and CPU Core entities
	t.Log("Waiting for exporter to start...")
	require.Eventually(t, func() bool {
		resp, _, err := httpGet(t, metricsURL)
		return err == nil && len(resp) > 0
	}, 60*time.Second, 500*time.Millisecond, "Exporter should start and return metrics")

	// Now we can programmatically trigger reloads!
	const numReloads = 5
	var parser expfmt.TextParser
	for i := 0; i < numReloads; i++ {
		t.Logf("Reload iteration %d/%d", i+1, numReloads)

		// Verify metrics endpoint is accessible before reload
		resp, statusCode, err := httpGet(t, metricsURL)
		require.NoError(t, err, "Metrics endpoint should be accessible before reload")
		require.Equal(t, 200, statusCode)
		require.NotEmpty(t, resp, "Should return metrics before reload")

		// Parse metrics to verify they're valid
		mf, err := parser.TextToMetricFamilies(strings.NewReader(resp))
		require.NoError(t, err, "Should parse metrics before reload")
		require.Greater(t, len(mf), 0, "Should have metrics before reload")

		// Send SIGHUP to trigger reload programmatically
		t.Log("Triggering reload...")
		testSigs.SendSignal(syscall.SIGHUP)

		// Wait for server to restart (race detector slows things down)
		var reloadedResp string
		require.Eventually(t, func() bool {
			r, _, e := httpGet(t, metricsURL)
			if e == nil && len(r) > 0 {
				reloadedResp = r
				return true
			}
			return false
		}, 30*time.Second, 500*time.Millisecond, "Metrics endpoint should be accessible after reload %d", i+1)

		// Parse metrics to verify they're still valid
		mf, err = parser.TextToMetricFamilies(strings.NewReader(reloadedResp))
		require.NoError(t, err, "Should parse metrics after reload")
		require.Greater(t, len(mf), 0, "Should have metrics after reload")

		// Wait for server to stabilize before next iteration (race detector needs more time)
		time.Sleep(2 * time.Second)
	}

	t.Logf("Successfully completed %d reload cycles", numReloads)

	// Note: cleanup is handled by deferred function
	// Give goroutines time to fully cleanup
	runtime.GC()
	time.Sleep(500 * time.Millisecond)
}

// TestGoroutineLeakOnReload verifies that goroutines don't leak during SIGHUP reloads
func TestGoroutineLeakOnReload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	)

	goroutinesBefore := runtime.NumGoroutine()
	t.Logf("Goroutines before starting app: %d", goroutinesBefore)

	testSigs := cmd.NewTestSignalSource()

	port := getRandomAvailablePort(t)

	cliCtx := createTestCLIContext(t, "./testdata/default-counters.csv", fmt.Sprintf(":%d", port))

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

	metricsURL := fmt.Sprintf("http://localhost:%d/metrics", port)

	// DCGM initialization can take a long time (30+ seconds) on CI systems
	// as it initializes GPU, NvSwitch, NvLink, CPU, and CPU Core entities
	require.Eventually(t, func() bool {
		resp, _, err := httpGet(t, metricsURL)
		return err == nil && len(resp) > 0
	}, 60*time.Second, 500*time.Millisecond, "Exporter should start and return metrics")

	goroutinesAfterStart := runtime.NumGoroutine()
	t.Logf("Goroutines after starting app: %d", goroutinesAfterStart)

	// Perform several reloads
	const numReloads = 3
	for i := 0; i < numReloads; i++ {
		t.Logf("Reload iteration %d", i+1)
		testSigs.SendSignal(syscall.SIGHUP)

		// Wait for server to restart
		require.Eventually(t, func() bool {
			r, _, e := httpGet(t, metricsURL)
			return e == nil && len(r) > 0
		}, 30*time.Second, 500*time.Millisecond, "Metrics should be accessible after reload %d", i+1)

		goroutinesAfterReload := runtime.NumGoroutine()
		t.Logf("Goroutines after reload %d: %d", i+1, goroutinesAfterReload)
	}

	// Note: cleanup is handled by deferred function
	// Force GC and wait for goroutines to cleanup
	runtime.GC()
	time.Sleep(500 * time.Millisecond)

	goroutinesAfterShutdown := runtime.NumGoroutine()
	t.Logf("Goroutines after shutdown: %d", goroutinesAfterShutdown)

	// Allow some tolerance (goleak will catch actual leaks)
	const maxGoroutineGrowth = 15
	growth := goroutinesAfterShutdown - goroutinesBefore
	assert.LessOrEqual(t, growth, maxGoroutineGrowth,
		"Goroutine count should not grow significantly. Growth: %d", growth)
}
