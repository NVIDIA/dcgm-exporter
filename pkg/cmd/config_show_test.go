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

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// fullVersion2Config sets a value for every key of the version 2 file that this build reads.
const fullVersion2Config = `version: 2
metrics:
  - name: gpu
    fields:
      - {name: DCGM_FI_DEV_GPU_TEMP, prometheus_type: gauge, help: Temperature.}
      - {name: DCGM_FI_DEV_POWER_USAGE, prometheus_type: gauge, help: Power.}
    every: 20s
    overrides:
      - name: fast-power
        match: ["DCGM_FI_DEV_POWER_*"]
        every: 1s
        sources:
          dcgm:
            watch: {max_keep_age: 1m, max_keep_samples: 0}
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 2m
      - match: [DCGM_EXP_CLOCK_EVENTS_COUNT]
        window: 3m
    entities:
      gpu: {policy: explicit, gpus: [0, 1], instances: none}
      switch: {indices: [0], links: all}
      cpu: {indices: all, cores: none}
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1, listen: ":9500"}
sources:
  dcgm:
    hostengine: "remote:5555"
    watch: {max_keep_age: 5m, max_keep_samples: 100}
    detect_bind_unbind: {enabled: true, every: 2s}
    cleanup_timeout: 90s
  kubernetes:
    pod_resources_socket: /run/kubelet.sock
    gpu_id_type: device-name
    additional_resource_names: [nvidia.com/mig]
    virtual_gpus: true
    pod_uid: true
    pod_labels: {enabled: true, allow: ["^app$"]}
  container: {runtime_socket: /run/containerd.sock}
  dra: {mode: kubernetes}
  hpc_jobs: {mapping_dir: /var/lib/jobs}
server:
  read_timeout: 5s
  write_timeout: 20s
  max_concurrent_scrapes: 4
  systemd_socket: true
  web_config_file: /etc/web.yml
  profiling: true
observability:
  metrics: {enabled: true}
  logs: {format: json, level: debug}
debug_dump: {enabled: true, directory: /var/dump, retention: 12h, compression: false}
`

// version1Config is a version 1 file in the shape of the 4.8.4 Helm example.
const version1Config = `version: 1
metrics:
  fields:
    - {name: DCGM_FI_DEV_GPU_TEMP, prometheusType: gauge, help: Temperature.}
  enableExporterMetrics: false
collection:
  interval: 30s
  retention: {maxAge: 10m, maxSamples: 0}
`

// settingsOf resolves a file and arguments the way startup does and returns
// the resolved config.
func settingsOf(t *testing.T, configFile string, args ...string) *appconfig.Config {
	t.Helper()
	resolved, err := resolveFileAt(t, configFile, args...)
	require.NoError(t, err)
	return resolved.Config
}

// comparableSettings clears what a saved document cannot carry, so two configs
// can be compared field by field: the file path, the settings that only a flag
// can set, the file's enrichment modes (a source a flag turned on is saved as
// auto), and the difference between a nil and an empty list.
func comparableSettings(config *appconfig.Config) *appconfig.Config {
	c := config.Clone()
	c.ConfigFile, c.ServiceInstanceID = "", ""
	c.UseOldNamespace, c.NoHostname, c.ReplaceBlanksInModelName = false, false, false
	c.UseFakeGPUs, c.DisableStartupValidate = false, false
	c.KubernetesPodLabelCacheSize = 0
	// Settings that do nothing while their feature is off cannot be written.
	if !c.EnableDCGMLog {
		c.DCGMLogLevel = ""
	}

	if !c.EnableGPUBindUnbindWatch {
		c.GPUBindUnbindPollInterval = 0
	}

	// The document writes the version 2 default socket even while container labels are off.
	if !c.ContainerLabels {
		c.ContainerRuntimeSocket = ""
	}

	c.EnrichmentModes = appconfig.EnrichmentModes{}
	if len(c.NvidiaResourceNames) == 0 {
		c.NvidiaResourceNames = nil
	}

	if len(c.KubernetesPodLabelAllowlistRegex) == 0 {
		c.KubernetesPodLabelAllowlistRegex = nil
	}

	return c
}

