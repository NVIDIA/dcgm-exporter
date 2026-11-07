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
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fullTestConfig sets a value in every part of a Config that the full holds.
func fullTestConfig() *Config {
	age, samples := time.Minute, int64(7)
	return &Config{
		Address:         ":9500",
		CollectInterval: 1500,
		MetricSource: MetricSource{Kind: MetricSourceInline, Fields: []MetricField{
			{Name: "DCGM_FI_DEV_GPU_TEMP_CELSIUS", PrometheusType: "gauge", Help: "Temperature."},
		}},
		WatchRetention: WatchRetention{MaxAge: 10 * time.Minute, MaxSamples: 3},
		WatchGroups: []WatchGroup{
			{Name: "fast", Interval: 1000, Fields: []string{"DCGM_FI_DEV_GPU_*"}, ExtendedMatch: true},
			{
				Name: "kept", Interval: 2000, Fields: []string{"DCGM_FI_DEV_BOARD_POWER_WATTS"}, ExtendedMatch: true,
				Retention: WatchRetentionOverride{MaxAge: &age, MaxSamples: &samples},
			},
		},
		GPUDeviceOptions:    DeviceOptions{MajorRange: []int{0, 2}, MinorRange: []int{-1}},
		SwitchDeviceOptions: DeviceOptions{MajorRange: []int{-1}},
		CPUDeviceOptions:    DeviceOptions{Flex: true},

		UseRemoteHE: true, RemoteHEInfo: "host:5555",
		EnableGPUBindUnbindWatch: true, GPUBindUnbindPollInterval: 2 * time.Second,
		EnableDCGMLog: true, DCGMLogLevel: "WARN", DCGMCleanupTimeout: 90 * time.Second,

		PodResourcesKubeletSocket: "/run/kubelet.sock", KubernetesGPUIdType: DeviceName,
		NvidiaResourceNames: []string{"nvidia.com/mig"}, KubernetesVirtualGPUs: true,
		KubernetesEnablePodUID: true, KubernetesEnablePodLabels: true,
		KubernetesPodLabelAllowlistRegex: []string{"^app$"},
		ContainerRuntimeSocket:           "/run/containerd.sock",
		HPCJobMappingDir:                 "/jobs",
		HPCJobMappingMaxFileBytes:        4096,
		HPCJobMappingMaxJobIDBytes:       32,
		HPCJobMappingMaxJobsPerFile:      8,
		Kubernetes:                       true, ContainerLabels: true, KubernetesEnableDRA: true,
		EnrichmentModes: EnrichmentModes{
			Kubernetes: EnrichmentRequired, Container: EnrichmentAuto, DRA: EnrichmentAuto, HPCJobs: EnrichmentAuto,
		},

		WebReadTimeout: 5 * time.Second, WebWriteTimeout: time.Minute, MaxConcurrentScrapes: 4,
		WebSystemdSocket: true, WebConfigFile: "/etc/web.yml", HealthRequireGPUs: true, EnablePprof: true,
		EnableExporterMetrics: true, LogFormat: "json", Debug: true,
		XIDCountWindowSize: 60000, ClockEventsCountWindowSize: 90000,
		DumpConfig: DumpConfig{
			Enabled: true, Directory: "/dump", Retention: 90 * time.Minute, Compression: true,
			MinInterval: 30 * time.Second, MaxFileBytes: 1024, MaxTotalBytes: 4096, MaxFiles: 9,
		},
	}
}

// TestFullConfigLoadsBackAsTheSameSettings checks that full config loads back as the same settings.
func TestFullConfigLoadsBackAsTheSameSettings(t *testing.T) {
	original := fullTestConfig()
	text, err := yaml.Marshal(NewFullConfig(original, ConfigNames{}))
	require.NoError(t, err)

	parsed, err := ParseYAMLConfig(text)
	require.NoError(t, err, string(text))
	loaded := &Config{}
	require.NoError(t, parsed.ApplyTo(loaded))

	// Every field of the config, not only the full built from it, must come
	// back. Loading also records where each override came from in the file, which
	// the original does not have.
	for i := range loaded.WatchGroups {
		loaded.WatchGroups[i].Path = ""
	}

	loaded.WindowOverrides = nil
	assert.Equal(t, original, loaded)
	assert.Equal(t, NewFullConfig(original, ConfigNames{}), NewFullConfig(loaded, ConfigNames{}))
}

