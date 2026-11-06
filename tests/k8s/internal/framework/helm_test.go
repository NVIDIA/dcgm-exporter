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
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/kube"
)

func TestBuildChartSpecPreservesHelmArguments(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   []any
	}{
		{
			name:   "nonempty list",
			values: []string{"arguments={-f=/etc/counters with spaces.csv,-c=1000}"},
			want:   []any{"-f=/etc/counters with spaces.csv", "-c=1000", "--debug"},
		},
		{
			name:   "empty list",
			values: []string{"arguments={}"},
			want:   []any{"--debug"},
		},
		{
			name: "no arguments",
			want: []any{"--debug"},
		},
		{
			name:   "existing debug",
			values: []string{"arguments={-c=1000,--debug}"},
			want:   []any{"-c=1000", "--debug"},
		},
		{
			name:   "indexed arguments",
			values: []string{"arguments[0]=-c=1000"},
			want:   []any{"-c=1000", "--debug"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &HelmClient{}
			spec, err := client.buildChartSpec("install", HelmChartOptions{GenerateName: true}, WithValues(tt.values...))
			if err != nil {
				t.Fatal(err)
			}
			values, err := spec.ValuesOptions.MergeValues(nil)
			if err != nil {
				t.Fatal(err)
			}
			args, ok := values["arguments"].([]any)
			if !ok || !reflect.DeepEqual(args, tt.want) {
				t.Fatalf("parsed arguments = %#v, want %#v", values["arguments"], tt.want)
			}
		})
	}
}

// TestBuildChartSpecUsesServerMutatingInstallsByDefault keeps normal installs server-side and live.
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

// TestBuildChartSpecUsesClientDryRunWhenRequested keeps an explicit dry run client-side.
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

// TestBuildChartSpecPreservesDisabledCleanupOnFail retains failed resources for diagnostics.
func TestBuildChartSpecPreservesDisabledCleanupOnFail(t *testing.T) {
	client := &HelmClient{chart: "/charts/dcgm-exporter", namespace: "dcgm-exporter"}
	spec, err := client.buildChartSpec("install", HelmChartOptions{
		CleanupOnFail: false,
		GenerateName:  true,
		Timeout:       time.Minute,
		Wait:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.CleanupOnFail {
		t.Fatal("CleanupOnFail = true, want failed resources retained for outer diagnostics")
	}
}

// TestUninstallChartSpecUsesWaitStrategy waits for release removal and ignores missing releases.
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

// TestBuildChartSpecPinsTheChartVersion checks that a chart version reaches the chart spec.
func TestBuildChartSpecPinsTheChartVersion(t *testing.T) {
	client := &HelmClient{chart: "previous/dcgm-exporter", namespace: "dcgm-exporter"}

	spec, err := client.buildChartSpec("install", HelmChartOptions{GenerateName: true, Version: "4.8.4"})
	if err != nil {
		t.Fatalf("buildChartSpec returned an error: %v", err)
	}

	if spec.Version != "4.8.4" {
		t.Fatalf("Version = %q, want %q", spec.Version, "4.8.4")
	}
}

// TestRepositoryConfigFileIsAFileInsideTheDirectory checks that the Helm
// repository list is a file path below the directory the client removes on close.
func TestRepositoryConfigFileIsAFileInsideTheDirectory(t *testing.T) {
	directory := t.TempDir()

	got := repositoryConfigFile(directory)

	if want := filepath.Join(directory, "repositories.yaml"); got != want {
		t.Errorf("repositoryConfigFile = %q, want %q", got, want)
	}
}