// TestShowOutputLoadsBackAsTheSameSettings saves the output of config show,
// loads it as a config file, and compares the two resolved configs field by
// field. A setting that show drops or writes wrongly fails here.
func TestShowOutputLoadsBackAsTheSameSettings(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		args       []string
	}{
		{name: "every key set", yaml: fullVersion2Config},
		{name: "version 2 alone", yaml: "version: 2\n"},
		{name: "version 1 file", yaml: version1Config},
		{name: "metric file", yaml: "version: 2\nmetrics:\n  - {name: gpu, file: /etc/metrics.csv}\noutputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n"},
		{
			name: "flags win over the file", yaml: fullVersion2Config,
			args: []string{
				"--collect-interval", "5000", "--devices", "g:2", "--xid-count-window-size", "1000",
				"--nvidia-resource-names", "nvidia.com/a,nvidia.com/b", "--enable-dcgm-log=false",
			},
		},
		{
			name: "unnamed group before a window override",
			yaml: withInlineMetrics("    overrides:\n      - {match: [DCGM_FI_DEV_GPU_TEMP], every: 2s}\n      - {match: [DCGM_EXP_XID_ERRORS_COUNT], window: 2m}\n"),
		},
		{
			name: "unnamed group after a window override",
			yaml: withInlineMetrics("    overrides:\n      - {match: [DCGM_EXP_XID_ERRORS_COUNT], window: 2m}\n      - {match: [DCGM_FI_DEV_GPU_TEMP], every: 2s}\n"),
		},
		{
			name: "repeated device indices from flags", yaml: "version: 2\n",
			args: []string{"--devices", "g:0,0,1-2,1+i:3,3", "--switch-devices", "g:1,1", "--cpu-devices", "i:2,2+g:0,0"},
		},
		{
			name: "repeated device indices in the file",
			yaml: withInlineMetrics("    entities:\n      gpu: {policy: explicit, gpus: [0, 0, 1], instances: [2, 2]}\n      cpu: {indices: [1, 1, 0], cores: none}\n"),
		},
		{name: "blank resource names", yaml: "version: 2\n", args: []string{"--nvidia-resource-names", " , nvidia.com/a ,"}},
		{name: "only blank resource names", yaml: "version: 2\n", args: []string{"--nvidia-resource-names", " "}},
		{name: "inactive poll interval of zero", yaml: "version: 2\n", args: []string{"--gpu-bind-unbind-poll-interval", "0s"}},
		{name: "log level while the log is off", yaml: "version: 2\n", args: []string{"--dcgm-log-level", "INFO"}},
		{
			name: "enrichment from flags", yaml: "version: 2\n",
			args: []string{"--kubernetes", "--container-runtime-socket", "/run/c.sock", "--kubernetes-enable-dra"},
		},
		{
			name: "settings from flags only", yaml: "version: 2\n",
			args: []string{
				"--switch-devices", "g:0-2", "--cpu-devices", "i", "--address", "[::]:9401",
				"--watch-max-keep-age", "90s", "--debug", "--log-format", "json", "--dump-enabled",
				"--kubernetes-enable-pod-uid", "--kubernetes-enable-pod-labels", "--kubernetes-pod-label-allowlist-regex", "^app$",
				"--web-config-file", "/etc/web.yml", "--dcgm-cleanup-timeout", "2m",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			original := writeConfigFile(t, tc.yaml)
			invocation := []string{"config", "show", original}
			if len(tc.args) > 0 {
				invocation = append(invocation, "--")
				invocation = append(invocation, tc.args...)
			}

			stdout, _, err := inspectionRun(t, invocation...)
			require.NoError(t, err)

			saved := writeConfigFile(t, stdout)
			assert.Equal(t, comparableSettings(settingsOf(t, original, tc.args...)), comparableSettings(settingsOf(t, saved)),
				"the saved document must load as the same settings")

			again, _, err := inspectionRun(t, "config", "show", saved)
			require.NoError(t, err)
			assert.Equal(t, stdout, again, "showing the saved document must not change it")
		})
	}
}

