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
	"strings"

	"github.com/NVIDIA/dcgm-exporter/internal/pkg/appconfig"
)

// inactive identifies settings ignored by the selected runtime mode.
func inactive(r *appconfig.Resolved, field string) string {
	if reason := inactiveKubernetes(r, field); reason != "" {
		return reason
	}

	if reason := inactiveOutput(r, field); reason != "" {
		return reason
	}

	return inactiveSource(r, field)
}

// inactiveKubernetes distinguishes disabled integration from disabled pod labels,
// including the kubelet socket and resource names without a Kubernetes prefix.
func inactiveKubernetes(r *appconfig.Resolved, field string) string {
	const kubernetesDisabledReason = "Kubernetes mode is disabled"
	c := r.Config
	if appconfig.ContainerDisplacesKubernetes(r.Config) && isKubernetesSetting(field) {
		return "Kubernetes mode is off because " + appconfig.EnrichmentContainer.Key() + " is true"
	}

	if !c.Kubernetes {
		if isKubernetesSetting(field) {
			return kubernetesDisabledReason
		}

		return ""
	}

	if field != "KubernetesPodLabelAllowlistRegex" {
		return ""
	}

	if !c.KubernetesEnablePodLabels {
		return "Kubernetes pod labels are disabled"
	}

	return ""
}

// isKubernetesSetting says the setting only matters while Kubernetes is active.
func isKubernetesSetting(field string) bool {
	return strings.HasPrefix(field, "Kubernetes") || field == "PodResourcesKubeletSocket" || field == "NvidiaResourceNames"
}

// inactiveOutput explains an output or debug dump setting that has no effect.
func inactiveOutput(r *appconfig.Resolved, field string) string {
	c := r.Config
	switch field {
	case "DumpConfig.Directory", "DumpConfig.Retention", "DumpConfig.Compression", "DumpConfig.MinInterval",
		"DumpConfig.MaxFileBytes", "DumpConfig.MaxTotalBytes", "DumpConfig.MaxFiles":
		if !c.DumpConfig.Enabled {
			return "debug dumps are disabled"
		}
	case "Address":
		if c.WebSystemdSocket {
			return "systemd socket activation supplies listeners"
		}
	case "MaxConcurrentScrapes", "EnableExporterMetrics":
		if c.DisablePrometheus {
			return "Prometheus metrics endpoint is disabled"
		}
	}

	return ""
}

// inactiveSource explains a source setting that has no effect.
func inactiveSource(r *appconfig.Resolved, field string) string {
	c := r.Config
	switch field {
	case "ContainerLabels", "ContainerRuntimeSocket":
		return inactiveContainerLabels(r)
	case "HPCJobMappingDir", appconfig.HPCJobsSwitchField, "HPCJobMappingMaxFileBytes",
		"HPCJobMappingMaxJobIDBytes", "HPCJobMappingMaxJobsPerFile":
		if c.HPCJobMappingDir == "" {
			return "HPC job labels are disabled"
		}
	case "DCGMLogLevel":
		if !c.EnableDCGMLog {
			return "DCGM logging is disabled"
		}
	case "GPUBindUnbindPollInterval":
		if !c.EnableGPUBindUnbindWatch {
			return "GPU bind/unbind watching is disabled"
		}
	case "RemoteHEInfo":
		if !c.UseRemoteHE {
			return "embedded hostengine is selected"
		}
	}

	return ""
}

// inactiveContainerLabels says when the configuration itself switches the
// container labels off, or has Kubernetes win. When both sources are auto, which
// one is used depends on the host at startup, so nothing is inactive here; note
// says it.
func inactiveContainerLabels(r *appconfig.Resolved) string {
	c := r.Config
	switch {
	case !c.ContainerLabels:
		return "container labels are disabled"
	case !c.Kubernetes || appconfig.ContainerDisplacesKubernetes(r.Config):
		return ""
	case c.EnrichmentModes.Kubernetes == appconfig.EnrichmentRequired:
		return "container labels are ignored in Kubernetes mode"
	default:
		return ""
	}
}

// enrichmentNote describes how the running exporter treats an enrichment
// switch. Inspection cannot know whether a source is available, so it says what
// the exporter will check rather than claiming a result.
func enrichmentNote(r *appconfig.Resolved, field string) string {
	c := r.Config
	var mode appconfig.EnrichmentMode
	var on bool
	switch field {
	case "Kubernetes":
		mode, on = c.EnrichmentModes.Kubernetes, c.Kubernetes
	case "ContainerLabels":
		mode, on = c.EnrichmentModes.Container, c.ContainerLabels
	case "KubernetesEnableDRA":
		mode, on = c.EnrichmentModes.DRA, c.KubernetesEnableDRA
	case appconfig.HPCJobsSwitchField:
		mode, on = c.EnrichmentModes.HPCJobs, c.HPCJobMappingDir != ""
	default:
		return ""
	}

	kubernetesActive := c.Kubernetes && !appconfig.ContainerDisplacesKubernetes(r.Config)
	switch {
	case field == "KubernetesEnableDRA" && !kubernetesActive:
		// DRA needs Kubernetes labels, so startup skips an auto source and fails
		// a required one at once instead of waiting for it.
		if mode == appconfig.EnrichmentRequired {
			return "required: startup fails because Kubernetes labels are not active"
		}

		return ""
	case field == "ContainerLabels" && c.Kubernetes && c.EnrichmentModes.Kubernetes == appconfig.EnrichmentRequired:
		// A required Kubernetes source is active or startup has failed, so the
		// container runtime socket is never checked; the inactive reason says so.
		return ""
	case mode == appconfig.EnrichmentRequired:
		wait := appconfig.FormatDuration(appconfig.RequiredEnrichmentWait)
		return "required: the exporter waits up to " + wait + " at startup"
	case !on:
		return ""
	case field == "Kubernetes" && !kubernetesActive:
		// A required container source turns Kubernetes off, so startup never checks it.
		return ""
	case field == "ContainerLabels" && kubernetesActive:
		return "auto: used if Kubernetes is unavailable at startup (checked by the running exporter)"
	default:
		return "auto: used when available (checked by the running exporter)"
	}
}
