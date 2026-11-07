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

	It("adds the exporter's own families from the flag and from the config key", func() {
		runExporterMetricsEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter default preset", Label("defaultPreset"), func() {
	It("serves metrics from the shipped counter preset", func() {
		runStartWithDefaultPreset(GinkgoTB())
	})
})

var _ = Describe("host DCGM library loading", Label("dcgmLibraryPath"), func() {
	It("loads libdcgm.so.4 from LD_LIBRARY_PATH", func() {
		runDCGMLibraryPath(GinkgoTB())
	})
})

var _ = Describe("host exporter TLS", Label("startupTLS"), func() {
	It("enforces TLS and basic authentication", func() {
		runStartWithTLSEnabledAndBasicAuth(GinkgoTB())
	})

	It("serves the baseline families over TLS from the flag and from the config key", func() {
		runTLSEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter YAML config", Label("configFile"), func() {
	It("uses inline metrics from a YAML config file", func() {
		runStartWithYAMLConfigFile(GinkgoTB())
	})

	It("updates every field at the file-level every interval", func() {
		runConfigFileEveryCadence(GinkgoTB())
	})

	It("serves the same families as the equivalent flags", func() {
		runVersion2FileMatchesFlags(GinkgoTB())
	})

	It("warns at startup when a count window is longer than the DCGM history", func() {
		runCountWindowWarnsAtStartup(GinkgoTB())
	})

	It("does not warn at startup for a flag that has no config key", func() {
		runFlagWithoutConfigKeyDoesNotWarnAtStartup(GinkgoTB())
	})

	It("writes private debug dump files when the file turns the dump on", func() {
		runDebugDumpWritesFiles(GinkgoTB())
	})

	It("refuses a debug dump directory that is a symbolic link and keeps serving metrics", func() {
		runDebugDumpRefusesSymlinkDirectory(GinkgoTB())
	})

	It("keeps the debug dump directory within its file and byte limits", func() {
		runDebugDumpStaysWithinLimits(GinkgoTB())
	})
})

var _ = Describe("host exporter configuration precedence", Label("configPrecedence"), func() {
	It("uses an explicit collectors environment variable over inline YAML metrics", func() {
		runYAMLCollectorsEnvironmentPrecedence(GinkgoTB())
	})

	It("applies the interval from the flag over the environment over the file", func() {
		runIntervalPrecedenceCadence(GinkgoTB())
	})

	It("explains the winning source and the replaced values", func() {
		runExplainShowsWinningInterval(GinkgoTB())
	})
})

var _ = Describe("host exporter config startup errors", Label("configStartupErrors"), func() {
	for _, tc := range configStartupErrorCases {
		It(tc.name, func() {
			runConfigStartupError(GinkgoTB(), tc)
		})
	}
})

var _ = Describe("host config commands", Label("configInspection"), func() {
	It("validates a good file and lists what it did not check", func() {
		runConfigValidateSucceeds(GinkgoTB())
	})

	It("exits 1 with located diagnostics and an empty stdout for invalid files", func() {
		runConfigValidateFailures(GinkgoTB())
	})

	It("names the flag or variable behind a rejected setting", func() {
		runConfigValidateSettingErrors(GinkgoTB())
	})

	It("exits 2 on wrong command usage", func() {
		runConfigUsageErrors(GinkgoTB())
	})

	It("accepts a version 1 file with a deprecation warning", func() {
		runConfigVersion1Warns(GinkgoTB())
	})

	It("round-trips its own show output", func() {
		runConfigShowRoundTrip(GinkgoTB())
	})

	It("explains a file in text and JSON", func() {
		runConfigExplainOutput(GinkgoTB())
	})

	It("explains a flag over an environment variable over the file for the listen address, log format and devices", func() {
		runExplainShowsPrecedenceForSettings(GinkgoTB())
	})

	It("accepts a window on an XID count family", func() {
		runConfigWindowOverride(GinkgoTB())
	})

	It("warns once for a deprecated flag and not for a flag with no config key", func() {
		runDeprecatedFlagWarnsOnceInValidate(GinkgoTB())
	})
})

var _ = Describe("host exporter config file under NVML injection", Label("configServesMetrics"), func() {
	It("serves the fields a version 1 config file lists and no others", func() {
		runConfigServesMetrics(GinkgoTB())
	})
})

var _ = Describe("host exporter NVML injection metrics", Label("nvmlInjectionMetrics"), func() {
	It("matches every metric, entity sample, and value reported by direct DCGM", func() {
		runNVMLInjectionMetrics(GinkgoTB())
	})
})

var _ = Describe("host exporter MIG XID counters", Label("migXidCounters"), func() {
	It("matches CI-only observations in independent native history", func() {
		runMIGXidCounters(GinkgoTB())
	})
})

var _ = Describe("host exporter NVSDM mock metrics", Label("nvsdmMockMetrics"), func() {
	It("exports deterministic NVSwitch and link telemetry", func() {
		runNVSDMMockMetrics(GinkgoTB())
	})
})

var _ = Describe("host exporter GPU-owned link fields", Label("gpuLinkFields"), func() {
	It("exports available DCGM fields with the physical GPU and link identity", func() {
		runGPULinkFields(GinkgoTB())
	})
})

var _ = Describe("host exporter YAML overrides", Label("watchGroups"), func() {
	It("starts with metrics overrides in a config file", func() {
		runStartWithYAMLOverrides(GinkgoTB())
	})

	It("updates each watch group at its own interval", func() {
		runWatchGroupCadence(GinkgoTB())
	})

	It("caps the cached samples of a watch group", func() {
		runWatchGroupRetention(GinkgoTB())
	})
})

var _ = Describe("host exporter latest-value batching", Label("latestValueBatching"), func() {
	It("scrapes fields beyond the DCGM latest-value request boundary", func() {
		runLatestValueBatching(GinkgoTB())
	})
})

var _ = Describe("host exporter oversized field groups", Label("oversizedFieldGroup"), func() {
	It("splits one logical field group at the DCGM capacity", func() {
		runOversizedFieldGroup(GinkgoTB())
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

	It("deduplicates repeated hpc_job labels from host mapping files", func() {
		runStartWithRepeatedHPCJobMapping(GinkgoTB())
	})

	It("ignores zero and blank lines and trims padding in host mapping files", func() {
		runStartWithHPCJobMappingLineRules(GinkgoTB())
	})

	It("does not follow a symlinked mapping directory", func() {
		runHPCMappingSymlinkedDirectoryRefused(GinkgoTB())
	})

	It("does not read a mapping file that is a symlink", func() {
		runHPCMappingSymlinkedFileRefused(GinkgoTB())
	})

	It("refuses a mapping file with a second hard link", func() {
		runHPCMappingHardLinkRefused(GinkgoTB())
	})

	It("adds no job labels from a mapping file over the size limit", func() {
		runHPCMappingOversizedFileRefused(GinkgoTB())
	})
})

var _ = Describe("host exporter profiling reload", Label("profilingReload", "profiling"), func() {
	It("preserves profiling metrics across SIGHUP reloads", func() {
		runSIGHUPReloadPreservesProfilingMetrics(GinkgoTB())
	})

	It("preserves profiling metrics across file-watcher reloads", func() {
		runFileWatcherReloadPreservesProfilingMetrics(GinkgoTB())
	})
})

var _ = Describe("host exporter reload", Label("reload"), func() {
	It("preserves multi-group event watches across SIGHUP reloads", func() {
		runSIGHUPReloadPreservesEventWatches(GinkgoTB())
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

	It("keeps the startup settings when the version 2 file is edited or broken", func() {
		runYAMLEditAndSIGHUPKeepsStartupSettings(GinkgoTB())
	})

	It("reloads when the counters file named by a version 2 file changes and keeps the last good metrics when it breaks", func() {
		runVersion2MetricsFileChangeReloads(GinkgoTB())
	})
})

var _ = Describe("host exporter IPv6 listen", Label("ipv6Listen"), func() {
	It("serves metrics on an IPv6 loopback address", func() {
		runIPv6ListenAndReadMetrics(GinkgoTB())
	})

	It("serves the baseline families on the all-interfaces address from the flag and from the config key", func() {
		runListenAddressEffect(GinkgoTB())
	})

	It("serves the baseline families on an IPv6 address from the flag and from the config key", func() {
		runIPv6ListenAddressEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter GPU bind/unbind watch", Label("gpuBindUnbindWatch"), func() {
	It("serves metrics while the GPU bind/unbind watcher is enabled", func() {
		runStartWithGPUBindUnbindWatch(GinkgoTB())
	})
})

var _ = Describe("host exporter GPU bind/unbind events", Label("gpuBindUnbindEvents"), func() {
	It("drops a GPU's series when it is unbound and restores them when it is bound again", func() {
		runGPUBindUnbindEvents(GinkgoTB())
	})
})

var _ = Describe("host exporter on MIG GPUs with two instances each", Label("migGpuLevelFields"), func() {
	It("stays up and reports GPU-level families once per physical GPU", func() {
		runMIGGPULevelFields(GinkgoTB())
	})
})

var _ = Describe("host exporter systemd socket activation", Label("systemdSocket"), func() {
	It("serves metrics on an inherited systemd listener", func() {
		runSystemdSocketActivation(GinkgoTB())
	})
})

var _ = Describe("host exporter GPU selection", Label("gpuSelection"), func() {
	It("reports only the selected GPUs from the flag and from the config key", func() {
		runGPUSelectionEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter model name labels", Label("modelNameLabels"), func() {
	It("replaces blanks in every model name label from the flag", func() {
		runModelNameLabelsEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter HTTP timeouts", Label("httpTimeouts"), func() {
	It("closes a stalled connection within a short read timeout from the flag and from the config key", func() {
		runReadTimeoutEffect(GinkgoTB())
	})

	It("fails the scrape and logs the failed write with a short write timeout", func() {
		runWriteTimeoutEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter log levels", Label("logLevels"), func() {
	It("writes debug records only with the debug flag or the log level key", func() {
		runDebugLogEffect(GinkgoTB())
	})

	It("writes DCGM log lines only when the embedded DCGM log is on, at the level asked for", func() {
		runEmbeddedDCGMLogEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter scrape limit", Label("scrapeLimit"), func() {
	It("refuses overlapping scrapes from the flag and from the config key", func() {
		runScrapeLimitEffect(GinkgoTB())
	})
})

var _ = Describe("host exporter injected GPU events", Label("injectedEventCounts"), func() {
	It("counts an injected XID and clock-event reason on that GPU only until the window ends", func() {
		runInjectedEventCounts(GinkgoTB())
	})

	It("counts an injected XID only for the window a metrics override sets", func() {
		runInjectedEventOverrideWindow(GinkgoTB())
	})
})

var _ = Describe("host exporter GPU load", Label("gpuLoadUtilization"), func() {
	It("reports utilization and energy that follow real load on that GPU only", func() {
		runGPULoadUtilization(GinkgoTB())
	})
})

var _ = Describe("host exporter workload enrichment", Label("configEnrichment"), func() {
	It("starts without container labels when an auto socket is missing", func() {
		runEnrichmentAutoMissingSocketStarts(GinkgoTB())
	})

	It("warns and skips an auto socket that cannot be used", func() {
		runEnrichmentAutoUnusableSocketWarns(GinkgoTB())
	})

	It("waits for a required socket and fails when it never appears", func() {
		runEnrichmentRequiredMissingSocketFails(GinkgoTB())
	})

	It("starts once a required socket appears after startup began", func() {
		runEnrichmentRequiredWaitsForLateSocket(GinkgoTB())
	})

	It("uses the socket for auto and true and never for false", func() {
		runEnrichmentFalseNeverTouchesSocket(GinkgoTB())
	})

	It("keeps auto Kubernetes enrichment on and warns when the kubelet socket is missing", func() {
		runEnrichmentKubernetesMissingSocket(GinkgoTB())
	})

	It("waits for a required HPC job directory that appears after startup began", func() {
		runEnrichmentHPCJobsTrueWaitsForLateDirectory(GinkgoTB())
	})

	It("adds no job labels when HPC job enrichment is off", func() {
		runEnrichmentHPCJobsFalseAddsNoLabels(GinkgoTB())
	})
})
