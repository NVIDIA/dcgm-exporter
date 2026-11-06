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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChartDumpVolume checks that enabled dumps get a size-limited emptyDir at
// the dump directory and that disabled dumps get no volume.
func TestChartDumpVolume(t *testing.T) {
	tests := []struct {
		name      string
		values    map[string]interface{}
		wantPath  string
		wantLimit string
	}{
		{
			name:   "disabled",
			values: nil,
		},
		{
			name: "config ignored with existing ConfigMap",
			values: map[string]interface{}{
				"config":    map[string]interface{}{"debug_dump": map[string]interface{}{"enabled": true}},
				"configMap": map[string]interface{}{"existingName": "external"},
			},
		},
		{
			name:      "config file with the default directory",
			values:    map[string]interface{}{"config": map[string]interface{}{"debug_dump": map[string]interface{}{"enabled": true}}},
			wantPath:  "/tmp/dcgm-exporter-debug",
			wantLimit: "512Mi",
		},
		{
			name: "config file with custom size",
			values: map[string]interface{}{
				"config":    map[string]interface{}{"debug_dump": map[string]interface{}{"enabled": true, "directory": "/custom"}},
				"debugDump": map[string]interface{}{"sizeLimit": "1Gi"},
			},
			wantPath:  "/custom",
			wantLimit: "1Gi",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ds := requireResourceKind(t, renderChart(t, tc.values), "DaemonSet")
			c := requireContainer(t, ds, "exporter")
			if tc.wantPath == "" {
				for _, v := range ds.Spec.Template.Spec.Volumes {
					require.NotEqual(t, "debug-dumps", v.Name)
				}
				return
			}
			v := requireVolume(t, ds, "debug-dumps")
			require.NotNil(t, v.EmptyDir)
			require.Equal(t, tc.wantLimit, v.EmptyDir.SizeLimit)
			require.Equal(t, tc.wantPath, requireVolumeMount(t, c, "debug-dumps").MountPath)
		})
	}
}

// TestChartDumpUserVolume checks that a values-supplied debug-dumps volume
// replaces the chart's emptyDir.
func TestChartDumpUserVolume(t *testing.T) {
	ds := requireResourceKind(t, renderChart(t, map[string]interface{}{
		"config":            map[string]interface{}{"debug_dump": map[string]interface{}{"enabled": true, "directory": "/host-dumps"}},
		"extraHostVolumes":  []interface{}{map[string]interface{}{"name": "debug-dumps", "hostPath": "/var/log/dumps"}},
		"extraVolumeMounts": []interface{}{map[string]interface{}{"name": "debug-dumps", "mountPath": "/host-dumps"}},
	}), "DaemonSet")
	c := requireContainer(t, ds, "exporter")
	volumes, mounts := 0, 0
	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.Name == "debug-dumps" {
			volumes++
			require.Nil(t, v.EmptyDir)
		}
	}
	for _, m := range c.VolumeMounts {
		if m.Name == "debug-dumps" {
			mounts++
		}
	}
	require.Equal(t, 1, volumes)
	require.Equal(t, 1, mounts)
}

// TestChartDumpSettingsComeFromConfig checks that config.debug_dump reaches the
// exporter's config file unchanged and that the chart passes no dump flag or
// environment variable.
func TestChartDumpSettingsComeFromConfig(t *testing.T) {
	dump := map[string]interface{}{"enabled": true, "directory": "/custom", "retention": "48h", "compression": false}
	resources := renderChart(t, map[string]interface{}{"config": map[string]interface{}{"debug_dump": dump}})

	configMap := requireResourceName(t, resources, "ConfigMap", defaultConfigMap)
	document := parseConfigDocument(t, configMap.Data["config.yaml"])
	require.Equal(t, dump, nestedValue(document, "debug_dump"))

	container := requireContainer(t, requireResourceKind(t, resources, "DaemonSet"), "exporter")
	require.Empty(t, container.Args)
	for name := range containerEnvValues(container) {
		require.NotContains(t, name, "DUMP")
	}
}
