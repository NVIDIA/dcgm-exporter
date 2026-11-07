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

package appconfig

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every key that mirrors one flag sets the Config field that flag sets,
// and each one is recorded in the setting origins.
func TestV2FlagKeysSetConfigFields(t *testing.T) {
	y, config := parseAndApply(t, `
version: 2
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
    listen: ":9500"
server:
  read_timeout: 5s
  write_timeout: 20s
  systemd_socket: true
  web_config_file: /etc/web.yml
  health_require_gpus: true
  profiling: true
sources:
  dcgm:
    hostengine: "tcp://host:5555"
  kubernetes:
    pod_resources_socket: /run/kubelet.sock
    gpu_id_type: device-name
    additional_resource_names: [nvidia.com/a100]
    virtual_gpus: true
    pod_uid: true
    pod_labels: {enabled: true, allow: ["^app$", "team-.*"]}
  container:
    runtime_socket: /run/containerd.sock
  dra:
    mode: kubernetes
  hpc_jobs:
    mapping_dir: /jobs
observability:
  logs: {format: json, level: debug}
debug_dump:
  enabled: true
  directory: /dumps
  retention: 0h
  compression: false
`)
	assert.Equal(t, ":9500", config.Address)
	assert.Equal(t, 5*time.Second, config.WebReadTimeout)
	assert.Equal(t, 20*time.Second, config.WebWriteTimeout)
	assert.True(t, config.WebSystemdSocket)
	assert.Equal(t, "/etc/web.yml", config.WebConfigFile)
	assert.True(t, config.HealthRequireGPUs)
	assert.True(t, config.EnablePprof)
	assert.True(t, config.UseRemoteHE)
	assert.Equal(t, "tcp://host:5555", config.RemoteHEInfo)
	assert.Equal(t, "/run/kubelet.sock", config.PodResourcesKubeletSocket)
	assert.Equal(t, DeviceName, config.KubernetesGPUIdType)
	assert.Equal(t, []string{"nvidia.com/a100"}, config.NvidiaResourceNames)
	assert.True(t, config.KubernetesVirtualGPUs)
	assert.True(t, config.KubernetesEnablePodUID)
	assert.True(t, config.KubernetesEnablePodLabels)
	assert.Equal(t, []string{"^app$", "team-.*"}, config.KubernetesPodLabelAllowlistRegex)
	assert.Equal(t, "/run/containerd.sock", config.ContainerRuntimeSocket)
	assert.Equal(t, "/jobs", config.HPCJobMappingDir)
	assert.Equal(t, "json", config.LogFormat)
	assert.True(t, config.Debug)
	assert.Equal(t, DumpConfig{Enabled: true, Directory: "/dumps", Retention: 0, Compression: false}, config.DumpConfig)
	// Source settings never turn an enrichment on by themselves.
	assert.False(t, config.Kubernetes)
	assert.False(t, config.ContainerLabels)
	assert.False(t, config.KubernetesEnableDRA)

	assert.Equal(t, map[string]string{
		"Address":                          "outputs.prometheus.listen",
		"WebReadTimeout":                   "server.read_timeout",
		"WebWriteTimeout":                  "server.write_timeout",
		"WebSystemdSocket":                 "server.systemd_socket",
		"WebConfigFile":                    "server.web_config_file",
		"HealthRequireGPUs":                "server.health_require_gpus",
		"EnablePprof":                      "server.profiling",
		"UseRemoteHE":                      "sources.dcgm.hostengine",
		"RemoteHEInfo":                     "sources.dcgm.hostengine",
		"PodResourcesKubeletSocket":        "sources.kubernetes.pod_resources_socket",
		"KubernetesGPUIdType":              "sources.kubernetes.gpu_id_type",
		"NvidiaResourceNames":              "sources.kubernetes.additional_resource_names",
		"KubernetesVirtualGPUs":            "sources.kubernetes.virtual_gpus",
		"KubernetesEnablePodUID":           "sources.kubernetes.pod_uid",
		"KubernetesEnablePodLabels":        "sources.kubernetes.pod_labels.enabled",
		"KubernetesPodLabelAllowlistRegex": "sources.kubernetes.pod_labels.allow",
		"ContainerRuntimeSocket":           "sources.container.runtime_socket",
		"HPCJobMappingDir":                 "sources.hpc_jobs.mapping_dir",
		"LogFormat":                        "observability.logs.format",
		"Debug":                            "observability.logs.level",
		"DumpConfig.Enabled":               "debug_dump.enabled",
		"DumpConfig.Directory":             "debug_dump.directory",
		"DumpConfig.Retention":             "debug_dump.retention",
		"DumpConfig.Compression":           "debug_dump.compression",
	}, y.SettingOrigins())
}