// TestShowOutputHasEveryKey checks a file with only a version shows every key
// that the reader accepts, with its default.
func TestShowOutputHasEveryKey(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	stdout, _, err := inspectionRun(t, "config", "show", writeConfigFile(t, "version: 2\n"))
	require.NoError(t, err)
	var shown map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(stdout), &shown))

	for path, want := range map[string]any{
		"version":                                      2,
		"metrics[0].name":                              "default",
		"metrics[0].every":                             "30s",
		"metrics[0].entities.gpu.policy":               "flex",
		"metrics[0].entities.switch.indices":           "all",
		"metrics[0].entities.cpu.cores":                "all",
		"outputs.prometheus.schema":                    "dcgm/v1",
		"outputs.prometheus.listen":                    ":9400",
		"sources.dcgm.hostengine":                      "",
		"sources.dcgm.watch.max_keep_age":              "10m",
		"sources.dcgm.watch.max_keep_samples":          0,
		"sources.dcgm.detect_bind_unbind.every":        "1s",
		"sources.dcgm.cleanup_timeout":                 "5m",
		"sources.kubernetes.gpu_id_type":               "uid",
		"sources.kubernetes.additional_resource_names": []any{},
		"sources.dra.mode":                             "kubernetes",
		"server.read_timeout":                          "10s",
		"server.write_timeout":                         "30s",
		"server.max_concurrent_scrapes":                16,
		"server.profiling":                             false,
		"observability.metrics.enabled":                false,
		"observability.logs.format":                    "text",
		"observability.logs.level":                     "info",
		"debug_dump.directory":                         "/tmp/dcgm-exporter-debug",
		"debug_dump.retention":                         "24h",
		"debug_dump.compression":                       true,
	} {
		got, found := appconfig.LookupValue(shown, path)
		assert.True(t, found, path)
		assert.Equal(t, want, got, path)
	}
}

// TestShowUsesKeyNamesOnly checks no output of any command shows a Go field name.
func TestShowUsesKeyNamesOnly(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	csv := inspectionCSV(t, "DCGM_FI_DEV_GPU_TEMP,gauge,Temperature\n")
	path := writeConfigFile(t, strings.Replace(fullVersion2Config, "fields:\n      - {name: DCGM_FI_DEV_GPU_TEMP, prometheus_type: gauge, help: Temperature.}\n      - {name: DCGM_FI_DEV_POWER_USAGE, prometheus_type: gauge, help: Power.}",
		"file: "+csv, 1))

	for _, args := range [][]string{
		{"config", "show", path},
		{"config", "show", path, "-o", "json"},
		{"config", "validate", path},
		{"config", "validate", path, "-o", "json"},
		{"config", "explain", path},
		{"config", "explain", path, "--verbose"},
		{"config", "explain", path, "-o", "json"},
	} {
		stdout, stderr, err := inspectionRun(t, append(args, "--", "--kubernetes", "--fake-gpus")...)
		// validate may fail on the web policy that the full file names; its
		// result is still printed, on stdout or stderr.
		require.NotEmpty(t, stdout+stderr, "%s: %v", strings.Join(args, " "), err)
		for _, internal := range []string{
			"CollectInterval", "DisablePrometheus", "ServiceInstanceID", "WatchRetention", "MetricSource",
			"GPUDeviceOptions", "WebReadTimeout", "DumpConfig", "EnableOTLP", "UseRemoteHE", "XIDCountWindowSize",
		} {
			assert.NotContains(t, stdout+stderr, internal, strings.Join(args, " "))
		}
	}
}

func TestShowJSONIsTheSameDocument(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, fullVersion2Config)
	asYAML, _, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err)
	asJSON, _, err := inspectionRun(t, "config", "show", path, "-o", "json")
	require.NoError(t, err)

	var fromYAML, fromJSON map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(asYAML), &fromYAML))
	require.NoError(t, json.Unmarshal([]byte(asJSON), &fromJSON))
	// Whole numbers decode as int from YAML and float64 from JSON; compare as JSON text.
	left, _ := json.Marshal(fromYAML)
	right, _ := json.Marshal(fromJSON)
	assert.JSONEq(t, string(left), string(right))
	assert.Equal(t, float64(2), fromJSON["version"], "show -o json is the bare document, not an envelope")
	assert.NotContains(t, fromJSON, "format_version")
}

