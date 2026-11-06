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
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestDeployDCGMCreatesRunOwnedNamespace verifies standalone DCGM setup labels its namespace.
func TestDeployDCGMCreatesRunOwnedNamespace(t *testing.T) {
	runner := &recordingRunner{}
	var out bytes.Buffer
	cfg := DefaultConfig()
	cfg.Kubeconfig = "/tmp/kubeconfig"
	dcgm := DefaultDCGMConfig()
	dcgm.Image = "dcgm:test"
	if err := DeployDCGM(context.Background(), runner, &out, cfg, dcgm); err != nil {
		t.Fatalf("DeployDCGM() error = %v", err)
	}
	if !runner.has("kubectl", "create", "-f", "-") ||
		!runner.stdinContains(`"kind":"Namespace"`) ||
		!runner.stdinContains(`"name":"dcgm-exporter-dcgm"`) ||
		!runner.stdinContains(`"e2eRunName":"dcgm-exporter"`) {
		t.Fatalf("DCGM namespace was not created with ownership labels: %#v", runner.commands)
	}
}

func TestDCGMManifestParsesAsYAML(t *testing.T) {
	cfg := DefaultConfig()
	dcgm := DefaultDCGMConfig()
	dcgm.Image = "dcgm:test"
	dcgm.ImagePullSecret = "pull-secret"

	manifest := dcgmManifest(cfg, dcgm)
	if strings.Contains(manifest, "\t") {
		t.Fatalf("standalone DCGM manifest contains tab indentation:\n%s", manifest)
	}
	for _, document := range strings.Split(manifest, "\n---\n") {
		document = strings.TrimSpace(document)
		if document == "" {
			continue
		}
		var resource map[string]any
		if err := yaml.Unmarshal([]byte(document), &resource); err != nil {
			t.Fatalf("standalone DCGM manifest did not parse:\n%s\nerror: %v", document, err)
		}
	}
}

func TestDCGMManifestIPv6OnlyConfiguresHostengineAndService(t *testing.T) {
	cfg := DefaultConfig()
	dcgm := DefaultDCGMConfig()
	dcgm.Image = "dcgm:test"
	dcgm.IPv6Only = true

	manifest := dcgmManifest(cfg, dcgm)
	if !strings.Contains(manifest, "- \"::\"") {
		t.Fatalf("IPv6-only standalone DCGM manifest did not bind nv-hostengine to IPv6:\n%s", manifest)
	}

	for _, document := range strings.Split(manifest, "\n---\n") {
		var resource struct {
			Kind string `json:"kind"`
			Spec struct {
				IPFamilyPolicy string   `json:"ipFamilyPolicy"`
				IPFamilies     []string `json:"ipFamilies"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(document), &resource); err != nil {
			t.Fatal(err)
		}
		if resource.Kind != "Service" {
			continue
		}
		if resource.Spec.IPFamilyPolicy != "SingleStack" || len(resource.Spec.IPFamilies) != 1 || resource.Spec.IPFamilies[0] != "IPv6" {
			t.Fatalf("IPv6-only Service = %#v, want SingleStack IPv6", resource.Spec)
		}
		return
	}
	t.Fatal("IPv6-only standalone DCGM manifest did not contain a Service")
}
