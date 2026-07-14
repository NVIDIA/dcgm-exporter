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
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// runIPv6ListenAndReadMetrics starts the exporter on IPv6 loopback and verifies metrics scrape.
func runIPv6ListenAndReadMetrics(t testing.TB) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	port := getRandomAvailableIPv6Port(t)
	_, metricsResp := startExporterAndWait(
		t,
		fmt.Sprintf("http://[::1]:%d/metrics", port),
		"--collectors", "./testdata/default-counters.csv",
		"--address", fmt.Sprintf("[::1]:%d", port),
	)

	require.Contains(t, metricsResp, "DCGM_FI_DEV_", "expected DCGM metrics from IPv6 listener")
	validateHostDefaultMetrics(t, metricsResp)
}

// getRandomAvailableIPv6Port reserves a unique IPv6 loopback TCP port for host tests.
func getRandomAvailableIPv6Port(t testing.TB) int {
	randomPortMutex.Lock()
	defer randomPortMutex.Unlock()
	t.Helper()

	for {
		addr, err := net.ResolveTCPAddr("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("IPv6 loopback is unavailable: %v", err)
		}
		listener, err := net.ListenTCP("tcp6", addr)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "cannot assign requested address") ||
				strings.Contains(strings.ToLower(err.Error()), "address family not supported") {
				t.Skipf("IPv6 loopback is unavailable: %v", err)
			}
			require.NoError(t, err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		require.NoError(t, listener.Close())
		if _, exist := usedPorts[port]; exist {
			continue
		}
		usedPorts[port] = struct{}{}
		return port
	}
}
