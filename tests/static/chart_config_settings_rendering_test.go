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

package static_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// settingEnvNames are the environment variables of the exporter settings the
// chart takes only from its config file.
var settingEnvNames = []string{
	"DCGM_EXPORTER_LISTEN",
	"DCGM_EXPORTER_KUBERNETES",
	"DCGM_EXPORTER_WEB_READ_TIMEOUT",
	"DCGM_EXPORTER_WEB_WRITE_TIMEOUT",
	"DCGM_EXPORTER_WEB_CONFIG_FILE",
	"DCGM_EXPORTER_SYSTEMD_SOCKET",
	envEnablePodLabels,
	"DCGM_EXPORTER_KUBERNETES_POD_LABEL_ALLOWLIST_REGEX",
	envEnablePodUID,
	envEnableDRA,
}

// requireNoSettingEnv checks that the exporter container gets none of
// settingEnvNames and no arguments, so the config file decides those settings.
func requireNoSettingEnv(t *testing.T, resources []chartResource) {
	t.Helper()
	container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
	env := containerEnvValues(container)
	for _, name := range settingEnvNames {
		assert.NotContains(t, env, name)
	}
	assert.Empty(t, container.Args)
}

// renderedConfig returns the exporter config document of the chart-created ConfigMap.
func renderedConfig(t *testing.T, resources []chartResource) map[string]interface{} {
	t.Helper()
	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	return parseConfigDocument(t, configMap.Data["config.yaml"])
}

// read484Values returns the 4.8.4 chart values fixture.
func read484Values(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(repoPath(t, "tests", "static", "testdata", "chart-4.8.4-values.yaml"))
	require.NoError(t, err)

	var values map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &values))
	return values
}

// The default chart passes no exporter setting as an environment variable or flag,
// and its config leaves the timeouts to the exporter's defaults.
func TestChartDefaultSetsNoSettingOutsideTheConfig(t *testing.T) {
	resources := renderChart(t, nil)
	requireNoSettingEnv(t, resources)

	document := renderedConfig(t, resources)
	assert.Nil(t, nestedValue(document, "server", "read_timeout"))
	assert.Nil(t, nestedValue(document, "server", "write_timeout"))
	assert.Nil(t, nestedValue(document, "server", "web_config_file"))
}

// Server and Kubernetes settings in `config` reach the exporter's config file
// unchanged, with no environment variable or flag that could override them.
func TestChartConfigSettingsReachTheExporter(t *testing.T) {
	kubernetes := map[string]interface{}{
		"pod_uid":    true,
		"pod_labels": map[string]interface{}{"enabled": true, "allow": []interface{}{"^app$", "^a{1,2}$"}},
	}
	resources := renderChart(t, map[string]interface{}{
		"config": map[string]interface{}{
			"metrics": draMetrics("auto"),
			"server":  map[string]interface{}{"read_timeout": "15s", "write_timeout": "45s"},
			"sources": map[string]interface{}{"kubernetes": kubernetes, "dra": map[string]interface{}{"mode": "kubernetes"}},
		},
	})
	requireNoSettingEnv(t, resources)

	document := renderedConfig(t, resources)
	assert.Equal(t, "15s", nestedValue(document, "server", "read_timeout"))
	assert.Equal(t, "45s", nestedValue(document, "server", "write_timeout"))
	assert.EqualValues(t, 16, nestedValue(document, "server", "max_concurrent_scrapes"), "the other server defaults stay")
	assert.Equal(t, kubernetes, nestedValue(document, "sources", "kubernetes"))
	assert.Equal(t, "kubernetes", nestedValue(document, "sources", "dra", "mode"))
	assert.Equal(t, "auto", nestedValue(firstMetricsEntry(t, document), "enrichment", "dra"))
}

