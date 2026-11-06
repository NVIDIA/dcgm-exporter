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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestExistingConfigPodError checks the config file path the exporter pod must read.
func TestExistingConfigPodError(t *testing.T) {
	podWith := func(env ...corev1.EnvVar) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "exporter"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: exporterContainerName, Env: env}}},
		}
	}

	tests := []struct {
		name    string
		pod     *corev1.Pod
		wantErr bool
	}{
		{name: "path under the config directory", pod: podWith(corev1.EnvVar{Name: "DCGM_EXPORTER_CONFIG_FILE", Value: chartConfigDirectory + "/" + existingConfigKey})},
		{name: "default key", pod: podWith(corev1.EnvVar{Name: "DCGM_EXPORTER_CONFIG_FILE", Value: chartConfigFile}), wantErr: true},
		{name: "no variable", pod: podWith(), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := existingConfigPodError(tt.pod, existingConfigKey); (got != nil) != tt.wantErr {
				t.Errorf("existingConfigPodError() = %v, wantErr %t", got, tt.wantErr)
			}
		})
	}
}

// TestExistingConfigMetricsError checks that only the config file's metrics pass.
func TestExistingConfigMetricsError(t *testing.T) {
	fromConfig := "# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge\nDCGM_FI_DEV_GPU_TEMP_CELSIUS 40\n" +
		"# TYPE DCGM_FI_DEV_FB_USED_BYTES gauge\nDCGM_FI_DEV_FB_USED_BYTES 1\n" +
		"# TYPE go_goroutines gauge\ngo_goroutines 7\n"
	withDefaults := fromConfig + "# TYPE DCGM_FI_DEV_BOARD_POWER_WATTS gauge\nDCGM_FI_DEV_BOARD_POWER_WATTS 90\n"
	withoutGo := "# TYPE DCGM_FI_DEV_GPU_TEMP_CELSIUS gauge\nDCGM_FI_DEV_GPU_TEMP_CELSIUS 40\n" +
		"# TYPE DCGM_FI_DEV_FB_USED_BYTES gauge\nDCGM_FI_DEV_FB_USED_BYTES 1\n"

	tests := []struct {
		name    string
		metrics string
		wantErr bool
	}{
		{name: "the config file's metrics", metrics: fromConfig},
		{name: "default list in use", metrics: withDefaults, wantErr: true},
		{name: "exporter metrics off", metrics: withoutGo, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := existingConfigMetricsError([]byte(tt.metrics)); (got != nil) != tt.wantErr {
				t.Errorf("existingConfigMetricsError() = %v, wantErr %t", got, tt.wantErr)
			}
		})
	}
}