// TestShowMakesRelativePathsAbsolute checks a path typed relative to the
// working directory still names the same file when the output is saved elsewhere.
func TestShowMakesRelativePathsAbsolute(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	stdout, _, err := inspectionRun(t, "config", "show", "--", "--collectors", "relative.csv", "--web-config-file", "web.yml")
	require.NoError(t, err)
	directory, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, stdout, "file: "+filepath.Join(directory, "relative.csv"))
	assert.Contains(t, stdout, "web_config_file: "+filepath.Join(directory, "web.yml"))
}

// TestShowReadsNoLocalFiles checks show works when the files the settings name are missing or invalid.
func TestShowReadsNoLocalFiles(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	policy := inspectionYAML(t, "tls_server_config:\n  cipher_suites: [secret-value]\n")
	stdout, stderr, err := inspectionRun(t, "config", "show", "--",
		"--collectors", "/missing/metrics.csv", "--web-config-file", policy)

	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.NotContains(t, stdout, "secret-value")
}

// TestShowNamesSettingsTheDocumentCannotHold checks a setting that only a flag can set is reported on stderr.
func TestShowNamesSettingsTheDocumentCannotHold(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	_, stderr, err := inspectionRun(t, "config", "show")
	require.NoError(t, err)
	assert.Empty(t, stderr, "default settings are all in the document")

	_, stderr, err = inspectionRun(t, "--fake-gpus", "--disable-startup-validate=false", "config", "show", "--", "--no-hostname")
	require.NoError(t, err)
	assert.Contains(t, stderr, "warning  --fake-gpus: left out of the output: the config file has no key for it")
	assert.Contains(t, stderr, "--no-hostname: left out of the output")
	assert.NotContains(t, stderr, "--disable-startup-validate", "a flag left at its default value is not mentioned")

	_, stderr, err = inspectionRun(t, "--configmap-data", "ns:name", "config", "show")
	require.NoError(t, err)
	assert.Contains(t, stderr, "--configmap-data: left out of the output: the config file cannot name a ConfigMap, so the output names the default metric file instead")
}

// TestShowPrintsTheVersion1Warning checks a version 1 file is shown as version 2 with the notice on stderr.
func TestShowPrintsTheVersion1Warning(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, version1Config)
	stdout, stderr, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stdout, "version: 2\n"))
	assert.Equal(t, "warning  "+path+":1:10 version: "+appconfig.LegacyConfigVersionWarning+"\n", stderr)
	assert.Contains(t, stdout, "name: DCGM_FI_DEV_GPU_TEMP")
	assert.Contains(t, stdout, "prometheus_type: gauge")
}

// TestShowFailsWithoutOutputWhenTheSettingsAreInvalid checks an invalid input
// writes its problems to stderr and nothing to stdout.
func TestShowFailsWithoutOutputWhenTheSettingsAreInvalid(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			"bad file value",
			[]string{"config", "show", inspectionYAML(t, "version: 2\nserver: {max_concurrent_scrapes: 0}\n")},
			"server.max_concurrent_scrapes",
		},
		{"bad flag value", []string{"--web-read-timeout", "0s", "config", "show"}, "--web-read-timeout"},
		{"profiling without web config", []string{"--enable-pprof", "config", "show"}, "--enable-pprof"},
		{"empty config file", []string{"--config-file", "", "config", "show"}, "empty"},
		{
			"retention of zero for age and samples",
			[]string{"config", "show", inspectionYAML(t, "version: 2\nsources:\n  dcgm:\n    watch: {max_keep_age: 0s}\n")},
			"cannot both be zero",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := inspectionRun(t, tc.args...)
			require.Error(t, err)
			assert.Equal(t, exitFailure, ExitStatus(err))
			assert.True(t, AlreadyReported(err))
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tc.want)
		})
	}
}

