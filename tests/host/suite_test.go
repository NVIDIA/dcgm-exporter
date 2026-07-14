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
	"os"
	"testing"

	"github.com/NVIDIA/dcgm-exporter/tests/internal/resultmarker"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// TestMain runs the systemd socket helper process when requested, otherwise it runs the host suite.
func TestMain(m *testing.M) {
	if os.Getenv("HOST_SYSTEMD_SOCKET_HELPER") == "1" {
		os.Exit(runSystemdSocketHelper())
	}
	os.Exit(m.Run())
}

// TestHostIntegration runs the Ginkgo suite for direct host exporter validation.
func TestHostIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Host Integration Suite")
}

var _ = resultmarker.RegisterSuite(
	func() bool { return resultmarker.EnabledFromEnv("E2E_SUITE_RESULT_MARKERS", "E2E_RESULT_MARKERS") },
	"host",
)

var _ = Describe("host exporter startup", Label("startupMetrics"), func() {
	It("serves parseable metrics from a direct host exporter", func() {
		runStartAndReadMetrics(GinkgoTB())
	})
})

var _ = Describe("host exporter TLS", Label("startupTLS"), func() {
	It("enforces TLS and basic authentication", func() {
		runStartWithTLSEnabledAndBasicAuth(GinkgoTB())
	})
})

var _ = Describe("host exporter YAML config", Label("configFile"), func() {
	It("uses inline metrics from a YAML config file", func() {
		runStartWithYAMLConfigFile(GinkgoTB())
	})
})

var _ = Describe("host exporter NVML injection metrics", Label("nvmlInjectionMetrics"), func() {
	It("exports every metric and entity sample reported by direct DCGM", func() {
		runNVMLInjectionMetrics(GinkgoTB())
	})
})

var _ = Describe("host exporter YAML watch groups", Label("watchGroups"), func() {
	It("starts with per-watch-group collection config", func() {
		runStartWithYAMLWatchGroups(GinkgoTB())
	})
})

var _ = Describe("host exporter runtime container labels", Label("runtimeContainerLabels"), func() {
	It("adds container labels from a Docker-compatible runtime socket", func() {
		runStartWithRuntimeContainerLabels(GinkgoTB())
	})
})

var _ = Describe("host exporter HPC job mapping", Label("hpcJobMapping"), func() {
	It("adds hpc_job labels from host mapping files", func() {
		runStartWithHPCJobMapping(GinkgoTB())
	})
})

var _ = Describe("host exporter reload", Label("reload"), func() {
	It("preserves profiling metrics across SIGHUP reloads", Label("profiling"), func() {
		runSIGHUPReloadPreservesProfilingMetrics(GinkgoTB())
	})

	It("preserves profiling metrics across file-watcher reloads", Label("profiling"), func() {
		runFileWatcherReloadPreservesProfilingMetrics(GinkgoTB())
	})

	It("keeps last-good metrics after failed SIGHUP reloads", func() {
		runSIGHUPReloadFailureKeepsLastGoodMetrics(GinkgoTB())
	})

	It("keeps last-good metrics after failed file-watcher reloads", func() {
		runFileWatcherReloadFailureKeepsLastGoodMetrics(GinkgoTB())
	})

	It("keeps scrapes parseable during concurrent SIGHUP reloads", func() {
		runConcurrentScrapeDuringSIGHUPReloads(GinkgoTB())
	})

	It("handles multiple SIGHUP reloads", func() {
		runMultipleSIGHUPReloads(GinkgoTB())
	})
})

var _ = Describe("host exporter IPv6 listen", Label("ipv6Listen"), func() {
	It("serves metrics on an IPv6 loopback address", func() {
		runIPv6ListenAndReadMetrics(GinkgoTB())
	})
})

var _ = Describe("host exporter GPU bind/unbind watch", Label("gpuBindUnbindWatch"), func() {
	It("serves metrics while the GPU bind/unbind watcher is enabled", func() {
		runStartWithGPUBindUnbindWatch(GinkgoTB())
	})
})

var _ = Describe("host exporter systemd socket activation", Label("systemdSocket"), func() {
	It("serves metrics on an inherited systemd listener", func() {
		runSystemdSocketActivation(GinkgoTB())
	})
})
