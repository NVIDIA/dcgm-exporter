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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/go-dcgm/pkg/dcgm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// flagKeyMapping says where one root flag lives in a version 2 file. Exactly
// one of key and flagOnlyReason is set.
//
// A row with a key also names a valid value for it, the other keys that must
// sit beside it (extra), the Config field it sets, and a reader for that field.
// The test applies the value and checks the field changed and its origin is the key.
type flagKeyMapping struct {
	flag           string
	key            string
	flagOnlyReason string

	value any
	extra map[string]any
	top   map[string]any // other top-level keys the document needs, such as a source block
	field string
	read  func(*appconfig.Config) any
}

// counterWindow is the extra key that picks the counter a window override sets.
func counterWindow(counter string) map[string]any {
	return map[string]any{"match": []string{counter}}
}

// flagKeyMappings lists every root flag. A flag added to app.go without a row
// here fails TestEveryRootFlagHasAVersion2KeyOrReason.
var flagKeyMappings = []flagKeyMapping{
	{flag: CLIConfigFile, flagOnlyReason: "selects the file itself"},
	{
		flag: CLIFieldsFile, key: "metrics[0].file", value: "/b.csv", field: "MetricSource",
		read: func(c *appconfig.Config) any { return c.MetricSource },
	},
	{
		flag: CLIAddress, key: "outputs.prometheus.listen", value: ":9500", field: "Address",
		read: func(c *appconfig.Config) any { return c.Address },
	},
	{
		flag: CLICollectInterval, key: "metrics[0].every", value: "10s", field: "CollectInterval",
		read: func(c *appconfig.Config) any { return c.CollectInterval },
	},
	{
		flag: CLIWatchMaxKeepAge, key: "sources.dcgm.watch.max_keep_age", value: "2m", field: "WatchRetention.MaxAge",
		read: func(c *appconfig.Config) any { return c.WatchRetention.MaxAge },
	},
	{
		flag: CLIWatchMaxKeepSamples, key: "sources.dcgm.watch.max_keep_samples", value: 5,
		field: "WatchRetention.MaxSamples",
		read:  func(c *appconfig.Config) any { return c.WatchRetention.MaxSamples },
	},
	{
		flag: CLIKubernetes, key: "metrics[0].enrichment.kubernetes", value: true, field: "Kubernetes",
		read: func(c *appconfig.Config) any { return c.Kubernetes },
	},
	{
		flag: CLIKubernetesEnablePodLabels, key: "sources.kubernetes.pod_labels.enabled", value: true,
		field: "KubernetesEnablePodLabels",
		read:  func(c *appconfig.Config) any { return c.KubernetesEnablePodLabels },
	},
	{
		flag: CLIKubernetesEnablePodUID, key: "sources.kubernetes.pod_uid", value: true,
		field: "KubernetesEnablePodUID",
		read:  func(c *appconfig.Config) any { return c.KubernetesEnablePodUID },
	},
	{
		flag: CLIKubernetesGPUIDType, key: "sources.kubernetes.gpu_id_type", value: "device-name",
		field: "KubernetesGPUIdType",
		read:  func(c *appconfig.Config) any { return c.KubernetesGPUIdType },
	},
	{
		flag: CLIKubernetesPodLabelAllowlistRegex, key: "sources.kubernetes.pod_labels.allow",
		value: []string{"^app$"}, field: "KubernetesPodLabelAllowlistRegex",
		read: func(c *appconfig.Config) any { return c.KubernetesPodLabelAllowlistRegex },
	},
	{flag: CLIUseOldNamespace, flagOnlyReason: "flag only; no config key"},
	{
		flag: CLIRemoteHEInfo, key: "sources.dcgm.hostengine", value: "host:5555", field: "RemoteHEInfo",
		read: func(c *appconfig.Config) any { return c.RemoteHEInfo },
	},
	{
		flag: CLIGPUDevices, key: "metrics[0].entities.gpu", value: map[string]any{"policy": "explicit", "gpus": []int{0}},
		field: "GPUDeviceOptions",
		read:  func(c *appconfig.Config) any { return c.GPUDeviceOptions },
	},
	{
		flag: CLISwitchDevices, key: "metrics[0].entities.switch",
		value: map[string]any{"indices": []int{0}, "links": "none"}, field: "SwitchDeviceOptions",
		read: func(c *appconfig.Config) any { return c.SwitchDeviceOptions },
	},
	{
		flag: CLICPUDevices, key: "metrics[0].entities.cpu",
		value: map[string]any{"indices": []int{0}, "cores": "none"}, field: "CPUDeviceOptions",
		read: func(c *appconfig.Config) any { return c.CPUDeviceOptions },
	},
	{flag: CLINoHostname, flagOnlyReason: "flag only; no config key"},
	{flag: CLIUseFakeGPUs, flagOnlyReason: "for tests only"},
	{flag: CLIConfigMapData, flagOnlyReason: "deprecated; mount the ConfigMap and use metrics[0].file"},
	{
		flag: CLIWebSystemdSocket, key: "server.systemd_socket", value: true, field: "WebSystemdSocket",
		read: func(c *appconfig.Config) any { return c.WebSystemdSocket },
	},
	{
		flag: CLIWebConfigFile, key: "server.web_config_file", value: "/w.yml", field: "WebConfigFile",
		read: func(c *appconfig.Config) any { return c.WebConfigFile },
	},
	{
		flag: CLIWebReadTimeout, key: "server.read_timeout", value: "5s", field: "WebReadTimeout",
		read: func(c *appconfig.Config) any { return c.WebReadTimeout },
	},
	{
		flag: CLIWebWriteTimeout, key: "server.write_timeout", value: "20s", field: "WebWriteTimeout",
		read: func(c *appconfig.Config) any { return c.WebWriteTimeout },
	},
	{
		flag: CLIMaxConcurrentScrapes, key: "server.max_concurrent_scrapes", value: 4, field: "MaxConcurrentScrapes",
		read: func(c *appconfig.Config) any { return c.MaxConcurrentScrapes },
	},
	{
		flag: CLIEnableExporterMetrics, key: "observability.metrics.enabled", value: true, field: "EnableExporterMetrics",
		read: func(c *appconfig.Config) any { return c.EnableExporterMetrics },
	},
	{
		flag: CLIXIDCountWindowSize, key: "metrics[0].overrides[0].window", value: "1m",
		extra: counterWindow("DCGM_EXP_XID_ERRORS_COUNT"), field: "XIDCountWindowSize",
		read: func(c *appconfig.Config) any { return c.XIDCountWindowSize },
	},
	{flag: CLIReplaceBlanksInModelName, flagOnlyReason: "flag only; no config key"},
	{
		flag: CLIDebugMode, key: "observability.logs.level", value: "debug", field: "Debug",
		read: func(c *appconfig.Config) any { return c.Debug },
	},
	{
		flag: CLIClockEventsCountWindowSize, key: "metrics[0].overrides[0].window", value: "1m",
		extra: counterWindow("DCGM_EXP_CLOCK_EVENTS_COUNT"), field: "ClockEventsCountWindowSize",
		read: func(c *appconfig.Config) any { return c.ClockEventsCountWindowSize },
	},
	{
		flag: CLIEnableDCGMLog, key: "sources.dcgm.embedded_log", value: map[string]any{"file": "stdout"},
		field: "EnableDCGMLog",
		read:  func(c *appconfig.Config) any { return c.EnableDCGMLog },
	},
	{
		flag: CLIDCGMLogLevel, key: "sources.dcgm.embedded_log.level", value: "debug", field: "DCGMLogLevel",
		read: func(c *appconfig.Config) any { return c.DCGMLogLevel },
	},
	{
		flag: CLILogFormat, key: "observability.logs.format", value: "json", field: "LogFormat",
		read: func(c *appconfig.Config) any { return c.LogFormat },
	},
	{
		flag: CLIPodResourcesKubeletSocket, key: "sources.kubernetes.pod_resources_socket", value: "/run/kubelet.sock",
		field: "PodResourcesKubeletSocket",
		read:  func(c *appconfig.Config) any { return c.PodResourcesKubeletSocket },
	},
	{
		flag: CLIHPCJobMappingDir, key: "sources.hpc_jobs.mapping_dir", value: "/jobs", field: "HPCJobMappingDir",
		read: func(c *appconfig.Config) any { return c.HPCJobMappingDir },
	},
	{
		flag: CLIHPCJobMappingMaxFileBytes, key: "sources.hpc_jobs.max_file_bytes", value: 4096,
		field: "HPCJobMappingMaxFileBytes",
		read:  func(c *appconfig.Config) any { return c.HPCJobMappingMaxFileBytes },
	},
	{
		flag: CLIHPCJobMappingMaxJobIDBytes, key: "sources.hpc_jobs.max_job_id_bytes", value: 32,
		field: "HPCJobMappingMaxJobIDBytes",
		read:  func(c *appconfig.Config) any { return c.HPCJobMappingMaxJobIDBytes },
	},
	{
		flag: CLIHPCJobMappingMaxJobsPerFile, key: "sources.hpc_jobs.max_jobs_per_file", value: 8,
		field: "HPCJobMappingMaxJobsPerFile",
		read:  func(c *appconfig.Config) any { return c.HPCJobMappingMaxJobsPerFile },
	},
	{
		flag: CLIContainerLabels, key: "metrics[0].enrichment.container", value: true, field: "ContainerLabels",
		read: func(c *appconfig.Config) any { return c.ContainerLabels },
	},
	{
		flag: CLIContainerRuntimeSocket, key: "sources.container.runtime_socket", value: "/run/containerd.sock",
		field: "ContainerRuntimeSocket",
		read:  func(c *appconfig.Config) any { return c.ContainerRuntimeSocket },
	},
	{
		flag: CLINvidiaResourceNames, key: "sources.kubernetes.additional_resource_names",
		value: []string{"nvidia.com/a100"}, field: "NvidiaResourceNames",
		read: func(c *appconfig.Config) any { return c.NvidiaResourceNames },
	},
	{
		flag: CLIKubernetesVirtualGPUs, key: "sources.kubernetes.virtual_gpus", value: true, field: "KubernetesVirtualGPUs",
		read: func(c *appconfig.Config) any { return c.KubernetesVirtualGPUs },
	},
	{
		flag: CLIDumpEnabled, key: "debug_dump.enabled", value: true, field: "DumpConfig.Enabled",
		read: func(c *appconfig.Config) any { return c.DumpConfig.Enabled },
	},
	{
		flag: CLIDumpDirectory, key: "debug_dump.directory", value: "/dumps", field: "DumpConfig.Directory",
		read: func(c *appconfig.Config) any { return c.DumpConfig.Directory },
	},
	{
		flag: CLIDumpRetention, key: "debug_dump.retention", value: "5h", field: "DumpConfig.Retention",
		read: func(c *appconfig.Config) any { return c.DumpConfig.Retention },
	},
	{
		flag: CLIDumpCompression, key: "debug_dump.compression", value: false, field: "DumpConfig.Compression",
		read: func(c *appconfig.Config) any { return c.DumpConfig.Compression },
	},
	{
		flag: CLIDumpMinInterval, key: "debug_dump.min_interval", value: "30s", field: "DumpConfig.MinInterval",
		read: func(c *appconfig.Config) any { return c.DumpConfig.MinInterval.String() },
	},
	{
		flag: CLIDumpMaxFileBytes, key: "debug_dump.max_file_bytes", value: int64(1024), field: "DumpConfig.MaxFileBytes",
		read: func(c *appconfig.Config) any { return c.DumpConfig.MaxFileBytes },
	},
	{
		flag: CLIDumpMaxTotalBytes, key: "debug_dump.max_total_bytes", value: int64(1 << 30), field: "DumpConfig.MaxTotalBytes",
		read: func(c *appconfig.Config) any { return c.DumpConfig.MaxTotalBytes },
	},
	{
		flag: CLIDumpMaxFiles, key: "debug_dump.max_files", value: int64(7), field: "DumpConfig.MaxFiles",
		read: func(c *appconfig.Config) any { return c.DumpConfig.MaxFiles },
	},
	{
		flag: CLIKubernetesEnableDRA, key: "metrics[0].enrichment.dra", value: true, field: "KubernetesEnableDRA",
		top:  map[string]any{"sources": map[string]any{"dra": map[string]any{"mode": "kubernetes"}}},
		read: func(c *appconfig.Config) any { return c.KubernetesEnableDRA },
	},
	{flag: CLIDisableStartupValidate, flagOnlyReason: "per-run escape hatch"},
	{
		flag: CLIEnableGPUBindUnbindWatch, key: "sources.dcgm.detect_bind_unbind.enabled", value: true,
		field: "EnableGPUBindUnbindWatch",
		read:  func(c *appconfig.Config) any { return c.EnableGPUBindUnbindWatch },
	},
	{
		flag: CLIGPUBindUnbindPollInterval, key: "sources.dcgm.detect_bind_unbind.every", value: "2s",
		field: "GPUBindUnbindPollInterval",
		read:  func(c *appconfig.Config) any { return c.GPUBindUnbindPollInterval },
	},
	{
		flag: CLIDCGMCleanupTimeout, key: appconfig.DCGMCleanupTimeoutKey, value: "90s",
		field: appconfig.FieldDCGMCleanupTimeout,
		read:  func(c *appconfig.Config) any { return c.DCGMCleanupTimeout },
	},
	{
		flag: CLIEnablePprof, key: "server.profiling", value: true, field: "EnablePprof",
		read: func(c *appconfig.Config) any { return c.EnablePprof },
	},
}

