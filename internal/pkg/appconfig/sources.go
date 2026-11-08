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

package appconfig

import (
	"fmt"
	"regexp"
	"strings"
)

// YAMLSources configures the data sources.
type YAMLSources struct {
	DCGM       *YAMLDCGMSource       `yaml:"dcgm,omitempty" json:"dcgm,omitempty"`
	Kubernetes *YAMLKubernetesSource `yaml:"kubernetes,omitempty" json:"kubernetes,omitempty"`
	Container  *YAMLContainerSource  `yaml:"container,omitempty" json:"container,omitempty"`
	DRA        *YAMLDRASource        `yaml:"dra,omitempty" json:"dra,omitempty"`
	HPCJobs    *YAMLHPCJobsSource    `yaml:"hpc_jobs,omitempty" json:"hpc_jobs,omitempty"`
}

// YAMLDCGMSource configures exporter-wide DCGM behavior.
type YAMLDCGMSource struct {
	HostEngine       string                `yaml:"hostengine" json:"hostengine"`
	Watch            *YAMLDCGMWatch        `yaml:"watch,omitempty" json:"watch,omitempty"`
	DetectBindUnbind *YAMLDetectBindUnbind `yaml:"detect_bind_unbind,omitempty" json:"detect_bind_unbind,omitempty"`
	// CleanupTimeout sets Config.DCGMCleanupTimeout, which bounds each wait
	// for DCGM, including starting or stopping the GPU bind/unbind watch.
	CleanupTimeout string           `yaml:"cleanup_timeout,omitempty" json:"cleanup_timeout,omitempty"`
	EmbeddedLog    *YAMLEmbeddedLog `yaml:"embedded_log,omitempty" json:"embedded_log,omitempty"`
}

// YAMLDetectBindUnbind configures physical GPU bind and unbind detection.
type YAMLDetectBindUnbind struct {
	Enabled *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Every   string `yaml:"every,omitempty" json:"every,omitempty"`
}

// YAMLEmbeddedLog turns on the embedded DCGM library's own log. Leaving the
// block out leaves that log off.
type YAMLEmbeddedLog struct {
	Level string `yaml:"level" json:"level"`
	// File is part of the shared key set. Only stdout is supported, because
	// that is where the embedded library writes.
	File string `yaml:"file" json:"file"`
}

// YAMLKubernetesSource configures how Kubernetes pod information is read.
// These settings do not switch Kubernetes mapping on; --kubernetes does that.
// pod_uid and pod_labels choose which pod details are added once it is on.
type YAMLKubernetesSource struct {
	PodResourcesSocket      string         `yaml:"pod_resources_socket" json:"pod_resources_socket"`
	GPUIDType               string         `yaml:"gpu_id_type" json:"gpu_id_type"`
	AdditionalResourceNames []string       `yaml:"additional_resource_names" json:"additional_resource_names"`
	VirtualGPUs             *bool          `yaml:"virtual_gpus,omitempty" json:"virtual_gpus,omitempty"`
	PodUID                  *bool          `yaml:"pod_uid,omitempty" json:"pod_uid,omitempty"`
	PodLabels               *YAMLPodLabels `yaml:"pod_labels,omitempty" json:"pod_labels,omitempty"`
}

// YAMLPodLabels configures the pod labels added to metrics. allow lists the
// RE2 patterns a label name must match; turning labels on needs at least one.
type YAMLPodLabels struct {
	Enabled *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Allow   []string `yaml:"allow" json:"allow"`
}

// YAMLContainerSource configures how container runtime labels are read.
// runtime_socket only says where the socket is; metrics[].enrichment.container
// decides whether container labels are on, and --container-labels still does
// that for a version 2 file without a metrics entry.
type YAMLContainerSource struct {
	RuntimeSocket string `yaml:"runtime_socket,omitempty" json:"runtime_socket,omitempty"`
}