// TestShowKeepsTheNamesInTheFile checks the metrics entry and output names of a
// version 2 file survive, and that the defaults are used without a file value.
func TestShowKeepsTheNamesInTheFile(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	named := withInlineMetrics("")
	named = strings.Replace(strings.Replace(named, "name: gpu", "name: mine", 1), "prometheus: {", "scrape: {", 1)
	stdout, _, err := inspectionRun(t, "config", "show", writeConfigFile(t, named))
	require.NoError(t, err)
	assert.Contains(t, stdout, "  - name: mine\n")
	assert.Contains(t, stdout, "outputs:\n  scrape:\n")

	stdout, _, err = inspectionRun(t, "config", "show", writeConfigFile(t, version1Config))
	require.NoError(t, err)
	assert.Contains(t, stdout, "  - name: default\n")
	assert.Contains(t, stdout, "outputs:\n  prometheus:\n")

	data := explainJSON(t, nil, writeConfigFile(t, named))
	assert.Equal(t, "mine", data.Config.Metrics[0].Name)
	origin := settingByPath(t, explainJSON(t, []string{"--address", ":9500"}, writeConfigFile(t, named)), "outputs.scrape.listen")
	assert.Equal(t, ":9500", origin.Value, "the flag is attributed to the output named in the file")
}

// TestShowWritesEnrichmentSwitches checks each switch is written the way the reader reads it back.
func TestShowWritesEnrichmentSwitches(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, withInlineMetrics("    enrichment: {kubernetes: true, container: false, dra: auto}\n")+"sources: {dra: {mode: kubernetes}}\n")
	stdout, _, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err)
	var shown map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(stdout), &shown))
	for key, want := range map[string]any{
		"metrics[0].enrichment.kubernetes": true, "metrics[0].enrichment.container": false,
		"metrics[0].enrichment.dra": "auto", "metrics[0].enrichment.hpc_jobs": "auto",
	} {
		got, found := appconfig.LookupValue(shown, key)
		assert.True(t, found, key)
		assert.Equal(t, want, got, key)
	}
}

// TestShowAcceptsOutputNamesWithDots checks an output name that contains a dot
// does not break show or explain, and is kept.
func TestShowAcceptsOutputNamesWithDots(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := writeConfigFile(t, strings.Replace(withInlineMetrics(""), "prometheus: {", `"scrape.prod": {`, 1))
	stdout, _, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err)
	assert.Contains(t, stdout, "  scrape.prod:\n")
	data := explainJSON(t, []string{"--address", ":9500"}, path)
	assert.Equal(t, ":9500", settingByPath(t, data, "outputs.scrape.prod.listen").Value)
}

// TestShowWarnsAboutASettingTheDocumentLeavesOut checks the reason is the true one.
func TestShowWarnsAboutASettingTheDocumentLeavesOut(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	_, stderr, err := inspectionRun(t, "--dcgm-log-level", "INFO", "config", "show")
	require.NoError(t, err)
	assert.Contains(t, stderr, "left out of the output: the key is left out while the embedded DCGM log is off")
	assert.NotContains(t, stderr, "has no key for it")
}

// TestShowMakesWorkloadPathsAbsolute checks every file system path in the document is absolute.
func TestShowMakesWorkloadPathsAbsolute(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	stdout, _, err := inspectionRun(t, "config", "show", "--", "--hpc-job-mapping-dir", "jobs",
		"--pod-resources-kubelet-socket", "kubelet.sock", "--container-runtime-socket", "unix:///run/c.sock")

	require.NoError(t, err)
	directory, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, stdout, "mapping_dir: "+filepath.Join(directory, "jobs"))
	assert.Contains(t, stdout, "pod_resources_socket: "+filepath.Join(directory, "kubelet.sock"))
	assert.Contains(t, stdout, `runtime_socket: "unix:///run/c.sock"`, "a value with a scheme is not a relative path")
}

