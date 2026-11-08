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
package configinspect

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

func TestInactiveNamesWhyASettingHasNoEffect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*appconfig.Config)
		field  string
		want   string
	}{
		{"dump directory without dumps", func(*appconfig.Config) {}, "DumpConfig.Directory", "debug dumps are disabled"},
		{"dump retention with dumps", func(c *appconfig.Config) { c.DumpConfig.Enabled = true }, "DumpConfig.Retention", ""},
		{"listener under systemd", func(c *appconfig.Config) { c.WebSystemdSocket = true }, "Address", "systemd socket activation supplies listeners"},
		{"scrape limit without Prometheus", func(c *appconfig.Config) { c.DisablePrometheus = true }, "MaxConcurrentScrapes", "Prometheus metrics endpoint is disabled"},
		{"HPC directory without HPC labels", func(*appconfig.Config) {}, "HPCJobMappingDir", "HPC job labels are disabled"},
		{"HPC directory with HPC labels", func(c *appconfig.Config) { c.HPCJobMappingDir = "/jobs" }, "HPCJobMappingDir", ""},
		{"DCGM log level without the log", func(*appconfig.Config) {}, "DCGMLogLevel", "DCGM logging is disabled"},
		{"poll interval without the watch", func(*appconfig.Config) {}, "GPUBindUnbindPollInterval", "GPU bind/unbind watching is disabled"},
		{"remote address with the embedded engine", func(*appconfig.Config) {}, "RemoteHEInfo", "embedded hostengine is selected"},
		{"remote address with a remote engine", func(c *appconfig.Config) { c.UseRemoteHE = true }, "RemoteHEInfo", ""},
		{"kubelet socket without Kubernetes", func(*appconfig.Config) {}, "PodResourcesKubeletSocket", "Kubernetes mode is disabled"},
		{"kubelet socket with Kubernetes", func(c *appconfig.Config) { c.Kubernetes = true }, "PodResourcesKubeletSocket", ""},
		{"pod label allowlist without pod labels", func(c *appconfig.Config) { c.Kubernetes = true }, "KubernetesPodLabelAllowlistRegex", "Kubernetes pod labels are disabled"},
		{"pod labels on", func(c *appconfig.Config) { c.Kubernetes, c.KubernetesEnablePodLabels = true, true }, "KubernetesPodLabelAllowlistRegex", ""},
		{"container labels off", func(*appconfig.Config) {}, "ContainerLabels", "container labels are disabled"},
		{"container labels alone", func(c *appconfig.Config) { c.ContainerLabels = true }, "ContainerLabels", ""},
		{"container labels under required Kubernetes", func(c *appconfig.Config) {
			c.ContainerLabels, c.Kubernetes = true, true
			c.EnrichmentModes.Kubernetes = appconfig.EnrichmentRequired
		}, "ContainerLabels", "container labels are ignored in Kubernetes mode"},
		{"container labels with both auto", func(c *appconfig.Config) {
			c.ContainerLabels, c.Kubernetes = true, true
			c.EnrichmentModes = appconfig.EnrichmentModes{Kubernetes: appconfig.EnrichmentAuto, Container: appconfig.EnrichmentAuto}
		}, "ContainerLabels", ""},
		{"Kubernetes setting under a required container source", func(c *appconfig.Config) {
			c.Kubernetes, c.ContainerLabels = true, true
			c.EnrichmentModes = appconfig.EnrichmentModes{Kubernetes: appconfig.EnrichmentAuto, Container: appconfig.EnrichmentRequired}
		}, "KubernetesEnableDRA", "Kubernetes mode is off because metrics[0].enrichment.container is true"},
		{"an unrelated setting", func(*appconfig.Config) {}, "CollectInterval", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resolvedDefaults()
			tc.change(r.Config)
			assert.Equal(t, tc.want, inactive(r, tc.field))
		})
	}
}