// Each removed chart value fails rendering and names its replacement, also with
// a ConfigMap the chart does not create. Values from several blocks are listed together.
func TestChartRejectsRemovedSettingValues(t *testing.T) {
	tests := []struct {
		block, name string
		value       interface{}
		replacement string
	}{
		{"configMap", "create", false, "remove it; the chart creates a ConfigMap from `config` unless configMap.existingName is set"},
		{"service", "address", ":9400", "config.outputs.<name>.listen, with the port in service.port"},
		{"service", "webReadTimeout", "10s", "config.server.read_timeout"},
		{"service", "webWriteTimeout", "30s", "config.server.write_timeout"},
		{"kubernetes", "enablePodLabels", false, "config.sources.kubernetes.pod_labels.enabled"},
		{"kubernetes", "podLabelAllowlistRegex", []interface{}{}, "config.sources.kubernetes.pod_labels.allow"},
		{"kubernetes", "enablePodUID", true, "config.sources.kubernetes.pod_uid"},
		{"kubernetesDRA", "enabled", false, "config.metrics[0].enrichment.dra: true, with config.sources.dra.mode: kubernetes"},
		{"debugDump", "enabled", true, "config.debug_dump.enabled"},
		{"debugDump", "directory", "/tmp/x", "config.debug_dump.directory"},
		{"debugDump", "retention", 24, "config.debug_dump.retention, a duration such as 24h"},
		{"debugDump", "compression", true, "config.debug_dump.compression"},
	}
	for _, tc := range tests {
		name := tc.block + "." + tc.name
		t.Run(name, func(t *testing.T) {
			for _, existing := range []bool{false, true} {
				err := renderChartError(t, removedValueSettings(tc.block, tc.name, tc.value, existing))
				require.ErrorContains(t, err, name+": removed chart values")
				assert.ErrorContains(t, err, name+" -> "+tc.replacement)
				assert.ErrorContains(t, err, "helm upgrade <release> <chart> -f <values file>")
				if existing {
					assert.ErrorContains(t, err, "without the `config.` prefix in that file, or for a version 1 file pass the matching flags in `arguments`")
				}
			}
		})
	}

	t.Run("several blocks", func(t *testing.T) {
		err := renderChartError(t, map[string]interface{}{
			"service":   map[string]interface{}{"webReadTimeout": "10s"},
			"debugDump": map[string]interface{}{"enabled": false, "sizeLimit": "1Gi"},
		})
		require.ErrorContains(t, err, "service.webReadTimeout, debugDump.enabled: removed chart values")
		assert.NotContains(t, err.Error(), "debugDump.sizeLimit", "sizeLimit is still a chart value")
	})
}

// removedValueSettings returns chart values that set block.name to value. With
// existing set they also name a ConfigMap the chart does not create; it joins the
// configMap block when the removed value is in that block.
func removedValueSettings(block, name string, value interface{}, existing bool) map[string]interface{} {
	settings := map[string]interface{}{name: value}
	values := map[string]interface{}{block: settings}
	if !existing {
		return values
	}

	if block == "configMap" {
		settings["existingName"] = "mine"
	} else {
		values["configMap"] = map[string]interface{}{"existingName": "mine"}
	}

	return values
}

