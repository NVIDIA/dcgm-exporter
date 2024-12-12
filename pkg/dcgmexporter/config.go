/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package dcgmexporter

import "github.com/NVIDIA/go-dcgm/pkg/dcgm"

type KubernetesGPUIDType string

const (
	GPUUID     KubernetesGPUIDType = "uid"
	DeviceName KubernetesGPUIDType = "device-name"
)

type DeviceOptions struct {
	Flex       bool  // If true, then monitor all GPUs if MIG mode is disabled or all GPU instances if MIG is enabled.
	MajorRange []int // The indices of each GPU/NvSwitch to monitor, or -1 to monitor all
	MinorRange []int // The indices of each GPUInstance/NvLink to monitor, or -1 to monitor all
}

type Config struct {
	CollectorsFile             string
	Address                    string
	CollectInterval            int
	Kubernetes                 bool
	KubernetesGPUIdType        KubernetesGPUIDType
	CollectDCP                 bool
	UseOldNamespace            bool
	UseRemoteHE                bool
	RemoteHEInfo               string
	GPUDevices                 DeviceOptions
	SwitchDevices              DeviceOptions
	CPUDevices                 DeviceOptions
	NoHostname                 bool
	UseFakeGPUs                bool
	ConfigMapData              string
	MetricGroups               []dcgm.MetricGroup
	WebSystemdSocket           bool
	WebConfigFile              string
	XIDCountWindowSize         int
	ReplaceBlanksInModelName   bool
	Debug                      bool
	ClockEventsCountWindowSize int
	EnableDCGMLog              bool
	DCGMLogLevel               string
	PodResourcesKubeletSocket  string
	HPCJobMappingDir           string
	NvidiaResourceNames        []string
	KubernetesVirtualGPUs      bool
}
