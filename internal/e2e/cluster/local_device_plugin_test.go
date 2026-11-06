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

package cluster

import (
	"bytes"
	"context"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestSharedGPUDevicePluginConfig checks the time-slicing config for plain and
// renamed shared GPUs.
func TestSharedGPUDevicePluginConfig(t *testing.T) {
	tests := []struct {
		name            string
		replicas        string
		renameByDefault bool
		wantReplicas    int
	}{
		{name: "shared as nvidia.com/gpu", replicas: "2", wantReplicas: 2},
		{name: "shared as nvidia.com/gpu.shared", replicas: "4", renameByDefault: true, wantReplicas: 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var config struct {
				Sharing struct {
					TimeSlicing struct {
						RenameByDefault bool `json:"renameByDefault"`
						Resources       []struct {
							Name     string `json:"name"`
							Replicas int    `json:"replicas"`
						} `json:"resources"`
					} `json:"timeSlicing"`
				} `json:"sharing"`
			}
			text := sharedGPUDevicePluginConfig(tc.replicas, tc.renameByDefault)
			if err := yaml.Unmarshal([]byte(text), &config); err != nil {
				t.Fatalf("config does not parse: %v\n%s", err, text)
			}

			slicing := config.Sharing.TimeSlicing
			if slicing.RenameByDefault != tc.renameByDefault {
				t.Fatalf("renameByDefault = %t, want %t", slicing.RenameByDefault, tc.renameByDefault)
			}
			if len(slicing.Resources) != 1 || slicing.Resources[0].Name != "nvidia.com/gpu" ||
				slicing.Resources[0].Replicas != tc.wantReplicas {
				t.Fatalf("resources = %+v, want nvidia.com/gpu with %d replicas", slicing.Resources, tc.wantReplicas)
			}
		})
	}
}

func TestResetDevicePluginReappliesRuntimeClass(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer

	if err := ResetDevicePlugin(context.Background(), runner, &out, clusterConfigForTest(t), defaultLocalConfigForTest(t)); err != nil {
		t.Fatal(err)
	}
	if !runner.has("kubectl", "apply", "-f", "-") {
		t.Fatalf("reset did not reapply RuntimeClass: %#v", runner.commands)
	}
	if !runner.has("helm", "upgrade", "--install", "nvidia-device-plugin") {
		t.Fatalf("reset did not reinstall device plugin: %#v", runner.commands)
	}
}