// TestFullConfigLoadsBackTheContainerSocketItWasGiven checks that full config loads back the container socket
// it was given.
func TestFullConfigLoadsBackTheContainerSocketItWasGiven(t *testing.T) {
	for _, tc := range []struct {
		name       string
		labels     bool
		socket     string
		wantSocket string
	}{
		{"labels off and no socket", false, "", ""},
		{"labels off and a socket", false, "/run/custom.sock", "/run/custom.sock"},
		{"labels on and no socket", true, "", defaultContainerRuntimeSocket},
		{"labels on and a socket", true, "/run/custom.sock", "/run/custom.sock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := DefaultConfig()
			original.ContainerLabels, original.ContainerRuntimeSocket = tc.labels, tc.socket
			original.ApplyDefaultContainerRuntimeSocket()
			text, err := yaml.Marshal(NewFullConfig(original, ConfigNames{}))
			require.NoError(t, err)

			parsed, err := ParseYAMLConfig(text)
			require.NoError(t, err, string(text))
			loaded := DefaultConfig()
			require.NoError(t, parsed.ApplyTo(loaded))
			assert.Equal(t, tc.labels, loaded.ContainerLabels)
			assert.Equal(t, tc.wantSocket, loaded.ContainerRuntimeSocket)
			assert.Equal(t, original.ContainerRuntimeSocket, loaded.ContainerRuntimeSocket, "the same setting comes back")
		})
	}
}

// TestContainerSocketFromAFileStillServesAFlagThatTurnsLabelsOn checks that container socket from a file still
// serves a flag that turns labels on.
func TestContainerSocketFromAFileStillServesAFlagThatTurnsLabelsOn(t *testing.T) {
	for socket, want := range map[string]string{"/run/custom.sock": "/run/custom.sock", defaultContainerRuntimeSocket: ""} {
		parsed, err := ParseYAMLConfig([]byte("version: 2\nmetrics:\n  - name: gpu\n    file: /tmp/a.csv\n    enrichment: {container: false}\n" +
			"sources:\n  container:\n    runtime_socket: " + socket + "\n" + v2Outputs))
		require.NoError(t, err)
		config := DefaultConfig()
		require.NoError(t, parsed.ApplyTo(config))
		assert.Equal(t, want, config.ContainerRuntimeSocket, "labels off keeps only a socket that is not the default")

		config.ContainerLabels = true
		config.ApplyDefaultContainerRuntimeSocket()
		assert.Equal(t, socket, config.ContainerRuntimeSocket, "a flag that turns labels on gets the file's socket or the default")
	}
}

// TestFullConfigWritesEveryKeyForAnEmptyConfig checks that full config writes every key for an empty config.
func TestFullConfigWritesEveryKeyForAnEmptyConfig(t *testing.T) {
	full := NewFullConfig(&Config{MetricSource: MetricSource{Kind: MetricSourceFile, File: "/a.csv"}}, ConfigNames{})
	text, err := yaml.Marshal(full)
	require.NoError(t, err)
	var generic map[string]any
	require.NoError(t, yaml.Unmarshal(text, &generic))
	for _, key := range []string{"version", "metrics", "outputs", "sources", "server", "observability", "debug_dump"} {
		assert.Contains(t, generic, key)
	}

	sources := generic["sources"].(map[string]any)
	assert.Equal(t, "", sources["dcgm"].(map[string]any)["hostengine"], "empty values are still written")
	assert.NotContains(t, sources["dcgm"], "embedded_log", "the embedded log block turns the log on")
	assert.Equal(t, []any{}, sources["kubernetes"].(map[string]any)["additional_resource_names"])
}