func TestEnrichmentNoteSaysHowTheExporterTreatsASwitch(t *testing.T) {
	const (
		required = "required: the exporter waits up to 1m at startup"
		auto     = "auto: used when available (checked by the running exporter)"
		fallback = "auto: used if Kubernetes is unavailable at startup (checked by the running exporter)"

		draNeedsKubernetes = "required: startup fails because Kubernetes labels are not active"
	)

	for _, tc := range []struct {
		name   string
		change func(*appconfig.Config)
		field  string
		want   string
	}{
		{"required Kubernetes", func(c *appconfig.Config) {
			c.Kubernetes, c.EnrichmentModes.Kubernetes = true, appconfig.EnrichmentRequired
		}, "Kubernetes", required},
		{"Kubernetes off", func(*appconfig.Config) {}, "Kubernetes", ""},
		{"Kubernetes on", func(c *appconfig.Config) { c.Kubernetes = true }, "Kubernetes", auto},
		{"DRA required", func(c *appconfig.Config) {
			c.Kubernetes, c.KubernetesEnableDRA, c.EnrichmentModes.DRA = true, true, appconfig.EnrichmentRequired
		}, "KubernetesEnableDRA", required},
		{"DRA on", func(c *appconfig.Config) { c.Kubernetes, c.KubernetesEnableDRA = true, true }, "KubernetesEnableDRA", auto},
		{"DRA on without Kubernetes", func(c *appconfig.Config) { c.KubernetesEnableDRA = true }, "KubernetesEnableDRA", ""},
		{"DRA required without Kubernetes", func(c *appconfig.Config) {
			c.KubernetesEnableDRA, c.EnrichmentModes.DRA = true, appconfig.EnrichmentRequired
		}, "KubernetesEnableDRA", draNeedsKubernetes},
		{"DRA required under a required container source", func(c *appconfig.Config) {
			c.Kubernetes, c.KubernetesEnableDRA, c.ContainerLabels = true, true, true
			c.EnrichmentModes.DRA, c.EnrichmentModes.Container = appconfig.EnrichmentRequired, appconfig.EnrichmentRequired
		}, "KubernetesEnableDRA", draNeedsKubernetes},
		{"container labels under a required Kubernetes source", func(c *appconfig.Config) {
			c.ContainerLabels, c.Kubernetes, c.EnrichmentModes.Kubernetes = true, true, appconfig.EnrichmentRequired
		}, "ContainerLabels", ""},
		{"required container labels under a required Kubernetes source", func(c *appconfig.Config) {
			c.ContainerLabels, c.Kubernetes = true, true
			c.EnrichmentModes.Kubernetes, c.EnrichmentModes.Container = appconfig.EnrichmentRequired, appconfig.EnrichmentRequired
		}, "ContainerLabels", ""},
		{"HPC jobs on", func(c *appconfig.Config) { c.HPCJobMappingDir = "/jobs" }, appconfig.HPCJobsSwitchField, auto},
		{"HPC jobs required", func(c *appconfig.Config) { c.EnrichmentModes.HPCJobs = appconfig.EnrichmentRequired }, appconfig.HPCJobsSwitchField, required},
		{"container labels with Kubernetes auto", func(c *appconfig.Config) {
			c.ContainerLabels, c.Kubernetes = true, true
		}, "ContainerLabels", fallback},
		{"container labels alone", func(c *appconfig.Config) { c.ContainerLabels = true }, "ContainerLabels", auto},
		{"Kubernetes under a required container source", func(c *appconfig.Config) {
			c.Kubernetes, c.ContainerLabels = true, true
			c.EnrichmentModes.Container = appconfig.EnrichmentRequired
		}, "Kubernetes", ""},
		{"a setting that is not a switch", func(*appconfig.Config) {}, "CollectInterval", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resolvedDefaults()
			tc.change(r.Config)
			assert.Equal(t, tc.want, enrichmentNote(r, tc.field))
		})
	}
}
