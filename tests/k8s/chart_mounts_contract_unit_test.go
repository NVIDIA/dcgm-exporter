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

package k8s

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// chartMountsExporterPod returns an exporter pod with the given mounts and
// config file variable.
func chartMountsExporterPod(configFile string, mounts ...corev1.VolumeMount) *corev1.Pod {
	return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name:         exporterContainerName,
		Env:          []corev1.EnvVar{{Name: "DCGM_EXPORTER_CONFIG_FILE", Value: configFile}},
		VolumeMounts: mounts,
	}}}}
}

// TestChartMountsPodError checks that a subPath mount, a missing metrics
// directory or a config file outside the chart's config directory is reported.
func TestChartMountsPodError(t *testing.T) {
	config := corev1.VolumeMount{Name: "dcgm-exporter-config", MountPath: chartConfigDirectory}
	metrics := corev1.VolumeMount{Name: "dcgm-exporter-metrics", MountPath: chartMetricsDirectory, ReadOnly: true}
	testCases := []struct {
		name    string
		pod     *corev1.Pod
		wantErr string
	}{
		{name: "directory mounts", pod: chartMountsExporterPod(chartConfigFile, config, metrics)},
		{
			name: "subPath mount",
			pod: chartMountsExporterPod(chartConfigFile, config, metrics, corev1.VolumeMount{
				Name: "extra", MountPath: "/etc/dcgm-exporter/extra.csv", SubPath: "extra.csv",
			}),
			wantErr: "volume extra is mounted at /etc/dcgm-exporter/extra.csv with subPath",
		},
		{
			name:    "no metrics directory",
			pod:     chartMountsExporterPod(chartConfigFile, config),
			wantErr: "no volume is mounted at " + chartMetricsDirectory,
		},
		{
			name:    "image config file",
			pod:     chartMountsExporterPod("/etc/dcgm-exporter/config.yaml", config, metrics),
			wantErr: `DCGM_EXPORTER_CONFIG_FILE = "/etc/dcgm-exporter/config.yaml"`,
		},
		{name: "no exporter container", pod: &corev1.Pod{}, wantErr: `no "exporter" container`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := chartMountsPodError(tc.pod)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("chartMountsPodError() = %v, want nil", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("chartMountsPodError() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestReadOnlyRootFilesystemError checks that only an explicit true passes.
func TestReadOnlyRootFilesystemError(t *testing.T) {
	testCases := []struct {
		name    string
		context *corev1.SecurityContext
		wantErr bool
	}{
		{name: "read-only", context: &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr.To(true)}},
		{name: "writable", context: &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr.To(false)}, wantErr: true},
		{name: "unset", context: &corev1.SecurityContext{}, wantErr: true},
		{name: "no security context", wantErr: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: exporterContainerName, SecurityContext: tc.context,
			}}}}

			err := readOnlyRootFilesystemError(pod)
			if (err != nil) != tc.wantErr {
				t.Fatalf("readOnlyRootFilesystemError() = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
