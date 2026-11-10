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
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

const (
	envConfigFile    = "DCGM_EXPORTER_CONFIG_FILE"
	envListenAddress = "DCGM_EXPORTER_LISTEN"
	envKubernetes    = "DCGM_EXPORTER_KUBERNETES"
	configVolumeName = "dcgm-exporter-config"
	defaultConfigMap = "dcgm-exporter-config"
)

// parseConfigDocument decodes the exporter configuration a ConfigMap carries.
func parseConfigDocument(t *testing.T, text string) map[string]interface{} {
	t.Helper()
	var document map[string]interface{}
	require.NoError(t, yaml.Unmarshal([]byte(text), &document))
	return document
}

// nestedValue returns the value at a path of map keys, or nil when any step is missing.
func nestedValue(document map[string]interface{}, path ...string) interface{} {
	var current interface{} = document
	for _, key := range path {
		next, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = next[key]
	}
	return current
}

// requireConfigMount checks that only the key file of the ConfigMap is mounted,
// as a directory at the chart's config directory, and that
// DCGM_EXPORTER_CONFIG_FILE names that file there.
func requireConfigMount(t *testing.T, resources []chartResource, configMapName, key string) {
	t.Helper()

	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	container := requireContainer(t, daemonSet, "exporter")

	configMap := requireVolume(t, daemonSet, configVolumeName).ConfigMap
	assert.Equal(t, configMapName, configMap.Name)
	assert.Equal(t, []keyToPath{{Key: key, Path: key}}, configMap.Items)

	mount := requireVolumeMount(t, container, configVolumeName)
	assert.Equal(t, configDirectory, mount.MountPath)
	assert.Empty(t, mount.SubPath)

	assert.Equal(t, configDirectory+"/"+key, containerEnvValues(container)[envConfigFile])
}

// firstMetricsEntry returns metrics[0] of a configuration document.
func firstMetricsEntry(t *testing.T, document map[string]interface{}) map[string]interface{} {
	t.Helper()
	metrics, ok := document["metrics"].([]interface{})
	require.True(t, ok, "metrics must be a list")
	require.NotEmpty(t, metrics)
	entry, ok := metrics[0].(map[string]interface{})
	require.True(t, ok)
	return entry
}

// requireNoConfigMount checks that the chart mounts no exporter configuration file
// and leaves DCGM_EXPORTER_CONFIG_FILE to the image.
func requireNoConfigMount(t *testing.T, resources []chartResource) {
	t.Helper()
	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	container := requireContainer(t, daemonSet, "exporter")
	assert.NotContains(t, daemonSetVolumeNames(daemonSet), configVolumeName)
	assert.NotContains(t, containerVolumeMountNames(container), configVolumeName)
	assert.NotContains(t, containerEnvValues(container), envConfigFile)
}

// With default values the chart creates a ConfigMap from the values, mounts it where
// the exporter looks for its configuration, and sets no listen or Kubernetes variable.
func TestChartConfigDefaultsRenderTheExporterConfig(t *testing.T) {
	resources := renderChart(t, nil)
	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	document := parseConfigDocument(t, configMap.Data["config.yaml"])

	entry := firstMetricsEntry(t, document)
	assert.Equal(t, chartMetricsFile, entry["file"])
	assert.Equal(t, "30s", entry["every"])
	assert.Equal(t, map[string]interface{}{
		"kubernetes": "auto",
		"container":  false,
		"dra":        false,
		"hpc_jobs":   false,
	}, entry["enrichment"])

	assert.Equal(t, map[string]interface{}{"type": "prometheus", "schema": "dcgm/v1", "listen": ":9400"},
		nestedValue(document, "outputs", "prometheus"))
	assert.EqualValues(t, 0, nestedValue(document, "sources", "dcgm", "watch", "max_keep_samples"))
	assert.Equal(t, "10m", nestedValue(document, "sources", "dcgm", "watch", "max_keep_age"))
	assert.EqualValues(t, 16, nestedValue(document, "server", "max_concurrent_scrapes"))
	assert.Equal(t, false, nestedValue(document, "observability", "metrics", "enabled"))

	requireConfigMount(t, resources, defaultConfigMap, "config.yaml")

	container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
	assertNoDefaultCollectorsArgs(t, container)
	assert.NotContains(t, containerEnvValues(container), envListenAddress)
	assert.NotContains(t, containerEnvValues(container), envKubernetes)
}

