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

import "time"

// DefaultConfig builds the single source of runtime defaults before YAML and
// explicit CLI overrides are applied. NewApp reads this same value for its
// displayed flag defaults, so help text and runtime behavior stay aligned.
func DefaultConfig() *Config {
	return &Config{
		Address:         ":9400",
		CollectInterval: 30000,
		WatchRetention:  DefaultWatchRetention(),

		Kubernetes:                       false,
		KubernetesEnablePodLabels:        false,
		KubernetesEnablePodUID:           false,
		KubernetesGPUIdType:              GPUUID,
		KubernetesPodLabelAllowlistRegex: nil,
		UseOldNamespace:                  false,

		UseRemoteHE:  false,
		RemoteHEInfo: "localhost:5555",

		GPUDeviceOptions:    DeviceOptions{Flex: true},
		SwitchDeviceOptions: DeviceOptions{Flex: true},
		CPUDeviceOptions:    DeviceOptions{Flex: true},
		NoHostname:          false,
		UseFakeGPUs:         false,
		HealthRequireGPUs:   false,

		MetricSource: MetricSource{
			Kind: MetricSourceFile,
			File: DefaultCollectorsFile,
		},

		WebSystemdSocket:      false,
		WebConfigFile:         "",
		WebReadTimeout:        DefaultWebReadTimeout,
		WebWriteTimeout:       DefaultWebWriteTimeout,
		MaxConcurrentScrapes:  DefaultMaxConcurrentScrapes,
		EnableExporterMetrics: false,

		XIDCountWindowSize:         int((5 * time.Minute).Milliseconds()),
		ReplaceBlanksInModelName:   false,
		Debug:                      false,
		LogFormat:                  "text",
		ClockEventsCountWindowSize: int((5 * time.Minute).Milliseconds()),
		EnableDCGMLog:              false,
		DCGMLogLevel:               DCGMDbgLvlNone,
		DCGMCleanupTimeout:         DefaultDCGMCleanupTimeout,

		PodResourcesKubeletSocket:   "/var/lib/kubelet/pod-resources/kubelet.sock",
		HPCJobMappingDir:            "",
		HPCJobMappingMaxFileBytes:   DefaultHPCJobMappingMaxFileBytes,
		HPCJobMappingMaxJobIDBytes:  DefaultHPCJobMappingMaxJobIDBytes,
		HPCJobMappingMaxJobsPerFile: DefaultHPCJobMappingMaxJobsPerFile,
		ContainerLabels:             false,
		ContainerRuntimeSocket:      "",
		NvidiaResourceNames:         nil,
		KubernetesVirtualGPUs:       false,

		DumpConfig: DefaultDumpConfig(),

		KubernetesEnableDRA:       false,
		DisableStartupValidate:    false,
		EnableGPUBindUnbindWatch:  false,
		GPUBindUnbindPollInterval: time.Second,
		EnablePprof:               false,
	}
}