// Keys the file leaves out keep the value Config already has, even when the
// block that holds them is present.
func TestV2FlagKeysOmittedKeepDefaults(t *testing.T) {
	_, config := parseAndApply(t, "version: 2\nsources: {kubernetes: {}, container: {}, hpc_jobs: {}}\n")
	assert.Equal(t, baseConfig(), config)
}

func TestV2ExplicitEmptyAndFalseFlagKeys(t *testing.T) {
	config := baseConfig()
	config.NvidiaResourceNames = []string{"x"}
	config.KubernetesVirtualGPUs = true
	config.Debug = true
	y, err := ParseYAMLConfig([]byte(`
version: 2
sources:
  kubernetes: {additional_resource_names: [], virtual_gpus: false}
observability: {logs: {level: info}}
`))
	require.NoError(t, err)
	require.NoError(t, y.ApplyTo(config))
	assert.Empty(t, config.NvidiaResourceNames)
	assert.False(t, config.KubernetesVirtualGPUs)
	assert.False(t, config.Debug)
	assert.Contains(t, y.SettingOrigins(), "NvidiaResourceNames")
}

func TestV2EmbeddedLog(t *testing.T) {
	levels := map[string]string{
		"none": "NONE", "fatal": "FATAL", "error": "ERROR", "warning": "WARN",
		"info": "INFO", "debug": "DEBUG", "verbose": "VERB",
	}
	for yamlLevel, want := range levels {
		t.Run(yamlLevel, func(t *testing.T) {
			y, config := parseAndApply(t,
				"version: 2\nsources: {dcgm: {embedded_log: {level: "+yamlLevel+", file: stdout}}}\n")
			assert.True(t, config.EnableDCGMLog)
			assert.Equal(t, want, config.DCGMLogLevel)
			assert.Equal(t, "sources.dcgm.embedded_log", y.SettingOrigins()["EnableDCGMLog"])
			assert.Equal(t, "sources.dcgm.embedded_log.level", y.SettingOrigins()["DCGMLogLevel"])
		})
	}
	_, config := parseAndApply(t, "version: 2\nsources: {dcgm: {embedded_log: {}}}\n")
	assert.True(t, config.EnableDCGMLog)
	assert.Empty(t, config.DCGMLogLevel)
}

func TestV2HostEngineEmptyMeansEmbedded(t *testing.T) {
	_, config := parseAndApply(t, "version: 2\nsources: {dcgm: {hostengine: \"\"}}\n")
	assert.False(t, config.UseRemoteHE)
}

func TestV2FlagKeyRejections(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"read timeout zero", "server: {read_timeout: 0s}", "server.read_timeout: must be greater than 0"},
		{"read timeout text", "server: {read_timeout: soon}", `server.read_timeout: invalid duration "soon" (use a number with a unit, such as 30s)`},
		{
			"read timeout too large", "server: {read_timeout: 99999999999999999999s}",
			`server.read_timeout: invalid duration "99999999999999999999s" (use a number with a unit, such as 30s)`,
		},
		{"write timeout negative", "server: {write_timeout: -1s}", "server.write_timeout: must be greater than 0"},
		{"embedded log file", "sources: {dcgm: {embedded_log: {file: /tmp/x}}}", "sources.dcgm.embedded_log.file"},
		{"embedded log level", "sources: {dcgm: {embedded_log: {level: warn}}}", "sources.dcgm.embedded_log.level"},
		{"log level", "observability: {logs: {level: warn}}", "observability.logs.level"},
		{"log format", "observability: {logs: {format: yaml}}", "observability.logs.format"},
		{"dra mode file", "sources: {dra: {mode: file}}", "sources.dra.mode"},
		{"gpu id type", "sources: {kubernetes: {gpu_id_type: name}}", "sources.kubernetes.gpu_id_type"},
		{
			"resource name empty", "sources: {kubernetes: {additional_resource_names: ['']}}",
			"sources.kubernetes.additional_resource_names[0]",
		},
		{"resource_names not accepted", "sources: {kubernetes: {resource_names: [x]}}", "sources.kubernetes.resource_names: unknown key"},
		{
			"regex", "sources: {kubernetes: {pod_labels: {allow: ['(']}}}",
			"sources.kubernetes.pod_labels.allow[0]",
		},
		{
			"enrichment unknown switch", "metrics: [{name: gpu, file: /a.csv, enrichment: {slurm: true}}]\noutputs: {prometheus: {type: prometheus, schema: dcgm/v1}}",
			"metrics[0].enrichment.slurm: unknown key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, "version: 2\n"+tt.yaml+"\n", tt.want)
		})
	}
}