// The chart never sets DCGM_EXPORTER_KUBERNETES: its config decides the Kubernetes
// labels, and without a chart config the user's file (or the image's) decides.
func TestChartSetsNoKubernetesEnvironment(t *testing.T) {
	for name, values := range map[string]map[string]interface{}{
		"default config":     nil,
		"config null":        {"config": nil},
		"existing ConfigMap": {"configMap": map[string]interface{}{"existingName": "my-exporter-config"}},
		"metrics null":       {"config": map[string]interface{}{"metrics": nil}},
	} {
		t.Run(name, func(t *testing.T) {
			container := requireContainer(t, requireResourceKind(t, renderChart(t, values), "DaemonSet"), "exporter")
			assert.NotContains(t, containerEnvValues(container), envKubernetes)
		})
	}
}

// A missing or non-2 config version fails rendering and points to configMap.existingName.
// With configMap.existingName the chart does not use `config`, so any version renders.
func TestChartConfigVersionMustBe2(t *testing.T) {
	const message = "config.version must be 2; to mount a version 1 file, set configMap.existingName"
	rejected := map[string]interface{}{"integer 1": 1, "string 1": "1", "integer 3": 3, "null": nil}

	for name, version := range rejected {
		t.Run(name, func(t *testing.T) {
			err := renderChartError(t, map[string]interface{}{"config": map[string]interface{}{"version": version}})
			require.ErrorContains(t, err, message)
		})
	}

	t.Run("version 1 with existing ConfigMap", func(t *testing.T) {
		resources := renderChart(t, map[string]interface{}{
			"config":    map[string]interface{}{"version": 1},
			"configMap": map[string]interface{}{"existingName": "my-exporter-config"},
		})
		assert.False(t, hasResourceName(resources, "ConfigMap", defaultConfigMap))
	})

	t.Run("string 2 renders an integer", func(t *testing.T) {
		resources := renderChart(t, map[string]interface{}{"config": map[string]interface{}{"version": "2"}})
		configFile := requireResourceName(t, resources, "ConfigMap", defaultConfigMap).Data["config.yaml"]
		assert.Regexp(t, `(?m)^version: 2$`, configFile)
	})
}

// A Kubernetes label setting in `config` reaches the rendered ConfigMap as written,
// and the chart does not replace it with an environment variable.
func TestChartConfigKubernetesLabelSettingReachesTheConfigMap(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"config": map[string]interface{}{
			"metrics": []interface{}{map[string]interface{}{
				"name":       "default",
				"file":       chartMetricsFile,
				"every":      "30s",
				"enrichment": map[string]interface{}{"kubernetes": false},
			}},
		},
	})

	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	entry := firstMetricsEntry(t, parseConfigDocument(t, configMap.Data["config.yaml"]))
	assert.Equal(t, false, nestedValue(entry, "enrichment", "kubernetes"))

	container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
	assert.NotContains(t, containerEnvValues(container), envKubernetes)
}

// outputConfig returns values whose config holds the named outputs, each with a listen address.
func outputConfig(listen map[string]string) map[string]interface{} {
	outputs := map[string]interface{}{}
	for name, address := range listen {
		outputs[name] = map[string]interface{}{"type": "prometheus", "schema": "dcgm/v1", "listen": address}
	}

	return map[string]interface{}{"config": map[string]interface{}{"outputs": outputs}}
}

