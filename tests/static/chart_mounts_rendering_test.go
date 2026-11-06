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
	"maps"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// customMetricsDeprecated is the advice every customMetrics render failure ends with.
const customMetricsDeprecated = "customMetrics is deprecated; use config.metrics[].fields instead"

// chartVolumeNames are the chart-owned volumes TestChartOwnedMountsUseNoSubPath checks by path.
var chartVolumeNames = []string{
	configVolumeName, metricsVolumeName, webConfigVolumeName, tlsVolumeName, debugDumpsVolumeName,
}

// chartMountCase is one value combination and the chart volumes it must mount,
// by volume name and mount path.
type chartMountCase struct {
	values map[string]interface{}
	mounts map[string]string
}

// withConfigMounts returns the config and metrics mounts plus extra.
func withConfigMounts(extra map[string]string) map[string]string {
	mounts := map[string]string{configVolumeName: configDirectory, metricsVolumeName: metricsDirectory}
	maps.Copy(mounts, extra)

	return mounts
}

// chartMountCases returns sample value combinations and the chart volumes each
// one mounts.
func chartMountCases() map[string]chartMountCase {
	tls := map[string]interface{}{"enabled": true}
	basicAuth := map[string]interface{}{"users": map[string]interface{}{"admin": "secret"}}
	webConfig := map[string]string{webConfigVolumeName: webConfigDirectory}
	tlsAndWebConfig := map[string]string{webConfigVolumeName: webConfigDirectory, tlsVolumeName: tlsDirectory}

	return map[string]chartMountCase{
		"default": {mounts: withConfigMounts(nil)},
		"TLS": {
			values: map[string]interface{}{"tlsServerConfig": tls},
			mounts: withConfigMounts(tlsAndWebConfig),
		},
		"basic auth": {
			values: map[string]interface{}{"basicAuth": basicAuth},
			mounts: withConfigMounts(webConfig),
		},
		"TLS and basic auth": {
			values: map[string]interface{}{"tlsServerConfig": tls, "basicAuth": basicAuth},
			mounts: withConfigMounts(tlsAndWebConfig),
		},
		"existingName": {
			values: map[string]interface{}{"configMap": map[string]interface{}{"existingName": "my-exporter-config"}},
			mounts: withConfigMounts(nil),
		},
		"existingName with a custom key": {
			values: map[string]interface{}{"configMap": map[string]interface{}{"existingName": "mine", "key": "x.yaml"}},
			mounts: withConfigMounts(nil),
		},
		"existingName with TLS": {
			values: map[string]interface{}{
				"configMap":       map[string]interface{}{"existingName": "mine"},
				"tlsServerConfig": tls,
			},
			mounts: withConfigMounts(tlsAndWebConfig),
		},
		"config null": {
			values: map[string]interface{}{"config": nil},
			mounts: map[string]string{metricsVolumeName: metricsDirectory},
		},
		"debug dumps": {
			values: map[string]interface{}{"config": map[string]interface{}{"debug_dump": map[string]interface{}{"enabled": true}}},
			mounts: withConfigMounts(map[string]string{debugDumpsVolumeName: debugDumpsDirectory}),
		},
		"read-only root": {
			values: map[string]interface{}{"securityContext": map[string]interface{}{"readOnlyRootFilesystem": true}},
			mounts: withConfigMounts(nil),
		},
		"custom metrics": {
			values: map[string]interface{}{"customMetrics": "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature"},
			mounts: withConfigMounts(nil),
		},
		"custom key": {
			values: map[string]interface{}{"configMap": map[string]interface{}{"key": "exporter.yaml"}},
			mounts: withConfigMounts(nil),
		},
	}
}

// For each case in chartMountCases, no container mount uses subPath, and
// each volume named in chartVolumeNames that the combination needs is rendered
// and mounted at its documented path. Volumes outside chartVolumeNames, such as
// the always-mounted pod-gpu-resources, are not checked here.
func TestChartOwnedMountsUseNoSubPath(t *testing.T) {
	for name, tc := range chartMountCases() {
		t.Run(name, func(t *testing.T) {
			daemonSet := requireResourceKind(t, renderChart(t, tc.values), "DaemonSet")
			for _, c := range append(daemonSet.Spec.Template.Spec.InitContainers, daemonSet.Spec.Template.Spec.Containers...) {
				for _, mount := range c.VolumeMounts {
					assert.Empty(t, mount.SubPath, "container %s mounts %s at %s with subPath", c.Name, mount.Name, mount.MountPath)
				}
			}

			container := requireContainer(t, daemonSet, "exporter")
			for _, volumeName := range chartVolumeNames {
				mount, mounted := findVolumeMount(container, volumeName)
				wantPath, want := tc.mounts[volumeName]
				if !want {
					assert.False(t, mounted, "volume %s is mounted at %s", volumeName, mount.MountPath)
					continue
				}

				requireVolume(t, daemonSet, volumeName)
				assert.True(t, mounted, "volume %s is not mounted", volumeName)
				assert.Equal(t, wantPath, mount.MountPath, "volume %s", volumeName)
			}
		})
	}
}