func TestV2ProfilingWithWebConfig(t *testing.T) {
	_, config := parseAndApply(t, "version: 2\nserver: {web_config_file: /w.yml, profiling: true}\n")
	assert.True(t, config.EnablePprof)
}

func TestV2EntityRows(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		field string
		want  DeviceOptions
	}{
		{"gpu flex", "gpu: {policy: flex}", "gpu", DeviceOptions{Flex: true}},
		{"gpu policy omitted is flex", "gpu: {}", "gpu", DeviceOptions{Flex: true}},
		{"gpu all", "gpu: {policy: explicit, gpus: all, instances: none}", "gpu", DeviceOptions{MajorRange: []int{-1}}},
		{"gpu instances", "gpu: {policy: explicit, gpus: none, instances: all}", "gpu", DeviceOptions{MinorRange: []int{-1}}},
		{
			"gpu both", "gpu: {policy: explicit, gpus: all, instances: all}", "gpu",
			DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{-1}},
		},
		{
			"gpu list", "gpu: {policy: explicit, gpus: [0, 2, 3, 4], instances: none}", "gpu",
			DeviceOptions{MajorRange: []int{0, 2, 3, 4}},
		},
		{"gpu instance list", "gpu: {policy: explicit, instances: [1]}", "gpu", DeviceOptions{MinorRange: []int{1}}},
		{
			"repeated indices keep first-seen order", "gpu: {policy: explicit, gpus: [3, 0, 3, 0], instances: [1, 1]}", "gpu",
			DeviceOptions{MajorRange: []int{3, 0}, MinorRange: []int{1}},
		},
		{"switch all", "switch: {indices: all, links: all}", "switch", DeviceOptions{Flex: true}},
		{"switch omitted lists mean all", "switch: {}", "switch", DeviceOptions{Flex: true}},
		{"switch indices only", "switch: {indices: all, links: none}", "switch", DeviceOptions{MajorRange: []int{-1}}},
		{"switch links only", "switch: {indices: none, links: all}", "switch", DeviceOptions{MinorRange: []int{-1}}},
		{
			"switch list", "switch: {indices: [0, 2], links: [1]}", "switch",
			DeviceOptions{MajorRange: []int{0, 2}, MinorRange: []int{1}},
		},
		{"cpu all", "cpu: {indices: all, cores: all}", "cpu", DeviceOptions{Flex: true}},
		{"cpu cores none", "cpu: {indices: [0, 1], cores: none}", "cpu", DeviceOptions{MajorRange: []int{0, 1}}},
		{
			"cpu missing indices means all", "cpu: {cores: [3]}", "cpu",
			DeviceOptions{MajorRange: []int{-1}, MinorRange: []int{3}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			y, config := parseAndApply(t, entitiesDocument(tt.yaml))
			got := map[string]DeviceOptions{
				"gpu": config.GPUDeviceOptions, "switch": config.SwitchDeviceOptions, "cpu": config.CPUDeviceOptions,
			}
			assert.Equal(t, tt.want, got[tt.field])
			assert.Equal(t, "metrics[0].entities."+tt.field, y.SettingOrigins()[map[string]string{
				"gpu": "GPUDeviceOptions", "switch": "SwitchDeviceOptions", "cpu": "CPUDeviceOptions",
			}[tt.field]])
		})
	}
}

