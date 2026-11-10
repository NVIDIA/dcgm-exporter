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
	_, metricsResp := startFastCSVExporterAndWait(
		t,
		fmt.Sprintf("http://[::1]:%d/metrics", port),
		defaultCollectorsFile(t),
		"--address", fmt.Sprintf("[::1]:%d", port),
	)

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

// runListenAddressEffect runs the exporter on the all-interfaces address
// (":port") given as the flag and as the output listen key, and checks each
// run serves the baseline families and matches the other form.
func runListenAddressEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{name: "--address", args: listenFlagArgs(":%d"), check: checkSameDCGMShapeAsBaseline},
		{name: "outputs.prometheus.listen", args: listenKeyArgs(":%d"), check: checkSameDCGMShapeAsBaseline},
	})
}

// runIPv6ListenAddressEffect is runListenAddressEffect for the IPv6 loopback
// address; it skips when the host cannot use that address.
func runIPv6ListenAddressEffect(t testing.TB) {
	base := startSettingBaseline(t)
	runSettingForms(t, base, []settingForm{
		{name: "--address [::1]", args: listenFlagArgs("[::1]:%d"), check: checkSameDCGMShapeAsBaseline, target: ipv6Target, ipv6: true},
		{name: "outputs.prometheus.listen [::1]", args: listenKeyArgs("[::1]:%d"), check: checkSameDCGMShapeAsBaseline, target: ipv6Target, ipv6: true},
	})
}

// ipv6Target is the plain HTTP metrics URL on the IPv6 loopback address.
func ipv6Target(port int) scrapeTarget {
	return scrapeTarget{url: fmt.Sprintf("http://[::1]:%d/metrics", port)}
}

// listenFlagArgs returns the arguments of a baseline run whose --address
// is addressFormat filled with the port.
func listenFlagArgs(addressFormat string) func(testing.TB, *settingBaseline, int) []string {
	return func(_ testing.TB, base *settingBaseline, port int) []string {
		return baselineArgsAt(base.collectors, fmt.Sprintf(addressFormat, port))
	}
}

// listenKeyArgs returns the arguments of a baseline run that has no --address
// and sets outputs.prometheus.listen to addressFormat filled with the port.
func listenKeyArgs(addressFormat string) func(testing.TB, *settingBaseline, int) []string {
	return func(t testing.TB, base *settingBaseline, port int) []string {
		return []string{
			"--collectors", base.collectors,
			"--collect-interval", "1000",
			"--config-file", writeSettingConfig(t, map[string]any{
				"version": 2,
				"outputs": map[string]any{"prometheus": prometheusOutput(fmt.Sprintf(addressFormat, port))},
			}),
		}
	}
}