// TestFullConfigJSONUsesTheYAMLKeyNames checks that full config JSON uses the YAML key names.
func TestFullConfigJSONUsesTheYAMLKeyNames(t *testing.T) {
	data, err := json.Marshal(NewFullConfig(fullTestConfig(), ConfigNames{}))
	require.NoError(t, err)
	var generic map[string]any
	require.NoError(t, json.Unmarshal(data, &generic))
	assert.Equal(t, float64(2), generic["version"])
	assert.Equal(t, 4.0, generic["server"].(map[string]any)["max_concurrent_scrapes"])
	assert.Equal(t, "1.5s", generic["metrics"].([]any)[0].(map[string]any)["every"])
}

// TestConfigNamesAConfigMapSourceByTheDefaultFile checks that config names a config map source by the default
// file.
func TestConfigNamesAConfigMapSourceByTheDefaultFile(t *testing.T) {
	full := NewFullConfig(&Config{MetricSource: MetricSource{
		Kind: MetricSourceConfigMap, ConfigMap: ConfigMapMetricSource{Namespace: "n", Name: "m"},
	}}, ConfigNames{})

	assert.Equal(t, DefaultCollectorsFile, full.Metrics[0].File)
}

// TestFormatDuration checks durations are written the way a config file would.
func TestFormatDuration(t *testing.T) {
	for in, want := range map[time.Duration]string{
		30 * time.Second: "30s", 10 * time.Minute: "10m", time.Hour: "1h", 90 * time.Minute: "1h30m",
		1500 * time.Millisecond: "1.5s", 0: "0s", 100 * time.Minute: "1h40m",
	} {
		assert.Equal(t, want, FormatDuration(in))
	}
}

// yamlStructTypes lists every struct type reachable from t through pointers,
// slices, maps and fields, each once.
func yamlStructTypes(t reflect.Type, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct || seen[t] {
		return
	}

	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).IsExported() {
			yamlStructTypes(t.Field(i).Type, seen)
		}
	}
}

// TestJSONKeysMatchYAMLKeys fails when a field of the config types is added with
// a yaml key but a different or missing json key, which would change the JSON
// that config explain prints.
func TestJSONKeysMatchYAMLKeys(t *testing.T) {
	types := map[reflect.Type]bool{}
	yamlStructTypes(reflect.TypeOf(YAMLConfig{}), types)
	for typ := range types {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}

			assert.Equal(t, field.Tag.Get("yaml"), field.Tag.Get("json"), "%s.%s", typ.Name(), field.Name)
		}
	}
}

// TestFullConfigEnrichmentValues checks the enrichment values of the full config.
func TestFullConfigEnrichmentValues(t *testing.T) {
	full := NewFullConfig(&Config{
		Kubernetes: true, ContainerLabels: true,
		EnrichmentModes: EnrichmentModes{Kubernetes: EnrichmentRequired},
	}, ConfigNames{})

	enrichment := full.Metrics[0].Enrichment
	assert.Equal(t, YAMLEnrichmentMode("true"), *enrichment.Kubernetes)
	assert.Equal(t, YAMLEnrichmentMode("auto"), *enrichment.Container)
	assert.Equal(t, YAMLEnrichmentMode("false"), *enrichment.DRA)
	assert.Equal(t, YAMLEnrichmentMode("false"), *enrichment.HPCJobs)
}

// TestFullConfigKeepsTheNamesOfTheFile checks that full config keeps the names of the file.
func TestFullConfigKeepsTheNamesOfTheFile(t *testing.T) {
	parsed, err := ParseYAMLConfig([]byte("version: 2\nmetrics:\n  - name: mine\n    file: /a.csv\noutputs:\n  scrape: {type: prometheus, schema: dcgm/v1}\n"))
	require.NoError(t, err)
	assert.Equal(t, ConfigNames{Metrics: "mine", Output: "scrape"}, parsed.NamesOf())
	full := NewFullConfig(&Config{}, parsed.NamesOf())
	assert.Equal(t, "mine", full.Metrics[0].Name)
	assert.Contains(t, full.Outputs, "scrape")

	defaults := NewFullConfig(&Config{}, (*YAMLConfig)(nil).NamesOf())
	assert.Equal(t, "default", defaults.Metrics[0].Name)
	assert.Contains(t, defaults.Outputs, "prometheus")
}