// templateComment matches a Go template comment, {{/* */}} or {{- /* */ -}}.
var templateComment = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)

// subPathKey matches a subPath key at the start of a YAML line.
var subPathKey = regexp.MustCompile(`(?m)^[\s-]*subPath\s*:.*$`)

// No chart template has a subPath key outside a template comment, so no
// template-defined mount gains one under values the render cases miss;
// extraVolumeMounts and initContainers render as given.
func TestChartTemplatesHaveNoSubPath(t *testing.T) {
	chart := loadChart(t)
	require.NotEmpty(t, chart.Templates)

	for _, template := range chart.Templates {
		source := templateComment.ReplaceAllString(string(template.Data), "")
		assert.Empty(t, subPathKey.FindAllString(source, -1), "%s has a subPath key", template.Name)
	}
}

// The chart mounts only the metrics key of the metrics ConfigMap, read-only, as
// default-counters.csv in its metrics directory, and the default config names
// that file.
func TestChartMountsTheMetricsList(t *testing.T) {
	resources := renderChart(t, nil)
	daemonSet := requireResourceKind(t, resources, "DaemonSet")

	metricsVolume := requireVolume(t, daemonSet, metricsVolumeName).ConfigMap
	assert.Equal(t, metricsConfigMapName, metricsVolume.Name)
	require.Equal(t, []keyToPath{{Key: "metrics", Path: metricsFileName}}, metricsVolume.Items)

	mount := requireVolumeMount(t, requireContainer(t, daemonSet, "exporter"), metricsVolumeName)
	assert.Equal(t, metricsDirectory, mount.MountPath)
	assert.True(t, mount.ReadOnly)

	// The rendered mount path and item path make up the file the rendered config names.
	configFile := firstMetricsEntry(t, renderedConfig(t, resources))["file"]
	assert.Equal(t, mount.MountPath+"/"+metricsVolume.Items[0].Path, configFile)
	assert.Equal(t, chartMetricsFile, configFile)
}

// customMetrics content reaches the metrics ConfigMap, and changing it leaves
// the DaemonSet pod template, annotations included, as it was, so a
// customMetrics change does not replace running pods.
func TestChartCustomMetricsChangeOnlyTheMetricsConfigMap(t *testing.T) {
	first := "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature\nDCGM_FI_DEV_POWER_USAGE, gauge, Power draw"
	second := "DCGM_FI_DEV_FB_USED, gauge, Framebuffer memory used"

	firstResources := renderChart(t, map[string]interface{}{"customMetrics": first})
	secondResources := renderChart(t, map[string]interface{}{"customMetrics": second})
	assert.Equal(t, first, requireResourceName(t, firstResources, "ConfigMap", metricsConfigMapName).Data["metrics"])
	assert.Equal(t, second, requireResourceName(t, secondResources, "ConfigMap", metricsConfigMapName).Data["metrics"])

	firstTemplate := renderedPodTemplate(t, map[string]interface{}{"customMetrics": first})
	assert.Equal(t, firstTemplate, renderedPodTemplate(t, map[string]interface{}{"customMetrics": second}))
	assert.Equal(t, firstTemplate, renderedPodTemplate(t, nil))
}