func TestV2EntityOmittedKeepsDefaults(t *testing.T) {
	config := baseConfig()
	config.GPUDeviceOptions = DeviceOptions{Flex: true}
	y, err := ParseYAMLConfig([]byte(entitiesDocument("switch: {indices: [1], links: none}")))
	require.NoError(t, err)
	require.NoError(t, y.ApplyTo(config))
	assert.Equal(t, DeviceOptions{Flex: true}, config.GPUDeviceOptions)
}

func TestV2EntityRejections(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"flex with lists", "gpu: {policy: flex, gpus: all}", "metrics[0].entities.gpu: policy flex cannot set gpus or instances"},
		{"explicit without lists", "gpu: {policy: explicit}", "metrics[0].entities.gpu: policy explicit requires gpus and/or instances"},
		{"explicit selects nothing", "gpu: {policy: explicit, gpus: none, instances: none}", "metrics[0].entities.gpu: selects nothing"},
		{"policy value", "gpu: {policy: some}", "metrics[0].entities.gpu.policy"},
		{"unknown word", "gpu: {policy: explicit, gpus: most}", "metrics[0].entities.gpu.gpus: must be all, none or a list"},
		{"number", "gpu: {policy: explicit, gpus: 3}", "metrics[0].entities.gpu.gpus: must be all, none or a list"},
		{"empty list", "gpu: {policy: explicit, gpus: []}", "metrics[0].entities.gpu.gpus: must not be an empty list"},
		{"negative index", "switch: {indices: [-1]}", "metrics[0].entities.switch.indices[0]: must be a non-negative integer"},
		{"text index", "cpu: {cores: [a]}", "metrics[0].entities.cpu.cores[0]: must be a non-negative integer"},
		{"switch selects nothing", "switch: {indices: none, links: none}", "metrics[0].entities.switch: selects nothing"},
		{"unknown key", "gpu: {policy: flex, mig: all}", "metrics[0].entities.gpu.mig: unknown key"},
		{"unknown family", "dpu: {}", "metrics[0].entities.dpu: unknown key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireParseError(t, entitiesDocument(tt.yaml), tt.want)
		})
	}
}

func entitiesDocument(entities string) string {
	return "version: 2\nmetrics:\n  - name: gpu\n    file: /a.csv\n    entities: {" + entities + "}\n" + v2Outputs
}

// A file that sets every flag key in one document parses and applies. It leaves
// enrichment at its default (auto) and leaves out embedded_log, which turns
// the log on; other tests cover both.
func TestV2FileWithEveryFlagKeyApplies(t *testing.T) {
	_, config := parseAndApply(t, `
version: 2
metrics:
  - name: gpu
    file: /etc/dcgm-exporter/default-counters.csv
    every: 30s
    entities:
      gpu: {policy: flex}
      switch: {indices: all, links: all}
      cpu: {indices: all, cores: all}
outputs:
  prometheus:
    type: prometheus
    schema: dcgm/v1
    listen: ":9400"
sources:
  dcgm:
    hostengine: ""
    detect_bind_unbind: {enabled: false, every: 1s}
  kubernetes:
    pod_resources_socket: /var/lib/kubelet/pod-resources/kubelet.sock
    gpu_id_type: uid
    additional_resource_names: []
    virtual_gpus: false
    pod_uid: false
    pod_labels: {enabled: false, allow: []}
  container:
    runtime_socket: /var/run/docker.sock
  dra:
    mode: kubernetes
  hpc_jobs:
    mapping_dir: /var/lib/dcgm-exporter/job-mapping
server:
  read_timeout: 10s
  write_timeout: 30s
  max_concurrent_scrapes: 16
  systemd_socket: false
  web_config_file: ""
  profiling: false
observability:
  metrics: {enabled: false}
  logs: {format: text, level: info}
debug_dump:
  enabled: false
  directory: /tmp/dcgm-exporter-debug
  retention: 24h
  compression: true
`)
	assert.Equal(t, ":9400", config.Address)
	assert.True(t, config.GPUDeviceOptions.Flex)
	assert.Equal(t, "text", config.LogFormat)
}

// A file-only embedded_log block turns the log on and leaves the level alone.
func TestV2EmbeddedLogFileStdout(t *testing.T) {
	y, config := parseAndApply(t, "version: 2\nsources: {dcgm: {embedded_log: {file: stdout}}}\n")
	assert.True(t, config.EnableDCGMLog)
	assert.Empty(t, config.DCGMLogLevel)
	assert.NotContains(t, y.SettingOrigins(), "DCGMLogLevel")
}