// TestFullConfigLeavesGeneratedGroupNamesOutAndWritesALoadablePollInterval checks that full config leaves
// generated group names out and writes a loadable poll interval.
func TestFullConfigLeavesGeneratedGroupNamesOutAndWritesALoadablePollInterval(t *testing.T) {
	full := NewFullConfig(&Config{
		WatchGroups: []WatchGroup{
			{Name: "metrics[0].overrides[0]", Interval: 1000, Fields: []string{"A"}},
			{Name: "metrics[0].overrides[1]", Interval: 1000, Fields: []string{"B"}},
			{Name: "mine", Interval: 1000, Fields: []string{"C"}},
		},
	}, ConfigNames{})

	assert.Empty(t, full.Metrics[0].Overrides[0].Name, "an unnamed override is written without a name")
	assert.Empty(t, full.Metrics[0].Overrides[1].Name)
	assert.Equal(t, "mine", full.Metrics[0].Overrides[2].Name)
	assert.Equal(t, "1s", full.Sources.DCGM.DetectBindUnbind.Every, "a zero interval would not load; detection is off, so the default has no effect")

	enabled := NewFullConfig(&Config{EnableGPUBindUnbindWatch: true, GPUBindUnbindPollInterval: time.Second}, ConfigNames{})
	assert.Equal(t, "1s", enabled.Sources.DCGM.DetectBindUnbind.Every)
}

// TestFullConfigRenamesWatchGroupNamesAnOverrideCannotHold checks that full config renames watch group names
// an override cannot hold.
func TestFullConfigRenamesWatchGroupNamesAnOverrideCannotHold(t *testing.T) {
	config := &Config{
		MetricSource:       MetricSource{Kind: MetricSourceFile, File: "/a.csv"},
		XIDCountWindowSize: int(DefaultCountWindow.Milliseconds()), ClockEventsCountWindowSize: int(DefaultCountWindow.Milliseconds()),
		WatchGroups: []WatchGroup{
			{Name: "gpu/temp", Interval: 1000, Fields: []string{"A"}},
			{Name: "gpu temp", Interval: 1000, Fields: []string{"B"}},
			{Name: "gpu-temp", Interval: 1000, Fields: []string{"C"}},
			{Name: " // ", Interval: 1000, Fields: []string{"D"}},
			{Name: "ok_name.1", Interval: 1000, Fields: []string{"E"}},
			{Name: "metrics[0].overrides[5]", Interval: 1000, Fields: []string{"F"}},
		},
	}

	var names []string
	for _, override := range NewFullConfig(config, ConfigNames{}).Metrics[0].Overrides {
		names = append(names, override.Name)
	}

	assert.Equal(t, []string{"gpu-temp-2", "gpu-temp-3", "gpu-temp", "override", "ok_name.1", ""}, names)
	for _, name := range names {
		assert.True(t, name == "" || validOverrideName(name), name)
	}

	renames := RenamedOverrides(config)
	require.Len(t, renames, 3)
	assert.Equal(t, OverrideRename{Group: 0, From: "gpu/temp", To: "gpu-temp-2"}, renames[0])
	assert.Equal(t, OverrideRename{Group: 3, From: " // ", To: "override"}, renames[2])
}