// The listen address comes from `config`; its port must equal service.port, which
// the container port, probes, Service and NetworkPolicy use. An output without
// `listen` uses the exporter default :9400.
func TestChartConfigListenMustUseTheServicePort(t *testing.T) {
	t.Run("custom port with matching service.port", func(t *testing.T) {
		values := outputConfig(map[string]string{"prometheus": "[::]:9500"})
		values["service"] = map[string]interface{}{"port": 9500}
		resources := renderChart(t, values)
		container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
		assert.NotContains(t, containerEnvValues(container), envListenAddress)
		document := parseConfigDocument(t, requireResourceName(t, resources, "ConfigMap", defaultConfigMap).Data["config.yaml"])
		assert.Equal(t, "[::]:9500", nestedValue(document, "outputs", "prometheus", "listen"))
	})

	t.Run("differing port fails", func(t *testing.T) {
		err := renderChartError(t, outputConfig(map[string]string{"prometheus": ":9500"}))
		require.ErrorContains(t, err, `config.outputs.prometheus.listen (":9500") must use service.port (9400)`)
	})

	t.Run("default listen with another service.port fails", func(t *testing.T) {
		values := map[string]interface{}{
			"config":  map[string]interface{}{"outputs": map[string]interface{}{"prometheus": map[string]interface{}{"listen": nil}}},
			"service": map[string]interface{}{"port": 9500},
		}
		require.ErrorContains(t, renderChartError(t, values), `config.outputs.prometheus.listen (":9400") must use service.port (9500)`)
	})

	t.Run("non-numeric port fails", func(t *testing.T) {
		err := renderChartError(t, outputConfig(map[string]string{"prometheus": ":metrics"}))
		require.ErrorContains(t, err, `config.outputs.prometheus.listen (":metrics") must be HOST:PORT with a numeric TCP port`)
	})

	t.Run("existing ConfigMap is not checked", func(t *testing.T) {
		renderChart(t, map[string]interface{}{
			"configMap": map[string]interface{}{"existingName": "mine"},
			"service":   map[string]interface{}{"port": 9500},
		})
	})
}

// A `config` or `config.outputs` that is not a map fails with the chart's own message.
func TestChartConfigMustBeAMap(t *testing.T) {
	err := renderChartError(t, map[string]interface{}{"config": "version: 2"})
	require.ErrorContains(t, err, "config must be a YAML map")

	err = renderChartError(t, map[string]interface{}{"config": map[string]interface{}{"outputs": "prometheus"}})
	require.ErrorContains(t, err, "config.outputs must be a map")
}

// One nested key set on its own changes only that key; the rest of the default
// document stays.
func TestChartConfigNestedKeyOverlayKeepsTheOtherDefaults(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"config": map[string]interface{}{
			"sources": map[string]interface{}{
				"dcgm": map[string]interface{}{"watch": map[string]interface{}{"max_keep_age": "5m"}},
			},
		},
	})
	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	document := parseConfigDocument(t, configMap.Data["config.yaml"])

	assert.Equal(t, "5m", nestedValue(document, "sources", "dcgm", "watch", "max_keep_age"))
	assert.EqualValues(t, 0, nestedValue(document, "sources", "dcgm", "watch", "max_keep_samples"))
	assert.Equal(t, "30s", firstMetricsEntry(t, document)["every"])
	assert.EqualValues(t, 2, document["version"])
}

// A metrics entry with inline fields and no file renders without customMetrics,
// and the rendered config file holds those fields.
func TestChartConfigInlineFieldsRender(t *testing.T) {
	fields := []interface{}{
		map[string]interface{}{"name": "DCGM_FI_DEV_GPU_TEMP_CELSIUS", "prometheus_type": "gauge", "help": "GPU temperature (in C)."},
		map[string]interface{}{"name": "DCGM_FI_DEV_BOARD_POWER_WATTS", "prometheus_type": "gauge", "help": "Power draw (in W)."},
	}
	resources := renderChart(t, map[string]interface{}{
		"config": map[string]interface{}{
			"metrics": []interface{}{map[string]interface{}{"name": "default", "fields": fields, "every": "30s"}},
		},
	})

	entry := firstMetricsEntry(t, renderedConfig(t, resources))
	assert.Equal(t, fields, entry["fields"])
	assert.NotContains(t, entry, "file")
}

