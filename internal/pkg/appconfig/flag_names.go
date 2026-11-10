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

// Flag names of the exporter's command-line flags, without the leading dashes.
// Validation and explain messages name flags, so the names live here; pkg/cmd
// defines the flags themselves and keeps its CLI-prefixed names as aliases of
// these.
const (
	FlagConfigFile                       = "config-file"
	FlagFieldsFile                       = "collectors"
	FlagAddress                          = "address"
	FlagCollectInterval                  = "collect-interval"
	FlagWatchMaxKeepAge                  = "watch-max-keep-age"
	FlagWatchMaxKeepSamples              = "watch-max-keep-samples"
	FlagKubernetes                       = "kubernetes"
	FlagKubernetesEnablePodLabels        = "kubernetes-enable-pod-labels"
	FlagKubernetesEnablePodUID           = "kubernetes-enable-pod-uid"
	FlagKubernetesGPUIDType              = "kubernetes-gpu-id-type"
	FlagKubernetesPodLabelAllowlistRegex = "kubernetes-pod-label-allowlist-regex"
	FlagUseOldNamespace                  = "use-old-namespace"
	FlagRemoteHEInfo                     = "remote-hostengine-info"
	FlagGPUDevices                       = "devices"
	FlagSwitchDevices                    = "switch-devices"
	FlagCPUDevices                       = "cpu-devices"
	FlagNoHostname                       = "no-hostname"
	FlagUseFakeGPUs                      = "fake-gpus"
	FlagConfigMapData                    = "configmap-data"
	FlagWebSystemdSocket                 = "web-systemd-socket"
	FlagWebConfigFile                    = "web-config-file"
	FlagWebReadTimeout                   = "web-read-timeout"
	FlagWebWriteTimeout                  = "web-write-timeout"
	FlagMaxConcurrentScrapes             = "max-concurrent-scrapes"
	FlagEnableExporterMetrics            = "enable-exporter-metrics"
	FlagXIDCountWindowSize               = "xid-count-window-size"
	FlagReplaceBlanksInModelName         = "replace-blanks-in-model-name"
	FlagDebugMode                        = "debug"
	FlagClockEventsCountWindowSize       = "clock-events-count-window-size"
	FlagEnableDCGMLog                    = "enable-dcgm-log"
	FlagDCGMLogLevel                     = "dcgm-log-level"
	FlagDCGMCleanupTimeout               = "dcgm-cleanup-timeout"
	FlagLogFormat                        = "log-format"
	FlagPodResourcesKubeletSocket        = "pod-resources-kubelet-socket"
	FlagHPCJobMappingDir                 = "hpc-job-mapping-dir"
	FlagHPCJobMappingMaxFileBytes        = "hpc-job-mapping-max-file-bytes"
	FlagHPCJobMappingMaxJobIDBytes       = "hpc-job-mapping-max-job-id-bytes"
	FlagHPCJobMappingMaxJobsPerFile      = "hpc-job-mapping-max-jobs-per-file"
	FlagContainerLabels                  = "container-labels"
	FlagContainerRuntimeSocket           = "container-runtime-socket"
	FlagNvidiaResourceNames              = "nvidia-resource-names"
	FlagKubernetesVirtualGPUs            = "kubernetes-virtual-gpus"
	FlagDumpEnabled                      = "dump-enabled"
	FlagDumpDirectory                    = "dump-directory"
	FlagDumpRetention                    = "dump-retention"
	FlagDumpCompression                  = "dump-compression"
	FlagDumpMinInterval                  = "dump-min-interval"
	FlagDumpMaxFileBytes                 = "dump-max-file-bytes"
	FlagDumpMaxTotalBytes                = "dump-max-total-bytes"
	FlagDumpMaxFiles                     = "dump-max-files"
	FlagKubernetesEnableDRA              = "kubernetes-enable-dra"
	FlagDisableStartupValidate           = "disable-startup-validate"
	FlagEnableGPUBindUnbindWatch         = "enable-gpu-bind-unbind-watch"
	FlagGPUBindUnbindPollInterval        = "gpu-bind-unbind-poll-interval"
	FlagEnablePprof                      = "enable-pprof"
)