// A setting that objects or checks of the chart depend on fails rendering when
// arguments or extraEnv set it, and the message names the config key.
func TestChartSettingOverridesRejected(t *testing.T) {
	settings := []struct{ key, flag, env string }{
		{"outputs.<name>.listen", "address", "DCGM_EXPORTER_LISTEN"},
		{"outputs.<name>.listen", "a", "DCGM_EXPORTER_LISTEN"},
		{"metrics[].file", "collectors", "DCGM_EXPORTER_COLLECTORS"},
		{"metrics[].file", "f", "DCGM_EXPORTER_COLLECTORS"},
		{"server.web_config_file", "web-config-file", "DCGM_EXPORTER_WEB_CONFIG_FILE"},
		{"debug_dump.enabled", "dump-enabled", "DCGM_EXPORTER_DUMP_ENABLED"},
		{"debug_dump.directory", "dump-directory", "DCGM_EXPORTER_DUMP_DIRECTORY"},
		{"sources.kubernetes.pod_labels.enabled", "kubernetes-enable-pod-labels", "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"},
		{"sources.kubernetes.pod_uid", "kubernetes-enable-pod-uid", "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_UID"},
		{"metrics[0].enrichment.dra", "kubernetes-enable-dra", "KUBERNETES_ENABLE_DRA"},
	}
	for _, setting := range settings {
		// A short alias is reported by its long flag name.
		longFlag := map[string]string{"a": "address", "f": "collectors"}[setting.flag]
		if longFlag == "" {
			longFlag = setting.flag
		}
		want := "set config." + setting.key + ", not --" + longFlag + " or " + setting.env
		sources := map[string]map[string]interface{}{
			"flag with value":  {"arguments": []interface{}{"--" + setting.flag + "=1"}},
			"single-dash flag": {"arguments": []interface{}{"-" + setting.flag, "1"}},
			"environment":      {"extraEnv": []interface{}{map[string]interface{}{"name": setting.env, "value": "1"}}},
		}
		for name, values := range sources {
			t.Run(setting.flag+"/"+name, func(t *testing.T) {
				require.ErrorContains(t, renderChartError(t, values), want)
			})
		}
	}
}

// Other settings may be overridden with flags or environment variables, which
// the exporter applies over the config file the chart renders.
func TestChartOtherSettingOverridesRender(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"arguments": []interface{}{"--web-read-timeout=15s", "--kubernetes", "-r", "host:5555"},
		"extraEnv":  []interface{}{map[string]interface{}{"name": "DCGM_EXPORTER_DUMP_RETENTION", "value": "1h"}},
	})

	container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
	assert.Equal(t, []string{"--web-read-timeout=15s", "--kubernetes", "-r", "host:5555"}, container.Args)
	assert.Equal(t, "1h", containerEnvValues(container)["DCGM_EXPORTER_DUMP_RETENTION"])
	assert.True(t, hasResourceName(resources, "ConfigMap", defaultConfigMap))
}

// Without a chart-rendered config (an existing ConfigMap, which may hold a version 1
// file, or `config: null`), flags and environment variables are the way to set
// these settings, so they render.
func TestChartSettingOverridesAllowedWithoutAChartConfig(t *testing.T) {
	for name, values := range map[string]map[string]interface{}{
		"existing ConfigMap": {"configMap": map[string]interface{}{"existingName": "mine"}},
		"config null":        {"config": nil},
	} {
		t.Run(name, func(t *testing.T) {
			values["arguments"] = []interface{}{"--dump-directory=/tmp/dumps", "--web-config-file=/etc/web.yaml"}
			values["extraEnv"] = []interface{}{map[string]interface{}{"name": "DCGM_EXPORTER_LISTEN", "value": ":9400"}}
			container := requireContainer(t, requireResourceKind(t, renderChart(t, values), "DaemonSet"), "exporter")
			assert.Equal(t, []string{"--dump-directory=/tmp/dumps", "--web-config-file=/etc/web.yaml"}, container.Args)
			assert.Equal(t, ":9400", containerEnvValues(container)["DCGM_EXPORTER_LISTEN"])
		})
	}
}

// Flags and environment variables that only start like a setting's name still render.
func TestChartSettingOverrideCheckMatchesWholeNames(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"arguments": []interface{}{"--web-config-files-note", "--dump-directory-mode=0700", "web-config-file", true, 3},
		"extraEnv":  []interface{}{map[string]interface{}{"name": "DCGM_EXPORTER_DUMP_DIRECTORY_NOTE", "value": "x"}},
	})
	container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
	assert.Equal(t, []string{"--web-config-files-note", "--dump-directory-mode=0700", "web-config-file", "true", "3"}, container.Args)
}