// rootFlagNames lists the root flags NewApp registers, without the help and
// version flags that the CLI library adds itself.
func rootFlagNames() []string {
	var names []string
	for _, f := range NewApp().Flags {
		if name := f.Names()[0]; name != "help" && name != "version" {
			names = append(names, name)
		}
	}
	return names
}

// yamlDocumentForKey builds a version 2 document that sets key to value. A
// metrics key gets the entry name, file and output that the entry needs, and
// extra holds sibling keys that must sit beside the key.
func yamlDocumentForKey(t *testing.T, key string, value any, extra map[string]any) string {
	t.Helper()
	segments := strings.Split(key, ".")
	root := map[string]any{"version": 2}
	leaf := map[string]any{segments[len(segments)-1]: value}
	for name, v := range extra {
		leaf[name] = v
	}
	if segments[0] == "outputs" {
		leaf["type"], leaf["schema"] = "prometheus", "dcgm/v1"
	}
	var node any = leaf
	for i := len(segments) - 2; i >= 0; i-- {
		name, isList := strings.CutSuffix(segments[i], "[0]")
		if isList {
			node = []any{node}
		}
		if i == 0 {
			root[name] = node
			break
		}
		node = map[string]any{name: node}
	}
	if segments[0] == "metrics[0]" {
		entry := root["metrics"].([]any)[0].(map[string]any)
		entry["name"] = "gpu"
		if _, ok := entry["file"]; !ok {
			entry["file"] = "/a.csv"
		}
		root["outputs"] = map[string]any{"prometheus": map[string]any{"type": "prometheus", "schema": "dcgm/v1"}}
	}
	data, err := json.Marshal(root)
	require.NoError(t, err)
	return string(data)
}