// configMap.key names the file in the ConfigMap and in the mounted directory.
func TestChartConfigMapKey(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"configMap": map[string]interface{}{"key": "exporter.yaml"},
	})
	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	assert.Contains(t, configMap.Data, "exporter.yaml")
	requireConfigMount(t, resources, defaultConfigMap, "exporter.yaml")
}

// Missing configMap values (kept by --reuse-values from an older chart) or a
// blank key fall back to the defaults instead of an invalid mount.
func TestChartConfigMapDefaultsFillMissingValues(t *testing.T) {
	for name, configMap := range map[string]interface{}{
		"no configMap": nil,
		"blank key":    map[string]interface{}{"key": ""},
	} {
		t.Run(name, func(t *testing.T) {
			resources := renderChart(t, map[string]interface{}{"configMap": configMap})
			assert.Contains(t, requireResourceName(t, resources, "ConfigMap", defaultConfigMap).Data, "config.yaml")
			requireConfigMount(t, resources, defaultConfigMap, "config.yaml")
		})
	}
}

// An existing ConfigMap is mounted as it is and the chart creates none.
func TestChartConfigMapExistingNameIsMounted(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"configMap": map[string]interface{}{
			"existingName": "my-exporter-config",
			"key":          "custom.yaml",
		},
	})
	assert.False(t, hasResourceName(resources, "ConfigMap", defaultConfigMap))
	assert.False(t, hasResourceName(resources, "ConfigMap", "my-exporter-config"))
	requireConfigMount(t, resources, "my-exporter-config", "custom.yaml")
}

// Setting `config` to null removes the chart-managed file. The image's own default
// file then applies, because the chart does not set DCGM_EXPORTER_CONFIG_FILE.
func TestChartConfigNullMountsNothing(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{"config": nil})
	assert.False(t, hasResourceName(resources, "ConfigMap", defaultConfigMap))
	requireNoConfigMount(t, resources)
}

// The removed 4.8.4 `config.*` values each fail rendering on their own and are
// named in the message; two together are both named.
func TestChartRejects484ConfigOptions(t *testing.T) {
	const removed = "removed chart values"
	options := map[string]interface{}{
		"enabled": true, "create": true, "name": "my-config",
		"key": "config.yaml", "mountPath": "/etc/dcgm-exporter/config.yaml", "data": "version: 2\n",
	}

	for key, value := range options {
		t.Run(key, func(t *testing.T) {
			err := renderChartError(t, map[string]interface{}{"config": map[string]interface{}{key: value}})
			require.ErrorContains(t, err, "config."+key+": "+removed)
			assert.ErrorContains(t, err, "helm upgrade <release> <chart> -f <values file>")
		})
	}

	t.Run("two keys", func(t *testing.T) {
		err := renderChartError(t, map[string]interface{}{"config": map[string]interface{}{"enabled": true, "data": "x"}})
		require.ErrorContains(t, err, "config.enabled, config.data: "+removed)
		assert.ErrorContains(t, err, "config.data -> `config`")
		assert.ErrorContains(t, err, "config.name -> configMap.existingName")
		assert.ErrorContains(t, err, "config.enabled: false -> `config: null`")
	})

	t.Run("message gives the values-file upgrade", func(t *testing.T) {
		err := renderChartError(t, map[string]interface{}{"config": map[string]interface{}{"enabled": true, "data": "version: 1\n"}})
		require.ErrorContains(t, err, "config.enabled, config.data: "+removed)
		assert.ErrorContains(t, err, "In your values file, set the new keys instead:")
		assert.ErrorContains(t, err, "rewritten as a YAML map with `version: 2`")
		assert.ErrorContains(t, err, "Then upgrade with that file: `helm upgrade <release> <chart> -f <values file>`")
		assert.ErrorContains(t, err, "`helm get values <release> -o yaml` prints the values you set")
		assert.ErrorContains(t, err, "Do not add `--reuse-values` or `--reset-then-reuse-values`")
	})

	t.Run("values kept by reuse-values", func(t *testing.T) {
		data, err := os.ReadFile(repoPath(t, "tests", "static", "testdata", "chart-4.8.4-values.yaml"))
		require.NoError(t, err)
		var previous map[string]interface{}
		require.NoError(t, yaml.Unmarshal(data, &previous))
		chart := loadChart(t)
		chart.Values = previous
		_, err = runHelmInstallChart(t, chart, nil)
		require.ErrorContains(t, err, removed)
	})
}