// With TLS or basic auth on, the chart writes the path of the web config it renders
// into config.server.web_config_file and mounts the file there. A path in
// `config` that resolves to the same file, relative to the config file's
// directory, is accepted.
func TestChartWebConfigPathReachesTheConfig(t *testing.T) {
	for name, values := range map[string]map[string]interface{}{
		"tls":        {"tlsServerConfig": map[string]interface{}{"enabled": true}},
		"basic auth": {"basicAuth": map[string]interface{}{"users": map[string]interface{}{"admin": "secret"}}},
		"same path in config": {
			"tlsServerConfig": map[string]interface{}{"enabled": true},
			"config":          map[string]interface{}{"server": map[string]interface{}{"web_config_file": webConfigPath}},
		},
		"same path relative to the config file": {
			"tlsServerConfig": map[string]interface{}{"enabled": true},
			"config":          map[string]interface{}{"server": map[string]interface{}{"web_config_file": "../web-config/" + webConfigKey}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			resources := renderChart(t, values)
			requireNoSettingEnv(t, resources)

			document := renderedConfig(t, resources)
			assert.Equal(t, webConfigPath, nestedValue(document, "server", "web_config_file"))
			assert.EqualValues(t, 16, nestedValue(document, "server", "max_concurrent_scrapes"), "the other server defaults stay")

			container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
			assert.Equal(t, webConfigDirectory, requireVolumeMount(t, container, "web-config-yaml").MountPath)
		})
	}
}

// With an existing ConfigMap the chart cannot write the web config path, so it
// sets nothing and the user's file must name the mounted path.
func TestChartWebConfigWithExistingConfigMap(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"tlsServerConfig": map[string]interface{}{"enabled": true},
		"configMap":       map[string]interface{}{"existingName": "mine"},
	})
	requireNoSettingEnv(t, resources)

	container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
	assert.Equal(t, webConfigDirectory, requireVolumeMount(t, container, "web-config-yaml").MountPath)
}

// TLS or basic auth fails when the chart cannot carry the web config path: another
// path in config.server.web_config_file, or no chart config at all.
func TestChartWebConfigConflictsRejected(t *testing.T) {
	err := renderChartError(t, map[string]interface{}{
		"basicAuth": map[string]interface{}{"users": map[string]interface{}{"admin": "secret"}},
		"config":    map[string]interface{}{"server": map[string]interface{}{"web_config_file": "/etc/other.yaml"}},
	})
	require.ErrorContains(t, err, `config.server.web_config_file ("/etc/other.yaml") differs from the web config the chart renders`)

	err = renderChartError(t, map[string]interface{}{
		"tlsServerConfig": map[string]interface{}{"enabled": true},
		"config":          nil,
	})
	require.ErrorContains(t, err, "tlsServerConfig and basicAuth need the chart's config file")
}