// YAMLDRASource holds the Dynamic Resource Allocation source settings. The mode
// is accepted and checked, and only kubernetes is supported. It sets nothing
// itself: metrics[].enrichment.dra switches DRA labels on, and
// --kubernetes-enable-dra still does that for a config file without a metrics
// entry. A switch written true needs this block.
type YAMLDRASource struct {
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// YAMLHPCJobsSource configures where HPC job mapping files are read. With a
// metrics entry, metrics[].enrichment.hpc_jobs decides whether HPC job labels
// are on, and a switch left out means auto with the default directory when
// mapping_dir is not set. A file without a metrics entry has no switch, so
// mapping_dir alone turns the labels on. The max_* keys bound what one mapping
// file may hold.
type YAMLHPCJobsSource struct {
	MappingDir     string `yaml:"mapping_dir,omitempty" json:"mapping_dir,omitempty"`
	MaxFileBytes   *int64 `yaml:"max_file_bytes,omitempty" json:"max_file_bytes,omitempty"`
	MaxJobIDBytes  *int64 `yaml:"max_job_id_bytes,omitempty" json:"max_job_id_bytes,omitempty"`
	MaxJobsPerFile *int64 `yaml:"max_jobs_per_file,omitempty" json:"max_jobs_per_file,omitempty"`
}

// embeddedLogLevels maps each YAML level name to the DCGM log level the flag
// accepts.
var embeddedLogLevels = map[string]string{
	"none":    "NONE",
	"fatal":   "FATAL",
	"error":   "ERROR",
	"warning": "WARN",
	"info":    "INFO",
	"debug":   "DEBUG",
	"verbose": "VERB",
}

// applySources applies the exporter-wide DCGM settings.
func (y *YAMLConfig) applySources(config *Config, rec originRecorder) error {
	if y.Sources == nil || y.Sources.DCGM == nil {
		return nil
	}

	source := y.Sources.DCGM
	if err := source.applyHostEngineAndLog(config, rec); err != nil {
		return err
	}

	if err := source.DetectBindUnbind.apply(config, rec); err != nil {
		return err
	}

	if err := source.applyCleanupTimeout(config, rec); err != nil {
		return err
	}

	if source.Watch != nil {
		const watchPath = "sources.dcgm.watch"
		retention, err := source.Watch.retentionOverride(watchPath)
		if err != nil {
			return err
		}

		if err := rejectZeroRetention(retention, watchPath, "max_keep_age", "max_keep_samples"); err != nil {
			return err
		}

		config.WatchRetention = retention.Resolve(config.WatchRetention)
		source.Watch.record(rec, "WatchRetention", watchPath)
	}

	return nil
}

// apply applies the bind and unbind detection settings. A missing block
// changes nothing.
func (d *YAMLDetectBindUnbind) apply(config *Config, rec originRecorder) error {
	if d == nil {
		return nil
	}

	if d.Enabled != nil {
		config.EnableGPUBindUnbindWatch = *d.Enabled
		rec.record("EnableGPUBindUnbindWatch", "sources.dcgm.detect_bind_unbind.enabled")
	}

	every := strings.TrimSpace(d.Every)
	if every == "" {
		return nil
	}

	const everyKey = "sources.dcgm.detect_bind_unbind.every"
	pollInterval, err := parsePositiveDuration(every, everyKey)
	if err != nil {
		return err
	}

	config.GPUBindUnbindPollInterval = pollInterval
	rec.record("GPUBindUnbindPollInterval", everyKey)

	return nil
}

// applyHostEngineAndLog applies sources.dcgm.hostengine and embedded_log.
// Whether the two can be combined is checked on the resolved Config, so it
// covers a value from any source.
func (s *YAMLDCGMSource) applyHostEngineAndLog(config *Config, rec originRecorder) error {
	if hostEngine := strings.TrimSpace(s.HostEngine); hostEngine != "" {
		config.UseRemoteHE = true
		config.RemoteHEInfo = hostEngine
		rec.record("UseRemoteHE", "sources.dcgm.hostengine")
		rec.record("RemoteHEInfo", "sources.dcgm.hostengine")
	}

	log := s.EmbeddedLog
	if log == nil {
		return nil
	}

	if file := strings.TrimSpace(log.File); file != "" && file != "stdout" {
		return keyErrorf("sources.dcgm.embedded_log.file", "%q is not supported; only stdout is available", log.File)
	}

	if level := strings.TrimSpace(log.Level); level != "" {
		dcgmLevel, ok := embeddedLogLevels[level]
		if !ok {
			return keyErrorf("sources.dcgm.embedded_log.level", "%q is not supported; use none, fatal, error, "+
				"warning, info, debug or verbose", log.Level)
		}

		config.DCGMLogLevel = dcgmLevel
		rec.record("DCGMLogLevel", "sources.dcgm.embedded_log.level")
	}

	config.EnableDCGMLog = true
	rec.record("EnableDCGMLog", "sources.dcgm.embedded_log")
	return nil
}

// applyWorkloadSources applies the Kubernetes, container, DRA and HPC job
// source settings. The DCGM source is applied by applySources.
func (y *YAMLConfig) applyWorkloadSources(config *Config, rec originRecorder) error {
	if y.Sources == nil {
		return nil
	}

	if err := y.Sources.Kubernetes.apply(config, rec); err != nil {
		return err
	}

	if c := y.Sources.Container; c != nil {
		if socket := strings.TrimSpace(c.RuntimeSocket); socket != "" {
			config.ContainerRuntimeSocket = socket
			rec.record("ContainerRuntimeSocket", "sources.container.runtime_socket")
		}
	}

	if d := y.Sources.DRA; d != nil {
		if mode := strings.TrimSpace(d.Mode); mode != "" && mode != "kubernetes" {
			return keyErrorf("sources.dra.mode", "%q is not supported; only kubernetes is available", d.Mode)
		}
	}

	return y.Sources.HPCJobs.apply(config, rec)
}

// kubernetesKeyBase is the key prefix of each Kubernetes source setting.
const kubernetesKeyBase = "sources.kubernetes."

// apply applies the Kubernetes source settings.
func (k *YAMLKubernetesSource) apply(config *Config, rec originRecorder) error {
	if k == nil {
		return nil
	}

	if socket := strings.TrimSpace(k.PodResourcesSocket); socket != "" {
		config.PodResourcesKubeletSocket = socket
		rec.record("PodResourcesKubeletSocket", kubernetesKeyBase+"pod_resources_socket")
	}

	if err := k.applyGPUIDType(config, rec); err != nil {
		return err
	}

	if err := k.applyAdditionalResourceNames(config, rec); err != nil {
		return err
	}

	if k.VirtualGPUs != nil {
		config.KubernetesVirtualGPUs = *k.VirtualGPUs
		rec.record("KubernetesVirtualGPUs", kubernetesKeyBase+"virtual_gpus")
	}

	return k.applyPodDetails(config, rec)
}

// applyPodDetails applies pod_uid and pod_labels. A missing pod_labels block
// changes nothing.
func (k *YAMLKubernetesSource) applyPodDetails(config *Config, rec originRecorder) error {
	if k.PodUID != nil {
		config.KubernetesEnablePodUID = *k.PodUID
		rec.record("KubernetesEnablePodUID", kubernetesKeyBase+"pod_uid")
	}

	labels := k.PodLabels
	if labels == nil {
		return nil
	}

	if labels.Enabled != nil {
		config.KubernetesEnablePodLabels = *labels.Enabled
		rec.record("KubernetesEnablePodLabels", kubernetesKeyBase+"pod_labels.enabled")
	}

	return applyPodLabelAllowlist(labels.Allow, config, rec)
}

// applyGPUIDType sets how GPUs are identified in pod mappings.
func (k *YAMLKubernetesSource) applyGPUIDType(config *Config, rec originRecorder) error {
	switch idType := strings.TrimSpace(k.GPUIDType); idType {
	case "":
	case string(GPUUID), string(DeviceName):
		config.KubernetesGPUIdType = KubernetesGPUIDType(idType)
		rec.record("KubernetesGPUIdType", kubernetesKeyBase+"gpu_id_type")
	default:
		return keyErrorf(kubernetesKeyBase+"gpu_id_type", "%q is not supported; use %s or %s",
			k.GPUIDType, GPUUID, DeviceName)
	}

	return nil
}

// applyAdditionalResourceNames sets the extra resource names to attribute to pods.
func (k *YAMLKubernetesSource) applyAdditionalResourceNames(config *Config, rec originRecorder) error {
	if k.AdditionalResourceNames == nil {
		return nil
	}

	names := make([]string, len(k.AdditionalResourceNames))
	for i, name := range k.AdditionalResourceNames {
		name = strings.TrimSpace(name)
		if name == "" {
			return keyErrorf(fmt.Sprintf(kubernetesKeyBase+"additional_resource_names[%d]", i), "must not be empty")
		}

		names[i] = name
	}

	config.NvidiaResourceNames = names
	rec.record("NvidiaResourceNames", kubernetesKeyBase+"additional_resource_names")

	return nil
}

// applyPodLabelAllowlist sets the pod label patterns after checking each
// compiles. A nil allow changes nothing.
func applyPodLabelAllowlist(allow []string, config *Config, rec originRecorder) error {
	if allow == nil {
		return nil
	}

	patterns := make([]string, len(allow))
	for i, pattern := range allow {
		if _, err := regexp.Compile(pattern); err != nil {
			return keyWrap(fmt.Sprintf(kubernetesKeyBase+"pod_labels.allow[%d]", i), err)
		}

		patterns[i] = pattern
	}

	config.KubernetesPodLabelAllowlistRegex = patterns
	rec.record("KubernetesPodLabelAllowlistRegex", kubernetesKeyBase+"pod_labels.allow")

	return nil
}