// renderedPodTemplate renders the chart and returns the DaemonSet's whole
// spec.template, so a comparison covers every field and annotation.
func renderedPodTemplate(t *testing.T, values map[string]interface{}) interface{} {
	t.Helper()

	release, err := runHelmInstall(t, values)
	require.NoError(t, err)

	for _, document := range releaseutil.SplitManifests(release.Manifest) {
		var resource struct {
			Kind string `json:"kind"`
			Spec struct {
				Template interface{} `json:"template"`
			} `json:"spec"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(document), &resource))
		if resource.Kind == "DaemonSet" {
			return resource.Spec.Template
		}
	}

	t.Fatal("the chart renders no DaemonSet")
	return nil
}

// A mount at the image's metrics CSV path fails the render when no
// config.metrics[].file of the chart's config names that path, as with the
// default config: the chart mounts the metrics list itself. With `config: null`
// or configMap.existingName the chart cannot see which file the exporter reads,
// so the mount renders.
func TestChartRejectsAMountAtTheImageMetricsFile(t *testing.T) {
	mount := func(mountPath string, extra map[string]interface{}) map[string]interface{} {
		values := map[string]interface{}{
			"extraConfigMapVolumes": []interface{}{map[string]interface{}{
				"name": "exporter-metrics-volume", "configMap": map[string]interface{}{"name": metricsConfigMapName},
			}},
			"extraVolumeMounts": []interface{}{map[string]interface{}{
				"name": "exporter-metrics-volume", "mountPath": mountPath, "subPath": metricsFileName,
			}},
		}
		maps.Copy(values, extra)

		return values
	}

	imageEntry := map[string]interface{}{"name": "default", "file": imageMetricsFile}
	relativeImageEntry := map[string]interface{}{"name": "default", "file": "../../" + metricsFileName}

	testCases := []struct {
		name     string
		values   map[string]interface{}
		rejected bool
	}{
		{name: "exact path", values: mount(imageMetricsFile, nil), rejected: true},
		{name: "trailing slash", values: mount(imageMetricsFile+"/", nil), rejected: true},
		{
			name:   "config names the path",
			values: mount(imageMetricsFile, map[string]interface{}{"config": metricsEntries(imageEntry)}),
		},
		{
			name:   "config names the path relative to the config file",
			values: mount(imageMetricsFile, map[string]interface{}{"config": metricsEntries(relativeImageEntry)}),
		},
		{name: "config null", values: mount(imageMetricsFile, map[string]interface{}{"config": nil})},
		{
			name:   "existingName",
			values: mount(imageMetricsFile, map[string]interface{}{"configMap": map[string]interface{}{"existingName": "mine"}}),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := renderChartError(t, tc.values)
			if !tc.rejected {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, `extraVolumeMounts entry "exporter-metrics-volume" mounts `+imageMetricsFile)
			assert.ErrorContains(t, err, "the chart mounts the metrics list itself, at "+chartMetricsFile)
			assert.ErrorContains(t, err, "list the metrics in config.metrics[].fields, or point config.metrics[].file at your own file")
		})
	}
}

// A user mount elsewhere renders next to the chart's own mounts.
func TestChartKeepsOtherExtraMounts(t *testing.T) {
	resources := renderChart(t, map[string]interface{}{
		"extraConfigMapVolumes": []interface{}{map[string]interface{}{
			"name": "extra-metrics", "configMap": map[string]interface{}{"name": "my-metrics"},
		}},
		"extraVolumeMounts": []interface{}{map[string]interface{}{
			"name": "extra-metrics", "mountPath": "/etc/dcgm-exporter/extra", "readOnly": true,
		}},
	})
	daemonSet := requireResourceKind(t, resources, "DaemonSet")
	assert.Equal(t, "my-metrics", requireVolume(t, daemonSet, "extra-metrics").ConfigMap.Name)

	container := requireContainer(t, daemonSet, "exporter")
	assert.Equal(t, "/etc/dcgm-exporter/extra", requireVolumeMount(t, container, "extra-metrics").MountPath)
	assert.Equal(t, metricsDirectory, requireVolumeMount(t, container, metricsVolumeName).MountPath)
}

// metricsEntries returns a `config` map whose metrics list holds entries.
func metricsEntries(entries ...map[string]interface{}) map[string]interface{} {
	list := make([]interface{}, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry)
	}

	return map[string]interface{}{"metrics": list}
}

// With the chart's config, customMetrics renders only when a
// config.metrics[].file names the chart's metrics file, resolved as the exporter
// does: relative to the config file's directory, then cleaned. With configMap.existingName or
// `config: null` the chart cannot see which file the exporter reads, so it
// does not check; -f and DCGM_EXPORTER_COLLECTORS are rejected with the
// chart's config by TestChartSettingOverridesRejected.
func TestChartRejectsUnusedCustomMetrics(t *testing.T) {
	const (
		custom  = "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature"
		ownFile = "/etc/dcgm-exporter/mine.csv"
	)
	configErr := "customMetrics is set, but no config.metrics[].file names " + chartMetricsFile

	ownEntry := map[string]interface{}{"name": "default", "file": ownFile}
	chartEntry := map[string]interface{}{"name": "chart", "file": chartMetricsFile}
	relativeEntry := map[string]interface{}{"name": "default", "file": "../metrics/" + metricsFileName}
	paddedEntry := map[string]interface{}{"name": "default", "file": " ../metrics/" + metricsFileName + " "}
	dottedEntry := map[string]interface{}{"name": "default", "file": configDirectory + "/./../metrics/" + metricsFileName}
	otherRelativeEntry := map[string]interface{}{"name": "default", "file": metricsFileName}
	fieldsEntry := map[string]interface{}{"name": "default", "fields": []interface{}{
		map[string]interface{}{"name": "DCGM_FI_DEV_GPU_TEMP", "prometheus_type": "gauge", "help": "GPU temperature"},
	}}
	withCustom := func(config interface{}) map[string]interface{} {
		return map[string]interface{}{"customMetrics": custom, "config": config}
	}

	testCases := []struct {
		name    string
		values  map[string]interface{}
		wantErr string
	}{
		{name: "default config", values: map[string]interface{}{"customMetrics": custom}},
		{name: "own file", values: withCustom(metricsEntries(ownEntry)), wantErr: configErr},
		{name: "a later entry names the chart file", values: withCustom(metricsEntries(ownEntry, chartEntry))},
		{name: "relative to the config file", values: withCustom(metricsEntries(relativeEntry))},
		{name: "relative with surrounding spaces", values: withCustom(metricsEntries(paddedEntry))},
		{name: "absolute with dot segments", values: withCustom(metricsEntries(dottedEntry))},
		{name: "relative to elsewhere", values: withCustom(metricsEntries(otherRelativeEntry)), wantErr: configErr},
		{name: "inline fields only", values: withCustom(metricsEntries(fieldsEntry)), wantErr: configErr},
		{name: "own file without customMetrics", values: map[string]interface{}{"config": metricsEntries(ownEntry)}},
		{name: "config null", values: withCustom(nil)},
		{
			name:   "existingName",
			values: map[string]interface{}{"customMetrics": custom, "configMap": map[string]interface{}{"existingName": "mine"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := renderChartError(t, tc.values)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tc.wantErr)
			assert.ErrorContains(t, err, customMetricsDeprecated)
		})
	}
}

// When the chart does not render the config file (configMap.existingName or
// `config: null`) and customMetrics is set, the install notes name the path the
// exporter must read; otherwise they do not mention it.
func TestChartNotesNameTheCustomMetricsPath(t *testing.T) {
	existingName := map[string]interface{}{"existingName": "mine"}
	testCases := map[string]struct {
		values map[string]interface{}
		want   bool
	}{
		"existingName and customMetrics": {
			values: map[string]interface{}{"configMap": existingName, "customMetrics": "x, gauge, x"},
			want:   true,
		},
		"config null, own config file and customMetrics": {
			values: map[string]interface{}{
				"config": nil, "customMetrics": "x, gauge, x", "arguments": []interface{}{"--config-file=/etc/mine/config.yaml"},
			},
			want: true,
		},
		"existingName only":  {values: map[string]interface{}{"configMap": existingName}},
		"customMetrics only": {values: map[string]interface{}{"customMetrics": "x, gauge, x"}},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			release, err := runHelmInstall(t, tc.values)
			require.NoError(t, err)

			notes := strings.Join(strings.Fields(release.Info.Notes), " ")
			if !tc.want {
				assert.NotContains(t, notes, chartMetricsFile)
				return
			}

			assert.Contains(t, notes, "customMetrics: the chart mounts it at "+chartMetricsFile)
			assert.Contains(t, notes, "only if metrics[].file in your config file, a -f argument, or DCGM_EXPORTER_COLLECTORS names that path.")
		})
	}
}

// configMap.mountPath is removed: setting it fails and names the new path.
func TestChartRejectsConfigMapMountPath(t *testing.T) {
	err := renderChartError(t, map[string]interface{}{
		"configMap": map[string]interface{}{"mountPath": "/etc/dcgm-exporter/config.yaml"},
	})
	require.ErrorContains(t, err, "configMap.mountPath: removed chart values")
	assert.ErrorContains(t, err, "configMap.mountPath -> remove it; the chart mounts the config file at "+configDirectory+"/<configMap.key>")
}