// TestFullConfigPutsAnUnnamedOverrideBackWhereTheReaderNamedIt checks that full config puts an unnamed
// override back where the reader named it.
func TestFullConfigPutsAnUnnamedOverrideBackWhereTheReaderNamedIt(t *testing.T) {
	config := &Config{
		MetricSource:       MetricSource{Kind: MetricSourceFile, File: "/a.csv"},
		XIDCountWindowSize: 60000, ClockEventsCountWindowSize: 120000,
		WatchGroups: []WatchGroup{
			{Name: "metrics[0].overrides[1]", Interval: 1000, Fields: []string{"A"}},
			{Name: "named", Interval: 1000, Fields: []string{"B"}},
			{Name: "metrics[0].overrides[3]", Interval: 1000, Fields: []string{"C"}},
		},
	}

	groups, windows := OverridePositions(config)
	assert.Equal(t, []int{1, 2, 3}, groups)
	assert.Equal(t, map[string]int{ExporterXIDErrorsCountField: 0, ExporterClockEventsCountField: 4}, windows)

	overrides := NewFullConfig(config, ConfigNames{}).Metrics[0].Overrides
	require.Len(t, overrides, 5)
	assert.Equal(t, "1m", overrides[0].Window)
	assert.Equal(t, []string{"A"}, overrides[1].Match)
	assert.Equal(t, "2m", overrides[4].Window)
}

// TestFullConfigKeepsAnUnnamedOverrideAfterAWindowAtItsDefault checks that an unnamed override written after
// a default window loads back under the same generated name.
func TestFullConfigKeepsAnUnnamedOverrideAfterAWindowAtItsDefault(t *testing.T) {
	config := fullTestConfig()
	config.XIDCountWindowSize = int(DefaultCountWindow.Milliseconds())
	config.ClockEventsCountWindowSize = int(DefaultCountWindow.Milliseconds())
	config.WatchGroups = []WatchGroup{{Name: "metrics[0].overrides[1]", Interval: 1000, Fields: []string{"A"}}}

	text, err := yaml.Marshal(NewFullConfig(config, ConfigNames{}))
	require.NoError(t, err)
	parsed, err := ParseYAMLConfig(text)
	require.NoError(t, err, string(text))
	loaded := &Config{}
	require.NoError(t, parsed.ApplyTo(loaded))

	require.Len(t, loaded.WatchGroups, 1)
	assert.Equal(t, "metrics[0].overrides[1]", loaded.WatchGroups[0].Name)
}

// TestFullConfigKeepsGroupOrderWhenTooFewWindowsFillThePlacesBeforeAGroup checks that full config keeps group
// order when too few windows fill the places before a group.
func TestFullConfigKeepsGroupOrderWhenTooFewWindowsFillThePlacesBeforeAGroup(t *testing.T) {
	config := &Config{
		MetricSource:       MetricSource{Kind: MetricSourceFile, File: "/a.csv"},
		XIDCountWindowSize: 60000, ClockEventsCountWindowSize: int(DefaultCountWindow.Milliseconds()),
		WatchGroups: []WatchGroup{
			{Name: "metrics[0].overrides[3]", Interval: 1000, Fields: []string{"A"}},
		},
	}

	groups, windows := OverridePositions(config)
	assert.Equal(t, []int{1}, groups, "one window exists to fill the places before the group")
	assert.Equal(t, map[string]int{ExporterXIDErrorsCountField: 0}, windows)
}

// TestFullConfigJSONWritesEmptyValuesAndEnrichmentSwitches checks that full config JSON writes empty values
// and enrichment switches.
func TestFullConfigJSONWritesEmptyValuesAndEnrichmentSwitches(t *testing.T) {
	data, err := json.Marshal(NewFullConfig(&Config{
		Kubernetes: true, EnrichmentModes: EnrichmentModes{Kubernetes: EnrichmentRequired},
	}, ConfigNames{}))

	require.NoError(t, err)

	var generic map[string]any
	require.NoError(t, json.Unmarshal(data, &generic))

	kubernetes := generic["sources"].(map[string]any)["kubernetes"].(map[string]any)
	assert.Equal(t, []any{}, kubernetes["additional_resource_names"])
	assert.Equal(t, "", generic["server"].(map[string]any)["web_config_file"])
	assert.NotContains(t, generic["sources"].(map[string]any)["dcgm"], "embedded_log")

	enrichment := generic["metrics"].([]any)[0].(map[string]any)["enrichment"].(map[string]any)
	assert.Equal(t, map[string]any{"kubernetes": true, "container": false, "dra": false, "hpc_jobs": false}, enrichment)
}