func TestEveryRootFlagHasAVersion2KeyOrReason(t *testing.T) {
	flags := rootFlagNames()
	mapped := map[string]flagKeyMapping{}
	for _, mapping := range flagKeyMappings {
		assert.NotContains(t, mapped, mapping.flag, "duplicate row for --%s", mapping.flag)
		mapped[mapping.flag] = mapping
		assert.NotEqual(t, mapping.key == "", mapping.flagOnlyReason == "",
			"--%s needs exactly one of a key and a flag-only reason", mapping.flag)
	}

	for _, flag := range flags {
		assert.Contains(t, mapped, flag, "--%s has no version 2 key and no flag-only reason; add it to flagKeyMappings", flag)
	}
	var stale []string
	for flag := range mapped {
		// The systemd socket flag is registered on Linux only.
		if flag == CLIWebSystemdSocket && runtime.GOOS != "linux" {
			continue
		}
		if !slices.Contains(flags, flag) {
			stale = append(stale, flag)
		}
	}
	sort.Strings(stale)
	assert.Empty(t, stale, "rows for flags that no longer exist")
}

// Each mapped key, given a valid value, changes the Config field its flag sets
// and is recorded as that field's origin.
func TestMappedVersion2KeysSetTheFlagsSetting(t *testing.T) {
	for _, mapping := range flagKeyMappings {
		if mapping.key == "" {
			continue
		}

		t.Run(mapping.flag, func(t *testing.T) {
			require.NotNil(t, mapping.read, "--%s needs a value and a field reader", mapping.flag)
			document := yamlDocumentForKey(t, mapping.key, mapping.value, mapping.extra)
			if mapping.top != nil {
				var root map[string]any
				require.NoError(t, json.Unmarshal([]byte(document), &root))
				for name, v := range mapping.top {
					root[name] = v
				}

				data, err := json.Marshal(root)
				require.NoError(t, err)
				document = string(data)
			}

			y, err := appconfig.ParseYAMLConfig([]byte(document))
			require.NoError(t, err)

			before := appconfig.DefaultConfig()
			after := appconfig.DefaultConfig()
			require.NoError(t, y.ApplyTo(after))
			assert.NotEqual(t, mapping.read(before), mapping.read(after), "%s did not change %s", mapping.key, mapping.field)
			assert.Equal(t, mapping.key, y.SettingOrigins()[mapping.field])
		})
	}
}