// TestShowLeavesCountWindowsOutAtTheirDefault checks a window is written only
// when it differs from the default, and that reloading the output gives the same windows.
func TestShowLeavesCountWindowsOutAtTheirDefault(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	stdout, _, err := inspectionRun(t, "config", "show")
	require.NoError(t, err)
	assert.NotContains(t, stdout, "window:")
	assert.NotContains(t, stdout, "DCGM_EXP_XID_ERRORS_COUNT")
	assert.NotContains(t, stdout, "overrides")

	stdout, _, err = inspectionRun(t, "--xid-count-window-size", "60000", "config", "show")
	require.NoError(t, err)
	assert.Contains(t, stdout, "DCGM_EXP_XID_ERRORS_COUNT")
	assert.Contains(t, stdout, "window: 1m")
	assert.NotContains(t, stdout, "DCGM_EXP_CLOCK_EVENTS_COUNT")
	saved := writeConfigFile(t, stdout)
	assert.Equal(t, 60000, settingsOf(t, saved).XIDCountWindowSize)
	assert.Equal(t, 300000, settingsOf(t, saved).ClockEventsCountWindowSize)
}

// TestShowWritesTheVersion2WorkloadPathDefaultsAndQuotesAmbiguousStrings checks
// the document names the default socket and directory even while their source
// is off, and quotes values that are easy to misread, and still loads back.
func TestShowWritesTheVersion2WorkloadPathDefaultsAndQuotesAmbiguousStrings(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	stdout, _, err := inspectionRun(t, "config", "show", "--", "--remote-hostengine-info", "[::1]:5555", "--address", "localhost:9400")
	require.NoError(t, err)
	assert.Contains(t, stdout, "runtime_socket: /var/run/docker.sock")
	assert.Contains(t, stdout, "mapping_dir: /var/lib/dcgm-exporter/job-mapping")
	assert.Contains(t, stdout, `listen: "localhost:9400"`)
	assert.Contains(t, stdout, `hostengine: '[::1]:5555'`)
	saved := writeConfigFile(t, stdout)
	assert.Equal(t, "localhost:9400", settingsOf(t, saved).Address)
	assert.Equal(t, "[::1]:5555", settingsOf(t, saved).RemoteHEInfo)

	stdout, _, err = inspectionRun(t, "config", "show")
	require.NoError(t, err)
	assert.Contains(t, stdout, `listen: ":9400"`)
}

// TestShowLeavesUnnamedOverridesWithoutAName checks show does not write the
// path name the reader gave an unnamed override, and keeps a name the user gave.
func TestShowLeavesUnnamedOverridesWithoutAName(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	path := explainFile(t, "    overrides:\n      - {match: [DCGM_FI_DEV_GPU_TEMP], every: 2s}\n      - {name: power, match: [\"DCGM_FI_DEV_POWER_*\"], every: 1s}\n")
	text, stderr, err := inspectionRun(t, "config", "show", path)
	require.NoError(t, err)
	assert.NotContains(t, text, "metrics[0].overrides[0]")
	assert.Contains(t, text, "name: power")
	assert.Empty(t, stderr, "a name no input set is nothing to warn about")
}

// TestShowRenamesVersion1WatchGroupsThatAnOverrideNameRejects checks a version 1
// watch group name that a version 2 override cannot carry is changed, that the
// change is reported once per group, and that the document loads back.
func TestShowRenamesVersion1WatchGroupsThatAnOverrideNameRejects(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	original := writeConfigFile(t, `version: 1
metrics:
  fields:
    - {name: DCGM_FI_DEV_GPU_TEMP, prometheusType: gauge, help: Temperature.}
    - {name: DCGM_FI_DEV_POWER_USAGE, prometheusType: gauge, help: Power.}
    - {name: DCGM_FI_DEV_FB_USED, prometheusType: gauge, help: Used framebuffer.}
collection:
  interval: 30s
  watchGroups:
    - {name: "gpu/temp", interval: 2s, fields: [DCGM_FI_DEV_GPU_TEMP]}
    - {name: "gpu temp", interval: 3s, fields: [DCGM_FI_DEV_POWER_USAGE]}
    - {name: "gpu-temp", interval: 4s, fields: [DCGM_FI_DEV_FB_USED]}
`)

	stdout, stderr, err := inspectionRun(t, "config", "show", original)
	require.NoError(t, err)

	warnings := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "is written as override") {
			warnings++
		}
	}

	assert.Equal(t, 2, warnings, stderr)
	assert.Contains(t, stderr, `watch group "gpu/temp" is written as override "gpu-temp-2"`)
	assert.Contains(t, stderr, `watch group "gpu temp" is written as override "gpu-temp-3"`)

	groups := settingsOf(t, writeConfigFile(t, stdout)).WatchGroups
	require.Len(t, groups, 3)
	assert.Equal(t, []string{"gpu-temp-2", "gpu-temp-3", "gpu-temp"}, []string{groups[0].Name, groups[1].Name, groups[2].Name})
	assert.Equal(t, 2000, groups[0].Interval)
}

