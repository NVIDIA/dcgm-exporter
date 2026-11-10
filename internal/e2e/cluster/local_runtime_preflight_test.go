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
	"strings"
	"testing"
)

func TestRuntimePreflightManifestUsesTheNestedNVIDIARuntime(t *testing.T) {
	manifest := runtimePreflightManifest(LocalConfig{
		RuntimeClass:      "nvidia",
		NodeSelectorKey:   "dcgm-exporter.nvidia.com/gpu-node",
		NodeSelectorValue: "enabled",
	}, "registry.example/cuda:dev")
	for _, want := range []string{
		"runtimeClassName: nvidia",
		"dcgm-exporter.nvidia.com/gpu-node: enabled",
		"imagePullPolicy: Never",
		"command: [\"nvidia-smi\", \"-L\"]",
		"NVIDIA_VISIBLE_DEVICES",
		"- name: NVIDIA_DISABLE_REQUIRE\n      value: \"true\"",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("runtime preflight manifest missing %q:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "resources:") {
		t.Fatalf("runtime preflight must not depend on the device plugin:\n%s", manifest)
	}
}