// The device selector flags and the entity rows describe the same selections:
// for each row the flag-parsed options equal the options the entities become.
// The one exception is the flag value g+i (all GPUs plus all GPU instances, or
// all switches or CPUs plus all links or cores). On switches and CPUs its entity
// row is all indices and all links or cores, the same as the flag value f (the
// flexible selector), so it becomes the flexible selector. The check the flag
// makes that each listed entity exists is not reproduced.
func TestDeviceSelectorsMatchEntities(t *testing.T) {
	tests := []struct {
		selector  string
		gpu       string
		switches  string
		cpu       string
		allIsFlex bool
	}{
		{
			selector: "f",
			gpu:      "{policy: flex}",
			switches: "{indices: all, links: all}",
			cpu:      "{indices: all, cores: all}",
		},
		{
			selector: "g",
			gpu:      "{policy: explicit, gpus: all, instances: none}",
			switches: "{indices: all, links: none}",
			cpu:      "{indices: all, cores: none}",
		},
		{
			selector: "i",
			gpu:      "{policy: explicit, gpus: none, instances: all}",
			switches: "{indices: none, links: all}",
			cpu:      "{indices: none, cores: all}",
		},
		{
			selector:  "g+i",
			gpu:       "{policy: explicit, gpus: all, instances: all}",
			switches:  "{indices: all, links: all}",
			cpu:       "{indices: all, cores: all}",
			allIsFlex: true,
		},
		{
			selector: "g:0,2-4",
			gpu:      "{policy: explicit, gpus: [0, 2, 3, 4], instances: none}",
			switches: "{indices: [0, 2, 3, 4], links: none}",
			cpu:      "{indices: [0, 2, 3, 4], cores: none}",
		},
		{
			selector: "i:1",
			gpu:      "{policy: explicit, gpus: none, instances: [1]}",
			switches: "{indices: none, links: [1]}",
			cpu:      "{indices: none, cores: [1]}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.selector, func(t *testing.T) {
			flagOptions, err := parseDeviceOptions(tt.selector)
			require.NoError(t, err)

			y, err := appconfig.ParseYAMLConfig([]byte(fmt.Sprintf(`
version: 2
metrics:
  - name: gpu
    file: /a.csv
    entities:
      gpu: %s
      switch: %s
      cpu: %s
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`, tt.gpu, tt.switches, tt.cpu)))

			require.NoError(t, err)
			config := appconfig.DefaultConfig()
			require.NoError(t, y.ApplyTo(config))

			assert.Equal(t, flagOptions, config.GPUDeviceOptions)
			if tt.allIsFlex {
				flex := appconfig.DeviceOptions{Flex: true}
				assert.Equal(t, flex, config.SwitchDeviceOptions)
				assert.Equal(t, flex, config.CPUDeviceOptions)
				return
			}

			assert.Equal(t, flagOptions, config.SwitchDeviceOptions)
			assert.Equal(t, flagOptions, config.CPUDeviceOptions)
		})
	}
}

