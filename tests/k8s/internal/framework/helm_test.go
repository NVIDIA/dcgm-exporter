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

package framework

import (
	"testing"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/kube"
)

func TestBuildChartSpecUsesServerMutatingInstallsByDefault(t *testing.T) {
	client := &HelmClient{
		chart:     "/charts/dcgm-exporter",
		namespace: "dcgm-exporter",
	}

	spec, err := client.buildChartSpec("install", HelmChartOptions{
		CleanupOnFail: true,
		GenerateName:  true,
		Timeout:       time.Minute,
		Wait:          true,
		DryRun:        false,
	})
	if err != nil {
		t.Fatalf("buildChartSpec returned an error: %v", err)
	}

	if spec.DryRunStrategy != action.DryRunNone {
		t.Fatalf("DryRunStrategy = %q, want %q", spec.DryRunStrategy, action.DryRunNone)
	}
	if spec.ServerSideApply != "true" {
		t.Fatalf("ServerSideApply = %q, want %q", spec.ServerSideApply, "true")
	}
}

func TestBuildChartSpecUsesClientDryRunWhenRequested(t *testing.T) {
	client := &HelmClient{
		chart:     "/charts/dcgm-exporter",
		namespace: "dcgm-exporter",
	}

	spec, err := client.buildChartSpec("install", HelmChartOptions{
		ReleaseName: "dcgm-exporter",
		Timeout:     time.Minute,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("buildChartSpec returned an error: %v", err)
	}

	if spec.DryRunStrategy != action.DryRunClient {
		t.Fatalf("DryRunStrategy = %q, want %q", spec.DryRunStrategy, action.DryRunClient)
	}
}

func TestUninstallChartSpecUsesWaitStrategy(t *testing.T) {
	spec := uninstallChartSpec("dcgm-exporter", "dcgm-exporter")

	if spec.ReleaseName != "dcgm-exporter" {
		t.Fatalf("ReleaseName = %q, want %q", spec.ReleaseName, "dcgm-exporter")
	}
	if spec.Namespace != "dcgm-exporter" {
		t.Fatalf("Namespace = %q, want %q", spec.Namespace, "dcgm-exporter")
	}
	if spec.WaitStrategy != kube.StatusWatcherStrategy {
		t.Fatalf("WaitStrategy = %q, want %q", spec.WaitStrategy, kube.StatusWatcherStrategy)
	}
	if !spec.IgnoreNotFound {
		t.Fatal("IgnoreNotFound = false, want true")
	}
	if spec.Timeout <= 0 {
		t.Fatalf("Timeout = %s, want positive duration", spec.Timeout)
	}
}