// The install notes carry no deprecation text.
func TestChartNotesHaveNoDeprecationText(t *testing.T) {
	release, err := runHelmInstall(t, nil)
	require.NoError(t, err)
	assert.NotContains(t, release.Info.Notes, "DEPRECATED")
}

// The chart's default `config` and the image's default file describe the same exporter
// settings. They differ only in the Kubernetes labels (`auto` in the chart and `false`
// in the image file) and in the metrics list path, which the chart mounts itself.
func TestChartDefaultConfigMatchesTheImageDefaultFile(t *testing.T) {
	data, err := os.ReadFile(repoPath(t, "etc", "config.yaml"))
	require.NoError(t, err)
	shipped := parseConfigDocument(t, string(data))
	firstMetricsEntry(t, shipped)["enrichment"].(map[string]interface{})["kubernetes"] = "auto"
	firstMetricsEntry(t, shipped)["file"] = chartMetricsFile

	resources := renderChart(t, nil)
	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	assert.Equal(t, shipped, parseConfigDocument(t, configMap.Data["config.yaml"]),
		"change etc/config.yaml and the `config` default in deployment/values.yaml together")
}

// With `config: null` an existing ConfigMap is still mounted.
func TestChartConfigNullWithExistingNameMountsIt(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"config":    nil,
		"configMap": map[string]interface{}{"existingName": "my-exporter-config"},
	})
	assert.False(t, hasResourceName(resources, "ConfigMap", defaultConfigMap))
	requireConfigMount(t, resources, "my-exporter-config", "config.yaml")
}

// When every setting but the version is removed, the file is just the version line.
func TestChartConfigWithOnlyVersionRendersValidYAML(t *testing.T) {
	nothing := map[string]interface{}{}
	for _, key := range []string{"metrics", "outputs", "sources", "server", "observability"} {
		nothing[key] = nil
	}
	resources := renderChart(t, map[string]interface{}{"config": nothing})
	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	assert.Equal(t, "version: 2", strings.TrimSpace(configMap.Data["config.yaml"]))
}

// A changed chart-managed config changes the pod template, so the DaemonSet rolls. A
// ConfigMap the chart does not create has no checksum.
func TestChartConfigChecksumFollowsTheConfig(t *testing.T) {
	checksum := func(values map[string]interface{}) string {
		daemonSet := requireResourceKind(t, renderChart(t, values), "DaemonSet")
		return daemonSet.Spec.Template.Metadata.Annotations["checksum/config"]
	}
	base := checksum(nil)
	assert.NotEmpty(t, base)
	assert.Equal(t, base, checksum(nil), "rendering twice gives the same checksum")

	changed := checksum(map[string]interface{}{"config": map[string]interface{}{
		"server": map[string]interface{}{"max_concurrent_scrapes": 8},
	}})
	assert.NotEmpty(t, changed)
	assert.NotEqual(t, base, changed)

	assert.Empty(t, checksum(map[string]interface{}{"configMap": map[string]interface{}{"existingName": "mine"}}))
	assert.Empty(t, checksum(map[string]interface{}{"config": nil}))
}