// The YAML level names map to levels the --dcgm-log-level flag accepts.
func TestEmbeddedLogLevelsAreFlagLevels(t *testing.T) {
	for _, level := range []string{"none", "fatal", "error", "warning", "info", "debug", "verbose"} {
		y, err := appconfig.ParseYAMLConfig([]byte("version: 2\nsources: {dcgm: {embedded_log: {level: " + level + "}}}\n"))
		require.NoError(t, err)
		config := appconfig.DefaultConfig()
		require.NoError(t, y.ApplyTo(config))
		assert.Contains(t, DCGMDbgLvlValues, config.DCGMLogLevel, level)
	}
}

// An index list accepts as many entries as the device selector flags expand to.
func TestEntityIndexCapMatchesSelectorLimit(t *testing.T) {
	indices := func(n int) string {
		values := make([]string, n)
		for i := range values {
			values[i] = strconv.Itoa(i)
		}
		return "[" + strings.Join(values, ",") + "]"
	}
	document := func(n int) string {
		return "version: 2\nmetrics:\n  - {name: gpu, file: /a.csv, entities: {cpu: {indices: " + indices(n) + "}}}\n" +
			"outputs:\n  prometheus: {type: prometheus, schema: dcgm/v1}\n"
	}
	limit := int(dcgm.MAX_NUM_CPU_CORES)
	_, err := appconfig.ParseYAMLConfig([]byte(document(limit)))
	require.NoError(t, err)
	_, err = appconfig.ParseYAMLConfig([]byte(document(limit + 1)))
	require.ErrorContains(t, err, "may contain at most")
}

// resolveFile writes a YAML file and resolves the config for it plus extra
// arguments, with every flag environment variable cleared first.
func resolveFile(t *testing.T, yamlText string, args ...string) (*appconfig.Resolved, error) {
	t.Helper()
	unsetFlagEnvVars(t, NewApp().Flags)
	return resolveFileAt(t, writeConfigFile(t, yamlText), args...)
}