// Each new block rejects keys it does not define, naming the path.
func TestV2NewBlocksRejectUnknownKeys(t *testing.T) {
	blocks := []struct{ name, yaml, want string }{
		{"server", "server: {bogus: 1}", "server.bogus"},
		{"debug_dump", "debug_dump: {bogus: 1}", "debug_dump.bogus"},
		{"observability.logs", "observability: {logs: {bogus: 1}}", "observability.logs.bogus"},
		{"sources.container", "sources: {container: {bogus: 1}}", "sources.container.bogus"},
		{"sources.hpc_jobs", "sources: {hpc_jobs: {bogus: 1}}", "sources.hpc_jobs.bogus"},
		{"sources.dra", "sources: {dra: {bogus: 1}}", "sources.dra.bogus"},
		{"sources.dcgm.embedded_log", "sources: {dcgm: {embedded_log: {bogus: 1}}}", "sources.dcgm.embedded_log.bogus"},
		{
			"outputs", "metrics: [{name: gpu, file: /a.csv}]\noutputs: {prometheus: {type: prometheus, schema: dcgm/v1, bogus: 1}}",
			"outputs.prometheus.bogus",
		},
	}
	for _, block := range blocks {
		t.Run(block.name, func(t *testing.T) {
			requireParseError(t, "version: 2\n"+block.yaml+"\n", block.want+": unknown key")
		})
	}
}

// TestV2RejectsKeysOutsideTheSchema checks that key names the version 2
// schema does not define are unknown keys, naming the path.
func TestV2RejectsKeysOutsideTheSchema(t *testing.T) {
	keys := []struct{ yaml, want string }{
		{"sources: {kubernetes: {enable_pod_uid: true}}", "sources.kubernetes.enable_pod_uid"},
		{"sources: {kubernetes: {enable_pod_labels: true}}", "sources.kubernetes.enable_pod_labels"},
		{"sources: {kubernetes: {pod_label_allowlist_regex: ['^app$']}}", "sources.kubernetes.pod_label_allowlist_regex"},
		{"sources: {kubernetes: {pod_labels: {limit: 5}}}", "sources.kubernetes.pod_labels.limit"},
		{"sources: {dcgm: {detect_bind_unbind: {poll_interval: 1s}}}", "sources.dcgm.detect_bind_unbind.poll_interval"},
		{"debug_dump: {retention_hours: 24}", "debug_dump.retention_hours"},
	}
	for _, key := range keys {
		t.Run(key.want, func(t *testing.T) {
			requireParseError(t, "version: 2\n"+key.yaml+"\n", key.want+": unknown key")
		})
	}
}

// TestV2EmptyValueChangesNothing checks that a key written with an empty value
// leaves its setting as it was and records no origin.
func TestV2EmptyValueChangesNothing(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"every", "sources: {dcgm: {detect_bind_unbind: {every: ''}}}"},
		{"retention", "debug_dump: {retention: ''}"},
		{"pod_uid", "sources: {kubernetes: {pod_uid: null}}"},
		{"pod_labels.enabled", "sources: {kubernetes: {pod_labels: {enabled: null}}}"},
		{"pod_labels.allow", "sources: {kubernetes: {pod_labels: {allow: null}}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			y, config := parseAndApply(t, "version: 2\n"+tc.yaml+"\n")

			assert.Equal(t, baseConfig(), config)
			assert.Empty(t, y.SettingOrigins())
		})
	}
}

// A relative web config file starts from the config file's directory.
func TestRelativeWebConfigFilePath(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	_, config := loadAndApply(t, write("rel.yaml", "version: 2\nserver: {web_config_file: web.yml}\n"))
	assert.Equal(t, filepath.Join(dir, "web.yml"), config.WebConfigFile)

	_, config = loadAndApply(t, write("abs.yaml", "version: 2\nserver: {web_config_file: /etc/web.yml}\n"))
	assert.Equal(t, "/etc/web.yml", config.WebConfigFile)

	_, config = loadAndApply(t, write("empty.yaml", "version: 2\nserver: {max_concurrent_scrapes: 4}\n"))
	assert.Empty(t, config.WebConfigFile)
}