// TestShowDuplicateOutputNamesTheFormatsOfTheCommand checks the usage error for a
// repeated --output lists the formats show writes.
func TestShowDuplicateOutputNamesTheFormatsOfTheCommand(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	_, _, err := inspectionRun(t, "config", "show", "--output", "json", "--output", "yaml")
	require.ErrorContains(t, err, "select yaml or json once")
}

// TestShowKeepsWhatAVersion1WatchGroupItemSelects checks that each version 1
// item, which is always a path.Match pattern, selects the same field names
// after config show writes it as a version 2 item.
func TestShowKeepsWhatAVersion1WatchGroupItemSelects(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	items := []struct {
		item string
		// the item as the document writes it
		want string
		// the field names that the item selects among those the file defines
		selects []string
	}{
		{item: `DCGM_FI_DEV_GPU_TEMP`, want: `DCGM_FI_DEV_GPU_TEMP`, selects: []string{"DCGM_FI_DEV_GPU_TEMP"}},
		{item: `DCGM_FI_DEV_GPU_TEM\P`, want: `DCGM_FI_DEV_GPU_TEMP`, selects: []string{"DCGM_FI_DEV_GPU_TEMP"}},
		{item: `DCGM_FI_DEV_POWER_*`, want: `DCGM_FI_DEV_POWER_*`, selects: []string{"DCGM_FI_DEV_POWER_USAGE"}},
		{item: `DCGM_FI_DEV_FB_[UF]SED`, want: `DCGM_FI_DEV_FB_[UF]SED`, selects: []string{"DCGM_FI_DEV_FB_USED"}},
		{item: `DCGM_FI_DEV_\FB_USED`, want: `DCGM_FI_DEV_FB_USED`, selects: []string{"DCGM_FI_DEV_FB_USED"}},
	}

	// The file the item sits in: the field definitions, then the watch groups.
	const prefix = "version: 1\nmetrics:\n  fields:\n" +
		"    - {name: DCGM_FI_DEV_GPU_TEMP, prometheusType: gauge, help: Temperature.}\n" +
		"    - {name: DCGM_FI_DEV_POWER_USAGE, prometheusType: gauge, help: Power.}\n" +
		"    - {name: DCGM_FI_DEV_FB_USED, prometheusType: gauge, help: Used framebuffer.}\n" +
		"collection:\n  interval: 30s\n  watchGroups:\n"

	// The field names that the file defines.
	defined := []string{"DCGM_FI_DEV_GPU_TEMP", "DCGM_FI_DEV_POWER_USAGE", "DCGM_FI_DEV_FB_USED"}

	for _, item := range items {
		t.Run(item.item, func(t *testing.T) {
			file := writeConfigFile(t, prefix+"    - {name: g, interval: 2s, fields: ['"+item.item+"']}\n")
			stdout, _, err := inspectionRun(t, "config", "show", file)
			require.NoError(t, err)

			before := settingsOf(t, file).WatchGroups
			after := settingsOf(t, writeConfigFile(t, stdout)).WatchGroups
			require.Len(t, before, 1)
			require.Len(t, after, 1)
			assert.Equal(t, []string{item.want}, after[0].Fields)
			for _, group := range [][]appconfig.WatchGroup{before, after} {
				matcher, err := group[0].FieldMatcher()
				require.NoError(t, err)
				var got []string
				for _, name := range defined {
					if matcher.Matches(name) {
						got = append(got, name)
					}
				}
				assert.Equal(t, item.selects, got)
			}
		})
	}
}