// writeConfigFile writes YAML text to a new config file and returns its path.
func writeConfigFile(t *testing.T, yamlText string) string {
	t.Helper()
	configFile := filepath.Join(t.TempDir(), "dcgm-exporter.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte(yamlText), 0o600))
	return configFile
}

// resolveFileAt resolves the config for an existing file plus extra arguments.
// Environment variables are the caller's to set.
func resolveFileAt(t *testing.T, configFile string, args ...string) (*appconfig.Resolved, error) {
	t.Helper()
	var resolved *appconfig.Resolved
	app := NewApp()
	app.Action = func(ctx *cli.Context) error {
		var err error
		resolved, err = resolveConfig(ctx, "", nil, true)
		return err
	}

	err := app.Run(append([]string{"dcgm-exporter", "--config-file", configFile}, args...))
	return resolved, err
}

// YAML is the lowest layer for the keys that set the log format, listen
// address, debug level and read timeout; environment and argv override it in
// that order. Every setting also reports which layer won.
func TestFlagKeyPrecedenceYAMLThenEnvThenArgv(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	configFile := writeConfigFile(t, `
version: 2
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1, listen: ":9500"}
observability:
  logs: {format: json, level: debug}
server: {read_timeout: 5s}
`)

	type outcome struct {
		address, format, timeout string
		debug                    bool
		origins                  map[string]string
	}

	yamlOrigins := map[string]string{
		"Address":        "YAML: outputs.prometheus.listen",
		"LogFormat":      "YAML: observability.logs.format",
		"Debug":          "YAML: observability.logs.level",
		"WebReadTimeout": "YAML: server.read_timeout",
	}

	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want outcome
	}{
		{
			name: "YAML only",
			want: outcome{address: ":9500", format: "json", debug: true, timeout: "5s", origins: yamlOrigins},
		},
		{
			name: "environment beats YAML",
			env: map[string]string{
				"DCGM_EXPORTER_LISTEN":           ":9600",
				"DCGM_EXPORTER_LOG_FORMAT":       "text",
				"DCGM_EXPORTER_DEBUG":            "false",
				"DCGM_EXPORTER_WEB_READ_TIMEOUT": "6s",
			},
			want: outcome{
				address: ":9600", format: "text", debug: false, timeout: "6s",
				origins: map[string]string{
					"Address":        "environment: DCGM_EXPORTER_LISTEN",
					"LogFormat":      "environment: DCGM_EXPORTER_LOG_FORMAT",
					"Debug":          "environment: DCGM_EXPORTER_DEBUG",
					"WebReadTimeout": "environment: DCGM_EXPORTER_WEB_READ_TIMEOUT",
				},
			},
		},
		{
			name: "argv beats environment and YAML",
			env:  map[string]string{"DCGM_EXPORTER_LISTEN": ":9600"},
			args: []string{"-a", ":9700", "--log-format", "text", "--debug=false", "--web-read-timeout", "7s"},
			want: outcome{
				address: ":9700", format: "text", debug: false, timeout: "7s",
				origins: map[string]string{
					"Address":        "argv: --address",
					"LogFormat":      "argv: --log-format",
					"Debug":          "argv: --debug",
					"WebReadTimeout": "argv: --web-read-timeout",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			resolved, err := resolveFileAt(t, configFile, tc.args...)
			require.NoError(t, err)
			assert.Equal(t, tc.want.address, resolved.Config.Address)
			assert.Equal(t, tc.want.format, resolved.Config.LogFormat)
			assert.Equal(t, tc.want.debug, resolved.Config.Debug)
			assert.Equal(t, tc.want.timeout, resolved.Config.WebReadTimeout.String())
			for field, origin := range tc.want.origins {
				assert.Equal(t, origin, resolved.Origin(field), field)
			}
		})
	}
}

// Entities, hostengine, web config file, embedded log and the dump directory
// follow the same layers as the other keys.
func TestFlagKeyPrecedenceForEntitiesAndOtherPaths(t *testing.T) {
	unsetFlagEnvVars(t, NewApp().Flags)
	configFile := writeConfigFile(t, `
version: 2
metrics:
  - name: gpu
    file: /a.csv
    entities:
      gpu: {policy: explicit, gpus: [0]}
      switch: {indices: [0], links: none}
      cpu: {indices: [0], cores: none}
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
server: {web_config_file: /yaml-web.yml}
sources:
  dcgm: {hostengine: "yaml-host:1"}
debug_dump: {directory: /yaml-dumps}
`)

	type outcome struct {
		gpu, switches, cpu           appconfig.DeviceOptions
		hostEngine, webConfig, dumps string
		origins                      map[string]string
	}

	yamlOrigins := map[string]string{
		"GPUDeviceOptions":     "YAML: metrics[0].entities.gpu",
		"SwitchDeviceOptions":  "YAML: metrics[0].entities.switch",
		"CPUDeviceOptions":     "YAML: metrics[0].entities.cpu",
		"RemoteHEInfo":         "YAML: sources.dcgm.hostengine",
		"WebConfigFile":        "YAML: server.web_config_file",
		"DumpConfig.Directory": "YAML: debug_dump.directory",
	}

	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want outcome
	}{
		{
			name: "YAML only",
			want: outcome{
				gpu:        appconfig.DeviceOptions{MajorRange: []int{0}},
				switches:   appconfig.DeviceOptions{MajorRange: []int{0}},
				cpu:        appconfig.DeviceOptions{MajorRange: []int{0}},
				hostEngine: "yaml-host:1", webConfig: "/yaml-web.yml", dumps: "/yaml-dumps",
				origins: yamlOrigins,
			},
		},
		{
			name: "environment beats YAML",
			env: map[string]string{
				"DCGM_EXPORTER_DEVICES_STR":       "f",
				"DCGM_EXPORTER_OTHER_DEVICES_STR": "g",
				"DCGM_EXPORTER_CPU_DEVICES_STR":   "i",
				"DCGM_REMOTE_HOSTENGINE_INFO":     "env-host:2",
				"DCGM_EXPORTER_WEB_CONFIG_FILE":   "/env-web.yml",
				"DCGM_EXPORTER_DUMP_DIRECTORY":    "/env-dumps",
			},
			want: outcome{
				gpu:        appconfig.DeviceOptions{Flex: true},
				switches:   appconfig.DeviceOptions{MajorRange: []int{-1}},
				cpu:        appconfig.DeviceOptions{MinorRange: []int{-1}},
				hostEngine: "env-host:2", webConfig: "/env-web.yml", dumps: "/env-dumps",
				origins: map[string]string{
					"GPUDeviceOptions":     "environment: DCGM_EXPORTER_DEVICES_STR",
					"SwitchDeviceOptions":  "environment: DCGM_EXPORTER_OTHER_DEVICES_STR",
					"CPUDeviceOptions":     "environment: DCGM_EXPORTER_CPU_DEVICES_STR",
					"RemoteHEInfo":         "environment: DCGM_REMOTE_HOSTENGINE_INFO",
					"WebConfigFile":        "environment: DCGM_EXPORTER_WEB_CONFIG_FILE",
					"DumpConfig.Directory": "environment: DCGM_EXPORTER_DUMP_DIRECTORY",
				},
			},
		},
		{
			name: "argv beats environment and YAML",
			env:  map[string]string{"DCGM_EXPORTER_DEVICES_STR": "g"},
			args: []string{
				"-d", "f", "-s", "g", "-p", "i", "-r", "argv-host:3",
				"--web-config-file", "/argv-web.yml", "--dump-directory", "/argv-dumps",
			},
			want: outcome{
				gpu:        appconfig.DeviceOptions{Flex: true},
				switches:   appconfig.DeviceOptions{MajorRange: []int{-1}},
				cpu:        appconfig.DeviceOptions{MinorRange: []int{-1}},
				hostEngine: "argv-host:3", webConfig: "/argv-web.yml", dumps: "/argv-dumps",
				origins: map[string]string{
					"GPUDeviceOptions":     "argv: --devices",
					"SwitchDeviceOptions":  "argv: --switch-devices",
					"CPUDeviceOptions":     "argv: --cpu-devices",
					"RemoteHEInfo":         "argv: --remote-hostengine-info",
					"WebConfigFile":        "argv: --web-config-file",
					"DumpConfig.Directory": "argv: --dump-directory",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			resolved, err := resolveFileAt(t, configFile, tc.args...)
			require.NoError(t, err)
			config := resolved.Config
			assert.Equal(t, tc.want.gpu, config.GPUDeviceOptions)
			assert.Equal(t, tc.want.switches, config.SwitchDeviceOptions)
			assert.Equal(t, tc.want.cpu, config.CPUDeviceOptions)
			assert.Equal(t, tc.want.hostEngine, config.RemoteHEInfo)
			assert.Equal(t, tc.want.webConfig, config.WebConfigFile)
			assert.Equal(t, tc.want.dumps, config.DumpConfig.Directory)
			for field, origin := range tc.want.origins {
				assert.Equal(t, origin, resolved.Origin(field), field)
			}
		})
	}

	t.Run("embedded log from argv", func(t *testing.T) {
		resolved, err := resolveFile(t, "version: 2\n", "--enable-dcgm-log", "--dcgm-log-level", "INFO")
		require.NoError(t, err)
		assert.True(t, resolved.Config.EnableDCGMLog)
		assert.Equal(t, "INFO", resolved.Config.DCGMLogLevel)
		assert.Equal(t, "argv: --enable-dcgm-log", resolved.Origin("EnableDCGMLog"))
		assert.Equal(t, "argv: --dcgm-log-level", resolved.Origin("DCGMLogLevel"))
	})

	t.Run("embedded log from YAML, level from argv", func(t *testing.T) {
		resolved, err := resolveFile(t,
			"version: 2\nsources: {dcgm: {embedded_log: {level: debug}}}\n", "--dcgm-log-level", "INFO")

		require.NoError(t, err)
		assert.Equal(t, "YAML: sources.dcgm.embedded_log", resolved.Origin("EnableDCGMLog"))
		assert.Equal(t, "INFO", resolved.Config.DCGMLogLevel)
		assert.Equal(t, "argv: --dcgm-log-level", resolved.Origin("DCGMLogLevel"))
	})
}

// Settings that cannot be used together, or are out of range, are rejected on
// the resolved config, so the source of each setting does not matter. The
// error names where each value came from.
func TestCrossSourceSettingChecks(t *testing.T) {
	const (
		profiling   = "version: 2\nserver: {profiling: true}\n"
		embeddedLog = "version: 2\nsources: {dcgm: {embedded_log: {level: info}}}\n"
		bothInYAML  = "version: 2\nsources: {dcgm: {hostengine: 'h:1', embedded_log: {level: info}}}\n"
		plain       = "version: 2\n"
	)
	tests := []struct {
		name    string
		yaml    string
		env     map[string]string
		args    []string
		wantErr string
	}{
		{
			name: "profiling in YAML without a web config file", yaml: profiling,
			wantErr: "enable-pprof requires web-config-file so profiling endpoints are protected by exporter-toolkit " +
				"auth or TLS; enable-pprof is set by YAML: server.profiling",
		},
		{name: "profiling in YAML, web config file on argv", yaml: profiling, args: []string{"--web-config-file", "/w.yml"}},
		{
			name: "embedded log in YAML, remote hostengine on argv", yaml: embeddedLog, args: []string{"-r", "localhost:5555"},
			wantErr: "enable-dcgm-log applies only to the embedded hostengine and cannot be used with " +
				"remote-hostengine-info; set by YAML: sources.dcgm.embedded_log and argv: --remote-hostengine-info",
		},
		{
			name: "embedded log and remote hostengine on argv", yaml: plain,
			args:    []string{"--enable-dcgm-log", "-r", "localhost:5555"},
			wantErr: "set by argv: --enable-dcgm-log and argv: --remote-hostengine-info",
		},
		{
			name: "embedded log and remote hostengine from the environment", yaml: plain,
			env: map[string]string{
				"DCGM_REMOTE_HOSTENGINE_INFO":   "localhost:5555",
				"DCGM_EXPORTER_ENABLE_DCGM_LOG": "true",
			},
			wantErr: "set by environment: DCGM_EXPORTER_ENABLE_DCGM_LOG and environment: DCGM_REMOTE_HOSTENGINE_INFO",
		},
		{
			name: "embedded log and hostengine both in YAML", yaml: bothInYAML,
			wantErr: "set by YAML: sources.dcgm.embedded_log and YAML: sources.dcgm.hostengine",
		},
		{name: "embedded log alone", yaml: embeddedLog},
		{
			name: "xid window zero on argv", yaml: plain, args: []string{"-x", "0"},
			wantErr: "invalid xid-count-window-size parameter value: 0, must be greater than 0; set by argv: --xid-count-window-size",
		},
		{name: "xid window one on argv", yaml: plain, args: []string{"-x", "1"}},
		{
			name: "xid window zero from the environment", yaml: plain,
			env:     map[string]string{"DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE": "0"},
			wantErr: "set by environment: DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE",
		},
		{
			name: "clock events window zero from the environment", yaml: plain,
			env:     map[string]string{"DCGM_EXPORTER_CLOCK_EVENTS_COUNT_WINDOW_SIZE": "0"},
			wantErr: "invalid clock-events-count-window-size parameter value: 0",
		},
		{
			name: "collect interval beyond a duration on argv", yaml: plain,
			args:    []string{"--collect-interval", "9223372036855"},
			wantErr: "invalid collect-interval parameter value: 9223372036855, must be at most 9223372036854 milliseconds; set by argv: --collect-interval",
		},
		{name: "collect interval at the largest duration on argv", yaml: plain, args: []string{"--collect-interval", "9223372036854"}},
		{
			name: "xid window beyond a duration from the environment", yaml: plain,
			env:     map[string]string{"DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE": "9223372036855"},
			wantErr: "must be at most 9223372036854 milliseconds; set by environment: DCGM_EXPORTER_XID_COUNT_WINDOW_SIZE",
		},
		{
			name: "collect interval zero on argv", yaml: plain,
			args:    []string{"--collect-interval", "0"},
			wantErr: "invalid collect-interval parameter value: 0, must be greater than 0; set by argv: --collect-interval",
		},
		{
			name: "clock events window beyond a duration on argv", yaml: plain,
			args: []string{"--clock-events-count-window-size", "9223372036855"},
			wantErr: "invalid clock-events-count-window-size parameter value: 9223372036855, " +
				"must be at most 9223372036854 milliseconds; set by argv: --clock-events-count-window-size",
		},
		{
			name: "xid window at the largest duration on argv", yaml: plain,
			args: []string{"--xid-count-window-size", "9223372036854"},
		},
		{
			name: "clock events window negative on argv", yaml: plain,
			args:    []string{"--clock-events-count-window-size", "-5"},
			wantErr: "invalid clock-events-count-window-size parameter value: -5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetFlagEnvVars(t, NewApp().Flags)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			_, err := resolveFileAt(t, writeConfigFile(t, tt.yaml), tt.args...)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestYAMLWindowOfZeroIsRejectedByReader(t *testing.T) {
	_, err := resolveFile(t, `
version: 2
metrics:
  - name: gpu
    file: /a.csv
    overrides:
      - match: [DCGM_EXP_XID_ERRORS_COUNT]
        window: 0s
outputs:
  prometheus: {type: prometheus, schema: dcgm/v1}
`)
	require.ErrorContains(t, err, "metrics[0].overrides[0].window: must be greater than 0")
}

// resolveConfigAndLogger installs the logger the resolved config selects: a
// file can pick the format and level, argv wins over the file, and a config
// error leaves the logger alone.
func TestResolveConfigAndLoggerInstallsConfiguredHandler(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })

	run := func(t *testing.T, yamlText string, args ...string) error {
		t.Helper()
		unsetFlagEnvVars(t, NewApp().Flags)
		configFile := writeConfigFile(t, yamlText)
		app := NewApp()
		app.Action = func(ctx *cli.Context) error {
			_, err := resolveConfigAndLogger(context.Background(), ctx)
			return err
		}
		return app.Run(append([]string{"dcgm-exporter", "--config-file", configFile}, args...))
	}
	const logsInYAML = "version: 2\nobservability: {logs: {format: json, level: debug}}\n"

	t.Run("file selects JSON at debug level", func(t *testing.T) {
		slog.SetDefault(original)
		require.NoError(t, run(t, logsInYAML))
		handler := slog.Default().Handler()
		assert.IsType(t, &slog.JSONHandler{}, handler)
		assert.True(t, handler.Enabled(context.Background(), slog.LevelDebug))
	})

	t.Run("argv format wins over the file", func(t *testing.T) {
		slog.SetDefault(original)
		require.NoError(t, run(t, logsInYAML, "--log-format", "text"))
		handler := slog.Default().Handler()
		assert.IsType(t, &slog.TextHandler{}, handler)
		assert.True(t, handler.Enabled(context.Background(), slog.LevelDebug))
	})

	t.Run("bad config returns an error before a handler is installed", func(t *testing.T) {
		slog.SetDefault(original)
		before := slog.Default().Handler()
		err := run(t, logsInYAML, "-x", "0")
		require.ErrorContains(t, err, "xid-count-window-size")
		assert.Same(t, before, slog.Default().Handler())
	})
}

// Config warnings are logged with the installed handler, after it is set up.
func TestResolveConfigAndLoggerLogsWarningsWithConfiguredHandler(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	unsetFlagEnvVars(t, NewApp().Flags)

	// The installed JSON handler writes to stderr, so capture it.
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	originalStderr := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = originalStderr })

	configFile := writeConfigFile(t, "version: 1\n")
	app := NewApp()
	app.Action = func(ctx *cli.Context) error {
		_, err := resolveConfigAndLogger(context.Background(), ctx)
		return err
	}
	require.NoError(t, app.Run([]string{"dcgm-exporter", "--config-file", configFile, "--log-format", "json"}))
	require.NoError(t, writer.Close())
	output, err := io.ReadAll(reader)
	require.NoError(t, err)

	assert.Contains(t, string(output), `"level":"WARN"`)
	assert.Contains(t, string(output), appconfig.LegacyConfigVersionWarning)
}

// TestEveryRootFlagIsInspected fails when a flag is added to the app without a
// settings row, because inspection would then never show or explain it.
func TestEveryRootFlagIsInspected(t *testing.T) {
	inspected := map[string]bool{}
	for _, setting := range appconfig.SettingKeys(appconfig.DefaultConfig(), appconfig.ConfigNames{}) {
		inspected[setting.Flag] = true
	}

	// A ConfigMap source is shown under the flag that selects it.
	inspected[CLIConfigMapData] = true
	for _, name := range rootFlagNames() {
		switch name {
		case CLIConfigFile, "help", "version":
			continue
		}

		assert.True(t, inspected[name], "flag --%s has no settings row", name)
	}
}