// The chart creates Kubernetes read access only for the features `config` turns
// on. DRA needs both a true or auto switch and a sources.dra block, and with an
// existing ConfigMap the chart cannot read the file, so it creates nothing.
func TestChartKubernetesAccessFollowsTheConfig(t *testing.T) {
	draSource := map[string]interface{}{"dra": map[string]interface{}{"mode": "kubernetes"}}
	tests := []struct {
		name                       string
		values                     map[string]interface{}
		wantPods, wantResourceRead bool
	}{
		{name: "default"},
		{
			name:   "pod labels off",
			values: map[string]interface{}{"config": map[string]interface{}{"sources": map[string]interface{}{"kubernetes": map[string]interface{}{"pod_labels": map[string]interface{}{"enabled": false}}}}},
		},
		{
			name:     "pod UID",
			values:   map[string]interface{}{"config": map[string]interface{}{"sources": map[string]interface{}{"kubernetes": map[string]interface{}{"pod_uid": true}}}},
			wantPods: true,
		},
		{
			name:     "DRA auto",
			values:   map[string]interface{}{"config": map[string]interface{}{"metrics": draMetrics("auto"), "sources": draSource}},
			wantPods: true, wantResourceRead: true,
		},
		{
			name:   "DRA switch without a DRA source",
			values: map[string]interface{}{"config": map[string]interface{}{"metrics": draMetrics("auto")}},
		},
		{
			name:   "DRA source with the switch off",
			values: map[string]interface{}{"config": map[string]interface{}{"sources": draSource}},
		},
		{
			name: "existing ConfigMap",
			values: map[string]interface{}{
				"config":    map[string]interface{}{"sources": map[string]interface{}{"kubernetes": map[string]interface{}{"pod_uid": true}}},
				"configMap": map[string]interface{}{"existingName": "mine"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resources := renderChart(t, tc.values)
			require.Equal(t, tc.wantPods, hasResourceKind(resources, "ClusterRole"))
			require.Equal(t, tc.wantPods, hasResourceKind(resources, "ClusterRoleBinding"))
			if tc.wantPods {
				rules := requireResourceKind(t, resources, "ClusterRole").Rules
				assert.Equal(t, tc.wantResourceRead, len(rules) == 2)
			}
		})
	}
}

// A values file copied from the 4.8.4 chart fails and names every removed value
// with its replacement. With those values moved to `config`, it still fails on the
// 4.8.4 metrics CSV mount; without that mount it renders and passes no exporter
// setting outside the config file.
func TestChart484ValuesMoveToConfig(t *testing.T) {
	values := read484Values(t)
	err := renderChartError(t, values)
	require.ErrorContains(t, err, "removed chart values")
	for _, move := range []string{
		"config.data -> `config`",
		"config.key -> configMap.key",
		"config.mountPath -> remove it; the chart mounts the config file at " + configDirectory + "/<configMap.key>",
		"service.address -> config.outputs.<name>.listen",
		"service.webReadTimeout -> config.server.read_timeout",
		"service.webWriteTimeout -> config.server.write_timeout",
		"kubernetes.enablePodLabels -> config.sources.kubernetes.pod_labels.enabled",
		"kubernetes.podLabelAllowlistRegex -> config.sources.kubernetes.pod_labels.allow",
		"kubernetes.enablePodUID -> config.sources.kubernetes.pod_uid",
		"kubernetesDRA.enabled -> config.metrics[0].enrichment.dra",
		"debugDump.enabled -> config.debug_dump.enabled",
		"debugDump.retention -> config.debug_dump.retention",
	} {
		assert.ErrorContains(t, err, move)
	}

	service := values["service"].(map[string]interface{})
	kubernetes := values["kubernetes"].(map[string]interface{})
	debugDump := values["debugDump"].(map[string]interface{})
	server := map[string]interface{}{"read_timeout": service["webReadTimeout"], "write_timeout": service["webWriteTimeout"]}
	dump := map[string]interface{}{
		"enabled":     debugDump["enabled"],
		"directory":   debugDump["directory"],
		"retention":   fmt.Sprintf("%vh", debugDump["retention"]),
		"compression": debugDump["compression"],
	}
	for _, name := range []string{"address", "webReadTimeout", "webWriteTimeout"} {
		delete(service, name)
	}
	for _, name := range []string{"enablePodLabels", "podLabelAllowlistRegex", "enablePodUID"} {
		delete(kubernetes, name)
	}
	for _, name := range []string{"enabled", "directory", "retention", "compression"} {
		delete(debugDump, name)
	}
	delete(values, "kubernetesDRA")
	values["config"] = map[string]interface{}{"server": server, "debug_dump": dump}

	// The chart mounts the metrics list itself, so the 4.8.4 metrics CSV mount fails the render.
	err = renderChartError(t, values)
	require.ErrorContains(t, err, "the chart mounts the metrics list itself, at "+chartMetricsFile)
	assert.ErrorContains(t, err, "list the metrics in config.metrics[].fields, or point config.metrics[].file at your own file")

	// Without that mount and its volume, the values render.
	delete(values, "extraConfigMapVolumes")
	delete(values, "extraVolumeMounts")

	resources := renderChart(t, values)
	requireNoSettingEnv(t, resources)
	document := renderedConfig(t, resources)
	for key, value := range server {
		assert.Equal(t, value, nestedValue(document, "server", key))
	}
	assert.Equal(t, dump, nestedValue(document, "debug_dump"))
}
